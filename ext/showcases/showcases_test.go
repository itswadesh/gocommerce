package showcases

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/ext/translations"
	"github.com/itswadesh/gocommerce/gctest"
)

const ytLink = "https://youtu.be/dQw4w9WgXcQ"

func itoa(v int64) string { return strconv.FormatInt(v, 10) }

func admin(t *testing.T, app *gocommerce.App, method, target string, body any, want int) Showcase {
	t.Helper()
	rec := gctest.AdminRequest(t, app, method, target, body)
	if rec.Code != want {
		t.Fatalf("%s %s = %d, want %d: %s", method, target, rec.Code, want, rec.Body)
	}
	var s Showcase
	if want < 300 && rec.Code != http.StatusNoContent {
		gctest.DecodeData(t, rec, &s)
	}
	return s
}

func newShowcase(t *testing.T, app *gocommerce.App, title string, extra map[string]any) Showcase {
	t.Helper()
	body := map[string]any{"title": title, "video_url": ytLink}
	for k, v := range extra {
		body[k] = v
	}
	return admin(t, app, http.MethodPost, "/api/admin/x/showcases", body, http.StatusCreated)
}

func public(t *testing.T, app *gocommerce.App, target string, want int) *httptest.ResponseRecorder {
	t.Helper()
	rec := gctest.Request(t, app, http.MethodGet, target, nil)
	if rec.Code != want {
		t.Fatalf("GET %s = %d, want %d: %s", target, rec.Code, want, rec.Body)
	}
	return rec
}

func TestModuleContract(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "showcases")
	gctest.AssertSpecCoversModuleRoutes(t, app, "showcases")
}

// The whole of it once: paste a link, attach products, feature one, publish,
// and a buyer reads the video, the store and the products — and nothing a
// buyer should not see.
func TestAShowcaseIsMadePublishedAndRead(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ring := gctest.CreateProduct(t, app, "SC-RING", 250000, 3)
	chain := gctest.CreateProduct(t, app, "SC-CHAIN", 180000, 0)
	if _, err := app.Profile().Set(context.Background(), gocommerce.StoreProfile{Name: "Varni Jewels"}, nil); err != nil {
		t.Fatal(err)
	}

	s := newShowcase(t, app, "Diwali Gold, 2026!", nil)
	if s.Status != StatusDraft || s.Slug != "diwali-gold-2026" || s.Label != LabelVideo || s.Video.Provider != ProviderYouTube {
		t.Fatalf("a new showcase = %+v", s)
	}
	public(t, app, "/x/showcases/diwali-gold-2026", http.StatusNotFound) // a draft is nobody's

	path := "/api/admin/x/showcases/" + itoa(s.ID)
	s = admin(t, app, http.MethodPatch, path, map[string]any{
		"product_ids": []int64{chain.ID, ring.ID, chain.ID}, "featured_product_id": ring.ID, "status": StatusPublished,
	}, http.StatusOK)
	if len(s.ProductIDs) != 2 || s.ProductIDs[0] != chain.ID || s.PublishedAt == nil || len(s.Products) != 2 {
		t.Fatalf("after publishing = %+v", s)
	}

	rec := public(t, app, "/x/showcases/diwali-gold-2026", http.StatusOK)
	body := rec.Body.String()
	for _, leak := range []string{`"cost"`, `"stock_on_hand"`, `"metadata"`, `"url":`, `"status"`, "youtu.be"} {
		if strings.Contains(body, leak) {
			t.Errorf("the public showcase carries %s: %s", leak, body)
		}
	}
	var got publicShowcase
	gctest.DecodeData(t, rec, &got)
	if got.Store != "Varni Jewels" {
		t.Errorf("store = %q, want the shop's own name", got.Store)
	}
	if got.Video.EmbedURL != "https://www.youtube-nocookie.com/embed/dQw4w9WgXcQ" || got.Label != LabelVideo {
		t.Errorf("video = %+v, label %q", got.Video, got.Label)
	}
	if got.FeaturedProductID == nil || *got.FeaturedProductID != ring.ID || len(got.Products) != 2 {
		t.Fatalf("featured %v, products %+v", got.FeaturedProductID, got.Products)
	}
	if p := got.Products[0]; p.ID != chain.ID || p.Available || p.Price == nil || p.Price.AmountMinor != 180000 || p.VariantID == 0 {
		t.Errorf("the out-of-stock chain = %+v", p)
	}
	if !got.Products[1].Available {
		t.Errorf("the ring in stock reads unavailable")
	}
}

// Publishing needs something to sell, and a featured product has to be one
// the showcase shows — removing it from the list stops featuring it.
func TestAShowcaseSellsWhatItShows(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ring := gctest.CreateProduct(t, app, "SC-R2", 1000, 3)
	other := gctest.CreateProduct(t, app, "SC-O2", 1000, 3)
	s := newShowcase(t, app, "Rings", nil)
	path := "/api/admin/x/showcases/" + itoa(s.ID)

	admin(t, app, http.MethodPatch, path, map[string]any{"status": StatusPublished}, http.StatusBadRequest)
	admin(t, app, http.MethodPatch, path, map[string]any{"product_ids": []int64{ring.ID}, "featured_product_id": other.ID}, http.StatusBadRequest)
	admin(t, app, http.MethodPatch, path, map[string]any{"product_ids": []int64{ring.ID, 999999}}, http.StatusBadRequest)
	s = admin(t, app, http.MethodPatch, path, map[string]any{"product_ids": []int64{ring.ID, other.ID}, "featured_product_id": other.ID}, http.StatusOK)
	if s.FeaturedProductID == nil {
		t.Fatal("a product in the list could not be featured")
	}
	s = admin(t, app, http.MethodPatch, path, map[string]any{"product_ids": []int64{ring.ID}}, http.StatusOK)
	if s.FeaturedProductID != nil {
		t.Errorf("a product taken out of the list is still featured")
	}
	admin(t, app, http.MethodPatch, path, map[string]any{"video_url": "https://www.youtube.com/@shop/live"}, http.StatusBadRequest)
}

// "Live" is only ever what the store said, and only for twelve hours: a link
// to a livestream is not a claim that anything is on now.
func TestLiveIsOnlyWhatTheStoreSays(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	p := gctest.CreateProduct(t, app, "SC-LIVE", 1000, 3)
	s := newShowcase(t, app, "Launch", map[string]any{"product_ids": []int64{p.ID}})
	path := "/api/admin/x/showcases/" + itoa(s.ID)

	admin(t, app, http.MethodPatch, path, map[string]any{"live_state": LiveNow}, http.StatusBadRequest)
	s = admin(t, app, http.MethodPatch, path, map[string]any{"kind": KindLive}, http.StatusOK)
	if s.Label != LabelVideo {
		t.Errorf("a livestream nobody started = %q, want video", s.Label)
	}
	admin(t, app, http.MethodPatch, path, map[string]any{"live_state": LiveScheduled}, http.StatusBadRequest)
	soon := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	s = admin(t, app, http.MethodPatch, path, map[string]any{"starts_at": soon, "live_state": LiveScheduled}, http.StatusOK)
	if s.Label != LabelScheduled {
		t.Errorf("scheduled = %q", s.Label)
	}
	s = admin(t, app, http.MethodPatch, path, map[string]any{"live_state": LiveNow, "status": StatusPublished}, http.StatusOK)
	if s.Label != LabelLive || s.LiveSince == nil {
		t.Fatalf("live = %q since %v", s.Label, s.LiveSince)
	}
	var now struct {
		Label string `json:"label"`
	}
	rec := public(t, app, "/x/showcases/"+s.Slug+"/now", http.StatusOK)
	gctest.DecodeData(t, rec, &now)
	if now.Label != LabelLive || !strings.Contains(rec.Header().Get("Cache-Control"), "max-age=5") {
		t.Errorf("now = %q, cache %q", now.Label, rec.Header().Get("Cache-Control"))
	}

	// A store that forgot to say it ended is not live a day later.
	if _, err := app.DB().ExecContext(context.Background(),
		`UPDATE showcases SET live_since = now() - interval '13 hours' WHERE id = $1`, s.ID); err != nil {
		t.Fatal(err)
	}
	if s = admin(t, app, http.MethodGet, path, nil, http.StatusOK); s.Label != LabelReplay {
		t.Errorf("live for thirteen hours = %q, want replay", s.Label)
	}
	if s = admin(t, app, http.MethodPatch, path, map[string]any{"live_state": LiveEnded}, http.StatusOK); s.Label != LabelReplay || s.LiveSince != nil {
		t.Errorf("ended = %q since %v", s.Label, s.LiveSince)
	}
}

// A collection's showcase reads the collection when shown, so a product added
// to it later appears and one archived goes; and the picked products become a
// collection of their own in one step.
func TestACollectionShowcaseFollowsItsCollection(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()
	a := gctest.CreateProduct(t, app, "SC-CA", 1000, 3)
	b := gctest.CreateProduct(t, app, "SC-CB", 1000, 3)
	col, err := app.Collections().Create(ctx, gocommerce.CollectionInput{Title: "Bridal"})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Collections().SetCollectionProducts(ctx, col.ID, []int64{a.ID}); err != nil {
		t.Fatal(err)
	}
	s := newShowcase(t, app, "Bridal", map[string]any{"collection_id": col.ID, "status": StatusPublished})
	if err := app.Collections().SetCollectionProducts(ctx, col.ID, []int64{a.ID, b.ID}); err != nil {
		t.Fatal(err)
	}
	archived := gocommerce.ProductArchived
	if _, err := app.Products().UpdateProduct(ctx, a.ID, gocommerce.ProductPatch{Status: &archived}); err != nil {
		t.Fatal(err)
	}
	var got publicShowcase
	gctest.DecodeData(t, public(t, app, "/x/showcases/"+s.Slug, http.StatusOK), &got)
	if len(got.Products) != 1 || got.Products[0].ID != b.ID {
		t.Errorf("the collection's showcase = %+v, want only the active product added later", got.Products)
	}

	picks := newShowcase(t, app, "Picks", map[string]any{"product_ids": []int64{b.ID, a.ID}})
	made := admin(t, app, http.MethodPost, "/api/admin/x/showcases/"+itoa(picks.ID)+"/collection",
		map[string]any{"title": "Festive picks"}, http.StatusCreated)
	if made.CollectionID == nil {
		t.Fatal("the new collection is not attached")
	}
	members, _, err := app.Collections().ProductsInCollection(ctx, *made.CollectionID, gocommerce.ProductQuery{})
	if err != nil || len(members) != 2 || members[0].ID != b.ID {
		t.Errorf("the collection made from the picks = %v, %v", members, err)
	}
}

// Each place on the storefront lists the showcases meant for it.
func TestEachPlaceListsItsShowcases(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()
	p := gctest.CreateProduct(t, app, "SC-PL", 1000, 3)
	q := gctest.CreateProduct(t, app, "SC-PQ", 1000, 3)
	col, _ := app.Collections().Create(ctx, gocommerce.CollectionInput{Title: "Edit"})
	_ = app.Collections().SetCollectionProducts(ctx, col.ID, []int64{q.ID})

	newShowcase(t, app, "Home", map[string]any{"product_ids": []int64{p.ID}, "on_home": true, "status": StatusPublished})
	newShowcase(t, app, "Edit", map[string]any{"collection_id": col.ID, "status": StatusPublished})
	newShowcase(t, app, "Draft", map[string]any{"product_ids": []int64{p.ID}, "on_home": true})

	titles := func(target string) string {
		var cards []publicCard
		gctest.DecodeData(t, public(t, app, target, http.StatusOK), &cards)
		var out []string
		for _, c := range cards {
			out = append(out, c.Title)
		}
		return strings.Join(out, ",")
	}
	if got := titles("/x/showcases?on=home"); got != "Home" {
		t.Errorf("home = %q", got)
	}
	if got := titles("/x/showcases?on=product&product=" + itoa(q.ID)); got != "Edit" {
		t.Errorf("q's page = %q, want the collection's showcase", got)
	}
	if got := titles("/x/showcases?on=collection&collection=" + itoa(col.ID)); got != "Edit" {
		t.Errorf("the collection's page = %q", got)
	}
	public(t, app, "/x/showcases?on=product", http.StatusBadRequest)
}

// What a buyer did is counted, nothing about the buyer is kept, and the order
// a showcase's basket became is counted once with its total.
func TestEventsAreCountedAndOrdersCreditedOnce(t *testing.T) {
	mod := New(Config{})
	app := gctest.New(t, mod)
	ctx := context.Background()
	p := gctest.CreateProduct(t, app, "SC-EV", 2500, 5)
	s := newShowcase(t, app, "Events", map[string]any{"product_ids": []int64{p.ID}, "status": StatusPublished})
	events := "/x/showcases/" + s.Slug + "/events"

	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, p.DefaultVariant().ID, 2); err != nil {
		t.Fatal(err)
	}
	for _, ev := range []map[string]any{
		{"type": "view"}, {"type": "view"}, {"type": "click", "product_id": p.ID},
		{"type": "add_to_cart", "product_id": p.ID, "cart": cart.Token},
	} {
		if rec := gctest.Request(t, app, http.MethodPost, events, ev); rec.Code != http.StatusNoContent {
			t.Fatalf("event %v = %d: %s", ev, rec.Code, rec.Body)
		}
	}
	if rec := gctest.Request(t, app, http.MethodPost, events, map[string]any{"type": "purchase"}); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown event = %d, want 400", rec.Code)
	}
	result, err := app.Order().Checkout(ctx, gocommerce.CodeCOD, gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "buyer@example.com", Name: "A Buyer",
		Address: gocommerce.Address{Line1: "1 Test Street", City: "Testville", State: "CA",
			PostalCode: "94117", Country: "US", Phone: "+15555550123"},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	gctest.DrainOutbox(t, app)
	// At least once: the same order.created again is not a second order.
	again, _ := json.Marshal(gocommerce.OrderEvent{OrderID: result.Order.ID, CartID: cartID(t, app, cart.Token),
		TotalMinor: result.Order.Total.AmountMinor})
	if err := mod.onOrderCreated(ctx, gocommerce.Event{Name: gocommerce.EventOrderCreated, Data: again}); err != nil {
		t.Fatal(err)
	}
	got := admin(t, app, http.MethodGet, "/api/admin/x/showcases/"+itoa(s.ID), nil, http.StatusOK)
	st := got.Stats
	if st.Views != 2 || st.Clicks != 1 || st.Adds != 1 || st.Orders != 1 || st.Revenue.AmountMinor != result.Order.Total.AmountMinor {
		t.Errorf("stats = %+v, want 2 views, 1 click, 1 add, 1 order of %d", st, result.Order.Total.AmountMinor)
	}
}

func cartID(t *testing.T, app *gocommerce.App, token string) int64 {
	t.Helper()
	c, err := app.Cart().GetByToken(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	return c.ID
}

// The storefront's events are bounded per address.
func TestEventsAreRateLimited(t *testing.T) {
	app := gctest.New(t, New(Config{EventsPerMinute: 2}))
	p := gctest.CreateProduct(t, app, "SC-RL", 1000, 3)
	s := newShowcase(t, app, "Limited", map[string]any{"product_ids": []int64{p.ID}, "status": StatusPublished})
	events := "/x/showcases/" + s.Slug + "/events"
	for i := 0; i < 2; i++ {
		if rec := gctest.Request(t, app, http.MethodPost, events, map[string]any{"type": "view"}); rec.Code != http.StatusNoContent {
			t.Fatalf("view %d = %d", i, rec.Code)
		}
	}
	rec := gctest.Request(t, app, http.MethodPost, events, map[string]any{"type": "view"})
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("the third view = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// Switched off, the storefront sees nothing and the panel still works.
func TestThePluginSwitchClosesTheStorefront(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	p := gctest.CreateProduct(t, app, "SC-OFF", 1000, 3)
	s := newShowcase(t, app, "Off", map[string]any{"product_ids": []int64{p.ID}, "status": StatusPublished})
	off := false
	if _, err := app.Plugins().Update(context.Background(), pluginKey, gocommerce.PluginPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	public(t, app, "/x/showcases/"+s.Slug, http.StatusNotFound)
	public(t, app, "/x/showcases", http.StatusNotFound)
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/showcases", nil); rec.Code != http.StatusOK {
		t.Errorf("the panel's list with the plugin off = %d, want 200", rec.Code)
	}
}

// Staff see showcases and cannot change them; making a collection also takes
// the collections right.
func TestShowcaseRights(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)
	if rec := gctest.SessionRequest(t, app, staff, http.MethodGet, "/api/admin/x/showcases", nil); rec.Code != http.StatusOK {
		t.Errorf("staff list = %d, want 200", rec.Code)
	}
	rec := gctest.SessionRequest(t, app, staff, http.MethodPost, "/api/admin/x/showcases",
		map[string]any{"title": "Nope", "video_url": ytLink})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "showcases.write") {
		t.Errorf("staff create = %d %s, want 403 naming showcases.write", rec.Code, rec.Body)
	}
}

// The link check says what YouTube says, and a YouTube that cannot be reached
// is not a refusal.
func TestTheLinkCheckAsksYouTube(t *testing.T) {
	status := http.StatusOK
	stub := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "dQw4w9WgXcQ") {
			t.Errorf("oEmbed asked about %q", r.URL.RawQuery)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"title":"Diwali gold, live"}`))
	})
	app := gctest.New(t, New(Config{YouTubeOEmbedURL: stub.URL}))
	check := func() Check {
		t.Helper()
		rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/showcases/check", map[string]any{"url": ytLink})
		if rec.Code != http.StatusOK {
			t.Fatalf("check = %d: %s", rec.Code, rec.Body)
		}
		var c Check
		gctest.DecodeData(t, rec, &c)
		return c
	}
	if c := check(); c.Title != "Diwali gold, live" || c.Problem != "" || c.EmbedURL == "" {
		t.Errorf("an embeddable video = %+v", c)
	}
	status = http.StatusUnauthorized
	if c := check(); !strings.Contains(c.Problem, "embedding") {
		t.Errorf("embedding turned off = %+v", c)
	}
	stub.Close()
	if c := check(); c.Problem != "" || c.EmbedURL == "" {
		t.Errorf("YouTube unreachable = %+v, want the player and no problem", c)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/showcases/check", map[string]any{"url": "https://fb.watch/x/"}); rec.Code != http.StatusBadRequest {
		t.Errorf("a short share link = %d, want 400", rec.Code)
	}
}

// A buyer reading French sees the products' French titles here as on their
// own pages: the showcase translates through the same hook core's reads use.
func TestAShowcaseSpeaksTheBuyersLanguage(t *testing.T) {
	app := gctest.NewWithConfig(t, gocommerce.Config{DefaultLanguage: "en", Languages: []string{"en", "fr"}},
		New(Config{}), translations.New())
	p := gctest.CreateProduct(t, app, "SC-FR", 1000, 3)
	if rec := gctest.AdminRequest(t, app, http.MethodPut, "/api/admin/x/translations/product/"+itoa(p.ID)+"/fr",
		map[string]any{"fields": map[string]string{"title": "Bague en or"}}); rec.Code != http.StatusOK {
		t.Fatalf("translate = %d: %s", rec.Code, rec.Body)
	}
	s := newShowcase(t, app, "Gold", map[string]any{"product_ids": []int64{p.ID}, "status": StatusPublished})
	var got publicShowcase
	gctest.DecodeData(t, public(t, app, "/x/showcases/"+s.Slug+"?lang=fr", http.StatusOK), &got)
	if len(got.Products) != 1 || got.Products[0].Title != "Bague en or" {
		t.Errorf("in French = %+v", got.Products)
	}
}
