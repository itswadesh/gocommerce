package telegramalerts

import (
	"net/http"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

type alertPatch struct {
	Template *string `json:"template,omitempty"`
	Active   *bool   `json:"active,omitempty"`
}

func patchAlert(t *testing.T, app *gocommerce.App, event string, p alertPatch) Alert {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/x/telegram-alerts/alerts/"+event, p)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch %s = %d %s", event, rec.Code, rec.Body)
	}
	var out Alert
	gctest.DecodeData(t, rec, &out)
	return out
}

func listAlerts(t *testing.T, app *gocommerce.App) []Alert {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/telegram-alerts/alerts", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list alerts = %d %s", rec.Code, rec.Body)
	}
	var out []Alert
	gctest.DecodeData(t, rec, &out)
	return out
}

// Every alert the module can send is on the list from the first day, on, with
// its default wording — the owner switches off what they do not want rather
// than having to build what they do.
func TestTheAlertsStartOnWithTheirDefaultWording(t *testing.T) {
	app, _, _ := newApp(t, gocommerce.Config{})
	got := listAlerts(t, app)
	want := []string{gocommerce.EventOrderCreated, gocommerce.EventOrderPaid,
		gocommerce.EventOrderCancelled, gocommerce.EventOrderReturned}
	if len(got) != len(want) {
		t.Fatalf("alerts = %d, want %d", len(got), len(want))
	}
	for i, a := range got {
		if a.Event != want[i] {
			t.Errorf("alert %d is %s, want %s", i, a.Event, want[i])
		}
		if !a.Active || a.Customized || a.Template == "" || a.Template != a.DefaultTemplate || a.Title == "" {
			t.Errorf("alert %s = %+v, want on, with its default wording", a.Event, a)
		}
		if len(a.Variables) == 0 {
			t.Errorf("alert %s names no variables for the editor", a.Event)
		}
	}
}

// The alerts are the module's own rows: created, read, changed and removed,
// and removing one puts the default back.
func TestAnAlertIsCreatedChangedAndReset(t *testing.T) {
	app, _, _ := newApp(t, gocommerce.Config{})
	base := "/api/admin/x/telegram-alerts/alerts"

	rec := gctest.AdminRequest(t, app, http.MethodPost, base, map[string]any{
		"event": gocommerce.EventOrderPaid, "template": "Paid: {{.order_number}}", "active": false,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var made Alert
	gctest.DecodeData(t, rec, &made)
	if made.Template != "Paid: {{.order_number}}" || made.Active || !made.Customized {
		t.Errorf("created = %+v", made)
	}

	if rec := gctest.AdminRequest(t, app, http.MethodPost, base, map[string]any{"event": gocommerce.EventOrderPaid}); rec.Code != http.StatusConflict {
		t.Errorf("a second order.paid alert = %d, want 409", rec.Code)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, base, map[string]any{"event": "order.shipped"}); rec.Code != http.StatusBadRequest {
		t.Errorf("an alert for an event it does not send = %d, want 400", rec.Code)
	}

	rec = gctest.AdminRequest(t, app, http.MethodGet, base+"/"+gocommerce.EventOrderPaid, nil)
	var one Alert
	gctest.DecodeData(t, rec, &one)
	if one.Template != made.Template || one.Active {
		t.Errorf("read back = %+v, want what was created", one)
	}

	on := true
	if a := patchAlert(t, app, gocommerce.EventOrderPaid, alertPatch{Active: &on}); !a.Active || a.Template != made.Template {
		t.Errorf("switched on = %+v, want the wording kept", a)
	}

	// A typo is refused as it is typed, not on the next sale.
	broken := "Paid: {{.order_number"
	rec = gctest.AdminRequest(t, app, http.MethodPatch, base+"/"+gocommerce.EventOrderPaid, alertPatch{Template: &broken})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a broken template = %d, want 400", rec.Code)
	}
	empty := "   "
	rec = gctest.AdminRequest(t, app, http.MethodPatch, base+"/"+gocommerce.EventOrderPaid, alertPatch{Template: &empty})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("an empty message = %d, want 400", rec.Code)
	}

	rec = gctest.AdminRequest(t, app, http.MethodDelete, base+"/"+gocommerce.EventOrderPaid, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("reset = %d %s", rec.Code, rec.Body)
	}
	var reset Alert
	gctest.DecodeData(t, rec, &reset)
	if reset.Customized || !reset.Active || reset.Template != reset.DefaultTemplate {
		t.Errorf("after a reset = %+v, want the default back", reset)
	}

	if rec := gctest.AdminRequest(t, app, http.MethodGet, base+"/order.shipped", nil); rec.Code != http.StatusNotFound {
		t.Errorf("an event with no alert = %d, want 404", rec.Code)
	}
}

// The preview fills the wording with a made-up order, so the owner sees the
// message before a real one arrives.
func TestThePreviewFillsTheWordingWithASampleOrder(t *testing.T) {
	app, _, fake := newApp(t, gocommerce.Config{PanelURL: "https://admin.shop.example"})
	path := "/api/admin/x/telegram-alerts/preview"

	rec := gctest.AdminRequest(t, app, http.MethodPost, path, map[string]any{"event": gocommerce.EventOrderCreated})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Event string `json:"event"`
		Text  string `json:"text"`
	}
	gctest.DecodeData(t, rec, &out)
	currency := app.Currency(t.Context())
	for _, want := range []string{"GC-001042", formatMoney(129900, currency), "Asha", "https://admin.shop.example/dash/orders/"} {
		if !strings.Contains(out.Text, want) {
			t.Errorf("preview %q does not carry %q", out.Text, want)
		}
	}

	wording := "{{.customer_first_name}} returned {{.return_units}} worth {{.return_value}}: {{.reason}}"
	rec = gctest.AdminRequest(t, app, http.MethodPost, path, map[string]any{
		"event": gocommerce.EventOrderReturned, "template": wording,
	})
	gctest.DecodeData(t, rec, &out)
	if !strings.HasPrefix(out.Text, "Asha returned 1 worth ") || strings.Contains(out.Text, "<no value>") {
		t.Errorf("preview of the owner's wording = %q", out.Text)
	}

	// Wording half-typed is the preview's ordinary input — every {{ passes
	// through a broken state — so it answers with what is wrong rather than
	// failing the request: a 400 per keystroke is a red line per keystroke in
	// the browser's console.
	rec = gctest.AdminRequest(t, app, http.MethodPost, path, map[string]any{
		"event": gocommerce.EventOrderCreated, "template": "{{.order_number",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("preview of a broken template = %d %s, want 200 naming the problem", rec.Code, rec.Body)
	}
	var broken struct {
		Text  string `json:"text"`
		Error string `json:"error"`
	}
	gctest.DecodeData(t, rec, &broken)
	if broken.Text != "" || !strings.Contains(broken.Error, "does not work") {
		t.Errorf("preview of a broken template = %+v, want no text and the reason", broken)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, path, map[string]any{"event": "order.shipped"}); rec.Code != http.StatusBadRequest {
		t.Errorf("preview of an event with no alert = %d, want 400", rec.Code)
	}
	if n := len(fake.messages()); n != 0 {
		t.Errorf("a preview sent %d messages, want none", n)
	}
}

// Find my chat: after the owner messages the bot, the chats it has heard from
// are offered to pick from, rather than asking anybody to find a chat id.
func TestFindMyChatListsTheChatsTheBotHasHeardFrom(t *testing.T) {
	app, _, fake := newApp(t, gocommerce.Config{})
	path := "/api/admin/x/telegram-alerts/chats"

	if rec := gctest.AdminRequest(t, app, http.MethodGet, path, nil); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "token") {
		t.Fatalf("chats with no token = %d %s, want 400 asking for the token", rec.Code, rec.Body)
	}

	setUp(t, app)
	fake.updates = `[
	  {"update_id":1,"message":{"message_id":1,"chat":{"id":424242,"type":"private","first_name":"Asha","last_name":"Rao","username":"asharao"},"text":"/start"}},
	  {"update_id":2,"message":{"message_id":2,"chat":{"id":424242,"type":"private","first_name":"Asha","last_name":"Rao","username":"asharao"},"text":"hello"}},
	  {"update_id":3,"my_chat_member":{"chat":{"id":-1001234567890,"type":"supergroup","title":"Shop team"}}}
	]`
	rec := gctest.AdminRequest(t, app, http.MethodGet, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("chats = %d %s", rec.Code, rec.Body)
	}
	var chats []Chat
	gctest.DecodeData(t, rec, &chats)
	if len(chats) != 2 {
		t.Fatalf("chats = %+v, want the private chat once and the group", chats)
	}
	// Newest first: the one the owner just added the bot to is the one they
	// are looking for.
	if chats[0].ID != "-1001234567890" || chats[0].Title != "Shop team" || chats[0].Type != "supergroup" {
		t.Errorf("first chat = %+v, want the group", chats[0])
	}
	if chats[1].ID != "424242" || chats[1].Title != "Asha Rao" || chats[1].Username != "asharao" {
		t.Errorf("second chat = %+v, want the owner's own", chats[1])
	}

	// A token Telegram does not know is said in words the owner can act on.
	fake.answer(http.StatusUnauthorized, `{"ok":false,"error_code":401,"description":"Unauthorized"}`)
	rec = gctest.AdminRequest(t, app, http.MethodGet, path, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "BotFather") {
		t.Errorf("chats with a token Telegram refuses = %d %s, want 422 pointing at @BotFather", rec.Code, rec.Body)
	}
}

// The test message is the one way to know the bot and the chat work before an
// order depends on it — and it works before alerts are switched on.
func TestTheTestMessageGoesToTheChosenChat(t *testing.T) {
	app, _, fake := newApp(t, gocommerce.Config{})
	path := "/api/admin/x/telegram-alerts/test"

	if rec := gctest.AdminRequest(t, app, http.MethodPost, path, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("test with nothing set up = %d %s, want 400", rec.Code, rec.Body)
	}

	off := false
	if _, err := app.Plugins().Update(t.Context(), PluginKey, gocommerce.PluginPatch{
		Enabled: &off, Settings: map[string]any{"bot_token": testToken, "chat_id": "424242"},
	}); err != nil {
		t.Fatal(err)
	}
	rec := gctest.AdminRequest(t, app, http.MethodPost, path, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("test = %d %s", rec.Code, rec.Body)
	}
	sent := fake.messages()
	if len(sent) != 1 || sent[0].ChatID != "424242" || !strings.Contains(sent[0].Text, "test") {
		t.Fatalf("sent = %+v, want one test message to chat 424242", sent)
	}

	fake.answer(http.StatusForbidden, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
	rec = gctest.AdminRequest(t, app, http.MethodPost, path, nil)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "blocked") {
		t.Errorf("test to a chat that blocked the bot = %d %s, want 422 with Telegram's reason", rec.Code, rec.Body)
	}

	if _, err := app.Plugins().Update(t.Context(), PluginKey, gocommerce.PluginPatch{
		Settings: map[string]any{"bot_token": "not a token"},
	}); err != nil {
		t.Fatal(err)
	}
	rec = gctest.AdminRequest(t, app, http.MethodPost, path, nil)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "BotFather") {
		t.Errorf("test with a malformed token = %d %s, want 400 pointing at @BotFather", rec.Code, rec.Body)
	}
}

// Owner holds both rights, a manager may read the alerts and not change them,
// and staff see neither (D65).
func TestTheRightsFollowTheRole(t *testing.T) {
	app, _, _ := newApp(t, gocommerce.Config{})
	manager := gctest.OperatorToken(t, app, "manager@example.com", gocommerce.RoleManager)
	staff := gctest.OperatorToken(t, app, "staff@example.com", gocommerce.RoleStaff)
	owner := gctest.OperatorToken(t, app, "owner@example.com", gocommerce.RoleOwner)
	list := "/api/admin/x/telegram-alerts/alerts"
	one := list + "/" + gocommerce.EventOrderCreated
	off := map[string]any{"active": false}

	for _, c := range []struct {
		who, token, method, path string
		body                     any
		want                     int
	}{
		{"manager", manager, http.MethodGet, list, nil, http.StatusOK},
		{"manager", manager, http.MethodPatch, one, off, http.StatusForbidden},
		{"manager", manager, http.MethodGet, "/api/admin/x/telegram-alerts/chats", nil, http.StatusForbidden},
		{"staff", staff, http.MethodGet, list, nil, http.StatusForbidden},
		{"owner", owner, http.MethodPatch, one, off, http.StatusOK},
	} {
		if rec := gctest.SessionRequest(t, app, c.token, c.method, c.path, c.body); rec.Code != c.want {
			t.Errorf("%s %s %s = %d, want %d", c.who, c.method, c.path, rec.Code, c.want)
		}
	}
}

func TestRoutesDeclareRightsAndAreDocumented(t *testing.T) {
	app, _, _ := newApp(t, gocommerce.Config{})
	gctest.AssertAdminRoutesDeclareRights(t, app, "telegram-alerts")
	gctest.AssertSpecCoversModuleRoutes(t, app, "telegram-alerts")
}
