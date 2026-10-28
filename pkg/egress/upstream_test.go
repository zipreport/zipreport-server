package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
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
