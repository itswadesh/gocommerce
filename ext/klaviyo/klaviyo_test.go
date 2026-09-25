package klaviyo

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// A Klaviyo that remembers what it was told.
type fakeKlaviyo struct {
	mu     sync.Mutex
	events []map[string]any
	auths  []string
	fail   bool
}

func (f *fakeKlaviyo) handler(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/api/events/" || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	f.auths = append(f.auths, r.Header.Get("Authorization")+"|"+r.Header.Get("revision"))
	if f.fail {
		http.Error(w, `{"errors":[{"detail":"nope"}]}`, http.StatusBadRequest)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var event map[string]any
	_ = json.Unmarshal(body, &event)
	f.events = append(f.events, event)
	w.WriteHeader(http.StatusAccepted)
}

func (f *fakeKlaviyo) metrics() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, e := range f.events {
		attrs := e["data"].(map[string]any)["attributes"].(map[string]any)
		metric := attrs["metric"].(map[string]any)["data"].(map[string]any)["attributes"].(map[string]any)["name"].(string)
		out = append(out, metric)
	}
	return out
}

func TestOrdersBecomeKlaviyoEvents(t *testing.T) {
	fake := &fakeKlaviyo{}
	server := gctest.StubHTTP(t, fake.handler)
	app := gctest.New(t, New(Config{BaseURL: server.URL}))
	ctx := context.Background()

	product := gctest.CreateProduct(t, app, "KL-TEE", 2500, 10)
	variant := product.Variants[0].ID

	// Off by default without a key: an order sends nothing.
	gctest.Buy(t, app, gocommerce.CodeCOD, variant, 1)
	gctest.DrainOutbox(t, app)
	if len(fake.events) != 0 {
		t.Fatalf("events were sent while the plugin was off: %v", fake.metrics())
	}

	on := true
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{"private_key": "pk_test_123"}}); err != nil {
		t.Fatalf("enable: %v", err)
	}
	result := gctest.Buy(t, app, gocommerce.CodeCOD, variant, 1)
	gctest.DrainOutbox(t, app)

	metrics := fake.metrics()
	if len(metrics) != 2 || metrics[0] != "Placed Order" || metrics[1] != "Ordered Product" {
		t.Fatalf("metrics = %v, want Placed Order then Ordered Product", metrics)
	}
	placed := fake.events[0]["data"].(map[string]any)["attributes"].(map[string]any)
	profile := placed["profile"].(map[string]any)["data"].(map[string]any)["attributes"].(map[string]any)
	if profile["email"] != "gctest@example.com" || profile["first_name"] != "GC" || profile["last_name"] != "Test" {
		t.Errorf("profile = %v", profile)
	}
	props := placed["properties"].(map[string]any)
	if props["OrderId"] != result.Order.Number || placed["value"] != float64(result.Order.Total.AmountMinor)/100 || placed["unique_id"] == "" {
		t.Errorf("placed order = %v", placed)
	}
	if items, _ := props["Items"].([]any); len(items) != 1 {
		t.Errorf("items = %v", props["Items"])
	}
	if fake.auths[0] != "Klaviyo-API-Key pk_test_123|"+revision {
		t.Errorf("auth = %q", fake.auths[0])
	}

	// A refusal is counted and logged, and never poisons the event.
	fake.fail = true
	gctest.Buy(t, app, gocommerce.CodeCOD, variant, 1)
	gctest.DrainOutbox(t, app)
	gctest.AssertOutboxEmpty(t, app)
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/klaviyo/status", nil)
	var st Status
	gctest.DecodeData(t, rec, &st)
	if !st.Enabled || !st.Configured || st.Sent != 2 || st.Failed != 2 || st.LastError == "" {
		t.Errorf("status = %+v, want two sent, two failed (the order and its line), and the error kept", st)
	}
}

func TestKlaviyoRoutesAreDocumentedAndGated(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "klaviyo")
	gctest.AssertSpecCoversModuleRoutes(t, app, "klaviyo")
}
