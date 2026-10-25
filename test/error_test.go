package test

import (
	"archive/zip"
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"zipreport-server/internal/apiserver"
)

// TestError_CorruptZip tests that a corrupt ZIP file returns 400
func TestError_CorruptZip(t *testing.T) {
	srv := setupAPIServer(t)

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

// TestError_ZeroTimeoutRendersWithinOneSecondBudget tests that timeout_job=0, clamped to 1s, still renders a trivial report
func TestError_ZeroTimeoutRendersWithinOneSecondBudget(t *testing.T) {
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

	// timeout_job=0 is clamped to 1s; the trivial report renders within it
	assert.Equal(t, http.StatusOK, w.Code,
		"Zero timeout is clamped to 1s and a trivial report still renders")
	assert.True(t, isValidPDF(w.Body.Bytes()), "Response should be a valid PDF")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestError_EmptyUpload tests that an empty file upload returns 400
func TestError_EmptyUpload(t *testing.T) {
	srv := setupAPIServer(t)

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
}

// TestError_NoFileField tests that a request without the report field returns 400
func TestError_NoFileField(t *testing.T) {
	srv := setupAPIServer(t)

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
}

// TestRender_ClosesUploadedFile tests that the uploaded report file handle is
// closed when the request is rejected after the file was opened
func TestRender_ClosesUploadedFile(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("needs /proc")
	}
	// disable GC so a finalizer cannot close a leaked fd
	defer debug.SetGCPercent(debug.SetGCPercent(-1))

	zipPath := filepath.Join(t.TempDir(), "big.zpt")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	w0, err := zw.CreateHeader(&zip.FileHeader{Name: "report.html", Method: zip.Store})
	require.NoError(t, err)
	_, err = w0.Write(make([]byte, 40<<20))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())

	// control: the upload must exceed gin's default 32 MiB MaxMultipartMemory so
	// mime/multipart spills it to a "multipart-*" temp file (formdata.go os.CreateTemp)
	st, err := os.Stat(zipPath)
	require.NoError(t, err)
	require.Greater(t, st.Size(), int64(32<<20))

	srv := setupAPIServer(t)

	req := createMultipartRequest(t, zipPath, map[string]string{
		"page_size": "Bogus",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)
	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)

	assertNoMultipartFd(t)
}

// assertNoMultipartFd fails if any open fd points at a mime/multipart temp file
// (formdata.go os.CreateTemp(tempDir, "multipart-"))
func assertNoMultipartFd(t *testing.T) {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	require.NoError(t, err)
	for _, e := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", e.Name()))
		if err != nil {
			continue // fd closed since the listing (e.g. the ReadDir handle itself)
		}
		assert.NotContains(t, target, "multipart-", "multipart temp file left open")
	}
}

func TestRender_ClosesUploadedFileAfterRender(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser render test in short mode")
	}
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("needs /proc")
	}
	// disable GC so a finalizer cannot close a leaked fd
	defer debug.SetGCPercent(debug.SetGCPercent(-1))

	zipPath := filepath.Join(t.TempDir(), "big.zpt")
	f, err := os.Create(zipPath)
	require.NoError(t, err)
	zw := zip.NewWriter(f)
	w0, err := zw.Create("report.html")
	require.NoError(t, err)
	_, err = w0.Write([]byte("<html><body><p>ok</p></body></html>"))
	require.NoError(t, err)
	w1, err := zw.CreateHeader(&zip.FileHeader{Name: "padding.bin", Method: zip.Store})
	require.NoError(t, err)
	_, err = w1.Write(make([]byte, 40<<20))
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	require.NoError(t, f.Close())

	st, err := os.Stat(zipPath)
	require.NoError(t, err)
	require.Greater(t, st.Size(), int64(32<<20)) // spills to a multipart temp file

	srv, engine, _, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	req := createMultipartRequest(t, zipPath, map[string]string{
		"page_size": "A4",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)
	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	assertNoMultipartFd(t)
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// TestRenderEndpoint_OversizedUploadRejected tests that a request body over MaxUploadBytes is rejected
func TestRenderEndpoint_OversizedUploadRejected(t *testing.T) {
	srv := setupAPIServer(t)

	pr, pw := io.Pipe()
	defer func() { _ = pr.Close() }()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			if err := mw.WriteField("page_size", "A4"); err != nil {
				return err
			}
			if err := mw.WriteField("margins", "standard"); err != nil {
				return err
			}
			part, err := mw.CreateFormFile("report", "test.zpt")
			if err != nil {
				return err
			}
			f, err := os.Open(filepath.Join("fixtures", "test.zpt"))
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			if _, err := io.Copy(part, f); err != nil {
				return err
			}
			pad, err := mw.CreateFormFile("padding", "padding.bin")
			if err != nil {
				return err
			}
			if _, err := io.Copy(pad, io.LimitReader(zeroReader{}, apiserver.MaxUploadBytes+1)); err != nil {
				return err
			}
			return mw.Close()
		}()
		_ = pw.CloseWithError(err)
	}()

	req := httptest.NewRequest("POST", "/v2/render", pr)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code, "body over MaxUploadBytes must be rejected")
}
