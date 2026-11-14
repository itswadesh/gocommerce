package gokwik

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
	"github.com/misiki-in/gocommerce/gctest"
)

// stub is GoKwik's predict endpoint, answering what the test sets.
type stub struct {
	mu     sync.Mutex
	calls  int
	path   string
	appID  string
	secret string
	body   map[string]any
	status int
	answer string
	delay  time.Duration
}

func (s *stub) handler(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls++
	s.path, s.appID, s.secret = r.URL.Path, r.Header.Get("appid"), r.Header.Get("appsecret")
	_ = json.Unmarshal(raw, &s.body)
	status, answer, delay := s.status, s.answer, s.delay
	s.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if status != 0 {
		w.WriteHeader(status)
	}
	_, _ = w.Write([]byte(answer))
}

func (s *stub) flag(flag string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.answer = 0, `{"status_code":200,"msg":"Success","data":{"request_id":"r1","risk_flag":"`+flag+`","reason":""},"error":null}`
}

func (s *stub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func newApp(t *testing.T, refuse string) (*gocommerce.App, *stub) {
	t.Helper()
	s := &stub{}
	s.flag("Low Risk")
	srv := gctest.StubHTTP(t, s.handler)
	app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "INR", AllowPrivateURLs: true}, New(Config{}))
	on := true
	settings := map[string]any{"app_id": "the-id", "app_secret": "the-secret", "api_url": srv.URL}
	if refuse != "" {
		settings["refuse"] = refuse
	}
	if _, err := app.Plugins().Update(context.Background(), PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: settings}); err != nil {
		t.Fatalf("set the plugin up: %v", err)
	}
	return app, s
}

// checkoutCOD places a cash-on-delivery order delivered to country.
func checkoutCOD(t *testing.T, app *gocommerce.App, country string) (*gocommerce.CheckoutResult, error) {
	t.Helper()
	ctx := context.Background()
	product := gctest.CreateProduct(t, app, "GK-"+t.Name(), 100000, 5)
	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Cart().AddLine(ctx, cart.Token, product.DefaultVariant().ID, 2); err != nil {
		t.Fatal(err)
	}
	return app.Order().Checkout(ctx, gocommerce.CodeCOD, gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "asha@example.com", Name: "Asha Rao",
		Address: gocommerce.Address{
			Line1: "12 MG Road", Line2: "Near the park", City: "Bengaluru", State: "KA",
			PostalCode: "560001", Country: country, Phone: "+91 98765 43210",
		},
	}, "")
}

func TestHighRiskRefusesCashOnDelivery(t *testing.T) {
	app, s := newApp(t, "")
	s.flag("High Risk")
	_, err := checkoutCOD(t, app, "IN")
	var apiErr *gocommerce.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeCODUnavailable || apiErr.Status != http.StatusForbidden {
		t.Fatalf("checkout = %v, want %s", err, CodeCODUnavailable)
	}

	if s.path != "/v2/rto/predict" || s.appID != "the-id" || s.secret != "the-secret" {
		t.Errorf("called %q with %q / %q", s.path, s.appID, s.secret)
	}
	order, _ := s.body["order"].(map[string]any)
	addr, _ := order["shipping_address"].(map[string]any)
	if addr["phone"] != "9876543210" || addr["postcode"] != float64(560001) || addr["first_name"] != "Asha" {
		t.Errorf("address = %v", addr)
	}
	if order["subtotal"] != "2000.00" || order["total_line_items"] != float64(1) ||
		order["total_line_items_quantity"] != float64(2) || order["user_type"] != "new" || order["platform_type"] != "custom" {
		t.Errorf("order = %v", order)
	}
	if lines, _ := order["line_items"].([]any); len(lines) != 1 {
		t.Errorf("line_items = %v", order["line_items"])
	}
}

func TestTheStoreChoosesWhetherMediumRiskIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name, refuse, flag string
		refused            bool
	}{
		{"low passes", refuseHigh, "Low Risk", false},
		{"medium passes by default", "", "Medium Risk", false},
		{"medium refused when chosen", refuseHighMedium, "Medium Risk", true},
		{"low passes the stricter setting", refuseHighMedium, "Low Risk", false},
		{"a control order passes", refuseHighMedium, "Control", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, s := newApp(t, tc.refuse)
			s.flag(tc.flag)
			_, err := checkoutCOD(t, app, "IN")
			if (err != nil) != tc.refused {
				t.Fatalf("checkout = %v, refused should be %v", err, tc.refused)
			}
		})
	}
}

// A store must not lose a sale to GoKwik having a bad moment.
func TestCashOnDeliveryStaysOpenWhenGoKwikCannotAnswer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		answer string
		delay  time.Duration
	}{
		{name: "server error", status: http.StatusInternalServerError, answer: `{"msg":"Score Calculation Failure"}`},
		{name: "keys refused", status: http.StatusForbidden, answer: `{}`},
		{name: "not JSON", answer: `<html>503</html>`},
		{name: "no flag", answer: `{"data":{}}`},
		{name: "past the time limit", answer: `{"data":{"risk_flag":"High Risk"}}`, delay: predictTimeout + 300*time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, s := newApp(t, "")
			s.status, s.answer, s.delay = tc.status, tc.answer, tc.delay
			if _, err := checkoutCOD(t, app, "IN"); err != nil {
				t.Fatalf("checkout = %v, want it to go ahead", err)
			}
			if s.count() != 1 {
				t.Fatalf("GoKwik asked %d times, want 1", s.count())
			}
		})
	}
}

func TestGoKwikIsAskedOnlyAboutIndianCashOnDeliveryWhileSwitchedOn(t *testing.T) {
	t.Run("another country", func(t *testing.T) {
		app, s := newApp(t, "")
		s.flag("High Risk")
		if _, err := checkoutCOD(t, app, "US"); err != nil {
			t.Fatalf("checkout = %v", err)
		}
		if s.count() != 0 {
			t.Fatal("GoKwik was asked about an order outside India")
		}
	})
	t.Run("another payment method", func(t *testing.T) {
		app, s := newApp(t, "")
		mod := &Module{app: app, log: app.Log(), client: http.DefaultClient}
		in := &gocommerce.CheckoutIntent{Method: "razorpay", Input: gocommerce.CheckoutInput{Address: gocommerce.Address{Country: "IN"}}}
		if err := mod.precheck(context.Background(), in); err != nil {
			t.Fatalf("precheck = %v", err)
		}
		if s.count() != 0 {
			t.Fatal("GoKwik was asked about a prepaid order")
		}
	})
	t.Run("switched off", func(t *testing.T) {
		app, s := newApp(t, "")
		s.flag("High Risk")
		off := false
		if _, err := app.Plugins().Update(context.Background(), PluginKey, gocommerce.PluginPatch{Enabled: &off}); err != nil {
			t.Fatal(err)
		}
		if _, err := checkoutCOD(t, app, "IN"); err != nil {
			t.Fatalf("checkout = %v", err)
		}
		if s.count() != 0 {
			t.Fatal("GoKwik was asked while switched off")
		}
	})
}

func TestMoneyAndPhonesAreWrittenAsGoKwikShowsThem(t *testing.T) {
	for minor, want := range map[int64]string{0: "0.00", 5: "0.05", 61875: "618.75", 100000: "1000.00"} {
		if got := rupees(minor); got != want {
			t.Errorf("rupees(%d) = %q, want %q", minor, got, want)
		}
	}
	for in, want := range map[string]string{"+91 98765-43210": "9876543210", "09876543210": "9876543210", "9876543210": "9876543210", "12345": "12345"} {
		if got := indianPhone(in); got != want {
			t.Errorf("indianPhone(%q) = %q, want %q", in, got, want)
		}
	}
}
