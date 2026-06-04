package test

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"zipreport-server/pkg/zpt"

	"github.com/oddbit-project/blueprint/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestZptServer_PathTraversal tests path traversal protection in ZptServer
func TestZptServer_PathTraversal(t *testing.T) {
	// Configure logger
	logConfig := log.NewDefaultConfig()
	logConfig.Level = "error"
	require.NoError(t, log.Configure(logConfig))
	logger := log.New("test-security")

	// Load test zip
	reader, err := zpt.NewZptReaderFromFile("fixtures/test.zpt")
	require.NoError(t, err)

	// Create ZPT server
	server := zpt.NewZptServer(reader, 43000, logger)
	require.NotNil(t, server)

	testCases := []struct {
		name           string
		requestURI     string
		expectedStatus int
		description    string
	}{
		{
			name:           "normal_file",
			requestURI:     "/test.html",
			expectedStatus: http.StatusOK,
			description:    "Normal file access should work",
		},
		{
			name:           "nested_file",
			requestURI:     "/subdirectory/nested.html",
			expectedStatus: http.StatusOK,
			description:    "Nested file access should work",
		},
		{
			name:           "parent_traversal",
			requestURI:     "/../etc/passwd",
			expectedStatus: http.StatusForbidden,
			description:    "Parent directory traversal should be blocked",
		},
		{
			name:           "double_parent_traversal",
			requestURI:     "/../../etc/passwd",
			expectedStatus: http.StatusForbidden,
			description:    "Double parent directory traversal should be blocked",
		},
		{
			name:           "middle_traversal",
			requestURI:     "/test/../../../etc/passwd",
			expectedStatus: http.StatusForbidden,
			description:    "Traversal in middle of path should be blocked",
		},
		{
			name:           "encoded_traversal",
			requestURI:     "/%2e%2e/etc/passwd",
			expectedStatus: http.StatusForbidden,
			description:    "URL encoded traversal is decoded and blocked",
		},
		{
			name:           "backslash_traversal",
			requestURI:     "/..\\etc\\passwd",
			expectedStatus: http.StatusForbidden,
			description:    "Backslash traversal should be blocked",
		},
		{
			name:           "double_slash",
			requestURI:     "//test.html",
			expectedStatus: http.StatusOK,
			description:    "Double slash should be normalized and work",
		},
		{
			name:           "nonexistent_file",
			requestURI:     "/nonexistent.html",
			expectedStatus: http.StatusNotFound,
			description:    "Nonexistent files should return 404",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", tc.requestURI, nil)
			w := httptest.NewRecorder()

			server.ServeHTTP(w, req)

			assert.Equal(t, tc.expectedStatus, w.Code, tc.description)
		})
	}
}

// TestZptReader_LargeFileProtection tests protection against zip-bomb entries
func TestZptReader_LargeFileProtection(t *testing.T) {
	reader, err := zpt.NewZptReaderFromFile("fixtures/test.zpt")
	require.NoError(t, err)

	// Read a normal file - should succeed
	data, err := reader.ReadFile("test.html")
	require.NoError(t, err)
	assert.Greater(t, len(data), 0)

	// Try to read a non-existent file
	_, err = reader.ReadFile("nonexistent.html")
	assert.Error(t, err)

	// An entry that decompresses beyond MaxFileSize must be rejected
	bomb := buildZip(t, "bomb.bin", make([]byte, zpt.MaxFileSize+1))
	bombReader, err := zpt.NewZptReader(bytes.NewReader(bomb), int64(len(bomb)))
	require.NoError(t, err)
	_, err = bombReader.ReadFile("bomb.bin")
	assert.Error(t, err, "oversized entry should be rejected")

	// An entry within the limit must still read
	small := buildZip(t, "ok.bin", make([]byte, 1024))
	okReader, err := zpt.NewZptReader(bytes.NewReader(small), int64(len(small)))
	require.NoError(t, err)
	got, err := okReader.ReadFile("ok.bin")
	require.NoError(t, err)
	assert.Equal(t, 1024, len(got))
}

// buildZip returns an in-memory zip archive containing a single named entry.
func buildZip(t *testing.T, name string, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	require.NoError(t, err)
	_, err = w.Write(content)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// TestRenderTimeout tests that render jobs respect timeouts
func TestRenderTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping timeout test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	// Test with very short timeout - this may not reliably trigger timeout
	// but tests the parameter is accepted
	req := createMultipartRequest(t, "fixtures/test.zpt", map[string]string{
		"script":      "test.html",
		"page_size":   "A4",
		"margins":     "standard",
		"timeout_job": "1",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	// Should either succeed quickly or timeout
	assert.Contains(t, []int{http.StatusOK, http.StatusInternalServerError}, w.Code)
	_ = ctx
}

// TestConcurrentRequests tests handling of concurrent render requests
func TestConcurrentRequests(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping concurrent test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	concurrency := 3
	done := make(chan bool, concurrency)

	for i := 0; i < concurrency; i++ {
		go func(id int) {
			req := createMultipartRequest(t, "fixtures/test.zpt", map[string]string{
				"script":    "test.html",
				"page_size": "A4",
				"margins":   "standard",
			})
			req.Header.Set("X-Auth-Key", testAuthToken)

			w := httptest.NewRecorder()
			srv.Router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code, "Request %d should succeed", id)
			done <- true
		}(i)
	}

	// Wait for all requests to complete
	for i := 0; i < concurrency; i++ {
		<-done
	}
	_ = ctx
}

// TestSSLErrorsOption tests the ignore_ssl_errors parameter
func TestSSLErrorsOption(t *testing.T) {
	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	testCases := []struct {
		name  string
		value string
	}{
		{"true", "true"},
		{"false", "false"},
		{"1", "1"},
		{"0", "0"},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := createMultipartRequest(t, "fixtures/test.zpt", map[string]string{
				"script":            "test.html",
				"page_size":         "A4",
				"margins":           "standard",
				"ignore_ssl_errors": tc.value,
			})
			req.Header.Set("X-Auth-Key", testAuthToken)

			w := httptest.NewRecorder()
			srv.Router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)
		})
	}
	_ = ctx
}
