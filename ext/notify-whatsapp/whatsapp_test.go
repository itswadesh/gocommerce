package whatsapp

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	cartrecovery "github.com/itswadesh/gocommerce/ext/cart-recovery"
	"github.com/itswadesh/gocommerce/gctest"
)

const (
	testPhoneID     = "1029384756"
	testWABA        = "5647382910"
	testToken       = "EAAG-whatsapp-system-user-token"
	testAppSecret   = "app-secret-for-signing"
	testVerifyToken = "verify-me-please-0123456789"
	shopperPhone    = "+919876543210"
)

// fakeGraph is the Graph API as Meta's references describe it: it takes
// messages and answers their wamid, lists the account's templates, and takes
// a new template for review. Nothing here reaches Meta.
type fakeGraph struct {
	mu        sync.Mutex
	messages  []map[string]any
	created   []map[string]any
	auth      []string
	templates []map[string]any
	failWith  string // a JSON error body to answer messages with
	status    int
}

func (f *fakeGraph) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	body, _ := io.ReadAll(r.Body)
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v26.0/"+testPhoneID+"/messages":
		if f.failWith != "" {
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(f.failWith))
			return
		}
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		f.messages = append(f.messages, m)
		fmt.Fprintf(w, `{"messaging_product":"whatsapp","contacts":[{"input":%q,"wa_id":"x"}],"messages":[{"id":"wamid.%d"}]}`,
			m["to"], len(f.messages))
	case r.Method == http.MethodGet && r.URL.Path == "/v26.0/"+testWABA+"/message_templates":
		out, _ := json.Marshal(map[string]any{"data": f.templates, "paging": map[string]any{}})
		_, _ = w.Write(out)
	case r.Method == http.MethodPost && r.URL.Path == "/v26.0/"+testWABA+"/message_templates":
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		f.created = append(f.created, m)
		_, _ = w.Write([]byte(`{"id":"777","status":"PENDING","category":"UTILITY"}`))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeGraph) sent() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.messages...)
}

func tpl(name, category, status, body string, quickReplies ...string) map[string]any {
	comps := []map[string]any{{"type": "BODY", "text": body}}
	if len(quickReplies) > 0 {
		var b []map[string]any
		for _, q := range quickReplies {
			b = append(b, map[string]any{"type": "QUICK_REPLY", "text": q})
		}
		comps = append(comps, map[string]any{"type": "BUTTONS", "buttons": b})
	}
	return map[string]any{"name": name, "language": "en", "status": status, "category": category, "components": comps}
}

type emailRecorder struct{ rec *gctest.RecordingNotifier }

func (emailRecorder) Name() string                       { return "recorder" }
func (emailRecorder) Migrations() []gocommerce.Migration { return nil }
func (m emailRecorder) Register(app *gocommerce.App) error {
	app.RegisterNotifier(gocommerce.ChannelEmail, m.rec)
	return nil
}

type harness struct {
	t     *testing.T
	app   *gocommerce.App
	mod   *Module
	graph *fakeGraph
	email *gctest.RecordingNotifier
	logs  *bytes.Buffer
	rec   *cartrecovery.Module
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	g := &fakeGraph{templates: []map[string]any{
		tpl("order_placed", "UTILITY", "APPROVED", "Hi {{1}}, order {{2}} for {{3}} is placed."),
		tpl("cod_check", "UTILITY", "APPROVED", "Hi {{1}}, confirm cash order {{2}}?", "Confirm", "Cancel"),
		tpl("promo_basket", "MARKETING", "APPROVED", "Hi {{1}}, your basket: {{2}}"),
		tpl("pending_one", "UTILITY", "PENDING", "Hi {{1}}"),
	}}
	srv := gctest.StubHTTP(t, g.handler)
	logs := &bytes.Buffer{}
	mod := New(Config{BaseURL: srv.URL})
	email := &gctest.RecordingNotifier{}
	rec := cartrecovery.New(cartrecovery.Config{StorefrontURL: "https://shop.example"})
	cfg := gocommerce.Config{Currency: "INR", Logger: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	app := gctest.NewWithConfig(t, cfg, mod, emailRecorder{email}, rec)
	h := &harness{t: t, app: app, mod: mod, graph: g, email: email, logs: logs, rec: rec}
	ctx := context.Background()
	if _, err := app.Profile().Set(ctx, gocommerce.StoreProfile{Name: "Asha Stores", Country: "IN"}, nil); err != nil {
		t.Fatalf("profile: %v", err)
	}
	return h
}

// connect is the owner's part: the account switched on with its IDs and
// secrets.
func (h *harness) connect(extra map[string]any) {
	h.t.Helper()
	on := true
	s := map[string]any{
		"phone_number_id": testPhoneID, "waba_id": testWABA, "access_token": testToken,
		"app_secret": testAppSecret, "verify_token": testVerifyToken,
	}
	for k, v := range extra {
		s[k] = v
	}
	if _, err := h.app.Plugins().Update(context.Background(), PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: s}); err != nil {
		h.t.Fatalf("connect: %v", err)
	}
}

func (h *harness) mapSlot(slot, name string, keys ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	var vars []Variable
	for i, k := range keys {
		vars = append(vars, Variable{Name: fmt.Sprint(i + 1), Key: k})
	}
	return gctest.AdminRequest(h.t, h.app, http.MethodPut, "/api/admin/x/notify-whatsapp/slots/"+slot,
		MapInput{Name: name, Language: "en", Variables: vars})
}

func (h *harness) mustMap(slot, name string, keys ...string) {
	h.t.Helper()
	if rec := h.mapSlot(slot, name, keys...); rec.Code != http.StatusOK {
		h.t.Fatalf("map %s: %d %s", slot, rec.Code, rec.Body)
	}
}

func (h *harness) cart(sku string) string {
	h.t.Helper()
	ctx := context.Background()
	p := gctest.CreateProduct(h.t, h.app, sku, 149900, 10)
	cart, err := h.app.Cart().Create(ctx, "")
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.app.Cart().AddLine(ctx, cart.Token, p.DefaultVariant().ID, 1); err != nil {
		h.t.Fatal(err)
	}
	return cart.Token
}

func (h *harness) consent(cartToken, phone string, orders, marketing bool) *httptest.ResponseRecorder {
	h.t.Helper()
	return gctest.Request(h.t, h.app, http.MethodPut, "/x/notify-whatsapp/consent",
		ConsentInput{CartID: cartToken, Phone: phone, OrderUpdates: orders, Marketing: marketing})
}

func (h *harness) checkout(cartToken, payment, phone string) *gocommerce.Order {
	h.t.Helper()
	res, err := h.app.Order().Checkout(context.Background(), payment, gocommerce.CheckoutInput{
		CartID: cartToken, Email: "asha@example.com", Phone: phone, Name: "Asha Rao",
		Address: gocommerce.Address{Line1: "1 MG Road", City: "Bengaluru", State: "KA", PostalCode: "560001", Country: "IN"},
	}, "")
	if err != nil {
		h.t.Fatalf("checkout: %v", err)
	}
	return res.Order
}

func (h *harness) deliver() {
	h.t.Helper()
	gctest.DrainOutbox(h.t, h.app)
	if _, err := h.mod.DeliverPass(context.Background()); err != nil {
		h.t.Fatalf("deliver: %v", err)
	}
}

func (h *harness) history(orderID int64) string {
	h.t.Helper()
	entries, _, err := h.app.Order().Timeline(context.Background(), orderID, 100, 0)
	if err != nil {
		h.t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Summary + "\n")
	}
	return b.String()
}

func (h *harness) webhook(body any, sign bool) *httptest.ResponseRecorder {
	h.t.Helper()
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/x/notify-whatsapp/webhook?store="+h.app.DefaultStore(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	if sign {
		mac := hmac.New(sha256.New, []byte(testAppSecret))
		mac.Write(raw)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	rec := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(rec, req)
	return rec
}

// event wraps messages or statuses the way Meta's webhook reference does.
func event(value map[string]any) map[string]any {
	value["messaging_product"] = "whatsapp"
	value["metadata"] = map[string]any{"phone_number_id": testPhoneID}
	return map[string]any{"object": "whatsapp_business_account", "entry": []any{map[string]any{
		"id": testWABA, "changes": []any{map[string]any{"field": "messages", "value": value}},
	}}}
}

func buttonReply(wamid, payload string) map[string]any {
	return event(map[string]any{"messages": []any{map[string]any{
		"from": "919876543210", "id": "wamid.in1", "type": "button",
		"context": map[string]any{"from": "15550000000", "id": wamid},
		"button":  map[string]any{"payload": payload, "text": "x"},
	}}})
}

func textFrom(body string) map[string]any {
	return event(map[string]any{"messages": []any{map[string]any{
		"from": "919876543210", "id": "wamid.in2", "type": "text", "text": map[string]any{"body": body},
	}}})
}

// ---------------------------------------------------------------------- tests

func TestNumbersAreReadAsE164InTheStoresCountry(t *testing.T) {
	cases := []struct{ in, country, want string }{
		{"98765 43210", "IN", "+919876543210"},
		{"098765-43210", "IN", "+919876543210"},
		{"919876543210", "IN", "+919876543210"},
		{"+44 7700 900123", "IN", "+447700900123"},
		{"0044 7700 900123", "", "+447700900123"},
		{"(415) 555-0123", "US", "+14155550123"},
	}
	for _, c := range cases {
		if got, ok := e164(c.in, c.country); !ok || got != c.want {
			t.Errorf("e164(%q, %s) = %q %v, want %q", c.in, c.country, got, ok, c.want)
		}
	}
	for _, bad := range []string{"98765", "9876543210", "+0123456789", ""} {
		country := "IN"
		if bad == "9876543210" {
			country = "ZZ" // a number with no code, in a country we cannot read it in
		}
		if got, ok := e164(bad, country); ok {
			t.Errorf("e164(%q) = %q, want refused", bad, got)
		}
	}
}

func TestNoMessageGoesWithoutConsent(t *testing.T) {
	h := newHarness(t)
	h.connect(nil)
	h.mustMap("order_confirmation", "order_placed", "customer_first_name", "order_number", "order_total")

	// No box ticked: nothing is sent, and the log has no WhatsApp row.
	h.checkout(h.cart("NC-1"), gocommerce.CodeCOD, "98765 43210")
	// Ticked offers only: order updates still need their own yes.
	tok := h.cart("NC-2")
	if rec := h.consent(tok, "98765 43210", false, true); rec.Code != http.StatusOK {
		t.Fatalf("consent: %d %s", rec.Code, rec.Body)
	}
	h.checkout(tok, gocommerce.CodeCOD, "98765 43210")
	h.deliver()
	if n := len(h.graph.sent()); n != 0 {
		t.Fatalf("%d messages sent without consent to order updates", n)
	}
	recs, _, err := h.app.Notifications().List(context.Background(), gocommerce.NotificationQuery{Channel: gocommerce.ChannelWhatsApp})
	if err != nil || len(recs) != 0 {
		t.Fatalf("whatsapp rows in the log: %d %v", len(recs), err)
	}
}

func TestAnOrderConfirmationIsTheMappedTemplateWithItsValues(t *testing.T) {
	h := newHarness(t)
	h.connect(nil)
	h.mustMap("order_confirmation", "order_placed", "customer_first_name", "order_number", "order_total")
	tok := h.cart("OC-1")
	if rec := h.consent(tok, "098765 43210", true, false); rec.Code != http.StatusOK {
		t.Fatalf("consent: %d %s", rec.Code, rec.Body)
	}
	order := h.checkout(tok, gocommerce.CodeCOD, "9876543210")
	h.deliver()

	sent := h.graph.sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d messages, want 1", len(sent))
	}
	got, _ := json.Marshal(sent[0])
	want := fmt.Sprintf(`{"messaging_product":"whatsapp","recipient_type":"individual","template":{"components":[{"parameters":[{"text":"Asha","type":"text"},{"text":%q,"type":"text"},{"text":"₹1,499.00","type":"text"}],"type":"body"}],"language":{"code":"en"},"name":"order_placed"},"to":"+919876543210","type":"template"}`, order.Number)
	if string(got) != want {
		t.Fatalf("payload\n got %s\nwant %s", got, want)
	}
	if a := h.graph.auth[len(h.graph.auth)-1]; a != "Bearer "+testToken {
		t.Fatalf("authorization %q", a)
	}
	if hist := h.history(order.ID); !strings.Contains(hist, "Order confirmation sent on WhatsApp to "+shopperPhone) {
		t.Fatalf("order history does not say it was sent:\n%s", hist)
	}

	// The outbox delivering order.created again sends nothing twice.
	h.app.DB().Exec(`UPDATE outbox_events SET published_at = NULL`)
	h.deliver()
	if n := len(h.graph.sent()); n != 1 {
		t.Fatalf("redelivery sent %d messages", n)
	}
}

func codSetup(t *testing.T, settings map[string]any) (*harness, *gocommerce.Order, map[string]any) {
	h := newHarness(t)
	h.connect(settings)
	h.mustMap("order_confirmation", "order_placed", "customer_first_name", "order_number", "order_total")
	h.mustMap("cod_confirmation", "cod_check", "customer_first_name", "order_number")
	tok := h.cart("COD-1")
	h.consent(tok, shopperPhone, true, false)
	order := h.checkout(tok, gocommerce.CodeCOD, shopperPhone)
	h.deliver()
	sent := h.graph.sent()
	if len(sent) != 1 {
		t.Fatalf("sent %d, want only the COD confirmation", len(sent))
	}
	return h, order, sent[0]
}

func TestACashOrderIsConfirmedByReply(t *testing.T) {
	h, order, msg := codSetup(t, nil)
	comps := msg["template"].(map[string]any)["components"].([]any)
	if len(comps) != 3 {
		t.Fatalf("components: %v", comps)
	}
	for i, want := range []string{"confirm", "cancel"} {
		b := comps[i+1].(map[string]any)
		p := b["parameters"].([]any)[0].(map[string]any)
		if b["type"] != "button" || b["sub_type"] != "quick_reply" || b["index"] != fmt.Sprint(i) ||
			p["type"] != "payload" || p["payload"] != fmt.Sprintf("cod:%s:%d", want, order.ID) {
			t.Fatalf("button %d: %v", i, b)
		}
	}

	if rec := h.webhook(buttonReply("wamid.1", fmt.Sprintf("cod:confirm:%d", order.ID)), true); rec.Code != http.StatusOK {
		t.Fatalf("webhook: %d %s", rec.Code, rec.Body)
	}
	if hist := h.history(order.ID); !strings.Contains(hist, "confirmed this cash-on-delivery order on WhatsApp") {
		t.Fatalf("history:\n%s", hist)
	}
	// Meta delivering the same reply twice records it once.
	h.webhook(buttonReply("wamid.1", fmt.Sprintf("cod:confirm:%d", order.ID)), true)
	if n := strings.Count(h.history(order.ID), "confirmed this cash-on-delivery"); n != 1 {
		t.Fatalf("confirmation recorded %d times", n)
	}
	// The thank-you is a free-form reply, inside the window the reply opened.
	h.deliver()
	last := h.graph.sent()[len(h.graph.sent())-1]
	if last["type"] != "text" || last["to"] != shopperPhone {
		t.Fatalf("reply: %v", last)
	}
}

func TestACancelReplyFlagsTheOrderUnlessTheStoreLetsItCancel(t *testing.T) {
	h, order, _ := codSetup(t, nil)
	h.webhook(buttonReply("wamid.1", fmt.Sprintf("cod:cancel:%d", order.ID)), true)
	got, _ := h.app.Order().Get(context.Background(), order.ID)
	if got.Status == gocommerce.OrderCancelled {
		t.Fatal("cancelled although the store did not allow it")
	}
	if hist := h.history(order.ID); !strings.Contains(hist, "asked on WhatsApp to cancel") {
		t.Fatalf("not flagged:\n%s", hist)
	}
	rec := gctest.AdminRequest(t, h.app, http.MethodGet, "/api/admin/x/notify-whatsapp/messages?reply=cancel_requested", nil)
	var flagged []Message
	gctest.DecodeData(t, rec, &flagged)
	if len(flagged) != 1 || flagged[0].OrderNumber != order.Number {
		t.Fatalf("flagged: %+v", flagged)
	}

	h2, order2, _ := codSetup(t, map[string]any{"cod_cancel": true})
	h2.webhook(buttonReply("wamid.1", fmt.Sprintf("cod:cancel:%d", order2.ID)), true)
	got2, _ := h2.app.Order().Get(context.Background(), order2.ID)
	if got2.Status != gocommerce.OrderCancelled {
		t.Fatalf("status %s, want cancelled", got2.Status)
	}
}

func TestAReplyFromAnotherNumberOrToAnotherOrderDoesNothing(t *testing.T) {
	h, order, _ := codSetup(t, map[string]any{"cod_cancel": true})
	// Another order's id in the payload: that order was never asked.
	h.webhook(buttonReply("wamid.1", fmt.Sprintf("cod:cancel:%d", order.ID+999)), true)
	// The right order, from a number it was not sent to.
	other := buttonReply("wamid.1", fmt.Sprintf("cod:cancel:%d", order.ID))
	other["entry"].([]any)[0].(map[string]any)["changes"].([]any)[0].(map[string]any)["value"].(map[string]any)["messages"].([]any)[0].(map[string]any)["from"] = "447700900123"
	h.webhook(other, true)
	got, _ := h.app.Order().Get(context.Background(), order.ID)
	if got.Status == gocommerce.OrderCancelled {
		t.Fatal("a stranger's reply cancelled the order")
	}
}

func TestAWebhookWithoutMetasSignatureIsRefused(t *testing.T) {
	h, order, _ := codSetup(t, map[string]any{"cod_cancel": true})
	body := buttonReply("wamid.1", fmt.Sprintf("cod:cancel:%d", order.ID))
	if rec := h.webhook(body, false); rec.Code != http.StatusForbidden {
		t.Fatalf("unsigned: %d", rec.Code)
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/x/notify-whatsapp/webhook?store="+h.app.DefaultStore(), bytes.NewReader(raw))
	mac := hmac.New(sha256.New, []byte("not-the-app-secret"))
	mac.Write(raw)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	rec := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("wrong secret: %d", rec.Code)
	}
	got, _ := h.app.Order().Get(context.Background(), order.ID)
	if got.Status == gocommerce.OrderCancelled {
		t.Fatal("an unsigned webhook cancelled the order")
	}
}

func TestTheVerifyHandshakeAnswersOnlyTheStoresToken(t *testing.T) {
	h := newHarness(t)
	h.connect(nil)
	base := "/x/notify-whatsapp/webhook?store=" + h.app.DefaultStore() + "&hub.mode=subscribe&hub.challenge=1158201444"
	rec := gctest.Request(t, h.app, http.MethodGet, base+"&hub.verify_token="+testVerifyToken, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "1158201444" {
		t.Fatalf("handshake: %d %q", rec.Code, rec.Body)
	}
	if rec := gctest.Request(t, h.app, http.MethodGet, base+"&hub.verify_token=guess", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong token: %d", rec.Code)
	}
}

func TestStatusesMoveForwardOntoTheOrder(t *testing.T) {
	h, order, _ := codSetup(t, nil)
	status := func(s string) {
		h.webhook(event(map[string]any{"statuses": []any{map[string]any{
			"id": "wamid.1", "status": s, "timestamp": "1751142888", "recipient_id": "919876543210",
		}}}), true)
	}
	status("delivered")
	status("read")
	status("delivered") // late, out of order
	var got string
	h.app.DB().QueryRow(`SELECT status FROM whatsapp_messages WHERE wamid = 'wamid.1'`).Scan(&got)
	if got != "read" {
		t.Fatalf("status %q, want read", got)
	}
	hist := h.history(order.ID)
	for _, want := range []string{"confirmation delivered on WhatsApp", "confirmation read on WhatsApp"} {
		if !strings.Contains(hist, want) {
			t.Fatalf("history lacks %q:\n%s", want, hist)
		}
	}
	if strings.Count(hist, "delivered on WhatsApp") != 1 {
		t.Fatalf("the late status was recorded:\n%s", hist)
	}
}

func TestStopIsHonouredUntilStart(t *testing.T) {
	h := newHarness(t)
	h.connect(nil)
	h.mustMap("order_confirmation", "order_placed", "customer_first_name", "order_number", "order_total")
	tok := h.cart("STOP-1")
	h.consent(tok, shopperPhone, true, true)

	h.webhook(textFrom(" stop "), true)
	h.checkout(tok, gocommerce.CodeCOD, shopperPhone)
	h.deliver()
	for _, m := range h.graph.sent() {
		if m["type"] == "template" {
			t.Fatalf("a template went after STOP: %v", m)
		}
	}
	if len(h.graph.sent()) != 1 || h.graph.sent()[0]["type"] != "text" {
		t.Fatalf("want only the opt-out acknowledgement, got %v", h.graph.sent())
	}

	h.webhook(textFrom("START"), true)
	h.checkout(h.cart("STOP-2"), gocommerce.CodeCOD, shopperPhone)
	h.deliver()
	templates := 0
	for _, m := range h.graph.sent() {
		if m["type"] == "template" {
			templates++
		}
	}
	if templates != 1 {
		t.Fatalf("after START, %d templates", templates)
	}
}

func TestOnlyAnApprovedTemplateOfTheRightKindCanBeChosen(t *testing.T) {
	h := newHarness(t)
	h.connect(nil)
	for _, c := range []struct {
		slot, name string
		keys       []string
		want       string
	}{
		{"order_confirmation", "pending_one", []string{"order_number"}, "not approved"},
		{"order_confirmation", "promo_basket", []string{"order_number", "order_total"}, "marketing template"},
		{"cart_reminder", "order_placed", []string{"summary", "summary", "summary"}, "utility template"},
		{"cod_confirmation", "order_placed", []string{"order_number", "order_number", "order_number"}, "quick-reply"},
		{"order_confirmation", "order_placed", []string{"order_number"}, "variables"},
		{"order_confirmation", "order_placed", []string{"order_number", "order_number", "recovery_url"}, "not a value"},
	} {
		rec := h.mapSlot(c.slot, c.name, c.keys...)
		if rec.Code != http.StatusBadRequest || !strings.Contains(strings.ToLower(rec.Body.String()), c.want) {
			t.Errorf("%s ← %s: %d %s, want 400 mentioning %q", c.slot, c.name, rec.Code, rec.Body, c.want)
		}
	}
}

func TestSecretsNeverLeave(t *testing.T) {
	h := newHarness(t)
	h.connect(nil)
	h.mustMap("order_confirmation", "order_placed", "customer_first_name", "order_number", "order_total")
	// Meta quoting the token back in a refusal.
	h.graph.mu.Lock()
	h.graph.failWith = `{"error":{"message":"Malformed access token ` + testToken + `","code":100}}`
	h.graph.status = http.StatusBadRequest
	h.graph.mu.Unlock()
	tok := h.cart("SEC-1")
	h.consent(tok, shopperPhone, true, false)
	order := h.checkout(tok, "cod", shopperPhone)
	h.deliver()

	var seen strings.Builder
	for _, path := range []string{
		"/api/admin/x/notify-whatsapp/status", "/api/admin/x/notify-whatsapp/messages",
		"/api/admin/plugins/" + PluginKey, "/api/admin/audit",
	} {
		rec := gctest.AdminRequest(t, h.app, http.MethodGet, path, nil)
		seen.WriteString(rec.Body.String())
	}
	seen.WriteString(h.history(order.ID))
	seen.WriteString(h.logs.String())
	for _, secret := range []string{testToken, testAppSecret, testVerifyToken} {
		if strings.Contains(seen.String(), secret) {
			t.Errorf("%q leaked", secret)
		}
	}
	if !strings.Contains(h.history(order.ID), "could not be sent") {
		t.Fatalf("the refusal is not on the order:\n%s", h.history(order.ID))
	}
}

func TestSuggestingATemplateSubmitsItForReview(t *testing.T) {
	h := newHarness(t)
	h.connect(nil)
	rec := gctest.AdminRequest(t, h.app, http.MethodPost, "/api/admin/x/notify-whatsapp/slots/cod_confirmation/suggest", nil)
	var out map[string]string
	gctest.DecodeData(t, rec, &out)
	if out["status"] != "PENDING" || out["name"] != "gc_cod_confirmation" {
		t.Fatalf("suggest: %d %v", rec.Code, out)
	}
	got, _ := json.Marshal(h.graph.created[0])
	for _, want := range []string{`"category":"UTILITY"`, `"QUICK_REPLY"`, `"Confirm order"`, `"body_text"`} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("created %s lacks %s", got, want)
		}
	}
}

func TestTheStorefrontIsToldWhichBoxesToShow(t *testing.T) {
	h := newHarness(t)
	read := func() (out struct{ Enabled, Marketing bool }) {
		rec := gctest.Request(t, h.app, http.MethodGet, "/x/notify-whatsapp/consent", nil)
		gctest.DecodeData(t, rec, &out)
		return
	}
	if s := read(); s.Enabled || s.Marketing {
		t.Fatalf("unconnected: %+v", s)
	}
	h.connect(nil)
	h.mustMap("order_confirmation", "order_placed", "customer_first_name", "order_number", "order_total")
	if s := read(); !s.Enabled || s.Marketing {
		t.Fatalf("order template only: %+v", s)
	}
	h.mustMap("cart_reminder", "promo_basket", "customer_first_name", "summary")
	if s := read(); !s.Enabled || !s.Marketing {
		t.Fatalf("with reminders: %+v", s)
	}
	if rec := h.consent(h.cart("BAD-1"), "12", true, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("a bad number: %d", rec.Code)
	}
}

// A recovery sequence of a WhatsApp step then an email: the shopper who agreed
// to reminders gets the WhatsApp, the one who did not has that step passed
// over, and both still get the email.
func TestCartRecoverySendsWhatsAppOnlyWithConsentAndEmailStill(t *testing.T) {
	h := newHarness(t)
	h.connect(nil)
	h.mustMap("cart_reminder", "promo_basket", "customer_first_name", "summary")
	h.app.DB().Exec(`UPDATE cart_recovery_settings SET installed_at = now() - interval '1 day'`)
	settings := cartrecovery.DefaultSettings()
	settings.Checkout.Steps = []cartrecovery.Step{
		{WaitMinutes: 1, Action: "send_whatsapp", Template: "cart.recovery.1"},
		{WaitMinutes: 1, Action: "send_email", Template: "cart.recovery.2"},
	}
	if rec := gctest.AdminRequest(t, h.app, http.MethodPut, "/api/admin/x/cart-recovery/settings", settings); rec.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body)
	}
	ctx := context.Background()
	yes, no := h.cart("CR-YES"), h.cart("CR-NO")
	h.app.Cart().SetEmail(ctx, yes, "yes@example.com")
	h.app.Cart().SetEmail(ctx, no, "no@example.com")
	h.consent(yes, shopperPhone, false, true)
	h.consent(no, "+919811111111", true, false) // order updates only: no reminders

	pass := func() {
		h.app.DB().Exec(`UPDATE carts SET updated_at = now() - interval '2 hours'`)
		h.app.DB().Exec(`UPDATE cart_recovery_abandonments SET next_step_at = now() - interval '1 second' WHERE next_step_at IS NOT NULL`)
		if _, err := h.rec.Pass(ctx); err != nil {
			t.Fatal(err)
		}
	}
	pass() // records both
	pass() // step 1
	if _, err := h.mod.DeliverPass(ctx); err != nil {
		t.Fatal(err)
	}
	sent := h.graph.sent()
	if len(sent) != 1 || sent[0]["to"] != shopperPhone || sent[0]["template"].(map[string]any)["name"] != "promo_basket" {
		t.Fatalf("whatsapp reminders: %v", sent)
	}
	pass() // step 2, email for both
	if n := h.email.Count("cart.recovery.2"); n != 2 {
		t.Fatalf("%d reminder emails, want 2", n)
	}
}

func TestRoutesAreDocumentedAndGated(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertSpecCoversModuleRoutes(t, app, "notify-whatsapp")
	gctest.AssertAdminRoutesDeclareRights(t, app, "notify-whatsapp")
}
