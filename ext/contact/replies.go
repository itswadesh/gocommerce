package contact

import (
	"context"
	"database/sql"
	"net/http"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Reply is one answer the store sent to a message.
type Reply struct {
	ID      int64  `json:"id"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
	// SentBy is the operator's address, "api key: …" for a key, or "token"
	// for a script holding the static admin token.
	SentBy    string    `json:"sent_by"`
	CreatedAt time.Time `json:"created_at"`
}

// ReplyInput is what an operator sends. An empty subject is "Re: " and the
// message's own.
type ReplyInput struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

func (m *Module) replies(ctx context.Context, messageID int64) ([]Reply, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, subject, body, sent_by, created_at FROM contact_replies
		WHERE message_id = $1 ORDER BY id`, messageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reply{}
	for rows.Next() {
		var rp Reply
		if err := rows.Scan(&rp.ID, &rp.Subject, &rp.Body, &rp.SentBy, &rp.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, rp)
	}
	return out, rows.Err()
}

// handleReply writes to the shopper now and keeps what was written.
//
// The email goes first and the record after, with no transaction open across
// the send (rule 5): a reply the provider did not take is not on the record as
// one, the message stays as it was, and the operator keeps the text to try
// again. NotifyNow is what makes that true — Notify would report a refusal,
// or a store with no provider at all, as delivered.
func (m *Module) handleReply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in ReplyInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	in.Subject, in.Body = strings.TrimSpace(in.Subject), strings.TrimSpace(in.Body)
	switch {
	case in.Body == "":
		gocommerce.RespondError(w, r, gocommerce.Validationf("the reply is empty"))
		return
	case len(in.Body) > maxBody:
		gocommerce.RespondError(w, r, gocommerce.Validationf("the reply is too long: %d characters at most", maxBody))
		return
	case len(in.Subject) > maxSubject:
		gocommerce.RespondError(w, r, gocommerce.Validationf("the subject is too long: %d characters at most", maxSubject))
		return
	}
	// Anybody may sign in to a public demo, and anybody may type an address
	// into its storefront form: a reply there would mail a stranger whatever
	// the visitor wrote, from the store's own domain.
	if m.app.Demo(ctx) {
		gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusForbidden, Code: "demo_frozen",
			Message: "this is a public demo: replies are not sent"})
		return
	}
	msg, err := m.byID(ctx, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if in.Subject == "" {
		in.Subject = replySubject(msg)
	}
	data := map[string]string{
		"name": msg.Name, "subject": in.Subject, "reply": in.Body,
		"original": msg.Body, "original_subject": msg.Subject, "store_name": m.storeName(ctx),
	}
	if err := m.app.NotifyNow(ctx, gocommerce.Notification{
		Event: "contact.reply", Channel: gocommerce.ChannelEmail, To: msg.Email, Data: data,
	}); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO contact_replies (message_id, subject, body, sent_by) VALUES ($1, $2, $3, $4)`,
			id, in.Subject, in.Body, actorOf(ctx)); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			UPDATE contact_messages SET status = 'replied', updated_at = now() WHERE id = $1`, id)
		return err
	})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out, err := m.withThread(ctx, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.mask(ctx, out)
	gocommerce.Respond(w, http.StatusCreated, out)
}

// replySubject is what a reply is called when the operator did not say: the
// conversation's own subject, so the shopper's mail threads it.
func replySubject(msg *Message) string {
	about := msg.Subject
	if about == "" {
		return "Re: your message"
	}
	if strings.HasPrefix(strings.ToLower(about), "re:") {
		return about
	}
	return "Re: " + about
}

// actorOf names whoever is behind an admin request: the operator's address
// (a key's is "api key: <name>"), or "token" for a script holding the static
// admin token.
func actorOf(ctx context.Context) string {
	if su := gocommerce.SuperuserFrom(ctx); su != nil && su.Email != "" {
		return su.Email
	}
	return "token"
}

// storeName is the shop's trading name, else the store's own: the shopper is
// told whose people answered.
func (m *Module) storeName(ctx context.Context) string {
	if p, err := m.app.Profile().Get(ctx); err == nil && p.Name != "" {
		return p.Name
	}
	if id, ok := gocommerce.StoreFrom(ctx); ok {
		if st, err := m.app.Stores().Get(ctx, id); err == nil {
			return st.Name
		}
	}
	return ""
}
