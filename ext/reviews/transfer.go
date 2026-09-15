package reviews

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

// Reviews as a spreadsheet.
//
// Two jobs, one file. A store moving in from another platform arrives with a
// few thousand reviews in a CSV and no other way to get them here; and a
// store already running here moderates in bulk faster in a spreadsheet than
// in a list, by exporting, sorting by rating, and pasting a column of
// statuses back.
//
// The product is named by slug, the way the product file names it, because
// an id from another store means nothing here and an id from this one means
// nothing in a spreadsheet somebody edits by hand. A row that carries an `id`
// is the store's own row coming back and updates it; a row without one is a
// new review.

// reviewCSVHeader is the file's layout. `product_title` is written for the
// reader and ignored on the way in — the slug is what resolves.
var reviewCSVHeader = []string{
	"id", "product_slug", "product_title", "name", "email",
	"rating", "title", "body", "status", "verified", "reply", "created_at",
}

// mountTransferRoutes adds the file doors. Data export and import rather
// than the catalogue's rights, the same judgement the engine's own exports
// make: reading a page of reviews on a screen and walking out with every
// review and every reviewer's email address are different acts.
func (m *Module) mountTransferRoutes(app *gocommerce.App) {
	app.HandleAdminFunc("GET /api/admin/x/reviews/export", m.handleExport, gocommerce.RightDataExport)
	app.HandleAdminFunc("POST /api/admin/x/reviews/import", m.handleImport, gocommerce.RightDataImport)
}

func (m *Module) handleExport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf(`attachment; filename="reviews-%s.csv"`, time.Now().UTC().Format("2006-01-02")))
	if err := m.Export(r.Context(), w); err != nil {
		// The response has already begun, so the status line is spent: the
		// truncated file is the signal, and pretending otherwise is worse.
		m.app.Log().Error("review export failed midway", "error", err)
	}
}

func (m *Module) handleImport(w http.ResponseWriter, r *http.Request) {
	dry := false
	switch r.URL.Query().Get("dry_run") {
	case "1", "true", "yes", "on":
		dry = true
	}
	result, err := m.Import(r.Context(), http.MaxBytesReader(w, r.Body, 32<<20), dry)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, result)
}

// Export streams every review, newest first, with the product it is about.
func (m *Module) Export(ctx context.Context, out io.Writer) error {
	w, err := gocommerce.NewCSVWriter(out, reviewCSVHeader)
	if err != nil {
		return err
	}
	// LEFT JOIN, because reviews.product_id is deliberately not a foreign
	// key: a review of a product that has since been deleted is still a fact
	// about the store, and leaving it out of the file would quietly lose it.
	rows, err := m.db.QueryContext(ctx, `
		SELECT r.id, coalesce(p.slug, ''), coalesce(p.title, ''), r.name, r.email,
		       r.rating, r.title, r.body, r.status, r.verified, r.reply, r.created_at
		FROM reviews r
		LEFT JOIN products p ON p.id = r.product_id
		ORDER BY r.id DESC`)
	if err != nil {
		return gocommerce.Internalf(err, "read the reviews")
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var slug, product, name, email, title, body, status, reply string
		var rating int
		var verified bool
		var created time.Time
		if err := rows.Scan(&id, &slug, &product, &name, &email, &rating,
			&title, &body, &status, &verified, &reply, &created); err != nil {
			return gocommerce.Internalf(err, "scan the reviews")
		}
		if err := w.Write([]string{
			strconv.FormatInt(id, 10), slug, product, name, email,
			strconv.Itoa(rating), title, body, status, strconv.FormatBool(verified),
			reply, created.UTC().Format(time.RFC3339),
		}); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return gocommerce.Internalf(err, "read the reviews")
	}
	return w.Flush()
}

// Import reads reviews back. A row carrying an `id` the store has updates
// that review; every other row is a new one.
//
// One row at a time, each in its own transaction, the shape the engine's
// inventory import has: a row naming a product that is not here is a row
// error and the other 999 still land.
func (m *Module) Import(ctx context.Context, in io.Reader, dryRun bool) (*gocommerce.ImportResult, error) {
	start := time.Now()
	r, err := gocommerce.NewCSVReader(in)
	if err != nil {
		return nil, err
	}
	if !r.Has("product_slug") && !r.Has("id") {
		return nil, gocommerce.Validationf(
			"the CSV needs %q to say what a review is about, or %q to name a review the store already has",
			"product_slug", "id")
	}

	result := &gocommerce.ImportResult{DryRun: dryRun}
	// Slugs resolve once each: a file of a thousand reviews of forty products
	// is forty questions, not a thousand.
	products := map[string]int64{}
	for {
		row, err := r.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			result.Errors = append(result.Errors, gocommerce.RowError{Line: r.Line(), Message: err.Error()})
			result.Skipped++
			continue
		}
		line := row.Line()
		fail := func(format string, args ...any) {
			result.Errors = append(result.Errors, gocommerce.RowError{Line: line, Message: fmt.Sprintf(format, args...)})
			result.Skipped++
		}

		id, err := row.Int64("id", 0)
		if err != nil {
			fail("%v", err)
			continue
		}
		rating, err := row.Int("rating", 0)
		if err != nil {
			fail("%v", err)
			continue
		}
		status := strings.ToLower(strings.TrimSpace(row.Get("status")))
		if status != "" && status != StatusPending && status != StatusApproved && status != StatusRejected {
			fail("status %q is not pending, approved or rejected", status)
			continue
		}

		var productID int64
		if slug := strings.ToLower(strings.TrimSpace(row.Get("product_slug"))); slug != "" {
			if productID = products[slug]; productID == 0 {
				err := m.db.QueryRowContext(ctx, `SELECT id FROM products WHERE lower(slug) = $1`, slug).Scan(&productID)
				if errors.Is(err, sql.ErrNoRows) {
					fail("no product has the slug %q", slug)
					continue
				}
				if err != nil {
					return nil, gocommerce.Internalf(err, "find the product")
				}
				products[slug] = productID
			}
		}

		updating := id > 0
		if !updating {
			// Everything the table insists on, checked here so the failure
			// names the column rather than the constraint.
			if productID == 0 {
				fail("a new review needs product_slug")
				continue
			}
			if strings.TrimSpace(row.Get("name")) == "" {
				fail("a new review needs a name")
				continue
			}
			if strings.TrimSpace(row.Get("body")) == "" {
				fail("a new review needs a body")
				continue
			}
			if rating < 1 || rating > 5 {
				fail("rating is %d; it has to be 1 to 5", rating)
				continue
			}
		} else if row.Has("rating") && (rating < 1 || rating > 5) {
			fail("rating is %d; it has to be 1 to 5", rating)
			continue
		}

		err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
			if updating {
				if err := m.updateFromRow(ctx, tx, id, row, productID, result); err != nil {
					return err
				}
			} else if err := m.insertFromRow(ctx, tx, row, productID, rating, status, result); err != nil {
				return err
			}
			if dryRun {
				// The row proved it would apply; rolling back is what makes
				// the rehearsal a rehearsal.
				return errDryRun
			}
			return nil
		})
		var reject rowReject
		switch {
		case err == nil, errors.Is(err, errDryRun):
		case errors.As(err, &reject):
			fail("%v", reject)
		default:
			return nil, err
		}
	}
	result.Duration = time.Since(start).String()
	return result, nil
}

// rowReject carries a row's own mistake back out of the transaction, so that
// it is reported as a row error rather than as a failed file.
type rowReject struct{ err error }

func (r rowReject) Error() string { return r.err.Error() }
func (r rowReject) Unwrap() error { return r.err }

func rejected(err error) error { return rowReject{err} }

// errDryRun rolls a rehearsal back. The row still proved it would apply,
// which is the only thing a rehearsal is for.
var errDryRun = errors.New("dry run")

func (m *Module) insertFromRow(ctx context.Context, tx *sql.Tx, row gocommerce.CSVRow,
	productID int64, rating int, status string, result *gocommerce.ImportResult) error {
	if status == "" {
		status = StatusPending
	}
	email := strings.TrimSpace(row.Get("email"))
	// Verified is the store's own judgement, not the file's: a file that does
	// not mention it gets the answer the orders give, which is the whole
	// point of the badge. A file that does mention it is believed, because a
	// store moving in carries verifications the orders here cannot show.
	verified := row.Bool("verified", false)
	if !row.Has("verified") && email != "" {
		bought, err := m.bought(ctx, email, productID)
		if err != nil {
			return err
		}
		verified = bought
	}
	body := row.Get("body")
	if len(body) > maxBody {
		return rejected(fmt.Errorf("the body is %d characters; the limit is %d", len(body), maxBody))
	}
	created := time.Now()
	if cell := strings.TrimSpace(row.Get("created_at")); cell != "" {
		parsed, err := parseReviewTime(cell)
		if err != nil {
			return rejected(err)
		}
		created = parsed
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO reviews (product_id, name, email, rating, title, body, status, verified, reply, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)`,
		productID, strings.TrimSpace(row.Get("name")), email, rating,
		strings.TrimSpace(row.Get("title")), body, status, verified,
		strings.TrimSpace(row.Get("reply")), created); err != nil {
		return rejected(err)
	}
	result.Created++
	return nil
}

func (m *Module) updateFromRow(ctx context.Context, tx *sql.Tx, id int64, row gocommerce.CSVRow,
	productID int64, result *gocommerce.ImportResult) error {
	sets, args := []string{"updated_at = now()"}, []any{}
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	// A blank cell says nothing and leaves the column alone, which is what
	// lets an operator paste one column of statuses back over an export.
	if productID != 0 {
		set("product_id", productID)
	}
	if row.Has("name") {
		set("name", row.Get("name"))
	}
	if row.Has("email") {
		set("email", row.Get("email"))
	}
	if row.Has("rating") {
		rating, err := row.Int("rating", 0)
		if err != nil {
			return rejected(err)
		}
		set("rating", rating)
	}
	if row.Has("title") {
		set("title", row.Get("title"))
	}
	if row.Has("body") {
		body := row.Get("body")
		if len(body) > maxBody {
			return rejected(fmt.Errorf("the body is %d characters; the limit is %d", len(body), maxBody))
		}
		set("body", body)
	}
	if row.Has("status") {
		set("status", strings.ToLower(strings.TrimSpace(row.Get("status"))))
	}
	if row.Has("verified") {
		set("verified", row.Bool("verified", false))
	}
	if row.Has("reply") {
		set("reply", row.Get("reply"))
	}

	res, err := tx.ExecContext(ctx,
		"UPDATE reviews SET "+strings.Join(sets, ", ")+fmt.Sprintf(" WHERE id = $%d", len(args)+1),
		append(args, id)...)
	if err != nil {
		return rejected(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return rejected(fmt.Errorf("no review has the id %d", id))
	}
	result.Updated++
	return nil
}

// parseReviewTime reads what a spreadsheet is likely to have written: the
// export's own format first, then the two a spreadsheet produces when it
// decides a timestamp is a date.
func parseReviewTime(cell string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, cell); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("created_at %q is not a date; write it as 2006-01-02 or 2006-01-02T15:04:05Z", cell)
}
