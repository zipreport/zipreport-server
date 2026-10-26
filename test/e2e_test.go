package test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"zipreport-server/pkg/render"
	"zipreport-server/pkg/zpt"
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

// TestE2E_SubresourceServedThroughHostCheck proves Chrome's subresource requests
// carry the Host the ephemeral server accepts: the ready signal comes from a
// script loaded as a subresource, so a 421 would stall until timeout_js.
func TestE2E_SubresourceServedThroughHostCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping Chrome-backed test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipBytes := buildZipFiles(t, map[string][]byte{
		"report.html": []byte(`<html><body><p>hello</p><script src="ready.js"></script></body></html>`),
		"ready.js":    []byte(`setTimeout(function(){ console.log("zpt-view-ready"); }, 1000);`),
	})
	zipPath := filepath.Join(t.TempDir(), "subresource.zpt")
	require.NoError(t, os.WriteFile(zipPath, zipBytes, 0o600))

	req := createMultipartRequest(t, zipPath, map[string]string{
		"page_size":  "A4",
		"margins":    "standard",
		"js_event":   "true",
		"timeout_js": "20",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	start := time.Now()
	srv.Router.ServeHTTP(w, req)
	elapsed := time.Since(start)
	t.Logf("elapsed: %s", elapsed)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, isValidPDF(w.Body.Bytes()))
	assert.GreaterOrEqual(t, elapsed, 1*time.Second)
	assert.Less(t, elapsed, 10*time.Second)
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

	assert.True(t, pdfHasText(body, "Test Report"), "rendered PDF should contain the <h1> text")
	assert.True(t, pdfHasText(body, "This is a test PDF generation."), "rendered PDF should contain the body paragraph")

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

func newInlineJob(t *testing.T, html string) *render.Job {
	t.Helper()
	zipBytes := buildZip(t, "index.html", []byte(html))
	reader, err := zpt.NewZptReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	require.NoError(t, err)
	job := render.NewRenderJob(reader, uuid.New())
	job.IndexFile = "index.html"
	return job
}

// TestE2E_JSEventImmediateSignal tests that a signal logged as early as possible is not missed
func TestE2E_JSEventImmediateSignal(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping JS event test in short mode")
	}

	srv, engine, _, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	zipBytes := buildZip(t, "index.html", []byte(`<html><head><script>console.log("zpt-view-ready")</script></head><body>x</body></html>`))
	zipPath := filepath.Join(t.TempDir(), "r.zpt")
	require.NoError(t, os.WriteFile(zipPath, zipBytes, 0o600))
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":     "index.html",
		"page_size":  "A4",
		"margins":    "standard",
		"js_event":   "true",
		"timeout_js": "20",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	start := time.Now()
	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)
	elapsed := time.Since(start)
	t.Logf("elapsed=%s", elapsed)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.True(t, isValidPDF(w.Body.Bytes()), "Response should be a valid PDF")
	assert.Less(t, elapsed, 10*time.Second)
}

// TestE2E_JSEventRespectsJobTimeout tests that the JS wait stops at the job deadline
func TestE2E_JSEventRespectsJobTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping job timeout test in short mode")
	}

	_, engine, _, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	job := newInlineJob(t, `<html><body>never signals</body></html>`)
	job.UseJSEvent = true
	job.JobTimeoutS = 3
	job.JsTimeoutS = 60

	start := time.Now()
	result := engine.RenderJob(job)
	elapsed := time.Since(start)
	t.Logf("elapsed=%s", elapsed)

	assert.False(t, result.Success)
	assert.True(t, errors.Is(result.Error, render.ErrJobTimeout), "error: %v", result.Error)
	assert.Greater(t, elapsed, 2*time.Second)
	assert.Less(t, elapsed, 15*time.Second)
}

// TestE2E_SettlingRespectsJobTimeout tests that the settling wait stops at the job deadline
func TestE2E_SettlingRespectsJobTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping job timeout test in short mode")
	}

	_, engine, _, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	job := newInlineJob(t, `<html><body>plain</body></html>`)
	job.JobTimeoutS = 2
	job.JobSettlingTimeMs = 30000

	start := time.Now()
	result := engine.RenderJob(job)
	elapsed := time.Since(start)
	t.Logf("elapsed=%s", elapsed)

	assert.False(t, result.Success)
	assert.True(t, errors.Is(result.Error, render.ErrJobTimeout), "error: %v", result.Error)
	assert.Greater(t, elapsed, 1*time.Second)
	assert.Less(t, elapsed, 12*time.Second)
}

// TestRenderJob_DeadlineCoversSlotWait tests that time queued for a render slot counts against timeout_job
func TestRenderJob_DeadlineCoversSlotWait(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}

	_, engine, _, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	jobA := newInlineJob(t, `<html><body>never signals</body></html>`)
	jobA.UseJSEvent = true
	jobA.JobTimeoutS = 8
	aDone := make(chan *render.JobResult, 1)
	go func() { aDone <- engine.RenderJob(jobA) }()
	time.Sleep(500 * time.Millisecond)

	jobB := newInlineJob(t, `<html><body>ready</body></html>`)
	jobB.JobTimeoutS = 2
	start := time.Now()
	resB := engine.RenderJob(jobB)
	elapsed := time.Since(start)
	t.Logf("B elapsed=%s", elapsed)

	assert.False(t, resB.Success)
	assert.True(t, errors.Is(resB.Error, render.ErrJobTimeout), "error: %v", resB.Error)
	assert.Less(t, elapsed, 4*time.Second)

	<-aDone
}

// TestRenderJob_CancelledRequestFreesSlot tests that a cancelled request context cancels its job
func TestRenderJob_CancelledRequestFreesSlot(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}

	_, engine, _, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()
	time.AfterFunc(1*time.Second, reqCancel)

	jobA := newInlineJob(t, `<html><body>never signals</body></html>`)
	jobA.UseJSEvent = true
	jobA.JobTimeoutS = 30
	jobA.Ctx = reqCtx
	start := time.Now()
	aDone := make(chan *render.JobResult, 1)
	go func() { aDone <- engine.RenderJob(jobA) }()

	var resA *render.JobResult
	select {
	case resA = <-aDone:
	case <-time.After(40 * time.Second):
		t.Fatal("job A still running long after its request was cancelled")
	}
	aElapsed := time.Since(start)
	t.Logf("A elapsed=%s", aElapsed)
	assert.False(t, resA.Success)
	assert.True(t, errors.Is(resA.Error, render.ErrJobCancelled), "error: %v", resA.Error)
	assert.Less(t, aElapsed, 3*time.Second)

	jobB := newInlineJob(t, `<html><body>ready</body></html>`)
	jobB.JobTimeoutS = 10
	resB := engine.RenderJob(jobB)
	bElapsed := time.Since(start)
	t.Logf("B done at %s", bElapsed)
	assert.True(t, resB.Success, "error: %v", resB.Error)
	assert.Less(t, bElapsed, 6*time.Second)

	b, err := engine.GetBrowser()
	require.NoError(t, err)
	defer engine.BrowserPool.Put(b)
	res, err := proto.TargetGetBrowserContexts{}.Call(b)
	require.NoError(t, err)
	assert.Empty(t, res.BrowserContextIDs, "cancelled job's browser context must be disposed")
}
