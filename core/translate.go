package gocommerce

import (
	"context"
	"net/http"
	"strings"
)

// The storefront's category and collection names in the shopper's language
// (D87). translateProducts did the products alone, so a store selling in Hindi
// showed Hindi titles under English navigation; these apply the same port to
// the names a shopper browses by — on their own routes and riding along on a
// product — and nowhere on an admin route, where the operator edits the
// store's own text.

// TranslateProducts names products in the request's language, with their
// category and collections, the way core's own storefront reads do. It is for
// a module's public route that hands out products (ext/showcases), so a buyer
// reading Hindi sees the same names there as on the product's own page.
func (a *App) TranslateProducts(r *http.Request, products []*Product) {
	a.translateProducts(r, products)
}

// translationLanguage is the language to translate this request into, or ""
// when there is nothing to do: no translator, or a request already in the
// store's main language, which is what its catalogue is written in.
func (a *App) translationLanguage(r *http.Request) string {
	if a.translator == nil {
		return ""
	}
	lang := a.RequestLanguage(r)
	if strings.EqualFold(lang, a.mainLanguage(r.Context())) {
		return ""
	}
	return lang
}

// translations is one batched lookup that degrades to nothing: a shopper would
// rather read the main language than an error page.
func (a *App) translations(ctx context.Context, lang, kind string, ids []int64) map[int64]map[string]string {
	if len(ids) == 0 {
		return nil
	}
	out, err := a.translator.Translate(ctx, lang, kind, ids)
	if err != nil {
		a.log.Error("translator failed", "language", lang, "kind", kind, "error", err)
		return nil
	}
	return out
}

// applyOverride overwrites dst with a non-empty override, keeping the stored text for a
// field nobody has translated yet.
func applyOverride(dst *string, fields map[string]string, key string) {
	if v := fields[key]; v != "" {
		*dst = v
	}
}

// translateCategories names a list, and any children it carries, in the
// request's language.
func (a *App) translateCategories(r *http.Request, list []*Category) {
	lang := a.translationLanguage(r)
	if lang == "" || len(list) == 0 {
		return
	}
	var ids []int64
	var walk func([]*Category)
	walk = func(cs []*Category) {
		for _, c := range cs {
			ids = append(ids, c.ID)
			walk(c.Children)
		}
	}
	walk(list)
	names := a.categoryNames(r.Context(), lang, ids)
	var apply func([]*Category)
	apply = func(cs []*Category) {
		for _, c := range cs {
			if n, ok := names[c.ID]; ok {
				c.Title, c.FullName = n.title, n.fullName
			}
			apply(c.Children)
		}
	}
	apply(list)
}

// translateCollections names a list in the request's language.
func (a *App) translateCollections(r *http.Request, list []*Collection) {
	lang := a.translationLanguage(r)
	if lang == "" || len(list) == 0 {
		return
	}
	ids := make([]int64, 0, len(list))
	for _, c := range list {
		ids = append(ids, c.ID)
	}
	over := a.translations(r.Context(), lang, KindCollection, ids)
	for _, c := range list {
		applyOverride(&c.Title, over[c.ID], "title")
		applyOverride(&c.Description, over[c.ID], "description")
	}
}

// translateProductLinks names the category and collections riding along on a
// page of products, so a breadcrumb reads in the same language as the title
// above it.
func (a *App) translateProductLinks(ctx context.Context, lang string, products []*Product) {
	var categoryIDs, collectionIDs []int64
	for _, p := range products {
		if p.Category != nil {
			categoryIDs = append(categoryIDs, p.Category.ID)
		}
		for _, c := range p.Collections {
			collectionIDs = append(collectionIDs, c.ID)
		}
	}
	names := a.categoryNames(ctx, lang, categoryIDs)
	collections := a.translations(ctx, lang, KindCollection, collectionIDs)
	for _, p := range products {
		if p.Category != nil {
			if n, ok := names[p.Category.ID]; ok {
				p.Category.Title, p.Category.FullName = n.title, n.fullName
			}
		}
		for i := range p.Collections {
			applyOverride(&p.Collections[i].Title, collections[p.Collections[i].ID], "title")
		}
	}
}

type categoryName struct{ title, fullName string }

// categoryNames is each category's title and full name in lang. A full name is
// its ancestors' titles joined, so every ancestor is translated with it, in
// the same one lookup: "Jewellery / Rings" becomes "आभूषण / अंगूठियाँ" only when
// both halves do, and keeps whichever half nobody has translated.
func (a *App) categoryNames(ctx context.Context, lang string, ids []int64) map[int64]categoryName {
	if len(ids) == 0 {
		return nil
	}
	// Root first per category; the depth bound only guards against a cycle
	// that the tree's own checks already prevent.
	rows, err := a.db.QueryContext(ctx, `
		WITH RECURSIVE chain AS (
		    SELECT id AS leaf, id, parent_id, title, 0 AS depth
		    FROM categories WHERE id = ANY($1::bigint[])
		    UNION ALL
		    SELECT chain.leaf, c.id, c.parent_id, c.title, chain.depth + 1
		    FROM categories c JOIN chain ON c.id = chain.parent_id
		    WHERE chain.depth < $2)
		SELECT leaf, id, title FROM chain ORDER BY leaf, depth DESC`, int64Array(ids), MaxCategoryDepth)
	if err != nil {
		a.log.Error("category names", "error", err)
		return nil
	}
	defer rows.Close()
	type link struct {
		id    int64
		title string
	}
	chains := map[int64][]link{}
	seen := map[int64]bool{}
	var all []int64
	for rows.Next() {
		var leaf int64
		var l link
		if err := rows.Scan(&leaf, &l.id, &l.title); err != nil {
			a.log.Error("category names", "error", err)
			return nil
		}
		chains[leaf] = append(chains[leaf], l)
		if !seen[l.id] {
			seen[l.id] = true
			all = append(all, l.id)
		}
	}
	if rows.Err() != nil {
		return nil
	}
	over := a.translations(ctx, lang, KindCategory, all)
	out := make(map[int64]categoryName, len(chains))
	for leaf, chain := range chains {
		titles := make([]string, len(chain))
		for i, l := range chain {
			titles[i] = l.title
			applyOverride(&titles[i], over[l.id], "title")
		}
		out[leaf] = categoryName{title: titles[len(titles)-1], fullName: strings.Join(titles, " / ")}
	}
	return out
}
