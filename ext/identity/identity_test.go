package identity

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The hashes these tests make are thrown away with the schema; there is
// nothing to protect and no reason to spend 600k iterations per sign-in.
func init() { pbkdf2Iterations = 1_000 }

// capture is a notifier that remembers what it was asked to send, so a test
// can read the reset token out of the "email".
type capture struct {
	mu   sync.Mutex
	sent []gocommerce.Notification
}

func (c *capture) Notify(_ context.Context, n gocommerce.Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, n)
	return nil
}

func (c *capture) last() *gocommerce.Notification {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		return nil
	}
	return &c.sent[len(c.sent)-1]
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}

func newApp(t *testing.T) (*gocommerce.App, *capture) {
	t.Helper()
	mail := &capture{}
	app := gctest.New(t, New(Config{Notifier: mail}))
	return app, mail
}

// as sends a request carrying a shopper session.
func as(t *testing.T, app *gocommerce.App, token, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, target, reader)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	app.Handler().ServeHTTP(rec, req)
	return rec
}

func register(t *testing.T, app *gocommerce.App, email string) AuthResponse {
	t.Helper()
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/register", map[string]any{
		"email": email, "password": "correct horse", "name": "Ada Lovelace", "phone": "+441234",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("register %s = %d: %s", email, rec.Code, rec.Body)
	}
	var auth AuthResponse
	gctest.DecodeData(t, rec, &auth)
	if auth.Token == "" || auth.Record == nil || auth.Record.Email != email {
		t.Fatalf("register returned %+v", auth)
	}
	return auth
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, rec.Body)
	}
	return env.Error.Code
}

func TestRegisterLoginAndLogout(t *testing.T) {
	app, _ := newApp(t)
	auth := register(t, app, "ada@example.com")

	// The token is a credential.
	rec := as(t, app, auth.Token, http.MethodGet, "/x/identity/me", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me = %d: %s", rec.Code, rec.Body)
	}
	var me Customer
	gctest.DecodeData(t, rec, &me)
	if me.Name != "Ada Lovelace" || me.Phone != "+441234" {
		t.Errorf("me = %+v", me)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("password")) {
		t.Error("the password hash leaked into the response")
	}

	// No token is no session; a made-up one is no session.
	if rec := as(t, app, "", http.MethodGet, "/x/identity/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("me without a token = %d, want 401", rec.Code)
	}
	if rec := as(t, app, "nope", http.MethodGet, "/x/identity/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("me with a bad token = %d, want 401", rec.Code)
	}
	// An admin token is not a shopper.
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/x/identity/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("me with the admin token = %d, want 401", rec.Code)
	}

	// Sign in, any capitalisation.
	rec = gctest.Request(t, app, http.MethodPost, "/x/identity/login", map[string]any{
		"email": "Ada@Example.com", "password": "correct horse",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d: %s", rec.Code, rec.Body)
	}
	var login AuthResponse
	gctest.DecodeData(t, rec, &login)
	if login.Token == auth.Token {
		t.Error("login reused the registration token")
	}

	// Wrong password and unknown email are indistinguishable.
	wrong := gctest.Request(t, app, http.MethodPost, "/x/identity/login", map[string]any{
		"email": "ada@example.com", "password": "wrong",
	})
	unknown := gctest.Request(t, app, http.MethodPost, "/x/identity/login", map[string]any{
		"email": "nobody@example.com", "password": "correct horse",
	})
	if wrong.Code != http.StatusBadRequest || unknown.Code != http.StatusBadRequest {
		t.Fatalf("wrong = %d, unknown = %d, want 400 for both", wrong.Code, unknown.Code)
	}
	if errorCode(t, wrong) != "invalid_credentials" || errorCode(t, unknown) != "invalid_credentials" {
		t.Errorf("codes = %q / %q", errorCode(t, wrong), errorCode(t, unknown))
	}

	// Refresh keeps the token; logout ends exactly this session.
	if rec := as(t, app, login.Token, http.MethodPost, "/x/identity/refresh", nil); rec.Code != http.StatusOK {
		t.Errorf("refresh = %d: %s", rec.Code, rec.Body)
	}
	if rec := as(t, app, login.Token, http.MethodPost, "/x/identity/logout", nil); rec.Code != http.StatusNoContent {
		t.Errorf("logout = %d: %s", rec.Code, rec.Body)
	}
	if rec := as(t, app, login.Token, http.MethodGet, "/x/identity/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("me after logout = %d, want 401", rec.Code)
	}
	if rec := as(t, app, auth.Token, http.MethodGet, "/x/identity/me", nil); rec.Code != http.StatusOK {
		t.Errorf("the other session should survive a logout: %d", rec.Code)
	}
}

func TestRegisterValidates(t *testing.T) {
	app, _ := newApp(t)
	post := func(body map[string]any) *httptest.ResponseRecorder {
		return gctest.Request(t, app, http.MethodPost, "/x/identity/register", body)
	}
	if rec := post(map[string]any{"email": "ada@example.com", "password": "short"}); rec.Code != http.StatusBadRequest {
		t.Errorf("short password = %d, want 400", rec.Code)
	}
	if rec := post(map[string]any{"email": "not an email", "password": "correct horse"}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad email = %d, want 400", rec.Code)
	}
	register(t, app, "ada@example.com")
	if rec := post(map[string]any{"email": "ADA@example.com", "password": "correct horse"}); rec.Code != http.StatusConflict {
		t.Errorf("duplicate email = %d, want 409", rec.Code)
	}
}

func TestProfileAndPasswordChange(t *testing.T) {
	app, _ := newApp(t)
	auth := register(t, app, "ada@example.com")
	other := gctest.Request(t, app, http.MethodPost, "/x/identity/login", map[string]any{
		"email": "ada@example.com", "password": "correct horse",
	})
	var second AuthResponse
	gctest.DecodeData(t, other, &second)

	rec := as(t, app, auth.Token, http.MethodPatch, "/x/identity/me", map[string]any{"name": "Countess", "phone": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch me = %d: %s", rec.Code, rec.Body)
	}
	var me Customer
	gctest.DecodeData(t, rec, &me)
	if me.Name != "Countess" || me.Phone != "" || me.Email != "ada@example.com" {
		t.Errorf("me = %+v", me)
	}

	if rec := as(t, app, auth.Token, http.MethodPut, "/x/identity/me/password", map[string]any{
		"current_password": "wrong", "password": "battery staple",
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("change with the wrong current password = %d, want 400", rec.Code)
	}
	rec = as(t, app, auth.Token, http.MethodPut, "/x/identity/me/password", map[string]any{
		"current_password": "correct horse", "password": "battery staple",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("change password = %d: %s", rec.Code, rec.Body)
	}
	var fresh AuthResponse
	gctest.DecodeData(t, rec, &fresh)

	// Every session that predates the change is gone, including the caller's
	// old one; the response carried its replacement.
	if rec := as(t, app, auth.Token, http.MethodGet, "/x/identity/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("old session after password change = %d, want 401", rec.Code)
	}
	if rec := as(t, app, second.Token, http.MethodGet, "/x/identity/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("other session after password change = %d, want 401", rec.Code)
	}
	if rec := as(t, app, fresh.Token, http.MethodGet, "/x/identity/me", nil); rec.Code != http.StatusOK {
		t.Errorf("replacement session = %d, want 200", rec.Code)
	}
	if rec := gctest.Request(t, app, http.MethodPost, "/x/identity/login", map[string]any{
		"email": "ada@example.com", "password": "battery staple",
	}); rec.Code != http.StatusOK {
		t.Errorf("login with the new password = %d: %s", rec.Code, rec.Body)
	}
}

func TestPasswordReset(t *testing.T) {
	app, mail := newApp(t)
	auth := register(t, app, "ada@example.com")

	// An unknown address gets the same answer and no email.
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/password-reset", map[string]any{"email": "nobody@example.com"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reset for an unknown email = %d, want 202", rec.Code)
	}
	if mail.last() != nil {
		t.Fatal("an unknown address must not produce an email")
	}

	rec = gctest.Request(t, app, http.MethodPost, "/x/identity/password-reset", map[string]any{"email": "ADA@example.com"})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reset = %d: %s", rec.Code, rec.Body)
	}
	note := mail.last()
	if note == nil {
		t.Fatal("no reset email was sent")
	}
	if note.Event != EventPasswordReset || note.Channel != gocommerce.ChannelEmail || note.To != "ada@example.com" {
		t.Errorf("notification = %+v", note)
	}
	token := note.Data["reset_token"]
	if token == "" {
		t.Fatalf("no reset_token in %v", note.Data)
	}
	if _, ok := note.Data["reset_url"]; ok {
		t.Error("reset_url should be absent when no ResetURL is configured")
	}

	if rec := gctest.Request(t, app, http.MethodPost, "/x/identity/password-reset/confirm", map[string]any{
		"token": "bogus", "password": "battery staple",
	}); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_token" {
		t.Errorf("confirm with a bad token = %d %s", rec.Code, rec.Body)
	}
	rec = gctest.Request(t, app, http.MethodPost, "/x/identity/password-reset/confirm", map[string]any{
		"token": token, "password": "battery staple",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d: %s", rec.Code, rec.Body)
	}
	var fresh AuthResponse
	gctest.DecodeData(t, rec, &fresh)
	if rec := as(t, app, fresh.Token, http.MethodGet, "/x/identity/me", nil); rec.Code != http.StatusOK {
		t.Errorf("session from the reset = %d", rec.Code)
	}
	// The reset ended every session that existed before it, and a token is
	// single-use.
	if rec := as(t, app, auth.Token, http.MethodGet, "/x/identity/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("session from before the reset = %d, want 401", rec.Code)
	}
	if rec := gctest.Request(t, app, http.MethodPost, "/x/identity/password-reset/confirm", map[string]any{
		"token": token, "password": "another one",
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("reused token = %d, want 400", rec.Code)
	}
}

func TestResetURLIsTheStoresNotTheClients(t *testing.T) {
	mail := &capture{}
	app := gctest.New(t, New(Config{
		Notifier: mail, ResetURL: "https://shop.example/reset?token={token}",
	}))
	register(t, app, "ada@example.com")
	gctest.Request(t, app, http.MethodPost, "/x/identity/password-reset", map[string]any{"email": "ada@example.com"})
	note := mail.last()
	if note == nil {
		t.Fatal("no reset email")
	}
	want := "https://shop.example/reset?token=" + note.Data["reset_token"]
	if note.Data["reset_url"] != want {
		t.Errorf("reset_url = %q, want %q", note.Data["reset_url"], want)
	}
}

func TestAddressBook(t *testing.T) {
	app, _ := newApp(t)
	ada := register(t, app, "ada@example.com")
	bob := register(t, app, "bob@example.com")

	// A book needs a deliverable address.
	if rec := as(t, app, ada.Token, http.MethodPost, "/x/identity/me/addresses", map[string]any{
		"line1": "1 Analytical Way",
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("address without city/postcode/country = %d, want 400", rec.Code)
	}

	save := func(token, label string) Address {
		rec := as(t, app, token, http.MethodPost, "/x/identity/me/addresses", map[string]any{
			"label": label, "name": "Ada", "line1": "1 Analytical Way", "city": "London",
			"postal_code": "N1 1AA", "country": "GB",
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("save %s = %d: %s", label, rec.Code, rec.Body)
		}
		var a Address
		gctest.DecodeData(t, rec, &a)
		return a
	}
	home := save(ada.Token, "Home")
	if !home.IsDefault {
		t.Error("the first address should be the default")
	}
	office := save(ada.Token, "Office")
	if office.IsDefault {
		t.Error("a second address should not steal the default")
	}

	// Moving the default un-defaults the other.
	rec := as(t, app, ada.Token, http.MethodPatch, "/x/identity/me/addresses/"+strconv.FormatInt(office.ID, 10),
		map[string]any{"is_default": true, "label": "Work"})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body)
	}
	rec = as(t, app, ada.Token, http.MethodGet, "/x/identity/me/addresses", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", rec.Code, rec.Body)
	}
	var list []Address
	gctest.DecodeData(t, rec, &list)
	if len(list) != 2 || !list[0].IsDefault || list[0].Label != "Work" || list[1].IsDefault {
		t.Errorf("list = %+v", list)
	}

	// Bob's book is Bob's.
	if rec := as(t, app, bob.Token, http.MethodGet, "/x/identity/me/addresses/"+strconv.FormatInt(home.ID, 10), nil); rec.Code != http.StatusNotFound {
		t.Errorf("another account reading the address = %d, want 404", rec.Code)
	}
	if rec := as(t, app, bob.Token, http.MethodDelete, "/x/identity/me/addresses/"+strconv.FormatInt(home.ID, 10), nil); rec.Code != http.StatusNotFound {
		t.Errorf("another account deleting the address = %d, want 404", rec.Code)
	}

	// Deleting the default hands it to what is left.
	if rec := as(t, app, ada.Token, http.MethodDelete, "/x/identity/me/addresses/"+strconv.FormatInt(office.ID, 10), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body)
	}
	rec = as(t, app, ada.Token, http.MethodGet, "/x/identity/me/addresses/"+strconv.FormatInt(home.ID, 10), nil)
	var remaining Address
	gctest.DecodeData(t, rec, &remaining)
	if !remaining.IsDefault {
		t.Error("the remaining address should have become the default")
	}
}

func TestOrderHistoryIsByClaim(t *testing.T) {
	app, _ := newApp(t)
	ada := register(t, app, "ada@example.com")
	bob := register(t, app, "bob@example.com")

	// Checkout is unchanged: a guest places the order, and holds its token.
	placed := gctest.PlaceOrder(t, app, "cod")
	number, token := placed.Order.Number, placed.Order.AccessToken
	if token == "" {
		t.Fatal("checkout returned no access token")
	}

	// Nothing yet.
	rec := as(t, app, ada.Token, http.MethodGet, "/x/identity/me/orders", nil)
	var none []gocommerce.Order
	gctest.DecodeData(t, rec, &none)
	if len(none) != 0 {
		t.Fatalf("history before any claim = %d orders", len(none))
	}

	// The number alone proves nothing.
	if rec := as(t, app, ada.Token, http.MethodPost, "/x/identity/me/orders", map[string]any{
		"number": number, "token": "guess",
	}); rec.Code != http.StatusNotFound {
		t.Errorf("claim with a wrong token = %d, want 404", rec.Code)
	}
	rec = as(t, app, ada.Token, http.MethodPost, "/x/identity/me/orders", map[string]any{"number": number, "token": token})
	if rec.Code != http.StatusCreated {
		t.Fatalf("claim = %d: %s", rec.Code, rec.Body)
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("access_token")) {
		t.Error("the access token must not come back in the history")
	}
	// Claiming again is fine; claiming from another account is not.
	if rec := as(t, app, ada.Token, http.MethodPost, "/x/identity/me/orders", map[string]any{"number": number, "token": token}); rec.Code != http.StatusOK {
		t.Errorf("second claim = %d, want 200", rec.Code)
	}
	if rec := as(t, app, bob.Token, http.MethodPost, "/x/identity/me/orders", map[string]any{"number": number, "token": token}); rec.Code != http.StatusConflict {
		t.Errorf("claim from another account = %d, want 409", rec.Code)
	}

	rec = as(t, app, ada.Token, http.MethodGet, "/x/identity/me/orders", nil)
	var history []gocommerce.Order
	gctest.DecodeData(t, rec, &history)
	if len(history) != 1 || history[0].Number != number {
		t.Errorf("history = %+v", history)
	}
	if rec := as(t, app, ada.Token, http.MethodGet, "/x/identity/me/orders/"+number, nil); rec.Code != http.StatusOK {
		t.Errorf("get from history = %d: %s", rec.Code, rec.Body)
	}
	if rec := as(t, app, bob.Token, http.MethodGet, "/x/identity/me/orders/"+number, nil); rec.Code != http.StatusNotFound {
		t.Errorf("another account reading the order = %d, want 404", rec.Code)
	}
}

func TestAdminSeesAccountsAndCanDeleteOne(t *testing.T) {
	app, _ := newApp(t)
	ada := register(t, app, "ada@example.com")
	register(t, app, "bob@example.com")

	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/identity/customers?q=ada%40", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin list = %d: %s", rec.Code, rec.Body)
	}
	var list []Customer
	gctest.DecodeData(t, rec, &list)
	if len(list) != 1 || list[0].Email != "ada@example.com" {
		t.Errorf("list = %+v", list)
	}
	// A shopper session opens no admin door.
	if rec := as(t, app, ada.Token, http.MethodGet, "/api/admin/x/identity/customers", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("admin list with a shopper token = %d, want 401", rec.Code)
	}

	id := strconv.FormatInt(ada.Record.ID, 10)
	if rec := gctest.AdminRequest(t, app, http.MethodDelete, "/api/admin/x/identity/customers/"+id, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body)
	}
	if rec := as(t, app, ada.Token, http.MethodGet, "/x/identity/me", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("session of a deleted account = %d, want 401", rec.Code)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodDelete, "/api/admin/x/identity/customers/"+id, nil); rec.Code != http.StatusNotFound {
		t.Errorf("deleting twice = %d, want 404", rec.Code)
	}
}

// TestEveryRouteIsDocumented is the module's copy of core's contract check:
// a route this module serves must appear in the merged OpenAPI document.
func TestEveryRouteIsDocumented(t *testing.T) {
	app, _ := newApp(t)
	paths, err := app.SpecPaths()
	if err != nil {
		t.Fatalf("SpecPaths: %v", err)
	}
	documented := map[string]bool{}
	for _, p := range paths {
		documented[p] = true
	}
	var served int
	for _, r := range app.Routes() {
		if r.Owner != "identity" {
			continue
		}
		served++
		if !documented[r.Path] {
			t.Errorf("route %s %s is served but absent from the contract", r.Method, r.Path)
		}
	}
	if served == 0 {
		t.Fatal("the module registered no routes")
	}
}

// TestAdminAccountRoutesAreGated proves the asymmetry is deliberate: reading
// account holders is accounts.read, and erasing one needs the module's own
// operating right as well.
//
// A session, not gctest.AdminToken: a static admin token carries every right,
// so it cannot tell a gated route from an open one.
func TestAdminAccountRoutesAreGated(t *testing.T) {
	app, _ := newApp(t)
	ada := register(t, app, "ada@example.com")
	id := strconv.FormatInt(ada.Record.ID, 10)

	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)
	owner := gctest.OperatorToken(t, app, "owner@example.com", gocommerce.RoleOwner)

	if rec := gctest.SessionRequest(t, app, staff, http.MethodGet, "/api/admin/x/identity/customers", nil); rec.Code != http.StatusOK {
		t.Errorf("staff listing accounts = %d, want 200: %s", rec.Code, rec.Body)
	}
	if rec := gctest.SessionRequest(t, app, staff, http.MethodGet, "/api/admin/x/identity/customers/"+id, nil); rec.Code != http.StatusOK {
		t.Errorf("staff reading an account = %d, want 200: %s", rec.Code, rec.Body)
	}

	rec := gctest.SessionRequest(t, app, staff, http.MethodDelete, "/api/admin/x/identity/customers/"+id, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("staff erasing an account = %d, want 403: %s", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), "accounts.erase") {
		t.Errorf("the refusal does not name the missing right: %s", rec.Body)
	}

	if rec := gctest.SessionRequest(t, app, owner, http.MethodDelete, "/api/admin/x/identity/customers/"+id, nil); rec.Code != http.StatusNoContent {
		t.Errorf("owner erasing an account = %d, want 204: %s", rec.Code, rec.Body)
	}
}

// TestAccountListIsRefusedWithoutCustomersRead is the privilege widening this
// gate closed, reproduced.
//
// A role deliberately cut down to the catalog is refused the engine's own
// customer route; before the module named a right, the identical request here
// returned every account holder's email, name and phone. Installing a module
// must not widen who may read personal data.
func TestAccountListIsRefusedWithoutCustomersRead(t *testing.T) {
	app, _ := newApp(t)
	register(t, app, "ada@example.com")

	// catalog.read is the floor every role keeps, so this is the narrowest
	// legal cut of staff.
	if _, err := app.Roles().Set(context.Background(), gocommerce.RoleStaff,
		[]gocommerce.Right{gocommerce.RightCatalogRead}, nil); err != nil {
		t.Fatalf("re-cut staff: %v", err)
	}
	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)

	rec := gctest.SessionRequest(t, app, staff, http.MethodGet, "/api/admin/x/identity/customers", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a catalog-only role listing accounts = %d, want 403: %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "ada@example.com") {
		t.Error("the refusal leaked the data it refused")
	}
}

func TestModuleContract(t *testing.T) {
	app, _ := newApp(t)
	gctest.AssertAdminRoutesDeclareRights(t, app, "identity")
	gctest.AssertSpecCoversModuleRoutes(t, app, "identity")
}

// ------------------------------------------------------- email confirmation

// requestConfirmation asks for the confirmation email and returns the token
// it carried.
func requestConfirmation(t *testing.T, app *gocommerce.App, mail *capture, session string) string {
	t.Helper()
	rec := as(t, app, session, http.MethodPost, "/x/identity/me/email-verification", nil)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("request confirmation = %d: %s", rec.Code, rec.Body)
	}
	note := mail.last()
	if note == nil || note.Event != EventEmailVerification {
		t.Fatalf("no confirmation email; last notification %+v", note)
	}
	token := note.Data["verify_token"]
	if token == "" {
		t.Fatalf("no verify_token in %v", note.Data)
	}
	return token
}

func confirmToken(t *testing.T, app *gocommerce.App, token string) *httptest.ResponseRecorder {
	t.Helper()
	return gctest.Request(t, app, http.MethodPost, "/x/identity/email-verification/confirm",
		map[string]any{"token": token})
}

// confirmAddress runs the whole flow for a signed-in shopper.
func confirmAddress(t *testing.T, app *gocommerce.App, mail *capture, session string) Customer {
	t.Helper()
	rec := confirmToken(t, app, requestConfirmation(t, app, mail, session))
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d: %s", rec.Code, rec.Body)
	}
	var c Customer
	gctest.DecodeData(t, rec, &c)
	if !c.EmailVerified {
		t.Fatalf("confirmed account came back unconfirmed: %+v", c)
	}
	return c
}

// forgetCooldown stands in for the minute a real shopper would wait between
// two confirmation emails.
func forgetCooldown(t *testing.T, app *gocommerce.App, id int64) {
	t.Helper()
	if _, err := app.DB().ExecContext(context.Background(),
		`UPDATE identity_customers SET email_verification_sent_at = now() - interval '1 hour' WHERE id = $1`,
		id); err != nil {
		t.Fatalf("forget cooldown: %v", err)
	}
}

func account(t *testing.T, app *gocommerce.App, session string) Customer {
	t.Helper()
	rec := as(t, app, session, http.MethodGet, "/x/identity/me", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("me = %d: %s", rec.Code, rec.Body)
	}
	var c Customer
	gctest.DecodeData(t, rec, &c)
	return c
}

func TestEmailVerification(t *testing.T) {
	mail := &capture{}
	app := gctest.New(t, New(Config{Notifier: mail, VerifyURL: "https://shop.example/confirm?token={token}"}))
	ada := register(t, app, "ada@example.com")

	// Signing up proves nothing and sends nothing: the storefront asks.
	if ada.Record.EmailVerified {
		t.Error("a new account came back confirmed")
	}
	if note := mail.last(); note != nil {
		t.Errorf("signup sent %s; asking is the storefront's call", note.Event)
	}

	if rec := as(t, app, "", http.MethodPost, "/x/identity/me/email-verification", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("request without a session = %d, want 401", rec.Code)
	}

	token := requestConfirmation(t, app, mail, ada.Token)
	note := mail.last()
	if note.Channel != gocommerce.ChannelEmail || note.To != "ada@example.com" {
		t.Errorf("notification = %+v", note)
	}
	if want := "https://shop.example/confirm?token=" + token; note.Data["verify_url"] != want {
		t.Errorf("verify_url = %q, want %q", note.Data["verify_url"], want)
	}
	if note.Data["expires_in_minutes"] != "1440" {
		t.Errorf("expires_in_minutes = %q, want the 24-hour default", note.Data["expires_in_minutes"])
	}

	// Asking again at once is what a script filling an inbox would do.
	rec := as(t, app, ada.Token, http.MethodPost, "/x/identity/me/email-verification", nil)
	if rec.Code != http.StatusTooManyRequests || errorCode(t, rec) != "too_many_attempts" {
		t.Errorf("second request inside the cooldown = %d %s, want 429 too_many_attempts", rec.Code, rec.Body)
	}
	if mail.count() != 1 {
		t.Errorf("%d emails sent, want 1: the refused request still sent one", mail.count())
	}

	if rec := confirmToken(t, app, "bogus"); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_token" {
		t.Errorf("confirm with a made-up token = %d %s", rec.Code, rec.Body)
	}
	rec = confirmToken(t, app, token)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm = %d: %s", rec.Code, rec.Body)
	}
	var confirmed Customer
	gctest.DecodeData(t, rec, &confirmed)
	if !confirmed.EmailVerified || confirmed.Email != "ada@example.com" {
		t.Errorf("confirmed = %+v", confirmed)
	}
	if !account(t, app, ada.Token).EmailVerified {
		t.Error("GET /me does not show the confirmation")
	}

	// A token is single-use, and a confirmed address has nothing left to ask
	// for — cooldown or no cooldown.
	if rec := confirmToken(t, app, token); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_token" {
		t.Errorf("reused token = %d %s, want 400 invalid_token", rec.Code, rec.Body)
	}
	forgetCooldown(t, app, ada.Record.ID)
	if rec := as(t, app, ada.Token, http.MethodPost, "/x/identity/me/email-verification", nil); rec.Code != http.StatusConflict {
		t.Errorf("request for a confirmed address = %d %s, want 409", rec.Code, rec.Body)
	}
}

func TestChangingTheAddressWithdrawsTheConfirmation(t *testing.T) {
	app, mail := newApp(t)
	ada := register(t, app, "ada@example.com")

	// A link goes to the address the account has now, and the account moves
	// before anybody opens it.
	stale := requestConfirmation(t, app, mail, ada.Token)
	rec := as(t, app, ada.Token, http.MethodPatch, "/x/identity/me", map[string]any{"email": "ada.new@example.com"})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch email = %d: %s", rec.Code, rec.Body)
	}
	if rec := confirmToken(t, app, stale); rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_token" {
		t.Errorf("a link mailed to the old address = %d %s, want 400 invalid_token", rec.Code, rec.Body)
	}
	if account(t, app, ada.Token).EmailVerified {
		t.Fatal("a link mailed to the old address confirmed the new one")
	}

	// The wait outlives the move. If changing the address reset it, flipping
	// the address back and forth would be a way around it.
	if rec := as(t, app, ada.Token, http.MethodPost, "/x/identity/me/email-verification", nil); rec.Code != http.StatusTooManyRequests {
		t.Errorf("request straight after an address change = %d, want 429", rec.Code)
	}

	forgetCooldown(t, app, ada.Record.ID)
	confirmed := confirmAddress(t, app, mail, ada.Token)
	if mail.last().To != "ada.new@example.com" || confirmed.Email != "ada.new@example.com" {
		t.Errorf("confirmation went to %q and confirmed %q, want the new address for both",
			mail.last().To, confirmed.Email)
	}

	// Retyping the same address in another case is not a move.
	rec = as(t, app, ada.Token, http.MethodPatch, "/x/identity/me", map[string]any{"email": "ADA.New@example.com"})
	var same Customer
	gctest.DecodeData(t, rec, &same)
	if !same.EmailVerified {
		t.Error("retyping the confirmed address in another case withdrew the confirmation")
	}

	// A real move withdraws it.
	rec = as(t, app, ada.Token, http.MethodPatch, "/x/identity/me", map[string]any{"email": "countess@example.com"})
	var moved Customer
	gctest.DecodeData(t, rec, &moved)
	if moved.EmailVerified || account(t, app, ada.Token).EmailVerified {
		t.Error("changing the address kept the old address's confirmation")
	}
}

func TestAPasswordResetConfirmsTheAddress(t *testing.T) {
	app, mail := newApp(t)
	ada := register(t, app, "ada@example.com")

	reset := func(email string) string {
		t.Helper()
		rec := gctest.Request(t, app, http.MethodPost, "/x/identity/password-reset", map[string]any{"email": email})
		if rec.Code != http.StatusAccepted {
			t.Fatalf("reset %s = %d: %s", email, rec.Code, rec.Body)
		}
		note := mail.last()
		if note == nil || note.Event != EventPasswordReset {
			t.Fatalf("no reset email for %s", email)
		}
		return note.Data["reset_token"]
	}
	complete := func(token string) Customer {
		t.Helper()
		rec := gctest.Request(t, app, http.MethodPost, "/x/identity/password-reset/confirm", map[string]any{
			"token": token, "password": "battery staple",
		})
		if rec.Code != http.StatusOK {
			t.Fatalf("confirm reset = %d: %s", rec.Code, rec.Body)
		}
		var auth AuthResponse
		gctest.DecodeData(t, rec, &auth)
		return *auth.Record
	}

	// A reset mailed to the old address still resets the password, and proves
	// nothing about the address the account has moved to.
	stale := reset("ada@example.com")
	if rec := as(t, app, ada.Token, http.MethodPatch, "/x/identity/me", map[string]any{
		"email": "ada.new@example.com",
	}); rec.Code != http.StatusOK {
		t.Fatalf("patch email = %d: %s", rec.Code, rec.Body)
	}
	if got := complete(stale); got.EmailVerified {
		t.Error("a reset mailed to the old address confirmed the new one")
	}

	// One mailed to the address the account has proves it.
	if got := complete(reset("ada.new@example.com")); !got.EmailVerified {
		t.Error("completing a reset did not confirm the address it was mailed to")
	}

	// A reset issued before resets recorded their address confirms nothing:
	// there is no knowing where it went.
	bob := register(t, app, "bob@example.com")
	if _, err := app.DB().ExecContext(context.Background(), `
		INSERT INTO identity_password_resets (token_hash, customer_id, expires_at)
		VALUES ($1, $2, now() + interval '1 hour')`, hashToken("legacy-reset"), bob.Record.ID); err != nil {
		t.Fatalf("insert legacy reset: %v", err)
	}
	if got := complete("legacy-reset"); got.EmailVerified {
		t.Error("a reset with no recorded address confirmed one")
	}
}

// The money path (D66): a group price reaches a basket through a signed-in
// account only once the account has proved the mailbox the group lists.
func TestClaimCartPricesTheBasketAsTheAccount(t *testing.T) {
	app, mail := newApp(t)
	ctx := context.Background()

	// One product at 10000, and a group whose member pays 6000 for it.
	variant := gctest.CreateProduct(t, app, "TRADE-1", 10000, 50).DefaultVariant().ID
	group, err := app.Pricing().CreateGroup(ctx, gocommerce.CustomerGroupInput{Code: "dealers", Name: "Dealers"})
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	if err := app.Pricing().AddMember(ctx, group.ID, "dealer@example.com"); err != nil {
		t.Fatalf("member: %v", err)
	}
	list, err := app.Pricing().CreateList(ctx, gocommerce.PriceListInput{Name: "Dealer", GroupID: &group.ID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := app.Pricing().SetPrice(ctx, list.ID,
		gocommerce.PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: 6000}); err != nil {
		t.Fatalf("price: %v", err)
	}

	cart, err := app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatalf("cart: %v", err)
	}
	if cart, err = app.Cart().AddLine(ctx, cart.Token, variant, 2); err != nil {
		t.Fatalf("add line: %v", err)
	}
	if got := cart.Lines[0].UnitPrice.AmountMinor; got != 10000 {
		t.Fatalf("anonymous line = %d, want the catalogue 10000", got)
	}

	claim := func(session, cartID string) *httptest.ResponseRecorder {
		t.Helper()
		return as(t, app, session, http.MethodPost, "/x/identity/me/carts", map[string]any{"cart_id": cartID})
	}

	// Somebody signed up as the dealer's address. A session is all they hold.
	dealer := register(t, app, "dealer@example.com")
	if rec := claim("", cart.Token); rec.Code != http.StatusUnauthorized {
		t.Errorf("claim without a session = %d, want 401", rec.Code)
	}
	rec := claim(dealer.Token, cart.Token)
	if rec.Code != http.StatusForbidden || errorCode(t, rec) != "email_unverified" {
		t.Fatalf("claim by an unconfirmed account = %d %s, want 403 email_unverified", rec.Code, rec.Body)
	}
	still, err := app.Cart().GetByToken(ctx, cart.Token)
	if err != nil {
		t.Fatalf("get cart: %v", err)
	}
	if still.VerifiedEmail != "" || still.Lines[0].UnitPrice.AmountMinor != 10000 {
		t.Fatalf("after the refusal: verified %q, line %d; want neither touched",
			still.VerifiedEmail, still.Lines[0].UnitPrice.AmountMinor)
	}

	// Proving the mailbox is what the group price was waiting on.
	confirmAddress(t, app, mail, dealer.Token)
	rec = claim(dealer.Token, cart.Token)
	if rec.Code != http.StatusOK {
		t.Fatalf("claim by a confirmed account = %d: %s", rec.Code, rec.Body)
	}
	var priced gocommerce.Cart
	gctest.DecodeData(t, rec, &priced)
	if priced.VerifiedEmail != "dealer@example.com" {
		t.Errorf("verified_email = %q, want dealer@example.com", priced.VerifiedEmail)
	}
	line := priced.Lines[0]
	if line.UnitPrice.AmountMinor != 6000 || line.PriceChanged {
		t.Errorf("line = %d (changed %v), want re-priced to the member's 6000 at once",
			line.UnitPrice.AmountMinor, line.PriceChanged)
	}
	if priced.Subtotal.AmountMinor != 12000 {
		t.Errorf("subtotal = %d, want 2 x 6000", priced.Subtotal.AmountMinor)
	}

	if rec := claim(dealer.Token, ""); rec.Code != http.StatusBadRequest {
		t.Errorf("claim with no cart_id = %d, want 400", rec.Code)
	}
	if rec := claim(dealer.Token, "no-such-cart"); rec.Code != http.StatusNotFound {
		t.Errorf("claim of an unknown cart = %d, want 404", rec.Code)
	}
}

// MarkEmailVerified and the exact lookups are for other modules, which reach
// the module in Go rather than over HTTP.
func TestOtherModulesConfirmOnlyTheAccountsOwnAddress(t *testing.T) {
	mail := &capture{}
	mod := New(Config{Notifier: mail})
	app := gctest.New(t, mod)
	ctx := context.Background()
	ada := register(t, app, "ada@example.com")
	id := ada.Record.ID

	// A link is outstanding when the other module's proof arrives.
	if err := mod.RequestVerification(ctx, id); err != nil {
		t.Fatalf("request confirmation: %v", err)
	}
	pending := mail.last().Data["verify_token"]

	if _, err := mod.MarkEmailVerified(ctx, id, "someone.else@example.com"); !errors.Is(err, gocommerce.ErrConflict) {
		t.Errorf("a proof about another address = %v, want a conflict", err)
	}
	if _, err := mod.MarkEmailVerified(ctx, id, "  "); !errors.Is(err, gocommerce.ErrValidation) {
		t.Errorf("no address = %v, want a validation error", err)
	}
	if _, err := mod.MarkEmailVerified(ctx, 1<<40, "ada@example.com"); !errors.Is(err, gocommerce.ErrNotFound) {
		t.Errorf("no such account = %v, want not found", err)
	}
	if c, err := mod.CustomerByID(ctx, id); err != nil || c.EmailVerified {
		t.Fatalf("after the refusals: %+v, %v; want still unconfirmed", c, err)
	}

	first, err := mod.MarkEmailVerified(ctx, id, " ADA@example.com ")
	if err != nil || !first.EmailVerified {
		t.Fatalf("mark the account's own address = %+v, %v", first, err)
	}
	again, err := mod.MarkEmailVerified(ctx, id, "ada@example.com")
	if err != nil || !again.EmailVerified {
		t.Errorf("marking twice = %+v, %v; want the same answer", again, err)
	}
	if again != nil && !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Error("marking a confirmed address again touched the account")
	}
	if rec := confirmToken(t, app, pending); rec.Code != http.StatusBadRequest {
		t.Errorf("the link outstanding before the proof = %d, want 400: it has nothing left to prove", rec.Code)
	}

	// Exact matches, which List's substring search is not.
	byEmail, err := mod.CustomerByEmail(ctx, "Ada@Example.com")
	if err != nil || byEmail.ID != id || !byEmail.EmailVerified {
		t.Errorf("CustomerByEmail = %+v, %v", byEmail, err)
	}
	if _, err := mod.CustomerByEmail(ctx, "ada@"); !errors.Is(err, gocommerce.ErrNotFound) {
		t.Errorf("CustomerByEmail of a fragment = %v, want not found", err)
	}
	if _, err := mod.CustomerByID(ctx, 1<<40); !errors.Is(err, gocommerce.ErrNotFound) {
		t.Errorf("CustomerByID of nobody = %v, want not found", err)
	}
}
