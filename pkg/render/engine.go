package render

import (
	"context"
	"encoding/json"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/rs/zerolog"
	"io"
	"time"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/zpt"
)

const DefaultConcurrency = 8
const DefaultBasePort = 42000

type EngineOptions struct {
	HttpDebug   bool
	Concurrency int
	BasePort    int
	LogConsole  bool
	Context     context.Context
	log         zerolog.Logger
}

type Engine struct {
	Opts        *EngineOptions
	ServerPool  *zpt.ServerPool
	BrowserPool rod.BrowserPool
	m           *monitor.Metrics
	log         zerolog.Logger
}

func DefaultEngineOptions(ctx context.Context, l zerolog.Logger) *EngineOptions {
	return &EngineOptions{
		HttpDebug:   false,
		Concurrency: DefaultConcurrency,
		BasePort:    DefaultBasePort,
		LogConsole:  false,
		Context:     ctx,
		log:         l,
	}
}

func NewEngine(opts *EngineOptions, m *monitor.Metrics) *Engine {
	return &Engine{
		Opts:        opts,
		ServerPool:  zpt.NewServerPoolWithContext(opts.Context, opts.Concurrency, opts.BasePort, opts.log, m),
		BrowserPool: rod.NewBrowserPool(opts.Concurrency),
		m:           m,
		log:         opts.log,
	}
}

func (e *Engine) EnableConsoleLog() {
	e.Opts.LogConsole = true
}

func (e *Engine) RenderJob(job *Job) *JobResult {
	e.log.Debug().
		Str("id", job.Id.String()).
		Any("Job", job).
		Msg("starting job")
	server := e.ServerPool.BuildServer(job.Zpt, e.Opts.HttpDebug)
	e.log.Info().
		Str("id", job.Id.String()).
		Str("address", server.Server.Addr).
		Msg("server address")
	browser := e.GetBrowser()
	defer e.BrowserPool.Put(browser)
	defer e.ServerPool.RemoveServer(server)
	e.log.Info().
		Str("id", job.Id.String()).
		Msg("Browser acquired")

	start := time.Now()
	browser.MustIgnoreCertErrors(job.IgnoreSSLErrors)
	url := "http://" + server.Server.Addr + "/" + job.IndexFile
	page := browser.
		Timeout(time.Duration(job.JobTimeoutS) * time.Second).
		MustPage()
	defer page.MustClose()

	if job.UseJSEvent {
		// render using jsEvent
		e.log.Debug().
			Str("id", job.Id.String()).
			Msg("using JS trigger - console message")
		// wait for complete event before proceeding
		timeout := make(chan bool, 1)
		done := make(chan struct{})

		go page.EachEvent(func(evt *proto.RuntimeConsoleAPICalled) {
			if e.Opts.LogConsole {
				// log JS console output
				e.log.Info().Str("job", job.Id.String()).Str("src", "js console").Msg(page.MustObjectsToJSON(evt.Args).String())
			}

			if page.MustObjectToJSON(evt.Args[0]).String() == "zpt-view-ready" {
				e.log.Debug().
					Str("id", job.Id.String()).
					Msg("console message received")
				close(done)
			}
		},
			func(evt *proto.LogEntryAdded) {
				if e.Opts.LogConsole && evt.Entry != nil {
					// log browser log output
					msg, _ := json.Marshal(evt.Entry)
					e.log.Info().Str("job", job.Id.String()).Str("src", "log").Msg(string(msg))
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
			e.log.Warn().
				Str("id", job.Id.String()).
				Msg("waiting for console message timed out")
			close(done)
		}
	} else {
		e.log.Debug().
			Str("id", job.Id.String()).
			Msg("using regular rendering")

		if e.Opts.LogConsole {
			// log console & browser log

			go page.EachEvent(func(evt *proto.RuntimeConsoleAPICalled) {
				e.log.Info().Str("job", job.Id.String()).Str("src", "js console").Msg(page.MustObjectsToJSON(evt.Args).String())
			},
				func(evt *proto.LogEntryAdded) {
					if evt.Entry != nil {
						msg, _ := json.Marshal(evt.Entry)
						e.log.Info().Str("job", job.Id.String()).Str("src", "log").Msg(string(msg))
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
		e.log.Info().
			Str("id", job.Id.String()).
			Int("size", len(result.Output)).
			Float64("elapsedTime", result.ElapsedTime).
			Msgf("finished job in %f seconds", result.ElapsedTime)
	} else {
		e.log.Info().
			Str("id", job.Id.String()).
			Float64("elapsedTime", result.ElapsedTime).
			Err(err).
			Msg("job failed")

	}
	return result
}

func (e *Engine) GetBrowser() *rod.Browser {
	return e.BrowserPool.Get(func() *rod.Browser {
		return rod.New().Context(e.Opts.Context).MustConnect()
	})
}

func (e *Engine) Shutdown() {
	e.log.Info().Msg("Shutting down render.Engine...")
	e.BrowserPool.Cleanup(func(p *rod.Browser) {
		// shutdown browsers
		p.MustClose()
	})
	e.ServerPool.Shutdown()
}
