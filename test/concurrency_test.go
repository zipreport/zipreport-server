package test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"zipreport-server/internal/apiserver"
	"zipreport-server/pkg/render"

	"github.com/oddbit-project/blueprint/log"
	"github.com/oddbit-project/blueprint/provider/httpserver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupConcurrentServer creates a test server with specified concurrency
func setupConcurrentServer(t *testing.T, concurrency int) (*httpserver.Server, *render.Engine, context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	logConfig := log.NewDefaultConfig()
	logConfig.Level = "error"
	require.NoError(t, log.Configure(logConfig))
	logger := log.New("test-concurrent")

	metrics := sharedMetrics

	portBase := portCounter
	portCounter += 100

	engine := render.NewEngine(ctx, concurrency, portBase, metrics, logger)

	cfg := httpserver.NewServerConfig()
	cfg.Host = "localhost"
	cfg.Port = 0
	cfg.Options = map[string]string{
		httpserver.OptDefaultSecurityHeaders: "1",
		httpserver.OptAuthTokenHeader:        "X-Auth-Key",
		httpserver.OptAuthTokenSecret:        testAuthToken,
	}

	srv, err := apiserver.NewApiServer(cfg, engine, metrics, logger)
	require.NoError(t, err)
	require.NotNil(t, srv)

	return srv, engine, ctx, cancel
}

// TestConcurrency_ParallelRenders submits N concurrent requests where N = pool size
func TestConcurrency_ParallelRenders(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping parallel renders test in short mode")
	}

	poolSize := 2
	srv, engine, ctx, cancel := setupConcurrentServer(t, poolSize)
	defer cancel()
	defer engine.Shutdown()

	var wg sync.WaitGroup
	results := make([]int, poolSize)

	for i := 0; i < poolSize; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			req := createMultipartRequest(t, filepath.Join("fixtures", "test.zpt"), map[string]string{
				"script":    "test.html",
				"page_size": "A4",
				"margins":   "standard",
			})
			req.Header.Set("X-Auth-Key", testAuthToken)

			w := httptest.NewRecorder()
			srv.Router.ServeHTTP(w, req)
			results[id] = w.Code
		}(i)
	}

	wg.Wait()

	for i, code := range results {
		assert.Equal(t, http.StatusOK, code, "Request %d should succeed", i)
	}

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestConcurrency_ExceedPoolSize submits more requests than pool capacity
func TestConcurrency_ExceedPoolSize(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping exceed pool size test in short mode")
	}

	poolSize := 2
	requestCount := poolSize + 2
	srv, engine, ctx, cancel := setupConcurrentServer(t, poolSize)
	defer cancel()
	defer engine.Shutdown()

	var wg sync.WaitGroup
	results := make([]int, requestCount)

	for i := 0; i < requestCount; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			req := createMultipartRequest(t, filepath.Join("fixtures", "test.zpt"), map[string]string{
				"script":    "test.html",
				"page_size": "A4",
				"margins":   "standard",
			})
			req.Header.Set("X-Auth-Key", testAuthToken)

			w := httptest.NewRecorder()
			srv.Router.ServeHTTP(w, req)
			results[id] = w.Code
		}(i)
	}

	wg.Wait()

	// All requests should eventually succeed due to pool backpressure
	for i, code := range results {
		assert.Equal(t, http.StatusOK, code,
			"Request %d should succeed even when exceeding pool size", i)
	}

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}

// TestConcurrency_IndependentJobs submits concurrent requests with different page sizes
func TestConcurrency_IndependentJobs(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping independent jobs test in short mode")
	}

	srv, engine, ctx, cancel := setupConcurrentServer(t, 2)
	defer cancel()
	defer engine.Shutdown()

	pageSizes := []string{"A4", "Letter", "A3"}

	var wg sync.WaitGroup
	type result struct {
		code    int
		pdfSize int
	}
	results := make([]result, len(pageSizes))

	for i, size := range pageSizes {
		wg.Add(1)
		go func(id int, pageSize string) {
			defer wg.Done()

			req := createMultipartRequest(t, filepath.Join("fixtures", "test.zpt"), map[string]string{
				"script":    "test.html",
				"page_size": pageSize,
				"margins":   "standard",
			})
			req.Header.Set("X-Auth-Key", testAuthToken)

			w := httptest.NewRecorder()
			srv.Router.ServeHTTP(w, req)
			results[id] = result{code: w.Code, pdfSize: len(w.Body.Bytes())}
		}(i, size)
	}

	wg.Wait()

	for i, r := range results {
		assert.Equal(t, http.StatusOK, r.code,
			"Request %d (%s) should succeed", i, pageSizes[i])
		assert.Greater(t, r.pdfSize, 100,
			"Request %d (%s) should produce a PDF with content", i, pageSizes[i])
	}

	time.Sleep(100 * time.Millisecond)
	_ = ctx
}
