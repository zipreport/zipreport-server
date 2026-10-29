package internal

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"
	"zipreport-server/internal/apiserver"
	"zipreport-server/pkg/egress"
	"zipreport-server/pkg/metrics"
	"zipreport-server/pkg/render"

	"github.com/oddbit-project/blueprint/log"
	"github.com/oddbit-project/blueprint/provider/httpserver"
	"github.com/rs/zerolog"
)

const (
	DefaultReadTimeoutSeconds  = 300
	DefaultWriteTimeoutSeconds = 300
	DefaultPort                = 6543
	// API read covers headers and the multipart upload (<=128 MiB); slow headers are
	// bounded separately by ReadHeaderTimeout.
	DefaultApiReadTimeout = 600
	// API write is armed after the request headers are read and so covers the upload (it must be
	// >= the read timeout); renderAction re-arms it after the upload to job timeout + write timeout.
	DefaultApiWriteTimeout = render.JobMaxTimeout + 120
	MinAuthSecretLength    = 32
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
	ReadTimeoutSeconds   int                 `json:"readTimeoutSeconds"`
	WriteTimeoutSeconds  int                 `json:"writeTimeoutSeconds"`
	EnableConsoleLogging bool                `json:"enableConsoleLogging"` // Enable JS console logging, if loglevel allows
	EnableHttpDebugging  bool                `json:"enableHttpDebugging"`
	EnableMetrics        bool                `json:"enableMetrics"` // Enable Prometheus endpoint
	Concurrency          int                 `json:"concurrency"`   // Concurrent browser instances
	BaseHttpPort         int                 `json:"baseHttpPort"`  // Internal HTTP server base port
	EgressAllow          []string            `json:"egressAllow"`   // CIDRs or exact host:port a rendered page may reach beyond public addresses
	UpstreamProxy        UpstreamProxyConfig `json:"upstreamProxy"`
}

// UpstreamProxyConfig chains the per-job egress proxy to an operator's proxy. An empty URL disables chaining.
type UpstreamProxyConfig struct {
	URL       string   `json:"url"`       // http://[user:pass@]host:port or https://...; ZIPREPORT_UPSTREAM_PROXY overrides it
	Resolve   string   `json:"resolve"`   // "local" (default): tunnel to the vetted IP; "upstream": hand over the host name
	NoProxy   []string `json:"noProxy"`   // public CIDRs or exact host:port dialled directly (still vetted)
	AuthProbe string   `json:"authProbe"` // host:port the upstream requires auth for; a 407 elsewhere is checked against it
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
		UpstreamProxy:        UpstreamProxyConfig{URL: "", Resolve: "local", NoProxy: []string{}, AuthProbe: ""},
	}
}

func (c *ZipReportConfig) ServerTimeouts() (read, write time.Duration) {
	return time.Duration(c.ReadTimeoutSeconds) * time.Second, time.Duration(c.WriteTimeoutSeconds) * time.Second
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
		return errors.New("baseHttpPort must be at least 1024")
	}
	if c.BaseHttpPort+c.Concurrency-1 > 65535 {
		return errors.New("baseHttpPort + concurrency - 1 must not exceed 65535")
	}
	for i, s := range c.EgressAllow {
		if _, err := egress.ParseRule(s); err != nil {
			return fmt.Errorf("zipReport.egressAllow[%d]: %w", i, err)
		}
	}
	if _, err := egress.ParseUpstream(c.UpstreamProxy.URL, c.UpstreamProxy.Resolve, c.UpstreamProxy.NoProxy); err != nil {
		return fmt.Errorf("zipReport.upstreamProxy: %w", err)
	}
	if p := c.UpstreamProxy.AuthProbe; p != "" {
		if err := egress.ParseAuthProbe(p); err != nil {
			return fmt.Errorf("zipReport.upstreamProxy: %w", err)
		}
		up, _ := egress.ParseUpstream(c.UpstreamProxy.URL, c.UpstreamProxy.Resolve, c.UpstreamProxy.NoProxy)
		if up == nil || up.URL.User == nil {
			return errors.New("zipReport.upstreamProxy: authProbe needs credentials in url or ZIPREPORT_UPSTREAM_PROXY")
		}
		host, port, _ := net.SplitHostPort(p)
		if strings.EqualFold(strings.TrimSuffix(host, "."), up.Hostname()) && port == up.URL.Port() {
			return errors.New("zipReport.upstreamProxy: authProbe must not be the upstream proxy itself")
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
	sc.ReadTimeout = DefaultApiReadTimeout
	sc.WriteTimeout = DefaultApiWriteTimeout
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
	if c.ApiServer.ReadTimeout <= 0 {
		c.ApiServer.ReadTimeout = DefaultApiReadTimeout
	}
	if c.ApiServer.WriteTimeout <= 0 {
		c.ApiServer.WriteTimeout = DefaultApiWriteTimeout
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
	first, last := c.ZipReport.BaseHttpPort, c.ZipReport.BaseHttpPort+c.ZipReport.Concurrency-1
	if c.ApiServer.Port >= first && c.ApiServer.Port <= last {
		return fmt.Errorf("apiServer.port %d is inside the report port range %d-%d", c.ApiServer.Port, first, last)
	}
	if c.ZipReport.EnableMetrics {
		if c.Prometheus.Port >= first && c.Prometheus.Port <= last {
			return fmt.Errorf("prometheus.port %d is inside the report port range %d-%d", c.Prometheus.Port, first, last)
		}
		if c.Prometheus.Port == c.ApiServer.Port {
			return errors.New("prometheus.port must differ from apiServer.port")
		}
	}
	if up, _ := egress.ParseUpstream(c.ZipReport.UpstreamProxy.URL, c.ZipReport.UpstreamProxy.Resolve, c.ZipReport.UpstreamProxy.NoProxy); up != nil {
		if egress.NamesThisHost(up.Hostname()) {
			port, _ := strconv.Atoi(up.URL.Port())
			what := ""
			switch {
			case port >= first && port <= last:
				what = "the report port range"
			case port == c.ApiServer.Port:
				what = "apiServer.port"
			case c.ZipReport.EnableMetrics && port == c.Prometheus.Port:
				what = "prometheus.port"
			}
			if what != "" {
				return fmt.Errorf("zipReport.upstreamProxy port %d collides with %s", port, what)
			}
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
//   - ZIPREPORT_UPSTREAM_PROXY: overrides zipReport.upstreamProxy.url
//
// HTTP_PROXY, HTTPS_PROXY and NO_PROXY are deliberately ignored.
func (c *Config) ApplyEnvOverrides() {
	if apiKey := os.Getenv("ZIPREPORT_API_KEY"); apiKey != "" && c.ApiServer != nil {
		c.ApiServer.AuthTokenSecret = apiKey
	}
	if up := os.Getenv("ZIPREPORT_UPSTREAM_PROXY"); up != "" && c.ZipReport != nil {
		c.ZipReport.UpstreamProxy.URL = up
	}
}

func (c *Config) DumpDefaults() (string, error) {
	result, err := json.MarshalIndent(c, "", "  ")
	return string(result), err
}

// HttpDebugSuppressedAt reports whether the log level filters out Info lines.
func HttpDebugSuppressedAt(level string) bool {
	lvl, err := zerolog.ParseLevel(level)
	return err == nil && lvl > zerolog.InfoLevel
}
