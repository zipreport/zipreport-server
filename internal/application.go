package internal

import (
	"context"
	"fmt"
	"time"

	"zipreport-server/internal/apiserver"
	"zipreport-server/pkg/egress"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"
	"zipreport-server/pkg/zpt"

	"github.com/oddbit-project/blueprint"
	"github.com/oddbit-project/blueprint/config/provider"
	"github.com/oddbit-project/blueprint/log"
	"github.com/oddbit-project/blueprint/provider/httpserver"
)

// ShutdownTimeout bounds all destructors together; it must stay below docker stop's
// default 10s grace period (Dockerfile STOPSIGNAL SIGINT) or the drain is killed
const ShutdownTimeout = 8 * time.Second

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
	blueprint.SetShutdownTimeout(ShutdownTimeout)

	var err error
	z.logger.Infof("initializing %s...", appName)

	cfg := NewConfig()
	z.AbortFatal(z.Config.Get(cfg))

	// Apply environment variable overrides (e.g., ZIPREPORT_API_KEY)
	cfg.ApplyEnvOverrides()

	z.AbortFatal(cfg.Validate())

	// initialize logger
	z.logger.Info("initializing Logging...")
	z.AbortFatal(cfg.Logging.Validate())
	z.AbortFatal(log.Configure(cfg.Logging))
	z.logger, err = cfg.Logging.Logger()
	z.AbortFatal(err)

	// initialize metrics
	metrics := monitor.NewMetrics()

	// initialize ZipReport engine
	// Blueprint cancels z.Context before destructors run; the engine must outlive it so the API
	// destructor can drain in-flight renders. It is cancelled by the engine destructor, which runs last.
	engineCtx, engineCancel := context.WithCancel(context.WithoutCancel(z.Context))
	if overlap, lo, hi, ok := zpt.EphemeralRangeOverlap(cfg.ZipReport.BaseHttpPort, cfg.ZipReport.Concurrency); ok && overlap {
		z.logger.Warn(fmt.Sprintf("render ports %d-%d overlap the OS ephemeral port range %d-%d; outbound connections can take a report port and fail jobs. Lower baseHttpPort below %d or run: sysctl net.ipv4.ip_local_reserved_ports=%d-%d",
			cfg.ZipReport.BaseHttpPort, cfg.ZipReport.BaseHttpPort+cfg.ZipReport.Concurrency-1, lo, hi, lo,
			cfg.ZipReport.BaseHttpPort, cfg.ZipReport.BaseHttpPort+cfg.ZipReport.Concurrency-1))
	}
	zptEngine := render.NewEngine(engineCtx, cfg.ZipReport.Concurrency, cfg.ZipReport.BaseHttpPort, metrics, z.logger)
	if cfg.ZipReport.EnableConsoleLogging {
		zptEngine.EnableConsoleLog()
	}
	if cfg.ZipReport.EnableHttpDebugging {
		zptEngine.EnableHttpDebugging()
	}
	policy := egress.Policy{ReservedPorts: []int{cfg.ApiServer.Port}}
	if cfg.ZipReport.EnableMetrics {
		policy.ReservedPorts = append(policy.ReservedPorts, cfg.Prometheus.Port)
	}
	for _, s := range cfg.ZipReport.EgressAllow {
		rule, err := egress.ParseRule(s) // validated in cfg.Validate()
		z.AbortFatal(err)
		policy.Allow = append(policy.Allow, rule)
	}
	zptEngine.SetEgressPolicy(policy)
	// register engine destructor to release browsers and ephemeral servers on shutdown
	blueprint.RegisterDestructor(func() error {
		zptEngine.Shutdown()
		engineCancel()
		return nil
	})

	// initialize Prometheus Endpoint
	if cfg.ZipReport.EnableMetrics {
		z.AbortFatal(cfg.Prometheus.Validate())

		prom, err := cfg.Prometheus.NewServer(z.logger)
		z.AbortFatal(err)

		// start the metrics endpoint (blocking call, so run in background)
		go func() {
			if err := prom.Start(); err != nil {
				z.logger.Error(err, "prometheus metrics server failed")
			}
		}()

		// register prometheus destructor
		blueprint.RegisterDestructor(func() error {
			_ = prom.Shutdown(blueprint.ShutdownContext())
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
		_ = z.api.Shutdown(blueprint.ShutdownContext())
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
