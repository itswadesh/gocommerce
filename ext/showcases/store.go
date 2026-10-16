package showcases

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Kinds, live states, statuses and labels.
const (
	KindVideo = "video"
	KindLive  = "live"

	LiveNone      = "none"
	LiveScheduled = "scheduled"
	LiveNow       = "live"
	LiveEnded     = "ended"

	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusArchived  = "archived"

	LabelVideo     = "video"
	LabelScheduled = "scheduled"
	LabelLive      = "live"
	LabelReplay    = "replay"
)

// Showcase is one as the panel sees it.
type Showcase struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Video       Video  `json:"video"`
	// Kind is the store's word for the video: a recording, or a livestream
	// it will run.
	Kind      string     `json:"kind"`
	LiveState string     `json:"live_state"`
	StartsAt  *time.Time `json:"starts_at"`
	LiveSince *time.Time `json:"live_since"`
	// Label is what a storefront may honestly call it now (see label).
	Label  string `json:"label"`
	Status string `json:"status"`
	// CollectionID, when set, is where the products come from; otherwise
	// they are ProductIDs, in that order.
	CollectionID      *int64     `json:"collection_id"`
	ProductIDs        []int64    `json:"product_ids"`
	FeaturedProductID *int64     `json:"featured_product_id"`
	OnHome            bool       `json:"on_home"`
	OnCollection      bool       `json:"on_collection"`
	OnProducts        bool       `json:"on_products"`
	Position          int        `json:"position"`
	PublishedAt       *time.Time `json:"published_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	Stats             Stats      `json:"stats"`
	// Products is filled on a single read: what the showcase holds now,
	// whatever its status, for the editor.
	Products []ProductSummary `json:"products,omitempty"`
	// Picked is, while a collection is shown instead, the products picked
	// before it, so the editor can go back to them without losing them.
	Picked []ProductSummary `json:"picked,omitempty"`
}

// Stats is how a showcase has done since it was made.
type Stats struct {
	Views  int64 `json:"views"`
	Clicks int64 `json:"clicks"`
	Adds   int64 `json:"adds_to_cart"`
	Orders int64 `json:"orders"`
	// Revenue is the total of the orders counted, in the store's currency.
	Revenue gocommerce.Money `json:"revenue"`
}

// Input creates or changes a showcase. A field left out is left as it is.
type Input struct {
	Title       *string `json:"title"`
	Slug        *string `json:"slug"`
	Description *string `json:"description"`
	VideoURL    *string `json:"video_url"`
	Kind        *string `json:"kind"`
	LiveState   *string `json:"live_state"`
	// StartsAt is an RFC 3339 time; "" clears it.
	StartsAt          *string               `json:"starts_at"`
	Status            *string               `json:"status"`
	ProductIDs        *[]int64              `json:"product_ids"`
	CollectionID      gocommerce.NullableID `json:"collection_id"`
	FeaturedProductID gocommerce.NullableID `json:"featured_product_id"`
	OnHome            *bool                 `json:"on_home"`
	OnCollection      *bool                 `json:"on_collection"`
	OnProducts        *bool                 `json:"on_products"`
	Position          *int                  `json:"position"`
}

// label is what a storefront may call a showcase now. Live only while the
// store says so and for twelve hours after; a livestream it never said
// anything about is just a video, and one that ended is a replay.
func label(kind, state string, startsAt, liveSince *time.Time, now time.Time) string {
	if kind != KindLive {
		return LabelVideo
	}
	switch state {
	case LiveScheduled:
		if startsAt != nil && startsAt.After(now) {
			return LabelScheduled
		}
	case LiveNow:
		if liveSince != nil && now.Sub(*liveSince) < liveFor {
			return LabelLive
		}
		return LabelReplay
	case LiveEnded:
		return LabelReplay
	}
	return LabelVideo
}

const showcaseColumns = `id, slug, title, description, provider, video_url, video_ref, kind, live_state,
	starts_at, live_since, status, collection_id, featured_product_id, on_home, on_collection,
	on_products, position, published_at, created_at, updated_at`

func scanShowcase(sc interface{ Scan(...any) error }) (*Showcase, error) {
	var s Showcase
	var v video
	if err := sc.Scan(&s.ID, &s.Slug, &s.Title, &s.Description, &v.Provider, &s.Video.URL, &v.Ref,
		&s.Kind, &s.LiveState, &s.StartsAt, &s.LiveSince, &s.Status, &s.CollectionID,
		&s.FeaturedProductID, &s.OnHome, &s.OnCollection, &s.OnProducts, &s.Position,
		&s.PublishedAt, &s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	pasted := s.Video.URL
	s.Video = describe(v)
	s.Video.URL = pasted
	s.Label = label(s.Kind, s.LiveState, s.StartsAt, s.LiveSince, time.Now())
	s.ProductIDs = []int64{}
	return &s, nil
}

func (m *Module) get(ctx context.Context, where string, arg any) (*Showcase, error) {
	s, err := scanShowcase(m.db.QueryRowContext(ctx, `SELECT `+showcaseColumns+` FROM showcases WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no such showcase")
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "read showcase")
	}
	if err := m.fill(ctx, []*Showcase{s}); err != nil {
		return nil, err
	}
	return s, nil
}

// list reads showcases in the store's order, newest first within it.
func (m *Module) list(ctx context.Context, where string, args ...any) ([]*Showcase, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT `+showcaseColumns+` FROM showcases WHERE `+where+`
		ORDER BY position, coalesce(published_at, created_at) DESC, id DESC`, args...)
	if err != nil {
		return nil, gocommerce.Internalf(err, "list showcases")
	}
	defer rows.Close()
	out := []*Showcase{}
	for rows.Next() {
		s, err := scanShowcase(rows)
		if err != nil {
			return nil, gocommerce.Internalf(err, "scan showcase")
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, gocommerce.Internalf(err, "list showcases")
	}
	return out, m.fill(ctx, out)
}

// fill adds each showcase's own product list and its totals, two queries for
// any number of showcases.
func (m *Module) fill(ctx context.Context, list []*Showcase) error {
	if len(list) == 0 {
		return nil
	}
	by := make(map[int64]*Showcase, len(list))
	ids := make([]int64, 0, len(list))
	for _, s := range list {
		by[s.ID] = s
		ids = append(ids, s.ID)
	}
	set := int64Set(ids)
	rows, err := m.db.QueryContext(ctx, `SELECT showcase_id, product_id FROM showcase_products
		WHERE showcase_id = ANY($1::bigint[]) ORDER BY showcase_id, position`, set)
	if err != nil {
		return gocommerce.Internalf(err, "read showcase products")
	}
	for rows.Next() {
		var sid, pid int64
		if err := rows.Scan(&sid, &pid); err != nil {
			rows.Close()
			return gocommerce.Internalf(err, "scan showcase product")
		}
		by[sid].ProductIDs = append(by[sid].ProductIDs, pid)
	}
	rows.Close()
	currency := m.app.Currency(ctx)
	for _, s := range list {
		s.Stats.Revenue = gocommerce.Money{Currency: currency}
	}
	rows, err = m.db.QueryContext(ctx, `SELECT showcase_id, sum(views), sum(clicks), sum(adds), sum(orders), sum(revenue_minor)
		FROM showcase_stats WHERE showcase_id = ANY($1::bigint[]) GROUP BY showcase_id`, set)
	if err != nil {
		return gocommerce.Internalf(err, "read showcase stats")
	}
	defer rows.Close()
	for rows.Next() {
		var sid int64
		var st Stats
		if err := rows.Scan(&sid, &st.Views, &st.Clicks, &st.Adds, &st.Orders, &st.Revenue.AmountMinor); err != nil {
			return gocommerce.Internalf(err, "scan showcase stats")
		}
		st.Revenue.Currency = currency
		by[sid].Stats = st
	}
	return rows.Err()
}

// create makes a draft from in, which needs a title and a video. The whole of
// in is checked before anything is written, so a refusal leaves nothing.
func (m *Module) create(ctx context.Context, in Input) (*Showcase, error) {
	if in.VideoURL == nil || strings.TrimSpace(*in.VideoURL) == "" {
		return nil, gocommerce.Validationf("paste the link to the video")
	}
	if in.Title == nil || strings.TrimSpace(*in.Title) == "" {
		return nil, gocommerce.Validationf("a showcase needs a title")
	}
	base := slugOf(*in.Title)
	if in.Slug != nil {
		if base = strings.TrimSpace(*in.Slug); !slugRE.MatchString(base) || len(base) > 70 {
			return nil, gocommerce.Validationf("a slug is lower-case letters, digits and single dashes")
		}
		in.Slug = nil // numbered below rather than refused when taken
	}
	blank := &Showcase{Kind: KindVideo, LiveState: LiveNone, Status: StatusDraft, Label: LabelVideo, ProductIDs: []int64{}}
	c, err := m.plan(ctx, blank, in)
	if err != nil {
		return nil, err
	}
	v, _ := parseVideo(*in.VideoURL) // checked by plan
	var id int64
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		// A taken slug gets a number rather than an error: two showcases
		// called "Diwali" are two showcases, and the address is not what
		// anyone typed.
		for n := 1; n <= 20 && id == 0; n++ {
			slug := base
			if n > 1 {
				slug = fmt.Sprintf("%s-%d", base, n)
			}
			err := tx.QueryRowContext(ctx, `
				INSERT INTO showcases (slug, title, provider, video_url, video_ref)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (store_id, slug) DO NOTHING
				RETURNING id`, slug, strings.TrimSpace(*in.Title), v.Provider, strings.TrimSpace(*in.VideoURL), v.Ref).Scan(&id)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return err
			}
		}
		if id == 0 {
			return gocommerce.Conflictf("every address for %q is taken; give this showcase a slug of its own", base)
		}
		return m.apply(ctx, tx, id, c)
	})
	if err != nil {
		return nil, saveErr(err)
	}
	return m.get(ctx, "id = $1", id)
}

// update applies in to showcase id and answers it as it now is.
func (m *Module) update(ctx context.Context, id int64, in Input) (*Showcase, error) {
	cur, err := m.get(ctx, "id = $1", id)
	if err != nil {
		return nil, err
	}
	c, err := m.plan(ctx, cur, in)
	if err != nil {
		return nil, err
	}
	if err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error { return m.apply(ctx, tx, id, c) }); err != nil {
		return nil, saveErr(err)
	}
	return m.get(ctx, "id = $1", id)
}

func saveErr(err error) error {
	var api *gocommerce.APIError
	switch {
	case errors.As(err, &api):
		return api
	case strings.Contains(err.Error(), "showcases_slug_key"):
		return gocommerce.Conflictf("another showcase already has that address")
	}
	return gocommerce.Internalf(err, "save showcase")
}

// change is what an Input does to a showcase: the columns to set, and the
// product list when it is replaced.
type change struct {
	sets     []string
	args     []any
	products []int64
	replace  bool
}

func (c *change) put(col string, v any) {
	c.args = append(c.args, v)
	c.sets = append(c.sets, fmt.Sprintf("%s = $%d", col, len(c.args)))
}

// plan checks in against the showcase as it is, and says what to write. It
// reads but never writes, so a refusal anywhere in in leaves nothing changed.
func (m *Module) plan(ctx context.Context, cur *Showcase, in Input) (*change, error) {
	c := &change{}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" || len([]rune(t)) > maxTitle {
			return nil, gocommerce.Validationf("a title is 1 to %d characters", maxTitle)
		}
		c.put("title", t)
	}
	if in.Slug != nil {
		s := strings.TrimSpace(*in.Slug)
		if !slugRE.MatchString(s) || len(s) > 80 {
			return nil, gocommerce.Validationf("a slug is lower-case letters, digits and single dashes")
		}
		c.put("slug", s)
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if len([]rune(d)) > maxDescription {
			return nil, gocommerce.Validationf("a description is at most %d characters", maxDescription)
		}
		c.put("description", d)
	}
	if in.VideoURL != nil {
		v, err := parseVideo(*in.VideoURL)
		if err != nil {
			return nil, err
		}
		c.put("provider", v.Provider)
		c.put("video_url", strings.TrimSpace(*in.VideoURL))
		c.put("video_ref", v.Ref)
	}

	kind, state, startsAt := cur.Kind, cur.LiveState, cur.StartsAt
	if in.Kind != nil {
		if *in.Kind != KindVideo && *in.Kind != KindLive {
			return nil, gocommerce.Validationf("kind is %q or %q", KindVideo, KindLive)
		}
		kind = *in.Kind
		c.put("kind", kind)
		if kind == KindVideo && in.LiveState == nil {
			none := LiveNone
			in.LiveState = &none
		}
	}
	if in.StartsAt != nil {
		startsAt = nil
		if raw := strings.TrimSpace(*in.StartsAt); raw != "" {
			t, err := time.Parse(time.RFC3339, raw)
			if err != nil {
				return nil, gocommerce.Validationf("starts_at is a time such as 2026-11-01T18:30:00+05:30")
			}
			startsAt = &t
		}
		c.put("starts_at", startsAt)
	}
	if in.LiveState != nil {
		state = *in.LiveState
		switch state {
		case LiveNone, LiveScheduled, LiveNow, LiveEnded:
		default:
			return nil, gocommerce.Validationf("live_state is none, scheduled, live or ended")
		}
		if state != LiveNone && kind != KindLive {
			return nil, gocommerce.Validationf("only a livestream has a live state; set kind to %q first", KindLive)
		}
		c.put("live_state", state)
		// The moment it went live, kept while it stays live — saying "live"
		// again mid-broadcast does not restart the twelve hours — and taken
		// afresh once they lapsed, for the next day's broadcast.
		switch {
		case state == LiveNow && cur.Label != LabelLive:
			c.put("live_since", time.Now())
		case state != LiveNow:
			c.put("live_since", nil)
		}
	}
	if state == LiveScheduled && startsAt == nil {
		return nil, gocommerce.Validationf("a scheduled broadcast needs its start time")
	}

	products := cur.ProductIDs
	if in.ProductIDs != nil {
		var err error
		if products, err = m.checkProducts(ctx, *in.ProductIDs); err != nil {
			return nil, err
		}
		c.products, c.replace = products, true
	}
	collection := cur.CollectionID
	if in.CollectionID.Present {
		collection = in.CollectionID.Value
		if collection != nil {
			if _, err := m.app.Collections().Get(ctx, *collection); err != nil {
				return nil, err
			}
		}
		c.put("collection_id", collection)
	}
	featured := cur.FeaturedProductID
	if in.FeaturedProductID.Present {
		featured = in.FeaturedProductID.Value
		c.put("featured_product_id", featured)
	}
	// A featured product has to be one the showcase shows, whichever of its
	// inputs moved; one that no longer is stops being featured.
	if featured != nil {
		shown, err := m.holds(ctx, collection, products, *featured)
		if err != nil {
			return nil, err
		}
		if !shown {
			if in.FeaturedProductID.Present {
				return nil, gocommerce.Validationf("the featured product has to be one of the showcase's products")
			}
			c.put("featured_product_id", nil)
		}
	}
	for _, f := range []struct {
		col string
		v   *bool
	}{{"on_home", in.OnHome}, {"on_collection", in.OnCollection}, {"on_products", in.OnProducts}} {
		if f.v != nil {
			c.put(f.col, *f.v)
		}
	}
	if in.Position != nil {
		c.put("position", *in.Position)
	}
	if in.Status != nil {
		switch *in.Status {
		case StatusPublished:
			if collection == nil && len(products) == 0 {
				return nil, gocommerce.Validationf("attach products or a collection before publishing: the point of a showcase is what it sells")
			}
			if cur.Status != StatusPublished {
				c.put("published_at", time.Now())
			}
		case StatusDraft, StatusArchived:
		default:
			return nil, gocommerce.Validationf("status is draft, published or archived")
		}
		c.put("status", *in.Status)
	}
	return c, nil
}

// apply writes a planned change to showcase id inside tx.
func (m *Module) apply(ctx context.Context, tx *sql.Tx, id int64, c *change) error {
	if len(c.sets) > 0 {
		args := append(c.args, id)
		if _, err := tx.ExecContext(ctx, `UPDATE showcases SET `+strings.Join(c.sets, ", ")+
			fmt.Sprintf(`, updated_at = now() WHERE id = $%d`, len(args)), args...); err != nil {
			return err
		}
	}
	if !c.replace {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM showcase_products WHERE showcase_id = $1`, id); err != nil {
		return err
	}
	for i, pid := range c.products {
		if _, err := tx.ExecContext(ctx, `INSERT INTO showcase_products (showcase_id, product_id, position)
			VALUES ($1, $2, $3)`, id, pid, i); err != nil {
			return err
		}
	}
	return nil
}

// present is the ids, in their order, of the products the store still has.
func (m *Module) present(ctx context.Context, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := m.db.QueryContext(ctx, `SELECT id FROM products WHERE id = ANY($1::bigint[])`, int64Set(ids))
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the showcase's products")
	}
	defer rows.Close()
	there := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, gocommerce.Internalf(err, "read the showcase's products")
		}
		there[id] = true
	}
	out := make([]int64, 0, len(there))
	for _, id := range ids {
		if there[id] {
			out = append(out, id)
		}
	}
	return out, rows.Err()
}

// checkProducts keeps a list once each, in order, and refuses one that names
// a product the store does not have.
func (m *Module) checkProducts(ctx context.Context, ids []int64) ([]int64, error) {
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	if len(out) > MaxProducts {
		return nil, gocommerce.Validationf("a showcase holds at most %d products; use a collection for more", MaxProducts)
	}
	if len(out) == 0 {
		return out, nil
	}
	var found int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM products WHERE id = ANY($1::bigint[])`,
		int64Set(out)).Scan(&found); err != nil {
		return nil, gocommerce.Internalf(err, "check showcase products")
	}
	if found != len(out) {
		return nil, gocommerce.Validationf("some of those products are not in this store")
	}
	return out, nil
}

// holds is whether a product is one the showcase shows: in its collection
// when it has one, else in its own list.
func (m *Module) holds(ctx context.Context, collection *int64, products []int64, product int64) (bool, error) {
	if collection == nil {
		return slices.Contains(products, product), nil
	}
	var in bool
	err := m.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM product_collections
		WHERE collection_id = $1 AND product_id = $2)`, *collection, product).Scan(&in)
	if err != nil {
		return false, gocommerce.Internalf(err, "check the featured product")
	}
	return in, nil
}

func int64Set(ids []int64) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return "{" + strings.Join(s, ",") + "}"
}

// count adds to one of a showcase's counters for today.
func (m *Module) count(ctx context.Context, q interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, showcase int64, column string, by int64) error {
	_, err := q.ExecContext(ctx, `
		INSERT INTO showcase_stats (showcase_id, day, `+column+`) VALUES ($1, current_date, $2)
		ON CONFLICT (store_id, showcase_id, day) DO UPDATE SET `+column+` = showcase_stats.`+column+` + $2`,
		showcase, by)
	return err
}
