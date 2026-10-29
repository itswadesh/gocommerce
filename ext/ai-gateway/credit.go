package aigateway

import (
	"context"
	"database/sql"
	"errors"
	"math"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// DefaultFreeTrialMinor is the free AI trial a store gets before an operator
// tops it up: minor units of the store's currency, so 500 is 5.00 in a
// two-decimal currency. It is a placeholder, not a decision about money: the
// platform operator sets the real figure through Config.FreeTrialMinor, or per
// store on the AI usage screen.
const DefaultFreeTrialMinor = 500

// freeTrialMicro is the configured free trial in micro-minor, the unit the
// gateway records cost in.
func (m *Module) freeTrialMicro() int64 {
	v := m.cfg.FreeTrialMinor
	if v <= 0 {
		v = DefaultFreeTrialMinor
	}
	return v * microPerMinor
}

// ensureCredit makes sure ctx's store has a credit row, seeded with the free
// trial. It is idempotent: a row that already exists is left as it is, so a
// later change to the default free trial does not reset a store that has
// started using AI.
func (m *Module) ensureCredit(ctx context.Context) error {
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO ai_credits (free_allowance_micro, granted_micro, consumed_micro, reserved_micro, currency)
		VALUES ($1, 0, 0, 0, $2)
		ON CONFLICT (store_id) DO NOTHING`,
		m.freeTrialMicro(), m.app.Currency(ctx))
	return err
}

// reserveCredit holds amount against ctx's store's available credit, or
// refuses. Available is the free allowance plus what was granted, less what is
// spent and already held. One conditional UPDATE under the row lock, so two
// runs racing for the last of the credit cannot both reserve it: the second
// waits on the row and re-evaluates its WHERE against what the first left.
func (m *Module) reserveCredit(ctx context.Context, amount int64) (bool, error) {
	if err := m.ensureCredit(ctx); err != nil {
		return false, err
	}
	var held int64
	err := m.db.QueryRowContext(ctx, `
		UPDATE ai_credits
		   SET reserved_micro = reserved_micro + $1, updated_at = now()
		 WHERE free_allowance_micro + granted_micro - consumed_micro - reserved_micro >= $1
		RETURNING reserved_micro`, amount).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// settleCredit turns a reservation into what the call cost: it releases the
// held amount and adds the real cost to what is consumed. Called on every way
// out of a call, so a run that reserved but never spent (a credit hold
// released when the budget refused, a call that never left) consumes nothing.
func (m *Module) settleCredit(ctx context.Context, reserved, cost int64) error {
	_, err := m.db.ExecContext(ctx, `
		UPDATE ai_credits
		   SET reserved_micro = greatest(reserved_micro - $1, 0),
		       consumed_micro = consumed_micro + $2,
		       updated_at = now()`, reserved, cost)
	return err
}

// releaseCredit gives back a hold that was never spent, on a context of its
// own so a cancelled run still frees what it reserved.
func (m *Module) releaseCredit(ctx context.Context, reserved int64) {
	if reserved == 0 {
		return
	}
	if err := m.settleCredit(context.WithoutCancel(ctx), reserved, 0); err != nil {
		m.log.Error("ai credit hold could not be released", "error", err)
	}
}

// Credit is a store's AI credit as the panel shows it: the free trial and the
// topped-up balance, what is spent and held, and what is left. Amounts are
// micro-minor; the panel formats them (rule 3: the API returns no formatted
// string).
type Credit struct {
	Currency                string `json:"currency"`
	FreeAllowanceMicroMinor int64  `json:"free_allowance_micro_minor"`
	GrantedMicroMinor       int64  `json:"granted_micro_minor"`
	ConsumedMicroMinor      int64  `json:"consumed_micro_minor"`
	ReservedMicroMinor      int64  `json:"reserved_micro_minor"`
	// FreeRemainingMicroMinor is the free trial not yet spent; a call draws it
	// before the balance.
	FreeRemainingMicroMinor int64 `json:"free_remaining_micro_minor"`
	// BalanceRemainingMicroMinor is the topped-up balance not yet spent.
	BalanceRemainingMicroMinor int64 `json:"balance_remaining_micro_minor"`
	// AvailableMicroMinor is what a call could draw now, free plus balance less
	// what is held for calls in flight.
	AvailableMicroMinor int64 `json:"available_micro_minor"`
	Exhausted           bool  `json:"exhausted"`
}

// Credit is ctx's store's AI credit. A store with no row yet has its full,
// unspent free trial.
func (m *Module) Credit(ctx context.Context) (*Credit, error) {
	c := &Credit{Currency: m.app.Currency(ctx), FreeAllowanceMicroMinor: m.freeTrialMicro()}
	err := m.db.QueryRowContext(ctx, `
		SELECT free_allowance_micro, granted_micro, consumed_micro, reserved_micro
		FROM ai_credits`).Scan(&c.FreeAllowanceMicroMinor, &c.GrantedMicroMinor, &c.ConsumedMicroMinor, &c.ReservedMicroMinor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	// Consumption draws the free allowance first, then the balance; the split
	// is arithmetic over one consumed counter.
	c.FreeRemainingMicroMinor = max(0, c.FreeAllowanceMicroMinor-c.ConsumedMicroMinor)
	// Floored at zero: an operator may lower the free allowance below what a
	// store has already spent, which would otherwise show a negative balance.
	c.BalanceRemainingMicroMinor = max(0, c.GrantedMicroMinor-max(0, c.ConsumedMicroMinor-c.FreeAllowanceMicroMinor))
	c.AvailableMicroMinor = c.FreeAllowanceMicroMinor + c.GrantedMicroMinor - c.ConsumedMicroMinor - c.ReservedMicroMinor
	c.Exhausted = c.AvailableMicroMinor <= 0
	return c, nil
}

// maxGrantMinor bounds a single grant so amountMinor*microPerMinor cannot
// overflow an int64.
const maxGrantMinor = math.MaxInt64 / microPerMinor

// Grant tops up ctx's store's balance by amountMinor minor units and records
// who did it. The operator does this by hand; there is no payment gateway yet.
func (m *Module) Grant(ctx context.Context, amountMinor int64, note string) (*Credit, error) {
	switch {
	case amountMinor <= 0:
		return nil, gocommerce.Validationf("the amount to grant is more than zero minor units")
	case amountMinor > maxGrantMinor:
		return nil, gocommerce.Validationf("the amount to grant is too large")
	}
	if err := m.ensureCredit(ctx); err != nil {
		return nil, err
	}
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE ai_credits SET granted_micro = granted_micro + $1, updated_at = now()`,
			amountMinor*microPerMinor); err != nil {
			return err
		}
		return m.writeLedger(ctx, tx, "grant", amountMinor, note)
	})
	if err != nil {
		return nil, err
	}
	return m.Credit(ctx)
}

// SetFreeAllowance sets ctx's store's free trial to amountMinor minor units
// and records who did it. It is the trial's size, not an addition: a store
// that has spent part of its trial keeps what it has spent.
func (m *Module) SetFreeAllowance(ctx context.Context, amountMinor int64, note string) (*Credit, error) {
	switch {
	case amountMinor < 0:
		return nil, gocommerce.Validationf("the free allowance is zero or more minor units")
	case amountMinor > maxGrantMinor:
		return nil, gocommerce.Validationf("the free allowance is too large")
	}
	if err := m.ensureCredit(ctx); err != nil {
		return nil, err
	}
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			UPDATE ai_credits SET free_allowance_micro = $1, updated_at = now()`,
			amountMinor*microPerMinor); err != nil {
			return err
		}
		return m.writeLedger(ctx, tx, "free_allowance", amountMinor, note)
	})
	if err != nil {
		return nil, err
	}
	return m.Credit(ctx)
}

func (m *Module) writeLedger(ctx context.Context, tx *sql.Tx, kind string, amountMinor int64, note string) error {
	su := gocommerce.SuperuserFrom(ctx)
	var id *int64
	var email string
	if su != nil {
		id, email = &su.ID, su.Email
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO ai_credit_ledger (kind, amount_minor, actor_id, actor_email, note)
		VALUES ($1, $2, $3, $4, $5)`, kind, amountMinor, id, email, note)
	return err
}

// CreditEntry is one change to a store's credit, for the operator's record.
type CreditEntry struct {
	ID          int64  `json:"id"`
	Kind        string `json:"kind"`
	AmountMinor int64  `json:"amount_minor"`
	ActorEmail  string `json:"actor_email,omitempty"`
	Note        string `json:"note,omitempty"`
	CreatedAt   string `json:"created_at"`
}

// CreditLedger lists ctx's store's credit changes, newest first.
func (m *Module) CreditLedger(ctx context.Context, limit, offset int) ([]CreditEntry, int, error) {
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM ai_credit_ledger`).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, kind, amount_minor, actor_email, note, to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS"Z"')
		FROM ai_credit_ledger ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []CreditEntry{}
	for rows.Next() {
		var e CreditEntry
		if err := rows.Scan(&e.ID, &e.Kind, &e.AmountMinor, &e.ActorEmail, &e.Note, &e.CreatedAt); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	return out, total, rows.Err()
}

// creditReason is what a run says when the store has no credit left: the
// owner's copy, told apart by whether the store has ever been topped up.
func (m *Module) creditReason(ctx context.Context) string {
	c, err := m.Credit(ctx)
	if err != nil || c.GrantedMicroMinor == 0 {
		return "You've used your free AI trial — top up to keep using AI"
	}
	return "You're out of AI credit — top up to keep using AI"
}
