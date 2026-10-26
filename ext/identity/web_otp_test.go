package identity

import (
	"context"
	"net/http"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// Chrome on Android fills a code in by itself when the text ends with
// "@<site> #<code>" and the page is on that site (the Web OTP API). The
// storefront says which site it is on; the code carries it only when the site
// is this store's own, or a page elsewhere could have a shopper's code filled
// in for it.
func TestTheSignInCodeNamesTheStoresOwnSite(t *testing.T) {
	sms := &capture{}
	mod := New(Config{Notifier: sms})
	app := gctest.New(t, mod)
	signInBy(t, app, LoginPhoneOTP)
	if _, err := mod.db.ExecContext(context.Background(), `INSERT INTO store_domains (host, store_id, verified_at)
		SELECT 'first.shops.test', id, now() FROM stores ORDER BY created_at LIMIT 1`); err != nil {
		t.Fatal(err)
	}

	ask := func(phone, domain string) *gocommerce.Notification {
		t.Helper()
		if _, err := mod.db.ExecContext(context.Background(), `UPDATE identity_login_codes SET created_at = now() - interval '2 minutes'`); err != nil {
			t.Fatal(err)
		}
		rec := gctest.Request(t, app, http.MethodPost, "/x/identity/otp", map[string]any{"phone": phone, "domain": domain})
		if rec.Code != http.StatusAccepted {
			t.Fatalf("ask = %d %s", rec.Code, rec.Body)
		}
		mod.waitForSends(5 * time.Second)
		return sms.last()
	}

	if got := ask("+919876543210", "First.Shops.Test:443"); got.Data["domain"] != "first.shops.test" {
		t.Fatalf("own site: domain = %q, want first.shops.test", got.Data["domain"])
	}
	if got := ask("+919876543211", "evil.example"); got.Data["domain"] != "" {
		t.Fatalf("another site: domain = %q, want none", got.Data["domain"])
	}
	if got := ask("+919876543212", ""); got.Data["domain"] != "" {
		t.Fatalf("no site: domain = %q, want none", got.Data["domain"])
	}
}

// The engine's own sign-in text ends with the line Chrome reads, when there
// is a site to name.
func TestTheSignInTextEndsWithTheWebOTPLine(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	tpl, ok, err := app.NotifyTemplates().GetIn(context.Background(), gocommerce.ChannelSMS, EventLoginCode, "en")
	if err != nil || !ok {
		t.Fatalf("sign-in template: ok=%v err=%v", ok, err)
	}
	got, err := gocommerce.RenderNotifyText(tpl.Body, map[string]string{"code": "0420", "store_name": "Chillish", "domain": "first.shops.test"})
	if err != nil {
		t.Fatal(err)
	}
	if want := "<#> Hi, 0420 is your OTP to login to Chillish\n\n@first.shops.test #0420"; got != want {
		t.Fatalf("text = %q, want %q", got, want)
	}
}
