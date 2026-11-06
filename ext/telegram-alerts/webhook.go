package telegramalerts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	// pendingTTL is how long a Confirm button works: long enough to read it,
	// short enough that nobody confirms this morning's price at teatime.
	pendingTTL = 5 * time.Minute

	// maxUpdatesPerMinute bounds what the chosen chat can make the store do, per
	// store. A person tapping does not reach it, and Telegram itself holds a bot
	// to about a message a second per chat, so past it nothing useful is lost.
	maxUpdatesPerMinute = 30

	// maxUpdateBytes is well over the largest text message Telegram carries.
	maxUpdateBytes = 1 << 16

	// updateRetention outlasts the day Telegram keeps trying an update.
	updateRetention = "2 days"

	lowStockThreshold = 5
	listLength        = 5
	maxStockChange    = 1_000_000
	maxTracking       = 100
)

const helpText = `What you can ask:
/sales - today's sales
/orders - the latest orders
/order GC-001042 - one order
/lowstock - what is running out
/health - is the store well

What you can change, after you tap Confirm:
/stock SKU 12 - set the count
/stock SKU +5 - add to it, or -5 to take away
/price SKU 1299.50 - set the price
/ship GC-001042 TRACKING - ship with a tracking number
/delivered GC-001042 - mark as delivered

Refunds are in the panel.`

// The parts of Telegram's Update this reads. Everything else, edited messages
// and channel posts among them, is not asked for at setWebhook and not read here.
type tgUpdate struct {
	UpdateID int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
	Callback *struct {
		ID      string     `json:"id"`
		From    *tgUser    `json:"from"`
		Data    string     `json:"data"`
		Message *tgMessage `json:"message"`
	} `json:"callback_query"`
}

type tgMessage struct {
	MessageID int64        `json:"message_id"`
	From      *tgUser      `json:"from"`
	Chat      telegramChat `json:"chat"`
	Text      string       `json:"text"`
}

type tgUser struct {
	ID    int64 `json:"id"`
	IsBot bool  `json:"is_bot"`
}

type inlineKeyboard struct {
	Rows [][]inlineButton `json:"inline_keyboard"`
}

// inlineButton's Data comes back from Telegram on a tap. It names an order or a
// pending change and never holds a price or a quantity: those stay on the server.
type inlineButton struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// handleWebhook is the only way anything reaches the commands. The secret is
// checked before the body is read, and "off" and "wrong secret" are the same
// answer, so a request learns nothing about whether a store uses this.
//
// A verified request is always answered 200, whatever became of it: Telegram
// redelivers anything else, and there is no update here worth a second try that
// the owner would not rather repeat themselves.
func (m *Module) handleWebhook(w http.ResponseWriter, r *http.Request) {
	c, err := m.commandsRow(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if c == nil || !validSecret(c.secretHash, r.Header.Get(secretHeader)) {
		gocommerce.RespondError(w, r, gocommerce.ErrUnauthorized)
		return
	}
	var u tgUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxUpdateBytes)).Decode(&u); err == nil {
		// Telegram hanging up must not abandon a change halfway through telling
		// the owner about it. The store binding on the context carries over.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 45*time.Second)
		defer cancel()
		m.onUpdate(ctx, c, u)
	}
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"ok": true})
}

// obeyed is the one question about who is talking. A group's updates carry the
// group's own chat id, but the type and the sender are checked too, so nothing
// hangs on any one field of the update being what it usually is.
func obeyed(c *commandsRow, chat telegramChat, from *tgUser) bool {
	return chat.Type == "private" && chat.ID == c.chatID && from != nil && !from.IsBot && from.ID == chat.ID
}

func (m *Module) onUpdate(ctx context.Context, c *commandsRow, u tgUpdate) {
	b := m.bot(ctx)
	if !m.listening(ctx, c, b) {
		return
	}
	// Strangers are dropped here, before anything is written or sent: the bot's
	// username is public, and anybody messaging it must cost nothing and learn
	// nothing, not even that there is a bot behind it that answers.
	tapped := u.Callback != nil && u.Callback.Message != nil && obeyed(c, u.Callback.Message.Chat, u.Callback.From)
	said := u.Message != nil && obeyed(c, u.Message.Chat, u.Message.From)
	if !tapped && !said {
		return
	}
	first, err := m.claimUpdate(ctx, u.UpdateID)
	if err != nil {
		m.log.Error("telegram: could not record an update", "error", err)
		return
	}
	if !first {
		return
	}
	if tapped {
		m.onButton(ctx, b, c, u.Callback.From.ID, u.Callback.ID, u.Callback.Message.MessageID, u.Callback.Data)
		return
	}
	m.reply(ctx, b, m.onCommand(ctx, c, u.Message.Text))
}

// claimUpdate reports whether this update should run: Telegram has not handed
// it over before, and the store is under its rate. It is one statement, so two
// redeliveries at once cannot both win. Over the rate it records nothing, so a
// flood cannot grow the table either.
func (m *Module) claimUpdate(ctx context.Context, updateID int64) (bool, error) {
	var claimed int64
	err := m.db.QueryRowContext(ctx, `
		INSERT INTO telegram_commands_updates (update_id)
		SELECT $1 WHERE (SELECT count(*) FROM telegram_commands_updates
		                 WHERE received_at > now() - interval '1 minute') < $2
		ON CONFLICT (store_id, update_id) DO NOTHING
		RETURNING update_id`, updateID, maxUpdatesPerMinute).Scan(&claimed)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM telegram_commands_updates WHERE received_at < now() - $1::interval`, updateRetention); err != nil {
		m.log.Warn("telegram: could not prune the update log", "error", err)
	}
	return true, nil
}

type reply struct {
	text    string
	buttons *inlineKeyboard
}

func (m *Module) reply(ctx context.Context, b bot, r reply) {
	text := r.text
	if runes := []rune(text); len(runes) > maxText {
		text = string(runes[:maxText-1]) + "…"
	}
	if err := m.sendButtons(ctx, b, text, r.buttons); err != nil {
		m.logFailure(ctx, "a reply", err)
	}
}

// ------------------------------------------------------------------ the commands

func (m *Module) onCommand(ctx context.Context, c *commandsRow, text string) reply {
	fields := strings.Fields(text)
	if len(fields) == 0 || !strings.HasPrefix(fields[0], "/") {
		return reply{text: "Send a command, or /help to see them."}
	}
	name, _, _ := strings.Cut(strings.ToLower(fields[0]), "@") // /sales@my_bot
	args := fields[1:]
	switch name {
	case "/start", "/help":
		return reply{text: helpText}
	case "/sales":
		return m.salesToday(ctx)
	case "/orders":
		return m.recentOrders(ctx)
	case "/lowstock":
		return m.lowStock(ctx)
	case "/health":
		return m.health(ctx)
	case "/order":
		if len(args) != 1 {
			return reply{text: "Which order? For example: /order GC-001042"}
		}
		return m.orderDetails(ctx, args[0])
	case "/stock":
		return m.proposeStock(ctx, c, args)
	case "/price":
		return m.proposePrice(ctx, c, args)
	case "/ship":
		return m.proposeShip(ctx, c, args)
	case "/delivered":
		return m.proposeDelivered(ctx, c, args)
	}
	return reply{text: "I do not know that one. Send /help to see what I can do."}
}

// plainly turns a service's refusal into a reply. A 4xx is written for the
// person who asked, and is the same words the panel shows; anything else is
// logged and not repeated into a chat.
func (m *Module) plainly(err error) reply {
	var api *gocommerce.APIError
	if errors.As(err, &api) && api.Status < http.StatusInternalServerError {
		return reply{text: api.Message}
	}
	m.log.Error("telegram: a command failed", "error", err)
	return reply{text: "Something went wrong on the store's side. Try again, or use the panel."}
}

// findOrder takes what the owner reads off an alert: the number, in any case,
// or the id in the panel's address. Behind the store policy either way.
func (m *Module) findOrder(ctx context.Context, ref string) (*gocommerce.Order, error) {
	o, err := m.app.Order().GetByNumber(ctx, ref)
	if errors.Is(err, gocommerce.ErrNotFound) && ref != strings.ToUpper(ref) {
		o, err = m.app.Order().GetByNumber(ctx, strings.ToUpper(ref))
	}
	if id, parseErr := strconv.ParseInt(ref, 10, 64); errors.Is(err, gocommerce.ErrNotFound) && parseErr == nil {
		o, err = m.app.Order().Get(ctx, id)
	}
	if errors.Is(err, gocommerce.ErrNotFound) {
		return nil, gocommerce.NotFoundf("I cannot find order %s in this store.", ref)
	}
	return o, err
}

func (m *Module) findVariant(ctx context.Context, ref string) (*gocommerce.Variant, string, error) {
	v, err := m.app.Products().GetVariantBySKU(ctx, ref)
	if id, parseErr := strconv.ParseInt(ref, 10, 64); errors.Is(err, gocommerce.ErrNotFound) && parseErr == nil {
		v, err = m.app.Products().GetVariant(ctx, id)
	}
	if errors.Is(err, gocommerce.ErrNotFound) {
		return nil, "", gocommerce.NotFoundf("I cannot find SKU %s in this store.", ref)
	}
	if err != nil {
		return nil, "", err
	}
	name := v.SKU
	if p, err := m.app.Products().GetProduct(ctx, v.ProductID); err == nil {
		name = v.SKU + " (" + p.Title
		if v.Label != "" {
			name += " / " + v.Label
		}
		name += ")"
	}
	return v, name, nil
}

// ------------------------------------------------------------------- reading

func (m *Module) salesToday(ctx context.Context) reply {
	zone := m.app.Profile().LocationOf(ctx)
	report, err := m.app.Reports().Sales(ctx, gocommerce.SalesQuery{
		From:     gocommerce.ReportBound{Date: time.Now().In(zone).Format("2006-01-02")},
		GroupBy:  gocommerce.GrainDay,
		TimeZone: zone.String(),
	})
	if err != nil {
		return m.plainly(err)
	}
	lines := []string{"Today so far:"}
	for _, block := range report.Currencies {
		t := block.Totals
		lines = append(lines, fmt.Sprintf("Orders: %d, worth %s\nPaid %s, still to collect %s",
			t.Orders, formatMoney(t.Total.AmountMinor, block.Currency),
			formatMoney(t.Paid.Total.AmountMinor, block.Currency),
			formatMoney(t.Outstanding.Total.AmountMinor, block.Currency)))
	}
	return reply{text: strings.Join(lines, "\n")}
}

func (m *Module) recentOrders(ctx context.Context) reply {
	orders, total, err := m.app.Order().List(ctx, gocommerce.OrderQuery{Limit: listLength})
	if err != nil {
		return m.plainly(err)
	}
	if len(orders) == 0 {
		return reply{text: "No orders yet."}
	}
	lines := []string{fmt.Sprintf("Latest %d of %d:", len(orders), total)}
	for _, o := range orders {
		line := fmt.Sprintf("%s: %s, %s, payment %s", o.Number,
			formatMoney(o.Total.AmountMinor, o.Total.Currency), o.Status, o.PaymentStatus)
		if name := firstName(o.Name); name != "" {
			line += ", " + name
		}
		lines = append(lines, line)
	}
	return reply{text: strings.Join(lines, "\n")}
}

// orderDetails says what the owner's alert says and no more: the first name, no
// email, phone or address. A phone in a chat is one forward away from anywhere,
// and the link goes to the panel, behind a sign-in, for the rest.
func (m *Module) orderDetails(ctx context.Context, ref string) reply {
	o, err := m.findOrder(ctx, ref)
	if err != nil {
		return m.plainly(err)
	}
	lines := []string{fmt.Sprintf("Order %s: %s", o.Number, formatMoney(o.Total.AmountMinor, o.Total.Currency))}
	for _, l := range o.Lines {
		item := fmt.Sprintf("%d x %s", l.Quantity, l.Title)
		if l.VariantLabel != "" {
			item += " / " + l.VariantLabel
		}
		lines = append(lines, item)
	}
	if name := firstName(o.Name); name != "" {
		lines = append(lines, "Customer: "+name)
	}
	lines = append(lines, "Status: "+o.Status, fmt.Sprintf("Payment: %s, %s", o.PaymentProvider, o.PaymentStatus))
	for _, f := range o.Fulfillments {
		if f.Tracking != "" {
			lines = append(lines, "Tracking: "+f.Tracking)
		}
	}
	if link := m.orderURL(o.ID); link != "" {
		lines = append(lines, link)
	}
	return reply{text: strings.Join(lines, "\n")}
}

func (m *Module) lowStock(ctx context.Context) reply {
	variants, total, err := m.app.Stock().LowStock(ctx, gocommerce.LowStockQuery{Threshold: lowStockThreshold, Limit: 2 * listLength})
	if err != nil {
		return m.plainly(err)
	}
	if len(variants) == 0 {
		return reply{text: fmt.Sprintf("Nothing is at %d or below.", lowStockThreshold)}
	}
	lines := []string{fmt.Sprintf("%d at %d or below:", total, lowStockThreshold)}
	for _, v := range variants {
		lines = append(lines, fmt.Sprintf("%s: %d left", v.SKU, v.Available))
	}
	return reply{text: strings.Join(lines, "\n")}
}

// health leaves out each check's Cause, which names database hosts and users,
// for the reason the panel's diagnostics route does.
func (m *Module) health(ctx context.Context) reply {
	report := m.app.Diagnose(ctx)
	failed := report.Failed()
	if len(failed) == 0 {
		return reply{text: fmt.Sprintf("All %d checks pass.", len(report.Checks))}
	}
	lines := []string{fmt.Sprintf("%d of %d checks need a look:", len(failed), len(report.Checks))}
	for _, d := range failed {
		lines = append(lines, fmt.Sprintf("%s (%s): %s", d.Name, d.Status, d.Detail))
	}
	return reply{text: strings.Join(lines, "\n")}
}

// ------------------------------------------------------------------- changing

// change is one thing the chat can do to the store, as stored while it waits
// for its Confirm and as recorded in the audit.
type change struct {
	Kind       string `json:"kind"`
	OrderID    int64  `json:"order_id,omitempty"`
	Number     string `json:"order_number,omitempty"`
	Tracking   string `json:"tracking,omitempty"`
	VariantID  int64  `json:"variant_id,omitempty"`
	SKU        string `json:"sku,omitempty"`
	Set        *int   `json:"set,omitempty"`
	Adjust     *int   `json:"adjust,omitempty"`
	PriceMinor *int64 `json:"price_minor,omitempty"`
	Currency   string `json:"currency,omitempty"`
}

const (
	kindMarkPaid  = "order.mark_paid"
	kindShip      = "order.ship"
	kindCancel    = "order.cancel"
	kindDelivered = "order.deliver"
	kindStock     = "variant.stock"
	kindPrice     = "variant.price"
)

// apply does the change, through the service the panel and the agent tools use:
// the rules are the service's, and there is no second copy of them here to
// drift (D209). The context names the person, so the order's history and the
// stock ledger say Telegram rather than "system".
func (m *Module) apply(ctx context.Context, user int64, c change) (string, error) {
	ctx = gocommerce.WithActorLabel(ctx, "Telegram user "+strconv.FormatInt(user, 10))
	audit, err := m.auditStart(ctx, user, c)
	if err != nil {
		return "", gocommerce.Internalf(err, "audit the change")
	}
	text, err := m.applyChange(ctx, c)
	m.auditFinish(ctx, audit, err)
	return text, err
}

func (m *Module) applyChange(ctx context.Context, c change) (string, error) {
	switch c.Kind {
	case kindMarkPaid:
		_, err := m.app.Pay().MarkPaid(ctx, c.OrderID, "")
		return "Order " + c.Number + " is marked paid.", err
	case kindShip:
		_, err := m.app.Ship().Create(ctx, c.OrderID, "", gocommerce.ShipRequest{Tracking: c.Tracking})
		if c.Tracking == "" {
			return "Order " + c.Number + " is marked shipped, with no tracking number.", err
		}
		return "Order " + c.Number + " is shipped, tracking " + c.Tracking + ".", err
	case kindCancel:
		_, err := m.app.Order().Cancel(ctx, c.OrderID, "")
		return "Order " + c.Number + " is cancelled.", err
	case kindDelivered:
		_, err := m.app.Order().MarkDelivered(ctx, c.OrderID)
		return "Order " + c.Number + " is marked delivered.", err
	case kindStock:
		var v *gocommerce.Variant
		var err error
		if c.Set != nil {
			v, err = m.app.Stock().SetOnHand(ctx, c.VariantID, 0, *c.Set, "Changed on Telegram")
		} else if c.Adjust != nil {
			v, err = m.app.Stock().Adjust(ctx, c.VariantID, 0, *c.Adjust, "Changed on Telegram")
		} else {
			err = fmt.Errorf("a stock change with no amount")
		}
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s now has %d on hand, %d available.", c.SKU, v.StockOnHand, v.Available), nil
	case kindPrice:
		if c.PriceMinor == nil {
			return "", fmt.Errorf("a price change with no price")
		}
		_, err := m.app.Products().UpdateVariant(ctx, c.VariantID, gocommerce.VariantPatch{PriceMinor: c.PriceMinor})
		return c.SKU + " now costs " + formatMoney(*c.PriceMinor, c.Currency) + ".", err
	}
	return "", fmt.Errorf("an unknown change %q", c.Kind)
}

// propose stores the change and asks. It replaces whatever was waiting, so
// one Confirm can only ever mean the question most recently asked.
func (m *Module) propose(ctx context.Context, c *commandsRow, ch change, question string) reply {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return m.plainly(err)
	}
	token := hex.EncodeToString(raw)
	action, err := json.Marshal(ch)
	if err != nil {
		return m.plainly(err)
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO telegram_commands_pending (token, chat_id, action, expires_at)
		VALUES ($1, $2, $3, now() + make_interval(secs => $4))
		ON CONFLICT (store_id) DO UPDATE SET
		    token = excluded.token, chat_id = excluded.chat_id, action = excluded.action, expires_at = excluded.expires_at`,
		token, c.chatID, action, pendingTTL.Seconds()); err != nil {
		return m.plainly(gocommerce.Internalf(err, "store the pending change"))
	}
	return reply{text: question, buttons: &inlineKeyboard{Rows: [][]inlineButton{{
		{Text: "Confirm", Data: "y:" + token}, {Text: "Leave it", Data: "n:" + token},
	}}}}
}

// claimPending takes the waiting change, once. The DELETE is the whole of the
// safety: of any number of taps at any moment, one gets the row and the rest get
// none, whatever order they arrive in and however many servers are answering.
func (m *Module) claimPending(ctx context.Context, c *commandsRow, token string) (*change, error) {
	var raw []byte
	err := m.db.QueryRowContext(ctx, `
		DELETE FROM telegram_commands_pending
		WHERE token = $1 AND chat_id = $2 AND expires_at > now()
		RETURNING action`, token, c.chatID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ch change
	if err := json.Unmarshal(raw, &ch); err != nil {
		return nil, err
	}
	return &ch, nil
}

func (m *Module) proposeStock(ctx context.Context, c *commandsRow, args []string) reply {
	if len(args) != 2 {
		return reply{text: "For example: /stock SKU 12 to set the count, or /stock SKU +5 to add to it."}
	}
	n, err := strconv.Atoi(args[1])
	if err != nil || n > maxStockChange || n < -maxStockChange {
		return reply{text: "The amount must be a whole number, like 12, +5 or -5."}
	}
	relative := strings.HasPrefix(args[1], "+") || strings.HasPrefix(args[1], "-")
	if relative && n == 0 {
		return reply{text: "That would change nothing."}
	}
	v, name, err := m.findVariant(ctx, args[0])
	if err != nil {
		return m.plainly(err)
	}
	// Stock changes land on the store's default location, as they do from the
	// agent tools, and a store with several must be told before it confirms.
	now := fmt.Sprintf("It has %d on hand now.", v.StockOnHand)
	if places, err := m.app.Stock().ByLocation(ctx, v.ID); err != nil {
		return m.plainly(err)
	} else if len(places) > 1 {
		now = fmt.Sprintf("This changes your default location only. Across all %d locations it has %d on hand now.", len(places), v.StockOnHand)
	}
	ch := change{Kind: kindStock, VariantID: v.ID, SKU: v.SKU}
	var question string
	switch {
	case !relative:
		ch.Set = &n
		question = fmt.Sprintf("Set the count of %s to %d?", name, n)
	case n > 0:
		ch.Adjust = &n
		question = fmt.Sprintf("Add %d to %s?", n, name)
	default:
		ch.Adjust = &n
		question = fmt.Sprintf("Take %d away from %s?", -n, name)
	}
	return m.propose(ctx, c, ch, question+" "+now)
}

var amountRE = regexp.MustCompile(`^([0-9]+)(?:\.([0-9]+))?$`)

// parseAmount reads "1299.50" as minor units without going near a float (rule
// 6). More decimals than the currency has is refused, rather than rounded into
// a price nobody typed.
func parseAmount(s, currency string) (int64, bool) {
	match := amountRE.FindStringSubmatch(s)
	exp := exponent(strings.ToUpper(currency))
	if match == nil || len(match[2]) > exp {
		return 0, false
	}
	digits := match[1] + match[2] + strings.Repeat("0", exp-len(match[2]))
	minor, err := strconv.ParseInt(digits, 10, 64)
	return minor, err == nil
}

func (m *Module) proposePrice(ctx context.Context, c *commandsRow, args []string) reply {
	if len(args) != 2 {
		return reply{text: "For example: /price SKU 1299.50"}
	}
	v, name, err := m.findVariant(ctx, args[0])
	if err != nil {
		return m.plainly(err)
	}
	minor, ok := parseAmount(args[1], v.Price.Currency)
	if !ok {
		return reply{text: fmt.Sprintf("Write the price as a plain number in %s, like 1299.50, with no commas or symbol.", v.Price.Currency)}
	}
	return m.propose(ctx, c, change{
		Kind: kindPrice, VariantID: v.ID, SKU: v.SKU, PriceMinor: &minor, Currency: v.Price.Currency,
	}, fmt.Sprintf("Change the price of %s from %s to %s?", name,
		formatMoney(v.Price.AmountMinor, v.Price.Currency), formatMoney(minor, v.Price.Currency)))
}

func (m *Module) proposeShip(ctx context.Context, c *commandsRow, args []string) reply {
	if len(args) != 2 || len(args[1]) > maxTracking {
		return reply{text: "For example: /ship GC-001042 TRACKINGNUMBER"}
	}
	o, err := m.findOrder(ctx, args[0])
	if err != nil {
		return m.plainly(err)
	}
	return m.propose(ctx, c, change{Kind: kindShip, OrderID: o.ID, Number: o.Number, Tracking: args[1]},
		fmt.Sprintf("Ship order %s (%s) with tracking number %s?", o.Number, formatMoney(o.Total.AmountMinor, o.Total.Currency), args[1]))
}

func (m *Module) proposeDelivered(ctx context.Context, c *commandsRow, args []string) reply {
	if len(args) != 1 {
		return reply{text: "For example: /delivered GC-001042"}
	}
	o, err := m.findOrder(ctx, args[0])
	if err != nil {
		return m.plainly(err)
	}
	return m.propose(ctx, c, change{Kind: kindDelivered, OrderID: o.ID, Number: o.Number},
		fmt.Sprintf("Mark order %s (%s) as delivered?", o.Number, formatMoney(o.Total.AmountMinor, o.Total.Currency)))
}

// ------------------------------------------------------------------- buttons

// alertButtons is what goes under a new-order alert, and only when the store is
// listening and the alert is going to the obeyed chat: a store that has not
// switched this on gets the message it always got. Failing to read the order
// costs the buttons and never the alert.
func (m *Module) alertButtons(ctx context.Context, b bot, p pending) *inlineKeyboard {
	if p.eventName != gocommerce.EventOrderCreated || p.orderID <= 0 {
		return nil
	}
	c, err := m.commandsRow(ctx)
	if err != nil || !m.listening(ctx, c, b) {
		return nil
	}
	o, err := m.app.Order().Get(ctx, p.orderID)
	if err != nil {
		return nil
	}
	return orderButtons(o)
}

// orderButtons offers what the order's state allows, so a button that could only
// be refused is not shown. The service still decides when one is tapped.
func orderButtons(o *gocommerce.Order) *inlineKeyboard {
	id := strconv.FormatInt(o.ID, 10)
	var row []inlineButton
	if o.PaymentStatus != gocommerce.PaymentPaid && o.PaymentStatus != gocommerce.PaymentRefunded && o.Status != gocommerce.OrderCancelled {
		row = append(row, inlineButton{Text: "Mark paid", Data: "o:paid:" + id})
	}
	if o.Status == gocommerce.OrderConfirmed || o.Status == gocommerce.OrderPartial {
		row = append(row, inlineButton{Text: "Ship", Data: "o:ship:" + id})
	}
	if o.Status == gocommerce.OrderPending || o.Status == gocommerce.OrderConfirmed {
		row = append(row, inlineButton{Text: "Cancel", Data: "o:cancel:" + id})
	}
	if len(row) == 0 {
		return nil
	}
	return &inlineKeyboard{Rows: [][]inlineButton{row}}
}

// onButton handles a tap. Telegram shows a spinner on the button until it is
// answered, so that comes first, whatever the tap turns out to be.
func (m *Module) onButton(ctx context.Context, b bot, c *commandsRow, user int64, callbackID string, messageID int64, data string) {
	if err := m.call(ctx, b.token, "answerCallbackQuery", map[string]any{"callback_query_id": callbackID}, nil); err != nil {
		m.logFailure(ctx, "answering a button", err)
	}
	verb, rest, _ := strings.Cut(data, ":")
	switch verb {
	case "y":
		ch, err := m.claimPending(ctx, c, rest)
		if err != nil {
			m.reply(ctx, b, m.plainly(gocommerce.Internalf(err, "read the pending change")))
			return
		}
		m.clearButtons(ctx, b, c, messageID)
		if ch == nil {
			m.reply(ctx, b, reply{text: "That Confirm has run out or was already used, so this tap changed nothing. Send the command again."})
			return
		}
		m.applyAndSay(ctx, b, user, *ch)
	case "n":
		if _, err := m.claimPending(ctx, c, rest); err != nil {
			m.log.Error("telegram: could not drop a pending change", "error", err)
		}
		m.clearButtons(ctx, b, c, messageID)
		m.reply(ctx, b, reply{text: "Left as it was."})
	case "o":
		m.onAlertButton(ctx, b, c, user, rest)
	}
}

// onAlertButton is a tap under an alert. The owner chose one tap for Mark paid
// and Ship and a Confirm for Cancel. All three are safe to repeat because the
// services are: a paid order stays paid, a shipped one refuses, and a cancelled
// one stays cancelled, each decided under the order's own lock.
func (m *Module) onAlertButton(ctx context.Context, b bot, c *commandsRow, user int64, rest string) {
	action, idText, _ := strings.Cut(rest, ":")
	id, err := strconv.ParseInt(idText, 10, 64)
	if err != nil || id <= 0 {
		return
	}
	o, err := m.app.Order().Get(ctx, id)
	if err != nil {
		m.reply(ctx, b, m.plainly(err))
		return
	}
	ch := change{OrderID: o.ID, Number: o.Number}
	switch action {
	case "paid":
		ch.Kind = kindMarkPaid
	case "ship":
		ch.Kind = kindShip
	case "cancel":
		ch.Kind = kindCancel
		m.reply(ctx, b, m.propose(ctx, c, ch, fmt.Sprintf("Cancel order %s (%s)? Its stock goes back on the shelf.",
			o.Number, formatMoney(o.Total.AmountMinor, o.Total.Currency))))
		return
	default:
		return
	}
	m.applyAndSay(ctx, b, user, ch)
}

func (m *Module) applyAndSay(ctx context.Context, b bot, user int64, ch change) {
	text, err := m.apply(ctx, user, ch)
	if err != nil {
		m.reply(ctx, b, m.plainly(err))
		return
	}
	m.reply(ctx, b, reply{text: text})
}

// clearButtons takes Confirm off the question it answered. Only tidiness: a
// second tap is already harmless, so a failure here matters to nobody.
func (m *Module) clearButtons(ctx context.Context, b bot, c *commandsRow, messageID int64) {
	if err := m.call(ctx, b.token, "editMessageReplyMarkup", map[string]any{
		"chat_id": c.chatID, "message_id": messageID,
	}, nil); err != nil {
		m.logFailure(ctx, "clearing the buttons", err)
	}
}
