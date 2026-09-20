package gocommerce

import (
	"net/http"
)

// Scoping a vendor to their own rows.
//
// The hard part of a marketplace is not showing a seller their own things. It
// is the thirty other places that were written before sellers existed and
// happily answer "every order", "every product", "every report" to whoever
// asks. A rule that has to be remembered in each of them is a rule that will be
// missed in one, and the one is a data leak with a competitor on the other end.
//
// So the rule runs the other way. Every admin route refuses a vendor account
// unless it was deliberately opened to them with HandleVendorFunc. A route
// added next year by somebody who has never heard of vendors is closed, and the
// cost of forgetting is a seller seeing a 403 rather than a seller seeing
// everybody's margins.
//
// Inside an opened route, VendorScopeOf narrows the rows — but that narrowing
// is the second lock. The first one is that almost nothing is open at all.

// refuseVendors closes a route to seller accounts.
//
// Wrapped around every admin handler that did not ask to be open. It runs after
// authentication (it needs to know who is asking) and its answer does not
// depend on rights: a store that grants the vendor role orders.read has decided
// what a vendor may do, not that a vendor may do it to everybody's orders.
func refuseVendors(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsVendorAccount(r.Context()) {
			RespondError(w, r, &APIError{
				Status: http.StatusForbidden,
				Code:   "vendor_scope",
				Message: "This is a vendor account, and this endpoint is not part of what a " +
					"vendor may reach. A vendor sees their own record, their own offers, and " +
					"the catalogue they can offer against.",
			})
			return
		}
		h.ServeHTTP(w, r)
	})
}

// HandleVendorFunc mounts an admin route that a seller may reach.
//
// The handler is then responsible for narrowing what it returns — see
// VendorScopeOf and the vendor handlers, which is the whole of the list this
// applies to. Using it is a decision somebody makes on purpose, in the route
// declaration, where a reviewer sees it.
func (a *App) HandleVendorFunc(pattern string, h http.HandlerFunc, rights ...Right) {
	a.mountVendorReachable(pattern, h, rights...)
}

// requireOwnVendor resolves the {id} in a vendor route against the caller.
//
// For an operator it is the id in the path. For a seller it is their own id,
// and any other id is reported as not found rather than forbidden: that a
// vendor with that id exists is itself something a competitor should not be
// able to confirm by watching the status code change.
func requireOwnVendor(r *http.Request, id int64) error {
	scope := VendorScopeOf(r.Context())
	if scope == nil {
		return nil
	}
	if *scope != id {
		return NotFoundf("vendor not found")
	}
	return nil
}

// stripCostFor removes the shop's own cost price from a catalogue response when
// the caller is a seller.
//
// A vendor reads the catalogue to find things to offer against, which is a
// reasonable thing to let them do and a bad thing to do naively: `cost` is what
// the shop paid, and handing that to somebody competing on price is handing
// them the shop's floor. The field is removed rather than zeroed, because zero
// is a number and a reader cannot tell it from "this cost nothing".
func stripCostFor(r *http.Request, products []*Product) {
	if !IsVendorAccount(r.Context()) {
		return
	}
	for _, p := range products {
		for _, v := range p.Variants {
			v.Cost = nil
		}
	}
}
