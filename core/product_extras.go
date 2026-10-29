package gocommerce

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a product says beside the buy box (D134) and on its picture (D157): a
// few short highlight lines, a free gift note and a badge. All are plain text
// a shop types, kept in the product's metadata under keys the engine owns, so
// they cost no column and travel on every read the product already has.
const (
	MetaHighlights = "highlights"
	MetaFreeGift   = "free_gift"
	MetaBadge      = "badge"

	MaxHighlights   = 8
	MaxProductBlurb = 80
	// A badge sits on a card's picture, which a listing draws 150 to 200px
	// wide. 24 characters holds a label such as "Hand block print" with room
	// to spare; a longer one covers the picture instead of labelling it.
	MaxProductBadge = 24
)

var productLineKeys = []struct {
	key, what string
	limit     int
}{
	{MetaFreeGift, "the free gift note", MaxProductBlurb},
	{MetaBadge, "the badge", MaxProductBadge},
}

// normalizeProductMetadata checks and tidies the keys above, leaving every
// other key as the caller sent it. A highlight list is trimmed and its empty
// lines dropped, so the panel's blank last row is not stored as a line, and an
// empty list or line is removed rather than kept as an empty value a
// storefront would have to know to ignore.
func normalizeProductMetadata(m Metadata) error {
	if m == nil {
		return nil
	}
	if raw, ok := m[MetaHighlights]; ok {
		lines, err := highlightLines(raw)
		if err != nil {
			return err
		}
		if len(lines) == 0 {
			delete(m, MetaHighlights)
		} else {
			m[MetaHighlights] = lines
		}
	}
	for _, k := range productLineKeys {
		raw, ok := m[k.key]
		if !ok {
			continue
		}
		s, isString := raw.(string)
		if raw != nil && !isString {
			return Validationf("metadata.%s must be text", k.key)
		}
		s, err := plainLine(s, k.what, k.limit)
		if err != nil {
			return err
		}
		if s == "" {
			delete(m, k.key)
		} else {
			m[k.key] = s
		}
	}
	return nil
}

func highlightLines(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		if typed, isStrings := raw.([]string); isStrings {
			list = make([]any, len(typed))
			for i, s := range typed {
				list[i] = s
			}
		} else {
			return nil, Validationf("metadata.%s must be a list of lines", MetaHighlights)
		}
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, Validationf("metadata.%s must be a list of lines", MetaHighlights)
		}
		s, err := plainLine(s, "a highlight", MaxProductBlurb)
		if err != nil {
			return nil, err
		}
		if s != "" {
			out = append(out, s)
		}
	}
	if len(out) > MaxHighlights {
		return nil, Validationf("a product can have at most %d highlights", MaxHighlights)
	}
	return out, nil
}

// plainLine is one line of a shop's own words: trimmed, at most limit
// characters, and with no line breaks or control characters, which a buy box
// drawing one line per entry would show as nothing or as a broken layout.
func plainLine(s, what string, limit int) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > limit {
		return "", Validationf("%s is limited to %d characters", what, limit)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", Validationf("%s must be one line of plain text", what)
		}
	}
	return s, nil
}

// productGallery is a product's media in display order, pictures and videos
// alike, for the public product: a storefront's gallery plays a video beside
// the photographs, and until now it could see only the lead picture and the
// ones a variant nominates. 3D models are left out; no storefront draws them.
func (c *Catalog) productGallery(ctx context.Context, productID int64) ([]VariantImage, error) {
	rows, err := c.app.db.QueryContext(ctx, `
		SELECT m.id, m.url, m.kind, m.alt
		FROM product_media pm
		JOIN media m ON m.id = pm.media_id
		WHERE pm.product_id = $1 AND m.kind IN ('image', 'video')
		ORDER BY pm.position, m.id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VariantImage{}
	for rows.Next() {
		var img VariantImage
		if err := rows.Scan(&img.MediaID, &img.URL, &img.Kind, &img.Alt); err != nil {
			return nil, err
		}
		out = append(out, img)
	}
	return out, rows.Err()
}
