package test

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
	"zipreport-server/internal/apiserver"
	"zipreport-server/pkg/monitor"
	"zipreport-server/pkg/render"

	"github.com/oddbit-project/blueprint/log"
	"github.com/oddbit-project/blueprint/provider/httpserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testAuthToken = "test-secret-token"

var (
	// Shared metrics instance to avoid duplicate registration
	sharedMetrics = monitor.NewMetrics()
	// Port counter to avoid conflicts between tests
	portCounter = 22000
)

// setupTestServer creates a test API server instance
func setupTestServer(t *testing.T) (*httpserver.Server, *render.Engine, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)

	// Configure logger
	logConfig := log.NewDefaultConfig()
	logConfig.Level = "error" // Reduce noise in tests
	require.NoError(t, log.Configure(logConfig))
	logger := log.New("test")

	// Use shared metrics to avoid duplicate registration
	metrics := sharedMetrics

	// Create render engine with minimal concurrency for tests
	// Use unique port base for each test to avoid conflicts
	portBase := portCounter
	portCounter += 100
	engine := render.NewEngine(ctx, 1, portBase, metrics, logger)

	// Configure API server
	cfg := &apiserver.ApiServerConfig{
		ServerConfig:           *httpserver.NewServerConfig(),
		AuthTokenHeader:        "X-Auth-Key",
		AuthTokenSecret:        testAuthToken,
		DefaultSecurityHeaders: true,
	}
	cfg.Host = "localhost"
	cfg.Port = 0 // Let OS assign port

	srv, err := apiserver.NewApiServer(cfg, engine, metrics, logger)
	require.NoError(t, err)
	require.NotNil(t, srv)

	return srv, engine, ctx, cancel
}

// setupAPIServer builds the API without a render engine, for tests that never reach
// rendering (auth and request validation); it needs no browser.
func setupAPIServer(t *testing.T) *httpserver.Server {
	logConfig := log.NewDefaultConfig()
	logConfig.Level = "error"
	require.NoError(t, log.Configure(logConfig))
	logger := log.New("test")

	cfg := &apiserver.ApiServerConfig{
		ServerConfig:           *httpserver.NewServerConfig(),
		AuthTokenHeader:        "X-Auth-Key",
		AuthTokenSecret:        testAuthToken,
		DefaultSecurityHeaders: true,
	}
	cfg.Host = "localhost"
	cfg.Port = 0

	srv, err := apiserver.NewApiServer(cfg, nil, sharedMetrics, logger)
	require.NoError(t, err)
	require.NotNil(t, srv)
	return srv
}

// createMultipartRequest creates a multipart form request with a file upload
func createMultipartRequest(t *testing.T, zipPath string, fields map[string]string) *http.Request {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Add the zip file
	file, err := os.Open(zipPath)
	require.NoError(t, err)
	defer func() { _ = file.Close() }()

	part, err := writer.CreateFormFile("report", filepath.Base(zipPath))
	require.NoError(t, err)

	_, err = io.Copy(part, file)
	require.NoError(t, err)

	// Add other form fields
	for key, val := range fields {
		err = writer.WriteField(key, val)
		require.NoError(t, err)
	}

	err = writer.Close()
	require.NoError(t, err)

	req := httptest.NewRequest("POST", "/v2/render", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	return req
}

// TestRenderEndpoint_Success tests successful PDF generation
func TestRenderEndpoint_Success(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "A4",
		"margins":   "standard",
	})

	// Add auth token
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))
	assert.Greater(t, len(w.Body.Bytes()), 100, "PDF should have content")

	// Verify PDF magic bytes
	pdfHeader := w.Body.Bytes()[:4]
	assert.Equal(t, []byte("%PDF"), pdfHeader, "Response should be a valid PDF")

	// Give time for cleanup
	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestRenderEndpoint_MissingAuth tests authentication requirement
func TestRenderEndpoint_MissingAuth(t *testing.T) {
	srv := setupAPIServer(t)

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "A4",
		"margins":   "standard",
	})

	// No auth token provided
	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestRenderEndpoint_InvalidAuth tests invalid authentication token
func TestRenderEndpoint_InvalidAuth(t *testing.T) {
	srv := setupAPIServer(t)

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "A4",
		"margins":   "standard",
	})

	// Invalid auth token
	req.Header.Set("X-Auth-Key", "wrong-token")

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

// TestRenderEndpoint_MissingFile tests missing report file
func TestRenderEndpoint_MissingFile(t *testing.T) {
	srv := setupAPIServer(t)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	require.NoError(t, writer.WriteField("page_size", "A4"))
	_ = writer.Close()

	req := httptest.NewRequest("POST", "/v2/render", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestRenderEndpoint_InvalidPageSize tests invalid page size validation
func TestRenderEndpoint_InvalidPageSize(t *testing.T) {
	srv := setupAPIServer(t)

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "INVALID",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestRenderEndpoint_InvalidMarginStyle tests invalid margin style validation
func TestRenderEndpoint_InvalidMarginStyle(t *testing.T) {
	srv := setupAPIServer(t)

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "A4",
		"margins":   "invalid-margin",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestRenderEndpoint_NegativeMargins tests negative margin validation
func TestRenderEndpoint_NegativeMargins(t *testing.T) {
	srv := setupAPIServer(t)

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":        "test.html",
		"page_size":     "A4",
		"margins":       "custom",
		"margin_left":   "-1.0",
		"margin_right":  "1.0",
		"margin_top":    "1.0",
		"margin_bottom": "1.0",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// TestRenderEndpoint_CustomMargins tests custom margin values
func TestRenderEndpoint_CustomMargins(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":        "test.html",
		"page_size":     "A4",
		"margins":       "custom",
		"margin_left":   "0.5",
		"margin_right":  "0.5",
		"margin_top":    "1.0",
		"margin_bottom": "1.0",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))
	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestRenderEndpoint_LandscapeOrientation tests landscape page orientation
func TestRenderEndpoint_LandscapeOrientation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "A4",
		"margins":   "standard",
		"landscape": "true",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))
	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestRenderEndpoint_NestedFile tests rendering a file in a subdirectory
func TestRenderEndpoint_NestedFile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "subdirectory/nested.html",
		"page_size": "A4",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))
	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestRenderEndpoint_DifferentPageSizes tests various page sizes
func TestRenderEndpoint_DifferentPageSizes(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	pageSizes := []string{"A3", "A4", "A5", "Letter", "Legal", "Tabloid"}

	for _, pageSize := range pageSizes {
		t.Run(pageSize, func(t *testing.T) {
			zipPath := filepath.Join("fixtures", "test.zpt")
			req := createMultipartRequest(t, zipPath, map[string]string{
				"script":    "test.html",
				"page_size": pageSize,
				"margins":   "standard",
			})
			req.Header.Set("X-Auth-Key", testAuthToken)

			w := httptest.NewRecorder()
			srv.Router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))
			time.Sleep(50 * time.Millisecond)
		})
	}
	_ = ctx
}
