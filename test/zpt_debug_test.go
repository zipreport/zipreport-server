package test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
	"zipreport-server/internal"
	"zipreport-server/pkg/render"
	"zipreport-server/pkg/zpt"

	"github.com/oddbit-project/blueprint/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func debugLogger(t *testing.T) (*log.Logger, *bytes.Buffer) {
	cfg := log.NewDefaultConfig()
	cfg.Level = "info"
	require.NoError(t, log.Configure(cfg))
	t.Cleanup(func() {
		c := log.NewDefaultConfig()
		c.Level = "error"
		_ = log.Configure(c)
	})
	var buf bytes.Buffer
	return log.New("test-httpdebug").WithOutput(&buf), &buf
}

func debugEntries(t *testing.T, buf *bytes.Buffer, message string) []map[string]any {
	var out []map[string]any
	for _, line := range strings.Split(buf.String(), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &m))
		if m["message"] == message {
			out = append(out, m)
		}
	}
	return out
}

func debugReader(t *testing.T) (*zpt.ZptReader, int64) {
	reader, err := zpt.NewZptReaderFromFile("fixtures/test.zpt")
	require.NoError(t, err)
	f, err := reader.Reader.Open("test.html")
	require.NoError(t, err)
	st, err := f.Stat()
	require.NoError(t, err)
	return reader, st.Size()
}

func debugGet(server *zpt.ZptServer, uri, host string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", uri, nil)
	req.Host = host
	w := httptest.NewRecorder()
	server.ServeHTTP(w, req)
	return w
}

func TestZptServer_HttpDebugLogsRequests(t *testing.T) {
	logger, buf := debugLogger(t)
	reader, want := debugReader(t)
	server := zpt.NewZptServer(reader, 31100, logger)
	server.HttpDebug = true
	addr := server.Server.Addr

	debugGet(server, "/test.html?secret=1", addr)
	debugGet(server, "/missing.html", addr)
	debugGet(server, "/test.html", "127.0.0.1:31160")
	debugGet(server, "/../test.html", addr)

	entries := debugEntries(t, buf, "zpt request")
	require.Len(t, entries, 4)
	statuses := []float64{200, 404, 421, 403}
	paths := []string{"/test.html", "/missing.html", "/test.html", "/../test.html"}
	for i, e := range entries {
		assert.Equal(t, statuses[i], e["status"], "entry %d", i)
		assert.Equal(t, "GET", e["method"])
		assert.Equal(t, paths[i], e["path"])
		assert.Equal(t, true, e["completed"])
		assert.Equal(t, false, e["writeError"])
		assert.Equal(t, addr, e["address"])
	}
	assert.Equal(t, float64(want), entries[0]["bytes"])
	assert.Equal(t, strconv.FormatInt(want, 10), entries[0]["contentLength"])
	assert.False(t, strings.Contains(buf.String(), "secret=1"))
}

func TestZptServer_HttpDebugOff(t *testing.T) {
	logger, buf := debugLogger(t)
	reader, _ := debugReader(t)
	server := zpt.NewZptServer(reader, 31100, logger)
	debugGet(server, "/test.html", server.Server.Addr)
	assert.Len(t, debugEntries(t, buf, "zpt request"), 0)
}

func TestZptServer_HttpDebugTruncates(t *testing.T) {
	logger, buf := debugLogger(t)
	reader, _ := debugReader(t)
	server := zpt.NewZptServer(reader, 31100, logger)
	server.HttpDebug = true

	req := httptest.NewRequest("GET", "/", nil)
	req.URL.Path = "/" + strings.Repeat("é", 300)
	req.Method = strings.Repeat("X", 40)
	req.Host = server.Server.Addr
	server.ServeHTTP(httptest.NewRecorder(), req)

	entries := debugEntries(t, buf, "zpt request")
	require.Len(t, entries, 1)
	const marker = "…[truncated]"
	path := entries[0]["path"].(string)
	assert.True(t, utf8.ValidString(path))
	assert.True(t, strings.HasSuffix(path, marker))
	assert.LessOrEqual(t, len(path), 256+len(marker))
	method := entries[0]["method"].(string)
	assert.True(t, strings.HasSuffix(method, marker))
	assert.LessOrEqual(t, len(method), 16+len(marker))
}

func TestZptServer_HttpDebugCapped(t *testing.T) {
	logger, buf := debugLogger(t)
	reader, _ := debugReader(t)
	server := zpt.NewZptServer(reader, 31100, logger)
	server.HttpDebug = true
	for i := 0; i < zpt.MaxDebugLogLines+5; i++ {
		debugGet(server, "/missing.html", server.Server.Addr)
	}
	require.NoError(t, server.Shutdown(context.Background()))

	assert.Len(t, debugEntries(t, buf, "zpt request"), zpt.MaxDebugLogLines)
	sum := debugEntries(t, buf, "zpt requests not logged")
	require.Len(t, sum, 1)
	assert.Equal(t, float64(5), sum[0]["suppressed"])
}

func TestZptServer_HttpDebugLogsPanics(t *testing.T) {
	logger, buf := debugLogger(t)
	server := zpt.NewZptServer(nil, 31161, logger)
	server.HttpDebug = true
	req := httptest.NewRequest("GET", "/test.html", nil)
	req.Host = server.Server.Addr
	w := httptest.NewRecorder()
	assert.Panics(t, func() { server.ServeHTTP(w, req) })

	entries := debugEntries(t, buf, "zpt request")
	require.Len(t, entries, 1)
	assert.Equal(t, false, entries[0]["completed"])
}

func TestServerPool_PropagatesHttpDebug(t *testing.T) {
	logger, _ := debugLogger(t)
	reader, _ := debugReader(t)
	pool := zpt.NewServerPool(1, 31200, sharedMetrics, logger)
	pool.HttpDebug = true
	srv := pool.BuildServer(reader)
	require.NotNil(t, srv)
	defer pool.RemoveServer(srv)
	assert.True(t, srv.HttpDebug)

	resp, err := http.Get("http://" + srv.Server.Addr + "/test.html")
	require.NoError(t, err)
	_ = resp.Body.Close()
	assert.Equal(t, 200, resp.StatusCode)
}

func TestHttpDebugSuppressedAt(t *testing.T) {
	assert.False(t, internal.HttpDebugSuppressedAt("debug"))
	assert.False(t, internal.HttpDebugSuppressedAt("info"))
	assert.True(t, internal.HttpDebugSuppressedAt("warn"))
	assert.True(t, internal.HttpDebugSuppressedAt("error"))
	assert.False(t, internal.HttpDebugSuppressedAt("bogus"))
}

func TestEngine_EnableHttpDebugging(t *testing.T) {
	if testing.Short() {
		t.Skip("launches Chrome in CI/Docker")
	}
	logger, _ := debugLogger(t)
	engine := render.NewEngine(context.Background(), 1, 31210, sharedMetrics, logger)
	defer engine.Shutdown()
	engine.EnableHttpDebugging()
	assert.True(t, engine.ServerPool.HttpDebug)
}
