package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// What a demo store publishes, proved against the routes rather than the
// functions. The unit tests in demo_test.go say the mask is right; these say it
// is actually reached, that the storefront is not caught by it, and that the
// two things masking could plausibly break — the round trip and the edit — do
// not break.

// demoApp boots a store with masking on. It is newTestApp with one field
// changed, spelled out here rather than added as a parameter that every other
// test would have to pass.
func demoApp(t *testing.T) *App {
	t.Helper()
	dsn := requireDB(t)
	resetSchema(t, dsn)

	cfg := testConfig(dsn)
	cfg.Demo = true
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func demoOrder(t *testing.T, app *App, sku, email, phone string) *Order {
	t.Helper()
	product := simpleProduct(t, app, sku, 1000, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	in := checkoutInput(cart.Token)
	in.Email, in.Phone = email, phone
	result, err := app.Order().Checkout(context.Background(), CodeCOD, in, "")
	if err != nil {
		t.Fatalf("checkout %s: %v", sku, err)
	}
	return result.Order
}

func TestADemoStoreMasksContactDetailsOnAdminRoutes(t *testing.T) {
	app := demoApp(t)
	order := demoOrder(t, app, "DEMO-1", "jane.doe@gmail.com", "+31 20 555 1242")

	for _, target := range []string{
		"/api/admin/orders",
		"/api/admin/orders/" + itoa(order.ID),
		"/api/admin/customers",
	} {
		rec := do(t, app, http.MethodGet, target, withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", target, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if strings.Contains(body, "jane.doe@gmail.com") {
			t.Errorf("GET %s published the address in full:\n%s", target, body)
		}
		if strings.Contains(body, "5551242") || strings.Contains(body, "555 1242") {
			t.Errorf("GET %s published the number in full:\n%s", target, body)
		}
		if !strings.Contains(body, "j•••@g•••.com") {
			t.Errorf("GET %s did not carry the masked address:\n%s", target, body)
		}
	}
}

// The mask is for the operator's screen. The person who placed the order is
// reading their own details back, and hiding them from the shopper would be
// hiding them from the one person they belong to.
func TestAShopperStillSeesTheirOwnOrderInFull(t *testing.T) {
	app := demoApp(t)
	order := demoOrder(t, app, "DEMO-2", "jane.doe@gmail.com", "+31 20 555 1242")

	rec := do(t, app, http.MethodGet,
		"/api/orders/"+order.Number+"?token="+order.AccessToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("guest read = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "jane.doe@gmail.com") {
		t.Errorf("a shopper was refused their own address:\n%s", rec.Body.String())
	}
}

// The Customers screen holds an address as the customer's identity and hands it
// straight back to open their drawer. Masking the column has to leave that link
// pointing at the same person.
func TestAMaskedAddressStillFindsTheirOrders(t *testing.T) {
	app := demoApp(t)
	demoOrder(t, app, "DEMO-3", "jane.doe@gmail.com", "+31 20 555 1242")

	rec := do(t, app, http.MethodGet, "/api/admin/customers", withAdmin)
	var customers []Customer
	decodeData(t, rec, &customers)
	if len(customers) != 1 {
		t.Fatalf("customers = %d, want 1", len(customers))
	}
	masked := customers[0].Email
	if masked != "j•••@g•••.com" {
		t.Fatalf("customer email = %q, want it masked", masked)
	}

	// Exactly what the drawer does with the value it was given.
	rec = do(t, app, http.MethodGet,
		"/api/admin/orders?email="+url.QueryEscape(masked), withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("filter by the mask = %d: %s", rec.Code, rec.Body.String())
	}
	var orders []Order
	decodeData(t, rec, &orders)
	if len(orders) != 1 {
		t.Fatalf("orders for the masked address = %d, want the 1 they placed", len(orders))
	}
}

// A store that is not a demo must keep the exact-match filter exact, which is
// the property the LIKE branch could quietly cost it.
func TestTheEmailFilterStaysExactOnALiveStore(t *testing.T) {
	app := newTestApp(t)
	demoOrder(t, app, "LIVE-1", "jane.doe@gmail.com", "")
	demoOrder(t, app, "LIVE-2", "jane.doe@gmail.co", "")

	rec := do(t, app, http.MethodGet,
		"/api/admin/orders?email="+url.QueryEscape("jane.doe@gmail.com"), withAdmin)
	var orders []Order
	decodeData(t, rec, &orders)
	if len(orders) != 1 {
		t.Fatalf("orders = %d, want only the exact address", len(orders))
	}
	if orders[0].Email != "jane.doe@gmail.com" {
		t.Errorf("matched %q", orders[0].Email)
	}
}

// The edit form is filled from the masked response, so saving it without
// touching the contact fields submits the mask. That must not become the stored
// address.
func TestSavingAMaskedFormLeavesTheAddressAlone(t *testing.T) {
	app := demoApp(t)
	order := demoOrder(t, app, "DEMO-4", "jane.doe@gmail.com", "+31 20 555 1242")

	body := `{"email":"j•••@g•••.com","phone":"+31 •• ••• ••42","name":"A Shopper"}`
	rec := do(t, app, http.MethodPatch, "/api/admin/orders/"+itoa(order.ID),
		withAdmin, jsonBody(t, json.RawMessage(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body.String())
	}

	stored, err := app.Order().Get(context.Background(), order.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if stored.Email != "jane.doe@gmail.com" {
		t.Errorf("the mask was written over the address: email = %q", stored.Email)
	}
	if stored.Phone != "+31 20 555 1242" {
		t.Errorf("the mask was written over the number: phone = %q", stored.Phone)
	}

	// An address the operator actually typed is still an edit.
	rec = do(t, app, http.MethodPatch, "/api/admin/orders/"+itoa(order.ID),
		withAdmin, jsonBody(t, map[string]string{"email": "someone.else@example.com"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch a real address = %d: %s", rec.Code, rec.Body.String())
	}
	stored, err = app.Order().Get(context.Background(), order.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if stored.Email != "someone.else@example.com" {
		t.Errorf("a real edit was dropped: email = %q", stored.Email)
	}
}

// The Events screen serves a payload the engine did not shape field by field,
// so it is the one surface where masking has to walk the JSON.
func TestTheEventPayloadIsMaskedToo(t *testing.T) {
	app := demoApp(t)
	demoOrder(t, app, "DEMO-6", "jane.doe@gmail.com", "+31 20 555 1242")

	// The listing carries no payload at all (it is detail-only), so the detail
	// route is the one that has to mask.
	rec := do(t, app, http.MethodGet, "/api/admin/events", withAdmin)
	var listed []OutboxEvent
	decodeData(t, rec, &listed)
	if len(listed) == 0 {
		t.Fatal("placing an order published no event")
	}

	rec = do(t, app, http.MethodGet, "/api/admin/events/"+listed[0].ID, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET the event = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "jane.doe@gmail.com") {
		t.Errorf("an order.placed payload published the address:\n%s", body)
	}
	if !strings.Contains(body, "j•••@g•••.com") {
		t.Errorf("the payload lost the address instead of masking it:\n%s", body)
	}
	if strings.Contains(body, "555 1242") {
		t.Errorf("an order.placed payload published the number:\n%s", body)
	}
}

// An export is the same screen in a file, and it leaves by a different door.
func TestTheCustomerExportIsMaskedToo(t *testing.T) {
	app := demoApp(t)
	demoOrder(t, app, "DEMO-5", "jane.doe@gmail.com", "+31 20 555 1242")

	for _, target := range []string{
		"/api/admin/export/admin-customers",
		"/api/admin/export/admin-orders",
	} {
		rec := do(t, app, http.MethodGet, target, withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", target, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "jane.doe@gmail.com") {
			t.Errorf("%s carried the address out in full:\n%s", target, rec.Body.String())
		}
	}
}

// Nothing above may happen on an ordinary store.
func TestALiveStoreMasksNothing(t *testing.T) {
	app := newTestApp(t)
	demoOrder(t, app, "LIVE-3", "jane.doe@gmail.com", "+31 20 555 1242")

	rec := do(t, app, http.MethodGet, "/api/admin/orders", withAdmin)
	if !strings.Contains(rec.Body.String(), "jane.doe@gmail.com") {
		t.Errorf("a live store masked an address:\n%s", rec.Body.String())
	}
}

// demoAccountApp is demoApp with a designated demo account — the one every
// visitor becomes — created and named in the config, beside an owner whose
// password the demo must leave alone.
func demoAccountApp(t *testing.T) (*App, *Superuser) {
	t.Helper()
	dsn := requireDB(t)
	resetSchema(t, dsn)

	cfg := testConfig(dsn)
	cfg.Demo = true
	cfg.DemoAccount = "demo@example.com"
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	newSuperuser(t, app, "owner@example.com", "the-owners-own-password")
	demo, err := app.Superusers().Create(context.Background(), "demo@example.com", "a-long-enough-password", RoleStaff)
	if err != nil {
		t.Fatalf("create demo account: %v", err)
	}
	return app, demo
}

// Whatever a visitor types, they become the demo account — and only that
// account. Typing the owner's address with the wrong password does not make
// them the owner.
func TestADemoWithADemoAccountSignsAnybodyInAsIt(t *testing.T) {
	app, demo := demoAccountApp(t)
	for _, tc := range []struct{ identity, password string }{
		{"owner@example.com", "not-the-owners-password"},
		{"visitor@example.org", "anything"},
		{"", ""},
	} {
		rec := do(t, app, http.MethodPost, "/api/admin/auth-with-password",
			jsonBody(t, map[string]string{"identity": tc.identity, "password": tc.password}))
		if rec.Code != http.StatusOK {
			t.Fatalf("sign in as %q = %d: %s", tc.identity, rec.Code, rec.Body.String())
		}
		var out struct {
			Token  string     `json:"token"`
			Record *Superuser `json:"record"`
		}
		decodeData(t, rec, &out)
		if out.Token == "" || out.Record == nil || out.Record.ID != demo.ID {
			t.Errorf("sign in as %q became %+v, want the demo account %d", tc.identity, out.Record, demo.ID)
		}
	}
}

// A demo without a designated account is the demo it always was: masked, and
// asking for real credentials.
func TestADemoWithoutADemoAccountStillAsksForAPassword(t *testing.T) {
	app := demoApp(t)
	newSuperuser(t, app, "owner@example.com", "the-owners-own-password")
	rec := do(t, app, http.MethodPost, "/api/admin/auth-with-password",
		jsonBody(t, map[string]string{"identity": "owner@example.com", "password": "wrong"}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("wrong password on a demo without a demo account = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// The routes that decide who can sign in refuse writes on a demo. Their reads
// stay open, because the Team screen is part of what a demo shows.
func TestADemoFreezesWhoCanSignIn(t *testing.T) {
	app, demo := demoAccountApp(t)
	for _, target := range []string{
		"/api/admin/superusers",
		"/api/admin/superusers/" + itoa(demo.ID),
		"/api/admin/api-keys",
		"/api/admin/roles/staff",
		"/api/admin/invitations",
		"/api/admin/me",
		"/api/admin/password-reset",
	} {
		rec := do(t, app, http.MethodPost, target, withAdmin, jsonBody(t, map[string]string{}))
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s on a demo = %d, want 403: %s", target, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "demo_frozen") {
			t.Errorf("POST %s did not name the reason:\n%s", target, rec.Body.String())
		}
	}
	rec := do(t, app, http.MethodGet, "/api/admin/superusers", withAdmin)
	if rec.Code != http.StatusOK {
		t.Errorf("GET /api/admin/superusers on a demo = %d, want reads open: %s", rec.Code, rec.Body.String())
	}
}

// The exploit the audit reproduced: sign in as anybody, ask an order for its
// guest token, replay the token on the storefront route that is deliberately
// unmasked. Closed by freezing the reveal, not by masking the guest's own view
// — the shopper still has to be able to read their own order.
func TestADemoVisitorCannotRevealAnOrdersGuestToken(t *testing.T) {
	app, _ := demoAccountApp(t)
	order := demoOrder(t, app, "DEMO-7", "jane.doe@gmail.com", "+31 20 555 1242")

	// Signed in the way any visitor is: no credential of their own.
	rec := do(t, app, http.MethodPost, "/api/admin/auth-with-password",
		jsonBody(t, map[string]string{"identity": "anyone@example.org", "password": "x"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("demo sign-in = %d: %s", rec.Code, rec.Body.String())
	}
	var session struct {
		Token string `json:"token"`
	}
	decodeData(t, rec, &session)

	rec = do(t, app, http.MethodPost, "/api/admin/orders/"+itoa(order.ID)+"/access-token",
		header("Authorization", "Bearer "+session.Token))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("revealing a guest token on a demo = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "demo_frozen") {
		t.Errorf("the refusal did not name the reason:\n%s", rec.Body.String())
	}

	// And the storefront route it was a key to still serves the shopper who
	// already holds their own token, which is the thing that must not break.
	rec = do(t, app, http.MethodGet, "/api/orders/"+order.Number+"?token="+order.AccessToken)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "jane.doe@gmail.com") {
		t.Errorf("a shopper lost their own order: %d %s", rec.Code, rec.Body.String())
	}
}

// The second reproduced exploit: the freeze was method-based, so the GET that
// returns an API key's secret walked through it.
func TestADemoVisitorCannotReadAnAPIKeySecret(t *testing.T) {
	app, _ := demoAccountApp(t)
	for _, target := range []string{
		"/api/admin/api-keys/1/secret",
		"/api/admin/reports/custom/run",
		"/api/admin/reports/custom/1/run",
		"/api/admin/vendors/1/users",
	} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			rec := do(t, app, method, target, withAdmin)
			if rec.Code != http.StatusForbidden {
				t.Errorf("%s %s on a demo = %d, want 403: %s",
					method, target, rec.Code, rec.Body.String())
			}
		}
	}
	// The listing itself stays readable — a demo shows that keys exist, just
	// not what they are.
	if rec := do(t, app, http.MethodGet, "/api/admin/api-keys", withAdmin); rec.Code != http.StatusOK {
		t.Errorf("GET /api/admin/api-keys on a demo = %d, want reads open: %s", rec.Code, rec.Body.String())
	}
}

// A contains-match against a hidden column reads it back a character at a time.
func TestTheSearchBoxIsNotAnAddressOracleOnADemo(t *testing.T) {
	app := demoApp(t)
	demoOrder(t, app, "DEMO-8", "jane.doe@gmail.com", "+31 20 555 1242")

	for _, probe := range []string{"jane", "jane.doe@gmail.com", "gmail"} {
		for _, target := range []string{"/api/admin/orders?q=", "/api/admin/customers?q="} {
			rec := do(t, app, http.MethodGet, target+url.QueryEscape(probe), withAdmin)
			if rec.Code != http.StatusOK {
				t.Fatalf("GET %s%s = %d: %s", target, probe, rec.Code, rec.Body.String())
			}
			var meta struct {
				Total int `json:"total"`
			}
			var env struct {
				Meta json.RawMessage `json:"meta"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("decode: %v", err)
			}
			_ = json.Unmarshal(env.Meta, &meta)
			if meta.Total != 0 {
				t.Errorf("searching %q on %s matched %d rows: the address is still searchable",
					probe, target, meta.Total)
			}
		}
	}

	// The name still finds them, or the box is useless.
	rec := do(t, app, http.MethodGet, "/api/admin/orders?q="+url.QueryEscape("Shopper"), withAdmin)
	if !strings.Contains(rec.Body.String(), "GC-") {
		t.Errorf("searching by name found nothing on a demo:\n%s", rec.Body.String())
	}
}

// The address carries a second phone, and the form submits the whole object.
func TestSavingAMaskedAddressLeavesTheNumberAlone(t *testing.T) {
	app := demoApp(t)
	order := demoOrder(t, app, "DEMO-9", "jane.doe@gmail.com", "+31 20 555 1242")
	if _, err := app.Order().Update(context.Background(), order.ID, OrderPatch{
		Address: &Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345",
			Country: "US", Phone: "+31 20 555 9999"},
	}); err != nil {
		t.Fatalf("set an address phone: %v", err)
	}

	rec := do(t, app, http.MethodGet, "/api/admin/orders/"+itoa(order.ID), withAdmin)
	var shown Order
	decodeData(t, rec, &shown)
	if shown.Address.Phone != "+31 •• ••• ••99" {
		t.Fatalf("address phone on a demo = %q, want it masked", shown.Address.Phone)
	}

	// Save the form back exactly as the panel received it.
	body, err := json.Marshal(map[string]any{"address": shown.Address})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	rec = do(t, app, http.MethodPatch, "/api/admin/orders/"+itoa(order.ID),
		withAdmin, jsonBody(t, json.RawMessage(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body.String())
	}

	stored, err := app.Order().Get(context.Background(), order.ID)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if stored.Address.Phone != "+31 20 555 9999" {
		t.Errorf("the mask was written over the address phone: %q", stored.Address.Phone)
	}
}

// Membership IS the address, so there is nothing to leave alone: a masked value
// has to be refused rather than silently doing nothing.
func TestAMaskedAddressIsRefusedWhereItIsTheSubject(t *testing.T) {
	app := demoApp(t)
	g, err := app.Pricing().CreateGroup(context.Background(), CustomerGroupInput{
		Code: "vip", Name: "VIP",
	})
	if err != nil {
		t.Skipf("customer groups unavailable in this shape: %v", err)
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		rec := do(t, app, method, "/api/admin/customer-groups/"+itoa(g.ID)+"/members",
			withAdmin, jsonBody(t, map[string]string{"email": "j•••@g•••.com"}))
		if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
			t.Errorf("%s a masked member = %d, want it refused: %s", method, rec.Code, rec.Body.String())
		}
	}
}
