package test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"zipreport-server/pkg/egress"
	"zipreport-server/pkg/render"
	"zipreport-server/pkg/zpt"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustRule(t *testing.T, s string) egress.Rule {
	t.Helper()
	r, err := egress.ParseRule(s)
	require.NoError(t, err)
	return r
}

func TestIsPublicAddr(t *testing.T) {
	notPublic := []string{
		"127.0.0.1", "127.0.0.2", "::1", "::ffff:127.0.0.1",
		"0.0.0.0", "0.1.2.3", "::",
		"10.0.0.5", "172.16.0.1", "192.168.1.1",
		"169.254.169.254", "fe80::1",
		"100.64.0.1", "::ffff:100.64.0.1",
		"192.0.0.1", "198.18.0.1", "240.0.0.1", "255.255.255.255",
		"168.63.129.16",
		"fd00::1", "fd00:ec2::254", "fec0::1",
		"224.0.0.1", "ff02::1",
		"64:ff9b::7f00:1", "64:ff9b:1::1",
		"2002:7f00:1::",
		"::7f00:1", "::a00:5",
	}
	for _, s := range notPublic {
		assert.False(t, egress.IsPublicAddr(netip.MustParseAddr(s)), s)
	}
	assert.False(t, egress.IsPublicAddr(netip.Addr{}), "zero Addr")

	for _, s := range []string{"8.8.8.8", "1.1.1.1", "2001:4860:4860::8888", "64:ff9b::808:808", "2002:808:808::"} {
		assert.True(t, egress.IsPublicAddr(netip.MustParseAddr(s)), s)
	}
}

func TestEgressParseRule(t *testing.T) {
	for _, s := range []string{"10.0.0.0/8", "fd00::/8", "assets.intranet:443", "[::1]:8080"} {
		_, err := egress.ParseRule(s)
		assert.NoError(t, err, s)
	}
	for _, s := range []string{"10.0.0.5", "host", "host:0", "host:70000", ":443", ""} {
		_, err := egress.ParseRule(s)
		assert.Error(t, err, "%q", s)
	}
}

func TestZipReportConfig_EgressAllow(t *testing.T) {
	cfg := configWithSecret(strongSecret)
	cfg.ZipReport.EgressAllow = []string{"10.0.0.0/8"}
	assert.NoError(t, cfg.Validate())

	cfg.ZipReport.EgressAllow = []string{"bad"}
	err := cfg.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "egressAllow[0]")
}

// --- proxy tests ---

type hit struct {
	host   string
	header http.Header
}

type proxyEnv struct {
	proxy    *egress.Proxy
	addr     string
	client   *http.Client
	ownPort  int
	ownHits  atomic.Int32
	mu       sync.Mutex
	last     hit
	redirect atomic.Value // string
	lookups  atomic.Int32
}

type proxyOpts struct {
	policy    egress.Policy
	poolFirst int // 0: the own server's port
	poolCount int // 0: 1
	lookup    map[string][]string
	dial      egress.DialFunc
	ifaces    []net.Addr
	ownDial   string
	idle      time.Duration

	upstream           *egress.Upstream
	upstreamTLS        *tls.Config
	upstreamTLSTimeout time.Duration
	lookupFn           egress.LookupFunc // replaces lookup when set
}

func ipnet(t *testing.T, cidr string) net.Addr {
	t.Helper()
	ip, n, err := net.ParseCIDR(cidr)
	require.NoError(t, err)
	return &net.IPNet{IP: ip, Mask: n.Mask}
}

func startProxy(t *testing.T, o proxyOpts) *proxyEnv {
	t.Helper()
	env := &proxyEnv{}
	own := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.ownHits.Add(1)
		env.mu.Lock()
		env.last = hit{host: r.Host, header: r.Header.Clone()}
		env.mu.Unlock()
		if loc, _ := env.redirect.Load().(string); loc != "" {
			http.Redirect(w, r, loc, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("own"))
	}))
	t.Cleanup(own.Close)
	env.ownPort = own.Listener.Addr().(*net.TCPAddr).Port

	if o.poolFirst == 0 {
		o.poolFirst = env.ownPort
	}
	if o.poolCount == 0 {
		o.poolCount = 1
	}
	ownDial := o.ownDial
	if ownDial == "" {
		ownDial = "127.0.0.1:" + strconv.Itoa(env.ownPort)
	}
	if o.upstream != nil {
		o.policy.Upstream = o.upstream
	}
	p := egress.New("localhost:"+strconv.Itoa(env.ownPort), ownDial, o.poolFirst, o.poolCount, o.policy)
	p.UpstreamTLS = o.upstreamTLS
	p.UpstreamTLSTimeout = o.upstreamTLSTimeout
	p.Lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
		env.lookups.Add(1)
		ips, ok := o.lookup[host]
		if !ok {
			return nil, errors.New("no such host")
		}
		var out []netip.Addr
		for _, s := range ips {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
	if o.lookupFn != nil {
		p.Lookup = o.lookupFn
	}
	if o.dial != nil {
		p.Dial = o.dial
	}
	ifaces := o.ifaces
	if ifaces == nil {
		ifaces = []net.Addr{ipnet(t, "192.0.2.1/24")}
	}
	p.InterfaceAddrs = func() ([]net.Addr, error) { return ifaces, nil }
	if o.idle > 0 {
		p.TunnelIdle = o.idle
	}
	addr, err := p.Start()
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	pu, err := url.Parse("http://" + addr)
	require.NoError(t, err)
	env.proxy = p
	env.addr = addr
	env.client = &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true},
	}
	return env
}

func (e *proxyEnv) ownURL(path string) string {
	return "http://localhost:" + strconv.Itoa(e.ownPort) + path
}

// newDecoy returns a server that counts accepted connections, so a tunnel counts as much as a request.
func newDecoy(t *testing.T) (*httptest.Server, *atomic.Int32, int) {
	t.Helper()
	var n atomic.Int32
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("decoy"))
	}))
	s.Config.ConnState = func(_ net.Conn, st http.ConnState) {
		if st == http.StateNew {
			n.Add(1)
		}
	}
	s.Start()
	t.Cleanup(s.Close)
	return s, &n, s.Listener.Addr().(*net.TCPAddr).Port
}

func status(t *testing.T, c *http.Client, u string) int {
	t.Helper()
	resp, err := c.Get(u)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode
}

func rawConnect(t *testing.T, proxyAddr, target, trailing string) (net.Conn, *bufio.Reader, int) {
	t.Helper()
	c, err := net.Dial("tcp", proxyAddr)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	_, err = fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n%s", target, target, trailing)
	require.NoError(t, err)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
	require.NoError(t, err)
	return c, br, resp.StatusCode
}

func TestEgressProxy_AllowsOwnHost(t *testing.T) {
	env := startProxy(t, proxyOpts{})
	assert.Equal(t, 200, status(t, env.client, env.ownURL("/")))
	env.mu.Lock()
	defer env.mu.Unlock()
	assert.Equal(t, "localhost:"+strconv.Itoa(env.ownPort), env.last.host)
	assert.True(t, env.proxy.SawOwnOrigin())
}

func TestEgressProxy_OwnHostDialFailureClosesWithoutResponse(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	closedAddr := l.Addr().String()
	require.NoError(t, l.Close())

	env := startProxy(t, proxyOpts{ownDial: closedAddr})
	resp, err := env.client.Get(env.ownURL("/"))
	if err == nil {
		_ = resp.Body.Close()
		t.Fatalf("expected a transport error, got HTTP %d", resp.StatusCode)
	}
}

func TestEgressProxy_BlocksOtherLoopbackPort(t *testing.T) {
	_, hits, port := newDecoy(t)
	env := startProxy(t, proxyOpts{})
	assert.Equal(t, 403, status(t, env.client, fmt.Sprintf("http://127.0.0.1:%d/x", port)))
	assert.Equal(t, int32(0), hits.Load())
	n, samples := env.proxy.Blocked()
	assert.Equal(t, 1, n)
	require.Len(t, samples, 1)
	assert.Contains(t, samples[0], "/x")
}

func TestEgressProxy_BlocksNameResolvingToLoopback(t *testing.T) {
	_, hits, port := newDecoy(t)
	env := startProxy(t, proxyOpts{lookup: map[string][]string{"internal.test": {"127.0.0.1"}}})
	assert.Equal(t, 403, status(t, env.client, fmt.Sprintf("http://internal.test:%d/", port)))
	assert.Equal(t, int32(0), hits.Load())
}

func TestEgressProxy_BlocksLocalInterfaceAddr(t *testing.T) {
	var dialed []string
	var mu sync.Mutex
	dial := func(_ context.Context, _, addr string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, addr)
		mu.Unlock()
		return nil, errors.New("no network in tests")
	}
	lookup := map[string][]string{"self.test": {"203.0.113.7"}}

	env := startProxy(t, proxyOpts{lookup: lookup, dial: dial, ifaces: []net.Addr{ipnet(t, "203.0.113.7/24")}})
	assert.Equal(t, 403, status(t, env.client, "http://self.test/"))
	mu.Lock()
	assert.Empty(t, dialed, "a local interface address must never be dialled")
	mu.Unlock()

	// control: the same name is dialled when the address is not local
	env = startProxy(t, proxyOpts{lookup: lookup, dial: dial, ifaces: []net.Addr{ipnet(t, "192.0.2.1/24")}})
	assert.Equal(t, 502, status(t, env.client, "http://self.test/"))
	mu.Lock()
	assert.Equal(t, []string{"203.0.113.7:80"}, dialed)
	mu.Unlock()
}

func TestEgressProxy_BlocksMixedAnswer(t *testing.T) {
	var dials atomic.Int32
	env := startProxy(t, proxyOpts{
		lookup: map[string][]string{"mixed.test": {"8.8.8.8", "127.0.0.1"}},
		dial: func(context.Context, string, string) (net.Conn, error) {
			dials.Add(1)
			return nil, errors.New("no network in tests")
		},
	})
	assert.Equal(t, 403, status(t, env.client, "http://mixed.test/"))
	assert.Equal(t, int32(0), dials.Load())
}

func TestEgressProxy_DialsVettedIPsInOrder(t *testing.T) {
	var mu sync.Mutex
	var dialed []string
	env := startProxy(t, proxyOpts{
		lookup: map[string][]string{"pinned.test": {"8.8.8.8", "1.1.1.1"}},
		dial: func(_ context.Context, _, addr string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, addr)
			mu.Unlock()
			return nil, errors.New("no network in tests")
		},
	})
	assert.Equal(t, 502, status(t, env.client, "http://pinned.test/"))
	mu.Lock()
	assert.Equal(t, []string{"8.8.8.8:80", "1.1.1.1:80"}, dialed)
	mu.Unlock()
	assert.Equal(t, int32(1), env.lookups.Load())
}

func TestEgressProxy_RedirectHopIsVetted(t *testing.T) {
	_, hits, port := newDecoy(t)
	env := startProxy(t, proxyOpts{})
	env.redirect.Store(fmt.Sprintf("http://127.0.0.1:%d/r", port))
	assert.Equal(t, 403, status(t, env.client, env.ownURL("/")))
	assert.Equal(t, int32(0), hits.Load())
}

func TestEgressProxy_ConnectToOwnHostRefused(t *testing.T) {
	// loopback is allowed and the port is outside the pool, so only the own-host rule can refuse this
	env := startProxy(t, proxyOpts{
		policy:    egress.Policy{Allow: []egress.Rule{mustRule(t, "127.0.0.0/8")}},
		poolFirst: 1,
		lookup:    map[string][]string{"localhost": {"127.0.0.1"}},
	})
	_, _, code := rawConnect(t, env.addr, "localhost:"+strconv.Itoa(env.ownPort), "")
	assert.Equal(t, 403, code)
	n, _ := env.proxy.Blocked()
	assert.Equal(t, 1, n)
}

func TestEgressProxy_ConnectBlocked(t *testing.T) {
	_, hits, port := newDecoy(t)
	env := startProxy(t, proxyOpts{})
	_, _, code := rawConnect(t, env.addr, fmt.Sprintf("127.0.0.1:%d", port), "")
	assert.Equal(t, 403, code)
	assert.Equal(t, int32(0), hits.Load())
}

// pipeUpstream returns a Dial hook handing out one end of a net.Pipe, recording the dialled address;
// the other end answers the first request line it reads with a 204.
func pipeUpstream(dialed *atomic.Value) egress.DialFunc {
	return func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed.Store(addr)
		client, upstream := net.Pipe()
		go func() {
			defer func() { _ = upstream.Close() }()
			br := bufio.NewReader(upstream)
			if _, err := br.ReadString('\n'); err != nil {
				return
			}
			_, _ = upstream.Write([]byte("HTTP/1.1 204 No Content\r\n\r\n"))
			// keep the tunnel open until the proxy closes it
			_, _ = br.Discard(1 << 20)
		}()
		return client, nil
	}
}

func TestEgressProxy_ConnectDialsVettedIP(t *testing.T) {
	var dialed atomic.Value
	env := startProxy(t, proxyOpts{
		lookup: map[string][]string{"pub.test": {"8.8.8.8"}},
		dial:   pipeUpstream(&dialed),
	})
	// the GET rides in the same write as the CONNECT: it sits in the hijacked reader's buffer
	_, br, code := rawConnect(t, env.addr, "pub.test:443", "GET / HTTP/1.1\r\nHost: pub.test\r\n\r\n")
	require.Equal(t, 200, code)
	assert.Equal(t, "8.8.8.8:443", dialed.Load())
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	require.NoError(t, err)
	assert.Equal(t, 204, resp.StatusCode)
}

func TestEgressProxy_ConnectStreamingTunnelNotIdle(t *testing.T) {
	dial := func(_ context.Context, _, _ string) (net.Conn, error) {
		client, upstream := net.Pipe()
		go func() {
			defer func() { _ = upstream.Close() }()
			for i := 0; i < 10; i++ {
				time.Sleep(300 * time.Millisecond)
				if _, err := upstream.Write([]byte("x")); err != nil {
					return
				}
			}
		}()
		return client, nil
	}
	env := startProxy(t, proxyOpts{
		idle:   time.Second,
		lookup: map[string][]string{"pub.test": {"8.8.8.8"}},
		dial:   dial,
	})
	c, br, code := rawConnect(t, env.addr, "pub.test:443", "")
	require.Equal(t, 200, code)
	_ = c.SetDeadline(time.Now().Add(8 * time.Second))
	got, err := io.ReadAll(br)
	require.NoError(t, err)
	assert.Equal(t, 10, len(got), "an active download must not be cut as idle")
}

func TestEgressProxy_ConnectIdleTunnelCloses(t *testing.T) {
	dial := func(_ context.Context, _, _ string) (net.Conn, error) {
		client, upstream := net.Pipe()
		t.Cleanup(func() { _ = upstream.Close() })
		return client, nil
	}
	env := startProxy(t, proxyOpts{
		idle:   time.Second,
		lookup: map[string][]string{"pub.test": {"8.8.8.8"}},
		dial:   dial,
	})
	c, br, code := rawConnect(t, env.addr, "pub.test:443", "")
	require.Equal(t, 200, code)
	start := time.Now()
	_ = c.SetDeadline(time.Now().Add(2500 * time.Millisecond))
	_, err := br.ReadByte()
	require.Error(t, err)
	assert.False(t, errors.Is(err, os.ErrDeadlineExceeded), "tunnel must be closed by the proxy, not by the client deadline")
	assert.Less(t, time.Since(start), 2500*time.Millisecond)
}

func TestEgressProxy_HardDenyOverridesAllow(t *testing.T) {
	_, hits, port := newDecoy(t)
	allow := egress.Policy{Allow: []egress.Rule{mustRule(t, "127.0.0.0/8")}}
	u := fmt.Sprintf("http://127.0.0.1:%d/", port)

	env := startProxy(t, proxyOpts{policy: allow, poolFirst: port, poolCount: 1})
	assert.Equal(t, 403, status(t, env.client, u))
	assert.Equal(t, int32(0), hits.Load(), "pool port must stay closed")

	env = startProxy(t, proxyOpts{policy: allow})
	assert.Equal(t, 200, status(t, env.client, u))
	assert.Equal(t, int32(1), hits.Load())

	allow.ReservedPorts = []int{port}
	hits.Store(0)
	env = startProxy(t, proxyOpts{policy: allow})
	assert.Equal(t, 403, status(t, env.client, u))
	assert.Equal(t, int32(0), hits.Load(), "reserved port must stay closed")
}

func TestEgressProxy_ExactAllowPermitsLoopbackTestServer(t *testing.T) {
	_, hits, port := newDecoy(t)
	_, otherHits, otherPort := newDecoy(t)
	env := startProxy(t, proxyOpts{policy: egress.Policy{Allow: []egress.Rule{mustRule(t, fmt.Sprintf("127.0.0.1:%d", port))}}})
	assert.Equal(t, 200, status(t, env.client, fmt.Sprintf("http://127.0.0.1:%d/", port)))
	assert.Equal(t, int32(1), hits.Load())
	assert.Equal(t, 403, status(t, env.client, fmt.Sprintf("http://127.0.0.1:%d/", otherPort)))
	assert.Equal(t, int32(0), otherHits.Load())
}

func TestEgressProxy_HopByHopStripped(t *testing.T) {
	env := startProxy(t, proxyOpts{})
	req, err := http.NewRequest(http.MethodGet, env.ownURL("/"), nil)
	require.NoError(t, err)
	req.Header.Set("Connection", "X-Secret")
	req.Header.Set("X-Secret", "1")
	req.Header.Set("X-Other", "kept")
	resp, err := env.client.Do(req)
	require.NoError(t, err)
	_ = resp.Body.Close()

	env.mu.Lock()
	defer env.mu.Unlock()
	assert.Equal(t, "kept", env.last.header.Get("X-Other"), "control: ordinary headers pass through")
	assert.Empty(t, env.last.header.Get("X-Secret"), "headers named by Connection must be stripped")
	assert.Empty(t, env.last.header.Get("Connection"))
}

func TestEgressProxy_CloseEndsTunnels(t *testing.T) {
	var dialed atomic.Value
	env := startProxy(t, proxyOpts{
		lookup: map[string][]string{"pub.test": {"8.8.8.8"}},
		dial:   pipeUpstream(&dialed),
	})
	c, _, code := rawConnect(t, env.addr, "pub.test:443", "")
	require.Equal(t, 200, code)

	require.NoError(t, env.proxy.Close())
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, err := c.Read(make([]byte, 1))
	require.Error(t, err)
	var ne net.Error
	if errors.As(err, &ne) {
		assert.False(t, ne.Timeout(), "tunnel must be closed by Close(), not time out")
	}
}

func TestEgressProxy_DenyAll(t *testing.T) {
	env := startProxy(t, proxyOpts{})
	assert.Equal(t, 200, status(t, env.client, env.ownURL("/")), "control: allowed before DenyAll")
	env.proxy.DenyAll()
	assert.Equal(t, 403, status(t, env.client, env.ownURL("/")))
	c, err := net.Dial("tcp", env.addr)
	require.NoError(t, err)
	_ = c.Close()
}

func echoUpstream(dialed *atomic.Value) egress.DialFunc {
	return func(_ context.Context, _, addr string) (net.Conn, error) {
		dialed.Store(addr)
		client, upstream := net.Pipe()
		go func() {
			defer func() { _ = upstream.Close() }()
			_, _ = io.Copy(upstream, upstream)
		}()
		return client, nil
	}
}

func fastRetire(env *proxyEnv, attempts int) {
	env.proxy.RetireInterval = 10 * time.Millisecond
	env.proxy.RetireAttempts = attempts
}

func waitDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("retire loop did not finish")
	}
}

func TestEgressProxy_RetireClosesTunnels(t *testing.T) {
	var dialed atomic.Value
	env := startProxy(t, proxyOpts{
		lookup: map[string][]string{"pub.test": {"8.8.8.8"}},
		dial:   echoUpstream(&dialed),
	})
	fastRetire(env, 1)
	c, br, code := rawConnect(t, env.addr, "pub.test:443", "")
	require.Equal(t, 200, code)
	_, err := c.Write([]byte("ping\n"))
	require.NoError(t, err)
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "ping\n", line, "control: tunnel echoes before Retire")

	done := env.proxy.Retire(func(context.Context) error { return errors.New("busy") })
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	_, err = br.ReadByte()
	require.Error(t, err)
	var ne net.Error
	if errors.As(err, &ne) {
		assert.False(t, ne.Timeout(), "tunnel must be cut by Retire, not time out")
	}
	assert.Equal(t, 403, status(t, env.client, env.ownURL("/")))
	waitDone(t, done)
}

func TestEgressProxy_RetireAbortsForwards(t *testing.T) {
	chunk := bytes.Repeat([]byte("x"), 64<<10)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		_, _ = w.Write(chunk)
		fl.Flush()
		deadline := time.After(30 * time.Second)
		for {
			select {
			case <-r.Context().Done():
				return
			case <-deadline:
				return
			case <-time.After(100 * time.Millisecond):
				if _, err := w.Write(chunk); err != nil {
					return
				}
				fl.Flush()
			}
		}
	}))
	t.Cleanup(up.Close)
	port := up.Listener.Addr().(*net.TCPAddr).Port
	env := startProxy(t, proxyOpts{policy: egress.Policy{Allow: []egress.Rule{mustRule(t, fmt.Sprintf("127.0.0.1:%d", port))}}})
	fastRetire(env, 1)

	pu, err := url.Parse("http://" + env.addr)
	require.NoError(t, err)
	tr := &http.Transport{Proxy: http.ProxyURL(pu), DisableKeepAlives: true}
	t.Cleanup(tr.CloseIdleConnections)
	resp, err := (&http.Client{Transport: tr}).Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	_, err = io.ReadFull(resp.Body, make([]byte, len(chunk)))
	require.NoError(t, err)

	done := env.proxy.Retire(func(context.Context) error { return errors.New("busy") })
	start := time.Now()
	_, err = io.Copy(io.Discard, resp.Body)
	assert.Less(t, time.Since(start), time.Second)
	assert.ErrorIs(t, err, io.ErrUnexpectedEOF, "the body must be truncated, not end cleanly")
	waitDone(t, done)
}

func TestEgressProxy_RetireClosesAfterDispose(t *testing.T) {
	env := startProxy(t, proxyOpts{})
	fastRetire(env, 6)
	var calls atomic.Int32
	done := env.proxy.Retire(func(context.Context) error {
		if calls.Add(1) < 3 {
			return errors.New("busy")
		}
		return nil
	})
	waitDone(t, done)
	assert.Equal(t, int32(3), calls.Load())
	c, err := net.DialTimeout("tcp", env.addr, time.Second)
	if err == nil {
		_ = c.Close()
	}
	assert.Error(t, err, "listener must be closed once the context is disposed")
}

func TestEgressProxy_RetireGivesUp(t *testing.T) {
	env := startProxy(t, proxyOpts{})
	fastRetire(env, 3)
	var calls atomic.Int32
	done := env.proxy.Retire(func(context.Context) error {
		calls.Add(1)
		return errors.New("busy")
	})
	waitDone(t, done)
	assert.Equal(t, int32(3), calls.Load())
	assert.Equal(t, 403, status(t, env.client, env.ownURL("/")), "still listening in deny-all mode")
}

func TestEgressProxy_BlockedSamplesBoundedAndDistinct(t *testing.T) {
	env := startProxy(t, proxyOpts{})
	_, _, port := newDecoy(t)
	long := strings.Repeat("a", 300)
	for i := 0; i < 40; i++ {
		u := fmt.Sprintf("http://127.0.0.1:%d/%02d-%s", port, i/2, long) // each of the 20 paths twice in a row, so duplicates occur inside the first 16
		assert.Equal(t, 403, status(t, env.client, u))
	}
	n, samples := env.proxy.Blocked()
	assert.Equal(t, 40, n)
	assert.Len(t, samples, 16)
	seen := map[string]bool{}
	for _, s := range samples {
		assert.LessOrEqual(t, len(s), 120)
		assert.False(t, seen[s], "duplicate sample %q", s)
		seen[s] = true
	}
}

// --- real Chrome ---

// egressRenderJob renders an in-memory zip straight through the engine so the JobResult is visible.
func egressRenderJob(t *testing.T, engine *render.Engine, files map[string][]byte) *render.JobResult {
	t.Helper()
	b := buildZipFiles(t, files)
	reader, err := zpt.NewZptReader(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	job := render.NewRenderJob(reader, uuid.New())
	job.IndexFile = "index.html"
	job.UseJSEvent = true
	job.JsTimeoutS = 20
	return engine.RenderJob(job)
}

var egressDecoyPaths = []string{
	"/iframe-port", "/iframe-ip", "/iframe-name", "/img-port", "/fetch-port",
	"/worker", "/sw", "/beacon", "/keepalive", "/prefetch",
}

func TestRender_EgressBlocksInternal(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser-based egress test in short mode")
	}
	counters := map[string]*atomic.Int32{}
	mux := http.NewServeMux()
	for _, p := range egressDecoyPaths {
		c := &atomic.Int32{}
		counters[p] = c
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			c.Add(1)
			w.Header().Set("Content-Type", "text/html")
			w.Header().Set("Access-Control-Allow-Origin", "*")
			_, _ = w.Write([]byte("<html><body>decoy</body></html>"))
		})
	}
	decoyA := httptest.NewServer(mux)
	defer decoyA.Close()
	portA := decoyA.Listener.Addr().(*net.TCPAddr).Port

	var wsHits atomic.Int32
	decoyW := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { wsHits.Add(1) }))
	defer decoyW.Close()
	portW := decoyW.Listener.Addr().(*net.TCPAddr).Port

	_, engine, _, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()
	// stands in for a public name whose DNS answer points at loopback
	engine.SetEgressLookup(func(ctx context.Context, host string) ([]netip.Addr, error) {
		if host == "internal.test" {
			return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
		}
		return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	})

	index := fmt.Sprintf(`<!DOCTYPE html><html><head><meta charset="UTF-8">
<link rel="prefetch" href="http://internal.test:%[1]d/prefetch">
</head><body><div>P</div>
<iframe src="http://localhost:%[1]d/iframe-port"></iframe>
<iframe src="http://127.0.0.1:%[1]d/iframe-ip"></iframe>
<iframe src="http://internal.test:%[1]d/iframe-name"></iframe>
<img src="http://localhost:%[1]d/img-port">
<script>
var waits = [];
function wait(fn) { waits.push(new Promise(function (res) { try { fn(res); } catch (e) { res(); } })); }
wait(function (res) { window.addEventListener('load', function () { res(); }); });
wait(function (res) { var w = new Worker('worker.js'); w.onmessage = w.onerror = function () { res(); }; });
wait(function (res) {
  navigator.serviceWorker.register('sw.js').then(function (r) {
    if (r.installing) { r.installing.addEventListener('statechange', function () { res(); }); } else { res(); }
  }).catch(function () { res(); });
});
wait(function (res) { var ws = new WebSocket("ws://internal.test:%[2]d/ws"); ws.onclose = ws.onerror = function () { res(); }; });
wait(function (res) { setTimeout(res, 1500); });
fetch("http://127.0.0.1:%[1]d/fetch-port", {mode: "no-cors"}).catch(function () {});
navigator.sendBeacon("http://internal.test:%[1]d/beacon");
fetch("http://internal.test:%[1]d/keepalive", {keepalive: true, mode: "no-cors"}).catch(function () {});
Promise.all(waits).then(function () { console.log('zpt-view-ready'); });
</script></body></html>`, portA, portW)
	worker := fmt.Sprintf(`fetch("http://internal.test:%d/worker", {mode: "no-cors"}).catch(function () {}).then(function () { postMessage('done'); });`, portA)
	sw := fmt.Sprintf(`self.addEventListener('install', function (e) { e.waitUntil(fetch("http://internal.test:%d/sw", {mode: "no-cors"}).catch(function () {})); });`, portA)

	result := egressRenderJob(t, engine, map[string][]byte{
		"index.html": []byte(index), "worker.js": []byte(worker), "sw.js": []byte(sw),
	})
	require.NotNil(t, result)

	for _, p := range egressDecoyPaths {
		t.Logf("counter %-13s = %d", p, counters[p].Load())
	}
	t.Logf("counter /ws (W)       = %d", wsHits.Load())
	t.Logf("blocked=%d samples=%v", result.BlockedRequests, result.BlockedSamples)

	assert.True(t, result.EgressSawOwnOrigin, "the report must reach its own server through the proxy (job success=%v, err=%v)", result.Success, result.Error)
	require.True(t, result.Success, "job error: %v", result.Error)
	assert.True(t, isValidPDF(result.Output))

	for _, p := range egressDecoyPaths {
		assert.Equal(t, int32(0), counters[p].Load(), "decoy %s must never be reached", p)
	}
	assert.Equal(t, int32(0), wsHits.Load(), "websocket decoy must never be reached")

	assert.GreaterOrEqual(t, result.BlockedRequests, 11)
	joined := strings.Join(result.BlockedSamples, "\n")
	for _, p := range egressDecoyPaths {
		assert.Contains(t, joined, p, "no blocked sample for %s", p)
	}
	assert.Contains(t, result.BlockedSamples, fmt.Sprintf("internal.test:%d", portW))
}

func TestRender_FailsClosedWhenContextBypassesProxy(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser-based egress test in short mode")
	}
	var seen atomic.Int32
	rp := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		seen.Add(1) // absolute-form request: Out keeps the target URL and Host
	}}
	plain := httptest.NewServer(rp)
	defer plain.Close()

	_, engine, _, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()
	engine.SetContextProxy(func(string) string { return plain.URL })

	result := egressRenderJob(t, engine, map[string][]byte{
		"index.html": []byte(`<html><body><div>P</div><script>console.log('zpt-view-ready')</script></body></html>`),
	})
	require.NotNil(t, result)
	assert.False(t, result.Success)
	require.Error(t, result.Error)
	assert.Contains(t, result.Error.Error(), "did not pass through the egress proxy")
	assert.Greater(t, seen.Load(), int32(0), "control: the page must have loaded through the bypass proxy")
}

// --- upstream proxy chaining ---

// backend records the Host and request URI of every request and answers "backend".
type backend struct {
	srv   *httptest.Server
	mu    sync.Mutex
	seen  []string // "host uri"
	auths []string // Proxy-Authorization values that reached the origin
}

func newBackend(t *testing.T) *backend {
	t.Helper()
	b := &backend{}
	b.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b.mu.Lock()
		b.seen = append(b.seen, r.Host+" "+r.RequestURI)
		if v := r.Header.Get("Proxy-Authorization"); v != "" {
			b.auths = append(b.auths, v)
		}
		b.mu.Unlock()
		_, _ = w.Write([]byte("backend"))
	}))
	t.Cleanup(b.srv.Close)
	return b
}

func (b *backend) requests() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.seen...)
}

type upReq struct {
	method string
	target string // second token of the request line: ip:port / host:port for CONNECT, the absolute URL otherwise
	hosts  []string
	auths  []string
}

// fakeUpstream is an in-process HTTP proxy. It reads requests raw: http.ReadRequest would rewrite Host on
// absolute-form requests and hide what was actually sent.
type fakeUpstream struct {
	ln         net.Listener
	port       int
	conns      atomic.Int32
	closed     atomic.Int32 // connections ended
	mu         sync.Mutex
	reqs       []upReq
	refuse     map[string]bool // CONNECT target -> 403
	authAll    bool            // 407 to everything
	originAuth bool            // 407 to absolute-form requests only, as if relayed from the origin
	banner     string          // written in the same write as the CONNECT 200
	backend    string          // host:port every CONNECT is spliced to

	authTargets map[string]bool // CONNECT target -> 407 with or without credentials (the upstream's policy)
	requireAuth map[string]bool // CONNECT target -> 407 without credentials
	silent      map[string]bool // CONNECT target with credentials -> no answer
	rotated     atomic.Bool     // behave as authAll (the password changed)
}

type fakeUpstreamOpts struct {
	refuse     map[string]bool
	authAll    bool
	originAuth bool
	banner     string
	tls        *tls.Config

	authTargets map[string]bool
	requireAuth map[string]bool
	silent      map[string]bool
}

func newFakeUpstream(t *testing.T, b *backend, o fakeUpstreamOpts) *fakeUpstream {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	f := &fakeUpstream{
		port: l.Addr().(*net.TCPAddr).Port, refuse: o.refuse, authAll: o.authAll, originAuth: o.originAuth, banner: o.banner,
		authTargets: o.authTargets, requireAuth: o.requireAuth, silent: o.silent,
		backend: b.srv.Listener.Addr().String(),
	}
	f.ln = l
	if o.tls != nil {
		f.ln = tls.NewListener(l, o.tls)
	}
	t.Cleanup(func() { _ = f.ln.Close() })
	go func() {
		for {
			c, err := f.ln.Accept()
			if err != nil {
				return
			}
			f.conns.Add(1)
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeUpstream) serve(c net.Conn) {
	defer func() { _ = c.Close(); f.closed.Add(1) }()
	br := bufio.NewReader(c)
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			return
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			return
		}
		r := upReq{method: parts[0], target: parts[1]}
		for {
			h, err := br.ReadString('\n')
			if err != nil {
				return
			}
			h = strings.TrimRight(h, "\r\n")
			if h == "" {
				break
			}
			name, val, _ := strings.Cut(h, ":")
			val = strings.TrimSpace(val)
			switch strings.ToLower(name) {
			case "host":
				r.hosts = append(r.hosts, val)
			case "proxy-authorization":
				r.auths = append(r.auths, val)
			}
		}
		f.mu.Lock()
		f.reqs = append(f.reqs, r)
		f.mu.Unlock()

		const resp407 = "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"x\"\r\nContent-Length: 0\r\n\r\n"
		f.mu.Lock()
		silent := f.silent[r.target]
		f.mu.Unlock()
		switch {
		case f.authAll || f.rotated.Load():
			_, _ = c.Write([]byte(resp407))
			return
		case r.method == http.MethodConnect && f.authTargets[r.target]:
			_, _ = c.Write([]byte(resp407))
			return
		case r.method == http.MethodConnect && f.requireAuth[r.target] && len(r.auths) == 0:
			_, _ = c.Write([]byte(resp407))
			return
		case r.method == http.MethodConnect && silent && len(r.auths) > 0:
			_, _ = io.Copy(io.Discard, br)
			return
		}
		if r.method == http.MethodConnect {
			if f.refuse[r.target] {
				_, _ = c.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
				return
			}
			up, err := net.Dial("tcp", f.backend)
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n" + f.banner))
			go func() { _, _ = io.Copy(up, br); _ = up.Close() }()
			_, _ = io.Copy(c, up)
			_ = up.Close()
			return
		}
		if f.originAuth {
			_, _ = c.Write([]byte("HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n"))
			continue
		}
		_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 8\r\n\r\nupstream"))
	}
}

func (f *fakeUpstream) requests() []upReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upReq(nil), f.reqs...)
}

func (f *fakeUpstream) targets() []string {
	var out []string
	for _, r := range f.requests() {
		out = append(out, r.target)
	}
	return out
}

// proxyURL is the upstream's URL as the operator would configure it, with host as the name part.
func (f *fakeUpstream) proxyURL(scheme, userinfo, host string) string {
	return fmt.Sprintf("%s://%s%s:%d", scheme, userinfo, host, f.port)
}

// dialLog records every address that reached the generic "fake public internet" branch of the dial seam.
type dialLog struct {
	mu    sync.Mutex
	addrs []string
}

func (d *dialLog) all() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addrs...)
}

// routingDial sends the upstream's address and any other loopback address to a real dialer and records
// everything else (fake public / private IPs) before handing out a pipe double: a direct dial is observable
// and nothing touches the network.
func routingDial(f *fakeUpstream, log *dialLog) egress.DialFunc {
	var real net.Dialer
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, _ := net.SplitHostPort(addr)
		if f != nil && port == strconv.Itoa(f.port) && (host == "127.0.0.1" || host == "127.0.0.2" || host == "203.0.113.10") {
			return real.DialContext(ctx, network, "127.0.0.1:"+port)
		}
		if host == "127.0.0.1" {
			return real.DialContext(ctx, network, addr)
		}
		log.mu.Lock()
		log.addrs = append(log.addrs, addr)
		log.mu.Unlock()
		var sink atomic.Value
		return pipeUpstream(&sink)(ctx, network, addr)
	}
}

func mkUpstream(t *testing.T, rawURL, resolve string, noProxy ...string) *egress.Upstream {
	t.Helper()
	up, err := egress.ParseUpstream(rawURL, resolve, noProxy)
	require.NoError(t, err)
	require.NotNil(t, up)
	return up
}

// tunnelGet CONNECTs to target and issues one GET inside the tunnel.
func tunnelGet(t *testing.T, env *proxyEnv, target, host string) (int, string) {
	t.Helper()
	_, br, code := rawConnect(t, env.addr, target, "GET /t HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n")
	if code != 200 {
		return code, ""
	}
	resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodGet})
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func connectCode(t *testing.T, env *proxyEnv, target string) int {
	t.Helper()
	_, _, code := rawConnect(t, env.addr, target, "")
	return code
}

var byNameLookup = map[string][]string{
	"example.com": {"203.0.113.10"},
	"pub.test":    {"8.8.8.8"},
	"intra.test":  {"10.0.0.5"},
}

type upstreamHarness struct {
	env  *proxyEnv
	fu   *fakeUpstream
	be   *backend
	dial *dialLog
}

// startLocal starts a proxy chained, in local mode, to a fake upstream at 127.0.0.1.
func startLocal(t *testing.T, fo fakeUpstreamOpts, lookup map[string][]string, policy egress.Policy, noProxy ...string) *upstreamHarness {
	t.Helper()
	return startLocalAt(t, "127.0.0.1", fo, lookup, policy, noProxy...)
}

// startLocalAt is startLocal with the upstream addressed at host (a loopback address).
func startLocalAt(t *testing.T, host string, fo fakeUpstreamOpts, lookup map[string][]string, policy egress.Policy, noProxy ...string) *upstreamHarness {
	t.Helper()
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fo)
	log := &dialLog{}
	policy.Upstream = mkUpstream(t, fu.proxyURL("http", "", host), "local", noProxy...)
	env := startProxy(t, proxyOpts{policy: policy, lookup: lookup, dial: routingDial(fu, log)})
	return &upstreamHarness{env: env, fu: fu, be: be, dial: log}
}

// startByName starts a proxy chained, by name, to a fake upstream named example.com (203.0.113.10).
func startByName(t *testing.T, fo fakeUpstreamOpts, lookup map[string][]string, policy egress.Policy, noProxy ...string) *upstreamHarness {
	t.Helper()
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fo)
	log := &dialLog{}
	policy.Upstream = mkUpstream(t, fu.proxyURL("http", "", "example.com"), "upstream", noProxy...)
	env := startProxy(t, proxyOpts{policy: policy, lookup: lookup, dial: routingDial(fu, log)})
	return &upstreamHarness{env: env, fu: fu, be: be, dial: log}
}

func TestEgressUpstream_UnvettableNameRefused(t *testing.T) {
	for _, tc := range []struct{ name, req string }{
		{"empty_url_host", "GET http:///x HTTP/1.1\r\nHost: 127.0.0.1:9222\r\n\r\n"},
		{"port_only_url", "GET http://:80/x HTTP/1.1\r\nHost: 127.0.0.1:9222\r\n\r\n"},
		{"port_only_connect", "CONNECT :443 HTTP/1.1\r\nHost: :443\r\n\r\n"},
		{"short_ipv4_connect", "CONNECT 127.1:80 HTTP/1.1\r\nHost: 127.1:80\r\n\r\n"},
		{"short_ipv4_get", "GET http://127.1/x HTTP/1.1\r\nHost: 127.1\r\n\r\n"},
		{"decimal_ipv4", "GET http://2130706433/x HTTP/1.1\r\nHost: 2130706433\r\n\r\n"},
		{"lone_dot", "GET http://./x HTTP/1.1\r\nHost: .\r\n\r\n"},
		{"non_ascii", "GET http://\xc3\xa9xample.test/x HTTP/1.1\r\nHost: x\r\n\r\n"},
		{"escaped_connect_host", "CONNECT /x HTTP/1.1\r\nHost: %31%32%37.0.0.1:80\r\n\r\n"},
		{"dotted_ipv4_get", "GET http://10.0.0.3./x HTTP/1.1\r\nHost: 10.0.0.3.\r\n\r\n"},
		{"dotted_ipv4_connect", "CONNECT 10.0.0.3.:443 HTTP/1.1\r\nHost: 10.0.0.3.:443\r\n\r\n"},
		{"hex_ipv4_get", "GET http://0x7f000001/x HTTP/1.1\r\nHost: 0x7f000001\r\n\r\n"},
		{"hex_last_label_connect", "CONNECT 127.0.0.0x1:80 HTTP/1.1\r\nHost: 127.0.0.0x1:80\r\n\r\n"},
		{"escaped_loopback_name", "CONNECT /x HTTP/1.1\r\nHost: %6c%6f%63%61%6c%68%6f%73%74:80\r\n\r\n"},
		{"empty_label_get", "GET http://a..b/x HTTP/1.1\r\nHost: a..b\r\n\r\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := startByName(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{})
			c, err := net.Dial("tcp", h.env.addr)
			require.NoError(t, err)
			defer func() { _ = c.Close() }()
			_ = c.SetDeadline(time.Now().Add(5 * time.Second))
			_, err = c.Write([]byte(tc.req))
			require.NoError(t, err)
			resp, err := http.ReadResponse(bufio.NewReader(c), nil)
			require.NoError(t, err)
			assert.Equal(t, http.StatusForbidden, resp.StatusCode)
			assert.Empty(t, h.fu.requests(), "the upstream must not receive a name the proxy could not vet")
		})
	}
	t.Run("plain_name_still_forwarded", func(t *testing.T) {
		h := startByName(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{})
		assert.Equal(t, 200, status(t, h.env.client, "http://pub.test/x"))
		assert.Len(t, h.fu.requests(), 1)
	})
}

func TestEgressUpstream_ConnectSendsVettedIP(t *testing.T) {
	h := startLocal(t, fakeUpstreamOpts{}, map[string][]string{"pub.test": {"8.8.8.8"}}, egress.Policy{})
	code, body := tunnelGet(t, h.env, "pub.test:443", "pub.test")
	require.Equal(t, 200, code)
	assert.Equal(t, "backend", body)
	assert.Equal(t, []string{"8.8.8.8:443"}, h.fu.targets())
	assert.Empty(t, h.dial.all(), "no direct dial of the public address")
}

func TestEgressUpstream_ByNameSendsHostName(t *testing.T) {
	h := startByName(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{})
	require.Equal(t, 200, connectCode(t, h.env, "pub.test:443"))
	assert.Equal(t, []string{"pub.test:443"}, h.fu.targets())
	assert.Empty(t, h.dial.all())
}

func TestEgressUpstream_ByNameUnresolvableForwardedByName(t *testing.T) {
	h := startByName(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{})
	require.Equal(t, 200, connectCode(t, h.env, "only-proxy.test:443"))
	assert.Equal(t, []string{"only-proxy.test:443"}, h.fu.targets())

	t.Run("exact_noproxy", func(t *testing.T) {
		h := startByName(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{}, "only-proxy.test:443")
		assert.Equal(t, 403, connectCode(t, h.env, "only-proxy.test:443"))
		assert.Equal(t, int32(0), h.fu.conns.Load())
	})
}

func TestEgressUpstream_ByNameStillRefusesPrivate(t *testing.T) {
	h := startByName(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{})
	assert.Equal(t, 403, connectCode(t, h.env, "intra.test:443"))
	assert.Equal(t, int32(0), h.fu.conns.Load())
	n, _ := h.env.proxy.Blocked()
	assert.Equal(t, 1, n)
}

func TestEgressUpstream_LocalModeRefusesUnresolvable(t *testing.T) {
	h := startLocal(t, fakeUpstreamOpts{}, map[string][]string{}, egress.Policy{})
	assert.Equal(t, 403, connectCode(t, h.env, "only-proxy.test:443"))
	assert.Equal(t, int32(0), h.fu.conns.Load())
}

func TestEgressUpstream_RefusesLoopbackNamesAndAddrs(t *testing.T) {
	h := startByName(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{})
	for _, target := range []string{
		"localhost:9", "localhost.:9", "LOCALHOST:9", "x.localhost:9", "x.localhost.:9", "localhost.localdomain:9",
		"localhost4:9", "localhost6:9", "127.0.0.1:9", "[::1]:9", "[::ffff:127.0.0.1]:9",
	} {
		assert.Equal(t, 403, connectCode(t, h.env, target), target)
	}
	assert.Equal(t, int32(0), h.fu.conns.Load())
}

func TestEgressUpstream_ByNameRefusesHardDeniedPorts(t *testing.T) {
	h := startByName(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{ReservedPorts: []int{6543}})
	assert.Equal(t, 403, connectCode(t, h.env, "pub.test:6543"))
	assert.Equal(t, 403, connectCode(t, h.env, "only-proxy.test:6543"))
	assert.Equal(t, int32(0), h.fu.conns.Load())
	assert.Equal(t, 200, connectCode(t, h.env, "pub.test:443"), "control")
	assert.Equal(t, int32(1), h.fu.conns.Load())
}

func TestEgressUpstream_UpstreamAddressRefused(t *testing.T) {
	t.Run("local", func(t *testing.T) {
		// the upstream is addressed by name so only the address check, not the name check, can refuse 127.0.0.1
		_, hits, otherPort := newDecoy(t) // a live server on another port of the upstream address
		h := startLocalAt(t, "proxy.test", fakeUpstreamOpts{}, map[string][]string{"proxy.test": {"127.0.0.1"}}, egress.Policy{Allow: []egress.Rule{mustRule(t, "127.0.0.0/8")}})
		assert.Equal(t, 403, connectCode(t, h.env, fmt.Sprintf("127.0.0.1:%d", h.fu.port)))
		assert.Equal(t, 403, connectCode(t, h.env, fmt.Sprintf("127.0.0.1:%d", otherPort)))
		assert.Equal(t, int32(0), hits.Load())
		assert.Equal(t, int32(0), h.fu.conns.Load())
		assert.Empty(t, h.dial.all())
	})
	t.Run("by_name", func(t *testing.T) {
		h := startByName(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{Allow: []egress.Rule{mustRule(t, "203.0.113.0/24")}})
		for _, target := range []string{
			fmt.Sprintf("203.0.113.10:%d", h.fu.port), "203.0.113.10:22",
			fmt.Sprintf("example.com:%d", h.fu.port), "example.com.:443", "EXAMPLE.com:443",
		} {
			assert.Equal(t, 403, connectCode(t, h.env, target), target)
		}
		assert.Equal(t, int32(0), h.fu.conns.Load())
		assert.Empty(t, h.dial.all())
	})
}

func TestEgressUpstream_EgressAllowedStaysDirect(t *testing.T) {
	t.Run("local_loopback", func(t *testing.T) {
		_, hits, port := newDecoy(t)
		// the upstream sits at 127.0.0.2: its own address is refused on every port, so the allowed loopback
		// destination must be a different address
		h := startLocalAt(t, "127.0.0.2", fakeUpstreamOpts{}, nil, egress.Policy{Allow: []egress.Rule{mustRule(t, fmt.Sprintf("127.0.0.1:%d", port))}})
		assert.Equal(t, 200, status(t, h.env.client, fmt.Sprintf("http://127.0.0.1:%d/", port)))
		assert.Equal(t, int32(1), hits.Load())
		assert.Equal(t, int32(0), h.fu.conns.Load())
	})
	t.Run("local_private", func(t *testing.T) {
		h := startLocal(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{Allow: []egress.Rule{mustRule(t, "10.0.0.0/8")}})
		assert.Equal(t, 200, connectCode(t, h.env, "intra.test:443"))
		assert.Equal(t, []string{"10.0.0.5:443"}, h.dial.all())
		assert.Equal(t, int32(0), h.fu.conns.Load())
	})
	t.Run("by_name_exact", func(t *testing.T) {
		_, hits, port := newDecoy(t)
		lookup := map[string][]string{"example.com": {"203.0.113.10"}, "localhost": {"127.0.0.1"}}
		h := startByName(t, fakeUpstreamOpts{}, lookup, egress.Policy{Allow: []egress.Rule{mustRule(t, fmt.Sprintf("localhost:%d", port))}})
		assert.Equal(t, 200, status(t, h.env.client, fmt.Sprintf("http://localhost:%d/", port)))
		assert.Equal(t, int32(1), hits.Load())
		assert.Equal(t, int32(0), h.fu.conns.Load())
	})
	t.Run("by_name_cidr", func(t *testing.T) {
		h := startByName(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{Allow: []egress.Rule{mustRule(t, "10.0.0.0/8")}})
		assert.Equal(t, 200, connectCode(t, h.env, "intra.test:443"))
		assert.Equal(t, []string{"10.0.0.5:443"}, h.dial.all())
		assert.Equal(t, int32(0), h.fu.conns.Load())
	})
}

func TestEgressUpstream_ForwardLocalModeTunnels(t *testing.T) {
	h := startLocal(t, fakeUpstreamOpts{}, map[string][]string{"pub.test": {"8.8.8.8"}}, egress.Policy{})
	resp, err := h.env.client.Get("http://pub.test/x?q=1")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "backend", string(body))
	assert.Equal(t, []string{"8.8.8.8:80"}, h.fu.targets())
	assert.Equal(t, []string{"pub.test /x?q=1"}, h.be.requests())
}

func TestEgressUpstream_ForwardTriesNextIP(t *testing.T) {
	h := startLocal(t, fakeUpstreamOpts{refuse: map[string]bool{"8.8.8.8:80": true}},
		map[string][]string{"pub.test": {"8.8.8.8", "8.8.4.4"}}, egress.Policy{})
	resp, err := h.env.client.Get("http://pub.test/x")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "backend", string(body))
	assert.Equal(t, []string{"8.8.8.8:80", "8.8.4.4:80"}, h.fu.targets())
}

func TestEgressUpstream_ForwardByNameAbsoluteForm(t *testing.T) {
	h := startByName(t, fakeUpstreamOpts{}, byNameLookup, egress.Policy{})
	resp, err := h.env.client.Get("http://pub.test/x")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, "upstream", string(body))
	reqs := h.fu.requests()
	require.Len(t, reqs, 1)
	assert.True(t, strings.HasPrefix(reqs[0].target, "http://pub.test"), reqs[0].target)
}

func TestEgressUpstream_ProxyAuthorization(t *testing.T) {
	const want = "Basic dXNlcjpzM2NyZXQ=" // user:s3cret

	t.Run("local_connect", func(t *testing.T) {
		be := newBackend(t)
		fu := newFakeUpstream(t, be, fakeUpstreamOpts{})
		up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "127.0.0.1"), "local")
		env := startProxy(t, proxyOpts{
			policy: egress.Policy{Upstream: up}, lookup: map[string][]string{"pub.test": {"8.8.8.8"}},
			dial: routingDial(fu, &dialLog{}),
		})
		require.Equal(t, 200, connectCode(t, env, "pub.test:443"))
		reqs := fu.requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, []string{want}, reqs[0].auths)
	})
	t.Run("by_name_forward", func(t *testing.T) {
		be := newBackend(t)
		fu := newFakeUpstream(t, be, fakeUpstreamOpts{})
		up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "example.com"), "upstream")
		env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: byNameLookup, dial: routingDial(fu, &dialLog{})})
		req, err := http.NewRequest(http.MethodGet, "http://pub.test/x", nil)
		require.NoError(t, err)
		req.Header.Set("Proxy-Authorization", "Basic ZXZpbA==") // the page's own value must not reach the upstream
		resp, err := env.client.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		reqs := fu.requests()
		require.Len(t, reqs, 1)
		assert.Equal(t, []string{want}, reqs[0].auths)
	})
	// without upstream credentials net/http adds no header of its own, so only stripping keeps the page's out
	pageAuth := func(t *testing.T, env *proxyEnv) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, "http://pub.test/x", nil)
		require.NoError(t, err)
		req.Header.Set("Proxy-Authorization", "Basic ZXZpbA==")
		resp, err := env.client.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
	}
	t.Run("by_name_forward_page_value_stripped", func(t *testing.T) {
		be := newBackend(t)
		fu := newFakeUpstream(t, be, fakeUpstreamOpts{})
		up := mkUpstream(t, fu.proxyURL("http", "", "example.com"), "upstream")
		env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: byNameLookup, dial: routingDial(fu, &dialLog{})})
		pageAuth(t, env)
		reqs := fu.requests()
		require.Len(t, reqs, 1)
		assert.Empty(t, reqs[0].auths)
	})
	t.Run("local_forward_page_value_stripped", func(t *testing.T) {
		h := startLocal(t, fakeUpstreamOpts{}, map[string][]string{"pub.test": {"8.8.8.8"}}, egress.Policy{})
		pageAuth(t, h.env)
		require.Len(t, h.be.requests(), 1)
		assert.Empty(t, h.be.auths, "the page's value must not reach the origin through the tunnel")
	})
}

func TestEgressUpstream_AuthFailureNotRetried(t *testing.T) {
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{authAll: true})
	up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "127.0.0.1"), "local")
	env := startProxy(t, proxyOpts{
		policy: egress.Policy{Upstream: up}, lookup: map[string][]string{"pub.test": {"8.8.8.8", "8.8.4.4"}},
		dial: routingDial(fu, &dialLog{}),
	})
	assert.Equal(t, 502, connectCode(t, env, "pub.test:443"))
	assert.Len(t, fu.requests(), 1)
	n, last := env.proxy.UpstreamErrors()
	assert.Equal(t, 1, n)
	assert.True(t, strings.HasPrefix(last, "407"), last)
	assert.NotContains(t, last, "s3cret")
}

// covers the rule without authProbe
func TestEgressUpstream_AuthFailureLatched(t *testing.T) {
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{authAll: true})
	up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "127.0.0.1"), "local")
	lookup := map[string][]string{
		"pub.test": {"8.8.8.8"}, "b.test": {"1.1.1.1"}, "c.test": {"9.9.9.9"}, "d.test": {"4.4.4.4"},
	}
	start := func() *proxyEnv {
		return startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: lookup, dial: routingDial(fu, &dialLog{})})
	}
	env1, env2 := start(), start()

	assert.Equal(t, 502, connectCode(t, env1, "pub.test:443"))
	require.Len(t, fu.requests(), 1)
	conns := fu.conns.Load()
	assert.Equal(t, 502, connectCode(t, env2, "pub.test:443"))
	assert.Len(t, fu.requests(), 1, "the refused target is blocked for every job")
	assert.Never(t, func() bool { return fu.conns.Load() != conns }, 200*time.Millisecond, 10*time.Millisecond,
		"a blocked destination opens no connection to the upstream")
	n, last := env2.proxy.UpstreamErrors()
	assert.Equal(t, 1, n)
	assert.Contains(t, last, "latched")

	// one destination's 407 may be the upstream's policy for it: other targets are still tried, until a wrong
	// password shows (407s on three distinct targets) and every target is blocked
	assert.Equal(t, 502, connectCode(t, env2, "b.test:443"))
	assert.Len(t, fu.requests(), 2)
	assert.Equal(t, 502, connectCode(t, env2, "c.test:443"))
	assert.Len(t, fu.requests(), 3)
	connsD := fu.conns.Load() // its own variable: the earlier Never closure still reads conns
	assert.Equal(t, 502, connectCode(t, env1, "d.test:443"))
	assert.Len(t, fu.requests(), 3, "latched: no further logins")
	assert.Never(t, func() bool { return fu.conns.Load() != connsD }, 200*time.Millisecond, 10*time.Millisecond,
		"a blocked destination opens no connection to the upstream")
}

func TestEgressUpstream_AuthWithoutCredentialsNeverBlocksAll(t *testing.T) {
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{authAll: true})
	up := mkUpstream(t, fu.proxyURL("http", "", "127.0.0.1"), "local")
	lookup := map[string][]string{
		"pub.test": {"8.8.8.8"}, "b.test": {"1.1.1.1"}, "c.test": {"9.9.9.9"}, "d.test": {"4.4.4.4"},
	}
	env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: lookup, dial: routingDial(fu, &dialLog{})})
	for i, target := range []string{"pub.test:443", "b.test:443", "c.test:443", "d.test:443"} {
		assert.Equal(t, 502, connectCode(t, env, target))
		assert.Len(t, fu.requests(), i+1, "no password to lock out: a page cannot block other destinations")
	}
	assert.Equal(t, 502, connectCode(t, env, "pub.test:443"))
	assert.Len(t, fu.requests(), 4, "the refused target itself stays blocked")
}

func TestEgressUpstream_ProbePassesPolicyAuthDoesNotLatch(t *testing.T) {
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{
		authTargets: map[string]bool{"1.1.1.1:443": true, "9.9.9.9:443": true, "4.4.4.4:443": true},
		requireAuth: map[string]bool{"probe.test:443": true},
	})
	up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "127.0.0.1"), "local")
	up.AuthProbe, up.ProbeQuiet = "probe.test:443", time.Hour
	lookup := map[string][]string{"b.test": {"1.1.1.1"}, "c.test": {"9.9.9.9"}, "d.test": {"4.4.4.4"}, "e.test": {"5.5.5.5"}}
	start := func() *proxyEnv {
		return startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: lookup, dial: routingDial(fu, &dialLog{})})
	}
	env1, env2 := start(), start()

	for _, d := range []string{"b.test:443", "c.test:443", "d.test:443"} {
		assert.Equal(t, 502, connectCode(t, env1, d))
	}
	// one probe (without, then with credentials); its pass covers the next policy 407s (two since the pass: no rerun)
	assert.Equal(t, []string{"1.1.1.1:443", "probe.test:443", "probe.test:443", "9.9.9.9:443", "4.4.4.4:443"}, fu.targets())
	reqs := fu.requests()
	assert.Empty(t, reqs[1].auths, "the first probe proves the target requires auth")
	assert.NotEmpty(t, reqs[2].auths)
	assert.Equal(t, 200, connectCode(t, env2, "e.test:443"), "policy 407s must not cut other jobs off the upstream")
	assert.Equal(t, 502, connectCode(t, env2, "b.test:443"), "the refused destination itself stays blocked")
	assert.Equal(t, "5.5.5.5:443", fu.targets()[len(fu.targets())-1])
}

func TestEgressUpstream_ProbeRerunsAfterSeveral407sSincePass(t *testing.T) {
	be := newBackend(t)
	// the password changes right after a passing probe: the quiet window must not hide it for long
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{
		authTargets: map[string]bool{"1.1.1.1:443": true},
		requireAuth: map[string]bool{"probe.test:443": true},
	})
	up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "127.0.0.1"), "local")
	up.AuthProbe, up.ProbeQuiet = "probe.test:443", time.Hour
	lookup := map[string][]string{
		"b.test": {"1.1.1.1"}, "c.test": {"9.9.9.9"}, "d.test": {"4.4.4.4"}, "f.test": {"6.6.6.6"}, "g.test": {"7.7.7.7"},
	}
	env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: lookup, dial: routingDial(fu, &dialLog{})})

	assert.Equal(t, 502, connectCode(t, env, "b.test:443")) // policy 407; probe passes; window opens
	fu.rotated.Store(true)
	for _, d := range []string{"c.test:443", "d.test:443", "f.test:443"} {
		assert.Equal(t, 502, connectCode(t, env, d))
	}
	// c and d: inside the window, counted; f is the third since the pass: probe now, its 407 latches
	assert.Equal(t, []string{"1.1.1.1:443", "probe.test:443", "probe.test:443",
		"9.9.9.9:443", "4.4.4.4:443", "6.6.6.6:443", "probe.test:443", "probe.test:443"}, fu.targets())
	assert.Equal(t, 502, connectCode(t, env, "g.test:443"))
	assert.Len(t, fu.targets(), 8, "latched: no further logins")
}

func TestEgressUpstream_ProbeLatchesChangedPassword(t *testing.T) {
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{
		authTargets: map[string]bool{"1.1.1.1:443": true},
		requireAuth: map[string]bool{"probe.test:443": true},
	})
	up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "127.0.0.1"), "local")
	up.AuthProbe, up.ProbeQuiet = "probe.test:443", time.Millisecond
	lookup := map[string][]string{"b.test": {"1.1.1.1"}, "c.test": {"9.9.9.9"}, "d.test": {"4.4.4.4"}}
	env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: lookup, dial: routingDial(fu, &dialLog{})})

	assert.Equal(t, 502, connectCode(t, env, "b.test:443")) // policy 407; the probe passes: credentials proven
	time.Sleep(20 * time.Millisecond)                       // past ProbeQuiet
	fu.rotated.Store(true)
	assert.Equal(t, 502, connectCode(t, env, "c.test:443"))
	assert.Equal(t, []string{"1.1.1.1:443", "probe.test:443", "probe.test:443", "9.9.9.9:443", "probe.test:443", "probe.test:443"}, fu.targets())
	conns := fu.conns.Load()
	assert.Equal(t, 502, connectCode(t, env, "d.test:443"))
	assert.Len(t, fu.targets(), 6, "latched after one 407: no further logins")
	assert.Never(t, func() bool { return fu.conns.Load() != conns }, 200*time.Millisecond, 10*time.Millisecond)
	_, last := env.proxy.UpstreamErrors()
	assert.Contains(t, last, "latched")
}

func TestEgressUpstream_ProbeStopsAfterRefusedCredentials(t *testing.T) {
	be := newBackend(t)
	// wrong password: every login, at the probe target too, gets a 407
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{authAll: true})
	up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "127.0.0.1"), "local")
	up.AuthProbe, up.ProbeQuiet = "probe.test:443", time.Millisecond
	lookup := map[string][]string{"b.test": {"1.1.1.1"}, "c.test": {"9.9.9.9"}, "d.test": {"4.4.4.4"}, "e.test": {"5.5.5.5"}}
	env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: lookup, dial: routingDial(fu, &dialLog{})})

	for _, d := range []string{"b.test:443", "c.test:443", "d.test:443"} {
		assert.Equal(t, 502, connectCode(t, env, d))
		time.Sleep(30 * time.Millisecond) // past ProbeQuiet: every 407 could start a probe
	}
	creds := 0
	for _, r := range fu.requests() {
		if r.target == "probe.test:443" && len(r.auths) > 0 {
			creds++
		}
	}
	assert.Equal(t, 1, creds, "the refused credentials are not sent to authProbe again")
	n := len(fu.requests())
	assert.Equal(t, 502, connectCode(t, env, "e.test:443"), "three distinct 407s latch")
	assert.Len(t, fu.requests(), n, "latched: e.test never reached the upstream")
}

func TestEgressUpstream_ProbeFallbacks(t *testing.T) {
	policy407 := map[string]bool{"1.1.1.1:443": true, "9.9.9.9:443": true, "4.4.4.4:443": true, "6.6.6.6:443": true, "7.7.7.7:443": true}
	lookup := map[string][]string{
		"b.test": {"1.1.1.1"}, "c.test": {"9.9.9.9"}, "d.test": {"4.4.4.4"}, "e.test": {"5.5.5.5"}, "f.test": {"6.6.6.6"}, "g.test": {"7.7.7.7"},
	}
	start := func(t *testing.T, o fakeUpstreamOpts, quiet time.Duration) (*fakeUpstream, *proxyEnv) {
		be := newBackend(t)
		fu := newFakeUpstream(t, be, o)
		up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "127.0.0.1"), "local")
		up.AuthProbe, up.ProbeTimeout, up.ProbeQuiet = "probe.test:443", 200*time.Millisecond, quiet
		return fu, startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: lookup, dial: routingDial(fu, &dialLog{})})
	}
	// never proven or misconfigured: three distinct 407s latch, as without a probe
	for name, o := range map[string]fakeUpstreamOpts{
		"answers_without_credentials": {authTargets: policy407},
		"refused_before_auth":         {authTargets: policy407, refuse: map[string]bool{"probe.test:443": true}},
		"silent_with_credentials":     {authTargets: policy407, requireAuth: map[string]bool{"probe.test:443": true}, silent: map[string]bool{"probe.test:443": true}},
		"never_proven":                {authAll: true},
	} {
		for qname, quiet := range map[string]time.Duration{"quiet_default": 0, "probe_every_407": time.Nanosecond} {
			t.Run(name+"/"+qname, func(t *testing.T) {
				fu, env := start(t, o, quiet)
				for _, d := range []string{"b.test:443", "c.test:443", "d.test:443"} {
					assert.Equal(t, 502, connectCode(t, env, d))
				}
				n := len(fu.requests())
				assert.Equal(t, 502, connectCode(t, env, "e.test:443"), "three distinct 407s latch")
				assert.Len(t, fu.requests(), n, "latched: e.test never reached the upstream")
				if o.requireAuth == nil && !o.authAll {
					for _, r := range fu.requests() {
						if r.target == "probe.test:443" {
							assert.Empty(t, r.auths, "an unproven probe target never gets the credentials")
						}
					}
				}
			})
		}
	}
	t.Run("probe_policy_407_one_request", func(t *testing.T) {
		// the account may not reach authProbe: its 407 is policy, and must not latch on one request
		_, env := start(t, fakeUpstreamOpts{authTargets: map[string]bool{"1.1.1.1:443": true, "probe.test:443": true}}, 0)
		assert.Equal(t, 502, connectCode(t, env, "b.test:443"))
		assert.Equal(t, 200, connectCode(t, env, "e.test:443"))
	})
	for qname, quiet := range map[string]time.Duration{"quiet_default": 0, "probe_every_407": time.Nanosecond} {
		t.Run("unavailable_after_proven_retries/"+qname, func(t *testing.T) {
			// proven once, then the probe gets no answer: 407s are not counted (a page could cause the outage)
			o := fakeUpstreamOpts{authTargets: policy407, requireAuth: map[string]bool{"probe.test:443": true}}
			fu, env := start(t, o, quiet)
			assert.Equal(t, 502, connectCode(t, env, "b.test:443")) // proven
			fu.mu.Lock()
			fu.silent = map[string]bool{"probe.test:443": true}
			fu.mu.Unlock()
			// c, d: since the pass; f: the third, forces a probe that gets no answer; g: in the retry window
			for _, d := range []string{"c.test:443", "d.test:443", "f.test:443", "g.test:443"} {
				assert.Equal(t, 502, connectCode(t, env, d))
			}
			assert.Equal(t, 200, connectCode(t, env, "e.test:443"), "unavailable probes after a pass do not count")
		})
	}
}

func TestEgressUpstream_ByNameForwardLatched(t *testing.T) {
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{authAll: true})
	up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "example.com"), "upstream")
	env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: byNameLookup, dial: routingDial(fu, &dialLog{})})
	for _, target := range []string{"a.test:443", "b.test:443", "c.test:443"} {
		require.Equal(t, 502, connectCode(t, env, target))
	}
	require.Len(t, fu.requests(), 3)
	assert.Equal(t, http.StatusBadGateway, status(t, env.client, "http://pub.test/x"))
	assert.Len(t, fu.requests(), 3, "latched: the by-name forward must not log in again")
	_, last := env.proxy.UpstreamErrors()
	assert.Equal(t, "407 (latched)", last)
}

func TestEgressUpstream_FailureClassification(t *testing.T) {
	refuseAll := func(context.Context, string, string) (net.Conn, error) { return nil, errors.New("connection refused") }
	t.Run("connect_dial_failed", func(t *testing.T) {
		be := newBackend(t)
		fu := newFakeUpstream(t, be, fakeUpstreamOpts{})
		up := mkUpstream(t, fu.proxyURL("http", "", "127.0.0.1"), "local")
		env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: map[string][]string{"pub.test": {"8.8.8.8"}}, dial: refuseAll})
		assert.Equal(t, 502, connectCode(t, env, "pub.test:443"))
		n, last := env.proxy.UpstreamErrors()
		assert.Equal(t, 1, n)
		assert.Equal(t, "dial failed", last)
	})
	t.Run("by_name_forward_failed", func(t *testing.T) {
		be := newBackend(t)
		fu := newFakeUpstream(t, be, fakeUpstreamOpts{})
		up := mkUpstream(t, fu.proxyURL("http", "", "example.com"), "upstream")
		env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: byNameLookup, dial: refuseAll})
		assert.Equal(t, http.StatusBadGateway, status(t, env.client, "http://pub.test/x"))
		n, last := env.proxy.UpstreamErrors()
		assert.Equal(t, 1, n)
		assert.Equal(t, "request failed", last)
	})
}

func TestEgressProxy_MultiAddressConnNotReused(t *testing.T) {
	be := newBackend(t)
	beAddr := be.srv.Listener.Addr().String()
	var dials []string
	var mu sync.Mutex
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dials = append(dials, addr)
		mu.Unlock()
		if addr == "8.8.8.8:80" {
			return nil, errors.New("unreachable")
		}
		var d net.Dialer
		return d.DialContext(ctx, network, beAddr)
	}
	env := startProxy(t, proxyOpts{
		lookup: map[string][]string{"multi.test": {"8.8.8.8", "8.8.4.4"}, "other.test": {"8.8.8.8"}}, dial: dial,
	})
	require.Equal(t, 200, status(t, env.client, "http://multi.test/a"))
	require.Equal(t, 200, status(t, env.client, "http://multi.test/c"), "the same name reuses its connection")
	// the connection above reached 8.8.4.4; other.test (8.8.8.8 only) must not be served on it
	assert.Equal(t, http.StatusBadGateway, status(t, env.client, "http://other.test/b"))
	assert.Equal(t, []string{"multi.test /a", "multi.test /c"}, be.requests())
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"8.8.8.8:80", "8.8.4.4:80", "8.8.8.8:80"}, dials)
}

func TestEgressUpstream_AuthKeyedByDestination(t *testing.T) {
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{authAll: true})
	up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "127.0.0.1"), "local")
	// one destination whose answer rotates, as CDN names do: three 407s from it are one destination, not three
	var calls atomic.Int32
	rotating := []string{"8.8.8.8", "8.8.4.4", "1.1.1.1"}
	lookup := func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "cdn.test":
			n := int(calls.Add(1))
			var out []netip.Addr
			for k := range rotating {
				out = append(out, netip.MustParseAddr(rotating[(n+k)%len(rotating)]))
			}
			return out, nil
		case "other.test":
			return []netip.Addr{netip.MustParseAddr("9.9.9.9")}, nil
		}
		return nil, errors.New("no such host")
	}
	env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookupFn: lookup, dial: routingDial(fu, &dialLog{})})
	for range 3 {
		assert.Equal(t, 502, connectCode(t, env, "cdn.test:443"))
	}
	assert.Len(t, fu.requests(), 1, "the 407'd destination is blocked whatever address comes first")
	assert.Equal(t, 502, connectCode(t, env, "other.test:443"))
	assert.Len(t, fu.requests(), 2, "one destination's 407s never block another")
}

func TestEgressUpstream_StalledTLSUpstreamFailsFast(t *testing.T) {
	// accepts TCP and never answers the TLS handshake
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })
	var held []net.Conn
	var hmu sync.Mutex
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			hmu.Lock()
			held = append(held, c)
			hmu.Unlock()
		}
	}()
	t.Cleanup(func() {
		hmu.Lock()
		defer hmu.Unlock()
		for _, c := range held {
			_ = c.Close()
		}
	})
	port := l.Addr().(*net.TCPAddr).Port
	toStall := func(ctx context.Context, network, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, network, l.Addr().String())
	}
	for _, c := range []struct{ name, host, resolve, want string }{
		{"connect", "127.0.0.1", "local", "tls handshake failed"},
		{"by_name_forward", "example.com", "upstream", "request failed"},
	} {
		t.Run(c.name, func(t *testing.T) {
			up := mkUpstream(t, fmt.Sprintf("https://%s:%d", c.host, port), c.resolve)
			env := startProxy(t, proxyOpts{
				policy: egress.Policy{Upstream: up}, lookup: byNameLookup, dial: toStall,
				upstreamTLSTimeout: 200 * time.Millisecond,
			})
			start := time.Now()
			if c.resolve == "local" {
				assert.Equal(t, 502, connectCode(t, env, "pub.test:443"))
			} else {
				assert.Equal(t, http.StatusBadGateway, status(t, env.client, "http://pub.test/x"))
			}
			assert.Less(t, time.Since(start), 3*time.Second, "the handshake is bounded, not left to the job timeout")
			_, last := env.proxy.UpstreamErrors()
			assert.Equal(t, c.want, last)
		})
	}
}

func TestRenderJob_UpstreamLookupCutByJobTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping browser-based test in short mode")
	}
	_, engine, _, cancel := setupTestServer(t)
	defer cancel()
	defer engine.Shutdown()
	engine.SetEgressPolicy(egress.Policy{Upstream: mkUpstream(t, "http://slow-proxy.test:3128", "local")})
	engine.SetEgressLookup(func(ctx context.Context, _ string) ([]netip.Addr, error) {
		<-ctx.Done() // a resolver that never answers
		return nil, ctx.Err()
	})
	b := buildZipFiles(t, map[string][]byte{"index.html": []byte("<html><body>x</body></html>")})
	reader, err := zpt.NewZptReader(bytes.NewReader(b), int64(len(b)))
	require.NoError(t, err)
	job := render.NewRenderJob(reader, uuid.New())
	job.IndexFile = "index.html"
	job.JobTimeoutS = 1
	result := engine.RenderJob(job)
	require.False(t, result.Success)
	assert.ErrorIs(t, result.Error, render.ErrJobTimeout)
}

func TestEgressUpstream_CancelledDialNotCounted(t *testing.T) {
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{})
	up := mkUpstream(t, fu.proxyURL("http", "", "127.0.0.1"), "local")
	entered, returned := make(chan struct{}), make(chan struct{})
	block := func(ctx context.Context, _, _ string) (net.Conn, error) {
		close(entered)
		<-ctx.Done()
		defer close(returned)
		return nil, ctx.Err()
	}
	env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: map[string][]string{"pub.test": {"8.8.8.8"}}, dial: block})
	c, err := net.Dial("tcp", env.addr)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	_, err = fmt.Fprintf(c, "CONNECT pub.test:443 HTTP/1.1\r\nHost: pub.test:443\r\n\r\n")
	require.NoError(t, err)
	<-entered
	require.NoError(t, env.proxy.Close())
	<-returned
	time.Sleep(200 * time.Millisecond) // let the handler finish its bookkeeping
	n, last := env.proxy.UpstreamErrors()
	assert.Zero(t, n, "a cancelled dial is not an upstream failure (last: %q)", last)
}

func TestEgressUpstream_OriginAuthResponseDoesNotLatch(t *testing.T) {
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{originAuth: true})
	up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "example.com"), "upstream")
	lookup := map[string][]string{
		"example.com": {"203.0.113.10"},
		"a.test":      {"8.8.8.8"}, "b.test": {"1.1.1.1"}, "c.test": {"9.9.9.9"}, "d.test": {"4.4.4.4"},
	}
	start := func() *proxyEnv {
		return startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: lookup, dial: routingDial(fu, &dialLog{})})
	}
	env1, env2 := start(), start()

	// three distinct destinations whose origin answers 407: a wrong password would look exactly like this if it latched
	for _, u := range []string{"http://a.test/x", "http://b.test/x", "http://c.test/x"} {
		resp, err := env1.client.Get(u)
		require.NoError(t, err)
		_ = resp.Body.Close()
		assert.Equal(t, 407, resp.StatusCode, "a forwarded 407 is relayed unchanged")
	}
	n, last := env1.proxy.UpstreamErrors()
	assert.Equal(t, 0, n)
	assert.NotContains(t, last, "Proxy Authentication Required")

	before := len(fu.requests())
	assert.Equal(t, 200, connectCode(t, env2, "d.test:443"), "another job must still reach the upstream")
	assert.Equal(t, before+1, len(fu.requests()))
	_, last = env2.proxy.UpstreamErrors()
	assert.NotContains(t, last, "Proxy Authentication Required")
}

func TestEgressUpstream_NoProxyDialsDirect(t *testing.T) {
	lookup := map[string][]string{"pub.test": {"8.8.8.8"}, "example.com": {"203.0.113.10"}}
	t.Run("local", func(t *testing.T) {
		h := startLocal(t, fakeUpstreamOpts{}, lookup, egress.Policy{}, "8.8.8.8/32")
		assert.Equal(t, 200, connectCode(t, h.env, "pub.test:443"))
		assert.Equal(t, []string{"8.8.8.8:443"}, h.dial.all())
		assert.Equal(t, int32(0), h.fu.conns.Load())
	})
	t.Run("by_name", func(t *testing.T) {
		h := startByName(t, fakeUpstreamOpts{}, lookup, egress.Policy{}, "8.8.8.8/32")
		assert.Equal(t, 200, connectCode(t, h.env, "pub.test:443"))
		assert.Equal(t, []string{"8.8.8.8:443"}, h.dial.all())
		assert.Equal(t, int32(0), h.fu.conns.Load())
	})
}

func TestEgressUpstream_RefusedTunnelTriesNextIP(t *testing.T) {
	lookup := map[string][]string{"pub.test": {"8.8.8.8", "8.8.4.4"}}
	h := startLocal(t, fakeUpstreamOpts{refuse: map[string]bool{"8.8.8.8:443": true}}, lookup, egress.Policy{})
	assert.Equal(t, 200, connectCode(t, h.env, "pub.test:443"))
	assert.Equal(t, []string{"8.8.8.8:443", "8.8.4.4:443"}, h.fu.targets())

	h = startLocal(t, fakeUpstreamOpts{refuse: map[string]bool{"8.8.8.8:443": true, "8.8.4.4:443": true}}, lookup, egress.Policy{})
	assert.Equal(t, 502, connectCode(t, h.env, "pub.test:443"))
	n, _ := h.env.proxy.UpstreamErrors()
	assert.Equal(t, 2, n)
}

func TestEgressUpstream_OwnOriginNeverUpstream(t *testing.T) {
	h := startLocal(t, fakeUpstreamOpts{}, nil, egress.Policy{})
	resp, err := h.env.client.Get(h.env.ownURL("/"))
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	assert.Equal(t, 200, resp.StatusCode)
	assert.Equal(t, "own", string(body))
	assert.True(t, h.env.proxy.SawOwnOrigin())
	assert.Equal(t, int32(0), h.fu.conns.Load())
}

func TestEgressUpstream_CloseEndsTunnel(t *testing.T) {
	lookup := map[string][]string{"pub.test": {"8.8.8.8"}}
	for _, mode := range []string{"close", "retire"} {
		t.Run(mode, func(t *testing.T) {
			h := startLocal(t, fakeUpstreamOpts{}, lookup, egress.Policy{})
			c, br, code := rawConnect(t, h.env.addr, "pub.test:443", "")
			require.Equal(t, 200, code)
			if mode == "close" {
				require.NoError(t, h.env.proxy.Close())
			} else {
				fastRetire(h.env, 1)
				done := h.env.proxy.Retire(func(context.Context) error { return errors.New("busy") })
				defer waitDone(t, done)
			}
			_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
			_, err := br.ReadByte()
			require.Error(t, err)
			var ne net.Error
			if errors.As(err, &ne) {
				assert.False(t, ne.Timeout(), "tunnel must be cut, not time out")
			}
		})
	}
}

func TestEgressUpstream_BufferedBytesAfterConnect(t *testing.T) {
	h := startLocal(t, fakeUpstreamOpts{banner: "HELLO\n"}, map[string][]string{"pub.test": {"8.8.8.8"}}, egress.Policy{})
	_, br, code := rawConnect(t, h.env.addr, "pub.test:443", "")
	require.Equal(t, 200, code)
	line, err := br.ReadString('\n')
	require.NoError(t, err)
	assert.Equal(t, "HELLO\n", line)
}

func TestEgressUpstream_HTTPSUpstream(t *testing.T) {
	cert := httptest.NewUnstartedServer(http.NotFoundHandler())
	cert.StartTLS()
	t.Cleanup(cert.Close)
	pool := x509.NewCertPool()
	pool.AddCert(cert.Certificate())
	serverTLS := &tls.Config{Certificates: cert.TLS.Certificates}

	t.Run("connect", func(t *testing.T) {
		be := newBackend(t)
		fu := newFakeUpstream(t, be, fakeUpstreamOpts{tls: serverTLS})
		up := mkUpstream(t, fu.proxyURL("https", "", "127.0.0.1"), "local")
		env := startProxy(t, proxyOpts{
			policy: egress.Policy{Upstream: up}, lookup: map[string][]string{"pub.test": {"8.8.8.8"}},
			dial: routingDial(fu, &dialLog{}), upstreamTLS: &tls.Config{RootCAs: pool},
		})
		code, body := tunnelGet(t, env, "pub.test:443", "pub.test")
		assert.Equal(t, 200, code)
		assert.Equal(t, "backend", body)
		assert.Equal(t, []string{"8.8.8.8:443"}, fu.targets())
	})
	t.Run("forward_by_name", func(t *testing.T) {
		be := newBackend(t)
		fu := newFakeUpstream(t, be, fakeUpstreamOpts{tls: serverTLS})
		up := mkUpstream(t, fu.proxyURL("https", "", "example.com"), "upstream")
		env := startProxy(t, proxyOpts{
			policy: egress.Policy{Upstream: up}, lookup: byNameLookup,
			dial: routingDial(fu, &dialLog{}), upstreamTLS: &tls.Config{RootCAs: pool},
		})
		resp, err := env.client.Get("http://pub.test/x")
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		assert.Equal(t, "upstream", string(body))
	})
	t.Run("untrusted", func(t *testing.T) {
		be := newBackend(t)
		fu := newFakeUpstream(t, be, fakeUpstreamOpts{tls: serverTLS})
		up := mkUpstream(t, fu.proxyURL("https", "", "127.0.0.1"), "local")
		env := startProxy(t, proxyOpts{
			policy: egress.Policy{Upstream: up}, lookup: map[string][]string{"pub.test": {"8.8.8.8"}},
			dial: routingDial(fu, &dialLog{}),
		})
		assert.Equal(t, 502, connectCode(t, env, "pub.test:443"))
		assert.Empty(t, fu.requests(), "nothing, credentials included, is sent before the certificate is verified")
		n, last := env.proxy.UpstreamErrors()
		assert.Equal(t, 1, n)
		assert.Equal(t, "tls handshake failed", last)
	})
}

func TestEgressUpstream_ByNameLocalUpstreamRejected(t *testing.T) {
	up := mkUpstream(t, "http://proxy.test:4000", "upstream")
	p := egress.New("localhost:1", "127.0.0.1:1", 1, 1, egress.Policy{Upstream: up})
	p.Lookup = func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	p.InterfaceAddrs = func() ([]net.Addr, error) { return nil, nil }
	_, err := p.Start()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "on this host")
}

func TestEgressUpstream_StartFailsWhenUpstreamUnresolvable(t *testing.T) {
	up := mkUpstream(t, "http://nowhere.test:3128", "local")
	p := egress.New("localhost:1", "127.0.0.1:1", 1, 1, egress.Policy{Upstream: up})
	p.Lookup = func(context.Context, string) ([]netip.Addr, error) { return nil, errors.New("no such host") }
	p.InterfaceAddrs = func() ([]net.Addr, error) { return nil, nil }
	_, err := p.Start()
	require.Error(t, err)
}

func TestEgressParseUpstream(t *testing.T) {
	for _, c := range []struct {
		url, resolve string
		noProxy      []string
	}{
		{"http://proxy:3128", "", nil},
		{"https://proxy.corp:8443", "local", nil},
		{"http://user:pw@proxy.corp:3128/", "upstream", nil},
		{"http://proxy:3128", "local", []string{"8.8.8.0/24", "cdn.example.com:443"}},
		{"", "", nil},
		{"", "local", nil},
		{"http://127.0.0.1.:3128", "local", nil},
	} {
		up, err := egress.ParseUpstream(c.url, c.resolve, c.noProxy)
		assert.NoError(t, err, c.url)
		if c.url == "" {
			assert.Nil(t, up)
		} else {
			require.NotNil(t, up)
			assert.Equal(t, c.resolve == "upstream", up.ByName)
		}
	}

	bad := []struct {
		url, resolve string
		noProxy      []string
	}{
		{"socks5://h:1080", "", nil},
		{"tok3n:s3cret@proxy:3128", "", nil},
		{"http://h", "", nil},
		{"http://:3128", "", nil},
		{"http://h:0", "", nil},
		{"http://h:3128/p", "", nil},
		{"http://h:3128?x=1", "", nil},
		{"http://h:3128", "dns", nil},
		{"http://h:3128", "", []string{"bad"}},
		{"http://h:3128", "", []string{"10.0.0.0/8"}},
		{"", "", []string{"8.8.8.0/24"}},
		{"", "upstream", nil},
		{"http://localhost:3128", "upstream", nil},
		{"http://LOCALHOST.:3128", "upstream", nil},
		{"http://127.0.0.1:3128", "upstream", nil},
		{"http://127.0.0.1.:3128", "upstream", nil},
		{"http://0.0.0.0.:3128", "upstream", nil},
		{"http://tok3n:s3cret@host:badport", "", nil},
	}
	for _, c := range bad {
		_, err := egress.ParseUpstream(c.url, c.resolve, c.noProxy)
		if assert.Error(t, err, "%q %q", c.url, c.resolve) {
			assert.NotContains(t, err.Error(), "s3cret")
			assert.NotContains(t, err.Error(), "tok3n")
		}
	}
}

func TestEgressUpstream_ForwardAuthLatched(t *testing.T) {
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{authAll: true})
	up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "127.0.0.1"), "local")
	lookup := map[string][]string{
		"pub.test": {"8.8.8.8"}, "b.test": {"1.1.1.1"}, "c.test": {"9.9.9.9"}, "d.test": {"4.4.4.4"},
	}
	env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookup: lookup, dial: routingDial(fu, &dialLog{})})
	// plain http rides a CONNECT tunnel in local mode: its 407s are keyed by destination like a page's CONNECTs
	for i, u := range []string{"http://pub.test/x", "http://b.test/x", "http://c.test/x"} {
		assert.Equal(t, http.StatusBadGateway, status(t, env.client, u))
		assert.Len(t, fu.requests(), i+1, "one destination's 407 never blocks another")
	}
	assert.Equal(t, http.StatusBadGateway, status(t, env.client, "http://d.test/x"))
	assert.Len(t, fu.requests(), 3, "latched: no further logins")
}

func TestEgressUpstream_ForwardMultiAddressConnNotReused(t *testing.T) {
	h := startLocal(t, fakeUpstreamOpts{refuse: map[string]bool{"8.8.8.8:80": true}},
		map[string][]string{"multi.test": {"8.8.8.8", "8.8.4.4"}, "other.test": {"8.8.8.8"}}, egress.Policy{})
	require.Equal(t, 200, status(t, h.env.client, "http://multi.test/a"))
	require.Equal(t, 200, status(t, h.env.client, "http://multi.test/c"), "the same name reuses its tunnel")
	// the tunnel above reached 8.8.4.4; other.test (8.8.8.8 only) must not be served on it
	assert.Equal(t, http.StatusBadGateway, status(t, h.env.client, "http://other.test/b"))
	assert.Equal(t, []string{"multi.test /a", "multi.test /c"}, h.be.requests())
	assert.Equal(t, []string{"8.8.8.8:80", "8.8.4.4:80", "8.8.8.8:80"}, h.fu.targets())
}

// scriptedUpstream reads one request head per connection, writes reply (nothing when empty) and reports when the
// egress proxy ends the connection.
type scriptedUpstream struct {
	port   int
	got    chan struct{}
	closed chan struct{}
}

func newScriptedUpstream(t *testing.T, reply string) *scriptedUpstream {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	s := &scriptedUpstream{port: l.Addr().(*net.TCPAddr).Port, got: make(chan struct{}, 8), closed: make(chan struct{}, 8)}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = l.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
			go func() {
				br := bufio.NewReader(c)
				for {
					line, err := br.ReadString('\n')
					if err != nil {
						return
					}
					if line == "\r\n" {
						break
					}
				}
				s.got <- struct{}{}
				if reply != "" {
					_, _ = c.Write([]byte(reply))
				}
				_, _ = io.Copy(io.Discard, br)
				s.closed <- struct{}{}
			}()
		}
	}()
	return s
}

func TestEgressUpstream_SilentUpstreamCutOnClose(t *testing.T) {
	su := newScriptedUpstream(t, "")
	up := mkUpstream(t, fmt.Sprintf("http://127.0.0.1:%d", su.port), "local")
	env := startProxy(t, proxyOpts{
		policy: egress.Policy{Upstream: up}, lookup: map[string][]string{"pub.test": {"8.8.8.8"}}, dial: routingDial(nil, &dialLog{}),
	})
	c, err := net.Dial("tcp", env.addr)
	require.NoError(t, err)
	defer func() { _ = c.Close() }()
	_, err = fmt.Fprintf(c, "CONNECT pub.test:443 HTTP/1.1\r\nHost: pub.test:443\r\n\r\n")
	require.NoError(t, err)
	select {
	case <-su.got:
	case <-time.After(5 * time.Second):
		t.Fatal("the CONNECT never reached the upstream")
	}
	require.NoError(t, env.proxy.Close())
	select {
	case <-su.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("the connection to a silent upstream outlived the proxy")
	}
}

func TestEgressUpstream_CloseDropsIdleUpstreamConns(t *testing.T) {
	for _, mode := range []string{"local", "by-name"} {
		t.Run(mode, func(t *testing.T) {
			start, lookup := startLocal, map[string][]string{"pub.test": {"8.8.8.8"}}
			if mode == "by-name" {
				start, lookup = startByName, byNameLookup
			}
			h := start(t, fakeUpstreamOpts{}, lookup, egress.Policy{})
			require.Equal(t, 200, status(t, h.env.client, "http://pub.test/x"))
			require.Equal(t, int32(1), h.fu.conns.Load())
			require.NoError(t, h.env.proxy.Close())
			assert.Eventually(t, func() bool { return h.fu.closed.Load() == 1 }, 2*time.Second, 10*time.Millisecond,
				"the pooled upstream connection outlived the proxy")
		})
	}
}

func TestEgressUpstream_NonOKConnectStatus(t *testing.T) {
	for _, reply := range []string{
		"HTTP/1.1 204 No Content\r\n\r\n",
		"HTTP/1.1 302 Found\r\nLocation: http://pub.test/\r\nContent-Length: 0\r\n\r\n",
		"HTTP/1.1 101 Switching Protocols\r\n\r\n",
	} {
		code := strings.Fields(reply)[1]
		t.Run(code, func(t *testing.T) {
			su := newScriptedUpstream(t, reply)
			up := mkUpstream(t, fmt.Sprintf("http://127.0.0.1:%d", su.port), "local")
			env := startProxy(t, proxyOpts{
				policy: egress.Policy{Upstream: up}, lookup: map[string][]string{"pub.test": {"8.8.8.8"}}, dial: routingDial(nil, &dialLog{}),
			})
			assert.Equal(t, http.StatusBadGateway, connectCode(t, env, "pub.test:443"), "only a 200 opens a tunnel")
			n, last := env.proxy.UpstreamErrors()
			assert.Equal(t, 1, n)
			assert.True(t, strings.HasPrefix(last, code), last)
		})
	}
}

func TestEgressUpstream_AuthKeyCanonical(t *testing.T) {
	be := newBackend(t)
	fu := newFakeUpstream(t, be, fakeUpstreamOpts{authAll: true})
	up := mkUpstream(t, fu.proxyURL("http", "user:s3cret@", "127.0.0.1"), "local")
	ips := map[string]string{"pub.test": "8.8.8.8", "d.test": "4.4.4.4"}
	lookup := func(_ context.Context, host string) ([]netip.Addr, error) {
		ip, ok := ips[strings.ToLower(host)]
		if !ok {
			return nil, errors.New("no such host")
		}
		return []netip.Addr{netip.MustParseAddr(ip)}, nil
	}
	env := startProxy(t, proxyOpts{policy: egress.Policy{Upstream: up}, lookupFn: lookup, dial: routingDial(fu, &dialLog{})})
	for _, target := range []string{"PUB.test:443", "pub.TEST:443", "pub.test:443"} {
		assert.Equal(t, http.StatusBadGateway, connectCode(t, env, target))
	}
	assert.Len(t, fu.requests(), 1, "one destination, however its name is spelled")
	assert.Equal(t, http.StatusBadGateway, connectCode(t, env, "d.test:443"))
	assert.Len(t, fu.requests(), 2, "case variants of one name must not trip the process-wide latch")
}
