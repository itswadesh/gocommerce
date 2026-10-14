# Shared Tenancy — Foundation and Modules Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** One `gocommerce serve` serves every store from one set of tables, each store-owned row carrying `store_id`, with PostgreSQL Row-Level Security as the net under explicit `WHERE store_id` filters — Parts 1 and 2 of the spec.

**Architecture:** The store id travels in `context.Context`. The engine's `*sql.DB` is opened through a thin `database/sql` driver wrapper that sets the session variable `gocommerce.store_id` on a connection whenever the context's store differs from the one last set there, so every existing `ctx`-taking call site is bound without changing its signature. A Go migration (`ScopeTables`) converts tables by introspection: adds `store_id`, rewrites unique keys and foreign keys to lead with it, and enables forced RLS. A request middleware resolves the store (API key, header, query, cookie, host, sole membership, sole store) and checks membership.

**Tech Stack:** Go 1.27, `database/sql` + `github.com/jackc/pgx/v5` v5.10.0 stdlib driver, PostgreSQL 17/18.

**Spec:** `docs/superpowers/specs/2026-10-14-shared-tenancy-design.md` (Parts 1, 2 and the Testing section). Parts 3 and 4 get their own plans.

## Global Constraints

- Migrations are append-only: new IDs from `0053` in `core/schema.go`; module migrations append to each module's list. Never edit a shipped migration.
- `ext/` packages add zero third-party dependencies. The ULID generator is hand-written in `core` (stdlib only).
- Money stays `*_minor` integers; nothing here touches money columns.
- Never write core tables from a module.
- Store id format: `store_` + 26-character Crockford base32 ULID, uppercase (e.g. `store_01JA2Z3K4M5N6P7Q8R9S0T1V2W`).
- Session variable name: `gocommerce.store_id`. Policy name on every store table: `store_scope`.
- Wire names: header `x-litekart-store` (id), alias header `X-Store` (slug or id), query parameter `store` (id or slug), cookie `litekart_store_id`.
- Global (unscoped) tables: `stores`, `store_domains`, `store_members`, `store_counters`, `api_key_index`, `superusers`, `superuser_sessions`, `superuser_password_resets`, `gocommerce_migrations`. Everything else is a store table.
- Unique keys that stay global: `carts.token`, `api_keys.prefix`, `outbox_events.event_id`.
- Errors: no store on a store route → 400 `store required`; unknown store → 404; not a member → 403; API key store ≠ named store → 403; suspended → 503.
- Tests run against `postgres://gocommerce@127.0.0.1:5460/gocommerce_test?sslmode=disable` (never 5433). Go is at `C:\tools\go\bin`.
- Comments explain why. Commit subjects are public (kitcommerce.store/updates); this branch is not pushed until Parts 1+2 are complete, and every commit carries `Update: skip` except the final one.
- Every commit ends with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`.

## Review Focus

1. **A pooled connection carrying store A's setting into a statement for store B** — expect B's statement to see only B. Pinned by Task 2's single-connection pool test and the rollback test.
2. **Rows iterated while a nested query runs on the same request** — expect no "conn busy": the wrapper never pins a connection to a request. Pinned by Task 2's nested-query test.
3. **A static admin token or a payment webhook on an install with several stores and no store named** — expect 400, never a silent default to store #1. Pinned by Task 8's resolution table test.
4. **An upgraded single-store install whose clients send nothing new** — expect everything to work exactly as before (rule 7: the only store). Pinned by Task 5's upgrade test and Task 8's "one store, no header" case.
5. **A background job writing with no store bound under `serve`** — expect the write to fail (`NOT NULL`), not land in some store. Pinned by Task 12's `TestUnboundWorkCannotWriteUnderServe`.

---

## File Structure

| File | Responsibility |
|---|---|
| `core/stores.go` (create) | Store ids (ULID), `WithStore`/`StoreFrom`/`StoreID`, the `Stores` service (lookup by id/slug/host, list active, create row) |
| `core/storedriver.go` (create) | `database/sql` driver wrapper that binds `gocommerce.store_id` per statement |
| `core/scope.go` (create) | `ScopeTables` — the introspecting converter used by core and module migrations |
| `core/storeresolve.go` (create) | Request → store resolution middleware and its exempt list |
| `core/foreachstore.go` (create) | `App.ForEachStore` for background work |
| `core/db.go` (modify) | `OpenDB` opens through the wrapper |
| `core/schema.go` (modify) | Migrations `0053_stores`, `0054_store_scope` |
| `core/gocommerce.go` (modify) | `Config.DefaultStore`, wiring, `Handler` chain, background work |
| `core/superusers.go`, `core/httpx.go` (modify) | Role and vendor come from `store_members` for the bound store |
| `core/checkout.go` (modify) | Per-store order numbers |
| `core/storelint_test.go` (create) | Static checks: every DML on a store table names `store_id`; no context-free DB calls |
| `core/tenancy_test.go` (create) | RLS coverage, per-store uniqueness, cross-store FK refusal, upgrade |
| `core/isolation_test.go` (create) | Two-store isolation suite |
| `core/helpers_test.go`, `gctest/gctest.go` (modify) | Engine runs as a non-superuser login |
| `platform/`, `docker-compose.platform.yml` (delete); `cmd/gocommerce/main.go` (modify) | One shape |
| `ext/*/` (modify) | Module migrations calling `ScopeTables`; `store_id` in statements |
| `cmd/gocommerce/tenancy_test.go` (create) | RLS coverage with every module mounted |

---

### Task 1: Store ids and the store in a context

**Files:**
- Create: `core/stores.go`
- Test: `core/stores_test.go`

**Interfaces:**
- Produces:
  - `func NewStoreID() (string, error)` — `"store_" + ULID`
  - `func ValidStoreID(s string) bool`
  - `func WithStore(ctx context.Context, storeID string) context.Context`
  - `func StoreFrom(ctx context.Context) (string, bool)`

- [ ] **Step 1: Write the failing test**

```go
package gocommerce

import (
	"context"
	"testing"
	"time"
)

func TestNewStoreIDIsAULIDAndSortsByTime(t *testing.T) {
	a, err := NewStoreID()
	if err != nil {
		t.Fatal(err)
	}
	if !ValidStoreID(a) || len(a) != len("store_")+26 {
		t.Fatalf("NewStoreID() = %q, want store_ + 26 Crockford characters", a)
	}
	time.Sleep(2 * time.Millisecond)
	b, _ := NewStoreID()
	if !(a < b) {
		t.Fatalf("ids do not sort by creation time: %q then %q", a, b)
	}
}

func TestValidStoreIDRefusesLookalikes(t *testing.T) {
	for _, s := range []string{"", "store_", "store_01JA2Z3K4M5N6P7Q8R9S0T1V2", "shop_01JA2Z3K4M5N6P7Q8R9S0T1V2W",
		"store_01JA2Z3K4M5N6P7Q8R9S0T1V2I", "store_01ja2z3k4m5n6p7q8r9s0t1v2w", "store_01JA2Z3K4M5N6P7Q8R9S0T1V2W'"} {
		if ValidStoreID(s) {
			t.Errorf("ValidStoreID(%q) = true", s)
		}
	}
}

func TestStoreTravelsInTheContext(t *testing.T) {
	if _, ok := StoreFrom(context.Background()); ok {
		t.Fatal("a bare context claims a store")
	}
	ctx := WithStore(context.Background(), "store_01JA2Z3K4M5N6P7Q8R9S0T1V2W")
	if got, ok := StoreFrom(ctx); !ok || got != "store_01JA2Z3K4M5N6P7Q8R9S0T1V2W" {
		t.Fatalf("StoreFrom = %q, %v", got, ok)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./core -run 'TestNewStoreID|TestValidStoreID|TestStoreTravels' -count=1`
Expected: FAIL — `undefined: NewStoreID`.

- [ ] **Step 3: Write minimal implementation**

```go
package gocommerce

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"time"
)

// crockford is ULID's alphabet: no I, L, O or U, so an id read aloud or
// copied by hand cannot be mistaken for another.
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewStoreID returns "store_" + a ULID: 48 bits of milliseconds, then 80
// random bits. The time prefix makes ids sort by creation, which is the
// order a store list is read in; kitcommerce-api's ids have the same shape,
// so a client written for it accepts ours.
func NewStoreID() (string, error) {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(time.Now().UnixMilli())<<16)
	if _, err := rand.Read(b[6:]); err != nil {
		return "", err
	}
	// 128 bits → 26 base-32 characters, most significant first.
	hi := binary.BigEndian.Uint64(b[:8])
	lo := binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := 25; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return "store_" + string(out[:]), nil
}

// ValidStoreID reports whether s has the exact shape NewStoreID produces. It
// gates every id that arrives from outside, so a header can never smuggle
// anything else into a query parameter or a log line.
func ValidStoreID(s string) bool {
	const prefix = "store_"
	if len(s) != len(prefix)+26 || s[:len(prefix)] != prefix {
		return false
	}
	for i := len(prefix); i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'A' && c <= 'Z') || c == 'I' || c == 'L' || c == 'O' || c == 'U' {
			return false
		}
	}
	return true
}

type storeKey struct{}

// WithStore binds ctx to a store. Every database statement run with the
// returned context is scoped to that store (see storedriver.go).
func WithStore(ctx context.Context, storeID string) context.Context {
	return context.WithValue(ctx, storeKey{}, storeID)
}

// StoreFrom returns the store ctx is bound to, if any.
func StoreFrom(ctx context.Context) (string, bool) {
	s, ok := ctx.Value(storeKey{}).(string)
	return s, ok && s != ""
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./core -run 'TestNewStoreID|TestValidStoreID|TestStoreTravels' -count=1`
Expected: PASS (3 tests).

- [ ] **Step 5: Commit**

```bash
git add core/stores.go core/stores_test.go
git commit -m "Add store ids and carry the store in a context" -m "Update: skip" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 2: A driver that binds each statement to its context's store

**Files:**
- Create: `core/storedriver.go`
- Modify: `core/db.go` (`OpenDB`, `OpenReportsDB`)
- Test: `core/storedriver_test.go`

**Interfaces:**
- Consumes: `StoreFrom(ctx)` (Task 1)
- Produces:
  - `type storeConnector struct` with `func (c *storeConnector) SetFallback(storeID string)`
  - `func OpenDB(ctx context.Context, dsn string) (*sql.DB, error)` — unchanged signature, now through the wrapper
  - `func storeConnectorOf(db *sql.DB) *storeConnector` — returns the connector `OpenDB` used (kept in a package map keyed by `*sql.DB`)
  - session variable `gocommerce.store_id` is `''` for an unbound context with no fallback

- [ ] **Step 1: Write the failing tests**

```go
package gocommerce

import (
	"context"
	"database/sql"
	"testing"
)

// boundTable makes a throwaway RLS table in the test schema, as the engine's
// own tables will be after 0054, so the driver can be tested before them.
func boundTable(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, q := range []string{
		`CREATE TABLE drv_rows (store_id text NOT NULL, v text NOT NULL)`,
		`ALTER TABLE drv_rows ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE drv_rows FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY store_scope ON drv_rows
		   USING (store_id = current_setting('gocommerce.store_id', true))
		   WITH CHECK (store_id = current_setting('gocommerce.store_id', true))`,
	} {
		if _, err := db.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func openOneConn(t *testing.T) *sql.DB {
	t.Helper()
	db, err := OpenDB(context.Background(), requireDB(t))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1) // every statement below reuses the same connection
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, ctx context.Context, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM drv_rows`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAConnectionNeverCarriesOneStoreIntoAnother(t *testing.T) {
	db := openOneConn(t)
	boundTable(t, db)
	a := WithStore(context.Background(), "store_A")
	b := WithStore(context.Background(), "store_B")
	for _, s := range []struct{ ctx context.Context; v string }{{a, "a1"}, {a, "a2"}, {b, "b1"}} {
		if _, err := db.ExecContext(s.ctx, `INSERT INTO drv_rows (store_id, v) VALUES (current_setting('gocommerce.store_id'), $1)`, s.v); err != nil {
			t.Fatal(err)
		}
	}
	if got := count(t, a, db); got != 2 {
		t.Fatalf("store A sees %d rows, want 2", got)
	}
	if got := count(t, b, db); got != 1 {
		t.Fatalf("store B sees %d rows, want 1", got)
	}
	if got := count(t, context.Background(), db); got != 0 {
		t.Fatalf("an unbound statement sees %d rows, want 0", got)
	}
	if _, err := db.ExecContext(b, `INSERT INTO drv_rows (store_id, v) VALUES ('store_A', 'smuggled')`); err == nil {
		t.Fatal("store B wrote a row for store A")
	}
}

func TestARolledBackTransactionForgetsItsStore(t *testing.T) {
	db := openOneConn(t)
	boundTable(t, db)
	a := WithStore(context.Background(), "store_A")
	if _, err := db.ExecContext(a, `INSERT INTO drv_rows VALUES ('store_A', 'x')`); err != nil {
		t.Fatal(err)
	}
	// Bind B inside a transaction, then roll it back: set_config is
	// transactional, so the server is back on A while a naive cache would
	// still believe B.
	b := WithStore(context.Background(), "store_B")
	tx, err := db.BeginTx(b, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = tx.Rollback()
	if got := count(t, b, db); got != 0 {
		t.Fatalf("after rollback, store B sees %d rows, want 0", got)
	}
	if got := count(t, a, db); got != 1 {
		t.Fatalf("after rollback, store A sees %d rows, want 1", got)
	}
}

func TestPreparedStatementsAreBoundToo(t *testing.T) {
	db := openOneConn(t)
	boundTable(t, db)
	a := WithStore(context.Background(), "store_A")
	tx, err := db.BeginTx(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := tx.PrepareContext(a, `INSERT INTO drv_rows VALUES ($1, $2)`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ExecContext(a, "store_A", "p"); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got := count(t, a, db); got != 1 {
		t.Fatalf("store A sees %d rows, want 1", got)
	}
}

func TestNestedQueriesWhileIteratingDoNotCollide(t *testing.T) {
	db, err := OpenDB(context.Background(), requireDB(t))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	boundTable(t, db)
	a := WithStore(context.Background(), "store_A")
	for _, v := range []string{"1", "2", "3"} {
		if _, err := db.ExecContext(a, `INSERT INTO drv_rows VALUES ('store_A', $1)`, v); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.QueryContext(a, `SELECT v FROM drv_rows`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		if got := count(t, a, db); got != 3 {
			t.Fatalf("nested count = %d, want 3", got)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestTheFallbackBindsAnUnboundContext(t *testing.T) {
	db := openOneConn(t)
	boundTable(t, db)
	storeConnectorOf(db).SetFallback("store_A")
	if _, err := db.ExecContext(context.Background(), `INSERT INTO drv_rows VALUES ('store_A', 'f')`); err != nil {
		t.Fatal(err)
	}
	if got := count(t, context.Background(), db); got != 1 {
		t.Fatalf("fallback sees %d rows, want 1", got)
	}
	if got := count(t, WithStore(context.Background(), "store_B"), db); got != 0 {
		t.Fatal("an explicit store lost to the fallback")
	}
}
```

Note: `store_A`/`store_B` are deliberately not valid ULIDs — the driver binds whatever the context carries; validation happens at the edge (Task 8).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./core -run 'TestAConnection|TestARolledBack|TestPrepared|TestNested|TestTheFallback' -count=1`
Expected: FAIL — the test DB login is a superuser until Task 3, so RLS is bypassed and "store B sees 3 rows"; and `storeConnectorOf` is undefined. Compile error first: `undefined: storeConnectorOf`.

Task 2 and Task 3 are verified together: implement Task 2's code (Step 3), then do Task 3, then run Step 4 here.

- [ ] **Step 3: Write the implementation**

`core/storedriver.go`:

```go
package gocommerce

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync"
	"sync/atomic"

	"github.com/jackc/pgx/v5/stdlib"
)

// The store a statement runs for is decided by its context, at the moment it
// runs, on whichever pooled connection it lands. Binding a connection to a
// request instead would pin it for the request's life, and any handler that
// queries while iterating rows would then fail with "conn busy"; binding per
// statement costs one extra round trip only when the store changes.

const unknownStore = "\x00unknown"

type storeConnector struct {
	inner    driver.Connector
	fallback atomic.Pointer[string]
}

// SetFallback names the store an unbound context is bound to. Empty means
// none: unbound work sees no store rows. Only embedders and tests set it.
func (c *storeConnector) SetFallback(storeID string) { c.fallback.Store(&storeID) }

func (c *storeConnector) want(ctx context.Context) string {
	if s, ok := StoreFrom(ctx); ok {
		return s
	}
	if f := c.fallback.Load(); f != nil {
		return *f
	}
	return ""
}

func (c *storeConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.inner.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return &storeConn{inner: conn, c: c, current: unknownStore}, nil
}

func (c *storeConnector) Driver() driver.Driver { return c.inner.Driver() }

var connectors sync.Map // *sql.DB → *storeConnector

func storeConnectorOf(db *sql.DB) *storeConnector {
	v, _ := connectors.Load(db)
	c, _ := v.(*storeConnector)
	return c
}

func newStoreConnector(dsn string) (*storeConnector, error) {
	inner, err := stdlib.GetDefaultDriver().(driver.DriverContext).OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return &storeConnector{inner: inner}, nil
}

type storeConn struct {
	inner   driver.Conn
	c       *storeConnector
	current string // what gocommerce.store_id is on the server, or unknownStore
}

func (s *storeConn) bind(ctx context.Context) error {
	want := s.c.want(ctx)
	if want == s.current {
		return nil
	}
	ex := s.inner.(driver.ExecerContext)
	if _, err := ex.ExecContext(ctx, `SELECT set_config('gocommerce.store_id', $1, false)`,
		[]driver.NamedValue{{Ordinal: 1, Value: want}}); err != nil {
		s.current = unknownStore
		return err
	}
	s.current = want
	return nil
}

func (s *storeConn) ExecContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Result, error) {
	if err := s.bind(ctx); err != nil {
		return nil, err
	}
	return s.inner.(driver.ExecerContext).ExecContext(ctx, q, args)
}

func (s *storeConn) QueryContext(ctx context.Context, q string, args []driver.NamedValue) (driver.Rows, error) {
	if err := s.bind(ctx); err != nil {
		return nil, err
	}
	return s.inner.(driver.QueryerContext).QueryContext(ctx, q, args)
}

func (s *storeConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	st, err := s.inner.(driver.ConnPrepareContext).PrepareContext(ctx, q)
	if err != nil {
		return nil, err
	}
	return &storeStmt{inner: st, conn: s}, nil
}

func (s *storeConn) Prepare(q string) (driver.Stmt, error) {
	return s.PrepareContext(context.Background(), q)
}

func (s *storeConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if err := s.bind(ctx); err != nil {
		return nil, err
	}
	tx, err := s.inner.(driver.ConnBeginTx).BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	return &storeTx{inner: tx, conn: s}, nil
}

func (s *storeConn) Begin() (driver.Tx, error) {
	return s.BeginTx(context.Background(), driver.TxOptions{})
}

func (s *storeConn) Close() error { return s.inner.Close() }

func (s *storeConn) Ping(ctx context.Context) error { return s.inner.(driver.Pinger).Ping(ctx) }

func (s *storeConn) ResetSession(ctx context.Context) error {
	if r, ok := s.inner.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (s *storeConn) IsValid() bool {
	if v, ok := s.inner.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

// CheckNamedValue forwards pgx's own argument conversion; without it
// database/sql would apply the default converter and reject pgx types.
func (s *storeConn) CheckNamedValue(nv *driver.NamedValue) error {
	if c, ok := s.inner.(driver.NamedValueChecker); ok {
		return c.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// Raw hands back pgx's own connection for the rare caller that needs it
// (COPY); such a caller is responsible for its own binding.
func (s *storeConn) Raw() driver.Conn { return s.inner }

type storeTx struct {
	inner driver.Tx
	conn  *storeConn
}

func (t *storeTx) Commit() error { return t.inner.Commit() }

// Rollback undoes any set_config the transaction ran, so what the server
// holds is no longer known; the next statement sets it again.
func (t *storeTx) Rollback() error {
	t.conn.current = unknownStore
	return t.inner.Rollback()
}

type storeStmt struct {
	inner driver.Stmt
	conn  *storeConn
}

func (s *storeStmt) Close() error  { return s.inner.Close() }
func (s *storeStmt) NumInput() int { return s.inner.NumInput() }

func (s *storeStmt) Exec(args []driver.Value) (driver.Result, error) {
	return nil, errors.New("gocommerce: statements must run with a context")
}

func (s *storeStmt) Query(args []driver.Value) (driver.Rows, error) {
	return nil, errors.New("gocommerce: statements must run with a context")
}

func (s *storeStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	if err := s.conn.bind(ctx); err != nil {
		return nil, err
	}
	return s.inner.(driver.StmtExecContext).ExecContext(ctx, args)
}

func (s *storeStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	if err := s.conn.bind(ctx); err != nil {
		return nil, err
	}
	return s.inner.(driver.StmtQueryContext).QueryContext(ctx, args)
}

func (s *storeStmt) CheckNamedValue(nv *driver.NamedValue) error {
	if c, ok := s.inner.(driver.NamedValueChecker); ok {
		return c.CheckNamedValue(nv)
	}
	return s.conn.CheckNamedValue(nv)
}
```

In `core/db.go`, replace `sql.Open("pgx", dsn)` in `OpenDB` with:

```go
	conn, err := newStoreConnector(dsn)
	if err != nil {
		return nil, fmt.Errorf("gocommerce: open database: %w", err)
	}
	db := sql.OpenDB(conn)
	connectors.Store(db, conn)
```

and in `OpenDB`'s ping-failure path and nowhere else add `connectors.Delete(db)` before `db.Close()`. `App.Close` (core/gocommerce.go, both `a.db.Close()` sites) calls `connectors.Delete(a.db)` first.

- [ ] **Step 4: Run tests to verify they pass** (after Task 3)

Run: `go test ./core -run 'TestAConnection|TestARolledBack|TestPrepared|TestNested|TestTheFallback' -count=1 -v`
Expected: PASS (5 tests).

- [ ] **Step 5: Commit**

```bash
git add core/storedriver.go core/storedriver_test.go core/db.go core/gocommerce.go
git commit -m "Bind every database statement to its context's store" -m "Update: skip" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 3: Tests run the engine as an ordinary login

Row-Level Security does not apply to superusers, and the local container's `gocommerce` login is one. Every test app must connect as a login without `SUPERUSER` or `BYPASSRLS`, or every isolation test passes vacuously.

**Files:**
- Modify: `core/helpers_test.go` (schema creation, `testConfig`)
- Modify: `gctest/gctest.go` (`IsolatedDSN`)
- Test: `core/tenancy_test.go` (create, first test)

**Interfaces:**
- Produces: test DSNs whose user is `gctest_engine` (password `gctest_engine`); the schema is owned by `gctest_engine`.

- [ ] **Step 1: Write the failing test**

```go
package gocommerce

import (
	"context"
	"testing"
)

func TestTheEngineCannotBypassRowLevelSecurity(t *testing.T) {
	app := newTestApp(t)
	var super, bypass bool
	if err := app.db.QueryRowContext(context.Background(),
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).Scan(&super, &bypass); err != nil {
		t.Fatal(err)
	}
	if super || bypass {
		t.Fatalf("the test engine logs in as superuser=%v bypassrls=%v; RLS tests would prove nothing", super, bypass)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./core -run TestTheEngineCannotBypass -count=1`
Expected: FAIL — `superuser=true`.

- [ ] **Step 3: Implement**

In `core/helpers_test.go`, next to `testReportsRole`:

```go
// testEngineRole is the login every test app runs as. It must not be a
// superuser: PostgreSQL skips Row-Level Security for superusers even when a
// table forces it, so a test run as one would prove no isolation at all.
const (
	testEngineRole     = "gctest_engine"
	testEnginePassword = "gctest_engine"
)
```

Before `CREATE SCHEMA`, create the role idempotently with the same `DO $$ … EXCEPTION WHEN duplicate_object` block used for the reports role:

```sql
CREATE ROLE gctest_engine LOGIN PASSWORD 'gctest_engine' NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
```

Change `CREATE SCHEMA `+schema to `CREATE SCHEMA `+schema+` AUTHORIZATION `+testEngineRole, and change the default-privileges grant to `ALTER DEFAULT PRIVILEGES FOR ROLE gctest_engine IN SCHEMA `+schema+` GRANT SELECT ON TABLES TO `+testReportsRole (the tables are now created by the engine role, so the default must be declared for it). Then rewrite the returned DSN's user exactly as `reportsDSN` does:

```go
	u, err := url.Parse(base + separator + "options=" + url.QueryEscape("-csearch_path="+schema))
	if err != nil {
		t.Fatalf("parse test DSN: %v", err)
	}
	u.User = url.UserPassword(testEngineRole, testEnginePassword)
	return u.String()
```

The `DROP SCHEMA` cleanup keeps using `base` (the superuser), which can drop a schema it does not own.

In `gctest/gctest.go`, `IsolatedDSN` gets the same three changes (role, `AUTHORIZATION`, DSN user). Copy the constants into `gctest` (it cannot import core's test file).

PostgreSQL 15+ no longer lets ordinary logins create objects in `public`; the engine only creates objects in its own schema, so no extra grant is needed. `pgcrypto`/extensions: grep `CREATE EXTENSION` in `core/schema.go` and `ext/`; if any exists, create it in the helper as the superuser before the engine's migrations run, in the test schema.

- [ ] **Step 4: Run the test and then the whole suite**

Run: `go test ./core -run 'TestTheEngineCannotBypass|TestAConnection|TestARolledBack|TestPrepared|TestNested|TestTheFallback' -count=1`
Expected: PASS (6 tests).

Run: `go test ./... -count=1 -timeout 60m > $SCRATCH/t3.txt 2>&1; tail -40 $SCRATCH/t3.txt`
Expected: every package `ok` (the `platform` package may fail on role creation in its own helper; if so, apply the same change to `platform/platform_test.go`'s helper — it is deleted in Task 14).

- [ ] **Step 5: Commit**

```bash
git add core/helpers_test.go core/tenancy_test.go gctest/gctest.go core/storedriver_test.go platform/platform_test.go
git commit -m "Run test engines as a login that cannot bypass row security" -m "Update: skip" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 4: Global store tables, store #1, and membership

**Files:**
- Modify: `core/schema.go` (append `{ID: "0053_stores", Run: migrate0053Stores}`)
- Modify: `core/stores.go` (the `Stores` service)
- Modify: `core/superusers.go`, `core/superusers_http.go`, `core/vendor_accounts.go`, `core/invitations.go` (role/vendor from `store_members`)
- Modify: `core/gocommerce.go` (`Config.DefaultStore`, `App.Stores()`, `App.DefaultStore()`)
- Test: `core/tenancy_test.go`

**Interfaces:**
- Consumes: `NewStoreID` (Task 1), `storeConnectorOf` (Task 2)
- Produces:
  - Tables: `stores(id text PK, slug text UNIQUE NOT NULL, name text NOT NULL, status text NOT NULL DEFAULT 'active' CHECK (status IN ('active','suspended')), order_prefix text, created_at timestamptz NOT NULL DEFAULT now())`, `store_domains(host text PK, store_id text NOT NULL REFERENCES stores ON DELETE CASCADE, created_at)`, `store_members(store_id text REFERENCES stores ON DELETE CASCADE, superuser_id bigint REFERENCES superusers ON DELETE CASCADE, role text NOT NULL, vendor_id bigint, created_at, PRIMARY KEY (store_id, superuser_id))`, `store_counters(store_id text REFERENCES stores ON DELETE CASCADE, name text, value bigint NOT NULL, PRIMARY KEY (store_id, name))`, `api_key_index(prefix text PK, store_id text NOT NULL REFERENCES stores ON DELETE CASCADE)`
  - `type Store struct { ID, Slug, Name, Status string; OrderPrefix *string; CreatedAt time.Time }`
  - `func (a *App) Stores() *Stores`
  - `func (s *Stores) Get(ctx, id string) (*Store, error)`; `BySlug(ctx, slug)`; `ByHost(ctx, host)`; `Active(ctx) ([]Store, error)`; `Count(ctx) (int, error)`; `MembershipsOf(ctx, superuserID int64) ([]Membership, error)`
  - `type Membership struct { StoreID, Role string; VendorID *int64 }`
  - `func (a *App) DefaultStore() string` — `Config.DefaultStore`, or empty
  - `Config.DefaultStore string` — `"first"` means "the oldest store", resolved after migrations
  - `func StoreID(ctx context.Context) string` is **not** added; code reads `storeOf(ctx)`: `func (a *App) storeOf(ctx context.Context) string` returning `StoreFrom(ctx)` or the fallback.

- [ ] **Step 1: Write the failing tests**

```go
func TestAFreshInstallHasOneStore(t *testing.T) {
	app := newTestApp(t)
	stores, err := app.Stores().Active(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stores) != 1 || !ValidStoreID(stores[0].ID) || stores[0].Slug == "" {
		t.Fatalf("stores = %+v, want exactly one with a valid id and slug", stores)
	}
	if app.DefaultStore() != stores[0].ID {
		t.Fatalf("DefaultStore() = %q, want %q", app.DefaultStore(), stores[0].ID)
	}
}

func TestAnOperatorsRoleLivesOnTheirMembership(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	su, err := app.Superusers().Create(ctx, "m@example.com", "a-long-password", RoleManager)
	if err != nil {
		t.Fatal(err)
	}
	ms, err := app.Stores().MembershipsOf(ctx, su.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].StoreID != app.DefaultStore() || ms[0].Role != RoleManager {
		t.Fatalf("memberships = %+v", ms)
	}
	var cols int
	_ = app.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'superusers' AND column_name IN ('role','vendor_id')`).Scan(&cols)
	if cols != 0 {
		t.Fatal("superusers still carries role/vendor_id; a person's role belongs to a store")
	}
}
```

`newTestApp`'s `testConfig` sets `DefaultStore: "first"` (add the field there in this step).

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./core -run 'TestAFreshInstall|TestAnOperatorsRole' -count=1`
Expected: FAIL — `app.Stores undefined`.

- [ ] **Step 3: Implement**

Migration `0053_stores` is a `Run` migration because store #1's id is generated:

```go
func migrate0053Stores(ctx context.Context, tx *sql.Tx) error {
	id, err := NewStoreID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
CREATE TABLE stores (
    id           text        PRIMARY KEY CHECK (id ~ '^store_[0-9A-HJKMNP-TV-Z]{26}$'),
    slug         text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name         text        NOT NULL DEFAULT '',
    status       text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    order_prefix text,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE store_domains (
    host       text        PRIMARY KEY CHECK (host = lower(host) AND host <> ''),
    store_id   text        NOT NULL REFERENCES stores (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX store_domains_store_idx ON store_domains (store_id);
CREATE TABLE store_counters (
    store_id text   NOT NULL REFERENCES stores (id) ON DELETE CASCADE,
    name     text   NOT NULL,
    value    bigint NOT NULL,
    PRIMARY KEY (store_id, name)
);
CREATE TABLE api_key_index (
    prefix   text PRIMARY KEY,
    store_id text NOT NULL REFERENCES stores (id) ON DELETE CASCADE
);
CREATE TABLE store_members (
    store_id     text        NOT NULL REFERENCES stores (id) ON DELETE CASCADE,
    superuser_id bigint      NOT NULL REFERENCES superusers (id) ON DELETE CASCADE,
    role         text        NOT NULL,
    vendor_id    bigint,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (store_id, superuser_id),
    CONSTRAINT store_members_vendor_account CHECK ((role = 'vendor') = (vendor_id IS NOT NULL))
);
CREATE INDEX store_members_superuser_idx ON store_members (superuser_id);
`); err != nil {
		return err
	}
	// Store #1 is the install as it was. Its slug comes from the store's name
	// when there is one, because that is what its owner will recognise in a
	// store list; "store" otherwise.
	if _, err := tx.ExecContext(ctx, `
INSERT INTO stores (id, slug, name)
SELECT $1,
       COALESCE(NULLIF(trim(both '-' from regexp_replace(lower(p.name), '[^a-z0-9]+', '-', 'g')), ''), 'store'),
       COALESCE(p.name, '')
FROM (SELECT (SELECT name FROM store_profile WHERE id = 1) AS name) p`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO store_members (store_id, superuser_id, role, vendor_id)
SELECT $1, id, role, vendor_id FROM superusers;
ALTER TABLE superusers DROP CONSTRAINT IF EXISTS superusers_vendor_account;
ALTER TABLE superusers DROP CONSTRAINT IF EXISTS superusers_role_fkey;
ALTER TABLE superusers DROP COLUMN vendor_id;
ALTER TABLE superusers DROP COLUMN role;`, id); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO api_key_index (prefix, store_id) SELECT prefix, $1 FROM api_keys`, id)
	return err
}
```

(`ExecContext` with parameters cannot run several statements under the extended protocol: split the second call into one parameterised `INSERT` and one unparameterised `ExecContext` for the three `ALTER`s. The slug truncation to 63 characters: wrap the expression in `left(…, 63)`.)

`store_members.role → roles` and `store_members.vendor_id → vendors` become composite foreign keys in Task 5, once `roles` and `vendors` carry `store_id`.

`Config.DefaultStore`: in `New`, after `a.migrate(ctx)`:

```go
	if a.cfg.DefaultStore == "first" {
		if err := a.db.QueryRowContext(ctx, `SELECT id FROM stores ORDER BY created_at, id LIMIT 1`).Scan(&a.defaultStore); err != nil {
			return nil, fmt.Errorf("gocommerce: find the first store: %w", err)
		}
	} else {
		a.defaultStore = a.cfg.DefaultStore
	}
	storeConnectorOf(a.db).SetFallback(a.defaultStore)
```

Document the field in `Config`:

```go
	// DefaultStore binds work that names no store to this one. It is for an
	// application that embeds the engine for a single store, and for tests;
	// "first" means the oldest store. `gocommerce serve` leaves it empty, so
	// in the product a statement that forgot its store sees nothing.
	DefaultStore string
```

`Stores` service in `core/stores.go`: plain queries on the global tables (no `store_id` filter needed; they are global). `ByHost` checks `store_domains` first, then `<slug>.<Config.BaseDomain>` when `Config.BaseDomain` is set (add that field; `serve`'s existing `-base-domain` flag moves from platform to it in Task 14).

Superusers: `superuserColumns` loses `role, vendor_id`; `scan` resolves the role by reading `store_members` for `a.storeOf(ctx)`:

```go
const superuserColumns = `id, email, password_hash, created_at, updated_at`

// membership is the person's standing in the store the context is bound to.
// No row means they are not staff here; Resolve treats that as "not signed
// in to this store" and the auth middleware answers 403.
func (s *Superusers) membership(ctx context.Context, id int64) (role string, vendorID *int64, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT role, vendor_id FROM store_members WHERE store_id = $1 AND superuser_id = $2`,
		s.app.storeOf(ctx), id).Scan(&role, &vendorID)
	return role, vendorID, err
}
```

`Create(ctx, email, password, role)`: if a superuser with that email exists, add a `store_members` row for this store (refuse with `ErrConflict` if already a member); otherwise insert the person and the membership in one transaction. Role changes, vendor links, listing the team (`List`), and removal (`Delete` removes the membership; the person row is deleted only when no membership remains) all go through `store_members` filtered by `store_id = $1`. Every `RoleOwner` count ("last owner" guards) counts `store_members` for the bound store.

- [ ] **Step 4: Run the tests and the core suite**

Run: `go test ./core -run 'TestAFreshInstall|TestAnOperatorsRole' -count=1`
Expected: PASS.
Run: `go test ./core -count=1 -timeout 60m > $SCRATCH/t4.txt 2>&1; grep -E '^(--- FAIL|FAIL|ok)' $SCRATCH/t4.txt`
Expected: `ok`. Failures will be superuser/role tests reading the dropped columns; fix the code (not the tests) until green.

- [ ] **Step 5: Commit**

```bash
git add core
git commit -m "Add stores and per-store membership; store #1 is the existing install" -m "Update: skip" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 5: Scope every core table to its store

**Files:**
- Create: `core/scope.go`
- Modify: `core/schema.go` (append `{ID: "0054_store_scope", Run: migrate0054StoreScope}`)
- Test: `core/tenancy_test.go`, `core/upgrade_test.go` (create), `core/testdata/upgrade_0052.sql` (create)

**Interfaces:**
- Consumes: store #1 in `stores` (Task 4)
- Produces:
  - `func ScopeTables(ctx context.Context, tx *sql.Tx, tables ...string) error` (exported; modules call it)
  - `var globalUniqueKeys = map[string][]string{"carts": {"token"}, "api_keys": {"prefix"}, "outbox_events": {"event_id"}}`
  - Every core store table: column `store_id` with default `NULLIF(current_setting('gocommerce.store_id', true), '')`, FK to `stores` cascade, `UNIQUE (store_id, id)` where the table has `id`, policy `store_scope`, RLS enabled and forced.

`ScopeTables` algorithm, per table, in this order (the order matters — each step depends on the previous):

1. `ALTER TABLE t ADD COLUMN store_id text NOT NULL DEFAULT '<store #1 id>'` — a constant default is a metadata-only change on PostgreSQL 11+ and fills every existing row without a rewrite. Then `ALTER COLUMN store_id SET DEFAULT NULLIF(current_setting('gocommerce.store_id', true), '')`, `ADD FOREIGN KEY (store_id) REFERENCES stores (id) ON DELETE CASCADE`. Store #1's id: `SELECT id FROM stores ORDER BY created_at, id LIMIT 1`. If `stores` is empty (a module table on a database with no store yet cannot happen after 0053), return an error.
2. If the table has an `id` column: `ADD CONSTRAINT t_store_id_key UNIQUE (store_id, id)`.
3. Every PRIMARY KEY / UNIQUE constraint and every unique index (from `pg_constraint` / `pg_index` where `indisunique AND NOT indisprimary` and not backing a constraint) whose columns do not already start with `store_id`, and which is not the bare `(id)` primary key and not in `globalUniqueKeys`: recreate with `store_id` prepended. For constraints: `ALTER TABLE t DROP CONSTRAINT c, ADD CONSTRAINT c PRIMARY KEY|UNIQUE [NULLS NOT DISTINCT] (store_id, cols…)` — read `pg_get_constraintdef(oid)` and splice `store_id, ` after the first `(`. For unique indexes: read `pg_get_indexdef(indexrelid)`, splice `store_id, ` after the first `(` following ` USING btree `, `DROP INDEX`, run the edited definition. Expression indexes like `discounts_code_key ON discounts (lower(code)) WHERE …` splice the same way: `(store_id, lower(code))`. Any unique key referenced by a foreign key is dropped with `CASCADE` only after step 4 has captured that foreign key's definition.
4. Before step 3, capture every foreign key **from** a table in the set (or from an already-scoped table) **to** a table in the set or already scoped (has a `store_scope` policy) — `pg_get_constraintdef` gives `FOREIGN KEY (a, b) REFERENCES p(x, y) ON DELETE …`. After step 3, drop each and recreate it as `FOREIGN KEY (store_id, a, b) REFERENCES p(store_id, x, y) …` with the same actions. One exception: `ON DELETE SET NULL` on a composite key would null `store_id` too, so for those use PostgreSQL 15's column list: `ON DELETE SET NULL (a, b)`. Foreign keys to global tables (`superusers`, `stores`) are left alone.
5. `ENABLE` and `FORCE ROW LEVEL SECURITY`; `CREATE POLICY store_scope ON t USING (store_id = current_setting('gocommerce.store_id', true)) WITH CHECK (store_id = current_setting('gocommerce.store_id', true))`.
6. `CREATE INDEX t_store_idx ON t (store_id)` when no index leads with `store_id` after step 3.

Steps 1, 2, 5 and 6 run per table; steps 4 and 3 run across the whole set (capture all FKs first, rewrite all keys, then recreate all FKs), so a migration scoping several related tables does them together.

`store_profile` is special-cased in `migrate0054StoreScope` before `ScopeTables` runs on it: `ALTER TABLE store_profile DROP CONSTRAINT store_profile_id_check, DROP CONSTRAINT store_profile_pkey` then, after `ScopeTables`, `ADD PRIMARY KEY (store_id)`; the `id` column stays (shipped migrations reference it) with its default `1`, and code stops reading it (Task 10). Look up the actual CHECK constraint name with `SELECT conname FROM pg_constraint WHERE conrelid = 'store_profile'::regclass AND contype = 'c'`.

After all tables: composite FKs for the membership table — `ALTER TABLE store_members ADD FOREIGN KEY (store_id, role) REFERENCES roles (store_id, key) ON DELETE RESTRICT, ADD FOREIGN KEY (store_id, vendor_id) REFERENCES vendors (store_id, id) ON DELETE CASCADE` — and the order counter: `INSERT INTO store_counters (store_id, name, value) SELECT store_id, 'order', COALESCE(max(id), 0) FROM orders GROUP BY store_id` plus a row with `0` for a store with no orders.

The table list for `migrate0054StoreScope` is every `CREATE TABLE` in `core/schema.go` minus `superusers`, `superuser_sessions`, `superuser_password_resets`:

```go
var coreStoreTables = []string{
	"products", "product_options", "product_option_values", "variants", "variant_option_values",
	"carts", "cart_line_items", "orders", "order_lines", "idempotency_keys", "outbox_events",
	"fulfillments", "collections", "product_collections", "media", "product_media", "categories",
	"taxonomy_attributes", "discounts", "discount_targets", "order_discounts", "tax_rates",
	"locations", "variant_stock", "superuser_invitations", "role_rights", "admin_audit",
	"fulfillment_lines", "order_refunds", "order_returns", "order_return_lines", "stock_movements",
	"shipping_zones", "shipping_rates", "customer_groups", "customer_group_members", "price_lists",
	"price_list_prices", "channels", "product_channels", "variant_media", "plugins",
	"notifications", "notification_templates", "role_profiles", "store_profile", "api_keys",
	"custom_reports", "vendors", "vendor_offers", "roles",
}
```

- [ ] **Step 1: Write the failing tests**

```go
// storeTables lists tables in the test schema that must be scoped: every
// table except the global ones. It reads the catalog, not a hand-kept list, so
// a table added later without a policy fails here.
func storeTables(t *testing.T, app *App) []string {
	t.Helper()
	global := map[string]bool{"stores": true, "store_domains": true, "store_members": true,
		"store_counters": true, "api_key_index": true, "superusers": true,
		"superuser_sessions": true, "superuser_password_resets": true, "gocommerce_migrations": true}
	rows, err := app.db.QueryContext(context.Background(),
		`SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		rows.Scan(&n)
		if !global[n] {
			out = append(out, n)
		}
	}
	return out
}

func TestEveryStoreTableIsForcedBehindTheStorePolicy(t *testing.T) {
	app := newTestApp(t)
	for _, tbl := range storeTables(t, app) {
		var rls, force bool
		var policies int
		if err := app.db.QueryRowContext(context.Background(), `
			SELECT c.relrowsecurity, c.relforcerowsecurity,
			       (SELECT count(*) FROM pg_policies p WHERE p.schemaname = current_schema()
			          AND p.tablename = c.relname AND p.policyname = 'store_scope')
			FROM pg_class c WHERE c.oid = $1::regclass`, tbl).Scan(&rls, &force, &policies); err != nil {
			t.Fatal(err)
		}
		if !rls || !force || policies != 1 {
			t.Errorf("%s: rls=%v force=%v store_scope policies=%d", tbl, rls, force, policies)
		}
	}
}

func TestAnUnboundConnectionSeesNoStoreRows(t *testing.T) {
	app := newTestApp(t)
	createTestProduct(t, app) // any existing helper that inserts a product + variant
	db, err := OpenDB(context.Background(), app.cfg.DBURL) // no fallback on this pool
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tbl := range storeTables(t, app) {
		var n int
		if err := db.QueryRowContext(context.Background(), `SELECT count(*) FROM `+tbl).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s: an unbound connection sees %d rows", tbl, n)
		}
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO collections (title, slug) VALUES ('x', 'x')`); err == nil {
		t.Error("an unbound insert succeeded")
	}
}

func TestKeysAreUniquePerStore(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	ctxA := WithStore(context.Background(), app.DefaultStore())
	ctxB := WithStore(context.Background(), b)
	for _, ctx := range []context.Context{ctxA, ctxB} {
		if _, err := app.db.ExecContext(ctx, `INSERT INTO collections (store_id, title, slug)
			VALUES (current_setting('gocommerce.store_id'), 'Sale', 'sale')`); err != nil {
			t.Fatalf("the same slug in two stores: %v", err)
		}
	}
	if _, err := app.db.ExecContext(ctxA, `INSERT INTO collections (store_id, title, slug)
		VALUES (current_setting('gocommerce.store_id'), 'Sale', 'sale')`); err == nil {
		t.Fatal("a duplicate slug inside one store was accepted")
	}
}

func TestARowCannotReferenceAnotherStoresRow(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	ctxA := WithStore(context.Background(), app.DefaultStore())
	var productA int64
	if err := app.db.QueryRowContext(ctxA, `INSERT INTO products (store_id, title, slug)
		VALUES (current_setting('gocommerce.store_id'), 'P', 'p') RETURNING id`).Scan(&productA); err != nil {
		t.Fatal(err)
	}
	ctxB := WithStore(context.Background(), b)
	_, err := app.db.ExecContext(ctxB, `INSERT INTO variants (store_id, product_id, sku, price_minor, currency)
		VALUES (current_setting('gocommerce.store_id'), $1, 'X', 100, 'USD')`, productA)
	if err == nil {
		t.Fatal("store B created a variant of store A's product")
	}
}
```

Add to `core/helpers_test.go`:

```go
// addTestStore creates another store directly, as Part 3's endpoint will,
// and returns its id.
func addTestStore(t *testing.T, app *App, slug string) string {
	t.Helper()
	id, err := NewStoreID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.ExecContext(context.Background(),
		`INSERT INTO stores (id, slug, name) VALUES ($1, $2, $2)`, id, slug); err != nil {
		t.Fatal(err)
	}
	return id
}
```

Adjust the `INSERT` column lists in these tests to the real `NOT NULL` columns of `collections`, `products` and `variants` in `core/schema.go` (check each `CREATE TABLE`); the point of each test is the store behaviour, not the columns.

The upgrade test, `core/upgrade_test.go`:

```go
func TestASingleStoreInstallUpgradesInPlace(t *testing.T) {
	dsn := requireDB(t)
	db, err := OpenDB(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Bring the schema to 0052, as a 1.2 install has it, and load fixtures.
	sets := []migrationSet{{Owner: coreMigrationOwner, Migrations: coreMigrations()[:52]}}
	if err := runMigrations(context.Background(), db, quietLogger(), sets); err != nil {
		t.Fatal(err)
	}
	fixture, _ := os.ReadFile("testdata/upgrade_0052.sql")
	if _, err := db.ExecContext(context.Background(), string(fixture)); err != nil {
		t.Fatal(err)
	}
	app, err := New(testConfig(dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	ctx := context.Background() // DefaultStore "first" binds it
	var orders, unscoped int
	app.db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE store_id <> $1) FROM orders`, app.DefaultStore()).Scan(&orders, &unscoped)
	if orders != 2 || unscoped != 0 {
		t.Fatalf("orders=%d, not in store #1=%d", orders, unscoped)
	}
	var number string
	app.db.QueryRowContext(ctx, `SELECT number FROM orders WHERE id = 7`).Scan(&number)
	if number != "GC-000007" {
		t.Fatalf("order 7 is now %q; ids and numbers must not change", number)
	}
	var role string
	app.db.QueryRowContext(ctx, `SELECT role FROM store_members m JOIN superusers s ON s.id = m.superuser_id
		WHERE s.email = 'owner@example.com'`).Scan(&role)
	if role != "owner" {
		t.Fatalf("the owner's membership role = %q", role)
	}
}
```

`testdata/upgrade_0052.sql` inserts, at schema 0052: a store profile row (`name` 'Old Shop'), one superuser `owner@example.com` with role `owner`, one product with one variant, two orders with ids 3 and 7 and numbers `GC-000003`, `GC-000007` (and their required columns), one API key row. Write it by reading the `CREATE TABLE`s for `store_profile`, `superusers`, `products`, `variants`, `orders`, `api_keys` and filling every `NOT NULL` column without a default.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./core -run 'TestEveryStoreTable|TestAnUnboundConnection|TestKeysAreUnique|TestARowCannot|TestASingleStoreInstall' -count=1`
Expected: FAIL — no `store_scope` policies; `store_id` column does not exist.

- [ ] **Step 3: Implement `core/scope.go` and `migrate0054StoreScope`** per the algorithm above. Keep each step a small function (`addStoreColumn`, `captureForeignKeys`, `rewriteUniqueKeys`, `restoreForeignKeys`, `enableStorePolicy`) so a failure names the table and the step: wrap errors as `fmt.Errorf("scope %s: %s: %w", table, step, err)`.

- [ ] **Step 4: Run the tests, then the whole suite**

Run: the Step 2 command. Expected: PASS (5 tests).
Run: `go test ./... -count=1 -timeout 60m > $SCRATCH/t5.txt 2>&1; grep -E '^(--- FAIL|FAIL|ok)' $SCRATCH/t5.txt`
Expected: all `ok`. The test apps bind to store #1 through `DefaultStore: "first"`, and the column default fills `store_id` on inserts, so existing tests keep passing before any query is rewritten. A failure here is a constraint `ScopeTables` rewrote wrongly — fix `scope.go`.

- [ ] **Step 5: Commit**

```bash
git add core
git commit -m "Scope every core table to its store, behind forced row security" -m "Update: skip" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Task 6: A lint that every statement on a store table names its store

**Files:**
- Create: `core/storelint_test.go`
- Create: `core/testdata/storelint_pending.txt`

**Interfaces:**
- Consumes: `coreStoreTables` (Task 5); module tables are discovered from `CREATE TABLE` literals in `ext/`
- Produces: `TestStatementsOnStoreTablesNameTheStore`, `TestDatabaseCallsTakeAContext`; the pending file, one `path:function` per line, which Tasks 7–11 and 16–17 shrink to empty.

Rule: inside one function body (or func literal), if any string literal matches `(?is)\b(from|join|update|into|delete\s+from)\s+(?:only\s+)?(<store table>)\b`, then at least one string literal in that same function must contain `store_id`. A function may opt out with a doc comment line `//storelint:global <reason>` (for code that legitimately spans stores: `ForEachStore`, `ScopeTables`, migrations, the doctor's counts). Package-level `const`/`var` SQL (migrations) is not checked.

- [ ] **Step 1: Write the test**

```go
package gocommerce

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var createTableRE = regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?([a-z_]+)`)

func lintStoreTables(t *testing.T) []string {
	t.Helper()
	tables := append([]string{}, coreStoreTables...)
	_ = filepath.WalkDir("../ext", func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, _ := os.ReadFile(p)
		for _, m := range createTableRE.FindAllStringSubmatch(string(b), -1) {
			tables = append(tables, m[1])
		}
		return nil
	})
	return tables
}

func lintFiles() []string {
	var files []string
	for _, root := range []string{".", "../ext"} {
		_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() && strings.HasSuffix(p, ".go") && !strings.HasSuffix(p, "_test.go") {
				files = append(files, filepath.ToSlash(p))
			}
			return nil
		})
	}
	return files
}

func funcName(fn ast.Node) string {
	if fd, ok := fn.(*ast.FuncDecl); ok {
		if fd.Recv != nil && len(fd.Recv.List) == 1 {
			var b strings.Builder
			ast.Fprint(&b, nil, fd.Recv.List[0].Type, nil)
			if se, ok := fd.Recv.List[0].Type.(*ast.StarExpr); ok {
				if id, ok := se.X.(*ast.Ident); ok {
					return id.Name + "." + fd.Name.Name
				}
			}
			if id, ok := fd.Recv.List[0].Type.(*ast.Ident); ok {
				return id.Name + "." + fd.Name.Name
			}
		}
		return fd.Name.Name
	}
	return "func"
}

func TestStatementsOnStoreTablesNameTheStore(t *testing.T) {
	tables := lintStoreTables(t)
	dml := regexp.MustCompile(`(?is)\b(from|join|update|into|delete\s+from)\s+(?:only\s+)?(` + strings.Join(tables, "|") + `)\b`)
	pending := map[string]bool{}
	if f, err := os.Open("testdata/storelint_pending.txt"); err == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			if line := strings.TrimSpace(s.Text()); line != "" {
				pending[line] = true
			}
		}
		f.Close()
	}
	var found []string
	for _, path := range lintFiles() {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			if fd.Doc != nil && strings.Contains(fd.Doc.Text(), "storelint:global") {
				continue
			}
			var hit, named bool
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					if dml.MatchString(lit.Value) {
						hit = true
					}
					if strings.Contains(lit.Value, "store_id") {
						named = true
					}
				}
				return true
			})
			if hit && !named {
				found = append(found, path+":"+funcName(fd))
			}
		}
	}
	sort.Strings(found)
	if os.Getenv("STORELINT_WRITE") == "1" {
		_ = os.WriteFile("testdata/storelint_pending.txt", []byte(strings.Join(found, "\n")+"\n"), 0o644)
		return
	}
	for _, f := range found {
		if !pending[f] {
			t.Errorf("%s runs SQL on a store table without naming store_id", f)
		}
	}
	for f := range pending {
		if !contains(found, f) {
			t.Errorf("%s is listed as pending but is clean now: remove it from testdata/storelint_pending.txt", f)
		}
	}
}

var contextFreeCall = regexp.MustCompile(`\.(Query|QueryRow|Exec|Begin|Prepare)\(`)

func TestDatabaseCallsTakeAContext(t *testing.T) {
	for _, path := range lintFiles() {
		b, _ := os.ReadFile(path)
		for i, line := range strings.Split(string(b), "\n") {
			if contextFreeCall.MatchString(line) && !strings.Contains(line, "URL.Query()") && !strings.Contains(line, "r.URL") {
				t.Errorf("%s:%d: a database call without a context runs unbound: %s", path, i+1, strings.TrimSpace(line))
			}
		}
	}
}
```

(`contains` already exists in core; if not, add a three-line helper in the test file. Remove the leftover `ast.Fprint` lines in `funcName` if `go vet` complains — the two type switches are what produce the name.)

- [ ] **Step 2: Generate the pending list and see the second test's findings**

Run: `$env:STORELINT_WRITE='1'; go test ./core -run TestStatementsOnStoreTablesNameTheStore -count=1; Remove-Item Env:STORELINT_WRITE; (Get-Content core/testdata/storelint_pending.txt).Count`
Expected: a few hundred lines.
Run: `go test ./core -run 'TestStatementsOnStoreTables|TestDatabaseCallsTakeAContext' -count=1`
Expected: the first PASSes against its pending list; the second lists any context-free calls. Fix each by switching to the `…Context` method with the caller's `ctx` (none are expected; the earlier survey found none outside `r.URL.Query()`).

- [ ] **Step 3: Mark the functions that legitimately span stores**

Add `//storelint:global <reason>` to the doc comment of `ScopeTables` and its helpers, `migrate0053Stores`, `migrate0054StoreScope`, and the `Stores` service's methods (global tables only — they will not match anyway). Regenerate the pending list.

- [ ] **Step 4: Run both tests**

Run: `go test ./core -run 'TestStatementsOnStoreTables|TestDatabaseCallsTakeAContext' -count=1`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add core/storelint_test.go core/testdata/storelint_pending.txt core
git commit -m "Lint that every statement on a store table names its store" -m "Update: skip" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

---

### Tasks 7–11: Name the store in every core statement

These five tasks share one procedure and differ in their file set. Each is one commit. The rules:

- **Reads** (`SELECT … FROM t`): add `t.store_id = $n` to the `WHERE` (for joins, filter the driving table and add `AND x.store_id = t.store_id` to each join condition on a store table).
- **Updates / deletes**: add `AND store_id = $n` to the `WHERE`.
- **Inserts**: add `store_id` to the column list and `$n` to `VALUES` (for `INSERT … SELECT`, select the bound store as a literal parameter).
- **Upserts**: an `ON CONFLICT (a, b)` target must match the rewritten key, so it becomes `ON CONFLICT (store_id, a, b)`; the `DO UPDATE` needs no store filter (the conflicting row is in the same store by construction).
- `$n` is a new last parameter whose value is `s.app.storeOf(ctx)` (or `a.storeOf(ctx)` on `*App`). Never interpolate it into SQL.
- `WHERE id = 1` on `store_profile` becomes `WHERE store_id = $n`.

Before (core/api_keys.go:292):

```go
_, _ = k.app.db.ExecContext(bg, `UPDATE api_keys SET last_used_at = now() WHERE id = $1`, id)
```

After:

```go
_, _ = k.app.db.ExecContext(bg, `UPDATE api_keys SET last_used_at = now() WHERE id = $1 AND store_id = $2`, id, k.app.storeOf(bg))
```

Procedure per task:

- [ ] **Step 1:** Delete this task's files' lines from `core/testdata/storelint_pending.txt`.
- [ ] **Step 2:** Run `go test ./core -run TestStatementsOnStoreTablesNameTheStore -count=1`. Expected: FAIL listing exactly this task's functions.
- [ ] **Step 3:** Rewrite each listed function's SQL by the rules above.
- [ ] **Step 4:** Run `go test ./core -run TestStatementsOnStoreTablesNameTheStore -count=1` (expected PASS), then `go test ./core -count=1 -timeout 60m > $SCRATCH/tN.txt 2>&1; grep -E '^(--- FAIL|FAIL|ok)' $SCRATCH/tN.txt` (expected `ok`).
- [ ] **Step 5:** Commit with the subject given in the task, `-m "Update: skip"` and the co-author line.

### Task 7: Catalogue statements

Files: `core/catalog.go`, `core/catalog_http.go`, `core/options.go`, `core/collections.go`, `core/categories.go`, `core/taxonomy.go`, `core/taxonomy_attributes.go`, `core/taxonomy_http.go`, `core/media.go`, `core/media_http.go`, `core/channels.go`, `core/channels_http.go`, `core/dimensions.go`, `core/sorting.go`, `core/screens.go`.
Subject: `Name the store in every catalogue statement`.
Extra test (add to `core/tenancy_test.go`, run in Step 2 — it fails before Step 3 because `CreateProduct` today ignores the store):

```go
func TestTheSameSKUCanBeSoldByTwoStores(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	ctxB := WithStore(context.Background(), b)
	if _, err := app.Catalog().CreateProduct(context.Background(), sampleProduct("SKU-1")); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Catalog().CreateProduct(ctxB, sampleProduct("SKU-1")); err != nil {
		t.Fatalf("store B could not use a SKU store A uses: %v", err)
	}
}
```

(`sampleProduct` — use the input the existing catalog tests build; grep `CreateProduct(` in `core/catalog_test.go` and reuse its literal.) Note: this passes as soon as Task 5 lands if the service does not pre-check SKUs; if it already passes at Step 2, keep it as a guard and note it in the ledger.

### Task 8: Request → store resolution and membership

(Not a statement-rewrite task; it sits here because Tasks 9–11 need the HTTP path bound.)

**Files:**
- Create: `core/storeresolve.go`
- Modify: `core/gocommerce.go` (`Handler`: insert `h = a.storeMW(h)` between `a.fallbackJSON` and `a.demoGuardMW`), `core/httpx.go` (`bearerAuth` answers 403 for a session with no membership here), `core/api_keys.go` (`Create` writes `api_key_index` in its transaction; `Resolve` reads the store from the index)
- Test: `core/storeresolve_test.go`

**Interfaces:**
- Consumes: `Stores` (Task 4), `WithStore`, `ValidStoreID`
- Produces: `func (a *App) storeMW(next http.Handler) http.Handler`; `var storeExempt = []string{...}`; errors `ErrStoreRequired` (400, code `store_required`), `ErrStoreNotFound` (404, code `store_not_found`), `ErrStoreSuspended` (503, code `store_unavailable`), `ErrNotAMember` (403, code `not_a_member`).

Resolution, first match wins (spec's order): API key → `x-litekart-store` / `X-Store` → `?store=` → cookie `litekart_store_id` → host (`store_domains`, then `<slug>.<BaseDomain>`) → the signed-in person's only store (session token → `superuser_sessions` → `store_members`; exactly one row) → the install's only active store (`Stores.Count == 1`). Then: if the app has a `DefaultStore` and nothing matched, use it. Nothing matched → `ErrStoreRequired`. A value that is a valid id or a slug but names no store → `ErrStoreNotFound`. Suspended → `ErrStoreSuspended`. An API key whose index store ≠ an explicitly named store → `ErrForbidden`.

`storeExempt` (path prefixes): `/health`, `/doc`, `/docs`, `/_/` (panel static files; check the panel's mount prefix in `core/admin_http.go`), `/api/admin/auth-with-password`, `/api/admin/auth-refresh` (if present), `/api/admin/request-password-reset`, `/api/admin/confirm-password-reset`, `/api/stores/` (Part 3's routes). Grep `mountSuperuserRoutes` for the actual sign-in/reset paths and list exactly those.

Membership is checked by `bearerAuth` (it already resolves the session): `Superusers.Resolve` returns `ok=false` with a sentinel `errNotMember` when the person exists but has no `store_members` row for the bound store; `bearerAuth` answers `ErrNotAMember` (403) for that, `ErrUnauthorized` (401) otherwise. Static admin tokens are install-wide and pass for any store.

- [ ] **Step 1: Write the failing table test**

```go
func TestARequestFindsItsStore(t *testing.T) {
	app := newStrictTestApp(t) // DefaultStore "" — as `serve` runs
	one := firstStore(t, app)
	two := addTestStore(t, app, "two")
	slugOne := storeSlug(t, app, one)
	cases := []struct {
		name   string
		set    func(*http.Request)
		status int
	}{
		{"header id", func(r *http.Request) { r.Header.Set("x-litekart-store", two) }, 200},
		{"X-Store slug", func(r *http.Request) { r.Header.Set("X-Store", slugOne) }, 200},
		{"query", func(r *http.Request) { r.URL.RawQuery = "store=" + two }, 200},
		{"cookie", func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "litekart_store_id", Value: two}) }, 200},
		{"nothing, two stores", func(r *http.Request) {}, 400},
		{"unknown id", func(r *http.Request) { r.Header.Set("x-litekart-store", "store_01JA2Z3K4M5N6P7Q8R9S0T1V2W") }, 404},
		{"garbage", func(r *http.Request) { r.Header.Set("x-litekart-store", "'; drop table stores;--") }, 404},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, app, http.MethodGet, "/api/admin/products", withAdmin, c.set)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
		})
	}
}

func TestAnInstallWithOneStoreNeedsNoHeader(t *testing.T) {
	app := newStrictTestApp(t)
	if rec := do(t, app, http.MethodGet, "/api/admin/products", withAdmin); rec.Code != 200 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}

func TestStaffOfOneStoreCannotOpenAnother(t *testing.T) {
	app := newStrictTestApp(t)
	two := addTestStore(t, app, "two")
	token := operatorSession(t, app, "staff@example.com", RoleStaff) // member of store #1 only
	rec := do(t, app, http.MethodGet, "/api/admin/products", withSession(token), func(r *http.Request) {
		r.Header.Set("x-litekart-store", two)
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	// With no store named, a person in exactly one store lands in it.
	if rec := do(t, app, http.MethodGet, "/api/admin/products", withSession(token)); rec.Code != 200 {
		t.Fatalf("sole-membership status = %d", rec.Code)
	}
}

func TestAnAPIKeyCannotBeAimedAtAnotherStore(t *testing.T) {
	app := newStrictTestApp(t)
	two := addTestStore(t, app, "two")
	key := createAPIKey(t, app, firstStore(t, app), "owner")
	rec := do(t, app, http.MethodGet, "/api/admin/products", withBearer(key), func(r *http.Request) {
		r.Header.Set("x-litekart-store", two)
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}
```

Helpers to add to `core/helpers_test.go`: `newStrictTestApp` (as `newTestApp` with `DefaultStore: ""`), `firstStore`, `storeSlug`, `operatorSession` (wrap the existing helper that signs a superuser in — grep `auth-with-password` in `core/*_test.go`), `withSession`, `withBearer`, `createAPIKey` (calls `app.APIKeys().Create` with `WithStore(ctx, store)`).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./core -run 'TestARequestFindsItsStore|TestAnInstallWithOneStore|TestStaffOfOneStore|TestAnAPIKeyCannot' -count=1`
Expected: FAIL — `newStrictTestApp` apps return 200 for "nothing, two stores" (no middleware) and the 403 cases return 200.

- [ ] **Step 3: Implement `storeMW` and the auth/API-key changes** as specified. The middleware writes the resolved store into the request context with `WithStore` and, for responses, nothing else (no cookie: setting the cookie is Part 3's switch endpoint).

- [ ] **Step 4: Run the tests, then the suite**

Run: the Step 2 command. Expected: PASS.
Run: `go test ./core -count=1 -timeout 60m` → `ok`.

- [ ] **Step 5: Commit** — subject `Find each request's store and refuse staff who do not belong to it`.

### Task 9: Commerce statements

Files: `core/cart.go`, `core/carts_http.go`, `core/checkout.go`, `core/commerce_http.go`, `core/orders.go`, `core/payments.go`, `core/fulfillment.go`, `core/returns.go`, `core/discounts.go`, `core/discounts_http.go`, `core/pricing.go`, `core/pricing_http.go`, `core/taxes.go`, `core/taxes_http.go`, `core/shipping.go`, `core/shipping_http.go`, `core/customers.go`, `core/carriers.go`.
Subject: `Name the store in every cart, checkout and order statement; number orders per store`.

Also in this task — per-store order numbers, replacing `core/checkout.go:409-412`:

```go
		var seq int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO store_counters (store_id, name, value) VALUES ($1, 'order', 1)
			ON CONFLICT (store_id, name) DO UPDATE SET value = store_counters.value + 1
			RETURNING value`, s.app.storeOf(ctx)).Scan(&seq); err != nil {
			return err
		}
		var prefix sql.NullString
		if err := tx.QueryRowContext(ctx, `SELECT order_prefix FROM stores WHERE id = $1`,
			s.app.storeOf(ctx)).Scan(&prefix); err != nil {
			return err
		}
		if !prefix.Valid {
			prefix.String = s.app.cfg.OrderPrefix
		}
		number := fmt.Sprintf("%s%06d", prefix.String, seq)
		if err := tx.QueryRowContext(ctx,
			`SELECT nextval(pg_get_serial_sequence('orders', 'id'))`).Scan(&orderID); err != nil {
			return err
		}
```

Extra test (Step 2 RED):

```go
func TestEachStoreNumbersItsOwnOrders(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	a1 := placeTestOrder(t, app, context.Background())
	b1 := placeTestOrder(t, app, WithStore(context.Background(), b))
	a2 := placeTestOrder(t, app, context.Background())
	if a1.Number != "GC-000001" || b1.Number != "GC-000001" || a2.Number != "GC-000002" {
		t.Fatalf("numbers = %s, %s, %s", a1.Number, b1.Number, a2.Number)
	}
}
```

`placeTestOrder(t, app, ctx)`: build from the existing checkout test helper (grep `func.*placeOrder\|func buy` in `core/*_test.go`), creating its own product in `ctx`'s store first and passing `ctx` through every call. The upgrade test (Task 5) already proves an upgraded store continues after its highest id.

### Task 10: Stock, sellers and reports statements

Files: `core/inventory.go`, `core/locations.go`, `core/locations_http.go`, `core/movements.go`, `core/vendors.go`, `core/vendors_http.go`, `core/vendor_accounts.go`, `core/vendor_scope.go`, `core/reports.go`, `core/reports_http.go`, `core/reports_payouts.go`.
Subject: `Name the store in every stock, seller and report statement`.

### Task 11: Operations statements

Files: `core/outbox.go`, `core/events.go`, `core/events_http.go`, `core/audit.go`, `core/audit_http.go`, `core/notifications.go`, `core/notifications_http.go`, `core/notify.go`, `core/notify_templates.go`, `core/notify_templates_http.go`, `core/plugins.go`, `core/plugins_http.go`, `core/plugins_builtin.go`, `core/settings.go`, `core/settings_http.go`, `core/store_profile.go`, `core/api_keys.go`, `core/invitations.go`, `core/invitations_http.go`, `core/roles.go`, `core/roles_http.go`, `core/rights.go`, `core/role_profiles.go`, `core/superusers.go`, `core/superusers_http.go`, `core/passwordreset.go`, `core/custom_reports.go`, `core/doctor.go`, `core/demo.go`, `core/ops_http.go`, `core/transfer*.go`, `core/csv.go`, and any file still in the pending list for `core/`.
Subject: `Name the store in every operations statement`.
At the end of this task, `core/testdata/storelint_pending.txt` contains no `core/` line (only `../ext/` lines remain). Add to Step 4: `Select-String -Path core/testdata/storelint_pending.txt -Pattern '^[^.]'` prints nothing.

New stores need their built-in roles: `roles` rows for `owner`, `manager`, `staff`, `vendor` were inserted by migration 0052 for store #1 only. Add `func (s *Stores) seed(ctx context.Context, storeID string) error` (bound to `storeID`) that inserts the four built-in roles and a `store_profile` row; `addTestStore` calls it. Part 3's create endpoint will too.

---

### Task 12: Background work runs one store at a time

**Files:**
- Create: `core/foreachstore.go`
- Modify: `core/gocommerce.go` (`runSweepers`), `core/outbox.go` (`run`), `core/api_keys.go` (`touch` — already bound via `context.WithoutCancel(ctx)`, which keeps values; confirm), `core/superusers.go` (reset email goroutine: same check)
- Test: `core/foreachstore_test.go`

**Interfaces:**
- Produces: `func (a *App) ForEachStore(ctx context.Context, fn func(ctx context.Context) error) error` — calls `fn` with `WithStore(ctx, id)` for each active store, oldest first; logs and continues past one store's error, returns the first error joined with `errors.Join`.

```go
// ForEachStore runs fn once per active store, bound to it. Background work
// goes through here because under `serve` a context with no store sees no
// rows: a sweep that forgot to name its store would do nothing, silently.
//
//storelint:global walks the global store list
func (a *App) ForEachStore(ctx context.Context, fn func(ctx context.Context) error) error {
	stores, err := a.Stores().Active(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, s := range stores {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := fn(WithStore(ctx, s.ID)); err != nil {
			a.log.Error("store job failed", "store", s.ID, "error", err)
			errs = append(errs, fmt.Errorf("%s: %w", s.ID, err))
		}
	}
	return errors.Join(errs...)
}
```

`runSweepers`' `sweep` body becomes `_ = a.ForEachStore(ctx, func(ctx context.Context) error { …existing four calls… ; return nil })`. `outbox.run`: each pass is `o.app.ForEachStore(ctx, func(ctx context.Context) error { for { n, err := o.deliverBatch(ctx); if err != nil || n == 0 { return err } } })` — give `outbox` an `app *App` field. Delivery inherits the bound context, so event handlers run in the event's store.

- [ ] **Step 1: Write the failing tests**

```go
func TestSweepsReachEveryStoreUnderServe(t *testing.T) {
	app := newStrictTestApp(t)
	b := addTestStore(t, app, "second")
	expireCartIn(t, app, WithStore(context.Background(), firstStore(t, app)))
	expireCartIn(t, app, WithStore(context.Background(), b))
	app.runSweepOnce(context.Background())
	for _, s := range []string{firstStore(t, app), b} {
		if n := countCarts(t, app, WithStore(context.Background(), s)); n != 0 {
			t.Errorf("store %s still has %d expired carts", s, n)
		}
	}
}

func TestAnEventIsDeliveredInItsOwnStore(t *testing.T) {
	app := newStrictTestApp(t)
	b := addTestStore(t, app, "second")
	got := make(chan string, 1)
	app.Subscribe("order.paid", func(ctx context.Context, e Event) error {
		s, _ := StoreFrom(ctx)
		got <- s
		return nil
	})
	payTestOrder(t, app, WithStore(context.Background(), b))
	app.deliverOutboxOnce(context.Background())
	if s := <-got; s != b {
		t.Fatalf("handler ran bound to %q, want %q", s, b)
	}
}

func TestUnboundWorkCannotWriteUnderServe(t *testing.T) {
	app := newStrictTestApp(t)
	if _, err := app.db.ExecContext(context.Background(),
		`INSERT INTO collections (title, slug) VALUES ('x', 'x')`); err == nil {
		t.Fatal("an unbound write landed somewhere")
	}
}
```

Extract the current `sweep` closure into `func (a *App) runSweepOnce(ctx context.Context)` and the outbox pass into `func (a *App) deliverOutboxOnce(ctx context.Context)` so tests call them without timers. `expireCartIn`, `countCarts`, `payTestOrder`: build from the existing cart-sweep and payment tests (grep `SweepExpired` and `MarkPaid` in `core/*_test.go`), passing the given ctx. Check the subscribe API's real name (`grep -n "func (a \*App) Subscribe\|func (a \*App) On(" core/*.go`).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./core -run 'TestSweepsReachEveryStore|TestAnEventIsDelivered|TestUnboundWork' -count=1`
Expected: FAIL — under the strict app, sweeps see no rows and the event never arrives (the third test passes already after Task 5; keep it as a guard).

- [ ] **Step 3: Implement** as above.
- [ ] **Step 4: Run them, then the core suite.** Expected: PASS; `ok`.
- [ ] **Step 5: Commit** — subject `Run sweeps and event delivery once per store`.

---

### Task 13: Custom reports are off until they have a store-safe login

**Files:**
- Modify: `core/custom_reports.go` / `core/reports_http.go` (the run endpoint), `core/doctor.go` (don't fail on the reports pool)
- Test: `core/custom_reports_test.go` (replace the run tests with one 503 test; keep the CRUD tests if they pass)

A report runs arbitrary SQL that could call `set_config` and read any store; until Part 4 gives each store its own reporting login, the run endpoint answers 503.

- [ ] **Step 1: Write the failing test**

```go
func TestCustomReportsAreOffUntilEachStoreHasItsOwnLogin(t *testing.T) {
	app := newTestApp(t)
	id := createTestReport(t, app, "SELECT 1")
	rec := do(t, app, http.MethodPost, fmt.Sprintf("/api/admin/custom-reports/%d/run", id), withAdmin)
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "reports_unavailable") {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
}
```

(Use the real run route: `grep -n "custom-reports" core/*.go`.)

- [ ] **Step 2: Run to verify failure.** Expected: 200.
- [ ] **Step 3: Implement** — the handler returns `RespondError(w, r, &Error{Status: 503, Code: "reports_unavailable", Message: "Custom SQL reports are switched off while stores share one database; they return once each store has its own reporting login."})` (match the engine's error type — grep `ErrUnauthorized =` for its shape). Delete the run tests that exercised execution; keep `withTestReports` helpers only if still used. Leave `OpenReportsDB` in place for Part 4.
- [ ] **Step 4: Run the suite.** Expected: `ok`.
- [ ] **Step 5: Commit** — subject `Switch custom SQL reports off until each store has its own login`.

---

### Task 14: One shape: remove platform mode

**Files:**
- Delete: `platform/` (whole package), `docker-compose.platform.yml`
- Modify: `cmd/gocommerce/main.go` (drop the `platform` command, its flags and usage text; move `-base-domain` / `$GOCOMMERCE_BASE_DOMAIN` to `serve` as `Config.BaseDomain`; `serve` sets `DefaultStore: ""`), `docker-compose.yml`, `Dockerfile` if it references platform, `README.md`, `docs/`, `skills/` mentions of platform mode, `scripts/` (grep `platform`)
- Test: `cmd/gocommerce` builds; `scripts/check-docs.ps1` passes

- [ ] **Step 1:** `git grep -n -i "platform" -- ':!PLAN.md' ':!docs/superpowers' ':!admin/build'` and list every hit in the ledger.
- [ ] **Step 2:** Delete the package and compose file; run `go build ./... ; go vet ./...`. Expected: FAIL in `cmd/gocommerce` (import of `platform`).
- [ ] **Step 3:** Remove the command and flags; move `-base-domain` to `serve`. Remove or rewrite each doc hit from Step 1: docs describe one engine serving many stores, resolved by header, cookie or host. The admin panel's `routes/platform` console (D73) is removed too: delete `admin/src/routes/platform/` and its sidebar link, then `.\scripts\build.ps1`.
- [ ] **Step 4:** Run `go build ./... ; go vet ./... ; go build -tags no_admin ./... ; .\scripts\check-docs.ps1`. Expected: all succeed.
- [ ] **Step 5: Commit** — subject `Serve every store from one engine; remove the separate platform mode`.

---

### Task 15: Doctor, Compose and first run

**Files:**
- Modify: `core/doctor.go` (new diagnostic `row security`), `docker-compose.yml` (an init script creating the engine login), create `scripts/db-init/01-engine-login.sql`, `cmd/gocommerce/main.go` (`bootstrapSuperuser` runs bound to the first store)
- Test: `core/doctor_test.go`

- [ ] **Step 1: Write the failing test**

```go
func TestDoctorFailsWhenTheEngineCanBypassRowSecurity(t *testing.T) {
	app := newTestApp(t)
	d := app.diagnoseRowSecurity(context.Background(), func(ctx context.Context) (bool, bool, error) {
		return true, false, nil // rolsuper, rolbypassrls
	})
	if d.OK || !strings.Contains(d.Detail, "superuser") {
		t.Fatalf("diagnostic = %+v", d)
	}
	d = app.diagnoseRowSecurity(context.Background(), nil) // the real login
	if !d.OK {
		t.Fatalf("the test login fails the check: %+v", d)
	}
}
```

- [ ] **Step 2: Run to verify failure.** Expected: `undefined: diagnoseRowSecurity`.
- [ ] **Step 3: Implement.** The diagnostic reads `SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user` (the injected func replaces this query in the test), and fails with `Detail: "the database login is a superuser or has BYPASSRLS, so stores are not isolated; connect as an ordinary login (see docker-compose.yml)"`. Add it to `Diagnose`'s list after `migrations`. `ListenAndServe` logs the same failure as an error at start, without refusing to start (an operator must be able to run `doctor` and read why).

`scripts/db-init/01-engine-login.sql` (mounted at `/docker-entrypoint-initdb.d/`, runs once on an empty volume as the Postgres superuser):

```sql
-- The engine's own login. It owns the tables it creates but is not a
-- superuser, because PostgreSQL lets superusers past Row-Level Security and
-- that policy is what keeps one store's rows out of another's.
CREATE ROLE gocommerce_app LOGIN PASSWORD 'gocommerce_app' NOSUPERUSER NOBYPASSRLS;
GRANT ALL ON DATABASE gocommerce TO gocommerce_app;
GRANT ALL ON SCHEMA public TO gocommerce_app;
```

`docker-compose.yml`: mount `./scripts/db-init:/docker-entrypoint-initdb.d:ro` on the database; the app's `DATABASE_URL` uses `gocommerce_app:${GOCOMMERCE_APP_DB_PASSWORD:-gocommerce_app}`. The password comes from an environment variable with that default; document it next to the other variables in `README.md`. An existing volume does not rerun init scripts — the README upgrade note says to run the SQL file once by hand (`docker compose exec db psql -U gocommerce -f /docker-entrypoint-initdb.d/01-engine-login.sql`) and then `ALTER TABLE … OWNER TO gocommerce_app` for every table: give that as a one-line `DO` block in the note.

`bootstrapSuperuser` creates the first operator with `gocommerce.WithStore(ctx, firstStoreID)` (read it with `app.Stores().Active`), so the membership lands in store #1.

- [ ] **Step 4:** `go test ./core -run TestDoctorFails -count=1` → PASS; `go build ./cmd/gocommerce`.
- [ ] **Step 5: Commit** — subject `Check that the engine cannot bypass row security, and give Compose its own login`.

---

### Task 16: Scope the module tables (commerce modules)

**Files:**
- Modify (each module's `Migrations()` and statements): `ext/b2b`, `ext/cart-recovery`, `ext/cms`, `ext/contact`, `ext/faq`, `ext/identity`, `ext/invoices`, `ext/mcp`, `ext/navigation`, `ext/newsletter`, `ext/reviews`, `ext/translations`, `ext/webhooks`, `ext/wishlist`, and the table-less modules' statements on core tables they read (`ext/feeds`, `ext/sitemaps`, `ext/indexnow`, `ext/search-meilisearch` — reads only)
- Test: `cmd/gocommerce/tenancy_test.go` (create)

Each module appends one migration:

```go
		{ID: "0NNN_store_scope", Run: func(ctx context.Context, tx *sql.Tx) error {
			return gocommerce.ScopeTables(ctx, tx, "b2b_companies", "b2b_members", "b2b_invitations",
				"b2b_orders", "b2b_approvals", "b2b_quotes", "b2b_quote_lines",
				"b2b_partial_checkouts", "b2b_territories", "b2b_leads")
		}},
```

(`0NNN` = the module's next migration number.) Tables per module:

| Module | Tables |
|---|---|
| b2b | b2b_companies, b2b_members, b2b_invitations, b2b_orders, b2b_approvals, b2b_quotes, b2b_quote_lines, b2b_partial_checkouts, b2b_territories, b2b_leads |
| cart-recovery | cart_recovery_abandonments, cart_recovery_events, cart_recovery_settings |
| cms | cms_pages |
| contact | contact_messages |
| faq | faq_entries |
| identity | identity_customers, identity_addresses, identity_sessions, identity_email_verifications, identity_password_resets, identity_orders |
| invoices | invoices_counters, invoices_documents |
| mcp | mcp_audit |
| navigation | navigation_menus, navigation_items |
| newsletter | newsletter_subscriptions |
| reviews | reviews |
| translations | translations_entries |
| webhooks | webhook_endpoints, webhook_deliveries |
| wishlist | wishlists, wishlist_items |

`cart_recovery_settings` and `invoices_counters` are single-row/keyed settings: check their keys after scoping (they become per store through `ScopeTables`' key rewrite; a `CHECK (id = 1)` needs the same special case as `store_profile` — grep `CHECK (id = 1)` in each).

Module jobs (cart-recovery's sequence runner, webhooks' retries, import jobs) go through `app.ForEachStore`: grep each module for `time.NewTicker` / `OnStart` and wrap the pass.

Then rewrite each module's statements by the Tasks 7–11 rules, using `app.StoreOf(ctx)` — export `storeOf` as `func (a *App) StoreOf(ctx context.Context) string` in this task for modules (a one-line wrapper).

- [ ] **Step 1: Write the failing test**

```go
package main

import (
	"context"
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

// Every table any module creates is behind the store policy, with every
// module mounted as `serve` mounts them.
func TestEveryModuleTableIsScoped(t *testing.T) {
	app := gctest.New(t, buildModules("")...)
	rows, err := app.DB().QueryContext(context.Background(), `
		SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind = 'r'
		  AND c.relname NOT IN ('stores','store_domains','store_members','store_counters','api_key_index',
		                        'superusers','superuser_sessions','superuser_password_resets','gocommerce_migrations')
		  AND NOT (c.relrowsecurity AND c.relforcerowsecurity AND EXISTS (
		      SELECT 1 FROM pg_policies p WHERE p.schemaname = n.nspname AND p.tablename = c.relname AND p.policyname = 'store_scope'))`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		rows.Scan(&name)
		t.Errorf("%s is not scoped to a store", name)
	}
}
```

(`buildModules` may need env vars to mount provider modules; if it mounts only some, list the module constructors directly with zero configs the same way each module's own tests do. Check `gctest.New`'s config sets `DefaultStore: "first"` — add it in `gctest.NewWithConfig` if Task 3 did not.)

- [ ] **Step 2: Run to verify failure.** `go test ./cmd/gocommerce -run TestEveryModuleTableIsScoped -count=1` → FAIL listing the module tables.
- [ ] **Step 3:** Add the migrations; remove these modules' lines from `core/testdata/storelint_pending.txt`; rewrite statements until `go test ./core -run TestStatementsOnStoreTablesNameTheStore` passes for them.
- [ ] **Step 4:** `go test ./cmd/gocommerce ./ext/... -count=1 -timeout 60m > $SCRATCH/t16.txt 2>&1; grep -E '^(--- FAIL|FAIL|ok)' $SCRATCH/t16.txt` → all `ok` (provider modules still listed in the RLS test's failures is expected until Task 17 — run Step 1's test with `-run` scoped and confirm only provider tables remain).
- [ ] **Step 5: Commit** — subject `Scope the commerce modules' tables to their store`.

### Task 17: Scope the provider modules

**Files:** `ext/payments-*` (9 `*_events` tables), `ext/fulfill-usps` (`fulfill_usps_labels`), `ext/import-amazon` (`import_amazon_jobs`), `ext/import-shopify` (`import_shopify_jobs`), plus every `ext/fulfill-*`, `ext/notify-*`, `ext/klaviyo` statement still in the pending list.

Same procedure as Task 16. Provider webhooks: a payment provider calls the engine with no header; the merchant configures the webhook URL with `?store=<store id>` (resolution step 3). Each payments module's docs/README line showing its webhook URL gains `?store=<your store id>`; the panel's provider settings screen shows the full URL with the store id (find where the webhook URL is displayed: `grep -rn "webhook" admin/src/routes/dash/plugins`), then `.\scripts\build.ps1`.

Plugin on/off and plugin settings live in core's `plugins` table, which is already per store after Task 5; confirm with a test:

```go
func TestAPluginIsSwitchedOnPerStore(t *testing.T) {
	app := newTestApp(t, reviews.New(reviews.Config{}))
	b := addTestStore(t, app, "second")
	enablePlugin(t, app, context.Background(), "reviews")
	if pluginEnabled(t, app, WithStore(context.Background(), b), "reviews") {
		t.Fatal("switching reviews on in store #1 switched it on in store B")
	}
}
```

(place it in `ext/reviews` as `reviews_test.go` with `gctest` equivalents; `enablePlugin`/`pluginEnabled` call the core plugin service — grep `func (p \*Plugins) Enable`.)

- [ ] Steps 1–5 as Task 16. At the end `core/testdata/storelint_pending.txt` is empty: delete the file and make the lint test fail if the file exists (remove the pending mechanism's read; keep `STORELINT_WRITE` out).
- Commit subject: `Scope the provider modules' tables to their store`.

---

### Task 18: The two-store isolation suite

**Files:**
- Create: `core/isolation_test.go`, `ext/b2b/isolation_test.go`

This is the guard the spec's Testing section asks for: everything observable through the API with two stores.

- [ ] **Step 1: Write the tests**

```go
// twoStores returns a strict app (as `serve` runs) with stores A and B, each
// holding one of everything a merchant creates first.
func twoStores(t *testing.T) (app *App, a, b string) {
	t.Helper()
	app = newStrictTestApp(t)
	a = firstStore(t, app)
	b = addTestStore(t, app, "b")
	for _, s := range []string{a, b} {
		seedStore(t, app, WithStore(context.Background(), s)) // product+variant, collection, category, discount "SAVE10", location, customer group, shipping zone
	}
	return app, a, b
}

func TestStoreBCannotSeeStoreAThroughTheAdminAPI(t *testing.T) {
	app, a, b := twoStores(t)
	inA := map[string]string{ // list route → one id in A, read via store A
		"/api/admin/products":    firstID(t, app, a, "/api/admin/products"),
		"/api/admin/collections": firstID(t, app, a, "/api/admin/collections"),
		"/api/admin/categories":  firstID(t, app, a, "/api/admin/categories"),
		"/api/admin/discounts":   firstID(t, app, a, "/api/admin/discounts"),
		"/api/admin/orders":      firstID(t, app, a, "/api/admin/orders"),
	}
	for list, id := range inA {
		rec := do(t, app, http.MethodGet, list+"/"+id, withAdmin, inStore(b))
		if rec.Code != http.StatusNotFound {
			t.Errorf("GET %s/%s as B = %d, want 404", list, id, rec.Code)
		}
		for _, row := range listIDs(t, app, b, list) {
			if row == id {
				t.Errorf("%s in B lists A's %s", list, id)
			}
		}
		for _, m := range []string{http.MethodPatch, http.MethodDelete} {
			rec := do(t, app, m, list+"/"+id, withAdmin, inStore(b), jsonBody(`{}`))
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s %s/%s as B = %d, want 404", m, list, id, rec.Code)
			}
		}
	}
}

func TestStoreBCannotUseStoreAsThings(t *testing.T) {
	app, a, b := twoStores(t)
	variantA := firstVariantID(t, app, a)
	cart := newCart(t, app, b)
	rec := do(t, app, http.MethodPost, "/api/carts/"+cart+"/items", inStore(b),
		jsonBody(fmt.Sprintf(`{"variant_id": %s, "quantity": 1}`, variantA)))
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("adding A's variant to B's cart = %d", rec.Code)
	}
	// A's discount code does nothing in B (B has its own SAVE10 seeded, so use
	// a code only A has).
	addDiscountCode(t, app, a, "ONLYA")
	rec = do(t, app, http.MethodPost, "/api/carts/"+cart+"/discount", inStore(b), jsonBody(`{"code":"ONLYA"}`))
	if rec.Code < 400 || rec.Code >= 500 {
		t.Fatalf("A's code in B = %d", rec.Code)
	}
}

func TestBothStoresCanUseTheSameNames(t *testing.T) {
	app, a, b := twoStores(t) // seedStore already used the same SKU, slugs and "SAVE10" in both
	if n := orderNumberOfFirstOrder(t, app, a); n != "GC-000001" {
		t.Fatalf("A's first order = %s", n)
	}
	if n := orderNumberOfFirstOrder(t, app, b); n != "GC-000001" {
		t.Fatalf("B's first order = %s", n)
	}
}
```

Build the helpers (`seedStore`, `firstID`, `listIDs`, `inStore`, `jsonBody`, `firstVariantID`, `newCart`, `addDiscountCode`, `orderNumberOfFirstOrder`) from the requests the existing HTTP tests make — grep each route in `core/*_test.go` for the body shape. Use the real route paths (`grep -n 'HandleFunc("' core/*.go`). `seedStore` places one order per store.

`ext/b2b/isolation_test.go`: a company and a quote in A are 404 from B's admin routes and absent from B's lists, via `gctest`.

- [ ] **Step 2: Run them.** Expected: PASS. These guard Tasks 5–17; a failure is a real isolation bug — fix the code, not the test.
- [ ] **Step 3: Prove the suite can fail.** Temporarily comment out the `store_id` filter in `Catalog.GetProduct` and drop the policy on `products` in the test (`ALTER TABLE products NO FORCE ROW LEVEL SECURITY; ALTER TABLE products DISABLE ROW LEVEL SECURITY` through a superuser connection) — `TestStoreBCannotSeeStoreAThroughTheAdminAPI` must FAIL. Revert both. Record the result in the ledger.
- [ ] **Step 4:** Full suite: `go test ./... -count=1 -timeout 60m` → all `ok`.
- [ ] **Step 5: Commit** — subject `Prove two stores cannot see or touch each other's data`.

---

### Task 19: Two stores end to end, docs, and the decision record

**Files:**
- Modify: `scripts/smoke.ps1` (walk the sale in store #1, create store B with `addStore` SQL through `docker exec` or a `-SecondStore` parameter that calls a small `gocommerce store create <slug>` CLI subcommand — add it to `cmd/gocommerce/main.go`: creates the `stores` row and runs `Stores.seed`), `PLAN.md` (D83 row; mark D70 superseded), `README.md`, `docs/` (multi-store page), `skills/` (any skill describing platform mode or `App.DB()` scoping rules — add the rule "name `store_id` in every statement" to the module-authoring skill), `AGENTS.md` (add rule 15: every statement on a store table names `store_id`; the lint enforces it)
- Test: `.\scripts\smoke.ps1`, `.\scripts\check-docs.ps1`, Playwright `check.mjs`

- [ ] **Step 1:** Add `gocommerce store create <slug> [-name N]` with a test in `cmd/gocommerce` that runs it against a test DB and finds the store active with its four built-in roles.
- [ ] **Step 2:** Run it RED, implement, GREEN.
- [ ] **Step 3:** Extend `smoke.ps1`: after the existing walk, create store `second`, repeat the sale with `-Headers @{ 'x-litekart-store' = $second }`, and assert the order number is `GC-000001` and store #1's product list did not grow.
- [ ] **Step 4:** Full verification, from CLAUDE.md:

```powershell
gofmt -l .
go vet ./...
go test ./... -count=1 -timeout 60m
go build -tags no_admin ./...
go test -tags no_admin ./core -count=1 -timeout 60m
.\scripts\check-docs.ps1
.\scripts\build.ps1
.\scripts\dev.ps1 -Port 8090 -PgPort 5460   # in the background; then:
.\scripts\smoke.ps1 -BaseUrl http://127.0.0.1:8090
node check.mjs --base http://127.0.0.1:8090  # twice
.\gocommerce.exe doctor
```

Expected: gofmt prints nothing; every command exits 0; doctor's `row security` line passes (the dev database must use the `gocommerce_app` login — `dev.ps1` creates it the same way the Compose init script does; update `dev.ps1` in this step).

- [ ] **Step 5: Commit** — this one is public when the branch merges, so no `Update: skip`:

```bash
git commit -m "Serve every store from one set of tables, each row kept to its own store" -m "One engine now runs every store. Each row carries its store, PostgreSQL row security keeps stores apart even if a query forgets, and an existing single-store install upgrades in place with nothing to change." -m "Update: feature" -m "Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>"
```

Merging to `main` (which deploys admin.kitcommerce.store) is not part of this plan: it needs the owner's go-ahead, the `gocommerce_app` login created on the Dokploy database, and the demo store re-seeded. List those three steps in the final report.
