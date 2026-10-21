package gocommerce

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The address check is the whole of the guard, so it is tested as a table: a
// range missing from it is an address a store can reach that it should not.
func TestOnlyPublicAddressesArePublic(t *testing.T) {
	cases := []struct {
		addr   string
		public bool
	}{
		// IPv4: the ranges netip names itself.
		{"0.0.0.0", false},
		{"127.0.0.1", false},
		{"127.255.255.254", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"172.31.255.255", false},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"224.0.0.1", false},
		{"239.255.255.250", false},
		// IPv4: the ranges it does not.
		{"0.1.2.3", false},
		{"100.64.0.1", false},
		{"100.127.255.255", false},
		{"192.0.0.8", false},
		{"192.0.2.1", false},
		{"198.18.0.1", false},
		{"198.19.255.255", false},
		{"198.51.100.7", false},
		{"203.0.113.9", false},
		{"240.0.0.1", false},
		{"255.255.255.255", false},
		// IPv4 that is public, including just past the edges above.
		{"1.1.1.1", true},
		{"8.8.8.8", true},
		{"172.32.0.1", true},
		{"100.128.0.1", true},
		{"198.20.0.1", true},

		// IPv6: the ranges netip names itself.
		{"::", false},
		{"::1", false},
		{"fe80::1", false},
		{"fe80::1%eth0", false},
		{"fc00::1", false},
		{"fd12:3456::1", false},
		{"ff01::1", false},
		{"ff02::1", false},
		{"ff0e::1", false},
		// IPv4 inside IPv6, which must be judged as the IPv4 address it is.
		{"::ffff:127.0.0.1", false},
		{"::ffff:10.0.0.1", false},
		{"::ffff:169.254.169.254", false},
		{"::ffff:1.1.1.1", true},
		// IPv6: the ranges it does not.
		{"::127.0.0.1", false},
		{"::a00:1", false},
		{"64:ff9b::a00:1", false},
		{"64:ff9b:1::1", false},
		{"100::1", false},
		{"2001::1", false},
		{"2001:db8::1", false},
		{"2002:a00:1::", false},
		{"fec0::1", false},
		{"fec0::1%eth0", false},
		// IPv6 that is public, including a neighbour of the Teredo prefix.
		{"2606:4700:4700::1111", true},
		{"2001:4860:4860::8888", true},
	}
	for _, c := range cases {
		ip, err := netip.ParseAddr(c.addr)
		if err != nil {
			t.Fatalf("parse %s: %v", c.addr, err)
		}
		if got := publicAddr(ip); got != c.public {
			t.Errorf("publicAddr(%s) = %v, want %v", c.addr, got, c.public)
		}
	}
	if publicAddr(netip.Addr{}) {
		t.Error("the zero address counts as public")
	}
}

// localServer is a stand-in for something on the server's own network: an
// httptest server listens on loopback, and it counts whether anything reached
// it, because a guard that refuses after connecting has already let a request
// through.
func localServer(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func TestTheOutboundClientRefusesAPrivateAddress(t *testing.T) {
	srv, hits := localServer(t)
	app := &App{cfg: Config{}}

	resp, err := app.OutboundClient(5 * time.Second).Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("the guarded client reached a loopback server")
	}
	if !errors.Is(err, ErrNotPublic) {
		t.Errorf("err = %v, want it to match ErrNotPublic", err)
	}
	if !strings.Contains(err.Error(), "127.0.0.1 is not a public address") {
		t.Errorf("err = %q, want it to name the host and say it is not public", err)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the server was reached %d times", n)
	}
}

// The error names what the operator typed and nothing the lookup found: a name
// that resolves inside the network must not come back with its address.
func TestARefusalNamesTheHostAndNotWhatItResolvedTo(t *testing.T) {
	srv, hits := localServer(t)
	app := &App{cfg: Config{}}
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]

	resp, err := app.OutboundClient(5 * time.Second).Get("http://localhost:" + port + "/")
	if err == nil {
		resp.Body.Close()
		t.Fatal("the guarded client reached localhost")
	}
	if !errors.Is(err, ErrNotPublic) {
		t.Errorf("err = %v, want it to match ErrNotPublic", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "localhost does not resolve to a public address") {
		t.Errorf("err = %q, want it to name localhost", msg)
	}
	for _, leaked := range []string{"127.0.0.1", "::1", "[::1]"} {
		if strings.Contains(msg, leaked) {
			t.Errorf("err = %q, which carries the resolved address %s", msg, leaked)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("the server was reached %d times", n)
	}
}

// An install whose operators run the server can let them reach it.
func TestAllowingPrivateURLsGivesAnOrdinaryClient(t *testing.T) {
	srv, hits := localServer(t)
	app := &App{cfg: Config{AllowPrivateURLs: true}}

	client := app.OutboundClient(5 * time.Second)
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("get with private addresses allowed: %v", err)
	}
	resp.Body.Close()
	if hits.Load() != 1 {
		t.Errorf("the server was reached %d times, want 1", hits.Load())
	}
	if client.Timeout != 5*time.Second {
		t.Errorf("timeout = %s, want the one asked for", client.Timeout)
	}
}

func TestTheGuardedClientKeepsItsTimeoutAndUsesNoProxy(t *testing.T) {
	client := (&App{cfg: Config{}}).OutboundClient(7 * time.Second)
	if client.Timeout != 7*time.Second {
		t.Errorf("timeout = %s, want 7s", client.Timeout)
	}
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport is %T, want *http.Transport", client.Transport)
	}
	// Through a proxy the dialer would only ever see the proxy's address.
	if tr.Proxy != nil {
		t.Error("the guarded transport honours a proxy, which would hide where requests go")
	}
}

func TestValidateOutboundURL(t *testing.T) {
	guarded := &App{cfg: Config{}}
	open := &App{cfg: Config{AllowPrivateURLs: true}}

	cases := []struct {
		raw         string
		guardedOK   bool
		openOK      bool
		mustMention string
	}{
		{"https://hooks.example.com/in", true, true, ""},
		{"http://hooks.example.com/in", true, true, ""},
		{"https://1.1.1.1/in", true, true, ""},
		{"https://[2606:4700:4700::1111]/in", true, true, ""},
		{"https://127.0.0.1/in", false, true, "127.0.0.1"},
		{"http://10.0.0.5:8080/in", false, true, "10.0.0.5"},
		{"http://169.254.169.254/", false, true, "169.254.169.254"},
		{"https://[::1]/in", false, true, "::1"},
		{"https://[::ffff:127.0.0.1]/in", false, true, "::ffff:127.0.0.1"},
		{"http://localhost:9000/in", false, true, "localhost"},
		{"http://LOCALHOST./in", false, true, "LOCALHOST"},
		{"http://shop.localhost/in", false, true, "shop.localhost"},
		{"http://printer.local/in", false, true, "printer.local"},
		{"http://metadata.google.internal/", false, true, "metadata.google.internal"},
		{"ftp://hooks.example.com/in", false, false, ""},
		{"hooks.example.com/in", false, false, ""},
		{"https:///in", false, false, ""},
		{"", false, false, ""},
	}
	for _, c := range cases {
		err := guarded.ValidateOutboundURL(c.raw)
		if (err == nil) != c.guardedOK {
			t.Errorf("guarded: ValidateOutboundURL(%q) = %v, want ok=%v", c.raw, err, c.guardedOK)
		}
		if err != nil {
			if !errors.Is(err, ErrValidation) {
				t.Errorf("guarded: ValidateOutboundURL(%q) = %v, want a validation error", c.raw, err)
			}
			if c.mustMention != "" && !strings.Contains(err.Error(), c.mustMention) {
				t.Errorf("guarded: ValidateOutboundURL(%q) = %q, want it to name %s", c.raw, err, c.mustMention)
			}
		}
		if err := open.ValidateOutboundURL(c.raw); (err == nil) != c.openOK {
			t.Errorf("open: ValidateOutboundURL(%q) = %v, want ok=%v", c.raw, err, c.openOK)
		}
	}
}

// The switch is right on one kind of install and wrong on another, and the
// doctor is where an operator finds out which kind theirs has been set up as.
func TestDoctorWarnsWhenPrivateURLsAreAllowed(t *testing.T) {
	off := (&App{cfg: Config{}}).checkOutbound()
	if off.Status != StatusOK || off.Detail == "" {
		t.Errorf("switch off: %+v, want ok with a detail", off)
	}

	on := (&App{cfg: Config{AllowPrivateURLs: true}}).checkOutbound()
	if on.Status != StatusWarn {
		t.Errorf("switch on: status %q, want %q", on.Status, StatusWarn)
	}
	if !strings.Contains(on.Detail, "private") || on.Hint == "" {
		t.Errorf("switch on: %+v, want a detail about private addresses and a hint", on)
	}

	both := (&App{cfg: Config{AllowPrivateURLs: true, Signup: true}}).checkOutbound()
	if both.Status != StatusWarn || !strings.Contains(both.Detail, "sign-up") {
		t.Errorf("switch on with sign-up: %+v, want the warning to say sign-up is on too", both)
	}
}

// A name that resolves inside the network and a name that does not resolve at
// all fail the same way, so an operator cannot tell the two apart through a
// delivery log or a provider's error.
func TestAPrivateNameAndAMissingOneFailAlike(t *testing.T) {
	srv, hits := localServer(t)
	port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
	client := (&App{cfg: Config{}}).OutboundClient(10 * time.Second)

	reason := func(host string) string {
		t.Helper()
		resp, err := client.Get("http://" + host + ":" + port + "/")
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s was reached", host)
		}
		if !errors.Is(err, ErrNotPublic) {
			t.Errorf("%s: err = %v, want it to match ErrNotPublic", host, err)
		}
		var ue *url.Error
		if !errors.As(err, &ue) {
			t.Fatalf("%s: err = %T, want a *url.Error", host, err)
		}
		return strings.ReplaceAll(ue.Err.Error(), host, "HOST")
	}
	private := reason("localhost")
	missing := reason("gocommerce-outbound-test.invalid")
	if private != missing {
		t.Errorf("a private name fails with %q and a missing one with %q; they must read the same", private, missing)
	}
	if !strings.Contains(private, "does not resolve to a public address") {
		t.Errorf("reason = %q, want it to say the name does not resolve to a public address", private)
	}
	if hits.Load() != 0 {
		t.Errorf("the server was reached %d times", hits.Load())
	}
}
