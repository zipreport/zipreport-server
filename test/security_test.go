package test

import (
	"archive/zip"
	"bytes"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	server := zpt.NewZptServer(reader, 29000, logger)
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
			req.Host = server.Server.Addr
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

// TestSSLErrorsOption only checks that ignore_ssl_errors values parse and still render; certificate behaviour is covered by TestJobIsolation_IgnoreSSLErrors* in isolation_test.go
func TestSSLErrorsOption(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
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

func newTestServer(t *testing.T, reader *zpt.ZptReader) *zpt.ZptServer {
	t.Helper()
	logConfig := log.NewDefaultConfig()
	logConfig.Level = "error"
	require.NoError(t, log.Configure(logConfig))
	return zpt.NewZptServer(reader, 29000, log.New("test-security"))
}

// buildForgedSizeZip returns a tiny zip whose entry declares an uncompressed
// size above zpt.MaxFileSize.
func buildForgedSizeZip(t *testing.T, declared uint64) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.CreateRaw(&zip.FileHeader{
		Name:               "big.bin",
		Method:             zip.Store,
		CompressedSize64:   4,
		UncompressedSize64: declared,
	})
	require.NoError(t, err)
	_, err = w.Write([]byte("abcd"))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

// maxWriteRecorder discards the body and records the largest single Write.
type maxWriteRecorder struct {
	*httptest.ResponseRecorder
	max, total int
}

func (m *maxWriteRecorder) Write(b []byte) (int, error) {
	if len(b) > m.max {
		m.max = len(b)
	}
	m.total += len(b)
	return len(b), nil
}

func TestZptServer_HostCheck(t *testing.T) {
	reader, err := zpt.NewZptReaderFromFile("fixtures/test.zpt")
	require.NoError(t, err)
	server := newTestServer(t, reader)

	testCases := []struct {
		name     string
		host     string
		expected int
	}{
		{"own_host", server.Server.Addr, http.StatusOK},
		{"default_test_host", "", http.StatusMisdirectedRequest},
		{"subdomain_localhost", "a.localhost:29000", http.StatusMisdirectedRequest},
		{"ip_loopback", "127.0.0.1:29000", http.StatusMisdirectedRequest},
		{"wrong_port", "localhost:29001", http.StatusMisdirectedRequest},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/test.html", nil)
			if tc.host != "" {
				req.Host = tc.host
			}
			w := httptest.NewRecorder()
			server.ServeHTTP(w, req)
			assert.Equal(t, tc.expected, w.Code)
		})
	}
}

func TestZptServer_Streaming(t *testing.T) {
	get := func(server *zpt.ZptServer, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		req.Host = server.Server.Addr
		w := httptest.NewRecorder()
		server.ServeHTTP(w, req)
		return w
	}

	t.Run("serves_body_with_length", func(t *testing.T) {
		reader, err := zpt.NewZptReaderFromFile("fixtures/test.zpt")
		require.NoError(t, err)
		want, err := reader.ReadFile("test.html")
		require.NoError(t, err)
		w := get(newTestServer(t, reader), "/test.html")
		assert.Equal(t, http.StatusOK, w.Code)
		assert.Equal(t, want, w.Body.Bytes())
		assert.Equal(t, strconv.Itoa(len(want)), w.Header().Get("Content-Length"))
	})

	// 1<<63 and MaxUint64 overflow int64 in FileInfo.Size() to a negative value.
	for name, declared := range map[string]uint64{
		"refuses_declared_oversize":  zpt.MaxFileSize + 1,
		"refuses_declared_2pow63":    1 << 63,
		"refuses_declared_maxuint64": math.MaxUint64,
	} {
		t.Run(name, func(t *testing.T) {
			b := buildForgedSizeZip(t, declared)
			reader, err := zpt.NewZptReader(bytes.NewReader(b), int64(len(b)))
			require.NoError(t, err)
			w := get(newTestServer(t, reader), "/big.bin")
			assert.Equal(t, http.StatusInternalServerError, w.Code)
			assert.Empty(t, w.Body.Bytes())
		})
	}

	t.Run("missing_entry", func(t *testing.T) {
		reader, err := zpt.NewZptReaderFromFile("fixtures/test.zpt")
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, get(newTestServer(t, reader), "/nope.html").Code)
	})

	t.Run("directory_entry", func(t *testing.T) {
		reader, err := zpt.NewZptReaderFromFile("fixtures/test.zpt")
		require.NoError(t, err)
		assert.Equal(t, http.StatusNotFound, get(newTestServer(t, reader), "/subdirectory").Code)
	})

	t.Run("streams_large_entry", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, err := zw.Create("zeros.bin")
		require.NoError(t, err)
		chunk := make([]byte, 1<<20)
		for i := 0; i < 16; i++ {
			_, err = w.Write(chunk)
			require.NoError(t, err)
		}
		require.NoError(t, zw.Close())
		reader, err := zpt.NewZptReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		require.NoError(t, err)

		server := newTestServer(t, reader)
		req := httptest.NewRequest("GET", "/zeros.bin", nil)
		req.Host = server.Server.Addr
		rec := &maxWriteRecorder{ResponseRecorder: httptest.NewRecorder()}
		server.ServeHTTP(rec, req)
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, "16777216", rec.Header().Get("Content-Length"))
		assert.Equal(t, 16<<20, rec.total)
		assert.LessOrEqual(t, rec.max, 64<<10)
	})
}
