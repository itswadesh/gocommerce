package contact

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// The limits are characters, as the form counts them: a letter in Hindi or
// Odia is three bytes, and a message well inside the limit was refused as
// too long when they were counted in bytes.
func TestALongMessageInAnIndianScriptIsCountedInCharacters(t *testing.T) {
	app := gctest.New(t, New(Config{NotifyEmail: "owner@example.com"}))

	hindi := strings.Repeat("नमस्ते", 300) // 1,800 characters, 5,400 bytes
	if rec := gctest.Request(t, app, http.MethodPost, "/x/contact/messages", MessageInput{
		Name: "Asha", Email: "asha@example.com", Subject: strings.Repeat("ऑर्डर", 40), Body: hindi,
	}); rec.Code != http.StatusCreated {
		t.Fatalf("a 1,800-character message = %d: %s", rec.Code, rec.Body)
	}
	if rec := gctest.Request(t, app, http.MethodPost, "/x/contact/messages", MessageInput{
		Name: "Asha", Email: "asha@example.com", Body: strings.Repeat("न", maxBody+1),
	}); rec.Code != http.StatusBadRequest {
		t.Errorf("one character over the limit = %d, want 400", rec.Code)
	}
}

func TestAMessageArrivesIsAnnouncedAndIsHandled(t *testing.T) {
	app := gctest.New(t, New(Config{NotifyEmail: "owner@example.com"}))
	ctx := context.Background()

	sent := gctest.Request(t, app, http.MethodPost, "/x/contact/messages", MessageInput{
		Name: "Ann", Email: "Ann@Example.com", Subject: "An order", Body: "Where is my parcel?", OrderNumber: "GC-000042",
	})
	if sent.Code != http.StatusCreated {
		t.Fatalf("submit = %d: %s", sent.Code, sent.Body)
	}
	if bot := gctest.Request(t, app, http.MethodPost, "/x/contact/messages", MessageInput{Name: "Bot", Email: "b@x.io", Body: "buy now", Website: "spam"}); bot.Code != http.StatusCreated {
		t.Errorf("honeypot = %d", bot.Code)
	}
	if bad := gctest.Request(t, app, http.MethodPost, "/x/contact/messages", MessageInput{Name: "No mail", Email: "nope", Body: "hi"}); bad.Code != http.StatusBadRequest {
		t.Errorf("bad email = %d", bad.Code)
	}

	// The operator was told, through the store's own channel.
	logged, _, err := app.Notifications().List(ctx, gocommerce.NotificationQuery{Event: "contact.message"})
	if err != nil || len(logged) != 1 || logged[0].To != "owner@example.com" || logged[0].Data["from_email"] != "ann@example.com" {
		t.Errorf("announcement = %+v, %v", logged, err)
	}

	list := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages?status=new", nil)
	var inbox []Message
	gctest.DecodeData(t, list, &inbox)
	if len(inbox) != 1 || inbox[0].Email != "ann@example.com" || inbox[0].OrderNumber != "GC-000042" || inbox[0].Status != StatusNew {
		t.Fatalf("inbox = %+v, want Ann's message and not the bot's", inbox)
	}
	id := strconv.FormatInt(inbox[0].ID, 10)
	found := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages?q=GC-000042", nil)
	var byOrder []Message
	gctest.DecodeData(t, found, &byOrder)
	if len(byOrder) != 1 {
		t.Errorf("search by order number = %+v", byOrder)
	}

	replied := StatusReplied
	notes := "Sent tracking by mail"
	patched := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/x/contact/messages/"+id, MessagePatch{Status: &replied, Notes: &notes})
	var msg Message
	gctest.DecodeData(t, patched, &msg)
	if patched.Code != http.StatusOK || msg.Status != StatusReplied || msg.Notes != notes {
		t.Errorf("patch = %d %+v", patched.Code, msg)
	}
	if bad := gctest.AdminRequest(t, app, http.MethodPatch, "/api/admin/x/contact/messages/"+id, map[string]any{"status": "lost"}); bad.Code != http.StatusBadRequest {
		t.Errorf("bad status = %d", bad.Code)
	}
	if del := gctest.AdminRequest(t, app, http.MethodDelete, "/api/admin/x/contact/messages/"+id, nil); del.Code != http.StatusNoContent {
		t.Errorf("delete = %d", del.Code)
	}
	if gone := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages/"+id, nil); gone.Code != http.StatusNotFound {
		t.Errorf("after delete = %d", gone.Code)
	}

	off := false
	if _, err := app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if shut := gctest.Request(t, app, http.MethodPost, "/x/contact/messages", MessageInput{Name: "Late", Email: "l@example.com", Body: "hi"}); shut.Code != http.StatusNotFound {
		t.Errorf("submit while off = %d", shut.Code)
	}
}

func TestContactRoutesAreDocumentedAndGated(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	gctest.AssertAdminRoutesDeclareRights(t, app, "contact")
	gctest.AssertSpecCoversModuleRoutes(t, app, "contact")
}

// An agent key reads the inbox with addresses masked, and ?q= cannot read an
// address back; the order number and the name still find a message.
func TestAnAgentReadsMessagesMaskedAndCannotSearchTheAddress(t *testing.T) {
	app := gctest.New(t, New(Config{}))
	if rec := gctest.Request(t, app, http.MethodPost, "/x/contact/messages", MessageInput{
		Name: "Ann", Email: "ann.smith@example.com", Subject: "Parcel", Body: "Where is it?", OrderNumber: "GC-000042",
	}); rec.Code != http.StatusCreated {
		t.Fatalf("submit = %d: %s", rec.Code, rec.Body)
	}
	agent := gctest.AgentKey(t, app, rightContactRead)

	found := func(q string) int {
		t.Helper()
		rec := gctest.SessionRequest(t, app, agent, http.MethodGet, "/api/admin/x/contact/messages?q="+q, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("agent search %q = %d: %s", q, rec.Code, rec.Body)
		}
		if strings.Contains(rec.Body.String(), "ann.smith@") {
			t.Errorf("the agent read the address in full: %s", rec.Body)
		}
		var out []Message
		gctest.DecodeData(t, rec, &out)
		return len(out)
	}
	if n := found("ann.smith"); n != 0 {
		t.Errorf("an agent searching by address found %d, want 0", n)
	}
	if n := found("GC-000042"); n != 1 {
		t.Errorf("an agent searching by order number found %d, want 1", n)
	}
	if n := found("ann"); n != 1 {
		t.Errorf("an agent searching by name found %d, want 1", n)
	}
}
