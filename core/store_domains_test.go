package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// A store's own domains: added, proven by a DNS TXT record the engine looks
// up itself, made primary, removed — and routed only once proven.

// fakeTXT answers TXT lookups from a map, so no test touches real DNS.
func fakeTXT(app *App, records map[string][]string) {
	app.lookupTXT = func(_ context.Context, name string) ([]string, error) {
		return records[name], nil
	}
}

func addDomain(t *testing.T, app *App, host string, opts ...func(*http.Request)) (int, Domain) {
	t.Helper()
	rec := doBody(t, app, http.MethodPost, "/api/admin/domains", `{"host":"`+host+`"}`, append([]func(*http.Request){withAdmin}, opts...)...)
	var d Domain
	if rec.Code == http.StatusCreated {
		decodeData(t, rec, &d)
	}
	return rec.Code, d
}

// An address under the base domain is refused with the store's own address in
// the answer, since that is what the owner was looking for.
func TestAnAddressUnderTheBaseDomainIsAnsweredWithTheStoresOwn(t *testing.T) {
	app := newTestApp(t)
	app.cfg.BaseDomain = "shops.test"
	st, err := app.Stores().Get(context.Background(), app.DefaultStore())
	if err != nil {
		t.Fatal(err)
	}
	own := st.Slug + ".shops.test"
	for host, want := range map[string]string{
		own:                own + " is already this store's address",
		"other.shops.test": "this store's is " + own,
		"shops.test":       "this store's is " + own,
	} {
		rec := doBody(t, app, http.MethodPost, "/api/admin/domains", `{"host":"`+host+`"}`, withAdmin)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("add %s = %d %s, want 400 saying %q", host, rec.Code, rec.Body, want)
		}
	}
}

func TestADomainRoutesOnlyOnceItsTXTRecordIsFound(t *testing.T) {
	app := newTestApp(t)
	records := map[string][]string{}
	fakeTXT(app, records)

	code, d := addDomain(t, app, "Shop.Example.COM")
	if code != http.StatusCreated {
		t.Fatalf("add = %d", code)
	}
	if d.Host != "shop.example.com" || d.Verified || d.TXTName != "_gocommerce.shop.example.com" || !strings.HasPrefix(d.TXTValue, "gocommerce-verify=") {
		t.Fatalf("domain = %+v", d)
	}
	if _, err := app.Stores().ByHost(context.Background(), "shop.example.com"); err == nil {
		t.Fatal("an unproven domain already routes to the store")
	}

	// The record is not there yet: the answer says so, and nothing changes.
	rec := doBody(t, app, http.MethodPost, "/api/admin/domains/shop.example.com/verify", ``, withAdmin)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "not_verified") {
		t.Fatalf("verify without the record = %d: %s", rec.Code, rec.Body)
	}

	records[d.TXTName] = []string{"something-else", d.TXTValue}
	rec = doBody(t, app, http.MethodPost, "/api/admin/domains/shop.example.com/verify", ``, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("verify = %d: %s", rec.Code, rec.Body)
	}
	var v Domain
	decodeData(t, rec, &v)
	if !v.Verified || v.VerifiedAt == nil {
		t.Fatalf("after verify: %+v", v)
	}
	st, err := app.Stores().ByHost(context.Background(), "shop.example.com")
	if err != nil || st.ID != app.DefaultStore() {
		t.Fatalf("a proven domain does not route: %v", err)
	}
}

func TestVerifiedIsTheEnginesWordAlone(t *testing.T) {
	app := newTestApp(t)
	fakeTXT(app, nil)
	// A body that tries to say so is refused outright: the engine reads
	// request bodies strictly, so a field it does not take is an error rather
	// than something quietly honoured or quietly dropped.
	rec := doBody(t, app, http.MethodPost, "/api/admin/domains", `{"host":"claimed.example.com","verified":true,"is_primary":true}`, withAdmin)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "verified") {
		t.Fatalf("add claiming verified = %d: %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/domains", withAdmin); strings.Contains(rec.Body.String(), "claimed.example.com") {
		t.Fatalf("a refused add was stored: %s", rec.Body)
	}
	// Added plainly, it starts unverified and not primary.
	rec = doBody(t, app, http.MethodPost, "/api/admin/domains", `{"host":"claimed.example.com"}`, withAdmin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add = %d: %s", rec.Code, rec.Body)
	}
	var d Domain
	decodeData(t, rec, &d)
	if d.Verified || d.Primary {
		t.Fatalf("a new domain = %+v, want unverified and not primary", d)
	}
}

func TestADomainIsOneStoresAndAWellFormedHost(t *testing.T) {
	app := newTestApp(t)
	fakeTXT(app, nil)
	other := addTestStore(t, app, "other")
	if code, _ := addDomain(t, app, "taken.example.com", header("x-litekart-store", other)); code != http.StatusCreated {
		t.Fatalf("first claim = %d", code)
	}
	if code, _ := addDomain(t, app, "taken.example.com"); code != http.StatusConflict {
		t.Fatalf("a domain already another store's = %d, want 409", code)
	}
	for _, bad := range []string{"https://x.example.com", "x.example.com/path", "localhost", "127.0.0.1", "no_underscores.example.com", "x.example.com:8080", ""} {
		if code, _ := addDomain(t, app, bad); code != http.StatusBadRequest {
			t.Fatalf("host %q = %d, want 400", bad, code)
		}
	}
	// Another store's domain is not this store's to see or remove.
	rec := do(t, app, http.MethodGet, "/api/admin/domains", withAdmin)
	if strings.Contains(rec.Body.String(), "taken.example.com") {
		t.Fatalf("one store lists another's domain: %s", rec.Body)
	}
	if rec := do(t, app, http.MethodDelete, "/api/admin/domains/taken.example.com", withAdmin); rec.Code != http.StatusNotFound {
		t.Fatalf("removing another store's domain = %d, want 404", rec.Code)
	}
}

func TestThePrimaryDomainIsWhereTheStoreLives(t *testing.T) {
	app := newTestApp(t)
	records := map[string][]string{}
	fakeTXT(app, records)
	verify := func(host string) {
		code, d := addDomain(t, app, host)
		if code != http.StatusCreated {
			t.Fatalf("add %s = %d", host, code)
		}
		records[d.TXTName] = []string{d.TXTValue}
		if rec := doBody(t, app, http.MethodPost, "/api/admin/domains/"+host+"/verify", ``, withAdmin); rec.Code != http.StatusOK {
			t.Fatalf("verify %s = %d", host, rec.Code)
		}
	}
	verify("first.example.com")
	verify("second.example.com")
	if code, _ := addDomain(t, app, "unproven.example.com"); code != http.StatusCreated {
		t.Fatal("add unproven")
	}
	if rec := doBody(t, app, http.MethodPost, "/api/admin/domains/unproven.example.com/primary", ``, withAdmin); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("an unproven domain made primary = %d, want 422", rec.Code)
	}
	if rec := doBody(t, app, http.MethodPost, "/api/admin/domains/second.example.com/primary", ``, withAdmin); rec.Code != http.StatusOK {
		t.Fatalf("primary = %d: %s", rec.Code, rec.Body)
	}
	mine := do(t, app, http.MethodGet, "/api/stores/my", withAdmin)
	var list []MyStore
	decodeData(t, mine, &list)
	if list[0].Domain != "second.example.com" {
		t.Fatalf("the store lives at %q, want its primary domain", list[0].Domain)
	}
	rec := do(t, app, http.MethodGet, "/api/admin/domains", withAdmin)
	var ds []Domain
	decodeData(t, rec, &ds)
	primaries := 0
	for _, d := range ds {
		if d.Primary {
			primaries++
		}
	}
	if primaries != 1 {
		t.Fatalf("%d primary domains, want one: %s", primaries, rec.Body)
	}
	if rec := do(t, app, http.MethodDelete, "/api/admin/domains/second.example.com", withAdmin); rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d", rec.Code)
	}
	if _, err := app.Stores().ByHost(context.Background(), "second.example.com"); err == nil {
		t.Fatal("a removed domain still routes")
	}
}

func TestADemoStoresDomainsAreFrozen(t *testing.T) {
	app := newTestApp(t)
	fakeTXT(app, nil)
	if err := app.Stores().SetDemo(context.Background(), app.DefaultStore(), true); err != nil {
		t.Fatal(err)
	}
	rec := doBody(t, app, http.MethodPost, "/api/admin/domains", `{"host":"hijack.example.com"}`, withAdmin)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("adding a domain to a demo store = %d, want 403", rec.Code)
	}
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	if env.Error.Code != "demo_frozen" {
		t.Fatalf("code = %q", env.Error.Code)
	}
}
