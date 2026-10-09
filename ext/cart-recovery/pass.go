package cartrecovery

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	kindCart     = "cart"
	kindCheckout = "checkout"

	statusAbandoned  = "abandoned"
	statusScheduled  = "scheduled"
	statusContacted  = "contacted"
	statusRecovered  = "recovered"
	statusSuppressed = "suppressed"
	statusExpired    = "expired"

	holdNoContact          = "no_contact"
	holdAutomationOff      = "automation_off"
	holdNoSteps            = "no_steps"
	holdBeforeInstall      = "before_install"
	holdNothingPurchasable = "nothing_purchasable"
	holdNoProvider         = "no_email_provider"
	holdSendFailed         = "send_failed"
	holdSequenceDone       = "sequence_done"
)

// Bounds on one pass. Each is a batch rather than a backlog, so a store that
// was down for a day catches up over a few minutes instead of holding the
// ticker for as long as the backlog takes.
const (
	detectBatch  = 500
	refreshBatch = 500
	sendBatch    = 100
	purgeBatch   = 500
	// claimWindow is how long a claimed step is hidden from other senders. A
	// process that dies mid-send releases it when this runs out.
	claimWindow = 5 * time.Minute
	// maxAttempts is how often one step is tried before the sequence stops
	// with send_failed rather than retrying a broken provider forever.
	maxAttempts = 3
)

// PassResult is what one pass did.
type PassResult struct {
	Recorded int `json:"recorded"`
	Resumed  int `json:"resumed"`
	Expired  int `json:"expired"`
	Sent     int `json:"sent"`
	Held     int `json:"held"`
	Failed   int `json:"failed"`
	Purged   int `json:"purged"`
}

// Pass runs the whole cycle once: record newly idle baskets, notice baskets
// that moved or vanished, send what is due, purge what has expired. Exported
// for the reason the engine exports DrainOutbox — a test, or an operator in a
// hurry, should not have to wait out a ticker.
func (m *Module) Pass(ctx context.Context) (PassResult, error) {
	var res PassResult
	set, err := m.loadSettings(ctx)
	if err != nil {
		return res, err
	}
	if res.Recorded, err = m.detect(ctx, set); err != nil {
		return res, fmt.Errorf("cart-recovery: detect: %w", err)
	}
	if res.Resumed, res.Expired, err = m.refresh(ctx, set); err != nil {
		return res, fmt.Errorf("cart-recovery: refresh: %w", err)
	}
	if err = m.sendDue(ctx, set, &res); err != nil {
		return res, fmt.Errorf("cart-recovery: send: %w", err)
	}
	if res.Purged, err = m.purge(ctx); err != nil {
		return res, fmt.Errorf("cart-recovery: purge: %w", err)
	}
	return res, nil
}

// snapLine is one line as it was when the basket was given up on.
type snapLine struct {
	VariantID      int64  `json:"variant_id"`
	ProductID      int64  `json:"product_id"`
	SKU            string `json:"sku"`
	Title          string `json:"title"`
	VariantLabel   string `json:"variant_label,omitempty"`
	Quantity       int    `json:"quantity"`
	UnitPriceMinor int64  `json:"unit_price_minor"`
	TotalMinor     int64  `json:"total_minor"`
}

func snapshot(lines []gocommerce.CartLine) []snapLine {
	out := make([]snapLine, 0, len(lines))
	for _, l := range lines {
		out = append(out, snapLine{
			VariantID: l.VariantID, ProductID: l.ProductID, SKU: l.SKU, Title: l.Title,
			VariantLabel: l.VariantLabel, Quantity: l.Quantity,
			UnitPriceMinor: l.UnitPrice.AmountMinor, TotalMinor: l.Total.AmountMinor,
		})
	}
	return out
}

// contactOf is the address a message goes to. The typed one first, because it
// is the one the shopper gave this basket; a signed-in account's verified
// address is the fallback that makes a basket that never reached checkout
// reachable at all.
func contactOf(d *gocommerce.CartDetail) string {
	if d.Email != "" {
		return d.Email
	}
	return d.VerifiedEmail
}

// kindOf: on a guest storefront an address arrives on a basket at checkout's
// first step and nowhere else, so its presence is the line between a basket
// and a checkout.
func kindOf(d *gocommerce.CartDetail) string {
	if d.Email != "" {
		return kindCheckout
	}
	return kindCart
}

func newLinkToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// detect records every basket that has sat untouched past its threshold and
// has no record yet. It records baskets nobody can be written to as well: the
// screen counts what was left behind, not only what can be chased.
func (m *Module) detect(ctx context.Context, set *settingsRow) (int, error) {
	s := set.Settings
	rows, err := m.app.DB().QueryContext(ctx, `
		SELECT c.id
		FROM carts c
		WHERE c.status IN ('open', 'abandoned')
		  AND c.updated_at <= now() - make_interval(mins =>
		        CASE WHEN coalesce(c.email, '') <> '' THEN $1::int ELSE $2::int END)
		  AND EXISTS (SELECT 1 FROM cart_line_items l WHERE l.cart_id = c.id)
		  AND NOT EXISTS (SELECT 1 FROM cart_recovery_abandonments a WHERE a.cart_id = c.id)
		ORDER BY c.updated_at
		LIMIT $3`,
		s.Checkout.AbandonAfterMinutes, s.Cart.AbandonAfterMinutes, detectBatch)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	recorded := 0
	for _, id := range ids {
		ok, err := m.record(ctx, set, id)
		if err != nil {
			m.log.Error("cart-recovery: record abandonment", "cart", id, "error", err)
			continue
		}
		if ok {
			recorded++
		}
	}
	return recorded, nil
}

func (m *Module) record(ctx context.Context, set *settingsRow, cartID int64) (bool, error) {
	d, err := m.app.Cart().Get(ctx, cartID)
	if errors.Is(err, gocommerce.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if len(d.Lines) == 0 {
		return false, nil
	}
	kind := kindOf(d)
	contact := contactOf(d)
	history := d.UpdatedAt.Before(set.InstalledAt)
	a := set.Settings.automation(kind)

	status, hold := statusScheduled, ""
	switch {
	case history:
		status, hold = statusAbandoned, holdBeforeInstall
	case contact == "":
		status, hold = statusAbandoned, holdNoContact
	case !a.Enabled:
		status, hold = statusAbandoned, holdAutomationOff
	case len(a.Steps) == 0:
		status, hold = statusAbandoned, holdNoSteps
	}
	firstWait := 0
	if status == statusScheduled {
		firstWait = a.Steps[0].WaitMinutes
	}

	lines, err := json.Marshal(snapshot(d.Lines))
	if err != nil {
		return false, err
	}
	tok, err := newLinkToken()
	if err != nil {
		return false, err
	}

	tx, err := m.app.DB().BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRowContext(ctx, `
		INSERT INTO cart_recovery_abandonments
		    (cart_id, kind, status, email, currency, item_count, subtotal_minor, lines,
		     discount_code, cart_created_at, last_active_at, history, hold_reason,
		     next_step_at, link_token)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13,
		        CASE WHEN $14::int > 0 THEN now() + make_interval(mins => $14::int) END, $15)
		ON CONFLICT (cart_id) DO NOTHING
		RETURNING id`,
		cartID, kind, status, nullString(contact), d.Currency, d.ItemCount, d.Subtotal.AmountMinor,
		lines, nullString(d.DiscountCode), d.CreatedAt, d.UpdatedAt, history, nullString(hold),
		firstWait, tok).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Another instance recorded it first.
		return false, nil
	}
	if err != nil {
		return false, err
	}
	idle := int(time.Since(d.UpdatedAt).Minutes())
	if err := addEvent(ctx, tx, id, "abandoned", map[string]any{
		"kind": kind, "idle_minutes": idle, "hold_reason": hold,
	}, actorSystem); err != nil {
		return false, err
	}
	if status == statusScheduled {
		if err := addEvent(ctx, tx, id, "scheduled", map[string]any{
			"step": 1, "template": a.Steps[0].Template, "wait_minutes": firstWait,
		}, actorSystem); err != nil {
			return false, err
		}
	}
	return true, tx.Commit()
}

// refresh notices two things about baskets already recorded: the shopper came
// back and touched it, or core's retention purge deleted it.
func (m *Module) refresh(ctx context.Context, set *settingsRow) (resumed, expired int, err error) {
	rows, err := m.app.DB().QueryContext(ctx, `
		SELECT a.id, a.status, c.id IS NULL
		FROM cart_recovery_abandonments a
		LEFT JOIN carts c ON c.id = a.cart_id
		WHERE a.status IN ('abandoned', 'scheduled', 'contacted', 'suppressed')
		  AND a.expired_at IS NULL
		  AND (c.id IS NULL OR (c.status <> 'converted' AND c.updated_at > a.last_active_at))
		ORDER BY a.id
		LIMIT $1`, refreshBatch)
	if err != nil {
		return 0, 0, err
	}
	type hit struct {
		id     int64
		status string
		gone   bool
	}
	var hits []hit
	for rows.Next() {
		var h hit
		if err := rows.Scan(&h.id, &h.status, &h.gone); err != nil {
			rows.Close()
			return 0, 0, err
		}
		hits = append(hits, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	for _, h := range hits {
		if h.gone {
			if err := m.expire(ctx, h.id); err != nil {
				return resumed, expired, err
			}
			expired++
			continue
		}
		ok, err := m.resume(ctx, set, h.id)
		if err != nil {
			m.log.Error("cart-recovery: refresh abandonment", "abandonment", h.id, "error", err)
			continue
		}
		if ok {
			resumed++
		}
	}
	return resumed, expired, nil
}

// expire records that the basket no longer exists. A suppressed record keeps
// its status — the operator's reason is the more useful thing to show — and
// only gains the stamp the purge clocks from.
func (m *Module) expire(ctx context.Context, id int64) error {
	tx, err := m.app.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE cart_recovery_abandonments
		SET status = CASE WHEN status = 'suppressed' THEN status ELSE 'expired' END,
		    expired_at = now(), next_step_at = NULL, claimed_until = NULL, updated_at = now()
		WHERE id = $1 AND expired_at IS NULL AND status <> 'recovered'`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	if err := addEvent(ctx, tx, id, "expired", map[string]any{}, actorSystem); err != nil {
		return err
	}
	return tx.Commit()
}

// resume takes in what the shopper did after the basket was marked. The record
// keeps its status — the basket is still not an order — and its snapshot and
// clock move to the latest visit, so the next reminder waits for this visit to
// go quiet too. A record that was held only for want of an address, or of
// anything buyable, gets its next step planned again now that may have
// changed.
func (m *Module) resume(ctx context.Context, set *settingsRow, id int64) (bool, error) {
	var cartID int64
	var status string
	var hold sql.NullString
	var stepsSent int
	var history bool
	if err := m.app.DB().QueryRowContext(ctx, `
		SELECT cart_id, status, hold_reason, steps_sent, history
		FROM cart_recovery_abandonments WHERE id = $1`, id).
		Scan(&cartID, &status, &hold, &stepsSent, &history); err != nil {
		return false, err
	}
	d, err := m.app.Cart().Get(ctx, cartID)
	if errors.Is(err, gocommerce.ErrNotFound) {
		return false, m.expire(ctx, id)
	}
	if err != nil {
		return false, err
	}
	kind := kindOf(d)
	contact := contactOf(d)
	a := set.Settings.automation(kind)

	replan := !history && (status == statusAbandoned || status == statusContacted) &&
		(hold.String == holdNoContact || hold.String == holdNothingPurchasable) &&
		contact != "" && a.Enabled && stepsSent < len(a.Steps)
	nextWait := 0
	if replan {
		// From the end of this visit: the threshold says when the visit counts
		// as over, and the step's own wait runs from there.
		nextWait = a.AbandonAfterMinutes + a.Steps[stepsSent].WaitMinutes
	}

	lines, err := json.Marshal(snapshot(d.Lines))
	if err != nil {
		return false, err
	}
	tx, err := m.app.DB().BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE cart_recovery_abandonments
		SET kind = $2, email = $3, item_count = $4, subtotal_minor = $5, lines = $6,
		    discount_code = $7, last_active_at = $8, updated_at = now(),
		    next_step_at = CASE WHEN $9::int > 0 THEN $8 + make_interval(mins => $9::int) ELSE next_step_at END,
		    hold_reason  = CASE WHEN $9::int > 0 THEN NULL ELSE hold_reason END,
		    status       = CASE WHEN $9::int > 0 AND status = 'abandoned' THEN 'scheduled' ELSE status END
		WHERE id = $1 AND last_active_at < $8 AND status NOT IN ('recovered', 'expired')`,
		id, kind, nullString(contact), d.ItemCount, d.Subtotal.AmountMinor, lines,
		nullString(d.DiscountCode), d.UpdatedAt, nextWait)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	if err := addEvent(ctx, tx, id, "resumed", map[string]any{
		"item_count": d.ItemCount, "subtotal_minor": d.Subtotal.AmountMinor,
	}, actorShopper); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// claimed is one step a sender holds.
type claimed struct {
	id        int64
	cartID    int64
	kind      string
	stepsSent int
	attempts  int
	linkToken string
}

func (m *Module) sendDue(ctx context.Context, set *settingsRow, res *PassResult) error {
	rows, err := m.app.DB().QueryContext(ctx, `
		UPDATE cart_recovery_abandonments a
		SET claimed_until = now() + make_interval(secs => $2), send_attempts = a.send_attempts + 1
		FROM (
			SELECT id FROM cart_recovery_abandonments
			WHERE next_step_at <= now() AND status IN ('scheduled', 'contacted')
			  AND (claimed_until IS NULL OR claimed_until < now())
			ORDER BY next_step_at
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		) AS due
		WHERE a.id = due.id
		RETURNING a.id, a.cart_id, a.kind, a.steps_sent, a.send_attempts, a.link_token`,
		sendBatch, claimWindow.Seconds())
	if err != nil {
		return err
	}
	var batch []claimed
	for rows.Next() {
		var c claimed
		if err := rows.Scan(&c.id, &c.cartID, &c.kind, &c.stepsSent, &c.attempts, &c.linkToken); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, c := range batch {
		if err := m.sendStep(ctx, set, c, res); err != nil {
			m.log.Error("cart-recovery: send step", "abandonment", c.id, "error", err)
		}
	}
	return nil
}

// cartNow is the basket as it is at the moment of a send.
type cartNow struct {
	gone      bool
	status    string
	token     string
	updatedAt time.Time
	detail    *gocommerce.CartDetail
}

func (m *Module) inspect(ctx context.Context, cartID int64) (*cartNow, error) {
	c := &cartNow{}
	err := m.app.DB().QueryRowContext(ctx,
		`SELECT status, token, updated_at FROM carts WHERE id = $1`, cartID).
		Scan(&c.status, &c.token, &c.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		c.gone = true
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	d, err := m.app.Cart().Get(ctx, cartID)
	if errors.Is(err, gocommerce.ErrNotFound) {
		c.gone = true
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	c.detail = d
	return c, nil
}

func purchasable(d *gocommerce.CartDetail) bool {
	for _, l := range d.Lines {
		if l.InStock {
			return true
		}
	}
	return false
}

// emailDelivers says whether an email would reach anybody. Without a backend
// other than the logger, Notify succeeds and nobody receives anything — a
// sequence that "sent" three reminders into a log file is worse than one that
// says it is waiting for a provider.
func (m *Module) emailDelivers() bool {
	for _, c := range m.app.NotifierChannels() {
		if c.Channel == gocommerce.ChannelEmail {
			return c.Delivers
		}
	}
	return false
}

// sendStep is the decision made at the moment of sending, never earlier: every
// rule is re-checked against the basket as it is now, because a basket
// scheduled yesterday may have been bought, emptied or deleted since.
func (m *Module) sendStep(ctx context.Context, set *settingsRow, c claimed, res *PassResult) error {
	cur, err := m.inspect(ctx, c.cartID)
	if err != nil {
		return m.release(ctx, c.id)
	}
	if cur.gone {
		res.Expired++
		return m.expire(ctx, c.id)
	}
	if cur.status == gocommerce.CartConverted {
		// order.created is on its way and marks the record recovered; until
		// then there is simply nothing more to send.
		_, err := m.app.DB().ExecContext(ctx, `
			UPDATE cart_recovery_abandonments
			SET next_step_at = NULL, claimed_until = NULL, updated_at = now()
			WHERE id = $1`, c.id)
		return err
	}

	// The settings are re-read for the kind the basket is now, which is the
	// sequence a resumed basket that reached checkout should be on.
	kind := kindOf(cur.detail)
	a := set.Settings.automation(kind)
	contact := contactOf(cur.detail)
	switch {
	case !a.Enabled:
		res.Held++
		return m.hold(ctx, c.id, holdAutomationOff, c.stepsSent+1)
	case c.stepsSent >= len(a.Steps):
		res.Held++
		return m.hold(ctx, c.id, holdSequenceDone, 0)
	case contact == "":
		res.Held++
		return m.hold(ctx, c.id, holdNoContact, c.stepsSent+1)
	case len(cur.detail.Lines) == 0 || !purchasable(cur.detail):
		res.Held++
		return m.hold(ctx, c.id, holdNothingPurchasable, c.stepsSent+1)
	case !m.emailDelivers():
		res.Held++
		return m.hold(ctx, c.id, holdNoProvider, c.stepsSent+1)
	}
	// Never write to somebody who is in the basket right now. Not a failure:
	// the step moves to when this visit will count as over.
	if quiet := cur.updatedAt.Add(a.threshold()); time.Now().Before(quiet) {
		_, err := m.app.DB().ExecContext(ctx, `
			UPDATE cart_recovery_abandonments
			SET next_step_at = $2, claimed_until = NULL,
			    send_attempts = greatest(send_attempts - 1, 0), updated_at = now()
			WHERE id = $1`, c.id, quiet)
		return err
	}

	step := a.Steps[c.stepsSent]
	data := m.data(set.Settings, cur, contact, c.linkToken, c.stepsSent+1)
	sendErr := m.app.Notify(ctx, gocommerce.Notification{
		Event: step.Template, Channel: gocommerce.ChannelEmail, To: contact, Data: data,
	})
	if sendErr != nil {
		res.Failed++
		return m.failed(ctx, c, sendErr)
	}
	res.Sent++

	nextWait, hold := 0, ""
	if c.stepsSent+1 < len(a.Steps) {
		nextWait = a.Steps[c.stepsSent+1].WaitMinutes
	} else {
		hold = holdSequenceDone
	}
	tx, err := m.app.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// The guard on status: an order may have landed while the message was in
	// flight, and recovered must stay recovered. The message still went, so
	// the timeline still says so.
	if _, err := tx.ExecContext(ctx, `
		UPDATE cart_recovery_abandonments
		SET steps_sent = steps_sent + 1, messages_sent = messages_sent + 1,
		    last_sent_at = now(), send_attempts = 0, claimed_until = NULL,
		    status = 'contacted', hold_reason = $3,
		    next_step_at = CASE WHEN $2::int > 0 THEN now() + make_interval(mins => $2::int) END,
		    updated_at = now()
		WHERE id = $1 AND status IN ('scheduled', 'contacted')`,
		c.id, nextWait, nullString(hold)); err != nil {
		return err
	}
	if err := addEvent(ctx, tx, c.id, "sent", map[string]any{
		"step": c.stepsSent + 1, "template": step.Template, "channel": gocommerce.ChannelEmail,
		"to": contact, "manual": false,
	}, actorSystem); err != nil {
		return err
	}
	if nextWait > 0 {
		if err := addEvent(ctx, tx, c.id, "scheduled", map[string]any{
			"step": c.stepsSent + 2, "template": a.Steps[c.stepsSent+1].Template, "wait_minutes": nextWait,
		}, actorSystem); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// data flattens one message for a template, in the flat strings the order
// notifications use: the module hands a notifier values, and the store's
// template owns the wording.
func (m *Module) data(s Settings, cur *cartNow, to, linkToken string, step int) map[string]string {
	d := cur.detail
	out := map[string]string{
		"cart_token":     cur.token,
		"currency":       d.Currency,
		"item_count":     strconv.Itoa(d.ItemCount),
		"subtotal_minor": strconv.FormatInt(d.Subtotal.AmountMinor, 10),
		"customer_email": to,
		"summary":        summarise(d.Lines),
		"step":           strconv.Itoa(step),
	}
	if link := m.link(s, linkToken, cur.token); link != "" {
		out["recovery_url"] = link
	}
	// Carried because a basket abandoned with a code on it is worth more of the
	// shopper's attention, and the template may want to say so.
	if d.DiscountCode != "" {
		out["discount_code"] = d.DiscountCode
	}
	return out
}

// link is where a message points. Through this module's own redirect when the
// engine knows its public address, because that is the only way a click can
// be counted; straight to the storefront basket when it does not, because an
// uncounted click still brings the shopper back.
func (m *Module) link(s Settings, linkToken, cartToken string) string {
	storefront := m.storefront(s)
	if storefront == "" {
		return ""
	}
	if panel := m.app.Config().PanelURL; panel != "" {
		return panel + "/x/cart-recovery/r/" + linkToken
	}
	return storefront + "/cart/" + cartToken
}

func (m *Module) release(ctx context.Context, id int64) error {
	_, err := m.app.DB().ExecContext(ctx, `
		UPDATE cart_recovery_abandonments
		SET claimed_until = NULL, send_attempts = greatest(send_attempts - 1, 0)
		WHERE id = $1`, id)
	return err
}

// hold stops the sequence for a reason the screen can show. A scheduled
// record goes back to abandoned, because "scheduled" with nothing scheduled is
// a lie the CHECK refuses.
func (m *Module) hold(ctx context.Context, id int64, reason string, step int) error {
	tx, err := m.app.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `
		UPDATE cart_recovery_abandonments
		SET next_step_at = NULL, claimed_until = NULL, send_attempts = 0, hold_reason = $2,
		    status = CASE WHEN status = 'scheduled' THEN 'abandoned' ELSE status END,
		    updated_at = now()
		WHERE id = $1`, id, reason); err != nil {
		return err
	}
	if reason != holdSequenceDone {
		if err := addEvent(ctx, tx, id, "skipped", map[string]any{
			"step": step, "reason": reason,
		}, actorSystem); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// failed records a send that did not go. It backs off and tries again, and
// after maxAttempts it stops rather than retrying a broken provider every few
// minutes for as long as the basket lives.
func (m *Module) failed(ctx context.Context, c claimed, sendErr error) error {
	tx, err := m.app.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	giveUp := c.attempts >= maxAttempts
	if giveUp {
		_, err = tx.ExecContext(ctx, `
			UPDATE cart_recovery_abandonments
			SET next_step_at = NULL, claimed_until = NULL, send_attempts = 0, hold_reason = 'send_failed',
			    status = CASE WHEN status = 'scheduled' THEN 'abandoned' ELSE status END,
			    updated_at = now()
			WHERE id = $1`, c.id)
	} else {
		_, err = tx.ExecContext(ctx, `
			UPDATE cart_recovery_abandonments
			SET next_step_at = now() + make_interval(mins => $2::int), claimed_until = NULL, updated_at = now()
			WHERE id = $1`, c.id, 10*c.attempts)
	}
	if err != nil {
		return err
	}
	if err := addEvent(ctx, tx, c.id, "send_failed", map[string]any{
		"step": c.stepsSent + 1, "error": sendErr.Error(), "attempt": c.attempts, "gave_up": giveUp,
	}, actorSystem); err != nil {
		return err
	}
	return tx.Commit()
}

// purge deletes records whose basket is long gone, on core's own retention
// clock, so the email on an expired record lives no longer than the basket it
// came from would have. A recovered record stays: it is the attribution of an
// order, and the order keeps the same address for as long as it exists.
func (m *Module) purge(ctx context.Context) (int, error) {
	res, err := m.app.DB().ExecContext(ctx, `
		DELETE FROM cart_recovery_abandonments
		WHERE id IN (
			SELECT id FROM cart_recovery_abandonments
			WHERE expired_at IS NOT NULL AND status <> 'recovered'
			  AND expired_at < now() - make_interval(secs => $1)
			LIMIT $2)`,
		m.app.Config().CartRetention.Seconds(), purgeBatch)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// onOrderCreated credits an order to the basket it came from. Idempotent by
// its status guard, because the outbox delivers at least once.
func (m *Module) onOrderCreated(ctx context.Context, e gocommerce.Event) error {
	var ev gocommerce.OrderEvent
	if err := e.Decode(&ev); err != nil {
		return fmt.Errorf("cart-recovery: decode %s payload: %w", e.Name, err)
	}
	if ev.CartID == 0 {
		return nil
	}
	tx, err := m.app.DB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id int64
	var after bool
	err = tx.QueryRowContext(ctx, `
		UPDATE cart_recovery_abandonments
		SET status = 'recovered', recovered_at = now(), recovered_order_id = $2,
		    recovered_order_number = $3, recovered_total_minor = $4,
		    recovered_after_message = messages_sent > 0,
		    next_step_at = NULL, claimed_until = NULL, hold_reason = NULL, updated_at = now()
		WHERE cart_id = $1 AND status NOT IN ('recovered', 'expired')
		RETURNING id, recovered_after_message`,
		ev.CartID, ev.OrderID, ev.Number, ev.TotalMinor).Scan(&id, &after)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := addEvent(ctx, tx, id, "recovered", map[string]any{
		"order_id": ev.OrderID, "order_number": ev.Number, "total_minor": ev.TotalMinor,
		"currency": ev.Currency, "after_message": after,
	}, actorShopper); err != nil {
		return err
	}
	return tx.Commit()
}

const (
	actorSystem  = "system"
	actorShopper = "shopper"
)

type execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

func addEvent(ctx context.Context, q execer, id int64, kind string, detail map[string]any, actor string) error {
	raw, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `
		INSERT INTO cart_recovery_events (abandonment_id, kind, detail, actor)
		VALUES ($1, $2, $3, $4)`, id, kind, raw, actor)
	return err
}

// actorOf names whoever is behind an admin request: the operator's address,
// or "token" for a script holding the static admin token.
func actorOf(ctx context.Context) string {
	if su := gocommerce.SuperuserFrom(ctx); su != nil {
		return su.Email
	}
	return "token"
}

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

// summarise is the one-line "2 x Blue shirt (M)" a plain template needs without
// walking the line array, matching what the order notifications already carry.
func summarise(lines []gocommerce.CartLine) string {
	var b strings.Builder
	for i, l := range lines {
		if i > 0 {
			b.WriteString(", ")
		}
		b.WriteString(strconv.Itoa(l.Quantity))
		b.WriteString(" x ")
		b.WriteString(l.Title)
		if l.VariantLabel != "" {
			b.WriteString(" (")
			b.WriteString(l.VariantLabel)
			b.WriteString(")")
		}
	}
	return b.String()
}
