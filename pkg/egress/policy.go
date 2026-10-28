package egress

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Rule is one egressAllow entry: a CIDR, or an exact host:port.
type Rule struct {
	Prefix   netip.Prefix // valid for CIDR rules
	HostPort string       // lower-cased "host:port" for exact rules
}

// ParseRule accepts a CIDR prefix, or host:port with a port in 1..65535.
// A bare IP without a prefix length is rejected; write /32 or /128.
func ParseRule(s string) (Rule, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return Rule{Prefix: p.Masked()}, nil
	}
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return Rule{}, fmt.Errorf("%q is neither a CIDR nor host:port", s)
	}
	if host == "" {
		return Rule{}, errors.New("empty host")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return Rule{}, fmt.Errorf("invalid port %q", port)
	}
	return Rule{HostPort: hostPortKey(host, port)}, nil
}

func hostPortKey(host, port string) string {
	return strings.ToLower(net.JoinHostPort(host, port))
}

// Policy holds the operator-supplied egress configuration.
type Policy struct {
	Allow         []Rule
	ReservedPorts []int     // API and metrics ports: never reachable on local addresses, whatever Allow says
	Upstream      *Upstream // nil: dial vetted destinations directly
}

func (p Policy) allowsPrefix(ip netip.Addr) bool {
	for _, r := range p.Allow {
		if r.Prefix.IsValid() && r.Prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func (p Policy) allowsHostPort(key string) bool {
	for _, r := range p.Allow {
		if r.HostPort != "" && r.HostPort == key {
			return true
		}
	}
	return false
}

const (
	defaultAuthCooldown  = 5 * time.Minute
	authLatchTargets     = 3                // distinct destinations answered 407 before every destination is blocked
	upstreamCacheTTL     = 30 * time.Second // how long one answer for the upstream's own name is reused
	maxSeenUpstreamAddrs = 64
	maxAuthFails         = 1024 // destinations remembered with a 407; the oldest is forgotten first
)

// Upstream routes vetted public traffic through an operator's HTTP(S) proxy. One value is shared by every job's
// proxy (Policy is copied, the pointer is not), which is what makes the 407 latch and the address cache process-wide.
type Upstream struct {
	URL          *url.URL      // http:// or https:// proxy; userinfo, if any, is sent as Basic Proxy-Authorization
	ByName       bool          // resolve "upstream": hand the upstream the host name instead of the vetted IP
	NoProxy      []Rule        // public destinations dialled directly instead (still vetted)
	AuthCooldown time.Duration // 0: 5m; how long a 407 blocks further attempts; test seam

	authBlockedUntil atomic.Int64 // unix nanos; set once authLatchTargets distinct destinations got a 407 (credentials only)

	mu        sync.Mutex
	authFails map[string]time.Time // requested destination ("host:port") -> its last 407; expired entries pruned lazily
	cached    []netip.Addr         // last answer for the upstream's name, as resolved
	cachedAt  time.Time
	seen      []netip.Addr // addresses that name has resolved to (unmapped, zone dropped), least recently seen first
}

func (u *Upstream) cooldown() time.Duration {
	if u.AuthCooldown > 0 {
		return u.AuthCooldown
	}
	return defaultAuthCooldown
}

// authBlocked reports whether dest (the requested "host:port") may not be tried: every destination is blocked once
// the latch tripped, and a destination that got a 407 itself stays blocked for the cooldown.
func (u *Upstream) authBlocked(target string, now time.Time) bool {
	if now.UnixNano() < u.authBlockedUntil.Load() {
		return true
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	at, ok := u.authFails[target]
	return ok && now.Sub(at) < u.cooldown()
}

// record407 notes a 407 in reply to our own CONNECT for dest, the requested "host:port" (never a vetted IP: one
// destination's addresses rotate). A 407 can also be the upstream's policy for that one destination, so a single
// destination never blocks the rest: only authLatchTargets distinct destinations within the cooldown (a wrong
// password fails everywhere) trip the latch. Without credentials there is no password to lock out, so a 407 blocks
// only its own destination.
func (u *Upstream) record407(dest string, now time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.authFails == nil {
		u.authFails = map[string]time.Time{}
	}
	for t, at := range u.authFails {
		if now.Sub(at) >= u.cooldown() {
			delete(u.authFails, t)
		}
	}
	if _, ok := u.authFails[dest]; !ok && len(u.authFails) >= maxAuthFails {
		var oldest string
		var at time.Time
		for t, a := range u.authFails {
			if oldest == "" || a.Before(at) {
				oldest, at = t, a
			}
		}
		delete(u.authFails, oldest)
	}
	u.authFails[dest] = now
	if u.URL.User != nil && len(u.authFails) >= authLatchTargets {
		u.authBlockedUntil.Store(now.Add(u.cooldown()).UnixNano())
	}
}

// addrs returns the upstream's addresses to dial (the cached answer while fresh, else a new lookup) and, for the
// destination refusal, the current answer plus the addresses its name resolved to before (up to the
// maxSeenUpstreamAddrs most recently seen): a rotating DNS answer must not hide one of the upstream's addresses.
func (u *Upstream) addrs(now time.Time, lookup func() ([]netip.Addr, error)) (dial, refuse []netip.Addr, err error) {
	u.mu.Lock()
	if u.cached != nil && now.Sub(u.cachedAt) < upstreamCacheTTL {
		dial, refuse = u.cached, u.refusals(u.cached)
		u.mu.Unlock()
		return dial, refuse, nil
	}
	u.mu.Unlock()

	res, err := lookup() // not under mu: a slow resolver must not serialise every job behind it
	if err != nil {
		return nil, nil, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cached, u.cachedAt = res, now
	for _, a := range res {
		a = a.WithZone("").Unmap() // not norm: ::1 must stay loopback for the on-host checks
		for i, s := range u.seen {
			if s == a {
				u.seen = append(u.seen[:i:i], u.seen[i+1:]...) // re-seen: move to the most recent end
				break
			}
		}
		u.seen = append(u.seen, a)
	}
	if len(u.seen) > maxSeenUpstreamAddrs {
		u.seen = append([]netip.Addr(nil), u.seen[len(u.seen)-maxSeenUpstreamAddrs:]...)
	}
	return res, u.refusals(res), nil
}

// refusals returns the history plus the current answer, which an answer larger than the history leaves out.
// Called with mu held.
func (u *Upstream) refusals(current []netip.Addr) []netip.Addr {
	refuse := append([]netip.Addr(nil), u.seen...)
	for _, a := range current {
		a = a.WithZone("").Unmap()
		if !slices.Contains(refuse, a) {
			refuse = append(refuse, a)
		}
	}
	return refuse
}

// refusalsCached is the refusal list without a lookup: the addresses the upstream's name resolved to recently, in
// any job, plus the cached answer; stale reports whether that answer is older than upstreamCacheTTL.
func (u *Upstream) refusalsCached(now time.Time) (refuse []netip.Addr, stale bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.refusals(u.cached), u.cached == nil || now.Sub(u.cachedAt) >= upstreamCacheTTL
}

// ParseUpstream validates the zipReport.upstreamProxy settings. An empty rawURL disables chaining and returns nil.
// Errors never include rawURL or any part of it: it may carry a password.
func ParseUpstream(rawURL, resolve string, noProxy []string) (*Upstream, error) {
	if rawURL == "" {
		if (resolve != "" && resolve != "local") || len(noProxy) > 0 {
			return nil, errors.New("url is required when resolve or noProxy is set")
		}
		return nil, nil
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		// never wrap err: it quotes the whole input, password included
		return nil, errors.New("url is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		// never echo the scheme: for "user:pass@proxy:3128" it is the username
		return nil, errors.New("url scheme is not supported; use http or https")
	}
	if u.Hostname() == "" {
		return nil, errors.New("url has no host")
	}
	if n, err := strconv.Atoi(u.Port()); err != nil || n < 1 || n > 65535 {
		return nil, errors.New("url must include a port, e.g. http://proxy:3128")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("url must not have a path, query or fragment")
	}
	if resolve != "" && resolve != "local" && resolve != "upstream" {
		return nil, fmt.Errorf("resolve %q must be \"local\" or \"upstream\"", resolve)
	}
	if resolve == "upstream" {
		if LoopbackName(u.Hostname()) {
			return nil, errors.New("resolve \"upstream\" cannot be used with an upstream proxy on this host")
		}
		if ip, err := netip.ParseAddr(u.Hostname()); err == nil && OnThisHost(ip, nil) {
			return nil, errors.New("resolve \"upstream\" cannot be used with an upstream proxy on this host")
		}
	}
	var rules []Rule
	for i, s := range noProxy {
		r, err := ParseRule(s)
		if err != nil {
			return nil, fmt.Errorf("noProxy[%d]: %w", i, err)
		}
		if r.Prefix.IsValid() && !IsPublicAddr(r.Prefix.Addr()) {
			return nil, fmt.Errorf("noProxy[%d]: only public destinations use the upstream; private ones are always dialled directly", i)
		}
		rules = append(rules, r)
	}
	return &Upstream{URL: u, ByName: resolve == "upstream", NoProxy: rules, AuthCooldown: defaultAuthCooldown}, nil
}

// Endpoint returns "scheme://host:port" — the only form of the upstream that may be logged.
func (u *Upstream) Endpoint() string {
	return u.URL.Scheme + "://" + net.JoinHostPort(u.Hostname(), u.URL.Port())
}

// Hostname returns the upstream host, lower-cased, without a trailing dot.
func (u *Upstream) Hostname() string {
	return strings.TrimSuffix(strings.ToLower(u.URL.Hostname()), ".")
}

// LookupName returns the upstream host as it is resolved: Hostname(), plus one trailing dot when the operator wrote
// the name rooted ("proxy.corp."), so the resolver's search domains are skipped. An IP literal comes back without
// the dot: it is never a DNS query.
func (u *Upstream) LookupName() string {
	h := u.Hostname()
	if _, err := netip.ParseAddr(h); err == nil || !strings.HasSuffix(u.URL.Hostname(), ".") {
		return h
	}
	return strings.TrimRight(h, ".") + "."
}

var loopbackNames = map[string]bool{
	"localhost": true, "localhost.localdomain": true, "localhost4": true, "localhost4.localdomain4": true,
	"localhost6": true, "localhost6.localdomain6": true, "ip6-localhost": true, "ip6-loopback": true,
}

// LoopbackName reports whether host is a name that conventionally means this machine: "localhost" and any
// "*.localhost" (RFC 6761), "localhost.localdomain", "localhost4", "localhost4.localdomain4", "localhost6",
// "localhost6.localdomain6", "ip6-localhost", "ip6-loopback". Case-insensitive; one trailing dot is ignored.
func LoopbackName(host string) bool {
	h := strings.TrimSuffix(strings.ToLower(host), ".")
	return loopbackNames[h] || strings.HasSuffix(h, ".localhost")
}

// InterfaceIPs converts net.InterfaceAddrs results to addresses (IPv4-mapped unmapped); entries of other types are
// skipped.
func InterfaceIPs(addrs []net.Addr) []netip.Addr {
	var out []netip.Addr
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		}
		if na, ok := netip.AddrFromSlice(ip); ok {
			out = append(out, na.Unmap())
		}
	}
	return out
}

// OnThisHost reports whether ip is loopback, unspecified, or one of ifaces, as given (zone dropped, IPv4-mapped
// unmapped) or in its NAT64, 6to4 or IPv4-compatible form.
func OnThisHost(ip netip.Addr, ifaces []netip.Addr) bool {
	for _, c := range []netip.Addr{ip.WithZone("").Unmap(), norm(ip)} {
		if c.IsLoopback() || c.IsUnspecified() {
			return true
		}
		for _, a := range ifaces {
			if a.WithZone("").Unmap() == c {
				return true
			}
		}
	}
	return false
}

// direct reports whether a public destination bypasses the upstream: an exact host:port noProxy rule matches, or
// every vetted address lies inside a noProxy prefix. addrs may be empty (then only host:port rules can match).
func (u *Upstream) direct(host, port string, addrs []netip.Addr) bool {
	key := hostPortKey(host, port)
	for _, r := range u.NoProxy {
		if r.HostPort != "" && r.HostPort == key {
			return true
		}
	}
	if len(addrs) == 0 {
		return false
	}
	for _, a := range addrs {
		a = norm(a)
		in := false
		for _, r := range u.NoProxy {
			if r.Prefix.IsValid() && r.Prefix.Contains(a) {
				in = true
				break
			}
		}
		if !in {
			return false
		}
	}
	return true
}

// norm returns the canonical form used for address comparisons: unmapped, zone dropped, and the IPv4 address
// embedded in NAT64 / 6to4 / IPv4-compatible forms (embeddedIPv4, addr.go) when present.
func norm(a netip.Addr) netip.Addr {
	a = a.WithZone("").Unmap()
	if v4, ok := embeddedIPv4(a); ok {
		return v4
	}
	return a
}
