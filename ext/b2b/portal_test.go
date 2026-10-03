package b2b

import (
	"net/http"
	"strconv"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The trade portal (D77) shows a buyer one of the company's orders with what
// was in it, by the rule the order list applies — and never the order's
// access token, which would let whoever held it act on the order as its
// shopper.
func TestABuyerReadsOneOfTheCompanysOrdersWithItsLines(t *testing.T) {
	f := newFixture(t)
	_, bossTok := f.member(t, "boss@acme.test", RoleAdmin)
	_, juniorTok := f.member(t, "junior@acme.test", RoleBuyer)
	_, colleagueTok := f.member(t, "colleague@acme.test", RoleBuyer)

	placed := f.checkout(t, juniorTok, f.cart(t, 2), map[string]any{"po_number": "PO-77"})
	if placed.code != http.StatusCreated {
		t.Fatalf("checkout = %d: %s", placed.code, placed.body)
	}
	var first struct {
		Order gocommerce.Order `json:"order"`
	}
	decodeBody(t, placed.body, &first)
	path := "/x/b2b/orders/" + strconv.FormatInt(first.Order.ID, 10)

	for who, tok := range map[string]string{"the buyer who placed it": juniorTok, "an admin": bossTok} {
		rec := gctest.SessionRequest(t, f.app, tok, http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s reading it = %d: %s", who, rec.Code, rec.Body)
		}
		var got BuyerOrder
		gctest.DecodeData(t, rec, &got)
		if got.CompanyOrder == nil || got.PONumber != "PO-77" || !got.OnAccount || got.DueAt == nil {
			t.Errorf("%s: ledger = %+v; want PO-77, on account, with a due date", who, got.CompanyOrder)
		}
		if got.Order == nil || len(got.Order.Lines) != 1 || got.Order.Lines[0].Quantity != 2 ||
			got.Order.Lines[0].UnitPrice.AmountMinor != 8000 {
			t.Fatalf("%s: order = %+v; want one line of 2 at the company's 8000", who, got.Order)
		}
		if got.Order.AccessToken != "" {
			t.Errorf("%s: the order carries its access token", who)
		}
	}

	if rec := gctest.SessionRequest(t, f.app, colleagueTok, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
		t.Errorf("a buyer reading a colleague's order = %d, want 404: a buyer sees their own", rec.Code)
	}
	otherCo := f.dealer(t, "Other Co", StatusActive)
	_, strangerTok := f.join(t, otherCo.ID, "buyer@other.test", RoleAdmin)
	if rec := gctest.SessionRequest(t, f.app, strangerTok, http.MethodGet, path, nil); rec.Code != http.StatusNotFound {
		t.Errorf("another company reading it = %d, want 404", rec.Code)
	}
	_, nobodyTok := f.account(t, "nobody@else.test", true)
	if rec := gctest.SessionRequest(t, f.app, nobodyTok, http.MethodGet, path, nil); rec.Code != http.StatusForbidden {
		t.Errorf("an account with no company = %d, want 403", rec.Code)
	}
}

// Repeating an order with something already in the basket adds to that
// basket rather than opening a second one beside it; without a basket named,
// it opens one, as it always did.
func TestARepeatOrderCanFillTheBasketTheBuyerAlreadyHas(t *testing.T) {
	f := newFixture(t)
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)
	other := gctest.CreateProduct(t, f.app, "B2B-OTHER", 5000, 100).Variants[0].ID

	placed := f.checkout(t, tok, f.cart(t, 2), nil)
	if placed.code != http.StatusCreated {
		t.Fatalf("checkout = %d: %s", placed.code, placed.body)
	}
	var first struct {
		Order gocommerce.Order `json:"order"`
	}
	decodeBody(t, placed.body, &first)
	path := "/x/b2b/orders/" + strconv.FormatInt(first.Order.ID, 10) + "/reorder"

	basket := f.basket(t, item{other, 1})
	rec := gctest.SessionRequest(t, f.app, tok, http.MethodPost, path, map[string]any{"cart_id": basket})
	if rec.Code != http.StatusCreated {
		t.Fatalf("reorder into the basket = %d: %s", rec.Code, rec.Body)
	}
	var fill CartFill
	gctest.DecodeData(t, rec, &fill)
	if fill.Cart.Token != basket {
		t.Fatalf("filled basket %q, want the one named, %q", fill.Cart.Token, basket)
	}
	qty := map[string]int{}
	for _, l := range fill.Cart.Lines {
		qty[l.SKU] = l.Quantity
	}
	if qty["B2B-OTHER"] != 1 || qty["B2B-WIDGET"] != 2 {
		t.Errorf("basket = %v; want what it held and the order's two widgets", qty)
	}

	rec = gctest.SessionRequest(t, f.app, tok, http.MethodPost, path, nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("reorder with no basket = %d: %s", rec.Code, rec.Body)
	}
	var fresh CartFill
	gctest.DecodeData(t, rec, &fresh)
	if fresh.Cart.Token == basket || len(fresh.Cart.Lines) != 1 {
		t.Errorf("with no basket named = %+v; want a new basket holding only the order's line", fresh.Cart)
	}

	// The route took no body before cart_id, and a client that sent one
	// anyway is answered as it always was.
	rec = gctest.SessionRequest(t, f.app, tok, http.MethodPost, path, map[string]any{"note": "same again"})
	if rec.Code != http.StatusCreated {
		t.Errorf("reorder with a field it does not know = %d, want 201 as before: %s", rec.Code, rec.Body)
	}
}
