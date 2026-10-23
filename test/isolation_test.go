package test

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"zipreport-server/pkg/egress"
	"zipreport-server/pkg/render"

	"github.com/go-rod/rod/lib/proto"
	"github.com/oddbit-project/blueprint/provider/httpserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const onePixelPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="

func isolationFields(extra map[string]string) map[string]string {
	fields := map[string]string{
		"script":     "index.html",
		"page_size":  "A4",
		"margins":    "standard",
		"js_event":   "true",
		"timeout_js": "15",
	}
	for k, v := range extra {
		fields[k] = v
	}
	return fields
}

// renderPages posts a .zpt (from a path) and returns the PDF page count.
func renderPages(t *testing.T, srv *httpserver.Server, zipPath string, extra map[string]string) int {
	t.Helper()
	req := createMultipartRequest(t, zipPath, isolationFields(extra))
	req.Header.Set("X-Auth-Key", testAuthToken)
	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.Bytes()
	require.True(t, isValidPDF(body), "Response should be a valid PDF")
	return parsePDFPageCount(body)
}

// renderZipPages writes in-memory zip bytes to a temp file and renders them.
func renderZipPages(t *testing.T, srv *httpserver.Server, zipBytes []byte, extra map[string]string) int {
	t.Helper()
	p := filepath.Join(t.TempDir(), "job.zpt")
	require.NoError(t, os.WriteFile(p, zipBytes, 0o600))
	return renderPages(t, srv, p, extra)
}

// buildZipFiles returns an in-memory zip archive containing the given entries.
func buildZipFiles(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		require.NoError(t, err)
		_, err = w.Write(content)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// allowTestServers lets the job proxy reach loopback test servers, by exact host:port. A server whose
// port falls in the engine's pool range would be refused regardless, so that is reported explicitly.
func allowTestServers(t *testing.T, engine *render.Engine, concurrency int, serverURLs ...string) {
	t.Helper()
	var p egress.Policy
	for _, u := range serverURLs {
		pu, err := url.Parse(u)
		require.NoError(t, err)
		port, err := strconv.Atoi(pu.Port())
		require.NoError(t, err)
		require.False(t, port >= engine.ServerPool.BasePort && port < engine.ServerPool.BasePort+concurrency,
			"test server port %d collides with the engine's pool range", port)
		p.Allow = append(p.Allow, mustRule(t, pu.Host))
	}
	engine.SetEgressPolicy(p)
}

func newPNGServer(t *testing.T) *httptest.Server {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString(onePixelPNG)
	require.NoError(t, err)
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_, _ = w.Write(png)
	}))
}

// TestJobIsolation_StorageNotShared verifies that state written by one job is
// invisible to the next job on the same origin and pooled browser.
func TestJobIsolation_StorageNotShared(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser-based isolation test in short mode")
	}
	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()
	_ = ctx

	writer := renderPages(t, srv, filepath.Join("fixtures", "isolation-writer.zpt"), nil)
	require.Equal(t, 2, writer, "writer must have stored all state (positive control)")

	reader := renderPages(t, srv, filepath.Join("fixtures", "isolation-reader.zpt"), nil)
	assert.Equal(t, 2, reader, "reader must see a clean origin: 1=script failed, 3=state leaked from the previous job")
}

func imgPage(imgURL string, delayMs int) []byte {
	return []byte(fmt.Sprintf(`<!DOCTYPE html>
<html><head><meta charset="UTF-8"></head>
<body><div>P</div>
<script>
function add(n) {
  for (var i = 0; i < n; i++) {
    var d = document.createElement('div');
    d.style.pageBreakBefore = 'always';
    d.textContent = 'x';
    document.body.appendChild(d);
  }
}
function go() {
  var img = new Image();
  img.onload = function () { add(1); console.log('zpt-view-ready'); };
  img.onerror = function () { add(2); console.log('zpt-view-ready'); };
  img.src = %q;
}
setTimeout(go, %d);
</script></body></html>`, imgURL, delayMs))
}

// TestJobIsolation_IgnoreSSLErrorsScopedToJob verifies ignore_ssl_errors=true
// works for its own job and does not disable validation for a concurrent job.
func TestJobIsolation_IgnoreSSLErrorsScopedToJob(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser-based isolation test in short mode")
	}
	tlsSrv := newPNGServer(t)
	defer tlsSrv.Close()

	zipX := buildZipFiles(t, map[string][]byte{"index.html": imgPage(tlsSrv.URL+"/px.png?x", 3000)})
	zipY := buildZipFiles(t, map[string][]byte{"index.html": imgPage(tlsSrv.URL+"/px.png?y", 0)})

	srv, engine, ctx, cancel := setupConcurrentServer(t, 2)
	defer cancel()
	defer engine.Shutdown()
	_ = ctx
	allowTestServers(t, engine, 2, tlsSrv.URL)

	pX := filepath.Join(t.TempDir(), "x.zpt")
	require.NoError(t, os.WriteFile(pX, zipX, 0o600))
	reqX := createMultipartRequest(t, pX, isolationFields(map[string]string{"ignore_ssl_errors": "true"}))
	reqX.Header.Set("X-Auth-Key", testAuthToken)
	wX := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Router.ServeHTTP(wX, reqX)
	}()
	time.Sleep(700 * time.Millisecond)
	y := renderZipPages(t, srv, zipY, map[string]string{"ignore_ssl_errors": "false"})
	<-done
	require.Equal(t, http.StatusOK, wX.Code)
	require.True(t, isValidPDF(wX.Body.Bytes()))
	x := parsePDFPageCount(wX.Body.Bytes())

	assert.Equal(t, 2, x, "X ignores cert errors, so its image must load")
	assert.Equal(t, 3, y, "Y validates certificates, so its image must fail even while X is running")
}

// TestJobIsolation_IgnoreSSLErrorsInCrossSiteIframe verifies the cert override
// still reaches a cross-site iframe loaded by the report page.
func TestJobIsolation_IgnoreSSLErrorsInCrossSiteIframe(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser-based isolation test in short mode")
	}
	tlsSrv := newPNGServer(t)
	defer tlsSrv.Close()

	frameHTML := fmt.Sprintf(`<!DOCTYPE html><html><body>
<img src=%q onload="parent.postMessage('ok','*')" onerror="parent.postMessage('err','*')">
</body></html>`, tlsSrv.URL+"/px.png?b2")
	frameSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(frameHTML))
	}))
	defer frameSrv.Close()

	indexHTML := fmt.Sprintf(`<!DOCTYPE html><html><body><div>P</div>
<iframe src=%q></iframe>
<script>
function add(n) {
  for (var i = 0; i < n; i++) {
    var d = document.createElement('div');
    d.style.pageBreakBefore = 'always';
    d.textContent = 'x';
    document.body.appendChild(d);
  }
}
window.addEventListener('message', function (e) {
  if (e.data === 'ok') { add(1); console.log('zpt-view-ready'); }
  else if (e.data === 'err') { add(2); console.log('zpt-view-ready'); }
});
setTimeout(function () { console.log('zpt-view-ready'); }, 10000);
</script></body></html>`, frameSrv.URL+"/frame.html")
	z := buildZipFiles(t, map[string][]byte{"index.html": []byte(indexHTML)})

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()
	_ = ctx
	allowTestServers(t, engine, 1, frameSrv.URL, tlsSrv.URL)

	pages := renderZipPages(t, srv, z, map[string]string{"ignore_ssl_errors": "true"})
	assert.Equal(t, 2, pages, "cert override must cover cross-site iframes")
}

// TestJobIsolation_ContextsDisposed verifies no per-job browser context
// outlives its job.
func TestJobIsolation_ContextsDisposed(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser-based isolation test in short mode")
	}
	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()
	_ = ctx

	for i := 0; i < 3; i++ {
		pages := renderPages(t, srv, filepath.Join("fixtures", "test.zpt"), map[string]string{
			"script":   "test.html",
			"js_event": "false",
		})
		require.GreaterOrEqual(t, pages, 1)
	}

	b, err := engine.GetBrowser()
	require.NoError(t, err)
	defer engine.BrowserPool.Put(b)

	res, err := proto.TargetGetBrowserContexts{}.Call(b)
	require.NoError(t, err)
	before := len(res.BrowserContextIDs)

	// positive control: the query must be able to see contexts
	inc, err := b.Incognito()
	require.NoError(t, err)
	res, err = proto.TargetGetBrowserContexts{}.Call(b)
	require.NoError(t, err)
	require.Len(t, res.BrowserContextIDs, before+1)
	require.NoError(t, inc.Close())

	assert.Equal(t, 0, before, "every job's browser context must be disposed")
}
