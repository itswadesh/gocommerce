package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
)

// The storefront listing counts its own filter values over the whole result
// set, each group ignoring its own selection, and every value it counts is one
// the same route can filter on.
func TestStorefrontListingFacets(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	bags, err := app.Categories().Create(ctx, CategoryInput{Title: "Bags"})
	if err != nil {
		t.Fatal(err)
	}
	shoes, err := app.Categories().Create(ctx, CategoryInput{Title: "Shoes"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		title    string
		price    int64
		vendor   string
		tags     []string
		category int64
		material string
		status   string
	}{
		{"Canvas pack", 1000, "Acme", []string{"summer", "sale"}, bags.ID, "Canvas", "active"},
		{"Leather pack", 3000, "Acme", []string{"winter"}, bags.ID, "Leather", "active"},
		{"Canvas tote", 2000, "Globex", []string{"summer"}, bags.ID, "Canvas", "active"},
		{"Runner", 5000, "Globex", nil, shoes.ID, "", "active"},
		{"Hidden draft", 9000, "Acme", []string{"summer"}, bags.ID, "Canvas", "draft"},
	} {
		meta := Metadata{}
		if p.material != "" {
			meta["category"] = map[string]any{"material": []string{p.material}}
		}
		price, category := p.price, p.category
		if _, err := app.Products().CreateProduct(ctx, ProductInput{
			Title: p.title, SKU: p.title, PriceMinor: &price, Status: p.status, Vendor: p.vendor,
			ProductType: "Bag", Tags: p.tags, CategoryID: &category, Metadata: meta,
		}); err != nil {
			t.Fatalf("create %s: %v", p.title, err)
		}
	}

	type body struct {
		Data []struct {
			Title string `json:"title"`
		} `json:"data"`
		Meta struct {
			Total  int            `json:"total"`
			Facets *ProductFacets `json:"facets"`
		} `json:"meta"`
	}
	get := func(query string) body {
		t.Helper()
		rec := do(t, app, http.MethodGet, "/api/products?"+query)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET ?%s = %d: %s", query, rec.Code, rec.Body.String())
		}
		var b body
		if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	counts := func(vs []FacetValue) map[string]int {
		out := map[string]int{}
		for _, v := range vs {
			out[v.Value] = v.Count
		}
		return out
	}
	eq := func(what string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", what, got, want)
		}
	}

	if f := get("").Meta.Facets; f != nil {
		t.Errorf("facets were sent without being asked for: %+v", f)
	}

	all := get("facets=1&limit=1").Meta.Facets
	eq("tags", counts(all.Tags), map[string]int{"summer": 2, "sale": 1, "winter": 1})
	eq("vendors", counts(all.Vendors), map[string]int{"Acme": 2, "Globex": 2})
	eq("product types", counts(all.ProductTypes), map[string]int{"Bag": 4})
	eq("material", counts(all.Attributes["material"]), map[string]int{"Canvas": 2, "Leather": 1})
	eq("price", *all.Price, PriceRange{Min: Money{1000, "USD"}, Max: Money{5000, "USD"}})
	if len(all.Categories) != 2 || all.Categories[0].Slug != bags.Slug || all.Categories[0].Count != 3 {
		t.Errorf("categories = %+v, want bags 3 then shoes 1", all.Categories)
	}

	// Ticking Acme narrows the listing and every other group, but the vendor
	// group still offers Globex with its full count.
	acme := get("facets=1&vendor=Acme")
	eq("Acme total", acme.Meta.Total, 2)
	eq("vendors under Acme", counts(acme.Meta.Facets.Vendors), map[string]int{"Acme": 2, "Globex": 2})
	eq("tags under Acme", counts(acme.Meta.Facets.Tags), map[string]int{"summer": 1, "sale": 1, "winter": 1})

	// Values of one group widen; groups narrow.
	eq("two tags", get("tag=sale&tag=winter").Meta.Total, 2)
	eq("tag and vendor", get("tag=summer&vendor=Globex").Meta.Total, 1)
	eq("two product types", get("product_type=Bag&product_type=Shoe").Meta.Total, 4)

	// A selected attribute keeps its siblings; the price range ignores its own bounds.
	canvas := get("facets=1&attr=material:Canvas&price_min=1500")
	eq("canvas over 15.00", canvas.Meta.Total, 1)
	eq("material under canvas", counts(canvas.Meta.Facets.Attributes["material"]), map[string]int{"Canvas": 1, "Leather": 1})
	eq("price under canvas", *canvas.Meta.Facets.Price, PriceRange{Min: Money{1000, "USD"}, Max: Money{2000, "USD"}})

	eq("price window", get("price_min=2000&price_max=3000").Meta.Total, 2)
	eq("category", get("category="+shoes.Slug).Meta.Total, 1)
	if rec := do(t, app, http.MethodGet, "/api/products?category=no-such-aisle"); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown category = %d, want 404", rec.Code)
	}

	cheap := get("sort=price&order=asc")
	if len(cheap.Data) != 4 || cheap.Data[0].Title != "Canvas pack" || cheap.Data[3].Title != "Runner" {
		t.Errorf("sort=price returned %+v", cheap.Data)
	}
	for _, bad := range []string{"sort=available", "price_min=-1", "price_max=ten"} {
		if rec := do(t, app, http.MethodGet, "/api/products?"+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d, want 400", bad, rec.Code)
		}
	}

	none := get("facets=1&q=nothing-matches-this").Meta.Facets
	if none.Price != nil || len(none.Tags) != 0 {
		t.Errorf("an empty listing has facets %+v", none)
	}
}
