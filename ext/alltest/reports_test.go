package alltest

import (
	"context"
	"regexp"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
	"github.com/itswadesh/gocommerce/internal/testdb"
)

// credentialName is a column name that may hold a key to something.
var credentialName = regexp.MustCompile(`token|secret|password|key|code_hash|session|credential|settings`)

// reportsMayRead are the columns whose names look like a credential and that
// a saved report may read all the same, each with the reason it is no key.
var reportsMayRead = map[string]string{
	"ai_executions.input_tokens":              "a count of model tokens",
	"ai_executions.output_tokens":             "a count of model tokens",
	"ai_executions.cache_read_tokens":         "a count of model tokens",
	"ai_executions.cache_write_tokens":        "a count of model tokens",
	"ai_jobs.idem_key":                        "a caller's retry key; it replays nothing without the request",
	"ai_jobs.claim_token":                     "a worker's lease, compared only by the engine's own job loop",
	"api_key_rates.key_id":                    "an id",
	"approval_requests.idem_key":              "a caller's retry key; it replays nothing without the request",
	"approval_requests.requested_by_key_id":   "an id",
	"b2b_approvals.idempotency_key":           "a buyer's retry key, matched only for that buyer",
	"b2b_partial_checkouts.idempotency_key":   "a buyer's retry key, matched only for that buyer",
	"cart_recovery_settings.settings":         "the reminder steps and the storefront's address; no key",
	"media.storage_key":                       "a file's path in storage",
	"plugins.key":                             "a module's name",
	"roles.key":                               "a role's name",
	"variants.option_key":                     "the variant's option values",
	"api_keys.token_hash":                     "a SHA-256 of a random key; it cannot be turned back into one",
	"b2b_invitations.token_hash":              "a SHA-256 of a 256-bit random token",
	"identity_email_verifications.token_hash": "a SHA-256 of a 256-bit random token",
	"identity_password_resets.token_hash":     "a SHA-256 of a 256-bit random token",
	"identity_sessions.token_hash":            "a SHA-256 of a 256-bit random token",
	"superuser_invitations.token_hash":        "a SHA-256 of a 256-bit random token",
}

// Staff may run any saved report, and a table is readable unless a migration
// withholds it (gocommerce.WithholdFromReports). So a column a report reads
// whose name looks like a credential fails here until it is withheld, or
// listed above with the reason it is no key.
func TestNoReportReadsAColumnNamedLikeACredential(t *testing.T) {
	app := gctest.NewWithReports(t, everyModule()...)
	rows, err := app.DB().QueryContext(context.Background(), `
		SELECT table_name || '.' || column_name FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND has_column_privilege($1, quote_ident(table_name), column_name, 'SELECT')
		ORDER BY 1`, testdb.ReportsRole)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := map[string]bool{}
	for rows.Next() {
		var col string
		if err := rows.Scan(&col); err != nil {
			t.Fatal(err)
		}
		seen[col] = true
		if _, name, _ := strings.Cut(col, "."); credentialName.MatchString(name) && reportsMayRead[col] == "" {
			t.Errorf("a saved report reads %s: withhold it with gocommerce.WithholdFromReports, or list it in reportsMayRead with the reason it is no key", col)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for col := range reportsMayRead {
		if !seen[col] {
			t.Errorf("reportsMayRead lists %s, which no report reads; drop it", col)
		}
	}
}

// With every module's migrations run, everything withheld is still there to
// withhold and still withheld: a module that renames a withheld column
// without withholding the new name fails here.
func TestEveryWithheldNameIsWithheld(t *testing.T) {
	app := gctest.NewWithReports(t, everyModule()...)
	for _, d := range app.Diagnose(context.Background()).Checks {
		if d.Name == "report withholding" {
			if d.Status != gocommerce.StatusOK {
				t.Errorf("%s: %s", d.Detail, d.Hint)
			}
			return
		}
	}
	t.Fatal("the doctor has no report withholding check")
}

// columnsHolding names every store-table column whose value contains needle,
// as "table.column": only those a saved report may read when readable is set,
// otherwise every one.
func columnsHolding(t *testing.T, app *gocommerce.App, needle string, readable bool) []string {
	t.Helper()
	ctx := gocommerce.WithStore(context.Background(), app.DefaultStore())
	rows, err := app.DB().QueryContext(ctx, `
		SELECT table_name, column_name FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND (NOT $2 OR has_column_privilege($1, quote_ident(table_name), column_name, 'SELECT'))
		ORDER BY 1, 2`, testdb.ReportsRole, readable)
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
		q := `SELECT EXISTS (SELECT 1 FROM "` + c[0] + `" WHERE strpos("` + c[1] + `"::text, $1) > 0)`
		if err := app.DB().QueryRowContext(ctx, q, needle).Scan(&hit); err != nil {
			t.Fatalf("%s.%s: %v", c[0], c[1], err)
		}
		if hit {
			found = append(found, c[0]+"."+c[1])
		}
	}
	return found
}

// cart.abandoned carries the basket's token so a recovery email can link back
// to it, and the event is kept after it is published, in the outbox and in
// each webhook delivery. No column a saved report may read holds that token.
func TestNoReportReadsAnAbandonedBasketsToken(t *testing.T) {
	app := gctest.NewWithReports(t, everyModule()...)
	ctx := gocommerce.WithStore(context.Background(), app.DefaultStore())
	if rec := gctest.AdminRequest(t, app, "POST", "/api/admin/x/webhooks/endpoints",
		map[string]any{"url": "https://hooks.example.com/baskets", "events": []string{"cart.abandoned"}}); rec.Code != 201 {
		t.Fatalf("create the endpoint = %d: %s", rec.Code, rec.Body)
	}
	product := gctest.CreateProduct(t, app, "ABANDON-REPORT", 2500, 5)
	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, product.DefaultVariant().ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cart().SetEmail(ctx, cart.Token, "shopper@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DB().ExecContext(ctx, `UPDATE carts SET expires_at = now() - interval '1 minute' WHERE token = $1`, cart.Token); err != nil {
		t.Fatal(err)
	}
	if n, err := app.Cart().Abandon(ctx); err != nil || n != 1 {
		t.Fatalf("Abandon = %d, %v; want 1", n, err)
	}
	gctest.DrainOutbox(t, app)

	all := strings.Join(columnsHolding(t, app, cart.Token, false), " ")
	for _, col := range []string{"outbox_events.payload", "webhook_deliveries.payload"} {
		if !strings.Contains(all, col) {
			t.Fatalf("the basket's token is in %s, not %s; this test looks at nothing", all, col)
		}
	}
	if got := columnsHolding(t, app, cart.Token, true); len(got) > 0 {
		t.Errorf("a saved report reads the basket's token out of %v", got)
	}
}
