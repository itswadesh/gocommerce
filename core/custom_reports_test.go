package gocommerce

import (
	"context"
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

// The transaction is the real enforcement, so a write that somehow gets past
// the parse still cannot land. Proven by going around the parse deliberately.
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
