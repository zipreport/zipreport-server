package test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"zipreport-server/pkg/browser"
	"zipreport-server/pkg/render"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadURLFor(t *testing.T) {
	const base = "https://storage.googleapis.com/chrome-for-testing-public/156.0.8078.47/"
	cases := []struct{ goos, goarch, want string }{
		{"linux", "amd64", base + "linux64/chrome-linux64.zip"},
		{"linux", "arm64", base + "linux-arm64/chrome-linux-arm64.zip"},
		{"darwin", "amd64", base + "mac-x64/chrome-mac-x64.zip"},
		{"darwin", "arm64", base + "mac-arm64/chrome-mac-arm64.zip"},
		{"windows", "386", base + "win32/chrome-win32.zip"},
		{"windows", "amd64", base + "win64/chrome-win64.zip"},
	}
	for _, c := range cases {
		got, err := browser.DownloadURLFor(c.goos, c.goarch)
		require.NoError(t, err, "%s/%s", c.goos, c.goarch)
		assert.Equal(t, c.want, got, "%s/%s", c.goos, c.goarch)
	}

	_, err := browser.DownloadURLFor("linux", "386")
	assert.Error(t, err)

	assert.Equal(t, "chrome-156.0.8078.47", filepath.Base(browser.BrowserDir()))
}

func TestNewLauncher_SiteIsolationFlags(t *testing.T) {
	l := render.NewLauncher("/nonexistent/chrome")

	v, _ := l.GetFlags("disable-features")
	assert.NotContains(t, v, "site-per-process")
	assert.Contains(t, v, "TranslateUI")
	assert.False(t, l.Has("disable-site-isolation-trials"))
	assert.Equal(t, "/nonexistent/chrome", l.Get(flags.Bin))
}

func TestNewLauncher_NoSandboxInContainers(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("GITHUB_ACTIONS", "")
	t.Setenv("KUBERNETES_SERVICE_HOST", "10.0.0.1")
	assert.True(t, render.NewLauncher("/x").Has(flags.NoSandbox))

	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	_, errDocker := os.Stat("/.dockerenv")
	_, errPodman := os.Stat("/.containerenv")
	if errDocker == nil || errPodman == nil {
		t.Log("running inside a container; skipping the no-container assertion")
		return
	}
	assert.False(t, render.NewLauncher("/x").Has(flags.NoSandbox))
}

func TestBrowser_SiteIsolationEnabled(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser test in short mode")
	}
	require.True(t, browser.IsInstalled(), "pinned browser not installed; run cmd/browser-update")

	child := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "<html><body>child</body></html>")
	}))
	defer child.Close()
	parent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprintf(w, `<html><body><iframe src="%s"></iframe></body></html>`, child.URL)
	}))
	defer parent.Close()

	u := render.NewLauncher(browser.BinPath()).MustLaunch()
	b := rod.New().ControlURL(u).MustConnect()
	defer b.MustClose()

	// localhost and 127.0.0.1 are different sites
	p := b.MustPage(strings.Replace(parent.URL, "127.0.0.1", "localhost", 1))
	p.MustWaitLoad()
	time.Sleep(time.Second)

	res, err := proto.TargetGetTargets{}.Call(b)
	require.NoError(t, err)
	n := 0
	for _, ti := range res.TargetInfos {
		if string(ti.Type) == "iframe" {
			n++
		}
	}
	assert.GreaterOrEqual(t, n, 1, "0 iframe targets means site isolation is off")
}

func TestEngine_LaunchesWithoutIsolationDisabledFlags(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser test in short mode")
	}
	_, engine, _, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	b, err := engine.GetBrowser()
	require.NoError(t, err)
	defer engine.BrowserPool.Put(b)

	res, err := proto.BrowserGetBrowserCommandLine{}.Call(b)
	require.NoError(t, err)
	require.NotEmpty(t, res.Arguments)
	for _, a := range res.Arguments {
		assert.NotContains(t, a, "site-per-process")
		assert.NotContains(t, a, "disable-site-isolation-trials")
	}
}
