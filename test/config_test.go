package test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"zipreport-server/internal"
	"zipreport-server/internal/apiserver"
	"zipreport-server/pkg/render"

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
