package wishlist

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// start mints a list and returns it.
func start(t *testing.T, app *gocommerce.App, email string) List {
	t.Helper()
	body := map[string]string{"email": email}
	rec := gctest.Request(t, app, http.MethodPost, "/x/wishlist", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("start a wishlist = %d %s", rec.Code, rec.Body)
	}
	var l List
	gctest.DecodeData(t, rec, &l)
	return l
}

func save(t *testing.T, app *gocommerce.App, token string, in ItemInput) (*List, int) {
	t.Helper()
	rec := gctest.Request(t, app, http.MethodPost, "/x/wishlist/"+token+"/items", in)
	if rec.Code != http.StatusCreated {
		return nil, rec.Code
	}
	var l List
	gctest.DecodeData(t, rec, &l)
	return &l, rec.Code
}

// The module's reason to exist: a guest saves something, sees what it costs
// and whether it is in stock, and the shop sees what is wanted.
func TestAGuestSavesAndTheShopSeesWhatIsWanted(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	mug := gctest.CreateProduct(t, app, "WL-MUG", 1200, 5)
	soldOut := gctest.CreateProduct(t, app, "WL-GONE", 3400, 0)

	list := start(t, app, "")
	if list.Token == "" || len(list.Items) != 0 {
		t.Fatalf("a new list = %+v, want a token and nothing on it", list)
	}

	saved, code := save(t, app, list.Token, ItemInput{ProductID: mug.ID})
	if code != http.StatusCreated {
		t.Fatalf("save a product = %d", code)
	}
	if len(saved.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(saved.Items))
	}
	it := saved.Items[0]
	if it.Title != mug.Title || it.Slug != mug.Slug {
		t.Errorf("item = %+v, want the product beside it so a page can be drawn", it)
	}
	if it.Price == nil || it.Price.AmountMinor != 1200 {
		t.Errorf("price = %+v, want the product's own", it.Price)
	}
	if !it.InStock || it.Available != 5 {
		t.Errorf("availability = %d in_stock=%v, want what the store could sell", it.Available, it.InStock)
	}

	// Saving it again is what somebody does when they forget: not an error
	// and not a second row.
	again, code := save(t, app, list.Token, ItemInput{ProductID: mug.ID})
	if code != http.StatusCreated || len(again.Items) != 1 {
		t.Errorf("saving twice = %d with %d items, want 201 and still one", code, len(again.Items))
	}

	// A variant has to belong to the product it is saved under.
	if _, code := save(t, app, list.Token, ItemInput{ProductID: mug.ID, VariantID: &soldOut.Variants[0].ID}); code != http.StatusNotFound {
		t.Errorf("a variant of another product = %d, want 404", code)
	}
	if _, code := save(t, app, list.Token, ItemInput{ProductID: 999999}); code != http.StatusNotFound {
		t.Errorf("an unknown product = %d, want 404", code)
	}
	if _, code := save(t, app, list.Token, ItemInput{}); code != http.StatusBadRequest {
		t.Errorf("no product_id = %d, want 400", code)
	}

	// A second shopper wants the mug and the sold-out one, and leaves an
	// address — which is what makes them reachable when it comes back.
	second := start(t, app, "Asha@Example.com ")
	if second.Email != "asha@example.com" {
		t.Errorf("email = %q, want it lower-cased and trimmed", second.Email)
	}
	save(t, app, second.Token, ItemInput{ProductID: mug.ID})
	save(t, app, second.Token, ItemInput{ProductID: soldOut.ID})

	// The ranking: the mug is on two lists, the sold-out one on a single
	// list whose shopper can be written to.
	var wanted []Wanted
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/wishlist/wanted", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("wanted = %d %s", rec.Code, rec.Body)
	}
	gctest.DecodeData(t, rec, &wanted)
	if len(wanted) != 2 {
		t.Fatalf("wanted = %d products, want 2", len(wanted))
	}
	if wanted[0].ProductID != mug.ID || wanted[0].Lists != 2 {
		t.Errorf("first = %+v, want the mug on two lists", wanted[0])
	}
	if wanted[0].Waiting != 1 {
		t.Errorf("waiting on the mug = %d, want the one list that gave an email", wanted[0].Waiting)
	}

	// The question the screen exists to answer: wanted, and out of stock.
	rec = gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/wishlist/wanted?out_of_stock=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("out of stock = %d %s", rec.Code, rec.Body)
	}
	gctest.DecodeData(t, rec, &wanted)
	if len(wanted) != 1 || wanted[0].ProductID != soldOut.ID {
		t.Fatalf("out of stock = %+v, want only the one nobody can buy", wanted)
	}
	if wanted[0].InStock || wanted[0].Waiting != 1 {
		t.Errorf("sold-out entry = %+v, want it out of stock with one shopper reachable", wanted[0])
	}

	// Taking it off, and taking it off twice.
	off := saved.Items[0].ID
	rec = gctest.Request(t, app, http.MethodDelete, "/x/wishlist/"+list.Token+"/items/"+strconv.FormatInt(off, 10), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body)
	}
	var after List
	gctest.DecodeData(t, rec, &after)
	if len(after.Items) != 0 {
		t.Errorf("items after removing = %d, want none", len(after.Items))
	}
	rec = gctest.Request(t, app, http.MethodDelete, "/x/wishlist/"+list.Token+"/items/"+strconv.FormatInt(off, 10), nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("removing it twice = %d, want 404", rec.Code)
	}

	// An unknown token is not a list, whatever is asked of it.
	if rec := gctest.Request(t, app, http.MethodGet, "/x/wishlist/nope", nil); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown token = %d, want 404", rec.Code)
	}
}

// The operator's list never carries the shopper's token.
func TestTheAdminListingKeepsTheTokenSecret(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	product := gctest.CreateProduct(t, app, "WL-SECRET", 500, 3)
	list := start(t, app, "keeper@example.com")
	save(t, app, list.Token, ItemInput{ProductID: product.ID})

	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/wishlist/lists", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("lists = %d %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); strings.Contains(body, list.Token) {
		t.Fatal("the admin listing must never carry a shopper's token")
	}
	var lists []struct {
		Email string `json:"email"`
		Items int    `json:"items"`
	}
	gctest.DecodeData(t, rec, &lists)
	if len(lists) != 1 || lists[0].Email != "keeper@example.com" || lists[0].Items != 1 {
		t.Fatalf("lists = %+v, want one list of one item with its email", lists)
	}

	// Narrowed to the ones the shop could actually write to.
	start(t, app, "")
	rec = gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/wishlist/lists?with_email=1", nil)
	gctest.DecodeData(t, rec, &lists)
	if len(lists) != 1 {
		t.Errorf("with_email = %d lists, want only the one that gave an address", len(lists))
	}
}

// An email arrives later, when the shopper decides to be told.
func TestAnEmailCanBeAddedAfterwards(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	list := start(t, app, "")

	rec := gctest.Request(t, app, http.MethodPatch, "/x/wishlist/"+list.Token, map[string]string{"email": "later@example.com"})
	if rec.Code != http.StatusOK {
		t.Fatalf("set the email = %d %s", rec.Code, rec.Body)
	}
	var out List
	gctest.DecodeData(t, rec, &out)
	if out.Email != "later@example.com" {
		t.Errorf("email = %q", out.Email)
	}
	if rec := gctest.Request(t, app, http.MethodPatch, "/x/wishlist/"+list.Token, map[string]string{"email": "not an address"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a bad address = %d, want 400", rec.Code)
	}
}

// Switched off, the storefront has no wishlist — and the operator still
// has the screen, which is where it is switched back on.
func TestThePluginGatesTheStorefrontOnly(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := t.Context()

	off := false
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &off}); err != nil {
		t.Fatalf("switch off: %v", err)
	}
	if rec := gctest.Request(t, app, http.MethodPost, "/x/wishlist", nil); rec.Code != http.StatusNotFound {
		t.Errorf("starting a list while off = %d, want 404", rec.Code)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/wishlist/wanted", nil); rec.Code != http.StatusOK {
		t.Errorf("the admin ranking while off = %d, want 200", rec.Code)
	}
}

func TestRoutesDeclareRightsAndAreDocumented(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "wishlist")
	gctest.AssertSpecCoversModuleRoutes(t, app, "wishlist")
}
