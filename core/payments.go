package gocommerce

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Payments owns payment state. Every gateway, every admin action and every
// webhook funnels through MarkPaid and MarkFailed, so there is exactly one
// place where an order's money status changes — and exactly one place where
// the event announcing it is written.
type Payments struct {
	app       *App
	providers map[string]PaymentProvider
}

// Pay returns the payment service.
func (a *App) Pay() *Payments { return a.payments }

func (p *Payments) provider(code string) (PaymentProvider, bool) {
	pr, ok := p.providers[code]
	return pr, ok
}

// Methods lists the payment codes this store accepts.
func (p *Payments) Methods() []string {
	codes := make([]string, 0, len(p.providers))
	for code := range p.providers {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

// PaymentMethod is one installed method as a storefront should show it: the
// code the checkout URL speaks, and the label to put beside the radio button.
//
// It is [ProviderInfo] without Module, under the same key: which module
// installed a gateway is a fact about this build, and an unauthenticated route
// has no business publishing it. One key for one string across both routes,
// because two names for it is how they come to disagree.
type PaymentMethod struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Installed lists the same methods as [Payments.Methods], with the name each
// provider gives itself.
//
// Methods() stays exactly as it is rather than changing shape: a storefront
// written against that array, ext/mcp's store_info tool and scripts/smoke.ps1
// all read it, and a second shape beside it is the only additive way to answer
// a question the first cannot.
//
// The fallback is the code itself and not a prettified version of it, because
// that is what GET /api/admin/settings already answers for the same provider
// (see paymentInfo). A provider whose code is an abbreviation implements
// [Named] — that is the whole of what the port is for.
func (p *Payments) Installed() []PaymentMethod {
	codes := p.Methods()
	out := make([]PaymentMethod, 0, len(codes))
	for _, code := range codes {
		m := PaymentMethod{Code: code, Name: code}
		if named, ok := p.providers[code].(Named); ok && named.DisplayName() != "" {
			m.Name = named.DisplayName()
		}
		out = append(out, m)
	}
	return out
}

// MarkPaid records that money arrived and confirms the order.
//
// It is idempotent by design, not by accident: a gateway will replay a webhook,
// and marking an already-paid order paid a second time must not commit its
// stock twice or emit a second order.paid.
//
// Confirming here is what makes a gateway order shippable — without it, a paid
// order would sit at "pending" forever waiting for a step nobody performs.
func (p *Payments) MarkPaid(ctx context.Context, orderID int64, reference string) (*Order, error) {
	return p.app.orders.transition(ctx, orderID, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		if o.PaymentStatus == PaymentPaid {
			return transitionResult{}, nil
		}
		if o.PaymentStatus == PaymentRefunded || o.Refunded.AmountMinor > 0 {
			// The mirror of MarkUnpaid's refusal below. Refund leaves the order
			// confirmed and only moves the payment, so a refunded order is not
			// paid and not cancelled — it would fall straight through here and
			// be rewritten to paid, announcing an order.paid for a sale that
			// already ended.
			//
			// The running total is read as well as the status because under D36
			// a partly refunded order still says `paid`. It reaches this only
			// through the no-op above, which is the right answer for a replayed
			// webhook — but the condition has to be true of the money rather
			// than of the word for the next person reading it.
			return transitionResult{}, Conflictf("order %s was refunded; recording payment on it again would erase that", o.Number)
		}
		if o.Status == OrderCancelled {
			// Money for a cancelled order is a refund problem, not a
			// confirmation problem, and silently reviving the order would
			// resurrect an inventory reservation nobody is holding.
			return transitionResult{}, Conflictf("order %s was cancelled; this payment needs a refund, not a confirmation", o.Number)
		}

		if _, err := tx.ExecContext(ctx, `
			UPDATE orders
			SET payment_status = $2,
			    payment_reference = coalesce(nullif($3, ''), payment_reference),
			    updated_at = now()
			WHERE id = $1`, o.ID, PaymentPaid, reference); err != nil {
			return transitionResult{}, err
		}
		o.PaymentStatus = PaymentPaid

		if o.Status == OrderPending {
			if err := commitOrderStock(ctx, tx, o, ""); err != nil {
				return transitionResult{}, err
			}
			if err := setOrderStatus(ctx, tx, o.ID, OrderConfirmed); err != nil {
				return transitionResult{}, err
			}
			o.Status = OrderConfirmed
		}
		res := transitionResult{
			Event: EventOrderPaid, Payload: p.app.orders.eventPayload(o),
			Action: AuditOrderMarkPaid, Summary: "Marked order " + o.Number + " paid",
		}
		if reference != "" {
			res.After = map[string]any{"payment_reference": reference}
		}
		return res, nil
	})
}

// MarkUnpaid takes back a payment status that was recorded by mistake — a
// payment that never arrived, or a failure recorded on the wrong row. Both are
// the same operation and the same right: put this order's money back where it
// was before somebody touched it.
//
// This is a correction, not a refund. A refund says money went out and is a
// fact about the world; this says the money never came in and somebody clicked
// the wrong row — so it leaves no refund behind and no trace but the event.
//
// It moves the payment and nothing else. The temptation is to also undo the
// confirmation MarkPaid performs, and that is wrong: a confirmed order awaiting
// payment is not a broken state, it is what every cash-on-delivery order in the
// store already is. Un-confirming would put the stock back on the shelf while
// the order is still live, which is the one outcome nobody wants — and for a
// COD order, whose checkout confirmed it long before anybody touched the
// payment, it would reverse a decision this never made.
//
// So the stock does not move. An operator who wants it back cancels the order,
// which is the operation that means that and already knows how.
func (p *Payments) MarkUnpaid(ctx context.Context, orderID int64) (*Order, error) {
	return p.app.orders.transition(ctx, orderID, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		// Before the switch, because under D36 a partly refunded order reads
		// `paid` and would fall past `case PaymentRefunded` to the general path
		// below — erasing a payment that partly came back, which is the exact
		// regression keeping the four statuses would otherwise open.
		if o.Refunded.AmountMinor > 0 {
			return transitionResult{}, Conflictf(
				"order %s has had %d %s refunded; that is a payment that happened and came back, not one to erase",
				o.Number, o.Refunded.AmountMinor, o.Currency)
		}
		switch o.PaymentStatus {
		case PaymentPending:
			// Already not paid. Nothing to take back.
			return transitionResult{}, nil
		case PaymentFailed:
			if o.Status == OrderCancelled {
				// Nothing to take back on a sale that ended. Its payment status
				// is a historical note by then, and moving it to pending would
				// say somebody might still pay for an order nobody can.
				return transitionResult{}, nil
			}
			// A recorded failure is the other thing this takes back, and the
			// only correction in the drawer that would otherwise have no undo:
			// MarkFailed writes no event and stores no reason, so a mis-click
			// on the wrong row leaves nothing to notice afterwards.
			//
			// Silently, though. EventOrderUnpaid means a payment that WAS
			// recorded has been taken back; emitting it here would tell every
			// notifier that money was reversed when none was ever recorded. The
			// failure was never announced, so its reversal has nothing to
			// correct. The audit row is still written — a person did this.
			if _, err := tx.ExecContext(ctx, `
				UPDATE orders SET payment_status = $2, payment_reference = '', updated_at = now()
				WHERE id = $1`, o.ID, PaymentPending); err != nil {
				return transitionResult{}, err
			}
			o.PaymentStatus = PaymentPending
			return transitionResult{
				Action:  AuditOrderMarkUnpaid,
				Summary: "Cleared the recorded payment failure on order " + o.Number,
			}, nil
		case PaymentRefunded:
			// Still reachable, and not dead code: an order whose total is zero —
			// a hundred-percent discount, or one imported that way — can say
			// `refunded` with nothing in the ledger, because there was no money
			// to record going back.
			return transitionResult{}, Conflictf("order %s was refunded; that is a payment that happened and came back, not one to erase", o.Number)
		}
		if o.Status == OrderCancelled {
			return transitionResult{}, Conflictf("order %s is cancelled; money on it is a refund, not a mistake to unrecord", o.Number)
		}

		// The reference goes with it. It described a payment that did not
		// happen, and leaving it behind would have the next person looking for
		// a transaction nobody can find.
		if _, err := tx.ExecContext(ctx, `
			UPDATE orders SET payment_status = $2, payment_reference = '', updated_at = now()
			WHERE id = $1`, o.ID, PaymentPending); err != nil {
			return transitionResult{}, err
		}
		o.PaymentStatus = PaymentPending
		return transitionResult{
			Event: EventOrderUnpaid, Payload: p.app.orders.eventPayload(o),
			Action:  AuditOrderMarkUnpaid,
			Summary: "Took back a payment recorded in error on order " + o.Number,
		}, nil
	})
}

// MarkFailed records that a payment attempt did not succeed. The order stays
// pending so the shopper can try again; if nobody does, the unpaid sweeper
// eventually cancels it and returns the stock — which it can only do because
// SweepUnpaid and the doctor both count a failed payment as unsettled (M23).
// [Payments.MarkUnpaid] takes the failure back.
func (p *Payments) MarkFailed(ctx context.Context, orderID int64, reason string) (*Order, error) {
	return p.app.orders.transition(ctx, orderID, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		// A sale that already ended records nothing, and says nothing about it.
		// This runs FIRST because Orders.Cancel has no payment guard, so
		// cancelled-and-paid is reachable: with the paid check first, a late
		// gateway webhook for such an order would get a 409, and both bundled
		// gateways answer 500 and release their idempotency claim on any error
		// from here — a webhook retried forever over an order nobody can fix.
		if o.Status == OrderCancelled {
			return transitionResult{}, nil
		}
		// Said before the already-paid refusal, which is what a partly refunded
		// order would otherwise get: true, and no help at all to somebody
		// looking at money that has come back.
		//
		// This route is gated on orders.write while refunding needs
		// orders.refund, so without it an operator who may not refund could
		// erase the record that somebody else did.
		if o.PaymentStatus == PaymentRefunded || o.Refunded.AmountMinor > 0 {
			return transitionResult{}, Conflictf("order %s was refunded; a payment that happened and came back is not one to fail", o.Number)
		}
		if o.PaymentStatus == PaymentPaid {
			return transitionResult{}, Conflictf("order %s is already paid", o.Number)
		}
		if o.PaymentStatus == PaymentFailed {
			return transitionResult{}, nil
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE orders SET payment_status = $2, updated_at = now() WHERE id = $1`,
			o.ID, PaymentFailed)
		if err != nil {
			return transitionResult{}, err
		}
		p.app.log.Info("payment failed", "order", o.Number, "reason", reason)
		// No event — this publishes none today and that stays true — but the
		// reason has until now reached only the process log and nowhere a
		// person could read it afterwards.
		return transitionResult{
			Action:  AuditOrderMarkFailed,
			Summary: "Recorded a failed payment on order " + o.Number + ": " + reason,
		}, nil
	})
}

// ------------------------------------------------------------------- refunds

// refundStaleAfter is how long a refund may sit `pending` before it has stopped
// being in flight and started being a stranded row. The doctor warns past it and
// [Payments.SettleRefund] refuses before it, so the two agree about when a
// refund has stopped being in flight — and an operator cannot settle a call that
// is still running.
const refundStaleAfter = 15 * time.Minute

// How much text the store keeps from a person and from a gateway. Both are
// clipped rather than refused at the database, because a truncated sentence is
// a better record than a failed refund.
const (
	refundReasonMax = 500
	refundErrorMax  = 500
)

// RefundRequest is what an operator asked to send back.
type RefundRequest struct {
	// AmountMinor omitted, zero or negative means everything that has not been
	// refunded yet — counting a refund still in flight — and not the order
	// total. It is the only reading that stays true on the second call, and it
	// keeps the one-click full refund working after a goodwill refund has
	// already gone out.
	AmountMinor int64 `json:"amount_minor"`
	// Reason is free text, recorded on the refund row and carried on the event
	// so that whatever tells the customer can say why. The acting operator
	// comes from the session and never from here.
	Reason string `json:"reason,omitempty"`
}

// RefundSettlement is an operator saying what actually happened to a refund the
// engine asked for and never heard back about.
type RefundSettlement struct {
	// Outcome is [RefundSucceeded] or [RefundFailed]. There is no third answer:
	// the point of the call is that somebody looked at the gateway and knows.
	Outcome string `json:"outcome"`
	// ProviderReference is the gateway's id for the refund, when the outcome is
	// that it did go out.
	ProviderReference string `json:"provider_reference,omitempty"`
	// Note is what the gateway said, when it did not.
	Note string `json:"note,omitempty"`
}

// Refund sends money back through the provider that took it, and records what
// went back.
//
// It runs in three steps, because AGENTS rule 5 forbids holding a transaction
// across the gateway call and the amount still has to be reserved against a
// concurrent second refund:
//
//  1. one transaction locks the order, works out what is still refundable —
//     counting refunds already in flight — and commits a `pending` refund row.
//     That committed row is the reservation; a held lock could not be one,
//     because it would have to be held across the network call.
//  2. the provider is asked, outside every transaction.
//  3. one [Orders.transition] moves `refunded_minor`, `payment_status` and the
//     `order.refunded` outbox row together, so the record of the money and the
//     announcement of it are inseparable (rule 4).
//
// A provider that cannot refund — cash on delivery, for one — simply does not
// implement [Refunder], and that is reported plainly in the pre-flight, before
// any row exists. A provider that declines leaves a `failed` row carrying what
// it said.
//
// The amount is what is left rather than the whole total: see [RefundRequest].
// Partiality is a number and not a status (D36) — `payment_status` stays `paid`
// until every minor unit has come back.
func (p *Payments) Refund(ctx context.Context, orderID int64, in RefundRequest, by *Superuser) (*Order, error) {
	reason := strings.TrimSpace(in.Reason)
	if len([]rune(reason)) > refundReasonMax {
		return nil, Validationf("a refund reason is at most %d characters", refundReasonMax)
	}

	// The pre-flight writes nothing, so a method that cannot refund at all is
	// refused before any row exists — which is what keeps a cash-on-delivery
	// refusal a clean 409 with no stranded reservation behind it.
	o, err := p.app.orders.Get(ctx, orderID)
	if err != nil {
		return nil, err
	}
	if o.PaymentStatus == PaymentRefunded {
		return nil, Conflictf("order %s has already been refunded in full", o.Number)
	}
	if o.PaymentStatus != PaymentPaid {
		return nil, Conflictf("order %s is not paid, so there is nothing to refund", o.Number)
	}
	if in.AmountMinor > o.Total.AmountMinor {
		return nil, Validationf("refund of %d exceeds the order total of %d",
			in.AmountMinor, o.Total.AmountMinor)
	}

	provider, ok := p.provider(o.PaymentProvider)
	if !ok {
		return nil, Conflictf("payment method %q is not installed in this build, so it cannot refund", o.PaymentProvider)
	}
	refunder, ok := provider.(Refunder)
	if !ok {
		return nil, Conflictf("payment method %q does not support refunds", o.PaymentProvider)
	}

	refundID, amount, err := p.reserveRefund(ctx, orderID, in.AmountMinor, reason, by)
	if err != nil {
		return nil, err
	}

	// Step two. Outside every transaction: refunding is a network round trip
	// and must not hold core write locks (rule 5).
	var reference string
	if rr, ok := provider.(ReferencedRefunder); ok {
		reference, err = rr.RefundWithReference(ctx, o, amount)
	} else {
		err = refunder.Refund(ctx, o, amount)
	}
	if err != nil {
		p.failRefund(ctx, refundID, err.Error())
		return nil, Internalf(err,
			"the refund was declined by %s; nothing was recorded as having moved, so check the provider before retrying",
			o.PaymentProvider)
	}

	return p.settleRefund(ctx, orderID, refundID, reference, false)
}

// SettleRefund records what actually happened to a refund the engine asked for
// and never heard back about — a process that died between the gateway
// accepting and the settling transaction committing.
//
// The engine genuinely cannot know whether the money left; a person looking at
// the gateway can. Without this the only remedy would be an UPDATE against a
// core table by hand, which is AGENTS rule 3 — a state change nothing was told
// about. A sweeper is deliberately absent: guessing an outcome is the one thing
// nothing here is entitled to do.
func (p *Payments) SettleRefund(ctx context.Context, orderID, refundID int64,
	in RefundSettlement, by *Superuser) (*Order, error) {

	if in.Outcome != RefundSucceeded && in.Outcome != RefundFailed {
		return nil, Validationf("outcome must be %q or %q; the point of settling a refund is to say which",
			RefundSucceeded, RefundFailed)
	}

	var owner int64
	var status string
	var stale bool
	// The staleness is decided by the database's clock, the same one that
	// stamped created_at and the same one the doctor's warning reads.
	err := p.app.db.QueryRowContext(ctx, `
		SELECT order_id, status, created_at < now() - $2::interval
		FROM order_refunds WHERE id = $1`,
		refundID, intervalSeconds(refundStaleAfter)).Scan(&owner, &status, &stale)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("refund %d does not exist", refundID)
	}
	if err != nil {
		return nil, err
	}
	if owner != orderID {
		// Not "it belongs to order 12": which order a refund is on is not
		// something the caller was entitled to discover by asking.
		return nil, NotFoundf("refund %d does not exist on order %d", refundID, orderID)
	}
	if status != RefundPending {
		return nil, Conflictf("refund %d is already %s", refundID, status)
	}
	if !stale {
		return nil, Conflictf(
			"refund %d was started less than %s ago and may still be in flight; wait before settling it by hand",
			refundID, refundStaleAfter)
	}

	actor := "a script"
	if by != nil {
		actor = by.Email
	}
	p.app.log.Info("refund settled by hand",
		"refund", refundID, "order", orderID, "outcome", in.Outcome, "by", actor)

	if in.Outcome == RefundSucceeded {
		return p.settleRefund(ctx, orderID, refundID, strings.TrimSpace(in.ProviderReference), true)
	}
	// Nothing about the order changed, so there is no event — but a person did
	// this, which is what the audit row is for.
	if err := p.failRefundByHand(ctx, orderID, refundID,
		clipText(strings.TrimSpace(in.Note), refundErrorMax)); err != nil {
		return nil, err
	}
	return p.app.orders.Get(ctx, orderID)
}

// reserveRefund commits the `pending` row that reserves an amount while the
// provider is being asked.
//
// It writes no event and no audit row: `refunded_minor` and `payment_status` do
// not move, so nothing about the order has changed yet and there is nothing to
// announce. Who asked is not lost — the row itself carries superuser_id and the
// actor's email, so even a refund that is later declined says whose decision it
// was.
func (p *Payments) reserveRefund(ctx context.Context, orderID, want int64,
	reason string, by *Superuser) (refundID, amount int64, err error) {

	err = InTx(ctx, p.app.db, func(tx *sql.Tx) error {
		o, err := lockOrder(ctx, tx, orderID)
		if err != nil {
			return err
		}
		// Re-asserted under the lock. The pre-flight read it without one, and
		// between the two an operator may have finished the refund.
		if o.PaymentStatus != PaymentPaid {
			return Conflictf("order %s is not paid, so there is nothing to refund", o.Number)
		}
		remaining, err := refundRemaining(ctx, tx, o)
		if err != nil {
			return err
		}
		if remaining <= 0 {
			return Conflictf("order %s has nothing left to refund", o.Number)
		}
		amount = want
		if amount <= 0 {
			amount = remaining
		}
		if amount > remaining {
			return Validationf(
				"refund of %d exceeds the %d still refundable on order %s (%d of %d has already gone back, and a refund in flight is counted)",
				amount, remaining, o.Number, o.Refunded.AmountMinor, o.Total.AmountMinor)
		}

		// Nil and empty for the static admin token: a credential is not a
		// person, and a script authenticating that way is the system rather
		// than somebody, which core treats as normal rather than as an error.
		var superuserID any
		actor := ""
		if by != nil {
			superuserID, actor = by.ID, by.Email
		}
		return tx.QueryRowContext(ctx, `
			INSERT INTO order_refunds (order_id, amount_minor, reason, provider, superuser_id, actor)
			VALUES ($1, $2, $3, $4, $5, $6)
			RETURNING id`,
			orderID, amount, reason, o.PaymentProvider, superuserID, actor).Scan(&refundID)
	})
	if err != nil {
		return 0, 0, err
	}
	return refundID, amount, nil
}

// refundRemaining is what is still refundable on an order the caller is already
// holding FOR UPDATE: the total, less what has gone back, less what is in
// flight. A pending row counts — that is the whole reason it is committed
// before the gateway is called.
func refundRemaining(ctx context.Context, tx *sql.Tx, o *Order) (int64, error) {
	var pending int64
	if err := tx.QueryRowContext(ctx, `
		SELECT coalesce(sum(amount_minor), 0) FROM order_refunds
		WHERE order_id = $1 AND status = $2`, o.ID, RefundPending).Scan(&pending); err != nil {
		return 0, err
	}
	return o.Total.AmountMinor - o.Refunded.AmountMinor - pending, nil
}

// settleRefund is step three: the refund row, the running total, the payment
// status and the event, in one transaction.
//
// byHand distinguishes the ordinary path from [Payments.SettleRefund] in the
// audit trail alone. What happens to the money is identical, and it has to be:
// a recovery path that moved a different amount would be a second refund
// implementation.
func (p *Payments) settleRefund(ctx context.Context, orderID, refundID int64,
	reference string, byHand bool) (*Order, error) {

	return p.app.orders.transition(ctx, orderID, func(ctx context.Context, tx *sql.Tx, o *Order) (transitionResult, error) {
		var amount int64
		var reason string
		err := tx.QueryRowContext(ctx, `
			UPDATE order_refunds
			SET status = $2, provider_reference = $3, updated_at = now()
			WHERE id = $1 AND status = $4
			RETURNING amount_minor, reason`,
			refundID, RefundSucceeded, reference, RefundPending).Scan(&amount, &reason)
		if errors.Is(err, sql.ErrNoRows) {
			// Somebody settled it first. The money moved once, so it is
			// recorded once and announced once.
			return transitionResult{}, nil
		}
		if err != nil {
			return transitionResult{}, err
		}

		refunded := o.Refunded.AmountMinor + amount
		status := o.PaymentStatus
		// >= rather than ==, so an over-refund that somehow arrived still lands
		// on the right status instead of leaving the order readable as paid.
		if refunded >= o.Total.AmountMinor {
			status = PaymentRefunded
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE orders SET refunded_minor = $2, payment_status = $3, updated_at = now()
			WHERE id = $1`, o.ID, refunded, status); err != nil {
			return transitionResult{}, err
		}
		o.Refunded = money(refunded, o.Currency)
		o.PaymentStatus = status

		payload := p.app.orders.eventPayload(o)
		// The same field order.cancelled already uses for the same question, so
		// a notifier keyed on data["reason"] works with no change.
		payload.Reason = reason
		payload.Refund = &OrderRefundEvent{
			AmountMinor:    amount,
			RefundedMinor:  refunded,
			RemainingMinor: max(o.Total.AmountMinor-refunded, 0),
			Reason:         reason,
			Reference:      reference,
		}

		res := transitionResult{
			Event: EventOrderRefunded, Payload: payload,
			Action:  AuditOrderRefund,
			Summary: "Refunded order " + o.Number + " through " + o.PaymentProvider,
			// Money is minor units plus a currency and never a formatted
			// amount (rule 6).
			After: map[string]any{
				"amount_minor":       amount,
				"refunded_minor":     refunded,
				"currency":           o.Currency,
				"provider":           o.PaymentProvider,
				"provider_reference": reference,
				"reason":             reason,
			},
		}
		if byHand {
			res.Action = AuditOrderRefundSettle
			res.Summary = "Settled a stranded refund on order " + o.Number + ": the money did go out"
		}
		return res, nil
	})
}

// failRefund records that the provider declined, so the amount stops being
// reserved and the attempt stays visible.
//
// Under context.WithoutCancel because a gateway timeout is exactly when this
// runs and the caller's context may already be gone. Failing to write it would
// leave the amount reserved against every later refund, so it logs rather than
// disappearing.
func (p *Payments) failRefund(ctx context.Context, refundID int64, msg string) {
	ctx = context.WithoutCancel(ctx)
	if _, err := p.app.db.ExecContext(ctx, `
		UPDATE order_refunds SET status = $2, error = $3, updated_at = now()
		WHERE id = $1 AND status = $4`,
		refundID, RefundFailed, clipText(msg, refundErrorMax), RefundPending); err != nil {
		p.app.log.Error("could not record a declined refund",
			"refund", refundID, "error", err)
	}
}

// failRefundByHand is the operator saying the money never left. It moves no
// money, so it writes no event — but a person did it to a money record, so the
// row and its audit entry commit together.
func (p *Payments) failRefundByHand(ctx context.Context, orderID, refundID int64, note string) error {
	return InTx(ctx, p.app.db, func(tx *sql.Tx) error {
		o, err := lockOrder(ctx, tx, orderID)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			UPDATE order_refunds SET status = $2, error = $3, updated_at = now()
			WHERE id = $1 AND status = $4`,
			refundID, RefundFailed, note, RefundPending)
		if err != nil {
			return err
		}
		// Somebody settled it between the read above and this write.
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return Conflictf("refund %d has already been settled", refundID)
		}
		summary := "Settled a stranded refund on order " + o.Number + ": it never went out"
		if note != "" {
			summary += " — " + note
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditOrderRefundSettle, Entity: AuditEntityOrder,
			ID: o.ID, Label: o.Number, Summary: summary,
			After: map[string]any{"refund_id": refundID, "outcome": RefundFailed, "note": note},
		})
	})
}

// loadOrderRefunds attaches the ledger to a page of orders in one query, beside
// the writing that produces it — the trade loadOrderDiscounts makes.
//
// Order.Refunded is deliberately not accumulated here: it comes off
// orders.refunded_minor through orderColumns, which is what the stored column
// is for, and summing it in two places is how the two would come to disagree.
func (s *Orders) loadOrderRefunds(ctx context.Context, byID map[int64]*Order, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.app.db.QueryContext(ctx, `
		SELECT order_id, id, amount_minor, reason, status, provider,
		       provider_reference, error, actor, created_at
		FROM order_refunds WHERE order_id = ANY($1::bigint[]) ORDER BY id`, int64Array(ids))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var orderID int64
		var r OrderRefund
		var amount int64
		if err := rows.Scan(&orderID, &r.ID, &amount, &r.Reason, &r.Status, &r.Provider,
			&r.ProviderReference, &r.Error, &r.By, &r.CreatedAt); err != nil {
			return err
		}
		if o := byID[orderID]; o != nil {
			r.Amount = money(amount, o.Currency)
			o.Refunds = append(o.Refunds, r)
		}
	}
	return rows.Err()
}

// intervalSeconds renders a duration as something PostgreSQL will cast to an
// interval, so a Go constant and a SQL predicate cannot drift apart.
func intervalSeconds(d time.Duration) string {
	return strconv.FormatInt(int64(d.Seconds()), 10) + " seconds"
}

// clipText keeps a stored message inside a sane length without cutting a rune
// in half. What a gateway says is not input this store controls the length of.
func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ------------------------------------------------------------ cash on delivery

// codProvider is the built-in payment method: the one that needs no third
// party, which is why it can live in core without dragging an SDK behind it.
//
// Checkout with cash on delivery confirms the order immediately — the sale is
// happening and the stock leaves the shelf — while payment stays pending until
// an operator marks it paid on delivery.
type codProvider struct{}

// CodeCOD is the payment code of the built-in cash-on-delivery method.
const CodeCOD = "cod"

func (codProvider) Code() string { return CodeCOD }

func (codProvider) DisplayName() string { return "Cash on delivery" }

func (codProvider) Initiate(ctx context.Context, o *Order, opts PayOptions) (PaymentIntent, error) {
	return PaymentIntent{Kind: IntentNone, Provider: CodeCOD}, nil
}

func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
