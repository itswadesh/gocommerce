package metacapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const (
	testPixel = "1234567890"
	testToken = "EAAB-meta-capi-test-token"
)

// fakeMeta is a Graph API that remembers what it was sent, and answers with
// whatever status the test sets.
type fakeMeta struct {
	mu       sync.Mutex
	status   int
	reply    string
	requests []url.Values
	paths    []string
	queries  []string
}

func (f *fakeMeta) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.requests = append(f.requests, r.PostForm)
	f.paths = append(f.paths, r.URL.Path)
	f.queries = append(f.queries, r.URL.RawQuery)
	if f.status != 0 && f.status != http.StatusOK {
		w.WriteHeader(f.status)
		_, _ = w.Write([]byte(f.reply))
		return
	}
	_, _ = w.Write([]byte(`{"events_received":1,"fbtrace_id":"x"}`))
}

func (f *fakeMeta) sent() []url.Values {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]url.Values(nil), f.requests...)
}

func (f *fakeMeta) set(status int, reply string) {
	f.mu.Lock()
	f.status, f.reply = status, reply
	f.mu.Unlock()
}

// fakeCard is a gateway the shopper leaves the shop to pay at, so its order
// is placed long before it is paid.
type fakeCard struct{}

func (fakeCard) Name() string                       { return "fake-card" }
func (fakeCard) Migrations() []gocommerce.Migration { return nil }
func (fakeCard) Register(app *gocommerce.App) error { app.RegisterPayment(fakeCard{}); return nil }
func (fakeCard) Code() string                       { return "fake-card" }
func (fakeCard) Initiate(context.Context, *gocommerce.Order, gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {
	return gocommerce.PaymentIntent{Kind: gocommerce.IntentRedirect, Provider: "fake-card",
		ClientData: map[string]string{"url": "https://pay.example/session"}}, nil
}

func newApp(t *testing.T, logs *bytes.Buffer) (*gocommerce.App, *Module, *fakeMeta) {
	t.Helper()
	fake := &fakeMeta{}
	srv := gctest.StubHTTP(t, fake.handler)
	mod := New(Config{BaseURL: srv.URL})
	cfg := gocommerce.Config{}
	if logs != nil {
		cfg.Logger = slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	return gctest.NewWithConfig(t, cfg, mod, fakeCard{}), mod, fake
}

// setUp is the owner's part: the Meta pixel's ID, and the Conversions API
// switched on with its token.
func setUp(t *testing.T, app *gocommerce.App, settings map[string]any) {
	t.Helper()
	ctx := context.Background()
	on := true
	if _, err := app.Plugins().Update(ctx, "meta-pixel", gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"pixel_id": testPixel}}); err != nil {
		t.Fatalf("meta pixel: %v", err)
	}
	s := map[string]any{"access_token": testToken}
	for k, v := range settings {
		s[k] = v
	}
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: s}); err != nil {
		t.Fatalf("meta capi: %v", err)
	}
}

// browserBlock is what the storefront's connector puts on an order placed in
// a browser that ran the Meta pixel.
func browserBlock() map[string]any {
	return map[string]any{
		"fbp":               "fb.1.1700000000000.1234567890",
		"fbc":               "fb.1.1700000000000.AbCdEf",
		"client_user_agent": "Mozilla/5.0 (Linux; Android 14) Mobile",
		"event_source_url":  "https://shop.example/checkout/payment?email=shopper%40example.com#top",
	}
}

type buyer struct {
	email, phone string
	meta         map[string]any
}

func buy(t *testing.T, app *gocommerce.App, payment string, b buyer) *gocommerce.Order {
	t.Helper()
	ctx := context.Background()
	product := gctest.CreateProduct(t, app, "CAPI-"+strings.ReplaceAll(b.email, "@", "-")+"-"+payment, 2500, 10)
	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, product.Variants[0].ID, 2); err != nil {
		t.Fatal(err)
	}
	in := gocommerce.CheckoutInput{
		CartID: cart.Token, Email: b.email, Phone: b.phone, Name: "Asha Rao",
		Address: gocommerce.Address{Line1: "1 MG Road", City: "Bengaluru", State: "KA", PostalCode: "560001", Country: "IN"},
	}
	if b.meta != nil {
		in.Metadata = gocommerce.Metadata{MetadataKey: b.meta}
	}
	result, err := app.Order().Checkout(ctx, payment, in, "")
	if err != nil {
		t.Fatalf("checkout with %s: %v", payment, err)
	}
	return result.Order
}

func deliver(t *testing.T, m *Module) int {
	t.Helper()
	n, err := m.DeliverPass(context.Background())
	if err != nil {
		t.Fatalf("deliver pass: %v", err)
	}
	return n
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// sentEvent decodes the one event in a request, keeping numbers as Meta
// will read them.
func sentEvent(t *testing.T, form url.Values) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(form.Get("data")))
	dec.UseNumber()
	var data []map[string]any
	if err := dec.Decode(&data); err != nil || len(data) != 1 {
		t.Fatalf("data = %q (%v), want one event", form.Get("data"), err)
	}
	return data[0]
}

// The module's reason to exist: a cash-on-delivery order placed in a browser
// that ran the pixel reaches Meta as a Purchase it can match to the shopper
// and to the pixel's own Purchase.
func TestAPurchaseReachesMetaHashedAndDeduplicable(t *testing.T) {
	app, mod, fake := newApp(t, nil)
	setUp(t, app, map[string]any{"test_event_code": "TEST123"})

	order := buy(t, app, gocommerce.CodeCOD, buyer{email: "  Asha.Rao@Example.COM ", phone: "+91 98765-43210", meta: browserBlock()})
	gctest.DrainOutbox(t, app)
	if n := deliver(t, mod); n != 1 {
		t.Fatalf("attempted %d, want the one purchase", n)
	}

	sent := fake.sent()
	if len(sent) != 1 {
		t.Fatalf("requests = %d, want 1", len(sent))
	}
	form := sent[0]
	if fake.paths[0] != "/"+graphVersion+"/"+testPixel+"/events" {
		t.Errorf("path = %q", fake.paths[0])
	}
	if fake.queries[0] != "" {
		t.Errorf("query = %q, want nothing in the address", fake.queries[0])
	}
	if form.Get("access_token") != testToken || form.Get("test_event_code") != "TEST123" {
		t.Errorf("token %q, test code %q", form.Get("access_token"), form.Get("test_event_code"))
	}

	ev := sentEvent(t, form)
	if ev["event_name"] != "Purchase" || ev["action_source"] != "website" {
		t.Errorf("event = %v", ev)
	}
	// The pixel's Purchase carries the order number as its eventID; Meta
	// keeps one of the two only if they match.
	if ev["event_id"] != order.Number {
		t.Errorf("event_id = %v, want the order number %s", ev["event_id"], order.Number)
	}
	if ev["event_source_url"] != "https://shop.example/checkout/payment" {
		t.Errorf("event_source_url = %v, want the page without its query", ev["event_source_url"])
	}
	if ts, err := ev["event_time"].(json.Number).Int64(); err != nil || time.Since(time.Unix(ts, 0)).Abs() > 2*time.Minute {
		t.Errorf("event_time = %v, want when the order was placed", ev["event_time"])
	}

	user := ev["user_data"].(map[string]any)
	if em := user["em"].([]any); len(em) != 1 || em[0] != sha("asha.rao@example.com") {
		t.Errorf("em = %v, want sha256 of the trimmed, lower-case address", user["em"])
	}
	if ph := user["ph"].([]any); len(ph) != 1 || ph[0] != sha("919876543210") {
		t.Errorf("ph = %v, want sha256 of the digits with the country code", user["ph"])
	}
	block := browserBlock()
	for _, k := range []string{"fbp", "fbc", "client_user_agent"} {
		if user[k] != block[k] {
			t.Errorf("%s = %v, want %v as the browser had it", k, user[k], block[k])
		}
	}
	if raw := form.Get("data"); strings.Contains(strings.ToLower(raw), "asha.rao@example.com") || strings.Contains(raw, "98765") {
		t.Errorf("the request carries the shopper's address or number in the clear: %s", raw)
	}

	custom := ev["custom_data"].(map[string]any)
	if custom["currency"] != order.Currency || custom["order_id"] != order.Number {
		t.Errorf("custom_data = %v", custom)
	}
	if custom["value"] != json.Number(decimal(order.Total.AmountMinor, gocommerce.CurrencyExponent(order.Currency))) {
		t.Errorf("value = %v, want the order's total %d minor as a decimal", custom["value"], order.Total.AmountMinor)
	}
	contents := custom["contents"].([]any)
	line := contents[0].(map[string]any)
	if len(contents) != 1 || line["quantity"] != json.Number("2") || line["item_price"] != json.Number("25.00") {
		t.Errorf("contents = %v", contents)
	}

	// Sent once: a second pass finds nothing to do.
	if n := deliver(t, mod); n != 0 || len(fake.sent()) != 1 {
		t.Errorf("a delivered purchase went again")
	}
	// And the shopper's browser and hashed keys are not kept once Meta has them.
	var kept string
	if err := app.DB().QueryRowContext(context.Background(), `SELECT payload::text FROM meta_capi_events`).Scan(&kept); err != nil || kept != "{}" {
		t.Errorf("payload after delivery = %q (%v), want it emptied", kept, err)
	}
}

// Only a sale with the shopper's own pixel behind it is a website purchase
// Meta can take, and only a sale the shop will be paid for is a purchase.
func TestOnlyPurchasesFromThePixelsBrowserAreSent(t *testing.T) {
	app, mod, fake := newApp(t, nil)
	ctx := context.Background()

	// Off: nothing is queued, so nothing goes later either, not even when
	// the cash is collected on the doorstep after it was switched on.
	off := buy(t, app, gocommerce.CodeCOD, buyer{email: "off@example.com", meta: browserBlock()})
	gctest.DrainOutbox(t, app)
	setUp(t, app, nil)
	if _, err := app.Pay().MarkPaid(ctx, off.ID, "cash"); err != nil {
		t.Fatal(err)
	}
	gctest.DrainOutbox(t, app)
	if n := deliver(t, mod); n != 0 {
		t.Errorf("an order placed while the plugin was off went once it was on")
	}

	// No browser block: staff entered it, or the pixel never ran.
	buy(t, app, gocommerce.CodeCOD, buyer{email: "nopixel@example.com"})
	gctest.DrainOutbox(t, app)
	if n := deliver(t, mod); n != 0 {
		t.Errorf("an order with no pixel behind it was sent")
	}

	// A card order is placed, then paid: the purchase is the payment.
	card := buy(t, app, "fake-card", buyer{email: "card@example.com", meta: browserBlock()})
	gctest.DrainOutbox(t, app)
	if n := deliver(t, mod); n != 0 {
		t.Errorf("an unpaid card order was sent as a purchase")
	}
	if _, err := app.Pay().MarkPaid(ctx, card.ID, "pay_1"); err != nil {
		t.Fatal(err)
	}
	gctest.DrainOutbox(t, app)
	if n := deliver(t, mod); n != 1 {
		t.Fatalf("the paid card order: attempted %d, want 1", n)
	}
	if ev := sentEvent(t, fake.sent()[0]); ev["event_id"] != card.Number {
		t.Errorf("event_id = %v, want %s", ev["event_id"], card.Number)
	}

	// Cash on delivery is a purchase when placed; paying for it on the
	// doorstep is the same purchase, not a second one.
	cod := buy(t, app, gocommerce.CodeCOD, buyer{email: "cod@example.com", meta: browserBlock()})
	gctest.DrainOutbox(t, app)
	deliver(t, mod)
	if _, err := app.Pay().MarkPaid(ctx, cod.ID, "cash"); err != nil {
		t.Fatal(err)
	}
	gctest.DrainOutbox(t, app)
	deliver(t, mod)
	var ids []any
	for _, form := range fake.sent() {
		ids = append(ids, sentEvent(t, form)["event_id"])
	}
	if len(ids) != 2 || ids[1] != cod.Number {
		t.Errorf("purchases sent = %v, want the card order then the COD order once", ids)
	}

	// Meta pixel switched off: the store has said not to, and the server
	// sends nothing either, though the purchase is queued.
	pixelOff := false
	if _, err := app.Plugins().Update(ctx, "meta-pixel", gocommerce.PluginPatch{Enabled: &pixelOff}); err != nil {
		t.Fatal(err)
	}
	buy(t, app, gocommerce.CodeCOD, buyer{email: "pixeloff@example.com", meta: browserBlock()})
	gctest.DrainOutbox(t, app)
	if n := deliver(t, mod); n != 0 || len(fake.sent()) != 2 {
		t.Errorf("sent with Meta pixel switched off")
	}
}

// Meta being down is Meta's problem: the sale goes through, the outbox is
// not held up, and the purchase waits in this module's own queue until Meta
// takes it. A refusal that retrying cannot fix is given up at once.
func TestAnOutageNeverFailsTheSale(t *testing.T) {
	app, mod, fake := newApp(t, nil)
	setUp(t, app, nil)
	fake.set(http.StatusServiceUnavailable, `{"error":{"message":"Service temporarily unavailable","is_transient":true}}`)

	order := buy(t, app, gocommerce.CodeCOD, buyer{email: "down@example.com", meta: browserBlock()})
	gctest.DrainOutbox(t, app)
	gctest.AssertOutboxEmpty(t, app)
	if order.Status != gocommerce.OrderConfirmed {
		t.Errorf("order status = %s, want the sale confirmed whatever Meta says", order.Status)
	}
	deliver(t, mod)
	st := status(t, app)
	if st.Pending != 1 || st.Delivered != 0 || !strings.Contains(st.LastError, "temporarily unavailable") {
		t.Fatalf("status = %+v, want the purchase still pending with Meta's reason", st)
	}

	// Backed off: the next pass does not hammer Meta, and once the wait is
	// over and Meta is back the purchase goes.
	if n := deliver(t, mod); n != 0 {
		t.Errorf("retried at once, %d", n)
	}
	if _, err := app.DB().ExecContext(context.Background(), `UPDATE meta_capi_events SET available_at = now()`); err != nil {
		t.Fatal(err)
	}
	fake.set(http.StatusOK, "")
	deliver(t, mod)
	if st := status(t, app); st.Pending != 0 || st.Delivered != 1 {
		t.Errorf("status = %+v, want it delivered on the retry", st)
	}

	// A refusal is final: the same request would be refused again.
	fake.set(http.StatusBadRequest, `{"error":{"message":"Invalid parameter","is_transient":false}}`)
	buy(t, app, gocommerce.CodeCOD, buyer{email: "refused@example.com", meta: browserBlock()})
	gctest.DrainOutbox(t, app)
	deliver(t, mod)
	if st := status(t, app); st.Dead != 1 || !strings.Contains(st.LastError, "Invalid parameter") {
		t.Errorf("status = %+v, want the refused purchase given up with Meta's reason", st)
	}

	// Unreachable to the last attempt, it is given up then.
	fake.set(http.StatusServiceUnavailable, `{"error":{"message":"Service temporarily unavailable","is_transient":true}}`)
	buy(t, app, gocommerce.CodeCOD, buyer{email: "last@example.com", meta: browserBlock()})
	gctest.DrainOutbox(t, app)
	if _, err := app.DB().ExecContext(context.Background(), `UPDATE meta_capi_events SET attempts = $1 WHERE delivered_at IS NULL AND NOT dead`, maxAttempts-1); err != nil {
		t.Fatal(err)
	}
	deliver(t, mod)
	if st := status(t, app); st.Dead != 2 || st.Pending != 0 {
		t.Errorf("status = %+v, want the purchase given up after %d attempts", st, maxAttempts)
	}

	// Older than Meta's seven days, nothing is sent: Meta would refuse it.
	fake.set(http.StatusOK, "")
	buy(t, app, gocommerce.CodeCOD, buyer{email: "stale@example.com", meta: browserBlock()})
	gctest.DrainOutbox(t, app)
	if _, err := app.DB().ExecContext(context.Background(), `UPDATE meta_capi_events SET created_at = now() - interval '8 days' WHERE delivered_at IS NULL AND NOT dead`); err != nil {
		t.Fatal(err)
	}
	before := len(fake.sent())
	deliver(t, mod)
	if st := status(t, app); len(fake.sent()) != before || st.Dead != 3 || !strings.Contains(st.LastError, "seven days") {
		t.Errorf("status = %+v, sent %d, want a week-old purchase given up unsent", st, len(fake.sent())-before)
	}
}

// A token or pixel Meta refuses is the store's setup, not the purchase: every
// purchase waits, uncounted, until the owner fixes it, and then goes.
func TestABrokenTokenHoldsPurchasesUntilItIsFixed(t *testing.T) {
	app, mod, fake := newApp(t, nil)
	setUp(t, app, nil)
	fake.set(http.StatusBadRequest, `{"error":{"message":"Error validating access token: Session has expired","type":"OAuthException","code":190,"is_transient":false}}`)
	buy(t, app, gocommerce.CodeCOD, buyer{email: "one@example.com", meta: browserBlock()})
	buy(t, app, gocommerce.CodeCOD, buyer{email: "two@example.com", meta: browserBlock()})
	gctest.DrainOutbox(t, app)
	deliver(t, mod)
	if got := len(fake.sent()); got != 1 {
		t.Errorf("requests = %d, want the pass to stop at the first refusal", got)
	}
	if st := status(t, app); st.Pending != 2 || st.Dead != 0 || !strings.Contains(st.LastError, "Session has expired") {
		t.Fatalf("status = %+v, want both waiting with Meta's reason", st)
	}

	fake.set(http.StatusOK, "")
	if _, err := app.DB().ExecContext(context.Background(), `UPDATE meta_capi_events SET available_at = now()`); err != nil {
		t.Fatal(err)
	}
	deliver(t, mod)
	if st := status(t, app); st.Delivered != 2 {
		t.Errorf("status = %+v, want both delivered once the token works", st)
	}
	var attempts int
	if err := app.DB().QueryRowContext(context.Background(), `SELECT max(attempts) FROM meta_capi_events`).Scan(&attempts); err != nil || attempts != 1 {
		t.Errorf("attempts = %d (%v), want the refused ones not counted", attempts, err)
	}
}

// The token is a credential for the store's ad account: it goes to Meta and
// nowhere else, not the panel, the audit trail, the queue or the log.
func TestTheAccessTokenNeverLeaves(t *testing.T) {
	var logs bytes.Buffer
	app, mod, fake := newApp(t, &logs)
	setUp(t, app, nil)
	// Meta quotes a malformed token back in its refusal.
	fake.set(http.StatusBadRequest, `{"error":{"message":"Malformed access token `+testToken+`","type":"OAuthException","code":190}}`)
	buy(t, app, gocommerce.CodeCOD, buyer{email: "leak@example.com", meta: browserBlock()})
	gctest.DrainOutbox(t, app)
	deliver(t, mod)

	for _, target := range []string{
		"/api/admin/plugins", "/api/admin/plugins/" + pluginKey,
		"/api/admin/x/meta-capi/status", "/api/admin/audit",
	} {
		rec := gctest.AdminRequest(t, app, http.MethodGet, target, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d", target, rec.Code)
		}
		if strings.Contains(rec.Body.String(), testToken) {
			t.Errorf("GET %s hands the token back", target)
		}
	}
	rec := gctest.Request(t, app, http.MethodGet, "/api/plugins", nil)
	if strings.Contains(rec.Body.String(), testToken) {
		t.Errorf("the storefront was handed the token: %s", rec.Body)
	}
	var queued string
	if err := app.DB().QueryRowContext(context.Background(), `SELECT string_agg(payload::text || coalesce(last_error, ''), '') FROM meta_capi_events`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(queued, testToken) {
		t.Error("the queue holds the token")
	}
	if strings.Contains(logs.String(), testToken) {
		t.Errorf("the log carries the token: %s", logs.String())
	}
	if !strings.Contains(logs.String(), "Malformed access token") {
		t.Errorf("the log does not say why Meta refused: %s", logs.String())
	}
}

func TestRoutesAreDocumentedAndGated(t *testing.T) {
	app, _, _ := newApp(t, nil)
	gctest.AssertAdminRoutesDeclareRights(t, app, "meta-capi")
	gctest.AssertSpecCoversModuleRoutes(t, app, "meta-capi")
}

// A queued event carries the shopper's browser, which staff running a saved
// report have no reason to read.
func TestReportsCannotReadTheQueue(t *testing.T) {
	gctest.AssertReportsCannotRead(t, gctest.NewWithReports(t, New(Config{})), "meta_capi_events", "payload")
}

func status(t *testing.T, app *gocommerce.App) Status {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/meta-capi/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s", rec.Code, rec.Body)
	}
	var st Status
	gctest.DecodeData(t, rec, &st)
	return st
}

// Meta's rules for the two keys it matches a shopper on: an address trimmed
// and lower-cased, a number as digits with its country code. A number
// written without one cannot be told from somebody else's elsewhere, so it
// is left out rather than guessed.
func TestShopperKeysAreNormalisedAsMetaRequires(t *testing.T) {
	cases := []struct{ in, email, phone string }{
		{in: " Asha@Example.com\t", email: "asha@example.com"},
		{in: "+91 98765 43210", phone: "919876543210"},
		{in: "0091-98765-43210", phone: "919876543210"},
		{in: "+44 (0)20 7946 0018", phone: "442079460018"},
		{in: "(555) 555-0123", phone: ""},
		{in: "+1 555", phone: ""},
	}
	for _, c := range cases {
		if c.email != "" {
			if got := hashedEmail(c.in); got != sha(c.email) {
				t.Errorf("email %q = %s, want sha256(%q)", c.in, got, c.email)
			}
			continue
		}
		want := ""
		if c.phone != "" {
			want = sha(c.phone)
		}
		if got := hashedPhone(c.in); got != want {
			t.Errorf("phone %q = %q, want sha256(%q)", c.in, got, c.phone)
		}
	}
	if hashedEmail("  ") != "" {
		t.Error("an empty address was hashed")
	}
	for minor, want := range map[int64]string{0: "0.00", 5: "0.05", 249900: "2499.00"} {
		if got := decimal(minor, 2); got != want {
			t.Errorf("decimal(%d, 2) = %s, want %s", minor, got, want)
		}
	}
	if decimal(1500, 0) != "1500" || decimal(1500, 3) != "1.500" {
		t.Errorf("decimal by exponent = %s, %s", decimal(1500, 0), decimal(1500, 3))
	}
}

// The block arrives from a shopper's browser, so it is checked before any of
// it is sent on as a website event.
func TestABrowserBlockIsCheckedBeforeItIsBelieved(t *testing.T) {
	if b, ok := browserOf(gocommerce.Metadata{MetadataKey: browserBlock()}); !ok || b.sourceURL != "https://shop.example/checkout/payment" {
		t.Fatalf("a good block = %+v, %v", b, ok)
	}
	for name, spoil := range map[string]func(map[string]any){
		"fbp not Meta's":       func(b map[string]any) { b["fbp"] = "not-a-cookie" },
		"fbp not text":         func(b map[string]any) { b["fbp"] = 42 },
		"no user agent":        func(b map[string]any) { delete(b, "client_user_agent") },
		"a user agent too big": func(b map[string]any) { b["client_user_agent"] = strings.Repeat("x", 600) },
		"a script for a page":  func(b map[string]any) { b["event_source_url"] = "javascript:alert(1)" },
		"no page":              func(b map[string]any) { delete(b, "event_source_url") },
	} {
		block := browserBlock()
		spoil(block)
		if _, ok := browserOf(gocommerce.Metadata{MetadataKey: block}); ok {
			t.Errorf("%s: believed", name)
		}
	}
	block := browserBlock()
	block["fbc"] = "garbage"
	if b, ok := browserOf(gocommerce.Metadata{MetadataKey: block}); !ok || b.fbc != "" {
		t.Errorf("a malformed fbc = %+v, %v; want it dropped and the rest kept", b, ok)
	}
}
