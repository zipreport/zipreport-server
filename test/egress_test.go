package test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/netip"
	"net/url"
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
	p := egress.New("localhost:"+strconv.Itoa(env.ownPort), ownDial, o.poolFirst, o.poolCount, o.policy)
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
	if o.dial != nil {
		p.Dial = o.dial
	}
	ifaces := o.ifaces
	if ifaces == nil {
		ifaces = []net.Addr{ipnet(t, "192.0.2.1/24")}
	}
	p.InterfaceAddrs = func() ([]net.Addr, error) { return ifaces, nil }
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
