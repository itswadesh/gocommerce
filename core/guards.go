package gocommerce

import (
	"context"
	"database/sql"
	"errors"
)

// A checkout guard lets a module refuse a checkout before the order exists
// (D67).
//
// Until now a module could act on a checkout only after it had committed: a
// payment provider's Initiate runs in phase B, and an event handler later still.
// Refusing there means an order that already exists, already reserved stock,
// already sent its order.created email, and has to be cancelled — so a credit
// limit was something to apologise for rather than something enforced. A guard
// runs inside phase A, after every line has been re-priced and reserved and the
// total is known, and before the order number is drawn. Returning an error rolls
// the whole transaction back: no order, no reservation, no number burned.
//
// Two things follow from running inside that transaction, and both are rules
// for the guard rather than for the engine. It must not do network I/O — the
// transaction holds the cart's row lock and every reserved variant's, and a
// guard waiting on a remote service would make the store wait with it (rule 5).
// And Tx is for the guard's own reads and locks: it may read core tables and
// take an advisory lock that serialises its own decision, but it never writes a
// core table, through Tx or otherwise (rule 3).

// CheckoutAttempt is a basket about to become an order, as a guard sees it.
type CheckoutAttempt struct {
	// Method is the payment method's code.
	Method string
	// Input is the checkout as the caller sent it: the email, the address,
	// the metadata. It is the caller's word, not proof of anything.
	Input CheckoutInput
	// VerifiedEmail is the address the cart was priced as (D66), empty when
	// nothing vouched for it. Unlike Input.Email, something proved it.
	VerifiedEmail string
	// Lines are the basket at the prices this order will charge.
	Lines []AttemptLine
	// The order's figures, exactly as they will be stored.
	Subtotal Money
	Shipping Money
	Discount Money
	Tax      Money
	Total    Money
	// ByOperator is true when the order is being placed through
	// Orders.Create — an operator on a customer's behalf, or a module standing
	// in for one — rather than by whoever holds the cart's token.
	ByOperator bool
	// Tx is the checkout's transaction. Read with it and lock with it; never
	// write a core table with it.
	Tx *sql.Tx
}

// AttemptLine is one line of a basket at checkout.
type AttemptLine struct {
	VariantID int64
	ProductID int64
	SKU       string
	Quantity  int
	UnitPrice Money
	// Agreed is true for a price set on the order rather than resolved from
	// the catalogue and the price lists: NewOrderLine.UnitPriceMinor.
	Agreed bool
}

// CheckoutGuard inspects a checkout and returns an error to refuse it. Return
// an *APIError — Forbiddenf, Conflictf, Validationf — for a refusal the caller
// should read; anything else is treated as the guard failing, answered as a
// 500, and logged.
type CheckoutGuard func(ctx context.Context, a *CheckoutAttempt) error

// RegisterCheckoutGuard adds a guard. Every guard runs on every checkout, in
// registration order, and the first refusal wins.
func (a *App) RegisterCheckoutGuard(g CheckoutGuard) {
	if g == nil {
		a.regErrf("module %q: a nil checkout guard", a.ownerName())
		return
	}
	a.guards = append(a.guards, guardEntry{owner: a.ownerName(), fn: g})
}

type guardEntry struct {
	owner string
	fn    CheckoutGuard
}

// runGuards asks each guard in turn. A guard's own refusal passes through
// untouched; any other error is the guard breaking, and the shopper is told so
// without being told the guard's internals.
func (a *App) runGuards(ctx context.Context, attempt *CheckoutAttempt) error {
	for _, g := range a.guards {
		err := g.fn(ctx, attempt)
		if err == nil {
			continue
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return err
		}
		return Internalf(err, "checkout guard from module %q failed", g.owner)
	}
	return nil
}

type byOperatorKey struct{}

// withByOperator marks a checkout as placed through Orders.Create.
func withByOperator(ctx context.Context) context.Context {
	return context.WithValue(ctx, byOperatorKey{}, true)
}

func byOperator(ctx context.Context) bool {
	v, _ := ctx.Value(byOperatorKey{}).(bool)
	return v
}
