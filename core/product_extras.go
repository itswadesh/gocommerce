package gocommerce

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"
)

// What a product page says beside the buy box (D134): a few short highlight
// lines and a free gift note. Both are plain text a shop types, kept in the
// product's metadata under keys the engine owns, so they cost no column and
// travel on every read the product already has.
const (
	MetaHighlights = "highlights"
	MetaFreeGift   = "free_gift"

	MaxHighlights   = 8
	MaxProductBlurb = 80
)

// normalizeProductMetadata checks and tidies the keys above, leaving every
// other key as the caller sent it. A highlight list is trimmed and its empty
// lines dropped, so the panel's blank last row is not stored as a line, and an
// empty list or note is removed rather than kept as an empty value a
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
	if raw, ok := m[MetaFreeGift]; ok {
		s, isString := raw.(string)
		if raw != nil && !isString {
			return Validationf("metadata.%s must be text", MetaFreeGift)
		}
		s, err := plainLine(s, "the free gift note")
		if err != nil {
			return err
		}
		if s == "" {
			delete(m, MetaFreeGift)
		} else {
			m[MetaFreeGift] = s
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
		s, err := plainLine(s, "a highlight")
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

// plainLine is one line of a shop's own words: trimmed, at most
// MaxProductBlurb characters, and with no line breaks or control characters,
// which a buy box drawing one line per entry would show as nothing or as a
// broken layout.
func plainLine(s, what string) (string, error) {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) > MaxProductBlurb {
		return "", Validationf("%s is limited to %d characters", what, MaxProductBlurb)
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
