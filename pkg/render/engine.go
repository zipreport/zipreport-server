package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"
	"zipreport-server/pkg/browser"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/zpt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/oddbit-project/blueprint/log"
)

const DefaultConcurrency = 8
const DefaultBasePort = 42000

type Engine struct {
	ServerPool     *zpt.ServerPool
	BrowserPool    rod.Pool[rod.Browser]
	metrics        *monitor.Metrics
	logger         *log.Logger
	httpDebug      bool
	consoleLogging bool
	launcherURL    string          // Shared launcher URL for no-sandbox mode
	ctx            context.Context // Application-level context for pooled browsers
}

func NewEngine(ctx context.Context, concurrency int, basePort int, m *monitor.Metrics, logger *log.Logger) *Engine {
	if logger == nil {
		logger = log.New("zipreport-engine")
	}

	// Check if we need to create a shared launcher with --no-sandbox
	var launcherURL string
	if needsNoSandbox() {
		logger.Info("Detected CI/Docker environment, launching Chrome with --no-sandbox")
		l := launcher.New().NoSandbox(true)
		if browser.IsInstalled() {
			// Pin the pre-installed binary so rod skips its Validate() probe,
			// which otherwise deletes and re-downloads Chromium at runtime.
			l = l.Bin(browser.BinPath())
		}
		launcherURL = l.MustLaunch()
	}

	return &Engine{
		ServerPool:  zpt.NewServerPoolWithContext(ctx, concurrency, basePort, m, logger),
		BrowserPool: rod.NewBrowserPool(concurrency),
		metrics:     m,
		logger:      logger,
		launcherURL: launcherURL,
		ctx:         ctx,
	}
}

func (e *Engine) EnableConsoleLog() {
	e.consoleLogging = true
}

func (e *Engine) EnableHttpDebugging() {
	e.httpDebug = true
}

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

	browser, err := e.GetBrowser()
	if err != nil {
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
		} else {
			e.logger.Warn("discarding broken browser instance", log.KV{"id": jobId})
			_ = browser.Close()
		}
	}()
	e.logger.Info("browser acquired", log.KV{"id": jobId})

	start := time.Now()
	err = browser.IgnoreCertErrors(job.IgnoreSSLErrors)
	if err != nil {
		e.logger.Error(err, "failed to set cert error policy", log.KV{"id": jobId})
		return &JobResult{
			ElapsedTime: 0,
			Success:     false,
			Output:      nil,
			Error:       err,
		}
	}

	url := "http://" + server.Server.Addr + "/" + job.IndexFile
	pageCtx, pageCancel := context.WithTimeout(e.ctx, time.Duration(jobTimeout)*time.Second)
	page, err := browser.Context(pageCtx).Page(proto.TargetCreateTarget{})
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
	pdf, err := page.PDF(job.ToPDFOptions())
	var buf []byte = nil
	if err == nil {
		buf, err = io.ReadAll(pdf)
	}
	elapsed := time.Since(start)
	result := &JobResult{
		ElapsedTime: elapsed.Seconds(),
		Success:     err == nil,
		Output:      buf,
		Error:       err,
	}

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
	return e.BrowserPool.Get(func() (*rod.Browser, error) {
		var browser *rod.Browser
		if e.launcherURL != "" {
			// Use shared launcher with --no-sandbox
			browser = rod.New().ControlURL(e.launcherURL).Context(e.ctx)
		} else {
			// Normal launch with sandbox
			browser = rod.New().Context(e.ctx)
		}

		err := browser.Connect()
		if err != nil {
			return nil, err
		}
		return browser, nil
	})
}

// needsNoSandbox checks if Chrome sandbox should be disabled
func needsNoSandbox() bool {
	// Check for CI environments
	if os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") == "true" {
		return true
	}
	// Check for Docker
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return false
}

func (e *Engine) Shutdown() {
	e.logger.Info("Shutting down render.Engine...")
	e.BrowserPool.Cleanup(func(p *rod.Browser) {
		// In shared-launcher mode, the first Close() kills Chrome and
		// subsequent calls fail; use non-panicking Close to handle this.
		_ = p.Close()
	})
	e.ServerPool.Shutdown()
}
