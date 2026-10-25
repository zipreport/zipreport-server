package test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	serverBinaryOnce sync.Once
	serverBinaryPath string
	serverBinaryErr  error
)

func buildServerBinary(t *testing.T) string {
	t.Helper()
	serverBinaryOnce.Do(func() {
		dir, err := os.MkdirTemp("", "zipreport-bin")
		if err != nil {
			serverBinaryErr = err
			return
		}
		serverBinaryPath = filepath.Join(dir, "zipreport-server")
		out, err := exec.Command("go", "build", "-buildvcs=false", "-o", serverBinaryPath, "../cmd/zipreport-server").CombinedOutput()
		if err != nil {
			serverBinaryErr = fmt.Errorf("build failed: %w: %s", err, out)
		}
	})
	require.NoError(t, serverBinaryErr)
	return serverBinaryPath
}

func startServer(t *testing.T, level string) (cmd *exec.Cmd, baseURL string, key string, out *bytes.Buffer) {
	t.Helper()
	return startServerWith(t, level, 600, 31000)
}

func startServerWith(t *testing.T, level string, apiWriteTimeout, baseHttpPort int) (cmd *exec.Cmd, baseURL string, key string, out *bytes.Buffer) {
	t.Helper()
	ps := startServerOpts(t, serverOpts{Level: level, WriteTimeout: apiWriteTimeout, BaseHttpPort: baseHttpPort})
	return ps.Cmd, ps.BaseURL, ps.Key, ps.Out
}

// serverOpts customises the config of a spawned server.
type serverOpts struct {
	Level        string
	WriteTimeout int
	BaseHttpPort int
	APIExtra     map[string]any // merged into apiServer
	ZipExtra     map[string]any // merged into zipReport
	Prometheus   map[string]any // sets the prometheus section when non-nil
	TLS          bool           // BaseURL uses https
}

type procServer struct {
	Cmd     *exec.Cmd
	BaseURL string
	Key     string
	Out     *bytes.Buffer
}

func startServerOpts(t *testing.T, o serverOpts) *procServer {
	t.Helper()
	bin := buildServerBinary(t)

	l, err := net.Listen("tcp", "localhost:0")
	require.NoError(t, err)
	port := l.Addr().(*net.TCPAddr).Port
	require.NoError(t, l.Close())

	key := strings.Repeat("ab", 32)
	api := map[string]any{
		"host":                   "localhost",
		"port":                   port,
		"readTimeout":            600,
		"writeTimeout":           o.WriteTimeout,
		"authTokenHeader":        "X-Auth-Key",
		"authTokenSecret":        key,
		"defaultSecurityHeaders": true,
	}
	for k, v := range o.APIExtra {
		api[k] = v
	}
	zr := map[string]any{
		"readTimeoutSeconds":  300,
		"writeTimeoutSeconds": 300,
		"concurrency":         1,
		"baseHttpPort":        o.BaseHttpPort,
		"enableMetrics":       false,
	}
	for k, v := range o.ZipExtra {
		zr[k] = v
	}
	cfg := map[string]any{
		"apiServer": api,
		"zipReport": zr,
		"log": map[string]any{
			"level":   o.Level,
			"format":  "pretty",
			"noColor": true,
		},
	}
	if o.Prometheus != nil {
		cfg["prometheus"] = o.Prometheus
	}
	data, err := json.Marshal(cfg)
	require.NoError(t, err)
	cfgPath := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, os.WriteFile(cfgPath, data, 0o600))

	out := &bytes.Buffer{}
	cmd := exec.Command(bin, "-c", cfgPath)
	cmd.Stdout = out
	cmd.Stderr = out
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	addr := fmt.Sprintf("localhost:%d", port)
	deadline := time.Now().Add(30 * time.Second)
	for {
		c, err := net.Dial("tcp", addr)
		if err == nil {
			_ = c.Close()
			break
		}
		require.True(t, time.Now().Before(deadline), "server did not start listening")
		time.Sleep(100 * time.Millisecond)
	}
	scheme := "http://"
	if o.TLS {
		scheme = "https://"
	}
	return &procServer{Cmd: cmd, BaseURL: scheme + addr, Key: key, Out: out}
}

func postRender(baseURL, key, fixture string, fields map[string]string) (*http.Response, error) {
	body := &bytes.Buffer{}
	w := multipart.NewWriter(body)
	f, err := os.Open(fixture)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	part, err := w.CreateFormFile("report", filepath.Base(fixture))
	if err != nil {
		return nil, err
	}
	if _, err = io.Copy(part, f); err != nil {
		return nil, err
	}
	for k, v := range fields {
		if err = w.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	req, err := http.NewRequest("POST", baseURL+"/v2/render", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	req.Header.Set("X-Auth-Key", key)
	client := &http.Client{Timeout: 60 * time.Second}
	return client.Do(req)
}

func TestProcess_GracefulShutdownDrainsRender(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	cmd, baseURL, key, _ := startServer(t, "info")

	type result struct {
		status int
		body   []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := postRender(baseURL, key, "fixtures/js-event-timeout.zpt", map[string]string{
			"script":     "index.html",
			"page_size":  "A4",
			"margins":    "standard",
			"js_event":   "true",
			"timeout_js": "7",
		})
		if err != nil {
			done <- result{err: err}
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, err := io.ReadAll(resp.Body)
		done <- result{status: resp.StatusCode, body: b, err: err}
	}()

	time.Sleep(1 * time.Second)
	require.NoError(t, cmd.Process.Signal(os.Interrupt))

	res := <-done
	require.NoError(t, res.err)
	assert.Equal(t, http.StatusOK, res.status)
	assert.True(t, isValidPDF(res.body))

	waitErr := make(chan error, 1)
	go func() { waitErr <- cmd.Wait() }()
	select {
	case err := <-waitErr:
		assert.NoError(t, err, "server must exit with code 0")
	case <-time.After(15 * time.Second):
		t.Fatal("server did not exit within 15s")
	}
}

func TestProcess_LogLevelFromConfig(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	for _, tc := range []struct {
		level    string
		expected bool
	}{{"debug", true}, {"info", false}} {
		t.Run(tc.level, func(t *testing.T) {
			cmd, baseURL, key, out := startServer(t, tc.level)
			resp, err := postRender(baseURL, key, "fixtures/test.zpt", map[string]string{
				"script":    "test.html",
				"page_size": "A4",
				"margins":   "standard",
			})
			require.NoError(t, err)
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			assert.Equal(t, http.StatusOK, resp.StatusCode)

			require.NoError(t, cmd.Process.Signal(os.Interrupt))
			require.NoError(t, cmd.Wait())
			if tc.expected {
				assert.Contains(t, out.String(), "starting job...")
			} else {
				assert.NotContains(t, out.String(), "starting job...")
			}
		})
	}
}

func TestProcess_WriteDeadlineMeasuredFromUpload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	_, baseURL, key, out := startServerWith(t, "debug", 2, 31700)

	resp, err := postRender(baseURL, key, "fixtures/test.zpt", map[string]string{
		"script":        "test.html",
		"page_size":     "A4",
		"margins":       "standard",
		"js_event":      "false",
		"settling_time": "4000",
		"timeout_job":   "20",
	})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t, strings.HasPrefix(string(body), "%PDF-"))
	assert.NotContains(t, out.String(), "cannot extend write deadline")
}
