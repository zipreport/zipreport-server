package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoopbackName(t *testing.T) {
	for _, h := range []string{
		"localhost", "LOCALHOST", "localhost.", "x.localhost", "a.b.localhost.", "localhost.localdomain",
		"localhost4", "localhost4.localdomain4", "localhost6", "localhost6.localdomain6", "ip6-localhost", "ip6-loopback",
	} {
		assert.True(t, LoopbackName(h), h)
	}
	for _, h := range []string{"", "localhost.example.com", "notlocalhost", "localhostx", "example.com", "127.0.0.1"} {
		assert.False(t, LoopbackName(h), h)
	}
}

func TestPlainHostName(t *testing.T) {
	for _, h := range []string{"example.com", "example.com.", "a-b_c.test", "xn--xample-9ua.test", "localhost", "x1.com", "a.0xg"} {
		assert.True(t, PlainHostName(h), h)
	}
	for _, h := range []string{"", ".", "..", "a..b", "127.1", "2130706433", "1.2.3.4", "10.0.0.3.", "0x7f000001",
		"127.0.0.0x1", "127.0.0.0X", "0x", "éxample.test", "%31.test", "a b", "ex!.com"} {
		assert.False(t, PlainHostName(h), h)
	}
}

func TestOnThisHost(t *testing.T) {
	ifaces := []netip.Addr{netip.MustParseAddr("192.0.2.1"), netip.MustParseAddr("fe80::1%eth0")}
	for _, s := range []string{"127.0.0.1", "127.9.9.9", "::1", "0.0.0.0", "::", "::ffff:127.0.0.1", "192.0.2.1",
		"::ffff:192.0.2.1", "fe80::1", "fe80::1%eth1", "64:ff9b::7f00:1", "64:ff9b::c000:201", "2002:c000:201::"} {
		assert.True(t, OnThisHost(netip.MustParseAddr(s), ifaces), s)
	}
	for _, s := range []string{"192.0.2.2", "8.8.8.8", "fe80::2"} {
		assert.False(t, OnThisHost(netip.MustParseAddr(s), ifaces), s)
	}
}

func TestNamesThisHost(t *testing.T) {
	for _, h := range []string{"localhost", "LOCALHOST.", "x.localhost", "127.0.0.1", "127.0.0.1.", "0.0.0.0.", "::1",
		"::ffff:127.0.0.1", "64:ff9b::7f00:1"} {
		assert.True(t, NamesThisHost(h), h)
	}
	for _, h := range []string{"", "proxy.test", "proxy.test.", "203.0.113.2", "203.0.113.2.", "127.0.0.1.example.com",
		"localhost..", "127.0.0.1.."} {
		assert.False(t, NamesThisHost(h), h)
	}
}

func TestNorm(t *testing.T) {
	cases := map[string]string{
		"::ffff:8.8.8.8":     "8.8.8.8",
		"fe80::1%eth0":       "fe80::1",
		"64:ff9b::808:808":   "8.8.8.8", // NAT64
		"2002:808:808::":     "8.8.8.8", // 6to4
		"::7f00:1":           "127.0.0.1",
		"2001:4860::8888":    "2001:4860::8888",
		"203.0.113.10":       "203.0.113.10",
		"::ffff:203.0.113.2": "203.0.113.2",
	}
	for in, want := range cases {
		assert.Equal(t, netip.MustParseAddr(want), norm(netip.MustParseAddr(in)), in)
	}
}

func TestUpstreamEndpointAndHostname(t *testing.T) {
	up, err := ParseUpstream("https://tok3n:s3cret@Proxy.Example.COM.:8443", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "https://proxy.example.com:8443", up.Endpoint())
	assert.NotContains(t, up.Endpoint(), "tok3n")
	assert.Equal(t, "proxy.example.com", up.Hostname())
	assert.Equal(t, "proxy.example.com.", up.LookupName(), "a rooted name stays rooted for DNS")
	for raw, want := range map[string]string{
		"http://Proxy.Corp:3128":    "proxy.corp",
		"http://Proxy.Corp..:3128":  "proxy.corp.",
		"http://127.0.0.1.:3128":    "127.0.0.1",
		"http://203.0.113.2:3128":   "203.0.113.2",
		"http://[2001:db8::1]:3128": "2001:db8::1",
	} {
		u, err := ParseUpstream(raw, "", nil)
		require.NoError(t, err)
		assert.Equal(t, want, u.LookupName(), raw)
	}
	assert.False(t, up.ByName)
	assert.Equal(t, 5*time.Minute, up.AuthCooldown)
}

func TestUpstreamDirect(t *testing.T) {
	up, err := ParseUpstream("http://p.test:3128", "", []string{"8.8.8.0/24", "cdn.test:443"})
	require.NoError(t, err)
	a := func(s ...string) []netip.Addr {
		var out []netip.Addr
		for _, x := range s {
			out = append(out, netip.MustParseAddr(x))
		}
		return out
	}
	assert.True(t, up.direct("cdn.test", "443", nil), "exact host:port needs no addresses")
	assert.True(t, up.direct("CDN.test", "443", nil), "host match is case-insensitive")
	assert.False(t, up.direct("cdn.test", "80", nil), "other port")
	assert.True(t, up.direct("x.test", "443", a("8.8.8.8", "8.8.8.4")))
	assert.True(t, up.direct("x.test", "443", a("::ffff:8.8.8.8")), "mapped address is normalised")
	assert.False(t, up.direct("x.test", "443", a("8.8.8.8", "1.1.1.1")), "every address must be inside")
	assert.False(t, up.direct("x.test", "443", nil), "a prefix rule needs addresses")
}

func ifaceAddrs(ips ...string) InterfaceAddrsFunc {
	return func() ([]net.Addr, error) {
		var out []net.Addr
		for _, s := range ips {
			out = append(out, &net.IPNet{IP: net.ParseIP(s), Mask: net.CIDRMask(24, 32)})
		}
		return out, nil
	}
}

// unitProxy builds a Proxy whose plan can run without Start: no listener, fixed interface addresses.
func unitProxy(t *testing.T, policy Policy, lookup map[string][]string, upstreamAddrs ...string) *Proxy {
	t.Helper()
	p := New("localhost:22000", "127.0.0.1:22000", 22000, 4, policy)
	p.proxyPort = 30000
	p.local = map[netip.Addr]struct{}{netip.MustParseAddr("192.0.2.1"): {}}
	p.Lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		ips, ok := lookup[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, s := range ips {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
	if up := policy.Upstream; up != nil {
		for _, s := range upstreamAddrs {
			p.upstreamAddrs = append(p.upstreamAddrs, norm(netip.MustParseAddr(s)))
		}
		p.upstreamPort = 3128
	}
	return p
}

func TestPlanRoutes(t *testing.T) {
	lookup := map[string][]string{
		"pub.test":         {"8.8.8.8", "8.8.4.4"},
		"intra.test":       {"10.0.0.5"},
		"mixed.test":       {"8.8.8.8", "10.0.0.5"},
		"loop.test":        {"127.0.0.1"},
		"iface.test":       {"192.0.2.1"},
		"proxy.test":       {"203.0.113.2"},
		"near.test":        {"203.0.113.2"}, // a different name for the upstream's address
		"nat64.test":       {"64:ff9b::cb00:7102"},
		"cdn.test":         {"1.1.1.1"},
		"nat64-iface.test": {"64:ff9b::c000:201"}, // NAT64 form of this host's interface address 192.0.2.1
	}
	allow := func(rules ...string) []Rule {
		var out []Rule
		for _, s := range rules {
			r, err := ParseRule(s)
			require.NoError(t, err)
			out = append(out, r)
		}
		return out
	}
	vetted := func(s ...string) route { return route{targets: s} }
	viaIP := func(s ...string) route { return route{targets: s, upstream: true} }
	viaName := func(s string) route { return route{targets: []string{s}, upstream: true, byName: true} }
	refused := route{}

	type tc struct {
		name          string
		resolve       string
		noProxy       []string
		allow         []Rule
		reserved      []int
		host, port    string
		want          route
		wantRefused   bool
		wantNoLookups bool
	}
	cases := []tc{
		{name: "local/public goes upstream by vetted IP", resolve: "local", host: "pub.test", port: "443",
			want: viaIP("8.8.8.8:443", "8.8.4.4:443")},
		{name: "local/literal public IP", resolve: "local", host: "8.8.8.8", port: "80", want: viaIP("8.8.8.8:80")},
		{name: "local/unresolvable refused", resolve: "local", host: "nowhere.test", port: "443", wantRefused: true},
		{name: "local/private refused", resolve: "local", host: "intra.test", port: "443", wantRefused: true},
		{name: "local/mixed answer refused", resolve: "local", host: "mixed.test", port: "443", wantRefused: true},
		{name: "local/egressAllow private stays direct", resolve: "local", allow: allow("10.0.0.0/8"),
			host: "intra.test", port: "443", want: vetted("10.0.0.5:443")},
		{name: "local/egressAllow mixed stays direct", resolve: "local", allow: allow("10.0.0.0/8"),
			host: "mixed.test", port: "443", want: vetted("8.8.8.8:443", "10.0.0.5:443")},
		{name: "local/noProxy prefix direct", resolve: "local", noProxy: []string{"8.8.0.0/16"},
			host: "pub.test", port: "443", want: vetted("8.8.8.8:443", "8.8.4.4:443")},
		{name: "local/noProxy host:port direct", resolve: "local", noProxy: []string{"cdn.test:443"},
			host: "cdn.test", port: "443", want: vetted("1.1.1.1:443")},
		{name: "local/upstream name any port refused", resolve: "local", host: "PROXY.test.", port: "443",
			wantRefused: true, wantNoLookups: true},
		{name: "local/upstream address other port refused", resolve: "local", host: "203.0.113.2", port: "22",
			wantRefused: true},
		{name: "local/other name for upstream address refused", resolve: "local", host: "near.test", port: "443",
			wantRefused: true},
		{name: "local/NAT64 form of upstream address refused", resolve: "local", host: "nat64.test", port: "443",
			wantRefused: true},
		{name: "local/bad port refused", resolve: "local", host: "pub.test", port: "http", wantRefused: true,
			wantNoLookups: true},
		{name: "local/upstream port on loopback refused even when allowed", resolve: "local",
			allow: allow("127.0.0.0/8"), host: "loop.test", port: "3128", wantRefused: true},
		{name: "local/NAT64 form of an interface address refused", resolve: "local", host: "nat64-iface.test",
			port: "443", wantRefused: true},
		{name: "local/NAT64 form of an interface address on a reserved port refused", resolve: "local",
			allow: allow("64:ff9b::/96"), host: "nat64-iface.test", port: "22001", wantRefused: true},
		{name: "local/upstream port on a public host allowed", resolve: "local", host: "pub.test", port: "3128",
			want: viaIP("8.8.8.8:3128", "8.8.4.4:3128")},

		{name: "by-name/resolvable public by name", resolve: "upstream", host: "pub.test", port: "443",
			want: viaName("pub.test:443")},
		{name: "by-name/unresolvable by name", resolve: "upstream", host: "only-proxy.test", port: "443",
			want: viaName("only-proxy.test:443")},
		{name: "by-name/literal IP still by vetted IP", resolve: "upstream", host: "8.8.8.8", port: "443",
			want: viaIP("8.8.8.8:443")},
		{name: "by-name/resolvable private refused", resolve: "upstream", host: "intra.test", port: "443",
			wantRefused: true},
		{name: "by-name/resolvable to an interface refused", resolve: "upstream", host: "iface.test", port: "443",
			wantRefused: true},
		{name: "by-name/loopback name refused before lookup", resolve: "upstream", host: "x.LOCALHOST.", port: "443",
			wantRefused: true, wantNoLookups: true},
		{name: "by-name/hard-denied port refused before lookup", resolve: "upstream", host: "only-proxy.test",
			port: "22001", wantRefused: true, wantNoLookups: true},
		{name: "by-name/reserved port refused", resolve: "upstream", reserved: []int{6543}, host: "pub.test",
			port: "6543", wantRefused: true, wantNoLookups: true},
		{name: "by-name/own proxy port refused", resolve: "upstream", host: "only-proxy.test", port: "30000",
			wantRefused: true, wantNoLookups: true},
		{name: "by-name/egressAllow CIDR stays direct", resolve: "upstream", allow: allow("10.0.0.0/8"),
			host: "intra.test", port: "443", want: vetted("10.0.0.5:443")},
		{name: "by-name/exact egressAllow is address-vetted and direct", resolve: "upstream",
			allow: allow("loop.test:8080"), host: "loop.test", port: "8080", want: vetted("127.0.0.1:8080")},
		{name: "by-name/exact egressAllow unresolvable refused", resolve: "upstream",
			allow: allow("gone.test:8080"), host: "gone.test", port: "8080", wantRefused: true},
		{name: "by-name/noProxy prefix direct", resolve: "upstream", noProxy: []string{"8.8.0.0/16"},
			host: "pub.test", port: "443", want: vetted("8.8.8.8:443", "8.8.4.4:443")},
		{name: "by-name/exact noProxy unresolvable refused", resolve: "upstream",
			noProxy: []string{"only-proxy.test:443"}, host: "only-proxy.test", port: "443", wantRefused: true},
		{name: "by-name/upstream address refused", resolve: "upstream", host: "near.test", port: "443",
			wantRefused: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			up, err := ParseUpstream("http://proxy.test:3128", c.resolve, c.noProxy)
			require.NoError(t, err)
			p := unitProxy(t, Policy{Allow: c.allow, ReservedPorts: c.reserved, Upstream: up}, lookup, "203.0.113.2")
			lookups := 0
			inner := p.Lookup
			p.Lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
				lookups++
				return inner(ctx, host)
			}
			got, err := p.plan(context.Background(), c.host, c.port)
			if c.wantRefused {
				require.ErrorIs(t, err, errRefused)
				assert.Equal(t, refused, got)
			} else {
				require.NoError(t, err)
				assert.Equal(t, c.want, got)
			}
			if c.wantNoLookups {
				assert.Zero(t, lookups, "refused before any lookup")
			}
		})
	}
}

func TestPlanWithoutUpstreamMatchesVet(t *testing.T) {
	lookup := map[string][]string{"pub.test": {"8.8.8.8"}, "intra.test": {"10.0.0.5"}}
	p := unitProxy(t, Policy{}, lookup)
	got, err := p.plan(context.Background(), "pub.test", "443")
	require.NoError(t, err)
	assert.Equal(t, route{targets: []string{"8.8.8.8:443"}}, got)
	_, err = p.plan(context.Background(), "intra.test", "443")
	assert.ErrorIs(t, err, errRefused)
}

func TestAuthLatch(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	mk := func(t *testing.T, rawURL string) *Upstream {
		up, err := ParseUpstream(rawURL, "", nil)
		require.NoError(t, err)
		return up
	}
	t.Run("a 407 blocks only its own target", func(t *testing.T) {
		up := mk(t, "http://u:p@proxy.test:3128")
		up.record407("a.test:443", t0)
		assert.True(t, up.authBlocked("a.test:443", t0))
		assert.False(t, up.authBlocked("b.test:443", t0), "one destination's 407 may be the upstream's policy")
	})
	// without authProbe
	t.Run("distinct targets trip the latch", func(t *testing.T) {
		up := mk(t, "http://u:p@proxy.test:3128")
		for _, tg := range []string{"a.test:443", "b.test:443"} {
			up.record407(tg, t0)
		}
		assert.False(t, up.authBlocked("z.test:443", t0))
		up.record407("a.test:443", t0) // the same target again is not a new one
		assert.False(t, up.authBlocked("z.test:443", t0))
		up.record407("c.test:443", t0)
		assert.True(t, up.authBlocked("z.test:443", t0), "a wrong password fails everywhere")
		assert.True(t, up.authBlocked("z.test:443", t0.Add(defaultAuthCooldown-time.Second)))
		assert.False(t, up.authBlocked("z.test:443", t0.Add(defaultAuthCooldown)), "the latch expires")
	})
	t.Run("old 407s do not count", func(t *testing.T) {
		up := mk(t, "http://u:p@proxy.test:3128")
		up.record407("a.test:443", t0)
		up.record407("b.test:443", t0)
		later := t0.Add(defaultAuthCooldown)
		up.record407("c.test:443", later)
		assert.False(t, up.authBlocked("z.test:443", later))
		assert.False(t, up.authBlocked("a.test:443", later), "a target's own block expires too")
	})
	t.Run("without credentials a 407 never blocks every target", func(t *testing.T) {
		up := mk(t, "http://proxy.test:3128")
		for _, tg := range []string{"a.test:443", "b.test:443", "c.test:443", "d.test:443"} {
			up.record407(tg, t0)
		}
		assert.False(t, up.authBlocked("z.test:443", t0))
		assert.True(t, up.authBlocked("a.test:443", t0))
	})
	t.Run("with an auth probe a 407 asks for it instead of counting", func(t *testing.T) {
		up := mk(t, "http://u:p@proxy.test:3128")
		up.AuthProbe = "probe.test:443"
		for _, tg := range []string{"a.test:443", "b.test:443", "c.test:443"} {
			assert.True(t, up.record407(tg, t0))
		}
		assert.False(t, up.authBlocked("z.test:443", t0), "only the probe may latch")
		assert.True(t, up.authBlocked("a.test:443", t0))
	})
	t.Run("without an auth probe the distinct-destination rule is unchanged", func(t *testing.T) {
		up := mk(t, "http://u:p@proxy.test:3128")
		for _, tg := range []string{"a.test:443", "b.test:443"} {
			assert.False(t, up.record407(tg, t0))
		}
		assert.False(t, up.authBlocked("z.test:443", t0))
		assert.False(t, up.record407("c.test:443", t0))
		assert.True(t, up.authBlocked("z.test:443", t0))
	})
	t.Run("without credentials no probe is asked for", func(t *testing.T) {
		up := mk(t, "http://proxy.test:3128")
		up.AuthProbe = "probe.test:443"
		assert.False(t, up.record407("a.test:443", t0))
	})
	t.Run("latchIfSpread counts only 407s after the last pass", func(t *testing.T) {
		up := mk(t, "http://u:p@proxy.test:3128")
		up.AuthProbe = "probe.test:443"
		up.record407("a.test:443", t0)
		up.record407("b.test:443", t0)
		up.probeOKAt.Store(t0.Add(time.Second).UnixNano())
		t1 := t0.Add(2 * time.Second)
		up.record407("c.test:443", t1)
		up.latchIfSpread(t1)
		assert.False(t, up.authBlocked("z.test:443", t1))
		t2 := t0.Add(3 * time.Second)
		up.record407("d.test:443", t2)
		up.record407("e.test:443", t2)
		up.latchIfSpread(t2)
		assert.True(t, up.authBlocked("z.test:443", t2))
	})
	t.Run("failsSincePass ignores 407s older than the cooldown", func(t *testing.T) {
		up := mk(t, "http://u:p@proxy.test:3128")
		up.AuthProbe = "probe.test:443"
		t3 := t0.Add(defaultAuthCooldown + time.Second)
		up.record407("a.test:443", t0)
		up.record407("b.test:443", t3) // prunes a
		up.record407("c.test:443", t3)
		assert.Equal(t, 2, up.failsSincePass(t3))
		up.mu.Lock()
		up.authFails["old.test:443"] = t0
		up.mu.Unlock()
		assert.Equal(t, 2, up.failsSincePass(t3))
	})
	t.Run("zero AuthCooldown means the default", func(t *testing.T) {
		u, err := ParseUpstream("http://u:p@proxy.test:3128", "", nil)
		require.NoError(t, err)
		up := &Upstream{URL: u.URL}
		for _, tg := range []string{"a.test:443", "b.test:443", "c.test:443"} {
			up.record407(tg, t0)
		}
		assert.True(t, up.authBlocked("z.test:443", t0.Add(defaultAuthCooldown-time.Second)))
	})
	t.Run("shared across proxies and recorded", func(t *testing.T) {
		up := mk(t, "http://u:p@proxy.test:3128")
		a := unitProxy(t, Policy{Upstream: up}, nil, "203.0.113.2")
		b := unitProxy(t, Policy{Upstream: up}, nil, "203.0.113.2")
		assert.False(t, a.authLatched("a.test:443"))
		up.record407("a.test:443", time.Now())
		assert.True(t, b.authLatched("a.test:443"))
		n, last := b.UpstreamErrors()
		assert.Equal(t, 1, n)
		assert.Equal(t, "407 (latched)", last)
	})
}

func TestUpstreamAddrsCache(t *testing.T) {
	t0 := time.Unix(1_000_000, 0)
	up, err := ParseUpstream("http://proxy.test:3128", "", nil)
	require.NoError(t, err)
	answer := []string{"203.0.113.2"}
	lookups := 0
	lookup := func() ([]netip.Addr, error) {
		lookups++
		var out []netip.Addr
		for _, s := range answer {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
	dial, refuse, err := up.addrs(t0, lookup)
	require.NoError(t, err)
	assert.Equal(t, []netip.Addr{netip.MustParseAddr("203.0.113.2")}, dial)
	assert.Equal(t, dial, refuse)

	answer = []string{"203.0.113.3"}
	dial, _, err = up.addrs(t0.Add(upstreamCacheTTL-time.Second), lookup)
	require.NoError(t, err)
	assert.Equal(t, 1, lookups, "a fresh answer is reused")
	assert.Equal(t, []netip.Addr{netip.MustParseAddr("203.0.113.2")}, dial)

	dial, refuse, err = up.addrs(t0.Add(upstreamCacheTTL), lookup)
	require.NoError(t, err)
	assert.Equal(t, 2, lookups)
	assert.Equal(t, []netip.Addr{netip.MustParseAddr("203.0.113.3")}, dial)
	assert.Equal(t, []netip.Addr{netip.MustParseAddr("203.0.113.2"), netip.MustParseAddr("203.0.113.3")}, refuse,
		"a rotating answer must not hide an earlier address from the refusal")

	_, _, err = up.addrs(t0.Add(2*upstreamCacheTTL), func() ([]netip.Addr, error) { return nil, errors.New("down") })
	assert.Error(t, err, "a stale cache does not hide a failed lookup")
}

func TestUpstreamAddrsHistory(t *testing.T) {
	up, err := ParseUpstream("http://proxy.test:3128", "", nil)
	require.NoError(t, err)
	t0 := time.Unix(1_000_000, 0)
	stable := netip.MustParseAddr("203.0.113.2")
	var refuse []netip.Addr
	for i := range 70 { // a rotating answer that always includes the stable address, first-seen long ago
		other := netip.AddrFrom4([4]byte{198, 51, 100, byte(i)})
		_, refuse, err = up.addrs(t0.Add(time.Duration(i)*upstreamCacheTTL), func() ([]netip.Addr, error) {
			return []netip.Addr{stable, other}, nil
		})
		require.NoError(t, err)
	}
	assert.Contains(t, refuse, stable, "a re-seen address is never evicted")
	assert.Contains(t, refuse, netip.MustParseAddr("198.51.100.69"))
	assert.NotContains(t, refuse, netip.MustParseAddr("198.51.100.0"), "only the most recently seen are kept")
	assert.Len(t, refuse, maxSeenUpstreamAddrs)

	// an address seen recently but missing from the latest answer stays refused, however early it was first seen
	up2, err := ParseUpstream("http://proxy.test:3128", "", nil)
	require.NoError(t, err)
	for i := range 128 {
		answer := []netip.Addr{netip.AddrFrom4([4]byte{198, 51, 100, byte(i)})}
		if i%2 == 0 {
			answer = append(answer, stable)
		}
		_, refuse, err = up2.addrs(t0.Add(time.Duration(i)*upstreamCacheTTL), func() ([]netip.Addr, error) { return answer, nil })
		require.NoError(t, err)
		require.Contains(t, refuse, stable, "lookup %d: seen at most one lookup ago", i)
	}

	big := make([]netip.Addr, 0, 80)
	for i := range 80 {
		big = append(big, netip.AddrFrom4([4]byte{192, 0, 2, byte(i)}))
	}
	_, refuse, err = up.addrs(t0.Add(100*upstreamCacheTTL), func() ([]netip.Addr, error) { return big, nil })
	require.NoError(t, err)
	for _, a := range big {
		assert.Contains(t, refuse, a, "the current answer is refused in full, larger than the history or not")
	}
	_, refuse, err = up.addrs(t0.Add(100*upstreamCacheTTL+time.Second), func() ([]netip.Addr, error) {
		t.Fatal("the cached answer is fresh")
		return nil, nil
	})
	require.NoError(t, err)
	for _, a := range big {
		assert.Contains(t, refuse, a, "the cached answer too")
	}
}

func TestPlanRefusesUpstreamAddressesSeenAfterStart(t *testing.T) {
	type job struct {
		p       *Proxy
		lookups *int
		hosts   *[]string // names handed to Lookup, in order
	}
	// start runs a job's Start against the shared up; answer is read at lookup time, fail makes lookups fail
	start := func(t *testing.T, up *Upstream, answer *string, fail *bool) job {
		t.Helper()
		lookups := 0
		var hosts []string
		p := unitProxy(t, Policy{Upstream: up}, nil)
		p.Lookup = func(lctx context.Context, host string) ([]netip.Addr, error) {
			lookups++
			hosts = append(hosts, host)
			if err := lctx.Err(); err != nil {
				return nil, err
			}
			if *fail {
				return nil, errors.New("resolver down")
			}
			return []netip.Addr{netip.MustParseAddr(*answer)}, nil
		}
		p.InterfaceAddrs = ifaceAddrs("192.0.2.1")
		_, err := p.Start()
		require.NoError(t, err)
		t.Cleanup(func() { _ = p.Close() })
		return job{p, &lookups, &hosts}
	}
	stale := func(up *Upstream) { // the shared answer is older than upstreamCacheTTL
		up.mu.Lock()
		up.cachedAt = time.Time{}
		up.mu.Unlock()
	}
	ctx := context.Background()

	t.Run("an address a later job resolved is refused in an earlier job", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		answer, fail := "203.0.113.2", false
		j1 := start(t, up, &answer, &fail)
		answer = "203.0.113.9"
		stale(up)
		start(t, up, &answer, &fail)
		_, err = j1.p.plan(ctx, "203.0.113.9", "443")
		assert.ErrorIs(t, err, errRefused)
		assert.Equal(t, 1, *j1.lookups, "the history is read without a lookup")
	})
	t.Run("a long job re-resolves the upstream once the answer is stale", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test.:3128", "local", nil) // rooted: the refresh must use LookupName
		require.NoError(t, err)
		answer, fail := "203.0.113.2", false
		j := start(t, up, &answer, &fail)
		answer = "203.0.113.9"
		stale(up)
		_, err = j.p.plan(ctx, "203.0.113.9", "443")
		assert.ErrorIs(t, err, errRefused)
		assert.Equal(t, 2, *j.lookups)
		got, err := j.p.plan(ctx, "8.8.8.8", "443")
		require.NoError(t, err)
		assert.Equal(t, route{targets: []string{"8.8.8.8:443"}, upstream: true}, got, "other public addresses still go upstream")
		_, err = j.p.plan(ctx, "203.0.113.2", "443")
		assert.ErrorIs(t, err, errRefused, "the address vetted at Start stays refused")
		assert.Equal(t, 2, *j.lookups, "the refreshed shared answer is fresh: no further lookup")

		// upstreamCacheTTL later the job refreshes again
		j.p.upstreamCheckedAt.Store(int64(time.Since(monoEpoch)) - int64(upstreamCacheTTL))
		answer = "203.0.113.10"
		stale(up)
		_, err = j.p.plan(ctx, "203.0.113.10", "443")
		assert.ErrorIs(t, err, errRefused, "a second rotation in the same job")
		assert.Equal(t, 3, *j.lookups)
		assert.Equal(t, []string{"p.test.", "p.test.", "p.test."}, *j.hosts, "Start and both refreshes look up the rooted name")
	})
	t.Run("a failed refresh keeps the history and is not retried at once", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		answer, fail := "203.0.113.2", false
		j1 := start(t, up, &answer, &fail)
		answer = "203.0.113.9"
		stale(up)
		start(t, up, &answer, &fail)
		fail = true
		stale(up)
		for range 2 {
			_, err = j1.p.plan(ctx, "203.0.113.9", "443")
			assert.ErrorIs(t, err, errRefused)
		}
		_, err = j1.p.plan(ctx, "8.8.8.8", "443")
		assert.NoError(t, err)
		assert.Equal(t, 2, *j1.lookups, "Start plus one failed refresh")
	})
	t.Run("a page cancelling its request does not cancel the refresh", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		answer, fail := "203.0.113.2", false
		j := start(t, up, &answer, &fail)
		answer = "203.0.113.9"
		stale(up)
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err = j.p.plan(cctx, "203.0.113.9", "443")
		assert.ErrorIs(t, err, errRefused)
		assert.Contains(t, up.cached, netip.MustParseAddr("203.0.113.9"), "the refresh completed")
	})
	t.Run("closing the job ends a refresh in flight", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		answer, fail := "203.0.113.2", false
		j := start(t, up, &answer, &fail)
		entered := make(chan struct{})
		j.p.Lookup = func(lctx context.Context, _ string) ([]netip.Addr, error) {
			close(entered)
			<-lctx.Done() // a resolver that never answers
			return nil, lctx.Err()
		}
		stale(up)
		go func() {
			time.Sleep(50 * time.Millisecond)
			_ = j.p.Close()
		}()
		begin := time.Now()
		_, _ = j.p.plan(ctx, "8.8.8.8", "443")
		assert.Less(t, time.Since(begin), time.Second, "not left to lookupTimeout")
		select {
		case <-entered:
		default:
			t.Fatal("the refresh never started")
		}
	})
	t.Run("a plan while the answer is fresh does not use up the refresh", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		answer, fail := "203.0.113.2", false
		j := start(t, up, &answer, &fail)
		_, err = j.p.plan(ctx, "8.8.8.8", "443") // fresh from Start: no lookup, and no throttle stamp
		require.NoError(t, err)
		answer = "203.0.113.9"
		stale(up)
		_, err = j.p.plan(ctx, "203.0.113.9", "443")
		assert.ErrorIs(t, err, errRefused)
	})
	t.Run("a refresh adds to the history read before it", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		answer, fail := "203.0.113.2", false
		j1 := start(t, up, &answer, &fail)
		answer = "203.0.113.9"
		stale(up)
		start(t, up, &answer, &fail)
		var big []netip.Addr // an answer larger than the history evicts 203.0.113.9 from it
		for i := range maxSeenUpstreamAddrs + 6 {
			big = append(big, netip.AddrFrom4([4]byte{198, 51, 100, byte(i)}))
		}
		j1.p.Lookup = func(context.Context, string) ([]netip.Addr, error) { return big, nil }
		stale(up)
		_, err = j1.p.plan(ctx, "203.0.113.9", "443")
		assert.ErrorIs(t, err, errRefused)
		refuse, _ := up.refusalsCached(time.Now())
		assert.NotContains(t, refuse, netip.MustParseAddr("203.0.113.9"), "control: the refresh evicted it")
	})
	t.Run("a refreshed answer in NAT64 form refuses the IPv4 address, and the reverse", func(t *testing.T) {
		for _, c := range []struct{ rotated, asked string }{
			{"64:ff9b::cb00:7109", "203.0.113.9"},
			{"203.0.113.9", "64:ff9b::cb00:7109"},
		} {
			up, err := ParseUpstream("http://p.test:3128", "local", nil)
			require.NoError(t, err)
			answer, fail := "203.0.113.2", false
			j := start(t, up, &answer, &fail)
			answer = c.rotated
			stale(up)
			_, err = j.p.plan(ctx, c.asked, "443")
			assert.ErrorIs(t, err, errRefused, "%s rotated, %s asked", c.rotated, c.asked)
		}
	})
	t.Run("an address evicted from the history stays refused for the job that vetted it", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		answer, fail := "203.0.113.2", false
		j := start(t, up, &answer, &fail)
		for i := range maxSeenUpstreamAddrs + 6 {
			stale(up)
			_, _, err := up.addrs(time.Now(), func() ([]netip.Addr, error) {
				return []netip.Addr{netip.AddrFrom4([4]byte{198, 51, 100, byte(i)})}, nil
			})
			require.NoError(t, err)
		}
		refuse, _ := up.refusalsCached(time.Now())
		require.NotContains(t, refuse, netip.MustParseAddr("203.0.113.2"), "evicted from the shared history")
		_, err = j.p.plan(ctx, "203.0.113.2", "443")
		assert.ErrorIs(t, err, errRefused, "still in this job's Start snapshot")
	})
	t.Run("an address seen before Start stays refused after eviction from the history", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		_, _, err = up.addrs(time.Now(), func() ([]netip.Addr, error) { // an earlier answer, e.g. from another job
			return []netip.Addr{netip.MustParseAddr("203.0.113.5")}, nil
		})
		require.NoError(t, err)
		stale(up)
		answer, fail := "203.0.113.2", false
		j := start(t, up, &answer, &fail) // dials 203.0.113.2; its snapshot also refuses 203.0.113.5
		for i := range maxSeenUpstreamAddrs + 6 {
			stale(up)
			_, _, err := up.addrs(time.Now(), func() ([]netip.Addr, error) {
				return []netip.Addr{netip.AddrFrom4([4]byte{198, 51, 100, byte(i)})}, nil
			})
			require.NoError(t, err)
		}
		refuse, _ := up.refusalsCached(time.Now())
		require.NotContains(t, refuse, netip.MustParseAddr("203.0.113.5"), "evicted from the shared history")
		_, err = j.p.plan(ctx, "203.0.113.5", "443")
		assert.ErrorIs(t, err, errRefused, "still in this job's Start snapshot, though Start never dialled it")
	})
}

func TestRecord407Bounded(t *testing.T) {
	up, err := ParseUpstream("http://proxy.test:3128", "", nil) // no credentials: the latch never trips
	require.NoError(t, err)
	t0 := time.Unix(1_000_000, 0)
	for i := range maxAuthFails + 100 {
		up.record407(fmt.Sprintf("d%d.test:443", i), t0.Add(time.Duration(i)*time.Millisecond))
	}
	assert.Len(t, up.authFails, maxAuthFails)
	now := t0.Add(time.Second)
	assert.True(t, up.authBlocked(fmt.Sprintf("d%d.test:443", maxAuthFails+99), now))
	assert.False(t, up.authBlocked("d0.test:443", now), "the oldest is forgotten first")

	// a caller's clock read before taking the lock can be older than every entry
	up.record407("late.test:443", t0.Add(-time.Second))
	assert.Len(t, up.authFails, maxAuthFails)
}

func TestDialUpstreamRechecksLatchBeforeCredentials(t *testing.T) {
	up, err := ParseUpstream("http://user:pw@proxy.test:3128", "local", nil)
	require.NoError(t, err)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	got := make(chan int, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			got <- -1
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		b, _ := io.ReadAll(c)
		got <- len(b)
	}()
	p := unitProxy(t, Policy{Upstream: up}, nil)
	p.upstreamDial = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	p.upstreamPort = l.Addr().(*net.TCPAddr).Port
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		up.record407("pub.test:443", time.Now()) // another request's 407 arrives while this one dials
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	_, err = p.dialUpstream(context.Background(), "pub.test:443", "8.8.8.8:443")
	require.ErrorIs(t, err, errUpstreamAuth)
	assert.Zero(t, <-got, "nothing, and so no credentials, was sent to the upstream")
}

func TestStartUpstreamChecks(t *testing.T) {
	start := func(t *testing.T, rawURL, resolve string, lookup map[string][]string) error {
		up, err := ParseUpstream(rawURL, resolve, nil)
		require.NoError(t, err)
		p := unitProxy(t, Policy{Upstream: up}, lookup)
		p.InterfaceAddrs = ifaceAddrs("192.0.2.1")
		_, err = p.Start()
		if err == nil {
			_ = p.Close()
		}
		return err
	}
	t.Run("unresolvable upstream fails closed", func(t *testing.T) {
		assert.EqualError(t, start(t, "http://gone.test:3128", "local", nil), "cannot resolve the upstream proxy")
	})
	t.Run("by-name upstream on loopback refused", func(t *testing.T) {
		err := start(t, "http://p.test:3128", "upstream", map[string][]string{"p.test": {"203.0.113.2", "127.0.0.1"}})
		assert.ErrorContains(t, err, "on this host")
	})
	t.Run("by-name upstream on a local interface refused", func(t *testing.T) {
		err := start(t, "http://p.test:3128", "upstream", map[string][]string{"p.test": {"::ffff:192.0.2.1"}})
		assert.ErrorContains(t, err, "on this host")
	})
	t.Run("local-mode upstream on loopback allowed", func(t *testing.T) {
		assert.NoError(t, start(t, "http://p.test:3128", "local", map[string][]string{"p.test": {"127.0.0.1"}}))
	})
	t.Run("by-name upstream on IPv6 loopback refused", func(t *testing.T) {
		err := start(t, "http://p.test:3128", "upstream", map[string][]string{"p.test": {"::1"}})
		assert.ErrorContains(t, err, "on this host")
	})
	t.Run("upstream dialled at its resolved address", func(t *testing.T) {
		for _, s := range []string{"::1", "64:ff9b::cb00:7102", "2002:cb00:7102::1", "203.0.113.2"} {
			up, err := ParseUpstream("http://p.test:3128", "local", nil)
			require.NoError(t, err)
			p := unitProxy(t, Policy{Upstream: up}, map[string][]string{"p.test": {s}})
			p.InterfaceAddrs = ifaceAddrs("192.0.2.1")
			var dialled []string
			p.Dial = func(_ context.Context, _, addr string) (net.Conn, error) {
				dialled = append(dialled, addr)
				return nil, errors.New("no network in unit tests")
			}
			_, err = p.Start()
			require.NoError(t, err)
			_, _ = p.dialUpstreamProxyRaw(context.Background())
			_ = p.Close()
			assert.Equal(t, []string{net.JoinHostPort(s, "3128")}, dialled, s)
			assert.True(t, p.isUpstreamAddr(netip.MustParseAddr(s)), s)
		}
	})
	t.Run("rooted upstream name looked up rooted", func(t *testing.T) {
		up, err := ParseUpstream("https://Proxy.Corp.:3128", "local", nil)
		require.NoError(t, err)
		p := unitProxy(t, Policy{Upstream: up}, nil)
		var names []string
		p.Lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
			names = append(names, host)
			return []netip.Addr{netip.MustParseAddr("203.0.113.2")}, nil
		}
		p.InterfaceAddrs = ifaceAddrs("192.0.2.1")
		_, err = p.Start()
		require.NoError(t, err)
		defer func() { _ = p.Close() }()
		assert.Equal(t, []string{"proxy.corp."}, names, "a trailing dot keeps search domains out of the lookup")
		assert.Equal(t, "proxy.corp", p.upstreamTLSConfig().ServerName, "SNI never carries the dot")
		for _, h := range []string{"proxy.corp", "PROXY.CORP."} {
			_, err := p.plan(context.Background(), h, "443")
			assert.ErrorIs(t, err, errRefused, h)
		}
	})
	t.Run("upstream dialled at its Start address, even after a refresh", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		answer := "203.0.113.2"
		p := unitProxy(t, Policy{Upstream: up}, nil)
		p.Lookup = func(_ context.Context, host string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr(answer)}, nil
		}
		p.InterfaceAddrs = ifaceAddrs("192.0.2.1")
		var dialled []string
		p.Dial = func(_ context.Context, _, addr string) (net.Conn, error) {
			dialled = append(dialled, addr)
			return nil, errors.New("no network in unit tests")
		}
		_, err = p.Start()
		require.NoError(t, err)
		defer func() { _ = p.Close() }()
		answer = "127.0.0.1" // a rebinding answer after Start must change nothing, even once a refresh sees it
		up.mu.Lock()
		up.cachedAt = time.Time{}
		up.mu.Unlock()
		_, err = p.plan(context.Background(), "8.8.8.8", "443")
		require.NoError(t, err)
		require.Contains(t, up.cached, netip.MustParseAddr("127.0.0.1"), "the plan above refreshed the shared answer")
		_, _ = p.dialUpstreamProxyRaw(context.Background())
		assert.Equal(t, []string{"203.0.113.2:3128"}, dialled)
	})
	t.Run("by-name refused when any address seen for the upstream is on this host", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "upstream", nil)
		require.NoError(t, err)
		_, _, err = up.addrs(time.Now().Add(-time.Hour), func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		})
		require.NoError(t, err)
		p := unitProxy(t, Policy{Upstream: up}, map[string][]string{"p.test": {"203.0.113.2"}})
		p.InterfaceAddrs = ifaceAddrs("192.0.2.1")
		_, err = p.Start()
		assert.ErrorContains(t, err, "on this host")
	})
	t.Run("by-name refused when the upstream once resolved to ::1", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "upstream", nil)
		require.NoError(t, err)
		_, _, err = up.addrs(time.Now().Add(-time.Hour), func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("::1")}, nil
		})
		require.NoError(t, err)
		p := unitProxy(t, Policy{Upstream: up}, map[string][]string{"p.test": {"203.0.113.2"}})
		p.InterfaceAddrs = ifaceAddrs("192.0.2.1")
		_, err = p.Start()
		assert.ErrorContains(t, err, "on this host")
	})
	t.Run("an earlier upstream address stays refused but is never dialled", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		_, _, err = up.addrs(time.Now().Add(-time.Hour), func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("203.0.113.2")}, nil
		})
		require.NoError(t, err)
		p := unitProxy(t, Policy{Upstream: up}, map[string][]string{"p.test": {"203.0.113.3"}})
		p.InterfaceAddrs = ifaceAddrs("192.0.2.1")
		var dialled []string
		p.Dial = func(_ context.Context, _, addr string) (net.Conn, error) {
			dialled = append(dialled, addr)
			return nil, errors.New("no network in unit tests")
		}
		_, err = p.Start()
		require.NoError(t, err)
		defer func() { _ = p.Close() }()
		_, err = p.plan(context.Background(), "203.0.113.2", "22")
		assert.Error(t, err, "a page cannot reach the upstream's earlier address, on any port")
		_, _ = p.dialUpstreamProxyRaw(context.Background())
		assert.Equal(t, []string{"203.0.113.3:3128"}, dialled, "credentials go only to the current address")
	})
	t.Run("lookup ends with the job context", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		p := unitProxy(t, Policy{Upstream: up}, nil)
		p.Lookup = func(ctx context.Context, _ string) ([]netip.Addr, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		p.InterfaceAddrs = ifaceAddrs("192.0.2.1")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		start := time.Now()
		_, err = p.StartContext(ctx)
		assert.ErrorIs(t, err, context.Canceled, "the job's end is told apart from an unresolvable upstream")
		assert.Less(t, time.Since(start), time.Second, "not left to lookupTimeout")
	})
	t.Run("upstream addresses normalised", func(t *testing.T) {
		up, err := ParseUpstream("http://p.test:3128", "local", nil)
		require.NoError(t, err)
		p := unitProxy(t, Policy{Upstream: up}, map[string][]string{"p.test": {"::ffff:203.0.113.2"}})
		p.InterfaceAddrs = ifaceAddrs("192.0.2.1")
		_, err = p.Start()
		require.NoError(t, err)
		defer func() { _ = p.Close() }()
		assert.Equal(t, []netip.Addr{netip.MustParseAddr("203.0.113.2")}, p.upstreamAddrs)
		assert.Equal(t, 3128, p.upstreamPort)
		assert.True(t, p.isUpstreamAddr(netip.MustParseAddr("64:ff9b::cb00:7102")))
	})
}

func TestForwardTunnelResponseWaitBounded(t *testing.T) {
	// an upstream that reads the CONNECT and never answers
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer func() { _ = l.Close() }()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(io.Discard, c); _ = c.Close() }()
		}
	}()
	old := respHeaderLimit
	respHeaderLimit = 200 * time.Millisecond
	defer func() { respHeaderLimit = old }()

	up, err := ParseUpstream(fmt.Sprintf("http://p.test:%d", l.Addr().(*net.TCPAddr).Port), "local", nil)
	require.NoError(t, err)
	p := unitProxy(t, Policy{Upstream: up}, map[string][]string{"p.test": {"127.0.0.1"}, "pub.test": {"8.8.8.8"}})
	p.InterfaceAddrs = ifaceAddrs("192.0.2.1")
	addr, err := p.Start()
	require.NoError(t, err)
	defer func() { _ = p.Close() }()
	pu, err := url.Parse("http://" + addr)
	require.NoError(t, err)
	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true}}

	// the tunnel dial of a plain-http forward does not end with the request, so only the deadline bounds it
	start := time.Now()
	resp, err := c.Get("http://pub.test/x")
	require.NoError(t, err, "the proxy answers before the client gives up")
	_ = resp.Body.Close()
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Less(t, time.Since(start), 3*time.Second)
	n, last := p.UpstreamErrors()
	assert.Equal(t, 1, n)
	assert.Equal(t, "no response", last)
}

// probeUpstream starts an upstream listener for the probe unit tests and points a Proxy at it. handle runs per
// accepted connection, with the 0-based accept index.
func probeUpstream(t *testing.T, authProbe string, handle func(n int, c net.Conn, up *Upstream)) (*Upstream, *Proxy) {
	t.Helper()
	up, err := ParseUpstream("http://user:pw@proxy.test:3128", "local", nil)
	require.NoError(t, err)
	up.AuthProbe = authProbe
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for n := 0; ; n++ {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				handle(n, c, up)
			}()
		}
	}()
	p := unitProxy(t, Policy{Upstream: up}, nil)
	p.upstreamDial = []netip.Addr{netip.MustParseAddr("127.0.0.1")}
	p.upstreamPort = l.Addr().(*net.TCPAddr).Port
	p.Dial = (&net.Dialer{}).DialContext
	return up, p
}

func TestProbeAuthStopsWhenLatched(t *testing.T) {
	var got atomic.Int64
	up, p := probeUpstream(t, "probe.test:443", func(_ int, c net.Conn, _ *Upstream) {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		b, _ := io.ReadAll(c)
		got.Add(int64(len(b)))
	})
	up.ProbeTimeout = 200 * time.Millisecond
	up.latchAuth(time.Now())
	p.probeAuth(context.Background())
	time.Sleep(100 * time.Millisecond)
	assert.Zero(t, got.Load(), "nothing, and so no credentials, was sent to the upstream")
}

func TestProbeAuthRefusesUpstreamAddress(t *testing.T) {
	for _, host := range []string{"203.0.113.10", "::ffff:203.0.113.10"} {
		t.Run(host, func(t *testing.T) {
			var conns atomic.Int64
			up, p := probeUpstream(t, "", func(_ int, c net.Conn, _ *Upstream) { conns.Add(1) })
			p.upstreamAddrs = []netip.Addr{netip.MustParseAddr("203.0.113.10")}
			up.AuthProbe = net.JoinHostPort(host, fmt.Sprint(p.upstreamPort))
			p.probeAuth(context.Background())
			time.Sleep(100 * time.Millisecond)
			assert.Zero(t, conns.Load(), "the probe reached the upstream")
			_, last := p.UpstreamErrors()
			assert.Equal(t, "authProbe misconfigured", last)
		})
	}
}

// probeRecord is what the probe tests' fake upstream saw; its handlers answer CONNECTs without credentials with 407
// and, with credentials, hold the first probe.test:443 request until the client gives up and answer 403 otherwise.
type probeRecord struct {
	mu      sync.Mutex
	targets []string
	auths   []bool
	running []bool
}

func TestProbeWaitBlocksCredentials(t *testing.T) {
	rec := &probeRecord{}
	held := make(chan struct{})
	var heldOnce atomic.Bool
	up, p := probeUpstream(t, "probe.test:443", func(n int, c net.Conn, up *Upstream) {
		if n >= 2 { // after the probe's two connections
			up.probeMu.Lock()
			running := up.probeDone != nil
			up.probeMu.Unlock()
			rec.mu.Lock()
			rec.running = append(rec.running, running)
			rec.mu.Unlock()
		}
		req, err := http.ReadRequest(bufio.NewReader(c))
		if err != nil {
			return
		}
		auth := req.Header.Get("Proxy-Authorization") != ""
		rec.mu.Lock()
		rec.targets = append(rec.targets, req.Host)
		rec.auths = append(rec.auths, auth)
		rec.mu.Unlock()
		switch {
		case !auth:
			_, _ = c.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n"))
		case req.Host == "probe.test:443" && heldOnce.CompareAndSwap(false, true):
			close(held)
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _ = io.Copy(io.Discard, c)
		default:
			_, _ = c.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
		}
	})
	up.ProbeTimeout = 300 * time.Millisecond

	probed := make(chan struct{})
	go func() { p.probeAuth(context.Background()); close(probed) }()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the probe never reached the upstream with credentials")
	}
	start := time.Now()
	p.probeAuth(context.Background()) // single flight: returns at once
	assert.Less(t, time.Since(start), 50*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.dialUpstream(ctx, "x.test:443", "8.8.8.8:443") // waits for the probe
	require.Error(t, err)
	<-probed

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.NotEmpty(t, rec.running)
	for _, r := range rec.running {
		assert.False(t, r, "a connection reached the upstream while the probe was running")
	}
	probeNo, probeYes := 0, 0
	for i, tg := range rec.targets {
		if tg == "probe.test:443" {
			if rec.auths[i] {
				probeYes++
			} else {
				probeNo++
			}
		}
	}
	assert.Equal(t, 1, probeNo)
	assert.Equal(t, 1, probeYes)
}

func TestProbeWaitBeforeWrite(t *testing.T) {
	rec := &probeRecord{}
	held := make(chan struct{})
	var heldOnce atomic.Bool
	up, p := probeUpstream(t, "probe.test:443", func(_ int, c net.Conn, up *Upstream) {
		req, err := http.ReadRequest(bufio.NewReader(c))
		if err != nil {
			return
		}
		if req.Host == "8.8.8.8:443" {
			up.probeMu.Lock()
			running := up.probeDone != nil
			up.probeMu.Unlock()
			rec.mu.Lock()
			rec.running = append(rec.running, running)
			rec.mu.Unlock()
		}
		auth := req.Header.Get("Proxy-Authorization") != ""
		switch {
		case !auth:
			_, _ = c.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n"))
		case req.Host == "probe.test:443" && heldOnce.CompareAndSwap(false, true):
			close(held)
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			_, _ = io.Copy(io.Discard, c)
		default:
			_, _ = c.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
		}
	})
	up.ProbeTimeout = 300 * time.Millisecond
	dialling, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	base := p.Dial
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if first.CompareAndSwap(false, true) {
			close(dialling)
			<-release
		}
		return base(ctx, network, addr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dialed := make(chan error, 1)
	go func() { _, err := p.dialUpstream(ctx, "x.test:443", "8.8.8.8:443"); dialed <- err }()
	<-dialling
	probed := make(chan struct{})
	go func() { p.probeAuth(context.Background()); close(probed) }()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the probe never reached the upstream with credentials")
	}
	close(release)
	require.Error(t, <-dialed)
	<-probed

	rec.mu.Lock()
	defer rec.mu.Unlock()
	require.Len(t, rec.running, 1, "the request reached the upstream once")
	assert.False(t, rec.running[0], "credentials were written while the probe was running")
}

// noDeadlineConn ignores SetDeadline, so a write is never stopped by the poisoned deadline connectUpstream sets
// when ctx ends: only the credential checks can stop it.
type noDeadlineConn struct{ net.Conn }

func (noDeadlineConn) SetDeadline(time.Time) error { return nil }

func TestDialUpstreamCancelledDuringProbeSendsNoCredentials(t *testing.T) {
	rec := &probeRecord{}
	up, p := probeUpstream(t, "probe.test:443", func(_ int, c net.Conn, _ *Upstream) {
		req, err := http.ReadRequest(bufio.NewReader(c))
		if err != nil {
			return
		}
		rec.mu.Lock()
		rec.auths = append(rec.auths, req.Header.Get("Proxy-Authorization") != "")
		rec.mu.Unlock()
		_, _ = c.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
	})
	up.ProbeTimeout = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	base := p.Dial
	p.Dial = func(dctx context.Context, network, addr string) (net.Conn, error) {
		c, err := base(dctx, network, addr)
		if err != nil {
			return nil, err
		}
		up.probeMu.Lock()
		up.probeDone = make(chan struct{})
		up.probeMu.Unlock()
		cancel()
		return noDeadlineConn{c}, nil
	}
	t.Cleanup(func() {
		up.probeMu.Lock()
		up.probeDone = nil
		up.probeMu.Unlock()
	})

	_, err := p.dialUpstream(ctx, "x.test:443", "8.8.8.8:443")
	require.Error(t, err)
	time.Sleep(200 * time.Millisecond)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	assert.NotContains(t, rec.auths, true, "credentials were written after the request ended during a probe")
}

func TestProbeGatedConnRefusesWhenLatched(t *testing.T) {
	up, err := ParseUpstream("http://user:pw@proxy.test:3128", "upstream", nil)
	require.NoError(t, err)
	p := unitProxy(t, Policy{Upstream: up}, nil)
	p.base = context.Background()
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	go func() { _, _ = io.Copy(io.Discard, b) }()
	up.latchAuth(time.Now())
	n, err := probeGatedConn{Conn: a, p: p}.Write([]byte("x"))
	assert.ErrorIs(t, err, errUpstreamAuth)
	assert.Zero(t, n)
}

func TestProbeGatedConnHoldsWhenClosedDuringProbe(t *testing.T) {
	up, err := ParseUpstream("http://user:pw@proxy.test:3128", "upstream", nil)
	require.NoError(t, err)
	p := unitProxy(t, Policy{Upstream: up}, nil)
	a, b := net.Pipe()
	defer func() { _ = a.Close() }()
	defer func() { _ = b.Close() }()
	go func() { _, _ = io.Copy(io.Discard, b) }()
	done := make(chan struct{})
	up.probeMu.Lock()
	up.probeDone = done
	up.probeMu.Unlock()
	t.Cleanup(func() { close(done) })
	up.ProbeTimeout = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.base = ctx
	n, err := probeGatedConn{Conn: a, p: p}.Write([]byte("x"))
	assert.ErrorIs(t, err, errUpstreamAuth)
	assert.Zero(t, n)
}

func TestForwardByNameWaitsForProbeBeforeWrite(t *testing.T) {
	up, err := ParseUpstream("http://user:pw@proxy.test:3128", "upstream", nil)
	require.NoError(t, err)
	up.AuthProbe = "probe.test:443"
	up.ProbeTimeout = time.Second
	p := unitProxy(t, Policy{Upstream: up}, map[string][]string{"proxy.test": {"203.0.113.10"}})
	p.InterfaceAddrs = ifaceAddrs("192.0.2.1")

	held := make(chan struct{})
	var heldOnce atomic.Bool
	var mu sync.Mutex
	var runs []bool
	dialling, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	probed := make(chan struct{})
	status := make(chan int, 1)

	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				br := bufio.NewReader(c)
				for {
					req, err := http.ReadRequest(br)
					if err != nil {
						return
					}
					auth := req.Header.Get("Proxy-Authorization") != ""
					switch {
					case req.Method == http.MethodConnect && !auth:
						_, _ = c.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n"))
					case req.Method == http.MethodConnect && req.Host == "probe.test:443" && heldOnce.CompareAndSwap(false, true):
						close(held)
						_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
						_, _ = io.Copy(io.Discard, c)
						return
					case req.Method == http.MethodConnect:
						_, _ = c.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
					default:
						up.probeMu.Lock()
						running := up.probeDone != nil
						up.probeMu.Unlock()
						mu.Lock()
						runs = append(runs, running)
						mu.Unlock()
						_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nok"))
					}
				}
			}()
		}
	}()

	d := &net.Dialer{}
	p.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if first.CompareAndSwap(false, true) {
			close(dialling)
			<-release
		}
		if addr == "203.0.113.10:3128" {
			return d.DialContext(ctx, network, l.Addr().String())
		}
		return nil, errors.New("unexpected dial " + addr)
	}

	addr, err := p.Start()
	require.NoError(t, err)
	defer func() { _ = p.Close() }()
	pu, err := url.Parse("http://" + addr)
	require.NoError(t, err)
	c := &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true}}
	go func() {
		resp, err := c.Get("http://pub.test/x")
		if err != nil {
			status <- -1
			return
		}
		_ = resp.Body.Close()
		status <- resp.StatusCode
	}()

	select {
	case <-dialling:
	case <-time.After(5 * time.Second):
		t.Fatal("the by-name request never dialled the upstream")
	}
	go func() { p.probeAuth(context.Background()); close(probed) }()
	select {
	case <-held:
	case <-time.After(5 * time.Second):
		t.Fatal("the probe never reached the upstream with credentials")
	}
	close(release)
	assert.Equal(t, http.StatusOK, <-status)
	<-probed

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, runs, 1, "the request reached the upstream once")
	assert.False(t, runs[0], "the by-name request was written while the probe was running")
}

func TestParseAuthProbe(t *testing.T) {
	for _, s := range []string{"h:1", "h:65535", "1.2.3.4:443", "[2001:db8::1]:443"} {
		assert.NoError(t, ParseAuthProbe(s), s)
	}
	for _, s := range []string{
		"zq9", ":443", "zq9:0", "zq9:+1", "zq9:01", "zq9:65536", "zq9:x", "zq 9:1", "localhost:1", "127.0.0.1:1",
		"[::1]:1", "0.0.0.0:1", "[zq9]:1", "[1.2.3.4]:443", "localhost.:1", "127.1:1", "0x7f000001:1", "127.0.0.1.:1",
		"[fe80::1%eth0]:443", "[fe80::1%25eth0]:443",
	} {
		err := ParseAuthProbe(s)
		require.Error(t, err, s)
		if strings.HasPrefix(s, "zq") {
			assert.NotContains(t, err.Error(), s)
		}
	}
}
