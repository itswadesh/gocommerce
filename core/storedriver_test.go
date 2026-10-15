package gocommerce

import (
	"context"
	"database/sql"
	"testing"
)

// boundTable makes a throwaway RLS table in the test schema, as the engine's
// own tables are after 0054, so the driver is tested on its own.
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

func countDrvRows(t *testing.T, ctx context.Context, db *sql.DB) int {
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
	for _, s := range []struct {
		ctx context.Context
		v   string
	}{{a, "a1"}, {a, "a2"}, {b, "b1"}} {
		if _, err := db.ExecContext(s.ctx,
			`INSERT INTO drv_rows (store_id, v) VALUES (current_setting('gocommerce.store_id'), $1)`, s.v); err != nil {
			t.Fatal(err)
		}
	}
	if got := countDrvRows(t, a, db); got != 2 {
		t.Fatalf("store A sees %d rows, want 2", got)
	}
	if got := countDrvRows(t, b, db); got != 1 {
		t.Fatalf("store B sees %d rows, want 1", got)
	}
	if got := countDrvRows(t, context.Background(), db); got != 0 {
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
	if got := countDrvRows(t, b, db); got != 0 {
		t.Fatalf("after rollback, store B sees %d rows, want 0", got)
	}
	if got := countDrvRows(t, a, db); got != 1 {
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
	if got := countDrvRows(t, a, db); got != 1 {
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
		if got := countDrvRows(t, a, db); got != 3 {
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
	if got := countDrvRows(t, context.Background(), db); got != 1 {
		t.Fatalf("fallback sees %d rows, want 1", got)
	}
	if got := countDrvRows(t, WithStore(context.Background(), "store_B"), db); got != 0 {
		t.Fatal("an explicit store lost to the fallback")
	}
}

// A transaction that switched store and then failed to commit leaves the
// server on the store it had before; the driver must not go on believing the
// switch happened, or the next statement for that store would skip binding
// and run as the previous one.
func TestAFailedCommitForgetsItsStore(t *testing.T) {
	db := openOneConn(t)
	boundTable(t, db)
	if _, err := db.ExecContext(context.Background(), `
		CREATE TABLE drv_parent (id int PRIMARY KEY);
		CREATE TABLE drv_child (parent int REFERENCES drv_parent DEFERRABLE INITIALLY DEFERRED)`); err != nil {
		t.Fatal(err)
	}
	a := WithStore(context.Background(), "store_A")
	if _, err := db.ExecContext(a, `INSERT INTO drv_rows VALUES ('store_A', 'x')`); err != nil {
		t.Fatal(err)
	}
	// Begun as A, switched to B inside the transaction (as 0054 does): the
	// switch is transactional, so a failed commit undoes it.
	b := WithStore(context.Background(), "store_B")
	tx, err := db.BeginTx(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(b, `INSERT INTO drv_child VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("the deferred foreign key let the commit through")
	}
	// The server is back on store A; a statement for B must set B again.
	if got := countDrvRows(t, b, db); got != 0 {
		t.Fatalf("after a failed commit, store B sees %d of store A's rows", got)
	}
}
