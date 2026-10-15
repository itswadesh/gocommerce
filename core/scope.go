package gocommerce

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
)

// globalUniqueKeys are the unique keys that stay unique across every store.
// Each is a random value looked up before any store is known, or unique by
// construction, so scoping it would only weaken it.
var globalUniqueKeys = map[string][]string{
	"carts":         {"token"},
	"api_keys":      {"prefix"},
	"outbox_events": {"event_id"},
}

// ScopeTables gives each table a store: a store_id column (existing rows go
// to the oldest store), unique keys and foreign keys that lead with it, and a
// forced row-security policy that shows and accepts only the bound store's
// rows. Core and every module call it from a migration of their own; a table
// created later should create store_id itself and still pass through here
// for its keys and its policy.
//
// The order matters: foreign keys are captured and dropped first, because the
// keys they point at are about to be rebuilt; then each table gets its column
// and keys; then the foreign keys come back including the store; the policy
// goes on last.
func ScopeTables(ctx context.Context, tx *sql.Tx, tables ...string) error {
	var first string
	if err := tx.QueryRowContext(ctx,
		`SELECT id FROM stores ORDER BY created_at, id LIMIT 1`).Scan(&first); err != nil {
		return fmt.Errorf("scope: find the first store: %w", err)
	}
	for _, t := range tables {
		var exists bool
		if err := tx.QueryRowContext(ctx, `SELECT to_regclass($1) IS NOT NULL`, t).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return fmt.Errorf("scope %s: no such table", t)
		}
	}
	scoped, err := scopedTables(ctx, tx)
	if err != nil {
		return err
	}
	inScope := func(t string) bool { return slices.Contains(tables, t) || scoped[t] }

	fks, err := captureForeignKeys(ctx, tx, tables, inScope)
	if err != nil {
		return err
	}
	for _, fk := range fks {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s DROP CONSTRAINT %s`,
			quoteIdent(fk.table), quoteIdent(fk.name))); err != nil {
			return fmt.Errorf("scope %s: drop foreign key %s: %w", fk.table, fk.name, err)
		}
	}
	for _, t := range tables {
		if err := addStoreColumn(ctx, tx, t, first); err != nil {
			return fmt.Errorf("scope %s: add store_id: %w", t, err)
		}
		if err := rewriteUniqueKeys(ctx, tx, t); err != nil {
			return fmt.Errorf("scope %s: unique keys: %w", t, err)
		}
	}
	for _, fk := range fks {
		def, err := storeForeignKey(fk.def)
		if err != nil {
			return fmt.Errorf("scope %s: %s: %w", fk.table, fk.name, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s %s`,
			quoteIdent(fk.table), quoteIdent(fk.name), def)); err != nil {
			return fmt.Errorf("scope %s: restore foreign key %s as %s: %w", fk.table, fk.name, def, err)
		}
	}
	for _, t := range tables {
		if err := enableStorePolicy(ctx, tx, t); err != nil {
			return fmt.Errorf("scope %s: policy: %w", t, err)
		}
	}
	return nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// scopedTables lists the tables in this schema that already carry the store
// policy, so a module's foreign keys into core tables become per-store too.
func scopedTables(ctx context.Context, tx *sql.Tx) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT tablename FROM pg_policies
		WHERE schemaname = current_schema() AND policyname = 'store_scope'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out[t] = true
	}
	return out, rows.Err()
}

type foreignKey struct{ table, name, def string }

// captureForeignKeys finds every foreign key between two store tables where
// at least one side is being scoped now.
func captureForeignKeys(ctx context.Context, tx *sql.Tx, tables []string, inScope func(string) bool) ([]foreignKey, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT f.relname, p.relname, c.conname, pg_get_constraintdef(c.oid)
		FROM pg_constraint c
		JOIN pg_class f ON f.oid = c.conrelid
		JOIN pg_class p ON p.oid = c.confrelid
		JOIN pg_namespace n ON n.oid = f.relnamespace
		WHERE c.contype = 'f' AND n.nspname = current_schema()
		ORDER BY f.relname, c.conname`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []foreignKey
	for rows.Next() {
		var from, to, name, def string
		if err := rows.Scan(&from, &to, &name, &def); err != nil {
			return nil, err
		}
		if !inScope(from) || !inScope(to) {
			continue
		}
		if !slices.Contains(tables, from) && !slices.Contains(tables, to) {
			continue
		}
		if strings.HasPrefix(def, "FOREIGN KEY (store_id") {
			continue
		}
		out = append(out, foreignKey{table: from, name: name, def: def})
	}
	return out, rows.Err()
}

// storeForeignKey turns "FOREIGN KEY (a) REFERENCES p(x) ON DELETE SET NULL"
// into "FOREIGN KEY (store_id, a) REFERENCES p(store_id, x) ON DELETE SET
// NULL (a)". SET NULL names its columns, or it would null store_id too.
func storeForeignKey(def string) (string, error) {
	const head = "FOREIGN KEY ("
	if !strings.HasPrefix(def, head) {
		return "", fmt.Errorf("unexpected definition %q", def)
	}
	cols := def[len(head):strings.Index(def, ")")]
	out := head + "store_id, " + def[len(head):]
	ref := strings.Index(out, "REFERENCES ")
	if ref < 0 {
		return "", fmt.Errorf("unexpected definition %q", def)
	}
	open := strings.Index(out[ref:], "(")
	if open < 0 {
		return "", fmt.Errorf("unexpected definition %q", def)
	}
	open += ref + 1
	out = out[:open] + "store_id, " + out[open:]
	for _, action := range []string{"ON DELETE SET NULL", "ON UPDATE SET NULL", "ON DELETE SET DEFAULT", "ON UPDATE SET DEFAULT"} {
		out = strings.Replace(out, action, action+" ("+cols+")", 1)
	}
	return out, nil
}

func addStoreColumn(ctx context.Context, tx *sql.Tx, t, first string) error {
	var has, hasID bool
	if err := tx.QueryRowContext(ctx, `
		SELECT bool_or(column_name = 'store_id'), bool_or(column_name = 'id')
		FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1`, t).Scan(&has, &hasID); err != nil {
		return err
	}
	if !ValidStoreID(first) {
		return fmt.Errorf("first store id %q is malformed", first)
	}
	if !has {
		// A constant default fills every existing row as a catalog change,
		// without rewriting the table.
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(
			`ALTER TABLE %s ADD COLUMN store_id text NOT NULL DEFAULT '%s'`, quoteIdent(t), first)); err != nil {
			return err
		}
	}
	// From now on an insert that forgets store_id takes the bound store, and
	// with none bound it fails on NOT NULL rather than landing anywhere.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`
		ALTER TABLE %[1]s ALTER COLUMN store_id SET DEFAULT NULLIF(current_setting('gocommerce.store_id', true), '');
		ALTER TABLE %[1]s ADD CONSTRAINT %[2]s FOREIGN KEY (store_id) REFERENCES stores (id) ON DELETE CASCADE`,
		quoteIdent(t), quoteIdent(t+"_store_fkey"))); err != nil {
		return err
	}
	if hasID {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s UNIQUE (store_id, id)`,
			quoteIdent(t), quoteIdent(t+"_store_id_key"))); err != nil {
			return err
		}
	}
	return nil
}

// rewriteUniqueKeys puts store_id in front of every primary key, unique
// constraint and unique index on t, except a bare (id) key — ids are unique
// across the install already — and the globalUniqueKeys.
func rewriteUniqueKeys(ctx context.Context, tx *sql.Tx, t string) error {
	type key struct {
		name, def string
		cols      []string
		index     bool
	}
	var keys []key
	rows, err := tx.QueryContext(ctx, `
		SELECT c.conname, pg_get_constraintdef(c.oid),
		       COALESCE((SELECT string_agg(a.attname, ',' ORDER BY k.n) FROM unnest(c.conkey) WITH ORDINALITY k(num, n)
		                 JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = k.num), '')
		FROM pg_constraint c WHERE c.conrelid = $1::regclass AND c.contype IN ('p', 'u')`, t)
	if err != nil {
		return err
	}
	for rows.Next() {
		var k key
		var cols string
		if err := rows.Scan(&k.name, &k.def, &cols); err != nil {
			rows.Close()
			return err
		}
		k.cols = strings.Split(cols, ",")
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	rows, err = tx.QueryContext(ctx, `
		SELECT ic.relname, pg_get_indexdef(i.indexrelid),
		       COALESCE((SELECT string_agg(COALESCE(a.attname, '(expr)'), ',' ORDER BY k.n)
		                 FROM unnest(i.indkey::int2[]) WITH ORDINALITY k(num, n)
		                 LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.num), '')
		FROM pg_index i JOIN pg_class ic ON ic.oid = i.indexrelid
		WHERE i.indrelid = $1::regclass AND i.indisunique
		  AND NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = i.indexrelid)`, t)
	if err != nil {
		return err
	}
	for rows.Next() {
		k := key{index: true}
		var cols string
		if err := rows.Scan(&k.name, &k.def, &cols); err != nil {
			rows.Close()
			return err
		}
		k.cols = strings.Split(cols, ",")
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, k := range keys {
		if len(k.cols) > 0 && k.cols[0] == "store_id" {
			continue
		}
		if !k.index && slices.Equal(k.cols, []string{"id"}) {
			continue
		}
		if g, ok := globalUniqueKeys[t]; ok && slices.Equal(k.cols, g) {
			continue
		}
		if k.index {
			at := strings.Index(k.def, " USING btree (")
			if at < 0 {
				return fmt.Errorf("unexpected index %q", k.def)
			}
			at += len(" USING btree (")
			def := k.def[:at] + "store_id, " + k.def[at:]
			if _, err := tx.ExecContext(ctx, `DROP INDEX `+quoteIdent(k.name)); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, def); err != nil {
				return fmt.Errorf("%s: %w", def, err)
			}
			continue
		}
		at := strings.Index(k.def, "(")
		def := k.def[:at+1] + "store_id, " + k.def[at+1:]
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s DROP CONSTRAINT %s, ADD CONSTRAINT %s %s`,
			quoteIdent(t), quoteIdent(k.name), quoteIdent(k.name), def)); err != nil {
			return fmt.Errorf("%s: %w", def, err)
		}
	}
	return nil
}

func enableStorePolicy(ctx context.Context, tx *sql.Tx, t string) error {
	q := quoteIdent(t)
	_, err := tx.ExecContext(ctx, fmt.Sprintf(`
		ALTER TABLE %[1]s ENABLE ROW LEVEL SECURITY;
		ALTER TABLE %[1]s FORCE ROW LEVEL SECURITY;
		CREATE POLICY store_scope ON %[1]s
		  USING      (store_id = current_setting('gocommerce.store_id', true))
		  WITH CHECK (store_id = current_setting('gocommerce.store_id', true));`, q))
	if err != nil {
		return err
	}
	// The policy's filter needs an index that leads with store_id; a table
	// with an id already has one in (store_id, id).
	var led bool
	if err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_index i JOIN pg_attribute a
		  ON a.attrelid = i.indrelid AND a.attnum = i.indkey[0]
		  WHERE i.indrelid = $1::regclass AND a.attname = 'store_id')`, t).Scan(&led); err != nil {
		return err
	}
	if !led {
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`CREATE INDEX %s ON %s (store_id)`, quoteIdent(t+"_store_idx"), q))
	}
	return err
}
