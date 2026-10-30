package customizer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"math"
	"net/http"
	"slices"
	"strconv"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/ext/layouts"
)

const (
	maxMockupPx     = 8000
	maxPrintMM      = 1000.0
	minDPI, maxDPI  = 72, 600
	maxStickers     = 30
	maxMinPhotoPx   = 10000
	defaultDPI      = 300
	defaultMinPhoto = 1000
	aspectTolerance = 0.02
	// maxPrintPixels is the largest print file a setup may ask for, so the
	// storefront's PNG of it fits an upload's maxUploadBytes whatever it
	// shows. Chrome's canvas wrote pure noise at 3.44 bytes a pixel, so 4
	// megapixels is at most 13.8 MB.
	maxPrintPixels = 4_000_000
)

var defaultFonts = []string{"inter", "poppins", "montserrat", "playfair-display", "lora"}

// Picture is an image the editor draws, at its own size in pixels.
type Picture struct {
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// PrintArea is where the design goes, in the mockup's pixels. Radius rounds
// its corners, as a phone case's are.
type PrintArea struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
	Radius int `json:"radius"`
}

// PrintSize is the printed design's real size and resolution: the print file
// is WidthMM / 25.4 × DPI pixels wide.
type PrintSize struct {
	WidthMM  float64 `json:"width_mm"`
	HeightMM float64 `json:"height_mm"`
	DPI      int     `json:"dpi"`
}

// Tools are the editor's tools a shopper is offered.
type Tools struct {
	Upload     bool `json:"upload"`
	Text       bool `json:"text"`
	Shapes     bool `json:"shapes"`
	Background bool `json:"background"`
	Stickers   bool `json:"stickers"`
}

// Setup is one product's design editor as the shop sets it. Mockup and
// PrintArea are nil until chosen.
type Setup struct {
	Enabled         bool       `json:"enabled"`
	Mockup          *Picture   `json:"mockup"`
	OverlayURL      *string    `json:"overlay_url"`
	PrintArea       *PrintArea `json:"print_area"`
	PrintSize       PrintSize  `json:"print_size"`
	Tools           Tools      `json:"tools"`
	Fonts           []string   `json:"fonts"`
	StickerMediaIDs []int64    `json:"sticker_media_ids"`
	MinPhotoPx      int        `json:"min_photo_px"`
}

// SetupInput is what the panel saves. A tool list, fonts, a resolution or a
// minimum photo size left out takes its default, so a product switched on
// with only its pictures and print area has a working editor.
type SetupInput struct {
	Enabled         bool       `json:"enabled"`
	Mockup          *Picture   `json:"mockup"`
	OverlayURL      *string    `json:"overlay_url"`
	PrintArea       *PrintArea `json:"print_area"`
	PrintSize       *PrintSize `json:"print_size"`
	Tools           *Tools     `json:"tools"`
	Fonts           []string   `json:"fonts"`
	StickerMediaIDs []int64    `json:"sticker_media_ids"`
	MinPhotoPx      *int       `json:"min_photo_px"`
}

type sticker struct {
	MediaID int64 `json:"media_id"`
	Width   int   `json:"width"`
	Height  int   `json:"height"`
}

func defaultTools(stickers int) Tools {
	return Tools{Upload: true, Text: true, Shapes: true, Background: true, Stickers: stickers > 0}
}

func defaultSetup() Setup {
	return Setup{
		PrintSize:       PrintSize{DPI: defaultDPI},
		Tools:           defaultTools(0),
		Fonts:           slices.Clone(defaultFonts),
		StickerMediaIDs: []int64{},
		MinPhotoPx:      defaultMinPhoto,
	}
}

func (in SetupInput) withDefaults() Setup {
	s := Setup{
		Enabled: in.Enabled, Mockup: in.Mockup, OverlayURL: in.OverlayURL, PrintArea: in.PrintArea,
		Fonts: in.Fonts, StickerMediaIDs: in.StickerMediaIDs, MinPhotoPx: defaultMinPhoto,
	}
	if s.OverlayURL != nil && *s.OverlayURL == "" {
		s.OverlayURL = nil
	}
	if in.PrintSize != nil {
		s.PrintSize = *in.PrintSize
	}
	if s.PrintSize.DPI == 0 {
		s.PrintSize.DPI = defaultDPI
	}
	if s.Fonts == nil {
		s.Fonts = slices.Clone(defaultFonts)
	}
	if s.StickerMediaIDs == nil {
		s.StickerMediaIDs = []int64{}
	}
	if in.Tools != nil {
		s.Tools = *in.Tools
	} else {
		s.Tools = defaultTools(len(s.StickerMediaIDs))
	}
	if in.MinPhotoPx != nil {
		s.MinPhotoPx = *in.MinPhotoPx
	}
	return s
}

// PrintPixels is the print file's size: what the storefront renders the
// design at.
func (p PrintSize) PrintPixels() (int, int) {
	return int(math.Round(p.WidthMM / 25.4 * float64(p.DPI))), int(math.Round(p.HeightMM / 25.4 * float64(p.DPI)))
}

func (s Setup) check() error {
	if s.Mockup != nil {
		if s.Mockup.Width < 1 || s.Mockup.Height < 1 || s.Mockup.Width > maxMockupPx || s.Mockup.Height > maxMockupPx {
			return gocommerce.Validationf("the mockup must be between 1 and %d pixels a side", maxMockupPx)
		}
	}
	if a := s.PrintArea; a != nil {
		if s.Mockup == nil {
			return gocommerce.Validationf("choose the mockup before the print area")
		}
		if a.Width < 1 || a.Height < 1 {
			return gocommerce.Validationf("the print area must be at least one pixel wide and high")
		}
		if a.X < 0 || a.Y < 0 || a.X+a.Width > s.Mockup.Width || a.Y+a.Height > s.Mockup.Height {
			return gocommerce.Validationf("the print area must lie inside the %d × %d mockup", s.Mockup.Width, s.Mockup.Height)
		}
		if a.Radius < 0 || 2*a.Radius > min(a.Width, a.Height) {
			return gocommerce.Validationf("the corner radius must be between 0 and %d, half the print area's shorter side", min(a.Width, a.Height)/2)
		}
	}
	p := s.PrintSize
	if p.WidthMM < 0 || p.HeightMM < 0 || p.WidthMM > maxPrintMM || p.HeightMM > maxPrintMM ||
		math.IsNaN(p.WidthMM) || math.IsNaN(p.HeightMM) {
		return gocommerce.Validationf("the print size must be between 0 and %g mm a side", maxPrintMM)
	}
	if p.DPI < minDPI || p.DPI > maxDPI {
		return gocommerce.Validationf("the resolution must be between %d and %d dpi", minDPI, maxDPI)
	}
	if w, h := p.PrintPixels(); w*h > maxPrintPixels || max(w, h) > maxUploadSide {
		return gocommerce.Validationf("the print file would be %d × %d pixels, and can be at most %g megapixels and %d pixels a side; larger prints are not supported yet, so lower the resolution or the print size", w, h, maxPrintPixels/1e6, maxUploadSide)
	}
	if p.WidthMM > 0 && p.HeightMM > 0 && s.PrintArea != nil {
		want := p.WidthMM / p.HeightMM
		got := float64(s.PrintArea.Width) / float64(s.PrintArea.Height)
		if math.Abs(got-want)/want > aspectTolerance {
			return gocommerce.Validationf("the print area is %.3f wide for every 1 high, and the print size %.3f; make them the same shape or the print comes out stretched", got, want)
		}
	}
	if s.MinPhotoPx < 0 || s.MinPhotoPx > maxMinPhotoPx {
		return gocommerce.Validationf("the smallest photo must be between 0 and %d pixels", maxMinPhotoPx)
	}
	if len(s.StickerMediaIDs) > maxStickers {
		return gocommerce.Validationf("a product offers at most %d stickers", maxStickers)
	}
	seen := map[int64]bool{}
	for _, id := range s.StickerMediaIDs {
		if seen[id] {
			return gocommerce.Validationf("sticker %d is chosen twice", id)
		}
		seen[id] = true
	}
	if s.Tools.Stickers && len(s.StickerMediaIDs) == 0 {
		return gocommerce.Validationf("choose a sticker or switch stickers off")
	}
	known := map[string]bool{}
	for _, f := range layouts.Fonts {
		known[f.Value] = true
	}
	chosen := map[string]bool{}
	for _, f := range s.Fonts {
		if !known[f] {
			return gocommerce.Validationf("%q is not a font the storefront can load", f)
		}
		if chosen[f] {
			return gocommerce.Validationf("%q is chosen twice", f)
		}
		chosen[f] = true
	}
	if s.Tools.Text && len(s.Fonts) == 0 {
		return gocommerce.Validationf("choose a font or switch words off")
	}
	if s.Enabled {
		switch {
		case s.Mockup == nil:
			return gocommerce.Validationf("choose the mockup before switching the customizer on")
		case s.PrintArea == nil:
			return gocommerce.Validationf("mark the print area before switching the customizer on")
		case p.WidthMM == 0 || p.HeightMM == 0:
			return gocommerce.Validationf("give the print size before switching the customizer on")
		case s.Tools == Tools{}:
			return gocommerce.Validationf("switch on at least one tool")
		}
	}
	return nil
}

func (m *Module) checkPictures(ctx context.Context, s Setup) ([]sticker, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if s.Mockup != nil {
		w, h, _, err := m.libraryPicture(ctx, "the mockup", s.Mockup.URL)
		if err != nil {
			return nil, err
		}
		if w != s.Mockup.Width || h != s.Mockup.Height {
			return nil, gocommerce.Validationf("the mockup is %d × %d pixels, not %d × %d", w, h, s.Mockup.Width, s.Mockup.Height)
		}
	}
	if s.OverlayURL != nil {
		if s.Mockup == nil {
			return nil, gocommerce.Validationf("choose the mockup before the overlay")
		}
		w, h, format, err := m.libraryPicture(ctx, "the overlay", *s.OverlayURL)
		if err != nil {
			return nil, err
		}
		if format != "png" {
			return nil, gocommerce.Validationf("the overlay must be a PNG, so the design shows through it")
		}
		if w != s.Mockup.Width || h != s.Mockup.Height {
			return nil, gocommerce.Validationf("the overlay is %d × %d pixels; it must be the mockup's size, %d × %d", w, h, s.Mockup.Width, s.Mockup.Height)
		}
	}
	stickers := make([]sticker, 0, len(s.StickerMediaIDs))
	for _, id := range s.StickerMediaIDs {
		w, h, _, err := m.pictureSize(ctx, fmt.Sprintf("sticker %d", id), id)
		if err != nil {
			return nil, err
		}
		stickers = append(stickers, sticker{MediaID: id, Width: w, Height: h})
	}
	return stickers, nil
}

func (m *Module) libraryPicture(ctx context.Context, what, url string) (width, height int, format string, err error) {
	var id int64
	err = m.db.QueryRowContext(ctx,
		`SELECT id FROM media WHERE url = $1 AND kind = 'image' ORDER BY id LIMIT 1`, url).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, 0, "", gocommerce.Validationf("%s must be a picture in this store's media library", what)
	}
	if err != nil {
		return 0, 0, "", gocommerce.Internalf(err, "find %s", what)
	}
	return m.pictureSize(ctx, what, id)
}

func (m *Module) pictureSize(ctx context.Context, what string, id int64) (width, height int, format string, err error) {
	body, err := m.app.MediaLibrary().Picture(ctx, id, math.MaxInt32)
	var apiErr *gocommerce.APIError
	switch {
	case errors.Is(err, gocommerce.ErrNotFound):
		return 0, 0, "", gocommerce.Validationf("%s must be a picture in this store's media library", what)
	case errors.As(err, &apiErr):
		return 0, 0, "", err
	case err != nil:
		return 0, 0, "", gocommerce.Validationf("%s must be a JPEG or a PNG", what)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return 0, 0, "", gocommerce.Validationf("%s is not a readable picture", what)
	}
	// The size a browser draws it at, which is what the panel measured and
	// the storefront scales by: a phone's JPEG held on its side is shown
	// turned by its EXIF orientation, its width and height swapped.
	if _, o, _ := jpegHeader(body); format == "jpeg" && o >= 5 {
		return cfg.Height, cfg.Width, format, nil
	}
	return cfg.Width, cfg.Height, format, nil
}

func (m *Module) load(ctx context.Context, productID int64) (Setup, []sticker, error) {
	var raw, rawStickers []byte
	err := m.db.QueryRowContext(ctx,
		`SELECT setup, stickers FROM customizer_products WHERE product_id = $1`, productID).Scan(&raw, &rawStickers)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultSetup(), nil, nil
	}
	if err != nil {
		return Setup{}, nil, gocommerce.Internalf(err, "read the customizer")
	}
	var s Setup
	var stickers []sticker
	if err := json.Unmarshal(raw, &s); err != nil {
		return Setup{}, nil, gocommerce.Internalf(err, "decode the customizer")
	}
	if err := json.Unmarshal(rawStickers, &stickers); err != nil {
		return Setup{}, nil, gocommerce.Internalf(err, "decode the stickers")
	}
	return s, stickers, nil
}

func (m *Module) productExists(ctx context.Context, productID int64) error {
	var ok bool
	if err := m.db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM products WHERE id = $1)`, productID).Scan(&ok); err != nil {
		return gocommerce.Internalf(err, "find the product")
	}
	if !ok {
		return gocommerce.NotFoundf("product %d does not exist", productID)
	}
	return nil
}

// Save validates and stores a product's setup.
func (m *Module) Save(ctx context.Context, productID int64, in SetupInput) (Setup, error) {
	if err := m.productExists(ctx, productID); err != nil {
		return Setup{}, err
	}
	s := in.withDefaults()
	stickers, err := m.checkPictures(ctx, s)
	if err != nil {
		return Setup{}, err
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return Setup{}, gocommerce.Internalf(err, "encode the customizer")
	}
	rawStickers, err := json.Marshal(stickers)
	if err != nil {
		return Setup{}, gocommerce.Internalf(err, "encode the stickers")
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO customizer_products (product_id, enabled, setup, stickers)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (store_id, product_id) DO UPDATE
		SET enabled = EXCLUDED.enabled, setup = EXCLUDED.setup, stickers = EXCLUDED.stickers, updated_at = now()`,
		productID, s.Enabled, raw, rawStickers); err != nil {
		return Setup{}, gocommerce.Internalf(err, "save the customizer")
	}
	return s, nil
}

// Font is a font the editor offers, as the storefront loads it.
type Font struct {
	Key    string `json:"key"`
	Family string `json:"family"`
}

func fontsOf(keys []string) []Font {
	family := map[string]string{}
	for _, f := range layouts.Fonts {
		family[f.Value] = f.Label
	}
	out := make([]Font, 0, len(keys))
	for _, k := range keys {
		out = append(out, Font{Key: k, Family: family[k]})
	}
	return out
}

// Sticker is a sticker the editor offers.
type Sticker struct {
	ID     int64  `json:"id"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// PublicSetup is what the storefront's editor reads: the setup without the
// switch and the sticker ids, with the fonts and stickers it needs to draw
// them, and the line to show before a photo is chosen.
type PublicSetup struct {
	ProductID  int64     `json:"product_id"`
	Mockup     Picture   `json:"mockup"`
	OverlayURL *string   `json:"overlay_url"`
	PrintArea  PrintArea `json:"print_area"`
	PrintSize  PrintSize `json:"print_size"`
	Tools      Tools     `json:"tools"`
	Fonts      []Font    `json:"fonts"`
	Stickers   []Sticker `json:"stickers"`
	MinPhotoPx int       `json:"min_photo_px"`
	Consent    string    `json:"consent"`
}

var errNotMadeToOrder = gocommerce.NotFoundf("this product cannot be designed here")

// Public is a product's setup as the storefront reads it.
func (m *Module) Public(ctx context.Context, productID int64) (*PublicSetup, error) {
	if !m.enabled(ctx) {
		return nil, errNotMadeToOrder
	}
	s, stickers, err := m.load(ctx, productID)
	if err != nil {
		return nil, err
	}
	if !s.Enabled || s.Mockup == nil || s.PrintArea == nil {
		return nil, errNotMadeToOrder
	}
	var onSale, mockupKept bool
	if err := m.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM products WHERE id = $1 AND status = 'active'),
		       EXISTS (SELECT 1 FROM media WHERE url = $2 AND kind = 'image')`,
		productID, s.Mockup.URL).Scan(&onSale, &mockupKept); err != nil {
		return nil, gocommerce.Internalf(err, "read the product")
	}
	if !onSale || !mockupKept {
		return nil, errNotMadeToOrder
	}
	out := &PublicSetup{
		ProductID: productID, Mockup: *s.Mockup, OverlayURL: s.OverlayURL, PrintArea: *s.PrintArea,
		PrintSize: s.PrintSize, Tools: s.Tools, Fonts: fontsOf(s.Fonts), Stickers: []Sticker{},
		MinPhotoPx: s.MinPhotoPx, Consent: m.app.Plugins().String(ctx, pluginKey, "consent"),
	}
	if s.Tools.Stickers {
		if out.Stickers, err = m.stickersOf(ctx, stickers); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (m *Module) stickersOf(ctx context.Context, chosen []sticker) ([]Sticker, error) {
	ids := make([]int64, len(chosen))
	for i, s := range chosen {
		ids[i] = s.MediaID
	}
	rows, err := m.db.QueryContext(ctx,
		`SELECT id, url FROM media WHERE id = ANY($1::bigint[]) AND kind = 'image'`, int64s(ids))
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the stickers")
	}
	defer rows.Close()
	urls := map[int64]string{}
	for rows.Next() {
		var id int64
		var url string
		if err := rows.Scan(&id, &url); err != nil {
			return nil, gocommerce.Internalf(err, "read the stickers")
		}
		urls[id] = url
	}
	if err := rows.Err(); err != nil {
		return nil, gocommerce.Internalf(err, "read the stickers")
	}
	out := []Sticker{}
	for _, s := range chosen {
		if url, ok := urls[s.MediaID]; ok {
			out = append(out, Sticker{ID: s.MediaID, URL: url, Width: s.Width, Height: s.Height})
		}
	}
	return out, nil
}

// int64s is a Postgres array literal, which database/sql cannot bind from a
// slice by itself.
func int64s(ids []int64) string {
	b := []byte{'{'}
	for i, id := range ids {
		if i > 0 {
			b = append(b, ',')
		}
		b = strconv.AppendInt(b, id, 10)
	}
	return string(append(b, '}'))
}

func productID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("product_id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.NotFoundf("no such product")
	}
	return id, nil
}

func (m *Module) handleFonts(w http.ResponseWriter, r *http.Request) {
	keys := make([]string, len(layouts.Fonts))
	for i, f := range layouts.Fonts {
		keys[i] = f.Value
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"fonts": fontsOf(keys), "default": defaultFonts})
}

func (m *Module) handleGetSetup(w http.ResponseWriter, r *http.Request) {
	id, err := productID(r)
	if err == nil {
		err = m.productExists(r.Context(), id)
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	s, _, err := m.load(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, s)
}

func (m *Module) handlePutSetup(w http.ResponseWriter, r *http.Request) {
	id, err := productID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in SetupInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	s, err := m.Save(r.Context(), id, in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, s)
}

func (m *Module) handlePublicSetup(w http.ResponseWriter, r *http.Request) {
	id, err := productID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	s, err := m.Public(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, s)
}
