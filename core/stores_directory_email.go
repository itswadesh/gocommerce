package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Asking a store's owner to verify their email, from the All stores page (D221).
// The words are fixed, the install's owner reads and may change them, and the
// engine adds the one-time link as it sends, so the panel never holds a live link.
// What went out is kept and shown until the address is verified (D225).

const (
	// EventOwnerEmailVerification names the message in the log line and to the
	// provider. It has no template: the owner's words are the message.
	EventOwnerEmailVerification = "superuser.email_verification"

	// ownerVerifyPlaceholder marks where the link goes in the words.
	ownerVerifyPlaceholder = "{{verify_link}}"
	// A week, not reset's hour: nobody is waiting on this one, and it may sit
	// unread for days.
	ownerVerifyTTL = 7 * 24 * time.Hour

	// What the kept copy of an email says in place of the link, which is a
	// credential until it is used.
	ownerVerifyLinkShown = "[verification link]"

	ownerEmailSendTimeout = 20 * time.Second
	ownerEmailMaxSubject  = 200
	ownerEmailMaxBody     = 5000
)

func (a *App) mountOwnerEmailRoutes() {
	a.Handle("POST /api/stores/directory/email-draft", a.personAuth(http.HandlerFunc(a.handleOwnerEmailDraft)))
	a.Handle("POST /api/stores/directory/email-send", a.personAuth(http.HandlerFunc(a.handleOwnerEmailSend)))
	// Unauthenticated, as a password reset's is: the emailed token is the
	// credential, and the person clicking may never have signed in on this device.
	a.HandleFunc("POST /api/stores/verify-email", a.handleVerifyOwnerEmail)
}

type ownerEmailTarget struct {
	SuperuserID int64  `json:"superuser_id"`
	StoreID     string `json:"store_id"`
}

// ownerEmailRecipient reads the person only if they own the named store, so the
// routes cannot be pointed at an address nobody chose from the list.
func (a *App) ownerEmailRecipient(ctx context.Context, t ownerEmailTarget) (email string, store string, brand Brand, err error) {
	var host string
	var proved bool
	err = a.db.QueryRowContext(ctx, `
		SELECT coalesce(su.email, ''), coalesce(su.email = su.email_proved, false), st.name, coalesce(st.brand_host, '')
		FROM store_members m
		JOIN superusers su ON su.id = m.superuser_id
		JOIN stores st ON st.id = m.store_id
		WHERE m.store_id = $1 AND m.superuser_id = $2 AND m.role = $3`,
		t.StoreID, t.SuperuserID, RoleOwner).Scan(&email, &proved, &store, &host)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", Brand{}, NotFoundf("that person does not own that store")
	case err != nil:
		return "", "", Brand{}, Internalf(err, "read the store's owner")
	case email == "":
		return "", "", Brand{}, Validationf("this owner has no email address")
	case proved:
		return "", "", Brand{}, Conflictf("%s is already verified", email)
	}
	return email, store, a.storeBrand(host), nil
}

// handleOwnerEmailDraft is the email to read and change before it is sent.
func (a *App) handleOwnerEmailDraft(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !a.isPlatformOwner(SuperuserFrom(ctx)) {
		RespondError(w, r, Forbiddenf("this list is for the install's owner"))
		return
	}
	var in ownerEmailTarget
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	email, store, brand, err := a.ownerEmailRecipient(ctx, in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	subject, body := ownerEmailWords(store, brand.Name)
	Respond(w, http.StatusOK, map[string]string{"from": a.cfg.PlatformEmailFrom, "to": email, "subject": subject, "body": body})
}

// ownerEmailWords is the email for a store and brand. A store's name was typed by
// a stranger, so it is flattened to one line: a subject cannot carry a line break.
func ownerEmailWords(store, brand string) (subject, body string) {
	store = strings.Join(strings.Fields(store), " ")
	if len(store) > 120 {
		store = strings.ToValidUTF8(store[:120], "")
	}
	about, signature := "", "The team"
	if brand != "" {
		about, signature = " on "+brand, "The "+brand+" team"
	}
	return "Please confirm your email address for " + store,
		"Hello,\n\nPlease confirm this email address for your store " + store + about + ". One click is all it takes:\n\n" +
			ownerVerifyPlaceholder + "\n\nThe link works for 7 days.\n\n" + signature
}

func (a *App) handleOwnerEmailSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !a.isPlatformOwner(SuperuserFrom(ctx)) {
		RespondError(w, r, Forbiddenf("this list is for the install's owner"))
		return
	}
	var in struct {
		ownerEmailTarget
		Subject string `json:"subject"`
		Body    string `json:"body"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	in.Subject, in.Body = strings.TrimSpace(in.Subject), strings.TrimSpace(in.Body)
	switch {
	case in.Subject == "" || in.Body == "":
		RespondError(w, r, Validationf("an email needs a subject and a message"))
		return
	case len(in.Subject) > ownerEmailMaxSubject || strings.ContainsAny(in.Subject, "\r\n"):
		RespondError(w, r, Validationf("the subject must be one line of at most %d characters", ownerEmailMaxSubject))
		return
	case len(in.Body) > ownerEmailMaxBody:
		RespondError(w, r, Validationf("the message must be at most %d characters", ownerEmailMaxBody))
		return
	}
	email, _, brand, err := a.ownerEmailRecipient(ctx, in.ownerEmailTarget)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	scope := a.superusers.identityMailScope(ctx)
	switch {
	case a.cfg.PlatformEmailFrom == "":
		RespondError(w, r, Conflictf("no sending address is set: GOCOMMERCE_PLATFORM_EMAIL_FROM"))
		return
	case brand.PanelURL == "":
		RespondError(w, r, Conflictf("this store's brand has no panel address to link to"))
		return
	case !a.notifier.delivers(scope, ChannelEmail):
		// Otherwise the log would take it and the screen would say it had gone.
		RespondError(w, r, Conflictf("this install cannot send email yet"))
		return
	}
	token, err := a.newOwnerEmailToken(ctx, in.SuperuserID, email)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	sendCtx, cancel := context.WithTimeout(scope, ownerEmailSendTimeout)
	defer cancel()
	err = a.notifier.send(sendCtx, Notification{
		Event:    EventOwnerEmailVerification,
		Channel:  ChannelEmail,
		To:       email,
		Language: a.cfg.DefaultLanguage,
		From:     a.cfg.PlatformEmailFrom,
		Subject:  in.Subject,
		Body:     ownerEmailWithLink(in.Body, brand.PanelURL+"/admin/auth/verify-email/"+token),
	})
	if err != nil {
		_, _ = a.db.ExecContext(ctx, `DELETE FROM superuser_email_verifications WHERE token_hash = $1`, hashToken(token))
		a.log.Error("owner email: not sent", "store", in.StoreID, "error", err)
		RespondError(w, r, &APIError{Status: http.StatusBadGateway, Code: "email_not_sent", Message: "the email was not sent: " + err.Error()})
		return
	}
	sent := OwnerEmailSent{
		Email:   email,
		From:    a.cfg.PlatformEmailFrom,
		Subject: in.Subject,
		Body:    ownerEmailWithLink(in.Body, ownerVerifyLinkShown),
		SentAt:  time.Now().UTC(),
	}
	// A failure here loses the copy and not the email, which has gone.
	if raw, err := json.Marshal(sent); err != nil {
		a.log.Error("owner email: the copy could not be encoded", "store", in.StoreID, "error", err)
	} else if _, err := a.db.ExecContext(ctx, `UPDATE superusers SET owner_email_sent = $2::jsonb WHERE id = $1`, in.SuperuserID, string(raw)); err != nil {
		a.log.Error("owner email: the copy was not kept", "store", in.StoreID, "error", err)
	}
	Respond(w, http.StatusOK, map[string]any{"sent_to": email, "email_sent": sent})
}

// OwnerEmailSent is the email last sent to a store's owner, with the link taken
// out, shown on the install owner's list until the address is verified (D225).
type OwnerEmailSent struct {
	Email   string    `json:"email"`
	From    string    `json:"from"`
	Subject string    `json:"subject"`
	Body    string    `json:"body"`
	SentAt  time.Time `json:"sent_at"`
}

// ownerEmailWithLink puts the link where the words say, or after them when an
// edit removed the mark: a verification email with no link does nothing.
func ownerEmailWithLink(body, link string) string {
	if strings.Contains(body, ownerVerifyPlaceholder) {
		return strings.ReplaceAll(body, ownerVerifyPlaceholder, link)
	}
	return body + "\n\n" + link
}

func (a *App) newOwnerEmailToken(ctx context.Context, superuserID int64, email string) (string, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", Internalf(err, "generate verification link")
	}
	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO superuser_email_verifications (token_hash, superuser_id, email, expires_at)
		VALUES ($1, $2, $3, $4)`, hashToken(token), superuserID, email, time.Now().Add(ownerVerifyTTL)); err != nil {
		return "", Internalf(err, "store verification link")
	}
	// Kept a day past expiry for no reason but simplicity: every claim checks
	// expires_at, so a kept row opens nothing.
	_, _ = a.db.ExecContext(ctx, `DELETE FROM superuser_email_verifications WHERE expires_at < now() - interval '1 day'`)
	return token, nil
}

// handleVerifyOwnerEmail spends a link. It is POST, sent by the page the link
// opens, because mail scanners fetch every link in a message and a GET would
// verify an address nobody looked at. It names nobody in its answer.
func (a *App) handleVerifyOwnerEmail(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	gone := &APIError{Status: http.StatusBadRequest, Code: "invalid_link", Message: "this link has expired or was already used"}
	err := InTx(ctx, a.db, func(tx *sql.Tx) error {
		var id int64
		var email string
		err := tx.QueryRowContext(ctx, `
			DELETE FROM superuser_email_verifications
			WHERE token_hash = $1 AND expires_at > now() RETURNING superuser_id, email`, hashToken(in.Token)).Scan(&id, &email)
		if errors.Is(err, sql.ErrNoRows) {
			return gone
		}
		if err != nil {
			return Internalf(err, "claim verification link")
		}
		// Matched on the address the link went to: an account that has since moved
		// to another one has not proved that one.
		res, err := tx.ExecContext(ctx, `UPDATE superusers SET email_proved = email, owner_email_sent = NULL, updated_at = now() WHERE id = $1 AND email = $2`, id, email)
		if err != nil {
			return Internalf(err, "record the proved address")
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return gone
		}
		return nil
	})
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, map[string]bool{"verified": true})
}
