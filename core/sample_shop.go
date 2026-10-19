package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// A sample shop is a store's sample products and everything a store shows
// around them (D96): categories and collections to browse them by, discounts
// to try at the checkout, a month of orders and the customers who placed
// them, and whatever the installed modules add of their own — reviews, pages,
// questions and answers, links in the menus. Every row is marked, by a column
// only the engine sets, so one erase takes all of it and nothing of the
// merchant's.

// migration0070SampleShop marks the rest of the sample shop the way 0068
// marked its products. The partial index answers the order count, asked on
// every visit to the Products screen, without reading the order book.
const migration0070SampleShop = `
ALTER TABLE categories  ADD COLUMN sample boolean NOT NULL DEFAULT false;
ALTER TABLE collections ADD COLUMN sample boolean NOT NULL DEFAULT false;
ALTER TABLE discounts   ADD COLUMN sample boolean NOT NULL DEFAULT false;
ALTER TABLE orders      ADD COLUMN sample boolean NOT NULL DEFAULT false;
CREATE INDEX orders_sample_idx ON orders (store_id) WHERE sample;
`

// SampleCounts is how many of each kind of sample a store has, or an add or
// an erase touched, keyed by kind: "products", "orders", "reviews" and so on.
type SampleCounts map[string]int

func (c SampleCounts) merge(more SampleCounts) {
	for kind, n := range more {
		c[kind] += n
	}
}

// total is every sample row the counts name. Customers are left out: they are
// a reading of the orders (customers.go), not rows of their own.
func (c SampleCounts) total() int {
	n := 0
	for kind, v := range c {
		if kind != "customers" {
			n += v
		}
	}
	return n
}

// SampleShop is what a module's samples can point at: the sample products,
// categories and pages, and the sample customers with what each has bought.
// AddSampleShop fills it before it asks the modules, and a module that adds
// something others may link to (pages) appends it.
type SampleShop struct {
	Products   []SampleItem
	Categories []SampleItem
	Pages      []SampleItem
	Customers  []SampleCustomer
}

// SampleItem is one sample row, by id and by the slug a link would use.
type SampleItem struct {
	ID    int64
	Slug  string
	Title string
}

// SampleCustomer is a sample shopper: a name, an address at example.com,
// which nobody receives mail at, and the slugs of the sample products their
// confirmed sample orders bought.
type SampleCustomer struct {
	Name   string
	Email  string
	Bought []string
}

// SampleSeeder is a module with sample content of its own. AddSamples adds
// what the store does not have yet and reports what it added, CountSamples
// what is there, and EraseSamples removes it and reports what went. Each is
// bound to the store, and each marks its rows so the merchant's own are never
// touched.
type SampleSeeder interface {
	AddSamples(ctx context.Context, shop *SampleShop) (SampleCounts, error)
	CountSamples(ctx context.Context) (SampleCounts, error)
	EraseSamples(ctx context.Context) (SampleCounts, error)
}

// SampleLinker is a SampleSeeder whose samples link to the others' — a menu
// linking the sample pages — so it is asked after them.
type SampleLinker interface {
	SampleSeeder
	LinksSamples()
}

// sampleSeeders are the installed modules with samples, linkers last.
func (a *App) sampleSeeders() []SampleSeeder {
	var first, last []SampleSeeder
	for _, mod := range a.modules {
		switch s := mod.(type) {
		case SampleLinker:
			last = append(last, s)
		case SampleSeeder:
			first = append(first, s)
		}
	}
	return append(first, last...)
}

// The sample categories, by product type: every sample product files under
// the one its type names.
var sampleCategories = []struct{ slug, title, productType string }{
	{"apparel", "Apparel", "Apparel"},
	{"accessories", "Accessories", "Accessories"},
	{"home", "Home", "Home"},
	{"stationery", "Stationery", "Stationery"},
}

var sampleCollections = []struct {
	slug, title, description string
	products                 []string
}{
	{"new-in", "New in", "The latest additions to the shop.",
		[]string{"cotton-crew-tee", "hooded-sweatshirt", "wool-beanie", "a5-notebook"}},
	{"gifts", "Gifts", "Small things that make good presents.",
		[]string{"ceramic-mug", "leather-card-holder", "linen-napkins", "canvas-tote-bag"}},
}

// The sample discounts are limited to the sample products, so one left
// switched on can never take money off anything the merchant sells.
var sampleDiscounts = []struct {
	code, title string
	percent     int
	minimum     int64 // in major units, scaled like the prices; 0 is none
}{
	{"WELCOME10", "10% off your first order", 10, 0},
	{"BUNDLE15", "15% off a basket of 60 or more", 15, 60},
}

var sampleCustomers = []struct{ name, email string }{
	{"Maya Patel", "maya.patel@example.com"},
	{"Daniel Okafor", "daniel.okafor@example.com"},
	{"Sofia Rossi", "sofia.rossi@example.com"},
	{"Liam Chen", "liam.chen@example.com"},
	{"Aisha Khan", "aisha.khan@example.com"},
	{"Noah Fischer", "noah.fischer@example.com"},
}

// sampleOrderLine names a variant by its product and option values.
type sampleOrderLine struct {
	slug    string
	options []string
	qty     int
}

// The sample orders: a month of trade, most of it paid, two awaiting payment
// and one cancelled, so every state the order list filters by has something
// in it. Numbered SAMPLE-1001 on, which no checkout ever takes and which says
// what they are wherever an order number is shown.
var sampleOrders = []struct {
	customer, daysAgo int
	status, payment   string
	lines             []sampleOrderLine
}{
	{0, 27, OrderConfirmed, PaymentPaid, []sampleOrderLine{{"cotton-crew-tee", []string{"M", "Black"}, 2}, {"canvas-tote-bag", nil, 1}}},
	{1, 23, OrderConfirmed, PaymentPaid, []sampleOrderLine{{"hooded-sweatshirt", []string{"L"}, 1}}},
	{2, 19, OrderConfirmed, PaymentPaid, []sampleOrderLine{{"ceramic-mug", nil, 2}, {"linen-napkins", nil, 1}}},
	{3, 15, OrderConfirmed, PaymentPaid, []sampleOrderLine{{"a5-notebook", nil, 3}, {"leather-card-holder", nil, 1}}},
	{4, 13, OrderCancelled, PaymentPending, []sampleOrderLine{{"wool-beanie", []string{"Yellow"}, 1}}},
	{5, 10, OrderConfirmed, PaymentPaid, []sampleOrderLine{{"cotton-crew-tee", []string{"L", "White"}, 1}, {"wool-beanie", []string{"Grey"}, 1}}},
	{0, 8, OrderConfirmed, PaymentPaid, []sampleOrderLine{{"leather-card-holder", nil, 1}}},
	{1, 6, OrderConfirmed, PaymentPaid, []sampleOrderLine{{"ceramic-mug", nil, 1}, {"a5-notebook", nil, 1}}},
	{2, 5, OrderConfirmed, PaymentPaid, []sampleOrderLine{{"hooded-sweatshirt", []string{"M"}, 1}, {"canvas-tote-bag", nil, 1}}},
	{3, 3, OrderPending, PaymentPending, []sampleOrderLine{{"cotton-crew-tee", []string{"S", "Black"}, 1}}},
	{4, 2, OrderConfirmed, PaymentPaid, []sampleOrderLine{{"linen-napkins", nil, 2}}},
	{5, 1, OrderPending, PaymentPending, []sampleOrderLine{{"wool-beanie", []string{"Grey"}, 2}, {"ceramic-mug", nil, 1}}},
}

// AddSampleShop gives the store ctx is bound to the sample shop and reports
// what it added. Whatever the store already has — a slug, a code, an order
// number — is skipped, so asking again adds only what is missing.
func (a *App) AddSampleShop(ctx context.Context) (SampleCounts, error) {
	added := SampleCounts{}
	n, err := a.catalog.AddSampleProducts(ctx)
	added["products"] = n
	if err != nil {
		return added, err
	}
	shop := &SampleShop{}
	bySlug := map[string]*Product{}
	for _, s := range sampleCatalogue {
		p, err := a.catalog.GetProductBySlug(ctx, s.slug)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return added, err
		}
		// A product of the merchant's own under a sample's slug is theirs,
		// and nothing of the sample shop points at it.
		if !p.Sample {
			continue
		}
		bySlug[s.slug] = p
		shop.Products = append(shop.Products, SampleItem{ID: p.ID, Slug: p.Slug, Title: p.Title})
	}

	steps := []func(context.Context, *SampleShop, map[string]*Product) (SampleCounts, error){
		a.addSampleCategories, a.addSampleCollections, a.addSampleDiscounts, a.addSampleOrders,
	}
	for _, step := range steps {
		got, err := step(ctx, shop, bySlug)
		added.merge(got)
		if err != nil {
			return added, err
		}
	}
	for _, seeder := range a.sampleSeeders() {
		got, err := seeder.AddSamples(ctx, shop)
		added.merge(got)
		if err != nil {
			return added, err
		}
	}
	return added, nil
}

// sampleRowID is the id of the row a sample's slug or code names, and whether
// that row is a sample; ok is false when there is none.
func (a *App) sampleRowID(ctx context.Context, table, column, value string) (id int64, sample, ok bool, err error) {
	err = a.db.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT id, sample FROM %s WHERE %s = $1`, table, column), value).Scan(&id, &sample)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, Internalf(err, "look up %s", table)
	}
	return id, sample, true, nil
}

func (a *App) addSampleCategories(ctx context.Context, shop *SampleShop, products map[string]*Product) (SampleCounts, error) {
	added := SampleCounts{}
	for _, c := range sampleCategories {
		id, sample, ok, err := a.sampleRowID(ctx, "categories", "slug", c.slug)
		if err != nil {
			return added, err
		}
		if ok && !sample {
			continue // the merchant's own; their products are not ours to file
		}
		if !ok {
			cat, err := a.Categories().Create(ctx, CategoryInput{Slug: c.slug, Title: c.title, sample: true})
			if err != nil {
				return added, err
			}
			id = cat.ID
			added["categories"]++
		}
		shop.Categories = append(shop.Categories, SampleItem{ID: id, Slug: c.slug, Title: c.title})
		for _, s := range sampleCatalogue {
			p := products[s.slug]
			if p == nil || s.productType != c.productType || p.Category != nil {
				continue
			}
			if _, err := a.catalog.UpdateProduct(ctx, p.ID, ProductPatch{CategoryID: SetID(id)}); err != nil {
				return added, err
			}
		}
	}
	return added, nil
}

func (a *App) addSampleCollections(ctx context.Context, _ *SampleShop, products map[string]*Product) (SampleCounts, error) {
	added := SampleCounts{}
	for _, c := range sampleCollections {
		_, _, ok, err := a.sampleRowID(ctx, "collections", "slug", c.slug)
		if err != nil {
			return added, err
		}
		if ok {
			continue
		}
		col, err := a.Collections().Create(ctx, CollectionInput{
			Slug: c.slug, Title: c.title, Description: c.description, sample: true,
		})
		if err != nil {
			return added, err
		}
		var ids []int64
		for _, slug := range c.products {
			if p := products[slug]; p != nil {
				ids = append(ids, p.ID)
			}
		}
		if err := a.Collections().SetCollectionProducts(ctx, col.ID, ids); err != nil {
			return added, err
		}
		added["collections"]++
	}
	return added, nil
}

func (a *App) addSampleDiscounts(ctx context.Context, shop *SampleShop, _ map[string]*Product) (SampleCounts, error) {
	added := SampleCounts{}
	if len(shop.Products) == 0 {
		return added, nil // nothing to discount, and a discount must not reach further
	}
	ids := make([]int64, 0, len(shop.Products))
	for _, p := range shop.Products {
		ids = append(ids, p.ID)
	}
	scale := currencyScale(a.Currency(ctx))
	for _, d := range sampleDiscounts {
		_, _, ok, err := a.sampleRowID(ctx, "discounts", "code", d.code)
		if err != nil {
			return added, err
		}
		if ok {
			continue
		}
		in := DiscountInput{
			Code: d.code, Title: d.title, Kind: DiscountPercentage, ValueBP: d.percent * 100,
			Scope: DiscountScopeProducts, TargetIDs: ids, sample: true,
		}
		if d.minimum > 0 {
			minimum := d.minimum * scale
			in.MinSubtotalMinor = &minimum
		}
		if _, err := a.discounts.Create(ctx, in); err != nil {
			return added, err
		}
		added["discounts"]++
	}
	return added, nil
}

// addSampleOrders writes the sample orders through the orders importer, which
// moves no stock and, told nothing else, announces nothing: no confirmation
// email, SMS or webhook goes anywhere for an order nobody placed.
func (a *App) addSampleOrders(ctx context.Context, shop *SampleShop, products map[string]*Product) (SampleCounts, error) {
	added := SampleCounts{}
	var file strings.Builder
	file.WriteString("number,email,name,sku,title,variant_label,quantity,unit_price_minor,created_at,status,payment_status,payment_provider\n")
	bought := make([][]string, len(sampleCustomers))
	now := time.Now().UTC().Truncate(time.Hour)
	for i, o := range sampleOrders {
		number := fmt.Sprintf("SAMPLE-%d", 1001+i)
		cust := sampleCustomers[o.customer]
		at := now.Add(-time.Duration(o.daysAgo)*24*time.Hour - time.Duration(i%5+1)*time.Hour)
		for _, l := range o.lines {
			p := products[l.slug]
			if p == nil {
				continue
			}
			v := sampleVariantOf(p, l.options)
			if v == nil {
				continue
			}
			fmt.Fprintf(&file, "%s,%s,%s,%s,%s,%s,%d,%d,%s,%s,%s,sample\n",
				number, cust.email, cust.name, v.SKU, csvField(p.Title), strings.Join(v.Options, " / "),
				l.qty, v.Price.AmountMinor, at.Format(time.RFC3339), o.status, o.payment)
			if o.status == OrderConfirmed && !slices.Contains(bought[o.customer], l.slug) {
				bought[o.customer] = append(bought[o.customer], l.slug)
			}
		}
	}
	for i, c := range sampleCustomers {
		shop.Customers = append(shop.Customers, SampleCustomer{Name: c.name, Email: c.email, Bought: bought[i]})
	}
	res, err := a.transfer.ImportOrders(ctx, strings.NewReader(file.String()), ImportOptions{sample: true})
	if err != nil {
		return added, err
	}
	if len(res.Errors) > 0 {
		return added, Internalf(errors.New(res.Errors[0].Message), "add sample orders")
	}
	added["orders"] = res.Created
	if res.Created > 0 {
		var n int
		if err := a.db.QueryRowContext(ctx,
			`SELECT count(DISTINCT lower(email)) FROM orders WHERE sample`).Scan(&n); err != nil {
			return added, Internalf(err, "count sample customers")
		}
		added["customers"] = n
	}
	return added, nil
}

// sampleVariantOf is the variant of p with exactly these option values, or
// its only variant when there are none.
func sampleVariantOf(p *Product, options []string) *Variant {
	for i, v := range p.Variants {
		if slices.Equal(v.Options, options) || (len(options) == 0 && len(p.Variants) == 1) {
			return &p.Variants[i]
		}
	}
	return nil
}

// csvField quotes a value that needs it.
func csvField(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// currencyScale is how many minor units make one major unit.
func currencyScale(currency string) int64 {
	scale := int64(1)
	for range currencyExponent(currency) {
		scale *= 10
	}
	return scale
}

// SampleShopCounts is how much of the sample shop the store still has, edited
// or not.
func (a *App) SampleShopCounts(ctx context.Context) (SampleCounts, error) {
	counts := SampleCounts{}
	n, err := a.catalog.SampleProductCount(ctx)
	if err != nil {
		return nil, err
	}
	counts["products"] = n
	for kind, query := range map[string]string{
		"categories":  `SELECT count(*) FROM categories WHERE sample`,
		"collections": `SELECT count(*) FROM collections WHERE sample`,
		"discounts":   `SELECT count(*) FROM discounts WHERE sample`,
		"orders":      `SELECT count(*) FROM orders WHERE sample`,
		"customers":   `SELECT count(DISTINCT lower(email)) FROM orders WHERE sample`,
	} {
		var n int
		if err := a.db.QueryRowContext(ctx, query).Scan(&n); err != nil {
			return nil, Internalf(err, "count sample %s", kind)
		}
		counts[kind] = n
	}
	for _, seeder := range a.sampleSeeders() {
		got, err := seeder.CountSamples(ctx)
		if err != nil {
			return nil, err
		}
		counts.merge(got)
	}
	return counts, nil
}

// EraseSampleShop removes the sample shop, edited or not, and reports what it
// removed. The modules go first, since their samples point at the rest; then
// the orders, discounts, collections and products, each through its service
// where it has one; the categories last, once nothing sample is filed in them.
// A sample category the merchant has filed their own product in stays: taking
// it would take their product's category with it. A failure part way leaves
// the rest for the next run.
func (a *App) EraseSampleShop(ctx context.Context) (SampleCounts, error) {
	erased := SampleCounts{}
	for _, seeder := range a.sampleSeeders() {
		got, err := seeder.EraseSamples(ctx)
		erased.merge(got)
		if err != nil {
			return erased, err
		}
	}
	n, err := a.orders.eraseSamples(ctx)
	erased["orders"] = n
	if err != nil {
		return erased, err
	}
	for _, step := range []struct {
		kind, query string
		del         func(context.Context, int64) error
	}{
		{"discounts", `SELECT id FROM discounts WHERE sample ORDER BY id`, a.discounts.Delete},
		{"collections", `SELECT id FROM collections WHERE sample ORDER BY id`, a.Collections().Delete},
	} {
		ids, err := a.listIDs(ctx, step.query, "sample "+step.kind)
		if err != nil {
			return erased, err
		}
		for _, id := range ids {
			if err := step.del(ctx, id); err != nil && !errors.Is(err, ErrNotFound) {
				return erased, err
			}
			erased[step.kind]++
		}
	}
	n, err = a.catalog.DeleteSampleProducts(ctx)
	erased["products"] = n
	if err != nil {
		return erased, err
	}
	ids, err := a.listIDs(ctx, `SELECT id FROM categories WHERE sample ORDER BY id`, "sample categories")
	if err != nil {
		return erased, err
	}
	for _, id := range ids {
		err := a.Categories().Delete(ctx, id)
		if errors.Is(err, ErrConflict) || errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return erased, err
		}
		erased["categories"]++
	}
	return erased, nil
}

// eraseSamples deletes the sample orders. There is no other way to delete an
// order, and this is not one for anybody else's: a sample order is a row the
// engine made up, which no customer placed and no money moved for.
//
// Their lines, fulfilments, refunds and discount records go with them by
// cascade. The notifications log names an order by number rather than by key,
// so its rows go here, and so do announcements still waiting in the outbox,
// which would otherwise go out about an order that is gone. Stock movements
// stay, as they do when a product is deleted: they are the shelf's history.
func (s *Orders) eraseSamples(ctx context.Context) (int, error) {
	var n int64
	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM notifications WHERE order_number IN (SELECT number FROM orders WHERE sample)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM outbox_events
			WHERE published_at IS NULL AND aggregate_type = $1
			  AND aggregate_id IN (SELECT id FROM orders WHERE sample)`, AggregateOrder); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM orders WHERE sample`)
		if err != nil {
			return err
		}
		n, err = res.RowsAffected()
		return err
	})
	if err != nil {
		return 0, Internalf(err, "erase sample orders")
	}
	return int(n), nil
}

func (a *App) handleSampleShopCounts(w http.ResponseWriter, r *http.Request) {
	counts, err := a.SampleShopCounts(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, map[string]any{"counts": counts, "total": counts.total()})
}

func (a *App) handleAddSampleShop(w http.ResponseWriter, r *http.Request) {
	added, err := a.AddSampleShop(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, map[string]any{"added": added})
}

func (a *App) handleEraseSampleShop(w http.ResponseWriter, r *http.Request) {
	erased, err := a.EraseSampleShop(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, map[string]any{"erased": erased})
}

// sampleShopRights are what adding or erasing the sample shop needs: the
// write right of every core area it touches, so nobody adds or erases through
// it what they could not add or erase by hand.
var sampleShopRights = []Right{
	RightCatalogWrite, RightCategoriesWrite, RightCollectionsWrite, RightDiscountsWrite,
	RightOrdersWrite, RightMediaWrite,
}
