package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Product statuses. Only an active product is visible to shoppers.
const (
	ProductDraft    = "draft"
	ProductActive   = "active"
	ProductArchived = "archived"
)

// Product is the merchandising concept: the thing a shopper browses. What
// they actually buy is a [Variant].
type Product struct {
	ID          int64  `json:"id"`
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
	Currency    string `json:"currency"`

	// Organisation. ProductType and Vendor are free text an operator groups
	// and filters by; Tags is a set, normalised on write (see normalizeTags).
	ProductType string   `json:"product_type"`
	Vendor      string   `json:"vendor"`
	Tags        []string `json:"tags"`
	// Category is where the product sits in the taxonomy — one node, with its
	// ancestry rendered for display. Nil means uncategorised, which is a normal
	// state for a draft rather than an error.
	Category *ProductCategory `json:"category,omitempty"`

	// Storefront <title> and meta description. Empty means "derive it from the
	// title", which is the storefront's call rather than the engine's.
	SEOTitle       string `json:"seo_title"`
	SEODescription string `json:"seo_description"`

	// ImageURL is the product's lead picture — the first in its media list.
	// Filled in on every read and never stored, so a listing can show what a
	// product looks like without a request per row.
	ImageURL string `json:"image_url,omitempty"`
	// MediaCount is how many pictures the product has, for a listing that
	// shows the lead one and wants to say there are more.
	MediaCount int `json:"media_count,omitempty"`

	Options  []ProductOption `json:"options"`
	Variants []Variant       `json:"variants"`
	// Collections the product belongs to, carried on every read so the admin
	// UI renders membership without a request per product.
	Collections []ProductCollection `json:"collections"`
	Metadata    Metadata            `json:"metadata"`
	CreatedAt   time.Time           `json:"created_at"`
	UpdatedAt   time.Time           `json:"updated_at"`
}

// DefaultVariant returns the variant to use when a client does not choose one.
// For a product with no options there is exactly one variant and this is it,
// which is what keeps the simple case simple.
func (p *Product) DefaultVariant() *Variant {
	for i := range p.Variants {
		if p.Variants[i].Active {
			return &p.Variants[i]
		}
	}
	return nil
}

// ProductOption is one axis of choice, such as Size.
type ProductOption struct {
	ID       int64         `json:"id"`
	Name     string        `json:"name"`
	Position int           `json:"position"`
	Values   []OptionValue `json:"values"`
}

// OptionValue is one choice on an axis, such as M.
type OptionValue struct {
	ID       int64  `json:"id"`
	Value    string `json:"value"`
	Position int    `json:"position"`
}

// Variant is the sellable unit: it carries the SKU, the price and the stock,
// and it is what cart lines and order lines reference.
type Variant struct {
	ID             int64  `json:"id"`
	ProductID      int64  `json:"product_id"`
	SKU            string `json:"sku"`
	Barcode        string `json:"barcode,omitempty"`
	Price          Money  `json:"price"`
	CompareAtPrice *Money `json:"compare_at_price,omitempty"`
	// Cost is what the item cost this store. Nil means nobody has recorded one,
	// which is not the same as zero — a zero cost reports a 100% margin, and a
	// margin nobody entered is worse than no margin at all.
	Cost *Money `json:"cost,omitempty"`
	// Taxable is whether tax applies to this variant. True for almost
	// everything, and the safe default to be wrong about: tax charged in error
	// is refundable, tax not charged is not.
	Taxable bool `json:"taxable"`
	// RequiresShipping is whether the thing is sent at all. False for a
	// download, a service or a gift card: nothing is charged to send it and
	// the checkout asks for no delivery option when the basket holds nothing
	// else.
	RequiresShipping bool     `json:"requires_shipping"`
	Options          []string `json:"options"`
	Label            string   `json:"label"`
	StockOnHand      int      `json:"stock_on_hand"`
	StockReserved    int      `json:"stock_reserved"`
	Available        int      `json:"available"`
	// AtLocation is this variant's holding at one particular place. It is filled
	// in only by the reads that are *about* a place — a location's stock listing
	// and the low-stock report for one shop — and is absent everywhere else,
	// because a variant is a store-wide thing and StockOnHand above is its
	// store-wide sum. When both are present they are both true and they are
	// answering different questions.
	AtLocation     *VariantStock `json:"at_location,omitempty"`
	TrackInventory bool          `json:"track_inventory"`
	// ContinueSelling takes the order when the count has run out, instead of
	// refusing it. Off by default: overselling is a promise the store has to
	// keep by hand, so it is opted into per variant rather than inherited.
	ContinueSelling bool `json:"continue_selling"`
	Active          bool `json:"active"`
	// OriginCountry is where the item was made, as ISO 3166-1 alpha-2, and
	// HSCode is its tariff number. Both are customs paperwork rather than
	// merchandising, and both are empty until somebody ships across a border.
	OriginCountry string `json:"origin_country,omitempty"`
	HSCode        string `json:"hs_code,omitempty"`
	// Weight is stored in grams and read in WeightUnit — the same split as
	// money's amount_minor plus currency. The grams are the fact a carrier is
	// given; the unit is how a person entered it and expects to see it back.
	WeightGrams *int   `json:"weight_grams,omitempty"`
	WeightUnit  string `json:"weight_unit,omitempty"`
	// Weight is the same mass rendered for display, so a client that only
	// wants to show it does not have to know that a pound is 453.59237 g.
	Weight string `json:"weight,omitempty"`
	// Dimensions are the parcel's three sides in whole millimetres, read in
	// DimensionUnit — the same split as the weight above. A nil side is one
	// nobody has measured, which is not a zero-sided box.
	Dimensions    Dimensions `json:"dimensions,omitempty"`
	DimensionUnit string     `json:"dimension_unit,omitempty"`
	// Size is the same parcel rendered for display — "30 × 20 × 45 cm" — so a
	// client that only wants to show it does not have to know that an inch is
	// 25.4 mm.
	Size string `json:"size,omitempty"`
	// Images are the ones of the product's media this variant shows, in
	// order — what a storefront swaps to when a shopper picks a colour. Empty
	// for most variants. Image is the first of them, kept because it is the
	// thumbnail every client that predates the list reads.
	Images   []VariantImage `json:"images,omitempty"`
	Image    *VariantImage  `json:"image,omitempty"`
	Position int            `json:"position"`
	Metadata Metadata       `json:"metadata"`

	optionKey string
}

// VariantImage is the trimmed media row a variant nominates: enough to render
// a thumbnail and link to the file, and no more. The full record is on the
// product's media list, which every client that shows a variant already has.
type VariantImage struct {
	MediaID int64  `json:"media_id"`
	URL     string `json:"url"`
	Kind    string `json:"kind"`
	Alt     string `json:"alt,omitempty"`
}

// InStock reports whether qty units may be sold. A variant that does not track
// inventory is always sellable — the case for digital goods and made-to-order.
func (v *Variant) InStock(qty int) bool {
	return !v.TrackInventory || v.ContinueSelling || v.Available >= qty
}

// ---------------------------------------------------------------------- input

// ProductInput creates a product, optionally with its options and variants in
// the same call. Doing it in one transaction is what lets an import create a
// coherent product rather than a half-built one.
type ProductInput struct {
	Slug        string `json:"slug"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`

	ProductType    string   `json:"product_type"`
	Vendor         string   `json:"vendor"`
	Tags           []string `json:"tags"`
	CategoryID     *int64   `json:"category_id"`
	SEOTitle       string   `json:"seo_title"`
	SEODescription string   `json:"seo_description"`

	Metadata Metadata       `json:"metadata"`
	Options  []OptionInput  `json:"options"`
	Variants []VariantInput `json:"variants"`

	// SKU and PriceMinor are the shorthand for a product with no options: give
	// these instead of Variants and the engine creates the single default
	// variant for you.
	SKU        string `json:"sku"`
	PriceMinor *int64 `json:"price_minor"`
	Stock      *int   `json:"stock"`
}

// OptionInput defines one axis and its values, in display order.
type OptionInput struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}

// VariantInput creates a variant. Options names the value chosen on each of
// the product's axes, in any order — "M", "Black".
type VariantInput struct {
	SKU                 string   `json:"sku"`
	Barcode             string   `json:"barcode"`
	PriceMinor          int64    `json:"price_minor"`
	CompareAtPriceMinor *int64   `json:"compare_at_price_minor"`
	CostMinor           *int64   `json:"cost_minor"`
	Taxable             *bool    `json:"taxable"`
	RequiresShipping    *bool    `json:"requires_shipping"`
	Options             []string `json:"options"`
	StockOnHand         *int     `json:"stock_on_hand"`
	TrackInventory      *bool    `json:"track_inventory"`
	ContinueSelling     *bool    `json:"continue_selling"`
	Active              *bool    `json:"active"`
	OriginCountry       string   `json:"origin_country"`
	HSCode              string   `json:"hs_code"`
	WeightGrams         *int     `json:"weight_grams"`
	// WeightUnit is how the weight should be read back. Supplying WeightValue
	// instead lets a client send "2.5" and "kg" and let the engine do the
	// conversion, which is what a form actually has.
	WeightUnit  string   `json:"weight_unit"`
	WeightValue *float64 `json:"weight"`
	// Dimensions and DimensionValues are the two shapes of the same three
	// sides: millimetres straight from an API client, or what a person typed
	// plus DimensionUnit. DimensionValues wins when both arrive, for the reason
	// WeightValue does.
	Dimensions      Dimensions      `json:"dimensions"`
	DimensionValues DimensionValues `json:"size"`
	DimensionUnit   string          `json:"dimension_unit"`
	Position        *int            `json:"position"`
	Metadata        Metadata        `json:"metadata"`
}

// ProductPatch updates a product. Every field is optional; a nil field is left
// alone, which is what distinguishes "not mentioned" from "set to empty".
type ProductPatch struct {
	Slug        *string `json:"slug"`
	Title       *string `json:"title"`
	Description *string `json:"description"`
	Status      *string `json:"status"`

	ProductType *string `json:"product_type"`
	Vendor      *string `json:"vendor"`
	// A patched tag list replaces the whole set: tags have no identity of their
	// own, so "add one" is not a thing the engine can distinguish from a client
	// that simply sent a stale list.
	Tags *[]string `json:"tags"`
	// CategoryID is a NullableID because "None" is a choice an operator makes:
	// a plain pointer could file a product but never un-file it.
	CategoryID     NullableID `json:"category_id"`
	SEOTitle       *string    `json:"seo_title"`
	SEODescription *string    `json:"seo_description"`

	Metadata *Metadata `json:"metadata"`
}

// VariantPatch updates a variant. Stock is deliberately absent: inventory
// moves through the inventory service so that every change is a transactional
// adjustment rather than a blind overwrite of a number another request may
// have just changed.
type VariantPatch struct {
	SKU        *string `json:"sku"`
	Barcode    *string `json:"barcode"`
	PriceMinor *int64  `json:"price_minor"`
	// CompareAtPriceMinor is a NullableAmount for the same reason CostMinor is:
	// an emptied compare-at box means "this is not on sale any more", and a
	// plain pointer cannot tell that from a patch that never mentioned the
	// field — so a struck-through price could be set and never taken off.
	CompareAtPriceMinor NullableAmount `json:"compare_at_price_minor"`
	// CostMinor is a NullableAmount because an emptied cost box means "nobody
	// has recorded one", and a plain pointer cannot tell that from a patch that
	// never mentioned cost at all.
	CostMinor        NullableAmount `json:"cost_minor"`
	Taxable          *bool          `json:"taxable"`
	RequiresShipping *bool          `json:"requires_shipping"`
	TrackInventory   *bool          `json:"track_inventory"`
	ContinueSelling  *bool          `json:"continue_selling"`
	Active           *bool          `json:"active"`
	OriginCountry    *string        `json:"origin_country"`
	HSCode           *string        `json:"hs_code"`
	WeightGrams      *int           `json:"weight_grams"`
	WeightUnit       *string        `json:"weight_unit"`
	// WeightValue is the weight in WeightUnit. Sending both it and the unit is
	// the form's shape; sending WeightGrams is the API's. Both are accepted,
	// and WeightValue wins, because a client that computed grams itself and
	// then also sent a value disagreed with itself.
	WeightValue *float64 `json:"weight"`
	// The parcel, in the unit the patch names. Mentioning the unit alone
	// re-reads the sides already stored in the new unit, exactly as WeightUnit
	// alone does: the millimetres are the fact and switching units is a change
	// of how they are read, not of how big the box is.
	Size          DimensionPatch `json:"size"`
	DimensionUnit *string        `json:"dimension_unit"`
	Position      *int           `json:"position"`
	Metadata      *Metadata      `json:"metadata"`
}

// ProductQuery filters a product listing. Every field is optional; a zero one
// does not narrow the result.
type ProductQuery struct {
	Search      string
	Status      string
	Vendor      string
	ProductType string
	// CategoryID matches the category and everything nested under it.
	CategoryID int64
	// Tag matches products carrying exactly this tag. Exact rather than
	// case-folded so the match is answered by the GIN index on products.tags;
	// tags are de-duplicated case-insensitively on write, so one spelling of a
	// tag is the only spelling stored.
	Tag string
	// CollectionID restricts the listing to one collection's members and
	// switches the ordering to the collection's own, which is the order an
	// operator curated by hand.
	CollectionID int64
	// Attributes narrows by the answers a category asked for. Several values of
	// one attribute widen the result and several attributes narrow it — see
	// AttributeFilter.
	Attributes []AttributeFilter
	// ChannelID narrows to one storefront: products published to it, plus every
	// product published to no channel at all. Zero does not narrow, which is
	// what a store with no channels wants and what every admin listing wants.
	ChannelID int64
	// Sort is an operator-chosen ordering. Zero keeps the listing's own —
	// newest first, or a collection's curated one — and an explicit sort beats
	// both, because the operator who asked for one asked for this one.
	Sort   Sort
	Limit  int
	Offset int
}

// AttributeFilter is one facet: an attribute's handle and the values that count
// as a match.
//
// The two directions are deliberately not symmetric, because this is what a
// person ticking boxes means. Several values of ONE attribute widen the result
// — canvas OR leather, because a shopper who ticks both wants either — while
// several attributes narrow it — canvas AND black, because they are describing
// one bag. A filter that ANDed within an attribute would return nothing the
// moment a second box was ticked, which is the usual way this is got wrong.
//
// An empty Values is not a filter at all rather than a filter matching nothing:
// an unticked facet must leave the listing alone, not empty the screen.
type AttributeFilter struct {
	Key    string
	Values []string
}

// -------------------------------------------------------------------- service

// Catalog owns products, options and variants.
type Catalog struct {
	app *App
}

// Products returns the catalog service.
func (a *App) Products() *Catalog { return a.catalog }

// CreateProduct creates a product with its options and variants in one
// transaction.
func (c *Catalog) CreateProduct(ctx context.Context, in ProductInput) (*Product, error) {
	if err := c.normalizeInput(&in); err != nil {
		return nil, err
	}

	var id int64
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		meta, err := in.Metadata.value()
		if err != nil {
			return Validationf("metadata is not valid JSON: %v", err)
		}
		tags, err := tagsValue(in.Tags)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO products (slug, title, description, status, currency,
			                      product_type, vendor, tags, category_id,
			                      seo_title, seo_description, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, $7, `+tagsExpr(8)+`, $9, $10, $11, $12) RETURNING id`,
			in.Slug, in.Title, in.Description, in.Status, c.app.cfg.Currency,
			in.ProductType, in.Vendor, tags, in.CategoryID, in.SEOTitle, in.SEODescription, meta,
		).Scan(&id); err != nil {
			return translateCatalogErr(err)
		}

		values, err := c.insertOptions(ctx, tx, id, in.Options, 0)
		if err != nil {
			return err
		}
		for i, v := range in.Variants {
			if strings.TrimSpace(v.SKU) == "" {
				v.SKU, err = freeSKU(ctx, tx, derivedSKU(in.Slug, v.Options...))
				if err != nil {
					return err
				}
			}
			if _, err := c.insertVariant(ctx, tx, id, v, values, i); err != nil {
				return err
			}
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditProductCreate, Entity: AuditEntityProduct,
			ID: id, Label: in.Title, Summary: "Created the product " + in.Title,
			After: map[string]any{"slug": in.Slug, "title": in.Title, "status": in.Status},
		})
	})
	if err != nil {
		return nil, err
	}
	return c.GetProduct(ctx, id)
}

// derivedSKU is the code a variant gets when nobody supplied one: the
// product's slug, then its option values, upper-cased and hyphenated.
//
// The same rule the option matrix has always used to name the variants it
// generates (see generateCombinations), lifted out so that a variant made by
// hand and one made by the matrix are named alike. A SKU somebody typed is
// never touched by this.
func derivedSKU(slug string, parts ...string) string {
	out := slug
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out += "-" + p
		}
	}
	return strings.ReplaceAll(strings.ToUpper(out), " ", "-")
}

// freeSKU returns base, or base with the lowest number after it that no
// variant is using.
//
// A typed SKU that collides is refused, and rightly: two products the operator
// named the same thing is a decision to revisit. A derived one is different —
// nobody chose it, so there is nobody to send back to fix it, and refusing
// would block a save over a code the operator never saw. Two products called
// "Field jumper" in different years is an ordinary thing to want.
//
// The walk is bounded because a store with a thousand collisions on one stem
// has a naming problem this cannot solve; at that point the operator supplies
// one.
func freeSKU(ctx context.Context, tx *sql.Tx, base string) (string, error) {
	for n := 1; n <= 50; n++ {
		candidate := base
		if n > 1 {
			candidate = base + "-" + strconv.Itoa(n)
		}
		var taken bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM variants WHERE sku = $1)`, candidate).Scan(&taken); err != nil {
			return "", Internalf(err, "check sku")
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", Validationf("could not derive a free sku from %q; supply one", base)
}

// normalizeInput validates and fills in the shorthand forms.
func (c *Catalog) normalizeInput(in *ProductInput) error {
	in.Title = strings.TrimSpace(in.Title)
	if in.Title == "" {
		return Validationf("title is required")
	}
	if in.Slug = strings.TrimSpace(in.Slug); in.Slug == "" {
		in.Slug = slugify(in.Title)
	}
	if in.Slug == "" {
		return Validationf("slug could not be derived from the title; supply one")
	}
	if in.Status == "" {
		in.Status = ProductDraft
	}
	if !validProductStatus(in.Status) {
		return Validationf("status must be draft, active or archived")
	}
	in.ProductType = strings.TrimSpace(in.ProductType)
	in.Vendor = strings.TrimSpace(in.Vendor)
	in.SEOTitle = strings.TrimSpace(in.SEOTitle)
	in.SEODescription = strings.TrimSpace(in.SEODescription)
	in.Tags = normalizeTags(in.Tags)

	// The single-variant shorthand: a product with no options is still a
	// product with one variant, the client just should not have to say so.
	if len(in.Variants) == 0 {
		if in.PriceMinor == nil {
			return Validationf("supply either variants, or price_minor for a single-variant product")
		}
		// An absent SKU is derived below, inside the transaction, where a
		// collision can be walked past. Left empty here on purpose so that the
		// variant loop can tell "nobody gave one" from "somebody gave this".
		v := VariantInput{SKU: in.SKU, PriceMinor: *in.PriceMinor, StockOnHand: in.Stock}
		in.Variants = []VariantInput{v}
	}
	if len(in.Options) == 0 && len(in.Variants) > 1 {
		return Validationf("a product with no options can have only one variant")
	}
	for i := range in.Variants {
		if in.Variants[i].PriceMinor < 0 {
			return Validationf("variants[%d].price_minor must not be negative", i)
		}
	}
	return nil
}

// insertOptions writes the product's option axes and returns a lookup from
// option value ("M") to its row id.
func (c *Catalog) insertOptions(ctx context.Context, tx *sql.Tx, productID int64, opts []OptionInput, basePos int) (map[string]int64, error) {
	values := map[string]int64{}
	for i, opt := range opts {
		name := strings.TrimSpace(opt.Name)
		if name == "" {
			return nil, Validationf("options[%d].name is required", i)
		}
		if len(opt.Values) == 0 {
			return nil, Validationf("option %q needs at least one value", name)
		}
		var optionID int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO product_options (product_id, name, position)
			VALUES ($1, $2, $3) RETURNING id`, productID, name, basePos+i).Scan(&optionID); err != nil {
			return nil, translateCatalogErr(err)
		}
		for j, raw := range opt.Values {
			value := strings.TrimSpace(raw)
			if value == "" {
				return nil, Validationf("option %q has an empty value", name)
			}
			var valueID int64
			if err := tx.QueryRowContext(ctx, `
				INSERT INTO product_option_values (option_id, value, position)
				VALUES ($1, $2, $3) RETURNING id`, optionID, value, j).Scan(&valueID); err != nil {
				return nil, translateCatalogErr(err)
			}
			if _, dup := values[strings.ToLower(value)]; dup {
				// Two axes sharing a value ("Small" as both a size and a cup
				// size) would make a variant's option list ambiguous.
				return nil, Validationf("option value %q appears on more than one option", value)
			}
			values[strings.ToLower(value)] = valueID
		}
	}
	return values, nil
}

func (c *Catalog) insertVariant(ctx context.Context, tx *sql.Tx, productID int64, in VariantInput, values map[string]int64, position int) (int64, error) {
	valueIDs, err := resolveOptionValues(in.Options, values)
	if err != nil {
		return 0, err
	}

	meta, err := in.Metadata.value()
	if err != nil {
		return 0, Validationf("variant metadata is not valid JSON: %v", err)
	}
	stock := 0
	if in.StockOnHand != nil {
		if *in.StockOnHand < 0 {
			return 0, Validationf("stock_on_hand must not be negative")
		}
		stock = *in.StockOnHand
	}
	pos := position
	if in.Position != nil {
		pos = *in.Position
	}
	grams, unit, err := resolveWeight(in.WeightGrams, in.WeightValue, in.WeightUnit)
	if err != nil {
		return 0, err
	}
	size, sizeUnit, err := resolveDimensions(in.Dimensions, in.DimensionValues, in.DimensionUnit)
	if err != nil {
		return 0, err
	}
	origin, err := normalizeOriginCountry(in.OriginCountry)
	if err != nil {
		return 0, err
	}
	hs, err := normalizeHSCode(in.HSCode)
	if err != nil {
		return 0, err
	}

	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO variants (product_id, sku, barcode, price_minor, compare_at_price_minor,
		                      cost_minor, taxable, requires_shipping, track_inventory,
		                      continue_selling, active, origin_country, hs_code,
		                      weight_grams, weight_unit,
		                      length_mm, width_mm, height_mm, dimension_unit,
		                      position, option_key, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
		RETURNING id`,
		productID, strings.TrimSpace(in.SKU), nullString(in.Barcode), in.PriceMinor,
		in.CompareAtPriceMinor, in.CostMinor, boolOr(in.Taxable, true), boolOr(in.RequiresShipping, true),
		boolOr(in.TrackInventory, true), boolOr(in.ContinueSelling, false),
		boolOr(in.Active, true), origin, hs,
		grams, unit,
		size.Length, size.Width, size.Height, sizeUnit,
		pos, optionKey(valueIDs), meta,
	).Scan(&id)
	if err != nil {
		return 0, translateCatalogErr(err)
	}

	// A variant is created with its opening stock in one place. `stock_on_hand`
	// on the input has no location and never did, so it means the default one;
	// anything else is a transfer, made afterwards and on purpose.
	loc, err := defaultLocationID(ctx, tx)
	if err != nil {
		return 0, err
	}
	var after stockBalance
	if err := tx.QueryRowContext(ctx,
		`INSERT INTO variant_stock (variant_id, location_id, on_hand) VALUES ($1, $2, $3)
		 RETURNING on_hand, reserved`,
		id, loc, stock).Scan(&after.OnHand, &after.Reserved); err != nil {
		return 0, translateCatalogErr(err)
	}
	// Without this a variant created after M26 would carry a balance its own
	// ledger could not explain — the same hole the migration's seed closes for
	// the ones created before it. recordMovement writes nothing when the
	// opening stock is zero, so a variant created empty stays quiet.
	if _, err := recordMovement(ctx, tx, id, loc, MovementOpening,
		stockBalance{OnHand: stock}, after, stockRef{Reason: "opening stock"}); err != nil {
		return 0, err
	}

	for _, valueID := range valueIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO variant_option_values (variant_id, option_value_id) VALUES ($1, $2)`,
			id, valueID); err != nil {
			return 0, translateCatalogErr(err)
		}
	}
	return id, nil
}

// resolveOptionValues maps a variant's chosen values to their row ids.
func resolveOptionValues(options []string, values map[string]int64) ([]int64, error) {
	if len(options) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(options))
	for _, opt := range options {
		id, ok := values[strings.ToLower(strings.TrimSpace(opt))]
		if !ok {
			return nil, Validationf("option value %q is not defined on this product", opt)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// optionKey normalises a variant's option selection into the string the unique
// index compares. Sorting is what makes {Black, M} and {M, Black} the same
// combination, which is the whole point of the constraint.
func optionKey(valueIDs []int64) string {
	if len(valueIDs) == 0 {
		return ""
	}
	sorted := append([]int64(nil), valueIDs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	parts := make([]string, len(sorted))
	for i, id := range sorted {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

// UpdateProduct applies a patch.
func (c *Catalog) UpdateProduct(ctx context.Context, id int64, patch ProductPatch) (*Product, error) {
	sets, args := []string{}, []any{}
	// after is filled by the same closure that builds the statement, so the
	// audit record can never name a column the UPDATE did not set.
	after := map[string]any{}
	add := func(expr string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", expr, len(args)))
		after[expr] = v
	}
	if patch.Slug != nil {
		s := strings.TrimSpace(*patch.Slug)
		if s == "" {
			return nil, Validationf("slug must not be empty")
		}
		add("slug", s)
	}
	if patch.Title != nil {
		if strings.TrimSpace(*patch.Title) == "" {
			return nil, Validationf("title must not be empty")
		}
		add("title", strings.TrimSpace(*patch.Title))
	}
	if patch.Description != nil {
		add("description", *patch.Description)
	}
	if patch.Status != nil {
		if !validProductStatus(*patch.Status) {
			return nil, Validationf("status must be draft, active or archived")
		}
		add("status", *patch.Status)
	}
	if patch.ProductType != nil {
		add("product_type", strings.TrimSpace(*patch.ProductType))
	}
	if patch.Vendor != nil {
		add("vendor", strings.TrimSpace(*patch.Vendor))
	}
	if patch.Tags != nil {
		tags, err := tagsValue(*patch.Tags)
		if err != nil {
			return nil, err
		}
		args = append(args, tags)
		sets = append(sets, "tags = "+tagsExpr(len(args)))
		after["tags"] = *patch.Tags
	}
	if patch.CategoryID.Present {
		add("category_id", patch.CategoryID.Value)
	}
	if patch.SEOTitle != nil {
		add("seo_title", strings.TrimSpace(*patch.SEOTitle))
	}
	if patch.SEODescription != nil {
		add("seo_description", strings.TrimSpace(*patch.SEODescription))
	}
	if patch.Metadata != nil {
		meta, err := patch.Metadata.value()
		if err != nil {
			return nil, Validationf("metadata is not valid JSON: %v", err)
		}
		add("metadata", meta)
	}
	if len(sets) == 0 {
		return c.GetProduct(ctx, id)
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)

	query := "UPDATE products SET " + strings.Join(sets, ", ") +
		fmt.Sprintf(" WHERE id = $%d RETURNING title", len(args))

	// The write gains a transaction so the change and the record of it commit
	// together. The extra SELECT takes a row lock the UPDATE two lines below
	// takes anyway — a round trip earlier, on an edit screen, and never on the
	// checkout path.
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		before := map[string]any{}
		var slug, title, status, productType, vendor string
		var categoryID *int64
		err := tx.QueryRowContext(ctx, `
			SELECT slug, title, status, product_type, vendor, category_id
			FROM products WHERE id = $1 FOR UPDATE`, id,
		).Scan(&slug, &title, &status, &productType, &vendor, &categoryID)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("product %d does not exist", id)
		}
		if err != nil {
			return err
		}
		for col, was := range map[string]any{
			"slug": slug, "title": title, "status": status,
			"product_type": productType, "vendor": vendor, "category_id": categoryID,
		} {
			if _, changed := after[col]; changed {
				before[col] = was
			}
		}

		var newTitle string
		if err := tx.QueryRowContext(ctx, query, args...).Scan(&newTitle); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return NotFoundf("product %d does not exist", id)
			}
			return translateCatalogErr(err)
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditProductUpdate, Entity: AuditEntityProduct,
			ID: id, Label: newTitle, Summary: "Edited the product " + newTitle,
			Before: before, After: after,
		})
	})
	if err != nil {
		return nil, err
	}
	return c.GetProduct(ctx, id)
}

// DeleteProduct removes a product and everything hanging off it. Order lines
// survive: they hold their own snapshot and their product reference is nulled.
func (c *Catalog) DeleteProduct(ctx context.Context, id int64) error {
	return InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		// RETURNING rather than RowsAffected, because the audit row needs the
		// name: a deleted product is exactly what somebody opens the log to ask
		// about, and after this statement nothing else can tell them.
		var title, slug string
		err := tx.QueryRowContext(ctx,
			`DELETE FROM products WHERE id = $1 RETURNING title, slug`, id).Scan(&title, &slug)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("product %d does not exist", id)
		}
		if err != nil {
			return translateCatalogErr(err)
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditProductDelete, Entity: AuditEntityProduct,
			ID: id, Label: title, Summary: "Deleted the product " + title,
			Before: map[string]any{"slug": slug, "title": title},
		})
	})
}

// AddOption adds an axis to an existing product.
func (c *Catalog) AddOption(ctx context.Context, productID int64, in OptionInput) (*Product, error) {
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM products WHERE id = $1)`, productID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return NotFoundf("product %d does not exist", productID)
		}
		var next int
		if err := tx.QueryRowContext(ctx,
			`SELECT coalesce(max(position) + 1, 0) FROM product_options WHERE product_id = $1`,
			productID).Scan(&next); err != nil {
			return err
		}
		if _, err := c.insertOptions(ctx, tx, productID, []OptionInput{in}, next); err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditProductOptionAdd, Entity: AuditEntityProduct,
			ID: productID, Summary: "Added the option " + in.Name,
			After: map[string]any{"option": in.Name, "values": in.Values},
		})
	})
	if err != nil {
		return nil, err
	}
	return c.GetProduct(ctx, productID)
}

// CreateVariant adds a variant to an existing product.
//
// A SKU is optional here as it is on create: without one the variant is named
// after its product and its option values, by the same rule the option matrix
// uses, and the operator can rename it afterwards.
func (c *Catalog) CreateVariant(ctx context.Context, productID int64, in VariantInput) (*Variant, error) {
	var id int64
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		values, err := c.optionValueLookup(ctx, tx, productID)
		if err != nil {
			return err
		}
		if strings.TrimSpace(in.SKU) == "" {
			var slug string
			if err := tx.QueryRowContext(ctx,
				`SELECT slug FROM products WHERE id = $1`, productID).Scan(&slug); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return NotFoundf("product %d does not exist", productID)
				}
				return Internalf(err, "read product slug")
			}
			if in.SKU, err = freeSKU(ctx, tx, derivedSKU(slug, in.Options...)); err != nil {
				return err
			}
		}
		if len(values) == 0 && len(in.Options) > 0 {
			return Validationf("this product has no options")
		}
		if len(values) > 0 && len(in.Options) == 0 {
			return Validationf("this product has options, so a variant must choose values for them")
		}
		var next int
		if err := tx.QueryRowContext(ctx,
			`SELECT coalesce(max(position) + 1, 0) FROM variants WHERE product_id = $1`,
			productID).Scan(&next); err != nil {
			return err
		}
		id, err = c.insertVariant(ctx, tx, productID, in, values, next)
		if err != nil {
			return err
		}
		after := map[string]any{
			"variant_id": id, "sku": in.SKU,
			"price_minor": in.PriceMinor, "currency": c.app.cfg.Currency,
		}
		if in.Barcode != "" {
			after["barcode"] = in.Barcode
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditVariantCreate, Entity: AuditEntityProduct,
			ID: productID, Label: in.SKU, Summary: "Added the variant " + in.SKU,
			After: after,
		})
	})
	if err != nil {
		return nil, err
	}
	return c.GetVariant(ctx, id)
}

func (c *Catalog) optionValueLookup(ctx context.Context, tx *sql.Tx, productID int64) (map[string]int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT v.id, v.value
		FROM product_option_values v
		JOIN product_options o ON o.id = v.option_id
		WHERE o.product_id = $1`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	values := map[string]int64{}
	for rows.Next() {
		var id int64
		var value string
		if err := rows.Scan(&id, &value); err != nil {
			return nil, err
		}
		values[strings.ToLower(value)] = id
	}
	return values, rows.Err()
}

// UpdateVariant applies a patch. Stock is not patchable here; see [Inventory].
func (c *Catalog) UpdateVariant(ctx context.Context, id int64, patch VariantPatch) (*Variant, error) {
	sets, args := []string{}, []any{}
	after := map[string]any{}
	add := func(expr string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", expr, len(args)))
		after[expr] = v
	}
	if patch.SKU != nil {
		if strings.TrimSpace(*patch.SKU) == "" {
			return nil, Validationf("sku must not be empty")
		}
		add("sku", strings.TrimSpace(*patch.SKU))
	}
	if patch.Barcode != nil {
		add("barcode", nullString(*patch.Barcode))
	}
	if patch.PriceMinor != nil {
		if *patch.PriceMinor < 0 {
			return nil, Validationf("price_minor must not be negative")
		}
		add("price_minor", *patch.PriceMinor)
	}
	if patch.CompareAtPriceMinor.Present {
		// Guarded here rather than left to the column CHECK, which
		// translateCatalogErr does not recognise: a negative number would come
		// back as a 500 on what is plainly a client mistake.
		if v := patch.CompareAtPriceMinor.Value; v != nil && *v < 0 {
			return nil, Validationf("compare_at_price_minor must not be negative")
		}
		add("compare_at_price_minor", patch.CompareAtPriceMinor.Value)
	}
	if patch.CostMinor.Present {
		if v := patch.CostMinor.Value; v != nil && *v < 0 {
			return nil, Validationf("cost_minor must not be negative")
		}
		add("cost_minor", patch.CostMinor.Value)
	}
	if patch.Taxable != nil {
		add("taxable", *patch.Taxable)
	}
	if patch.RequiresShipping != nil {
		add("requires_shipping", *patch.RequiresShipping)
	}
	if patch.TrackInventory != nil {
		add("track_inventory", *patch.TrackInventory)
	}
	if patch.ContinueSelling != nil {
		// Switching it off restores the invariant that stock never goes
		// negative, which a variant already past zero cannot satisfy. The CHECK
		// refuses it either way; this is what turns that into a sentence naming
		// the shortfall, since "adjust the stock first" is the only way out and
		// the constraint does not say so.
		if !*patch.ContinueSelling {
			// The largest shortfall at any single location, not the total: the
			// invariant is per shelf, and a store with +5 in the warehouse and
			// -2 in the shop is still oversold by 2 in the shop.
			var short int
			err := c.app.db.QueryRowContext(ctx,
				`SELECT coalesce(max(reserved - on_hand), 0) FROM variant_stock
				 WHERE variant_id = $1`,
				id).Scan(&short)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, Internalf(err, "read stock")
			}
			if short > 0 {
				return nil, Conflictf(
					"this variant is oversold by %d; add stock before it stops selling past zero",
					short)
			}
		}
		add("continue_selling", *patch.ContinueSelling)
	}
	if patch.Active != nil {
		add("active", *patch.Active)
	}
	if patch.OriginCountry != nil {
		origin, err := normalizeOriginCountry(*patch.OriginCountry)
		if err != nil {
			return nil, err
		}
		add("origin_country", origin)
	}
	if patch.HSCode != nil {
		hs, err := normalizeHSCode(*patch.HSCode)
		if err != nil {
			return nil, err
		}
		add("hs_code", hs)
	}
	// Weight and its unit move together: converting a value needs to know
	// which unit it is in, and changing only the unit would silently restate
	// the same grams as a different mass.
	if patch.WeightGrams != nil || patch.WeightValue != nil || patch.WeightUnit != nil {
		unit := ""
		if patch.WeightUnit != nil {
			unit = *patch.WeightUnit
		} else {
			// Not mentioned: keep whatever the variant already reads in.
			_ = c.app.db.QueryRowContext(ctx,
				`SELECT weight_unit FROM variants WHERE id = $1`, id).Scan(&unit)
		}
		grams, normalized, err := resolveWeight(patch.WeightGrams, patch.WeightValue, unit)
		if err != nil {
			return nil, err
		}
		if grams != nil {
			add("weight_grams", *grams)
		}
		add("weight_unit", normalized)
	}
	// The parcel and its unit move together, for the reason the weight above
	// gives: converting a typed side needs to know which unit it is in, and
	// changing only the unit would silently restate the same millimetres as a
	// different box.
	if patch.Size.Mentioned() || patch.DimensionUnit != nil {
		unit := ""
		if patch.DimensionUnit != nil {
			unit = *patch.DimensionUnit
		} else {
			// Not mentioned: keep whatever the variant already reads in.
			_ = c.app.db.QueryRowContext(ctx,
				`SELECT dimension_unit FROM variants WHERE id = $1`, id).Scan(&unit)
		}
		sides, normalized, err := resolveDimensionPatch(patch.Size, unit)
		if err != nil {
			return nil, err
		}
		// Only the sides the patch spoke about, so changing the height alone
		// does not erase a length somebody measured earlier. A mentioned side
		// with no value is an emptied box, which stores NULL — "nobody has
		// measured this" — rather than a zero, which would claim a flat parcel.
		for _, side := range sides {
			if side.MM == nil {
				add(side.Column, nil)
				continue
			}
			add(side.Column, *side.MM)
		}
		add("dimension_unit", normalized)
	}
	if patch.Position != nil {
		add("position", *patch.Position)
	}
	if patch.Metadata != nil {
		meta, err := patch.Metadata.value()
		if err != nil {
			return nil, Validationf("metadata is not valid JSON: %v", err)
		}
		add("metadata", meta)
	}
	if len(sets) == 0 {
		return c.GetVariant(ctx, id)
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)

	// A variant edit is filed against its PRODUCT: the panel edits variants
	// inside the product editor, and that is the record whose history a
	// merchandiser reads. Stock movements are the other half and are filed
	// against the variant under entity type `stock`, which is the line
	// catalog.read and inventory.read are already drawn along.
	err := InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		// This method reads nothing about the row today, so `before` costs one
		// real statement — acceptable on an edit screen, and this is the row
		// that answers "who dropped the price".
		before := map[string]any{}
		var wasSKU string
		var wasBarcode sql.NullString
		var wasPrice int64
		var wasCompare sql.NullInt64
		var wasPosition int
		var wasActive bool
		var productID int64
		err := tx.QueryRowContext(ctx, `
			SELECT product_id, sku, price_minor, compare_at_price_minor, barcode, position, active
			FROM variants WHERE id = $1 FOR UPDATE`, id,
		).Scan(&productID, &wasSKU, &wasPrice, &wasCompare, &wasBarcode, &wasPosition, &wasActive)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("variant %d does not exist", id)
		}
		if err != nil {
			return err
		}
		for col, was := range map[string]any{
			"sku": wasSKU, "price_minor": wasPrice, "position": wasPosition, "active": wasActive,
			"compare_at_price_minor": nullInt64Value(wasCompare),
			"barcode":                wasBarcode.String,
		} {
			if _, changed := after[col]; changed {
				before[col] = was
			}
		}

		var sku string
		err = tx.QueryRowContext(ctx,
			"UPDATE variants SET "+strings.Join(sets, ", ")+
				fmt.Sprintf(" WHERE id = $%d RETURNING product_id, sku", len(args)), args...,
		).Scan(&productID, &sku)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("variant %d does not exist", id)
		}
		if err != nil {
			return translateCatalogErr(err)
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditVariantUpdate, Entity: AuditEntityProduct,
			ID: productID, Label: sku, Summary: "Edited the variant " + sku,
			Before: before, After: after,
		})
	})
	if err != nil {
		return nil, err
	}
	return c.GetVariant(ctx, id)
}

// nullInt64Value renders a nullable integer for a changes payload: the number
// when there is one, and JSON null when there is not. A zero would be a lie —
// "no compare-at price" and "a compare-at price of zero" are different facts.
func nullInt64Value(v sql.NullInt64) any {
	if !v.Valid {
		return nil
	}
	return v.Int64
}

// DeleteVariant removes a variant. The last variant of a product cannot be
// removed: a product with nothing sellable is not a product, and every
// downstream reference assumes one exists.
func (c *Catalog) DeleteVariant(ctx context.Context, id int64) error {
	return InTx(ctx, c.app.db, func(tx *sql.Tx) error {
		var productID int64
		if err := tx.QueryRowContext(ctx,
			`SELECT product_id FROM variants WHERE id = $1`, id).Scan(&productID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return NotFoundf("variant %d does not exist", id)
			}
			return err
		}
		var count int
		if err := tx.QueryRowContext(ctx,
			`SELECT count(*) FROM variants WHERE product_id = $1`, productID).Scan(&count); err != nil {
			return err
		}
		if count <= 1 {
			return Conflictf("a product must keep at least one variant")
		}
		var sku string
		if err := tx.QueryRowContext(ctx,
			`DELETE FROM variants WHERE id = $1 RETURNING sku`, id).Scan(&sku); err != nil {
			return translateCatalogErr(err)
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditVariantDelete, Entity: AuditEntityProduct,
			ID: productID, Label: sku, Summary: "Deleted the variant " + sku,
			Before: map[string]any{"variant_id": id, "sku": sku},
		})
	})
}

// ------------------------------------------------------------------- reading

// productColumns selects tags as JSON rather than as text[]. pgx's
// database/sql driver hands a text[] back as its raw PostgreSQL literal, which
// would leave this package parsing quoted array syntax; to_jsonb turns it into
// something encoding/json already knows how to read, and tagsExpr converts
// back on the way in. See tagsValue.
const productColumns = `p.id, p.slug, p.title, p.description, p.status, p.currency,
	p.product_type, p.vendor, to_jsonb(p.tags), p.seo_title, p.seo_description,
	p.metadata, p.created_at, p.updated_at`

// scanProduct reads one productColumns row. Both the single-row and the list
// query go through it, so a column added to one is never missing from the
// other.
func scanProduct(row interface{ Scan(...any) error }) (*Product, error) {
	var p Product
	var tags, meta []byte
	if err := row.Scan(&p.ID, &p.Slug, &p.Title, &p.Description, &p.Status, &p.Currency,
		&p.ProductType, &p.Vendor, &tags, &p.SEOTitle, &p.SEODescription,
		&meta, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	if err := scanMetadata(meta, &p.Metadata); err != nil {
		return nil, err
	}
	if err := scanTags(tags, &p.Tags); err != nil {
		return nil, err
	}
	return &p, nil
}

// GetProduct loads a product with its options and variants.
func (c *Catalog) GetProduct(ctx context.Context, id int64) (*Product, error) {
	return c.getProductWhere(ctx, "p.id = $1", id)
}

// GetProductBySlug loads a product by its URL slug.
func (c *Catalog) GetProductBySlug(ctx context.Context, slug string) (*Product, error) {
	return c.getProductWhere(ctx, "p.slug = $1", slug)
}

func (c *Catalog) getProductWhere(ctx context.Context, where string, arg any) (*Product, error) {
	p, err := scanProduct(c.app.db.QueryRowContext(ctx,
		`SELECT `+productColumns+` FROM products p WHERE `+where, arg))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, NotFoundf("product not found")
		}
		return nil, err
	}
	if err := c.loadProductChildren(ctx, []*Product{p}); err != nil {
		return nil, err
	}
	return p, nil
}

// productFilters renders a ProductQuery as a join fragment and a set of
// predicates, appending each bound value to args and numbering its placeholder
// after whatever the caller had already bound.
//
// Taking and returning the slice is the whole point: the catalog export binds
// one argument per stock location before it filters anything, so a builder that
// assumed $1 would quietly select the wrong rows rather than fail, and every
// single-location test would still pass.
//
// The collection join cannot multiply rows: the primary key is
// (product_id, collection_id) and the join pins one collection_id, so a product
// contributes at most one row to it.
//
// Ordering is deliberately not decided here. The listing and the export
// disagree about it on purpose: one wants a collection's curated order, the
// other wants the contiguous per-product runs the CSV importer requires.
func productFilters(q ProductQuery, args []any) (join string, where []string, out []any) {
	where = []string{"1 = 1"}
	// The collection argument is bound first, so the listing's own SQL is
	// numbered exactly as it was before this builder existed.
	if q.CollectionID > 0 {
		args = append(args, q.CollectionID)
		join = fmt.Sprintf(
			" JOIN product_collections pc ON pc.product_id = p.id AND pc.collection_id = $%d", len(args))
	}
	if q.Status != "" {
		args = append(args, q.Status)
		where = append(where, fmt.Sprintf("p.status = $%d", len(args)))
	}
	if s := strings.TrimSpace(q.Search); s != "" {
		args = append(args, "%"+strings.ToLower(s)+"%")
		where = append(where, fmt.Sprintf(
			"(lower(p.title) LIKE $%d OR lower(p.description) LIKE $%d)", len(args), len(args)))
	}
	if s := strings.TrimSpace(q.Vendor); s != "" {
		args = append(args, s)
		where = append(where, fmt.Sprintf("p.vendor = $%d", len(args)))
	}
	if s := strings.TrimSpace(q.ProductType); s != "" {
		args = append(args, s)
		where = append(where, fmt.Sprintf("p.product_type = $%d", len(args)))
	}
	if s := strings.TrimSpace(q.Tag); s != "" {
		args = append(args, s)
		where = append(where, fmt.Sprintf("p.tags @> ARRAY[$%d]::text[]", len(args)))
	}
	if q.CategoryID > 0 {
		args = append(args, q.CategoryID)
		where = append(where, categoryFilter(fmt.Sprintf("$%d", len(args))))
	}
	if q.ChannelID > 0 {
		args = append(args, q.ChannelID)
		where = append(where, channelFilter(fmt.Sprintf("$%d", len(args))))
	}
	// Containment rather than a join or an unnested comparison: a product's
	// answers live in one jsonb object, `{handle: [values]}`, and
	// `metadata->'category' @> '{"handle":["value"]}'` asks exactly the
	// question — does this product's answer list for that field include this
	// value — in one operator that the GIN index in M33 can answer. Unnesting
	// would need a lateral join per attribute and could not use an index at all.
	//
	// Exact, not LIKE. "Can" must not match "Canvas": a substring match here
	// would quietly cross value boundaries and would also give up the index.
	for _, f := range q.Attributes {
		key := strings.TrimSpace(f.Key)
		if key == "" || len(f.Values) == 0 {
			continue
		}
		ors := make([]string, 0, len(f.Values))
		for _, v := range f.Values {
			doc, err := json.Marshal(map[string][]string{key: {v}})
			if err != nil {
				// A string and a string cannot fail to marshal; skipping rather
				// than failing keeps a listing answerable whatever arrives.
				continue
			}
			args = append(args, string(doc))
			ors = append(ors, fmt.Sprintf("p.metadata -> 'category' @> $%d::jsonb", len(args)))
		}
		if len(ors) > 0 {
			where = append(where, "("+strings.Join(ors, " OR ")+")")
		}
	}
	return join, where, args
}

// productSorts is the product listing's allow-list. Every value is a literal
// written here; a request supplies only a key.
var productSorts = SortSpec{
	Tiebreak: "p.id",
	Columns: map[string]sortField{
		"title":      {"lower(p.title) ASC", "lower(p.title) DESC"},
		"status":     {"p.status ASC", "p.status DESC"},
		"created_at": {"p.created_at ASC", "p.created_at DESC"},
		"updated_at": {"p.updated_at ASC", "p.updated_at DESC"},
		"price":      {productMinPrice + " ASC NULLS LAST", productMinPrice + " DESC NULLS LAST"},
		"available":  {productAvailable + " ASC NULLS LAST", productAvailable + " DESC NULLS LAST"},
		"id":         {"p.id ASC", "p.id DESC"},
	},
}

// ListProducts returns a page of products and the total matching count.
func (c *Catalog) ListProducts(ctx context.Context, q ProductQuery) ([]*Product, int, error) {
	join, where, args := productFilters(q, nil)
	from, order := "products p"+join, "p.id DESC"
	if q.CollectionID > 0 {
		// A collection is curated by hand, so its own order is the answer to
		// "what order should these be in" — not the catalog's newest-first.
		// member_position, not position: position is where this collection sits
		// in the product's own list, which is a different question (D40).
		order = "pc.member_position, pc.product_id"
	}
	clause := strings.Join(where, " AND ")

	// Resolved before any database work, so a rejected sort costs none — and
	// after the collection branch above, so an explicit sort overrides the
	// curated order. That combination is reachable only through this Go API:
	// the HTTP handler refuses the two parameters together rather than choosing
	// silently on the operator's behalf.
	order, err := productSorts.Clause(q.Sort, order)
	if err != nil {
		return nil, 0, err
	}

	var total int
	if err := c.app.db.QueryRowContext(ctx,
		`SELECT count(*) FROM `+from+` WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	limit, offset := q.Limit, q.Offset
	if limit <= 0 {
		limit = DefaultLimit
	}
	args = append(args, limit, offset)
	rows, err := c.app.db.QueryContext(ctx,
		`SELECT `+productColumns+` FROM `+from+` WHERE `+clause+
			fmt.Sprintf(" ORDER BY %s LIMIT $%d OFFSET $%d", order, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var products []*Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, 0, err
		}
		products = append(products, p)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := c.loadProductChildren(ctx, products); err != nil {
		return nil, 0, err
	}
	return products, total, nil
}

// loadProductChildren fetches options and variants for a page of products in
// two queries rather than two per product.
func (c *Catalog) loadProductChildren(ctx context.Context, products []*Product) error {
	if len(products) == 0 {
		return nil
	}
	byID := make(map[int64]*Product, len(products))
	ids := make([]int64, 0, len(products))
	for _, p := range products {
		byID[p.ID] = p
		ids = append(ids, p.ID)
		p.Options = []ProductOption{}
		p.Variants = []Variant{}
		p.Collections = []ProductCollection{}
		p.Category = nil
		p.ImageURL = ""
		p.MediaCount = 0
	}

	// The lead picture, for the whole page in one query. DISTINCT ON takes the
	// first by position, which is the same image the media list shows first and
	// the same one a storefront would lead with.
	leadRows, err := c.app.db.QueryContext(ctx, `
		SELECT DISTINCT ON (pm.product_id) pm.product_id, m.url
		FROM product_media pm
		JOIN media m ON m.id = pm.media_id
		WHERE pm.product_id = ANY($1::bigint[]) AND m.kind = 'image'
		ORDER BY pm.product_id, pm.position, pm.media_id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer leadRows.Close()
	for leadRows.Next() {
		var productID int64
		var url string
		if err := leadRows.Scan(&productID, &url); err != nil {
			return err
		}
		if p := byID[productID]; p != nil {
			p.ImageURL = url
		}
	}
	if err := leadRows.Err(); err != nil {
		return err
	}
	countRows, err := c.app.db.QueryContext(ctx, `
		SELECT pm.product_id, count(*)
		FROM product_media pm JOIN media m ON m.id = pm.media_id
		WHERE pm.product_id = ANY($1::bigint[]) AND m.kind = 'image'
		GROUP BY pm.product_id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer countRows.Close()
	for countRows.Next() {
		var productID int64
		var n int
		if err := countRows.Scan(&productID, &n); err != nil {
			return err
		}
		if p := byID[productID]; p != nil {
			p.MediaCount = n
		}
	}
	if err := countRows.Err(); err != nil {
		return err
	}

	optionRows, err := c.app.db.QueryContext(ctx, `
		SELECT o.product_id, o.id, o.name, o.position,
		       coalesce(v.id, 0), coalesce(v.value, ''), coalesce(v.position, 0)
		FROM product_options o
		LEFT JOIN product_option_values v ON v.option_id = o.id
		WHERE o.product_id = ANY($1::bigint[])
		ORDER BY o.position, o.id, v.position, v.id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer optionRows.Close()

	optionIndex := map[int64]int{} // option id -> index within its product
	for optionRows.Next() {
		var productID, optionID, valueID int64
		var name, value string
		var optionPos, valuePos int
		if err := optionRows.Scan(&productID, &optionID, &name, &optionPos, &valueID, &value, &valuePos); err != nil {
			return err
		}
		p := byID[productID]
		if p == nil {
			continue
		}
		idx, seen := optionIndex[optionID]
		if !seen {
			p.Options = append(p.Options, ProductOption{
				ID: optionID, Name: name, Position: optionPos, Values: []OptionValue{},
			})
			idx = len(p.Options) - 1
			optionIndex[optionID] = idx
		}
		if valueID != 0 {
			p.Options[idx].Values = append(p.Options[idx].Values,
				OptionValue{ID: valueID, Value: value, Position: valuePos})
		}
	}
	if err := optionRows.Err(); err != nil {
		return err
	}

	variants, err := c.queryVariants(ctx, `v.product_id = ANY($1::bigint[])`, int64Array(ids))
	if err != nil {
		return err
	}
	for _, v := range variants {
		if p := byID[v.ProductID]; p != nil {
			p.Variants = append(p.Variants, *v)
		}
	}
	if err := c.app.loadProductCollections(ctx, byID, ids); err != nil {
		return err
	}
	return c.app.loadProductCategories(ctx, byID, ids)
}

// The two numbers M17 moved out of `variants` and into a row per place. They
// are still one number each on the way out, because that is what a variant *is*
// to a client — the store has this many — and because storing a copy of a sum is
// storing a number that can be wrong.
//
// Written as fragments rather than a join so that any WHERE or ORDER BY can use
// the same expression the SELECT does, and the two cannot drift.
const (
	variantOnHand   = `coalesce((SELECT sum(vs.on_hand)  FROM variant_stock vs WHERE vs.variant_id = v.id), 0)`
	variantReserved = `coalesce((SELECT sum(vs.reserved) FROM variant_stock vs WHERE vs.variant_id = v.id), 0)`
	// Available is the difference of the two, and deliberately not a third
	// independent sum: `available = on_hand - reserved` is stated in the API
	// docs, the skills and the smoke test, and an expression that could disagree
	// with it would make all three wrong at once.
	variantAvailable = `(` + variantOnHand + ` - ` + variantReserved + `)`
)

// The two orderings a product listing offers that are not columns on
// `products`, written as correlated scalar subqueries and never as joins.
//
// ListProducts counts from the same un-joined FROM the page query uses, so a
// join to variants would multiply a product by its variants and make meta.total
// disagree with the rows it describes — the one thing a sort must never do. A
// scalar subquery cannot change the row count, which is the property that makes
// it the only safe shape here.
const (
	// min, not max: the panel shows the range's left edge as the price, and
	// ordering DESC by max would mean ascending and descending sorted by two
	// different columns.
	productMinPrice = `(SELECT min(v.price_minor) FROM variants v WHERE v.product_id = p.id)`
	// NULL over zero tracked variants rather than 0, which is what the panel
	// means by "not tracked" — and why an untracked product sorts last in both
	// directions instead of at the bottom of the ascending page. It embeds
	// variantAvailable so the ordering and the number on screen are the same
	// arithmetic.
	productAvailable = `(SELECT sum(` + variantAvailable + `) FROM variants v
		WHERE v.product_id = p.id AND v.track_inventory)`
)

const variantColumns = `v.id, v.product_id, v.sku, coalesce(v.barcode, ''), v.price_minor,
	v.compare_at_price_minor, v.cost_minor, v.taxable, v.requires_shipping, ` + variantOnHand + `, ` + variantReserved + `,
	v.track_inventory, v.continue_selling, v.active, v.origin_country, v.hs_code,
	v.weight_grams, v.weight_unit,
	v.length_mm, v.width_mm, v.height_mm, v.dimension_unit,
	v.position, v.option_key, v.metadata`

func (c *Catalog) queryVariants(ctx context.Context, where string, args ...any) ([]*Variant, error) {
	return c.selectVariants(ctx, where, "v.position, v.id", "", args...)
}

// queryVariantsOrdered is queryVariants with an explicit ordering and a page.
func (c *Catalog) queryVariantsOrdered(ctx context.Context, where, orderBy string, limit, offset int, args ...any) ([]*Variant, error) {
	args = append(args, limit, offset)
	page := fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)-1, len(args))
	return c.selectVariants(ctx, where, orderBy, page, args...)
}

func (c *Catalog) selectVariants(ctx context.Context, where, orderBy, page string, args ...any) ([]*Variant, error) {
	rows, err := c.app.db.QueryContext(ctx,
		`SELECT `+variantColumns+` FROM variants v WHERE `+where+` ORDER BY `+orderBy+page, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var variants []*Variant
	ids := []int64{}
	byID := map[int64]*Variant{}
	for rows.Next() {
		v := &Variant{}
		var meta []byte
		var compareAt sql.NullInt64
		var cost sql.NullInt64
		var weight sql.NullInt64
		var length, width, height sql.NullInt64
		if err := rows.Scan(&v.ID, &v.ProductID, &v.SKU, &v.Barcode, &v.Price.AmountMinor,
			&compareAt, &cost, &v.Taxable, &v.RequiresShipping, &v.StockOnHand, &v.StockReserved,
			&v.TrackInventory, &v.ContinueSelling, &v.Active, &v.OriginCountry, &v.HSCode,
			&weight, &v.WeightUnit,
			&length, &width, &height, &v.DimensionUnit,
			&v.Position, &v.optionKey, &meta); err != nil {
			return nil, err
		}
		v.Price.Currency = c.app.cfg.Currency
		if compareAt.Valid {
			m := money(compareAt.Int64, c.app.cfg.Currency)
			v.CompareAtPrice = &m
		}
		if cost.Valid {
			m := money(cost.Int64, c.app.cfg.Currency)
			v.Cost = &m
		}
		if weight.Valid {
			w := int(weight.Int64)
			v.WeightGrams = &w
			// The rendered form travels with the raw one so a client that only
			// displays it never has to know the conversion factors.
			v.Weight = FormatWeight(w, v.WeightUnit)
		}
		// A side is carried only when it was measured, so an unmeasured parcel
		// answers with no dimensions at all rather than three zeroes.
		if length.Valid {
			mm := int(length.Int64)
			v.Dimensions.Length = &mm
		}
		if width.Valid {
			mm := int(width.Int64)
			v.Dimensions.Width = &mm
		}
		if height.Valid {
			mm := int(height.Int64)
			v.Dimensions.Height = &mm
		}
		if v.Dimensions.Set() {
			// The rendered form travels with the raw one, for the reason the
			// weight above gives.
			v.Size = FormatDimensions(v.Dimensions, v.DimensionUnit)
		}
		v.Available = v.StockOnHand - v.StockReserved
		v.Options = []string{}
		if err := scanMetadata(meta, &v.Metadata); err != nil {
			return nil, err
		}
		variants = append(variants, v)
		ids = append(ids, v.ID)
		byID[v.ID] = v
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(variants) == 0 {
		return variants, nil
	}

	valueRows, err := c.app.db.QueryContext(ctx, `
		SELECT vov.variant_id, pov.value
		FROM variant_option_values vov
		JOIN product_option_values pov ON pov.id = vov.option_value_id
		JOIN product_options o ON o.id = pov.option_id
		WHERE vov.variant_id = ANY($1::bigint[])
		ORDER BY o.position, o.id`, int64Array(ids))
	if err != nil {
		return nil, err
	}
	defer valueRows.Close()

	for valueRows.Next() {
		var variantID int64
		var value string
		if err := valueRows.Scan(&variantID, &value); err != nil {
			return nil, err
		}
		if v := byID[variantID]; v != nil {
			v.Options = append(v.Options, value)
		}
	}
	if err := valueRows.Err(); err != nil {
		return nil, err
	}
	for _, v := range variants {
		v.Label = strings.Join(v.Options, " / ")
	}

	// Every variant's pictures, for the whole page in one query, each list
	// in the order it was set (M36).
	imageRows, err := c.app.db.QueryContext(ctx, `
		SELECT vm.variant_id, m.id, m.url, m.kind, m.alt
		FROM variant_media vm
		JOIN media m ON m.id = vm.media_id
		WHERE vm.variant_id = ANY($1::bigint[])
		ORDER BY vm.variant_id, vm.position, vm.media_id`, int64Array(ids))
	if err != nil {
		return nil, err
	}
	defer imageRows.Close()

	for imageRows.Next() {
		var variantID int64
		var img VariantImage
		if err := imageRows.Scan(&variantID, &img.MediaID, &img.URL, &img.Kind, &img.Alt); err != nil {
			return nil, err
		}
		if v := byID[variantID]; v != nil {
			v.Images = append(v.Images, img)
		}
	}
	for _, v := range variants {
		if len(v.Images) > 0 {
			v.Image = &v.Images[0]
		}
	}
	return variants, imageRows.Err()
}

// GetVariant loads one variant.
func (c *Catalog) GetVariant(ctx context.Context, id int64) (*Variant, error) {
	variants, err := c.queryVariants(ctx, `v.id = $1`, id)
	if err != nil {
		return nil, err
	}
	if len(variants) == 0 {
		return nil, NotFoundf("variant %d does not exist", id)
	}
	return variants[0], nil
}

// GetVariantBySKU loads a variant by SKU, which is unique store-wide.
func (c *Catalog) GetVariantBySKU(ctx context.Context, sku string) (*Variant, error) {
	variants, err := c.queryVariants(ctx, `v.sku = $1`, sku)
	if err != nil {
		return nil, err
	}
	if len(variants) == 0 {
		return nil, NotFoundf("no variant with sku %q", sku)
	}
	return variants[0], nil
}

// ListVariants returns a product's variants.
func (c *Catalog) ListVariants(ctx context.Context, productID int64) ([]*Variant, error) {
	return c.queryVariants(ctx, `v.product_id = $1`, productID)
}

// ------------------------------------------------------------------- helpers

func validProductStatus(s string) bool {
	return s == ProductDraft || s == ProductActive || s == ProductArchived
}

// normalizeTags trims, drops empties, de-duplicates case-insensitively and
// sorts.
//
// Sorting is the point: a tag list stored in the order somebody typed it makes
// every subsequent edit look like a change even when the set is identical, and
// that noise lands in exports, diffs and the panel's dirty-form check. Case is
// preserved because "T-Shirt" is what an operator wants to read back, but two
// spellings of one tag are one tag.
func normalizeTags(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		tag := strings.TrimSpace(raw)
		if tag == "" {
			continue
		}
		fold := strings.ToLower(tag)
		if seen[fold] {
			continue
		}
		seen[fold] = true
		out = append(out, tag)
	}
	// Folded comparison, and total: no two survivors share a folded form.
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

// tagsExpr is the SQL that turns the JSON array bound at position n back into
// a text[]. ARRAY(subquery) preserves the subquery's row order, and
// jsonb_array_elements_text emits in array order, so the sort normalizeTags
// applied survives the round trip.
func tagsExpr(n int) string {
	return fmt.Sprintf("ARRAY(SELECT jsonb_array_elements_text($%d::jsonb))", n)
}

// tagsValue renders tags for tagsExpr. JSON in both directions means no
// hand-written PostgreSQL array-literal quoting exists to get wrong.
func tagsValue(tags []string) ([]byte, error) {
	encoded, err := json.Marshal(normalizeTags(tags))
	if err != nil {
		return nil, Internalf(err, "encode tags")
	}
	return encoded, nil
}

// scanTags reads the to_jsonb(tags) column. The result is always a list, never
// null: a product with no tags has an empty array, and a client should not
// have to tell those apart.
func scanTags(raw []byte, dst *[]string) error {
	*dst = []string{}
	if len(raw) == 0 {
		return nil
	}
	var tags []string
	if err := json.Unmarshal(raw, &tags); err != nil {
		return err
	}
	if tags != nil {
		*dst = tags
	}
	return nil
}

// int64Array renders ids as a PostgreSQL array literal for `= ANY($1::bigint[])`.
// Batching the children of a page of products into one query per level is what
// keeps a catalog listing at three queries instead of two per product.
func int64Array(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// translateCatalogErr turns PostgreSQL constraint violations into the API's
// own vocabulary, so a client learns what it did wrong rather than reading a
// driver message.
func translateCatalogErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "variants_sku_key"):
		return Conflictf("that sku is already used by another variant")
	case strings.Contains(msg, "products_slug_key"):
		return Conflictf("that slug is already used by another product")
	case strings.Contains(msg, "products_category_id_fkey"):
		return NotFoundf("that category does not exist")
	case strings.Contains(msg, "variants_product_option_key_idx"):
		return Conflictf("a variant with that combination of options already exists")
	case strings.Contains(msg, "product_options_product_id_name_key"):
		return Conflictf("that option is already defined on this product")
	case strings.Contains(msg, "product_option_values_option_id_value_key"):
		return Conflictf("that option value is already defined")
	case strings.Contains(msg, "variants_reserved_within_on_hand"),
		strings.Contains(msg, "stock_on_hand_check"),
		strings.Contains(msg, "variant_stock_reserved_check"),
		strings.Contains(msg, "stock_reserved_check"):
		return Conflictf("the requested stock change would leave inventory inconsistent")
	}
	return err
}

// normalizeOriginCountry accepts an ISO 3166-1 alpha-2 code in any case and
// stores it upper. The list of valid codes is deliberately not held here: it
// changes when countries do, a store shipping from a place this engine has not
// heard of should not be stopped, and the shape is what a carrier's API
// actually rejects on.
func normalizeOriginCountry(in string) (string, error) {
	code := strings.ToUpper(strings.TrimSpace(in))
	if code == "" {
		return "", nil
	}
	if len(code) != 2 || strings.IndexFunc(code, func(r rune) bool { return r < 'A' || r > 'Z' }) >= 0 {
		return "", Validationf("origin_country must be a two-letter country code, like GB")
	}
	return code, nil
}

// normalizeHSCode keeps the digits and refuses anything else.
//
// Operators copy tariff numbers out of documents that write them "6109.10" or
// "6109 10 00", and all three spellings are the same code — so the separators
// are dropped rather than being an error nobody can see the cause of. Length is
// checked after that: 6 digits is the international part, and an importing
// country extends it to 8 or 10.
func normalizeHSCode(in string) (string, error) {
	var digits strings.Builder
	for _, r := range strings.TrimSpace(in) {
		switch {
		case r >= '0' && r <= '9':
			digits.WriteRune(r)
		case r == '.' || r == ' ' || r == '-':
			// A separator, not a digit and not an error.
		default:
			return "", Validationf("hs_code must be digits, like 610910 or 6109.10.00")
		}
	}
	code := digits.String()
	if code == "" {
		return "", nil
	}
	if len(code) < 6 || len(code) > 10 {
		return "", Validationf("hs_code must be 6 to 10 digits, like 610910")
	}
	return code, nil
}
