package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A provider that is installed but not set up is listed and offered nowhere.

type idleGateway struct{ ready bool }

func (g *idleGateway) Code() string                    { return "idle-pay" }
func (g *idleGateway) DisplayName() string             { return "Idle Pay" }
func (g *idleGateway) Configured(context.Context) bool { return g.ready }
func (g *idleGateway) Initiate(context.Context, *Order, PayOptions) (PaymentIntent, error) {
	return PaymentIntent{Kind: "none"}, nil
}

type idleCarrier struct{ ready bool }

func (c *idleCarrier) Code() string                    { return "idle-ship" }
func (c *idleCarrier) Configured(context.Context) bool { return c.ready }
func (c *idleCarrier) Ship(_ context.Context, _ *Order, req ShipRequest) (Shipment, error) {
	return Shipment{Tracking: "IDLE-1"}, nil
}

type idleModule struct {
	gateway *idleGateway
	carrier *idleCarrier
}

func (m *idleModule) Name() string            { return "idle-test" }
func (m *idleModule) Migrations() []Migration { return nil }
func (m *idleModule) Register(app *App) error {
	app.RegisterPayment(m.gateway)
	app.RegisterFulfillment(m.carrier)
	return nil
}

func TestIdleProvidersAreListedButNotOffered(t *testing.T) {
	mod := &idleModule{gateway: &idleGateway{}, carrier: &idleCarrier{}}
	app := newTestApp(t, mod)
	ctx := context.Background()

	// The settings list both, and say they are not set up.
	var payInfo, shipInfo *ProviderInfo
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == "idle-pay" {
			p := p
			payInfo = &p
		}
	}
	for _, p := range app.Settings().FulfillmentProviders {
		if p.Code == "idle-ship" {
			p := p
			shipInfo = &p
		}
	}
	if payInfo == nil || shipInfo == nil {
		t.Fatal("idle providers should still be listed in the settings")
	}
	if payInfo.Configured || shipInfo.Configured {
		t.Fatalf("configured = %v/%v before setup", payInfo.Configured, shipInfo.Configured)
	}
	// The built-ins say the opposite.
	for _, p := range app.Settings().PaymentMethods {
		if p.Code == CodeCOD && !p.Configured {
			t.Fatal("cash on delivery is always configured")
		}
	}

	// Shoppers are not offered the gateway, and cannot use it by name.
	for _, code := range app.Pay().Methods() {
		if code == "idle-pay" {
			t.Fatal("an idle gateway must not be in Methods()")
		}
	}
	rec := do(t, app, http.MethodGet, "/api/checkout")
	if strings.Contains(rec.Body.String(), "idle-pay") {
		t.Fatalf("the public checkout listing should not carry the idle gateway: %s", rec.Body)
	}
	// Refused before the input is even looked at, with the same words an
	// unknown code gets: a storefront cannot tell idle from absent.
	if _, err := app.Order().Checkout(ctx, "idle-pay", CheckoutInput{}, ""); err == nil || !strings.Contains(err.Error(), "no payment method") {
		t.Fatalf("checkout through an idle gateway = %v, want it refused as unknown", err)
	}

	// An operator cannot ship through the idle carrier.
	order := placeOrder(t, app, "IDLE-1")
	if _, err := app.Ship().Create(ctx, order.ID, "idle-ship", ShipRequest{}); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("shipping through an idle carrier = %v, want a refusal that says it is not set up", err)
	}

	// Set up, both work — with no restart in between.
	mod.gateway.ready = true
	mod.carrier.ready = true
	offered := false
	for _, code := range app.Pay().Methods() {
		offered = offered || code == "idle-pay"
	}
	if !offered {
		t.Fatal("once configured, the gateway is offered")
	}
	if _, err := app.Ship().Create(ctx, order.ID, "idle-ship", ShipRequest{}); err != nil {
		t.Fatalf("shipping through the configured carrier: %v", err)
	}
	for _, p := range app.Settings().FulfillmentProviders {
		if p.Code == "idle-ship" && !p.Configured {
			t.Fatal("once configured, the settings say so")
		}
	}
}

// Plugins.Fill lays the panel's settings over a Config by tag.

type fillNested struct {
	Line1   string `plugin:"from_line1"`
	Country string `plugin:"from_country"`
}

type fillConfig struct {
	Key      string  `plugin:"api_key"`
	Mode     string  `plugin:"mode"`
	Grams    int     `plugin:"grams"`
	Ratio    float64 `plugin:"ratio"`
	Sandbox  bool    `plugin:"sandbox"`
	Untagged string
	From     fillNested
}

func TestPluginsFill(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	if err := app.plugins.register(PluginDef{
		Key: "fill-test", Title: "Fill test", Category: "integration",
		Fields: []PluginField{
			{Key: "api_key", Label: "Key", Kind: "secret"},
			{Key: "mode", Label: "Mode", Kind: "select", Options: []string{"a", "b"}, Default: "a"},
			{Key: "grams", Label: "Grams", Kind: "number", Default: 500},
			{Key: "ratio", Label: "Ratio", Kind: "number"},
			{Key: "sandbox", Label: "Sandbox", Kind: "bool"},
			{Key: "from_line1", Label: "Line 1", Kind: "text"},
			{Key: "from_country", Label: "Country", Kind: "text"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	// Nothing stored: Config's own values stand, and defaults fill the empty.
	c := fillConfig{Key: "from-code", Grams: 0, Untagged: "keep"}
	if err := app.Plugins().Fill(ctx, "fill-test", &c); err != nil {
		t.Fatal(err)
	}
	if c.Key != "from-code" || c.Mode != "a" || c.Grams != 500 || c.Untagged != "keep" {
		t.Fatalf("after Fill with nothing stored: %+v", c)
	}

	// Stored values win, typed by kind, nested structs included; an empty
	// stored value leaves Config alone.
	on := true
	if _, err := app.Plugins().Update(ctx, "fill-test", PluginPatch{Enabled: &on, Settings: map[string]any{
		"api_key": "from-panel", "mode": "b", "grams": 750, "ratio": "1.5", "sandbox": true,
		"from_line1": " 1 Ship St ", "from_country": "",
	}}); err != nil {
		t.Fatal(err)
	}
	c = fillConfig{Key: "from-code", From: fillNested{Country: "SG"}}
	if err := app.Plugins().Fill(ctx, "fill-test", &c); err != nil {
		t.Fatal(err)
	}
	if c.Key != "from-panel" || c.Mode != "b" || c.Grams != 750 || c.Ratio != 1.5 || !c.Sandbox {
		t.Fatalf("after Fill: %+v", c)
	}
	if c.From.Line1 != "1 Ship St" || c.From.Country != "SG" {
		t.Fatalf("nested after Fill: %+v", c.From)
	}

	// A wrong shape is refused rather than silently zeroed.
	if err := app.Plugins().Fill(ctx, "fill-test", c); err == nil {
		t.Fatal("Fill into a value rather than a pointer should be refused")
	}
	if err := app.Plugins().Fill(ctx, "nothing", &c); err == nil {
		t.Fatal("Fill for an unknown plugin should be refused")
	}
}
