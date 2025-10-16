package internal

import (
	"zipreport-server/internal/apiserver"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"

	"github.com/oddbit-project/blueprint"
	"github.com/oddbit-project/blueprint/config/provider"
	"github.com/oddbit-project/blueprint/log"
	"github.com/oddbit-project/blueprint/provider/httpserver"
)

type CliArgs struct {
	ConfigFile   *string
	ShowVersion  *bool
	SampleConfig *bool
}

type ZipReport struct {
	*blueprint.Container
	api    *httpserver.Server
	args   *CliArgs
	logger *log.Logger
}

func NewZipReport(args *CliArgs, logger *log.Logger) (*ZipReport, error) {
	cfg, err := provider.NewJsonProvider(*args.ConfigFile)
	if err != nil {
		return nil, err
	}

	return &ZipReport{
		Container: blueprint.NewContainer(cfg),
		api:       nil,
		args:      args,
		logger:    logger,
	}, nil
}

func (z *ZipReport) Build(appName string) {
	var err error
	z.logger.Infof("initializing %s...", appName)

	cfg := NewConfig()
	z.AbortFatal(z.Config.Get(cfg))

	z.AbortFatal(cfg.Validate())

	// initialize logger
	z.logger.Info("initializing Logging...")
	z.AbortFatal(cfg.Logging.Validate())
	z.logger, err = cfg.Logging.Logger()
	z.AbortFatal(err)

	// initialize metrics
	metrics := monitor.NewMetrics()

	// initialize ZipReport engine
	zptEngine := render.NewEngine(z.Context, cfg.ZipReport.Concurrency, cfg.ZipReport.BaseHttpPort, metrics, z.logger)
	if cfg.ZipReport.EnableConsoleLogging {
		zptEngine.EnableConsoleLog()
	}
	if cfg.ZipReport.EnableHttpDebugging {
		zptEngine.EnableHttpDebugging()
	}

	// initialize Prometheus Endpoint
	if cfg.ZipReport.EnableMetrics {
		z.AbortFatal(cfg.Prometheus.Validate())

		prom, err := cfg.Prometheus.NewServer(z.logger)
		z.AbortFatal(err)

		// register prometheus destructor
		blueprint.RegisterDestructor(func() error {
			_ = prom.Shutdown(z.Context)
			return nil
		})
	}

	// initialize Api Server
	z.api, err = apiserver.NewApiServer(cfg.ApiServer, zptEngine, metrics, z.logger)
	z.AbortFatal(err)

}

func (z *ZipReport) Start() {
	// register apiServer destructor
	blueprint.RegisterDestructor(func() error {
		_ = z.api.Shutdown(z.Context)
		return nil
	})

	z.Run(
		func(app interface{}) error {
			go func() {
				// this call is blocking
				err := z.api.Start()
				if err != nil {
					z.logger.Error(err, "zipReport API server failed")
				}
			}()
			return nil
		},
	)
}
