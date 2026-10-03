package b2b

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Who changed a company's terms.
const (
	// ActorOperator is somebody signed in to the panel, or an API key.
	ActorOperator = "operator"
	// ActorToken is a static admin token: a script, with nobody behind it.
	ActorToken = "token"
	// ActorBuyer is one of the company's own admins.
	ActorBuyer = "buyer"
	// ActorSystem is the engine itself, or Go code calling the module with
	// nobody on the context.
	ActorSystem = "system"
)

// Whether a history entry is a company's first terms or a change to them.
const (
	HistoryCreated = "created"
	HistoryChanged = "changed"
)

// HistoryEntry is one term of a company changing, or being set when it was
// created. OldValue and NewValue are the values as the API takes them —
// credit_limit_minor is minor units, group_id an id, null "none" — and
// Currency is set on the money fields, so an amount is never read without one.
type HistoryEntry struct {
	ID         int64           `json:"id"`
	CompanyID  int64           `json:"company_id"`
	Field      string          `json:"field"`
	Action     string          `json:"action"`
	OldValue   json.RawMessage `json:"old_value"`
	NewValue   json.RawMessage `json:"new_value"`
	Currency   string          `json:"currency,omitempty"`
	ActorKind  string          `json:"actor_kind"`
	ActorID    *int64          `json:"actor_id"`
	ActorEmail string          `json:"actor_email"`
	ChangedAt  time.Time       `json:"changed_at"`
}

// moneyFields are the terms whose values are minor units of the store's
// currency.
var moneyFields = map[string]bool{"credit_limit_minor": true, "approval_threshold_minor": true}

// actor is who is changing a company's terms.
type actor struct {
	kind  string
	id    *int64
	email string
}

type actorKey struct{}

// withActor names who is acting, for a caller the context cannot otherwise
// describe: an admin request authenticated by a static token carries no
// operator, and without this it would read as the engine's own work.
func withActor(ctx context.Context, a actor) context.Context {
	return context.WithValue(ctx, actorKey{}, a)
}

// actorFrom reads who is acting. An operator signed in to the panel is on
// every admin request's context already; anything else is named by
// withActor, or is the engine.
func actorFrom(ctx context.Context) actor {
	if a, ok := ctx.Value(actorKey{}).(actor); ok {
		return a
	}
	if su := gocommerce.SuperuserFrom(ctx); su != nil {
		id := su.ID
		return actor{kind: ActorOperator, id: &id, email: su.Email}
	}
	return actor{kind: ActorSystem}
}

// terms are the parts of a company the history records. Name, code, tax
// number and notes are not terms: nothing a buyer is allowed rests on them.
type terms struct {
	Status            string
	GroupID           sql.NullInt64
	CreditLimit       sql.NullInt64
	NetDays           int
	ApprovalThreshold sql.NullInt64
	RequirePO         bool
	CatalogueID       sql.NullInt64
}

const termsColumns = `status, group_id, credit_limit_minor, net_days,
	approval_threshold_minor, require_po, catalogue_id`

func readTerms(ctx context.Context, tx *sql.Tx, companyID int64, lock bool) (terms, error) {
	var t terms
	q := `SELECT ` + termsColumns + ` FROM b2b_companies WHERE id = $1`
	if lock {
		q += ` FOR UPDATE`
	}
	err := tx.QueryRowContext(ctx, q, companyID).Scan(&t.Status, &t.GroupID, &t.CreditLimit,
		&t.NetDays, &t.ApprovalThreshold, &t.RequirePO, &t.CatalogueID)
	return t, err
}

// fields lists the terms in the order a person reads them, as JSON values.
func (t terms) fields() []struct {
	name  string
	value any
} {
	nullable := func(v sql.NullInt64) any {
		if !v.Valid {
			return nil
		}
		return v.Int64
	}
	return []struct {
		name  string
		value any
	}{
		{"status", t.Status},
		{"credit_limit_minor", nullable(t.CreditLimit)},
		{"net_days", t.NetDays},
		{"approval_threshold_minor", nullable(t.ApprovalThreshold)},
		{"require_po", t.RequirePO},
		{"group_id", nullable(t.GroupID)},
		{"catalogue_id", nullable(t.CatalogueID)},
	}
}

// recordTerms writes what moved between two readings of a company's terms, in
// the caller's transaction. before nil is a company being created: every term
// is written, so "what were its terms when it was opened" has an answer.
func recordTerms(ctx context.Context, tx *sql.Tx, companyID int64, before *terms, after terms) error {
	who := actorFrom(ctx)
	next := after.fields()
	var prev []struct {
		name  string
		value any
	}
	if before != nil {
		prev = before.fields()
	}
	for i, f := range next {
		newJSON, err := json.Marshal(f.value)
		if err != nil {
			return err
		}
		action := HistoryCreated
		var oldJSON []byte
		if before != nil {
			if oldJSON, err = json.Marshal(prev[i].value); err != nil {
				return err
			}
			if string(oldJSON) == string(newJSON) {
				continue
			}
			action = HistoryChanged
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO b2b_company_history (company_id, field, action, old_value, new_value,
			                                 actor_kind, actor_id, actor_email)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			companyID, f.name, action, nullJSON(oldJSON), newJSON, who.kind, who.id, who.email); err != nil {
			return err
		}
	}
	return nil
}

// nullJSON is SQL NULL for no value at all, which is not the JSON null a
// cleared limit is.
func nullJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return b
}

// History is a company's terms as they changed, newest first.
func (m *Module) History(ctx context.Context, companyID int64, limit, offset int) ([]*HistoryEntry, int, error) {
	if _, err := m.Company(ctx, companyID); err != nil {
		return nil, 0, err
	}
	var total int
	if err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM b2b_company_history WHERE company_id = $1`, companyID).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, company_id, field, action, old_value, new_value,
		       actor_kind, actor_id, actor_email, changed_at
		FROM b2b_company_history WHERE company_id = $1
		ORDER BY id DESC LIMIT $2 OFFSET $3`, companyID, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*HistoryEntry{}
	for rows.Next() {
		e := &HistoryEntry{}
		var oldV, newV []byte
		var actorID sql.NullInt64
		if err := rows.Scan(&e.ID, &e.CompanyID, &e.Field, &e.Action, &oldV, &newV,
			&e.ActorKind, &actorID, &e.ActorEmail, &e.ChangedAt); err != nil {
			return nil, 0, err
		}
		e.OldValue, e.NewValue = jsonOrNull(oldV), jsonOrNull(newV)
		if actorID.Valid {
			e.ActorID = &actorID.Int64
		}
		if moneyFields[e.Field] {
			e.Currency = m.app.Config().Currency
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

func jsonOrNull(b []byte) json.RawMessage {
	if len(b) == 0 {
		return json.RawMessage("null")
	}
	return json.RawMessage(b)
}
