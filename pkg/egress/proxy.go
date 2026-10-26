package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/netutil"
)

const (
	maxSamples      = 16
	maxSampleLen    = 120
	lookupTimeout   = 5 * time.Second
	dialTimeout     = 10 * time.Second
	tunnelIdle      = 60 * time.Second
	maxConnections  = 128
	respHeaderLimit = 30 * time.Second
)

type LookupFunc func(ctx context.Context, host string) ([]netip.Addr, error)
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)
type InterfaceAddrsFunc func() ([]net.Addr, error)

type targetsKey struct{}

// hop-by-hop headers are meaningful for one connection only and are never forwarded
var hopByHop = []string{
	"Connection", "Proxy-Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"Te", "Trailer", "Transfer-Encoding", "Upgrade",
}

// Proxy is a per-job forward proxy that refuses destinations a rendered page must not reach.
type Proxy struct {
	RetireInterval time.Duration      // default 10s; delay before each disposal attempt in Retire
	RetireAttempts int                // default 6; disposal attempts in Retire
	Lookup         LookupFunc         // default net.DefaultResolver.LookupNetIP; test seam
	Dial           DialFunc           // default net.Dialer with a 10s timeout; test seam
	InterfaceAddrs InterfaceAddrsFunc // default net.InterfaceAddrs; test seam
	TunnelIdle     time.Duration      // default 60s; CONNECT tunnel idle limit; test seam
	UpstreamTLS    *tls.Config        // default: system roots, ServerName from the upstream URL; test seam for https upstreams

	ownHost   string
	ownDial   string
	poolFirst int
	poolCount int
	policy    Policy

	listener  net.Listener
	server    *http.Server
	transport *http.Transport
	// tunnelTransport (address-vetted upstream routes) and upstreamTransport (by-name routes) exist only with an upstream
	tunnelTransport   *http.Transport
	upstreamTransport *http.Transport
	upstreamAddrs     []netip.Addr // vetted at Start: every address of the upstream proxy, normalised
	upstreamPort      int
	base              context.Context // cancelled to abort in-flight forwards
	cancel            context.CancelFunc
	proxyPort         int
	local             map[netip.Addr]struct{}

	mu      sync.Mutex
	closed  bool
	denyAll bool
	tunnels map[net.Conn]struct{}
	samples []string

	upstreamErrs int    // guarded by mu
	upstreamLast string // guarded by mu; status text only, never an address or URL

	blocked atomic.Int64
	sawOwn  atomic.Bool
}

// New returns a proxy for one job. ownHost is the job's server as the browser names it (localhost:PORT),
// ownDial the address that server is actually dialled at.
func New(ownHost, ownDial string, poolFirst, poolCount int, policy Policy) *Proxy {
	return &Proxy{
		ownHost:   strings.ToLower(ownHost),
		ownDial:   ownDial,
		poolFirst: poolFirst,
		poolCount: poolCount,
		policy:    policy,
		tunnels:   map[net.Conn]struct{}{},

		RetireInterval: 10 * time.Second,
		RetireAttempts: 6,
		TunnelIdle:     tunnelIdle,
	}
}

// Start listens on an ephemeral 127.0.0.1 port and returns its address.
func (p *Proxy) Start() (string, error) {
	if p.Lookup == nil {
		p.Lookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if p.Dial == nil {
		d := &net.Dialer{Timeout: dialTimeout}
		p.Dial = d.DialContext
	}
	if p.InterfaceAddrs == nil {
		p.InterfaceAddrs = net.InterfaceAddrs
	}

	p.local = map[netip.Addr]struct{}{}
	addrs, err := p.InterfaceAddrs()
	if err != nil {
		return "", fmt.Errorf("listing local interface addresses: %w", err)
	}
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if na, ok := netip.AddrFromSlice(ip); ok {
			p.local[na.Unmap()] = struct{}{}
		}
	}

	if up := p.policy.Upstream; up != nil {
		addrs, err := p.resolve(context.Background(), up.Hostname())
		if err != nil {
			// fail closed: without its addresses the upstream-address refusals cannot work
			return "", errors.New("cannot resolve the upstream proxy")
		}
		p.upstreamAddrs = p.upstreamAddrs[:0]
		for _, a := range addrs {
			a = norm(a)
			p.upstreamAddrs = append(p.upstreamAddrs, a)
			if up.ByName && (a.IsLoopback() || a.IsUnspecified() || p.isLocal(a)) {
				return "", errors.New("resolve \"upstream\" cannot be used with an upstream proxy on this host")
			}
		}
		p.upstreamPort, _ = strconv.Atoi(up.URL.Port())
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	p.proxyPort = l.Addr().(*net.TCPAddr).Port
	p.listener = netutil.LimitListener(l, maxConnections)
	p.transport = &http.Transport{
		Proxy:                 nil,
		DialContext:           p.transportDial,
		DisableCompression:    true,
		ResponseHeaderTimeout: respHeaderLimit,
		IdleConnTimeout:       30 * time.Second,
	}
	if up := p.policy.Upstream; up != nil {
		// address-vetted upstream routes: plain http rides a CONNECT <vetted-ip>:<port> tunnel; the request stays
		// origin-form with its own Host (absolute-form to an IP would lose Host: RFC 9112 section 3.2.2)
		p.tunnelTransport = &http.Transport{
			Proxy:                 nil,
			DialContext:           p.tunnelDial,
			DisableCompression:    true,
			ResponseHeaderTimeout: respHeaderLimit,
			IdleConnTimeout:       30 * time.Second,
		}
		// by-name routes: absolute-form request to the upstream, which resolves the name
		p.upstreamTransport = &http.Transport{
			Proxy: http.ProxyURL(up.URL), // never the process environment
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return p.dialUpstreamProxyRaw(ctx)
			},
			TLSClientConfig:       p.upstreamTLSConfig(),
			DisableCompression:    true,
			ResponseHeaderTimeout: respHeaderLimit,
			IdleConnTimeout:       30 * time.Second,
		}
	}
	p.server = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	p.base, p.cancel = context.WithCancel(context.Background())
	go func() { _ = p.server.Serve(p.listener) }()
	return l.Addr().String(), nil
}

// Close stops the proxy and tears down every open tunnel.
func (p *Proxy) Close() error {
	p.mu.Lock()
	p.closed = true
	if p.cancel != nil {
		p.cancel()
	}
	var err error
	if p.server != nil {
		err = p.server.Close()
		p.closeIdle()
	}
	for c := range p.tunnels {
		_ = c.Close()
	}
	p.tunnels = map[net.Conn]struct{}{}
	p.mu.Unlock()
	return err
}

func (p *Proxy) closeIdle() {
	p.transport.CloseIdleConnections()
	if p.tunnelTransport != nil {
		p.tunnelTransport.CloseIdleConnections()
	}
	if p.upstreamTransport != nil {
		p.upstreamTransport.CloseIdleConnections()
	}
}

// DenyAll refuses every request from now on; the listener stays open.
func (p *Proxy) DenyAll() {
	p.mu.Lock()
	p.denyAll = true
	p.mu.Unlock()
}

// Retire is for a proxy whose browser context may still exist: it refuses every new
// request, cuts open tunnels and in-flight forwards, and retries dispose in the
// background, closing the proxy once dispose succeeds. If every attempt fails the
// proxy stays open in deny-all mode, so the context's traffic can never reach a
// later job's proxy on a reused port.
// The returned channel is closed when the retry loop ends (closed proxy or gave up).
func (p *Proxy) Retire(dispose func(context.Context) error) <-chan struct{} {
	interval, attempts := p.RetireInterval, p.RetireAttempts
	p.DenyAll()
	p.mu.Lock()
	for c := range p.tunnels {
		_ = c.Close()
	}
	p.tunnels = map[net.Conn]struct{}{}
	p.mu.Unlock()
	if p.cancel != nil {
		p.cancel()
	}
	p.closeIdle()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < attempts; i++ {
			time.Sleep(interval)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := dispose(ctx)
			cancel()
			if err == nil {
				_ = p.Close()
				return
			}
		}
	}()
	return done
}

// Blocked returns the number of refused requests and a bounded set of distinct sample destinations.
func (p *Proxy) Blocked() (int, []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return int(p.blocked.Load()), append([]string(nil), p.samples...)
}

// UpstreamErrors returns how many upstream tunnels or requests failed and the last failure (status text only).
func (p *Proxy) UpstreamErrors() (int, string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.upstreamErrs, p.upstreamLast
}

func (p *Proxy) upstreamFailed(last string) {
	p.mu.Lock()
	p.upstreamErrs++
	p.upstreamLast = last
	p.mu.Unlock()
}

// SawOwnOrigin reports whether the browser reached the job's own server through this proxy.
func (p *Proxy) SawOwnOrigin() bool { return p.sawOwn.Load() }

func (p *Proxy) block(sample string) {
	p.blocked.Add(1)
	if len(sample) > maxSampleLen {
		sample = sample[:maxSampleLen]
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.samples) >= maxSamples {
		return
	}
	for _, s := range p.samples {
		if s == sample {
			return
		}
	}
	p.samples = append(p.samples, sample)
}

func (p *Proxy) isDenyAll() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.denyAll
}

func (p *Proxy) isLocal(ip netip.Addr) bool {
	_, ok := p.local[ip]
	return ok
}

func (p *Proxy) hardDenyPort(port int) bool {
	return (port >= p.poolFirst && port < p.poolFirst+p.poolCount) || port == p.proxyPort ||
		func() bool {
			for _, r := range p.policy.ReservedPorts {
				if r == port {
					return true
				}
			}
			return false
		}()
}

var errRefused = errors.New("destination refused")

// resolve returns the addresses of host: the literal address itself, or the lookup answer.
func (p *Proxy) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
	defer cancel()
	res, err := p.Lookup(lctx, host)
	if err != nil || len(res) == 0 {
		return nil, errRefused
	}
	addrs := make([]netip.Addr, 0, len(res))
	for _, a := range res {
		addrs = append(addrs, a.Unmap())
	}
	return addrs, nil
}

// check refuses a destination whose addresses a rendered page must not reach.
func (p *Proxy) check(host, port string, portNum int, addrs []netip.Addr) error {
	// No egressAllow entry may open another job's server, this proxy, the API or metrics. The upstream proxy's own
	// port is covered for local addresses only: refusing it for every name would break real sites on that port.
	if p.hardDenyPort(portNum) || (p.upstreamPort != 0 && portNum == p.upstreamPort) {
		for _, a := range addrs {
			if a.IsLoopback() || a.IsUnspecified() || p.isLocal(a) {
				return errRefused
			}
		}
	}

	if !p.policy.allowsHostPort(hostPortKey(host, port)) {
		for _, a := range addrs {
			if !p.policy.allowsPrefix(a) && (!IsPublicAddr(a) || p.isLocal(a)) {
				return errRefused
			}
		}
	}
	return nil
}

func targetsOf(addrs []netip.Addr, port string) []string {
	targets := make([]string, len(addrs))
	for i, a := range addrs {
		targets[i] = net.JoinHostPort(a.String(), port)
	}
	return targets
}

// vet resolves host and returns the vetted "ip:port" dial targets, or an error if the destination is refused.
func (p *Proxy) vet(ctx context.Context, host, port string) ([]string, error) {
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, errRefused
	}
	addrs, err := p.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	if err := p.check(host, port, portNum, addrs); err != nil {
		return nil, err
	}
	return targetsOf(addrs, port), nil
}

func (p *Proxy) isUpstreamAddr(a netip.Addr) bool {
	a = norm(a)
	for _, u := range p.upstreamAddrs {
		if u == a {
			return true
		}
	}
	return false
}

func (p *Proxy) isUpstreamName(host string) bool {
	return strings.TrimSuffix(strings.ToLower(host), ".") == p.policy.Upstream.Hostname()
}

// route is how one destination is reached.
type route struct {
	targets  []string // vetted "ip:port", or the single "host:port" for a by-name route
	upstream bool     // hand the targets to the upstream proxy
	byName   bool     // targets is a host name the upstream resolves (only with upstream)
}

// plan vets host:port and decides its route. A refused destination returns errRefused.
// The order of the checks is the security property: do not reorder.
func (p *Proxy) plan(ctx context.Context, host, port string) (route, error) {
	up := p.policy.Upstream
	if up == nil {
		targets, err := p.vet(ctx, host, port)
		if err != nil {
			return route{}, err
		}
		return route{targets: targets}, nil
	}
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return route{}, errRefused
	}
	if p.isUpstreamName(host) {
		return route{}, errRefused
	}

	_, perr := netip.ParseAddr(host)
	byName := up.ByName && perr != nil && !p.policy.allowsHostPort(hostPortKey(host, port))
	if !byName {
		// address-vetted route: literal IP, local mode, or a host with an exact egressAllow rule
		addrs, err := p.resolve(ctx, host)
		if err != nil {
			return route{}, errRefused
		}
		if err := p.check(host, port, portNum, addrs); err != nil {
			return route{}, err
		}
		for _, a := range addrs {
			if p.isUpstreamAddr(a) {
				return route{}, errRefused
			}
		}
		vetted := targetsOf(addrs, port)
		for _, a := range addrs {
			if !IsPublicAddr(a) || p.isLocal(norm(a)) {
				return route{targets: vetted}, nil // admitted only by egressAllow: always direct
			}
		}
		if up.direct(host, port, addrs) {
			return route{targets: vetted}, nil
		}
		return route{targets: vetted, upstream: true}, nil
	}

	// by-name route
	if LoopbackName(host) {
		return route{}, errRefused
	}
	if p.hardDenyPort(portNum) {
		return route{}, errRefused // a by-name route cannot prove its final address is not local
	}
	name := []string{net.JoinHostPort(host, port)}
	addrs, err := p.resolve(ctx, host)
	if err != nil {
		if up.direct(host, port, nil) {
			return route{}, errRefused // a direct dial needs an address
		}
		return route{targets: name, upstream: true, byName: true}, nil
	}
	if err := p.check(host, port, portNum, addrs); err != nil {
		return route{}, err
	}
	for _, a := range addrs {
		if p.isUpstreamAddr(a) {
			return route{}, errRefused
		}
	}
	vetted := targetsOf(addrs, port)
	for _, a := range addrs {
		if !IsPublicAddr(a) || p.isLocal(norm(a)) {
			return route{targets: vetted}, nil
		}
	}
	if up.direct(host, port, addrs) {
		return route{targets: vetted}, nil
	}
	return route{targets: name, upstream: true, byName: true}, nil
}

func (p *Proxy) dialTargets(ctx context.Context, targets []string) (net.Conn, error) {
	var last error
	for _, t := range targets {
		c, err := p.Dial(ctx, "tcp", t)
		if err == nil {
			return c, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no dial targets")
	}
	return nil, last
}

// transportDial ignores addr: the only addresses ever dialled are the vetted ones carried in ctx.
func (p *Proxy) transportDial(ctx context.Context, _, _ string) (net.Conn, error) {
	targets, _ := ctx.Value(targetsKey{}).([]string)
	return p.dialTargets(ctx, targets)
}

// tunnelDial ignores addr like transportDial: it asks the upstream for a tunnel to each vetted address in turn.
// net/http hands dial functions a context detached from request cancellation, so a Close during the handshake is
// bounded by respHeaderLimit rather than cut at once; there is no security impact, tunnel registration re-checks denyAll.
func (p *Proxy) tunnelDial(ctx context.Context, _, _ string) (net.Conn, error) {
	targets, _ := ctx.Value(targetsKey{}).([]string)
	return p.dialViaUpstream(ctx, targets)
}

var (
	errUpstreamAuth = errors.New("upstream proxy authentication failed")
	errUpstreamTLS  = errors.New("upstream proxy tls handshake failed")
)

func (p *Proxy) upstreamTLSConfig() *tls.Config {
	cfg := &tls.Config{}
	if p.UpstreamTLS != nil {
		cfg = p.UpstreamTLS.Clone()
	}
	if cfg.ServerName == "" {
		cfg.ServerName = p.policy.Upstream.Hostname()
	}
	cfg.MinVersion = tls.VersionTLS12
	return cfg
}

// dialUpstreamProxyRaw dials the upstream proxy itself at the addresses vetted in Start, in order (never
// re-resolving its name). The upstream proxy's own address is the one destination in this package that comes from
// operator config rather than from a rendered page.
func (p *Proxy) dialUpstreamProxyRaw(ctx context.Context) (net.Conn, error) {
	var last error
	for _, a := range p.upstreamAddrs {
		c, err := p.Dial(ctx, "tcp", net.JoinHostPort(a.String(), strconv.Itoa(p.upstreamPort)))
		if err == nil {
			return c, nil
		}
		last = err
	}
	if last == nil {
		last = errors.New("no upstream proxy addresses")
	}
	return nil, last
}

// dialUpstreamProxy is dialUpstreamProxyRaw plus the TLS hop for an https:// upstream.
func (p *Proxy) dialUpstreamProxy(ctx context.Context) (net.Conn, error) {
	conn, err := p.dialUpstreamProxyRaw(ctx)
	if err != nil {
		return nil, err
	}
	if p.policy.Upstream.URL.Scheme != "https" {
		return conn, nil
	}
	tc := tls.Client(conn, p.upstreamTLSConfig())
	if err := tc.HandshakeContext(ctx); err != nil {
		_ = conn.Close()
		return nil, errUpstreamTLS
	}
	return tc, nil
}

type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) { return c.r.Read(b) }

// authLatched reports whether a recent 407 blocks further upstream use, and records the attempt if so.
func (p *Proxy) authLatched() bool {
	if time.Now().UnixNano() < p.policy.Upstream.authBlockedUntil.Load() {
		p.upstreamFailed("407 (latched)")
		return true
	}
	return false
}

func (p *Proxy) latchAuth() {
	up := p.policy.Upstream
	up.authBlockedUntil.Store(time.Now().Add(up.AuthCooldown).UnixNano())
}

// dialUpstream asks the upstream proxy for a tunnel to target and returns it once the upstream answers 200.
func (p *Proxy) dialUpstream(ctx context.Context, target string) (net.Conn, error) {
	up := p.policy.Upstream
	if p.authLatched() {
		return nil, errUpstreamAuth
	}
	conn, err := p.dialUpstreamProxy(ctx)
	if err != nil {
		// fixed strings: err.Error() can contain the address
		if errors.Is(err, errUpstreamTLS) {
			p.upstreamFailed("tls handshake failed")
		} else {
			p.upstreamFailed("dial failed")
		}
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
	defer stop()

	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: target}, Host: target, Header: http.Header{}}
	if up.URL.User != nil {
		pw, _ := up.URL.User.Password()
		req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(up.URL.User.Username()+":"+pw)))
	}
	_ = conn.SetDeadline(time.Now().Add(respHeaderLimit))
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		p.upstreamFailed("no response")
		return nil, errors.New("upstream proxy request failed")
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		p.upstreamFailed("no response")
		return nil, errors.New("upstream proxy gave no response")
	}
	if resp.StatusCode == http.StatusProxyAuthRequired {
		_ = conn.Close()
		p.latchAuth()
		p.upstreamFailed(resp.Status)
		return nil, errUpstreamAuth
	}
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		p.upstreamFailed(resp.Status)
		return nil, fmt.Errorf("upstream proxy refused tunnel: %s", resp.Status)
	}
	if !stop() { // ctx ended while we were reading: the deadline may already be poisoned
		_ = conn.Close()
		return nil, ctx.Err()
	}
	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// dialViaUpstream tries each target through its own upstream tunnel, in order, like dialTargets, but stops at once
// on errUpstreamAuth.
func (p *Proxy) dialViaUpstream(ctx context.Context, targets []string) (net.Conn, error) {
	var last error
	for _, t := range targets {
		c, err := p.dialUpstream(ctx, t)
		if err == nil {
			return c, nil
		}
		if errors.Is(err, errUpstreamAuth) {
			return nil, err
		}
		last = err
	}
	if last == nil {
		last = errors.New("no dial targets")
	}
	return nil, last
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch {
	case p.isDenyAll():
		sample := req.Host
		if req.Method != http.MethodConnect && req.URL.IsAbs() {
			sample = req.URL.String()
		}
		p.block(sample)
		http.Error(w, "blocked", http.StatusForbidden)
	case req.Method == http.MethodConnect:
		p.connect(w, req)
	case req.URL.IsAbs() && req.URL.Scheme == "http":
		p.forward(w, req)
	default:
		http.Error(w, "unsupported request", http.StatusBadRequest)
	}
}

func stripHopByHop(h http.Header) {
	for _, v := range h["Connection"] {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				h.Del(name)
			}
		}
	}
	for _, name := range hopByHop {
		h.Del(name)
	}
}

func abortConn(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if c, _, err := hj.Hijack(); err == nil {
			_ = c.Close()
			return
		}
	}
	http.Error(w, "bad gateway", http.StatusBadGateway)
}

func (p *Proxy) forward(w http.ResponseWriter, req *http.Request) {
	own := strings.ToLower(req.URL.Host) == p.ownHost
	var targets []string
	var rt route
	if own {
		p.sawOwn.Store(true)
		targets = []string{p.ownDial}
	} else {
		port := req.URL.Port()
		if port == "" {
			port = "80"
		}
		var err error
		rt, err = p.plan(req.Context(), req.URL.Hostname(), port)
		if err != nil {
			p.block(req.URL.String())
			http.Error(w, "blocked", http.StatusForbidden)
			return
		}
		targets = rt.targets
	}

	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(p.base, cancel)
	defer stop()
	defer cancel()
	outReq := req.Clone(context.WithValue(ctx, targetsKey{}, targets))
	outReq.RequestURI = ""
	outReq.Close = false // the client's connection semantics do not apply to the upstream hop
	outReq.URL.Host = targets[0]
	outReq.Host = req.Host // the zpt server checks Host against its own name
	stripHopByHop(outReq.Header)

	transport := p.transport
	switch {
	case rt.upstream && rt.byName:
		transport = p.upstreamTransport
		if p.authLatched() {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
	case rt.upstream:
		transport = p.tunnelTransport
	}

	resp, err := transport.RoundTrip(outReq)
	// a forwarded 407 may come from the origin: relay it unchanged, never latch or record its status text
	if err != nil && rt.upstream && rt.byName {
		p.upstreamFailed("request failed")
	}
	if err != nil {
		if own {
			// close without a response so Chrome fails the navigation instead of printing an error page
			abortConn(w)
			return
		}
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	stripHopByHop(resp.Header)
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		// abort the connection so the client sees a truncated body, not a complete one
		panic(http.ErrAbortHandler)
	}
}

func (p *Proxy) connect(w http.ResponseWriter, req *http.Request) {
	host, port, err := net.SplitHostPort(req.Host)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	// Chrome never needs a tunnel to the job's own server
	if hostPortKey(host, port) == p.ownHost {
		p.block(req.Host)
		http.Error(w, "blocked", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithCancel(req.Context())
	stopBase := context.AfterFunc(p.base, cancel)
	defer stopBase()
	defer cancel()
	r, err := p.plan(ctx, host, port)
	if err != nil {
		p.block(req.Host)
		http.Error(w, "blocked", http.StatusForbidden)
		return
	}
	var up net.Conn
	if r.upstream {
		up, err = p.dialViaUpstream(ctx, r.targets)
	} else {
		up, err = p.dialTargets(ctx, r.targets)
	}
	if err != nil {
		http.Error(w, "bad gateway", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		_ = up.Close()
		http.Error(w, "cannot hijack", http.StatusInternalServerError)
		return
	}
	client, brw, err := hj.Hijack()
	if err != nil {
		_ = up.Close()
		return
	}

	p.mu.Lock()
	if p.closed || p.denyAll {
		p.mu.Unlock()
		_ = client.Close()
		_ = up.Close()
		return
	}
	p.tunnels[client] = struct{}{}
	p.tunnels[up] = struct{}{}
	p.mu.Unlock()
	defer func() {
		_ = client.Close()
		_ = up.Close()
		p.mu.Lock()
		delete(p.tunnels, client)
		delete(p.tunnels, up)
		p.mu.Unlock()
	}()

	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		return
	}
	// bytes the client sent right behind CONNECT are already in the server's buffer
	if n := brw.Reader.Buffered(); n > 0 {
		b, _ := brw.Peek(n)
		if _, err := up.Write(b); err != nil {
			return
		}
	}

	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyIdle(up, client, p.TunnelIdle, &last) }()
	go func() { defer wg.Done(); copyIdle(client, up, p.TunnelIdle, &last) }()
	wg.Wait()
}

// copyIdle copies src to dst until either side ends or the tunnel (both directions, tracked in last) has
// moved no data for idle, then closes both.
func copyIdle(dst, src net.Conn, idle time.Duration, last *atomic.Int64) {
	defer func() {
		_ = dst.Close()
		_ = src.Close()
	}()
	poll := min(5*time.Second, idle/4)
	buf := make([]byte, 32*1024)
	for {
		_ = src.SetReadDeadline(time.Now().Add(poll))
		n, err := src.Read(buf)
		if n > 0 {
			last.Store(time.Now().UnixNano())
			_ = dst.SetWriteDeadline(time.Now().Add(idle))
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
			last.Store(time.Now().UnixNano())
		}
		if err != nil {
			if errors.Is(err, os.ErrDeadlineExceeded) && time.Since(time.Unix(0, last.Load())) < idle {
				continue
			}
			return
		}
	}
}
