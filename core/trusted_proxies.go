package gocommerce

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
)

// ParseTrustedProxies reads Config.TrustedProxies from a comma-separated list
// of CIDR ranges and bare addresses, a bare address being a range of one (a
// /32 or a /128). Empty is none. An entry that does not read is an error
// rather than dropped: a proxy silently left out puts every shopper behind it
// under one limit again, which is what the setting exists to end.
func ParseTrustedProxies(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		p, err := netip.ParsePrefix(entry)
		if err != nil {
			addr, aerr := netip.ParseAddr(entry)
			if aerr != nil {
				return nil, fmt.Errorf("trusted proxy %q is neither an address nor a CIDR range such as 10.0.0.0/8", entry)
			}
			p = netip.PrefixFrom(addr, addr.BitLen())
		}
		// A request's address is compared unmapped, so a range written as
		// IPv4-in-IPv6 would never match anything.
		if p.Addr().Is4In6() {
			return nil, fmt.Errorf("trusted proxy %q is an IPv4 address written as IPv6; write it as IPv4", entry)
		}
		out = append(out, p)
	}
	return out, nil
}

// trustedProxiesMW makes r.RemoteAddr the client's address when the request
// came from a proxy in Config.TrustedProxies, so every limit that counts by
// address (clientIP, and each module's copy of it) counts shoppers rather than
// the proxy. It changes a shallow copy, never the caller's request.
func (a *App) trustedProxiesMW(next http.Handler) http.Handler {
	trusted := a.cfg.TrustedProxies
	if len(trusted) == 0 {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if peer, err := netip.ParseAddrPort(r.RemoteAddr); err == nil {
			remote := peer.Addr().Unmap()
			if addr := clientAddr(remote, r.Header.Values("X-Forwarded-For"), trusted); addr != remote {
				proxied := *r
				proxied.RemoteAddr = net.JoinHostPort(addr.String(), "0")
				r = &proxied
			}
		}
		next.ServeHTTP(w, r)
	})
}

// clientAddr is the address a request from remote is counted against. Only a
// trusted remote is believed about X-Forwarded-For. Each proxy appends the
// address it heard from, so the walk runs from the right, passes the proxies
// the install named, and stops at the first hop none of them vouches for:
// everything left of it was written by the client. An entry that is not an
// address ends the walk at the last one that was; with every hop trusted, the
// leftmost is the client.
func clientAddr(remote netip.Addr, forwarded []string, trusted []netip.Prefix) netip.Addr {
	if !isTrustedProxy(remote, trusted) {
		return remote
	}
	var hops []string
	for _, line := range forwarded {
		hops = append(hops, strings.Split(line, ",")...)
	}
	addr := remote
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		addr = hop.Unmap()
		if !isTrustedProxy(addr, trusted) {
			break
		}
	}
	return addr
}

func isTrustedProxy(addr netip.Addr, trusted []netip.Prefix) bool {
	return slices.ContainsFunc(trusted, func(p netip.Prefix) bool { return p.Contains(addr) })
}
