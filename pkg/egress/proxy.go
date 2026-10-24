package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
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

	ownHost   string
	ownDial   string
	poolFirst int
	poolCount int
	policy    Policy

	listener  net.Listener
	server    *http.Server
	transport *http.Transport
	base      context.Context // cancelled to abort in-flight forwards
	cancel    context.CancelFunc
	proxyPort int
	local     map[netip.Addr]struct{}

	mu      sync.Mutex
	closed  bool
	denyAll bool
	tunnels map[net.Conn]struct{}
	samples []string

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
		p.transport.CloseIdleConnections()
	}
	for c := range p.tunnels {
		_ = c.Close()
	}
	p.tunnels = map[net.Conn]struct{}{}
	p.mu.Unlock()
	return err
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
	p.transport.CloseIdleConnections()

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

// vet resolves host and returns the vetted "ip:port" dial targets, or an error if the destination is refused.
func (p *Proxy) vet(ctx context.Context, host, port string) ([]string, error) {
	portNum, err := strconv.Atoi(port)
	if err != nil {
		return nil, errRefused
	}

	var addrs []netip.Addr
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = []netip.Addr{ip.Unmap()}
	} else {
		lctx, cancel := context.WithTimeout(ctx, lookupTimeout)
		defer cancel()
		res, err := p.Lookup(lctx, host)
		if err != nil || len(res) == 0 {
			return nil, errRefused
		}
		for _, a := range res {
			addrs = append(addrs, a.Unmap())
		}
	}

	// No egressAllow entry may open another job's server, this proxy, the API or metrics.
	if p.hardDenyPort(portNum) {
		for _, a := range addrs {
			if a.IsLoopback() || a.IsUnspecified() || p.isLocal(a) {
				return nil, errRefused
			}
		}
	}

	if !p.policy.allowsHostPort(hostPortKey(host, port)) {
		for _, a := range addrs {
			if !p.policy.allowsPrefix(a) && !(IsPublicAddr(a) && !p.isLocal(a)) {
				return nil, errRefused
			}
		}
	}

	targets := make([]string, len(addrs))
	for i, a := range addrs {
		targets[i] = net.JoinHostPort(a.String(), port)
	}
	return targets, nil
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
	if own {
		p.sawOwn.Store(true)
		targets = []string{p.ownDial}
	} else {
		port := req.URL.Port()
		if port == "" {
			port = "80"
		}
		var err error
		targets, err = p.vet(req.Context(), req.URL.Hostname(), port)
		if err != nil {
			p.block(req.URL.String())
			http.Error(w, "blocked", http.StatusForbidden)
			return
		}
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

	resp, err := p.transport.RoundTrip(outReq)
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
	targets, err := p.vet(req.Context(), host, port)
	if err != nil {
		p.block(req.Host)
		http.Error(w, "blocked", http.StatusForbidden)
		return
	}
	up, err := p.dialTargets(req.Context(), targets)
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
		b, _ := brw.Reader.Peek(n)
		if _, err := up.Write(b); err != nil {
			return
		}
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); copyIdle(up, client) }()
	go func() { defer wg.Done(); copyIdle(client, up) }()
	wg.Wait()
}

// copyIdle copies src to dst until either side ends or src is idle for tunnelIdle, then closes both.
func copyIdle(dst, src net.Conn) {
	defer func() {
		_ = dst.Close()
		_ = src.Close()
	}()
	buf := make([]byte, 32*1024)
	for {
		_ = src.SetReadDeadline(time.Now().Add(tunnelIdle))
		n, err := src.Read(buf)
		if n > 0 {
			_ = dst.SetWriteDeadline(time.Now().Add(tunnelIdle))
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			return
		}
	}
}
