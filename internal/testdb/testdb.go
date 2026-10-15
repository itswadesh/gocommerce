// Package testdb gives each integration test a database of its own, copied
// from a template that was migrated once.
//
// Migrating per test cost every test several seconds once the schema grew a
// pass that puts every table under its store (D83), and a thousand tests made
// the core package outlast CI's hour. Copying a migrated database with
// CREATE DATABASE … TEMPLATE takes a fraction of that, and a test still gets a
// database nobody else is touching.
//
// It imports nothing from the engine, so the engine's own tests and gctest
// can both use it.
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// EnvVar names the PostgreSQL URL tests run against. It must name a database
// whose name contains "test": these helpers create and drop databases.
const EnvVar = "GOCOMMERCE_TEST_DB"

// The logins tests run as. The engine's must not be a superuser: PostgreSQL
// skips row security for one, so a test run as one would prove no store
// isolation at all.
const (
	EngineRole      = "gctest_engine"
	EnginePassword  = "gctest_engine"
	ReportsRole     = "gctest_reports"
	ReportsPassword = "gctest_reports"
)

// buildLockKey serialises template builds across processes ("gctpl" + 1).
const buildLockKey int64 = 0x6763_74706c01

// staleAfter is how old a leftover database from a crashed run must be before
// a later run drops it.
const staleAfter = 3 * time.Hour

var (
	mu        sync.Mutex
	templates = map[string]string{} // key → template database name
	swept     bool
)

// Base returns the configured URL, skipping the test when there is none and
// failing it when the database name does not say "test".
func Base(t testing.TB) string {
	t.Helper()
	base := os.Getenv(EnvVar)
	if base == "" {
		t.Skipf("set %s to a PostgreSQL URL to run integration tests", EnvVar)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("testdb: %s is not a URL: %v", EnvVar, err)
	}
	if !strings.Contains(strings.ToLower(strings.TrimPrefix(u.Path, "/")), "test") {
		t.Fatalf("testdb: refusing to use %q — the %s database name must contain \"test\"", u.Path, EnvVar)
	}
	return base
}

// DSN points base at database db, logging in as user.
func DSN(base, db, user, password string) string {
	u, _ := url.Parse(base)
	u.Path = "/" + db
	if user != "" {
		u.User = url.UserPassword(user, password)
	}
	q := u.Query()
	q.Del("options")
	u.RawQuery = q.Encode()
	return u.String()
}

func admin(t testing.TB, base string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", base)
	if err != nil {
		t.Fatalf("testdb: open: %v", err)
	}
	return db
}

// EnsureRoles creates the engine and reports logins if they do not exist.
// They are cluster-wide and shared by every concurrently running package.
func EnsureRoles(t testing.TB, db *sql.DB) {
	t.Helper()
	for _, r := range []struct{ name, pw string }{{EngineRole, EnginePassword}, {ReportsRole, ReportsPassword}} {
		if _, err := db.ExecContext(context.Background(), `
			DO $$
			BEGIN
				IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '`+r.name+`') THEN
					CREATE ROLE `+r.name+` LOGIN PASSWORD '`+r.pw+`' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
				END IF;
			EXCEPTION WHEN duplicate_object THEN
				NULL;
			END $$;`); err != nil {
			t.Fatalf("testdb: create role %s: %v", r.name, err)
		}
	}
}

func suffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return strconv.FormatInt(time.Now().Unix(), 10) + "_" + hex.EncodeToString(b[:])
}

// sweep drops databases a crashed run left behind, once per process.
func sweep(db *sql.DB) {
	if swept {
		return
	}
	swept = true
	rows, err := db.QueryContext(context.Background(),
		`SELECT datname FROM pg_database WHERE datname LIKE 'gctpl\_%' OR datname LIKE 'gct\_%'`)
	if err != nil {
		return
	}
	var stale []string
	cutoff := time.Now().Add(-staleAfter).Unix()
	for rows.Next() {
		var name string
		if rows.Scan(&name) != nil {
			continue
		}
		parts := strings.Split(name, "_")
		if len(parts) < 3 {
			continue
		}
		if at, err := strconv.ParseInt(parts[1], 10, 64); err == nil && at < cutoff {
			stale = append(stale, name)
		}
	}
	rows.Close()
	for _, name := range stale {
		_, _ = db.ExecContext(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`)
	}
}

// Clone returns the URL of a new database for this test alone, logging in as
// the engine's role, copied from the template for key. The first call for a
// key makes the template: an empty database owned by the engine's role, which
// build then migrates by booting an engine against it. The copy is dropped
// when the test ends.
//
// key must name everything that decides the schema — the modules mounted, in
// order — so two tests share a template only when their schemas match.
func Clone(t testing.TB, key string, build func(dsn string) error) string {
	t.Helper()
	base := Base(t)
	db := admin(t, base)
	defer db.Close()

	mu.Lock()
	defer mu.Unlock()
	sweep(db)
	tpl, ok := templates[key]
	if !ok {
		// One template is built at a time across every test process: a
		// migration that scopes every table takes a lock per table, index
		// and constraint, and a dozen packages doing it at once exhaust the
		// server's shared lock table ("out of shared memory"). Copies stay
		// concurrent; only building waits.
		conn, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("testdb: lock connection: %v", err)
		}
		defer conn.Close()
		if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, buildLockKey); err != nil {
			t.Fatalf("testdb: take the build lock: %v", err)
		}
		defer conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, buildLockKey)
		EnsureRoles(t, db)
		tpl = "gctpl_" + suffix()
		if _, err := db.ExecContext(context.Background(),
			`CREATE DATABASE "`+tpl+`" OWNER `+EngineRole); err != nil {
			t.Fatalf("testdb: create template: %v", err)
		}
		// The engine's role owns the schema its tables go in, and the reports
		// role can read whatever the engine creates there.
		suName, suPass := superUser(base)
		tdb, err := sql.Open("pgx", DSN(base, tpl, suName, suPass))
		if err != nil {
			t.Fatalf("testdb: open template: %v", err)
		}
		for _, stmt := range []string{
			`ALTER SCHEMA public OWNER TO ` + EngineRole,
			`ALTER DEFAULT PRIVILEGES FOR ROLE ` + EngineRole + ` IN SCHEMA public GRANT SELECT ON TABLES TO ` + ReportsRole,
		} {
			if _, err := tdb.ExecContext(context.Background(), stmt); err != nil {
				tdb.Close()
				t.Fatalf("testdb: prepare template: %v", err)
			}
		}
		tdb.Close()
		if err := build(DSN(base, tpl, EngineRole, EnginePassword)); err != nil {
			t.Fatalf("testdb: migrate template: %v", err)
		}
		templates[key] = tpl
	}

	name := "gct_" + suffix()
	if _, err := db.ExecContext(context.Background(),
		fmt.Sprintf(`CREATE DATABASE "%s" TEMPLATE "%s" OWNER %s`, name, tpl, EngineRole)); err != nil {
		t.Fatalf("testdb: copy template: %v", err)
	}
	t.Cleanup(func() {
		cdb := admin(t, base)
		defer cdb.Close()
		if _, err := cdb.ExecContext(context.Background(), `DROP DATABASE IF EXISTS "`+name+`" WITH (FORCE)`); err != nil {
			t.Logf("testdb: could not drop %s: %v", name, err)
		}
	})
	return DSN(base, name, EngineRole, EnginePassword)
}

// superUser is the login and password the configured URL carries.
func superUser(base string) (string, string) {
	u, _ := url.Parse(base)
	if u.User == nil {
		return "", ""
	}
	pw, _ := u.User.Password()
	return u.User.Username(), pw
}
