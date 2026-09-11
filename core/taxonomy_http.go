package gocommerce

import (
	"bufio"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

func (a *App) mountTaxonomyRoutes() {
	// The two imports sit under /api/admin/import/ because that prefix is the
	// data door and RightDataImport is already its key (see transfer_http.go);
	// they live in this file because the importers are the taxonomy's, not the
	// CSV transfer's.
	a.HandleAdminFunc("POST /api/admin/import/taxonomy", a.handleImportTaxonomy, RightDataImport)
	a.HandleAdminFunc("POST /api/admin/import/category-attributes", a.handleImportCategoryAttributes, RightDataImport)

	// The dictionary is catalog vocabulary — what a product may be asked — so
	// it is read with the catalog and written with it, exactly like categories.
	a.HandleAdminFunc("GET /api/admin/taxonomy-attributes", a.handleListTaxonomyAttributes, RightCatalogRead)
	a.HandleAdminFunc("POST /api/admin/taxonomy-attributes", a.handleCreateTaxonomyAttribute, RightCatalogWrite)
	a.HandleAdminFunc("GET /api/admin/taxonomy-attributes/{handle}", a.handleGetTaxonomyAttribute, RightCatalogRead)
	a.HandleAdminFunc("PATCH /api/admin/taxonomy-attributes/{handle}", a.handleUpdateTaxonomyAttribute, RightCatalogWrite)
	a.HandleAdminFunc("DELETE /api/admin/taxonomy-attributes/{handle}", a.handleDeleteTaxonomyAttribute, RightCatalogWrite)
}

// ------------------------------------------------------------------- imports

// extendDeadlines gives one request longer than the server's own ReadTimeout
// and WriteTimeout.
//
// Both taxonomy imports are a single transaction over ~14,000 rows, which on a
// remote database outruns the 60-second write timeout and hands the operator a
// dead connection over a transaction that commits anyway; and both newly accept
// an operator's own multi-megabyte file, which a 15-second read timeout can kill
// before a row is parsed.
//
// The failure is logged rather than swallowed. A test recorder that cannot carry
// a deadline is not a reason to refuse an import — but a production chain that
// lost its Unwrap (Config.AdminAuth is a seam a store replaces) must be visible,
// because the symptom is a truncated response nobody can explain.
func extendDeadlines(w http.ResponseWriter, r *http.Request, d time.Duration) {
	rc, until := http.NewResponseController(w), time.Now().Add(d)
	if err := rc.SetWriteDeadline(until); err != nil {
		logFrom(r).Warn("could not extend the write deadline for a long import", "error", err)
	}
	if err := rc.SetReadDeadline(until); err != nil {
		logFrom(r).Warn("could not extend the read deadline for a long import", "error", err)
	}
}

// taxonomyBody returns the reader an import should read, and whether it is the
// embedded set. An empty body means embedded, the same default the CLI takes.
//
// It peeks rather than reading the body, because an operator's own file is
// megabytes and the importer wants a reader, not a copy of one held in memory —
// and because ImportTaxonomy answers an empty reader with "that file contains no
// categories", which is not what an empty body means here.
func taxonomyBody(w http.ResponseWriter, r *http.Request, embedded string) (io.Reader, bool) {
	body := bufio.NewReader(limitedBody(w, r, maxUploadBytes))
	if _, err := body.Peek(1); err != nil {
		// Anything unreadable is treated as absent: a body that cannot be read
		// at all has nothing in it either, and the import that follows reports
		// what it did with the embedded set.
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return strings.NewReader(embedded), true
		}
	}
	return body, false
}

// handleImportTaxonomy loads a category tree, embedded or uploaded.
func (a *App) handleImportTaxonomy(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, r, taxonomyImportDeadline)
	body, embedded := taxonomyBody(w, r, ShopifyTaxonomy())

	cats := a.Categories()
	tree, err := cats.ImportTaxonomy(r.Context(), body)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	result := TaxonomyImportResult{Source: "upload", Categories: tree}
	if embedded {
		result.Source = "embedded"
		// The fields follow the tree, and only for the embedded source: they
		// are matched on the taxonomy id ImportTaxonomy writes, so a tree that
		// came from somewhere else has nothing to match and every line would be
		// unmatched — a report of zeros that reads like a success. This is the
		// rule `gocommerce taxonomy import` already follows.
		attrs, err := cats.ImportCategoryAttributes(r.Context(),
			strings.NewReader(ShopifyCategoryAttributes()))
		if err != nil {
			RespondError(w, r, err)
			return
		}
		result.Attributes = &attrs
	}
	Respond(w, http.StatusOK, result)
}

// handleImportCategoryAttributes loads the field definitions alone, for a tree
// that is already in place.
func (a *App) handleImportCategoryAttributes(w http.ResponseWriter, r *http.Request) {
	extendDeadlines(w, r, taxonomyImportDeadline)
	body, _ := taxonomyBody(w, r, ShopifyCategoryAttributes())

	result, err := a.Categories().ImportCategoryAttributes(r.Context(), body)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, result)
}

// ---------------------------------------------------------------- dictionary

// pathHandle folds the same way normalizeHandle does, so
// /taxonomy-attributes/Color addresses the row a category asking for "color"
// will actually match.
func pathHandle(r *http.Request) (string, error) { return normalizeHandle(r.PathValue("handle")) }

func (a *App) handleListTaxonomyAttributes(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	list, total, err := a.Categories().ListAttributes(r.Context(), TaxonomyAttributeQuery{
		Search: r.URL.Query().Get("q"),
		Limit:  limit,
		Offset: offset,
	})
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, list, ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (a *App) handleGetTaxonomyAttribute(w http.ResponseWriter, r *http.Request) {
	handle, err := pathHandle(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	attr, err := a.Categories().GetAttribute(r.Context(), handle)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, attr)
}

func (a *App) handleCreateTaxonomyAttribute(w http.ResponseWriter, r *http.Request) {
	var in TaxonomyAttributeInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	attr, err := a.Categories().CreateAttribute(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, attr)
}

func (a *App) handleUpdateTaxonomyAttribute(w http.ResponseWriter, r *http.Request) {
	handle, err := pathHandle(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// A patch carrying "handle" is rejected by DecodeJSON's unknown-field check
	// rather than ignored, which is the whole reason the field is absent from
	// the patch type: a rename that silently did nothing is worse than a 400.
	var patch TaxonomyAttributePatch
	if err := DecodeJSON(w, r, &patch); err != nil {
		RespondError(w, r, err)
		return
	}
	attr, err := a.Categories().UpdateAttribute(r.Context(), handle, patch)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, attr)
}

func (a *App) handleDeleteTaxonomyAttribute(w http.ResponseWriter, r *http.Request) {
	handle, err := pathHandle(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.Categories().DeleteAttribute(r.Context(), handle); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
