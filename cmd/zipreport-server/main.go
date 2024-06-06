package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"os"
	"os/signal"
	"syscall"
	"zipreport-server/pkg/apiserver"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"
)

const VERSION = "2.1.1"

func usage() {
	fmt.Printf("Usage: %s [OPTIONS] argument ...\n\n", os.Args[0])
	flag.PrintDefaults()
}

func buildServer(ctx context.Context) (*apiserver.ApiServer, *render.Engine, error) {
	flag.Usage = usage
	/* General options */
	addr := flag.String("addr", "127.0.0.1", "API Address")
	port := flag.Int("port", apiserver.DefaultPort, "API Port to listen")
	keyFile := flag.String("certkey", "", "Certificate key file")
	crtFile := flag.String("certificate", "", "Certificate file")
	apiKey := flag.String("apikey", "", "API access key")

	/* Advanced options */
	readTimeout := flag.Int("httprt", apiserver.DefaultReadTimeout, "HTTP read timeout")
	writeTimeout := flag.Int("httpwt", apiserver.DefaultWriteTimeout, "HTTP write timeout")
	debug := flag.Bool("debug", false, "Enable webserver verbose output")
	console := flag.Bool("console", false, "Enable JS console logging, if loglevel allows")
	noMetrics := flag.Bool("nometrics", false, "Disable Prometheus endpoint")
	concurrency := flag.Int("concurrency", render.DefaultConcurrency, "Concurrent browser instances")
	basePort := flag.Int("baseport", render.DefaultBasePort, "Internal HTTP server base port")
	loglevel := flag.Int("loglevel", int(zerolog.InfoLevel), "Log verbosity")
	version := flag.Bool("version", false, "Show version")

	flag.Parse()
	/*
		if flag.NFlag() == 0 {
			flag.Usage()
			os.Exit(0)
		}*/

	if *version {
		fmt.Println(VERSION)
		os.Exit(0)
	}

	if *basePort < 1024 {
		fmt.Println("baseport must be > 1024")
		os.Exit(1)
	}
	if *loglevel < -1 || *loglevel > 7 {
		fmt.Println("invalid loglevel")
		os.Exit(1)
	}

	zerolog.SetGlobalLevel(zerolog.Level(*loglevel))
	logger := zerolog.New(os.Stdout).With().Logger()
	opts := render.DefaultEngineOptions(ctx, logger)
	metrics := monitor.NewMetrics()
	opts.Concurrency = *concurrency
	opts.BasePort = *basePort
	opts.HttpDebug = *debug

	engine := render.NewEngine(opts, metrics)

	// Enable console logging
	if *console {
		engine.EnableConsoleLog()
	}

	// Api server configuration
	apiCfg := apiserver.DefaultApiOptions(ctx, logger)
	apiCfg.Addr = *addr
	apiCfg.Port = *port
	apiCfg.TLS = len(*crtFile) > 0 && len(*keyFile) > 0
	apiCfg.SSLKeyFile = *keyFile
	apiCfg.SSLCertFile = *crtFile
	apiCfg.ReadTimeout = *readTimeout
	apiCfg.WriteTimeout = *writeTimeout
	apiCfg.Debug = *debug
	apiCfg.ApiKey = *apiKey
	apiCfg.UseMetrics = !*noMetrics

	server := apiserver.NewApiServer(apiCfg, apiserver.ApiRouter(apiCfg, engine, metrics))
	return server, engine, nil
}

func main() {
	log.Output(os.Stdout)
	zerolog.SetGlobalLevel(zerolog.WarnLevel)

	ctx, cancel := context.WithCancel(context.Background())
	monitor := make(chan os.Signal, 1)
	signal.Notify(monitor, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	server, engine, err := buildServer(ctx)
	if err != nil {
		log.Err(err).Msg("fatal error")
		os.Exit(1)
	}

	go func() {
		if err = server.Run(); err != nil {
			log.Err(err).Msg("fatal error")
			os.Exit(1)
		}
	}()

	for {
		select {
		case <-monitor:
			log.Info().Msg("Shutting down...")
			cancel()

		case <-ctx.Done():
			signal.Stop(monitor)
			server.Shutdown(ctx)
			engine.Shutdown()
			os.Exit(0)
		}
	}
}
