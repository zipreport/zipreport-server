package test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"zipreport-server/pkg/egress"
	"zipreport-server/pkg/zpt"
)

func skipWithoutProcNet(t *testing.T) {
	t.Helper()
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		if _, err := os.Stat(f); err != nil {
			t.Skipf("%s unavailable", f)
		}
	}
}

func requireOnlyLoopback(t *testing.T, before, after map[string]bool) {
	t.Helper()
	added := 0
	for addr := range after {
		if before[addr] {
			continue
		}
		added++
		ok := strings.HasPrefix(addr, "0100007F:") || strings.HasPrefix(addr, "00000000000000000000000001000000:")
		assert.True(t, ok, "listener %s is not loopback", addr)
	}
	require.Positive(t, added, "no new listener observed")
}

func TestZptServer_BindsLoopbackOnly(t *testing.T) {
	skipWithoutProcNet(t)
	reader, logger := poolTestSetup(t)
	before := ownListeners(t)

	pool := zpt.NewServerPool(1, 31950, sharedMetrics, logger)
	srv := pool.BuildServerContext(context.Background(), reader)
	require.NotNil(t, srv)
	defer pool.RemoveServer(srv)

	requireOnlyLoopback(t, before, ownListeners(t))
}

func TestEgressProxy_BindsLoopbackOnly(t *testing.T) {
	skipWithoutProcNet(t)
	before := ownListeners(t)

	p := egress.New("localhost:1", "127.0.0.1:1", 1, 1, egress.Policy{})
	_, err := p.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	requireOnlyLoopback(t, before, ownListeners(t))
}
