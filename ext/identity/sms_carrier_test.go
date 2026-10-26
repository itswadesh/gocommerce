package identity

import (
	"context"
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// pickySMS is an SMS provider that carries only the messages it was set up
// for, as MSG91 does: it texts nothing it has no DLT template for.
type pickySMS struct {
	rec    *gctest.RecordingNotifier
	serves map[string]bool
}

func (pickySMS) Name() string                       { return "picky-sms" }
func (pickySMS) Migrations() []gocommerce.Migration { return nil }
func (p pickySMS) Register(app *gocommerce.App) error {
	app.RegisterNotifier(gocommerce.ChannelSMS, p)
	return nil
}
func (p pickySMS) Notify(ctx context.Context, n gocommerce.Notification) error {
	return p.rec.Notify(ctx, n)
}
func (p pickySMS) Serves(_ context.Context, n gocommerce.Notification) bool { return p.serves[n.Event] }

// A store whose SMS provider texts about orders but has nothing set up for
// sign-in codes cannot sign anybody in by phone. Asking for a code says so,
// rather than answering "sent" for a text that only reaches the log.
func TestASignInCodeNobodyWouldCarryIsRefused(t *testing.T) {
	rec := &gctest.RecordingNotifier{}
	sms := pickySMS{rec: rec, serves: map[string]bool{gocommerce.EventOrderShipped: true}}
	app := gctest.New(t, New(Config{}), sms)
	signInBy(t, app, LoginPhoneOTP)

	got := gctest.Request(t, app, http.MethodPost, "/x/identity/otp", map[string]any{"phone": "+919876543210"})
	if got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "sign-in codes") {
		t.Fatalf("ask for a code = %d %s, want 409 saying sign-in codes cannot be texted", got.Code, got.Body)
	}
	if n := len(rec.All()); n != 0 {
		t.Fatalf("texted %d messages, want none", n)
	}

	sms.serves[EventLoginCode] = true
	if got := gctest.Request(t, app, http.MethodPost, "/x/identity/otp", map[string]any{"phone": "+919876543210"}); got.Code != http.StatusAccepted {
		t.Fatalf("with sign-in codes set up, ask = %d %s", got.Code, got.Body)
	}
}

// The sign-in text reads like the DLT template MSG91 carries, so a store on
// another SMS provider sends the same words. "<#>" lets Android read the code
// for the shopper.
func TestTheSignInTextReadsLikeTheDLTTemplate(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	ctx := context.Background()
	tpl, ok, err := app.NotifyTemplates().GetIn(ctx, gocommerce.ChannelSMS, EventLoginCode, "en")
	if err != nil || !ok {
		t.Fatalf("sign-in template: ok=%v err=%v", ok, err)
	}
	got, err := gocommerce.RenderNotifyText(tpl.Body, map[string]string{"code": "0420", "store_name": "Chillish", "expires_in_minutes": "10"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "<#> Hi, 0420 is your OTP to login to Chillish"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}
