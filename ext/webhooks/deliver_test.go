package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// captured is what the merchant's server saw.
type captured struct {
	mu     sync.Mutex
	bodies []string
	sigs   []string
	count  int
}

func (c *captured) record(r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	body, _ := io.ReadAll(r.Body)
	c.bodies = append(c.bodies, string(body))
	c.sigs = append(c.sigs, r.Header.Get("X-GoCommerce-Signature"))
	c.count++
}

func (c *captured) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// deliverOnce runs one pass of the worker, the way DrainOutbox runs one pass of
// the engine's.
func deliverOnce(t *testing.T, m *Module) int {
	t.Helper()
	n, err := m.DeliverPass(context.Background())
	if err != nil {
		t.Fatalf("deliver pass: %v", err)
	}
	return n
}

func TestADeliveryIsPostedAndMarkedDelivered(t *testing.T) {
	cap := &captured{}
	srv := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.WriteHeader(http.StatusOK)
	})

	mod := New(Config{})
	app := localApp(t, mod)
	e := create(t, app, map[string]any{"url": srv.URL, "events": []string{"order.*"}})

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	if n := deliverOnce(t, mod); n == 0 {
		t.Fatal("the worker delivered nothing")
	}

	if cap.calls() == 0 {
		t.Fatal("the endpoint was never called")
	}
	for _, d := range deliveries(t, app) {
		if d.State != StateDelivered {
			t.Errorf("delivery %d is %s, want %s (last error %q)", d.ID, d.State, StateDelivered, d.LastError)
		}
		if d.EndpointID != e.ID {
			t.Errorf("delivery %d went to endpoint %d, want %d", d.ID, d.EndpointID, e.ID)
		}
	}
}

// The signature is the only thing telling the merchant this really came from
// the store, so it has to verify with nothing but the secret and the body.
func TestTheRequestIsSignedWithTheEndpointSecret(t *testing.T) {
	cap := &captured{}
	srv := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.WriteHeader(http.StatusOK)
	})

	mod := New(Config{})
	app := localApp(t, mod)
	e := create(t, app, map[string]any{"url": srv.URL, "events": []string{"order.*"}})

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.sigs) == 0 {
		t.Fatal("no request captured")
	}

	ts, v1 := parseSignature(t, cap.sigs[0])
	mac := hmac.New(sha256.New, []byte(e.Secret))
	mac.Write([]byte(ts + "." + cap.bodies[0]))
	want := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(v1), []byte(want)) {
		t.Errorf("signature %s does not verify against the secret", v1)
	}

	// And the body is the event, not a translation of it.
	var env map[string]any
	if err := json.Unmarshal([]byte(cap.bodies[0]), &env); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	for _, k := range []string{"id", "name", "at", "data"} {
		if _, ok := env[k]; !ok {
			t.Errorf("body has no %q", k)
		}
	}
}

// A merchant having a bad afternoon is the normal case this is built for.
func TestAFailedDeliveryBacksOffAndStaysPending(t *testing.T) {
	srv := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	mod := New(Config{})
	app := localApp(t, mod)
	create(t, app, map[string]any{"url": srv.URL, "events": []string{"order.*"}})

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	ds := deliveries(t, app)
	if len(ds) == 0 {
		t.Fatal("no deliveries")
	}
	for _, d := range ds {
		if d.State != StatePending {
			t.Errorf("delivery %d is %s after one failure, want %s", d.ID, d.State, StatePending)
		}
		if d.Attempts != 1 {
			t.Errorf("attempts = %d, want 1", d.Attempts)
		}
		if d.LastStatus != http.StatusInternalServerError {
			t.Errorf("last_status = %d, want 500", d.LastStatus)
		}
		if !d.AvailableAt.After(time.Now()) {
			t.Errorf("available_at %v is not in the future, so there is no backoff", d.AvailableAt)
		}
	}
}

// A hook's address often carries its own key, in the path or the query, and
// Go's client error quotes the whole address. The delivery log says what
// failed without it.
func TestAFailedDeliverysErrorDoesNotQuoteTheAddress(t *testing.T) {
	mod := New(Config{})
	app := localApp(t, mod)
	insertEndpoint(t, app, "https://gc-no-such-host.invalid/hooks/PATHKEY4417?key=QUERYKEY9021")

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	ds := deliveries(t, app)
	if len(ds) == 0 {
		t.Fatal("no deliveries")
	}
	for _, d := range ds {
		if strings.Contains(d.LastError, "PATHKEY4417") || strings.Contains(d.LastError, "QUERYKEY9021") {
			t.Errorf("delivery %d last_error = %q, want no path or query", d.ID, d.LastError)
		}
		if !strings.Contains(d.LastError, "Post") || !strings.Contains(d.LastError, "gc-no-such-host.invalid") {
			t.Errorf("delivery %d last_error = %q, want what failed and which host", d.ID, d.LastError)
		}
	}
}

// A delivery that failed before the address was taken out keeps the cause.
func TestAnOlderErrorLosesTheAddressAndKeepsTheCause(t *testing.T) {
	app := localApp(t, New(Config{}))
	insertEndpoint(t, app, "https://hooks.example.com/hooks")
	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	ctx := gocommerce.WithStore(context.Background(), app.DefaultStore())
	if _, err := app.DB().ExecContext(ctx, `UPDATE webhook_deliveries SET last_error =
		'Post "https://hooks.example.com/hooks/PATHKEY4417?key=QUERYKEY9021": dial tcp: lookup hooks.example.com: no such host'`); err != nil {
		t.Fatal(err)
	}
	if err := gocommerce.InTx(ctx, app.DB(), func(tx *sql.Tx) error { return dropAddressFromOldErrors(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	ds := deliveries(t, app)
	if len(ds) == 0 {
		t.Fatal("no deliveries")
	}
	for _, d := range ds {
		if want := "Post: dial tcp: lookup hooks.example.com: no such host"; d.LastError != want {
			t.Errorf("delivery %d last_error = %q, want %q", d.ID, d.LastError, want)
		}
	}
}

// Giving up has to be visible. A delivery nobody could make is evidence, and
// evidence should survive long enough to be looked at.
func TestDeliveryGivesUpVisiblyAndCanBeRetriedByHand(t *testing.T) {
	srv := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	mod := New(Config{})
	app := localApp(t, mod)
	create(t, app, map[string]any{"url": srv.URL, "events": []string{"order.*"}})

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)

	// Exhaust the attempts, making each one due immediately so the test does
	// not wait out a real backoff.
	for i := 0; i < maxAttempts; i++ {
		if _, err := app.DB().ExecContext(context.Background(),
			`UPDATE webhook_deliveries SET available_at = now()`); err != nil {
			t.Fatalf("make due: %v", err)
		}
		deliverOnce(t, mod)
	}

	ds := deliveries(t, app)
	if len(ds) == 0 {
		t.Fatal("no deliveries")
	}
	dead := ds[0]
	if dead.State != StateDead {
		t.Fatalf("after %d attempts the delivery is %s, want %s", maxAttempts, dead.State, StateDead)
	}

	rec := gctest.AdminRequest(t, app, "POST",
		"/api/admin/x/webhooks/deliveries/"+itoa(dead.ID)+"/retry", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry: status %d, body %s", rec.Code, rec.Body.String())
	}
	after := deliveries(t, app)
	if after[0].State != StatePending {
		t.Errorf("after a hand retry the delivery is %s, want %s", after[0].State, StatePending)
	}
}

// A delivery already made is not made again, whatever else runs.
func TestADeliveredRowIsNotSentTwice(t *testing.T) {
	cap := &captured{}
	srv := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.WriteHeader(http.StatusOK)
	})

	mod := New(Config{})
	app := localApp(t, mod)
	create(t, app, map[string]any{"url": srv.URL, "events": []string{"order.*"}})

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)
	first := cap.calls()

	deliverOnce(t, mod)
	if cap.calls() != first {
		t.Errorf("a second pass re-sent a delivered row: %d calls, want %d", cap.calls(), first)
	}
}

func parseSignature(t *testing.T, header string) (ts, v1 string) {
	t.Helper()
	if header == "" {
		t.Fatal("no X-GoCommerce-Signature header")
	}
	for _, part := range strings.Split(header, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch k {
		case "t":
			ts = v
		case "v1":
			v1 = v
		}
	}
	if ts == "" || v1 == "" {
		t.Fatalf("signature %q has no t= or v1=", header)
	}
	return ts, v1
}
