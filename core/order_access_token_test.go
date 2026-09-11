package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// The gap this closes, stated as a test: the access token is returned once, at
// checkout, and a shopper who lost the mail carrying it had nobody who could
// help them — no admin read returns it and nothing re-issues it.

func TestTheAccessTokenCanBeReadBackOut(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := signInAs(t, app, "owner@example.com", RoleOwner)

	product := simpleProduct(t, app, "TOKEN-1", 1500, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	order := result.Order
	if order.AccessToken == "" {
		t.Fatal("checkout returned no access token; the rest of this test is meaningless")
	}

	rec := doBody(t, app, "POST",
		"/api/admin/orders/"+strconv.FormatInt(order.ID, 10)+"/access-token", "", bearer(owner))
	if rec.Code != http.StatusOK {
		t.Fatalf("reveal = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data OrderAccessToken `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.AccessToken != order.AccessToken {
		t.Errorf("revealed %q, want the token checkout issued (%q) — a reveal that hands back "+
			"a different string has silently rotated the credential",
			body.Data.AccessToken, order.AccessToken)
	}
	if body.Data.Number != order.Number {
		t.Errorf("number = %q, want %q", body.Data.Number, order.Number)
	}

	// The point of the whole route: the customer's own read has to work with
	// what came back, because that is the thing the operator is reading out.
	guest := do(t, app, "GET", "/api/orders/"+order.Number+"?token="+body.Data.AccessToken)
	if guest.Code != http.StatusOK {
		t.Fatalf("the revealed token does not open the order: %d %s", guest.Code, guest.Body.String())
	}

	// And the link already in the customer's inbox still works, which is what
	// separates revealing from re-issuing.
	again := do(t, app, "GET", "/api/orders/"+order.Number+"?token="+order.AccessToken)
	if again.Code != http.StatusOK {
		t.Errorf("the original token stopped working: %d — the reveal rotated it", again.Code)
	}
}

// A bearer credential handed out leaves a row saying who asked for it, and the
// row does not contain the credential.
func TestRevealingTheTokenIsRecorded(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := signInAs(t, app, "owner@example.com", RoleOwner)

	product := simpleProduct(t, app, "TOKEN-2", 800, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	order := result.Order

	rec := doBody(t, app, "POST",
		"/api/admin/orders/"+strconv.FormatInt(order.ID, 10)+"/access-token", "", bearer(owner))
	if rec.Code != http.StatusOK {
		t.Fatalf("reveal = %d: %s", rec.Code, rec.Body.String())
	}

	row := onlyRow(t, auditRows(t, app, "action = $1", AuditOrderTokenReveal))
	if row.ActorEmail != "owner@example.com" {
		t.Errorf("actor = %q, want the operator who asked", row.ActorEmail)
	}
	if row.EntityType != AuditEntityOrder || row.EntityID != strconv.FormatInt(order.ID, 10) {
		t.Errorf("row points at %s/%s, want the order", row.EntityType, row.EntityID)
	}
	if !strings.Contains(row.Summary, order.Number) {
		t.Errorf("summary = %q, want it to name the order", row.Summary)
	}
	blob, err := json.Marshal(row)
	if err != nil {
		t.Fatalf("marshal row: %v", err)
	}
	if strings.Contains(string(blob), order.AccessToken) {
		t.Errorf("the token itself reached the audit trail, which nothing can delete from: %s", blob)
	}

	// Read twice, recorded twice: the row is the only control over a credential
	// leaving the building, so it cannot be an at-most-once record.
	if rec := doBody(t, app, "POST",
		"/api/admin/orders/"+strconv.FormatInt(order.ID, 10)+"/access-token", "",
		bearer(owner)); rec.Code != http.StatusOK {
		t.Fatalf("second reveal = %d: %s", rec.Code, rec.Body.String())
	}
	if rows := auditRows(t, app, "action = $1", AuditOrderTokenReveal); len(rows) != 2 {
		t.Errorf("%d rows after two reveals, want 2", len(rows))
	}
}

// Reading an order is what every role does; handing out the key to it is not.
func TestRevealingTheTokenNeedsOrdersWrite(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	staff := signInAs(t, app, "staff@example.com", RoleStaff)

	product := simpleProduct(t, app, "TOKEN-3", 400, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	path := "/api/admin/orders/" + strconv.FormatInt(result.Order.ID, 10) + "/access-token"

	// Staff hold orders.write by default, and should: taking the call from the
	// customer who lost their link is their job.
	if rec := doBody(t, app, "POST", path, "", bearer(staff)); rec.Code != http.StatusOK {
		t.Fatalf("staff reveal = %d: %s", rec.Code, rec.Body.String())
	}

	narrowed := []Right{}
	for _, r := range DefaultRightsOf(RoleStaff) {
		if r != RightOrdersWrite {
			narrowed = append(narrowed, r)
		}
	}
	if _, err := app.Roles().Set(ctx, RoleStaff, narrowed, nil); err != nil {
		t.Fatalf("narrow staff: %v", err)
	}

	rec := doBody(t, app, "POST", path, "", bearer(staff))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("narrowed staff = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), string(RightOrdersWrite)) {
		t.Errorf("the refusal does not name orders.write: %s", rec.Body.String())
	}
	// Still able to read the order itself, which is what makes the split
	// meaningful rather than decorative.
	if rec := do(t, app, "GET", "/api/admin/orders/"+strconv.FormatInt(result.Order.ID, 10),
		bearer(staff)); rec.Code != http.StatusOK {
		t.Errorf("the same operator lost the order read: %d", rec.Code)
	}
}

// Still not on any order read. The route exists so the token can be recovered
// deliberately, not so it starts riding along on every listing.
func TestTheOrderReadStillCarriesNoToken(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "TOKEN-4", 900, 5)
	cart := newCart(t, app)
	addToCart(t, app, cart.Token, product.DefaultVariant().ID, 1)
	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	id := strconv.FormatInt(result.Order.ID, 10)

	for _, path := range []string{"/api/admin/orders", "/api/admin/orders/" + id} {
		rec := do(t, app, "GET", path, withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d: %s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), result.Order.AccessToken) {
			t.Errorf("%s now carries the access token", path)
		}
	}

	if rec := doBody(t, app, "POST", "/api/admin/orders/999999/access-token", "",
		withAdmin); rec.Code != http.StatusNotFound {
		t.Errorf("reveal on a missing order = %d, want 404", rec.Code)
	}
}
