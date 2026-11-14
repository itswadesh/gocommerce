package telegramalerts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

// Two-way control: the owner runs the store from the same Telegram chat the
// alerts arrive in, by command and by button (D216).
//
// It is off until the owner switches it on here, and a store that never does
// sends and receives exactly what it did before. Switched on, a row in
// telegram_commands records the one thing that may be obeyed: this bot, this
// private chat, this webhook secret. Everything in the settings can change
// after that, and whoever can change plugin settings cannot give themselves
// the store by doing so: a different bot or chat stops the bot listening
// until somebody holding rightCommands switches it on again.
//
// The webhook secret is random and not derived from the bot token, because a
// leaked token must not also be the key to this store's webhook.

const (
	webhookPath  = "/x/telegram-alerts/webhook"
	secretHeader = "X-Telegram-Bot-Api-Secret-Token"

	rightCommands gocommerce.Right = "alerts.commands"

	// webhookConnections is how many requests Telegram may have open at once:
	// a flood waits at Telegram's end rather than taking the store's database
	// connections. One owner in one chat has no use for Telegram's default of 40.
	webhookConnections = 5
)

// commandRights is everything the chat can do, and so what the person handing
// it over must hold: alerts.commands alone must not be a way to get the rest.
var commandRights = []gocommerce.Right{
	gocommerce.RightOrdersRead, gocommerce.RightOrdersWrite, gocommerce.RightOrdersFulfill,
	gocommerce.RightInventoryRead, gocommerce.RightInventoryWrite, gocommerce.RightCatalogWrite,
	gocommerce.RightReportsRead, gocommerce.RightStoreOperate,
}

type commandsRow struct {
	botID      string
	chatID     int64
	secretHash string
}

func (m *Module) commandsRow(ctx context.Context) (*commandsRow, error) {
	var c commandsRow
	err := m.db.QueryRowContext(ctx, `SELECT bot_id, chat_id, secret_hash FROM telegram_commands`).
		Scan(&c.botID, &c.chatID, &c.secretHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the telegram commands")
	}
	return &c, nil
}

// botID is the number before the colon, which Telegram treats as public: the
// same bot with a new token keeps working, another bot does not.
func botID(token string) string {
	id, _, _ := strings.Cut(token, ":")
	return id
}

// listening is whether updates are obeyed right now. Every reason it can be
// false fails closed, and the status route explains each one.
func (m *Module) listening(ctx context.Context, c *commandsRow, b bot) bool {
	return c != nil && m.pausedBecause(ctx, c, b) == ""
}

func (m *Module) pausedBecause(ctx context.Context, c *commandsRow, b bot) string {
	switch {
	case !m.enabled(ctx):
		return "The alerts are switched off, and the bot ignores commands while they are."
	case botID(b.token) != c.botID:
		return "The bot token now belongs to a different bot, so commands are ignored. Switch two-way control off and on again to hand the store to this one."
	case b.chat != strconv.FormatInt(c.chatID, 10):
		return "The chosen chat has changed, so commands are ignored. Switch two-way control off and on again to hand the store to this chat."
	}
	return ""
}

// webhookURL is where Telegram sends this store's updates: the store named the
// way ext/notify-whatsapp names it. Empty when the panel has no HTTPS address,
// which Telegram would refuse.
func (m *Module) webhookURL(ctx context.Context) string {
	base := m.app.StoreBrand(ctx).PanelURL
	if base == "" {
		base = m.app.Config().PanelURL
	}
	if !strings.HasPrefix(base, "https://") {
		return ""
	}
	return strings.TrimRight(base, "/") + webhookPath + "?store=" + url.QueryEscape(m.storeID(ctx))
}

func (m *Module) storeID(ctx context.Context) string {
	if store, ok := gocommerce.StoreFrom(ctx); ok {
		return store
	}
	return m.app.DefaultStore()
}

// webhookIsOurs compares the path and the store and not the host, so a store
// that moved to a new address can still take back its own webhook.
func (m *Module) webhookIsOurs(ctx context.Context, address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Path == webhookPath && u.Query().Get("store") == m.storeID(ctx)
}

// removeOwnWebhook reports whether a webhook of this store's was found and
// removed. Failing is not an error to anybody: the caller's 409 still stands.
func (m *Module) removeOwnWebhook(ctx context.Context, token string) bool {
	var info struct {
		URL string `json:"url"`
	}
	if err := m.call(ctx, token, "getWebhookInfo", map[string]any{}, &info); err != nil || !m.webhookIsOurs(ctx, info.URL) {
		return false
	}
	return m.call(ctx, token, "deleteWebhook", map[string]any{}, nil) == nil
}

// switchOnBlocker is why two-way control cannot be switched on, in words for
// the owner, or nothing when it can.
func (m *Module) switchOnBlocker(ctx context.Context, b bot) string {
	id, err := strconv.ParseInt(b.chat, 10, 64)
	switch {
	case m.app.Demo(ctx):
		return "This is a public demo, and anybody signed in here could take it over."
	case !m.enabled(ctx):
		return "Switch the alerts on first."
	case b.token == "":
		return "Save your bot token first."
	case b.chat == "":
		return "Choose your chat with the bot first."
	case err != nil || id <= 0:
		// A private chat's id is the person's, and always positive. Groups and
		// channels are negative or a @name, and everyone in one would be holding
		// the store.
		return "Two-way control only works in your own private chat with the bot, not in a group or channel. Choose that chat first."
	case m.webhookURL(ctx) == "":
		return "This store's panel has no public HTTPS address, and Telegram can only send messages to one."
	}
	return ""
}

// CommandsStatus is what the Setup Telegram screen shows about two-way control.
// The secret and the token are never in it.
type CommandsStatus struct {
	// On is whether the owner switched it on; Paused says why it is ignoring
	// commands although they did.
	On     bool   `json:"on"`
	Paused string `json:"paused,omitempty"`
	ChatID string `json:"chat_id,omitempty"`
	// Blocker is why it cannot be switched on yet.
	Blocker string `json:"blocker,omitempty"`
	// WebhookLeft is true right after switching off, when Telegram could not be
	// told. The webhook does nothing any more; Find my chat removes it.
	WebhookLeft bool `json:"webhook_left,omitempty"`
}

func (m *Module) commandsStatus(ctx context.Context) (CommandsStatus, error) {
	c, err := m.commandsRow(ctx)
	if err != nil {
		return CommandsStatus{}, err
	}
	b := m.bot(ctx)
	if c == nil {
		return CommandsStatus{Blocker: m.switchOnBlocker(ctx, b)}, nil
	}
	return CommandsStatus{On: true, ChatID: strconv.FormatInt(c.chatID, 10), Paused: m.pausedBecause(ctx, c, b)}, nil
}

func (m *Module) handleCommandsStatus(w http.ResponseWriter, r *http.Request) {
	st, err := m.commandsStatus(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, st)
}

// handleCommandsSwitch turns two-way control on or off. The right on the route
// says who may be asked; holding all of what the chat could do is checked here.
func (m *Module) handleCommandsSwitch(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		On *bool `json:"on"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if in.On == nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("send on: true or on: false"))
		return
	}
	var left bool
	var err error
	if *in.On {
		err = m.switchOn(ctx)
	} else {
		left, err = m.switchOff(ctx)
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	now, err := m.commandsStatus(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	now.WebhookLeft = left
	gocommerce.Respond(w, http.StatusOK, now)
}

func (m *Module) switchOn(ctx context.Context) error {
	if su := gocommerce.SuperuserFrom(ctx); su != nil {
		for _, right := range commandRights {
			if !su.Has(right) {
				return gocommerce.Forbiddenf("two-way control can ship, cancel and reprice from a chat, so switching it on takes %s, and your role does not carry it", right)
			}
		}
	}
	b := m.bot(ctx)
	if blocker := m.switchOnBlocker(ctx, b); blocker != "" {
		return gocommerce.Validationf("%s", blocker)
	}
	chatID, _ := strconv.ParseInt(b.chat, 10, 64)

	// Telegram holds one webhook per bot, and setting ours would silently cut
	// off whatever else is using this one.
	var info struct {
		URL string `json:"url"`
	}
	if err := m.call(ctx, b.token, "getWebhookInfo", map[string]any{}, &info); err != nil {
		return m.failure("check the bot", err)
	}
	if info.URL != "" && !m.webhookIsOurs(ctx, info.URL) {
		return gocommerce.Conflictf("This bot already sends its messages to another service. Make a new bot with @BotFather for your store, or remove that webhook first.")
	}

	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return gocommerce.Internalf(err, "make the webhook secret")
	}
	secret := hex.EncodeToString(raw)
	if err := m.call(ctx, b.token, "setWebhook", map[string]any{
		"url":             m.webhookURL(ctx),
		"secret_token":    secret,
		"allowed_updates": []string{"message", "callback_query"},
		"max_connections": webhookConnections,
		// Whatever piled up while this was off was not said to this feature, and
		// a week-old "/stock" should not run the moment it comes on.
		"drop_pending_updates": true,
	}, nil); err != nil {
		return m.failure("switch on two-way control", err)
	}
	// After the call, never around it (rule 5). If this write fails the webhook
	// is set and every update is refused, and switching on again puts it right.
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO telegram_commands (bot_id, chat_id, secret_hash) VALUES ($1, $2, $3)
		ON CONFLICT (store_id) DO UPDATE SET
		    bot_id = excluded.bot_id, chat_id = excluded.chat_id, secret_hash = excluded.secret_hash, created_at = now()`,
		botID(b.token), chatID, hashSecret(secret)); err != nil {
		return gocommerce.Internalf(err, "switch on two-way control")
	}
	m.auditSwitch(ctx, "commands.on", b.chat)
	// The test of the whole round trip the owner can see. A refusal here is not
	// worth undoing the switch for: they can press Start and send /help.
	if err := m.sendTo(ctx, b, "You can now run your store from this chat. Only this chat is obeyed.\n\n"+helpText); err != nil {
		m.logFailure(ctx, "the greeting", err)
	}
	return nil
}

// switchOff stops obeying first and tells Telegram second, so that Telegram
// being down can never leave the switch on. It reports whether the webhook was
// left behind.
func (m *Module) switchOff(ctx context.Context) (webhookLeft bool, err error) {
	if _, err := m.db.ExecContext(ctx, `DELETE FROM telegram_commands`); err != nil {
		return false, gocommerce.Internalf(err, "switch off two-way control")
	}
	if _, err := m.db.ExecContext(ctx, `DELETE FROM telegram_commands_pending`); err != nil {
		return false, gocommerce.Internalf(err, "switch off two-way control")
	}
	m.auditSwitch(ctx, "commands.off", "")
	// Whatever is on the bot now: this store's, or nothing. Another service's
	// webhook could not have been left there by switching on.
	token := m.bot(ctx).token
	if token == "" {
		return false, nil
	}
	if err := m.call(ctx, token, "deleteWebhook", map[string]any{}, nil); err != nil {
		m.logFailure(ctx, "removing the webhook", err)
		return true, nil
	}
	return false, nil
}

func hashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func validSecret(wantHash, got string) bool {
	return got != "" && subtle.ConstantTimeCompare([]byte(hashSecret(got)), []byte(wantHash)) == 1
}

func (m *Module) logFailure(ctx context.Context, what string, err error) {
	var te *telegramError
	reason := "unexpected error"
	if errors.As(err, &te) {
		reason = explain(te) // scrubbed on the way in, by call
	}
	m.log.Warn("telegram: "+what+" failed", "store", m.storeID(ctx), "reason", reason)
}

// ------------------------------------------------------------------- the audit

// AuditEntry is one change made from the chat, or two-way control being
// switched. TelegramUserID is absent on a switch, which an operator made.
type AuditEntry struct {
	ID             int64           `json:"id"`
	TelegramUserID *int64          `json:"telegram_user_id,omitempty"`
	Action         string          `json:"action"`
	Arguments      json.RawMessage `json:"arguments"`
	Outcome        string          `json:"outcome"`
	Detail         string          `json:"detail,omitempty"`
	CreatedAt      time.Time       `json:"created_at"`
}

// auditSwitch records who handed the store over. A failure here is logged, not
// answered: the switch has already happened, and it must not read as undone.
func (m *Module) auditSwitch(ctx context.Context, action, chat string) {
	who := "the admin token"
	if su := gocommerce.SuperuserFrom(ctx); su != nil {
		who = "operator " + strconv.FormatInt(su.ID, 10)
	}
	args, _ := json.Marshal(map[string]string{"by": who, "chat_id": chat})
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO telegram_commands_audit (action, arguments, outcome) VALUES ($1, $2, 'ok')`, action, args); err != nil {
		m.log.Error("telegram: could not audit the switch", "action", action, "error", err)
	}
}

// auditStart is written before the change: a crash between the change and its
// record then leaves a row saying "started", rather than a change with no trace.
// If it cannot be written the change does not happen.
func (m *Module) auditStart(ctx context.Context, user int64, c change) (int64, error) {
	args, err := json.Marshal(c)
	if err != nil {
		return 0, err
	}
	var id int64
	err = m.db.QueryRowContext(ctx, `
		INSERT INTO telegram_commands_audit (telegram_user_id, action, arguments, outcome)
		VALUES ($1, $2, $3, 'started') RETURNING id`, user, c.Kind, args).Scan(&id)
	return id, err
}

func (m *Module) auditFinish(ctx context.Context, id int64, cause error) {
	outcome, detail := "ok", ""
	if cause != nil {
		outcome, detail = "error", cause.Error()
	}
	if _, err := m.db.ExecContext(ctx, `
		UPDATE telegram_commands_audit SET outcome = $2, detail = nullif($3, '') WHERE id = $1`, id, outcome, detail); err != nil {
		m.log.Error("telegram: could not finish the audit record", "id", id, "error", err)
	}
}

func (m *Module) handleCommandsAudit(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*) FROM telegram_commands_audit`).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "count the telegram audit"))
		return
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, telegram_user_id, action, arguments, outcome, coalesce(detail, ''), created_at
		FROM telegram_commands_audit ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read the telegram audit"))
		return
	}
	defer rows.Close()
	out := []AuditEntry{}
	for rows.Next() {
		var e AuditEntry
		var args []byte
		if err := rows.Scan(&e.ID, &e.TelegramUserID, &e.Action, &args, &e.Outcome, &e.Detail, &e.CreatedAt); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read the telegram audit"))
			return
		}
		e.Arguments = args
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read the telegram audit"))
		return
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}
