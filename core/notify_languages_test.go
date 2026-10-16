package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func sellIn(t *testing.T, app *App, ctx context.Context, langs ...string) {
	t.Helper()
	if _, err := app.Profile().Set(ctx, StoreProfile{Languages: langs}, nil); err != nil {
		t.Fatalf("sell in %v: %v", langs, err)
	}
}

// Each language has its own wording, and one nobody has worded borrows the
// main language's before falling back to the default (D87).
func TestEachLanguageHasItsOwnWording(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	sellIn(t, app, ctx, "en", "hi")
	tpls := app.NotifyTemplates()

	got, _, err := tpls.GetIn(ctx, ChannelEmail, EventOrderShipped, "hi")
	if err != nil || got.Source != WordingDefault || got.Customized || got.Language != "hi" {
		t.Fatalf("nothing worded: %+v, %v", got, err)
	}
	if _, err := tpls.Set(ctx, ChannelEmail, EventOrderShipped, "Shipped {{.order_number}}", "On its way."); err != nil {
		t.Fatal(err)
	}
	got, _, _ = tpls.GetIn(ctx, ChannelEmail, EventOrderShipped, "hi")
	if got.Source != WordingMain || got.Customized || got.Subject != "Shipped {{.order_number}}" {
		t.Errorf("Hindi before it is worded = %+v, want the main language's", got)
	}
	if _, err := tpls.SetIn(ctx, ChannelEmail, EventOrderShipped, "HI", "भेज दिया {{.order_number}}", "रास्ते में है।"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = tpls.GetIn(ctx, ChannelEmail, EventOrderShipped, "hi")
	if got.Source != WordingOwn || !got.Customized || got.Subject != "भेज दिया {{.order_number}}" {
		t.Errorf("Hindi once worded = %+v", got)
	}
	if main, _, _ := tpls.Get(ctx, ChannelEmail, EventOrderShipped); main.Subject != "Shipped {{.order_number}}" || main.Language != "en" {
		t.Errorf("the main language after Hindi was worded = %+v", main)
	}
	list, err := tpls.ListIn(ctx, ChannelEmail, "hi")
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range list {
		if l.Event == EventOrderShipped && l.Source != WordingOwn {
			t.Errorf("the Hindi list's shipped email = %+v", l)
		}
		if l.Event == EventOrderCreated && l.Source != WordingDefault {
			t.Errorf("the Hindi list's order email = %+v", l)
		}
	}

	// A message for a language the store has dropped goes in the main one;
	// wording one is refused, since it would never be sent.
	if got, ok, err := tpls.GetIn(ctx, ChannelEmail, EventOrderShipped, "de"); err != nil || !ok || got.Language != "en" {
		t.Errorf("a message in German = %+v, %v", got, err)
	}
	if _, err := tpls.SetIn(ctx, ChannelEmail, EventOrderShipped, "de", "s", "b"); err == nil {
		t.Error("wording a language the store does not sell in should be refused")
	}

	reset, err := tpls.ResetIn(ctx, ChannelEmail, EventOrderShipped, "hi")
	if err != nil || reset.Source != WordingMain || reset.Subject != "Shipped {{.order_number}}" {
		t.Errorf("Hindi reset = %+v, %v; want the main language's wording back", reset, err)
	}
}

// Wording stored before languages existed stands for the main language, and
// the main language's own wording replaces it rather than sitting beside it.
func TestWordingFromBeforeLanguagesIsTheMainLanguages(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	sellIn(t, app, ctx, "en", "hi")
	if _, err := app.db.ExecContext(ctx, `INSERT INTO notification_templates (channel, event, language, subject, body)
		VALUES ('email', 'order.created', '', 'Old {{.order_number}}', 'Old body')`); err != nil {
		t.Fatal(err)
	}
	tpls := app.NotifyTemplates()
	if got, _, _ := tpls.Get(ctx, ChannelEmail, EventOrderCreated); got.Source != WordingOwn || !got.Customized || got.Subject != "Old {{.order_number}}" {
		t.Errorf("the main language = %+v, want the old row as its own", got)
	}
	if got, _, _ := tpls.GetIn(ctx, ChannelEmail, EventOrderCreated, "hi"); got.Source != WordingMain || got.Subject != "Old {{.order_number}}" {
		t.Errorf("Hindi = %+v, want the old row as the main language's", got)
	}
	if _, err := tpls.Set(ctx, ChannelEmail, EventOrderCreated, "New {{.order_number}}", "New body"); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := app.db.QueryRowContext(ctx, `SELECT count(*) FROM notification_templates WHERE event = 'order.created'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("rows after the main language was worded = %d, want 1", rows)
	}

	// The main language moving needs no data change: Hindi, now main, has no
	// wording of its own, so English's stands for it.
	sellIn(t, app, ctx, "hi", "en")
	if got, _, _ := tpls.Get(ctx, ChannelEmail, EventOrderCreated); got.Language != "hi" || got.Source != WordingDefault {
		t.Errorf("Hindi as the main language = %+v, want the default: English is no longer main", got)
	}
	if got, _, _ := tpls.GetIn(ctx, ChannelEmail, EventOrderCreated, "en"); got.Source != WordingOwn || got.Subject != "New {{.order_number}}" {
		t.Errorf("English after the move = %+v, want its own wording", got)
	}
}

// The panel picks the language with ?language= on the same three routes.
func TestNotifyTemplateRoutesTakeALanguage(t *testing.T) {
	app := newTestApp(t)
	sellIn(t, app, context.Background(), "en", "hi")
	rec := do(t, app, http.MethodPut, "/api/admin/notifications/templates/sms/order.shipped?language=hi", withAdmin,
		jsonBody(t, map[string]string{"body": "आपका ऑर्डर {{.order_number}} भेज दिया गया"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("put in Hindi = %d: %s", rec.Code, rec.Body)
	}
	body := do(t, app, http.MethodGet, "/api/admin/notifications/templates?channel=sms&language=hi", withAdmin).Body.String()
	if !strings.Contains(body, "आपका ऑर्डर") || !strings.Contains(body, `"source":"language"`) {
		t.Errorf("the Hindi list = %s", body)
	}
	if body := do(t, app, http.MethodGet, "/api/admin/notifications/templates?channel=sms", withAdmin).Body.String(); strings.Contains(body, "आपका ऑर्डर") {
		t.Errorf("the main language's list carries the Hindi wording: %s", body)
	}
	if rec := do(t, app, http.MethodGet, "/api/admin/notifications/templates?channel=sms&language=xx", withAdmin); rec.Code != http.StatusBadRequest {
		t.Errorf("a language the store does not sell in = %d, want 400", rec.Code)
	}
	if rec := do(t, app, http.MethodDelete, "/api/admin/notifications/templates/sms/order.shipped?language=hi", withAdmin); rec.Code != http.StatusOK {
		t.Errorf("reset in Hindi = %d: %s", rec.Code, rec.Body)
	}
}

// Wording from before languages belongs to the main language it was written
// in: moving the main language labels it with that language, so wording the
// new main language does not overwrite it.
func TestMovingTheMainLanguageKeepsTheOldWording(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	sellIn(t, app, ctx, "en", "hi")
	if _, err := app.db.ExecContext(ctx, `INSERT INTO notification_templates (channel, event, language, subject, body)
		VALUES ('email', 'order.created', '', 'Old {{.order_number}}', 'Old body')`); err != nil {
		t.Fatal(err)
	}
	sellIn(t, app, ctx, "hi", "en")
	tpls := app.NotifyTemplates()
	if got, _, _ := tpls.GetIn(ctx, ChannelEmail, EventOrderCreated, "en"); got.Source != WordingOwn || got.Subject != "Old {{.order_number}}" {
		t.Errorf("English after the move = %+v, want its old wording as its own", got)
	}
	if got, _, _ := tpls.Get(ctx, ChannelEmail, EventOrderCreated); got.Source != WordingDefault {
		t.Errorf("Hindi, now main, before it is worded = %+v, want the default", got)
	}
	if _, err := tpls.Set(ctx, ChannelEmail, EventOrderCreated, "नया {{.order_number}}", "नया"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := tpls.GetIn(ctx, ChannelEmail, EventOrderCreated, "en"); got.Subject != "Old {{.order_number}}" {
		t.Errorf("English after Hindi was worded = %q, want the old English wording kept", got.Subject)
	}
}
