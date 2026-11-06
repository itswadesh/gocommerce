package gocommerce

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ownerEmailApp has an install owner, a store owned by somebody else, recorded
// mail and a stand-in for the model.
func ownerEmailApp(t *testing.T) (app *App, rec *recordingNotifier, tok string, target ownerEmailTarget, asked *string) {
	t.Helper()
	dsn := requireDB(t)
	rec = &recordingNotifier{}
	cfg := testConfig(dsn)
	cfg.PanelURL = "https://panel.example"
	cfg.PlatformOwnerPhone = directoryPhone
	cfg.PlatformEmailFrom = "hi@platform.example"
	cfg.PlatformAnthropicKey = "test-key"
	app, err := New(cfg, &notifyModule{rec: rec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	asked = new(string)
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*asked = string(raw)
		words, _ := json.Marshal(map[string]string{"subject": "Please confirm your email", "body": "Hello.\n\n{{verify_link}}\n\nThe team"})
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": string(words)}}})
	}))
	t.Cleanup(model.Close)
	app.ownerDraftHost = model.URL

	tok = directoryOwner(t, app, "me@example.com", true)
	store := addTestStore(t, app, "someone-elses")
	signInAs(t, app, "seller@example.com", RoleStaff)
	joinTestStore(t, app, store, "seller@example.com", RoleOwner)
	var id int64
	if err := app.db.QueryRow(`SELECT id FROM superusers WHERE email = 'seller@example.com'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return app, rec, tok, ownerEmailTarget{SuperuserID: id, StoreID: store}, asked
}

func TestTheInstallOwnerSendsAnEmailThatVerifiesTheAddress(t *testing.T) {
	app, rec, tok, target, asked := ownerEmailApp(t)

	rec1 := do(t, app, http.MethodPost, "/api/stores/directory/email-draft", jsonBody(t, target), bearer(tok))
	if rec1.Code != http.StatusOK {
		t.Fatalf("draft = %d: %s", rec1.Code, rec1.Body)
	}
	var draft struct{ From, To, Subject, Body, Problem string }
	decodeData(t, rec1, &draft)
	if draft.From != "hi@platform.example" || draft.To != "seller@example.com" || draft.Subject == "" || draft.Problem != "" {
		t.Fatalf("draft = %+v", draft)
	}
	if !strings.Contains(draft.Body, ownerVerifyPlaceholder) || strings.Contains(draft.Body, "http") {
		t.Fatalf("draft body = %q, want the mark and no live link", draft.Body)
	}
	if strings.Contains(*asked, "seller@example.com") || !strings.Contains(*asked, "someone-elses") {
		t.Fatalf("the model was asked %s, want the store and not the person", *asked)
	}

	send := map[string]any{"superuser_id": target.SuperuserID, "store_id": target.StoreID, "subject": "Edited subject", "body": draft.Body}
	if got := do(t, app, http.MethodPost, "/api/stores/directory/email-send", jsonBody(t, send), bearer(tok)); got.Code != http.StatusOK {
		t.Fatalf("send = %d: %s", got.Code, got.Body)
	}
	var note *Notification
	rec.mu.Lock()
	for i := range rec.sent {
		if rec.sent[i].Event == EventOwnerEmailVerification {
			note = &rec.sent[i]
		}
	}
	rec.mu.Unlock()
	if note == nil {
		t.Fatal("nothing was sent")
	}
	if note.From != "hi@platform.example" || note.To != "seller@example.com" || note.Subject != "Edited subject" {
		t.Fatalf("sent %+v", note)
	}
	const prefix = "https://panel.example/admin/auth/verify-email/"
	at := strings.Index(note.Body, prefix)
	if at < 0 || strings.Contains(note.Body, ownerVerifyPlaceholder) {
		t.Fatalf("body = %q, want the link in place of the mark", note.Body)
	}
	token, _, _ := strings.Cut(note.Body[at+len(prefix):], "\n")

	proved := func() bool {
		t.Helper()
		var yes bool
		if err := app.db.QueryRow(`SELECT coalesce(email = email_proved, false) FROM superusers WHERE id = $1`, target.SuperuserID).Scan(&yes); err != nil {
			t.Fatal(err)
		}
		return yes
	}
	if proved() {
		t.Fatal("sending the email proved the address before anybody clicked")
	}
	body := jsonBody(t, map[string]string{"token": token})
	if got := do(t, app, http.MethodPost, "/api/stores/verify-email", body); got.Code != http.StatusOK {
		t.Fatalf("verify = %d: %s", got.Code, got.Body)
	}
	if !proved() {
		t.Fatal("the link did not prove the address")
	}
	if got := do(t, app, http.MethodPost, "/api/stores/verify-email", jsonBody(t, map[string]string{"token": token})); got.Code != http.StatusBadRequest {
		t.Fatalf("second use = %d, want 400", got.Code)
	}
	if got := do(t, app, http.MethodPost, "/api/stores/directory/email-draft", jsonBody(t, target), bearer(tok)); got.Code != http.StatusConflict {
		t.Fatalf("draft for a verified address = %d, want 409", got.Code)
	}
}

func TestALinkDoesNotProveAnAddressTheAccountHasLeft(t *testing.T) {
	app, _, _, target, _ := ownerEmailApp(t)
	token, err := app.newOwnerEmailToken(t.Context(), target.SuperuserID, "seller@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`UPDATE superusers SET email = 'moved@example.com' WHERE id = $1`, target.SuperuserID); err != nil {
		t.Fatal(err)
	}
	if got := do(t, app, http.MethodPost, "/api/stores/verify-email", jsonBody(t, map[string]string{"token": token})); got.Code != http.StatusBadRequest {
		t.Fatalf("verify = %d, want 400", got.Code)
	}
}

func TestOnlyTheInstallOwnerCanWriteToAStoresOwner(t *testing.T) {
	app, rec, _, target, _ := ownerEmailApp(t)
	other := signInAs(t, app, "another@example.com", RoleOwner)
	send := map[string]any{"superuser_id": target.SuperuserID, "store_id": target.StoreID, "subject": "s", "body": "b"}
	for path, body := range map[string]any{"email-draft": target, "email-send": send} {
		for who, tok := range map[string]string{"a store's owner": other, "the static token": "test-admin-token"} {
			if got := do(t, app, http.MethodPost, "/api/stores/directory/"+path, jsonBody(t, body), bearer(tok)); got.Code != http.StatusForbidden {
				t.Errorf("%s on %s = %d, want 403", who, path, got.Code)
			}
		}
		if got := do(t, app, http.MethodPost, "/api/stores/directory/"+path, jsonBody(t, body)); got.Code != http.StatusUnauthorized {
			t.Errorf("no credential on %s = %d, want 401", path, got.Code)
		}
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	for _, n := range rec.sent {
		if n.Event == EventOwnerEmailVerification {
			t.Fatalf("sent %+v", n)
		}
	}
}

// The routes take ids, so they must not reach a person the store does not belong to.
func TestAStoresOwnerIsTheOnlyPersonThatStoreCanBeWrittenAbout(t *testing.T) {
	app, _, tok, target, _ := ownerEmailApp(t)
	var me int64
	if err := app.db.QueryRow(`SELECT id FROM superusers WHERE email = 'me@example.com'`).Scan(&me); err != nil {
		t.Fatal(err)
	}
	wrong := ownerEmailTarget{SuperuserID: me, StoreID: target.StoreID}
	if got := do(t, app, http.MethodPost, "/api/stores/directory/email-draft", jsonBody(t, wrong), bearer(tok)); got.Code != http.StatusNotFound {
		t.Fatalf("draft = %d, want 404", got.Code)
	}
}

// With no key the screen still opens, with the reason, and the owner can write it.
func TestTheDraftSaysWhyWhenTheModelCannotWriteIt(t *testing.T) {
	app, _, tok, target, _ := ownerEmailApp(t)
	app.cfg.PlatformAnthropicKey = ""
	got := do(t, app, http.MethodPost, "/api/stores/directory/email-draft", jsonBody(t, target), bearer(tok))
	if got.Code != http.StatusOK {
		t.Fatalf("draft = %d: %s", got.Code, got.Body)
	}
	var draft struct{ From, To, Subject, Problem string }
	decodeData(t, got, &draft)
	if draft.Problem == "" || draft.Subject != "" || draft.To != "seller@example.com" {
		t.Fatalf("draft = %+v", draft)
	}
}

func TestAnEditThatDropsTheMarkStillCarriesTheLink(t *testing.T) {
	if got := ownerEmailWithLink("Hello", "https://x/y"); got != "Hello\n\nhttps://x/y" {
		t.Fatalf("got %q", got)
	}
	if got := ownerEmailWithLink("A {{verify_link}} B", "https://x/y"); got != "A https://x/y B" {
		t.Fatalf("got %q", got)
	}
}
