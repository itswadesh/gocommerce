package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// Finding every stored secret, for two readers: `gocommerce secrets seal`,
// which seals what is readable and moves what the previous key sealed under
// the current one, and the doctor, which only counts.
//
// Plugin settings are core's to walk. A secret a module keeps in a table of
// its own — a webhook endpoint's signing secret — is reached because the
// module named the column at Register; core never learns what the module is.

// SecretColumn is a column of a module's own table holding one secret a row
// that the engine reads back, sealed with SealSecret under Field. Empty means
// unset.
type SecretColumn struct {
	Table  string
	Column string
	// Field is the label the values are sealed under.
	Field string
	// Label says what the values are, in the seal command's output.
	Label string
}

var sqlNameRE = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// RegisterSecretColumn names a column holding sealed secrets, from a module's
// Register, so `gocommerce secrets seal` and the doctor reach it. The table
// must be behind the store policy, as every module table is: the walk runs
// once per store, and seals each value for the store it belongs to.
func (a *App) RegisterSecretColumn(c SecretColumn) {
	if !sqlNameRE.MatchString(c.Table) || !sqlNameRE.MatchString(c.Column) || c.Field == "" || c.Label == "" {
		a.regErrf("module %q: secret column %q.%q: want a lowercase table and column name, a field and a label",
			a.ownerName(), c.Table, c.Column)
		return
	}
	a.secretColumns = append(a.secretColumns, c)
}

// SecretCount is where one kind of secret stands in the stores walked.
type SecretCount struct {
	Label string `json:"label"`
	// Readable values are stored as typed. A seal pass seals them.
	Readable int `json:"readable"`
	// Previous values are sealed under GOCOMMERCE_SECRETS_KEY_PREVIOUS. A seal
	// pass moves them under the current key.
	Previous int `json:"previous"`
	// Current values are sealed under GOCOMMERCE_SECRETS_KEY already.
	Current int `json:"current"`
	// Unreadable values are sealed and open under no key this install has.
	// They read as unset and are left exactly as they are.
	Unreadable int `json:"unreadable"`
	// Where names the fields the unreadable ones are in, never their values.
	Where []string `json:"where,omitempty"`
}

func (c *SecretCount) add(o SecretCount) {
	c.Readable += o.Readable
	c.Previous += o.Previous
	c.Current += o.Current
	c.Unreadable += o.Unreadable
	for _, w := range o.Where {
		if !slices.Contains(c.Where, w) {
			c.Where = append(c.Where, w)
		}
	}
}

type secretState int

const (
	stateReadable secretState = iota
	stateCurrent
	statePrevious
	stateUnreadable
)

// classify says where one stored value stands, and opens it when it can.
func (k secretKeys) classify(store, field, stored string) (secretState, string) {
	if !isSealed(stored) {
		return stateReadable, stored
	}
	plain, by, err := k.open(store, field, stored)
	switch {
	case err != nil:
		return stateUnreadable, ""
	case by == k.current:
		return stateCurrent, plain
	default:
		return statePrevious, plain
	}
}

func (c *SecretCount) count(state secretState, n int, where string) {
	switch state {
	case stateReadable:
		c.Readable += n
	case stateCurrent:
		c.Current += n
	case statePrevious:
		c.Previous += n
	case stateUnreadable:
		c.Unreadable += n
		if !slices.Contains(c.Where, where) {
			c.Where = append(c.Where, where)
		}
	}
}

// SealReport is what one run of `gocommerce secrets seal` did.
type SealReport struct {
	// KeyID is the current key's id, as sealed values name it.
	KeyID  string        `json:"key_id"`
	Stores int           `json:"stores"`
	Counts []SecretCount `json:"counts"`
	// NotInstalled are modules that have tables in this database and are not
	// installed in this run, so any secret they keep was not reached.
	NotInstalled []string `json:"not_installed,omitempty"`
}

// SealSecrets seals every readable secret in every store under the current
// key, and moves what the previous key sealed under it too, so the previous
// key can then be retired. Running it again changes nothing. A value no key
// here opens is counted, named and left exactly as it is.
//
// Every store, suspended ones too: a store reopened after the previous key is
// gone would otherwise find its secrets unreadable.
func (a *App) SealSecrets(ctx context.Context) (*SealReport, error) {
	if a.secrets.current == nil {
		return nil, fmt.Errorf("%s is not set, so there is no key to seal under; set it, restart the store with it, then run this", SecretsKeyEnv)
	}
	rows, err := a.db.QueryContext(ctx, `SELECT id FROM stores ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	var stores []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		stores = append(stores, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	rep := &SealReport{KeyID: a.secrets.current.id, Stores: len(stores)}
	for _, id := range stores {
		counts, err := a.surveySecrets(WithStore(ctx, id), true)
		if err != nil {
			return rep, fmt.Errorf("store %s: %w", id, err)
		}
		if rep.Counts == nil {
			rep.Counts = counts
			continue
		}
		for i := range counts {
			rep.Counts[i].add(counts[i])
		}
	}
	rep.NotInstalled, err = a.modulesNotInstalled(ctx)
	return rep, err
}

// modulesNotInstalled are the owners of applied migrations this App was not
// built with.
func (a *App) modulesNotInstalled(ctx context.Context) ([]string, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT DISTINCT owner FROM `+migrationsTable+` WHERE owner <> $1 ORDER BY owner`, coreMigrationOwner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	installed := a.Modules()
	var out []string
	for rows.Next() {
		var owner string
		if err := rows.Scan(&owner); err != nil {
			return nil, err
		}
		if !slices.Contains(installed, owner) {
			out = append(out, owner)
		}
	}
	return out, rows.Err()
}

// surveySecrets counts the bound store's secrets and, with write, seals the
// readable ones and moves the previous key's under the current one. The
// counts come back in a fixed order: plugin settings, then each registered
// column.
func (a *App) surveySecrets(ctx context.Context, write bool) ([]SecretCount, error) {
	plugins, err := a.surveyPluginSecrets(ctx, write)
	if err != nil {
		return nil, err
	}
	out := []SecretCount{plugins}
	for _, col := range a.secretColumns {
		c, err := a.surveySecretColumn(ctx, col, write)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

func (a *App) surveyPluginSecrets(ctx context.Context, write bool) (SecretCount, error) {
	c := SecretCount{Label: "plugin settings"}
	store := a.storeOf(ctx)
	err := InTx(ctx, a.db, func(tx *sql.Tx) error {
		q := `SELECT key, settings FROM plugins ORDER BY key`
		if write {
			// Held until the row is rewritten, so an operator's save in the
			// meantime is neither lost nor overwritten with an older value.
			q += ` FOR UPDATE`
		}
		rows, err := tx.QueryContext(ctx, q)
		if err != nil {
			return err
		}
		type row struct {
			key      string
			settings map[string]any
		}
		var all []row
		for rows.Next() {
			var r row
			var raw []byte
			if err := rows.Scan(&r.key, &raw); err != nil {
				rows.Close()
				return err
			}
			if json.Unmarshal(raw, &r.settings) != nil {
				continue
			}
			all = append(all, r)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		for _, r := range all {
			// A field is a secret when this binary's descriptor says so. A row
			// whose plugin is not installed here is still walked for values that
			// are sealed already — those are secrets whatever the descriptor —
			// and its readable ones wait for a run that has the module.
			secret := map[string]bool{}
			if def, ok := a.plugins.def(r.key); ok {
				for _, f := range def.Fields {
					secret[f.Key] = f.Kind == "secret"
				}
			}
			keys := make([]string, 0, len(r.settings))
			for k := range r.settings {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			changed := false
			for _, k := range keys {
				s, ok := r.settings[k].(string)
				if !ok || s == "" || (!isSealed(s) && !secret[k]) {
					continue
				}
				field := pluginSecretField(r.key, k)
				state, plain := a.secrets.classify(store, field, s)
				c.count(state, 1, field)
				if write && (state == stateReadable || state == statePrevious) {
					sealed, err := a.secrets.seal(store, field, plain)
					if err != nil {
						return err
					}
					r.settings[k] = sealed
					changed = true
				}
			}
			if !changed {
				continue
			}
			encoded, err := json.Marshal(r.settings)
			if err != nil {
				return err
			}
			// updated_at stays: nobody changed a setting, and the panel shows
			// that date as when somebody did.
			if _, err := tx.ExecContext(ctx, `UPDATE plugins SET settings = $2 WHERE key = $1`, r.key, encoded); err != nil {
				return err
			}
		}
		return nil
	})
	return c, err
}

func (a *App) surveySecretColumn(ctx context.Context, col SecretColumn, write bool) (SecretCount, error) {
	c := SecretCount{Label: col.Label}
	store := a.storeOf(ctx)
	// Identifiers were checked at registration; they come from module code,
	// never from a request.
	rows, err := a.db.QueryContext(ctx, fmt.Sprintf(`SELECT %[2]s, count(*) FROM %[1]s WHERE %[2]s <> '' GROUP BY 1`,
		quoteIdent(col.Table), quoteIdent(col.Column)))
	if err != nil {
		return c, err
	}
	type value struct {
		stored string
		n      int
	}
	var values []value
	for rows.Next() {
		var v value
		if err := rows.Scan(&v.stored, &v.n); err != nil {
			rows.Close()
			return c, err
		}
		values = append(values, v)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return c, err
	}

	for _, v := range values {
		state, plain := a.secrets.classify(store, col.Field, v.stored)
		c.count(state, v.n, col.Field)
		if !write || (state != stateReadable && state != statePrevious) {
			continue
		}
		sealed, err := a.secrets.seal(store, col.Field, plain)
		if err != nil {
			return c, err
		}
		// Compare-and-set on the value itself: a row the module rewrote since
		// the read keeps what the module wrote, which it sealed already.
		if _, err := a.db.ExecContext(ctx, fmt.Sprintf(`UPDATE %[1]s SET %[2]s = $2 WHERE %[2]s = $1`,
			quoteIdent(col.Table), quoteIdent(col.Column)), v.stored, sealed); err != nil {
			return c, err
		}
	}
	return c, nil
}

// checkSecrets says whether this store's secrets are sealed, and what to do
// when they are not.
//
// Readable secrets with no key are a warning rather than a failure: that is
// how every install ran before D110, and the store is serving. With nothing
// stored there is nothing at risk, so it is ok — a warning on every fresh
// install would teach an operator to skip the line.
func (a *App) checkSecrets(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "secrets"}
	counts, err := a.surveySecrets(ctx, false)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "cannot read the stored secrets", err.Error()
		d.Hint = "check that the migrations are applied"
		return d
	}
	var all SecretCount
	for _, c := range counts {
		all.add(c)
	}
	where := strings.Join(all.Where, ", ")
	if len(all.Where) > 5 {
		where = strings.Join(all.Where[:5], ", ") + fmt.Sprintf(" and %d more", len(all.Where)-5)
	}
	keyHint := "set " + SecretsKeyEnv + " to 32 random bytes in base64 (`openssl rand -base64 32`), restart, " +
		"then run `gocommerce secrets seal`; keep a copy of the key away from this server, because losing it means entering every secret again"

	if a.secrets.current == nil {
		switch {
		case all.Unreadable > 0:
			d.Status = StatusWarn
			d.Detail = fmt.Sprintf("%d secret(s) are sealed and %s is not set, so they read as unset: %s", all.Unreadable, SecretsKeyEnv, where)
			d.Hint = "set " + SecretsKeyEnv + " to the key that sealed them and restart"
		case all.Readable > 0:
			d.Status = StatusWarn
			d.Detail = fmt.Sprintf("%d secret(s) are stored readable: %s is not set", all.Readable, SecretsKeyEnv)
			d.Hint = keyHint
		default:
			d.Status = StatusOK
			d.Detail = "no secrets stored; " + SecretsKeyEnv + " is not set, so any entered will be stored readable"
		}
		return d
	}

	sealed := fmt.Sprintf("%d secret(s) sealed under key %s", all.Current, a.secrets.current.id)
	switch {
	case all.Unreadable > 0:
		d.Status = StatusWarn
		d.Detail = fmt.Sprintf("%d secret(s) were sealed under a key this install does not have, and read as unset: %s", all.Unreadable, where)
		d.Hint = "put the key that sealed them in " + SecretsKeyPreviousEnv + " and run `gocommerce secrets seal`, or enter those secrets again"
	case all.Readable > 0:
		d.Status = StatusWarn
		d.Detail = fmt.Sprintf("%d secret(s) are still stored readable; %s", all.Readable, sealed)
		d.Hint = "run `gocommerce secrets seal`"
	case all.Previous > 0:
		d.Status = StatusOK
		d.Detail = fmt.Sprintf("%s; %d still under the previous key", sealed, all.Previous)
		d.Hint = "run `gocommerce secrets seal`, then unset " + SecretsKeyPreviousEnv
	default:
		d.Status = StatusOK
		d.Detail = sealed
	}
	return d
}
