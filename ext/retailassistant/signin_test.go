package retailassistant

import (
	"context"
	"net/http"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const ownerPhone = "+919800000077"

func signinApp(t *testing.T, template string) (*gocommerce.App, *fakeMeta) {
	t.Helper()
	meta := &fakeMeta{files: map[string][]byte{}}
	meta.srv = gctest.StubHTTP(t, meta.handler)
	mod := New(Config{
		AppSecret: testSecret, VerifyToken: "verify-me", AccessToken: "test-access-token", PhoneNumberID: phoneID,
		BaseURL: meta.srv.URL, Client: meta.srv.Client(), LoginTemplate: template,
	})
	app := gctest.NewWithConfig(t, gocommerce.Config{Currency: "INR"}, mod)
	return app, meta
}

func (f *fakeMeta) waitFor(t *testing.T, phone string) sentMessage {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if got := f.last(phone); got.To != "" {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("nothing was sent to %s", phone)
	return sentMessage{}
}

// ownerWithPhone is an operator who added a phone the way a person does: by asking
// for a code on it and giving it back with their password.
func ownerWithPhone(t *testing.T, app *gocommerce.App, meta *fakeMeta) {
	t.Helper()
	ctx := context.Background()
	const password = "a long enough password"
	su, err := app.Superusers().Create(ctx, "owner@example.com", password, gocommerce.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Superusers().RequestOwnPhoneCode(ctx, su.ID, ownerPhone, "203.0.113.9"); err != nil {
		t.Fatalf("ask to add a phone: %v", err)
	}
	if _, err := app.Superusers().SetOwnPhone(ctx, su.ID, password, ownerPhone, meta.waitFor(t, digits(ownerPhone)).Body); err != nil {
		t.Fatalf("add a phone: %v", err)
	}
	meta.mu.Lock()
	meta.sent = nil
	meta.mu.Unlock()
	// Core allows one code a minute per number, and this one just had one.
	if _, err := app.DB().ExecContext(ctx, `DELETE FROM superuser_login_codes`); err != nil {
		t.Fatal(err)
	}
}

func signinState(t *testing.T, app *gocommerce.App) map[string]any {
	t.Helper()
	rec := gctest.Request(t, app, http.MethodGet, "/api/admin/auth-state", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth state = %d %s", rec.Code, rec.Body)
	}
	var out map[string]any
	gctest.DecodeData(t, rec, &out)
	return out
}

func TestAWhatsAppCodeSignsInThroughCoresOwnFlow(t *testing.T) {
	app, meta := signinApp(t, "retail_login")
	ownerWithPhone(t, app, meta)
	if signinState(t, app)["phone_login"] != true {
		t.Fatal("the sign-in screen does not offer a phone")
	}
	rec := gctest.Request(t, app, http.MethodPost, "/api/admin/auth-with-phone/code", map[string]string{"phone": ownerPhone})
	if rec.Code >= 300 {
		t.Fatalf("ask for a code = %d %s", rec.Code, rec.Body)
	}
	sent := meta.waitFor(t, digits(ownerPhone))
	if sent.Template != "retail_login" || sent.Components != 2 || sent.Body == "" {
		t.Fatalf("sent = %+v", sent)
	}

	right := gctest.Request(t, app, http.MethodPost, "/api/admin/auth-with-phone", map[string]string{"phone": ownerPhone, "code": sent.Body})
	if right.Code != http.StatusOK {
		t.Fatalf("the right code = %d %s", right.Code, right.Body)
	}
	var session struct {
		Token string `json:"token"`
	}
	gctest.DecodeData(t, right, &session)
	if session.Token == "" {
		t.Fatalf("no session: %s", right.Body)
	}
	if me := gctest.SessionRequest(t, app, session.Token, http.MethodGet, "/api/admin/me", nil); me.Code != http.StatusOK {
		t.Fatalf("the session = %d %s", me.Code, me.Body)
	}
	replay := gctest.Request(t, app, http.MethodPost, "/api/admin/auth-with-phone", map[string]string{"phone": ownerPhone, "code": sent.Body})
	if replay.Code < 400 {
		t.Fatalf("a used code worked again = %d", replay.Code)
	}
	gctest.AssertNotificationLogHides(t, app, gocommerce.EventLoginCode, sent.Body)
}

func TestWithNoTemplateNothingChanges(t *testing.T) {
	app, meta := signinApp(t, "")
	if signinState(t, app)["phone_login"] == true {
		t.Fatal("a phone was offered with nothing to carry a code")
	}
	rec := gctest.Request(t, app, http.MethodPost, "/api/admin/auth-with-phone/code", map[string]string{"phone": ownerPhone})
	if rec.Code < 400 {
		t.Fatalf("ask for a code = %d", rec.Code)
	}
	if got := meta.to(digits(ownerPhone)); len(got) != 0 {
		t.Fatalf("sent %q", got)
	}
}

// A number's being linked must not change the answer, or asking would say which
// phones belong to a store.
func TestASignInRequestSaysNothingAboutWhoANumberBelongsTo(t *testing.T) {
	app, meta := signinApp(t, "retail_login")
	ownerWithPhone(t, app, meta)
	ask := func(phone string) (int, string) {
		rec := gctest.Request(t, app, http.MethodPost, "/api/admin/auth-with-phone/code", map[string]string{"phone": phone})
		return rec.Code, rec.Body.String()
	}
	knownCode, knownBody := ask(ownerPhone)
	strangerCode, strangerBody := ask("+919800000066")
	if knownCode != strangerCode || knownBody != strangerBody {
		t.Fatalf("known %d %s, stranger %d %s", knownCode, knownBody, strangerCode, strangerBody)
	}
}

func TestOtherTextMessagesAreNotCarried(t *testing.T) {
	l := loginCodes{}
	if l.Serves(context.Background(), gocommerce.Notification{Event: gocommerce.EventOrderCreated, Channel: gocommerce.ChannelSMS}) {
		t.Fatal("an order message would go on the sign-in template")
	}
	if !l.Serves(context.Background(), gocommerce.Notification{Event: gocommerce.EventLoginCode, Channel: gocommerce.ChannelSMS}) {
		t.Fatal("a sign-in code was not taken")
	}
}
