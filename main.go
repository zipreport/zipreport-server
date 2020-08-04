package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	log "github.com/sirupsen/logrus"
	"net/http"
	"os"
	"os/signal"
	"time"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"
	"zipreport-server/pkg/storage"
	"zipreport-server/pkg/zptserver"
)

func usage() {
	fmt.Printf("Usage: %s [OPTIONS] argument ...\n\n", os.Args[0])
	flag.PrintDefaults()
}

func buildServer() (*zptserver.ZptServer, error) {
	flag.Usage = usage
	/* General options */
	addr := flag.String("addr", "127.0.0.1", "Address to listen")
	port := flag.Int("port", zptserver.DefaultPort, "Port to listen")
	keyFile := flag.String("certkey", "", "Certificate key file")
	crtFile := flag.String("certificate", "", "Certificate file")
	apiKey := flag.String("apikey", "", "API access key")
	storagePath := flag.String("storage", "", "Temporary storage path")
	cli := flag.String("cli", "", "zipreport-client binary")

	/* Advanced options */
	//chromePath := flag.String("chrome", "", "Browser path")
	readTimeout := flag.Int("httprt", zptserver.DefaultReadTimeout, "HTTP read timeout")
	writeTimeout := flag.Int("httpwt", zptserver.DefaultWriteTimeout, "HTTP write timeout")
	debug := flag.Bool("debug", false, "Enable webserver verbose output")
	noMetrics := flag.Bool("nometrics", false, "Disable Prometheus endpoint")

	flag.Parse()
	if flag.NFlag() == 0 {
		flag.Usage()
		os.Exit(0)
	}

	if len(*cli) == 0 {
		return nil, errors.New("-cli parameter missing")
	}

	if len(*storagePath) == 0 {
		return nil, errors.New("-storage parameter missing")
	}

	if *debug {
		log.SetLevel(log.InfoLevel)
	}

	// Initialize renderer
	renderer := render.NewZptRenderer(*cli)
	if err := renderer.Init(); err != nil {
		return nil, err
	}

	// Initialize storage
	storage := storage.NewDiskBackend(*storagePath)
	if err := storage.Init(); err != nil {
		return nil, err
	}

	// Server configuration
	cfg := zptserver.NewConfiguration()
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
	server := zptserver.NewZptServer(cfg, zptserver.DefaultRouter(cfg, storage, renderer, metrics))
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
	cancelChan := make(chan struct{})

	go func() {
		sigint := make(chan os.Signal, 1)
		signal.Notify(sigint, os.Interrupt)
		<-sigint
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		fmt.Println("\nShutting down...")
		if err := server.Shutdown(ctx); err != nil {
			log.Fatal(err)
		}
		close(cancelChan)
	}()

	err = server.Run()
	if err != http.ErrServerClosed {
		log.Fatal(err)
	}
	<-cancelChan
}
