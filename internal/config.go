package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"zipreport-server/internal/apiserver"
	"zipreport-server/pkg/egress"
	"zipreport-server/pkg/metrics"
	"zipreport-server/pkg/render"

	"github.com/oddbit-project/blueprint/log"
	"github.com/oddbit-project/blueprint/provider/httpserver"
)

const (
	DefaultReadTimeoutSeconds  = 300
	DefaultWriteTimeoutSeconds = 300
	DefaultPort                = 6543
	MinAuthSecretLength        = 32
)

var (
	ErrEmptyAuthSecret       = errors.New("authTokenSecret option cannot be empty")
	ErrWhitespaceAuthSecret  = errors.New("authTokenSecret must not start or end with whitespace")
	ErrPlaceholderAuthSecret = errors.New("authTokenSecret is a publicly documented placeholder; set a unique secret in apiServer.authTokenSecret or ZIPREPORT_API_KEY")
	ErrWeakAuthSecret        = errors.New("authTokenSecret must be at least 32 characters; generate one with: openssl rand -hex 32")
)

// values published as example secrets in sample configs and docs
var placeholderAuthSecrets = []string{
	"my-super-secret-token",
	"your-secret",
	"your-secure-random-token-here",
	"your-secret-key",
	"my-secret",
	"your-secure-token-here",
	"<generate: openssl rand -hex 32>",
}

type ZipReportConfig struct {
	ReadTimeoutSeconds   int      `json:"readTimeoutSeconds"`
	WriteTimeoutSeconds  int      `json:"writeTimeoutSeconds"`
	EnableConsoleLogging bool     `json:"enableConsoleLogging"` // Enable JS console logging, if loglevel allows
	EnableHttpDebugging  bool     `json:"enableHttpDebugging"`
	EnableMetrics        bool     `json:"enableMetrics"` // Enable Prometheus endpoint
	Concurrency          int      `json:"concurrency"`   // Concurrent browser instances
	BaseHttpPort         int      `json:"baseHttpPort"`  // Internal HTTP server base port
	EgressAllow          []string `json:"egressAllow"`   // CIDRs or exact host:port a rendered page may reach beyond public addresses
}

type Config struct {
	ApiServer  *apiserver.ApiServerConfig `json:"apiServer"`
	Prometheus *metrics.PrometheusConfig  `json:"prometheus"`
	ZipReport  *ZipReportConfig           `json:"zipReport"`
	Logging    *log.Config                `json:"log"`
}

func NewZipReportConfig() *ZipReportConfig {
	return &ZipReportConfig{
		ReadTimeoutSeconds:   DefaultReadTimeoutSeconds,
		WriteTimeoutSeconds:  DefaultWriteTimeoutSeconds,
		EnableConsoleLogging: false,
		EnableMetrics:        false,
		Concurrency:          render.DefaultConcurrency,
		BaseHttpPort:         render.DefaultBasePort,
		EgressAllow:          []string{},
	}
}

func (c *ZipReportConfig) Validate() error {
	if c.ReadTimeoutSeconds < 1 {
		return errors.New("readTimeoutSeconds must be greater than zero")
	}
	if c.WriteTimeoutSeconds < 1 {
		return errors.New("writeTimeoutSeconds must be greater than zero")
	}
	if c.Concurrency < 1 {
		return errors.New("concurrency must be greater than zero")
	}
	if c.BaseHttpPort < 1024 {
		return errors.New("baseHttpPort must be greater than 1024")
	}
	for i, s := range c.EgressAllow {
		if _, err := egress.ParseRule(s); err != nil {
			return fmt.Errorf("zipReport.egressAllow[%d]: %w", i, err)
		}
	}
	return nil
}

func NewConfig() *Config {
	return &Config{
		ApiServer:  apiServerConfig(),
		Prometheus: metrics.NewPrometheusConfig(),
		ZipReport:  NewZipReportConfig(),
		Logging:    log.NewDefaultConfig(),
	}
}

func apiServerConfig() *apiserver.ApiServerConfig {
	sc := httpserver.NewServerConfig()
	sc.Port = DefaultPort
	return &apiserver.ApiServerConfig{
		ServerConfig:           *sc,
		AuthTokenHeader:        "X-Auth-Key",
		AuthTokenSecret:        "",
		DefaultSecurityHeaders: true,
	}
}

func (c *Config) Validate() error {
	if c.ApiServer == nil {
		return errors.New("apiServer is required")
	}
	if err := c.ApiServer.Validate(); err != nil {
		return err
	}

	s := c.ApiServer.AuthTokenSecret
	t := strings.TrimSpace(s)
	if t == "" {
		return ErrEmptyAuthSecret
	}
	if t != s {
		return ErrWhitespaceAuthSecret
	}
	if slices.Contains(placeholderAuthSecrets, t) {
		return ErrPlaceholderAuthSecret
	}
	if len(t) < MinAuthSecretLength {
		return ErrWeakAuthSecret
	}

	if c.ZipReport == nil {
		return errors.New("zipreport is required")
	}
	if err := c.ZipReport.Validate(); err != nil {
		return err
	}
	if c.ZipReport.EnableMetrics {
		if c.Prometheus == nil {
			return errors.New("prometheus is required")
		}
		if err := c.Prometheus.Validate(); err != nil {
			return err
		}
	}
	if c.Logging == nil {
		return errors.New("logging is required")
	}
	if err := c.Logging.Validate(); err != nil {
		return err
	}
	return nil
}

// ApplyEnvOverrides applies environment variable overrides to the config.
// Environment variables take precedence over config file values.
// Supported variables:
//   - ZIPREPORT_API_KEY: overrides apiServer.authTokenSecret
func (c *Config) ApplyEnvOverrides() {
	if apiKey := os.Getenv("ZIPREPORT_API_KEY"); apiKey != "" {
		c.ApiServer.AuthTokenSecret = apiKey
	}
}

func (c *Config) DumpDefaults() (string, error) {
	result, err := json.MarshalIndent(c, "", "  ")
	return string(result), err
}
