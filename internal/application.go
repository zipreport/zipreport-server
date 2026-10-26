package internal

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
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
	z.AbortFatal(ConfigureEngine(zptEngine, cfg))
	if up := zptEngine.EgressPolicy().Upstream; up != nil {
		z.logger.Info("egress upstream proxy enabled", upstreamLogKV(up))
		if up.ByName {
			z.checkByNameUpstream(up)
		}
	}
	if cfg.ZipReport.EnableHttpDebugging && HttpDebugSuppressedAt(cfg.Logging.Level) {
		z.logger.Warn("enableHttpDebugging is on but log.level is above info; request lines will not be written")
	}
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
				z.AbortFatal(err)
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
					z.AbortFatal(err)
				}
			}()
			return nil
		},
	)
}

// upstreamLogKV is the only description of the upstream that is logged: never its URL, which may carry userinfo.
func upstreamLogKV(up *egress.Upstream) log.KV {
	resolve := "local"
	if up.ByName {
		resolve = "upstream"
	}
	return log.KV{"upstream": up.Endpoint(), "resolve": resolve}
}

// upstreamOnThisHost reports whether any of the upstream's addresses is loopback, unspecified or an interface address.
func upstreamOnThisHost(addrs []netip.Addr, ifaces []net.Addr) bool {
	local := egress.InterfaceIPs(ifaces)
	for _, a := range addrs {
		if egress.OnThisHost(a, local) {
			return true
		}
	}
	return false
}

// checkByNameUpstream refuses resolve "upstream" with an upstream on this host and warns about its trade-off.
func (z *ZipReport) checkByNameUpstream(up *egress.Upstream) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, lerr := net.DefaultResolver.LookupNetIP(ctx, "ip", up.Hostname())
	ifaces, ierr := net.InterfaceAddrs()
	if lerr != nil || ierr != nil {
		z.logger.Warn("cannot resolve the upstream proxy at startup; each job will check it")
		return
	}
	if upstreamOnThisHost(addrs, ifaces) {
		z.AbortFatal(errors.New("zipReport.upstreamProxy: resolve \"upstream\" cannot be used with an upstream proxy on this host"))
		return
	}
	z.logger.Warn("zipReport.upstreamProxy.resolve is \"upstream\": the upstream proxy decides where host names go, so a rendered page can reach anything the upstream can reach; configure destination ACLs on the upstream and never use this mode with an upstream that shares this server's network namespace")
}

// ConfigureEngine applies the zipReport settings to the render engine.
func ConfigureEngine(e *render.Engine, cfg *Config) error {
	if cfg.ZipReport.EnableConsoleLogging {
		e.EnableConsoleLog()
	}
	if cfg.ZipReport.EnableHttpDebugging {
		e.EnableHttpDebugging()
	}
	policy := egress.Policy{ReservedPorts: []int{cfg.ApiServer.Port}}
	if cfg.ZipReport.EnableMetrics {
		policy.ReservedPorts = append(policy.ReservedPorts, cfg.Prometheus.Port)
	}
	for _, s := range cfg.ZipReport.EgressAllow {
		rule, err := egress.ParseRule(s) // validated in cfg.Validate()
		if err != nil {
			return err
		}
		policy.Allow = append(policy.Allow, rule)
	}
	up, err := egress.ParseUpstream(cfg.ZipReport.UpstreamProxy.URL, cfg.ZipReport.UpstreamProxy.Resolve, cfg.ZipReport.UpstreamProxy.NoProxy)
	if err != nil {
		return fmt.Errorf("zipReport.upstreamProxy: %w", err)
	}
	policy.Upstream = up
	e.SetEgressPolicy(policy)
	e.SetServerTimeouts(cfg.ZipReport.ServerTimeouts())
	return nil
}
