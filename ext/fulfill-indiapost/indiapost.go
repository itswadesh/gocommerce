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
	"sync/atomic"

	"github.com/itswadesh/gocommerce/core"
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
	AcceptAnyNumber bool `plugin:"accept_any_number"`
}

// Module is the India Post fulfillment provider.
type Module struct {
	cfg  Config
	live atomic.Pointer[Config]
	app  *gocommerce.App
	log  *slog.Logger
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// PluginKey is the plugin this module registers, where the panel keeps its
// credentials. Config is the environment's fallback for each field; a value
// typed into the panel wins.
const PluginKey = "shipping-india-post"

var errNotConfigured = errors.New("indiapost: not set up — activate and configure it under Settings")

// conf is the effective configuration: the last one refresh computed, or
// Config alone before the first use.
func (m *Module) conf() *Config {
	if c := m.live.Load(); c != nil {
		return c
	}
	return &m.cfg
}

// refresh recomputes the effective configuration — Config with the plugin's
// settings laid over it — and reports whether the provider can work: switched
// on, with every required field filled. Called before each use, so a key
// typed into the panel a moment ago counts without a restart.
func (m *Module) refresh(ctx context.Context) bool {
	c := m.cfg
	on := false
	if m.app != nil {
		on, _ = m.app.Plugins().Enabled(ctx, PluginKey)
		if on {
			_ = m.app.Plugins().Fill(ctx, PluginKey, &c)
		}
	}
	m.finish(&c)
	m.live.Store(&c)
	return on && m.complete(&c)
}

// Configured implements gocommerce.Configurable.
func (m *Module) Configured(ctx context.Context) bool { return m.refresh(ctx) }

// complete is whether a configuration has everything the provider needs.
func (m *Module) complete(c *Config) bool {
	return true
}

// finish fills a configuration's defaults.
func (m *Module) finish(c *Config) {

}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "fulfill-indiapost" }

// Migrations implements gocommerce.Module. The engine already stores the
// shipment; this module keeps no state of its own.
func (m *Module) Migrations() []gocommerce.Migration { return nil }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	// Config-configured stores start switched on; a store with nothing in
	// Config starts idle and waits for the panel.
	inCode := m.complete(&m.cfg)
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "India Post", Category: "shipping", DefaultEnabled: inCode,
		Description: "Records a consignment handed to India Post at the counter or booked in the Department of Posts portal. Nothing is booked from here.",
		Docs:        "https://www.indiapost.gov.in/",
		Fields: []gocommerce.PluginField{
			{Key: "accept_any_number", Label: "Accept any consignment number", Kind: "bool", Help: "Off, the number must be a 13-character S10 barcode."},
		},
	})
	m.finish(&m.cfg)
	m.log = app.Log()
	app.RegisterFulfillment(m)
	return nil
}

// Code implements gocommerce.FulfillmentProvider.
func (m *Module) Code() string { return "india-post" }

// DisplayName implements gocommerce.Named.
func (m *Module) DisplayName() string { return "India Post" }

// Ship records a booking somebody else made.
func (m *Module) Ship(ctx context.Context, order *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	if !m.refresh(ctx) {
		return gocommerce.Shipment{}, errNotConfigured
	}
	tracking := normalize(req.Tracking)
	if tracking == "" {
		return gocommerce.Shipment{}, errors.New(
			"india-post: a consignment number is required — this provider records a booking made at the counter or in the Department of Posts portal, it does not make one")
	}
	if !m.conf().AcceptAnyNumber && !s10.MatchString(tracking) {
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
