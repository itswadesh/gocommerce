package gocommerce

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (a *App) mountCatalogRoutes() {
	// Public catalog. Only active products are visible; a draft or archived
	// product is not found rather than forbidden, because its existence is
	// not a shopper's business.
	a.HandleFunc("GET /api/products", a.handleListProducts)
	a.HandleFunc("GET /api/products/{id}", a.handleGetProduct)
	a.HandleFunc("GET /api/products/slug/{slug}", a.handleGetProductBySlug)
	a.HandleFunc("GET /api/products/sku/{sku}", a.handleGetProductBySKU)
	// Variants are their own collection rather than a sub-path of a product.
	// "/api/products/{id}/variants" would collide with the slug and sku
	// lookups above — "/api/products/slug/variants" matches both patterns and
	// neither is more specific — and Litekart compatibility makes those
	// lookups the ones that have to keep their shape.
	a.HandleFunc("GET /api/variants", a.handleListVariants)
	a.HandleFunc("GET /api/variants/{id}", a.handleGetVariant)

	// Admin catalog.
	a.HandleAdminFunc("GET /api/admin/products", a.handleAdminListProducts, RightCatalogRead)
	a.HandleAdminFunc("POST /api/admin/products", a.handleCreateProduct, RightCatalogWrite)
	a.HandleAdminFunc("GET /api/admin/products/{id}", a.handleAdminGetProduct, RightCatalogRead)
	a.HandleAdminFunc("PATCH /api/admin/products/{id}", a.handleUpdateProduct, RightCatalogWrite)
	a.HandleAdminFunc("DELETE /api/admin/products/{id}", a.handleDeleteProduct, RightCatalogWrite)
	a.HandleAdminFunc("POST /api/admin/products/{id}/options", a.handleAddOption, RightCatalogWrite)
	// PUT replaces the whole matrix and reconciles the variants with it. That
	// is what an editor needs: renaming an axis or dropping a value is one
	// intent, and splitting it across calls leaves the product incoherent in
	// between.
	a.HandleAdminFunc("PUT /api/admin/products/{id}/options", a.handleSetOptions, RightCatalogWrite)
	a.HandleAdminFunc("POST /api/admin/products/{id}/variants", a.handleCreateVariant, RightCatalogWrite)
	a.HandleAdminFunc("PATCH /api/admin/variants/{id}", a.handleUpdateVariant, RightCatalogWrite)
	a.HandleAdminFunc("DELETE /api/admin/variants/{id}", a.handleDeleteVariant, RightCatalogWrite)
}

// ------------------------------------------------------------------- public

func (a *App) handleListProducts(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// The storefront gets the facet filter too, and it is the place it earns
	// its keep: narrowing a category down to the canvas bags is a shopper's
	// move before it is an operator's. Status stays pinned to active here —
	// this listing has never shown a draft and an attribute must not be a way
	// to reach one.
	products, total, err := a.catalog.ListProducts(r.Context(), ProductQuery{
		Search:     r.URL.Query().Get("q"),
		Status:     ProductActive,
		Attributes: attributeFilters(r.URL.Query()["attr"]),
		Limit:      limit,
		Offset:     offset,
	})
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.translateProducts(r, products)
	RespondList(w, products, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleGetProduct(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	p, err := a.catalog.GetProduct(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.respondPublicProduct(w, r, p)
}

func (a *App) handleGetProductBySlug(w http.ResponseWriter, r *http.Request) {
	p, err := a.catalog.GetProductBySlug(r.Context(), r.PathValue("slug"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.respondPublicProduct(w, r, p)
}

// handleGetProductBySKU resolves a variant SKU to its product. SKU is already
// the catalog's stable key for import and export, so it is the natural handle
// for a storefront deep link too.
func (a *App) handleGetProductBySKU(w http.ResponseWriter, r *http.Request) {
	v, err := a.catalog.GetVariantBySKU(r.Context(), r.PathValue("sku"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	p, err := a.catalog.GetProduct(r.Context(), v.ProductID)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.respondPublicProduct(w, r, p)
}

func (a *App) respondPublicProduct(w http.ResponseWriter, r *http.Request, p *Product) {
	if p.Status != ProductActive {
		RespondError(w, r, NotFoundf("product not found"))
		return
	}
	a.translateProducts(r, []*Product{p})
	Respond(w, http.StatusOK, p)
}

func (a *App) handleListVariants(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("product_id")
	if raw == "" {
		RespondError(w, r, Validationf("product_id is required"))
		return
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		RespondError(w, r, Validationf("product_id must be a positive integer"))
		return
	}
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	variants, err := a.catalog.ListVariants(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}

	// One product's variants are a bounded set, so this pages in memory rather
	// than in SQL. What matters is that the endpoint honours the same
	// limit/offset/page contract as every other collection: a client that asks
	// for ten and silently receives fifty has no way to know it happened.
	total := len(variants)
	if offset > total {
		offset = total
	}
	end := min(offset+limit, total)
	RespondList(w, variants[offset:end], ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleGetVariant(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	v, err := a.catalog.GetVariant(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, v)
}

// -------------------------------------------------------------------- admin

func (a *App) handleAdminListProducts(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	pq, err := productQueryFrom(r.URL.Query())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// sortBy rather than sort: the stdlib `sort` is imported by sibling files in
	// this package, and a local shadow of it is a trap for the next edit.
	sortBy, err := ParseSort(r, productSorts)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// A collection's order is the one an operator curated by hand, so the two
	// parameters together are a contradiction rather than a precedence puzzle.
	// Refused, not resolved: silently overriding the curation would make the
	// same column header mean different things on two screens, and silently
	// ignoring the sort would look like a sort that worked.
	if pq.CollectionID > 0 && sortBy.Field != "" {
		RespondError(w, r, Validationf(
			"sort cannot be combined with collection_id: a collection is shown in its curated order"))
		return
	}
	pq.Sort = sortBy
	pq.Limit, pq.Offset = limit, offset
	products, total, err := a.catalog.ListProducts(r.Context(), pq)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, products, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleAdminGetProduct(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	p, err := a.catalog.GetProduct(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, p)
}

func (a *App) handleCreateProduct(w http.ResponseWriter, r *http.Request) {
	var in ProductInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	p, err := a.catalog.CreateProduct(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, p)
}

func (a *App) handleUpdateProduct(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var patch ProductPatch
	if err := DecodeJSON(w, r, &patch); err != nil {
		RespondError(w, r, err)
		return
	}
	p, err := a.catalog.UpdateProduct(r.Context(), id, patch)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, p)
}

func (a *App) handleDeleteProduct(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.catalog.DeleteProduct(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetOptions replaces the option matrix. The response carries what
// changed alongside the product, so the panel can tell the operator which
// variants it just created or removed rather than leaving them to notice.
func (a *App) handleSetOptions(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in OptionSet
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	p, change, err := a.catalog.SetOptions(r.Context(), id, in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, map[string]any{"product": p, "changed": change})
}

func (a *App) handleAddOption(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in OptionInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	p, err := a.catalog.AddOption(r.Context(), id, in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, p)
}

func (a *App) handleCreateVariant(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var in VariantInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	v, err := a.catalog.CreateVariant(r.Context(), id, in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, v)
}

func (a *App) handleUpdateVariant(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	var patch VariantPatch
	if err := DecodeJSON(w, r, &patch); err != nil {
		RespondError(w, r, err)
		return
	}
	v, err := a.catalog.UpdateVariant(r.Context(), id, patch)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, v)
}

func (a *App) handleDeleteVariant(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.catalog.DeleteVariant(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ------------------------------------------------------------------ helpers

func pathInt64(r *http.Request, name string) (int64, error) {
	raw := r.PathValue(name)
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, Validationf("%s must be a positive integer", name)
	}
	return id, nil
}

// productQueryFrom reads the filter vocabulary the admin product listing, the
// collection membership read and the catalog export share, so an operator can
// export exactly the rows a screen is showing them.
//
// Paging is deliberately not part of it: a listing takes a window, an export
// takes everything that matched.
func productQueryFrom(q url.Values) (ProductQuery, error) {
	status := q.Get("status")
	if status != "" && !validProductStatus(status) {
		return ProductQuery{}, Validationf("status must be draft, active or archived")
	}
	categoryID, err := queryInt64(q, "category_id")
	if err != nil {
		return ProductQuery{}, err
	}
	collectionID, err := queryInt64(q, "collection_id")
	if err != nil {
		return ProductQuery{}, err
	}
	return ProductQuery{
		Search:       q.Get("q"),
		Status:       status,
		Vendor:       q.Get("vendor"),
		ProductType:  q.Get("product_type"),
		Tag:          q.Get("tag"),
		CategoryID:   categoryID,
		CollectionID: collectionID,
		Attributes:   attributeFilters(q["attr"]),
	}, nil
}

// attributeFilters reads the repeated `?attr=handle:value` parameter.
//
// Repeated rather than comma-joined, because a taxonomy value may contain a
// comma — "Bags, totes and cases" is a real Shopify value — and there is no
// separator that is safe inside free text. The same reason picks the split:
// only the FIRST colon separates, so "color:Black:ish" is the value
// "Black:ish" rather than an error. Attribute handles are normalised on the way
// in and never contain a colon, so the first one is always the right one.
//
// Values for the same handle are gathered together, because that is what makes
// them widen the result rather than narrow it: `?attr=m:Canvas&attr=m:Leather`
// is one filter with two values, not two filters that no product can satisfy.
//
// A parameter with no colon, an empty handle or an empty value is ignored
// rather than refused. A hand-edited or truncated URL should narrow to
// everything or to nothing; answering 400 to a listing because one facet was
// malformed takes the whole screen away over a fixable typo.
func attributeFilters(raw []string) []AttributeFilter {
	if len(raw) == 0 {
		return nil
	}
	order := make([]string, 0, len(raw))
	byKey := make(map[string][]string, len(raw))
	for _, entry := range raw {
		key, value, ok := strings.Cut(entry, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		if _, seen := byKey[key]; !seen {
			order = append(order, key)
		}
		byKey[key] = append(byKey[key], value)
	}
	// Built in the order the keys first appeared, so the SQL a request produces
	// is stable and a slow query log stays readable.
	out := make([]AttributeFilter, 0, len(order))
	for _, key := range order {
		out = append(out, AttributeFilter{Key: key, Values: byKey[key]})
	}
	return out
}

// queryInt64 reads an optional positive id from the query string. Absent is 0
// and not an error; present but unparseable is an error rather than a silent 0,
// because a filter that quietly matches everything is how a typo in a category
// id turns into "why is this listing showing the whole catalogue".
func queryInt64(q url.Values, name string) (int64, error) {
	raw := strings.TrimSpace(q.Get(name))
	if raw == "" {
		return 0, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, Validationf("%s must be a positive integer", name)
	}
	return id, nil
}
