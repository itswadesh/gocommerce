package gocommerce

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// columnsHolding names every store-table column whose value contains needle,
// as "table.column": only those the reports login may read when readable is
// set, otherwise every one.
func columnsHolding(t *testing.T, app *App, needle string, readable bool) []string {
	t.Helper()
	ctx := WithStore(context.Background(), app.DefaultStore())
	rows, err := app.db.QueryContext(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND (NOT $2 OR has_column_privilege($1, quote_ident(table_name), column_name, 'SELECT'))
		ORDER BY 1, 2`, testReportsRole, readable)
	if err != nil {
		t.Fatal(err)
	}
	var cols [][2]string
	for rows.Next() {
		var c [2]string
		if err := rows.Scan(&c[0], &c[1]); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	rows.Close()
	var found []string
	for _, c := range cols {
		var hit bool
		q := fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM %s WHERE strpos(%s::text, $1) > 0)`, quoteIdent(c[0]), quoteIdent(c[1]))
		if err := app.db.QueryRowContext(ctx, q, needle).Scan(&hit); err != nil {
			t.Fatalf("%s.%s: %v", c[0], c[1], err)
		}
		if hit {
			found = append(found, c[0]+"."+c[1])
		}
	}
	return found
}

// A checkout under an Idempotency-Key keeps its whole answer for the retry,
// the guest's order token and the provider's payment details with it. No
// column a saved report may read holds the guest's or the basket's token.
func TestNoReportReadsACheckoutsTokens(t *testing.T) {
	app := newTestApp(t)
	product := simpleProduct(t, app, "IDEM-REPORT", 1500, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(context.Background(), CodeCOD, checkoutInput(cart.Token), "report-key")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if result.Order.AccessToken == "" {
		t.Fatal("the checkout returned no order token; there is nothing to look for")
	}
	if got := columnsHolding(t, app, result.Order.AccessToken, false); !strings.Contains(strings.Join(got, " "), "idempotency_keys.response") {
		t.Fatalf("the order token is in %v; want it kept for the retry, or this test looks at nothing", got)
	}
	for _, token := range []string{result.Order.AccessToken, cart.Token} {
		if got := columnsHolding(t, app, token, true); len(got) > 0 {
			t.Errorf("a saved report reads a token out of %v", got)
		}
	}
	if code, body := runReportOver(t, app, app.DefaultStore(), "SELECT count(*) FROM idempotency_keys"); code != http.StatusBadRequest || !strings.Contains(body, "permission denied") {
		t.Errorf("reading idempotency_keys = %d, want the database's permission denied: %s", code, body)
	}
}

// Every start sets up the reports login, and an install may start several
// copies at once.
func TestReportsAccessIsSetUpByConcurrentStarts(t *testing.T) {
	app := newTestApp(t)
	// No policy yet, so every start below also creates them all.
	if _, err := app.db.ExecContext(context.Background(), `
		DO $$ DECLARE t text; BEGIN
		FOR t IN SELECT tablename FROM pg_policies WHERE schemaname = current_schema() AND policyname = 'report_scope' LOOP
			EXECUTE format('DROP POLICY report_scope ON %I', t);
		END LOOP; END $$`); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = app.ensureReportsAccess(context.Background())
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("start %d: %v", i, err)
		}
	}
}

// What an older version grants, or a renamed column, opens what is withheld;
// the doctor says so.
func TestDoctorFailsWhenReportsCanReadWhatIsWithheld(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if d := findCheck(t, app.Diagnose(ctx), "report withholding"); d.Status != StatusOK {
		t.Fatalf("a fresh install: %+v, want ok", d)
	}

	// An older version's start grants the whole table back.
	if _, err := app.db.ExecContext(ctx, `GRANT SELECT ON carts TO `+testReportsRole); err != nil {
		t.Fatal(err)
	}
	d := findCheck(t, app.Diagnose(ctx), "report withholding")
	if d.Status != StatusFail || !strings.Contains(d.Detail, "carts.token") || d.Hint == "" {
		t.Errorf("after a whole-table grant: %+v, want a failure naming carts.token", d)
	}
	if err := app.ensureReportsAccess(ctx); err != nil {
		t.Fatal(err)
	}
	if d := findCheck(t, app.Diagnose(ctx), "report withholding"); d.Status != StatusOK {
		t.Errorf("after this version starts again: %+v, want ok", d)
	}

	// A renamed column is no longer the name withheld.
	if _, err := app.db.ExecContext(ctx, `ALTER TABLE orders RENAME COLUMN access_token TO guest_token`); err != nil {
		t.Fatal(err)
	}
	if err := app.ensureReportsAccess(ctx); err != nil {
		t.Fatal(err)
	}
	d = findCheck(t, app.Diagnose(ctx), "report withholding")
	if d.Status != StatusWarn || !strings.Contains(d.Detail, "orders.access_token") {
		t.Errorf("after a rename: %+v, want a warning naming orders.access_token", d)
	}
}

// The log's order-number column is written from the scrubbed data, so a
// sender that names it a credential keeps it out of that column too.
func TestTheLogsOrderNumberColumnKeepsNoCredential(t *testing.T) {
	app, _ := resetApp(t)
	const secret = "KEY-8812-ZZQ"
	if err := app.Notify(context.Background(), Notification{
		Event: "test.secret_order_number", Channel: ChannelEmail, To: "shopper@example.com",
		Data:    map[string]string{"order_number": secret},
		Secrets: []string{"order_number"},
	}); err != nil {
		t.Fatal(err)
	}
	var number string
	if err := app.db.QueryRowContext(WithStore(context.Background(), app.DefaultStore()),
		`SELECT coalesce(order_number, '') FROM notifications WHERE event = 'test.secret_order_number'`).Scan(&number); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(number, secret) {
		t.Errorf("order_number = %q, want the credential hidden", number)
	}
}
