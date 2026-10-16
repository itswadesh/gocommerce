package gocommerce

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
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

// reportsRole is the login the reports pool connects as.
func reportsRole(dsn string) (string, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil || u.User.Username() == "" {
		return "", fmt.Errorf("gocommerce: the reports database URL names no user")
	}
	return u.User.Username(), nil
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
	rows, err := a.db.QueryContext(ctx, `
		SELECT c.relname,
		       EXISTS (SELECT 1 FROM pg_policies p
		               WHERE p.schemaname = n.nspname AND p.tablename = c.relname AND p.policyname = 'store_scope'),
		       (SELECT p.roles::text[] FROM pg_policies p
		        WHERE p.schemaname = n.nspname AND p.tablename = c.relname AND p.policyname = 'report_scope')
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p') AND n.nspname = current_schema()
		ORDER BY c.relname`)
	if err != nil {
		return fmt.Errorf("gocommerce: list tables for reports: %w", err)
	}
	type table struct {
		name    string
		scoped  bool
		reports []string
	}
	var tables []table
	for rows.Next() {
		var t table
		var roles []string
		if err := rows.Scan(&t.name, &t.scoped, (*pgTextArray)(&roles)); err != nil {
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

	return InTx(ctx, a.db, func(tx *sql.Tx) error {
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
			if !t.scoped {
				// No one store's: not a report's to read at all.
				if err := exec(`REVOKE ALL ON ` + name + ` FROM ` + ident); err != nil {
					return err
				}
				continue
			}
			if err := exec(`GRANT SELECT ON ` + name + ` TO ` + ident); err != nil {
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
