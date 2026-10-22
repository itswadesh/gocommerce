package contact

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// mailModule installs a recording email provider, so a reply has somewhere to
// go and the test can read what went.
type mailModule struct{ rec *gctest.RecordingNotifier }

func (mailModule) Name() string                       { return "testmail" }
func (mailModule) Migrations() []gocommerce.Migration { return nil }
func (m mailModule) Register(app *gocommerce.App) error {
	app.RegisterNotifier(gocommerce.ChannelEmail, m.rec)
	return nil
}

// refusingMail is a provider that answered and said no.
type refusingMail struct{}

func (refusingMail) Notify(context.Context, gocommerce.Notification) error {
	return fmt.Errorf("testmail: %w: the sending domain is not verified", gocommerce.ErrNotificationRefused)
}

type refusingMailModule struct{}

func (refusingMailModule) Name() string                       { return "testmail" }
func (refusingMailModule) Migrations() []gocommerce.Migration { return nil }
func (refusingMailModule) Register(app *gocommerce.App) error {
	app.RegisterNotifier(gocommerce.ChannelEmail, refusingMail{})
	return nil
}

// write posts one message through the storefront's form and returns its id.
func write(t *testing.T, app *gocommerce.App, in MessageInput) string {
	t.Helper()
	if rec := gctest.Request(t, app, http.MethodPost, "/x/contact/messages", in); rec.Code != http.StatusCreated {
		t.Fatalf("submit = %d: %s", rec.Code, rec.Body)
	}
	var inbox []Message
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages?limit=1", nil), &inbox)
	if len(inbox) == 0 {
		t.Fatal("the message is not in the inbox")
	}
	return strconv.FormatInt(inbox[0].ID, 10)
}

// An operator answers from the panel: the shopper gets an email from the
// store's own provider, the reply is kept on the message with who sent it,
// and the message is marked replied. A second reply joins the thread.
func TestAnOperatorRepliesAndTheThreadIsKept(t *testing.T) {
	mail := &gctest.RecordingNotifier{}
	app := gctest.New(t, mailModule{rec: mail}, New(Config{MessagesPerMinute: -1}))
	ctx := context.Background()
	if _, ok, err := app.NotifyTemplates().Get(ctx, gocommerce.ChannelEmail, "contact.reply"); err != nil || !ok {
		t.Fatalf("no wording for a reply: %v %v", ok, err)
	}
	id := write(t, app, MessageInput{Name: "Ann", Email: "ann@example.com", Subject: "An order", Body: "Where is my parcel?"})
	manager := gctest.OperatorToken(t, app, "mia@example.com", gocommerce.RoleManager)

	rec := gctest.SessionRequest(t, app, manager, http.MethodPost, "/api/admin/x/contact/messages/"+id+"/reply",
		ReplyInput{Body: "It left the warehouse this morning."})
	if rec.Code != http.StatusCreated {
		t.Fatalf("reply = %d: %s", rec.Code, rec.Body)
	}
	var msg Message
	gctest.DecodeData(t, rec, &msg)
	if msg.Status != StatusReplied || len(msg.Replies) != 1 {
		t.Fatalf("after the reply = %+v", msg)
	}
	first := msg.Replies[0]
	if first.Subject != "Re: An order" || first.Body != "It left the warehouse this morning." || first.SentBy != "mia@example.com" {
		t.Errorf("reply = %+v, want the default subject, the text and the operator", first)
	}

	sent := mail.All()
	if len(sent) != 1 || sent[0].Event != "contact.reply" || sent[0].To != "ann@example.com" {
		t.Fatalf("sent = %+v, want one contact.reply to Ann", sent)
	}
	if d := sent[0].Data; d["reply"] != first.Body || d["subject"] != "Re: An order" || d["name"] != "Ann" ||
		d["original"] != "Where is my parcel?" {
		t.Errorf("the email's data = %+v", d)
	}

	if rec := gctest.SessionRequest(t, app, manager, http.MethodPost, "/api/admin/x/contact/messages/"+id+"/reply",
		ReplyInput{Subject: "Your tracking number", Body: "It is 1Z999."}); rec.Code != http.StatusCreated {
		t.Fatalf("second reply = %d: %s", rec.Code, rec.Body)
	}
	var thread Message
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages/"+id, nil), &thread)
	if len(thread.Replies) != 2 || thread.Replies[0].Body != first.Body || thread.Replies[1].Subject != "Your tracking number" {
		t.Errorf("thread = %+v, want both replies, oldest first", thread.Replies)
	}
	// The list stays light: the thread is the drawer's, read with the one message.
	var inbox []Message
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages?status=replied", nil), &inbox)
	if len(inbox) != 1 || inbox[0].Replies != nil {
		t.Errorf("list = %+v, want the message without its thread", inbox)
	}

	for name, body := range map[string]ReplyInput{
		"empty":    {Body: "   "},
		"too long": {Body: strings.Repeat("x", maxBody+1)},
		"subject":  {Subject: strings.Repeat("s", maxSubject+1), Body: "hi"},
	} {
		if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/contact/messages/"+id+"/reply", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s reply = %d, want 400", name, rec.Code)
		}
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/contact/messages/999999/reply", ReplyInput{Body: "hi"}); rec.Code != http.StatusNotFound {
		t.Errorf("reply to nothing = %d, want 404", rec.Code)
	}
	if len(mail.All()) != 2 {
		t.Errorf("sent = %d, want only the two good replies", len(mail.All()))
	}

	// Staff read the inbox and may not write from the store's address.
	staff := gctest.OperatorToken(t, app, "sam@example.com", gocommerce.RoleStaff)
	if rec := gctest.SessionRequest(t, app, staff, http.MethodGet, "/api/admin/x/contact/messages/"+id, nil); rec.Code != http.StatusOK {
		t.Errorf("staff read = %d", rec.Code)
	}
	if rec := gctest.SessionRequest(t, app, staff, http.MethodPost, "/api/admin/x/contact/messages/"+id+"/reply", ReplyInput{Body: "hi"}); rec.Code != http.StatusForbidden {
		t.Errorf("staff reply = %d, want 403", rec.Code)
	}
}

// A reply that did not reach the shopper is not on the record as one: with no
// provider, or one that refuses, the operator is told why, the message stays
// as it was, and the text is theirs to send again. The refusal is in
// Notifications like any other failed send.
func TestAReplyThatDoesNotGoIsNotRecorded(t *testing.T) {
	ctx := context.Background()
	for name, mods := range map[string][]gocommerce.Module{
		"no provider": {New(Config{MessagesPerMinute: -1})},
		"refused":     {refusingMailModule{}, New(Config{MessagesPerMinute: -1})},
	} {
		t.Run(name, func(t *testing.T) {
			app := gctest.New(t, mods...)
			id := write(t, app, MessageInput{Name: "Ann", Email: "ann@example.com", Body: "Hello?"})
			rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/contact/messages/"+id+"/reply", ReplyInput{Body: "Hi Ann"})
			want, words := http.StatusConflict, "No email provider"
			if name == "refused" {
				want, words = http.StatusUnprocessableEntity, "the sending domain is not verified"
			}
			if rec.Code != want || !strings.Contains(rec.Body.String(), words) {
				t.Fatalf("reply = %d %s, want %d saying %q", rec.Code, rec.Body, want, words)
			}
			var msg Message
			gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/contact/messages/"+id, nil), &msg)
			if msg.Status != StatusNew || len(msg.Replies) != 0 {
				t.Errorf("after a failed reply = %+v, want it new with no thread", msg)
			}
			if name == "refused" {
				logged, _, err := app.Notifications().List(ctx, gocommerce.NotificationQuery{Event: "contact.reply"})
				if err != nil || len(logged) != 1 || logged[0].Status != gocommerce.NotificationFailed {
					t.Errorf("Notifications = %+v, %v; want the failed send", logged, err)
				}
			}
		})
	}
}

// Anybody can sign in to a public demo, and anybody can type an address into
// the storefront's form: replying there would mail a stranger whatever the
// visitor wrote, from the store's own domain. A demo keeps the button's answer
// and sends nothing.
func TestADemoStoreSendsNoReplies(t *testing.T) {
	mail := &gctest.RecordingNotifier{}
	app := gctest.New(t, mailModule{rec: mail}, New(Config{MessagesPerMinute: -1}))
	id := write(t, app, MessageInput{Name: "Ann", Email: "victim@example.com", Body: "Hello?"})
	if err := app.Stores().SetDemo(context.Background(), app.DefaultStore(), true); err != nil {
		t.Fatal(err)
	}
	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/contact/messages/"+id+"/reply", ReplyInput{Body: "Buy now"})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "demo") {
		t.Errorf("reply on a demo = %d %s, want 403 saying why", rec.Code, rec.Body)
	}
	if n := len(mail.All()); n != 0 {
		t.Errorf("a demo sent %d emails", n)
	}
}

// What an agent reads of a thread is masked like the rest of the message: the
// shopper's address, and the address of whoever replied.
func TestAnAgentReadsAThreadMasked(t *testing.T) {
	mail := &gctest.RecordingNotifier{}
	app := gctest.New(t, mailModule{rec: mail}, New(Config{MessagesPerMinute: -1}))
	id := write(t, app, MessageInput{Name: "Ann", Email: "ann.smith@example.com", Body: "Hello?"})
	manager := gctest.OperatorToken(t, app, "mia.jones@example.com", gocommerce.RoleManager)
	if rec := gctest.SessionRequest(t, app, manager, http.MethodPost, "/api/admin/x/contact/messages/"+id+"/reply", ReplyInput{Body: "Hi Ann"}); rec.Code != http.StatusCreated {
		t.Fatalf("reply = %d: %s", rec.Code, rec.Body)
	}
	agent := gctest.AgentKey(t, app, rightContactRead)
	rec := gctest.SessionRequest(t, app, agent, http.MethodGet, "/api/admin/x/contact/messages/"+id, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("agent read = %d: %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); strings.Contains(body, "ann.smith@") || strings.Contains(body, "mia.jones@") {
		t.Errorf("the agent read an address in full: %s", body)
	}
	var msg Message
	gctest.DecodeData(t, rec, &msg)
	if len(msg.Replies) != 1 || msg.Replies[0].Body != "Hi Ann" {
		t.Errorf("agent's thread = %+v", msg.Replies)
	}
}
