package gocommerce

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"
)

// decodeSettings reads the data envelope as the raw keys that crossed the wire,
// so a test can ask what was actually served rather than what a Go struct would
// have filled in for it.
func decodeSettings(t *testing.T, body []byte) map[string]json.RawMessage {
	t.Helper()
	var out struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode the settings response: %v\n%s", err, body)
	}
	return out.Data
}

// The endpoint reports what this binary was started with, not what the engine
// defaults to. Every field is set to something no default would produce, which
// is what makes a hardcoded answer fail here.
func TestSettingsReportsWhatTheBinaryWasStartedWith(t *testing.T) {
	dsn := requireDB(t)
	cfg := testConfig(dsn)
	cfg.Currency = "JPY"
	cfg.DefaultLanguage = "ja"
	cfg.Languages = []string{"ja", "en"}
	cfg.PricesIncludeTax = true
	cfg.FlatShippingMinor = 500
	cfg.OrderPrefix = "JP-"
	cfg.CartTTL = 48 * time.Hour
	cfg.OrderTTL = 2 * time.Hour
	cfg.MediaDir = t.TempDir()

	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	rec := do(t, app, "GET", "/api/admin/settings", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/settings = %d: %s", rec.Code, rec.Body)
	}

	var got struct {
		Data StoreSettings `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}
	s := got.Data

	if s.Version != Version {
		t.Errorf("version = %q, want %q", s.Version, Version)
	}
	if s.Currency != "JPY" {
		t.Errorf("currency = %q, want JPY", s.Currency)
	}
	if s.DefaultLanguage != "ja" {
		t.Errorf("default_language = %q, want ja", s.DefaultLanguage)
	}
	if !slices.Equal(s.Languages, []string{"ja", "en"}) {
		t.Errorf("languages = %v, want [ja en]", s.Languages)
	}
	if !s.PricesIncludeTax {
		t.Error("prices_include_tax is false in a store that was started with it true")
	}
	// Money, not a bare integer: 500 without JPY beside it is 500 of nothing,
	// and JPY is the case that makes the difference visible — no decimals.
	if s.FlatShipping != (Money{AmountMinor: 500, Currency: "JPY"}) {
		t.Errorf("flat_shipping = %+v, want {500 JPY}", s.FlatShipping)
	}
	if s.OrderPrefix != "JP-" {
		t.Errorf("order_prefix = %q, want JP-", s.OrderPrefix)
	}
	// Seconds, because a time.Duration marshals as nanoseconds and 172800000000000
	// is not a number anybody reads correctly.
	if s.CartTTLSeconds != 172800 {
		t.Errorf("cart_ttl_seconds = %d, want 172800", s.CartTTLSeconds)
	}
	if s.OrderTTLSeconds != 7200 {
		t.Errorf("order_ttl_seconds = %d, want 7200", s.OrderTTLSeconds)
	}
	if !s.MediaUploadsEnabled {
		t.Error("media_uploads_enabled is false in a store configured with a media directory")
	}

	raw := decodeSettings(t, rec.Body.Bytes())
	if string(raw["flat_shipping"]) == "500" {
		t.Error("flat_shipping crossed as a bare integer, which pairs a number with a currency by hand")
	}
	if string(raw["cart_ttl_seconds"]) == "172800000000000" {
		t.Error("cart_ttl_seconds crossed as nanoseconds")
	}
}

// The response is a whitelist, and this pins the whole of it.
//
// It has to be exact equality rather than "the DSN is absent", because the
// obvious wrong implementation cannot fail the absence test: Config carries no
// json tags, so marshalling it would emit DBURL and AdminTokens rather than
// db_url and admin_tokens — and it would not even get that far, since
// Config.AdminAuth is a func, writeJSON marshals into a buffer first, and the
// handler would answer 500 with a body containing neither. Equality fails on
// that, on a Config field leaking in later, and on a served key quietly going
// away.
func TestSettingsServesExactlyItsWhitelist(t *testing.T) {
	app := newTestApp(t)

	rec := do(t, app, "GET", "/api/admin/settings", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/settings = %d: %s", rec.Code, rec.Body)
	}

	want := []string{
		"cart_ttl_seconds",
		"currency",
		"default_language",
		"flat_shipping",
		"fulfillment_providers",
		"languages",
		"media_uploads_enabled",
		"order_prefix",
		"order_ttl_seconds",
		"payment_methods",
		"prices_include_tax",
		"version",
	}
	got := make([]string, 0, len(want))
	for k := range decodeSettings(t, rec.Body.Bytes()) {
		got = append(got, k)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("the response serves %v, want exactly %v", got, want)
	}

	// The belt to that pair of braces: whatever else changes, these two never
	// appear.
	body := rec.Body.String()
	if strings.Contains(body, testAdminToken) {
		t.Error("the response carries an admin token")
	}
	if strings.Contains(body, app.Config().DBURL) {
		t.Error("the response carries the database URL")
	}
}

// A credential is needed; a right is not. Every screen formats money before it
// can draw anything, so a role that could not read this would read prices in
// the wrong currency and the wrong number of decimals.
func TestSettingsNeedsACredentialButNoRight(t *testing.T) {
	app := newTestApp(t)

	if rec := do(t, app, "GET", "/api/admin/settings"); rec.Code != http.StatusUnauthorized {
		t.Errorf("an anonymous read = %d, want 401: %s", rec.Code, rec.Body)
	}

	cases := []struct {
		name  string
		token string
	}{
		{"staff", signInAs(t, app, "staff@example.com", RoleStaff)},
		{"manager", signInAs(t, app, "manager@example.com", RoleManager)},
		{"owner", signInAs(t, app, "owner@example.com", RoleOwner)},
		{"the static admin token", testAdminToken},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, app, "GET", "/api/admin/settings", bearer(c.token))
			if rec.Code != http.StatusOK {
				t.Errorf("%s reading the settings = %d, want 200: %s", c.name, rec.Code, rec.Body)
			}
		})
	}
}

// A provider's label belongs to whatever installed it, and so does its name in
// this response.
func TestSettingsNamesProvidersAndTheirModules(t *testing.T) {
	app := newTestApp(t, refundableModule{})

	rec := do(t, app, "GET", "/api/admin/settings", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/settings = %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Data StoreSettings `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v\n%s", err, rec.Body)
	}

	wantPay := []ProviderInfo{
		{Code: CodeCOD, Name: "Cash on delivery", Module: "core"},
		// refundableProvider implements no Named, so its name is its code —
		// and its module is the one that registered it, not core.
		{Code: "refundable", Name: "refundable", Module: "refundable"},
	}
	if !slices.Equal(got.Data.PaymentMethods, wantPay) {
		t.Errorf("payment_methods = %+v, want %+v", got.Data.PaymentMethods, wantPay)
	}
	wantShip := []ProviderInfo{{Code: ProviderManual, Name: "Manual", Module: "core"}}
	if !slices.Equal(got.Data.FulfillmentProviders, wantShip) {
		t.Errorf("fulfillment_providers = %+v, want %+v", got.Data.FulfillmentProviders, wantShip)
	}

	// Sorted by code, because a chip row that reshuffles between two reads of
	// the same store looks like something changed.
	if !slices.IsSortedFunc(got.Data.PaymentMethods, func(a, b ProviderInfo) int {
		return strings.Compare(a.Code, b.Code)
	}) {
		t.Errorf("payment_methods is not sorted by code: %+v", got.Data.PaymentMethods)
	}
}

// The upload flag follows the store's actual ability to store a file, which is
// what lets the panel explain a missing button instead of offering one that
// fails.
func TestSettingsMediaUploadsFollowsTheStore(t *testing.T) {
	read := func(t *testing.T, app *App) bool {
		t.Helper()
		rec := do(t, app, "GET", "/api/admin/settings", withAdmin)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/admin/settings = %d: %s", rec.Code, rec.Body)
		}
		var got struct {
			Data StoreSettings `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("decode: %v\n%s", err, rec.Body)
		}
		return got.Data.MediaUploadsEnabled
	}

	// No MediaDir and no MediaStore is a supported configuration: the library
	// still records files by URL.
	if read(t, newTestApp(t)) {
		t.Error("media_uploads_enabled is true in a store with nowhere to put a file")
	}

	cfg := testConfig(requireDB(t))
	cfg.MediaDir = t.TempDir()
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	if !read(t, app) {
		t.Error("media_uploads_enabled is false in a store with a media directory")
	}
}

// Every list is a JSON array, never null, so no client needs a guard before it
// can iterate one. Small, but it is the difference between a chip row rendering
// and a screen throwing.
func TestSettingsSerialisesEmptyListsAsArrays(t *testing.T) {
	app := newTestApp(t)

	rec := do(t, app, "GET", "/api/admin/settings", withAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/admin/settings = %d: %s", rec.Code, rec.Body)
	}
	raw := decodeSettings(t, rec.Body.Bytes())
	for _, key := range []string{"languages", "payment_methods", "fulfillment_providers"} {
		if !strings.HasPrefix(string(raw[key]), "[") {
			t.Errorf("%s = %s, want a JSON array", key, raw[key])
		}
	}

	// The same promise held directly against the snapshot, which is where an
	// empty store would produce the nil that marshals as null.
	empty := &App{cfg: app.cfg, payments: &Payments{}, fulfillment: &Fulfillments{}}
	s := empty.Settings()
	if s.PaymentMethods == nil || s.FulfillmentProviders == nil || s.Languages == nil {
		t.Errorf("a store with nothing installed produces nil slices: %+v", s)
	}
}
