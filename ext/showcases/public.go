package showcases

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// publicCard is a showcase in a list: what a storefront needs to put a player
// on a page and link to the showcase.
type publicCard struct {
	Slug              string     `json:"slug"`
	Title             string     `json:"title"`
	Description       string     `json:"description"`
	Video             Video      `json:"video"`
	Label             string     `json:"label"`
	StartsAt          *time.Time `json:"starts_at,omitempty"`
	FeaturedProductID *int64     `json:"featured_product_id"`
}

// publicShowcase is one showcase: the card, who is showing it, and what.
type publicShowcase struct {
	publicCard
	// Store is the shop's own name, so a buyer knows whose people these are.
	Store    string           `json:"store"`
	Products []ProductSummary `json:"products"`
}

func card(s *Showcase) publicCard {
	v := s.Video
	v.URL = "" // what the store pasted is its business; the player is built
	return publicCard{Slug: s.Slug, Title: s.Title, Description: s.Description, Video: v,
		Label: s.Label, StartsAt: s.StartsAt, FeaturedProductID: s.FeaturedProductID}
}

func (m *Module) closed(w http.ResponseWriter, r *http.Request) bool {
	if m.enabled(r.Context()) {
		return false
	}
	gocommerce.RespondError(w, r, gocommerce.NotFoundf("this store has no video showcases"))
	return true
}

// handlePublicList is the published showcases for a place on the storefront:
// the home page, a product's page, or a collection's.
func (m *Module) handlePublicList(w http.ResponseWriter, r *http.Request) {
	if m.closed(w, r) {
		return
	}
	q := r.URL.Query()
	where, args := "status = 'published'", []any{}
	idParam := func(name string) (int64, bool) {
		id, err := strconv.ParseInt(q.Get(name), 10, 64)
		if err != nil || id <= 0 {
			gocommerce.RespondError(w, r, gocommerce.Validationf("%s is a %s id", name, name))
			return 0, false
		}
		return id, true
	}
	switch q.Get("on") {
	case "":
	case "home":
		where += " AND on_home"
	case "product":
		id, ok := idParam("product")
		if !ok {
			return
		}
		args = append(args, id)
		where += ` AND on_products AND (
			EXISTS (SELECT 1 FROM showcase_products sp WHERE sp.showcase_id = showcases.id AND sp.product_id = $1)
			OR EXISTS (SELECT 1 FROM product_collections pc
			           WHERE pc.collection_id = showcases.collection_id AND pc.product_id = $1))`
	case "collection":
		id, ok := idParam("collection")
		if !ok {
			return
		}
		args = append(args, id)
		where += " AND on_collection AND collection_id = $1"
	default:
		gocommerce.RespondError(w, r, gocommerce.Validationf("on is home, product or collection"))
		return
	}
	list, err := m.list(r.Context(), where, args...)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out := make([]publicCard, 0, len(list))
	for _, s := range list {
		out = append(out, card(s))
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) published(ctx context.Context, slug string) (*Showcase, error) {
	s, err := m.get(ctx, "slug = $1", slug)
	if err == nil && s.Status != StatusPublished {
		err = gocommerce.NotFoundf("no such showcase")
	}
	return s, err
}

func (m *Module) handlePublicGet(w http.ResponseWriter, r *http.Request) {
	if m.closed(w, r) {
		return
	}
	ctx := r.Context()
	s, err := m.published(ctx, r.PathValue("slug"))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	products, err := m.shown(r, s)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out := publicShowcase{publicCard: card(s), Products: make([]ProductSummary, 0, len(products))}
	featured := false
	for _, p := range products {
		out.Products = append(out.Products, summarize(p, false))
		featured = featured || (s.FeaturedProductID != nil && p.ID == *s.FeaturedProductID)
	}
	// A featured product nobody can buy here — archived, or not on this
	// channel — is not pointed at.
	if !featured {
		out.FeaturedProductID = nil
	}
	out.Store = m.storeName(ctx)
	gocommerce.Respond(w, http.StatusOK, out)
}

// storeName is the shop's trading name, else the store's own: a buyer is
// always told whose people these are.
func (m *Module) storeName(ctx context.Context) string {
	if p, err := m.app.Profile().Get(ctx); err == nil && p.Name != "" {
		return p.Name
	}
	if id, ok := gocommerce.StoreFrom(ctx); ok {
		if st, err := m.app.Stores().Get(ctx, id); err == nil {
			return st.Name
		}
	}
	return ""
}

// handlePublicNow is what changes during a broadcast — the label and the
// product being shown — small enough for a storefront to ask every few
// seconds, and cacheable for five.
func (m *Module) handlePublicNow(w http.ResponseWriter, r *http.Request) {
	if m.closed(w, r) {
		return
	}
	s, err := m.published(r.Context(), r.PathValue("slug"))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=5")
	gocommerce.Respond(w, http.StatusOK, map[string]any{
		"label": s.Label, "featured_product_id": s.FeaturedProductID,
	})
}

// Event types a storefront reports.
const (
	EventView      = "view"
	EventClick     = "click"
	EventAddToCart = "add_to_cart"
)

// handleEvent counts what a buyer did with a showcase. Nothing about the
// buyer is kept: a day's counts, and for an add to the cart which showcase
// the basket came from, so its order can be counted. A storefront reports
// add_to_cart only once the cart has accepted the line.
func (m *Module) handleEvent(w http.ResponseWriter, r *http.Request) {
	if m.closed(w, r) {
		return
	}
	ctx := r.Context()
	store, _ := gocommerce.StoreFrom(ctx)
	if wait, ok := m.events.allow(store+" "+peerAddr(r), time.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusTooManyRequests,
			Code: "too_many_requests", Message: "too many showcase events from this address; try again shortly"})
		return
	}
	var in struct {
		Type      string `json:"type"`
		ProductID *int64 `json:"product_id"`
		Cart      string `json:"cart"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	column := map[string]string{EventView: "views", EventClick: "clicks", EventAddToCart: "adds"}[in.Type]
	if column == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("type is view, click or add_to_cart"))
		return
	}
	s, err := m.published(ctx, r.PathValue("slug"))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var cartID int64
	if in.Type == EventAddToCart && in.Cart != "" {
		cart, err := m.app.Cart().GetByToken(ctx, in.Cart)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		cartID = cart.ID
	}
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if err := m.count(ctx, tx, s.ID, column, 1); err != nil {
			return err
		}
		if cartID == 0 {
			return nil
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO showcase_carts (cart_id, showcase_id) VALUES ($1, $2)
			ON CONFLICT (store_id, cart_id) DO UPDATE SET showcase_id = EXCLUDED.showcase_id, created_at = now()`,
			cartID, s.ID)
		return err
	})
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "count showcase event"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// onOrderCreated counts an order whose basket a showcase filled, once:
// order.created is delivered at least once.
func (m *Module) onOrderCreated(ctx context.Context, e gocommerce.Event) error {
	var ev gocommerce.OrderEvent
	if err := e.Decode(&ev); err != nil {
		return fmt.Errorf("showcases: decode %s payload: %w", e.Name, err)
	}
	if ev.CartID == 0 {
		return nil
	}
	return gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var showcase int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO showcase_orders (order_id, showcase_id)
			SELECT $1, showcase_id FROM showcase_carts WHERE cart_id = $2
			ON CONFLICT (store_id, order_id) DO NOTHING
			RETURNING showcase_id`, ev.OrderID, ev.CartID).Scan(&showcase)
		if errors.Is(err, sql.ErrNoRows) {
			return nil // not from a showcase, or counted already
		}
		if err != nil {
			return err
		}
		if err := m.count(ctx, tx, showcase, "orders", 1); err != nil {
			return err
		}
		return m.count(ctx, tx, showcase, "revenue_minor", ev.TotalMinor)
	})
}

// throttle is a fixed window per key, the way ext/b2b bounds its sign-ups.
// The key is the store and the peer address, which behind a proxy is the
// proxy's: X-Forwarded-For is not read, for core's reason — without a proxy
// it is whatever a client says. So the bound is generous, enough that a
// store's real traffic through one proxy fits, and only a script inflating
// the counts meets it.
type throttle struct {
	mu    sync.Mutex
	limit int
	seen  map[string]*window
	swept time.Time
}

type window struct {
	start time.Time
	count int
}

func newThrottle(perMinute int) *throttle {
	if perMinute == 0 {
		perMinute = 600
	}
	return &throttle{limit: perMinute, seen: map[string]*window{}}
}

func (t *throttle) allow(key string, now time.Time) (time.Duration, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if now.Sub(t.swept) >= time.Minute {
		for k, w := range t.seen {
			if now.Sub(w.start) >= time.Minute {
				delete(t.seen, k)
			}
		}
		t.swept = now
	}
	w := t.seen[key]
	if w == nil || now.Sub(w.start) >= time.Minute {
		t.seen[key] = &window{start: now, count: 1}
		return 0, true
	}
	if w.count >= t.limit {
		return w.start.Add(time.Minute).Sub(now), false
	}
	w.count++
	return 0, true
}

func peerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
