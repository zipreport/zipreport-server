package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"zipreport-server/pkg/browser"
	"zipreport-server/pkg/egress"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/zpt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/oddbit-project/blueprint"
	"github.com/oddbit-project/blueprint/log"
)

// ErrJobTimeout is returned when the job deadline expires while waiting for the page.
var ErrJobTimeout = errors.New("render job timed out")

// jobCtxErr maps a finished job context to ErrJobTimeout or ErrJobCancelled.
func jobCtxErr(ctx context.Context) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrJobTimeout, ctx.Err())
	}
	return fmt.Errorf("%w: %w", ErrJobCancelled, ctx.Err())
}

// ErrJobCancelled is returned when the job's caller went away before the job finished.
var ErrJobCancelled = errors.New("render job cancelled")

const DefaultConcurrency = 8
const DefaultBasePort = 22000

// maxConsoleMessages caps console/log entries logged per job when console logging is on.
const maxConsoleMessages = 200

type Engine struct {
	ServerPool     *zpt.ServerPool
	BrowserPool    rod.Pool[rod.Browser]
	metrics        *monitor.Metrics
	logger         *log.Logger
	consoleLogging bool
	launcherURL    string             // Shared launcher URL for no-sandbox mode
	launcherMx     sync.Mutex         // Guards launcherURL and sharedLauncher during relaunch
	sharedLauncher *launcher.Launcher // process behind launcherURL in no-sandbox mode
	ctx            context.Context    // Application-level context for pooled browsers
	browserBin     string             // resolved once in NewEngine; never downloaded on the request path
	concurrency    int
	egressPolicy   egress.Policy
	egressLookup   egress.LookupFunc
	contextProxy   func(proxyAddr string) string
	conns          sync.Map // *rod.Browser -> context.CancelFunc; the cancel closes that browser's CDP connection
	dedicated      sync.Map // *rod.Browser -> *launcher.Launcher; the process behind each dedicated-mode browser
}

func NewEngine(ctx context.Context, concurrency int, basePort int, m *monitor.Metrics, logger *log.Logger) *Engine {
	if logger == nil {
		logger = log.New("zipreport-engine")
	}

	bin, err := browserBin()
	if err != nil {
		panic(err)
	}

	// Check if we need to create a shared launcher with --no-sandbox
	var launcherURL string
	var sharedLauncher *launcher.Launcher
	if needsNoSandbox() {
		logger.Info("Detected CI/Docker environment, launching Chrome with --no-sandbox")
		sharedLauncher = NewLauncher(bin)
		launcherURL = sharedLauncher.MustLaunch()
	}

	return &Engine{
		ServerPool:     zpt.NewServerPoolWithContext(ctx, concurrency, basePort, m, logger),
		BrowserPool:    rod.NewBrowserPool(concurrency),
		metrics:        m,
		logger:         logger,
		launcherURL:    launcherURL,
		sharedLauncher: sharedLauncher,
		ctx:            ctx,
		browserBin:     bin,
		concurrency:    concurrency,
	}
}

// NewLauncher returns a launcher for bin with rod's defaults, except that
// site isolation stays enabled: renderers run caller-supplied scripts.
func NewLauncher(bin string) *launcher.Launcher {
	l := launcher.New().Bin(bin)
	if needsNoSandbox() {
		l = l.NoSandbox(true)
	}
	if v, ok := l.GetFlags("disable-features"); ok {
		keep := make([]string, 0, len(v))
		for _, f := range v {
			if f != "site-per-process" {
				keep = append(keep, f)
			}
		}
		if len(keep) == 0 {
			l = l.Delete("disable-features")
		} else {
			l = l.Set("disable-features", keep...)
		}
	}
	return l.Delete("disable-site-isolation-trials")
}

// browserBin returns the pinned browser, downloading it if missing, so rod
// never falls back to its own (older) download.
func browserBin() (string, error) {
	if browser.IsInstalled() {
		return browser.BinPath(), nil
	}
	return browser.Download()
}

func (e *Engine) EnableConsoleLog() {
	e.consoleLogging = true
}

func (e *Engine) EnableHttpDebugging() {
	e.ServerPool.HttpDebug = true
}

// SetServerTimeouts sets the read/write timeouts of the per-job report servers.
// 0 means no timeout (net/http semantics).
func (e *Engine) SetServerTimeouts(read, write time.Duration) {
	e.ServerPool.ReadTimeout = read
	e.ServerPool.WriteTimeout = write
}

func (e *Engine) SetEgressPolicy(p egress.Policy) { e.egressPolicy = p }

// EgressPolicy returns the configured egress policy.
func (e *Engine) EgressPolicy() egress.Policy { return e.egressPolicy }

// SetContextProxy overrides the proxy server given to each job's browser context. Test-only: production never calls it.
func (e *Engine) SetContextProxy(fn func(proxyAddr string) string) { e.contextProxy = fn }

func (e *Engine) proxyServerFor(proxyAddr string) string {
	if e.contextProxy != nil {
		return e.contextProxy(proxyAddr)
	}
	return "http://" + proxyAddr
}

// SetEgressLookup replaces DNS resolution in job proxies. Test-only: production never calls it.
func (e *Engine) SetEgressLookup(fn egress.LookupFunc) { e.egressLookup = fn }

func (e *Engine) RenderJob(job *Job) *JobResult {
	jobId := job.Id.String()
	e.logger.Debug("starting job...", log.KV{"id": jobId, "job": job})

	// Validate timeouts to prevent immediately-canceled contexts
	jsTimeout := job.JsTimeoutS
	if jsTimeout <= 0 {
		jsTimeout = JobDefaultJsTimeout
	}

	// the deadline starts here, so waiting for a render slot counts against it
	parent := job.Ctx
	if parent == nil {
		parent = context.Background()
	}
	jobCtx, jobCancel := context.WithTimeout(e.ctx, job.Timeout())
	defer jobCancel()
	stop := context.AfterFunc(parent, jobCancel) // client gone: job cancelled
	defer stop()

	server := e.ServerPool.BuildServerContext(jobCtx, job.Zpt)
	if server == nil {
		err := errors.New("failed to build server")
		if jobCtx.Err() != nil {
			err = fmt.Errorf("waiting for a render slot: %w", jobCtxErr(jobCtx))
		}
		e.logger.Error(err, "failed to build server")
		return &JobResult{
			ElapsedTime: 0,
			Success:     false,
			Output:      nil,
			Error:       err,
		}
	}
	defer e.ServerPool.RemoveServer(server)
	e.logger.Info("started ephemeral http server", log.KV{"id": jobId, "address": server.Server.Addr})

	proxy := egress.New(server.Server.Addr, "127.0.0.1:"+strconv.Itoa(server.Port), e.ServerPool.BasePort, e.concurrency, e.egressPolicy)
	if e.egressLookup != nil {
		proxy.Lookup = e.egressLookup
	}
	proxyAddr, err := proxy.Start()
	if err != nil {
		e.logger.Error(err, "failed to start egress proxy", log.KV{"id": jobId})
		return &JobResult{Success: false, Error: err}
	}
	// nothing to dispose until the job's browser context exists
	disposed := true
	var jobBrowser *rod.Browser
	// Track whether browser is healthy; only return to pool if so
	browserOK := false
	defer func() {
		if !disposed && !browserOK {
			// The browser was discarded: its connection is already dropped (or its process closed), because
			// the browser defer below runs before this one, so the context cannot outlive it.
			_ = proxy.Close()
			e.logger.Warn("browser discarded; egress proxy closed", log.KV{"id": jobId})
		} else if !disposed {
			// The context may still exist and points at this port; keep the proxy listening in deny-all
			// mode rather than freeing the port for another job's proxy, and close it once the context is gone.
			proxy.Retire(func(ctx context.Context) error { return jobBrowser.Context(ctx).Close() })
			e.logger.Error(nil, "browser context not disposed; egress proxy retired, retrying disposal", log.KV{"id": jobId})
		} else {
			_ = proxy.Close()
		}
		if n, s := proxy.Blocked(); n > 0 {
			e.logger.Warn("blocked network requests from rendered page", log.KV{"id": jobId, "count": n, "samples": s})
		}
	}()

	browser, err := e.GetBrowser()
	if err != nil {
		// Get() consumed a pool slot even though creation failed; return an
		// empty slot so the pool capacity is not permanently reduced.
		e.BrowserPool.Put(nil)
		e.logger.Error(err, "could not fetch browser instance", log.KV{"id": jobId})
		return &JobResult{
			ElapsedTime: 0,
			Success:     false,
			Output:      nil,
			Error:       err,
		}
	}
	defer func() {
		if browserOK {
			// A dead CDP connection must not go back to the pool, and the proxy defer (which runs after
			// this one) relies on browserOK being accurate: probe the connection, it may have died mid-job.
			probeCtx, probeCancel := context.WithTimeout(context.WithoutCancel(e.ctx), 2*time.Second)
			_, perr := proto.BrowserGetVersion{}.Call(browser.Context(probeCtx))
			probeCancel()
			if perr != nil {
				browserOK = false
			}
		}
		if browserOK {
			e.BrowserPool.Put(browser)
			return
		}
		e.logger.Warn("discarding broken browser instance", log.KV{"id": jobId})
		e.metrics.Browsers.Dec()
		// In shared-launcher mode every pooled browser is a connection to the
		// same Chrome process; closing it would terminate rendering for all
		// concurrent jobs. Drop the connection instead. In dedicated mode each
		// browser is its own process, so close it to release resources.
		if e.launcherURL == "" {
			cctx, ccancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = browser.Context(cctx).Close()
			ccancel()
			if v, ok := e.dedicated.LoadAndDelete(browser); ok {
				go cleanupLauncher(v.(*launcher.Launcher), 5*time.Second)
			}
		}
		// closing the CDP connection detaches it, so Chrome disposes the DisposeOnDetach contexts
		e.dropConn(browser)
		// Return an empty slot so a fresh browser is created on the next Get.
		e.BrowserPool.Put(nil)
	}()
	e.logger.Info("browser acquired", log.KV{"id": jobId})

	start := time.Now()
	// Each job gets its own browser context so cookies, storage, caches and
	// service workers never outlive the job or leak between callers. Same as rod's
	// incognito helper, but all of the context's traffic, loopback and link-local
	// included, goes through the job's egress proxy.
	ctxRes, err := proto.TargetCreateBrowserContext{
		DisposeOnDetach: true,
		ProxyServer:     e.proxyServerFor(proxyAddr),
		ProxyBypassList: "<-loopback>",
	}.Call(browser)
	if err != nil {
		e.logger.Error(err, "failed to create browser context", log.KV{"id": jobId})
		return &JobResult{
			ElapsedTime: 0,
			Success:     false,
			Output:      nil,
			Error:       err,
		}
	}
	jb := *browser
	jb.BrowserContextID = ctxRes.BrowserContextID
	jobBrowser = &jb
	disposed = false
	defer func() {
		// disposes only this context; never call Close() on the pooled browser here
		disposeCtx, disposeCancel := context.WithTimeout(context.WithoutCancel(e.ctx), 5*time.Second)
		defer disposeCancel()
		if err := jobBrowser.Context(disposeCtx).Close(); err != nil {
			e.logger.Warn("failed to dispose browser context", log.KV{"id": jobId, "error": err.Error()})
		} else {
			disposed = true
		}
	}()

	url := "http://" + server.Server.Addr + "/" + job.IndexFile
	pageCtx, pageCancel := context.WithCancel(jobCtx)
	page, err := jobBrowser.Context(pageCtx).Page(proto.TargetCreateTarget{})
	if err != nil {
		pageCancel()
		if jobCtx.Err() != nil {
			// the browser is healthy; the job ran out of time or was cancelled
			browserOK = true
			err = fmt.Errorf("creating page: %w", jobCtxErr(jobCtx))
		}
		e.logger.Error(err, "failed to create page", log.KV{"id": jobId})
		return &JobResult{
			ElapsedTime: time.Since(start).Seconds(),
			Success:     false,
			Output:      nil,
			Error:       err,
		}
	}
	// Page created successfully — browser connection is alive
	browserOK = true

	// WaitGroup to synchronize event goroutines with page closure
	var evtWg sync.WaitGroup
	defer func() {
		pageCancel() // cancel page context, immediately unblocking event goroutines
		evtWg.Wait() // wait for event goroutines to finish
		closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(e.ctx), 5*time.Second)
		defer closeCancel()
		// bounded: a beforeunload handler on a busy renderer never lets Page.close finish;
		// disposing the browser context afterwards tears the tab down regardless
		_ = page.Context(closeCtx).Close()
	}()

	if job.IgnoreSSLErrors {
		// page-scoped: a browser-level override would leak to other jobs on the shared Chrome
		if err := (proto.SecuritySetIgnoreCertificateErrors{Ignore: true}).Call(page); err != nil {
			e.logger.Error(err, "failed to set cert error policy", log.KV{"id": jobId})
			return &JobResult{ElapsedTime: time.Since(start).Seconds(), Success: false, Output: nil, Error: err}
		}
	}

	var consoleCount atomic.Int32
	logConsole := func(f func()) {
		n := consoleCount.Add(1)
		if n <= maxConsoleMessages {
			f()
		} else if n == maxConsoleMessages+1 {
			e.logger.Warn("console output truncated", log.KV{"id": jobId, "limit": maxConsoleMessages})
		}
	}

	if job.UseJSEvent {
		// render using jsEvent
		e.logger.Debug("using JS trigger - console message", log.KV{"id": jobId})

		// wait for complete event before proceeding
		timeout := make(chan bool, 1)
		done := make(chan struct{})
		closed := atomic.Int32{}

		// subscribe before navigating so an early signal is not dropped
		waitEvents := page.EachEvent(func(evt *proto.RuntimeConsoleAPICalled) {
			if e.consoleLogging {
				// log JS console output
				logConsole(func() { e.logConsoleArgs(evt.Args, jobId) })
			}

			if len(evt.Args) > 0 && evt.Args[0].Value.String() == "zpt-view-ready" {
				e.logger.Debug("console message received", log.KV{"id": jobId})
				if closed.CompareAndSwap(0, 1) {
					close(done)
				}
			}
		},
			func(evt *proto.LogEntryAdded) {
				if e.consoleLogging && evt.Entry != nil {
					logConsole(func() {
						msg, _ := json.Marshal(evt.Entry)
						e.logger.Info("console message received", log.KV{"id": jobId, "src": "logger", "message": string(msg)})
					})
				}
			})
		evtWg.Add(1)
		go func() {
			defer evtWg.Done()
			waitEvents()
		}()

		// JS timeout procedure
		cancel := time.AfterFunc(time.Duration(jsTimeout)*time.Second, func() {
			close(timeout)
		})
		defer cancel.Stop()

		waitLoadEvt := page.WaitEvent(&proto.PageLoadEventFired{})
		loadFired := make(chan struct{})
		evtWg.Add(1)
		go func() { defer evtWg.Done(); waitLoadEvt(); close(loadFired) }()
		err = page.Navigate(url)
		if err != nil {
			e.logger.Error(err, "failed to navigate", log.KV{"id": jobId})
			return &JobResult{
				ElapsedTime: time.Since(start).Seconds(),
				Success:     false,
				Output:      nil,
				Error:       err,
			}
		}
		err = page.WaitLoad()
		if err != nil {
			e.logger.Error(err, "failed to wait for page load", log.KV{"id": jobId})
			return &JobResult{
				ElapsedTime: time.Since(start).Seconds(),
				Success:     false,
				Output:      nil,
				Error:       err,
			}
		}
		select {
		case <-loadFired:
		case <-pageCtx.Done():
		}
		select {
		case <-done:
			break
		case <-pageCtx.Done():
			err = jobCtxErr(pageCtx)
			e.logger.Error(err, "job timed out waiting for console message", log.KV{"id": jobId})
			return &JobResult{ElapsedTime: time.Since(start).Seconds(), Success: false, Output: nil, Error: err}
		case <-timeout:
			e.logger.Warn("waiting for console message timed out", log.KV{"id": jobId})
		}
	} else {
		e.logger.Debug("using regular rendering", log.KV{"id": jobId})

		if e.consoleLogging {
			// log console & browser logger
			waitEvents := page.EachEvent(func(evt *proto.RuntimeConsoleAPICalled) {
				logConsole(func() { e.logConsoleArgs(evt.Args, jobId) })
			},
				func(evt *proto.LogEntryAdded) {
					if evt.Entry != nil {
						logConsole(func() {
							msg, _ := json.Marshal(evt.Entry)
							e.logger.Info(string(msg), log.KV{"id": jobId, "src": "logger"})
						})
					}
				})
			evtWg.Add(1)
			go func() {
				defer evtWg.Done()
				waitEvents()
			}()
		}

		// no JS event, proceed as expected
		err = page.Navigate(url)
		if err != nil {
			e.logger.Error(err, "failed to navigate", log.KV{"id": jobId})
			return &JobResult{
				ElapsedTime: time.Since(start).Seconds(),
				Success:     false,
				Output:      nil,
				Error:       err,
			}
		}
		err = page.WaitLoad()
		if err != nil {
			e.logger.Error(err, "failed to wait for page load", log.KV{"id": jobId})
			return &JobResult{
				ElapsedTime: time.Since(start).Seconds(),
				Success:     false,
				Output:      nil,
				Error:       err,
			}
		}
		// settling time
		select {
		case <-time.After(time.Duration(job.JobSettlingTimeMs) * time.Millisecond):
		case <-pageCtx.Done():
			err = jobCtxErr(pageCtx)
			e.logger.Error(err, "job timed out during settling time", log.KV{"id": jobId})
			return &JobResult{ElapsedTime: time.Since(start).Seconds(), Success: false, Output: nil, Error: err}
		}
	}
	// fail closed: if the page never reached its own server through the proxy, the
	// browser is not routing through it and nothing it fetched was vetted
	if !proxy.SawOwnOrigin() {
		err := errors.New("navigation did not pass through the egress proxy")
		e.logger.Error(err, "egress proxy bypassed", log.KV{"id": jobId})
		blocked, samples := proxy.Blocked()
		return &JobResult{
			ElapsedTime:     time.Since(start).Seconds(),
			Success:         false,
			Error:           err,
			BlockedRequests: blocked,
			BlockedSamples:  samples,
		}
	}
	pdf, err := page.PDF(job.ToPDFOptions())
	var buf []byte = nil
	if err == nil {
		buf, err = io.ReadAll(pdf)
	}
	elapsed := time.Since(start)
	result := &JobResult{
		ElapsedTime:        elapsed.Seconds(),
		Success:            err == nil,
		Output:             buf,
		Error:              err,
		EgressSawOwnOrigin: proxy.SawOwnOrigin(),
	}
	result.BlockedRequests, result.BlockedSamples = proxy.Blocked()

	if err == nil {
		e.logger.Info(fmt.Sprintf("finished job in %f seconds", result.ElapsedTime), log.KV{"id": jobId, "size": len(result.Output), "elapsedTime": result.ElapsedTime})
	} else {
		e.logger.Info("job failed", log.KV{"id": jobId, "elapsedTime": result.ElapsedTime})
	}
	return result
}

func (e *Engine) logConsoleArgs(args []*proto.RuntimeRemoteObject, jobId string) {
	var parts []string
	for _, obj := range args {
		if obj.ObjectID == "" {
			parts = append(parts, obj.Value.String())
		} else {
			parts = append(parts, obj.Description)
		}
	}
	if len(parts) > 0 {
		e.logger.Info(fmt.Sprintf("%v", parts), log.KV{"id": jobId, "src": "js console"})
	}
}

func (e *Engine) GetBrowser() (*rod.Browser, error) {
	return e.BrowserPool.Get(e.connectBrowser)
}

// connectBrowser establishes a browser connection. In shared-launcher mode it
// reconnects to the shared Chrome, relaunching it once if the process has died.
func (e *Engine) connectBrowser() (*rod.Browser, error) {
	if e.launcherURL == "" {
		// Dedicated launch of the pinned browser, sandboxed
		l := NewLauncher(e.browserBin).Context(e.ctx)
		u, err := l.Launch()
		if err != nil {
			removeProfile(l)
			return nil, err
		}
		b, err := e.connectChecked(u)
		if err != nil {
			go func() {
				l.Kill()
				l.Cleanup()
			}()
			return nil, err
		}
		e.dedicated.Store(b, l)
		e.metrics.Browsers.Inc()
		return b, nil
	}

	e.launcherMx.Lock()
	url := e.launcherURL
	e.launcherMx.Unlock()

	b, err := e.connectChecked(url)
	if err == nil {
		e.metrics.Browsers.Inc()
		return b, nil
	}
	if errors.Is(err, errProcessProxy) {
		return nil, err
	}

	// The shared Chrome may have died; relaunch it once and retry.
	url, err = e.relaunchSharedBrowser(url)
	if err != nil {
		return nil, err
	}
	b, err = e.connectChecked(url)
	if err != nil {
		return nil, err
	}
	e.metrics.Browsers.Inc()
	return b, nil
}

var errProcessProxy = errors.New("browser runs with a process-wide proxy setting")

// connectChecked connects to the browser at url and refuses it if it runs with a
// process-wide proxy: that could carry traffic the per-job egress proxy never sees.
// The connection is dropped on failure without closing the (possibly shared) browser.
func (e *Engine) connectChecked(url string) (*rod.Browser, error) {
	// the child context is already cancelled together with e.ctx
	ctx, cancel := context.WithCancel(e.ctx)
	b := rod.New().ControlURL(url).Context(ctx)
	if err := b.Connect(); err != nil {
		cancel()
		return nil, err
	}
	cl, err := proto.BrowserGetBrowserCommandLine{}.Call(b)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("reading browser command line: %w", err)
	}
	if _, bad := ProcessProxyArg(cl.Arguments); bad {
		cancel()
		return nil, errProcessProxy
	}
	e.conns.Store(b, cancel)
	return b, nil
}

// dropConn closes the browser's CDP connection; safe to call more than once.
func (e *Engine) dropConn(b *rod.Browser) {
	if c, ok := e.conns.LoadAndDelete(b); ok {
		c.(context.CancelFunc)()
	}
}

// ProcessProxyArg returns the first browser argument that sets a process-wide proxy.
func ProcessProxyArg(args []string) (string, bool) {
	for _, a := range args {
		if strings.HasPrefix(a, "--proxy-server") || strings.HasPrefix(a, "--proxy-pac-url") || strings.HasPrefix(a, "--no-proxy-server") {
			return a, true
		}
	}
	return "", false
}

// relaunchSharedBrowser relaunches the shared no-sandbox Chrome, unless another
// goroutine already relaunched it since prevURL was observed (single-flight).
func (e *Engine) relaunchSharedBrowser(prevURL string) (string, error) {
	e.launcherMx.Lock()
	defer e.launcherMx.Unlock()
	if e.launcherURL != prevURL {
		return e.launcherURL, nil
	}
	l := NewLauncher(e.browserBin)
	url, err := l.Launch()
	if err != nil {
		removeProfile(l)
		e.logger.Error(err, "failed to relaunch shared browser")
		return "", err
	}
	e.logger.Warn("relaunched shared browser instance")
	// the previous process is not killed (it may still serve other jobs); its profile goes once it exits
	if prev := e.sharedLauncher; prev != nil {
		go prev.Cleanup()
	}
	e.sharedLauncher = l
	e.launcherURL = url
	return url, nil
}

// needsNoSandbox checks if Chrome sandbox should be disabled
func needsNoSandbox() bool {
	// Check for CI environments
	if os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") == "true" {
		return true
	}
	// Mirror rod's utils.InContainer: Docker, Podman, Kubernetes
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	if _, err := os.Stat("/.containerenv"); err == nil {
		return true
	}
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

func (e *Engine) Shutdown() {
	e.logger.Info("Shutting down render.Engine...")
	e.BrowserPool.Cleanup(func(p *rod.Browser) {
		e.metrics.Browsers.Dec()
		// In shared-launcher mode, the first Close() kills Chrome and
		// subsequent calls fail; use non-panicking Close to handle this.
		_ = p.Context(blueprint.ShutdownContext()).Close()
	})
	e.closeSharedBrowser()
	e.cleanupLaunchers()
	e.ServerPool.Shutdown()
}

// closeSharedBrowser asks the shared Chrome to exit, so its profile cleanup need not wait out the grace
// period. Errors are ignored: a dead or unreachable Chrome falls through to cleanupLaunchers.
func (e *Engine) closeSharedBrowser() {
	e.launcherMx.Lock()
	url := e.launcherURL
	e.launcherMx.Unlock()
	if url == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	b := rod.New().ControlURL(url).Context(ctx)
	if b.Connect() == nil {
		_ = b.Close()
	}
}

// cleanupLaunchers removes the profiles of every launched browser still known (jobs in flight are not
// visited by the pool cleanup), waiting at most 5s.
func (e *Engine) cleanupLaunchers() {
	var ls []*launcher.Launcher
	e.launcherMx.Lock()
	if e.sharedLauncher != nil {
		ls = append(ls, e.sharedLauncher)
		e.sharedLauncher = nil
	}
	e.launcherMx.Unlock()
	e.dedicated.Range(func(k, v any) bool {
		ls = append(ls, v.(*launcher.Launcher))
		e.dedicated.Delete(k)
		return true
	})
	var wg sync.WaitGroup
	for _, l := range ls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cleanupLauncher(l, 3*time.Second)
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
	}
}

// cleanupLauncher removes l's profile once its browser exits. If the process is still running after
// grace it is killed; Kill is never called once Cleanup has returned (the PID could be reused).
func cleanupLauncher(l *launcher.Launcher, grace time.Duration) {
	done := make(chan struct{})
	go func() {
		l.Cleanup()
		close(done)
	}()
	select {
	case <-done:
		return
	case <-time.After(grace):
	}
	l.Kill()
	<-done
}

// removeProfile deletes the profile of a launcher whose Launch failed; Cleanup would block forever
// if the process never started.
func removeProfile(l *launcher.Launcher) {
	dir := l.Get(flags.UserDataDir)
	go func() { _ = os.RemoveAll(dir) }()
}
