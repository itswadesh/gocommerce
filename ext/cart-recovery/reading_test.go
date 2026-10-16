package cartrecovery

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The record as the screens read it: who the shopper is, as far as the
// store's orders say, and the words its timeline is told in.

// buyAs checks a basket out as a named shopper, so the store has an order that
// says who they are.
func (h *harness) buyAs(sku, email, name, phone string) {
	h.t.Helper()
	token, _, _ := h.basket(sku, email, 1)
	if _, err := h.app.Order().Checkout(context.Background(), "cod", gocommerce.CheckoutInput{
		CartID: token, Email: email, Name: name, Phone: phone,
		Address: gocommerce.Address{Line1: "1 Test Street", City: "Testville", State: "CA",
			PostalCode: "94117", Country: "US"},
	}, ""); err != nil {
		h.t.Fatalf("checkout: %v", err)
	}
	gctest.DrainOutbox(h.t, h.app)
}

func (h *harness) list(query string) []*Abandonment {
	h.t.Helper()
	rec := gctest.AdminRequest(h.t, h.app, http.MethodGet, "/api/admin/x/cart-recovery/abandonments?"+query, nil)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("list %q: %d %s", query, rec.Code, rec.Body)
	}
	var rows []*Abandonment
	gctest.DecodeData(h.t, rec, &rows)
	return rows
}

func TestAReturningShopperIsNamedFromTheirLastOrder(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	h.buyAs("N-1", "ann@example.com", "Ann Lee", "+15555550199")
	_, cartID, _ := h.basket("N-2", "Ann@Example.com", 1)
	h.idle(cartID, 20)
	_, strangerCart, _ := h.basket("N-3", "new@example.com", 1)
	h.idle(strangerCart, 20)
	h.pass()

	a := h.record(cartID)
	if a.Name != "Ann Lee" || a.Phone != "+15555550199" {
		t.Errorf("record names %q %q, want the name and phone on her order", a.Name, a.Phone)
	}
	if s := h.record(strangerCart); s.Name != "" || s.Phone != "" {
		t.Errorf("a first-time shopper is named %q %q, want nothing the store was never told", s.Name, s.Phone)
	}

	for _, q := range []string{"q=ann+lee", "q=0199", "q=ANN%40EXAMPLE"} {
		rows := h.list(q)
		if len(rows) != 1 || rows[0].CartID != cartID {
			t.Errorf("search %s found %d records, want Ann's alone", q, len(rows))
		}
	}
	if rows := h.list("q=nobody"); len(rows) != 0 {
		t.Errorf("search for nobody found %d", len(rows))
	}

	d := h.detail(a.ID)
	if d.Customer == nil || d.Customer.Name != "Ann Lee" || d.Customer.Phone != "+15555550199" || d.Customer.Orders != 1 {
		t.Errorf("customer = %+v, want Ann with her one order", d.Customer)
	}
}

func TestALineCarriesItsProductsPicture(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	_, cartID, variantID := h.basket("P-1", "pic@example.com", 1)
	var productID, mediaID int64
	h.app.DB().QueryRow(`SELECT product_id FROM variants WHERE id = $1`, variantID).Scan(&productID)
	if err := h.app.DB().QueryRow(`INSERT INTO media (kind, url) VALUES ('image', 'https://cdn.example.com/second.jpg') RETURNING id`).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	h.exec(`INSERT INTO product_media (product_id, media_id, position) VALUES ($1, $2, 1)`, productID, mediaID)
	if err := h.app.DB().QueryRow(`INSERT INTO media (kind, url) VALUES ('image', 'https://cdn.example.com/lead.jpg') RETURNING id`).Scan(&mediaID); err != nil {
		t.Fatal(err)
	}
	h.exec(`INSERT INTO product_media (product_id, media_id, position) VALUES ($1, $2, 0)`, productID, mediaID)
	h.idle(cartID, 20)
	h.pass()

	d := h.detail(h.record(cartID).ID)
	if len(d.Lines) != 1 || d.Lines[0].ImageURL != "https://cdn.example.com/lead.jpg" {
		t.Errorf("lines = %+v, want the product's lead picture", d.Lines)
	}
}

func TestTheTimelineIsToldInTheScreensWords(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	_, cartID, _ := h.basket("T-1", "words@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	h.due(cartID)
	h.pass()

	d := h.detail(h.record(cartID).ID)
	labels := map[string][]string{}
	for _, e := range d.Timeline {
		labels[e.Kind] = append(labels[e.Kind], e.Label)
	}
	want := map[string]string{
		"cart_created": "Cart created",
		"abandoned":    "Checkout abandoned after 20 minutes without activity",
		"sent":         "Recovery email #1 sent to words@example.com",
	}
	for kind, label := range want {
		if len(labels[kind]) == 0 || labels[kind][0] != label {
			t.Errorf("%s = %q, want %q", kind, labels[kind], label)
		}
	}
	sched := labels["scheduled"]
	if len(sched) < 2 || !strings.HasSuffix(sched[0], "after abandonment") || !strings.HasSuffix(sched[1], "after the previous email") {
		t.Errorf("scheduled = %q, want the first timed from abandonment and the next from the email before", sched)
	}
}

func TestTheRecoveryLinkSaysUntilWhenItWorks(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, gocommerce.Config{PanelURL: "https://admin.example.com"})
	_, cartID, _ := h.basket("L-1", "link@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	var expires time.Time
	h.app.DB().QueryRow(`SELECT expires_at FROM carts WHERE id = $1`, cartID).Scan(&expires)

	rec := gctest.AdminRequest(t, h.app, http.MethodPost,
		"/api/admin/x/cart-recovery/abandonments/"+itoa(h.record(cartID).ID)+"/link", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("link = %d %s", rec.Code, rec.Body)
	}
	var got struct {
		URL       string    `json:"url"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	gctest.DecodeData(t, rec, &got)
	if got.URL == "" || !got.ExpiresAt.Equal(expires) {
		t.Errorf("link = %+v, want it to work until the cart expires at %v", got, expires)
	}
}

func TestASuppressionNoteMayRunToAThousandCharacters(t *testing.T) {
	h := newHarness(t, Config{StorefrontURL: "https://shop.example.com"}, testAppConfig())
	_, cartID, _ := h.basket("S-1", "note@example.com", 1)
	h.idle(cartID, 20)
	h.pass()
	path := "/api/admin/x/cart-recovery/abandonments/" + itoa(h.record(cartID).ID) + "/suppress"

	if rec := gctest.AdminRequest(t, h.app, http.MethodPost, path,
		map[string]any{"reason": "other", "note": strings.Repeat("x", 1001)}); rec.Code != http.StatusBadRequest {
		t.Errorf("1001 characters = %d, want 400", rec.Code)
	}
	if rec := gctest.AdminRequest(t, h.app, http.MethodPost, path,
		map[string]any{"reason": "other", "note": strings.Repeat("x", 1000)}); rec.Code != http.StatusOK {
		t.Errorf("1000 characters = %d %s, want it kept", rec.Code, rec.Body)
	}
}
