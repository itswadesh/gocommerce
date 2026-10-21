package gocommerce

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Addresses a store chooses and the server calls (D109).
//
// A store operator types addresses the server then connects to: a webhook
// endpoint, a provider's API base URL, a search host, a shop to import from.
// Where the person typing is the person who runs the server, any address is
// theirs to choose. Where the install hosts stores for other people, it is
// not: what sits beside the server on its own network was never offered to
// them. So unless the install says otherwise (Config.AllowPrivateURLs), those
// requests go only to public addresses.
//
// The check is made on the address being connected to, after the name has
// been resolved, for every connection — redirects included, since each hop
// dials again. Checking the URL when it is saved is a courtesy and nothing
// more: a name can resolve to one address when it is saved and another when
// it is used.

// ErrNotPublic is what a request from an OutboundClient fails with when the
// address it would connect to is not a public one. Match it with errors.Is;
// the error itself names the host that was asked for.
var ErrNotPublic = errors.New("not a public address")

// notPublicError names the host the request asked for. It deliberately
// carries nothing else: the address the name resolved to is something only
// the inside of the network knows, and the operator reading this typed the
// name, not the address.
//
// For a name it reads the same whether the name resolved somewhere private
// or did not resolve at all. Two different messages would let anyone who can
// set an address and read the error find out which names exist inside the
// network.
type notPublicError struct{ host string }

func (e *notPublicError) Error() string {
	if _, err := netip.ParseAddr(e.host); err == nil {
		return e.host + " is not a public address"
	}
	return e.host + " does not resolve to a public address"
}
func (e *notPublicError) Unwrap() error { return ErrNotPublic }

// OutboundClient returns a client for calling an address a store operator or
// imported data chose, with the given timeout. Unless the install allows
// private addresses it connects only to public ones, and a request to
// anything else fails with an error matching ErrNotPublic. With them allowed
// it is an ordinary client.
//
// A module builds its default client here rather than with &http.Client{}
// whenever where it connects can be decided by a store setting or by data it
// fetched. A client the store's own main() injects is used as given.
func (a *App) OutboundClient(timeout time.Duration) *http.Client {
	if a.cfg.AllowPrivateURLs {
		return &http.Client{Timeout: timeout}
	}
	return &http.Client{Timeout: timeout, Transport: publicTransport()}
}

// publicTransport is shared by every guarded client, so modules share one
// connection pool the way clients on http.DefaultTransport do.
var publicTransport = sync.OnceValue(func() *http.Transport {
	dialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
		Control:   refuseNonPublic,
	}
	return &http.Transport{
		// No proxy, whatever the environment says. Through a proxy the dialer
		// sees only the proxy's address, and the destination it is here to
		// judge travels inside the request where nothing checks it.
		Proxy: nil,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			var dnsErr *net.DNSError
			if errors.Is(err, ErrNotPublic) || errors.As(err, &dnsErr) {
				// The dialer's own error names the resolved address, or the
				// resolver it asked; this one names only what was asked for,
				// and a failed lookup reads as a refusal (notPublicError).
				host, _, splitErr := net.SplitHostPort(addr)
				if splitErr != nil {
					host = addr
				}
				return nil, &notPublicError{host: host}
			}
			return conn, err
		},
		// The rest is http.DefaultTransport's.
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
})

// refuseNonPublic runs after the name is resolved and before the socket
// connects, once for every address the dialer tries. An address it cannot
// read is refused: the guard fails closed.
func refuseNonPublic(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil || !publicAddr(ap.Addr()) {
		return ErrNotPublic
	}
	return nil
}

// nonPublicPrefixes are the ranges netip's own predicates leave out: shared
// address space, the documentation and benchmarking ranges, reserved space,
// and the IPv6 prefixes that carry an IPv4 address inside them — through
// which a private IPv4 address could be reached by an IPv6 one.
var nonPublicPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// publicAddr is whether an address is somewhere on the public internet.
//
// Unmap first, so ::ffff:127.0.0.1 is judged as the 127.0.0.1 it is; drop
// the zone, because netip.Prefix never matches an address that has one.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// ValidateOutboundURL is the check for an address a store is about to save:
// an http or https URL with a host and — unless the install allows private
// addresses — not an IP address that is not public, nor localhost or a name
// under .localhost, .local or .internal. Its error is a validation error,
// ready for RespondError.
//
// It reads the URL and resolves nothing, so it is not the boundary;
// OutboundClient is. Its job is to tell the operator while they are typing,
// rather than in a failed delivery later.
func (a *App) ValidateOutboundURL(raw string) error {
	if problem := a.outboundURLProblem(raw); problem != "" {
		return Validationf("%s", problem)
	}
	return nil
}

// outboundURLProblem is ValidateOutboundURL's finding as a sentence, so a
// caller can put a field's name in front of it.
func (a *App) outboundURLProblem(raw string) string {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Sprintf("%q is not a web address", raw)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Sprintf("%q must start with https://", raw)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Sprintf("%q has no host", raw)
	}
	if a.cfg.AllowPrivateURLs || publicName(host) {
		return ""
	}
	return host + " is not a public address, and this server connects only to public ones"
}

// publicName is whether a host, as typed, could be a public address: an IP
// address that is public, or a name outside the suffixes that by convention
// never are.
//
// A host whose last label is a number is not a name at all. Browsers read
// it as an IPv4 address in one of the older spellings — 2130706433,
// 0x7f000001, 0177.0.0.1, 127.1 — and some resolvers do too, while
// netip.ParseAddr reads none of them; so it is refused rather than judged.
func publicName(host string) bool {
	name := strings.TrimRight(strings.ToLower(host), ".")
	if ip, err := netip.ParseAddr(name); err == nil {
		return publicAddr(ip)
	}
	if name == "" || name == "localhost" {
		return false
	}
	if numericLabel(name[strings.LastIndexByte(name, '.')+1:]) {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal"} {
		if strings.HasSuffix(name, suffix) {
			return false
		}
	}
	return true
}

// numericLabel is whether a label reads as a number: decimal digits, or 0x
// and hex digits.
func numericLabel(label string) bool {
	digits, base := label, "0123456789"
	if len(label) >= 2 && label[0] == '0' && label[1] == 'x' {
		digits, base = label[2:], "0123456789abcdef"
	} else if label == "" {
		return false
	}
	for _, c := range digits {
		if !strings.ContainsRune(base, c) {
			return false
		}
	}
	return true
}

// checkOutbound says when the addresses a store sets may reach the server's
// own network. That is right where everyone who sets them runs the server,
// and wrong where the install hosts stores for other people, and nothing else
// in the report tells the two apart.
func (a *App) checkOutbound() Diagnostic {
	d := Diagnostic{Name: "outbound addresses"}
	if !a.cfg.AllowPrivateURLs {
		d.Status = StatusOK
		d.Detail = "webhook endpoints and the other addresses a store sets are called only when they are public"
		return d
	}
	d.Status = StatusWarn
	d.Detail = "private addresses are allowed: a store operator can make this server call addresses on its own network"
	if a.cfg.Signup {
		d.Detail += ", and sign-up is on, so anybody can open a store and become one"
	}
	d.Hint = "keep GOCOMMERCE_ALLOW_PRIVATE_URLS (-allow-private-urls) only on an install where every store " +
		"operator is trusted with the server itself, such as one business's own store or a development machine"
	return d
}
