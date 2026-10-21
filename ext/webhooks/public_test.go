package webhooks

import (
	"context"
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// localApp is an install whose operators run the server, so a receiver on this
// machine — an httptest server, in practice — can be an endpoint.
func localApp(t *testing.T, mod *Module) *gocommerce.App {
	t.Helper()
	return gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true}, mod)
}

// The payload is a shopper's order, so a new endpoint has to be https; plain
// http would carry it across the internet in the clear.
func TestANewEndpointMustBeHTTPS(t *testing.T) {
	app := newApp(t)
	rec := gctest.AdminRequest(t, app, "POST", "/api/admin/x/webhooks/endpoints",
		map[string]any{"url": "http://merchant.example.com/hooks", "events": []string{"order.*"}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("an http endpoint got status %d, want 400; body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "https://") {
		t.Errorf("refusal %s does not say to use https://", rec.Body.String())
	}
}

// An endpoint is somewhere the server posts to, so on an install that hosts
// other people's stores it has to be on the public internet.
func TestANewEndpointMustBePublic(t *testing.T) {
	app := newApp(t)
	for _, url := range []string{
		"https://127.0.0.1/hooks",
		"https://localhost:8443/hooks",
		"https://10.0.0.5/hooks",
		"https://169.254.169.254/hooks",
		"https://[::1]/hooks",
		"https://billing.internal/hooks",
	} {
		rec := gctest.AdminRequest(t, app, "POST", "/api/admin/x/webhooks/endpoints",
			map[string]any{"url": url, "events": []string{"order.*"}})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("url %q got status %d, want 400", url, rec.Code)
			continue
		}
		if !strings.Contains(rec.Body.String(), "not a public address") {
			t.Errorf("url %q: refusal %s does not say why", url, rec.Body.String())
		}
	}
}

// Editing the address is held to the same rule as creating one; repeating the
// saved address alongside another change is not an edit of it.
func TestAnEditedEndpointIsHeldToTheSameRule(t *testing.T) {
	app := newApp(t)
	e := create(t, app, map[string]any{"url": "https://merchant.example.com/hooks", "events": []string{"order.*"}})
	path := "/api/admin/x/webhooks/endpoints/" + itoa(e.ID)

	for _, url := range []string{"http://merchant.example.com/hooks", "https://10.0.0.5/hooks"} {
		rec := gctest.AdminRequest(t, app, "PATCH", path, map[string]any{"url": url})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH url %q got status %d, want 400", url, rec.Code)
		}
	}

	rec := gctest.AdminRequest(t, app, "PATCH", path,
		map[string]any{"url": "https://merchant.example.com/hooks", "events": []string{"cart.*"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("repeating the saved url: status %d, body %s", rec.Code, rec.Body.String())
	}
}

// An endpoint saved with plain http before the rule — or on a private address,
// while an install allowed it — is not refused when it is next edited for
// something else: refusing it would stop an integration nobody touched.
func TestAnOlderEndpointCanStillBeEdited(t *testing.T) {
	app := newApp(t)
	id := insertEndpoint(t, app, "http://merchant.example.com/hooks")

	rec := gctest.AdminRequest(t, app, "PATCH", "/api/admin/x/webhooks/endpoints/"+itoa(id),
		map[string]any{"url": "http://merchant.example.com/hooks", "active": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("editing an older http endpoint: status %d, body %s", rec.Code, rec.Body.String())
	}
}

// With private addresses allowed, a receiver on this machine over plain http
// is an ordinary endpoint.
func TestALocalEndpointIsFineWhereTheInstallAllowsIt(t *testing.T) {
	app := localApp(t, New(Config{}))
	create(t, app, map[string]any{"url": "http://127.0.0.1:9000/hooks", "events": []string{"order.*"}})
}

// The save-time check reads only the URL. Whatever got into the table — an
// endpoint saved before this rule, or a name that resolves somewhere private —
// meets the same refusal when the worker connects, and the refusal is in the
// delivery log like any other failure.
func TestADeliveryToAPrivateAddressIsRefusedAndLogged(t *testing.T) {
	cap := &captured{}
	srv := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.WriteHeader(http.StatusOK)
	})

	mod := New(Config{})
	app := gctest.New(t, mod)
	insertEndpoint(t, app, srv.URL+"/hooks")

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	if n := deliverOnce(t, mod); n == 0 {
		t.Fatal("the worker attempted nothing")
	}

	if cap.calls() != 0 {
		t.Fatalf("the private endpoint was reached %d times", cap.calls())
	}
	ds := deliveries(t, app)
	if len(ds) == 0 {
		t.Fatal("no deliveries recorded")
	}
	for _, d := range ds {
		if d.State != StatePending || d.Attempts != 1 {
			t.Errorf("delivery %d is %s after %d attempts, want pending after 1", d.ID, d.State, d.Attempts)
		}
		if !strings.Contains(d.LastError, "127.0.0.1 is not a public address") {
			t.Errorf("delivery %d last_error = %q, want the refusal", d.ID, d.LastError)
		}
	}
}

// insertEndpoint writes an endpoint the way an earlier release could have
// saved it, past today's checks.
func insertEndpoint(t *testing.T, app *gocommerce.App, url string) int64 {
	t.Helper()
	var id int64
	if err := app.DB().QueryRowContext(context.Background(), `
		INSERT INTO webhook_endpoints (url, secret, events)
		VALUES ($1, 'whsec_test', '{order.*}') RETURNING id`, url).Scan(&id); err != nil {
		t.Fatalf("insert endpoint: %v", err)
	}
	return id
}
