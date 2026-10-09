package cartrecovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Settings is the automation screen: one sequence for baskets that reached
// checkout and one for baskets that did not, and where the link lands.
type Settings struct {
	Checkout Automation `json:"checkout"`
	Cart     Automation `json:"cart"`
	// StorefrontURL overrides Config.StorefrontURL when set.
	StorefrontURL string `json:"storefront_url"`
}

// Automation is one sequence.
type Automation struct {
	Enabled bool `json:"enabled"`
	// AbandonAfterMinutes is how long a basket sits untouched before it counts.
	AbandonAfterMinutes int    `json:"abandon_after_minutes"`
	Steps               []Step `json:"steps"`
}

// Step is "wait, then do". WaitMinutes runs from the step before it — the
// first from the moment the basket was marked abandoned — because that is how
// the screen reads a sequence top to bottom, and an absolute offset would make
// inserting a step silently move every one after it.
type Step struct {
	WaitMinutes int    `json:"wait_minutes"`
	Action      string `json:"action"`
	Template    string `json:"template"`
}

// Action is something a step can do. Only email is available: a basket carries
// an address and no phone number, so an SMS step would be a step that can
// never send. The others are listed so the screen is built around a list of
// actions rather than around email, and says why each is not there yet.
type Action struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

const actionEmail = "send_email"

var actions = []Action{
	{Key: actionEmail, Label: "Send email", Available: true},
	{Key: "send_sms", Label: "Send SMS", Reason: "A basket carries no phone number"},
	{Key: "send_whatsapp", Label: "Send WhatsApp", Reason: "A basket carries no phone number"},
	{Key: "create_discount", Label: "Create discount", Reason: "Not built yet"},
	{Key: "notify_staff", Label: "Notify staff", Reason: "Not built yet"},
}

// DefaultSettings is what a store that never saved the screen runs: the
// checkout sequence a store of this kind usually starts from, and a shorter
// one for a basket that never reached checkout.
func DefaultSettings() Settings {
	return Settings{
		Checkout: Automation{
			Enabled: true, AbandonAfterMinutes: 10,
			Steps: []Step{
				{WaitMinutes: 60, Action: actionEmail, Template: templateFirst},
				{WaitMinutes: 20 * 60, Action: actionEmail, Template: templateSecond},
				{WaitMinutes: 48 * 60, Action: actionEmail, Template: templateLast},
			},
		},
		Cart: Automation{
			Enabled: true, AbandonAfterMinutes: 30,
			Steps: []Step{
				{WaitMinutes: 2 * 60, Action: actionEmail, Template: templateFirst},
				{WaitMinutes: 24 * 60, Action: actionEmail, Template: templateSecond},
			},
		},
	}
}

// Limits on what the screen may save. A week of idleness is the most a
// threshold can usefully mean against a thirty-day cart TTL, and thirty days of
// waiting is a step that would arrive after the basket is gone.
const (
	maxSteps        = 5
	maxThresholdMin = 7 * 24 * 60
	maxWaitMin      = 30 * 24 * 60
)

func (s Settings) automation(kind string) Automation {
	if kind == kindCheckout {
		return s.Checkout
	}
	return s.Cart
}

func (a Automation) threshold() time.Duration {
	return time.Duration(a.AbandonAfterMinutes) * time.Minute
}

func validate(s *Settings) error {
	clean, err := cleanStorefrontURL(s.StorefrontURL)
	if err != nil {
		return err
	}
	s.StorefrontURL = clean
	for _, side := range []struct {
		name string
		a    *Automation
	}{{"checkout", &s.Checkout}, {"cart", &s.Cart}} {
		a := side.a
		if a.AbandonAfterMinutes < 1 || a.AbandonAfterMinutes > maxThresholdMin {
			return gocommerce.Validationf("%s.abandon_after_minutes must be between 1 and %d", side.name, maxThresholdMin)
		}
		if len(a.Steps) > maxSteps {
			return gocommerce.Validationf("%s: at most %d steps", side.name, maxSteps)
		}
		if a.Steps == nil {
			a.Steps = []Step{}
		}
		for i, st := range a.Steps {
			if st.WaitMinutes < 1 || st.WaitMinutes > maxWaitMin {
				return gocommerce.Validationf("%s step %d: wait_minutes must be between 1 and %d", side.name, i+1, maxWaitMin)
			}
			if st.Action != actionEmail {
				return gocommerce.Validationf("%s step %d: %q is not an action a recovery step can take yet", side.name, i+1, st.Action)
			}
			if !slices.Contains(templateEvents, st.Template) {
				return gocommerce.Validationf("%s step %d: unknown template %q", side.name, i+1, st.Template)
			}
		}
	}
	return nil
}

// settingsRow is the one row: what the screen saved, and when this module was
// installed.
type settingsRow struct {
	Settings    Settings
	Customized  bool
	InstalledAt time.Time
	UpdatedAt   *time.Time
	UpdatedBy   string
}

func (m *Module) loadSettings(ctx context.Context) (*settingsRow, error) {
	var raw []byte
	var updatedAt sql.NullTime
	var updatedBy sql.NullString
	row := &settingsRow{}
	err := m.app.DB().QueryRowContext(ctx, `
		SELECT installed_at, settings, updated_at, updated_by
		FROM cart_recovery_settings WHERE id = 1`).
		Scan(&row.InstalledAt, &raw, &updatedAt, &updatedBy)
	if err != nil {
		return nil, fmt.Errorf("cart-recovery: read settings: %w", err)
	}
	row.Settings = DefaultSettings()
	if raw != nil {
		var saved Settings
		if err := json.Unmarshal(raw, &saved); err != nil {
			return nil, fmt.Errorf("cart-recovery: decode settings: %w", err)
		}
		row.Settings = saved
		row.Customized = true
	}
	if updatedAt.Valid {
		t := updatedAt.Time
		row.UpdatedAt = &t
	}
	row.UpdatedBy = updatedBy.String
	return row, nil
}

// storefront is where a link lands: the screen's address, else the Config's.
func (m *Module) storefront(s Settings) string {
	if s.StorefrontURL != "" {
		return s.StorefrontURL
	}
	return m.cfg.StorefrontURL
}

// saveSettings stores the screen and re-plans every open record in the same
// transaction, so "I switched it off" is true the moment the save returns
// rather than on the next pass — a reminder already claimed by the sender is
// the one exception, and the sender re-reads the settings before it sends.
func (m *Module) saveSettings(ctx context.Context, s Settings, by string) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tx, err := m.app.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var installedAt time.Time
	if err := tx.QueryRowContext(ctx, `
		UPDATE cart_recovery_settings
		SET settings = $1, updated_at = now(), updated_by = $2
		WHERE id = 1
		RETURNING installed_at`, raw, by).Scan(&installedAt); err != nil {
		return err
	}

	for _, kind := range []string{kindCheckout, kindCart} {
		a := s.automation(kind)
		if !a.Enabled || len(a.Steps) == 0 {
			reason := holdAutomationOff
			if a.Enabled {
				reason = holdNoSteps
			}
			if _, err := tx.ExecContext(ctx, `
				UPDATE cart_recovery_abandonments
				SET next_step_at = NULL, hold_reason = $2,
				    status = CASE WHEN status = 'scheduled' THEN 'abandoned' ELSE status END,
				    updated_at = now()
				WHERE kind = $1 AND status IN ('abandoned', 'scheduled', 'contacted')
				  AND next_step_at IS NOT NULL`, kind, reason); err != nil {
				return err
			}
			continue
		}
		// Switched on, or its steps changed: a record held only because the
		// automation was off, or that ran out of steps the new sequence now
		// has, gets its next step planned from its last message — or from its
		// abandonment if nothing was sent. A step due in the past goes out on
		// the next pass, which is what turning a sequence on means. A record
		// held for a missing or failing email provider is retried too: saving
		// this screen is the operator saying the setup is fixed.
		waits := make([]int, len(a.Steps))
		for i, st := range a.Steps {
			waits[i] = st.WaitMinutes
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE cart_recovery_abandonments
			SET next_step_at = coalesce(last_sent_at, abandoned_at)
			                   + make_interval(mins => ($2::int[])[steps_sent + 1]),
			    hold_reason = NULL,
			    status = CASE WHEN status = 'abandoned' THEN 'scheduled' ELSE status END,
			    updated_at = now()
			WHERE kind = $1 AND status IN ('abandoned', 'scheduled', 'contacted')
			  AND NOT history AND email IS NOT NULL
			  AND steps_sent < $3
			  AND (hold_reason IS NULL OR hold_reason IN
			       ('automation_off', 'no_steps', 'sequence_done', 'no_email_provider', 'send_failed'))`,
			kind, pqIntArray(waits), len(a.Steps)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE cart_recovery_abandonments
			SET next_step_at = NULL, hold_reason = 'sequence_done',
			    status = CASE WHEN status = 'scheduled' THEN 'abandoned' ELSE status END,
			    updated_at = now()
			WHERE kind = $1 AND status IN ('abandoned', 'scheduled', 'contacted')
			  AND steps_sent >= $2 AND next_step_at IS NOT NULL`, kind, len(a.Steps)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// pqIntArray renders a Postgres int[] literal. pgx's stdlib driver takes a
// []int64 as an array, but a literal keeps this independent of which driver a
// store's main() registered.
func pqIntArray(xs []int) string {
	b := []byte{'{'}
	for i, x := range xs {
		if i > 0 {
			b = append(b, ',')
		}
		b = fmt.Appendf(b, "%d", x)
	}
	return string(append(b, '}'))
}
