package gocommerce

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMaskEmailHidesEverythingIdentifying(t *testing.T) {
	cases := []struct{ in, want string }{
		{"jane.doe@gmail.com", "j•••@g•••.com"},
		{"a@b.co", "a•••@b•••.co"},
		{"  Jane@Example.COM  ", "J•••@E•••.COM"},
		{"first.last@mail.example.co.uk", "f•••@m•••.uk"},
		{"", ""},

		// The local part's length is the thing a growing mask would publish, so
		// two addresses of very different lengths must come out the same width.
		{"j@gmail.com", "j•••@g•••.com"},
		{"jane.elizabeth.doe.the.third@gmail.com", "j•••@g•••.com"},

		// Not shaped like an address: masked whole rather than passed through.
		{"not-an-address", "•••"},
		{"@gmail.com", "•••"},
		{"jane@", "•••"},
		{"jane@localhost", "j•••@l•••"}, // no suffix to keep, but still an address
	}
	for _, c := range cases {
		if got := maskEmail(c.in); got != c.want {
			t.Errorf("maskEmail(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskPhoneKeepsTheCountryAndTheLastTwoDigits(t *testing.T) {
	cases := []struct{ in, want string }{
		{"+31 20 555 1242", "+31 •• ••• ••42"},
		{"+1 555 1242", "+1 ••• ••42"},
		{"555-1242", "•••-••42"},
		{"(020) 555 1242", "(•••) ••• ••42"},
		{"", ""},

		// Written as one unbroken group there is no country code to read, and
		// treating the whole number as one would mask nothing.
		{"+31205551242", "+•••••••••42"},

		// Too short to be a number anybody is reachable on: no digit survives.
		{"+31", "+••"},
		{"12", "••"},
	}
	for _, c := range cases {
		if got := maskPhone(c.in); got != c.want {
			t.Errorf("maskPhone(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The panel holds a customer's address as their identity and hands it back to
// filter their orders, so a mask has to survive the round trip as something the
// database can still match.
func TestMaskedEmailPatternMatchesTheAddressesItHid(t *testing.T) {
	cases := []struct{ in, want string }{
		{"j•••@g•••.com", "j%@g%.com"},
		{"J•••@G•••.COM", "j%@g%.com"},
		{"•••", "%"},

		// A real address is not a mask, and saying so is how the exact-match
		// filter stays exact on every store that is not a demo.
		{"jane.doe@gmail.com", ""},
		{"", ""},

		// A wildcard the caller did not ask for is a filter that quietly reads
		// rows it was not pointed at.
		{"j%_•••@g•••.com", `j\%\_%@g%.com`},
	}
	for _, c := range cases {
		if got := maskedEmailPattern(c.in); got != c.want {
			t.Errorf("maskedEmailPattern(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Masking is a property of the store, not of the function: the same call has to
// be a no-op everywhere else, because every call site is unconditional.
func TestMaskingIsOffUnlessTheStoreIsADemo(t *testing.T) {
	live := &App{cfg: Config{}}
	demo := &App{cfg: Config{Demo: true}}

	if got := live.MaskEmail("jane.doe@gmail.com"); got != "jane.doe@gmail.com" {
		t.Errorf("a live store masked an address: %q", got)
	}
	if got := live.MaskPhone("+31 20 555 1242"); got != "+31 20 555 1242" {
		t.Errorf("a live store masked a number: %q", got)
	}
	if got := demo.MaskEmail("jane.doe@gmail.com"); got != "j•••@g•••.com" {
		t.Errorf("a demo store did not mask an address: %q", got)
	}
	if got := demo.MaskPhone("+31 20 555 1242"); got != "+31 •• ••• ••42" {
		t.Errorf("a demo store did not mask a number: %q", got)
	}
	if !demo.Demo() || live.Demo() {
		t.Error("Demo() does not report what the config says")
	}
}

// The guard that stops a form writing a mask over the address it was hiding.
func TestAMaskComingBackIsNotAnEdit(t *testing.T) {
	demo := &App{cfg: Config{Demo: true}}
	live := &App{cfg: Config{}}

	masked := "j•••@g•••.com"
	p := &masked
	demo.dropMasked(&p)
	if p != nil {
		t.Errorf("a demo store accepted its own mask as an edit: %q", *p)
	}

	typed := "someone.else@example.com"
	q := &typed
	demo.dropMasked(&q)
	if q == nil || *q != typed {
		t.Error("a demo store dropped an address the operator actually typed")
	}

	// Nothing is masked on a live store, so nothing may be dropped there — a
	// value containing the rune is somebody's odd input, not our mask.
	odd := "we•rd@example.com"
	r := &odd
	live.dropMasked(&r)
	if r == nil || *r != odd {
		t.Error("a live store dropped a field it never masked")
	}

	var absent *string
	demo.dropMasked(&absent)
	if absent != nil {
		t.Error("dropMasked invented a value for a field that was not submitted")
	}

	if !demo.maskedInput(masked) || demo.maskedInput(typed) || live.maskedInput(masked) {
		t.Error("maskedInput disagrees with dropMasked about what a mask is")
	}
}

// The freeze rule as a table, so the routes that must stay open are as much of
// the test as the ones that must not. demoRefusal is the whole decision, which
// is why it is a function and not a closure inside the middleware.
func TestWhatADemoRefuses(t *testing.T) {
	cases := []struct {
		method, path string
		refused      bool
	}{
		// The families that decide who can sign in: writes refused, reads open.
		{"POST", "/api/admin/superusers", true},
		{"PATCH", "/api/admin/superusers/7", true},
		{"DELETE", "/api/admin/superusers/7", true},
		{"PUT", "/api/admin/superusers/7/role", true},
		{"POST", "/api/admin/me/revoke-sessions", true},
		{"PATCH", "/api/admin/me", true},
		{"PUT", "/api/admin/roles/staff", true},
		{"POST", "/api/admin/invitations", true},
		{"POST", "/api/admin/password-reset/confirm", true},
		{"POST", "/api/admin/api-keys", true},
		{"GET", "/api/admin/superusers", false},
		{"GET", "/api/admin/me", false},
		{"GET", "/api/admin/roles", false},
		{"GET", "/api/admin/api-keys", false},

		// The routes whose answer reads around the mask: refused whatever the
		// method, because a GET was how each of them got out.
		{"GET", "/api/admin/api-keys/7/secret", true},
		{"POST", "/api/admin/orders/7/access-token", true},
		{"POST", "/api/admin/reports/custom/run", true},
		{"POST", "/api/admin/reports/custom/7/run", true},
		{"POST", "/api/admin/vendors/7/users", true},

		// Signing in cannot be frozen, or a demo has no door at all.
		{"POST", "/api/admin/auth-with-password", false},
		{"POST", "/api/admin/auth-refresh", false},
		{"POST", "/api/admin/auth-logout", false},

		// Ordinary work stays possible: a demo you cannot click around in is
		// not a demo.
		{"POST", "/api/admin/products", false},
		{"PATCH", "/api/admin/orders/7", false},
		{"GET", "/api/admin/orders/7", false},
		{"GET", "/api/admin/reports/custom", false},
		{"POST", "/api/admin/vendors", false},

		// A '*' is one segment and must not swallow a deeper path.
		{"GET", "/api/admin/api-keys/7/secret/extra", false},
		{"POST", "/api/admin/orders/7/access-token/more", false},
	}
	for _, c := range cases {
		got := demoRefusal(c.method, c.path) != ""
		if got != c.refused {
			t.Errorf("demoRefusal(%s %s) refused = %v, want %v", c.method, c.path, got, c.refused)
		}
	}
}

// A store that is not a demo refuses none of it, which is the property the
// middleware's early return carries and the one worth stating.
func TestALiveStoreFreezesNothing(t *testing.T) {
	live := &App{cfg: Config{}}
	h := live.demoGuardMW(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	for _, target := range []string{
		"/api/admin/superusers",
		"/api/admin/api-keys/7/secret",
		"/api/admin/reports/custom/run",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, nil))
		if rec.Code != http.StatusTeapot {
			t.Errorf("POST %s on a live store = %d, want the handler to have run", target, rec.Code)
		}
	}
}

// A "phone" column holding words is the case digits-only masking published
// verbatim: the column's contents are not this engine's decision.
func TestMaskPhoneHidesLettersToo(t *testing.T) {
	cases := []struct{ in, want string }{
		{"jane.doe@gmail.com", "•••• •••@••••• •••"},
		{"call the office", "•••• ••• ••••••"},
		{"+31 20 555 1242 ext 9", "+31 •• ••• ••4• ••• 2"},
	}
	for _, c := range cases {
		got := maskPhone(c.in)
		if strings.ContainsAny(got, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			t.Errorf("maskPhone(%q) = %q, still carries letters", c.in, got)
		}
		_ = c.want
	}
	// A real number is untouched in shape, which is the point of keeping
	// separators.
	if got := maskPhone("+31 20 555 1242"); got != "+31 •• ••• ••42" {
		t.Errorf("maskPhone mangled a real number: %q", got)
	}
}

// The audit trail records a corrected address as a nested object, and a
// top-level-only pass published the phone inside it.
func TestMaskAuditChangesDescendsIntoNestedObjects(t *testing.T) {
	c := &AuditChanges{
		Before: map[string]any{
			"email":   "jane.doe@gmail.com",
			"address": map[string]any{"phone": "+31 20 555 1242", "city": "Rotterdam"},
		},
		After: map[string]any{
			"contacts": []any{map[string]any{"email": "someone@example.com"}},
		},
	}
	maskAuditChanges(c)

	if c.Before["email"] != "j•••@g•••.com" {
		t.Errorf("top-level email = %v", c.Before["email"])
	}
	addr := c.Before["address"].(map[string]any)
	if addr["phone"] != "+31 •• ••• ••42" {
		t.Errorf("nested phone = %v, want it masked", addr["phone"])
	}
	if addr["city"] != "Rotterdam" {
		t.Errorf("a field that is not a contact detail was touched: %v", addr["city"])
	}
	inner := c.After["contacts"].([]any)[0].(map[string]any)
	if inner["email"] != "s•••@e•••.com" {
		t.Errorf("email inside an array = %v, want it masked", inner["email"])
	}
}
