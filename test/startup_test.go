package test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartup_ExitsWhenListenerFails(t *testing.T) {
	if testing.Short() {
		t.Skip("starts the real binary, which launches Chrome in CI/Docker")
	}
	bin := buildServerBinary(t)

	cases := []struct {
		name    string
		metrics bool
		msg     string
	}{
		{"api_port_in_use", false, "zipReport API server failed"},
		{"metrics_port_in_use", true, "prometheus metrics server failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			held, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer held.Close()
			heldPort := held.Addr().(*net.TCPAddr).Port

			secret := make([]byte, 20)
			_, err = rand.Read(secret)
			require.NoError(t, err)

			apiPort := heldPort
			zr := map[string]any{"baseHttpPort": 31500, "concurrency": 1}
			cfg := map[string]any{"zipReport": zr}
			if tc.metrics {
				l, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				apiPort = l.Addr().(*net.TCPAddr).Port
				require.NoError(t, l.Close())
				zr["enableMetrics"] = true
				cfg["prometheus"] = map[string]any{"host": "127.0.0.1", "port": heldPort}
			}
			cfg["apiServer"] = map[string]any{
				"host":            "127.0.0.1",
				"port":            apiPort,
				"authTokenSecret": hex.EncodeToString(secret),
			}
			data, err := json.Marshal(cfg)
			require.NoError(t, err)
			cfgPath := filepath.Join(t.TempDir(), "config.json")
			require.NoError(t, os.WriteFile(cfgPath, data, 0o600))

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, bin, "-c", cfgPath)
			cmd.WaitDelay = 5 * time.Second
			out, err := cmd.CombinedOutput()

			require.NoError(t, ctx.Err(), "server kept running with no listener:\n%s", out)
			var ee *exec.ExitError
			require.ErrorAs(t, err, &ee, "output:\n%s", out)
			assert.NotEqual(t, 0, ee.ExitCode(), "output:\n%s", out)
			assert.Contains(t, string(out), "address already in use", "output:\n%s", out)
			assert.Contains(t, string(out), tc.msg, "output:\n%s", out)
		})
	}
}
