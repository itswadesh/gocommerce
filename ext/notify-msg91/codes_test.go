package msg91

import (
	"context"
	"testing"

	"github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func codeNote(event, code string) gocommerce.Notification {
	return gocommerce.Notification{
		Event: event, Channel: gocommerce.ChannelSMS, To: "+91 98765 43210",
		Data: map[string]string{"code": code, "store_name": "Chillish", "expires_in_minutes": "10"},
	}
}

// The sign-in and order codes are texts like any other: each has its own
// template id, and with one MSG91 sends the code as a variable.
func TestTextsTheSignInAndOrderCodes(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true}, New(Config{BaseURL: server.URL, AuthKey: "k"}))
	ctx := context.Background()

	if app.WouldDeliver(ctx, codeNote(EventLoginCode, "0420")) {
		t.Fatal("with no sign-in template, the code would reach only the log")
	}
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{
		Settings: map[string]any{"template_sign_in_code": "tpl-signin", "template_order_code": "tpl-order"},
	}); err != nil {
		t.Fatal(err)
	}
	if !app.WouldDeliver(ctx, codeNote(EventLoginCode, "0420")) {
		t.Fatal("with a sign-in template, MSG91 should carry the code")
	}

	for _, c := range []struct{ event, code, tpl string }{
		{EventLoginCode, "0420", "tpl-signin"},
		{gocommerce.EventOrderLookupCode, "1234", "tpl-order"},
	} {
		if err := app.Notify(ctx, codeNote(c.event, c.code)); err != nil {
			t.Fatalf("%s: %v", c.event, err)
		}
		calls := s.all()
		last := calls[len(calls)-1]
		if last.TemplateID != c.tpl || last.Recipients[0]["code"] != c.code || last.Recipients[0]["mobiles"] != "919876543210" {
			t.Errorf("%s sent %+v, want template %s carrying the code", c.event, last, c.tpl)
		}
		// A DLT template names its variables ##otp## and ##website##.
		if last.Recipients[0]["otp"] != c.code || last.Recipients[0]["website"] != "Chillish" {
			t.Errorf("%s sent %+v, want otp and website for the template's variables", c.event, last.Recipients[0])
		}
	}
}

// The install's own template ids, from its environment, serve every store
// that has not set its own, as the install's auth key does. A store's own id
// wins.
func TestTemplatesFromTheEnvironmentServeEveryStore(t *testing.T) {
	env := map[string]string{"SMS_OTP_TEMPLATE_ID": " env-signin ", "MSG91_TEMPLATE_ORDER_SHIPPED": "env-shipped", "UNRELATED": "x"}
	tpl := EnvTemplates(func(k string) string { return env[k] })
	if len(tpl) != 2 || tpl[EventLoginCode] != "env-signin" || tpl[gocommerce.EventOrderShipped] != "env-shipped" {
		t.Fatalf("EnvTemplates = %v, want the sign-in and shipped ids", tpl)
	}

	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true}, New(Config{BaseURL: server.URL, AuthKey: "k", Templates: tpl}))
	ctx := context.Background()
	if err := app.Notify(ctx, codeNote(EventLoginCode, "0420")); err != nil {
		t.Fatal(err)
	}
	if calls := s.all(); len(calls) != 1 || calls[0].TemplateID != "env-signin" {
		t.Fatalf("calls = %+v, want the install's sign-in template", calls)
	}

	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{
		Settings: map[string]any{"template_sign_in_code": "store-signin"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.Notify(ctx, codeNote(EventLoginCode, "0421")); err != nil {
		t.Fatal(err)
	}
	if calls := s.all(); calls[len(calls)-1].TemplateID != "store-signin" {
		t.Fatalf("last call = %+v, want the store's own template", calls[len(calls)-1])
	}
}

// The install's DLT sender goes with every text, so MSG91 sends from the
// header the template is registered under.
func TestTextsGoFromTheInstallsSender(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.NewWithConfig(t, gocommerce.Config{AllowPrivateURLs: true}, New(Config{
		BaseURL: server.URL, AuthKey: "k", Sender: " MISIKI ",
		Templates: map[string]string{EventLoginCode: "tpl-signin"},
	}))
	if err := app.Notify(context.Background(), codeNote(EventLoginCode, "0420")); err != nil {
		t.Fatal(err)
	}
	if calls := s.all(); len(calls) != 1 || calls[0].Sender != "MISIKI" {
		t.Fatalf("calls = %+v, want one text from MISIKI", calls)
	}
}
