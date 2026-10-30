package whatsapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Meta's webhooks, by its "Create a webhook endpoint" guide: a GET to verify
// the address, answered with hub.challenge when hub.verify_token is the one
// the store chose; then a POST per batch of events, signed in
// X-Hub-Signature-256 as "sha256=" and the hex HMAC-SHA256 of the raw body
// keyed with the app secret. The address names its store (?store=), so each
// store's own secrets judge it.

func (m *Module) handleVerify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	want := m.account(r.Context()).verifyToken
	got := q.Get("hub.verify_token")
	if q.Get("hub.mode") != "subscribe" || want == "" ||
		subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("the verify token does not match"))
		return
	}
	// Meta reads the challenge back as the body, plain.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, q.Get("hub.challenge"))
}

// validSignature is X-Hub-Signature-256 checked against the raw body.
func validSignature(secret string, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok || secret == "" {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// The notification, by the names in Meta's webhook reference for messages and
// statuses.
type notification struct {
	Object string `json:"object"`
	Entry  []struct {
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				Metadata struct {
					PhoneNumberID string `json:"phone_number_id"`
				} `json:"metadata"`
				Messages []inbound `json:"messages"`
				Statuses []struct {
					ID     string `json:"id"`
					Status string `json:"status"`
					Errors []struct {
						Code    int    `json:"code"`
						Title   string `json:"title"`
						Message string `json:"message"`
					} `json:"errors"`
				} `json:"statuses"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

type inbound struct {
	From string `json:"from"`
	ID   string `json:"id"`
	Type string `json:"type"`
	Text struct {
		Body string `json:"body"`
	} `json:"text"`
	Button struct {
		Payload string `json:"payload"`
		Text    string `json:"text"`
	} `json:"button"`
	Interactive struct {
		ButtonReply struct {
			ID string `json:"id"`
		} `json:"button_reply"`
	} `json:"interactive"`
}

func (m *Module) handleWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the body could not be read"))
		return
	}
	acct := m.account(ctx)
	if !validSignature(acct.appSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("the signature does not match"))
		return
	}
	var n notification
	if err := json.Unmarshal(body, &n); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the body is not a WhatsApp notification"))
		return
	}
	// A database failure is a 500, which Meta retries; anything about one
	// message is that message's and the rest still count.
	for _, e := range n.Entry {
		for _, c := range e.Changes {
			v := c.Value
			if c.Field != "messages" || (acct.phoneID != "" && v.Metadata.PhoneNumberID != acct.phoneID) {
				continue
			}
			for _, s := range v.Statuses {
				reason := ""
				if len(s.Errors) > 0 {
					reason = s.Errors[0].Title
					if s.Errors[0].Message != "" {
						reason = s.Errors[0].Message
					}
				}
				if err := m.applyStatus(ctx, s.ID, s.Status, reason); err != nil {
					gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record a whatsapp status"))
					return
				}
			}
			for _, msg := range v.Messages {
				if err := m.receive(ctx, acct, msg); err != nil {
					gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record a whatsapp reply"))
					return
				}
			}
		}
	}
	m.nudge()
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"received": true})
}

// statusRank orders a message's fate. Meta's webhooks can arrive out of order,
// and a "delivered" landing after "read" must not move it back.
var statusRank = map[string]int{"queued": 0, "sent": 1, "delivered": 2, "read": 3}

func (m *Module) applyStatus(ctx context.Context, wamid, status, reason string) error {
	rank, known := statusRank[status]
	if wamid == "" || (!known && status != "failed") {
		return nil
	}
	var id int64
	var orderID sql.NullInt64
	var slot string
	err := m.db.QueryRowContext(ctx, `
		UPDATE whatsapp_messages
		SET status = $2, last_error = CASE WHEN $2 = 'failed' THEN nullif($4, '') ELSE last_error END, updated_at = now()
		WHERE wamid = $1 AND status <> 'failed'
		  AND ($2 = 'failed' OR array_position(ARRAY['queued','sent','delivered','read'], status) - 1 < $3)
		RETURNING id, order_id, slot`, wamid, status, rank, reason).Scan(&id, &orderID, &slot)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !orderID.Valid || slot == slotReply {
		return nil
	}
	line := slotLabel(slot) + " " + status + " on WhatsApp"
	if status == "failed" && reason != "" {
		line = slotLabel(slot) + " on WhatsApp failed: " + reason
	}
	m.onOrder(ctx, &orderID.Int64, line)
	return nil
}

// optOutWords and optInWords are the replies that stop and restart messages.
// Meta asks a business to honour an opt-out however it arrives; these are the
// ones written as a word.
const stopPromotions = "Stop promotions"

var (
	optOutWords = map[string]bool{"STOP": true, "UNSUBSCRIBE": true, "STOP ALL": true, "OPT OUT": true, "OPTOUT": true}
	optInWords  = map[string]bool{"START": true, "SUBSCRIBE": true, "UNSTOP": true}
)

func (m *Module) receive(ctx context.Context, acct account, msg inbound) error {
	phone, ok := e164("+"+strings.TrimPrefix(msg.From, "+"), "")
	if !ok {
		return nil
	}
	word := strings.ToUpper(strings.Join(strings.Fields(msg.Text.Body), " "))
	switch {
	case msg.Type == "text" && optOutWords[word]:
		if err := m.setOptOut(ctx, phone, true); err != nil {
			return err
		}
		return m.reply(ctx, phone, msg.ID, "You will not get WhatsApp messages from us any more. Reply START to undo.")
	case msg.Type == "text" && optInWords[word]:
		if err := m.setOptOut(ctx, phone, false); err != nil {
			return err
		}
		return m.reply(ctx, phone, msg.ID, "You are subscribed again to the WhatsApp messages you agreed to.")
	}
	if err := m.heard(ctx, phone); err != nil {
		return err
	}
	// The suggested reminder's own opt-out button: offers stop, order updates
	// go on, as the shopper asked for no more than that.
	if msg.Type == "button" && strings.EqualFold(strings.TrimSpace(msg.Button.Text), stopPromotions) {
		if _, err := m.db.ExecContext(ctx, `
			UPDATE whatsapp_contacts SET marketing = false, updated_at = now() WHERE phone = $1`, phone); err != nil {
			return err
		}
		return m.reply(ctx, phone, msg.ID, "You will not get offers or basket reminders from us on WhatsApp any more.")
	}
	payload := msg.Button.Payload
	if msg.Type == "interactive" {
		payload = msg.Interactive.ButtonReply.ID
	}
	if action, rest, ok := strings.Cut(strings.TrimPrefix(payload, "cod:"), ":"); ok && strings.HasPrefix(payload, "cod:") {
		return m.codReply(ctx, acct, phone, msg, action, rest)
	}
	return nil
}

// codReply records the shopper's answer to a cash-on-delivery confirmation.
// The message it answers must be one this store sent that number about that
// order, so a payload from anybody else does nothing; and only the first
// answer counts, since Meta may deliver a webhook twice. The reply's context
// id is not required to match: a tap can arrive before the worker has written
// the id Meta gave the message, and the signed payload already names both.
func (m *Module) codReply(ctx context.Context, acct account, phone string, msg inbound, action, orderRef string) error {
	if action != "confirm" && action != "cancel" {
		return nil
	}
	orderID, err := strconv.ParseInt(orderRef, 10, 64)
	if err != nil {
		return nil
	}
	var id int64
	var number string
	err = m.db.QueryRowContext(ctx, `
		SELECT id, coalesce(order_number, '') FROM whatsapp_messages
		WHERE slot = 'cod_confirmation' AND order_id = $1 AND phone = $2 AND reply IS NULL
		ORDER BY id DESC LIMIT 1`, orderID, phone).Scan(&id, &number)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}

	// Claimed before anything is done, so a redelivered webhook finds the
	// answer already taken and a failure below is retried by nobody.
	res, err := m.db.ExecContext(ctx, `
		UPDATE whatsapp_messages SET reply = $2, updated_at = now() WHERE id = $1 AND reply IS NULL`, id, action)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}

	reply, line, answer := "confirmed",
		"The shopper confirmed this cash-on-delivery order on WhatsApp",
		"Thank you, your order "+number+" is confirmed."
	if action == "cancel" {
		reply = "cancel_requested"
		line = "The shopper asked on WhatsApp to cancel this cash-on-delivery order. Call them before it ships."
		answer = "Thanks, we have your request to cancel order " + number + " and will be in touch."
		if acct.codCancel {
			cctx := gocommerce.WithActorLabel(ctx, "WhatsApp")
			if _, err := m.app.Order().Cancel(cctx, orderID, "The shopper cancelled on WhatsApp"); err == nil {
				reply, line = "cancelled", ""
				answer = "Your order " + number + " is cancelled."
			} else if !errors.Is(err, gocommerce.ErrConflict) {
				// Let Meta's retry try again rather than leave the answer
				// taken and nothing done about it.
				_, _ = m.db.ExecContext(ctx, `UPDATE whatsapp_messages SET reply = NULL WHERE id = $1`, id)
				return err
			} else {
				line = "The shopper asked on WhatsApp to cancel this order, but it could not be cancelled: " + err.Error()
			}
		}
	}
	if _, err := m.db.ExecContext(ctx, `
		UPDATE whatsapp_messages SET reply = $2, updated_at = now() WHERE id = $1`, id, reply); err != nil {
		return err
	}
	if line != "" {
		m.onOrder(ctx, &orderID, line)
	}
	return m.reply(ctx, phone, msg.ID, answer)
}

// reply queues a free-form answer; the shopper has just written, so the
// 24-hour window is open, and the worker checks again before it sends.
func (m *Module) reply(ctx context.Context, phone, inboundID, text string) error {
	return m.queue(ctx, "whatsapp.reply", slotReply, phone,
		map[string]string{"in_reply_to": inboundID}, textMessage(phone, text))
}
