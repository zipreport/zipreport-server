package internal

import (
	"net"
	"net/netip"
	"testing"

	"zipreport-server/pkg/egress"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpstreamLogKV(t *testing.T) {
	for _, resolve := range []string{"local", "upstream"} {
		up, err := egress.ParseUpstream("http://tok3n:s3cret@Proxy.Test:3128", resolve, nil)
		require.NoError(t, err)
		kv := upstreamLogKV(up)
		assert.Equal(t, "http://proxy.test:3128", kv["upstream"])
		assert.Equal(t, resolve, kv["resolve"])
		for _, v := range kv {
			assert.NotContains(t, v, "s3cret")
			assert.NotContains(t, v, "tok3n")
		}
	}
}

func TestUpstreamOnThisHost(t *testing.T) {
	ifaces := []net.Addr{
		&net.IPNet{IP: net.ParseIP("192.0.2.1"), Mask: net.CIDRMask(24, 32)},
		&net.IPAddr{IP: net.ParseIP("2001:db8::1")},
	}
	addrs := func(s ...string) []netip.Addr {
		var out []netip.Addr
		for _, x := range s {
			out = append(out, netip.MustParseAddr(x))
		}
		return out
	}
	for _, s := range []string{"127.0.0.1", "::1", "0.0.0.0", "::", "192.0.2.1", "::ffff:192.0.2.1", "2001:db8::1"} {
		assert.True(t, upstreamOnThisHost(addrs("203.0.113.2", s), ifaces), s)
	}
	assert.False(t, upstreamOnThisHost(addrs("203.0.113.2", "2001:db8::2"), ifaces))
}
