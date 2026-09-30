package webhooks

import (
	"net/http"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func newApp(t *testing.T) *gocommerce.App {
	t.Helper()
	return gctest.New(t, New(Config{}))
}

// create makes an endpoint and returns the decoded response.
func create(t *testing.T, app *gocommerce.App, body map[string]any) Endpoint {
	t.Helper()
	rec := gctest.AdminRequest(t, app, "POST", "/api/admin/x/webhooks/endpoints", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create endpoint: status %d, body %s", rec.Code, rec.Body.String())
	}
	var e Endpoint
	gctest.DecodeData(t, rec, &e)
	return e
}

// The secret is the whole security model, and the only moment it can be handed
// over is the one that generates it.
func TestCreatingAnEndpointReturnsItsSecretOnce(t *testing.T) {
	app := newApp(t)

	e := create(t, app, map[string]any{
		"url":    "https://merchant.example.com/hooks",
		"events": []string{"order.*"},
	})

	if e.Secret == "" {
		t.Fatal("create returned no secret")
	}
	if e.ID == 0 {
		t.Error("create returned no id")
	}
	if !e.Active {
		t.Error("a new endpoint should be active")
	}

	rec := gctest.AdminRequest(t, app, "GET", "/api/admin/x/webhooks/endpoints/"+itoa(e.ID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("read back: status %d", rec.Code)
	}
	var got Endpoint
	gctest.DecodeData(t, rec, &got)
	if got.Secret != "" {
		t.Errorf("reading an endpoint back returned the secret %q, want it withheld", got.Secret)
	}
	if got.SecretHint == "" {
		t.Error("no secret hint, so an operator cannot tell two endpoints apart")
	}
}

// A URL is where somebody else's server is. Refusing a bad one at the door
// beats discovering it twelve delivery attempts later.
func TestABadEndpointURLIsRefused(t *testing.T) {
	app := newApp(t)
	for _, url := range []string{"", "merchant.example.com/hooks", "ftp://merchant.example.com"} {
		rec := gctest.AdminRequest(t, app, "POST", "/api/admin/x/webhooks/endpoints",
			map[string]any{"url": url, "events": []string{"order.*"}})
		if rec.Code < 400 {
			t.Errorf("url %q accepted with status %d, want a refusal", url, rec.Code)
		}
	}
}

// An endpoint that names no events would either hear everything or nothing, and
// both are surprises worth refusing.
func TestAnEndpointMustNameAtLeastOneEvent(t *testing.T) {
	app := newApp(t)
	rec := gctest.AdminRequest(t, app, "POST", "/api/admin/x/webhooks/endpoints",
		map[string]any{"url": "https://merchant.example.com/hooks", "events": []string{}})
	if rec.Code < 400 {
		t.Fatalf("an endpoint with no events was accepted with status %d", rec.Code)
	}
}

// The fan-out: one event becomes one delivery per endpoint that asked for it.
func TestAnEventBecomesOneDeliveryPerMatchingEndpoint(t *testing.T) {
	app := newApp(t)

	wanted := create(t, app, map[string]any{
		"url": "https://a.example.com/hooks", "events": []string{"order.*"}})
	create(t, app, map[string]any{
		"url": "https://b.example.com/hooks", "events": []string{"cart.abandoned"}})

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)

	ds := deliveries(t, app)
	if len(ds) == 0 {
		t.Fatal("no deliveries recorded for order.created")
	}
	for _, d := range ds {
		if d.EndpointID != wanted.ID {
			t.Errorf("delivery %d went to endpoint %d, want only %d", d.ID, d.EndpointID, wanted.ID)
		}
		if d.EventName == "" {
			t.Errorf("delivery %d records no event name", d.ID)
		}
	}
}

// An endpoint somebody switched off is switched off.
func TestAnInactiveEndpointGetsNothing(t *testing.T) {
	app := newApp(t)
	e := create(t, app, map[string]any{
		"url": "https://a.example.com/hooks", "events": []string{"order.*"}, "active": false})
	if e.Active {
		t.Fatal("active:false was ignored on create")
	}

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)

	if ds := deliveries(t, app); len(ds) != 0 {
		t.Fatalf("inactive endpoint collected %d deliveries, want 0", len(ds))
	}
}

// Writing a delivery row must not be able to fail the event for everybody else:
// dispatch joins subscriber errors and the outbox retries the whole event, so a
// webhook problem would re-run every other consumer. Nothing here should leave
// the outbox holding the event.
func TestDeliveryFanOutDoesNotHoldUpTheOutbox(t *testing.T) {
	app := newApp(t)
	create(t, app, map[string]any{
		"url": "https://a.example.com/hooks", "events": []string{"*"}})

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	gctest.AssertOutboxEmpty(t, app)
}

func TestModuleRoutesDeclareRightsAndAreDocumented(t *testing.T) {
	app := newApp(t)
	gctest.AssertAdminRoutesDeclareRights(t, app, "webhooks")
	gctest.AssertSpecCoversModuleRoutes(t, app, "webhooks")
}

func deliveries(t *testing.T, app *gocommerce.App) []Delivery {
	t.Helper()
	rec := gctest.AdminRequest(t, app, "GET", "/api/admin/x/webhooks/deliveries", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list deliveries: status %d, body %s", rec.Code, rec.Body.String())
	}
	var out []Delivery
	gctest.DecodeData(t, rec, &out)
	return out
}

func itoa(id int64) string {
	const digits = "0123456789"
	if id == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for id > 0 {
		i--
		b[i] = digits[id%10]
		id /= 10
	}
	return string(b[i:])
}
