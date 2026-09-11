package gocommerce

import "net/http"

// Reporting is a reading of the orders, so it is gated by the right that reads
// them. An aggregate discloses strictly less than the rows behind it: orders.read
// already returns every one of those rows a page at a time, and data.export
// already streams the whole order book as CSV with no limit clause. A
// twenty-first right would be a lock on a window whose door is standing open,
// and D24's test for a right is whether a real store would ever draw the line
// there. A store that wants revenue kept from staff re-cuts staff in
// role_rights, which is what D24 exists for.
func (a *App) mountReportRoutes() {
	a.HandleAdminFunc("GET /api/admin/reports/sales", a.handleSalesReport, RightOrdersRead)
	a.HandleAdminFunc("GET /api/admin/reports/top-products", a.handleTopProducts, RightOrdersRead)
}

func (a *App) handleSalesReport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	query := SalesQuery{GroupBy: q.Get("group_by"), TimeZone: q.Get("tz")}

	var err error
	if query.From, err = parseReportBound(q.Get("from")); err != nil {
		RespondError(w, r, err)
		return
	}
	if query.To, err = parseReportBound(q.Get("to")); err != nil {
		RespondError(w, r, err)
		return
	}

	report, err := a.reports.Sales(r.Context(), query)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// One object, not RespondList: a report is a single reading with totals,
	// and page two of a bar chart is not a thing an operator wants. The window
	// is bounded by the bucket guard instead.
	Respond(w, http.StatusOK, report)
}

func (a *App) handleTopProducts(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := Page(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}

	q := r.URL.Query()
	query := TopProductsQuery{
		TimeZone: q.Get("tz"),
		Currency: q.Get("currency"),
		By:       q.Get("by"),
		Sort:     q.Get("sort"),
		Limit:    limit,
		Offset:   offset,
	}
	if query.From, err = parseReportBound(q.Get("from")); err != nil {
		RespondError(w, r, err)
		return
	}
	if query.To, err = parseReportBound(q.Get("to")); err != nil {
		RespondError(w, r, err)
		return
	}

	rows, total, err := a.reports.TopProducts(r.Context(), query)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	RespondList(w, rows, ListMeta{Total: total, Limit: limit, Offset: offset})
}
