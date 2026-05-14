package test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestError_CorruptZip tests that a corrupt ZIP file returns 400
func TestError_CorruptZip(t *testing.T) {
	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "corrupt.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"page_size": "A4",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code,
		"Corrupt ZIP should return 400 Bad Request")
	_ = ctx
}

// TestError_MissingIndexFile tests that a ZIP missing the default index returns 500
func TestError_MissingIndexFile(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping missing index test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "missing-index.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"page_size": "A4",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	// Navigation to non-existent report.html should cause a server error
	assert.Equal(t, http.StatusInternalServerError, w.Code,
		"Missing index file should return 500")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestError_ZeroTimeout tests that timeout_job=0 uses the default and render succeeds
func TestError_ZeroTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping zero timeout test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":      "test.html",
		"page_size":   "A4",
		"margins":     "standard",
		"timeout_job": "0",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	// timeout_job=0 should fall back to default timeout, render should succeed
	assert.Equal(t, http.StatusOK, w.Code,
		"Zero timeout should use default and succeed")
	assert.True(t, isValidPDF(w.Body.Bytes()), "Response should be a valid PDF")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestError_EmptyUpload tests that an empty file upload returns 400
func TestError_EmptyUpload(t *testing.T) {
	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	// Create an empty file part
	part, err := writer.CreateFormFile("report", "empty.zpt")
	assert.NoError(t, err)
	// Write zero bytes to the part
	_, _ = part.Write([]byte{})

	require.NoError(t, writer.WriteField("page_size", "A4"))
	require.NoError(t, writer.WriteField("margins", "standard"))
	_ = writer.Close()

	req := httptest.NewRequest("POST", "/v2/render", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code,
		"Empty file upload should return 400 Bad Request")
	_ = ctx
}

// TestError_NoFileField tests that a request without the report field returns 400
func TestError_NoFileField(t *testing.T) {
	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	require.NoError(t, writer.WriteField("page_size", "A4"))
	require.NoError(t, writer.WriteField("margins", "standard"))
	_ = writer.Close()

	req := httptest.NewRequest("POST", "/v2/render", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusBadRequest, w.Code,
		"Missing report field should return 400 Bad Request")
	_ = ctx
}
