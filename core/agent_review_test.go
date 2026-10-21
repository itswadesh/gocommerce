package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// The review of agent keys (PR #69): what an agent could still reach, what
// outlived its issuer, and what a filter could confirm.

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
