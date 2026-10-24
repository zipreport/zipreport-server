package test

import (
	"strings"
	"testing"
	"time"
	"zipreport-server/internal"
	"zipreport-server/internal/apiserver"

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
