// Package indiapost records shipments handed to India Post.
//
// Registering it adds "india-post" as a fulfillment provider, so an operator
// ships by posting to the engine's own /api/admin/create-fulfillment with
// `"provider": "india-post"` and the consignment number from the counter
// receipt.
//
//	app, err := gocommerce.New(cfg, indiapost.New(indiapost.Config{}))
//
// # This module books nothing, and that is the honest part
//
// Every other fulfillment module here calls an API and gets a waybill back.
// India Post has no such API that a store can sign up for: the Department of
// Posts integrates bulk customers one at a time, and the endpoint, the
// credentials and the payload are issued under that agreement rather than
// published. There is no public sandbox, no developer portal that hands out a
// key, and no documented request body — so a module that claimed to book a
// shipment would be guessing at somebody else's contract.
//
// What this module does instead is the part that can be done correctly:
//
//   - it takes the consignment number the operator read off the receipt, or
//     printed from DoP's own portal;
//   - it checks that the number is actually an India Post article number
//     before the order is marked shipped, which catches the transposed digit
//     that otherwise surfaces a week later as a customer with a dead link;
//   - it records the carrier as india-post rather than leaving the engine to
//     work it out, so the number is never attributed to somebody else.
//
// A store with a DoP integration agreement should write a module against the
// endpoints in that agreement; this one is for everybody else, and it is better
// than the manual provider only in that it refuses a number India Post did not
// issue.
//
// # The number's shape
//
// India Post uses the UPU's S10 standard: two letters, nine digits, and "IN" —
// EE123456789IN for Speed Post, RX… for registered, CX… for business parcels.
// That is what is checked. Products that do not use S10 exist; a store shipping
// one sets AcceptAnyNumber and takes the check off.
package indiapost

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	"github.com/misiki/gocommerce/core"
)

// s10 is the UPU standard India Post issues under: two letters, nine digits,
// the origin country. It is the same rule the engine's own carrier detection
// uses to identify an India Post number, and checking it here means the number
// stored will detect as India Post rather than as nothing.
var s10 = regexp.MustCompile(`^[A-Z]{2}[0-9]{9}IN$`)

// Config configures the module.
type Config struct {
	// AcceptAnyNumber turns off the S10 check. India Post has products that
	// number differently, and a store shipping one should be able to record
	// the number it was given rather than argue with this module.
	AcceptAnyNumber bool
}

// Module is the India Post fulfillment provider.
type Module struct {
	cfg Config
	log *slog.Logger
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-indiapost" }

// Migrations implements gocommerce.Module. The engine already stores the
// shipment; this module keeps no state of its own.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.log = app.Log()
	app.RegisterFulfillment(m)
	return nil
}

// Code implements gocommerce.FulfillmentProvider.
func (m *Module) Code() string { return "india-post" }

// Ship records a booking somebody else made.
func (m *Module) Ship(_ context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	tracking := normalize(req.Tracking)
	if tracking == "" {
		return gocommerce.Shipment{}, errors.New(
			"india-post: a consignment number is required — this provider records a booking made at the counter or in the Department of Posts portal, it does not make one")
	}
	if !m.cfg.AcceptAnyNumber && !s10.MatchString(tracking) {
		// Caught here rather than a week later, when the customer follows a
		// link that goes nowhere and reads it as a lost parcel.
		return gocommerce.Shipment{}, fmt.Errorf(
			"india-post: %q is not an India Post article number — they look like EE123456789IN: two letters, nine digits, IN. Set AcceptAnyNumber for a product that numbers differently",
			req.Tracking)
	}

	m.log.Info("recorded an India Post consignment",
		"order_id", order.ID, "order_number", order.Number, "tracking", tracking)

	return gocommerce.Shipment{
		Provider: m.Code(),
		Tracking: tracking,
		Carrier:  "india-post",
	}, nil
}

// normalize strips what people paste around a number off a receipt: spaces,
// the hyphens some counters print, and the case they happened to type. It
// matches what the engine does before detecting a carrier, so a number this
// module accepts is one the engine will recognise.
func normalize(tracking string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(tracking)) {
		switch {
		case r >= '0' && r <= '9', r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		}
	}
	return b.String()
}
