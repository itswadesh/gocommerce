package gocommerce

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// ------------------------------------------------------- without a database

// TestParseSortReadsTheRequest covers the two input guards with no database at
// all, so they stay tested on a machine with no PostgreSQL.
func TestParseSortReadsTheRequest(t *testing.T) {
	spec := SortSpec{
		Tiebreak: "t.id",
		Columns: map[string]sortField{
			"title": {"lower(t.title) ASC", "lower(t.title) DESC"},
			"id":    {"t.id ASC", "t.id DESC"},
		},
	}

	for _, tc := range []struct {
		query   string
		want    Sort
		wantErr string
	}{
		{query: "", want: Sort{}},
		{query: "?sort=title", want: Sort{Field: "title"}},
		{query: "?sort=title&order=asc", want: Sort{Field: "title"}},
		{query: "?sort=title&order=desc", want: Sort{Field: "title", Desc: true}},
		{query: "?sort=title&order=DESC", want: Sort{Field: "title", Desc: true}},
		{query: "?sort=+title+&order=+desc+", want: Sort{Field: "title", Desc: true}},
		{query: "?sort=title&order=sideways", wantErr: "order must be asc or desc"},
		{query: "?order=asc", wantErr: "order needs a sort field"},
		{query: "?sort=nonsense&order=asc", wantErr: "sort must be one of id, title"},
		{query: "?sort=t.id", wantErr: "sort must be one of id, title"},
	} {
		r := httptest.NewRequest(http.MethodGet, "/x"+tc.query, nil)
		got, err := ParseSort(r, spec)
		switch {
		case tc.wantErr == "" && err != nil:
			t.Errorf("%q: unexpected error %v", tc.query, err)
		case tc.wantErr == "" && got != tc.want:
			t.Errorf("%q = %+v, want %+v", tc.query, got, tc.want)
		case tc.wantErr != "":
			if err == nil {
				t.Errorf("%q: want an error naming %q, got %+v", tc.query, tc.wantErr, got)
				continue
			}
			var api *APIError
			if !asAPIError(err, &api) || api.Status != http.StatusBadRequest {
				t.Errorf("%q: want a 400 validation error, got %#v", tc.query, err)
				continue
			}
			if !strings.Contains(api.Message, tc.wantErr) {
				t.Errorf("%q: message %q does not contain %q", tc.query, api.Message, tc.wantErr)
			}
		}
	}
}

// TestSortClauseAlwaysEndsInAUniqueKey is the partition invariant, stated as a
// property of every spec in the engine rather than proved once per listing.
//
// Deleting the tiebreaker append from Clause must fail here, because it is the
// only thing that makes LIMIT/OFFSET a partition of the result rather than
// three arbitrary draws from it.
func TestSortClauseAlwaysEndsInAUniqueKey(t *testing.T) {
	for name, spec := range allSortSpecs() {
		if spec.Tiebreak == "" {
			t.Errorf("%s has no tiebreaker: its pages may repeat and skip rows", name)
			continue
		}
		for _, field := range spec.Fields() {
			for _, desc := range []bool{false, true} {
				dir := " ASC"
				if desc {
					dir = " DESC"
				}
				clause, err := spec.Clause(Sort{Field: field, Desc: desc}, "unused")
				if err != nil {
					t.Fatalf("%s.%s: %v", name, field, err)
				}
				if !strings.HasSuffix(clause, spec.Tiebreak+dir) {
					t.Errorf("%s ?sort=%s&order=%s resolved to %q, which does not end in %q",
						name, field, strings.ToLower(strings.TrimSpace(dir)),
						clause, spec.Tiebreak+dir)
				}
				// A NULLS clause on the tiebreaker would pin a placement the
				// indexes cannot serve; on a key that can be NULL it is
				// required. Both are checked by the pairing below.
				if strings.Contains(clause, "NULLS FIRST") {
					t.Errorf("%s.%s uses NULLS FIRST: the engine pins NULLS LAST in both directions",
						name, field)
				}
			}
		}
	}
}

// TestNullsPlacementIsPinnedPerFieldNotPerDirection guards the subtlety that a
// later "tidy-up" is most likely to undo: a field that can be NULL says NULLS
// LAST in BOTH directions, and a field that cannot says it in neither.
//
// Getting the second half wrong breaks nothing visible — the descending
// direction simply stops using M29's ascending indexes, silently.
func TestNullsPlacementIsPinnedPerFieldNotPerDirection(t *testing.T) {
	for name, spec := range allSortSpecs() {
		for field, f := range spec.Columns {
			ascNulls := strings.Contains(f.asc, "NULLS LAST")
			descNulls := strings.Contains(f.desc, "NULLS LAST")
			if ascNulls != descNulls {
				t.Errorf("%s.%s pins NULLS LAST in one direction only (asc=%v desc=%v): "+
					"the rows with no value would move from the bottom to the top on the "+
					"operator's second click", name, field, ascNulls, descNulls)
			}
			// Only a nullable expression may carry the clause, and the two
			// shapes that can be NULL here are nullif() and a scalar aggregate
			// over a possibly empty set.
			nullable := strings.Contains(f.asc, "nullif(") ||
				strings.Contains(f.asc, "SELECT min(") ||
				strings.Contains(f.asc, "SELECT sum(") ||
				strings.HasPrefix(f.asc, "lower(code)")
			if ascNulls && !nullable {
				t.Errorf("%s.%s pins NULLS LAST on an expression that cannot be NULL, "+
					"which stops an ascending btree serving the descending scan", name, field)
			}
		}
	}
}

// TestSortParametersAreDocumented is the one mechanical guard against the
// allow-list and the published contract drifting apart. TestSpecCoversEveryCoreRoute
// checks that a path exists and never looks at its parameters.
func TestSortParametersAreDocumented(t *testing.T) {
	// The enum is read as raw JSON and decoded only for the `sort` parameter:
	// a sibling on the same route (categories' `flat`) enumerates numbers, and a
	// []string field there would fail on a parameter this test is not about.
	var doc struct {
		Paths map[string]map[string]struct {
			Parameters []struct {
				Name   string `json:"name"`
				Ref    string `json:"$ref"`
				Schema struct {
					Enum json.RawMessage `json:"enum"`
				} `json:"schema"`
			} `json:"parameters"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(specSource, &doc); err != nil {
		t.Fatalf("parse openapi.json: %v", err)
	}

	for path, spec := range map[string]SortSpec{
		"/api/admin/products":   productSorts,
		"/api/admin/orders":     orderSorts,
		"/api/admin/customers":  customerSorts,
		"/api/admin/discounts":  discountSorts,
		"/api/admin/media":      mediaSorts,
		"/api/admin/categories": categorySorts,
		"/api/categories":       categorySorts,
	} {
		get, ok := doc.Paths[path]["get"]
		if !ok {
			t.Errorf("%s has no documented GET", path)
			continue
		}
		var enum []string
		var hasOrder bool
		for _, p := range get.Parameters {
			if p.Name == "sort" {
				if err := json.Unmarshal(p.Schema.Enum, &enum); err != nil {
					t.Errorf("%s: sort enum is not a list of strings: %v", path, err)
				}
			}
			if p.Ref == "#/components/parameters/SortOrder" {
				hasOrder = true
			}
		}
		if want := spec.Fields(); !reflect.DeepEqual(enum, want) {
			t.Errorf("%s documents sort=%v, the allow-list accepts %v", path, enum, want)
		}
		if !hasOrder {
			t.Errorf("%s documents no order parameter", path)
		}
	}
}

// allSortSpecs is every allow-list in the engine, so a property of "a sort" can
// be asserted once rather than six times — and so a seventh listing added later
// is caught by the same assertions the moment it is registered here.
func allSortSpecs() map[string]SortSpec {
	return map[string]SortSpec{
		"products":   productSorts,
		"orders":     orderSorts,
		"customers":  customerSorts,
		"discounts":  discountSorts,
		"media":      mediaSorts,
		"categories": categorySorts,
	}
}

// ---------------------------------------------------------- over the engine

type sortedIDs struct {
	Data []struct {
		ID       int64  `json:"id"`
		Slug     string `json:"slug"`
		Number   string `json:"number"`
		Email    string `json:"email"`
		Name     string `json:"name"`
		Code     string `json:"code"`
		Filename string `json:"filename"`
		FullName string `json:"full_name"`
	} `json:"data"`
	Meta ListMeta `json:"meta"`
}

// listSorted issues one admin listing request and decodes it.
func listSorted(t *testing.T, app *App, path string) sortedIDs {
	t.Helper()
	rec := do(t, app, http.MethodGet, path, withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body)
	}
	var out sortedIDs
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return out
}

// keysOf reduces a page to the identifying string of each row, whichever field
// the listing calls it.
func keysOf(p sortedIDs) []string {
	out := make([]string, 0, len(p.Data))
	for _, r := range p.Data {
		switch {
		case r.Slug != "":
			out = append(out, r.Slug)
		case r.Number != "":
			out = append(out, r.Number)
		case r.Filename != "":
			out = append(out, r.Filename)
		case r.Code != "":
			out = append(out, r.Code)
		case r.Email != "":
			out = append(out, r.Email)
		default:
			out = append(out, fmt.Sprint(r.ID))
		}
	}
	return out
}

// sortedStore seeds one app with enough of everything that every listing has
// rows to order, and returns it. Seeding once keeps the suite's PostgreSQL time
// down: these are read-only assertions over the same fixture.
func sortedStore(t *testing.T) *App {
	t.Helper()
	dsn := requireDB(t)
	cfg := testConfig(dsn)
	cfg.MediaDir = t.TempDir()
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	ctx := context.Background()
	// Titles deliberately out of id order, so a title sort and the default
	// cannot agree by accident.
	titles := []string{"Zebra", "apple", "Mango", "banana", "Cherry"}
	for i, title := range titles {
		price := int64(5000 - 300*i)
		stock := 10 + i
		if _, err := app.Products().CreateProduct(ctx, ProductInput{
			Title: title, Status: ProductActive,
			SKU: fmt.Sprintf("SORT-%02d", i), PriceMinor: &price, Stock: &stock,
		}); err != nil {
			t.Fatalf("create product %s: %v", title, err)
		}
	}

	variant := simpleProduct(t, app, "SORT-ORD", 1000, 500).DefaultVariant()
	address := Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345", Country: "US"}
	for i, who := range []struct {
		email, name string
		qty         int
	}{
		{"carol@example.com", "Carol", 3},
		{"alice@example.com", "", 1},
		{"bob@example.com", "Bob", 2},
	} {
		if _, err := app.Order().Create(ctx, NewOrderInput{
			Email: who.email, Name: who.name, Address: address,
			Lines: []NewOrderLine{{VariantID: variant.ID, Quantity: who.qty}},
		}); err != nil {
			t.Fatalf("place order %d: %v", i, err)
		}
	}

	newDiscount(t, app, DiscountInput{Code: "ZEBRA", Title: "Zebra sale", Kind: DiscountPercentage, ValueBP: 1000})
	newDiscount(t, app, DiscountInput{Code: "apple", Title: "Apple sale", Kind: DiscountPercentage, ValueBP: 500})
	newDiscount(t, app, DiscountInput{Title: "Automatic, no code", Kind: DiscountPercentage, ValueBP: 100})

	lib := app.MediaLibrary()
	for _, u := range []string{
		"https://cdn.example/zebra.jpg",
		"https://cdn.example/apple.png",
		"https://cdn.example/promo.mp4",
		// No last path segment, so filenameForURL answers "" — which is the
		// case the nullif() in mediaSorts exists for.
		"https://cdn.example/nameless/",
	} {
		if _, err := lib.AddURL(ctx, u, "", ""); err != nil {
			t.Fatalf("AddURL %s: %v", u, err)
		}
	}

	// Categories, so the search branch has something to order. Every title
	// carries an "a", which is the term the listings table searches for.
	for _, title := range []string{"Apparel", "Bakery", "Cameras"} {
		if _, err := app.Categories().Create(ctx, CategoryInput{Title: title}); err != nil {
			t.Fatalf("create category %s: %v", title, err)
		}
	}
	return app
}

// sortableListings is every route the feature covers, with the spec that gates
// it, so the table-driven tests below stay one list rather than six copies.
func sortableListings() []struct {
	path string
	spec SortSpec
} {
	return []struct {
		path string
		spec SortSpec
	}{
		{"/api/admin/products", productSorts},
		{"/api/admin/orders", orderSorts},
		{"/api/admin/customers", customerSorts},
		{"/api/admin/discounts", discountSorts},
		{"/api/admin/media", mediaSorts},
		{"/api/admin/categories?q=a", categorySorts},
	}
}

func join(path, extra string) string {
	if strings.Contains(path, "?") {
		return path + "&" + extra
	}
	return path + "?" + extra
}

// TestSortRejectsWhatIsNotOnTheAllowList is the test that fails the moment
// somebody "simplifies" the map into a formatter.
func TestSortRejectsWhatIsNotOnTheAllowList(t *testing.T) {
	app := sortedStore(t)

	// Percent-encoded the way a client would have to send them: what is being
	// asserted is that the engine refuses the decoded value, not that a space in
	// a request line is malformed.
	bad := []string{
		"sort=nonsense",
		"sort=p.id",
		"sort=" + url.QueryEscape("title; DROP TABLE products"),
		"sort=" + url.QueryEscape("title)"),
		"sort=" + url.QueryEscape("(SELECT 1)"),
		"sort=title&order=sideways",
		"sort=title&order=" + url.QueryEscape("asc; --"),
		"order=desc",
	}
	for _, listing := range sortableListings() {
		before := listSorted(t, app, listing.path)
		for _, q := range bad {
			target := join(listing.path, q)
			rec := do(t, app, http.MethodGet, target, withAdmin)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("GET %s = %d, want 400", target, rec.Code)
				continue
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if body.Error.Code != "validation_failed" {
				t.Errorf("GET %s error code = %q, want validation_failed", target, body.Error.Code)
			}
		}
		// Nothing executed and nothing was dropped.
		after := listSorted(t, app, listing.path)
		if after.Meta.Total != before.Meta.Total || len(after.Data) != len(before.Data) {
			t.Errorf("%s changed after the refusals: %d/%d rows, was %d/%d",
				listing.path, len(after.Data), after.Meta.Total,
				len(before.Data), before.Meta.Total)
		}
	}
}

// TestSortNamesTheFieldsThatExist: the refusal is usable without opening the
// spec, and deterministic across runs.
func TestSortNamesTheFieldsThatExist(t *testing.T) {
	app := sortedStore(t)

	rec := do(t, app, http.MethodGet, "/api/admin/products?sort=nonsense", withAdmin)
	var body struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := "sort must be one of available, created_at, id, price, status, title, updated_at"
	if body.Error.Message != want {
		t.Errorf("message = %q, want %q", body.Error.Message, want)
	}
}

// TestEverySortRunsBothWays exercises every key of every spec in both
// directions. A typo in any SQL expression becomes a failure here rather than
// in production, and the total proves no ORDER BY leaked into the count query.
func TestEverySortRunsBothWays(t *testing.T) {
	app := sortedStore(t)

	for _, listing := range sortableListings() {
		base := listSorted(t, app, listing.path)
		for _, field := range listing.spec.Fields() {
			for _, order := range []string{"asc", "desc"} {
				target := join(listing.path, "sort="+field+"&order="+order)
				got := listSorted(t, app, target)
				if got.Meta.Total != base.Meta.Total {
					t.Errorf("GET %s: total %d, unsorted total %d",
						target, got.Meta.Total, base.Meta.Total)
				}
				if len(got.Data) != len(base.Data) {
					t.Errorf("GET %s: %d rows, unsorted %d",
						target, len(got.Data), len(base.Data))
				}
			}
		}
	}
}

// TestDescendingIsTheExactReverse proves the tiebreaker flips with the
// direction, which is also what lets one ascending btree serve both scans.
func TestDescendingIsTheExactReverse(t *testing.T) {
	app := sortedStore(t)

	// status ties every product against every other; created_at is distinct.
	for _, field := range []string{"status", "created_at", "title"} {
		asc := keysOf(listSorted(t, app, "/api/admin/products?sort="+field+"&order=asc"))
		desc := keysOf(listSorted(t, app, "/api/admin/products?sort="+field+"&order=desc"))
		slices.Reverse(desc)
		if !reflect.DeepEqual(asc, desc) {
			t.Errorf("sort=%s: descending reversed is %v, ascending is %v", field, desc, asc)
		}
	}
}

// TestSortedPagesPartitionTheCollection is the reason Clause appends a
// tiebreaker at all: every row ties on the sort column here, so without one the
// pages are three arbitrary draws from the same set.
func TestSortedPagesPartitionTheCollection(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	const total = 12
	for i := 0; i < total; i++ {
		price := int64(1000)
		stock := 1
		if _, err := app.Products().CreateProduct(ctx, ProductInput{
			Title: fmt.Sprintf("Tied %02d", i), Status: ProductActive,
			SKU: fmt.Sprintf("TIE-%02d", i), PriceMinor: &price, Stock: &stock,
		}); err != nil {
			t.Fatalf("create product %d: %v", i, err)
		}
	}

	seen := map[string]int{}
	for p := 1; p <= 3; p++ {
		page := listSorted(t, app,
			fmt.Sprintf("/api/admin/products?sort=status&order=asc&limit=5&page=%d", p))
		for _, slug := range keysOf(page) {
			seen[slug]++
		}
	}
	if len(seen) != total {
		t.Errorf("the sorted pages saw %d distinct products, want %d", len(seen), total)
	}
	for slug, count := range seen {
		if count != 1 {
			t.Errorf("%s appeared %d times across the sorted pages", slug, count)
		}
	}
}

// TestSortedListingIsStableAcrossRepeatedRequests catches a tiebreaker missing
// from one direction: a plan flip is how ties reorder in practice.
func TestSortedListingIsStableAcrossRepeatedRequests(t *testing.T) {
	app := sortedStore(t)

	for i := 0; i < 3; i++ {
		first := keysOf(listSorted(t, app, "/api/admin/products?sort=status&order=desc"))
		second := keysOf(listSorted(t, app, "/api/admin/products?sort=status&order=desc"))
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("run %d: %v then %v", i, first, second)
		}
	}
}

// TestUnsortedListingsAreUnchanged is the regression guard for every caller
// that never asked for sorting — ext/mcp's list_products and list_orders
// included, which build their query structs with field names and so leave Sort
// zero.
func TestUnsortedListingsAreUnchanged(t *testing.T) {
	app := sortedStore(t)
	ctx := context.Background()

	products, _, err := app.Products().ListProducts(ctx, ProductQuery{})
	if err != nil {
		t.Fatalf("ListProducts: %v", err)
	}
	for i := 1; i < len(products); i++ {
		if products[i-1].ID < products[i].ID {
			t.Errorf("products are not newest-first: %d before %d", products[i-1].ID, products[i].ID)
		}
	}

	orders, _, err := app.Order().List(ctx, OrderQuery{})
	if err != nil {
		t.Fatalf("Orders.List: %v", err)
	}
	for i := 1; i < len(orders); i++ {
		if orders[i-1].ID < orders[i].ID {
			t.Errorf("orders are not newest-first: %d before %d", orders[i-1].ID, orders[i].ID)
		}
	}

	discounts, _, err := app.Discounts().List(ctx, DiscountQuery{})
	if err != nil {
		t.Fatalf("Discounts.List: %v", err)
	}
	for i := 1; i < len(discounts); i++ {
		if discounts[i-1].ID < discounts[i].ID {
			t.Errorf("discounts are not newest-first")
		}
	}

	media, _, err := app.MediaLibrary().List(ctx, MediaQuery{})
	if err != nil {
		t.Fatalf("Media.List: %v", err)
	}
	for i := 1; i < len(media); i++ {
		if media[i-1].ID < media[i].ID {
			t.Errorf("media is not newest-first")
		}
	}

	customers, _, err := app.Order().Customers(ctx, CustomerQuery{})
	if err != nil {
		t.Fatalf("Customers: %v", err)
	}
	for i := 1; i < len(customers); i++ {
		if customers[i-1].LastOrderAt.Before(customers[i].LastOrderAt) {
			t.Errorf("customers are not newest-order-first")
		}
	}
}

// TestProductPriceSortsOnTheCheapestVariant proves min semantics and the
// no-fan-out shape in one assertion: a 3-variant product must count once.
func TestProductPriceSortsOnTheCheapestVariant(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	if _, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "Many", Slug: "many", Status: ProductActive,
		Options: []OptionInput{{Name: "Size", Values: []string{"S", "M", "L"}}},
		Variants: []VariantInput{
			{SKU: "MANY-S", PriceMinor: 500, Options: []string{"S"}},
			{SKU: "MANY-M", PriceMinor: 1500, Options: []string{"M"}},
			{SKU: "MANY-L", PriceMinor: 9000, Options: []string{"L"}},
		},
	}); err != nil {
		t.Fatalf("create Many: %v", err)
	}
	one := int64(1000)
	if _, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "One", Slug: "one", Status: ProductActive, SKU: "ONE-1", PriceMinor: &one,
	}); err != nil {
		t.Fatalf("create One: %v", err)
	}

	asc := listSorted(t, app, "/api/admin/products?sort=price&order=asc")
	if asc.Meta.Total != 2 {
		t.Fatalf("total = %d, want 2 — a sort must not multiply a product by its variants",
			asc.Meta.Total)
	}
	if got := keysOf(asc); !reflect.DeepEqual(got, []string{"many", "one"}) {
		t.Errorf("ascending by price = %v, want the 500 product first", got)
	}
	desc := listSorted(t, app, "/api/admin/products?sort=price&order=desc")
	if got := keysOf(desc); !reflect.DeepEqual(got, []string{"one", "many"}) {
		t.Errorf("descending by price = %v: both directions must order by the same min", got)
	}
}

// TestProductAvailableSortMatchesTheVariantArithmetic guards that
// productAvailable and variantAvailable stay the same arithmetic, and that a
// product nobody tracks sorts last rather than as zero.
func TestProductAvailableSortMatchesTheVariantArithmetic(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	price := int64(1000)
	for _, seed := range []struct {
		slug  string
		stock int
	}{{"few", 2}, {"many", 40}} {
		s := seed.stock
		if _, err := app.Products().CreateProduct(ctx, ProductInput{
			Title: seed.slug, Slug: seed.slug, Status: ProductActive,
			SKU: strings.ToUpper(seed.slug), PriceMinor: &price, Stock: &s,
		}); err != nil {
			t.Fatalf("create %s: %v", seed.slug, err)
		}
	}
	untracked := false
	if _, err := app.Products().CreateProduct(ctx, ProductInput{
		Title: "untracked", Slug: "untracked", Status: ProductActive,
		Variants: []VariantInput{{SKU: "UNTRACKED", PriceMinor: price, TrackInventory: &untracked}},
	}); err != nil {
		t.Fatalf("create untracked: %v", err)
	}

	asc := keysOf(listSorted(t, app, "/api/admin/products?sort=available&order=asc"))
	if !reflect.DeepEqual(asc, []string{"few", "many", "untracked"}) {
		t.Errorf("ascending by available = %v, want few, many, then the untracked one", asc)
	}
	desc := keysOf(listSorted(t, app, "/api/admin/products?sort=available&order=desc"))
	if !reflect.DeepEqual(desc, []string{"many", "few", "untracked"}) {
		t.Errorf("descending by available = %v: not-tracked is not a quantity, so it sorts "+
			"last either way", desc)
	}
}

// TestOrderNumberSortsByTheIdBehindIt: eleven orders, so the number crosses a
// digit boundary inside the %06d padding.
func TestOrderNumberSortsByTheIdBehindIt(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	variant := simpleProduct(t, app, "NUM-1", 1000, 200).DefaultVariant()
	address := Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345", Country: "US"}
	var ids []int64
	for i := 0; i < 11; i++ {
		res, err := app.Order().Create(ctx, NewOrderInput{
			Email: fmt.Sprintf("buyer%02d@example.com", i), Address: address,
			Lines: []NewOrderLine{{VariantID: variant.ID, Quantity: 1}},
		})
		if err != nil {
			t.Fatalf("place %d: %v", i, err)
		}
		ids = append(ids, res.Order.ID)
	}

	page := listSorted(t, app, "/api/admin/orders?sort=number&order=asc&limit=50")
	if len(page.Data) != len(ids) {
		t.Fatalf("got %d orders, want %d", len(page.Data), len(ids))
	}
	for i, row := range page.Data {
		if row.ID != ids[i] {
			t.Fatalf("row %d is order %d, want %d — the number is the id under a prefix",
				i, row.ID, ids[i])
		}
	}
}

// TestOrderSortByTotal proves the `total` key maps to total_minor, and that
// sorting never introduces a formatted or floating amount.
func TestOrderSortByTotal(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	variant := simpleProduct(t, app, "TOT-1", 1000, 200).DefaultVariant()
	address := Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345", Country: "US"}
	for _, qty := range []int{2, 7, 4} {
		if _, err := app.Order().Create(ctx, NewOrderInput{
			Email: fmt.Sprintf("q%d@example.com", qty), Address: address,
			Lines: []NewOrderLine{{VariantID: variant.ID, Quantity: qty}},
		}); err != nil {
			t.Fatalf("place: %v", err)
		}
	}

	rec := do(t, app, http.MethodGet, "/api/admin/orders?sort=total&order=desc", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("= %d: %s", rec.Code, rec.Body)
	}
	var page struct {
		Data []struct {
			Total Money `json:"total"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := []int64{7000, 4000, 2000}
	for i, row := range page.Data {
		if row.Total.AmountMinor != want[i] {
			t.Errorf("row %d total = %d, want %d", i, row.Total.AmountMinor, want[i])
		}
		if row.Total.Currency == "" {
			t.Errorf("row %d lost its currency: money is minor units plus a code", i)
		}
	}
}

// TestNullAndEmptyValuesSortLastInBothDirections is the test that fails if a
// nullable field's NULLS LAST is dropped from one direction.
func TestNullAndEmptyValuesSortLastInBothDirections(t *testing.T) {
	app := sortedStore(t)

	// orders.name: "Ada" beside one with an empty name.
	for _, order := range []string{"asc", "desc"} {
		page := listSorted(t, app, "/api/admin/orders?sort=name&order="+order)
		last := page.Data[len(page.Data)-1]
		if last.Name != "" {
			t.Errorf("orders sort=name&order=%s ends with %q, want the nameless order last",
				order, last.Name)
		}
	}

	// discounts.code: an automatic discount has none.
	for _, order := range []string{"asc", "desc"} {
		page := listSorted(t, app, "/api/admin/discounts?sort=code&order="+order)
		if len(page.Data) != 3 {
			t.Fatalf("discounts: %d rows, want 3 — the codeless one must not be dropped",
				len(page.Data))
		}
		if last := page.Data[len(page.Data)-1]; last.Code != "" {
			t.Errorf("discounts sort=code&order=%s ends with %q, want the codeless one last",
				order, last.Code)
		}
	}

	// media.filename: one seeded item is a URL with no last path segment, so
	// its filename is the empty string.
	for _, order := range []string{"asc", "desc"} {
		page := listSorted(t, app, "/api/admin/media?sort=filename&order="+order)
		if last := page.Data[len(page.Data)-1]; last.Filename != "" {
			t.Errorf("media sort=filename&order=%s ends with %q, want the unnamed file last",
				order, last.Filename)
		}
	}
}

// TestDiscountSortHandlesNullCodes also covers used_count and title.
func TestDiscountSortHandlesNullCodes(t *testing.T) {
	app := sortedStore(t)

	titles := func(q string) []string {
		rec := do(t, app, http.MethodGet, "/api/admin/discounts"+q, withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", q, rec.Code, rec.Body)
		}
		var page struct {
			Data []struct {
				Title string `json:"title"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode: %v", err)
		}
		out := make([]string, 0, len(page.Data))
		for _, d := range page.Data {
			out = append(out, d.Title)
		}
		return out
	}

	if got := titles("?sort=title&order=asc"); !reflect.DeepEqual(got,
		[]string{"Apple sale", "Automatic, no code", "Zebra sale"}) {
		t.Errorf("sort=title asc = %v", got)
	}
	if got := titles("?sort=used_count&order=desc"); len(got) != 3 {
		t.Errorf("sort=used_count returned %d rows, want 3", len(got))
	}
}

// TestMediaSortOrders: a sort composes with the filters rather than replacing
// them, and size_bytes orders as a number.
func TestMediaSortOrders(t *testing.T) {
	app := sortedStore(t)

	names := func(q string) []string {
		page := listSorted(t, app, "/api/admin/media"+q)
		out := make([]string, 0, len(page.Data))
		for _, m := range page.Data {
			out = append(out, m.Filename)
		}
		return out
	}

	if got := names("?sort=filename&order=asc"); !reflect.DeepEqual(got,
		[]string{"apple.png", "promo.mp4", "zebra.jpg", ""}) {
		t.Errorf("sort=filename asc = %v, want case-folded alphabetical with the unnamed one last", got)
	}
	// A sort narrows nothing: the kind filter still applies, and the video is
	// gone while the unnamed image stays.
	if got := names("?kind=image&sort=filename&order=asc"); !reflect.DeepEqual(got,
		[]string{"apple.png", "zebra.jpg", ""}) {
		t.Errorf("kind=image with a sort = %v — a sort must not replace a filter", got)
	}
	if _, _, err := app.MediaLibrary().List(context.Background(),
		MediaQuery{Sort: Sort{Field: "size_bytes", Desc: true}}); err != nil {
		t.Errorf("size_bytes desc: %v", err)
	}
}

// TestCustomerSortsRunOnTheAggregates: the ordering is on the same number the
// row shows, and the email tiebreak makes limit=1 pages a partition.
func TestCustomerSortsRunOnTheAggregates(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	variant := simpleProduct(t, app, "CS-1", 1000, 500).DefaultVariant()
	address := Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345", Country: "US"}
	place := func(email string, qty int) *Order {
		t.Helper()
		res, err := app.Order().Create(ctx, NewOrderInput{
			Email: email, Address: address,
			Lines: []NewOrderLine{{VariantID: variant.ID, Quantity: qty}},
		})
		if err != nil {
			t.Fatalf("place for %s: %v", email, err)
		}
		return res.Order
	}

	big := place("big@example.com", 9)
	if _, err := app.Pay().MarkPaid(ctx, big.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	// Unpaid: a real order, and not yet money.
	place("unpaid@example.com", 50)
	small := place("small@example.com", 2)
	if _, err := app.Pay().MarkPaid(ctx, small.ID, "cash"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	place("small@example.com", 1)

	emails := func(q string) []string {
		page := listSorted(t, app, "/api/admin/customers"+q)
		out := make([]string, 0, len(page.Data))
		for _, c := range page.Data {
			out = append(out, c.Email)
		}
		return out
	}

	if got := emails("?sort=spent&order=desc"); got[0] != "big@example.com" {
		t.Errorf("sort=spent desc = %v, want the paid 9000 first — an order awaiting "+
			"cash on delivery is not money", got)
	}
	if got := emails("?sort=orders&order=desc"); got[0] != "small@example.com" {
		t.Errorf("sort=orders desc = %v, want the buyer with two orders first", got)
	}
	if got := emails("?sort=email&order=asc"); !reflect.DeepEqual(got,
		[]string{"big@example.com", "small@example.com", "unpaid@example.com"}) {
		t.Errorf("sort=email asc = %v", got)
	}

	// One row per page, walked to the end: the lower(o.email) tiebreaker is
	// what makes this a partition of the three groups.
	seen := map[string]int{}
	for p := 1; p <= 3; p++ {
		for _, e := range emails(fmt.Sprintf("?sort=last_order_at&order=desc&limit=1&page=%d", p)) {
			seen[e]++
		}
	}
	if len(seen) != 3 {
		t.Errorf("walking one at a time saw %v, want all three exactly once", seen)
	}
	for email, n := range seen {
		if n != 1 {
			t.Errorf("%s appeared %d times", email, n)
		}
	}
}

// TestCustomerDefaultOrderTiebreaksToo: the pre-existing tear, now fixed. Two
// shoppers whose newest orders share a timestamp, walked with NO sort at all.
func TestCustomerDefaultOrderTiebreaksToo(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	variant := simpleProduct(t, app, "TIE-C", 1000, 100).DefaultVariant()
	address := Address{Line1: "1 Test Street", City: "Testville", PostalCode: "12345", Country: "US"}
	for _, email := range []string{"one@example.com", "two@example.com"} {
		if _, err := app.Order().Create(ctx, NewOrderInput{
			Email: email, Address: address,
			Lines: []NewOrderLine{{VariantID: variant.ID, Quantity: 1}},
		}); err != nil {
			t.Fatalf("place: %v", err)
		}
	}
	// The same instant for both, which is what a batch import produces.
	if _, err := app.DB().ExecContext(ctx,
		`UPDATE orders SET created_at = timestamptz '2026-01-01 00:00:00+00'`); err != nil {
		t.Fatalf("flatten the timestamps: %v", err)
	}

	seen := map[string]int{}
	for p := 1; p <= 2; p++ {
		page := listSorted(t, app, fmt.Sprintf("/api/admin/customers?limit=1&page=%d", p))
		for _, c := range page.Data {
			seen[c.Email]++
		}
	}
	if len(seen) != 2 {
		t.Errorf("the default order saw %v across two pages, want both customers once", seen)
	}
	for email, n := range seen {
		if n != 1 {
			t.Errorf("%s appeared %d times on the unsorted pages", email, n)
		}
	}
}

// TestCollectionOrderSurvivesUnlessAskedOtherwise is a Go-level test on
// purpose: the HTTP listing refuses sort together with collection_id, so this
// interaction exists only through the Go API.
func TestCollectionOrderSurvivesUnlessAskedOtherwise(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()

	price := int64(1000)
	var ids []int64
	// Created Zebra, Apple, Mango; curated in that same order, which disagrees
	// with the alphabet.
	for _, title := range []string{"Zebra", "Apple", "Mango"} {
		p, err := app.Products().CreateProduct(ctx, ProductInput{
			Title: title, Slug: strings.ToLower(title), Status: ProductActive,
			SKU: "COL-" + title, PriceMinor: &price,
		})
		if err != nil {
			t.Fatalf("create %s: %v", title, err)
		}
		ids = append(ids, p.ID)
	}
	col, err := app.Collections().Create(ctx, CollectionInput{Title: "Curated", Slug: "curated"})
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	if err := app.Collections().SetCollectionProducts(ctx, col.ID, ids); err != nil {
		t.Fatalf("curate: %v", err)
	}

	slugs := func(q ProductQuery) []string {
		t.Helper()
		q.CollectionID = col.ID
		list, _, err := app.Products().ListProducts(ctx, q)
		if err != nil {
			t.Fatalf("ListProducts: %v", err)
		}
		out := make([]string, 0, len(list))
		for _, p := range list {
			out = append(out, p.Slug)
		}
		return out
	}

	if got := slugs(ProductQuery{}); !reflect.DeepEqual(got, []string{"zebra", "apple", "mango"}) {
		t.Errorf("a zero Sort = %v, want the curated order", got)
	}
	if got := slugs(ProductQuery{Sort: Sort{Field: "title"}}); !reflect.DeepEqual(got,
		[]string{"apple", "mango", "zebra"}) {
		t.Errorf("an explicit sort = %v, want it to override the curation", got)
	}

	// Over HTTP the two together are a contradiction, not a precedence puzzle.
	rec := do(t, app, http.MethodGet,
		fmt.Sprintf("/api/admin/products?collection_id=%d&sort=title", col.ID), withAdmin)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("collection_id with a sort = %d, want 400: %s", rec.Code, rec.Body)
	}
	rec = do(t, app, http.MethodGet,
		fmt.Sprintf("/api/admin/products?collection_id=%d", col.ID), withAdmin)
	if rec.Code != http.StatusOK {
		t.Errorf("collection_id alone = %d, want 200: %s", rec.Code, rec.Body)
	}
}
