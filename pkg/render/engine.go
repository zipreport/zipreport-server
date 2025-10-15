package render

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/zpt"

	"github.com/go-rod/rod"
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
}

func NewEngine(ctx context.Context, concurrency int, basePort int, m *monitor.Metrics, logger *log.Logger) *Engine {
	if logger == nil {
		logger = log.New("zipreport-engine")
	}
	return &Engine{
		ServerPool:  zpt.NewServerPoolWithContext(ctx, concurrency, basePort, m, logger),
		BrowserPool: rod.NewBrowserPool(concurrency),
		metrics:     m,
		logger:      logger,
	}
}

func (e *Engine) EnableConsoleLog() {
	e.consoleLogging = true
}

func (e *Engine) EnableHttpDebugging() {
	e.httpDebug = true
}

func (e *Engine) RenderJob(ctx context.Context, job *Job) *JobResult {
	jobId := job.Id.String()
	e.logger.Debug("starting job...", log.KV{"id": jobId, "job": job})
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
	e.logger.Info("started ephemeral http server", log.KV{"id": jobId, "address": server.Server.Addr})

	browser, err := e.GetBrowser(ctx)
	if err != nil {
		e.logger.Error(err, "could not fetch browser instance", log.KV{"id": jobId})
		return &JobResult{
			ElapsedTime: 0,
			Success:     false,
			Output:      nil,
			Error:       err,
		}
	}
	defer e.BrowserPool.Put(browser)
	defer e.ServerPool.RemoveServer(server)
	e.logger.Info("browser acquired", log.KV{"id": jobId})

	start := time.Now()
	browser.MustIgnoreCertErrors(job.IgnoreSSLErrors)
	url := "http://" + server.Server.Addr + "/" + job.IndexFile
	page := browser.
		Timeout(time.Duration(job.JobTimeoutS) * time.Second).
		MustPage()
	defer page.MustClose()

	if job.UseJSEvent {
		// render using jsEvent
		e.logger.Debug("using JS trigger - console message", log.KV{"id": jobId})

		// wait for complete event before proceeding
		timeout := make(chan bool, 1)
		done := make(chan struct{})
		closed := atomic.Int32{}

		go page.EachEvent(func(evt *proto.RuntimeConsoleAPICalled) {
			if e.consoleLogging {
				// logger JS console output
				e.logger.Info(page.MustObjectsToJSON(evt.Args).String(), log.KV{"id": jobId, "src": "js console"})
			}

			if page.MustObjectToJSON(evt.Args[0]).String() == "zpt-view-ready" {
				e.logger.Debug("console message received", log.KV{"id": jobId})
				if closed.CompareAndSwap(0, 1) {
					close(done)
				}
			}
		},
			func(evt *proto.LogEntryAdded) {
				if e.consoleLogging && evt.Entry != nil {
					// logger browser logger output
					msg, _ := json.Marshal(evt.Entry)
					e.logger.Info("console message received", log.KV{"id": jobId, "src": "logger", "message": string(msg)})
				}
			})()

		// JS timeout procedure
		cancel := time.AfterFunc(time.Duration(job.JsTimeoutS)*time.Second, func() {
			close(timeout)
		})

		wait := page.WaitEvent(&proto.PageLoadEventFired{})
		page.MustNavigate(url).MustWaitLoad()
		wait()
		select {
		case <-done:
			cancel.Stop()
			break
		case <-timeout:
			e.logger.Warn("waiting for console message timed out", log.KV{"id": jobId})
			if closed.CompareAndSwap(0, 1) {
				close(done)
			}
		}
	} else {
		e.logger.Debug("using regular rendering", log.KV{"id": jobId})

		if e.consoleLogging {
			// logger console & browser logger

			go page.EachEvent(func(evt *proto.RuntimeConsoleAPICalled) {
				e.logger.Info(page.MustObjectsToJSON(evt.Args).String(), log.KV{"id": jobId, "src": "js console"})
			},
				func(evt *proto.LogEntryAdded) {
					if evt.Entry != nil {
						msg, _ := json.Marshal(evt.Entry)
						e.logger.Info(string(msg), log.KV{"id": jobId, "src": "logger"})
					}
				})()
		}

		// no JS event, proceed as expected
		page.MustNavigate(url).MustWaitLoad()
		// settling time
		time.Sleep(time.Duration(job.JobSettlingTimeMs) * time.Millisecond)
	}
	pdf, err := page.PDF(job.ToPDFOptions())
	var buf []byte = nil
	if err == nil {
		buf, err = io.ReadAll(pdf)
	}
	elapsed := time.Now().Sub(start)
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

func (e *Engine) GetBrowser(ctx context.Context) (*rod.Browser, error) {
	return e.BrowserPool.Get(func() (*rod.Browser, error) {
		browser := rod.New().Context(ctx)
		err := browser.Connect()
		if err != nil {
			return nil, err
		}
		return browser, nil
	})
}

func (e *Engine) Shutdown() {
	e.logger.Info("Shutting down render.Engine...")
	e.BrowserPool.Cleanup(func(p *rod.Browser) {
		// shutdown browsers
		p.MustClose()
	})
	e.ServerPool.Shutdown()
}
