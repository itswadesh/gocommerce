package telegramalerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// telegramError is why a call to the Bot API did not succeed: Telegram's own
// answer, no answer at all, or settings that never let a call be made.
type telegramError struct {
	// status is the HTTP status Telegram answered with; 0 when none came back.
	status      int
	description string
	retryAfter  time.Duration
	migrateTo   int64
	unreachable bool
	// missing names a setting that is not there ("token" or "chat"), and
	// badToken a token not in @BotFather's form; either way nothing was sent.
	missing  string
	badToken bool
}

func (e *telegramError) Error() string { return explain(e) }

// refused is Telegram saying no — an unknown chat, a blocked bot, a revoked
// token — which it will say again on the twelfth try. Being unreachable, busy
// or rate-limited is not, and neither are settings the owner may be fixing
// right now: those wait and go again.
func (e *telegramError) refused() bool {
	return e.status >= 400 && e.status < 500 && e.status != http.StatusTooManyRequests
}

// explain is the reason in words the owner can act on, never carrying the
// token: every description that could have quoted it was scrubbed on the way in.
func explain(e *telegramError) string {
	desc := e.description
	lower := strings.ToLower(desc)
	switch {
	case e.missing == "token":
		return "No bot token is set. Enter the one @BotFather gave you under Notifications › Setup Telegram."
	case e.missing == "chat":
		return "No chat is chosen for the alerts. Pick one with Find my chat under Notifications › Setup Telegram."
	case e.badToken:
		return "That is not a bot token from @BotFather: it is the bot's number, a colon and a long code, like 123456789:AAE…. Copy it again from your chat with @BotFather."
	case e.unreachable:
		return "Could not reach Telegram: " + desc
	case e.status == http.StatusUnauthorized || e.status == http.StatusNotFound:
		return "Telegram does not know this bot token (" + desc + "). Copy it again from @BotFather; if you revoked it there, use the new one."
	case e.migrateTo != 0:
		return "This group became a supergroup, and Telegram moved it to chat " + strconv.FormatInt(e.migrateTo, 10) + ". Choose it again with Find my chat."
	case e.status == http.StatusForbidden:
		return "The bot cannot write to this chat (" + desc + "). Unblock it in Telegram, or add it to the group again."
	case e.status == http.StatusBadRequest && strings.Contains(lower, "chat not found"):
		return "Telegram cannot find this chat (" + desc + "). Send the bot a message first, then choose the chat with Find my chat."
	case e.status == http.StatusConflict:
		return "This bot has a webhook set, so Telegram hands its messages to that address and not to this screen (" + desc + "). Type the chat ID yourself, or remove the bot's webhook."
	case e.status == http.StatusTooManyRequests:
		return "Telegram asked to slow down (" + desc + ")."
	case e.status != 0:
		return "Telegram answered " + strconv.Itoa(e.status) + ": " + desc
	}
	return desc
}

// call makes one Bot API call and decodes its result into result.
//
// The token is the path — https://api.telegram.org/bot<token>/<method> is the
// whole of Telegram's authentication — so the request URL is a secret. Go's
// network errors quote it ("Post \"https://…/bot123:AA…/sendMessage\": dial
// tcp …"), which is why no error leaves here without being scrubbed.
func (m *Module) call(ctx context.Context, token, method string, payload, result any) error {
	if token == "" {
		return &telegramError{missing: "token"}
	}
	if !tokenRE.MatchString(token) {
		return &telegramError{badToken: true}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.cfg.BaseURL+"/bot"+token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return &telegramError{unreachable: true, description: "the request could not be built"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "gocommerce-telegram-alerts/1")

	resp, err := m.client.Do(req)
	if err != nil {
		return &telegramError{unreachable: true, description: scrub(token, err)}
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var answer struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter      int   `json:"retry_after"`
			MigrateToChatID int64 `json:"migrate_to_chat_id"`
		} `json:"parameters"`
	}
	_ = json.Unmarshal(raw, &answer)
	if resp.StatusCode == http.StatusOK && answer.OK {
		if result != nil && len(answer.Result) > 0 {
			if err := json.Unmarshal(answer.Result, result); err != nil {
				return &telegramError{status: resp.StatusCode, description: "Telegram's answer could not be read"}
			}
		}
		return nil
	}
	e := &telegramError{
		status:      resp.StatusCode,
		description: scrubText(token, answer.Description),
		retryAfter:  time.Duration(answer.Parameters.RetryAfter) * time.Second,
		migrateTo:   answer.Parameters.MigrateToChatID,
	}
	if e.description == "" {
		e.description = resp.Status
	}
	return e
}

// scrub is a network error without the URL that carried the token.
func scrub(token string, err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return scrubText(token, err.Error())
}

func scrubText(token, s string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "<bot token>")
	}
	return s
}

// sendTo sends one plain-text message. Plain, not Markdown or HTML: a
// customer's name with an underscore in it would otherwise be a formatting
// error and an alert that never arrived. Link previews are off, so an order
// link does not unfold the panel's sign-in page into the chat.
func (m *Module) sendTo(ctx context.Context, b bot, text string) error {
	if b.token == "" {
		return &telegramError{missing: "token"}
	}
	if b.chat == "" {
		return &telegramError{missing: "chat"}
	}
	return m.call(ctx, b.token, "sendMessage", map[string]any{
		"chat_id":              b.chat,
		"text":                 text,
		"link_preview_options": map[string]bool{"is_disabled": true},
	}, nil)
}

// failure is a Bot API error as a response: the settings' fault is a 400, and
// Telegram's answer a 422 carrying its reason — not a 5xx, whose message the
// engine replaces, because the reason is what the owner pressed the button for.
func (m *Module) failure(action string, err error) error {
	var te *telegramError
	if !errors.As(err, &te) {
		return gocommerce.Internalf(err, "%s", action)
	}
	switch {
	case te.missing != "" || te.badToken:
		return gocommerce.Validationf("%s", explain(te))
	case te.unreachable || te.status >= 500 || te.status == http.StatusOK:
		m.log.Warn("telegram could not be reached", "action", action, "reason", explain(te))
		return &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "telegram_unreachable",
			Message: "Could not reach Telegram, so nothing was done. Try again in a minute; if it keeps failing, the server log says why."}
	default:
		return &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "telegram_refused", Message: explain(te)}
	}
}

// Chat is a chat the bot has heard from: somewhere the alerts could go.
type Chat struct {
	// ID is a string because a group's is a 13-digit negative number, and a
	// browser reading it as a float is one rounding away from another chat.
	ID string `json:"id"`
	// Type is private, group, supergroup or channel.
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username,omitempty"`
}

type telegramChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

// chatsFrom is every chat in a getUpdates answer, newest first, once each. An
// update is its id and one payload — a message, a channel post, the bot being
// added to a group — and whichever it is, the chat is in it.
func chatsFrom(updates []map[string]json.RawMessage) []Chat {
	out := []Chat{}
	seen := map[int64]bool{}
	for i := len(updates) - 1; i >= 0; i-- {
		for key, raw := range updates[i] {
			if key == "update_id" {
				continue
			}
			var payload struct {
				Chat *telegramChat `json:"chat"`
			}
			if json.Unmarshal(raw, &payload) != nil || payload.Chat == nil || payload.Chat.ID == 0 || seen[payload.Chat.ID] {
				continue
			}
			c := payload.Chat
			seen[c.ID] = true
			title := c.Title
			if title == "" {
				title = strings.TrimSpace(c.FirstName + " " + c.LastName)
			}
			if title == "" && c.Username != "" {
				title = "@" + c.Username
			}
			out = append(out, Chat{ID: strconv.FormatInt(c.ID, 10), Type: c.Type, Title: title, Username: c.Username})
		}
	}
	return out
}

// handleChats asks Telegram which chats have written to the bot lately, so the
// owner picks theirs rather than hunting for a number. getUpdates without an
// offset confirms nothing, so asking twice gives the same answer, and Telegram
// keeps an update for a day — a message sent this morning is still there.
func (m *Module) handleChats(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var updates []map[string]json.RawMessage
	if err := m.call(ctx, m.bot(ctx).token, "getUpdates", map[string]int{"limit": 100, "timeout": 0}, &updates); err != nil {
		gocommerce.RespondError(w, r, m.failure("find the chat", err))
		return
	}
	gocommerce.Respond(w, http.StatusOK, chatsFrom(updates))
}

// handleTest sends one message to the chosen chat now. It works before the
// alerts are switched on, which is the point: the owner sees the bot and the
// chat work before an order depends on them.
func (m *Module) handleTest(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	b := m.bot(ctx)
	name := m.storeName(ctx)
	if name == "" {
		name = "your store"
	}
	text := "This is a test from " + name + ". Order alerts will arrive in this chat."
	if !m.enabled(ctx) {
		text += "\nSwitch the alerts on under Notifications › Setup Telegram to start getting them."
	}
	if err := m.sendTo(ctx, b, text); err != nil {
		gocommerce.RespondError(w, r, m.failure("send the test", err))
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"sent": true, "chat_id": b.chat})
}
