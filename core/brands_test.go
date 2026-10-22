package gocommerce

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// One install, several brands (D111): each brand is a name and an admin
// domain, each store belongs to one brand and answers only under that brand's
// base domain, and a request belongs to the brand its host names.

func twoBrands() []Brand {
	return []Brand{
		{Name: "Alpha", PanelURL: "https://admin.alpha.test", BaseDomain: "alpha.test"},
		{Name: "Beta", PanelURL: "https://admin.beta.test", BaseDomain: "beta.test", LogoURL: "https://cdn.beta.test/logo.svg"},
	}
}

// apexBrand keeps its stores on its own apex, and still answers at the
// shops.<apex> it used before.
func apexBrand() Brand {
	return Brand{Name: "Gamma", PanelURL: "https://staff.gamma.test", BaseDomain: "gamma.test",
		AliasBaseDomains: []string{"shops.gamma.test"}, Reserved: []string{"updates"}}
}

func onHost(host string) func(*http.Request) {
	return func(r *http.Request) { r.Host = host }
}

// inBrand gives store id the brand whose panel is on host, as a store opened
// on that brand's admin domain has.
func inBrand(t *testing.T, app *App, id, host string) {
	t.Helper()
	if _, err := app.db.Exec(`UPDATE stores SET brand_host = $2 WHERE id = $1`, id, host); err != nil {
		t.Fatal(err)
	}
}

func sameBrandFields(a, b Brand) bool {
	return a.Name == b.Name && a.PanelURL == b.PanelURL && a.BaseDomain == b.BaseDomain &&
		a.LogoURL == b.LogoURL && slices.Equal(a.AliasBaseDomains, b.AliasBaseDomains) && slices.Equal(a.Reserved, b.Reserved)
}

func TestBrandsAreReadFromTheEnvironmentsJSON(t *testing.T) {
	got, err := ParseBrands(`[
		{"name":"Alpha","panel_url":"https://admin.alpha.test","base_domain":"alpha.test","logo_url":""},
		{"name":"Gamma","panel_url":"https://staff.gamma.test","base_domain":"gamma.test",
		 "alias_base_domains":["shops.gamma.test"],"reserved":["updates"],"logo_url":"https://cdn.gamma.test/logo.svg"}
	]`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "Alpha" || !sameBrandFields(got[1], Brand{Name: "Gamma",
		PanelURL: "https://staff.gamma.test", BaseDomain: "gamma.test", AliasBaseDomains: []string{"shops.gamma.test"},
		Reserved: []string{"updates"}, LogoURL: "https://cdn.gamma.test/logo.svg"}) {
		t.Fatalf("brands = %+v", got)
	}
	if got, err := ParseBrands("  "); err != nil || got != nil {
		t.Fatalf("unset = %+v, %v; want none and no error", got, err)
	}
	for _, bad := range []string{
		`{"name":"Alpha"}`, // an object, not a list
		`[{"name":"Alpha","panelurl":"https://admin.alpha.test"}]`, // a misspelt key is not silently dropped
		`[]`,
		`[{"name":"Alpha"}] trailing`,
	} {
		if _, err := ParseBrands(bad); err == nil || !strings.Contains(err.Error(), BrandsEnv) {
			t.Errorf("ParseBrands(%s) = %v, want an error naming %s", bad, err, BrandsEnv)
		}
	}
}

// A brand that cannot work stops the engine at start, naming the entry, rather
// than mailing a broken link months later.
func TestABadBrandStopsTheEngineFromStarting(t *testing.T) {
	cases := []struct {
		name   string
		brands []Brand
		edit   func(*Config)
		want   string
	}{
		{"no name", []Brand{{Name: " ", PanelURL: "https://admin.alpha.test"}}, nil, "brand 1 has no name"},
		{"no panel", []Brand{{Name: "Alpha"}}, nil, `brand 1 (Alpha)`},
		{"plain http", []Brand{{Name: "Alpha", PanelURL: "http://admin.alpha.test"}}, nil, "https"},
		{"a path", []Brand{{Name: "Alpha", PanelURL: "https://alpha.test/admin"}}, nil, "no path"},
		{"not a url", []Brand{{Name: "Alpha", PanelURL: "admin.alpha.test"}}, nil, `brand 1 (Alpha)`},
		{"a bad base domain", []Brand{{Name: "Alpha", PanelURL: "https://admin.alpha.test", BaseDomain: "https://alpha.test"}}, nil, "not a domain name"},
		{"a bad alias", []Brand{{Name: "Alpha", PanelURL: "https://admin.alpha.test", BaseDomain: "alpha.test", AliasBaseDomains: []string{"old/alpha"}}}, nil, "not a domain name"},
		{"aliases with no base", []Brand{{Name: "Alpha", PanelURL: "https://admin.alpha.test", AliasBaseDomains: []string{"old.alpha.test"}}}, nil, "need a base_domain"},
		{"a bad reserved name", []Brand{{Name: "Alpha", PanelURL: "https://admin.alpha.test", Reserved: []string{"no dots."}}}, nil, "reserved name"},
		{"a bad logo", []Brand{{Name: "Alpha", PanelURL: "https://admin.alpha.test", LogoURL: "logo.svg"}}, nil, "logo_url"},
		{"one panel twice", []Brand{
			{Name: "Alpha", PanelURL: "https://admin.alpha.test"},
			{Name: "Beta", PanelURL: "https://ADMIN.alpha.test:8443"},
		}, nil, `brand 2 (Beta)`},
		{"one base domain twice", []Brand{
			{Name: "Alpha", PanelURL: "https://admin.alpha.test", BaseDomain: "shops.test"},
			{Name: "Beta", PanelURL: "https://admin.beta.test", BaseDomain: "Shops.Test"},
		}, nil, `brand 2 (Beta)`},
		{"another brand's base as an alias", []Brand{
			{Name: "Alpha", PanelURL: "https://admin.alpha.test", BaseDomain: "alpha.test"},
			{Name: "Beta", PanelURL: "https://admin.beta.test", BaseDomain: "beta.test", AliasBaseDomains: []string{"alpha.test"}},
		}, nil, `brand 2 (Beta)`},
		{"an old panel address no brand has", twoBrands(), func(c *Config) { c.PanelURL = "https://admin.delta.test" }, "GOCOMMERCE_PANEL_URL"},
		{"an old base domain no brand has", twoBrands(), func(c *Config) { c.BaseDomain = "delta.test" }, "GOCOMMERCE_BASE_DOMAIN"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{DBURL: "postgres://unused", AdminTokens: []string{"t"}, Brands: tc.brands}
			if tc.edit != nil {
				tc.edit(&cfg)
			}
			err := cfg.applyDefaults()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("applyDefaults = %v, want an error mentioning %q", err, tc.want)
			}
		})
	}
}

func TestGoodBrandsAreTidiedAndLendTheFirstToTheOldSettings(t *testing.T) {
	cfg := Config{DBURL: "postgres://unused", AdminTokens: []string{"t"}, Brands: []Brand{
		{Name: " Alpha ", PanelURL: "https://admin.alpha.test/", BaseDomain: "Alpha.Test."},
		{Name: "Gamma", PanelURL: "https://staff.gamma.test", BaseDomain: "gamma.test",
			AliasBaseDomains: []string{"Shops.Gamma.Test"}, Reserved: []string{" Updates "}},
	}}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatal(err)
	}
	if b := cfg.Brands[0]; b.Name != "Alpha" || b.PanelURL != "https://admin.alpha.test" || b.BaseDomain != "alpha.test" {
		t.Fatalf("first brand = %+v, want it tidied", b)
	}
	if b := cfg.Brands[1]; b.AliasBaseDomains[0] != "shops.gamma.test" || b.Reserved[0] != "updates" {
		t.Fatalf("second brand = %+v, want its alias and reserved name tidied", b)
	}
	// A module reading Config.PanelURL (cart recovery's tracked links) still
	// finds the install's address.
	if cfg.PanelURL != "https://admin.alpha.test" || cfg.BaseDomain != "alpha.test" {
		t.Fatalf("PanelURL, BaseDomain = %q, %q; want the first brand's", cfg.PanelURL, cfg.BaseDomain)
	}

	// The old settings may stay, naming one of the brands — the base domain
	// may be a brand's alias, which is where its stores used to be.
	cfg = Config{DBURL: "postgres://unused", AdminTokens: []string{"t"}, Brands: append(twoBrands(), apexBrand()),
		PanelURL: "https://staff.gamma.test", BaseDomain: "shops.gamma.test"}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("old settings naming the third brand: %v", err)
	}

	// A developer's machine has no certificate.
	cfg = Config{DBURL: "postgres://unused", Dev: true, Brands: []Brand{
		{Name: "Alpha", PanelURL: "http://admin.alpha.localhost:8092", BaseDomain: "alpha.localhost"},
	}}
	if err := cfg.applyDefaults(); err != nil {
		t.Fatalf("http in Dev: %v", err)
	}
}

// An install that never set brands is one brand made of its old settings, so
// it behaves exactly as it did.
func TestWithNoBrandsTheInstallIsOneBrandOfItsOldSettings(t *testing.T) {
	app := newTestApp(t)
	if got := app.Brands(); len(got) != 1 || !sameBrandFields(got[0], Brand{Name: DefaultBrandName}) {
		t.Fatalf("brands = %+v, want one called %s with no addresses", got, DefaultBrandName)
	}
	app.cfg.PanelURL, app.cfg.BaseDomain = "https://shop.example", "Shops.Test"
	want := Brand{Name: DefaultBrandName, PanelURL: "https://shop.example", BaseDomain: "shops.test"}
	if got := app.Brands(); len(got) != 1 || !sameBrandFields(got[0], want) {
		t.Fatalf("brands = %+v, want %+v", got, want)
	}
	if got := app.Brand(context.Background()); !sameBrandFields(got, want) {
		t.Fatalf("brand outside a request = %+v, want %+v", got, want)
	}
	// Every store is in it, and answers where it did.
	st, err := app.Stores().Get(context.Background(), app.DefaultStore())
	if err != nil {
		t.Fatal(err)
	}
	if got, err := app.Stores().ByHost(context.Background(), st.Slug+".shops.test"); err != nil || got.ID != st.ID {
		t.Fatalf("ByHost = %v, %v; want the store at its old address", got, err)
	}
}

// The brand is the request's host matched against the brands: a panel's own
// host, or a host under a brand's base domain or alias. Anything else is the
// first brand — never a brand made up from the host.
func TestARequestIsTheBrandItsHostNames(t *testing.T) {
	app := newTestApp(t)
	app.cfg.Brands = append(twoBrands(), apexBrand())
	var seen Brand
	var had bool
	h := app.storeMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, had = BrandFrom(r.Context())
	}))
	for host, want := range map[string]string{
		"admin.beta.test":           "Beta",
		"ADMIN.BETA.TEST:8443":      "Beta",
		"admin.beta.test.":          "Beta",
		"admin.alpha.test":          "Alpha",
		"bright-shop.beta.test":     "Beta",
		"beta.test":                 "Beta",
		"staff.gamma.test":          "Gamma",
		"old-shop.shops.gamma.test": "Gamma",
		"evil.example":              "Alpha",
		"admin.beta.test.evil":      "Alpha",
		"":                          "Alpha",
	} {
		// The panel's files and a person's routes are no store's, and still
		// a brand's.
		for _, path := range []string{"/api/admin/auth-state", "/admin/auth/login", "/api/stores/my"} {
			seen, had = Brand{}, false
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Host = host
			h.ServeHTTP(httptest.NewRecorder(), req)
			if !had || seen.Name != want {
				t.Errorf("%s%s: brand = %q (%v), want %s", host, path, seen.Name, had, want)
			}
		}
	}
	if _, ok := BrandFrom(context.Background()); ok {
		t.Error("work no request started has a request's brand")
	}
}

// A store answers under its own brand's base domain and nobody else's.
func TestAStoreAnswersOnlyUnderItsBrandsBaseDomain(t *testing.T) {
	app := newStrictTestApp(t)
	app.cfg.Brands = twoBrands()
	bright := addTestStore(t, app, "bright-shop")
	inBrand(t, app, bright, "admin.alpha.test")
	dark := addTestStore(t, app, "dark-shop")
	inBrand(t, app, dark, "admin.beta.test")
	ctx := context.Background()
	for host, want := range map[string]string{
		"bright-shop.alpha.test":      bright,
		"Bright-Shop.Alpha.Test:443":  bright,
		"dark-shop.beta.test":         dark,
		"bright-shop.beta.test":       "",
		"dark-shop.alpha.test":        "",
		"bright-shop.gamma.test":      "",
		"bright-shop.admin.beta.test": "",
	} {
		st, err := app.Stores().ByHost(ctx, host)
		switch {
		case want == "" && !errors.Is(err, ErrNotFound):
			t.Errorf("ByHost(%s) = %v, %v; want no store", host, st, err)
		case want != "" && (err != nil || st.ID != want):
			t.Errorf("ByHost(%s) = %v, %v; want %s", host, st, err, want)
		}
	}
	// A store with no brand recorded is in the default one.
	plain := addTestStore(t, app, "plain-shop")
	if st, err := app.Stores().ByHost(ctx, "plain-shop.alpha.test"); err != nil || st.ID != plain {
		t.Errorf("a store with no brand: ByHost = %v, %v", st, err)
	}

	// Through a request, too: with several stores, the host alone names one,
	// and suspending that one shows it was the one named.
	if rec := do(t, app, http.MethodGet, "/api/products", onHost("dark-shop.beta.test")); rec.Code != http.StatusOK {
		t.Fatalf("dark-shop.beta.test: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodGet, "/api/products", onHost("dark-shop.alpha.test")); rec.Code != http.StatusBadRequest {
		t.Fatalf("another brand's address named a store: %d %s", rec.Code, rec.Body)
	}
	if _, err := app.db.Exec(`UPDATE stores SET status = 'suspended' WHERE id = $1`, dark); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodGet, "/api/products", onHost("dark-shop.beta.test")); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after suspending dark-shop: %d, want 503", rec.Code)
	}
}

// A brand on its apex keeps its old shops.<apex> as an alias: stores answer at
// both, and everything the engine hands out uses the apex.
func TestAStoreAnswersAtItsBrandsAliasAndIsGivenOnlyTheBaseDomain(t *testing.T) {
	app := newTestApp(t)
	app.cfg.Brands = append(twoBrands(), apexBrand())
	id := addTestStore(t, app, "old-shop")
	inBrand(t, app, id, "staff.gamma.test")
	for _, host := range []string{"old-shop.gamma.test", "old-shop.shops.gamma.test"} {
		if st, err := app.Stores().ByHost(context.Background(), host); err != nil || st.ID != id {
			t.Errorf("ByHost(%s) = %v, %v; want old-shop", host, st, err)
		}
	}
	tok := signInAs(t, app, "gamma@example.com", RoleOwner)
	joinTestStore(t, app, id, "gamma@example.com", RoleOwner)
	var mine []MyStore
	decodeData(t, do(t, app, http.MethodGet, "/api/stores/my", bearer(tok), onHost("staff.gamma.test")), &mine)
	if len(mine) != 1 || mine[0].Subdomain != "old-shop.gamma.test" || mine[0].Domain != "old-shop.gamma.test" {
		t.Fatalf("stores on the Gamma panel = %+v, want old-shop at old-shop.gamma.test", mine)
	}
}

// The names a platform keeps (D93), the first label of each brand's panel
// host and the names a brand lists are nobody's store: a new store cannot take
// one, and a store that had one before does not answer there.
func TestAReservedNameIsNobodysStoreUnderAnyBrand(t *testing.T) {
	app := newStrictTestApp(t)
	app.cfg.Brands = append(twoBrands(), apexBrand())
	for _, slug := range []string{"admin", "www", "staff", "updates", "shops"} {
		_, err := app.Stores().CreateFor(context.Background(), "Kept", slug, 0)
		if err == nil || !strings.Contains(err.Error(), "is kept for the platform's own use") {
			t.Errorf("opening a store at %q = %v, want it refused as kept", slug, err)
		}
	}
	// "staff" is somebody's address on an install without brands.
	plain := newTestApp(t)
	if _, err := plain.Stores().CreateFor(context.Background(), "Staff", "staff", 0); err != nil {
		t.Fatalf("without brands, staff is free: %v", err)
	}

	for slug, host := range map[string]string{"admin": "admin.alpha.test", "staff": "staff.gamma.test", "updates": "staff.gamma.test"} {
		inBrand(t, app, addTestStore(t, app, slug), host)
	}
	for _, host := range []string{"admin.alpha.test", "admin.beta.test", "staff.gamma.test", "updates.gamma.test", "updates.shops.gamma.test"} {
		if st, err := app.Stores().ByHost(context.Background(), host); !errors.Is(err, ErrNotFound) {
			t.Errorf("ByHost(%s) = %v, %v; want no store", host, st, err)
		}
	}
	// Such a store went quiet at its address, so the doctor names it.
	d := findCheck(t, app.Diagnose(context.Background()), "brands")
	for _, slug := range []string{"admin", "staff", "updates"} {
		if d.Status != StatusWarn || !strings.Contains(d.Detail, "the store "+slug+" ") {
			t.Errorf("stores at kept addresses: %+v, want %s named", d, slug)
		}
	}
}

// On a brand's admin domain a person sees that brand's stores, each at its
// own address; the install's token sees every store.
func TestTheStoreListShowsOnlyTheBrandsStores(t *testing.T) {
	app := newTestApp(t)
	app.cfg.Brands = twoBrands()
	inBrand(t, app, app.DefaultStore(), "admin.alpha.test")
	beta := addTestStore(t, app, "dark-shop")
	inBrand(t, app, beta, "admin.beta.test")
	tok := signInAs(t, app, "both@example.com", RoleOwner)
	joinTestStore(t, app, beta, "both@example.com", RoleOwner)

	list := func(opts ...func(*http.Request)) []MyStore {
		var mine []MyStore
		decodeData(t, do(t, app, http.MethodGet, "/api/stores/my", opts...), &mine)
		return mine
	}
	alpha := list(bearer(tok), onHost("admin.alpha.test"))
	if len(alpha) != 1 || alpha[0].ID != app.DefaultStore() || alpha[0].Subdomain != alpha[0].Slug+".alpha.test" {
		t.Fatalf("on Alpha: %+v, want the Alpha store alone", alpha)
	}
	betas := list(bearer(tok), onHost("admin.beta.test"))
	if len(betas) != 1 || betas[0].ID != beta || betas[0].Subdomain != "dark-shop.beta.test" {
		t.Fatalf("on Beta: %+v, want dark-shop alone", betas)
	}
	all := list(withAdmin, onHost("admin.alpha.test"))
	if len(all) != 2 || all[1].Subdomain != "dark-shop.beta.test" {
		t.Fatalf("the install's token: %+v, want both stores, each at its own brand's address", all)
	}
}

// A store of another brand is answered as one the person is not on, by name,
// by switching and by default; the install's token keeps its reach.
func TestAStoreOfAnotherBrandIsRefusedLikeOneThePersonIsNotOn(t *testing.T) {
	app := newStrictTestApp(t)
	app.cfg.Brands = twoBrands()
	alpha := firstStore(t, app)
	inBrand(t, app, alpha, "admin.alpha.test")
	beta := addTestStore(t, app, "dark-shop")
	inBrand(t, app, beta, "admin.beta.test")
	tok := sessionIn(t, app, beta, "beta@example.com", RoleOwner)

	if rec := do(t, app, http.MethodGet, "/api/admin/products", bearer(tok), header("X-Store", beta), onHost("admin.beta.test")); rec.Code != http.StatusOK {
		t.Fatalf("own brand: %d %s", rec.Code, rec.Body)
	}
	rec := do(t, app, http.MethodGet, "/api/admin/products", bearer(tok), header("X-Store", beta), onHost("admin.alpha.test"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), ErrNotAMember.Code) {
		t.Fatalf("another brand's panel: %d %s, want 403 %s", rec.Code, rec.Body, ErrNotAMember.Code)
	}
	rec = doBody(t, app, http.MethodPost, "/api/stores/switch", `{"store_id":"`+beta+`"}`, bearer(tok), onHost("admin.alpha.test"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("switching on another brand's panel: %d %s", rec.Code, rec.Body)
	}
	// Their only store is not theirs by default on a panel it is not on.
	if rec := do(t, app, http.MethodGet, "/api/admin/products", bearer(tok), onHost("admin.alpha.test")); rec.Code == http.StatusOK {
		t.Fatalf("their one store opened on another brand's panel: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/products", bearer(tok), onHost("admin.beta.test")); rec.Code != http.StatusOK {
		t.Fatalf("their one store on its own panel: %d %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/products", withAdmin, header("X-Store", beta), onHost("admin.alpha.test")); rec.Code != http.StatusOK {
		t.Fatalf("the install's token on another brand's panel: %d %s", rec.Code, rec.Body)
	}
}

func TestANewStoreBelongsToTheBrandItIsOpenedFrom(t *testing.T) {
	app := newTestApp(t)
	app.cfg.Brands = twoBrands()
	tok := signInAs(t, app, "founder@example.com", RoleStaff)
	rec := doBody(t, app, http.MethodPost, "/api/stores/create", `{"name":"Night","slug":"night-shop"}`,
		bearer(tok), onHost("admin.beta.test"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	st, err := app.Stores().BySlug(context.Background(), "night-shop")
	if err != nil {
		t.Fatal(err)
	}
	if b := app.StoreBrand(WithStore(context.Background(), st.ID)); b.Name != "Beta" || st.BrandHost != "admin.beta.test" {
		t.Fatalf("the new store is %q (%q), want Beta", b.Name, st.BrandHost)
	}
	if got, err := app.Stores().ByHost(context.Background(), "night-shop.beta.test"); err != nil || got.ID != st.ID {
		t.Fatalf("ByHost = %v, %v", got, err)
	}
}

// The install's owner moves a store; its address moves with it, and the
// store's own trail says so.
func TestMovingAStoreToAnotherBrandMovesItsAddress(t *testing.T) {
	app := newTestApp(t)
	app.cfg.Brands = twoBrands()
	ctx := context.Background()
	id := addTestStore(t, app, "moving-shop")
	inBrand(t, app, id, "admin.alpha.test")

	if _, err := app.Stores().SetBrand(ctx, id, "admin.delta.test"); err == nil {
		t.Fatal("a store was moved to a brand the install does not have")
	}
	to, err := app.Stores().SetBrand(WithActorLabel(ctx, "gocommerce store brand"), id, "ADMIN.BETA.TEST")
	if err != nil || to.Name != "Beta" {
		t.Fatalf("SetBrand = %+v, %v", to, err)
	}
	if st, err := app.Stores().ByHost(ctx, "moving-shop.beta.test"); err != nil || st.ID != id {
		t.Fatalf("at its new address: %v, %v", st, err)
	}
	if _, err := app.Stores().ByHost(ctx, "moving-shop.alpha.test"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("still at its old address: %v", err)
	}
	rows, _, err := app.Audit().List(WithStore(ctx, id), AuditQuery{Action: AuditStoreSetBrand})
	if err != nil || len(rows) != 1 || rows[0].Summary != "Moved from Alpha to Beta" || rows[0].ActorLabel != "gocommerce store brand" {
		t.Fatalf("audit = %+v, %v", rows, err)
	}
}

// At start, every store without a brand takes the one whose base domain or
// alias is the old GOCOMMERCE_BASE_DOMAIN, where those stores already were,
// and keeps it.
func TestStoresWithoutABrandTakeTheBrandOfTheirOldBaseDomain(t *testing.T) {
	before := newTestApp(t)
	st, err := before.Stores().Get(context.Background(), before.DefaultStore())
	if err != nil || st.BrandHost != "" {
		t.Fatalf("before brands, the store's brand is %q (%v); want none", st.BrandHost, err)
	}
	cfg := testConfig(before.cfg.DBURL)
	cfg.Brands = append(twoBrands(), apexBrand())
	cfg.BaseDomain = "shops.gamma.test"
	after := newApp(t, cfg)
	st, err = after.Stores().Get(context.Background(), after.DefaultStore())
	if err != nil || st.BrandHost != "staff.gamma.test" {
		t.Fatalf("after brands, the store's brand is %q (%v); want staff.gamma.test", st.BrandHost, err)
	}
	if got, err := after.Stores().ByHost(context.Background(), st.Slug+".shops.gamma.test"); err != nil || got.ID != st.ID {
		t.Fatalf("its old address: %v, %v", got, err)
	}

	// With no old base domain, the first brand.
	fresh := newTestApp(t)
	cfg = testConfig(fresh.cfg.DBURL)
	cfg.Brands = twoBrands()
	after = newApp(t, cfg)
	if st, _ := after.Stores().Get(context.Background(), after.DefaultStore()); st.BrandHost != "admin.alpha.test" {
		t.Fatalf("with no old base domain, the store's brand is %q, want the first", st.BrandHost)
	}
}

// A brand's panel host may also be a store's own domain: the panel there knows
// its brand, and the request is bound to that store exactly as before.
func TestAPanelHostThatIsAlsoAStoresDomainKeepsBoth(t *testing.T) {
	app := newStrictTestApp(t)
	app.cfg.Brands = append(twoBrands(), apexBrand())
	held := addTestStore(t, app, "held-shop")
	inBrand(t, app, held, "staff.gamma.test")
	addTestStore(t, app, "other-shop")
	if _, err := app.db.Exec(`INSERT INTO store_domains (host, store_id, verified_at) VALUES ('staff.gamma.test', $1, now())`, held); err != nil {
		t.Fatal(err)
	}

	var seen Brand
	probe := app.storeMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = BrandFrom(r.Context())
		if s, _ := StoreFrom(r.Context()); s != held {
			t.Errorf("bound to %q, want held-shop", s)
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/products", nil)
	req.Host = "staff.gamma.test"
	probe.ServeHTTP(httptest.NewRecorder(), req)
	if seen.Name != "Gamma" {
		t.Fatalf("brand = %q, want Gamma", seen.Name)
	}
	if _, err := app.db.Exec(`UPDATE stores SET status = 'suspended' WHERE id = $1`, held); err != nil {
		t.Fatal(err)
	}
	if rec := do(t, app, http.MethodGet, "/api/products", onHost("staff.gamma.test")); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("after suspending held-shop: %d, want 503", rec.Code)
	}
	d := findCheck(t, app.Diagnose(context.Background()), "brands")
	if d.Status != StatusOK || !strings.Contains(d.Detail, "staff.gamma.test is the Gamma panel and also the store held-shop's own domain") {
		t.Fatalf("doctor: %+v, want it noted and not warned", d)
	}
}

// Addresses under any brand's base domain or alias come with stores; a
// brand's panel host may be a store's own domain.
func TestNoAddressUnderABrandsDomainsCanBeAddedAsAStoresOwn(t *testing.T) {
	app := newTestApp(t)
	app.cfg.Brands = append(twoBrands(), apexBrand())
	for _, host := range []string{"mine.alpha.test", "mine.beta.test", "beta.test", "mine.gamma.test", "mine.shops.gamma.test"} {
		rec := doBody(t, app, http.MethodPost, "/api/admin/domains", `{"host":"`+host+`"}`, withAdmin)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("adding %s = %d %s, want 400", host, rec.Code, rec.Body)
		}
	}
	app.cfg.Brands = append(app.cfg.Brands, Brand{Name: "Delta", PanelURL: "https://panel.delta.example"})
	for _, host := range []string{"shop.example.org", "panel.delta.example"} {
		if rec := doBody(t, app, http.MethodPost, "/api/admin/domains", `{"host":"`+host+`"}`, withAdmin); rec.Code != http.StatusCreated {
			t.Fatalf("adding %s = %d %s", host, rec.Code, rec.Body)
		}
	}
}

// The store cookie is Secure when the brand the request came through is
// served on https.
func TestTheStoreCookieFollowsTheRequestsBrand(t *testing.T) {
	app := newTestApp(t)
	app.cfg.Brands = []Brand{
		{Name: "Alpha", PanelURL: "http://admin.alpha.localhost:8092"},
		{Name: "Beta", PanelURL: "https://admin.beta.test"},
	}
	tok := signInAs(t, app, "cookie@example.com", RoleOwner)
	secure := func(host string) bool {
		rec := doBody(t, app, http.MethodPost, "/api/stores/switch", `{"store_id":"`+app.DefaultStore()+`"}`,
			bearer(tok), onHost(host))
		for _, c := range rec.Result().Cookies() {
			if c.Name == storeCookie {
				return c.Secure
			}
		}
		t.Fatalf("no store cookie: %d %s", rec.Code, rec.Body)
		return false
	}
	if secure("admin.alpha.localhost:8092") {
		t.Fatal("a cookie set through a brand on plain http is Secure")
	}
	inBrand(t, app, app.DefaultStore(), "admin.beta.test")
	if !secure("admin.beta.test") {
		t.Fatal("a cookie set through a brand on https is not Secure")
	}
}

func TestTheDoctorListsTheBrands(t *testing.T) {
	app := newTestApp(t)
	d := findCheck(t, app.Diagnose(context.Background()), "brands")
	if d.Status != StatusOK || !strings.Contains(d.Detail, DefaultBrandName) {
		t.Fatalf("one implicit brand: %+v", d)
	}

	app.cfg.Brands = append(twoBrands(), apexBrand())
	inBrand(t, app, app.DefaultStore(), "admin.beta.test")
	d = findCheck(t, app.Diagnose(context.Background()), "brands")
	if d.Status != StatusOK {
		t.Fatalf("three good brands: %+v", d)
	}
	for _, want := range []string{
		"Alpha (panel at https://admin.alpha.test, stores at <slug>.alpha.test, 0 store(s))",
		"Beta (panel at https://admin.beta.test, stores at <slug>.beta.test, 1 store(s))",
		"Gamma (panel at https://staff.gamma.test, stores at <slug>.gamma.test, also <slug>.shops.gamma.test, 0 store(s), keeps updates)",
	} {
		if !strings.Contains(d.Detail, want) {
			t.Errorf("detail %q does not say %q", d.Detail, want)
		}
	}

	// A store whose brand the install no longer has.
	inBrand(t, app, app.DefaultStore(), "admin.delta.test")
	d = findCheck(t, app.Diagnose(context.Background()), "brands")
	if d.Status != StatusWarn || !strings.Contains(d.Detail, "(admin.delta.test)") || !strings.Contains(d.Hint, "gocommerce store brand") {
		t.Fatalf("a store of a brand that is gone: %+v", d)
	}

	// Two brands on one panel host get past nothing that starts the engine, but
	// an embedder can still hand them over after New: the doctor re-checks.
	app.cfg.Brands = []Brand{
		{Name: "Alpha", PanelURL: "https://admin.alpha.test"},
		{Name: "Beta", PanelURL: "https://admin.alpha.test"},
	}
	d = findCheck(t, app.Diagnose(context.Background()), "brands")
	if d.Status != StatusFail || !strings.Contains(d.Detail, "Beta") {
		t.Fatalf("two brands on one panel host: %+v", d)
	}
}

// The panel learns its brand from auth-state, which answers before anybody
// signs in: the sign-in screen wears the brand too.
func TestAuthStateSaysWhichBrandItIs(t *testing.T) {
	app := newTestApp(t)
	type brandState struct {
		Brand struct {
			Name    string `json:"name"`
			LogoURL string `json:"logo_url"`
		} `json:"brand"`
		ResetLink bool `json:"reset_link"`
	}
	var st brandState
	decodeData(t, do(t, app, http.MethodGet, "/api/admin/auth-state"), &st)
	if st.Brand.Name != DefaultBrandName || st.Brand.LogoURL != "" {
		t.Fatalf("without brands: %+v", st)
	}

	app.cfg.Brands = twoBrands()
	for host, want := range map[string][2]string{
		"admin.alpha.test": {"Alpha", ""},
		"admin.beta.test":  {"Beta", "https://cdn.beta.test/logo.svg"},
		"evil.example":     {"Alpha", ""},
	} {
		st = brandState{}
		decodeData(t, do(t, app, http.MethodGet, "/api/admin/auth-state", onHost(host)), &st)
		if st.Brand.Name != want[0] || st.Brand.LogoURL != want[1] || !st.ResetLink {
			t.Errorf("%s: %+v, want %v with a reset link", host, st, want)
		}
	}
}
