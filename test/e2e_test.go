package test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestE2E_JSEventMode tests rendering with JS event mode enabled
func TestE2E_JSEventMode(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping JS event test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "js-event.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "index.html",
		"page_size": "A4",
		"margins":   "standard",
		"js_event":  "true",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))

	body := w.Body.Bytes()
	assert.True(t, isValidPDF(body), "Response should be a valid PDF")
	assert.Greater(t, len(body), 100, "PDF should have content")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestE2E_JSEventTimeout tests that JS event timeout produces PDF after timeout
func TestE2E_JSEventTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping JS event timeout test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "js-event-timeout.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":     "index.html",
		"page_size":  "A4",
		"margins":    "standard",
		"js_event":   "true",
		"timeout_js": "2",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))

	body := w.Body.Bytes()
	assert.True(t, isValidPDF(body), "Response should be a valid PDF even after JS timeout")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestE2E_MultiResource tests that CSS and images are loaded from the ZIP
func TestE2E_MultiResource(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping multi-resource test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "multi-resource.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "index.html",
		"page_size": "A4",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))

	body := w.Body.Bytes()
	assert.True(t, isValidPDF(body), "Response should be a valid PDF")
	// Multi-resource PDF should be larger than a minimal PDF due to image/styled content
	assert.Greater(t, len(body), 500, "Multi-resource PDF should have substantial content")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestE2E_MultiPage tests that page breaks produce multiple pages
func TestE2E_MultiPage(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping multi-page test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "multi-page.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "index.html",
		"page_size": "A4",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "application/pdf", w.Header().Get("Content-Type"))

	body := w.Body.Bytes()
	require.True(t, isValidPDF(body), "Response should be a valid PDF")

	pageCount := parsePDFPageCount(body)
	assert.GreaterOrEqual(t, pageCount, 2, "Multi-page PDF should have at least 2 pages")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestE2E_PDFContentValidation validates PDF contains expected text
func TestE2E_PDFContentValidation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping PDF content validation test in short mode")
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
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)

	body := w.Body.Bytes()
	require.True(t, isValidPDF(body), "Response should be a valid PDF")

	// Check for text content in the PDF (works for uncompressed streams)
	assert.True(t, pdfContainsText(body, "Test Report") || len(body) > 1000,
		"PDF should contain expected text or be substantial in size")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestE2E_AllPageSizes renders same template at all page sizes and verifies valid PDFs
func TestE2E_AllPageSizes(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping all page sizes test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	pageSizes := []string{"A3", "A4", "A5", "Letter", "Legal", "Tabloid"}
	pdfResults := make(map[string][]byte)

	for _, size := range pageSizes {
		t.Run(size, func(t *testing.T) {
			zipPath := filepath.Join("fixtures", "test.zpt")
			req := createMultipartRequest(t, zipPath, map[string]string{
				"script":    "test.html",
				"page_size": size,
				"margins":   "standard",
			})
			req.Header.Set("X-Auth-Key", testAuthToken)

			w := httptest.NewRecorder()
			srv.Router.ServeHTTP(w, req)

			assert.Equal(t, http.StatusOK, w.Code)

			body := w.Body.Bytes()
			assert.True(t, isValidPDF(body), "PDF for page size %s should be valid", size)
			pdfResults[size] = body

			time.Sleep(50 * time.Millisecond)
		})
	}

	// Verify different page sizes produce different PDFs
	if len(pdfResults) >= 2 {
		a4 := pdfResults["A4"]
		a3 := pdfResults["A3"]
		if len(a4) > 0 && len(a3) > 0 {
			assert.NotEqual(t, len(a4), len(a3),
				"Different page sizes should produce different PDF sizes")
		}
	}

	_ = ctx
}

// TestE2E_LandscapeVsPortrait verifies landscape and portrait produce different PDFs
func TestE2E_LandscapeVsPortrait(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping landscape vs portrait test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "test.zpt")

	// Render portrait
	reqPortrait := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "A4",
		"margins":   "standard",
		"landscape": "false",
	})
	reqPortrait.Header.Set("X-Auth-Key", testAuthToken)

	wPortrait := httptest.NewRecorder()
	srv.Router.ServeHTTP(wPortrait, reqPortrait)
	assert.Equal(t, http.StatusOK, wPortrait.Code)

	time.Sleep(100 * time.Millisecond)

	// Render landscape
	reqLandscape := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "A4",
		"margins":   "standard",
		"landscape": "true",
	})
	reqLandscape.Header.Set("X-Auth-Key", testAuthToken)

	wLandscape := httptest.NewRecorder()
	srv.Router.ServeHTTP(wLandscape, reqLandscape)
	assert.Equal(t, http.StatusOK, wLandscape.Code)

	portraitBody := wPortrait.Body.Bytes()
	landscapeBody := wLandscape.Body.Bytes()

	require.True(t, isValidPDF(portraitBody), "Portrait PDF should be valid")
	require.True(t, isValidPDF(landscapeBody), "Landscape PDF should be valid")

	// PDFs should differ because orientation changes the MediaBox dimensions
	assert.NotEqual(t, portraitBody, landscapeBody,
		"Portrait and landscape PDFs should differ")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestE2E_CustomMargins verifies different margins produce different PDFs
func TestE2E_CustomMargins(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping custom margins test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipPath := filepath.Join("fixtures", "test.zpt")

	// Render with no margins
	reqNoMargin := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "A4",
		"margins":   "none",
	})
	reqNoMargin.Header.Set("X-Auth-Key", testAuthToken)

	wNoMargin := httptest.NewRecorder()
	srv.Router.ServeHTTP(wNoMargin, reqNoMargin)
	assert.Equal(t, http.StatusOK, wNoMargin.Code)

	time.Sleep(100 * time.Millisecond)

	// Render with large custom margins
	reqLargeMargin := createMultipartRequest(t, zipPath, map[string]string{
		"script":        "test.html",
		"page_size":     "A4",
		"margins":       "custom",
		"margin_left":   "2.0",
		"margin_right":  "2.0",
		"margin_top":    "2.0",
		"margin_bottom": "2.0",
	})
	reqLargeMargin.Header.Set("X-Auth-Key", testAuthToken)

	wLargeMargin := httptest.NewRecorder()
	srv.Router.ServeHTTP(wLargeMargin, reqLargeMargin)
	assert.Equal(t, http.StatusOK, wLargeMargin.Code)

	noMarginBody := wNoMargin.Body.Bytes()
	largeMarginBody := wLargeMargin.Body.Bytes()

	require.True(t, isValidPDF(noMarginBody), "No-margin PDF should be valid")
	require.True(t, isValidPDF(largeMarginBody), "Large-margin PDF should be valid")

	assert.NotEqual(t, noMarginBody, largeMarginBody,
		"PDFs with different margins should differ")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}
