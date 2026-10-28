package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The proxy's routes for store domains (D113): one router per verified
// domain, served only to whoever holds the token.

const testTraefikToken = "proxy-token-for-tests"

func withProxyToken(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testTraefikToken) }

// verifyDomain adds host to the test store and proves it.
func verifyDomain(t *testing.T, app *App, records map[string][]string, host string) {
	t.Helper()
	code, d := addDomain(t, app, host)
	if code != http.StatusCreated {
		t.Fatalf("add %s = %d", host, code)
	}
	records[d.TXTName] = []string{d.TXTValue}
	if rec := doBody(t, app, http.MethodPost, "/api/admin/domains/"+host+"/verify", ``, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("verify %s = %d %s", host, rec.Code, rec.Body)
	}
}

// traefikRoutes fetches the configuration and decodes its http section.
func traefikRoutes(t *testing.T, app *App) (routers map[string]traefikRouter, services map[string]json.RawMessage) {
	t.Helper()
	rec := do(t, app, http.MethodGet, TraefikConfigPath, withProxyToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", TraefikConfigPath, rec.Code, rec.Body)
	}
	var body struct {
		HTTP struct {
			Routers  map[string]traefikRouter   `json:"routers"`
			Services map[string]json.RawMessage `json:"services"`
		} `json:"http"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body, err)
	}
	return body.HTTP.Routers, body.HTTP.Services
}

func TestTheProxyRouteIsAbsentUntilConfigured(t *testing.T) {
	app := newTestApp(t)
	if rec := do(t, app, http.MethodGet, TraefikConfigPath, withProxyToken); rec.Code != http.StatusNotFound {
		t.Fatalf("unconfigured = %d, want 404", rec.Code)
	}
}

// The proxy asks for every store's domains at once, from no store's host, so
// on an install with several stores the route must not wait to be told one.
func TestTheProxyRouteNamesNoStore(t *testing.T) {
	app := newTestApp(t)
	app.cfg.TraefikToken, app.cfg.TraefikUpstream = testTraefikToken, "http://gocommerce:8080"
	// As `gocommerce serve` runs: no store answers for a request that names none.
	app.defaultStore = ""
	if _, err := app.Stores().Create(context.Background(), "Second Shop", "second-shop"); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodGet, TraefikConfigPath, withProxyToken); rec.Code != http.StatusOK {
		t.Fatalf("with two stores = %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestTheProxyRouteWantsItsOwnToken(t *testing.T) {
	app := newTestApp(t)
	app.cfg.TraefikToken, app.cfg.TraefikUpstream = testTraefikToken, "http://gocommerce:8080"
	// The admin token is not this route's: a leaked proxy setting must not
	// open the panel, and an operator's token has no reason to read this.
	for name, opt := range map[string]func(*http.Request){
		"none":  func(*http.Request) {},
		"admin": withAdmin,
		"wrong": func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testTraefikToken+"x") },
	} {
		if rec := do(t, app, http.MethodGet, TraefikConfigPath, opt); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s token = %d, want 401", name, rec.Code)
		}
	}
}

func TestOnlyVerifiedDomainsAreRouted(t *testing.T) {
	app := newTestApp(t)
	app.cfg.TraefikToken, app.cfg.TraefikUpstream, app.cfg.TraefikCertResolver =
		testTraefikToken, "http://gocommerce:8080", "letsencrypt"
	records := map[string][]string{}
	fakeTXT(app, records)

	// Nothing verified yet: an empty configuration, which Traefik accepts.
	if rec := do(t, app, http.MethodGet, TraefikConfigPath, withProxyToken); strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("no domains = %s, want {}", rec.Body)
	}

	verifyDomain(t, app, records, "shop.example.com")
	if code, _ := addDomain(t, app, "unproven.example.com"); code != http.StatusCreated {
		t.Fatalf("add unproven = %d", code)
	}

	routers, services := traefikRoutes(t, app)
	if len(routers) != 2 {
		t.Fatalf("routers = %v, want the verified domain's two", routers)
	}
	secure := routers["gocommerce-shop_example_com"]
	if secure.Rule != "Host(`shop.example.com`)" || secure.TLS == nil || secure.TLS.CertResolver != "letsencrypt" ||
		len(secure.EntryPoints) != 1 || secure.EntryPoints[0] != "websecure" || secure.Service != traefikServiceName {
		t.Fatalf("https router = %+v", secure)
	}
	plain := routers["gocommerce-shop_example_com-http"]
	if plain.Rule != secure.Rule || plain.TLS != nil || len(plain.Middlewares) != 1 || plain.EntryPoints[0] != "web" {
		t.Fatalf("http router = %+v, want a redirect to https", plain)
	}
	if !strings.Contains(string(services[traefikServiceName]), "http://gocommerce:8080") {
		t.Fatalf("services = %v, want the upstream URL", services)
	}

	// Removing the domain takes its routes away on the next poll.
	if rec := do(t, app, http.MethodDelete, "/api/admin/domains/shop.example.com", withAdmin); rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d", rec.Code)
	}
	if rec := do(t, app, http.MethodGet, TraefikConfigPath, withProxyToken); strings.TrimSpace(rec.Body.String()) != "{}" {
		t.Fatalf("after removal = %s, want {}", rec.Body)
	}
}

// An upstream named as another provider's service is referred to, not
// redefined: a provider may not define a service under another's name.
func TestAnUpstreamServiceIsReferredTo(t *testing.T) {
	app := newTestApp(t)
	app.cfg.TraefikToken, app.cfg.TraefikUpstream = testTraefikToken, "storefront@docker"
	records := map[string][]string{}
	fakeTXT(app, records)
	verifyDomain(t, app, records, "shop.example.com")

	routers, services := traefikRoutes(t, app)
	if len(services) != 0 {
		t.Fatalf("services = %v, want none", services)
	}
	if r := routers["gocommerce-shop_example_com"]; r.Service != "storefront@docker" {
		t.Fatalf("router service = %q, want storefront@docker", r.Service)
	}
}

func TestADomainSaysWhereToPointIt(t *testing.T) {
	for target, want := range map[string]string{
		"203.0.113.10":       "A",
		"2001:db8::1":        "AAAA",
		"stores.example.com": "CNAME",
	} {
		app := newTestApp(t)
		app.cfg.DomainTarget = target
		code, d := addDomain(t, app, "shop.example.com")
		if code != http.StatusCreated || d.PointType != want || d.PointName != "shop.example.com" || d.PointValue != target {
			t.Fatalf("target %s: add = %d %+v, want a %s record to it", target, code, d, want)
		}
	}
	app := newTestApp(t)
	if _, d := addDomain(t, app, "shop.example.com"); d.PointType != "" {
		t.Fatalf("no target configured, but the domain says %s %s", d.PointType, d.PointValue)
	}
}

func TestDomainRoutingConfigIsChecked(t *testing.T) {
	for name, c := range map[string]Config{
		"token without upstream": {TraefikToken: "t"},
		"upstream not a URL":     {TraefikToken: "t", TraefikUpstream: "gocommerce:8080"},
		"upstream ftp":           {TraefikToken: "t", TraefikUpstream: "ftp://gocommerce"},
		"target with a scheme":   {DomainTarget: "https://stores.example.com"},
		"target without a dot":   {DomainTarget: "localhost"},
	} {
		if err := c.validateDomainRouting(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, c := range map[string]Config{
		"off":             {},
		"url upstream":    {TraefikToken: "t", TraefikUpstream: "http://gocommerce:8080"},
		"service":         {TraefikToken: "t", TraefikUpstream: "storefront@docker"},
		"ip target":       {DomainTarget: "203.0.113.10"},
		"hostname target": {DomainTarget: "Stores.Example.com."},
	} {
		if err := c.validateDomainRouting(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Caddy asks before getting a certificate for a name (D143): yes for a
// verified domain and a store's address under the base domain, no for an
// unproven domain, an unknown slug, a stranger's name or nothing at all.
func TestCaddyAsksOnlyForStoreDomains(t *testing.T) {
	app := newTestApp(t)
	app.cfg.BaseDomain = "shops.test"
	records := map[string][]string{}
	fakeTXT(app, records)
	verifyDomain(t, app, records, "shop.example.com")
	if code, _ := addDomain(t, app, "unproven.example.com"); code != http.StatusCreated {
		t.Fatalf("add unproven = %d", code)
	}
	st, err := app.Stores().Get(context.Background(), app.defaultStore)
	if err != nil {
		t.Fatal(err)
	}
	for domain, want := range map[string]int{
		"shop.example.com":        http.StatusOK,
		"SHOP.example.com.":       http.StatusOK,
		st.Slug + ".shops.test":   http.StatusOK,
		"unproven.example.com":    http.StatusNotFound,
		"no-such-shop.shops.test": http.StatusNotFound,
		"stranger.example.org":    http.StatusNotFound,
		"127.0.0.1":               http.StatusNotFound,
		"":                        http.StatusNotFound,
	} {
		if rec := do(t, app, http.MethodGet, CaddyAskPath+"?domain="+domain); rec.Code != want {
			t.Errorf("ask %q = %d, want %d", domain, rec.Code, want)
		}
	}
}
