package egress

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
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
	ReservedPorts []int // API and metrics ports: never reachable on local addresses, whatever Allow says
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
