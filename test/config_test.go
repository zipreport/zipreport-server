package test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"zipreport-server/internal"
	"zipreport-server/internal/apiserver"
	"zipreport-server/pkg/render"

	"github.com/gin-gonic/gin"
	"github.com/oddbit-project/blueprint/config/provider"
	"github.com/oddbit-project/blueprint/log"
	"github.com/oddbit-project/blueprint/provider/httpserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var strongSecret = strings.Repeat("a1", 32)

func configWithSecret(secret string) *internal.Config {
	cfg := internal.NewConfig()
	cfg.ApiServer.AuthTokenSecret = secret
	return cfg
}

func TestConfig_RejectsEmptySecret(t *testing.T) {
	assert.ErrorIs(t, internal.NewConfig().Validate(), internal.ErrEmptyAuthSecret)
}

func TestConfig_RejectsBlankSecret(t *testing.T) {
	assert.ErrorIs(t, configWithSecret("   \t").Validate(), internal.ErrEmptyAuthSecret)
}

func TestConfig_RejectsWhitespaceSecret(t *testing.T) {
	assert.ErrorIs(t, configWithSecret(" "+strongSecret).Validate(), internal.ErrWhitespaceAuthSecret)
	assert.ErrorIs(t, configWithSecret(strongSecret+"\n").Validate(), internal.ErrWhitespaceAuthSecret)
}

var placeholderSecrets = []string{
	"my-super-secret-token",
	"your-secret",
	"your-secure-random-token-here",
	"your-secret-key",
	"my-secret",
	"your-secure-token-here",
	"<generate: openssl rand -hex 32>",
}

func TestConfig_RejectsPlaceholderSecrets(t *testing.T) {
	for _, p := range placeholderSecrets {
		t.Run(p, func(t *testing.T) {
			assert.ErrorIs(t, configWithSecret(p).Validate(), internal.ErrPlaceholderAuthSecret)
		})
	}
}

func TestConfig_LengthBoundary(t *testing.T) {
	assert.ErrorIs(t, configWithSecret(strings.Repeat("x", 31)).Validate(), internal.ErrWeakAuthSecret)
	assert.NoError(t, configWithSecret(strings.Repeat("x", 32)).Validate())
}

func TestConfig_AcceptsStrongSecret(t *testing.T) {
	assert.NoError(t, configWithSecret(strongSecret).Validate())
}

func TestConfig_EnvOverrideReplacesPlaceholder(t *testing.T) {
	cfg := configWithSecret(placeholderSecrets[0])
	t.Setenv("ZIPREPORT_API_KEY", strongSecret)
	cfg.ApplyEnvOverrides()
	assert.NoError(t, cfg.Validate())
}

func TestConfig_EnvPlaceholderRejected(t *testing.T) {
	cfg := configWithSecret(strongSecret)
	t.Setenv("ZIPREPORT_API_KEY", placeholderSecrets[3])
	cfg.ApplyEnvOverrides()
	assert.ErrorIs(t, cfg.Validate(), internal.ErrPlaceholderAuthSecret)
}

func TestSampleConfig_RequiresApiKey(t *testing.T) {
	p, err := provider.NewJsonProvider("../config/config.sample.json")
	require.NoError(t, err)
	cfg := internal.NewConfig()
	require.NoError(t, p.Get(cfg))

	t.Setenv("ZIPREPORT_API_KEY", "")
	cfg.ApplyEnvOverrides()
	assert.ErrorIs(t, cfg.Validate(), internal.ErrEmptyAuthSecret)

	t.Setenv("ZIPREPORT_API_KEY", strongSecret)
	cfg.ApplyEnvOverrides()
	assert.NoError(t, cfg.Validate())
}

func TestNewApiServer_RejectsEmptySecret(t *testing.T) {
	logger := log.New("test")
	cfg := &apiserver.ApiServerConfig{
		ServerConfig:    *httpserver.NewServerConfig(),
		AuthTokenSecret: testAuthToken,
	}
	cfg.Host = "localhost"
	cfg.Port = 0

	srv, err := apiserver.NewApiServer(cfg, nil, sharedMetrics, logger)
	require.NoError(t, err)
	require.NotNil(t, srv)

	cfg.AuthTokenSecret = ""
	srv, err = apiserver.NewApiServer(cfg, nil, sharedMetrics, logger)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authTokenSecret")
	assert.Nil(t, srv)
}

func TestZipReportConfig_ServerTimeouts(t *testing.T) {
	c := internal.NewZipReportConfig()
	c.ReadTimeoutSeconds = 7
	c.WriteTimeoutSeconds = 8
	r, w := c.ServerTimeouts()
	assert.Equal(t, 7*time.Second, r)
	assert.Equal(t, 8*time.Second, w)
}

func loadConfigJSON(t *testing.T, body string) *internal.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	cfg := internal.NewConfig()
	p, err := provider.NewJsonProvider(path)
	require.NoError(t, err)
	require.NoError(t, p.Get(cfg))
	return cfg
}

func TestConfig_CompleteReferenceMatchesDefaults(t *testing.T) {
	dump, err := internal.NewConfig().DumpDefaults()
	require.NoError(t, err)
	raw, err := os.ReadFile("../config/config.complete.json")
	require.NoError(t, err)
	var want, got map[string]any
	require.NoError(t, json.Unmarshal([]byte(dump), &want))
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, want, got)
}

func TestConfig_APITimeoutDefaults(t *testing.T) {
	cfg := loadConfigJSON(t, `{"apiServer":{"port":6543}}`)
	assert.Equal(t, render.JobMaxTimeout+120, cfg.ApiServer.WriteTimeout)
	assert.GreaterOrEqual(t, cfg.ApiServer.WriteTimeout-render.JobMaxTimeout, 120)
	assert.Equal(t, 600, cfg.ApiServer.ReadTimeout)
}

func TestConfig_APITimeoutZeroNormalised(t *testing.T) {
	cfg := loadConfigJSON(t, `{"apiServer":{"port":6543,"readTimeout":0,"writeTimeout":0,"authTokenSecret":"`+strongSecret+`"}}`)
	require.NoError(t, cfg.Validate())
	assert.Equal(t, 600, cfg.ApiServer.ReadTimeout)
	assert.Equal(t, render.JobMaxTimeout+120, cfg.ApiServer.WriteTimeout)
}

func TestNewApiServer_ConnectionTimeouts(t *testing.T) {
	cfg := internal.NewConfig().ApiServer
	cfg.AuthTokenSecret = strongSecret
	cfg.Host = "localhost"
	cfg.Port = 0
	srv, err := apiserver.NewApiServer(cfg, nil, sharedMetrics, log.New("test"))
	require.NoError(t, err)
	assert.Equal(t, 15*time.Second, srv.Server.ReadHeaderTimeout)
	assert.Equal(t, 60*time.Second, srv.Server.IdleTimeout)
}

func TestConfigureEngine_AppliesZipReportSettings(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser-based test in short mode")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := render.NewEngine(ctx, 1, 31800, sharedMetrics, nil)
	defer engine.Shutdown()

	cfg := configWithSecret(strongSecret)
	cfg.ZipReport.ReadTimeoutSeconds = 7
	cfg.ZipReport.WriteTimeoutSeconds = 8
	cfg.ZipReport.EnableHttpDebugging = true
	cfg.ZipReport.EnableMetrics = true
	cfg.Prometheus.Port = 2299
	cfg.ApiServer.Port = 6599
	cfg.ZipReport.EgressAllow = []string{"192.0.2.10:443"}

	require.NoError(t, internal.ConfigureEngine(engine, cfg))
	assert.Equal(t, 7*time.Second, engine.ServerPool.ReadTimeout)
	assert.Equal(t, 8*time.Second, engine.ServerPool.WriteTimeout)
	assert.True(t, engine.ServerPool.HttpDebug)
	assert.Contains(t, engine.EgressPolicy().ReservedPorts, 6599)
	assert.Contains(t, engine.EgressPolicy().ReservedPorts, 2299)
	assert.Len(t, engine.EgressPolicy().Allow, 1)

	cfg.ZipReport.EgressAllow = []string{"not a rule"}
	assert.Error(t, internal.ConfigureEngine(engine, cfg))
}

func TestPrometheusConfig_ValidateEndpointAndPort(t *testing.T) {
	cases := []struct {
		name     string
		endpoint string
		port     int
		ok       bool
	}{
		{"default", "/metrics", 2220, true},
		{"empty_endpoint", "", 2220, false},
		{"relative_endpoint", "metrics", 2220, false},
		{"bad_wildcard_endpoint", "/metrics{", 2220, false},
		{"port_too_high", "/metrics", 70000, false},
		{"port_max", "/metrics", 65535, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := internal.NewConfig().Prometheus
			cfg.Endpoint = tc.endpoint
			cfg.Port = tc.port
			if tc.ok {
				assert.NoError(t, cfg.Validate())
			} else {
				assert.Error(t, cfg.Validate())
			}
		})
	}
}

func TestConfig_PortRanges(t *testing.T) {
	base := func() *internal.Config {
		cfg := configWithSecret(strongSecret)
		cfg.ZipReport.BaseHttpPort = 22000
		cfg.ZipReport.Concurrency = 8
		cfg.ZipReport.EnableMetrics = true
		return cfg
	}
	require.NoError(t, base().Validate())

	t.Run("range_overflow", func(t *testing.T) {
		cfg := base()
		cfg.ZipReport.BaseHttpPort = 65530
		cfg.ZipReport.Concurrency = 8
		assert.Error(t, cfg.Validate())
	})
	t.Run("range_ends_at_65535", func(t *testing.T) {
		cfg := base()
		cfg.ZipReport.BaseHttpPort = 65528
		cfg.ZipReport.Concurrency = 8
		assert.NoError(t, cfg.Validate())
	})
	t.Run("api_in_range", func(t *testing.T) {
		cfg := base()
		cfg.ApiServer.Port = 22003
		assert.Error(t, cfg.Validate())
	})
	t.Run("metrics_in_range", func(t *testing.T) {
		cfg := base()
		cfg.Prometheus.Port = 22007
		assert.Error(t, cfg.Validate())
	})
	t.Run("equal_ports", func(t *testing.T) {
		cfg := base()
		cfg.Prometheus.Port = cfg.ApiServer.Port
		assert.Error(t, cfg.Validate())
	})
	t.Run("metrics_in_range_but_disabled", func(t *testing.T) {
		cfg := base()
		cfg.ZipReport.EnableMetrics = false
		cfg.Prometheus.Port = 22003
		assert.NoError(t, cfg.Validate())
	})
}

func TestNewApiServer_ForwardedHeadersNeedTrustedProxies(t *testing.T) {
	clientIP := func(t *testing.T, trusted []string, header, value string) string {
		cfg := &apiserver.ApiServerConfig{
			ServerConfig:    *httpserver.NewServerConfig(),
			AuthTokenHeader: "X-Auth-Key",
			AuthTokenSecret: testAuthToken,
		}
		cfg.Host = "localhost"
		cfg.Port = 0
		cfg.TrustedProxies = trusted
		srv, err := apiserver.NewApiServer(cfg, nil, sharedMetrics, log.New("test"))
		require.NoError(t, err)
		srv.Router.GET("/ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })

		req := httptest.NewRequest(http.MethodGet, "/ip", nil)
		req.RemoteAddr = "192.0.2.1:1234"
		req.Header.Set("X-Auth-Key", testAuthToken)
		req.Header.Set(header, value)
		w := httptest.NewRecorder()
		srv.Router.ServeHTTP(w, req)
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}

	assert.Equal(t, "192.0.2.1", clientIP(t, nil, "X-Forwarded-For", "203.0.113.9"))
	assert.Equal(t, "192.0.2.1", clientIP(t, nil, "X-Real-IP", "203.0.113.9"))
	assert.Equal(t, "203.0.113.9", clientIP(t, []string{"192.0.2.0/24"}, "X-Forwarded-For", "203.0.113.9"))
}

func TestConfig_UpstreamProxyValidation(t *testing.T) {
	cfg := configWithSecret(strongSecret)
	cfg.ZipReport.UpstreamProxy.URL = "http://user:s3cret@proxy.test:3128"
	assert.NoError(t, cfg.Validate())

	cfg.ZipReport.UpstreamProxy.URL = "tok3n:s3cret@proxy.test:3128"
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "zipReport.upstreamProxy")
	assert.NotContains(t, err.Error(), "s3cret")
	assert.NotContains(t, err.Error(), "tok3n")

	cfg = configWithSecret(strongSecret)
	cfg.ZipReport.UpstreamProxy.URL = "http://127.0.0.1:3128"
	assert.NoError(t, cfg.Validate(), "loopback upstream outside every reserved port")

	cfg.ZipReport.UpstreamProxy.URL = fmt.Sprintf("http://127.0.0.1:%d", cfg.ZipReport.BaseHttpPort+1)
	cfg.ZipReport.Concurrency = 4
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")

	cfg.ZipReport.UpstreamProxy.URL = fmt.Sprintf("http://localhost:%d", cfg.ApiServer.Port)
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")

	cfg.ZipReport.EnableMetrics = true
	cfg.ZipReport.UpstreamProxy.URL = fmt.Sprintf("http://127.0.0.1:%d", cfg.Prometheus.Port)
	err = cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides")

	cfg.ZipReport.UpstreamProxy.URL = fmt.Sprintf("http://proxy.test:%d", cfg.Prometheus.Port)
	assert.NoError(t, cfg.Validate(), "a remote upstream may use any port")

	cfg = configWithSecret(strongSecret)
	cfg.ZipReport.Concurrency = 4
	first, last := cfg.ZipReport.BaseHttpPort, cfg.ZipReport.BaseHttpPort+3
	for _, c := range []struct {
		host    string
		port    int
		metrics bool
		collide bool
	}{
		{"127.0.0.1", first, false, true},
		{"127.0.0.1", last, false, true},
		{"127.0.0.1", last + 1, false, false},
		{"127.0.0.1", first - 1, false, false},
		{"[::1]", cfg.ApiServer.Port, false, true},
		{"0.0.0.0", cfg.ApiServer.Port, false, true},
		{"127.0.0.2", cfg.ApiServer.Port, false, true},
		{"127.0.0.1", cfg.Prometheus.Port, false, false},
		{"127.0.0.1", cfg.Prometheus.Port, true, true},
	} {
		cfg.ZipReport.EnableMetrics = c.metrics
		cfg.ZipReport.UpstreamProxy.URL = fmt.Sprintf("http://%s:%d", c.host, c.port)
		err := cfg.Validate()
		if c.collide {
			assert.ErrorContains(t, err, "collides", cfg.ZipReport.UpstreamProxy.URL)
		} else {
			assert.NoError(t, err, cfg.ZipReport.UpstreamProxy.URL)
		}
	}
}

func TestConfig_UpstreamProxyEnvOverride(t *testing.T) {
	cfg := configWithSecret(strongSecret)
	t.Setenv("ZIPREPORT_UPSTREAM_PROXY", "http://p.test:3128")
	cfg.ApplyEnvOverrides()
	assert.Equal(t, "http://p.test:3128", cfg.ZipReport.UpstreamProxy.URL)
}

func TestConfig_HTTPProxyEnvIgnored(t *testing.T) {
	cfg := configWithSecret(strongSecret)
	t.Setenv("ZIPREPORT_UPSTREAM_PROXY", "") // hermetic: a value exported in the shell would fail this test
	t.Setenv("HTTP_PROXY", "http://env.test:3128")
	t.Setenv("HTTPS_PROXY", "http://env.test:3128")
	t.Setenv("NO_PROXY", "example.com")
	cfg.ApplyEnvOverrides()
	assert.Equal(t, "", cfg.ZipReport.UpstreamProxy.URL)
	assert.Empty(t, cfg.ZipReport.UpstreamProxy.NoProxy)
}

func TestConfigureEngine_UpstreamPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser-based test in short mode")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	engine := render.NewEngine(ctx, 1, 31810, sharedMetrics, nil)
	defer engine.Shutdown()

	cfg := configWithSecret(strongSecret)
	require.NoError(t, internal.ConfigureEngine(engine, cfg))
	assert.Nil(t, engine.EgressPolicy().Upstream, "control: unset by default")

	cfg.ZipReport.UpstreamProxy.URL = "http://p.test:3128"
	cfg.ZipReport.UpstreamProxy.Resolve = "upstream"
	require.NoError(t, internal.ConfigureEngine(engine, cfg))
	up := engine.EgressPolicy().Upstream
	require.NotNil(t, up)
	assert.True(t, up.ByName)
	assert.Equal(t, "p.test:3128", up.URL.Host)
}
