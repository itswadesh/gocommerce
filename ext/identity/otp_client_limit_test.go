package identity

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/itswadesh/gocommerce/gctest"
)

// Limits per phone do not stop one client texting many numbers. The two routes that
// text share the client's count (D212).
func TestAShopperClientGetsTwentyCodesAnHour(t *testing.T) {
	app, mod, sms := phoneApp(t)
	for i := range loginCodesPerClientHr {
		phoneCode(t, app, mod, sms, fmt.Sprintf("+9198767%05d", i))
	}
	rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp", map[string]any{"phone": "+919876799999"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the next code = %d, want 429: %s", rec.Code, rec.Body)
	}
	mod.waitForSends(5 * time.Second)
	if got := sms.count(); got != loginCodesPerClientHr {
		t.Fatalf("%d texts, want %d", got, loginCodesPerClientHr)
	}
	// A number that is refused anyway says what is wrong with it.
	rec = gctest.Request(t, app, http.MethodPost, "/x/identity/otp", map[string]any{"phone": "not a number"})
	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("a refused number was answered with the limit")
	}
}

func TestAShopperClientsLimitCoversLinkingAPhone(t *testing.T) {
	sms := &capture{}
	mod := New(Config{Notifier: sms})
	app := gctest.New(t, mod)
	signInBy(t, app, LoginEither)
	auth := register(t, app, "ada@example.com")
	for i := range loginCodesPerClientHr {
		if rec := as(t, app, auth.Token, http.MethodPost, "/x/identity/me/phone", map[string]any{"phone": fmt.Sprintf("+9198767%05d", i)}); rec.Code != http.StatusAccepted {
			t.Fatalf("link %d = %d: %s", i, rec.Code, rec.Body)
		}
	}
	rec := as(t, app, auth.Token, http.MethodPost, "/x/identity/me/phone", map[string]any{"phone": "+919876799998"})
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("linking = %d, want 429: %s", rec.Code, rec.Body)
	}
}
