package gocommerce

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// ownerEmailApp has an install owner, a store owned by somebody else, and recorded
// mail.
func ownerEmailApp(t *testing.T) (app *App, rec *recordingNotifier, tok string, target ownerEmailTarget) {
	t.Helper()
	dsn := requireDB(t)
	rec = &recordingNotifier{}
	cfg := testConfig(dsn)
	cfg.PanelURL = "https://panel.example"
	cfg.PlatformOwnerPhone = directoryPhone
	cfg.PlatformEmailFrom = "hi@platform.example"
	app, err := New(cfg, &notifyModule{rec: rec})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	tok = directoryOwner(t, app, "me@example.com", true)
	store := addTestStore(t, app, "someone-elses")
	signInAs(t, app, "seller@example.com", RoleStaff)
	joinTestStore(t, app, store, "seller@example.com", RoleOwner)
	var id int64
	if err := app.db.QueryRow(`SELECT id FROM superusers WHERE email = 'seller@example.com'`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return app, rec, tok, ownerEmailTarget{SuperuserID: id, StoreID: store}
}

func TestTheInstallOwnerSendsAnEmailThatVerifiesTheAddress(t *testing.T) {
	app, rec, tok, target := ownerEmailApp(t)

	rec1 := do(t, app, http.MethodPost, "/api/stores/directory/email-draft", jsonBody(t, target), bearer(tok))
	if rec1.Code != http.StatusOK {
		t.Fatalf("draft = %d: %s", rec1.Code, rec1.Body)
	}
	var draft struct{ From, To, Subject, Body string }
	decodeData(t, rec1, &draft)
	if draft.From != "hi@platform.example" || draft.To != "seller@example.com" || draft.Subject == "" {
		t.Fatalf("draft = %+v", draft)
	}
	if !strings.Contains(draft.Body, ownerVerifyPlaceholder) || strings.Contains(draft.Body, "http") {
		t.Fatalf("draft body = %q, want the mark and no live link", draft.Body)
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

	// The list shows what went out, until the address is verified, without the link.
	shown := func() *OwnerEmailSent {
		t.Helper()
		owners, err := app.storeOwners(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return owners[target.StoreID][0].EmailSent
	}
	sent := shown()
	if sent == nil || sent.Subject != "Edited subject" || sent.Email != "seller@example.com" || sent.SentAt.IsZero() {
		t.Fatalf("the list shows %+v", sent)
	}
	if !strings.Contains(sent.Body, ownerVerifyLinkShown) || strings.Contains(sent.Body, token) || strings.Contains(sent.Body, "http") {
		t.Fatalf("the kept copy = %q, want it without the link", sent.Body)
	}

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
	if got := shown(); got != nil {
		t.Fatalf("the list still shows the email after verifying: %+v", got)
	}
	if got := do(t, app, http.MethodPost, "/api/stores/verify-email", jsonBody(t, map[string]string{"token": token})); got.Code != http.StatusBadRequest {
		t.Fatalf("second use = %d, want 400", got.Code)
	}
	if got := do(t, app, http.MethodPost, "/api/stores/directory/email-draft", jsonBody(t, target), bearer(tok)); got.Code != http.StatusConflict {
		t.Fatalf("draft for a verified address = %d, want 409", got.Code)
	}
}

func TestALinkDoesNotProveAnAddressTheAccountHasLeft(t *testing.T) {
	app, _, _, target := ownerEmailApp(t)
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
	app, rec, _, target := ownerEmailApp(t)
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
	app, _, tok, target := ownerEmailApp(t)
	var me int64
	if err := app.db.QueryRow(`SELECT id FROM superusers WHERE email = 'me@example.com'`).Scan(&me); err != nil {
		t.Fatal(err)
	}
	wrong := ownerEmailTarget{SuperuserID: me, StoreID: target.StoreID}
	if got := do(t, app, http.MethodPost, "/api/stores/directory/email-draft", jsonBody(t, wrong), bearer(tok)); got.Code != http.StatusNotFound {
		t.Fatalf("draft = %d, want 404", got.Code)
	}
}

// An owner who moves to another address has not been asked about that one.
func TestTheSentEmailIsNotShownForAnAddressTheAccountHasLeft(t *testing.T) {
	app, _, tok, target := ownerEmailApp(t)
	send := map[string]any{"superuser_id": target.SuperuserID, "store_id": target.StoreID, "subject": "Confirm", "body": "Hello {{verify_link}}"}
	if got := do(t, app, http.MethodPost, "/api/stores/directory/email-send", jsonBody(t, send), bearer(tok)); got.Code != http.StatusOK {
		t.Fatalf("send = %d: %s", got.Code, got.Body)
	}
	if _, err := app.db.Exec(`UPDATE superusers SET email = 'moved@example.com' WHERE id = $1`, target.SuperuserID); err != nil {
		t.Fatal(err)
	}
	owners, err := app.storeOwners(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := owners[target.StoreID][0].EmailSent; got != nil {
		t.Fatalf("shown for the new address: %+v", got)
	}
}

func TestTheOwnerEmailsWordsNeedNoModel(t *testing.T) {
	subject, body := ownerEmailWords("Saffron\r\nBcc: x@example.com", "ShopNx")
	if strings.ContainsAny(subject, "\r\n") {
		t.Fatalf("subject = %q, want one line", subject)
	}
	if !strings.Contains(body, ownerVerifyPlaceholder) || !strings.Contains(body, "The ShopNx team") || !strings.Contains(body, "7 days") {
		t.Fatalf("body = %q", body)
	}
	if _, body := ownerEmailWords("Saffron", ""); !strings.Contains(body, "The team") || strings.Contains(body, " on .") {
		t.Fatalf("no brand: body = %q", body)
	}
	if subject, _ := ownerEmailWords(strings.Repeat("é", 200), "ShopNx"); !utf8.ValidString(subject) || len(subject) > ownerEmailMaxSubject {
		t.Fatalf("long name: %d bytes, valid %v", len(subject), utf8.ValidString(subject))
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
