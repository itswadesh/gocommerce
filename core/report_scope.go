package gocommerce

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
)

// Custom reports under shared tenancy (D83, Part 4).
//
// A report is SQL its author writes, and the engine's own row policy reads
// the store from gocommerce.store_id — a setting any SQL can change with
// set_config. So the reports login gets a second, restrictive policy on every
// store table, keyed on something a query cannot touch: the connection it is
// running on. Before each run the engine records "this backend runs a report
// for store X" in report_runs, a table the reports login has no rights on;
// the policy admits only X's rows, through a SECURITY DEFINER function that
// reads that record. A connection with no record sees nothing.
//
// The reports login may read store tables and nothing else: the tables that
// belong to no one store — people, their sessions and password resets, the
// store directory, API key routing — are revoked from it at every start, so a
// table added later is covered without anybody remembering this list.

// migration0060ReportRuns adds the record a report's connection is pinned by,
// and the function the policy reads it through. The function is a definer's
// so the reports login can ask "which store is mine" without being able to
// read, let alone write, anybody's record.
const migration0060ReportRuns = `
CREATE TABLE report_runs (
    pid        integer     PRIMARY KEY,
    store_id   text        NOT NULL REFERENCES stores (id) ON DELETE CASCADE,
    started_at timestamptz NOT NULL DEFAULT now()
);
CREATE FUNCTION gocommerce_report_store() RETURNS text
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path FROM CURRENT
    AS 'SELECT store_id FROM report_runs WHERE pid = pg_backend_pid()';
REVOKE ALL ON FUNCTION gocommerce_report_store() FROM PUBLIC;
`

// migration0079ReportWithheld is the list of store tables, and columns of
// them, a custom report may not read all the same, because a saved report
// hands what it reads to everyone with reports.read (D120): the error
// tracker's tables carry what errors.read guards (D86), and a one-time code or
// a token is a key to somebody's account. An empty column_name withholds the
// whole table. It is a table, not a list in code, so a module's entries stay
// withheld even on an install that later drops the module but keeps its
// tables. Modules add to it with WithholdFromReports.
const migration0079ReportWithheld = `
CREATE TABLE report_withheld (
    table_name  text NOT NULL,
    column_name text NOT NULL DEFAULT '',
    PRIMARY KEY (table_name, column_name)
);
`

// WithholdFromReports is a migration that keeps custom reports off a store
// table ("identity_login_codes") or one column of it ("carts.token"): one line
// in a module's Migrations, after the migration that creates the table. Name
// every table or column that holds a credential: staff may run any saved
// report. A name that matches nothing fails the migration, so a typo cannot
// leave a credential readable.
func WithholdFromReports(id string, targets ...string) Migration {
	return Migration{ID: id, Run: func(ctx context.Context, tx *sql.Tx) error {
		for _, target := range targets {
			table, column, _ := strings.Cut(target, ".")
			var found bool
			if err := tx.QueryRowContext(ctx, `
				SELECT EXISTS (SELECT 1 FROM information_schema.columns
				               WHERE table_schema = current_schema() AND table_name = $1
				                 AND ($2 = '' OR column_name = $2))`, table, column).Scan(&found); err != nil {
				return err
			}
			if !found {
				return fmt.Errorf("gocommerce: withhold %q from reports: no such table or column", target)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO report_withheld (table_name, column_name)
				VALUES ($1, $2) ON CONFLICT DO NOTHING`, table, column); err != nil {
				return err
			}
		}
		return nil
	}}
}

// reportsRole is the login the reports pool connects as.
func reportsRole(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil || u.User.Username() == "" {
		return "", fmt.Errorf("gocommerce: the reports database URL names no user")
	}
	return u.User.Username(), nil
}

// reportsAccessLockKey is an advisory lock key ("reportsa" in ASCII).
const reportsAccessLockKey int64 = 0x7265706F72747361

// reportsWithholdingProblems compares report_withheld with what the reports
// login may read. readable names what it may read all the same, granted by a
// version that did not know the name; gone names what a later migration
// renamed or dropped, which withholds nothing under the new name.
func (a *App) reportsWithholdingProblems(ctx context.Context) (readable, gone []string, err error) {
	role, err := reportsRole(a.cfg.ReportsDBURL)
	if err != nil {
		return nil, nil, err
	}
	rows, err := a.db.QueryContext(ctx, `
		WITH w AS (
		    SELECT w.table_name, w.column_name,
		           quote_ident(current_schema()) || '.' || quote_ident(w.table_name) AS rel,
		           EXISTS (SELECT 1 FROM information_schema.columns c
		                   WHERE c.table_schema = current_schema() AND c.table_name = w.table_name
		                     AND (w.column_name = '' OR c.column_name = w.column_name)) AS present
		    FROM report_withheld w)
		SELECT CASE WHEN column_name = '' THEN table_name ELSE table_name || '.' || column_name END,
		       present,
		       present AND CASE WHEN column_name = '' THEN has_any_column_privilege($1, rel, 'SELECT')
		                        ELSE has_column_privilege($1, rel, column_name, 'SELECT') END
		FROM w ORDER BY 1`, role)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var present, open bool
		if err := rows.Scan(&name, &present, &open); err != nil {
			return nil, nil, err
		}
		switch {
		case !present:
			gone = append(gone, name)
		case open:
			readable = append(readable, name)
		}
	}
	return readable, gone, rows.Err()
}

// checkReportWithholding fails when reports can read what is withheld from
// them (D120), and warns when a withheld name no longer exists.
func (a *App) checkReportWithholding(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "report withholding", Status: StatusOK}
	if a.reportsDB == nil {
		d.Detail = "custom reports are off"
		return d
	}
	readable, gone, err := a.reportsWithholdingProblems(ctx)
	switch {
	case err != nil:
		d.Status, d.Detail, d.Cause = StatusFail, "could not compare what reports may read with what is withheld", err.Error()
		d.Hint = "check that the engine can read report_withheld and the catalogue"
	case len(readable) > 0:
		d.Status = StatusFail
		d.Detail = "saved reports can read what is withheld from them: " + strings.Join(readable, ", ")
		d.Hint = "an older version granted it, during a rolling deploy or after a rollback; start this version again, which takes it back"
	case len(gone) > 0:
		d.Status = StatusWarn
		d.Detail = "withheld from reports but no longer in the database: " + strings.Join(gone, ", ")
		d.Hint = "a migration renamed or dropped it; a renamed one is readable under its new name until a migration withholds that name"
	default:
		d.Detail = "saved reports read nothing that is withheld from them"
	}
	return d
}

// ensureReportsAccess gives the reports login read on the store tables, under
// the restrictive policy, and takes everything else away from it. It runs at
// every start, after the migrations, and it is idempotent. It runs as the
// engine's own login, which owns the tables.
func (a *App) ensureReportsAccess(ctx context.Context) error {
	role, err := reportsRole(a.cfg.ReportsDBURL)
	if err != nil {
		return err
	}
	ident := quoteIdent(role)
	return InTx(ctx, a.db, func(tx *sql.Tx) error {
		// Copies of the engine starting together would grant on the same objects
		// at once, which PostgreSQL refuses, and each would create the same
		// policy: one at a time, reading the tables once it is this one's turn.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, reportsAccessLockKey); err != nil {
			return fmt.Errorf("gocommerce: set up reports access: %w", err)
		}
		rows, err := tx.QueryContext(ctx, `
		SELECT c.relname,
		       EXISTS (SELECT 1 FROM pg_policies p
		               WHERE p.schemaname = n.nspname AND p.tablename = c.relname AND p.policyname = 'store_scope'),
		       (SELECT p.roles::text[] FROM pg_policies p
		        WHERE p.schemaname = n.nspname AND p.tablename = c.relname AND p.policyname = 'report_scope'),
		       EXISTS (SELECT 1 FROM report_withheld w WHERE w.table_name = c.relname AND w.column_name = ''),
		       CASE WHEN EXISTS (SELECT 1 FROM report_withheld w WHERE w.table_name = c.relname AND w.column_name <> '')
		       THEN array_to_string(ARRAY(
		           SELECT quote_ident(a.attname) FROM pg_attribute a
		           WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped
		             AND a.attname NOT IN (SELECT w.column_name FROM report_withheld w WHERE w.table_name = c.relname)
		           ORDER BY a.attnum), ', ') END
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p') AND n.nspname = current_schema()
		ORDER BY c.relname`)
		if err != nil {
			return fmt.Errorf("gocommerce: list tables for reports: %w", err)
		}
		type table struct {
			name     string
			scoped   bool
			reports  []string
			withheld bool
			// readable lists the columns a report may read, when some of the
			// table's are withheld; NULL when all of them may be read.
			readable sql.NullString
		}
		var tables []table
		for rows.Next() {
			var t table
			var roles []string
			if err := rows.Scan(&t.name, &t.scoped, (*pgTextArray)(&roles), &t.withheld, &t.readable); err != nil {
				rows.Close()
				return fmt.Errorf("gocommerce: list tables for reports: %w", err)
			}
			t.reports = roles
			tables = append(tables, t)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("gocommerce: list tables for reports: %w", err)
		}

		exec := func(q string) error {
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("gocommerce: set up reports access (%s): %w", q, err)
			}
			return nil
		}
		if err := exec(`GRANT EXECUTE ON FUNCTION gocommerce_report_store() TO ` + ident); err != nil {
			return err
		}
		for _, t := range tables {
			name := quoteIdent(t.name)
			if !t.scoped || t.withheld || (t.readable.Valid && t.readable.String == "") {
				// No one store's, or withheld: not a report's to read at all.
				if err := exec(`REVOKE ALL ON ` + name + ` FROM ` + ident); err != nil {
					return err
				}
				continue
			}
			grant := `GRANT SELECT ON ` + name + ` TO ` + ident
			if t.readable.Valid {
				// Column by column, after taking back any grant on the whole
				// table: that one covers the withheld columns too.
				if err := exec(`REVOKE ALL ON ` + name + ` FROM ` + ident); err != nil {
					return err
				}
				grant = `GRANT SELECT (` + t.readable.String + `) ON ` + name + ` TO ` + ident
			}
			if err := exec(grant); err != nil {
				return err
			}
			if len(t.reports) == 1 && t.reports[0] == role {
				continue
			}
			if t.reports != nil {
				// Made for another reports login; this one is the login now.
				if err := exec(`DROP POLICY report_scope ON ` + name); err != nil {
					return err
				}
			}
			if err := exec(`CREATE POLICY report_scope ON ` + name +
				` AS RESTRICTIVE FOR SELECT TO ` + ident +
				` USING (store_id = gocommerce_report_store())`); err != nil {
				return err
			}
		}
		return nil
	})
}

// pgTextArray scans a text[] (or NULL) into a []string without pulling in a
// driver-specific array type.
type pgTextArray []string

func (p *pgTextArray) Scan(src any) error {
	if src == nil {
		*p = nil
		return nil
	}
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("pgTextArray: cannot scan %T", src)
	}
	// Role names here are identifiers the engine created or was given; the
	// policy's role list is one name, so a plain split of {a,b} is enough.
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return fmt.Errorf("pgTextArray: not an array: %q", s)
	}
	*p = []string{}
	for _, part := range splitComma(s[1 : len(s)-1]) {
		if len(part) >= 2 && part[0] == '"' && part[len(part)-1] == '"' {
			part = part[1 : len(part)-1]
		}
		*p = append(*p, part)
	}
	return nil
}

func splitComma(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}
