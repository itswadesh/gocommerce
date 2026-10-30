package gocommerce

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestParseTrustedProxies(t *testing.T) {
	got, err := ParseTrustedProxies(" 10.0.0.0/8, 192.0.2.7 ,,2001:db8::/32,::1")
	if err != nil {
		t.Fatal(err)
	}
	if s := fmt.Sprint(got); s != "[10.0.0.0/8 192.0.2.7/32 2001:db8::/32 ::1/128]" {
		t.Errorf("parsed %s", s)
	}
	if got, err := ParseTrustedProxies(""); err != nil || got != nil {
		t.Errorf("empty = %v, %v; want none", got, err)
	}
	for _, bad := range []string{"10.0.0.0/33", "proxy.internal", "10.0.0.1:8080", "::ffff:10.0.0.1", "10.0.0.1, nope"} {
		if got, err := ParseTrustedProxies(bad); err == nil {
			t.Errorf("%q = %v, want an error", bad, got)
		}
	}
}

// What a route behind the engine sees as RemoteAddr, for a request from
// remote carrying these X-Forwarded-For lines.
func TestTrustedProxiesResolveTheClient(t *testing.T) {
	trusted, err := ParseTrustedProxies("10.0.0.0/8, 192.0.2.10, 2001:db8:ffff::/48")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name      string
		noProxies bool
		remote    string
		xff       []string
		want      string
	}{
		{name: "untrusted remote keeps its address and a spoofed header is ignored",
			remote: "203.0.113.5:4444", xff: []string{"198.51.100.1"}, want: "203.0.113.5:4444"},
		{name: "trusted remote with no header keeps its address",
			remote: "10.0.0.2:5555", want: "10.0.0.2:5555"},
		{name: "trusted remote takes the rightmost untrusted hop, not what the client wrote left of it",
			remote: "10.0.0.2:5555", xff: []string{"198.51.100.9, 203.0.113.7"}, want: "203.0.113.7:0"},
		{name: "a chain of two trusted proxies",
			remote: "10.0.0.2:5555", xff: []string{"198.51.100.9, 203.0.113.7, 192.0.2.10"}, want: "203.0.113.7:0"},
		{name: "every hop trusted takes the leftmost",
			remote: "10.0.0.2:5555", xff: []string{"10.1.1.1, 192.0.2.10"}, want: "10.1.1.1:0"},
		{name: "garbage ends the walk at the last address before it",
			remote: "10.0.0.2:5555", xff: []string{"203.0.113.7, not-an-ip, 10.9.9.9"}, want: "10.9.9.9:0"},
		{name: "garbage on the right keeps the proxy's own address",
			remote: "10.0.0.2:5555", xff: []string{"203.0.113.7, unknown"}, want: "10.0.0.2:5555"},
		{name: "an address with a port is garbage",
			remote: "10.0.0.2:5555", xff: []string{"203.0.113.7:1234"}, want: "10.0.0.2:5555"},
		{name: "header lines are read as one list",
			remote: "10.0.0.2:5555", xff: []string{"198.51.100.9", "203.0.113.7", "10.3.3.3"}, want: "203.0.113.7:0"},
		{name: "IPv6 proxy and client",
			remote: "[2001:db8:ffff::1]:5555", xff: []string{"2001:db8:1::5"}, want: "[2001:db8:1::5]:0"},
		{name: "an IPv4 proxy seen as IPv4-in-IPv6 is still trusted",
			remote: "[::ffff:10.0.0.2]:5555", xff: []string{"::ffff:203.0.113.7"}, want: "203.0.113.7:0"},
		{name: "no proxies configured trusts nobody",
			noProxies: true, remote: "10.0.0.2:5555", xff: []string{"203.0.113.7"}, want: "10.0.0.2:5555"},
		{name: "no proxies configured trusts not even loopback",
			noProxies: true, remote: "127.0.0.1:5555", xff: []string{"203.0.113.7"}, want: "127.0.0.1:5555"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := &App{cfg: Config{TrustedProxies: trusted}}
			if tc.noProxies {
				app.cfg.TrustedProxies = nil
			}
			var seen string
			h := app.trustedProxiesMW(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { seen = r.RemoteAddr }))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = tc.remote
			for _, line := range tc.xff {
				req.Header.Add("X-Forwarded-For", line)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
			if seen != tc.want {
				t.Errorf("RemoteAddr = %q, want %q", seen, tc.want)
			}
			if req.RemoteAddr != tc.remote {
				t.Errorf("the caller's request was changed to %q", req.RemoteAddr)
			}
		})
	}
}

// Through the real handler: two shoppers behind one trusted storefront get an
// order-lookup budget each, and a client that is not a trusted proxy cannot
// buy a fresh budget by writing X-Forwarded-For.
func TestATrustedProxyCountsEachShopperAgainstTheirOwnLimit(t *testing.T) {
	app := newTestApp(t)
	app.cfg.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("10.0.0.2/32")}
	open := func(from, forwardedFor string) int {
		t.Helper()
		return doBody(t, app, http.MethodPost, "/api/orders/NO-SUCH-1/open", `{"contact":"shopper@example.com"}`,
			fromIP(from), header("X-Forwarded-For", forwardedFor)).Code
	}

	const storefront = "10.0.0.2"
	for i := range orderOpensPerWindow {
		if code := open(storefront, "203.0.113.1"); code != http.StatusNotFound {
			t.Fatalf("shopper 1 try %d = %d, want 404", i+1, code)
		}
	}
	if code := open(storefront, "203.0.113.1"); code != http.StatusTooManyRequests {
		t.Errorf("shopper 1 past the limit = %d, want 429", code)
	}
	if code := open(storefront, "203.0.113.2"); code != http.StatusNotFound {
		t.Errorf("shopper 2 behind the same storefront = %d, want 404 from a budget of their own", code)
	}

	const attacker = "198.51.100.66"
	for i := range orderOpensPerWindow {
		if code := open(attacker, fmt.Sprintf("203.0.113.%d", 100+i)); code != http.StatusNotFound {
			t.Fatalf("attacker try %d = %d, want 404", i+1, code)
		}
	}
	if code := open(attacker, "203.0.113.200"); code != http.StatusTooManyRequests {
		t.Errorf("attacker with a fresh X-Forwarded-For = %d, want 429: the header is not theirs to write", code)
	}
	if code := open(storefront, "203.0.113.100"); code != http.StatusNotFound {
		t.Errorf("a shopper the attacker named = %d, want 404: their budget was never spent", code)
	}
}
