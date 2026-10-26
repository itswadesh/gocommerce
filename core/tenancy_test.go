package gocommerce

import (
	"context"
	"errors"
	"testing"
	"time"
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
	if su.Role != RoleManager {
		t.Fatalf("Create returned role %q", su.Role)
	}
	ms, err := app.Stores().MembershipsOf(ctx, su.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].StoreID != app.DefaultStore() || ms[0].Role != RoleManager {
		t.Fatalf("memberships = %+v", ms)
	}
	var cols int
	if err := app.db.QueryRowContext(ctx, `SELECT count(*) FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'superusers' AND column_name IN ('role','vendor_id')`).Scan(&cols); err != nil {
		t.Fatal(err)
	}
	if cols != 0 {
		t.Fatal("superusers still carries role/vendor_id; a person's role belongs to a store")
	}
}

// storeTables lists tables in the test schema that must be scoped: every
// table except the global ones. It reads the catalog, not a hand-kept list, so
// a table added later without a policy fails here.
func storeTables(t *testing.T, app *App) []string {
	t.Helper()
	global := map[string]bool{"stores": true, "store_domains": true, "store_members": true,
		"store_counters": true, "api_key_index": true, "superusers": true,
		"superuser_sessions": true, "superuser_password_resets": true, "gocommerce_migrations": true,
		// Which store a reports connection runs for, by backend pid. It is how
		// the reports policy is keyed on something a query cannot set, so it
		// must not itself be read through gocommerce.store_id; the reports
		// login has no rights on it at all (report_scope.go).
		"report_runs": true,
		// Which tables and columns reports may not read: the schema's, the same
		// for every store (D120).
		"report_withheld": true}
	rows, err := app.db.QueryContext(context.Background(),
		`SELECT tablename FROM pg_tables WHERE schemaname = current_schema() ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
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
	simpleProduct(t, app, "UNBOUND-1", 1000, 3)
	db, err := OpenDB(context.Background(), app.cfg.DBURL) // a pool with no fallback
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
	if err := app.db.QueryRowContext(ctxA, `INSERT INTO products (store_id, title, slug, currency)
		VALUES (current_setting('gocommerce.store_id'), 'P', 'p', 'USD') RETURNING id`).Scan(&productA); err != nil {
		t.Fatal(err)
	}
	ctxB := WithStore(context.Background(), b)
	if _, err := app.db.ExecContext(ctxB, `INSERT INTO variants (store_id, product_id, sku, price_minor)
		VALUES (current_setting('gocommerce.store_id'), $1, 'X', 100)`, productA); err == nil {
		t.Fatal("store B created a variant of store A's product")
	}
}

func TestTheSameSKUCanBeSoldByTwoStores(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	ctxB := WithStore(context.Background(), b)
	price, stock := int64(1000), 5
	in := ProductInput{Title: "Tee", Status: ProductActive, SKU: "SKU-1", PriceMinor: &price, Stock: &stock}
	pa, err := app.Products().CreateProduct(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	pb, err := app.Products().CreateProduct(ctxB, in)
	if err != nil {
		t.Fatalf("store B could not use a SKU store A uses: %v", err)
	}
	if _, err := app.Products().GetProduct(ctxB, pa.ID); err == nil {
		t.Fatal("store B read store A's product by id")
	}
	if got, err := app.Products().GetProduct(ctxB, pb.ID); err != nil || got.ID != pb.ID {
		t.Fatalf("store B cannot read its own product: %v", err)
	}
}

// placeOrderIn sells one unit of a fresh product in the store ctx is bound to.
func placeOrderIn(t *testing.T, app *App, ctx context.Context, sku string) *Order {
	t.Helper()
	price, stock := int64(1200), 5
	p, err := app.Products().CreateProduct(ctx, ProductInput{Title: sku, Status: ProductActive, SKU: sku, PriceMinor: &price, Stock: &stock})
	if err != nil {
		t.Fatal(err)
	}
	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, p.DefaultVariant().ID, 1); err != nil {
		t.Fatal(err)
	}
	res, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	return res.Order
}

func TestEachStoreNumbersItsOwnOrders(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	a1 := placeOrderIn(t, app, context.Background(), "A-1")
	b1 := placeOrderIn(t, app, WithStore(context.Background(), b), "B-1")
	a2 := placeOrderIn(t, app, context.Background(), "A-2")
	if a1.Number != "GC-000001" || b1.Number != "GC-000001" || a2.Number != "GC-000002" {
		t.Fatalf("numbers = %s, %s, %s; want GC-000001 in each store, then GC-000002", a1.Number, b1.Number, a2.Number)
	}
}

func TestAStoreCanChooseItsOrderPrefix(t *testing.T) {
	app := newTestApp(t)
	b := addTestStore(t, app, "second")
	if _, err := app.db.ExecContext(context.Background(), `UPDATE stores SET order_prefix = 'SB-' WHERE id = $1`, b); err != nil {
		t.Fatal(err)
	}
	if got := placeOrderIn(t, app, WithStore(context.Background(), b), "B-1").Number; got != "SB-000001" {
		t.Fatalf("number = %s, want SB-000001", got)
	}
}

// A plugin's switch and its settings — an email provider's key, say — are one
// store's, not the install's.
func TestAPluginIsSwitchedOnPerStore(t *testing.T) {
	app := newTestApp(t, fakePlugin{})
	b := addTestStore(t, app, "second")
	ctxA, ctxB := context.Background(), WithStore(context.Background(), b)
	on := true
	if _, err := app.Plugins().Update(ctxA, "fake-mail", PluginPatch{
		Enabled: &on, Settings: map[string]any{"api_key": "store-a-secret"}}); err != nil {
		t.Fatal(err)
	}
	if enabled, err := app.Plugins().Enabled(ctxB, "fake-mail"); err != nil || enabled {
		t.Fatalf("store B's fake-mail enabled=%v err=%v; store A switched it on, not B", enabled, err)
	}
	if enabled, err := app.Plugins().Enabled(ctxA, "fake-mail"); err != nil || !enabled {
		t.Fatalf("store A's fake-mail enabled=%v err=%v", enabled, err)
	}
}

func TestCreatingAStoreGivesItWhatItNeedsToSell(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	st, err := app.Stores().Create(ctx, "Corner Shop", "corner")
	if err != nil {
		t.Fatal(err)
	}
	if !ValidStoreID(st.ID) || st.Slug != "corner" || st.Name != "Corner Shop" || st.Status != "active" {
		t.Fatalf("store = %+v", st)
	}
	bound := WithStore(ctx, st.ID)
	var roles, locations, profiles int
	if err := app.db.QueryRowContext(bound, `SELECT
		(SELECT count(*) FROM roles WHERE builtin),
		(SELECT count(*) FROM locations WHERE is_default),
		(SELECT count(*) FROM store_profile)`).Scan(&roles, &locations, &profiles); err != nil {
		t.Fatal(err)
	}
	if roles != 4 || locations != 1 || profiles != 1 {
		t.Fatalf("roles=%d default locations=%d profiles=%d; want 4, 1, 1", roles, locations, profiles)
	}
	if _, err := app.Stores().Create(ctx, "Again", "corner"); !errors.Is(err, ErrConflict) {
		t.Fatalf("a second store with the same slug: %v, want a conflict", err)
	}
	if _, err := app.Stores().Create(ctx, "Bad", "Not A Slug!"); !errors.Is(err, ErrValidation) {
		t.Fatalf("a malformed slug: %v, want a validation error", err)
	}
}

// Taking an order number holds nothing until commit: a checkout that has its
// number and is still finishing does not make the store's next checkout wait.
func TestTakingAnOrderNumberDoesNotHoldUpTheNextCheckout(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	held, err := app.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Rollback()
	if _, err := app.orderNumber(ctx, held); err != nil {
		t.Fatal(err)
	}
	price, stock := int64(1200), 5
	p, err := app.Products().CreateProduct(ctx, ProductInput{Title: "Not waiting", Status: ProductActive, SKU: "NOT-WAITING", PriceMinor: &price, Stock: &stock})
	if err != nil {
		t.Fatal(err)
	}
	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, p.DefaultVariant().ID, 1); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("checkout: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the checkout waited for another transaction's order number to commit")
	}
}
