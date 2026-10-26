package test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
	"zipreport-server/pkg/render"
	"zipreport-server/pkg/zpt"

	"github.com/oddbit-project/blueprint/log"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func poolTestSetup(t *testing.T) (*zpt.ZptReader, *log.Logger) {
	t.Helper()
	logConfig := log.NewDefaultConfig()
	logConfig.Level = "error"
	require.NoError(t, log.Configure(logConfig))
	reader, err := zpt.NewZptReaderFromFile("fixtures/test.zpt")
	require.NoError(t, err)
	return reader, log.New("test-pool")
}

// buildWithGuard fails the test if BuildServer blocks (leaked slot).
func buildWithGuard(t *testing.T, pool *zpt.ServerPool, reader *zpt.ZptReader) *zpt.ZptServer {
	t.Helper()
	ch := make(chan *zpt.ZptServer, 1)
	go func() { ch <- pool.BuildServer(reader) }()
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatal("BuildServer blocked: slot leaked")
		return nil
	}
}

func getStatus(t *testing.T, url string) int {
	t.Helper()
	resp, err := http.Get(url)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func TestServerPool_BindFailureFailsBuild(t *testing.T) {
	reader, logger := poolTestSetup(t)
	occupier, err := net.Listen("tcp", "127.0.0.1:30100")
	require.NoError(t, err)
	defer func() { _ = occupier.Close() }()

	pool := zpt.NewServerPool(1, 30100, sharedMetrics, logger)
	assert.Nil(t, buildWithGuard(t, pool, reader))

	_ = occupier.Close()
	srv := buildWithGuard(t, pool, reader)
	require.NotNil(t, srv)
	assert.Equal(t, 200, getStatus(t, "http://localhost:30100/test.html"))
	pool.RemoveServer(srv)
}

func TestServerPool_SkipsHeldPort(t *testing.T) {
	reader, logger := poolTestSetup(t)
	occupier, err := net.Listen("tcp", "127.0.0.1:30500")
	require.NoError(t, err)
	defer func() { _ = occupier.Close() }()

	pool := zpt.NewServerPool(2, 30500, sharedMetrics, logger)
	srv := buildWithGuard(t, pool, reader)
	require.NotNil(t, srv)
	assert.Equal(t, 30501, srv.Port)
	assert.Equal(t, 200, getStatus(t, "http://localhost:30501/test.html"))
	pool.RemoveServer(srv)

	_ = occupier.Close()
	srv2 := buildWithGuard(t, pool, reader)
	require.NotNil(t, srv2)
	assert.Equal(t, 30500, srv2.Port)
	pool.RemoveServer(srv2)
}

func TestServerPool_IPv6HeldPortFailsBuild(t *testing.T) {
	reader, logger := poolTestSetup(t)
	occupier, err := net.Listen("tcp", "[::1]:30600")
	if err != nil {
		t.Skip("IPv6 loopback unavailable: " + err.Error())
	}
	defer func() { _ = occupier.Close() }()

	pool := zpt.NewServerPool(1, 30600, sharedMetrics, logger)
	assert.Nil(t, buildWithGuard(t, pool, reader))
}

func TestZptServer_ShutdownBeforeRunReleasesPort(t *testing.T) {
	reader, logger := poolTestSetup(t)
	srv := zpt.NewZptServer(reader, 30700, logger)
	require.NoError(t, srv.Listen())
	require.NoError(t, srv.Shutdown(context.Background()))

	l, err := net.Listen("tcp", "127.0.0.1:30700")
	require.NoError(t, err)
	_ = l.Close()
}

func TestServerPool_GaugeBalancedAfterShutdown(t *testing.T) {
	reader, logger := poolTestSetup(t)
	before := testutil.ToFloat64(sharedMetrics.HttpServers)

	pool := zpt.NewServerPool(1, 30200, sharedMetrics, logger)
	srv := buildWithGuard(t, pool, reader)
	require.NotNil(t, srv)
	require.Eventually(t, func() bool {
		resp, err := http.Get("http://localhost:30200/test.html")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == 200
	}, 2*time.Second, 50*time.Millisecond)
	assert.Equal(t, before+1, testutil.ToFloat64(sharedMetrics.HttpServers))

	pool.Shutdown()
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, before, testutil.ToFloat64(sharedMetrics.HttpServers))
}

func TestServerPool_UsesConfiguredTimeouts(t *testing.T) {
	reader, logger := poolTestSetup(t)

	pool := zpt.NewServerPool(1, 30800, sharedMetrics, logger)
	pool.ReadTimeout = 7 * time.Second
	pool.WriteTimeout = 8 * time.Second
	srv := pool.BuildServer(reader)
	require.NotNil(t, srv)
	assert.Equal(t, 7*time.Second, srv.Server.ReadTimeout)
	assert.Equal(t, 8*time.Second, srv.Server.WriteTimeout)
	pool.RemoveServer(srv)

	pool2 := zpt.NewServerPool(1, 30810, sharedMetrics, logger)
	srv2 := pool2.BuildServer(reader)
	require.NotNil(t, srv2)
	assert.Equal(t, zpt.ReadTimeout, srv2.Server.ReadTimeout)
	assert.Equal(t, zpt.WriteTimeout, srv2.Server.WriteTimeout)
	pool2.RemoveServer(srv2)
}

func TestEngine_SetServerTimeouts(t *testing.T) {
	if testing.Short() {
		t.Skip("launches Chrome in CI/Docker")
	}
	_, logger := poolTestSetup(t)
	engine := render.NewEngine(context.Background(), 1, 30900, sharedMetrics, logger)
	defer engine.Shutdown()

	engine.SetServerTimeouts(7*time.Second, 8*time.Second)
	assert.Equal(t, 7*time.Second, engine.ServerPool.ReadTimeout)
	assert.Equal(t, 8*time.Second, engine.ServerPool.WriteTimeout)
}
