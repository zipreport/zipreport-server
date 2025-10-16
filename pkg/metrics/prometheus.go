package metrics

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/oddbit-project/blueprint/log"
	"github.com/oddbit-project/blueprint/log/writer"
	httplog "github.com/oddbit-project/blueprint/provider/httpserver/log"
	tlsProvider "github.com/oddbit-project/blueprint/provider/tls"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const (
	DefaultEndpoint = "/metrics"
	DefaultPort     = 2220
	serverName      = "prometheus"
)

type PrometheusConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Endpoint string `json:"endpoint"`
	tlsProvider.ServerConfig
}

type PrometheusServer struct {
	Router *http.ServeMux
	Server *http.Server
	Logger *log.Logger
}

func NewPrometheusConfig() *PrometheusConfig {
	return &PrometheusConfig{
		Host:     "localhost",
		Port:     DefaultPort,
		Endpoint: DefaultEndpoint,
		ServerConfig: tlsProvider.ServerConfig{
			TLSCert: "",
			TLSKey:  "",
			TlsKeyCredential: tlsProvider.TlsKeyCredential{
				Password:       "",
				PasswordEnvVar: "",
				PasswordFile:   "",
			},
			TLSAllowedCACerts:  nil,
			TLSCipherSuites:    nil,
			TLSMinVersion:      "",
			TLSMaxVersion:      "",
			TLSAllowedDNSNames: nil,
			TLSEnable:          false,
		},
	}
}

func (c *PrometheusConfig) Validate() error {
	if c.Host == "" {
		return errors.New("prometheus host is required")
	}
	if c.Port < 1024 {
		return errors.New("prometheus port is less than 1024")
	}
	return nil
}

func (c *PrometheusConfig) NewServer(logger *log.Logger, cs ...prometheus.Collector) (*PrometheusServer, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return NewServer(c, logger, cs...)
}

// NewServer creates a new prometheus server
//
// Example usage:
//
//	cfg := &ServerConfig{...}
//	server, err := NewServer(cfg)
//	if err != nil {
//	  log.Fatal(err)
//	}
//	server.Start()
func NewServer(cfg *PrometheusConfig, logger *log.Logger, cs ...prometheus.Collector) (*PrometheusServer, error) {
	if cfg == nil {
		cfg = NewPrometheusConfig()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if logger == nil {
		logger = httplog.NewHTTPLogger(serverName)
	}

	tlsConfig, err := cfg.TLSConfig()
	if err != nil {
		return nil, err
	}

	// register collectors
	if len(cs) > 0 {
		prometheus.MustRegister(cs...)
	}

	router := http.NewServeMux()
	router.Handle(cfg.Endpoint, promhttp.Handler())

	result := &PrometheusServer{
		Router: router,
		Logger: logger,
		Server: &http.Server{
			Addr:      fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
			Handler:   router,
			TLSConfig: tlsConfig,
			ErrorLog:  writer.NewErrorLog(logger), // error log wrapper
		},
	}

	return result, nil
}

// Start
// blocking function
func (s *PrometheusServer) Start() error {
	var err error
	if s.Server.TLSConfig == nil {
		err = s.Server.ListenAndServe()
	} else {
		err = s.Server.ListenAndServeTLS("", "")
	}
	// when Shutdown() is called, the return error is http.ErrServerClosed
	if !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Stop prometheus
func (s *PrometheusServer) Shutdown(ctx context.Context) error {
	return s.Server.Shutdown(ctx)
}
