package test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getHistogramSampleCount extracts the sample count from a prometheus Histogram
func getHistogramSampleCount(h prometheus.Histogram) uint64 {
	ch := make(chan prometheus.Metric, 1)
	h.Collect(ch)
	m := <-ch
	var metric dto.Metric
	if err := m.Write(&metric); err != nil {
		return 0
	}
	return metric.GetHistogram().GetSampleCount()
}

// TestMetrics_SuccessCounter verifies SuccessOps is incremented on successful render
func TestMetrics_SuccessCounter(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping metrics success counter test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	before := testutil.ToFloat64(sharedMetrics.SuccessOps)

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "A4",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	after := testutil.ToFloat64(sharedMetrics.SuccessOps)
	assert.Greater(t, after, before, "SuccessOps should be incremented after successful render")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestMetrics_FailureCounter verifies FailedOps is incremented on failed render
func TestMetrics_FailureCounter(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping metrics failure counter test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	before := testutil.ToFloat64(sharedMetrics.FailedOps)

	// Use missing-index.zpt to trigger a render failure (navigation to non-existent file)
	zipPath := filepath.Join("fixtures", "missing-index.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"page_size": "A4",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	assert.Equal(t, http.StatusInternalServerError, w.Code)

	after := testutil.ToFloat64(sharedMetrics.FailedOps)
	assert.Greater(t, after, before, "FailedOps should be incremented after failed render")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestMetrics_TotalCounter verifies TotalOps is incremented on any render attempt
func TestMetrics_TotalCounter(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping metrics total counter test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	before := testutil.ToFloat64(sharedMetrics.TotalOps)

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "A4",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	after := testutil.ToFloat64(sharedMetrics.TotalOps)
	assert.Greater(t, after, before, "TotalOps should be incremented after any render attempt")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestMetrics_ConversionTime verifies ConversionTime histogram records observations
func TestMetrics_ConversionTime(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping metrics conversion time test in short mode")
	}

	srv, engine, ctx, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()

	beforeCount := getHistogramSampleCount(sharedMetrics.ConversionTime)

	zipPath := filepath.Join("fixtures", "test.zpt")
	req := createMultipartRequest(t, zipPath, map[string]string{
		"script":    "test.html",
		"page_size": "A4",
		"margins":   "standard",
	})
	req.Header.Set("X-Auth-Key", testAuthToken)

	w := httptest.NewRecorder()
	srv.Router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	afterCount := getHistogramSampleCount(sharedMetrics.ConversionTime)
	assert.Greater(t, afterCount, beforeCount,
		"ConversionTime histogram should have new observations after successful render")

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}
