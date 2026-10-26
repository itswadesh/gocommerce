package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// A custom report is a saved SELECT an operator wrote.
//
// It is the one place in the engine where a person's own SQL reaches the
// database, so most of what is tested here is what it refuses. The refusals
// are two layers: a parse that rejects anything that is not a single read, and
// a read-only transaction that would refuse a write even if the parse were
// fooled. The second is the one that actually holds; the first exists so the
// operator gets "a report may only read" instead of a Postgres error.
func TestACustomReportRunsAndReturnsRows(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(1500)
	if _, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Report tee", Slug: "report-tee", Status: "active", SKU: "REPORT-1", PriceMinor: &price,
	}); err != nil {
		t.Fatal(err)
	}

	saved, err := app.CustomReports().Save(ctx, CustomReportInput{
		Name:        "  Active products  ",
		Description: "What is on sale",
		SQL:         "SELECT title, status FROM products WHERE status = 'active' ORDER BY title",
	}, nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.Name != "Active products" {
		t.Errorf("name = %q, want it trimmed", saved.Name)
	}

	out, err := app.CustomReports().Run(ctx, saved.SQL)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(out.Columns) != 2 || out.Columns[0] != "title" || out.Columns[1] != "status" {
		t.Errorf("columns = %v, want the ones the query names", out.Columns)
	}
	if len(out.Rows) == 0 {
		t.Fatal("no rows, but a product was seeded")
	}
	if out.Truncated {
		t.Error("one product should not have hit the row cap")
	}
	if out.Took <= 0 {
		t.Error("how long it took is part of the answer for a report somebody is tuning")
	}
}

// Everything that is not a single read is refused, and the message says why
// rather than leaking a driver error.
func TestACustomReportMayOnlyRead(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	for _, q := range []string{
		"",
		"   ",
		"UPDATE products SET title = 'x'",
		"DELETE FROM orders",
		"INSERT INTO products (slug, title, currency) VALUES ('x', 'x', 'USD')",
		"DROP TABLE products",
		"TRUNCATE products",
		"ALTER TABLE products ADD COLUMN x text",
		"CREATE TABLE evil (id int)",
		"GRANT ALL ON products TO PUBLIC",
		// Two statements: the second is the payload, and a check that only
		// looked at the first word would pass this.
		"SELECT 1; DROP TABLE products",
		"SELECT 1;DELETE FROM orders",
		// A leading comment hiding the verb.
		"-- harmless\nDELETE FROM orders",
		"/* nothing to see */ UPDATE products SET title = 'x'",
		// A CTE that writes, which is the one way a statement starting with
		// WITH can change data.
		"WITH gone AS (DELETE FROM orders RETURNING id) SELECT * FROM gone",
	} {
		if _, err := app.CustomReports().Run(ctx, q); err == nil {
			t.Errorf("%q was allowed to run", q)
		}
	}
}

// A plain SELECT and a read-only CTE are the two shapes that do run.
func TestTheTwoShapesThatRun(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	for _, q := range []string{
		"SELECT 1 AS n",
		"select 1 as n",
		"  \n SELECT 1 AS n  ",
		"-- a comment first\nSELECT 1 AS n",
		"WITH recent AS (SELECT id FROM orders LIMIT 5) SELECT count(*) FROM recent",
		// A trailing semicolon is how everybody writes SQL and is not a second
		// statement.
		"SELECT 1 AS n;",
	} {
		if _, err := app.CustomReports().Run(ctx, q); err != nil {
			t.Errorf("%q was refused: %v", q, err)
		}
	}
}

// A write that gets past the parse still cannot land: the reports role cannot
// write and the transaction is read-only, so two layers refuse it below the
// parse. Proven by going around the parse deliberately.
func TestTheTransactionRefusesAWriteTheParseMissed(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	price := int64(1000)
	if _, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Untouched", Slug: "untouched", Status: "active", SKU: "UNTOUCHED-1", PriceMinor: &price,
	}); err != nil {
		t.Fatal(err)
	}

	// runInReadOnlyTx is what Run calls after parsing. Calling it directly is
	// the only way to ask "if the parse were wrong, would anything happen".
	if _, err := app.CustomReports().runInReadOnlyTx(ctx, "UPDATE products SET title = 'changed'"); err == nil {
		t.Fatal("a read-only transaction accepted an UPDATE")
	}

	var title string
	if err := app.db.QueryRowContext(ctx,
		`SELECT title FROM products WHERE id = (SELECT min(id) FROM products)`).Scan(&title); err != nil {
		t.Fatal(err)
	}
	if title == "changed" {
		t.Fatal("the write landed; the read-only transaction is not doing anything")
	}
}

// The real barrier is the database role, not the read-only transaction. A
// read-only transaction stops writes and DDL but allows PostgreSQL's
// maintenance functions, which under a superuser role can terminate
// connections, create replication slots or read files off the server. The
// reports role was granted nothing but SELECT, so the database itself refuses
// them. If a change ever ran reports on the engine's own (superuser)
// connection, the is_superuser assertion and these refusals would start to
// fail — which is the regression this test exists to catch (D72).
func TestAReportRunsAsALeastPrivilegedRole(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	out, err := app.CustomReports().Run(ctx, "SELECT current_setting('is_superuser') AS v")
	if err != nil {
		t.Fatalf("reading is_superuser: %v", err)
	}
	if len(out.Rows) != 1 || out.Rows[0][0] == nil || *out.Rows[0][0] != "off" {
		t.Fatal("the reports role is a superuser; the barrier is gone")
	}

	// Each of these needs a privilege the reports role was never granted, so
	// the database denies it even though every one is a plain SELECT a
	// read-only transaction would wave through.
	for _, q := range []string{
		"SELECT pg_read_file('/etc/passwd')",
		"SELECT pg_ls_dir('.')",
		"SELECT pg_create_physical_replication_slot('gctest_probe')",
		"SELECT pg_reload_conf()",
	} {
		if _, err := app.CustomReports().Run(ctx, q); err == nil {
			t.Errorf("%q was permitted; a report can do more than read", q)
		}
	}
}

// With no reports role configured the feature fails closed: saving and listing
// still work, but running a report returns a clear error rather than falling
// back to the engine's own connection — which is the hole the role closes.
func TestCustomReportsFailClosedWithoutARole(t *testing.T) {
	dsn := requireDB(t)
	cfg := testConfig(dsn)
	cfg.ReportsDBURL = "" // this store never configured a reports role
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	ctx := context.Background()

	saved, err := app.CustomReports().Save(ctx, CustomReportInput{Name: "Later", SQL: "SELECT 1 AS n"}, nil)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if rows, err := app.CustomReports().Saved(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("saved = %d, %v; save and list must work with reports off", len(rows), err)
	}

	_, err = app.CustomReports().Run(ctx, saved.SQL)
	if err == nil {
		t.Fatal("a report ran with no reports role configured")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "reports_disabled" {
		t.Fatalf("error = %v, want code reports_disabled", err)
	}
}

// A report that returns more than the cap says so rather than quietly handing
// back a prefix, which is the difference between a number an operator can
// trust and one they cannot.
func TestTooManyRowsIsReportedRatherThanHidden(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	out, err := app.CustomReports().Run(ctx, "SELECT generate_series(1, 5000) AS n")
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) != MaxReportRows {
		t.Errorf("rows = %d, want the cap of %d", len(out.Rows), MaxReportRows)
	}
	if !out.Truncated {
		t.Error("it did not say it had been cut short")
	}
}

// Saved reports are listed, edited and deleted, and a name is required so the
// list means something.
func TestSavedReportsAreManaged(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	if _, err := app.CustomReports().Save(ctx, CustomReportInput{Name: "", SQL: "SELECT 1"}, nil); err == nil {
		t.Error("a report with no name should be refused")
	}
	// The SQL is checked when it is saved, not only when it is run: a report
	// that cannot run is not worth keeping, and finding out at save time is
	// the moment the author can fix it.
	if _, err := app.CustomReports().Save(ctx, CustomReportInput{Name: "Bad", SQL: "DELETE FROM orders"}, nil); err == nil {
		t.Error("a report that writes should be refused at save time")
	}

	first, err := app.CustomReports().Save(ctx, CustomReportInput{Name: "One", SQL: "SELECT 1 AS n"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := app.CustomReports().Save(ctx, CustomReportInput{
		ID: first.ID, Name: "One, renamed", SQL: "SELECT 2 AS n",
	}, nil)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ID != first.ID || updated.Name != "One, renamed" || !strings.Contains(updated.SQL, "2") {
		t.Errorf("update = %+v, want the same row changed", updated)
	}

	rows, err := app.CustomReports().Saved(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("saved = %d, want the one row after an update", len(rows))
	}

	if err := app.CustomReports().Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if rows, _ := app.CustomReports().Saved(ctx); len(rows) != 0 {
		t.Errorf("saved after delete = %d", len(rows))
	}
}

// Writing a report is owner's work. Reading one is not: the point of saving a
// report is that somebody else can run it.
func TestWhoMayWriteAReport(t *testing.T) {
	if !defaultRightsHas(RoleOwner, RightReportsWrite) {
		t.Error("owner should be able to write a custom report")
	}
	for _, role := range []string{RoleManager, RoleStaff} {
		if defaultRightsHas(role, RightReportsWrite) {
			t.Errorf("%s carries reports.write by default; arbitrary SQL reaches every table in the store", role)
		}
	}
	if !defaultRightsHas(RoleManager, RightReportsRead) {
		t.Error("manager should be able to run a report somebody saved")
	}
}

// A report is SQL its author writes, and under shared tenancy that SQL could
// call set_config and name another store. It runs pinned to its own store by
// a policy the reports login cannot get around (D83, Part 4): the run is
// recorded against its connection in a table only the engine can write, and
// set_config moves nothing that policy reads.
func runReportOver(t *testing.T, app *App, store, query string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/admin/reports/custom/run",
		strings.NewReader(`{"sql":`+strconv.Quote(query)+`}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-litekart-store", store)
	withAdmin(req)
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestAReportReadsOnlyItsOwnStore(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "bee")
	for _, c := range []struct{ store, code string }{{app.DefaultStore(), "mine"}, {b, "theirs"}} {
		if _, err := app.db.ExecContext(WithStore(context.Background(), c.store),
			`INSERT INTO customer_groups (code, name) VALUES ($1, $1)`, c.code); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{
		"SELECT code FROM customer_groups",
		// Naming the other store, the way the engine's own connection does.
		"SELECT code FROM customer_groups WHERE set_config('gocommerce.store_id', '" + b + "', true) IS NOT NULL",
		"SELECT code FROM customer_groups WHERE store_id = '" + b + "'",
	} {
		code, body := runReportOver(t, app, app.DefaultStore(), q)
		if code != http.StatusOK {
			t.Fatalf("%q = %d: %s", q, code, body)
		}
		if strings.Contains(body, "theirs") {
			t.Fatalf("a report read another store's rows with %q: %s", q, body)
		}
	}
	// And the other store's report sees its own.
	if code, body := runReportOver(t, app, b, "SELECT code FROM customer_groups"); code != http.StatusOK ||
		!strings.Contains(body, "theirs") || strings.Contains(body, "mine") {
		t.Fatalf("store B's report = %d: %s", code, body)
	}
}

// The tables that belong to no one store — people, their sessions, the store
// directory — are not a report's to read at all.
func TestAReportCannotReadTheInstallsOwnTables(t *testing.T) {
	app := newTestApp(t)
	for _, table := range []string{"superusers", "superuser_sessions", "superuser_password_resets",
		"stores", "store_members", "api_key_index", "report_runs", "report_withheld"} {
		code, body := runReportOver(t, app, app.DefaultStore(), "SELECT count(*) FROM "+table)
		if code != http.StatusBadRequest || !strings.Contains(body, "permission denied") {
			t.Fatalf("reading %s = %d, want the database's permission denied: %s", table, code, body)
		}
	}
}

// A connection the engine did not record a run for sees nothing: the policy
// fails closed rather than falling back to whatever store_id says.
func TestAnUnrecordedReportConnectionSeesNothing(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.db.ExecContext(WithStore(context.Background(), app.DefaultStore()),
		`INSERT INTO customer_groups (code, name) VALUES ('hidden', 'hidden')`); err != nil {
		t.Fatal(err)
	}
	conn, err := app.reportsDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Naming the store first, in a statement of its own, as a session would.
	if _, err := conn.ExecContext(context.Background(),
		`SELECT set_config('gocommerce.store_id', '`+app.DefaultStore()+`', false)`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := conn.QueryRowContext(context.Background(), `SELECT count(*) FROM customer_groups`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("an unrecorded reports connection read %d rows", n)
	}
}

// Staff may run any saved report, so a report reads no key to somebody's
// account: not an order look-up code, not the token that opens a basket or a
// guest's order. It still reads the rest of those rows.
func TestASavedReportReadsNoCodeOrToken(t *testing.T) {
	app := newTestApp(t)
	staff := bearer(signInAs(t, app, "report-staff@example.com", RoleStaff))
	run := func(query string) (int, string) {
		t.Helper()
		saved, err := app.CustomReports().Save(context.Background(), CustomReportInput{Name: query, SQL: query}, nil)
		if err != nil {
			t.Fatalf("save %q: %v", query, err)
		}
		rec := do(t, app, http.MethodPost, "/api/admin/reports/custom/"+strconv.FormatInt(saved.ID, 10)+"/run", staff)
		return rec.Code, rec.Body.String()
	}
	for _, q := range []string{
		"SELECT code_hash FROM order_lookup_codes",
		"SELECT count(*) FROM order_lookup_codes",
		"SELECT token FROM carts",
		"SELECT * FROM carts",
		"SELECT access_token FROM orders",
	} {
		if code, body := run(q); code != http.StatusBadRequest || !strings.Contains(body, "permission denied") {
			t.Errorf("staff running %q = %d, want the database's permission denied: %s", q, code, body)
		}
	}
	for _, q := range []string{"SELECT id, created_at FROM carts", "SELECT number, status FROM orders"} {
		if code, body := run(q); code != http.StatusOK {
			t.Errorf("staff running %q = %d, want the rest of the row: %s", q, code, body)
		}
	}
}

// A misspelt name would leave the credential it meant readable, so it fails.
func TestWithholdingANameThatIsNotThereFails(t *testing.T) {
	app := newTestApp(t)
	for _, target := range []string{"carts.tokn", "no_such_table"} {
		ctx := context.Background()
		err := InTx(ctx, app.db, func(tx *sql.Tx) error { return WithholdFromReports("x", target).Run(ctx, tx) })
		if err == nil || !strings.Contains(err.Error(), "no such table or column") {
			t.Errorf("withholding %q = %v, want it refused", target, err)
		}
	}
}
