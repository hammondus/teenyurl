package main

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// proxyTrust decides which peers may set forwarding headers.
type proxyTrust struct {
	nets []netip.Prefix
}

// parseTrustedProxies reads a comma-separated list of CIDR blocks.
func parseTrustedProxies(list string) (*proxyTrust, error) {
	p := &proxyTrust{}
	for _, field := range strings.Split(list, ",") {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		n, err := netip.ParsePrefix(field)
		if err != nil {
			return nil, fmt.Errorf("trusted proxy %q: %w", field, err)
		}
		p.nets = append(p.nets, n.Masked())
	}
	return p, nil
}

func (p *proxyTrust) trusted(a netip.Addr) bool {
	if !a.IsValid() {
		return false
	}
	a = a.Unmap()
	for _, n := range p.nets {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// clientIP returns the address to attribute a request to.
//
// X-Forwarded-For is attacker-controlled: anyone can send it. Honour it only
// when the immediate peer is a trusted proxy, then walk the list right to
// left, skipping further trusted hops. The first untrusted address is the
// client; if the peer is not trusted, ignore the header entirely.
//
// This is not only a logging concern. An attacker who can forge the header
// resets their own login rate limit bucket.
func (p *proxyTrust) clientIP(r *http.Request) netip.Addr {
	peer := peerAddr(r)
	if !p.trusted(peer) {
		return peer
	}
	var hops []string
	for _, v := range r.Header.Values("X-Forwarded-For") {
		for _, s := range strings.Split(v, ",") {
			hops = append(hops, strings.TrimSpace(s))
		}
	}
	for i := len(hops) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(hops[i])
		if err != nil {
			// A malformed hop means nothing to its left can be believed.
			return peer
		}
		if a = a.Unmap(); !p.trusted(a) {
			return a
		}
	}
	return peer
}

// peerAddr is the address at the other end of the connection.
func peerAddr(r *http.Request) netip.Addr {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return netip.Addr{}
	}
	return a.Unmap().WithZone("")
}
