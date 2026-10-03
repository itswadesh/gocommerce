package b2b

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/ext/identity"
)

// RejectNotInCatalogue is a line outside the catalogue its company is held
// to: quick order, a file and a repeat order leave it out, and a checkout or a
// quote containing one is refused with the same code.
const RejectNotInCatalogue = "not_in_catalogue"

// A catalogue is bounded so that saving one is one bounded request. A dealer
// held to more than five thousand products one by one is better given the
// categories they sit in.
const (
	maxCatalogueCategories = 1000
	maxCatalogueProducts   = 5000
)

// Catalogue is what a company held to it may buy: every product in its
// categories, the categories under them included, and its products one by
// one. Categories and Products name each id as the catalogue reads now, and
// mark one core has since deleted.
type Catalogue struct {
	ID           int64             `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	CategoryIDs  []int64           `json:"category_ids"`
	ProductIDs   []int64           `json:"product_ids"`
	Categories   []CatalogueMember `json:"categories"`
	Products     []CatalogueMember `json:"products"`
	CompanyCount int               `json:"company_count"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// CatalogueMember is one category or product a catalogue names. Missing is
// one deleted from the catalogue since: it allows nothing, and is shown so
// somebody can take it out.
type CatalogueMember struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Missing bool   `json:"missing,omitempty"`
}

// CatalogueInput creates a catalogue or, with every field optional, patches
// one. CategoryIDs and ProductIDs replace the lists when given.
type CatalogueInput struct {
	Name        *string  `json:"name"`
	Description *string  `json:"description"`
	CategoryIDs *[]int64 `json:"category_ids"`
	ProductIDs  *[]int64 `json:"product_ids"`
}

// Catalogue reads one catalogue with what it names.
func (m *Module) Catalogue(ctx context.Context, id int64) (*Catalogue, error) {
	c := &Catalogue{CategoryIDs: []int64{}, ProductIDs: []int64{}, Categories: []CatalogueMember{}, Products: []CatalogueMember{}}
	err := m.db.QueryRowContext(ctx, `
		SELECT id, name, description, created_at, updated_at,
		       (SELECT count(*) FROM b2b_companies bc WHERE bc.catalogue_id = b2b_catalogues.id)
		FROM b2b_catalogues WHERE id = $1`, id).
		Scan(&c.ID, &c.Name, &c.Description, &c.CreatedAt, &c.UpdatedAt, &c.CompanyCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("catalogue %d does not exist", id)
	}
	if err != nil {
		return nil, err
	}
	if c.CategoryIDs, err = m.idsOf(ctx, `SELECT category_id FROM b2b_catalogue_categories
		WHERE catalogue_id = $1 ORDER BY category_id`, id); err != nil {
		return nil, err
	}
	if c.ProductIDs, err = m.idsOf(ctx, `SELECT product_id FROM b2b_catalogue_products
		WHERE catalogue_id = $1 ORDER BY product_id`, id); err != nil {
		return nil, err
	}
	for _, cid := range c.CategoryIDs {
		member := CatalogueMember{ID: cid}
		cat, err := m.app.Categories().Get(ctx, cid)
		switch {
		case errors.Is(err, gocommerce.ErrNotFound):
			member.Missing = true
		case err != nil:
			return nil, err
		default:
			member.Name = firstNonEmpty(cat.FullName, cat.Title)
		}
		c.Categories = append(c.Categories, member)
	}
	if len(c.ProductIDs) > 0 {
		titles := map[int64]string{}
		rows, err := m.db.QueryContext(ctx, `SELECT id, title FROM products WHERE id = ANY($1)`, c.ProductIDs)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var pid int64
			var title string
			if err := rows.Scan(&pid, &title); err != nil {
				rows.Close()
				return nil, err
			}
			titles[pid] = title
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, pid := range c.ProductIDs {
			title, ok := titles[pid]
			c.Products = append(c.Products, CatalogueMember{ID: pid, Name: title, Missing: !ok})
		}
	}
	return c, nil
}

func (m *Module) idsOf(ctx context.Context, q string, args ...any) ([]int64, error) {
	rows, err := m.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CatalogueSummary is a catalogue as a list shows it.
type CatalogueSummary struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	CategoryCount int       `json:"category_count"`
	ProductCount  int       `json:"product_count"`
	CompanyCount  int       `json:"company_count"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Catalogues lists catalogues by name. Search matches part of the name.
func (m *Module) Catalogues(ctx context.Context, search string, limit, offset int) ([]*CatalogueSummary, int, error) {
	where, args := "true", []any{}
	if s := strings.ToLower(strings.TrimSpace(search)); s != "" {
		args = append(args, "%"+s+"%")
		where = "lower(c.name) LIKE $1"
	}
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM b2b_catalogues c WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(ctx, `
		SELECT c.id, c.name, c.description, c.created_at, c.updated_at,
		       (SELECT count(*) FROM b2b_catalogue_categories x WHERE x.catalogue_id = c.id),
		       (SELECT count(*) FROM b2b_catalogue_products x WHERE x.catalogue_id = c.id),
		       (SELECT count(*) FROM b2b_companies x WHERE x.catalogue_id = c.id)
		FROM b2b_catalogues c WHERE `+where+`
		ORDER BY lower(c.name), c.id
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*CatalogueSummary{}
	for rows.Next() {
		s := &CatalogueSummary{}
		if err := rows.Scan(&s.ID, &s.Name, &s.Description, &s.CreatedAt, &s.UpdatedAt,
			&s.CategoryCount, &s.ProductCount, &s.CompanyCount); err != nil {
			return nil, 0, err
		}
		out = append(out, s)
	}
	return out, total, rows.Err()
}

// CreateCatalogue adds a catalogue.
func (m *Module) CreateCatalogue(ctx context.Context, in CatalogueInput) (*Catalogue, error) {
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		return nil, gocommerce.Validationf("a catalogue needs a name")
	}
	cats, prods, err := m.checkCatalogueInput(ctx, in)
	if err != nil {
		return nil, err
	}
	var id int64
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO b2b_catalogues (name, description) VALUES ($1, $2) RETURNING id`,
			strings.TrimSpace(*in.Name), deref(in.Description)).Scan(&id); err != nil {
			return err
		}
		return writeCatalogueMembers(ctx, tx, id, cats, prods)
	})
	if err != nil {
		return nil, translateCatalogueErr(err)
	}
	return m.Catalogue(ctx, id)
}

// UpdateCatalogue patches a catalogue. A company held to it is held to the new
// lists from its next basket and its next checkout.
func (m *Module) UpdateCatalogue(ctx context.Context, id int64, in CatalogueInput) (*Catalogue, error) {
	if _, err := m.Catalogue(ctx, id); err != nil {
		return nil, err
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return nil, gocommerce.Validationf("a catalogue needs a name")
	}
	cats, prods, err := m.checkCatalogueInput(ctx, in)
	if err != nil {
		return nil, err
	}
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE b2b_catalogues
			SET name = coalesce($2, name), description = coalesce($3, description), updated_at = now()
			WHERE id = $1`, id, trimmed(in.Name), trimmed(in.Description)); err != nil {
			return err
		}
		return writeCatalogueMembers(ctx, tx, id, cats, prods)
	})
	if err != nil {
		return nil, translateCatalogueErr(err)
	}
	return m.Catalogue(ctx, id)
}

// DeleteCatalogue removes a catalogue nobody is held to. One a company is held
// to is refused, naming the companies: dropping it from under them would let
// them buy everything, which is the opposite of what somebody set it up for.
func (m *Module) DeleteCatalogue(ctx context.Context, id int64) error {
	c, err := m.Catalogue(ctx, id)
	if err != nil {
		return err
	}
	if c.CompanyCount > 0 {
		names, err := m.heldCompanies(ctx, id)
		if err != nil {
			return err
		}
		return gocommerce.Conflictf("%s is what %s may buy; give them another catalogue, or none, first",
			c.Name, names)
	}
	_, err = m.db.ExecContext(ctx, `DELETE FROM b2b_catalogues WHERE id = $1`, id)
	return translateCatalogueErr(err)
}

// heldCompanies names the companies held to a catalogue, the first three and
// "others" past that: enough to know where to go.
func (m *Module) heldCompanies(ctx context.Context, catalogueID int64) (string, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT name FROM b2b_companies WHERE catalogue_id = $1 ORDER BY lower(name) LIMIT 4`, catalogueID)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return "", err
		}
		names = append(names, n)
	}
	if len(names) == 4 {
		return strings.Join(names[:3], ", ") + " and others", rows.Err()
	}
	return strings.Join(names, ", "), rows.Err()
}

// checkCatalogueInput refuses an id core does not have, so a catalogue is
// never saved naming nothing; a member deleted later is shown as missing.
func (m *Module) checkCatalogueInput(ctx context.Context, in CatalogueInput) (cats, prods []int64, err error) {
	if in.CategoryIDs != nil {
		if cats, err = distinctPositive("category_ids", *in.CategoryIDs, maxCatalogueCategories); err != nil {
			return nil, nil, err
		}
		for _, id := range cats {
			if _, err := m.app.Categories().Get(ctx, id); err != nil {
				if errors.Is(err, gocommerce.ErrNotFound) {
					return nil, nil, gocommerce.Validationf("category %d does not exist", id)
				}
				return nil, nil, err
			}
		}
	}
	if in.ProductIDs != nil {
		if prods, err = distinctPositive("product_ids", *in.ProductIDs, maxCatalogueProducts); err != nil {
			return nil, nil, err
		}
		found, err := m.idsOf(ctx, `SELECT id FROM products WHERE id = ANY($1)`, prods)
		if err != nil {
			return nil, nil, err
		}
		if len(found) != len(prods) {
			for _, id := range prods {
				if !slices.Contains(found, id) {
					return nil, nil, gocommerce.Validationf("product %d does not exist", id)
				}
			}
		}
	}
	return cats, prods, nil
}

// distinctPositive is the ids sorted with repeats dropped: naming a category
// twice is not an error worth refusing a save over.
func distinctPositive(field string, ids []int64, limit int) ([]int64, error) {
	out := slices.Clone(ids)
	slices.Sort(out)
	out = slices.Compact(out)
	if len(out) > limit {
		return nil, gocommerce.Validationf("%s has %d entries; a catalogue takes at most %d", field, len(out), limit)
	}
	for _, id := range out {
		if id <= 0 {
			return nil, gocommerce.Validationf("%s must be positive integers", field)
		}
	}
	if out == nil {
		out = []int64{}
	}
	return out, nil
}

// writeCatalogueMembers replaces whichever list was given; nil leaves a list
// as it was.
func writeCatalogueMembers(ctx context.Context, tx *sql.Tx, id int64, cats, prods []int64) error {
	if cats != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM b2b_catalogue_categories WHERE catalogue_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO b2b_catalogue_categories (catalogue_id, category_id)
			SELECT $1, unnest($2::bigint[])`, id, cats); err != nil {
			return err
		}
	}
	if prods != nil {
		if _, err := tx.ExecContext(ctx, `DELETE FROM b2b_catalogue_products WHERE catalogue_id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO b2b_catalogue_products (catalogue_id, product_id)
			SELECT $1, unnest($2::bigint[])`, id, prods); err != nil {
			return err
		}
	}
	return nil
}

func trimmed(s *string) *string {
	if s == nil {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}

func translateCatalogueErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "b2b_catalogues_name_key"):
		return gocommerce.Conflictf("a catalogue with that name already exists")
	case strings.Contains(msg, "b2b_catalogues_name_check"):
		return gocommerce.Validationf("a catalogue needs a name")
	case strings.Contains(msg, "b2b_companies_catalogue_id_fkey"):
		return gocommerce.Conflictf("a company was given this catalogue a moment ago; take it off that company first")
	}
	return err
}

// ------------------------------------------------------------- membership

type querier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// catalogueAllowsSQL is the products of $2 a catalogue $1 allows: named one
// by one, or filed under one of its categories or anything beneath them. The
// tree is walked as core walks it for a category listing, down to
// MaxCategoryDepth, so "everything under Tools" means what the storefront
// means by it.
const catalogueAllowsSQL = `
	WITH RECURSIVE allowed AS (
	    SELECT category_id AS id, 0 AS depth FROM b2b_catalogue_categories WHERE catalogue_id = $1
	  UNION ALL
	    SELECT c.id, allowed.depth + 1
	    FROM allowed JOIN categories c ON c.parent_id = allowed.id
	    WHERE allowed.depth < $3
	)
	SELECT p.id FROM products p
	WHERE p.id = ANY($2)
	  AND (EXISTS (SELECT 1 FROM b2b_catalogue_products cp
	               WHERE cp.catalogue_id = $1 AND cp.product_id = p.id)
	       OR p.category_id IN (SELECT id FROM allowed))`

// outsideCatalogue is which of these products a catalogue does not allow. It
// is worked out as what it does allow, taken away from what was asked, so a
// product it cannot find — deleted a moment ago — is outside rather than in.
func outsideCatalogue(ctx context.Context, q querier, catalogueID int64, productIDs []int64) (map[int64]bool, error) {
	out := map[int64]bool{}
	if len(productIDs) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, catalogueAllowsSQL, catalogueID, productIDs, gocommerce.MaxCategoryDepth)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	allowed := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		allowed[id] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, id := range productIDs {
		if !allowed[id] {
			out[id] = true
		}
	}
	return out, nil
}

// guardCatalogue refuses a checkout holding a line outside the company's
// catalogue, reading the catalogue and the category tree with the checkout's
// own transaction, as they stand at the moment the order would be placed.
func (m *Module) guardCatalogue(ctx context.Context, a *gocommerce.CheckoutAttempt, company *Company, catalogueID int64) error {
	ids := make([]int64, 0, len(a.Lines))
	for _, l := range a.Lines {
		ids = append(ids, l.ProductID)
	}
	outside, err := outsideCatalogue(ctx, a.Tx, catalogueID, ids)
	if err != nil || len(outside) == 0 {
		return err
	}
	var lines []gocommerce.LineConflict
	for _, l := range a.Lines {
		if outside[l.ProductID] {
			lines = append(lines, gocommerce.LineConflict{VariantID: l.VariantID, SKU: l.SKU, Reason: RejectNotInCatalogue})
		}
	}
	return notInCatalogue(company, lines)
}

// quoteInCatalogue refuses a quote holding a line its company's catalogue no
// longer allows.
func (m *Module) quoteInCatalogue(ctx context.Context, q *Quote) error {
	company, err := m.Company(ctx, q.CompanyID)
	if err != nil || company.CatalogueID == nil {
		return err
	}
	variants := make([]int64, len(q.Lines))
	for i, l := range q.Lines {
		variants[i] = l.VariantID
	}
	productOf := map[int64]int64{}
	rows, err := m.db.QueryContext(ctx, `SELECT id, product_id FROM variants WHERE id = ANY($1)`, variants)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v, p int64
		if err := rows.Scan(&v, &p); err != nil {
			rows.Close()
			return err
		}
		productOf[v] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	products := make([]int64, 0, len(productOf))
	for _, p := range productOf {
		products = append(products, p)
	}
	outside, err := outsideCatalogue(ctx, m.db, *company.CatalogueID, products)
	if err != nil {
		return err
	}
	var lines []gocommerce.LineConflict
	for _, l := range q.Lines {
		// A variant deleted since is not a catalogue's business: placing the
		// order says so in core's own words.
		if p, ok := productOf[l.VariantID]; ok && outside[p] {
			lines = append(lines, gocommerce.LineConflict{VariantID: l.VariantID, SKU: l.SKU, Reason: RejectNotInCatalogue})
		}
	}
	if len(lines) == 0 {
		return nil
	}
	return notInCatalogue(company, lines)
}

// notInCatalogue is the refusal for lines a company may not buy, naming them
// the way a refused checkout names a line it cannot sell.
func notInCatalogue(company *Company, lines []gocommerce.LineConflict) error {
	skus := make([]string, 0, len(lines))
	for _, l := range lines {
		skus = append(skus, firstNonEmpty(l.SKU, "variant "+strconv.FormatInt(l.VariantID, 10)))
	}
	verb := " is"
	if len(skus) > 1 {
		verb = " are"
	}
	return (&gocommerce.APIError{Status: http.StatusForbidden, Code: RejectNotInCatalogue,
		Message: strings.Join(skus, ", ") + verb + " not in " + company.Name + "'s catalogue"}).WithDetails(lines)
}

// ------------------------------------------------------------- buyer's view

// CatalogueVariant is one variant a buyer may buy, at their price for one.
type CatalogueVariant struct {
	ID        int64    `json:"id"`
	SKU       string   `json:"sku"`
	Label     string   `json:"label"`
	Options   []string `json:"options"`
	Available int      `json:"available"`
	// PriceMinor is what one costs in a basket priced as this buyer, on the
	// storefront a quick order fills: their group's price lists, else the
	// variant's own. A quantity break lowers it in the basket.
	PriceMinor int64  `json:"price_minor"`
	Currency   string `json:"currency"`
}

// CatalogueProduct is one product a buyer may buy.
type CatalogueProduct struct {
	ID       int64                       `json:"id"`
	Slug     string                      `json:"slug"`
	Title    string                      `json:"title"`
	ImageURL string                      `json:"image_url,omitempty"`
	Category *gocommerce.ProductCategory `json:"category,omitempty"`
	Variants []CatalogueVariant          `json:"variants"`
}

// CatalogueQuery narrows what a buyer may buy.
type CatalogueQuery struct {
	// Search matches part of the title or of a variant's SKU, in any case.
	Search string
	// CategoryID keeps one category and everything under it.
	CategoryID    int64
	Limit, Offset int
}

// BuyerCatalogue lists the products a buyer's company may buy — its
// catalogue, or every product on sale when it has none — with each variant's
// price as that buyer. Prices are core's: Pricing.PriceInChannel, for the
// address the company's group holds for the buyer, on the default storefront
// a quick order's basket is opened on — the price that basket would charge
// for one. Nothing here reads a price list.
func (m *Module) BuyerCatalogue(ctx context.Context, buyer *identity.Customer, q CatalogueQuery) ([]*CatalogueProduct, int, error) {
	mem, company, err := m.memberAndCompany(ctx, buyer)
	if err != nil {
		return nil, 0, err
	}
	channel, err := m.app.Channels().Resolve(ctx, "")
	if err != nil {
		return nil, 0, err
	}
	var channelID int64
	if channel != nil {
		channelID = channel.ID
	}

	conds := []string{"p.status = $1"}
	args := []any{gocommerce.ProductActive}
	arg := func(v any) string {
		args = append(args, v)
		return "$" + strconv.Itoa(len(args))
	}
	if company.CatalogueID != nil {
		cat := arg(*company.CatalogueID)
		depth := arg(gocommerce.MaxCategoryDepth)
		conds = append(conds, `(EXISTS (SELECT 1 FROM b2b_catalogue_products cp
			    WHERE cp.catalogue_id = `+cat+` AND cp.product_id = p.id)
			OR p.category_id IN (
			    WITH RECURSIVE allowed AS (
			        SELECT category_id AS id, 0 AS depth FROM b2b_catalogue_categories WHERE catalogue_id = `+cat+`
			      UNION ALL
			        SELECT c.id, allowed.depth + 1 FROM allowed JOIN categories c ON c.parent_id = allowed.id
			        WHERE allowed.depth < `+depth+`)
			    SELECT id FROM allowed))`)
	}
	if q.CategoryID > 0 {
		cat := arg(q.CategoryID)
		depth := arg(gocommerce.MaxCategoryDepth)
		conds = append(conds, `p.category_id IN (
			WITH RECURSIVE down AS (
			    SELECT id, 0 AS depth FROM categories WHERE id = `+cat+`
			  UNION ALL
			    SELECT c.id, down.depth + 1 FROM down JOIN categories c ON c.parent_id = down.id
			    WHERE down.depth < `+depth+`)
			SELECT id FROM down)`)
	}
	if s := strings.ToLower(strings.TrimSpace(q.Search)); s != "" {
		like := arg("%" + s + "%")
		conds = append(conds, `(lower(p.title) LIKE `+like+` OR EXISTS (
			SELECT 1 FROM variants v WHERE v.product_id = p.id AND lower(v.sku) LIKE `+like+`))`)
	}
	// Published to the storefront the basket is opened on, or to none — the
	// rule the public listing applies.
	if channelID > 0 {
		ch := arg(channelID)
		conds = append(conds, `(EXISTS (SELECT 1 FROM product_channels pc WHERE pc.product_id = p.id AND pc.channel_id = `+ch+`)
			OR NOT EXISTS (SELECT 1 FROM product_channels pc WHERE pc.product_id = p.id))`)
	}
	where := strings.Join(conds, " AND ")
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM products p WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	ids, err := m.idsOf(ctx, `SELECT p.id FROM products p WHERE `+where+`
		ORDER BY lower(p.title), p.id LIMIT `+arg(q.Limit)+` OFFSET `+arg(q.Offset), args...)
	if err != nil {
		return nil, 0, err
	}

	currency := m.app.Config().Currency
	out := make([]*CatalogueProduct, 0, len(ids))
	for _, id := range ids {
		p, err := m.app.Products().GetProduct(ctx, id)
		if errors.Is(err, gocommerce.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		item := &CatalogueProduct{ID: p.ID, Slug: p.Slug, Title: p.Title, ImageURL: p.ImageURL,
			Category: p.Category, Variants: []CatalogueVariant{}}
		for _, v := range p.Variants {
			if !v.Active {
				continue
			}
			price, err := m.app.Pricing().PriceInChannel(ctx, v.ID, 1, mem.Email, channelID)
			if err != nil {
				return nil, 0, err
			}
			item.Variants = append(item.Variants, CatalogueVariant{ID: v.ID, SKU: v.SKU, Label: v.Label,
				Options: v.Options, Available: v.Available, PriceMinor: price, Currency: currency})
		}
		out = append(out, item)
	}
	return out, total, nil
}
