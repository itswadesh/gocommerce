package customizer

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"slices"
	"unicode/utf8"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	documentVersion  = 1
	maxDocumentBytes = 64 << 10
	maxLayers        = 50
	maxTextRunes     = 200
	maxCoordinate    = 100000.0
	maxFontSize      = 2000.0
)

const (
	layerImage   = "image"
	layerText    = "text"
	layerShape   = "shape"
	layerSticker = "sticker"
)

var (
	hexColour = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	layerID   = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	aligns    = []string{"left", "center", "right"}
	shapes    = []string{"rect", "circle", "heart", "star"}
)

// Document is a design: a background colour and layers drawn in order,
// bottom first. Coordinates are in the mockup's pixels, measured from the
// print area's top left corner.
type Document struct {
	Version    int     `json:"version"`
	Background *string `json:"background"`
	Layers     []Layer `json:"layers"`
}

// Layer is one thing on a design. Type decides which of the optional fields
// it carries.
type Layer struct {
	ID       string  `json:"id"`
	Type     string  `json:"type"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Width    float64 `json:"width"`
	Height   float64 `json:"height"`
	Rotation float64 `json:"rotation"`
	Opacity  float64 `json:"opacity"`

	UploadID       *string  `json:"upload_id,omitempty"`
	StickerMediaID *int64   `json:"sticker_media_id,omitempty"`
	Text           *string  `json:"text,omitempty"`
	Font           *string  `json:"font,omitempty"`
	Color          *string  `json:"color,omitempty"`
	FontSize       *float64 `json:"font_size,omitempty"`
	Align          *string  `json:"align,omitempty"`
	Shape          *string  `json:"shape,omitempty"`
}

func parseDocument(raw json.RawMessage, s Setup) (Document, []string, error) {
	var d Document
	if len(raw) > maxDocumentBytes {
		return d, nil, gocommerce.Validationf("a design can be at most %d KB", maxDocumentBytes>>10)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, nil, gocommerce.Validationf("the design is not a version %d document: %v", documentVersion, err)
	}
	if d.Version != documentVersion {
		return d, nil, gocommerce.Validationf("the design's version must be %d", documentVersion)
	}
	if d.Background != nil {
		if !s.Tools.Background {
			return d, nil, gocommerce.Validationf("this product does not offer a background colour")
		}
		if !hexColour.MatchString(*d.Background) {
			return d, nil, gocommerce.Validationf("the background must be a colour such as #1a2b3c")
		}
	}
	if len(d.Layers) > maxLayers {
		return d, nil, gocommerce.Validationf("a design can have at most %d layers", maxLayers)
	}
	seen := map[string]bool{}
	var uploads []string
	for i, l := range d.Layers {
		if !layerID.MatchString(l.ID) {
			return d, nil, gocommerce.Validationf("layer %d needs an id of letters, digits, - and _", i+1)
		}
		if seen[l.ID] {
			return d, nil, gocommerce.Validationf("two layers are called %q", l.ID)
		}
		seen[l.ID] = true
		if p := l.problem(s); p != "" {
			return d, nil, gocommerce.Validationf("layer %q: %s", l.ID, p)
		}
		if l.UploadID != nil && !slices.Contains(uploads, *l.UploadID) {
			uploads = append(uploads, *l.UploadID)
		}
	}
	return d, uploads, nil
}

func (l Layer) problem(s Setup) string {
	for _, v := range []float64{l.X, l.Y, l.Width, l.Height, l.Rotation, l.Opacity} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return "every number must be a number"
		}
	}
	switch {
	case math.Abs(l.X) > maxCoordinate || math.Abs(l.Y) > maxCoordinate:
		return "it is placed too far from the print area"
	case l.Width <= 0 || l.Height <= 0 || l.Width > maxCoordinate || l.Height > maxCoordinate:
		return "its width and height must be above 0"
	case l.Rotation < -360 || l.Rotation > 360:
		return "its rotation must be between -360 and 360 degrees"
	case l.Opacity < 0 || l.Opacity > 1:
		return "its opacity must be between 0 and 1"
	}
	has := map[string]bool{
		"upload_id": l.UploadID != nil, "sticker_media_id": l.StickerMediaID != nil, "text": l.Text != nil,
		"font": l.Font != nil, "color": l.Color != nil, "font_size": l.FontSize != nil,
		"align": l.Align != nil, "shape": l.Shape != nil,
	}
	var allowed []string
	switch l.Type {
	case layerImage:
		if !s.Tools.Upload {
			return "this product does not take photos"
		}
		if l.UploadID == nil || !validID(*l.UploadID) {
			return "a photo needs the upload_id the upload returned"
		}
		allowed = []string{"upload_id"}
	case layerSticker:
		if !s.Tools.Stickers {
			return "this product does not offer stickers"
		}
		if l.StickerMediaID == nil || !slices.Contains(s.StickerMediaIDs, *l.StickerMediaID) {
			return "that is not one of this product's stickers"
		}
		allowed = []string{"sticker_media_id"}
	case layerText:
		if !s.Tools.Text {
			return "this product does not take words"
		}
		switch {
		case l.Text == nil || *l.Text == "" || utf8.RuneCountInString(*l.Text) > maxTextRunes:
			return "its words must be 1 to 200 characters"
		case l.Font == nil || !slices.Contains(s.Fonts, *l.Font):
			return "its font must be one of this product's fonts"
		case l.Color == nil || !hexColour.MatchString(*l.Color):
			return "its colour must be a colour such as #1a2b3c"
		case l.FontSize == nil || !(*l.FontSize > 0 && *l.FontSize <= maxFontSize):
			return "its font size must be above 0 and at most 2000"
		case l.Align != nil && !slices.Contains(aligns, *l.Align):
			return "it must be aligned left, center or right"
		}
		allowed = []string{"text", "font", "color", "font_size", "align"}
	case layerShape:
		if !s.Tools.Shapes {
			return "this product does not offer shapes"
		}
		switch {
		case l.Shape == nil || !slices.Contains(shapes, *l.Shape):
			return "its shape must be rect, circle, heart or star"
		case l.Color == nil || !hexColour.MatchString(*l.Color):
			return "its colour must be a colour such as #1a2b3c"
		}
		allowed = []string{"shape", "color"}
	default:
		return "its type must be image, text, shape or sticker"
	}
	for field, set := range has {
		if set && !slices.Contains(allowed, field) {
			return "a " + l.Type + " layer has no " + field
		}
	}
	return ""
}

// DesignInput is what the storefront saves.
type DesignInput struct {
	ProductID       int64           `json:"product_id"`
	VariantID       int64           `json:"variant_id"`
	Document        json.RawMessage `json:"document"`
	PreviewUploadID string          `json:"preview_upload_id"`
	PrintUploadID   string          `json:"print_upload_id"`
}

// Saved is a design as the storefront holds it until it is in the basket.
type Saved struct {
	ID         string `json:"design_id"`
	PreviewURL string `json:"preview_url"`
}

// CreateDesign checks a design against its product and stores it. A design is
// never edited: a shopper who changes it saves a new one.
func (m *Module) CreateDesign(ctx context.Context, in DesignInput) (*Saved, error) {
	if !m.enabled(ctx) {
		return nil, errNotMadeToOrder
	}
	s, _, err := m.load(ctx, in.ProductID)
	if err != nil {
		return nil, err
	}
	var onSale, ofProduct bool
	if err := m.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM products WHERE id = $1 AND status = 'active'),
		       EXISTS (SELECT 1 FROM variants WHERE id = $2 AND product_id = $1)`,
		in.ProductID, in.VariantID).Scan(&onSale, &ofProduct); err != nil {
		return nil, gocommerce.Internalf(err, "read the product")
	}
	if !s.Enabled || !onSale {
		return nil, errNotMadeToOrder
	}
	if !ofProduct {
		return nil, gocommerce.Validationf("variant %d is not a variant of product %d", in.VariantID, in.ProductID)
	}
	if len(in.Document) == 0 {
		return nil, gocommerce.Validationf("a design needs its document")
	}
	doc, photos, err := parseDocument(in.Document, s)
	if err != nil {
		return nil, err
	}
	if !validID(in.PreviewUploadID) || !validID(in.PrintUploadID) {
		return nil, gocommerce.Validationf("a design needs the preview_upload_id and print_upload_id its uploads returned")
	}
	uploads := slices.Clone(photos)
	for _, id := range []string{in.PreviewUploadID, in.PrintUploadID} {
		if !slices.Contains(uploads, id) {
			uploads = append(uploads, id)
		}
	}
	stored, err := json.Marshal(doc)
	if err != nil {
		return nil, gocommerce.Internalf(err, "encode the design")
	}
	printSize, err := json.Marshal(s.PrintSize)
	if err != nil {
		return nil, gocommerce.Internalf(err, "encode the print size")
	}
	printArea, err := json.Marshal(s.PrintArea)
	if err != nil {
		return nil, gocommerce.Internalf(err, "encode the print area")
	}
	id, err := randomID()
	if err != nil {
		return nil, gocommerce.Internalf(err, "name the design")
	}
	var preview string
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		held, err := holdUploads(ctx, tx, uploads)
		if err != nil {
			return err
		}
		// The print shop prints the file at the print size, so a file of any
		// other size comes out stretched or soft. A pixel either way is a
		// rounding of the editor's own.
		w, h := s.PrintSize.PrintPixels()
		if p := held[in.PrintUploadID]; abs(p.Width-w) > 1 || abs(p.Height-h) > 1 {
			return gocommerce.Validationf("the print file is %d × %d pixels; this product prints at %d × %d", p.Width, p.Height, w, h)
		}
		preview = held[in.PreviewUploadID].URL
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO customizer_designs (id, product_id, variant_id, document, preview_upload_id, print_upload_id,
			                                print_size, print_area)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, in.ProductID, in.VariantID, stored, in.PreviewUploadID, in.PrintUploadID, printSize, printArea); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO customizer_design_uploads (design_id, upload_id)
			SELECT $1, unnest($2::text[])`, id, texts(uploads))
		return err
	})
	if err != nil {
		var apiErr *gocommerce.APIError
		if errors.As(err, &apiErr) {
			return nil, err
		}
		return nil, gocommerce.Internalf(err, "save the design")
	}
	return &Saved{ID: id, PreviewURL: preview}, nil
}

// holdUploads reads the uploads and keeps the sweep off them until the
// transaction ends: it deletes only uploads it can lock.
func holdUploads(ctx context.Context, tx *sql.Tx, ids []string) (map[string]Upload, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, url, width, height FROM customizer_uploads WHERE id = ANY($1::text[]) FOR SHARE`, texts(ids))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	held := map[string]Upload{}
	for rows.Next() {
		var u Upload
		if err := rows.Scan(&u.ID, &u.URL, &u.Width, &u.Height); err != nil {
			return nil, err
		}
		held[u.ID] = u
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		if _, ok := held[id]; !ok {
			return nil, gocommerce.Validationf("upload %s is not one of this store's, or has expired; upload it again", id)
		}
	}
	return held, nil
}

func abs(n int) int { return max(n, -n) }

func texts(values []string) string {
	b := []byte{'{'}
	for i, v := range values {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '"')
		for _, r := range []byte(v) {
			if r == '"' || r == '\\' {
				b = append(b, '\\')
			}
			b = append(b, r)
		}
		b = append(b, '"')
	}
	return string(append(b, '}'))
}

func (m *Module) handleCreateDesign(w http.ResponseWriter, r *http.Request) {
	if !m.enabled(r.Context()) {
		gocommerce.RespondError(w, r, errNotMadeToOrder)
		return
	}
	if !m.allow(w, r, m.designs, "designs") {
		return
	}
	var in DesignInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	saved, err := m.CreateDesign(r.Context(), in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, saved)
}

// lineDesign stamps vouched_at, which also takes the design's row lock: a
// sweep cannot delete it mid-purchase, and a second checkout of it waits for
// the first and then finds its order line.
func (m *Module) lineDesign(ctx context.Context, tx *sql.Tx, designID string, variantID int64) (*gocommerce.LineDesign, error) {
	if !validID(designID) {
		return nil, gocommerce.NotFoundf("no design %q in this store", designID)
	}
	var productID, madeFor int64
	var raw, printSize, printArea []byte
	var preview, print string
	err := tx.QueryRowContext(ctx, `
		UPDATE customizer_designs d SET vouched_at = now()
		FROM customizer_uploads p, customizer_uploads x
		WHERE d.id = $1 AND p.id = d.preview_upload_id AND x.id = d.print_upload_id
		RETURNING d.product_id, d.variant_id, d.document, p.url, x.url, d.print_size, d.print_area`, designID).
		Scan(&productID, &madeFor, &raw, &preview, &print, &printSize, &printArea)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no design %q in this store", designID)
	}
	if err != nil {
		return nil, err
	}
	if madeFor != variantID {
		return nil, gocommerce.Validationf("this design was made for variant %d, not %d", madeFor, variantID)
	}
	var made, ordered bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM customizer_products WHERE product_id = $1 AND enabled),
		       EXISTS (SELECT 1 FROM order_lines WHERE metadata ? 'design' AND metadata->'design'->>'id' = $2)`,
		productID, designID).Scan(&made, &ordered); err != nil {
		return nil, err
	}
	on, err := m.app.Plugins().EnabledTx(ctx, tx, pluginKey)
	if err != nil {
		return nil, err
	}
	if !made || !on {
		return nil, gocommerce.Conflictf("this product is no longer made to a design")
	}
	if ordered {
		return nil, gocommerce.Conflictf("this design is already on an order; save it again to order it once more")
	}
	var doc Document
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	photos, err := photosOf(ctx, tx, doc)
	if err != nil {
		return nil, err
	}
	var fonts []string
	for _, l := range doc.Layers {
		if l.Font != nil && !slices.Contains(fonts, *l.Font) {
			fonts = append(fonts, *l.Font)
		}
	}
	return &gocommerce.LineDesign{
		PreviewURL: preview,
		Snapshot: gocommerce.Metadata{
			"id": designID, "preview_url": preview, "print_url": print, "document": doc,
			"photos": photos, "fonts": fontsOf(fonts),
			"print_size": json.RawMessage(printSize), "print_area": json.RawMessage(printArea),
		},
	}, nil
}

// Photo is a shopper's original photo as an order line keeps it, for a print
// operator who needs to print it again.
type Photo struct {
	UploadID string `json:"upload_id"`
	URL      string `json:"url"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

func photosOf(ctx context.Context, tx *sql.Tx, doc Document) ([]Photo, error) {
	photos := []Photo{}
	for _, l := range doc.Layers {
		if l.UploadID == nil || slices.ContainsFunc(photos, func(p Photo) bool { return p.UploadID == *l.UploadID }) {
			continue
		}
		p := Photo{UploadID: *l.UploadID}
		if err := tx.QueryRowContext(ctx,
			`SELECT url, width, height FROM customizer_uploads WHERE id = $1`, p.UploadID).
			Scan(&p.URL, &p.Width, &p.Height); err != nil {
			return nil, err
		}
		photos = append(photos, p)
	}
	return photos, nil
}
