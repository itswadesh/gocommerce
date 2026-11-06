package telegramalerts

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

const (
	owner        = int64(424242)
	commandsBase = "/api/admin/x/telegram-alerts/commands"
)

// bot2Token is a second bot's token, in the shape @BotFather hands out.
const bot2Token = "654321:XYZ-DEF1234ghIkl-zyx57W2v1u123ew22"

var nextUpdate atomic.Int64

// chatBot is one store's bot from Telegram's side: where it sends updates, and
// the secret it was given, read from what the fake received at setWebhook.
type chatBot struct {
	t      *testing.T
	app    *gocommerce.App
	fake   *fakeTelegram
	target string
	secret string
}

// switchOn presses the switch as the panel does and returns the bot.
func switchOn(t *testing.T, app *gocommerce.App, fake *fakeTelegram, token, query string) *chatBot {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodPost, commandsBase+query, map[string]bool{"on": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("switch on = %d %s", rec.Code, rec.Body)
	}
	fake.mu.Lock()
	hook := fake.webhooks[token]
	fake.mu.Unlock()
	u, err := url.Parse(hook.URL)
	if err != nil || hook.Secret == "" {
		t.Fatalf("no webhook set: %+v", hook)
	}
	return &chatBot{t: t, app: app, fake: fake, target: u.RequestURI(), secret: hook.Secret}
}

// onApp is a store with the alerts set up and two-way control switched on.
func onApp(t *testing.T) (*gocommerce.App, *Module, *fakeTelegram, *chatBot) {
	t.Helper()
	app, mod, fake := newApp(t, gocommerce.Config{PanelURL: "https://admin.shop.example"})
	setUp(t, app)
	bot := switchOn(t, app, fake, testToken, "")
	fake.reset()
	return app, mod, fake, bot
}

func (f *fakeTelegram) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent, f.methods = nil, nil
}

func (f *fakeTelegram) called(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.methods {
		if m == method {
			n++
		}
	}
	return n
}

func (f *fakeTelegram) allCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.methods)
}

func (f *fakeTelegram) lastText(t *testing.T) string {
	t.Helper()
	sent := f.messages()
	if len(sent) == 0 {
		t.Fatal("the bot said nothing")
	}
	return sent[len(sent)-1].Text
}

// post delivers one raw update, as Telegram would.
func (b *chatBot) post(secret string, update any) *httptest.ResponseRecorder {
	b.t.Helper()
	body, err := json.Marshal(update)
	if err != nil {
		b.t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, b.target, bytes.NewReader(body))
	if secret != "" {
		req.Header.Set(secretHeader, secret)
	}
	rec := httptest.NewRecorder()
	b.app.Handler().ServeHTTP(rec, req)
	return rec
}

func messageUpdate(id int64, chatID int64, chatType string, from int64, text string) map[string]any {
	return map[string]any{"update_id": id, "message": map[string]any{
		"message_id": 7, "from": map[string]any{"id": from},
		"chat": map[string]any{"id": chatID, "type": chatType}, "text": text,
	}}
}

func callbackUpdate(id int64, chatID int64, chatType string, from int64, data string) map[string]any {
	return map[string]any{"update_id": id, "callback_query": map[string]any{
		"id": "cb", "from": map[string]any{"id": from}, "data": data,
		"message": map[string]any{"message_id": 7, "chat": map[string]any{"id": chatID, "type": chatType}},
	}}
}

// say is the owner typing in their own chat.
func (b *chatBot) say(text string) string {
	b.t.Helper()
	before := len(b.fake.messages())
	if rec := b.post(b.secret, messageUpdate(nextUpdate.Add(1), owner, "private", owner, text)); rec.Code != http.StatusOK {
		b.t.Fatalf("%s = %d %s", text, rec.Code, rec.Body)
	}
	sent := b.fake.messages()
	if len(sent) <= before {
		b.t.Fatalf("%s: the bot said nothing", text)
	}
	return sent[len(sent)-1].Text
}

// tap is the owner pressing a button.
func (b *chatBot) tap(data string) {
	b.t.Helper()
	if rec := b.post(b.secret, callbackUpdate(nextUpdate.Add(1), owner, "private", owner, data)); rec.Code != http.StatusOK {
		b.t.Fatalf("tap %s = %d %s", data, rec.Code, rec.Body)
	}
}

// confirmData is the Confirm button on the bot's last message.
func (b *chatBot) confirmData() string {
	b.t.Helper()
	sent := b.fake.messages()
	if len(sent) == 0 || len(sent[len(sent)-1].Buttons) != 2 {
		b.t.Fatalf("the last message has no Confirm: %+v", sent)
	}
	return sent[len(sent)-1].Buttons[0]
}

func onHand(t *testing.T, app *gocommerce.App, id int64) int {
	t.Helper()
	v, err := app.Products().GetVariant(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return v.StockOnHand
}

func auditEntries(t *testing.T, app *gocommerce.App) []AuditEntry {
	t.Helper()
	rec := gctest.AdminRequest(t, app, http.MethodGet, commandsBase+"/audit", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audit = %d %s", rec.Code, rec.Body)
	}
	var out []AuditEntry
	gctest.DecodeData(t, rec, &out)
	return out
}

// ------------------------------------------------- off until the owner says so

// A store that keeps the alerts and never touches this gets what it always got:
// no webhook, no buttons, Find my chat as before, and the receiving route shut.
func TestCommandsAreOffUntilSwitchedOn(t *testing.T) {
	app, mod, fake := newApp(t, gocommerce.Config{PanelURL: "https://admin.shop.example"})
	setUp(t, app)

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)
	if sent := fake.messages(); len(sent) != 1 || sent[0].HasMarkup {
		t.Errorf("alert = %+v, want one plain alert with no buttons", sent)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/telegram-alerts/chats", nil); rec.Code != http.StatusOK {
		t.Errorf("Find my chat = %d %s", rec.Code, rec.Body)
	}
	if fake.called("setWebhook")+fake.called("getWebhookInfo")+fake.called("deleteWebhook") != 0 {
		t.Errorf("Telegram was touched: %v", fake.methods)
	}

	bot := &chatBot{t: t, app: app, fake: fake, target: webhookPath}
	before := fake.allCalls()
	if rec := bot.post("anything", messageUpdate(1, owner, "private", owner, "/orders")); rec.Code != http.StatusUnauthorized {
		t.Errorf("an update while off = %d, want 401", rec.Code)
	}
	if fake.allCalls() != before {
		t.Error("an update while off reached Telegram")
	}
}

// Switching on tells Telegram where to send updates and which ones, keeps only
// a hash of the secret, and greets the chat.
func TestSwitchingOnSetsTheWebhookAndKeepsNoSecret(t *testing.T) {
	app, _, fake := newApp(t, gocommerce.Config{PanelURL: "https://admin.shop.example"})
	setUp(t, app)
	bot := switchOn(t, app, fake, testToken, "")

	hook := fake.webhooks[testToken]
	if !strings.HasPrefix(hook.URL, "https://admin.shop.example"+webhookPath+"?store=") {
		t.Errorf("webhook = %q", hook.URL)
	}
	if len(hook.AllowedUpdates) != 2 || !hook.DropPending {
		t.Errorf("webhook = %+v, want message and callback_query only, dropping what piled up", hook)
	}
	if len(bot.secret) < 32 {
		t.Errorf("secret %q is too short", bot.secret)
	}
	if strings.Contains(bot.secret, testToken) || strings.Contains(hook.URL, testToken) {
		t.Error("the token went into the webhook's secret or address")
	}
	var stored string
	if err := app.DB().QueryRowContext(context.Background(), `SELECT secret_hash FROM telegram_commands`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == bot.secret || stored != hashSecret(bot.secret) {
		t.Errorf("stored %q: want the secret's hash, not the secret", stored)
	}
	rec := gctest.AdminRequest(t, app, http.MethodGet, commandsBase, nil)
	if strings.Contains(rec.Body.String(), bot.secret) || strings.Contains(rec.Body.String(), testToken) {
		t.Errorf("status carries a credential: %s", rec.Body)
	}
	if !strings.Contains(fake.lastText(t), "/orders") {
		t.Errorf("greeting = %q", fake.lastText(t))
	}
}

// What stops two-way control being switched on, each with its reason, and each
// leaving the bot's webhook untouched.
func TestSwitchingOnRefuses(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chat   string
		off    bool
		panel  string
		want   string
		status int
	}{
		{name: "group", chat: "-1001234567890", panel: "https://a.example", want: "private chat", status: 400},
		{name: "channel name", chat: "@shopnews", panel: "https://a.example", want: "private chat", status: 400},
		{name: "alerts off", chat: "424242", off: true, panel: "https://a.example", want: "alerts on", status: 400},
		{name: "no https", chat: "424242", panel: "http://127.0.0.1:8090", want: "HTTPS", status: 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app, _, fake := newApp(t, gocommerce.Config{PanelURL: tc.panel})
			on := !tc.off
			if _, err := app.Plugins().Update(context.Background(), PluginKey, gocommerce.PluginPatch{
				Enabled: &on, Settings: map[string]any{"bot_token": testToken, "chat_id": tc.chat},
			}); err != nil {
				t.Fatal(err)
			}
			rec := gctest.AdminRequest(t, app, http.MethodPost, commandsBase, map[string]bool{"on": true})
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.want) {
				t.Errorf("= %d %s, want %d saying %q", rec.Code, rec.Body, tc.status, tc.want)
			}
			if fake.called("setWebhook") != 0 {
				t.Error("the webhook was set anyway")
			}
		})
	}
}

// A public demo lets anybody sign in, so it is the one store that can never
// hand itself to a chat.
func TestADemoStoreCannotSwitchOn(t *testing.T) {
	app, _, fake := newApp(t, gocommerce.Config{PanelURL: "https://a.example"})
	setUp(t, app)
	if err := app.Stores().SetDemo(context.Background(), app.DefaultStore(), true); err != nil {
		t.Fatal(err)
	}
	rec := gctest.AdminRequest(t, app, http.MethodPost, commandsBase, map[string]bool{"on": true})
	if rec.Code == http.StatusOK || fake.called("setWebhook") != 0 {
		t.Errorf("= %d %s, set %d webhooks, want a refusal", rec.Code, rec.Body, fake.called("setWebhook"))
	}
}

// Rights: handing over the store takes alerts.commands and everything the chat
// could do, so a role cannot be given the first and collect the rest.
func TestSwitchingOnTakesTheRights(t *testing.T) {
	app, _, fake := newApp(t, gocommerce.Config{PanelURL: "https://a.example"})
	setUp(t, app)
	manager := gctest.OperatorToken(t, app, "manager@example.com", gocommerce.RoleManager)
	if rec := gctest.SessionRequest(t, app, manager, http.MethodPost, commandsBase, map[string]bool{"on": true}); rec.Code != http.StatusForbidden {
		t.Errorf("manager = %d, want 403", rec.Code)
	}
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/roles", map[string]any{
		"key": "switcher", "title": "Switcher", "rights": []string{"alerts.commands", "alerts.read"},
	}); rec.Code != http.StatusCreated {
		t.Fatalf("create role = %d %s", rec.Code, rec.Body)
	}
	switcher := gctest.OperatorToken(t, app, "switcher@example.com", "switcher")
	if rec := gctest.SessionRequest(t, app, switcher, http.MethodPost, commandsBase, map[string]bool{"on": true}); rec.Code != http.StatusForbidden {
		t.Errorf("role with alerts.commands only = %d %s, want 403", rec.Code, rec.Body)
	}
	if fake.called("setWebhook") != 0 {
		t.Error("the webhook was set for somebody without the rights")
	}
	ownerToken := gctest.OperatorToken(t, app, "owner@example.com", gocommerce.RoleOwner)
	if rec := gctest.SessionRequest(t, app, ownerToken, http.MethodPost, commandsBase, map[string]bool{"on": true}); rec.Code != http.StatusOK {
		t.Errorf("owner = %d %s", rec.Code, rec.Body)
	}
	entries := auditEntries(t, app)
	if len(entries) != 1 || entries[0].Action != "commands.on" || !strings.Contains(string(entries[0].Arguments), "operator") {
		t.Errorf("audit = %+v, want the owner switching on", entries)
	}
	// Off takes the route's right too: a manager reads the state, and cannot
	// cut the owner's chat off.
	if rec := gctest.SessionRequest(t, app, manager, http.MethodPost, commandsBase, map[string]bool{"on": false}); rec.Code != http.StatusForbidden {
		t.Errorf("manager switching off = %d, want 403", rec.Code)
	}
	if rec := gctest.SessionRequest(t, app, manager, http.MethodGet, commandsBase, nil); rec.Code != http.StatusOK {
		t.Errorf("manager reading the state = %d, want 200", rec.Code)
	}
}

// --------------------------------------------------------- the webhook's secret

func TestAnUnverifiedRequestDoesNothing(t *testing.T) {
	app, _, fake, bot := onApp(t)
	for _, secret := range []string{"", "wrong", bot.secret + "x", testToken, hashSecret(bot.secret)} {
		update := messageUpdate(nextUpdate.Add(1), owner, "private", owner, "/orders")
		if rec := bot.post(secret, update); rec.Code != http.StatusUnauthorized {
			t.Errorf("secret %q = %d, want 401", secret, rec.Code)
		}
	}
	if n := fake.allCalls(); n != 0 {
		t.Errorf("unverified requests made %d calls to Telegram: %v", n, fake.methods)
	}
	var seen int
	if err := app.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM telegram_commands_updates`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != 0 {
		t.Errorf("unverified requests recorded %d updates", seen)
	}
}

// A timing difference cannot be seen by a test of behaviour, so this reads the
// source: the comparison is the constant-time one, and on hashes.
func TestTheSecretIsComparedInConstantTime(t *testing.T) {
	src, err := os.ReadFile("commands.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), "subtle.ConstantTimeCompare([]byte(hashSecret(got)), []byte(wantHash))") {
		t.Error("validSecret no longer compares in constant time")
	}
}

// Switching off and on makes a new secret, and the old one is dead at once.
func TestAnOldSecretStopsWorking(t *testing.T) {
	app, _, fake, first := onApp(t)
	if rec := gctest.AdminRequest(t, app, http.MethodPost, commandsBase, map[string]bool{"on": false}); rec.Code != http.StatusOK {
		t.Fatalf("off = %d %s", rec.Code, rec.Body)
	}
	if rec := first.post(first.secret, messageUpdate(nextUpdate.Add(1), owner, "private", owner, "/orders")); rec.Code != http.StatusUnauthorized {
		t.Errorf("after switching off = %d, want 401", rec.Code)
	}
	second := switchOn(t, app, fake, testToken, "")
	if second.secret == first.secret {
		t.Fatal("switching on again reused the secret")
	}
	if rec := first.post(first.secret, messageUpdate(nextUpdate.Add(1), owner, "private", owner, "/orders")); rec.Code != http.StatusUnauthorized {
		t.Errorf("the old secret = %d, want 401", rec.Code)
	}
}

// ------------------------------------------------------------- who is obeyed

// Anyone can find a bot's username. Whoever is not the chosen person in their
// private chat gets no answer at all, and nothing changes.
func TestOnlyTheChosenPrivateChatIsObeyed(t *testing.T) {
	app, _, fake, bot := onApp(t)
	const stranger, group = int64(999), int64(-1001234567890)
	product := gctest.CreateProduct(t, app, "OBEY-1", 2500, 10)

	for name, update := range map[string]map[string]any{
		"a stranger":                messageUpdate(nextUpdate.Add(1), stranger, "private", stranger, "/orders"),
		"the owner in a group":      messageUpdate(nextUpdate.Add(1), group, "group", owner, "/orders"),
		"the owner in a supergroup": messageUpdate(nextUpdate.Add(1), group, "supergroup", owner, "/orders"),
		"a group wearing the id":    messageUpdate(nextUpdate.Add(1), owner, "group", owner, "/orders"),
		"a channel":                 messageUpdate(nextUpdate.Add(1), owner, "channel", owner, "/orders"),
		"somebody else, same chat":  messageUpdate(nextUpdate.Add(1), owner, "private", stranger, "/orders"),
		"no sender": {"update_id": nextUpdate.Add(1), "message": map[string]any{
			"chat": map[string]any{"id": owner, "type": "private"}, "text": "/orders"}},
		"a bot as the sender": {"update_id": nextUpdate.Add(1), "message": map[string]any{
			"from": map[string]any{"id": owner, "is_bot": true},
			"chat": map[string]any{"id": owner, "type": "private"}, "text": "/orders"}},
		"a tap from a stranger":    callbackUpdate(nextUpdate.Add(1), stranger, "private", stranger, "o:paid:1"),
		"a tap in a group":         callbackUpdate(nextUpdate.Add(1), group, "supergroup", owner, "o:paid:1"),
		"a tap, somebody else":     callbackUpdate(nextUpdate.Add(1), owner, "private", stranger, "o:paid:1"),
		"a change from a stranger": messageUpdate(nextUpdate.Add(1), stranger, "private", stranger, "/stock OBEY-1 0"),
	} {
		rec := bot.post(bot.secret, update)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want the same quiet 200 as any verified update", name, rec.Code)
		}
		if n := fake.allCalls(); n != 0 {
			t.Fatalf("%s got an answer: %v %+v", name, fake.methods, fake.messages())
		}
	}
	var seen int
	if err := app.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM telegram_commands_updates`).Scan(&seen); err != nil {
		t.Fatal(err)
	}
	if seen != 0 {
		t.Errorf("strangers wrote %d rows", seen)
	}
	if got := onHand(t, app, product.DefaultVariant().ID); got != 10 {
		t.Errorf("stock = %d", got)
	}
}

// An update Telegram would never send still has to come to nothing: a tap with
// no message beside a message from the owner is the message, and a tap with no
// message alone is nobody's.
func TestAnOddUpdateIsHandledQuietly(t *testing.T) {
	_, _, fake, bot := onApp(t)
	lonely := map[string]any{"update_id": nextUpdate.Add(1), "callback_query": map[string]any{
		"id": "cb", "from": map[string]any{"id": owner}, "data": "o:paid:1"}}
	if rec := bot.post(bot.secret, lonely); rec.Code != http.StatusOK || fake.allCalls() != 0 {
		t.Errorf("a tap with no message = %d, calls %v", rec.Code, fake.methods)
	}
	both := messageUpdate(nextUpdate.Add(1), owner, "private", owner, "/help")
	both["callback_query"] = lonely["callback_query"]
	if rec := bot.post(bot.secret, both); rec.Code != http.StatusOK || !strings.Contains(fake.lastText(t), "/orders") {
		t.Errorf("a message beside a broken tap = %d", rec.Code)
	}
}

// Whoever may edit the plugin's settings cannot give themselves the store by
// pointing them elsewhere: a changed chat or bot stops the listening, it does
// not move it, and buttons stop being offered.
func TestAChangedChatOrBotStopsTheCommands(t *testing.T) {
	for name, settings := range map[string]map[string]any{
		"another chat":  {"chat_id": "777"},
		"a group":       {"chat_id": "-1009"},
		"another bot":   {"bot_token": bot2Token},
		"a third party": {"bot_token": bot2Token, "chat_id": "777"},
	} {
		t.Run(name, func(t *testing.T) {
			app, mod, fake, bot := onApp(t)
			if _, err := app.Plugins().Update(context.Background(), PluginKey, gocommerce.PluginPatch{Settings: settings}); err != nil {
				t.Fatal(err)
			}
			for _, chat := range []int64{owner, 777} {
				bot.post(bot.secret, messageUpdate(nextUpdate.Add(1), chat, "private", chat, "/orders"))
			}
			if fake.allCalls() != 0 {
				t.Errorf("the bot answered: %v", fake.methods)
			}
			rec := gctest.AdminRequest(t, app, http.MethodGet, commandsBase, nil)
			var st CommandsStatus
			gctest.DecodeData(t, rec, &st)
			if !st.On || st.Paused == "" {
				t.Errorf("status = %+v, want on and saying why it is paused", st)
			}
			gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
			gctest.DrainOutbox(t, app)
			deliverOnce(t, mod)
			for _, m := range fake.messages() {
				if m.HasMarkup {
					t.Errorf("buttons went to a chat that is not obeyed: %+v", m)
				}
			}
		})
	}
}

// The same bot with a new token is the same bot: revoking a leaked token at
// @BotFather must not take the commands down.
func TestANewTokenForTheSameBotKeepsWorking(t *testing.T) {
	app, _, _, bot := onApp(t)
	rotated := "123456:NEW-DEF1234ghIkl-zyx57W2v1u123ew99"
	if _, err := app.Plugins().Update(context.Background(), PluginKey, gocommerce.PluginPatch{
		Settings: map[string]any{"bot_token": rotated}}); err != nil {
		t.Fatal(err)
	}
	if got := bot.say("/help"); !strings.Contains(got, "/orders") {
		t.Errorf("reply = %q", got)
	}
}

func TestSwitchingTheAlertsOffPausesTheCommands(t *testing.T) {
	app, _, fake, bot := onApp(t)
	off := false
	if _, err := app.Plugins().Update(context.Background(), PluginKey, gocommerce.PluginPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	bot.post(bot.secret, messageUpdate(nextUpdate.Add(1), owner, "private", owner, "/orders"))
	if fake.allCalls() != 0 {
		t.Errorf("the bot answered: %v", fake.methods)
	}
}

// ------------------------------------------------------------- reading

func TestTheChecksAnswerFromTheStore(t *testing.T) {
	app, _, _, bot := onApp(t)
	product := gctest.CreateProduct(t, app, "LOW-1", 2500, 2)
	order := gctest.Buy(t, app, gocommerce.CodeCOD, product.DefaultVariant().ID, 1).Order
	total := formatMoney(order.Total.AmountMinor, order.Total.Currency)

	if got := bot.say("/sales"); !strings.Contains(got, "Orders: 1") || !strings.Contains(got, total) {
		t.Errorf("/sales = %q", got)
	}
	if got := bot.say("/orders"); !strings.Contains(got, order.Number) || !strings.Contains(got, total) {
		t.Errorf("/orders = %q", got)
	}
	if got := bot.say("/order " + strings.ToLower(order.Number)); !strings.Contains(got, order.Number) || !strings.Contains(got, "1 x") {
		t.Errorf("/order = %q", got)
	}
	if got := bot.say("/lowstock"); !strings.Contains(got, "LOW-1: 1 left") {
		t.Errorf("/lowstock = %q", got)
	}
	if got := bot.say("/health"); !strings.Contains(got, "check") {
		t.Errorf("/health = %q", got)
	}
	if got := bot.say("/refund " + order.Number); !strings.Contains(got, "do not know") {
		t.Errorf("/refund = %q, want it refused: refunds are in the panel", got)
	}
	if got := bot.say("hello"); !strings.Contains(got, "/help") {
		t.Errorf("plain text = %q, want a pointer to /help", got)
	}
	entries := auditEntries(t, app)
	for _, e := range entries {
		if e.TelegramUserID != nil {
			t.Errorf("a read was audited as a change: %+v", e)
		}
	}
}

// Nothing the bot says about an order goes past what the alert already shows.
func TestRepliesCarryNoMoreOfTheShopperThanTheAlert(t *testing.T) {
	app, mod, fake, bot := onApp(t)
	order := gctest.PlaceOrder(t, app, gocommerce.CodeCOD).Order
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)

	bot.say("/orders")
	bot.say("/order " + order.Number)
	bot.say("/ship " + order.Number + " TRACK1")
	bot.tap("o:cancel:" + strconv.FormatInt(order.ID, 10))
	bot.say("/delivered " + order.Number)

	for _, m := range fake.messages() {
		for _, private := range []string{"gctest@example.com", "+15555550123", "5555550123", "1 Test Street", "Testville", "94117", "GC Test"} {
			if strings.Contains(m.Text, private) {
				t.Errorf("%q carries %q", m.Text, private)
			}
		}
	}
	if !strings.Contains(fake.messages()[0].Text, "Customer: GC") {
		t.Errorf("alert = %q, want the first name, which is what it always showed", fake.messages()[0].Text)
	}
}

// ------------------------------------------------------------- changing

func TestAStockChangeWaitsForConfirmThenIsAudited(t *testing.T) {
	app, _, _, bot := onApp(t)
	id := gctest.CreateProduct(t, app, "STOCK-1", 2500, 10).DefaultVariant().ID

	got := bot.say("/stock STOCK-1 12")
	if !strings.Contains(got, "12") || !strings.Contains(got, "10") || !strings.Contains(got, "STOCK-1") {
		t.Errorf("question = %q, want it to repeat the change", got)
	}
	if onHand(t, app, id) != 10 {
		t.Fatal("the stock moved before Confirm")
	}
	if len(auditEntries(t, app)) != 1 { // commands.on only
		t.Fatal("a change was audited before it was made")
	}
	bot.tap(bot.confirmData())
	if onHand(t, app, id) != 12 {
		t.Errorf("stock = %d, want 12", onHand(t, app, id))
	}
	if !strings.Contains(bot.fake.lastText(t), "12 on hand") {
		t.Errorf("reply = %q", bot.fake.lastText(t))
	}
	entries := auditEntries(t, app)
	last := entries[0]
	if last.TelegramUserID == nil || *last.TelegramUserID != owner || last.Action != kindStock || last.Outcome != "ok" {
		t.Errorf("audit = %+v, want the user, the action and ok", last)
	}
	if !strings.Contains(string(last.Arguments), "STOCK-1") {
		t.Errorf("audit arguments = %s", last.Arguments)
	}

	bot.say("/stock STOCK-1 +5")
	bot.tap(bot.confirmData())
	bot.say("/stock STOCK-1 -2")
	bot.tap(bot.confirmData())
	if onHand(t, app, id) != 15 {
		t.Errorf("stock = %d, want 15", onHand(t, app, id))
	}
}

// With two shelves, /stock moves the default one, as the agent tools do, and the
// question says so before anybody confirms a number meant for the whole store.
func TestAStockChangeSaysWhichLocation(t *testing.T) {
	app, _, _, bot := onApp(t)
	id := gctest.CreateProduct(t, app, "STOCK-3", 2500, 10).DefaultVariant().ID
	if rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/locations",
		map[string]string{"code": "second", "name": "Second shop"}); rec.Code != http.StatusCreated {
		t.Fatalf("create location = %d %s", rec.Code, rec.Body)
	}
	if got := bot.say("/stock STOCK-3 12"); !strings.Contains(got, "default location only") {
		t.Errorf("question = %q, want it to say which location", got)
	}
	bot.tap(bot.confirmData())
	if onHand(t, app, id) != 12 {
		t.Errorf("stock = %d, want 12", onHand(t, app, id))
	}
}

// A price comes from the server's record of what was asked, whatever the button
// says, and the button says nothing about it.
func TestThePriceIsHeldOnTheServerNotInTheButton(t *testing.T) {
	app, _, fake, bot := onApp(t)
	id := gctest.CreateProduct(t, app, "PRICE-1", 2500, 10).DefaultVariant().ID

	got := bot.say("/price PRICE-1 19.99")
	if !strings.Contains(got, formatMoney(2500, "USD")) || !strings.Contains(got, formatMoney(1999, "USD")) {
		t.Errorf("question = %q, want from and to", got)
	}
	sent := fake.messages()
	for _, data := range sent[len(sent)-1].Buttons {
		if !regexp.MustCompile(`^[yn]:[0-9a-f]{32}$`).MatchString(data) {
			t.Errorf("button data %q carries more than an opaque token", data)
		}
		if len(data) > 64 {
			t.Errorf("button data %q is over Telegram's 64 bytes", data)
		}
	}
	confirm := bot.confirmData()
	bot.tap(confirm + ":1")
	bot.tap("y:" + strings.Repeat("0", 32))
	v, _ := app.Products().GetVariant(context.Background(), id)
	if v.Price.AmountMinor != 2500 {
		t.Fatalf("a forged button changed the price to %d", v.Price.AmountMinor)
	}
	bot.tap(confirm)
	v, _ = app.Products().GetVariant(context.Background(), id)
	if v.Price.AmountMinor != 1999 {
		t.Errorf("price = %d, want 1999", v.Price.AmountMinor)
	}
}

func TestAPriceThatIsNotOneIsRefused(t *testing.T) {
	app, _, _, bot := onApp(t)
	gctest.CreateProduct(t, app, "PRICE-2", 2500, 10)
	for _, bad := range []string{"19.999", "1,299", "-5", "abc", "99999999999999999999999"} {
		if got := bot.say("/price PRICE-2 " + bad); !strings.Contains(got, "plain number") {
			t.Errorf("/price %s = %q", bad, got)
		}
	}
	for in, want := range map[string]int64{"19.99": 1999, "20": 2000, "0.5": 50} {
		if got, ok := parseAmount(in, "USD"); !ok || got != want {
			t.Errorf("parseAmount(%q, USD) = %d, %v", in, got, ok)
		}
	}
	if got, ok := parseAmount("150", "JPY"); !ok || got != 150 {
		t.Errorf("parseAmount(150, JPY) = %d, %v", got, ok)
	}
	if _, ok := parseAmount("1.5", "JPY"); ok {
		t.Error("yen has no decimals")
	}
}

func TestLeaveItChangesNothing(t *testing.T) {
	app, _, _, bot := onApp(t)
	id := gctest.CreateProduct(t, app, "STOCK-2", 2500, 10).DefaultVariant().ID
	bot.say("/stock STOCK-2 1")
	sent := bot.fake.messages()
	buttons := sent[len(sent)-1].Buttons
	bot.tap(buttons[1])
	bot.tap(buttons[0])
	if onHand(t, app, id) != 10 {
		t.Errorf("stock = %d after Leave it and then Confirm", onHand(t, app, id))
	}
}

// ---------------------------------------- one pending action, used once

func TestADoubleTapAppliesOnce(t *testing.T) {
	app, _, _, bot := onApp(t)
	id := gctest.CreateProduct(t, app, "TAP-1", 2500, 10).DefaultVariant().ID
	bot.say("/stock TAP-1 +5")
	confirm := bot.confirmData()
	bot.tap(confirm)
	bot.tap(confirm)
	if onHand(t, app, id) != 15 {
		t.Errorf("stock = %d, want 15", onHand(t, app, id))
	}
	if !strings.Contains(bot.fake.lastText(t), "changed nothing") {
		t.Errorf("second tap said %q", bot.fake.lastText(t))
	}
}

func TestTelegramRedeliveringAnUpdateAppliesOnce(t *testing.T) {
	app, _, fake, bot := onApp(t)
	id := gctest.CreateProduct(t, app, "TAP-2", 2500, 10).DefaultVariant().ID
	bot.say("/stock TAP-2 +5")
	update := callbackUpdate(nextUpdate.Add(1), owner, "private", owner, bot.confirmData())
	bot.post(bot.secret, update)
	replies := len(fake.messages())
	bot.post(bot.secret, update)
	if onHand(t, app, id) != 15 {
		t.Errorf("stock = %d, want 15", onHand(t, app, id))
	}
	if len(fake.messages()) != replies {
		t.Errorf("the redelivery was answered: %+v", fake.messages())
	}

	// And a command, which has no Confirm to protect it, asks only once.
	message := messageUpdate(nextUpdate.Add(1), owner, "private", owner, "/help")
	bot.post(bot.secret, message)
	replies = len(fake.messages())
	bot.post(bot.secret, message)
	if len(fake.messages()) != replies {
		t.Errorf("the redelivered command was answered twice")
	}
}

func TestAConfirmExpires(t *testing.T) {
	app, _, _, bot := onApp(t)
	id := gctest.CreateProduct(t, app, "TAP-3", 2500, 10).DefaultVariant().ID
	bot.say("/stock TAP-3 +5")
	confirm := bot.confirmData()
	if _, err := app.DB().ExecContext(context.Background(),
		`UPDATE telegram_commands_pending SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	bot.tap(confirm)
	if onHand(t, app, id) != 10 {
		t.Errorf("an expired Confirm moved the stock to %d", onHand(t, app, id))
	}
	if !strings.Contains(bot.fake.lastText(t), "changed nothing") {
		t.Errorf("reply = %q", bot.fake.lastText(t))
	}
}

// Asking again replaces the question: an old Confirm cannot run a change the
// owner has moved on from, and only one row can ever be waiting.
func TestANewQuestionKillsTheOldConfirm(t *testing.T) {
	app, _, _, bot := onApp(t)
	id := gctest.CreateProduct(t, app, "TAP-4", 2500, 10).DefaultVariant().ID
	bot.say("/stock TAP-4 +5")
	old := bot.confirmData()
	bot.say("/stock TAP-4 +1")
	fresh := bot.confirmData()
	var waiting int
	if err := app.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM telegram_commands_pending`).Scan(&waiting); err != nil {
		t.Fatal(err)
	}
	if waiting != 1 {
		t.Errorf("%d changes waiting, want 1", waiting)
	}
	bot.tap(old)
	if onHand(t, app, id) != 10 {
		t.Fatalf("the old Confirm ran: stock %d", onHand(t, app, id))
	}
	bot.tap(fresh)
	if onHand(t, app, id) != 11 {
		t.Errorf("stock = %d, want 11", onHand(t, app, id))
	}
}

// Two confirms at once, as two servers or a nervous thumb would send them.
func TestConfirmsAtOnceApplyOnce(t *testing.T) {
	app, _, _, bot := onApp(t)
	id := gctest.CreateProduct(t, app, "RACE-1", 2500, 10).DefaultVariant().ID
	bot.say("/stock RACE-1 +5")
	confirm := bot.confirmData()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		update := callbackUpdate(nextUpdate.Add(1), owner, "private", owner, confirm)
		wg.Add(1)
		go func() {
			defer wg.Done()
			bot.post(bot.secret, update)
		}()
	}
	wg.Wait()
	if onHand(t, app, id) != 15 {
		t.Errorf("stock = %d after 8 confirms at once, want 15", onHand(t, app, id))
	}
	applied := 0
	for _, e := range auditEntries(t, app) {
		if e.Action == kindStock {
			applied++
		}
	}
	if applied != 1 {
		t.Errorf("audited %d changes, want 1", applied)
	}
}

// The same update arriving twice at once is one update.
func TestARedeliveryRacingItselfRunsOnce(t *testing.T) {
	app, _, _, bot := onApp(t)
	id := gctest.CreateProduct(t, app, "RACE-2", 2500, 10).DefaultVariant().ID
	bot.say("/stock RACE-2 +5")
	update := callbackUpdate(nextUpdate.Add(1), owner, "private", owner, bot.confirmData())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bot.post(bot.secret, update)
		}()
	}
	wg.Wait()
	if onHand(t, app, id) != 15 {
		t.Errorf("stock = %d, want 15", onHand(t, app, id))
	}
	if n := bot.fake.called("answerCallbackQuery"); n != 1 {
		t.Errorf("handled %d times, want 1", n)
	}
}

// ------------------------------------------------------- the alert's buttons

func alertFor(t *testing.T, app *gocommerce.App, mod *Module, fake *fakeTelegram) (*gocommerce.Order, message) {
	t.Helper()
	order := gctest.PlaceOrder(t, app, gocommerce.CodeCOD).Order
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)
	sent := fake.messages()
	if len(sent) == 0 {
		t.Fatal("no alert")
	}
	return order, sent[len(sent)-1]
}

func TestANewOrderAlertCarriesItsButtons(t *testing.T) {
	app, mod, fake, bot := onApp(t)
	order, alert := alertFor(t, app, mod, fake)
	id := strconv.FormatInt(order.ID, 10)
	want := []string{"o:paid:" + id, "o:ship:" + id, "o:cancel:" + id}
	if strings.Join(alert.Buttons, " ") != strings.Join(want, " ") {
		t.Fatalf("buttons = %v, want %v", alert.Buttons, want)
	}
	if !strings.Contains(alert.Text, order.Number) {
		t.Errorf("alert = %q", alert.Text)
	}

	// Mark paid and Ship are one tap, and a tap is the same service call the
	// panel makes.
	bot.tap(want[0])
	got, _ := app.Order().Get(context.Background(), order.ID)
	if got.PaymentStatus != gocommerce.PaymentPaid {
		t.Errorf("payment = %s after Mark paid", got.PaymentStatus)
	}
	bot.tap(want[1])
	got, _ = app.Order().Get(context.Background(), order.ID)
	if got.Status != gocommerce.OrderShipped || len(got.Fulfillments) != 1 {
		t.Errorf("order = %s with %d shipments after Ship", got.Status, len(got.Fulfillments))
	}
	// A second Ship is refused by the order's rules and says so.
	bot.tap(want[1])
	got, _ = app.Order().Get(context.Background(), order.ID)
	if len(got.Fulfillments) != 1 {
		t.Errorf("a double tap on Ship made %d shipments", len(got.Fulfillments))
	}
	entries := auditEntries(t, app)
	if len(entries) < 3 || entries[0].Action != kindShip || entries[0].Outcome != "error" ||
		entries[1].Action != kindShip || entries[1].Outcome != "ok" ||
		entries[2].Action != kindMarkPaid || entries[2].TelegramUserID == nil || *entries[2].TelegramUserID != owner {
		t.Errorf("audit = %+v", entries)
	}
	// The order's own history names who, through the engine's actor seam.
	var label string
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT actor_label FROM admin_audit WHERE action = $1 ORDER BY id DESC LIMIT 1`, gocommerce.AuditOrderMarkPaid).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != "Telegram user "+strconv.FormatInt(owner, 10) {
		t.Errorf("actor label = %q", label)
	}
}

func TestShipTappedTwiceAtOnceShipsOnce(t *testing.T) {
	app, mod, fake, bot := onApp(t)
	order, alert := alertFor(t, app, mod, fake)
	var ship string
	for _, b := range alert.Buttons {
		if strings.HasPrefix(b, "o:ship:") {
			ship = b
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		update := callbackUpdate(nextUpdate.Add(1), owner, "private", owner, ship)
		wg.Add(1)
		go func() {
			defer wg.Done()
			bot.post(bot.secret, update)
		}()
	}
	wg.Wait()
	got, _ := app.Order().Get(context.Background(), order.ID)
	if len(got.Fulfillments) != 1 {
		t.Errorf("%d shipments, want 1", len(got.Fulfillments))
	}
}

func TestCancelFromAnAlertTakesAConfirm(t *testing.T) {
	app, mod, fake, bot := onApp(t)
	order, _ := alertFor(t, app, mod, fake)
	bot.tap("o:cancel:" + strconv.FormatInt(order.ID, 10))
	got, _ := app.Order().Get(context.Background(), order.ID)
	if got.Status == gocommerce.OrderCancelled {
		t.Fatal("cancelled without a Confirm")
	}
	if !strings.Contains(fake.lastText(t), order.Number) {
		t.Errorf("question = %q, want it to say which order", fake.lastText(t))
	}
	confirm := bot.confirmData()
	bot.tap(confirm)
	got, _ = app.Order().Get(context.Background(), order.ID)
	if got.Status != gocommerce.OrderCancelled {
		t.Errorf("order = %s after Confirm", got.Status)
	}
	bot.tap(confirm)
	if got := auditEntries(t, app)[0]; got.Action != kindCancel || got.Outcome != "ok" {
		t.Errorf("audit = %+v", got)
	}
}

// Changes from the chat meet the rules changes from the panel meet, in the same
// words, because they are the same services.
func TestTheOrderRulesAreTheServices(t *testing.T) {
	app, _, fake, bot := onApp(t)
	order := gctest.PlaceOrder(t, app, gocommerce.CodeCOD).Order

	bot.say("/delivered " + order.Number)
	bot.tap(bot.confirmData())
	if !strings.Contains(fake.lastText(t), "must be shipped before") {
		t.Errorf("delivered before shipping: %q", fake.lastText(t))
	}
	if got := auditEntries(t, app)[0]; got.Outcome != "error" || got.Detail == "" {
		t.Errorf("a refusal should be audited: %+v", got)
	}

	bot.say("/ship " + order.Number + " 1Z999AA10123456784")
	bot.tap(bot.confirmData())
	got, _ := app.Order().Get(context.Background(), order.ID)
	if got.Status != gocommerce.OrderShipped || got.Fulfillments[0].Tracking != "1Z999AA10123456784" {
		t.Errorf("order = %s, shipments %+v", got.Status, got.Fulfillments)
	}

	bot.tap("o:cancel:" + strconv.FormatInt(order.ID, 10))
	bot.tap(bot.confirmData())
	if !strings.Contains(fake.lastText(t), "already shipped") {
		t.Errorf("cancel after shipping: %q", fake.lastText(t))
	}

	bot.say("/delivered " + order.Number)
	bot.tap(bot.confirmData())
	got, _ = app.Order().Get(context.Background(), order.ID)
	if got.Status != gocommerce.OrderDelivered {
		t.Errorf("order = %s", got.Status)
	}

	id := gctest.CreateProduct(t, app, "RULE-1", 2500, 1).DefaultVariant().ID
	gctest.Buy(t, app, gocommerce.CodeCOD, id, 1)
	bot.say("/stock RULE-1 -5")
	bot.tap(bot.confirmData())
	if got := auditEntries(t, app)[0]; got.Outcome != "error" {
		t.Errorf("taking stock below zero: %+v", got)
	}
}

func TestAFailedAuditStopsTheChange(t *testing.T) {
	app, _, _, bot := onApp(t)
	id := gctest.CreateProduct(t, app, "AUDIT-1", 2500, 10).DefaultVariant().ID
	bot.say("/stock AUDIT-1 +5")
	confirm := bot.confirmData()
	// Rename rather than drop, so the row policy on it is left intact.
	if _, err := app.DB().ExecContext(context.Background(),
		`ALTER TABLE telegram_commands_audit RENAME TO telegram_commands_audit_gone`); err != nil {
		t.Skipf("the test login cannot rename the table: %v", err)
	}
	defer app.DB().ExecContext(context.Background(), `ALTER TABLE telegram_commands_audit_gone RENAME TO telegram_commands_audit`)
	bot.tap(confirm)
	if onHand(t, app, id) != 10 {
		t.Errorf("stock = %d: a change nobody could record went through", onHand(t, app, id))
	}
}

// ------------------------------------------------------------ between stores

// Two stores, two bots. One store's bot, secret and buttons reach nothing of the
// other's: the policy is what binds the statements, and the secret is checked
// against the store the request names.
func TestOneStoresBotCannotReachAnother(t *testing.T) {
	app, _, fake := newApp(t, gocommerce.Config{PanelURL: "https://admin.shop.example"})
	setUp(t, app)
	first := switchOn(t, app, fake, testToken, "")
	order := gctest.PlaceOrder(t, app, gocommerce.CodeCOD).Order
	firstID := gctest.CreateProduct(t, app, "FIRST-1", 2500, 10).DefaultVariant().ID

	st, err := app.Stores().Create(context.Background(), "Second", "second-store")
	if err != nil {
		t.Fatal(err)
	}
	secondCtx := gocommerce.WithStore(context.Background(), st.ID)
	on := true
	if _, err := app.Plugins().Update(secondCtx, PluginKey, gocommerce.PluginPatch{
		Enabled: &on, Settings: map[string]any{"bot_token": bot2Token, "chat_id": strconv.FormatInt(owner, 10)},
	}); err != nil {
		t.Fatal(err)
	}
	second := switchOn(t, app, fake, bot2Token, "?store="+st.ID)
	if first.target == second.target || first.secret == second.secret {
		t.Fatalf("the two stores share a webhook: %s, %s", first.target, second.target)
	}

	// Store one's secret at store two's address, and the reverse.
	before := len(fake.messages())
	for _, c := range []struct{ target, secret string }{{second.target, first.secret}, {first.target, second.secret}} {
		wrong := &chatBot{t: t, app: app, fake: fake, target: c.target}
		if rec := wrong.post(c.secret, messageUpdate(nextUpdate.Add(1), owner, "private", owner, "/orders")); rec.Code != http.StatusUnauthorized {
			t.Errorf("a secret at the other store's address = %d, want 401", rec.Code)
		}
	}
	if len(fake.messages()) != before {
		t.Error("a request with the other store's secret was answered")
	}

	// Store two asks for store one's order and variant, by number, id and SKU.
	if got := second.say("/order " + order.Number); !strings.Contains(got, "cannot find") {
		t.Errorf("/order in the other store = %q", got)
	}
	if got := second.say("/order " + strconv.FormatInt(order.ID, 10)); !strings.Contains(got, "cannot find") {
		t.Errorf("/order by id in the other store = %q", got)
	}
	if got := second.say("/stock FIRST-1 0"); !strings.Contains(got, "cannot find") {
		t.Errorf("/stock in the other store = %q", got)
	}
	if got := second.say("/price " + strconv.FormatInt(firstID, 10) + " 1"); !strings.Contains(got, "cannot find") {
		t.Errorf("/price by id in the other store = %q", got)
	}
	if got := second.say("/orders"); !strings.Contains(got, "No orders") {
		t.Errorf("/orders in the other store = %q", got)
	}
	second.tap("o:paid:" + strconv.FormatInt(order.ID, 10))
	got, _ := app.Order().Get(context.Background(), order.ID)
	if got.PaymentStatus == gocommerce.PaymentPaid {
		t.Error("the other store's button paid this store's order")
	}

	// Store one's Confirm tapped through store two's bot.
	first.say("/stock FIRST-1 +5")
	confirm := first.confirmData()
	second.tap(confirm)
	if onHand(t, app, firstID) != 10 {
		t.Errorf("store two's bot ran store one's Confirm: stock %d", onHand(t, app, firstID))
	}
	first.tap(confirm)
	if onHand(t, app, firstID) != 15 {
		t.Errorf("stock = %d: store one's own Confirm should still work", onHand(t, app, firstID))
	}

	// Each store's record holds only its own. Update ids are per bot, so the same
	// number at both stores is two updates.
	var entries []AuditEntry
	gctest.DecodeData(t, gctest.AdminRequest(t, app, http.MethodGet, commandsBase+"/audit?store="+st.ID, nil), &entries)
	for _, e := range entries {
		if strings.Contains(string(e.Arguments), "FIRST-1") {
			t.Errorf("store two's audit holds store one's change: %+v", e)
		}
	}
	shared := nextUpdate.Add(1)
	first.post(first.secret, messageUpdate(shared, owner, "private", owner, "/help"))
	n := len(fake.messages())
	second.post(second.secret, messageUpdate(shared, owner, "private", owner, "/help"))
	if len(fake.messages()) != n+1 {
		t.Error("an update id at one store silenced the same id at another")
	}
}

// ---------------------------------------------------------------- bounds

func TestAFloodIsBounded(t *testing.T) {
	app, _, fake, bot := onApp(t)
	for i := 0; i < maxUpdatesPerMinute+15; i++ {
		bot.post(bot.secret, messageUpdate(nextUpdate.Add(1), owner, "private", owner, "/help"))
	}
	if n := len(fake.messages()); n != maxUpdatesPerMinute {
		t.Errorf("%d replies to a flood, want %d", n, maxUpdatesPerMinute)
	}
	var kept int
	if err := app.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM telegram_commands_updates`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != maxUpdatesPerMinute {
		t.Errorf("%d updates kept, want %d", kept, maxUpdatesPerMinute)
	}
	// A minute on, it is open again.
	if _, err := app.DB().ExecContext(context.Background(),
		`UPDATE telegram_commands_updates SET received_at = now() - interval '2 minutes'`); err != nil {
		t.Fatal(err)
	}
	bot.say("/help")

	// A body far past any real update is not read to its end.
	huge := messageUpdate(nextUpdate.Add(1), owner, "private", owner, "/help "+strings.Repeat("x", maxUpdateBytes*2))
	replies := len(fake.messages())
	if rec := bot.post(bot.secret, huge); rec.Code != http.StatusOK {
		t.Errorf("oversized update = %d", rec.Code)
	}
	if len(fake.messages()) != replies {
		t.Error("an oversized update was obeyed")
	}
}

func TestTheOldUpdateLogIsPruned(t *testing.T) {
	app, _, _, bot := onApp(t)
	if _, err := app.DB().ExecContext(context.Background(),
		`INSERT INTO telegram_commands_updates (update_id, received_at) VALUES (1, now() - interval '3 days')`); err != nil {
		t.Fatal(err)
	}
	bot.say("/help")
	var old int
	if err := app.DB().QueryRowContext(context.Background(), `SELECT count(*) FROM telegram_commands_updates WHERE update_id = 1`).Scan(&old); err != nil {
		t.Fatal(err)
	}
	if old != 0 {
		t.Error("a three-day-old update is still kept")
	}
}

// ---------------------------------------------------- the token stays hidden

func TestNoFailureLogsTheTokenOrSecret(t *testing.T) {
	var logged bytes.Buffer
	fake := &fakeTelegram{}
	srv := gctest.StubHTTP(t, fake.handler)
	mod := New(Config{BaseURL: srv.URL})
	app := gctest.NewWithConfig(t, gocommerce.Config{
		PanelURL: "https://admin.shop.example",
		Logger:   slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})),
	}, mod)
	setUp(t, app)
	bot := switchOn(t, app, fake, testToken, "")
	gctest.CreateProduct(t, app, "LOG-1", 2500, 10)

	// Telegram gone: Go's error for this quotes the URL, token and all.
	mod.cfg.BaseURL = "http://127.0.0.1:1"
	bot.post(bot.secret, messageUpdate(nextUpdate.Add(1), owner, "private", owner, "/help"))
	bot.post(bot.secret, callbackUpdate(nextUpdate.Add(1), owner, "private", owner, "n:nothing"))
	rec := gctest.AdminRequest(t, app, http.MethodPost, commandsBase, map[string]bool{"on": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("off = %d %s", rec.Code, rec.Body)
	}
	var st CommandsStatus
	gctest.DecodeData(t, rec, &st)
	if st.On || !st.WebhookLeft {
		t.Errorf("status = %+v, want off with the webhook left behind", st)
	}
	if !strings.Contains(logged.String(), "telegram:") {
		t.Fatalf("nothing was logged, so this proves nothing: %s", logged.String())
	}
	for name, secret := range map[string]string{"token": testToken, "secret": bot.secret} {
		if strings.Contains(logged.String(), secret) || strings.Contains(rec.Body.String(), secret) {
			t.Errorf("the %s leaked: %s", name, logged.String())
		}
	}
	var row []byte
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT coalesce(jsonb_agg(a), '[]') FROM telegram_commands_audit a`).Scan(&row); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(row), testToken) || strings.Contains(string(row), bot.secret) {
		t.Errorf("the audit holds a credential: %s", row)
	}
}

// ----------------------------------------------- chats and the webhook, in turn

func TestFindMyChatAndTheWebhookTakeTurns(t *testing.T) {
	app, _, fake, bot := onApp(t)

	// On: not asked of Telegram, which would only answer 409.
	rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/telegram-alerts/chats", nil)
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "Two-way control is on") {
		t.Errorf("Find my chat while on = %d %s", rec.Code, rec.Body)
	}
	if fake.called("getUpdates") != 0 {
		t.Error("getUpdates was called with a webhook set")
	}

	// Off: the webhook goes, and Find my chat works.
	rec = gctest.AdminRequest(t, app, http.MethodPost, commandsBase, map[string]bool{"on": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("off = %d %s", rec.Code, rec.Body)
	}
	if _, set := fake.webhooks[testToken]; set {
		t.Error("the webhook is still set")
	}
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/telegram-alerts/chats", nil); rec.Code != http.StatusOK {
		t.Errorf("Find my chat after off = %d %s", rec.Code, rec.Body)
	}

	// Off while Telegram refused to remove it: stopped obeying all the same, and
	// the next Find my chat clears our own webhook out of the way.
	switchOn(t, app, fake, testToken, "")
	fake.refuse = map[string]bool{"deleteWebhook": true}
	rec = gctest.AdminRequest(t, app, http.MethodPost, commandsBase, map[string]bool{"on": false})
	var st CommandsStatus
	gctest.DecodeData(t, rec, &st)
	if rec.Code != http.StatusOK || st.On || !st.WebhookLeft {
		t.Errorf("off with Telegram refusing = %d %+v", rec.Code, st)
	}
	if rec := bot.post(bot.secret, messageUpdate(nextUpdate.Add(1), owner, "private", owner, "/help")); rec.Code != http.StatusUnauthorized {
		t.Errorf("an update after off = %d, want 401", rec.Code)
	}
	fake.refuse = nil
	if rec := gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/telegram-alerts/chats", nil); rec.Code != http.StatusOK {
		t.Errorf("Find my chat with our webhook left over = %d %s", rec.Code, rec.Body)
	}
	if _, set := fake.webhooks[testToken]; set {
		t.Error("our left-over webhook is still set")
	}
}

// A bot already wired to something else keeps it: nothing here takes another
// service's webhook, on either path.
func TestAnotherServicesWebhookIsLeftAlone(t *testing.T) {
	app, _, fake := newApp(t, gocommerce.Config{PanelURL: "https://admin.shop.example"})
	setUp(t, app)
	theirs := fakeWebhook{URL: "https://elsewhere.example/hook?store=" + app.DefaultStore()}
	fake.webhooks = map[string]fakeWebhook{testToken: theirs}

	rec := gctest.AdminRequest(t, app, http.MethodPost, commandsBase, map[string]bool{"on": true})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "another service") {
		t.Errorf("switching on = %d %s", rec.Code, rec.Body)
	}
	rec = gctest.AdminRequest(t, app, http.MethodGet, "/api/admin/x/telegram-alerts/chats", nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("Find my chat = %d %s", rec.Code, rec.Body)
	}
	if fake.called("setWebhook")+fake.called("deleteWebhook") != 0 || fake.webhooks[testToken].URL != theirs.URL {
		t.Errorf("their webhook was touched: %v %+v", fake.methods, fake.webhooks)
	}
}

// A store that moved its panel to a new address can still take back its webhook.
func TestItsOwnWebhookAtAnOldAddressIsItsOwn(t *testing.T) {
	app, _, fake := newApp(t, gocommerce.Config{PanelURL: "https://admin.shop.example"})
	setUp(t, app)
	fake.webhooks = map[string]fakeWebhook{testToken: {URL: "https://old.example" + webhookPath + "?store=" + app.DefaultStore()}}
	switchOn(t, app, fake, testToken, "")
	if !strings.HasPrefix(fake.webhooks[testToken].URL, "https://admin.shop.example") {
		t.Errorf("webhook = %+v", fake.webhooks[testToken])
	}
}

func TestTheSecretsAreWithheldFromReports(t *testing.T) {
	fake := &fakeTelegram{}
	srv := gctest.StubHTTP(t, fake.handler)
	app := gctest.NewWithReports(t, New(Config{BaseURL: srv.URL}))
	gctest.AssertReportsCannotRead(t, app, "telegram_commands", "secret_hash")
	gctest.AssertReportsCannotRead(t, app, "telegram_commands_pending", "token")
}
