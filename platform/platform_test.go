package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const platformToken = "platform-token-for-tests-only"

// newPlatform boots a platform in a namespace of its own on the test
// database, so it cannot meet another test's stores, and drops every schema
// it made when the test ends.
func newPlatform(t *testing.T, opts ...func(*Config)) *Platform {
	t.Helper()
	dsn := os.Getenv("GOCOMMERCE_TEST_DB")
	if dsn == "" {
		t.Skip("set GOCOMMERCE_TEST_DB to a PostgreSQL URL to run integration tests")
	}
	b := make([]byte, 5)
	rand.Read(b)
	ns := "pt" + hex.EncodeToString(b)
	quiet := slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
	cfg := Config{
		DBURL:      dsn,
		Namespace:  ns,
		BaseDomain: "shops.test",
		APIHosts:   []string{"api.shops.test"},
		Tokens:     []string{platformToken},
		Store:      gocommerce.Config{Logger: quiet},
		MediaRoot:  t.TempDir(),
		Logger:     quiet,
	}
	for _, o := range opts {
		o(&cfg)
	}
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("platform: %v", err)
	}
	t.Cleanup(func() {
		p.Close()
		admin, err := gocommerce.OpenDB(context.Background(), dsn)
		if err != nil {
			return
		}
		defer admin.Close()
		rows, err := admin.Query(`SELECT schema_name FROM information_schema.schemata WHERE schema_name LIKE $1`, ns+`\_%`)
		if err != nil {
			return
		}
		var schemas []string
		for rows.Next() {
			var s string
			rows.Scan(&s)
			schemas = append(schemas, s)
		}
		rows.Close()
		for _, s := range schemas {
			admin.Exec(`DROP SCHEMA IF EXISTS ` + s + ` CASCADE`)
		}
		// Per-store reports roles are cluster-global, so they outlive a dropped
		// schema; drop this namespace's own after its schemas (D79). The shared
		// base role is left alone — other tests use it.
		rrows, err := admin.Query(`SELECT rolname FROM pg_roles WHERE rolname LIKE $1`, ns+`\_reports\_%`)
		if err != nil {
			return
		}
		var roles []string
		for rrows.Next() {
			var r string
			rrows.Scan(&r)
			roles = append(roles, r)
		}
		rrows.Close()
		for _, r := range roles {
			admin.Exec(`DROP ROLE IF EXISTS ` + r)
		}
	})
	if err := p.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return p
}

// call sends a request to the platform as if to host.
func call(t *testing.T, p *Platform, host, method, path, token string, body any, header ...string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = strings.NewReader(string(b))
	}
	req := httptest.NewRequest(method, path, r)
	req.Host = host
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	rec := httptest.NewRecorder()
	p.Handler().ServeHTTP(rec, req)
	return rec
}

func provision(t *testing.T, p *Platform, slug string, domains ...string) *Provisioned {
	t.Helper()
	rec := call(t, p, "platform.shops.test", http.MethodPost, "/api/platform/tenants", platformToken, map[string]any{
		"slug": slug, "name": strings.ToUpper(slug), "owner_email": "owner@" + slug + ".test", "domains": domains,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("provision %s = %d: %s", slug, rec.Code, rec.Body)
	}
	var out struct {
		Data Provisioned `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Data.AdminToken == "" || out.Data.OwnerPassword == "" {
		t.Fatalf("provisioning returned no token or generated password: %s", rec.Body)
	}
	return &out.Data
}

func errCode(rec *httptest.ResponseRecorder) string {
	var out struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	return out.Error.Code
}

// A store answers at its own host and its custom domains, and nowhere else.
func TestAStoreAnswersAtItsHosts(t *testing.T) {
	p := newPlatform(t)
	provision(t, p, "acme", "shop.acme.example")

	for _, host := range []string{"acme.shops.test", "shop.acme.example", "SHOP.ACME.EXAMPLE:443"} {
		if rec := call(t, p, host, http.MethodGet, "/health", "", nil); rec.Code != http.StatusOK {
			t.Errorf("GET /health at %s = %d, want 200", host, rec.Code)
		}
	}
	if rec := call(t, p, "nobody.shops.test", http.MethodGet, "/health", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown host = %d, want 404", rec.Code)
	}
	// The platform API is not served on a store's host.
	if rec := call(t, p, "acme.shops.test", http.MethodGet, "/api/platform/tenants", platformToken, nil); rec.Code == http.StatusOK {
		t.Errorf("a store's host served the platform API")
	}
}

// Two stores share nothing: a product made in one is not in the other, and
// neither one's credentials open the other.
func TestStoresAreIsolated(t *testing.T) {
	p := newPlatform(t)
	a := provision(t, p, "alpha")
	b := provision(t, p, "beta")

	rec := call(t, p, "alpha.shops.test", http.MethodPost, "/api/admin/products", a.AdminToken, map[string]any{
		"title": "Alpha only", "status": "active", "sku": "ALPHA-1", "price_minor": 1000, "stock": 5,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create product in alpha = %d: %s", rec.Code, rec.Body)
	}
	if body := call(t, p, "beta.shops.test", http.MethodGet, "/api/products", "", nil).Body.String(); strings.Contains(body, "ALPHA-1") {
		t.Errorf("beta's catalogue shows alpha's product: %s", body)
	}
	if body := call(t, p, "alpha.shops.test", http.MethodGet, "/api/products", "", nil).Body.String(); !strings.Contains(body, "ALPHA-1") {
		t.Errorf("alpha's catalogue is missing its own product: %s", body)
	}

	if rec := call(t, p, "beta.shops.test", http.MethodGet, "/api/admin/orders", a.AdminToken, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("alpha's token on beta's admin API = %d, want 401", rec.Code)
	}
	if rec := call(t, p, "beta.shops.test", http.MethodGet, "/api/admin/orders", b.AdminToken, nil); rec.Code != http.StatusOK {
		t.Errorf("beta's own token = %d, want 200", rec.Code)
	}
	if rec := call(t, p, "beta.shops.test", http.MethodGet, "/api/admin/orders", platformToken, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("the platform token on a store's admin API = %d, want 401: it is not a store credential", rec.Code)
	}

	// Each store's owner signs in to their own store, and only theirs.
	login := func(host, email, password string) int {
		return call(t, p, host, http.MethodPost, "/api/admin/auth-with-password", "",
			map[string]string{"identity": email, "password": password}).Code
	}
	if code := login("alpha.shops.test", a.OwnerEmail, a.OwnerPassword); code != http.StatusOK {
		t.Errorf("alpha's owner signing in to alpha = %d, want 200", code)
	}
	if code := login("beta.shops.test", a.OwnerEmail, a.OwnerPassword); code == http.StatusOK {
		t.Errorf("alpha's owner signed in to beta")
	}
}

// The platform API needs a platform token; a store's token is not one.
func TestThePlatformAPINeedsAPlatformToken(t *testing.T) {
	p := newPlatform(t)
	a := provision(t, p, "acme")
	host := "platform.shops.test"

	if rec := call(t, p, host, http.MethodGet, "/api/platform/tenants", "", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("no token = %d, want 401", rec.Code)
	}
	if rec := call(t, p, host, http.MethodGet, "/api/platform/tenants", a.AdminToken, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("a store's token = %d, want 401", rec.Code)
	}
	rec := call(t, p, host, http.MethodGet, "/api/platform/tenants", platformToken, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"slug":"acme"`) {
		t.Errorf("platform token = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), a.AdminToken) {
		t.Error("the tenant list exposes a store's admin token")
	}

	// The TLS ask needs no token and answers only for served hosts.
	if rec := call(t, p, host, http.MethodGet, "/api/platform/tls/allowed?domain=acme.shops.test", "", nil); rec.Code != http.StatusOK {
		t.Errorf("TLS ask for a store host = %d, want 200", rec.Code)
	}
	if rec := call(t, p, host, http.MethodGet, "/api/platform/tls/allowed?domain=evil.example", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("TLS ask for a stranger's host = %d, want 404", rec.Code)
	}
}

// On a shared API host, X-Store names the store, by slug or by domain.
func TestASharedAPIHostRoutesByXStore(t *testing.T) {
	p := newPlatform(t)
	a := provision(t, p, "acme", "shop.acme.example")
	call(t, p, "acme.shops.test", http.MethodPost, "/api/admin/products", a.AdminToken, map[string]any{
		"title": "Widget", "status": "active", "sku": "W-1", "price_minor": 500, "stock": 1,
	})
	for _, name := range []string{"acme", "shop.acme.example"} {
		body := call(t, p, "api.shops.test", http.MethodGet, "/api/products", "", nil, "X-Store", name).Body.String()
		if !strings.Contains(body, "W-1") {
			t.Errorf("X-Store: %s did not reach acme: %s", name, body)
		}
	}
	if rec := call(t, p, "api.shops.test", http.MethodGet, "/api/products", "", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("no X-Store on the shared host = %d, want 400", rec.Code)
	}
}

// Suspending closes a store without losing it; deleting takes a suspended
// store and the slug repeated, and then the store and its schema are gone.
func TestSuspendResumeAndDelete(t *testing.T) {
	p := newPlatform(t)
	a := provision(t, p, "acme")
	ph := "platform.shops.test"

	rec := call(t, p, ph, http.MethodPatch, "/api/platform/tenants/acme", platformToken, map[string]string{"status": StatusSuspended})
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend = %d: %s", rec.Code, rec.Body)
	}
	if rec := call(t, p, "acme.shops.test", http.MethodGet, "/health", "", nil); rec.Code != http.StatusServiceUnavailable ||
		errCode(rec) != "store_unavailable" || !strings.Contains(rec.Body.String(), "not open") {
		t.Errorf("a suspended store = %d %s, want 503 store_unavailable saying so", rec.Code, rec.Body)
	}

	if rec := call(t, p, ph, http.MethodPatch, "/api/platform/tenants/acme", platformToken, map[string]string{"status": StatusActive}); rec.Code != http.StatusOK {
		t.Fatalf("resume = %d: %s", rec.Code, rec.Body)
	}
	if rec := call(t, p, "acme.shops.test", http.MethodGet, "/api/admin/orders", a.AdminToken, nil); rec.Code != http.StatusOK {
		t.Errorf("a resumed store's admin API = %d, want 200", rec.Code)
	}

	if rec := call(t, p, ph, http.MethodDelete, "/api/platform/tenants/acme?confirm=acme", platformToken, nil); rec.Code != http.StatusConflict {
		t.Errorf("deleting an active store = %d, want 409", rec.Code)
	}
	call(t, p, ph, http.MethodPatch, "/api/platform/tenants/acme", platformToken, map[string]string{"status": StatusSuspended})
	if rec := call(t, p, ph, http.MethodDelete, "/api/platform/tenants/acme", platformToken, nil); rec.Code != http.StatusBadRequest {
		t.Errorf("deleting without ?confirm= = %d, want 400", rec.Code)
	}
	if rec := call(t, p, ph, http.MethodDelete, "/api/platform/tenants/acme?confirm=acme", platformToken, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body)
	}
	if rec := call(t, p, "acme.shops.test", http.MethodGet, "/health", "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("a deleted store's host = %d, want 404", rec.Code)
	}
	var exists bool
	p.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)`,
		p.schemaFor("acme")).Scan(&exists)
	if exists {
		t.Error("the deleted store's schema is still there")
	}
}

// A slug or a domain that is taken refuses the whole store, and leaves no
// schema behind.
func TestProvisioningIsAllOrNothing(t *testing.T) {
	p := newPlatform(t)
	provision(t, p, "acme", "shop.acme.example")
	ph := "platform.shops.test"

	if rec := call(t, p, ph, http.MethodPost, "/api/platform/tenants", platformToken, map[string]any{
		"slug": "acme", "name": "Again", "owner_email": "x@y.test",
	}); rec.Code != http.StatusConflict {
		t.Errorf("a taken slug = %d, want 409", rec.Code)
	}
	if rec := call(t, p, ph, http.MethodPost, "/api/platform/tenants", platformToken, map[string]any{
		"slug": "copycat", "name": "Copycat", "owner_email": "x@y.test", "domains": []string{"shop.acme.example"},
	}); rec.Code != http.StatusConflict {
		t.Errorf("a taken domain = %d, want 409", rec.Code)
	}
	var exists bool
	p.db.QueryRow(`SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = $1)`,
		p.schemaFor("copycat")).Scan(&exists)
	if exists {
		t.Error("a refused store left its schema behind")
	}
	for _, bad := range []map[string]any{
		{"slug": "Bad Slug", "name": "x", "owner_email": "x@y.test"},
		{"slug": "platform", "name": "x", "owner_email": "x@y.test"},
		{"slug": "ok", "name": "x", "owner_email": "x@y.test", "domains": []string{"sub.shops.test"}},
	} {
		if rec := call(t, p, ph, http.MethodPost, "/api/platform/tenants", platformToken, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%v = %d, want 400", bad, rec.Code)
		}
	}
}

// Rotating a store's token stops the old one at once; a support owner can
// sign in to the store.
func TestRotateTheTokenAndAddAnOwner(t *testing.T) {
	p := newPlatform(t)
	a := provision(t, p, "acme")
	ph := "platform.shops.test"

	rec := call(t, p, ph, http.MethodPost, "/api/platform/tenants/acme/token", platformToken, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate = %d: %s", rec.Code, rec.Body)
	}
	var rotated struct {
		Data struct {
			AdminToken string `json:"admin_token"`
		} `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &rotated)
	if c := call(t, p, "acme.shops.test", http.MethodGet, "/api/admin/orders", a.AdminToken, nil).Code; c != http.StatusUnauthorized {
		t.Errorf("the old token after rotation = %d, want 401", c)
	}
	if c := call(t, p, "acme.shops.test", http.MethodGet, "/api/admin/orders", rotated.Data.AdminToken, nil).Code; c != http.StatusOK {
		t.Errorf("the new token = %d, want 200", c)
	}

	rec = call(t, p, ph, http.MethodPost, "/api/platform/tenants/acme/owners", platformToken, map[string]string{"email": "support@platform.test"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add owner = %d: %s", rec.Code, rec.Body)
	}
	var owner struct {
		Data struct {
			Password string `json:"password"`
		} `json:"data"`
	}
	json.Unmarshal(rec.Body.Bytes(), &owner)
	if c := call(t, p, "acme.shops.test", http.MethodPost, "/api/admin/auth-with-password", "",
		map[string]string{"identity": "support@platform.test", "password": owner.Data.Password}).Code; c != http.StatusOK {
		t.Errorf("the support owner signing in = %d, want 200", c)
	}
}

// A platform restarted over the same database brings its stores back.
func TestStoresComeBackAfterARestart(t *testing.T) {
	p := newPlatform(t)
	a := provision(t, p, "acme")
	cfg := p.cfg
	p.Close()

	again, err := New(cfg)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	defer again.Close()
	if err := again.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if c := call(t, again, "acme.shops.test", http.MethodGet, "/api/admin/orders", a.AdminToken, nil).Code; c != http.StatusOK {
		t.Errorf("the store after a restart = %d, want 200", c)
	}
}

// Every route the platform serves is in its contract, and every path in the
// contract is served.
func TestTheContractCoversThePlatformAPI(t *testing.T) {
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(openapiDoc, &doc); err != nil {
		t.Fatalf("openapi.json: %v", err)
	}
	documented := map[string]bool{}
	for path, ops := range doc.Paths {
		for method := range ops {
			if method == "parameters" {
				continue
			}
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}
	served := map[string]bool{}
	for _, rt := range (&Platform{}).routeTable() {
		r := rt.pattern
		served[r] = true
		if !documented[r] {
			t.Errorf("served but not documented: %s", r)
		}
	}
	for r := range documented {
		if !served[r] {
			t.Errorf("documented but not served: %s", r)
		}
	}
}

// The platform's hosts serve the console — the panel's /platform screens and
// the files they load — and nothing else of the panel: every other path is
// still a JSON 404, and a store's own host is untouched (D73).
func TestThePlatformHostServesTheConsole(t *testing.T) {
	p := newPlatform(t)
	provision(t, p, "acme")
	host := "platform.shops.test"

	jsonNotFound := func(method, path string) {
		t.Helper()
		rec := call(t, p, host, method, path, "", nil)
		if rec.Code != http.StatusNotFound || errCode(rec) == "" {
			t.Errorf("%s %s on the platform host = %d %.120s, want a JSON 404", method, path, rec.Code, rec.Body)
		}
	}

	if !gocommerce.HasAdminPanel() {
		jsonNotFound(http.MethodGet, "/")
		jsonNotFound(http.MethodGet, "/platform")
		return
	}

	rec := call(t, p, host, http.MethodGet, "/", "", nil)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/platform" {
		t.Errorf("GET / = %d to %q, want 302 to /platform", rec.Code, rec.Header().Get("Location"))
	}
	for _, path := range []string{"/platform", "/platform/acme", "/_app/version.json"} {
		rec := call(t, p, host, http.MethodGet, path, "", nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want the panel's file", path, rec.Code)
		}
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'none'") {
			t.Errorf("GET %s carries no panel CSP: the console must be served the way a store's panel is", path)
		}
	}
	if ct := call(t, p, host, http.MethodGet, "/platform", "", nil).Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET /platform Content-Type = %q, want the panel's page", ct)
	}

	// No store screen, no store API, no write: none of it is a store.
	for _, path := range []string{"/orders", "/platformx", "/health", "/api/store", "/api/platform/nope", "/_app/missing.js"} {
		jsonNotFound(http.MethodGet, path)
	}
	jsonNotFound(http.MethodPost, "/platform")

	// The console reads the base domain from the health report.
	rec = call(t, p, host, http.MethodGet, "/api/platform/health", platformToken, nil)
	if !strings.Contains(rec.Body.String(), `"base_domain":"shops.test"`) {
		t.Errorf("health = %s, want base_domain", rec.Body)
	}

	// A store's host still serves its own panel at its root.
	if rec := call(t, p, "acme.shops.test", http.MethodGet, "/", "", nil); rec.Code != http.StatusOK {
		t.Errorf("a store's root = %d, want its panel", rec.Code)
	}
}

// withTestReports configures a platform with a least-privileged reports login.
// It is a template: the platform clones a role per store from it (D79). The
// base role is the DSN's user — cluster-global and shared with the core tests,
// so created idempotently — and the platform never grants it a schema.
func withTestReports(t *testing.T, dsn string) func(*Config) {
	t.Helper()
	admin, err := gocommerce.OpenDB(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open admin db: %v", err)
	}
	defer admin.Close()
	if _, err := admin.ExecContext(context.Background(), `
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'gctest_reports') THEN
				CREATE ROLE gctest_reports LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
			END IF;
		EXCEPTION WHEN duplicate_object THEN
			NULL;
		END $$;`); err != nil {
		t.Fatalf("create reports role: %v", err)
	}
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	u.User = url.User("gctest_reports")
	reportsURL := u.String()
	return func(c *Config) {
		c.ReportsDBURL = reportsURL
		c.ReportsRole = "gctest_reports"
	}
}

// A store provisioned on a platform with reports configured runs custom reports
// as its own least-privileged role, against its own schema. The platform
// creates the role and grants it SELECT on the schema when it boots the store,
// so a report reads the store's tables and nothing it was not granted (D78).
func TestAStoreRunsReportsAsAReadOnlyRole(t *testing.T) {
	dsn := os.Getenv("GOCOMMERCE_TEST_DB")
	if dsn == "" {
		t.Skip("set GOCOMMERCE_TEST_DB to a PostgreSQL URL to run integration tests")
	}
	p := newPlatform(t, withTestReports(t, dsn))
	provision(t, p, "acme")

	app := p.App("acme")
	if app == nil {
		t.Fatal("no app for the provisioned store")
	}
	ctx := context.Background()

	// The role is not a superuser, and it can read the store's own tables.
	out, err := app.CustomReports().Run(ctx, "SELECT current_setting('is_superuser') AS v")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out.Rows) != 1 || out.Rows[0][0] == nil || *out.Rows[0][0] != "off" {
		t.Fatal("a store's reports role is a superuser; the barrier is gone")
	}
	if _, err := app.CustomReports().Run(ctx, "SELECT count(*) FROM products"); err != nil {
		t.Fatalf("reading the store's own table: %v", err)
	}

	// And it still cannot reach past reading.
	if _, err := app.CustomReports().Run(ctx, "SELECT pg_read_file('/etc/passwd')"); err == nil {
		t.Error("a store's report read a file off the server")
	}
}

// One store's custom report cannot read another store's data. Each store runs
// its reports as its own role, a member of no other, so naming another store's
// schema outright — or trying to switch into its role mid-query — is refused by
// PostgreSQL, not by the parser (D79). search_path keeps unqualified names in
// the store's own schema but is not a privilege boundary.
func TestOneStoresReportCannotReadAnother(t *testing.T) {
	dsn := os.Getenv("GOCOMMERCE_TEST_DB")
	if dsn == "" {
		t.Skip("set GOCOMMERCE_TEST_DB to a PostgreSQL URL to run integration tests")
	}
	p := newPlatform(t, withTestReports(t, dsn))
	provision(t, p, "alpha")
	b := provision(t, p, "beta")
	ctx := context.Background()

	// Give beta a product worth reading, and confirm beta reads it itself.
	if rec := call(t, p, "beta.shops.test", http.MethodPost, "/api/admin/products", b.AdminToken, map[string]any{
		"title": "Beta secret", "status": "active", "sku": "BETA-SECRET", "price_minor": 1000, "stock": 1,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create product in beta = %d: %s", rec.Code, rec.Body)
	}
	out, err := p.App("beta").CustomReports().Run(ctx, "SELECT title FROM products")
	if err != nil {
		t.Fatalf("beta reading its own product: %v", err)
	}
	if len(out.Rows) != 1 || out.Rows[0][0] == nil || *out.Rows[0][0] != "Beta secret" {
		t.Fatalf("beta read %d rows of its own product, want its one title", len(out.Rows))
	}

	alpha := p.App("alpha")
	betaSchema := p.schemaFor("beta")

	// (1) Alpha names beta's schema outright. Its role has no USAGE there.
	if _, err := alpha.CustomReports().Run(ctx, fmt.Sprintf("SELECT title FROM %s.products", betaSchema)); err == nil {
		t.Error("alpha's report read beta's products by qualified name")
	}

	// (2) The escape that slips past the parser: a plain SELECT that switches
	// role. Alpha's session role is its own, a member of no other, so the
	// switch is refused — the role is the barrier, not the parse.
	bt, err := p.Tenant(ctx, "beta")
	if err != nil {
		t.Fatal(err)
	}
	betaRole := p.reportsRoleFor(bt.ID)
	if _, err := alpha.CustomReports().Run(ctx, fmt.Sprintf("SELECT set_config('role', '%s', false)", betaRole)); err == nil {
		t.Errorf("alpha's report switched into beta's role %q", betaRole)
	}
}
