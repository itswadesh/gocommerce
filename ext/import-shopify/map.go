package shopify

import (
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Shopify's model into this one.
//
// The two are close enough that most of this is renaming, and the places they
// differ are the places to be careful. Those are written down here rather than
// discovered later by an operator whose prices came out a hundred times too
// small.

// toMinor reads Shopify's decimal string into minor units.
//
// Shopify sends money as "19.99" — a string, deliberately, so that nobody
// parses it as a float and loses a cent. This does the same: it splits on the
// point and reads two integers, rather than multiplying a float64 by 100 and
// rounding, because 19.99 * 100 is 1998.9999999999998 and the rounding that
// hides it is one edit away from being removed by somebody tidying up.
//
// The exponent comes from the store's own currency rather than being assumed to
// be two: an import into a yen store must not divide by a hundred.
func toMinor(amount string, exponent int) (int64, error) {
	s := strings.TrimSpace(amount)
	if s == "" {
		return 0, nil
	}
	negative := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")

	whole, frac, _ := strings.Cut(s, ".")
	if whole == "" {
		whole = "0"
	}
	// Padded or truncated to the currency's own precision. Shopify writes two
	// decimals whatever the currency, so a yen price arrives as "2500.00" and
	// the fraction is dropped rather than multiplied in.
	if len(frac) > exponent {
		frac = frac[:exponent]
	}
	for len(frac) < exponent {
		frac += "0"
	}

	digits := whole + frac
	minor, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not an amount", amount)
	}
	if negative {
		minor = -minor
	}
	return minor, nil
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// plainText turns Shopify's body_html into something this engine's description
// field can hold.
//
// The description here is text, not markup, and pasting a shop's HTML into it
// would put tags on the storefront of anyone who renders it plainly. Block
// boundaries become newlines first so a list does not collapse into one run-on
// sentence, and the entities are decoded because "&amp;" in a plain-text field
// is a bug a person has to fix by hand later.
func plainText(body string) string {
	s := body
	for _, br := range []string{"<br>", "<br/>", "<br />", "</p>", "</li>", "</div>", "</h1>", "</h2>", "</h3>"} {
		s = strings.ReplaceAll(s, br, br+"\n")
		s = strings.ReplaceAll(s, strings.ToUpper(br), br+"\n")
	}
	s = tagRE.ReplaceAllString(s, "")
	s = html.UnescapeString(s)

	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			// One blank line between paragraphs, not the eight that markup
			// stripping tends to leave behind.
			if blank || len(out) == 0 {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// splitTags reads Shopify's comma-separated tag string.
func splitTags(tags string) []string {
	out := []string{}
	for _, t := range strings.Split(tags, ",") {
		if t = strings.TrimSpace(t); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// statusOf maps Shopify's product status onto this engine's.
//
// The vocabularies happen to match, which is worth checking rather than
// assuming: both have active, draft and archived, and they mean the same three
// things. Anything unrecognised becomes a draft, because the safe direction to
// be wrong in is "not on sale yet".
func statusOf(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "active":
		return "active"
	case "archived":
		return "archived"
	default:
		return "draft"
	}
}

// Mapped is one Shopify product translated, with the pictures kept beside it.
//
// The images are separate because the engine attaches media in a second call:
// a product is created first, then its pictures are linked by URL.
type Mapped struct {
	Input gocommerce.ProductInput
	// ImageURLs are Shopify's CDN addresses, in the product's own order.
	ImageURLs []string
	ImageAlts []string
	// VariantImages maps a variant SKU to the picture Shopify had it showing.
	VariantImages map[string]string
	Warnings      []string
}

// MapProduct translates one Shopify product.
//
// What it deliberately does not do: download a picture, invent a SKU, or guess
// at a price it could not read. Each of those is reported as a warning on the
// job instead, so the operator sees what came across imperfectly rather than
// finding it themselves in six months.
func MapProduct(p Product, currency string, exponent int) (*Mapped, error) {
	out := &Mapped{VariantImages: map[string]string{}}

	title := strings.TrimSpace(p.Title)
	if title == "" {
		return nil, fmt.Errorf("product %d has no title", p.ID)
	}

	byImageID := map[int64]string{}
	for _, img := range p.Images {
		if img.Src == "" {
			continue
		}
		byImageID[img.ID] = img.Src
		out.ImageURLs = append(out.ImageURLs, img.Src)
		out.ImageAlts = append(out.ImageAlts, img.Alt)
	}

	options := []gocommerce.OptionInput{}
	for _, o := range p.Options {
		// Shopify gives a product with no real options a single axis called
		// "Title" holding "Default Title". Carrying that across would put a
		// meaningless size picker on the storefront.
		if strings.EqualFold(o.Name, "Title") && len(o.Values) == 1 &&
			strings.EqualFold(o.Values[0], "Default Title") {
			continue
		}
		options = append(options, gocommerce.OptionInput{Name: o.Name, Values: o.Values})
	}

	variants := []gocommerce.VariantInput{}
	for _, v := range p.Variants {
		priceMinor, err := toMinor(v.Price, exponent)
		if err != nil {
			out.Warnings = append(out.Warnings,
				fmt.Sprintf("%s: price %q could not be read, imported as 0", v.SKU, v.Price))
			priceMinor = 0
		}

		var compareAt *int64
		if v.CompareAtPrice != nil && strings.TrimSpace(*v.CompareAtPrice) != "" {
			if minor, err := toMinor(*v.CompareAtPrice, exponent); err == nil && minor > 0 {
				compareAt = &minor
			}
		}

		// Shopify tracks stock when inventory_management is set (to "shopify",
		// usually) and not at all when it is null. A variant it does not track
		// reports inventory_quantity 0, which would arrive here as "none left"
		// rather than "not counted" and take the product off sale.
		tracked := v.InventoryMgmt != nil && strings.TrimSpace(*v.InventoryMgmt) != ""
		stock := v.InventoryQuantity
		if !tracked || stock < 0 {
			// Shopify allows negative on-hand; this engine's CHECK does not,
			// and an oversold count is not a number to import.
			stock = 0
		}

		taxable := v.Taxable
		requiresShipping := v.RequiresShipping
		continueSelling := strings.EqualFold(v.InventoryPolicy, "continue")
		onHand := stock
		trackInventory := tracked

		variants = append(variants, gocommerce.VariantInput{
			// Left empty when Shopify has none: the engine derives one from the
			// product and its options, which is a better code than anything
			// invented here.
			SKU:                 strings.TrimSpace(v.SKU),
			Barcode:             strings.TrimSpace(v.Barcode),
			PriceMinor:          priceMinor,
			CompareAtPriceMinor: compareAt,
			Taxable:             &taxable,
			RequiresShipping:    &requiresShipping,
			Options:             v.OptionValues(len(options)),
			StockOnHand:         &onHand,
			TrackInventory:      &trackInventory,
			ContinueSelling:     &continueSelling,
			WeightGrams:         weightOf(v),
			WeightUnit:          "g",
		})

		if v.ImageID != nil {
			if src, ok := byImageID[*v.ImageID]; ok && v.SKU != "" {
				out.VariantImages[v.SKU] = src
			}
		}
	}

	if len(variants) == 0 {
		return nil, fmt.Errorf("product %q has no variants", title)
	}

	out.Input = gocommerce.ProductInput{
		Title: title,
		// Shopify's handle is its slug and it is what the old storefront's
		// links used. Carrying it across is what makes a redirect possible.
		Slug:        strings.TrimSpace(p.Handle),
		Description: plainText(p.BodyHTML),
		Status:      statusOf(p.Status),
		// Shopify's `vendor` is the brand — the manufacturer — and so is this
		// engine's. It is not a marketplace seller, and it does not become one.
		Vendor:      strings.TrimSpace(p.Vendor),
		ProductType: strings.TrimSpace(p.ProductType),
		Tags:        splitTags(p.Tags),
		Options:     options,
		Variants:    variants,
		// Where the product came from, so a second import updates this row
		// rather than making another one beside it.
		Metadata: gocommerce.Metadata{
			"shopify": map[string]any{
				"id":         p.ID,
				"handle":     p.Handle,
				"updated_at": p.UpdatedAt,
			},
		},
	}
	return out, nil
}

func weightOf(v Variant) *int {
	if v.Grams <= 0 {
		return nil
	}
	grams := v.Grams
	return &grams
}
