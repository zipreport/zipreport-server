package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	log "github.com/sirupsen/logrus"
	"os"
	"os/signal"
	"syscall"
	"zipreport-server/pkg/apiserver"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"
	"zipreport-server/pkg/zpt"
)

const VERSION = "1.0.2"

func usage() {
	fmt.Printf("Usage: %s [OPTIONS] argument ...\n\n", os.Args[0])
	flag.PrintDefaults()
}

func buildServer() (*apiserver.ApiServer, error) {
	flag.Usage = usage
	/* General options */
	addr := flag.String("addr", "127.0.0.1", "Address to listen")
	port := flag.Int("port", apiserver.DefaultPort, "Port to listen")
	keyFile := flag.String("certkey", "", "Certificate key file")
	crtFile := flag.String("certificate", "", "Certificate file")
	apiKey := flag.String("apikey", "", "API access key")
	storagePath := flag.String("zpt", "", "Temporary zpt path")
	cli := flag.String("cli", "", "zipreport-client binary")

	/* Advanced options */
	readTimeout := flag.Int("httprt", apiserver.DefaultReadTimeout, "HTTP read timeout")
	writeTimeout := flag.Int("httpwt", apiserver.DefaultWriteTimeout, "HTTP write timeout")
	debug := flag.Bool("debug", false, "Enable webserver verbose output")
	noMetrics := flag.Bool("nometrics", false, "Disable Prometheus endpoint")
	noSandbox := flag.Bool("no-sandbox", false, "Disable chromium sandbox")
	noGpu := flag.Bool("no-gpu", false, "Disable GPU acceleration")
	version := flag.Bool("version", false, "Show version")

	flag.Parse()
	if flag.NFlag() == 0 {
		flag.Usage()
		os.Exit(0)
	}

	if *version {
		fmt.Println(VERSION)
		os.Exit(0)
	}

	if len(*cli) == 0 {
		return nil, errors.New("-cli parameter missing")
	}

	if len(*storagePath) == 0 {
		return nil, errors.New("-zpt parameter missing")
	}

	if *debug {
		log.SetLevel(log.InfoLevel)
	}

	// Initialize renderer
	renderer := render.NewZptRenderer(*cli, *noSandbox, *noGpu)
	if err := renderer.Init(); err != nil {
		return nil, err
	}

	// Initialize zpt
	storage := zpt.NewDiskBackend(*storagePath)
	if err := storage.Init(); err != nil {
		return nil, err
	}

	// Server configuration
	cfg := apiserver.NewConfiguration()
	cfg.Addr = *addr
	cfg.Port = *port
	cfg.TLS = len(*crtFile) > 0 && len(*keyFile) > 0
	cfg.SSLKeyFile = *keyFile
	cfg.SSLCertFile = *crtFile
	cfg.ReadTimeout = *readTimeout
	cfg.WriteTimeout = *writeTimeout
	cfg.Debug = *debug
	cfg.ApiKey = *apiKey

	// prometheus metrics
	var metrics *monitor.Metrics = nil
	if !*noMetrics {
		metrics = monitor.NewMetrics()
	}
	// http server
	server := apiserver.NewApiServer(cfg, apiserver.DefaultRouter(cfg, storage, renderer, metrics))
	return server, nil
}

func main() {
	log.SetFormatter(&log.TextFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.InfoLevel)

	server, err := buildServer()
	if err != nil {
		log.Fatal(err)
	}

	// if not debug mode, reduce log level
	if !server.Config.Debug {
		log.SetLevel(log.WarnLevel)
	}

	fmt.Printf("Starting Server in %s:%d...\n", server.Config.Addr, server.Config.Port)

	go func() {
		if err = server.Run(); err != nil {
			log.Fatal(err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())
	monitor := make(chan os.Signal, 1)
	signal.Notify(monitor, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	for {
		select {
		case <-monitor:
			log.Info("Shutting down...")
			cancel()

		case <-ctx.Done():
			signal.Stop(monitor)
			server.Shutdown(ctx)
			os.Exit(0)
		}
	}
}
