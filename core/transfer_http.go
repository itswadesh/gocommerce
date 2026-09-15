package gocommerce

import (
	"fmt"
	"net/http"
	"time"
)

func (a *App) mountTransferRoutes() {
	// /api/admin/import/ is also served from taxonomy_http.go — the category
	// tree and its attribute dictionary, whose importers belong to the taxonomy
	// rather than to CSV transfer. Grep both before concluding the prefix has
	// two members.
	a.HandleAdminFunc("GET /api/admin/export/admin-products", a.handleExportProducts, RightDataExport)
	a.HandleAdminFunc("POST /api/admin/import/products", a.handleImportProducts, RightDataImport)
	a.HandleAdminFunc("GET /api/admin/export/admin-orders", a.handleExportOrders, RightDataExport)
	a.HandleAdminFunc("POST /api/admin/import/orders", a.handleImportOrders, RightDataImport)
	// data.export and not customers.read, which is the same judgement the other
	// two exports already carry: reading a listing on a screen and walking out
	// of the building with the whole of it as a file are different acts, and
	// data.export is the right that names the second one. There is no matching
	// import — a customer is a reading of the orders and has no table to be
	// written back into.
	a.HandleAdminFunc("GET /api/admin/export/admin-customers", a.handleExportCustomers, RightDataExport)
	// The stock-take's file: every count at every location, and the counts
	// back. The product file carries stock too; this one is for the day the
	// count is the only thing changing.
	a.HandleAdminFunc("GET /api/admin/export/admin-inventory", a.handleExportInventory, RightDataExport)
	a.HandleAdminFunc("POST /api/admin/import/inventory", a.handleImportInventory, RightDataImport)
	// The tree as a spreadsheet, the trail from the root in one cell. The
	// taxonomy importer next door reads Shopify's published list; this one
	// reads the store's own file, and adds or updates without ever moving
	// anything.
	a.HandleAdminFunc("GET /api/admin/export/admin-categories", a.handleExportCategories, RightDataExport)
	a.HandleAdminFunc("POST /api/admin/import/categories", a.handleImportCategories, RightDataImport)
}

func (a *App) handleExportCategories(w http.ResponseWriter, r *http.Request) {
	opts, err := exportOptionsFrom(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// Refused before a header is written, the shape handleExportProducts
	// explains: once the CSV headers are out the status line is spent.
	if opts.Format == FormatShopify {
		RespondError(w, r, errCategoryDialect)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", exportFilename("categories", opts.Format))
	if err := a.transfer.ExportCategories(r.Context(), w, opts); err != nil {
		a.log.Error("category export failed midway", "error", err)
	}
}

func (a *App) handleImportCategories(w http.ResponseWriter, r *http.Request) {
	opts, err := importOptionsFrom(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	body := limitedBody(w, r, maxUploadBytes)
	result, err := a.transfer.ImportCategories(r.Context(), body, opts)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, result)
}

func (a *App) handleExportInventory(w http.ResponseWriter, r *http.Request) {
	opts, err := exportOptionsFrom(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", exportFilename("inventory", opts.Format))
	if err := a.transfer.ExportInventory(r.Context(), w, opts); err != nil {
		a.log.Error("inventory export failed midway", "error", err)
	}
}

func (a *App) handleImportInventory(w http.ResponseWriter, r *http.Request) {
	opts, err := importOptionsFrom(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	body := limitedBody(w, r, maxUploadBytes)
	result, err := a.transfer.ImportInventory(r.Context(), body, opts)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, result)
}

func (a *App) handleExportProducts(w http.ResponseWriter, r *http.Request) {
	// Parsed before a header is written, the shape handleExportOrders already
	// has: once the CSV headers are out the status line is spent, so a bad
	// filter has to become a 400 in the envelope before then.
	query, err := productQueryFrom(r.URL.Query())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	opts, err := exportOptionsFrom(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", exportFilename("products", opts.Format))
	if err := a.transfer.ExportProducts(r.Context(), w, query, opts); err != nil {
		// The response has already begun, so the status line is spent. Log it
		// and let the truncated file be the signal — pretending it succeeded
		// would be worse.
		a.log.Error("product export failed midway", "error", err)
	}
}

func (a *App) handleExportOrders(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := OrderQuery{Status: q.Get("status")}
	var err error
	if query.From, err = parseDate(q.Get("from")); err != nil {
		RespondError(w, r, err)
		return
	}
	if query.To, err = parseDate(q.Get("to")); err != nil {
		RespondError(w, r, err)
		return
	}
	opts, err := exportOptionsFrom(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", exportFilename("orders", opts.Format))
	if err := a.transfer.ExportOrders(r.Context(), w, query, opts); err != nil {
		a.log.Error("order export failed midway", "error", err)
	}
}

func (a *App) handleExportCustomers(w http.ResponseWriter, r *http.Request) {
	// Both parsed before a header is written, for handleExportProducts' reason:
	// once the CSV headers are out the status line is spent, so a bad sort has
	// to become a 400 in the envelope before then.
	sortBy, err := ParseSort(r, customerSorts)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	query := CustomerQuery{Search: r.URL.Query().Get("q"), Sort: sortBy}
	opts, err := exportOptionsFrom(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", exportFilename("customers", opts.Format))
	if err := a.transfer.ExportCustomers(r.Context(), w, query, opts); err != nil {
		a.log.Error("customer export failed midway", "error", err)
	}
}

func (a *App) handleImportProducts(w http.ResponseWriter, r *http.Request) {
	opts, err := importOptionsFrom(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	body := limitedBody(w, r, maxUploadBytes)
	result, err := a.transfer.ImportProducts(r.Context(), body, opts)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, result)
}

// importOptionsFrom reads the switches every import shares. `format` is
// optional — the header says which dialect a file is in — and `overwrite`
// is only ever sent to turn the default off.
func importOptionsFrom(r *http.Request) (ImportOptions, error) {
	q := r.URL.Query()
	format, err := ParseFormat(q.Get("format"))
	if err != nil {
		return ImportOptions{}, err
	}
	opts := ImportOptions{DryRun: boolParam(r, "dry_run"), FireEvents: boolParam(r, "fire_events")}
	if q.Get("format") != "" {
		opts.Format = format
	}
	if q.Get("overwrite") != "" {
		overwrite := boolParam(r, "overwrite")
		opts.Overwrite = &overwrite
	}
	return opts, nil
}

// exportOptionsFrom reads the dialect, and where the store is: picture URLs
// leave as absolute addresses so the file means the same thing elsewhere.
func exportOptionsFrom(r *http.Request) (ExportOptions, error) {
	format, err := ParseFormat(r.URL.Query().Get("format"))
	if err != nil {
		return ExportOptions{}, err
	}
	return ExportOptions{Format: format, BaseURL: requestBaseURL(r)}, nil
}

// requestBaseURL is the address the request came to, as a proxy in front
// reports it, or empty when nothing said.
func requestBaseURL(r *http.Request) string {
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	if host == "" {
		return ""
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + host
}

// exportFilename names the download by what it holds and the dialect it is
// in, so two files on one desk can be told apart.
func exportFilename(kind string, format Format) string {
	name := kind
	if format == FormatShopify {
		name += "-shopify"
	}
	return fmt.Sprintf(`attachment; filename="%s-%s.csv"`, name, time.Now().UTC().Format("2006-01-02"))
}

func (a *App) handleImportOrders(w http.ResponseWriter, r *http.Request) {
	// Events are off unless explicitly asked for: importing history must not
	// email five thousand people about orders they placed last year.
	opts, err := importOptionsFrom(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	body := limitedBody(w, r, maxUploadBytes)
	result, err := a.transfer.ImportOrders(r.Context(), body, opts)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.nudgeOutbox()
	Respond(w, http.StatusOK, result)
}

func boolParam(r *http.Request, name string) bool {
	switch r.URL.Query().Get(name) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
