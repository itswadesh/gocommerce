package aigateway

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// A month's cap is minor units; what is held and spent is millionths of
// them, because that is what tokens times a price per million tokens comes
// to without dividing.
const microPerMinor = 1_000_000

// monthOf is the first day of t's calendar month in UTC. One month is one
// row; a run that crosses midnight at the month's end settles into the month
// it reserved in.
func monthOf(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// reserve holds amount against the month's cap, or refuses. One statement,
// so two runs racing for the last of a month's money cannot both have it:
// the conflicting upsert waits on the row and then evaluates its WHERE
// against what the winner left. The cap is the store's setting as it stands,
// so a raised cap counts at once.
func (m *Module) reserve(ctx context.Context, month time.Time, capMinor, amount int64) (bool, error) {
	if amount > capMinor*microPerMinor {
		return false, nil
	}
	var held int64
	err := m.db.QueryRowContext(ctx, `
		INSERT INTO ai_budgets (month, cap_minor, reserved_micro)
		VALUES ($1::date, $2, $3)
		ON CONFLICT (store_id, month) DO UPDATE
		   SET cap_minor = EXCLUDED.cap_minor,
		       reserved_micro = ai_budgets.reserved_micro + EXCLUDED.reserved_micro,
		       updated_at = now()
		 WHERE ai_budgets.spent_micro + ai_budgets.reserved_micro + EXCLUDED.reserved_micro
		       <= EXCLUDED.cap_minor * 1000000
		RETURNING reserved_micro`,
		month.Format(time.DateOnly), capMinor, amount).Scan(&held)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// settle turns a reservation into what the call cost. The real cost is
// charged even when it passes the reservation, because the money was spent;
// with a ceiling priced at the dearest input rate that only happens when a
// provider counts more tokens than the request was estimated at.
func (m *Module) settle(ctx context.Context, month time.Time, reserved, cost int64) error {
	_, err := m.db.ExecContext(ctx, `
		UPDATE ai_budgets
		   SET reserved_micro = greatest(reserved_micro - $2, 0),
		       spent_micro = spent_micro + $3,
		       updated_at = now()
		 WHERE month = $1::date`,
		month.Format(time.DateOnly), reserved, cost)
	return err
}

// Budget is a store's month as the usage screen heads it: the cap, what is
// spent and held against it, and whether the store can make calls at all.
type Budget struct {
	Month    string `json:"month"`
	Currency string `json:"currency"`
	// CapMinor is the setting, in minor units; nil when none is set.
	CapMinor           *int64 `json:"cap_minor"`
	SpentMicroMinor    int64  `json:"spent_micro_minor"`
	ReservedMicroMinor int64  `json:"reserved_micro_minor"`
	// Ready is whether a call could be made now; Reason says why not.
	Ready       bool   `json:"ready"`
	Reason      string `json:"reason,omitempty"`
	Enabled     bool   `json:"enabled"`
	Provider    string `json:"provider"`
	SmallModel  string `json:"small_model"`
	StrongModel string `json:"strong_model"`
	// Priced is whether each model has the input and output price a call
	// needs.
	SmallPriced  bool   `json:"small_priced"`
	StrongPriced bool   `json:"strong_priced"`
	Policy       Policy `json:"policy"`
}

// Budget is ctx's store's current month.
func (m *Module) Budget(ctx context.Context) (*Budget, error) {
	st, err := m.settings(ctx)
	if err != nil {
		return nil, err
	}
	month := monthOf(time.Now())
	b := &Budget{
		Month: month.Format(time.DateOnly), Currency: m.app.Currency(ctx),
		CapMinor: st.capMinor, Enabled: st.enabled, Provider: st.provider,
		SmallModel: st.models[TierSmall], StrongModel: st.models[TierStrong],
		SmallPriced: st.prices[TierSmall].complete, StrongPriced: st.prices[TierStrong].complete,
		Policy: st.policy,
	}
	if err := st.ready(); err != nil {
		b.Reason = err.Error()
	} else {
		b.Ready = true
	}
	err = m.db.QueryRowContext(ctx, `
		SELECT spent_micro, reserved_micro FROM ai_budgets WHERE month = $1::date`,
		b.Month).Scan(&b.SpentMicroMinor, &b.ReservedMicroMinor)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	return b, nil
}
