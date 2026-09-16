package gocommerce

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Custom reports: a SELECT somebody saved.
//
// The canned reports answer "how much did we sell", which is the question
// every shop has. This answers the ones only this shop has — which of my
// wholesale customers has not ordered since March, what did the Tuesday
// promotion actually cost — and no fixed screen can enumerate those.
//
// It is the one place in this engine where a person's own SQL reaches the
// database, so the interesting part is what it refuses, and there are two
// layers.
//
// The outer one is a parse. It accepts a single statement beginning SELECT or
// WITH, with comments stripped first so a verb cannot hide behind one, and
// rejects a WITH whose body writes — which is the one way a statement starting
// with WITH can change data. It exists for the error message: an operator who
// pastes an UPDATE should read "a report may only read" rather than a driver
// error about a read-only transaction.
//
// The inner one is the read-only transaction, and that is what actually holds.
// Every run happens inside BEGIN ... SET TRANSACTION READ ONLY with a
// statement timeout, and the transaction is always rolled back. PostgreSQL
// refuses INSERT, UPDATE, DELETE, and every DDL inside it, so a parse bug is a
// bad error message rather than a lost table. A test proves that by going
// round the parse on purpose.
//
// What this does NOT do is sandbox reading. A report can select anything the
// engine's database user can select, which is everything — so reports.write is
// owner's alone, and the note on the screen says that saving a report is
// deciding what everyone with reports.read may look at.

const (
	// MaxReportRows is what one run will return. A report is something a
	// person reads or exports; past a thousand rows it is a query somebody
	// should be running against a replica with a real tool.
	MaxReportRows = 1000
	// reportTimeout stops one bad join holding a connection open. It is a
	// statement timeout inside the transaction, so PostgreSQL enforces it
	// rather than the client giving up while the query runs on.
	reportTimeout = 15 * time.Second
	// MaxReportName keeps the list readable; MaxReportSQL is a sanity bound.
	MaxReportName = 120
	MaxReportSQL  = 20000
)

// CustomReport is one saved report.
type CustomReport struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	SQL         string    `json:"sql"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	CreatedBy   *int64    `json:"created_by,omitempty"`
	UpdatedBy   *int64    `json:"updated_by,omitempty"`
}

// CustomReportInput is what an operator provides. A zero ID means a new one.
type CustomReportInput struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	SQL         string `json:"sql"`
}

// ReportResult is one run.
type ReportResult struct {
	Columns []string `json:"columns"`
	// Rows are strings because a report is displayed and exported, and a
	// column can hold anything PostgreSQL can return. Rendering is the
	// database's job through its own cast, not this file's through a type
	// switch that would have to know about every type a store might add.
	Rows [][]*string `json:"rows"`
	// Truncated says the answer was cut at MaxReportRows. Without it a
	// thousand-row prefix looks exactly like a thousand-row answer.
	Truncated bool `json:"truncated"`
	// Took is how long the query ran, which is the number somebody tuning a
	// report is looking for.
	Took time.Duration `json:"took_ms"`
}

// CustomReports is the custom-report service. Named apart from Reports, which
// is the canned sales report: they answer different kinds of question and
// share nothing but the word.
type CustomReports struct{ app *App }

// CustomReports returns the service.
func (a *App) CustomReports() *CustomReports { return a.customReports }

// ------------------------------------------------------------- the parse

// reportComments strips -- to end of line and /* ... */, so a verb cannot hide
// behind one. Not a full SQL lexer: a comment marker inside a string literal
// is stripped too, which can only ever make a query fail to parse rather than
// let one through, and failing closed is the direction to be wrong in.
var reportComments = regexp.MustCompile(`(?s)--[^\n]*|/\*.*?\*/`)

// reportWrites finds a writing verb anywhere in the statement, which is how a
// CTE smuggles one past a first-word check:
//
//	WITH gone AS (DELETE FROM orders RETURNING id) SELECT * FROM gone
//
// Word-bounded so a column called `update_id` or a table called `inserts` is
// not mistaken for one.
var reportWrites = regexp.MustCompile(`(?i)\b(insert|update|delete|truncate|drop|alter|create|grant|revoke|copy|vacuum|call|do|merge|refresh|reindex|lock|comment|security)\b`)

// checkReportSQL says whether a string is a single read, and why not if it is
// not. The error is what an operator sees, so it says what to do.
func checkReportSQL(query string) (string, error) {
	trimmed := strings.TrimSpace(query)
	if trimmed == "" {
		return "", Validationf("a report needs a query")
	}
	if len(trimmed) > MaxReportSQL {
		return "", Validationf("the query is at most %d characters", MaxReportSQL)
	}

	// What the database will actually execute, minus anything a reader would
	// skip. The original is what gets stored and run; this is only judged.
	bare := strings.TrimSpace(reportComments.ReplaceAllString(trimmed, " "))
	// A trailing semicolon is how everybody writes SQL. One in the middle is a
	// second statement.
	bare = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(bare), ";"))
	if strings.Contains(bare, ";") {
		return "", Validationf("a report is one statement; there is a semicolon in the middle of this one")
	}
	upper := strings.ToUpper(bare)
	if !strings.HasPrefix(upper, "SELECT") && !strings.HasPrefix(upper, "WITH") {
		return "", Validationf("a report may only read: start the query with SELECT, or with WITH for a common table expression")
	}
	if where := reportWrites.FindString(bare); where != "" {
		return "", Validationf("a report may only read, and this one contains %s", strings.ToUpper(where))
	}
	return trimmed, nil
}

// ------------------------------------------------------------- running

// Run executes a query and returns what it selected.
func (r *CustomReports) Run(ctx context.Context, query string) (*ReportResult, error) {
	checked, err := checkReportSQL(query)
	if err != nil {
		return nil, err
	}
	return r.runInReadOnlyTx(ctx, checked)
}

// runInReadOnlyTx is the layer that actually holds, and is called directly by
// the test that proves a write cannot land even when the parse is bypassed.
func (r *CustomReports) runInReadOnlyTx(ctx context.Context, query string) (*ReportResult, error) {
	tx, err := r.app.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, Internalf(err, "start the report")
	}
	// Always. There is nothing to commit — a read-only transaction has made no
	// changes — and rolling back is the statement that this was never going to
	// write anything.
	defer func() { _ = tx.Rollback() }()

	// Belt and braces over the driver's ReadOnly, which is a connection option
	// rather than a promise about this transaction.
	if _, err := tx.ExecContext(ctx, "SET TRANSACTION READ ONLY"); err != nil {
		return nil, Internalf(err, "start the report")
	}
	if _, err := tx.ExecContext(ctx,
		fmt.Sprintf("SET LOCAL statement_timeout = %d", reportTimeout.Milliseconds())); err != nil {
		return nil, Internalf(err, "start the report")
	}

	started := time.Now()
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		// The database's own words. An operator debugging their SQL needs the
		// column name PostgreSQL could not find, and wrapping it in something
		// tidier throws that away.
		return nil, Validationf("%s", err.Error())
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		return nil, Internalf(err, "read the report")
	}

	out := &ReportResult{Columns: columns, Rows: [][]*string{}}
	for rows.Next() {
		if len(out.Rows) >= MaxReportRows {
			out.Truncated = true
			break
		}
		cells := make([]*string, len(columns))
		into := make([]any, len(columns))
		for i := range cells {
			into[i] = &cells[i]
		}
		if err := rows.Scan(into...); err != nil {
			return nil, Internalf(err, "read a row")
		}
		out.Rows = append(out.Rows, cells)
	}
	if err := rows.Err(); err != nil {
		return nil, Validationf("%s", err.Error())
	}
	out.Took = time.Since(started)
	return out, nil
}

// ------------------------------------------------------------- saving

// Save creates a report or updates one, and refuses SQL that could not run.
//
// Checked at save time and not only at run time, because a report that cannot
// run is not worth keeping and the moment its author can fix it is now.
func (r *CustomReports) Save(ctx context.Context, in CustomReportInput, by *Superuser) (*CustomReport, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, Validationf("a report needs a name, so the list says what each one answers")
	}
	if len([]rune(name)) > MaxReportName {
		return nil, Validationf("the name is at most %d characters", MaxReportName)
	}
	query, err := checkReportSQL(in.SQL)
	if err != nil {
		return nil, err
	}
	description := strings.TrimSpace(in.Description)

	var actor *int64
	if by != nil {
		actor = &by.ID
	}
	var out CustomReport
	if in.ID == 0 {
		err = r.app.db.QueryRowContext(ctx, `
			INSERT INTO custom_reports (name, description, sql, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $4)
			RETURNING id, name, description, sql, created_at, updated_at, created_by, updated_by`,
			name, description, query, actor,
		).Scan(&out.ID, &out.Name, &out.Description, &out.SQL,
			&out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy)
	} else {
		err = r.app.db.QueryRowContext(ctx, `
			UPDATE custom_reports
			   SET name = $2, description = $3, sql = $4, updated_at = now(), updated_by = $5
			 WHERE id = $1
			RETURNING id, name, description, sql, created_at, updated_at, created_by, updated_by`,
			in.ID, name, description, query, actor,
		).Scan(&out.ID, &out.Name, &out.Description, &out.SQL,
			&out.CreatedAt, &out.UpdatedAt, &out.CreatedBy, &out.UpdatedBy)
		if err == sql.ErrNoRows {
			return nil, NotFoundf("no such report")
		}
	}
	if err != nil {
		return nil, Internalf(err, "save the report")
	}
	return &out, nil
}

// Saved lists every report, newest first.
func (r *CustomReports) Saved(ctx context.Context) ([]CustomReport, error) {
	rows, err := r.app.db.QueryContext(ctx, `
		SELECT id, name, description, sql, created_at, updated_at, created_by, updated_by
		FROM custom_reports ORDER BY name`)
	if err != nil {
		return nil, Internalf(err, "list the reports")
	}
	defer rows.Close()

	out := []CustomReport{}
	for rows.Next() {
		var c CustomReport
		if err := rows.Scan(&c.ID, &c.Name, &c.Description, &c.SQL,
			&c.CreatedAt, &c.UpdatedAt, &c.CreatedBy, &c.UpdatedBy); err != nil {
			return nil, Internalf(err, "read a report")
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Get reads one report.
func (r *CustomReports) Get(ctx context.Context, id int64) (*CustomReport, error) {
	var c CustomReport
	err := r.app.db.QueryRowContext(ctx, `
		SELECT id, name, description, sql, created_at, updated_at, created_by, updated_by
		FROM custom_reports WHERE id = $1`, id,
	).Scan(&c.ID, &c.Name, &c.Description, &c.SQL,
		&c.CreatedAt, &c.UpdatedAt, &c.CreatedBy, &c.UpdatedBy)
	if err == sql.ErrNoRows {
		return nil, NotFoundf("no such report")
	}
	if err != nil {
		return nil, Internalf(err, "read the report")
	}
	return &c, nil
}

// Delete removes a report. It holds no data of its own, so there is nothing to
// keep a record of.
func (r *CustomReports) Delete(ctx context.Context, id int64) error {
	res, err := r.app.db.ExecContext(ctx, `DELETE FROM custom_reports WHERE id = $1`, id)
	if err != nil {
		return Internalf(err, "delete the report")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return NotFoundf("no such report")
	}
	return nil
}

// ------------------------------------------------------------------- routes

func (a *App) mountCustomReportRoutes() {
	a.HandleAdminFunc("GET /api/admin/reports/custom", a.handleListCustomReports, RightReportsRead)
	a.HandleAdminFunc("POST /api/admin/reports/custom", a.handleSaveCustomReport, RightReportsWrite)
	a.HandleAdminFunc("PATCH /api/admin/reports/custom/{id}", a.handleSaveCustomReport, RightReportsWrite)
	a.HandleAdminFunc("DELETE /api/admin/reports/custom/{id}", a.handleDeleteCustomReport, RightReportsWrite)
	// Running a saved one is reports.read: the point of saving a report is
	// that somebody else can run it, and its SQL was reviewed by whoever could
	// write it.
	a.HandleAdminFunc("POST /api/admin/reports/custom/{id}/run", a.handleRunSavedReport, RightReportsRead)
	// Running unsaved SQL is reports.write, because that is writing a report —
	// it just has not been kept.
	a.HandleAdminFunc("POST /api/admin/reports/custom/run", a.handleRunAdhocReport, RightReportsWrite)
}

func (a *App) handleListCustomReports(w http.ResponseWriter, r *http.Request) {
	out, err := a.customReports.Saved(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}

func (a *App) handleSaveCustomReport(w http.ResponseWriter, r *http.Request) {
	var in CustomReportInput
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	// The path wins over the body: a PATCH to /42 that carries id 7 is a typo,
	// and guessing which one was meant is how one report overwrites another.
	if raw := r.PathValue("id"); raw != "" {
		id, err := pathInt64(r, "id")
		if err != nil {
			RespondError(w, r, err)
			return
		}
		in.ID = id
	}
	out, err := a.customReports.Save(r.Context(), in, SuperuserFrom(r.Context()))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	status := http.StatusOK
	if in.ID == 0 {
		status = http.StatusCreated
	}
	Respond(w, status, out)
}

func (a *App) handleDeleteCustomReport(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.customReports.Delete(r.Context(), id); err != nil {
		RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleRunSavedReport(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt64(r, "id")
	if err != nil {
		RespondError(w, r, err)
		return
	}
	report, err := a.customReports.Get(r.Context(), id)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.respondReportRun(w, r, report.SQL)
}

func (a *App) handleRunAdhocReport(w http.ResponseWriter, r *http.Request) {
	var in struct {
		SQL string `json:"sql"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	a.respondReportRun(w, r, in.SQL)
}

func (a *App) respondReportRun(w http.ResponseWriter, r *http.Request, query string) {
	out, err := a.customReports.Run(r.Context(), query)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// Milliseconds on the wire: a Duration marshals as nanoseconds, which is
	// a number nobody reads.
	Respond(w, http.StatusOK, struct {
		*ReportResult
		Took int64 `json:"took_ms"`
	}{ReportResult: out, Took: out.Took.Milliseconds()})
}
