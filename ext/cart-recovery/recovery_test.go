package cartrecovery

import (
	"context"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// abandonOne builds a cart holding something, puts an address on it, and runs
// the sweep that gives up on it. CartTTL is a millisecond in these tests, so
// "expired" is true by the time the sweep looks.
func abandonOne(t *testing.T, app *gocommerce.App, email string) string {
	t.Helper()
	ctx := context.Background()

	v := gctest.CreateProduct(t, app, "RECOVER-1", 2500, 5)
	cart, err := app.Cart().Create(ctx, email)
	if err != nil {
		t.Fatalf("create cart: %v", err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, v.Variants[0].ID, 2); err != nil {
		t.Fatalf("add line: %v", err)
	}
	// AddLine revives the cart it touches, so the email goes on afterwards and
	// the TTL is re-stamped from the last mutation either way.
	if email != "" {
		if _, err := app.Cart().SetEmail(ctx, cart.Token, email); err != nil {
			t.Fatalf("set email: %v", err)
		}
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := app.Cart().Abandon(ctx); err != nil {
		t.Fatalf("abandon: %v", err)
	}
	gctest.DrainOutbox(t, app)
	return cart.Token
}

func testConfig() gocommerce.Config {
	return gocommerce.Config{CartTTL: time.Millisecond}
}

// The whole point: a basket someone gave up on, and a link back to it.
func TestAnAbandonedCartIsChasedWithALinkBackToIt(t *testing.T) {
	rec := &gctest.RecordingNotifier{}
	mod := New(Config{StorefrontURL: "https://shop.example.com"})
	app := gctest.NewWithConfig(t, testConfig(), mod, notifierModule{rec})

	token := abandonOne(t, app, "shopper@example.com")

	if got := rec.Count(gocommerce.EventCartAbandoned); got != 1 {
		t.Fatalf("cart.abandoned notifications = %d, want 1", got)
	}
	n := rec.All()[0]
	if n.To != "shopper@example.com" {
		t.Errorf("To = %q, want shopper@example.com", n.To)
	}
	if want := "https://shop.example.com/cart/" + token; n.Data["recovery_url"] != want {
		t.Errorf("recovery_url = %q, want %q", n.Data["recovery_url"], want)
	}
	if n.Data["cart_token"] != token {
		t.Errorf("cart_token = %q, want %q", n.Data["cart_token"], token)
	}
	if n.Data["item_count"] != "2" {
		t.Errorf("item_count = %q, want 2", n.Data["item_count"])
	}
	if n.Data["subtotal_minor"] != "5000" {
		t.Errorf("subtotal_minor = %q, want 5000", n.Data["subtotal_minor"])
	}
}

// There is nowhere to send it, and no way for the module to find out where.
func TestACartWithNoAddressIsNotChased(t *testing.T) {
	rec := &gctest.RecordingNotifier{}
	app := gctest.NewWithConfig(t, testConfig(),
		New(Config{StorefrontURL: "https://shop.example.com"}), notifierModule{rec})

	abandonOne(t, app, "")

	if got := rec.Count(gocommerce.EventCartAbandoned); got != 0 {
		t.Fatalf("notifications for a cart with no email = %d, want 0", got)
	}
}

// Installing this module must not mail everyone whose basket went stale months
// ago. The engine's own note on subscribeNotifications names that as the reason
// core does not do this itself, so the module that does has to answer it.
func TestCartsAbandonedBeforeInstallAreLeftAlone(t *testing.T) {
	rec := &gctest.RecordingNotifier{}
	mod := New(Config{StorefrontURL: "https://shop.example.com"})
	// Installed "now", but told it started tomorrow: every cart the sweep finds
	// was abandoned before this module was watching.
	mod.startedAt = time.Now().Add(24 * time.Hour)
	app := gctest.NewWithConfig(t, testConfig(), mod, notifierModule{rec})

	abandonOne(t, app, "shopper@example.com")

	if got := rec.Count(gocommerce.EventCartAbandoned); got != 0 {
		t.Fatalf("notifications for a cart abandoned before install = %d, want 0", got)
	}
}

// A store that has not said where its storefront is still gets the token, and
// its template branches on the absence exactly as the password-reset one does.
func TestWithoutAStorefrontURLTheTokenTravelsAlone(t *testing.T) {
	rec := &gctest.RecordingNotifier{}
	app := gctest.NewWithConfig(t, testConfig(), New(Config{}), notifierModule{rec})

	token := abandonOne(t, app, "shopper@example.com")

	if got := rec.Count(gocommerce.EventCartAbandoned); got != 1 {
		t.Fatalf("cart.abandoned notifications = %d, want 1", got)
	}
	n := rec.All()[0]
	if _, ok := n.Data["recovery_url"]; ok {
		t.Errorf("recovery_url = %q, want it absent", n.Data["recovery_url"])
	}
	if n.Data["cart_token"] != token {
		t.Errorf("cart_token = %q, want %q", n.Data["cart_token"], token)
	}
}

// A storefront URL that is not a URL is a configuration mistake worth refusing
// at startup rather than discovering in somebody's inbox.
func TestABadStorefrontURLIsRefusedAtStartup(t *testing.T) {
	if err := New(Config{StorefrontURL: "shop.example.com"}).Register(nil); err == nil {
		t.Fatal("a StorefrontURL with no scheme was accepted, want an error")
	}
}

// notifierModule installs the recorder through the public module surface, the
// same way a real store installs ext/notify-sendgrid.
type notifierModule struct{ rec *gctest.RecordingNotifier }

func (notifierModule) Name() string                       { return "recorder" }
func (notifierModule) Migrations() []gocommerce.Migration { return nil }
func (m notifierModule) Register(app *gocommerce.App) error {
	app.RegisterNotifier(gocommerce.ChannelEmail, m.rec)
	return nil
}
