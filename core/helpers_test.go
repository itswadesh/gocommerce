package gocommerce

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

// testDSNEnv names the environment variable holding the PostgreSQL URL used
// by integration tests. Tests that need a database skip without it, so
// `go test ./...` still runs the pure-logic suite on a machine with no
// PostgreSQL — but CI always sets it, so the database path is never untested
// where it counts.
const testDSNEnv = "GOCOMMERCE_TEST_DB"

const testAdminToken = "test-admin-token"

// testReportsRole is the least-privileged login custom reports run as in tests,
// mirroring the production role in scripts/reports-role.sql: it can connect and
// SELECT and nothing else. It is a cluster-global object shared by every
// concurrently-running test schema, created once and granted per schema; a
// schema's grants vanish with it when the schema is dropped. Trust auth on the
// test container lets it log in without a password (see pg_hba).
const testReportsRole = "gctest_reports"

// requireDB returns a connection string pointing at a PostgreSQL schema
// created for this test alone, dropped when the test finishes.
//
// Isolation is per test rather than "empty the database", because `go test
// ./...` runs each package as its own binary, concurrently: a shared database
// that every test wipes on entry means one package deleting another's tables
// halfway through its run.
func requireDB(t *testing.T) string {
	t.Helper()

	base := os.Getenv(testDSNEnv)
	if base == "" {
		t.Skipf("set %s to a PostgreSQL URL to run integration tests", testDSNEnv)
	}
	if !strings.Contains(strings.ToLower(dsnDatabase(base)), "test") {
		t.Fatalf("%s database name must contain \"test\" (refusing to use %q)",
			testDSNEnv, dsnDatabase(base))
	}

	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("generate a schema name: %v", err)
	}
	schema := "gctest_" + hex.EncodeToString(suffix[:])

	db, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `CREATE SCHEMA `+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	// Provision the reports role and grant it read on this schema. The role is
	// created idempotently because packages run concurrently and share it; the
	// default-privileges grant has to precede the migrations New() will run, so
	// every table they create is readable by the role without a second grant.
	if _, err := db.ExecContext(context.Background(), `
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '`+testReportsRole+`') THEN
				CREATE ROLE `+testReportsRole+` LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
			END IF;
		EXCEPTION WHEN duplicate_object THEN
			NULL;
		END $$;`); err != nil {
		t.Fatalf("create reports role: %v", err)
	}
	for _, stmt := range []string{
		`GRANT USAGE ON SCHEMA ` + schema + ` TO ` + testReportsRole,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA ` + schema + ` GRANT SELECT ON TABLES TO ` + testReportsRole,
	} {
		if _, err := db.ExecContext(context.Background(), stmt); err != nil {
			t.Fatalf("grant to reports role: %v", err)
		}
	}
	t.Cleanup(func() {
		cleanup, err := sql.Open("pgx", base)
		if err != nil {
			return
		}
		defer cleanup.Close()
		if _, err := cleanup.ExecContext(context.Background(),
			`DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			t.Logf("could not drop the test schema %s: %v", schema, err)
		}
	})

	// libpq's `options` applies per connection, so every connection in the
	// pool lands in the same schema — which a bare SET search_path would not
	// guarantee.
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	return base + separator + "options=" + url.QueryEscape("-csearch_path="+schema)
}

// resetSchema is a no-op now that every test gets its own schema. It remains
// so the call sites read the same as before.
func resetSchema(t *testing.T, dsn string) { t.Helper() }

// reportsDSN rewrites an admin test DSN to log in as the least-privileged
// reports role, keeping the same schema (the options= parameter is preserved).
// It is how a test app's ReportsDBURL is derived, so the reports pool lands in
// the same schema the engine migrated and reads the tables it created.
func reportsDSN(adminDSN string) string {
	u, err := url.Parse(adminDSN)
	if err != nil {
		// The test DSNs are URL-style; a parse failure is a test-setup bug, and
		// returning the admin DSN unchanged would silently defeat the isolation
		// the reports role exists to prove, so make it fail loudly instead.
		panic("reportsDSN: cannot parse test DSN: " + err.Error())
	}
	u.User = url.User(testReportsRole)
	return u.String()
}

// dsnDatabase extracts the database name from a URL- or keyword-style DSN,
// well enough for the safety guard above.
func dsnDatabase(dsn string) string {
	if i := strings.Index(dsn, "dbname="); i >= 0 {
		rest := dsn[i+len("dbname="):]
		if j := strings.IndexAny(rest, " \t"); j >= 0 {
			return rest[:j]
		}
		return rest
	}
	s := dsn
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[i+1:]
		if j := strings.IndexAny(s, "?"); j >= 0 {
			s = s[:j]
		}
		return s
	}
	return ""
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}

func testConfig(dsn string) Config {
	return Config{
		DBURL:        dsn,
		ReportsDBURL: reportsDSN(dsn),
		AdminTokens:  []string{testAdminToken},
		Logger:       quietLogger(),
	}
}

// newTestApp boots an engine against a freshly emptied test database.
func newTestApp(t *testing.T, mods ...Module) *App {
	t.Helper()
	dsn := requireDB(t)
	resetSchema(t, dsn)

	app, err := New(testConfig(dsn), mods...)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

// do runs a request through the engine's full middleware chain.
func do(t *testing.T, app *App, method, target string, opts ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func withAdmin(r *http.Request) { r.Header.Set("Authorization", "Bearer "+testAdminToken) }

func header(k, v string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set(k, v) }
}

// ---------------------------------------------------------------- test module

// testModule is a minimal module used to prove the extension mechanism: it
// owns a table, mounts a public and an admin route, contributes to the API
// contract, and records its lifecycle hooks.
type testModule struct {
	name      string
	migration *Migration
	routes    []string
	admin     []string
	spec      []byte

	started, stopped bool
	registerErr      error
}

func (m *testModule) Name() string { return m.name }

func (m *testModule) Migrations() []Migration {
	if m.migration == nil {
		return nil
	}
	return []Migration{*m.migration}
}

func (m *testModule) Register(app *App) error {
	if m.registerErr != nil {
		return m.registerErr
	}
	for _, p := range m.routes {
		app.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			Respond(w, http.StatusOK, map[string]string{"module": m.name})
		})
	}
	for _, p := range m.admin {
		app.HandleAdminFunc(p, func(w http.ResponseWriter, r *http.Request) {
			Respond(w, http.StatusOK, map[string]string{"module": m.name, "scope": "admin"})
		})
	}
	app.OnStart(func(context.Context) error { m.started = true; return nil })
	app.OnStop(func(context.Context) error { m.stopped = true; return nil })
	return nil
}

func (m *testModule) OpenAPI() []byte { return m.spec }
