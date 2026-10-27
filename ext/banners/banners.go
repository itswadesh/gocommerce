// Package banners is the pictures a shop puts on its storefront's pages:
// slides across the top of a page, and grids of tiles under a heading, each a
// picture and a link (D99).
//
// It is the shape the owner's other admin keeps on a page record —
// desktopBanners and mobileBanners for the slider, sections of banners for
// the grids — kept here as rows, one per picture, so a shop adds, reorders
// and switches off one banner without rewriting a page. The storefront reads
// one page's live banners at /x/banners?page=home, already split into slides
// and grids, which is what its home page renders.
package banners

import (
	"context"
	"database/sql"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey = "banners"
	maxTitle  = 200
	maxURL    = 2048

	// PlacementSlide is a picture in the slider across the top of a page.
	PlacementSlide = "slide"
	// PlacementGrid is a tile in a grid under a heading, its GroupTitle.
	PlacementGrid = "grid"
)

var pageRE = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Config configures the module. It has nothing to set yet; the type is here
// so a setting can arrive without changing how the module is installed.
type Config struct{}

// Banner is one picture on a storefront page.
type Banner struct {
	ID int64 `json:"id"`
	// Page is the storefront page it shows on, by its address name: "home",
	// or a content page's slug.
	Page      string `json:"page"`
	Placement string `json:"placement"`
	// GroupTitle is a grid's heading: tiles with the same one on a page form
	// one grid. Empty for a slide.
	GroupTitle string `json:"group_title"`
	Position   int    `json:"position"`
	// Title is what the banner says, and its alternative text.
	Title string `json:"title"`
	// Link is where it takes a shopper: a path on this storefront, or a full
	// web address. Empty is a picture that goes nowhere.
	Link string `json:"link"`
	// ImageURL is the picture, wide enough for a desktop. MobileImageURL is a
	// narrower crop for a phone; empty uses ImageURL there too.
	ImageURL       string    `json:"image_url"`
	MobileImageURL string    `json:"mobile_image_url"`
	Active         bool      `json:"active"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// BannerInput creates or replaces a banner.
type BannerInput struct {
	Page           string `json:"page"`
	Placement      string `json:"placement"`
	GroupTitle     string `json:"group_title"`
	Title          string `json:"title"`
	Link           string `json:"link"`
	ImageURL       string `json:"image_url"`
	MobileImageURL string `json:"mobile_image_url"`
	Active         *bool  `json:"active"`
}

// Module is the routes and the table.
type Module struct {
	cfg Config
	app *gocommerce.App
	db  *sql.DB
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "banners" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_banners",
		SQL: `
CREATE TABLE banners (
    id               bigserial   PRIMARY KEY,
    page             text        NOT NULL DEFAULT 'home',
    placement        text        NOT NULL DEFAULT 'slide' CHECK (placement IN ('slide', 'grid')),
    group_title      text        NOT NULL DEFAULT '',
    -- The order is the shop's own, page-wide: a page reads top to bottom,
    -- and sorting it by anything the database knows would scramble that.
    position         integer     NOT NULL DEFAULT 0,
    title            text        NOT NULL DEFAULT '',
    link             text        NOT NULL DEFAULT '',
    image_url        text        NOT NULL,
    mobile_image_url text        NOT NULL DEFAULT '',
    active           boolean     NOT NULL DEFAULT true,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    -- The sample shop's banners (D96), which only the engine marks.
    sample           boolean     NOT NULL DEFAULT false
);
CREATE INDEX banners_page_idx ON banners (page, position, id);
`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "banners"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.registerRights(app)
	m.app = app
	m.db = app.DB()
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Banners", Icon: "ri-image-2-line", Category: "storefront", DefaultEnabled: true,
		Home: "/dash/themes/banners", AlwaysOn: true,
		Description: "Pictures on the storefront's pages — slides across the top and grids of tiles under a heading — edited on the Banners screen and served at /x/banners.",
	})

	app.HandleFunc("GET /x/banners", m.handlePublic)

	app.HandleAdminFunc("GET /api/admin/x/banners", m.handleList, rightBannersRead)
	app.HandleAdminFunc("POST /api/admin/x/banners", m.handleCreate, rightBannersWrite)
	app.HandleAdminFunc("PATCH /api/admin/x/banners/{id}", m.handleUpdate, rightBannersWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/banners/{id}", m.handleDelete, rightBannersWrite)
	// A page's whole order at once, as the FAQ saves its: one request per
	// moved banner would leave the page half sorted if the second failed.
	app.HandleAdminFunc("PUT /api/admin/x/banners/order", m.handleReorder, rightBannersWrite)
	return nil
}

func (m *Module) enabled(ctx context.Context) bool {
	on, _ := m.app.Plugins().Enabled(ctx, pluginKey)
	return on
}

const bannerColumns = `id, page, placement, group_title, position, title, link, image_url, mobile_image_url, active, created_at, updated_at`

func scan(sc interface{ Scan(...any) error }) (*Banner, error) {
	var b Banner
	if err := sc.Scan(&b.ID, &b.Page, &b.Placement, &b.GroupTitle, &b.Position, &b.Title, &b.Link,
		&b.ImageURL, &b.MobileImageURL, &b.Active, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, err
	}
	return &b, nil
}

// list reads the banners of one page, or of every page when page is empty,
// in the order the shop put them in.
func (m *Module) list(ctx context.Context, page string, activeOnly bool) ([]*Banner, error) {
	where, args := []string{"1 = 1"}, []any{}
	if page != "" {
		args = append(args, page)
		where = append(where, "page = $1")
	}
	if activeOnly {
		where = append(where, "active")
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT `+bannerColumns+` FROM banners WHERE `+strings.Join(where, " AND ")+` ORDER BY page, position, id`, args...)
	if err != nil {
		return nil, gocommerce.Internalf(err, "list banners")
	}
	defer rows.Close()
	out := []*Banner{}
	for rows.Next() {
		b, err := scan(rows)
		if err != nil {
			return nil, gocommerce.Internalf(err, "scan banner")
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// safeURL accepts a web address or a path on this site, and nothing a
// browser would run or fetch from somewhere unnamed: no javascript: or data:,
// and no protocol-relative //host, which reads as a path and is not one.
// Whitespace and control characters are refused anywhere in it: a browser
// drops a tab or a newline from a URL, which turns a path written "/<tab>/host"
// into //host after this check, and a space breaks a picture's srcset.
func safeURL(s string) bool {
	if strings.IndexFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return false
	}
	if strings.HasPrefix(s, "/") {
		return !strings.HasPrefix(s, "//") && !strings.HasPrefix(s, `/\`)
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func validate(in BannerInput) (BannerInput, error) {
	in.Page = strings.TrimSpace(in.Page)
	if in.Page == "" {
		in.Page = "home"
	}
	in.Placement = strings.TrimSpace(in.Placement)
	if in.Placement == "" {
		in.Placement = PlacementSlide
	}
	in.GroupTitle = strings.TrimSpace(in.GroupTitle)
	in.Title = strings.TrimSpace(in.Title)
	in.Link = strings.TrimSpace(in.Link)
	in.ImageURL = strings.TrimSpace(in.ImageURL)
	in.MobileImageURL = strings.TrimSpace(in.MobileImageURL)
	switch {
	case !pageRE.MatchString(in.Page):
		return in, gocommerce.Validationf("the page is its address name, lowercase letters, digits and hyphens, such as %q", "home")
	case in.Page != "home":
		// The column names a page so others can follow; until a storefront
		// shows banners anywhere but its home page, one put elsewhere would
		// be a banner nobody sees.
		return in, gocommerce.Validationf("banners show on the home page for now")
	case in.Placement != PlacementSlide && in.Placement != PlacementGrid:
		return in, gocommerce.Validationf("placement is %q or %q", PlacementSlide, PlacementGrid)
	case in.ImageURL == "":
		return in, gocommerce.Validationf("a banner needs a picture")
	case len(in.Title) > maxTitle || len(in.GroupTitle) > maxTitle:
		return in, gocommerce.Validationf("a title is at most %d characters", maxTitle)
	case len(in.Link) > maxURL || len(in.ImageURL) > maxURL || len(in.MobileImageURL) > maxURL:
		return in, gocommerce.Validationf("an address is at most %d characters", maxURL)
	case !safeURL(in.ImageURL) || (in.MobileImageURL != "" && !safeURL(in.MobileImageURL)):
		return in, gocommerce.Validationf("a picture is a web address or a path on this site, such as /media/…")
	case in.Link != "" && !safeURL(in.Link):
		return in, gocommerce.Validationf("a link is a path on the storefront, such as /collections/new-in, or a web address")
	}
	if in.Placement == PlacementSlide {
		in.GroupTitle = ""
	}
	return in, nil
}

// ----------------------------------------------------------------- public

// PublicBanner is a banner as the storefront sees it: what to show, and
// where it leads.
type PublicBanner struct {
	ID             int64  `json:"id"`
	Title          string `json:"title"`
	Link           string `json:"link"`
	ImageURL       string `json:"image_url"`
	MobileImageURL string `json:"mobile_image_url"`
}

// PublicGrid is one grid: its heading and its tiles, in order.
type PublicGrid struct {
	Title   string         `json:"title"`
	Banners []PublicBanner `json:"banners"`
}

func (m *Module) handlePublic(w http.ResponseWriter, r *http.Request) {
	if !m.enabled(r.Context()) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("this store has no banners"))
		return
	}
	page := strings.TrimSpace(r.URL.Query().Get("page"))
	if page == "" {
		page = "home"
	}
	banners, err := m.list(r.Context(), page, true)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// Grids in the order their first tile appears, which is the order the
	// shop arranged the page in.
	slides, grids := []PublicBanner{}, []PublicGrid{}
	at := map[string]int{}
	for _, b := range banners {
		pb := PublicBanner{ID: b.ID, Title: b.Title, Link: b.Link, ImageURL: b.ImageURL, MobileImageURL: b.MobileImageURL}
		if b.Placement == PlacementSlide {
			slides = append(slides, pb)
			continue
		}
		i, seen := at[b.GroupTitle]
		if !seen {
			i = len(grids)
			at[b.GroupTitle] = i
			grids = append(grids, PublicGrid{Title: b.GroupTitle, Banners: []PublicBanner{}})
		}
		grids[i].Banners = append(grids[i].Banners, pb)
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"page": page, "slides": slides, "grids": grids})
}

// ------------------------------------------------------------------ admin

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	banners, err := m.list(r.Context(), strings.TrimSpace(r.URL.Query().Get("page")), false)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, banners, gocommerce.ListMeta{Total: len(banners), Limit: len(banners), Offset: 0})
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	var in BannerInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	in, err := validate(in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	b, err := m.insert(r.Context(), in, false)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, b)
}

// insert writes a banner last on its page, which is where a new one belongs
// until somebody moves it.
func (m *Module) insert(ctx context.Context, in BannerInput, sample bool) (*Banner, error) {
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	b, err := scan(m.db.QueryRowContext(ctx, `
		INSERT INTO banners (page, placement, group_title, position, title, link, image_url, mobile_image_url, active, sample)
		SELECT $1, $2, $3, coalesce(max(position), -1) + 1, $4, $5, $6, $7, $8, $9 FROM banners WHERE page = $1
		RETURNING `+bannerColumns,
		in.Page, in.Placement, in.GroupTitle, in.Title, in.Link, in.ImageURL, in.MobileImageURL, active, sample))
	if err != nil {
		return nil, gocommerce.Internalf(err, "create the banner")
	}
	return b, nil
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := bannerID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in BannerInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	in, err = validate(in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// A save that does not mention the switch leaves it where it was.
	b, err := scan(m.db.QueryRowContext(r.Context(), `
		UPDATE banners SET page = $2, placement = $3, group_title = $4, title = $5, link = $6,
		       image_url = $7, mobile_image_url = $8, active = coalesce($9, active), updated_at = now()
		WHERE id = $1 RETURNING `+bannerColumns,
		id, in.Page, in.Placement, in.GroupTitle, in.Title, in.Link, in.ImageURL, in.MobileImageURL, in.Active))
	if err == sql.ErrNoRows {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no banner %d", id))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "update the banner"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, b)
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := bannerID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.db.ExecContext(r.Context(), `DELETE FROM banners WHERE id = $1`, id)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "delete the banner"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no banner %d", id))
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// reorderInput is one page's banners in the order they should read.
type reorderInput struct {
	Page string  `json:"page"`
	IDs  []int64 `json:"ids"`
}

func (m *Module) handleReorder(w http.ResponseWriter, r *http.Request) {
	var in reorderInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	in.Page = strings.TrimSpace(in.Page)
	if in.Page == "" {
		in.Page = "home"
	}
	err := gocommerce.InTx(r.Context(), m.db, func(tx *sql.Tx) error {
		var held int
		if err := tx.QueryRowContext(r.Context(), `SELECT count(*) FROM banners WHERE page = $1`, in.Page).Scan(&held); err != nil {
			return gocommerce.Internalf(err, "count banners")
		}
		if held != len(in.IDs) {
			return gocommerce.Validationf("the order must name every banner on the page: %d sent, %d held", len(in.IDs), held)
		}
		// Once each: a repeat would leave another banner off the order with
		// the position it had, tied with one that moved.
		seen := map[int64]bool{}
		for _, id := range in.IDs {
			if seen[id] {
				return gocommerce.Validationf("the order names banner %d twice", id)
			}
			seen[id] = true
		}
		for i, id := range in.IDs {
			res, err := tx.ExecContext(r.Context(),
				`UPDATE banners SET position = $3, updated_at = now() WHERE id = $1 AND page = $2`, id, in.Page, i)
			if err != nil {
				return gocommerce.Internalf(err, "reorder banners")
			}
			if n, _ := res.RowsAffected(); n == 0 {
				return gocommerce.Validationf("no banner %d on the page %q", id, in.Page)
			}
		}
		return nil
	})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	banners, err := m.list(r.Context(), in.Page, false)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, banners, gocommerce.ListMeta{Total: len(banners), Limit: len(banners), Offset: 0})
}

func bannerID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.Validationf("the id must be a number")
	}
	return id, nil
}

// The rights this module's screen is gated on. Reading is staff's as well as
// the manager's, as a content page's is; changing what every visitor sees on
// arrival is the manager's.
const (
	rightBannersRead  gocommerce.Right = "banners.read"
	rightBannersWrite gocommerce.Right = "banners.write"
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightBannersRead,
		Label:   "See the banners",
		Scope:   "The pictures on the storefront's pages",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightBannersWrite,
		Label:   "Edit the banners",
		Scope:   "Adding, ordering, switching off and removing them",
		Default: []string{gocommerce.RoleManager},
	})
}
