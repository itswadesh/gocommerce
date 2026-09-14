package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

// One catalogue, several storefronts.
//
// The decision everything else hangs off is what an *unpublished* product
// means, and it is asserted first because getting it the other way round would
// empty every existing catalogue the moment a store created its first channel.
// A product with no publication rows is in EVERY channel, not none: channels
// are a narrowing an operator opts into, product by product, not a gate they
// must walk the whole catalogue through before anything sells again.
//
// The second property is that a store with no channels at all behaves exactly
// as it did before this existed, and pays nothing for it — the same bargain the
// translator makes.
func TestChannelsAreOptInAndBackwardCompatible(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	a := simpleProduct(t, app, "CH-A", 1000, 10)
	b := simpleProduct(t, app, "CH-B", 1000, 10)

	// No channels configured: the public listing is the whole catalogue.
	if got := publicProductIDs(t, app, ""); len(got) != 2 {
		t.Fatalf("with no channels the listing returned %d products, want 2", len(got))
	}

	web, err := app.Channels().Create(ctx, ChannelInput{Code: "web", Name: "Web"})
	if err != nil {
		t.Fatalf("create web: %v", err)
	}
	wholesale, err := app.Channels().Create(ctx, ChannelInput{Code: "wholesale", Name: "Wholesale"})
	if err != nil {
		t.Fatalf("create wholesale: %v", err)
	}

	// Channels now exist and nothing has been published. Every product must
	// still be in every channel, or creating a channel just emptied the shop.
	for _, code := range []string{"web", "wholesale"} {
		if got := publicProductIDs(t, app, code); len(got) != 2 {
			t.Errorf("channel %q sees %d products, want both — an unpublished product is in every channel",
				code, len(got))
		}
	}

	// Publishing narrows: A becomes web-only the moment it names a channel.
	if err := app.Channels().Publish(ctx, a.ID, []int64{web.ID}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if got := publicProductIDs(t, app, "web"); !hasID(got, a.ID) || !hasID(got, b.ID) {
		t.Errorf("web sees %v, want both A (published there) and B (published nowhere)", got)
	}
	if got := publicProductIDs(t, app, "wholesale"); hasID(got, a.ID) {
		t.Errorf("wholesale sees A, which is published to web only")
	}
	if got := publicProductIDs(t, app, "wholesale"); !hasID(got, b.ID) {
		t.Errorf("wholesale lost B, which names no channel and so belongs to all of them")
	}

	_ = wholesale
}

// A channel code nobody recognises is refused rather than quietly served the
// default storefront's catalogue: a typo in a storefront's configuration should
// fail loudly at the first request, not sell the wrong catalogue for a week.
func TestUnknownChannelIsRefused(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	simpleProduct(t, app, "CH-X", 1000, 5)

	if _, err := app.Channels().Create(ctx, ChannelInput{Code: "web", Name: "Web"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	rec := do(t, app, http.MethodGet, "/api/products?channel=typo")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown channel = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// An inactive channel sells nothing. Deactivating is how a storefront is taken
// down without deleting what it was, so it must be a refusal rather than an
// empty catalogue somebody mistakes for a broken import.
func TestInactiveChannelIsRefused(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	simpleProduct(t, app, "CH-Y", 1000, 5)

	ch, err := app.Channels().Create(ctx, ChannelInput{Code: "pop-up", Name: "Pop-up"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	off := false
	if _, err := app.Channels().Update(ctx, ch.ID, ChannelPatch{Active: &off}); err != nil {
		t.Fatalf("deactivate: %v", err)
	}
	rec := do(t, app, http.MethodGet, "/api/products?channel=pop-up")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an inactive channel = %d, want 400: %s", rec.Code, rec.Body)
	}
}

// Exactly one channel is the default, and making a second one default moves it
// rather than producing two — a store with two defaults has no default.
func TestExactlyOneDefaultChannel(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	web, err := app.Channels().Create(ctx, ChannelInput{Code: "web", Name: "Web", Default: true})
	if err != nil {
		t.Fatalf("create web: %v", err)
	}
	pos, err := app.Channels().Create(ctx, ChannelInput{Code: "pos", Name: "Till", Default: true})
	if err != nil {
		t.Fatalf("create pos: %v", err)
	}

	all, err := app.Channels().List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defaults := 0
	for _, c := range all {
		if c.Default {
			defaults++
			if c.ID != pos.ID {
				t.Errorf("the default is %q, want the one most recently set", c.Code)
			}
		}
	}
	if defaults != 1 {
		t.Errorf("%d channels are default, want exactly 1", defaults)
	}
	_ = web
}

// Price lists reach into channels, which is what makes "the same product costs
// less on wholesale" expressible without a second catalogue.
func TestPriceListScopedToChannel(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "CH-PRICE", 10000, 50)
	variant := product.DefaultVariant().ID

	web, err := app.Channels().Create(ctx, ChannelInput{Code: "web", Name: "Web", Default: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	trade, err := app.Channels().Create(ctx, ChannelInput{Code: "wholesale", Name: "Wholesale"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	list, err := app.Pricing().CreateList(ctx, PriceListInput{
		Name: "Wholesale rate", ChannelID: &trade.ID,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := app.Pricing().SetPrice(ctx, list.ID,
		PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: 6000}); err != nil {
		t.Fatalf("price: %v", err)
	}

	// On the wholesale channel the list applies; on the web one it does not,
	// and a list scoped to a channel must never leak to a cart from another.
	if got, _ := app.Pricing().PriceInChannel(ctx, variant, 1, "", trade.ID); got != 6000 {
		t.Errorf("wholesale price = %d, want 6000", got)
	}
	if got, _ := app.Pricing().PriceInChannel(ctx, variant, 1, "", web.ID); got != 10000 {
		t.Errorf("web price = %d, want the catalogue 10000", got)
	}
	// And with no channel in play at all — a store that has not adopted them —
	// a channel-scoped list is not something anybody should be charged.
	if got, _ := app.Pricing().PriceFor(ctx, variant, 1, ""); got != 10000 {
		t.Errorf("unscoped price = %d, want the catalogue 10000", got)
	}
}

// A cart remembers which storefront opened it, and the order keeps it. Without
// that, "how did wholesale do last month" has no answer and the channel-scoped
// price could not be resolved at checkout either.
func TestCartAndOrderCarryTheChannel(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	product := simpleProduct(t, app, "CH-CART", 10000, 50)
	variant := product.DefaultVariant().ID

	trade, err := app.Channels().Create(ctx, ChannelInput{Code: "wholesale", Name: "Wholesale"})
	if err != nil {
		t.Fatalf("channel: %v", err)
	}
	list, err := app.Pricing().CreateList(ctx, PriceListInput{
		Name: "Wholesale rate", ChannelID: &trade.ID,
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := app.Pricing().SetPrice(ctx, list.ID,
		PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: 6000}); err != nil {
		t.Fatalf("price: %v", err)
	}

	cart, err := app.Cart().CreateInChannel(ctx, "", trade.Code)
	if err != nil {
		t.Fatalf("cart: %v", err)
	}
	if cart.ChannelCode != "wholesale" {
		t.Errorf("cart channel = %q, want wholesale", cart.ChannelCode)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, variant, 1); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, err := app.Cart().GetByToken(ctx, cart.Token)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Lines[0].UnitPrice.AmountMinor != 6000 {
		t.Errorf("line = %d, want the channel's 6000", got.Lines[0].UnitPrice.AmountMinor)
	}

	result, err := app.Order().Checkout(ctx, CodeCOD, checkoutInput(cart.Token), "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	if result.Order.ChannelCode != "wholesale" {
		t.Errorf("order channel = %q, want wholesale", result.Order.ChannelCode)
	}
	if result.Order.Lines[0].UnitPrice.AmountMinor != 6000 {
		t.Errorf("order line = %d, want 6000", result.Order.Lines[0].UnitPrice.AmountMinor)
	}
}

// ------------------------------------------------------------------ helpers

func publicProductIDs(t *testing.T, app *App, channel string) []int64 {
	t.Helper()
	target := "/api/products?limit=100"
	if channel != "" {
		target += "&channel=" + channel
	}
	rec := do(t, app, http.MethodGet, target)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", target, rec.Code, rec.Body)
	}
	var body struct {
		Data []struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out := make([]int64, 0, len(body.Data))
	for _, p := range body.Data {
		out = append(out, p.ID)
	}
	return out
}

func hasID(ids []int64, id int64) bool {
	for _, got := range ids {
		if got == id {
			return true
		}
	}
	return false
}
