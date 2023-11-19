package render

import (
	"context"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/rs/zerolog"
	"io"
	"time"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/zpt"
)

type EngineOptions struct {
	HttpDebug   bool
	Concurrency int
	BasePort    int
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
		Concurrency: 8,
		BasePort:    42000,
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

func (e *Engine) RenderJob(job *Job) *JobResult {
	e.log.Debug().
		Str("id", job.Id.String()).
		Any("Job", job).
		Msg("starting job")
	server := e.ServerPool.BuildServer(job.Zpt, e.Opts.HttpDebug)
	e.log.Debug().
		Str("id", job.Id.String()).
		Str("address", server.Server.Addr).
		Msg("server address")
	browser := e.GetBrowser()
	defer e.BrowserPool.Put(browser)
	defer e.ServerPool.RemoveServer(server)

	start := time.Now()
	browser.MustIgnoreCertErrors(job.IgnoreSSLErrors)
	url := "http://" + server.Server.Addr
	page := browser.
		MustPage().
		Timeout(time.Duration(job.JobTimeoutS) * time.Second)

	// EachEvent allows us to achieve the same functionality as above.
	if job.UseJSEvent {
		e.log.Debug().
			Str("id", job.Id.String()).
			Msg("using JS console message")
		// wait for complete event before proceeding
		done := make(chan struct{})
		go page.EachEvent(func(e *proto.RuntimeConsoleAPICalled) {
			if page.MustObjectToJSON(e.Args[0]).String() == "zpt-view-ready" {
				close(done)
			}
		})()
		wait := page.WaitEvent(&proto.PageLoadEventFired{})
		page.MustNavigate(url).MustWaitLoad()
		wait()
		<-done
	} else {
		e.log.Debug().
			Str("ID", job.Id.String()).
			Msg("using regular rendering")
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
	page.MustClose()

	if err == nil {
		e.log.Info().
			Str("ID", job.Id.String()).
			Int("Size", len(result.Output)).
			Msgf("finished job successfully in %f seconds", result.ElapsedTime)
	} else {
		e.log.Info().
			Str("ID", job.Id.String()).
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
