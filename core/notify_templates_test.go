package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// The catalogue is the engine's, the edits are the operator's, and a
// notifier reads the effective wording at send time.

func TestNotifyTemplatesCatalogueAndEdits(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	templates := app.NotifyTemplates()

	emails, err := templates.List(ctx, ChannelEmail)
	if err != nil {
		t.Fatal(err)
	}
	if len(emails) < 8 {
		t.Fatalf("email catalogue = %d messages, want the order events and the operator reset", len(emails))
	}
	if emails[0].Event != EventOrderCreated || emails[0].Customized || emails[0].Subject == "" {
		t.Fatalf("first email = %+v, want the default order.created", emails[0])
	}
	for _, tpl := range emails {
		if tpl.Title == "" || tpl.Body == "" {
			t.Errorf("%s has no title or body", tpl.Event)
		}
	}
	all, err := templates.List(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) <= len(emails) {
		t.Fatalf("the whole catalogue (%d) should include the SMS messages beyond the %d emails", len(all), len(emails))
	}

	// The default renders with an order's data.
	subject, body, ok, err := templates.Render(ctx, ChannelEmail, EventOrderShipped,
		map[string]string{"order_number": "GC-7", "customer_name": "Asha", "tracking": "TRK-1", "items_summary": "1 x Tea"})
	if err != nil || !ok {
		t.Fatalf("render: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(subject, "GC-7") || !strings.Contains(body, "TRK-1") {
		t.Errorf("rendered subject %q / body %q should carry the order and the tracking", subject, body)
	}

	// An unknown event has nothing to say, and says so without erroring.
	if _, _, ok, err := templates.Render(ctx, ChannelEmail, "nothing.happened", nil); ok || err != nil {
		t.Fatalf("unknown event: ok=%v err=%v, want neither", ok, err)
	}

	// An edit wins, is marked, and survives a list.
	edited, err := templates.Set(ctx, ChannelEmail, EventOrderShipped, "Bhej diya {{.order_number}}", "Namaste {{.customer_name}}")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if !edited.Customized || edited.UpdatedAt == nil || edited.Title != "Order shipped" {
		t.Fatalf("edited = %+v, want customized with its title kept", edited)
	}
	subject, _, _, err = templates.Render(ctx, ChannelEmail, EventOrderShipped, map[string]string{"order_number": "GC-8"})
	if err != nil || subject != "Bhej diya GC-8" {
		t.Fatalf("rendered subject = %q (%v), want the edit", subject, err)
	}
	emails, _ = templates.List(ctx, ChannelEmail)
	var shipped NotifyTemplate
	for _, tpl := range emails {
		if tpl.Event == EventOrderShipped {
			shipped = tpl
		}
	}
	if !shipped.Customized || shipped.Subject != "Bhej diya {{.order_number}}" {
		t.Fatalf("listed = %+v, want the edit", shipped)
	}
	// The other messages are untouched.
	got, _, _ := templates.Get(ctx, ChannelEmail, EventOrderCreated)
	if got.Customized {
		t.Fatal("order.created should still be the default")
	}

	// The audit knows.
	feed, _, err := app.Audit().OfEntity(ctx, AuditEntityNotificationTemplate, "email/"+EventOrderShipped, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(feed) != 1 || feed[0].Action != AuditNotificationTemplateUpdate {
		t.Fatalf("audit = %+v, want one notification_template.update", feed)
	}

	// A typo is refused, and nothing changes.
	if _, err := templates.Set(ctx, ChannelEmail, EventOrderShipped, "{{.order_number", "x"); err == nil {
		t.Fatal("an unparseable subject should be refused")
	}
	if _, err := templates.Set(ctx, ChannelEmail, EventOrderShipped, "ok", "   "); err == nil {
		t.Fatal("an empty body should be refused")
	}
	if _, err := templates.Set(ctx, ChannelEmail, EventOrderShipped, "", "body"); err == nil {
		t.Fatal("an email with no subject should be refused")
	}
	if _, err := templates.Set(ctx, ChannelSMS, EventOrderShipped, "", "Order {{.order_number}} shipped"); err != nil {
		t.Fatalf("an SMS needs no subject: %v", err)
	}
	if _, err := templates.Set(ctx, ChannelEmail, "nothing.happened", "s", "b"); err == nil {
		t.Fatal("an event nobody registered should be refused")
	}

	// Reset restores the default.
	restored, err := templates.Reset(ctx, ChannelEmail, EventOrderShipped)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if restored.Customized || !strings.Contains(restored.Subject, "on its way") {
		t.Fatalf("restored = %+v, want the default", restored)
	}
	// Resetting what was never edited is a no-op, not an error or an audit row.
	if _, err := templates.Reset(ctx, ChannelEmail, EventOrderCreated); err != nil {
		t.Fatalf("reset of a default: %v", err)
	}
	feed, _, _ = app.Audit().OfEntity(ctx, AuditEntityNotificationTemplate, "email/"+EventOrderCreated, 10, 0)
	if len(feed) != 0 {
		t.Fatalf("resetting an unedited message wrote %d audit rows, want 0", len(feed))
	}
}

// A module registers the messages it introduces; twice is a mistake.
func TestRegisterNotifyTemplate(t *testing.T) {
	app := newTestApp(t, &templateModule{})
	ctx := context.Background()
	got, ok, err := app.NotifyTemplates().Get(ctx, ChannelEmail, "thing.happened")
	if err != nil || !ok || got.Title != "A thing" {
		t.Fatalf("module template = %+v ok=%v err=%v", got, ok, err)
	}
	// The notification log names it by that title.
	if err := app.Notify(ctx, Notification{Event: "thing.happened", Channel: ChannelEmail, To: "a@b.c"}); err != nil {
		t.Fatal(err)
	}
	rows, _, err := app.Notifications().List(ctx, NotificationQuery{Event: "thing.happened"})
	if err != nil || len(rows) != 1 || rows[0].Title != "A thing" {
		t.Fatalf("log rows = %+v (%v), want one titled by the template", rows, err)
	}

	// The engine's own event cannot be re-registered by a module.
	if err := app.NotifyTemplates().register(NotifyTemplate{
		Channel: ChannelEmail, Event: EventOrderCreated, Title: "Mine", Subject: "s", Body: "b",
	}); err == nil {
		t.Fatal("re-registering order.created should be a registration error")
	}
}

type templateModule struct{ event string }

func (m *templateModule) Name() string            { return "tpl-test" }
func (m *templateModule) Migrations() []Migration { return nil }
func (m *templateModule) Register(app *App) error {
	event := m.event
	if event == "" {
		event = "thing.happened"
	}
	app.RegisterNotifyTemplate(NotifyTemplate{
		Channel: ChannelEmail, Event: event, Title: "A thing", Subject: "A thing happened", Body: "It did.",
	})
	return nil
}

// The routes: reading takes store.operate, an edit is a PUT, a reset a DELETE.
func TestNotifyTemplateRoutes(t *testing.T) {
	app := newTestApp(t)

	get := func(path string) (int, string) {
		rec := do(t, app, http.MethodGet, path, withAdmin)
		return rec.Code, rec.Body.String()
	}
	send := func(method, path string, body any) int {
		if body == nil {
			return do(t, app, method, path, withAdmin).Code
		}
		return do(t, app, method, path, withAdmin, jsonBody(t, body)).Code
	}

	if code, body := get("/api/admin/notifications/templates?channel=email"); code != 200 || !strings.Contains(body, `"event":"order.created"`) {
		t.Fatalf("list = %d %s", code, body)
	}
	if code, _ := get("/api/admin/notifications/templates?channel=fax"); code != 400 {
		t.Fatalf("a bad channel = %d, want 400", code)
	}
	edit := func(subject, body string) map[string]string {
		return map[string]string{"subject": subject, "body": body}
	}
	if code := send(http.MethodPut, "/api/admin/notifications/templates/email/order.created", edit("Hi {{.order_number}}", "Thanks")); code != 200 {
		t.Fatalf("put = %d, want 200", code)
	}
	if code, body := get("/api/admin/notifications/templates?channel=email"); code != 200 || !strings.Contains(body, `"subject":"Hi {{.order_number}}"`) || !strings.Contains(body, `"customized":true`) {
		t.Fatalf("after the edit: %d %s", code, body)
	}
	if code := send(http.MethodPut, "/api/admin/notifications/templates/email/order.created", edit("{{.x", "b")); code != 400 {
		t.Fatalf("a typo = %d, want 400", code)
	}
	if code := send(http.MethodPut, "/api/admin/notifications/templates/email/nothing.happened", edit("s", "b")); code != 404 {
		t.Fatalf("an unknown event = %d, want 404", code)
	}
	if code := send(http.MethodDelete, "/api/admin/notifications/templates/email/order.created", nil); code != 200 {
		t.Fatalf("delete = %d, want 200", code)
	}
	if _, body := get("/api/admin/notifications/templates?channel=email"); strings.Contains(body, `"customized":true`) {
		t.Fatalf("after the reset nothing should be customized: %s", body)
	}
}
