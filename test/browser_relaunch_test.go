package test

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sharedLauncherMode mirrors needsNoSandbox in pkg/render/engine.go: when true the engine
// uses one shared no-sandbox Chrome.
func sharedLauncherMode() bool {
	if os.Getenv("CI") == "true" || os.Getenv("GITHUB_ACTIONS") == "true" {
		return true
	}
	for _, f := range []string{"/.dockerenv", "/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			return true
		}
	}
	return os.Getenv("KUBERNETES_SERVICE_HOST") != ""
}

func TestSharedLauncherMode(t *testing.T) {
	for _, k := range []string{"CI", "GITHUB_ACTIONS", "KUBERNETES_SERVICE_HOST"} {
		v := "true"
		if k == "KUBERNETES_SERVICE_HOST" {
			v = "x"
		}
		t.Run(k, func(t *testing.T) {
			for _, o := range []string{"CI", "GITHUB_ACTIONS", "KUBERNETES_SERVICE_HOST"} {
				t.Setenv(o, "")
			}
			t.Setenv(k, v)
			assert.True(t, sharedLauncherMode())
		})
	}
}

// TestRenderJob_RelaunchedSharedBrowserKeepsSiteIsolation tests that the shared Chrome relaunched after a crash
// is not started with the site-isolation-disabling flags
func TestRenderJob_RelaunchedSharedBrowserKeepsSiteIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}
	if !sharedLauncherMode() {
		t.Skip("shared-launcher mode only applies in containers and CI")
	}

	_, engine, _, cancel := setupConcurrentServer(t, 1)
	defer cancel()
	defer engine.Shutdown()

	res := engine.RenderJob(newInlineJob(t, `<html><body>warm</body></html>`))
	require.True(t, res.Success, "initial render: %v", res.Error)

	require.Positive(t, killChrome(t), "no Chrome process found to kill")
	time.Sleep(500 * time.Millisecond)

	// the pooled connection is dead: the first job may fail, a later one must relaunch the shared Chrome
	ok := false
	for i := 0; i < 3 && !ok; i++ {
		ok = engine.RenderJob(newInlineJob(t, `<html><body>after crash</body></html>`)).Success
	}
	require.True(t, ok, "engine must recover by relaunching the shared browser")

	b, err := engine.GetBrowser()
	require.NoError(t, err)
	defer engine.BrowserPool.Put(b)

	cl, err := proto.BrowserGetBrowserCommandLine{}.Call(b)
	require.NoError(t, err)
	require.NotEmpty(t, cl.Arguments)
	for _, a := range cl.Arguments {
		assert.NotContains(t, a, "disable-site-isolation-trials")
		if strings.HasPrefix(a, "--disable-features=") {
			assert.NotContains(t, a, "site-per-process")
		}
	}
}
