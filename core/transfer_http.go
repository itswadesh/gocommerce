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
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="products-%s.csv"`, time.Now().UTC().Format("2006-01-02")))
	if err := a.transfer.ExportProducts(r.Context(), w, query); err != nil {
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
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="orders-%s.csv"`, time.Now().UTC().Format("2006-01-02")))
	if err := a.transfer.ExportOrders(r.Context(), w, query); err != nil {
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
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="customers-%s.csv"`, time.Now().UTC().Format("2006-01-02")))
	if err := a.transfer.ExportCustomers(r.Context(), w, query); err != nil {
		a.log.Error("customer export failed midway", "error", err)
	}
}

func (a *App) handleImportProducts(w http.ResponseWriter, r *http.Request) {
	body := limitedBody(w, r, maxUploadBytes)
	result, err := a.transfer.ImportProducts(r.Context(), body, boolParam(r, "dry_run"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, result)
}

func (a *App) handleImportOrders(w http.ResponseWriter, r *http.Request) {
	body := limitedBody(w, r, maxUploadBytes)
	// Events are off unless explicitly asked for: importing history must not
	// email five thousand people about orders they placed last year.
	result, err := a.transfer.ImportOrders(r.Context(), body,
		boolParam(r, "dry_run"), boolParam(r, "fire_events"))
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
