package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The review of agent keys (PR #69): what an agent could still reach, what
// outlived its issuer, and what a filter could confirm.

// wantAgentRefused asserts the 403 an agent key gets where it can never go.
func wantAgentRefused(t *testing.T, rec interface {
	Result() *http.Response
}, body, what string) {
	t.Helper()
	if code := rec.Result().StatusCode; code != http.StatusForbidden || !strings.Contains(body, "agent_key_refused") {
		t.Errorf("%s = %d, want 403 agent_key_refused: %s", what, code, body)
	}
}

// The notification log is the full text of what the store sent, filled in,
// and an agent reached it with orders.read alone. So it is closed to an agent
// whatever it holds. The templates, which are wording, stay readable.
func TestAnAgentCannotReadTheNotificationLog(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)
	_, agent := newAgentKey(t, app, owner, "Order desk", RightOrdersRead, RightNotificationsRead)
	_, staff, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "Staff script", Role: RoleStaff}, owner)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/admin/notifications",
		"/api/admin/notifications/1",
		"/api/admin/notifications/1/preview",
	} {
		rec := do(t, app, http.MethodGet, path, bearer(agent))
		wantAgentRefused(t, rec, rec.Body.String(), "agent GET "+path)
	}
	rec := doBody(t, app, http.MethodPost, "/api/admin/notifications/1/resend", `{}`, bearer(agent))
	wantAgentRefused(t, rec, rec.Body.String(), "agent resend")

	if rec := do(t, app, http.MethodGet, "/api/admin/notifications/templates", bearer(agent)); rec.Code != http.StatusOK {
		t.Errorf("agent reading the templates = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/notifications", bearer(staff)); rec.Code != http.StatusOK {
		t.Errorf("an integration key reading the log = %d, want 200: %s", rec.Code, rec.Body)
	}
}

// An agent key holds what its issuer still holds: its list, cut to the
// issuer's current rights in this store on every request. An issuer who has
// left the store, or whose account is gone, takes the key with them.
func TestAnAgentKeyLosesWhatItsIssuerLoses(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	demoted := person(t, app, "demoted@example.com", RoleOwner)
	removed := person(t, app, "removed@example.com", RoleOwner)
	deleted := person(t, app, "deleted@example.com", RoleOwner)
	person(t, app, "stays@example.com", RoleOwner)

	_, first := newAgentKey(t, app, demoted, "Desk", RightOrdersRead, RightVendorsRead)
	_, second := newAgentKey(t, app, removed, "Desk two", RightOrdersRead)
	_, third := newAgentKey(t, app, deleted, "Desk three", RightOrdersRead)

	who, ok := app.APIKeys().Resolve(ctx, first)
	if !ok || !slices.Equal(who.Rights, []Right{RightOrdersRead, RightVendorsRead}) {
		t.Fatalf("before = %v, %v", who, ok)
	}

	// Demoted to manager, who does not carry vendors.read.
	if _, err := app.Superusers().SetRole(ctx, demoted.ID, RoleManager); err != nil {
		t.Fatal(err)
	}
	who, ok = app.APIKeys().Resolve(ctx, first)
	if !ok || !slices.Equal(who.Rights, []Right{RightOrdersRead}) {
		t.Errorf("after the issuer is demoted = %v (%v), want orders.read alone", who.Rights, ok)
	}
	// The store re-cuts manager without orders.read: the key keeps nothing.
	if _, err := app.Roles().Set(ctx, RoleManager, []Right{RightCatalogRead, RightCustomersRead}, nil); err != nil {
		t.Fatal(err)
	}
	who, ok = app.APIKeys().Resolve(ctx, first)
	if !ok || len(who.Rights) != 0 {
		t.Errorf("after manager loses orders.read = %v (%v), want no rights", who.Rights, ok)
	}

	// Removed from the store, the account still alive elsewhere.
	if _, err := app.DB().ExecContext(ctx,
		`DELETE FROM store_members WHERE superuser_id = $1`, removed.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.APIKeys().Resolve(ctx, second); ok {
		t.Error("a key whose issuer left the store still authenticates")
	}

	// The account deleted: created_by becomes null.
	if err := app.Superusers().Delete(ctx, deleted.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := app.APIKeys().Resolve(ctx, third); ok {
		t.Error("a key whose issuer was deleted still authenticates")
	}
}

// The merchant's free-form fields — an order's and a line's metadata, the
// shop's note on an order, a cart's metadata, a parcel's label link — are not
// fields the mask can read, so an agent is given none of them.
func TestAnAgentDoesNotSeeTheMerchantsFreeText(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	owner := person(t, app, "owner@example.com", RoleOwner)
	ownerToken := signInAs(t, app, "owner2@example.com", RoleOwner)
	_, agent := newAgentKey(t, app, owner, "Order desk", RightOrdersRead, RightCartsRead)
	_, staff, err := app.APIKeys().Create(ctx, APIKeyInput{Name: "Staff script", Role: RoleStaff}, owner)
	if err != nil {
		t.Fatal(err)
	}

	order := paidOrder(t, app, "AGENT-META", 1500, 3)
	id := strconv.FormatInt(order.ID, 10)
	if _, err := app.DB().ExecContext(ctx, `UPDATE order_lines SET metadata = '{"engraving":"For Jane, +31 20 555 1242"}' WHERE order_id = $1`, order.ID); err != nil {
		t.Fatal(err)
	}
	if rec := doBody(t, app, http.MethodPatch, "/api/admin/orders/"+id,
		`{"metadata":{"notes":"Call Jane on +31 20 555 1242","gift":"leave with neighbour at 4 Elm St"}}`,
		bearer(ownerToken)); rec.Code != http.StatusOK {
		t.Fatalf("write the note = %d: %s", rec.Code, rec.Body)
	}
	cart := newCart(t, app)
	if _, err := app.DB().ExecContext(ctx, `UPDATE carts SET metadata = '{"note":"ring Jane"}' WHERE token = $1`, cart.Token); err != nil {
		t.Fatal(err)
	}
	var cartID int64
	if err := app.DB().QueryRowContext(ctx, `SELECT id FROM carts WHERE token = $1`, cart.Token).Scan(&cartID); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		"/api/admin/orders/" + id,
		"/api/admin/orders",
		"/api/admin/orders/" + id + "/timeline",
		"/api/admin/carts/" + strconv.FormatInt(cartID, 10),
	} {
		body := do(t, app, http.MethodGet, path, bearer(agent)).Body.String()
		for _, secret := range []string{"Call Jane", "4 Elm St", "For Jane", "ring Jane"} {
			if strings.Contains(body, secret) {
				t.Errorf("%s: the agent read %q: %s", path, secret, body)
			}
		}
	}
	full := do(t, app, http.MethodGet, "/api/admin/orders/"+id, bearer(staff)).Body.String()
	if !strings.Contains(full, "Call Jane") || !strings.Contains(full, "For Jane") {
		t.Errorf("an integration key lost the metadata: %s", full)
	}
	var shaped struct {
		Data struct {
			Metadata map[string]any `json:"metadata"`
		} `json:"data"`
	}
	_ = json.Unmarshal(do(t, app, http.MethodGet, "/api/admin/orders/"+id, bearer(agent)).Body.Bytes(), &shaped)
	if shaped.Data.Metadata == nil {
		t.Error("an agent's order has metadata null; want an empty object, so a client reading it does not break")
	}
}
