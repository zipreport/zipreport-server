package test

import (
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"zipreport-server/pkg/render"
)

// TestRenderJob_BeforeUnloadBusyPageReleasesSlot tests that a busy page with a beforeunload handler cannot wedge a render slot
func TestRenderJob_BeforeUnloadBusyPageReleasesSlot(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}

	_, engine, _, cancel := setupConcurrentServer(t, 1)
	defer cancel()
	defer engine.Shutdown()

	job := newInlineJob(t, `<html><body>x<script>window.onbeforeunload=function(e){e.preventDefault();e.returnValue='x'};window.addEventListener('load',function(){setTimeout(function(){for(;;){}},200)});</script></body></html>`)
	job.JobTimeoutS = 4
	job.JobSettlingTimeMs = 1000
	done := make(chan *render.JobResult, 1)
	go func() { done <- engine.RenderJob(job) }()

	select {
	case res := <-done:
		assert.False(t, res.Success, "busy page must not render")
	case <-time.After(30 * time.Second):
		t.Fatal("RenderJob did not return: page close wedged")
	}

	next := make(chan *render.JobResult, 1)
	go func() { next <- engine.RenderJob(newInlineJob(t, `<html><body>ok</body></html>`)) }()
	select {
	case res := <-next:
		assert.True(t, res.Success, "slot must be reusable: %v", res.Error)
	case <-time.After(20 * time.Second):
		t.Fatal("follow-up job did not finish: slot or browser leaked")
	}
}

// TestRenderJob_ConsoleObjectFloodKeepsHeapBounded tests that a console object flood cannot grow the heap without bound
func TestRenderJob_ConsoleObjectFloodKeepsHeapBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}
	runConsoleObjectFlood(t, false)
}

// TestRenderJob_ConsoleObjectFloodWithLoggingKeepsHeapBounded tests the same flood with console logging enabled
func TestRenderJob_ConsoleObjectFloodWithLoggingKeepsHeapBounded(t *testing.T) {
	if testing.Short() {
		t.Skip("needs Chrome")
	}
	runConsoleObjectFlood(t, true)
}

func runConsoleObjectFlood(t *testing.T, logging bool) {
	_, engine, _, cancel := setupConcurrentServer(t, 1)
	defer cancel()
	defer engine.Shutdown()
	if logging {
		engine.EnableConsoleLog()
	}

	job := newInlineJob(t, `<html><body>x<script>window.addEventListener('load',function(){var i=0;for(;;){console.log({n:i++,pad:'xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx'})}})</script></body></html>`)
	job.UseJSEvent = true
	job.JsTimeoutS = 20
	job.JobTimeoutS = 20

	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	baseline := ms.HeapAlloc
	var mu sync.Mutex
	peak := baseline

	stop := make(chan struct{})
	sampled := make(chan struct{})
	go func() {
		defer close(sampled)
		tick := time.NewTicker(250 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				mu.Lock()
				if m.HeapAlloc > peak {
					peak = m.HeapAlloc
				}
				mu.Unlock()
			}
		}
	}()

	start := time.Now()
	done := make(chan *render.JobResult, 1)
	go func() { done <- engine.RenderJob(job) }()
	select {
	case <-done:
	case <-time.After(60 * time.Second):
		close(stop)
		<-sampled
		t.Fatal("RenderJob did not return")
	}
	elapsed := time.Since(start)
	close(stop)
	<-sampled

	mu.Lock()
	growth := int64(peak) - int64(baseline)
	mu.Unlock()
	assert.GreaterOrEqual(t, elapsed, 15*time.Second, "flood must have run until the deadline")
	require.Less(t, growth, int64(64<<20), "heap grew %d MiB (baseline %d MiB, peak %d MiB)",
		growth>>20, baseline>>20, peak>>20)
}
