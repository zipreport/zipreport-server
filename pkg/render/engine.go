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
	"github.com/go-rod/rod/lib/proto"
	"github.com/oddbit-project/blueprint"
	"github.com/oddbit-project/blueprint/log"
)

const DefaultConcurrency = 8
const DefaultBasePort = 22000

type Engine struct {
	ServerPool     *zpt.ServerPool
	BrowserPool    rod.Pool[rod.Browser]
	metrics        *monitor.Metrics
	logger         *log.Logger
	httpDebug      bool
	consoleLogging bool
	launcherURL    string          // Shared launcher URL for no-sandbox mode
	launcherMx     sync.Mutex      // Guards launcherURL during relaunch
	ctx            context.Context // Application-level context for pooled browsers
	browserBin     string          // resolved once in NewEngine; never downloaded on the request path
	concurrency    int
	egressPolicy   egress.Policy
	egressLookup   egress.LookupFunc
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
	if needsNoSandbox() {
		logger.Info("Detected CI/Docker environment, launching Chrome with --no-sandbox")
		launcherURL = NewLauncher(bin).MustLaunch()
	}

	return &Engine{
		ServerPool:  zpt.NewServerPoolWithContext(ctx, concurrency, basePort, m, logger),
		BrowserPool: rod.NewBrowserPool(concurrency),
		metrics:     m,
		logger:      logger,
		launcherURL: launcherURL,
		ctx:         ctx,
		browserBin:  bin,
		concurrency: concurrency,
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
	e.httpDebug = true
}

func (e *Engine) SetEgressPolicy(p egress.Policy) { e.egressPolicy = p }

// SetEgressLookup replaces DNS resolution in job proxies. Test-only: production never calls it.
func (e *Engine) SetEgressLookup(fn egress.LookupFunc) { e.egressLookup = fn }

func (e *Engine) RenderJob(job *Job) *JobResult {
	jobId := job.Id.String()
	e.logger.Debug("starting job...", log.KV{"id": jobId, "job": job})

	// Validate timeouts to prevent immediately-canceled contexts
	jobTimeout := job.JobTimeoutS
	if jobTimeout <= 0 {
		jobTimeout = JobDefaultTimeout
	}
	jsTimeout := job.JsTimeoutS
	if jsTimeout <= 0 {
		jsTimeout = JobDefaultJsTimeout
	}

	server := e.ServerPool.BuildServer(job.Zpt)
	if server == nil {
		err := errors.New("failed to build server")
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
	defer func() {
		if !disposed {
			// The context may still exist and points at this port; keep it and refuse everything
			// rather than freeing the port for another job's proxy.
			proxy.DenyAll()
			e.logger.Error(nil, "browser context not disposed; egress proxy left open in deny-all mode", log.KV{"id": jobId})
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
	// Track whether browser is healthy; only return to pool if so
	browserOK := false
	defer func() {
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
			_ = browser.Close()
		}
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
		ProxyServer:     "http://" + proxyAddr,
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
	jobBrowser := &jb
	disposed = false
	defer func() {
		// disposes only this context; never call Close() on the pooled browser here
		disposeCtx, disposeCancel := context.WithTimeout(e.ctx, 5*time.Second)
		defer disposeCancel()
		if err := jobBrowser.Context(disposeCtx).Close(); err != nil {
			e.logger.Warn("failed to dispose browser context", log.KV{"id": jobId, "error": err.Error()})
		} else {
			disposed = true
		}
	}()

	url := "http://" + server.Server.Addr + "/" + job.IndexFile
	pageCtx, pageCancel := context.WithTimeout(e.ctx, time.Duration(jobTimeout)*time.Second)
	page, err := jobBrowser.Context(pageCtx).Page(proto.TargetCreateTarget{})
	if err != nil {
		pageCancel()
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
		pageCancel()                    // cancel page context, immediately unblocking event goroutines
		evtWg.Wait()                    // wait for event goroutines to finish
		_ = page.Context(e.ctx).Close() // close tab using a live context
	}()

	if job.IgnoreSSLErrors {
		// page-scoped: a browser-level override would leak to other jobs on the shared Chrome
		if err := (proto.SecuritySetIgnoreCertificateErrors{Ignore: true}).Call(page); err != nil {
			e.logger.Error(err, "failed to set cert error policy", log.KV{"id": jobId})
			return &JobResult{ElapsedTime: time.Since(start).Seconds(), Success: false, Output: nil, Error: err}
		}
	}

	if job.UseJSEvent {
		// render using jsEvent
		e.logger.Debug("using JS trigger - console message", log.KV{"id": jobId})

		// wait for complete event before proceeding
		timeout := make(chan bool, 1)
		done := make(chan struct{})
		closed := atomic.Int32{}

		evtWg.Add(1)
		go func() {
			defer evtWg.Done()
			page.EachEvent(func(evt *proto.RuntimeConsoleAPICalled) {
				if e.consoleLogging {
					// log JS console output
					e.logConsoleArgs(page, evt.Args, jobId)
				}

				if len(evt.Args) > 0 {
					if val, err := page.ObjectToJSON(evt.Args[0]); err == nil && val.String() == "zpt-view-ready" {
						e.logger.Debug("console message received", log.KV{"id": jobId})
						if closed.CompareAndSwap(0, 1) {
							close(done)
						}
					}
				}
			},
				func(evt *proto.LogEntryAdded) {
					if e.consoleLogging && evt.Entry != nil {
						msg, _ := json.Marshal(evt.Entry)
						e.logger.Info("console message received", log.KV{"id": jobId, "src": "logger", "message": string(msg)})
					}
				})()
		}()

		// JS timeout procedure
		cancel := time.AfterFunc(time.Duration(jsTimeout)*time.Second, func() {
			close(timeout)
		})
		defer cancel.Stop()

		wait := page.WaitEvent(&proto.PageLoadEventFired{})
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
		wait()
		select {
		case <-done:
			break
		case <-timeout:
			e.logger.Warn("waiting for console message timed out", log.KV{"id": jobId})
		}
	} else {
		e.logger.Debug("using regular rendering", log.KV{"id": jobId})

		if e.consoleLogging {
			// log console & browser logger
			evtWg.Add(1)
			go func() {
				defer evtWg.Done()
				page.EachEvent(func(evt *proto.RuntimeConsoleAPICalled) {
					e.logConsoleArgs(page, evt.Args, jobId)
				},
					func(evt *proto.LogEntryAdded) {
						if evt.Entry != nil {
							msg, _ := json.Marshal(evt.Entry)
							e.logger.Info(string(msg), log.KV{"id": jobId, "src": "logger"})
						}
					})()
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
		time.Sleep(time.Duration(job.JobSettlingTimeMs) * time.Millisecond)
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

func (e *Engine) logConsoleArgs(page *rod.Page, args []*proto.RuntimeRemoteObject, jobId string) {
	var parts []string
	for _, obj := range args {
		if val, err := page.ObjectToJSON(obj); err == nil {
			parts = append(parts, val.String())
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
		u, err := NewLauncher(e.browserBin).Context(e.ctx).Launch()
		if err != nil {
			return nil, err
		}
		b, err := e.connectChecked(u)
		if err != nil {
			return nil, err
		}
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
	ctx, cancel := context.WithCancel(e.ctx)
	context.AfterFunc(e.ctx, cancel)
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
	for _, a := range cl.Arguments {
		if strings.HasPrefix(a, "--proxy-server") || strings.HasPrefix(a, "--proxy-pac-url") || strings.HasPrefix(a, "--no-proxy-server") {
			cancel()
			return nil, errProcessProxy
		}
	}
	return b, nil
}

// relaunchSharedBrowser relaunches the shared no-sandbox Chrome, unless another
// goroutine already relaunched it since prevURL was observed (single-flight).
func (e *Engine) relaunchSharedBrowser(prevURL string) (string, error) {
	e.launcherMx.Lock()
	defer e.launcherMx.Unlock()
	if e.launcherURL != prevURL {
		return e.launcherURL, nil
	}
	url, err := NewLauncher(e.browserBin).Launch()
	if err != nil {
		e.logger.Error(err, "failed to relaunch shared browser")
		return "", err
	}
	e.logger.Warn("relaunched shared browser instance")
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
	e.ServerPool.Shutdown()
}
