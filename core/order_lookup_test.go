package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// A guest opens their order on the storefront with its number and a 4-digit
// code sent to the email or phone on it, not a link (D115).

// smsAndEmail records both channels a code can go by.
type smsAndEmail struct{ rec *recordingNotifier }

func (m *smsAndEmail) Name() string            { return "recorder" }
func (m *smsAndEmail) Migrations() []Migration { return nil }
func (m *smsAndEmail) Register(app *App) error {
	app.RegisterNotifier(ChannelEmail, m.rec)
	app.RegisterNotifier(ChannelSMS, m.rec)
	return nil
}

// lookupApp is an engine with recording email and SMS backends and one
// placed order.
func lookupApp(t *testing.T) (*App, *recordingNotifier, *Order) {
	t.Helper()
	rec := &recordingNotifier{}
	app := newTestApp(t, &smsAndEmail{rec: rec})
	return app, rec, placeOrder(t, app, "LOOKUP-1")
}

// lookupCodes drains the sends in flight and returns the codes mailed so far.
func lookupCodes(t *testing.T, app *App, rec *recordingNotifier) []Notification {
	t.Helper()
	app.waitForLookupSends(5 * time.Second)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	var out []Notification
	for _, n := range rec.sent {
		if n.Event == EventOrderLookupCode {
			out = append(out, n)
		}
	}
	return out
}

func askCode(t *testing.T, app *App, number, contact string) int {
	t.Helper()
	return doBody(t, app, http.MethodPost, "/api/orders/"+number+"/code", `{"contact":"`+contact+`"}`).Code
}

func tryCode(t *testing.T, app *App, number, code string) (int, OrderLookup) {
	t.Helper()
	rec := doBody(t, app, http.MethodPost, "/api/orders/"+number+"/code/verify", `{"code":"`+code+`"}`)
	var out OrderLookup
	if rec.Code == http.StatusOK {
		var env struct {
			Data OrderLookup `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode %s: %v", rec.Body, err)
		}
		out = env.Data
	}
	return rec.Code, out
}

func TestACodeOpensTheOrder(t *testing.T) {
	app, rec, order := lookupApp(t)

	if code := askCode(t, app, order.Number, "Shopper@Example.com"); code != http.StatusAccepted {
		t.Fatalf("ask = %d, want 202", code)
	}
	sent := lookupCodes(t, app, rec)
	if len(sent) != 1 || sent[0].To != "shopper@example.com" || sent[0].Channel != ChannelEmail {
		t.Fatalf("sent = %+v, want one email to the order's address", sent)
	}
	code := sent[0].Data["code"]
	if len(code) != 4 {
		t.Fatalf("code = %q, want 4 digits", code)
	}

	status, got := tryCode(t, app, order.Number, code)
	if status != http.StatusOK || got.Order == nil || got.Order.Number != order.Number || got.AccessToken == "" {
		t.Fatalf("verify = %d %+v, want the order and its token", status, got)
	}
	// The token is the one the storefront already reads orders with.
	if _, err := app.Order().GetForGuest(context.Background(), order.Number, got.AccessToken); err != nil {
		t.Fatalf("the returned token opens nothing: %v", err)
	}
	// A code is spent once it has opened the order.
	if status, _ := tryCode(t, app, order.Number, code); status == http.StatusOK {
		t.Fatal("a used code opened the order again")
	}
}

// Somebody guessing at order numbers learns nothing: a contact that is not
// the order's gets the same answer and no message is sent.
func TestACodeGoesOnlyToTheOrdersOwnContact(t *testing.T) {
	app, rec, order := lookupApp(t)
	for _, contact := range []string{"someone@else.com", "9999999999"} {
		if code := askCode(t, app, order.Number, contact); code != http.StatusAccepted {
			t.Fatalf("ask with %s = %d, want 202", contact, code)
		}
	}
	if code := askCode(t, app, "GC-999999", "shopper@example.com"); code != http.StatusAccepted {
		t.Fatalf("ask for an unknown order = %d, want 202", code)
	}
	if sent := lookupCodes(t, app, rec); len(sent) != 0 {
		t.Fatalf("sent = %+v, want nothing", sent)
	}
}

// Four digits are guessable, so a code takes five tries and then opens
// nothing, even the right digits.
func TestAWrongCodeSpendsATry(t *testing.T) {
	app, rec, order := lookupApp(t)
	askCode(t, app, order.Number, "shopper@example.com")
	code := lookupCodes(t, app, rec)[0].Data["code"]
	wrong := "0000"
	if code == wrong {
		wrong = "1111"
	}
	for i := 0; i < orderLookupAttempts; i++ {
		if status, _ := tryCode(t, app, order.Number, wrong); status != http.StatusUnprocessableEntity {
			t.Fatalf("wrong code %d = %d, want 422", i+1, status)
		}
	}
	if status, _ := tryCode(t, app, order.Number, code); status == http.StatusOK {
		t.Fatal("the right code worked after every try was spent")
	}
}

// A shop that cannot text says so, before anything about the order.
func TestAShopWithoutSMSSaysSo(t *testing.T) {
	rec := &recordingNotifier{}
	app := newTestApp(t, &notifyModule{rec: rec})
	order := placeOrder(t, app, "LOOKUP-2")
	if code := askCode(t, app, order.Number, "98765 43210"); code != http.StatusConflict {
		t.Fatalf("ask by phone with no SMS provider = %d, want 409", code)
	}
}

// One code a minute, so the button cannot be used to flood somebody's inbox.
func TestCodesAreNotSentBackToBack(t *testing.T) {
	app, rec, order := lookupApp(t)
	askCode(t, app, order.Number, "shopper@example.com")
	if code := askCode(t, app, order.Number, "shopper@example.com"); code != http.StatusTooManyRequests {
		t.Fatalf("second ask at once = %d, want 429", code)
	}
	if sent := lookupCodes(t, app, rec); len(sent) != 1 {
		t.Fatalf("sent %d codes, want 1", len(sent))
	}
}

// A phone matches by its last ten digits, so +91 and spaces do not matter.
func TestAPhoneIsMatchedByItsDigits(t *testing.T) {
	if !sameContact("+91 98765 43210", "", "9876543210") {
		t.Error("a +91 number did not match its ten digits")
	}
	if sameContact("98765 43210", "", "") {
		t.Error("a phone matched an order with none")
	}
	if !sameContact(" A@B.co ", "a@b.co", "") {
		t.Error("an email did not match regardless of case and spaces")
	}
}
