package invoices

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/misiki/gocommerce/core"
	"github.com/misiki/gocommerce/gctest"
)

func testModule() *Module {
	return New(Config{
		SellerName:    "Example Ltd",
		SellerAddress: "1 Commerce Way, Testville",
		TaxID:         "GB123456789",
		NumberFormat:  "INV-{year}-{seq:04}",
	})
}

func TestRegisterRequiresSeller(t *testing.T) {
	t.Parallel()
	if err := New(Config{}).Register(nil); err == nil {
		t.Error("a missing SellerName should be refused")
	}
}

func TestFormatNumber(t *testing.T) {
	t.Parallel()
	tests := []struct {
		format string
		seq    int
		want   string
	}{
		{"INV-{year}-{seq:04}", 7, "INV-2026-0007"},
		{"INV-{year}-{seq}", 7, "INV-2026-7"},
		{"{seq:06}", 42, "000042"},
		{"INV-{year}-{seq:04}", 12345, "INV-2026-12345"}, // wider than the pad
	}
	for _, tc := range tests {
		if got := formatNumber(tc.format, 2026, tc.seq); got != tc.want {
			t.Errorf("formatNumber(%q, 2026, %d) = %q, want %q", tc.format, tc.seq, got, tc.want)
		}
	}
}

// TestInvoiceIssuedOnPayment is the module's whole job.
func TestInvoiceIssuedOnPayment(t *testing.T) {
	app := gctest.New(t, testModule())
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)

	// Cash on delivery is not paid at checkout, so no invoice yet: an invoice
	// is an accounting document for money that actually arrived.
	gctest.DrainOutbox(t, app)
	if count := invoiceCount(t, app); count != 0 {
		t.Fatalf("invoices before payment = %d, want 0", count)
	}

	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	gctest.DrainOutbox(t, app)

	if count := invoiceCount(t, app); count != 1 {
		t.Fatalf("invoices after payment = %d, want 1", count)
	}

	var number string
	if err := app.DB().QueryRowContext(ctx,
		`SELECT number FROM invoices_documents WHERE order_id = $1`, result.Order.ID).Scan(&number); err != nil {
		t.Fatalf("read invoice: %v", err)
	}
	if !strings.HasPrefix(number, "INV-") || !strings.HasSuffix(number, "-0001") {
		t.Errorf("invoice number = %q, want the configured format starting at 0001", number)
	}
}

// TestInvoicesAreIdempotent: delivery is at-least-once, so a redelivered
// order.paid must not produce a second document for the same order.
func TestInvoicesAreIdempotent(t *testing.T) {
	app := gctest.New(t, testModule())
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	gctest.DrainOutbox(t, app)

	// Replay the same event three more times, as a queue would after a crash.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE outbox_events SET published_at = NULL, available_at = now()
		 WHERE event_name = $1`, gocommerce.EventOrderPaid); err != nil {
		t.Fatalf("replay events: %v", err)
	}
	gctest.DrainOutbox(t, app)

	if count := invoiceCount(t, app); count != 1 {
		t.Errorf("invoices after redelivery = %d, want 1", count)
	}
}

// TestNumbersAreSequentialAndGapless: an invoice sequence with holes in it is
// a problem for whoever has to file the accounts.
func TestNumbersAreSequentialAndGapless(t *testing.T) {
	app := gctest.New(t, testModule())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		product := gctest.CreateProduct(t, app, "INV-SEQ-"+string(rune('a'+i)), 1000, 5)
		cart, err := app.Cart().Create(ctx, "")
		if err != nil {
			t.Fatalf("create cart: %v", err)
		}
		if _, err := app.Cart().AddLine(ctx, cart.Token, product.DefaultVariant().ID, 1); err != nil {
			t.Fatalf("add to cart: %v", err)
		}
		result, err := app.Order().Checkout(ctx, gocommerce.CodeCOD, gocommerce.CheckoutInput{
			CartID: cart.Token, Email: "buyer@example.com",
			Address: gocommerce.Address{Line1: "1 St", City: "Town", PostalCode: "1", Country: "US"},
		}, "")
		if err != nil {
			t.Fatalf("checkout %d: %v", i, err)
		}
		if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, ""); err != nil {
			t.Fatalf("mark paid %d: %v", i, err)
		}
	}
	gctest.DrainOutbox(t, app)

	rows, err := app.DB().QueryContext(ctx,
		`SELECT sequence FROM invoices_documents ORDER BY sequence`)
	if err != nil {
		t.Fatalf("read sequences: %v", err)
	}
	defer rows.Close()

	var seqs []int
	for rows.Next() {
		var s int
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		seqs = append(seqs, s)
	}
	if len(seqs) != 3 {
		t.Fatalf("invoices = %d, want 3", len(seqs))
	}
	for i, s := range seqs {
		if s != i+1 {
			t.Errorf("sequence[%d] = %d, want %d — the numbering has a gap", i, s, i+1)
		}
	}
}

// TestReconcileIssuesMissingInvoices is the safety net: an order that was paid
// while this module was not installed still gets its invoice.
func TestReconcileIssuesMissingInvoices(t *testing.T) {
	ctx := context.Background()

	// A store with no invoices module takes a payment.
	plain := gctest.New(t, nil...)
	result := gctest.PlaceOrder(t, plain, gocommerce.CodeCOD)
	if _, err := plain.Pay().MarkPaid(ctx, result.Order.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	dsn := plain.Config().DBURL
	if err := plain.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The module is installed later, against the same database. Because
	// reconcile runs at startup rather than trusting that it saw every event,
	// the earlier payment is not left without a document.
	mod := testModule()
	app, err := gocommerce.New(gocommerce.Config{
		DBURL:       dsn,
		AdminTokens: []string{gctest.AdminToken},
		Logger:      plain.Config().Logger,
	}, mod)
	if err != nil {
		t.Fatalf("boot with the invoices module: %v", err)
	}
	defer app.Close()

	if err := mod.reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if count := invoiceCount(t, app); count != 1 {
		t.Errorf("invoices after reconcile = %d, want 1", count)
	}
}

// TestInvoiceEndpoints checks both representations an operator might want.
func TestInvoiceEndpoints(t *testing.T) {
	app := gctest.New(t, testModule())
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	gctest.DrainOutbox(t, app)

	path := "/api/admin/x/invoices/" + itoa(result.Order.ID)

	// Unauthenticated: choosing HandleAdmin is the authentication.
	if rec := gctest.Request(t, app, http.MethodGet, path, nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated status = %d, want 401", rec.Code)
	}

	rec := gctest.AdminRequest(t, app, http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	html := rec.Body.String()
	for _, want := range []string{"Example Ltd", "GB123456789", result.Order.Number, "INV-"} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered invoice is missing %q", want)
		}
	}

	rec = gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/invoices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "INV-") {
		t.Errorf("invoice list is missing the invoice: %s", rec.Body)
	}
}

func invoiceCount(t *testing.T, app *gocommerce.App) int {
	t.Helper()
	var n int
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT count(*) FROM invoices_documents`).Scan(&n); err != nil {
		t.Fatalf("count invoices: %v", err)
	}
	return n
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var digits []byte
	for v > 0 {
		digits = append([]byte{byte('0' + v%10)}, digits...)
		v /= 10
	}
	return string(digits)
}

// TestInvoiceRoutesRequireOrdersRead proves the document is behind the same
// right as the order it renders. The snapshot embeds the buyer's name, email
// and address, so a role that may not read the order may not read the invoice.
//
// A session, not gctest.AdminToken: a static admin token carries every right
// and so cannot tell a gated route from an open one.
func TestInvoiceRoutesRequireOrdersRead(t *testing.T) {
	app := gctest.New(t, testModule())
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	gctest.DrainOutbox(t, app)

	document := "/api/admin/x/invoices/" + itoa(result.Order.ID)
	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)

	// Staff carries orders.read by default.
	if rec := gctest.SessionRequest(t, app, staff, http.MethodGet, "/api/admin/x/invoices", nil); rec.Code != http.StatusOK {
		t.Errorf("staff listing invoices = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := gctest.SessionRequest(t, app, staff, http.MethodGet, document, nil); rec.Code != http.StatusOK {
		t.Errorf("staff reading an invoice = %d, want 200: %s", rec.Code, rec.Body)
	}

	// Cut staff back to the catalog — the narrowest legal role — and both
	// doors close.
	if _, err := app.Roles().Set(ctx, gocommerce.RoleStaff,
		[]gocommerce.Right{gocommerce.RightCatalogRead}, nil); err != nil {
		t.Fatalf("re-cut staff: %v", err)
	}
	for _, target := range []string{"/api/admin/x/invoices", document} {
		rec := gctest.SessionRequest(t, app, staff, http.MethodGet, target, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("a catalog-only role reading %s = %d, want 403: %s", target, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "gctest@example.com") {
			t.Errorf("the refusal of %s leaked the buyer's email", target)
		}
	}
}

// TestDocumentIsHTMLUnlessJSONIsAsked pins the negotiation a printing client
// depends on: no Accept header — what a browser fetch sends by default — is the
// printable document, and rule 6 still binds the JSON branch.
func TestDocumentIsHTMLUnlessJSONIsAsked(t *testing.T) {
	app := gctest.New(t, testModule())
	ctx := context.Background()

	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	if _, err := app.Pay().MarkPaid(ctx, result.Order.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	gctest.DrainOutbox(t, app)
	path := "/api/admin/x/invoices/" + itoa(result.Order.ID)

	rec := gctest.AdminRequest(t, app, http.MethodGet, path, nil)
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/html") {
		t.Errorf("without an Accept header the content type is %q, want text/html", got)
	}

	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+gctest.AdminToken)
	req.Header.Set("Accept", "application/json")
	jsonRec := httptest.NewRecorder()
	app.Handler().ServeHTTP(jsonRec, req)
	if got := jsonRec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("with Accept: application/json the content type is %q", got)
	}
	var doc struct {
		Number   string            `json:"number"`
		IssuedAt time.Time         `json:"issued_at"`
		Order    *gocommerce.Order `json:"order"`
	}
	gctest.DecodeData(t, jsonRec, &doc)
	if doc.Number == "" || doc.Order == nil {
		t.Fatalf("the JSON branch returned %+v", doc)
	}
	// Money crosses the wire as minor units and a currency code, here as
	// everywhere else.
	if doc.Order.Total.Currency == "" || doc.Order.Total.AmountMinor == 0 {
		t.Errorf("the total is not a {amount_minor, currency} object: %+v", doc.Order.Total)
	}
}

func TestModuleContract(t *testing.T) {
	app := gctest.New(t, testModule())
	gctest.AssertAdminRoutesDeclareRights(t, app, "invoices")
	gctest.AssertSpecCoversModuleRoutes(t, app, "invoices")
}
