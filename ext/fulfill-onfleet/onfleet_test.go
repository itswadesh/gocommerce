package onfleet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// task is the part of the payload this package is responsible for getting
// right.
type task struct {
	Destination struct {
		Address map[string]any `json:"address"`
	} `json:"destination"`
	Recipients []map[string]any `json:"recipients"`
	Notes      string           `json:"notes"`
	Quantity   int              `json:"quantity"`
	AutoAssign map[string]any   `json:"autoAssign"`
	Metadata   []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"metadata"`
}

type stub struct {
	mu       sync.Mutex
	last     task
	auth     string
	failWith string
}

func (s *stub) set(tk task, auth string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last, s.auth = tk, auth
}

func (s *stub) get() (task, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.last, s.auth
}

func newApp(t *testing.T, cfg Config, shape func(*stub)) (*gocommerce.App, *stub) {
	t.Helper()
	st := &stub{}
	if shape != nil {
		shape(st)
	}
	server := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/v2/tasks" {
			http.NotFound(w, r)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var tk task
		_ = json.Unmarshal(body, &tk)
		st.set(tk, r.Header.Get("Authorization"))

		st.mu.Lock()
		failWith := st.failWith
		st.mu.Unlock()
		if failWith != "" {
			http.Error(w, `{"message":{"error":1702,"message":"`+failWith+`"}}`, http.StatusBadRequest)
			return
		}
		_, _ = io.WriteString(w, `{"id":"11z5jsUXbaHZ5eLGPgbGJ8Ca","shortId":"5e9e4a1b","trackingURL":"https://onf.lt/5e9e4a1b","state":0}`)
	})

	cfg.APIKey = "onfleet_test"
	cfg.BaseURL = server.URL + "/api/v2"
	return gctest.New(t, New(cfg)), st
}

// Onfleet authenticates with the key as a Basic username and an empty
// password. Sending it as a bearer is a 401 that reads like a bad key.
func TestAuthIsBasicWithAnEmptyPassword(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "OF-1", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
	gctest.Ship(t, app, result.Order.ID, "onfleet", gocommerce.ShipRequest{})

	_, auth := st.get()
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("onfleet_test:"))
	if auth != want {
		t.Errorf("Authorization = %q, want %q", auth, want)
	}
}

// The module's reason to exist: a task a driver can act on, recorded against
// the order as something an operator can find again.
func TestTaskCarriesTheAddressAndTheOrder(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "OF-2", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 2)

	order := gctest.Ship(t, app, result.Order.ID, "onfleet", gocommerce.ShipRequest{})
	shipment := order.Fulfillments[0]
	// The shortId, not the long id: it is what an operator types into the
	// dashboard.
	if shipment.Tracking != "5e9e4a1b" {
		t.Errorf("tracking = %q, want the task shortId", shipment.Tracking)
	}
	if shipment.Carrier != "onfleet" {
		t.Errorf("carrier = %q, want onfleet", shipment.Carrier)
	}
	// Onfleet issues no label, and claiming one would put a tracking page
	// behind a button that says "print".
	if shipment.LabelURL != "" {
		t.Errorf("label = %q, want none — Onfleet issues no label", shipment.LabelURL)
	}

	sent, _ := st.get()
	if sent.Destination.Address["street"] != "1 Test Street" {
		t.Errorf("street = %v, want the order's", sent.Destination.Address["street"])
	}
	if sent.Destination.Address["postalCode"] != "94117" {
		t.Errorf("postalCode = %v, want the order's", sent.Destination.Address["postalCode"])
	}
	if len(sent.Recipients) != 1 || sent.Recipients[0]["phone"] != "+15555550123" {
		t.Errorf("recipients = %+v, want one carrying the order's phone", sent.Recipients)
	}
	if sent.Quantity != 2 {
		t.Errorf("quantity = %d, want 2", sent.Quantity)
	}

	byName := map[string]string{}
	for _, m := range sent.Metadata {
		byName[m.Name] = m.Value
	}
	if byName["order_number"] != result.Order.Number {
		t.Errorf("metadata order_number = %q, want the order number", byName["order_number"])
	}
}

// The driver is the one who will be asked for money, or will forget to ask, so
// it has to be the first thing they read.
func TestUnpaidOrderTellsTheDriverToCollect(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "OF-3", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
	gctest.Ship(t, app, result.Order.ID, "onfleet", gocommerce.ShipRequest{})

	sent, _ := st.get()
	if !strings.HasPrefix(sent.Notes, "COLLECT ") {
		t.Errorf("notes = %q, want it to open with what to collect", sent.Notes)
	}
	if !strings.Contains(sent.Notes, "25.00") {
		t.Errorf("notes = %q, want the amount in it", sent.Notes)
	}
}

func TestPaidOrderDoesNotAskForMoney(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "OF-4", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
	if _, err := app.Pay().MarkPaid(t.Context(), result.Order.ID, "ref"); err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	gctest.Ship(t, app, result.Order.ID, "onfleet", gocommerce.ShipRequest{})

	sent, _ := st.get()
	if strings.Contains(sent.Notes, "COLLECT") {
		t.Errorf("notes = %q — a driver must not ask a customer who has paid", sent.Notes)
	}
}

// Assigning somebody's next two hours is a decision, so it is off unless asked
// for.
func TestAutoAssignIsOffByDefault(t *testing.T) {
	app, st := newApp(t, Config{}, nil)
	product := gctest.CreateProduct(t, app, "OF-5", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)
	gctest.Ship(t, app, result.Order.ID, "onfleet", gocommerce.ShipRequest{})
	if sent, _ := st.get(); sent.AutoAssign != nil {
		t.Errorf("autoAssign = %+v, want none by default", sent.AutoAssign)
	}

	app2, st2 := newApp(t, Config{AutoAssign: true, TeamID: "team-1"}, nil)
	p2 := gctest.CreateProduct(t, app2, "OF-6", 2500, 10)
	r2 := gctest.Buy(t, app2, gocommerce.CodeCOD, p2.DefaultVariant().ID, 1)
	gctest.Ship(t, app2, r2.Order.ID, "onfleet", gocommerce.ShipRequest{})
	sent, _ := st2.get()
	if sent.AutoAssign == nil || sent.AutoAssign["team"] != "team-1" {
		t.Errorf("autoAssign = %+v, want the configured team", sent.AutoAssign)
	}
}

func TestErrorsAreReported(t *testing.T) {
	app, _ := newApp(t, Config{}, func(s *stub) { s.failWith = "The address could not be geocoded" })
	product := gctest.CreateProduct(t, app, "OF-7", 2500, 10)
	result := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1)

	_, err := app.Ship().Create(t.Context(), result.Order.ID, "onfleet", gocommerce.ShipRequest{})
	if err == nil || !strings.Contains(err.Error(), "geocoded") {
		t.Errorf("error = %v, want Onfleet's own reason in it", err)
	}
}

// The carrier code has to exist in the engine's registry, or a stored shipment
// renders with no name at all.
func TestOnfleetIsAKnownCarrier(t *testing.T) {
	t.Parallel()
	carrier, ok := gocommerce.CarrierByCode("onfleet", "5e9e4a1b")
	if !ok {
		t.Fatal("the engine does not know an onfleet carrier")
	}
	if carrier.Name != "Onfleet" {
		t.Errorf("name = %q, want Onfleet", carrier.Name)
	}
	// Deliberately none: normalising a tracking number upper-cases it, and an
	// upper-cased Onfleet task id does not resolve.
	if carrier.TrackURL != "" {
		t.Errorf("track URL = %q, want none", carrier.TrackURL)
	}
}

// TestInstalledIdleUntilConfigured: with nothing in Config the module still
// registers — the panel lists it — but the settings say it is not set up and
// nothing can be sent through it, until its fields are typed into the plugin.
func TestInstalledIdleUntilConfigured(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()

	var info *gocommerce.ProviderInfo
	for _, p := range app.Settings().FulfillmentProviders {
		if p.Code == "onfleet" {
			p := p
			info = &p
		}
	}
	if info == nil {
		t.Fatal("an idle module should still be listed in the settings")
	}
	if info.Configured {
		t.Fatalf("configured = %v before anything was typed in", info.Configured)
	}
	result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	if _, err := app.Ship().Create(ctx, result.Order.ID, "onfleet", gocommerce.ShipRequest{Tracking: "T-1"}); err == nil || !strings.Contains(err.Error(), "not set up") {
		t.Fatalf("shipping through an idle provider = %v, want a refusal that says it is not set up", err)
	}

	on := true
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"api_key": "k-1"}}); err != nil {
		t.Fatalf("configure from the panel: %v", err)
	}
	for _, p := range app.Settings().FulfillmentProviders {
		if p.Code == "onfleet" && !p.Configured {
			t.Fatal("configured from the panel, the settings should say so")
		}
	}

}
