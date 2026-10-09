package cartrecovery

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Abandonment is one record as the screens read it. Money is minor units plus
// the currency, like every other figure the engine serves.
type Abandonment struct {
	ID     int64  `json:"id"`
	CartID int64  `json:"cart_id"`
	Kind   string `json:"kind"`
	Status string `json:"status"`
	// Indicator is the one secondary fact worth a word under the status —
	// "email_sent", "clicked", "no_contact" — chosen here so every screen
	// that draws a record says the same thing about it.
	Indicator    string           `json:"indicator,omitempty"`
	HoldReason   string           `json:"hold_reason,omitempty"`
	Email        string           `json:"email,omitempty"`
	ItemCount    int              `json:"item_count"`
	Subtotal     gocommerce.Money `json:"subtotal"`
	DiscountCode string           `json:"discount_code,omitempty"`
	CartCreated  time.Time        `json:"cart_created_at"`
	LastActiveAt time.Time        `json:"last_active_at"`
	AbandonedAt  time.Time        `json:"abandoned_at"`
	// History is a basket that went idle before this module was installed:
	// shown, never written to.
	History        bool       `json:"history"`
	StepsSent      int        `json:"steps_sent"`
	StepsTotal     int        `json:"steps_total"`
	MessagesSent   int        `json:"messages_sent"`
	NextStepAt     *time.Time `json:"next_step_at,omitempty"`
	LastSentAt     *time.Time `json:"last_sent_at,omitempty"`
	Clicks         int        `json:"clicks"`
	FirstClickedAt *time.Time `json:"first_clicked_at,omitempty"`

	RecoveredAt           *time.Time      `json:"recovered_at,omitempty"`
	RecoveredOrder        *RecoveredOrder `json:"recovered_order,omitempty"`
	RecoveredAfterMessage *bool           `json:"recovered_after_message,omitempty"`
	Suppression           *Suppression    `json:"suppression,omitempty"`
	ExpiredAt             *time.Time      `json:"expired_at,omitempty"`
}

// RecoveredOrder is the order a basket became.
type RecoveredOrder struct {
	ID     int64            `json:"id"`
	Number string           `json:"number"`
	Total  gocommerce.Money `json:"total"`
}

// Suppression is why an operator stopped the reminders.
type Suppression struct {
	Reason string    `json:"reason"`
	Note   string    `json:"note,omitempty"`
	At     time.Time `json:"at"`
	By     string    `json:"by,omitempty"`
}

const abandonmentColumns = `a.id, a.cart_id, a.kind, a.status, coalesce(a.hold_reason, ''),
	coalesce(a.email, ''), a.item_count, a.subtotal_minor, a.currency, coalesce(a.discount_code, ''),
	a.cart_created_at, a.last_active_at, a.abandoned_at, a.history, a.steps_sent, a.messages_sent,
	a.next_step_at, a.last_sent_at, a.clicks, a.first_clicked_at,
	a.recovered_at, a.recovered_order_id, coalesce(a.recovered_order_number, ''),
	a.recovered_total_minor, a.recovered_after_message,
	a.suppressed_at, coalesce(a.suppression_reason, ''), coalesce(a.suppression_note, ''),
	coalesce(a.suppressed_by, ''), a.expired_at`

type scanner interface{ Scan(dest ...any) error }

func scanAbandonment(row scanner) (*Abandonment, error) {
	a := &Abandonment{}
	var currency string
	var subtotal int64
	var nextStep, lastSent, firstClick, recoveredAt, suppressedAt, expiredAt sql.NullTime
	var orderID, orderTotal sql.NullInt64
	var orderNumber, reason, note, by string
	var after sql.NullBool
	if err := row.Scan(&a.ID, &a.CartID, &a.Kind, &a.Status, &a.HoldReason,
		&a.Email, &a.ItemCount, &subtotal, &currency, &a.DiscountCode,
		&a.CartCreated, &a.LastActiveAt, &a.AbandonedAt, &a.History, &a.StepsSent, &a.MessagesSent,
		&nextStep, &lastSent, &a.Clicks, &firstClick,
		&recoveredAt, &orderID, &orderNumber, &orderTotal, &after,
		&suppressedAt, &reason, &note, &by, &expiredAt); err != nil {
		return nil, err
	}
	a.Subtotal = gocommerce.Money{AmountMinor: subtotal, Currency: currency}
	a.NextStepAt = timePtr(nextStep)
	a.LastSentAt = timePtr(lastSent)
	a.FirstClickedAt = timePtr(firstClick)
	a.RecoveredAt = timePtr(recoveredAt)
	a.ExpiredAt = timePtr(expiredAt)
	if orderID.Valid {
		a.RecoveredOrder = &RecoveredOrder{ID: orderID.Int64, Number: orderNumber,
			Total: gocommerce.Money{AmountMinor: orderTotal.Int64, Currency: currency}}
	}
	if after.Valid {
		v := after.Bool
		a.RecoveredAfterMessage = &v
	}
	if suppressedAt.Valid {
		a.Suppression = &Suppression{Reason: reason, Note: note, At: suppressedAt.Time, By: by}
	}
	a.Indicator = indicatorOf(a)
	return a, nil
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// indicatorOf picks the one secondary fact, most decisive first: an order
// beats a click, a click beats a sent message, and a reason nothing was sent
// is only worth saying when nothing was.
func indicatorOf(a *Abandonment) string {
	switch {
	case a.Status == statusRecovered:
		return "purchased"
	case a.Clicks > 0:
		return "clicked"
	case a.MessagesSent > 0:
		return "email_sent"
	case a.HoldReason == holdNothingPurchasable:
		return "inventory_unavailable"
	case a.HoldReason == holdSendFailed:
		return "send_failed"
	case a.HoldReason == holdNoProvider:
		return holdNoProvider
	case a.NextStepAt != nil:
		return "scheduled"
	case a.HoldReason != "":
		return a.HoldReason
	}
	return ""
}

// filter is the WHERE every reading of the records shares, so the cards above
// the list can never count a different set from the list itself.
type filter struct {
	where []string
	args  []any
}

func (f *filter) add(clause string, v any) {
	f.args = append(f.args, v)
	f.where = append(f.where, strings.ReplaceAll(clause, "?", "$"+strconv.Itoa(len(f.args))))
}

func (f *filter) sql() string {
	if len(f.where) == 0 {
		return ""
	}
	return " WHERE " + strings.Join(f.where, " AND ")
}

var (
	allStatuses = []string{statusAbandoned, statusScheduled, statusContacted, statusRecovered, statusSuppressed, statusExpired}
	allKinds    = []string{kindCart, kindCheckout}
)

func parseFilter(r *http.Request) (*filter, error) {
	q := r.URL.Query()
	f := &filter{}
	if v := q.Get("status"); v != "" {
		list := splitList(v)
		for _, s := range list {
			if !slices.Contains(allStatuses, s) {
				return nil, gocommerce.Validationf("status must be one of %s", strings.Join(allStatuses, ", "))
			}
		}
		f.add("a.status = ANY(?::text[])", "{"+strings.Join(list, ",")+"}")
	}
	if v := q.Get("kind"); v != "" {
		list := splitList(v)
		for _, s := range list {
			if !slices.Contains(allKinds, s) {
				return nil, gocommerce.Validationf("kind must be cart or checkout")
			}
		}
		f.add("a.kind = ANY(?::text[])", "{"+strings.Join(list, ",")+"}")
	}
	if v := q.Get("from"); v != "" {
		t, err := parseTime(v, false)
		if err != nil {
			return nil, gocommerce.Validationf("from must be a date or an RFC 3339 time")
		}
		f.add("a.abandoned_at >= ?", t)
	}
	if v := q.Get("to"); v != "" {
		t, err := parseTime(v, true)
		if err != nil {
			return nil, gocommerce.Validationf("to must be a date or an RFC 3339 time")
		}
		f.add("a.abandoned_at < ?", t)
	}
	if v := strings.TrimSpace(q.Get("q")); v != "" {
		f.add("lower(a.email) LIKE ?", "%"+escapeLike(strings.ToLower(v))+"%")
	}
	for _, p := range []struct{ key, op string }{{"min_value_minor", ">="}, {"max_value_minor", "<="}} {
		if v := q.Get(p.key); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n < 0 {
				return nil, gocommerce.Validationf("%s must be a non-negative integer of minor units", p.key)
			}
			f.add("a.subtotal_minor "+p.op+" ?", n)
		}
	}
	switch q.Get("reachable") {
	case "":
	case "true":
		f.where = append(f.where, "a.email IS NOT NULL")
	case "false":
		f.where = append(f.where, "a.email IS NULL")
	default:
		return nil, gocommerce.Validationf("reachable must be true or false")
	}
	switch q.Get("channel") {
	case "":
	case gocommerce.ChannelEmail:
		f.where = append(f.where, "a.messages_sent > 0")
	default:
		return nil, gocommerce.Validationf("channel must be email")
	}
	return f, nil
}

func splitList(v string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// parseTime takes a date or a time. A bare date as an upper bound means the
// end of that day, which is what a date picker's "to" means.
func parseTime(v string, upper bool) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	t, err := time.Parse("2006-01-02", v)
	if err != nil {
		return t, err
	}
	if upper {
		t = t.AddDate(0, 0, 1)
	}
	return t, nil
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// sortOrders is the allow-list (D46): a request names a key, and the SQL that
// key means is a literal here.
var sortOrders = map[string][2]string{
	"abandoned_at":   {"a.abandoned_at ASC, a.id ASC", "a.abandoned_at DESC, a.id DESC"},
	"value":          {"a.subtotal_minor ASC, a.id ASC", "a.subtotal_minor DESC, a.id DESC"},
	"last_active_at": {"a.last_active_at ASC, a.id ASC", "a.last_active_at DESC, a.id DESC"},
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	f, err := parseFilter(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	key := r.URL.Query().Get("sort")
	if key == "" {
		key = "abandoned_at"
	}
	order, ok := sortOrders[key]
	if !ok {
		gocommerce.RespondError(w, r, gocommerce.Validationf("sort must be abandoned_at, value or last_active_at"))
		return
	}
	dir := order[1]
	switch r.URL.Query().Get("order") {
	case "", "desc":
	case "asc":
		dir = order[0]
	default:
		gocommerce.RespondError(w, r, gocommerce.Validationf("order must be asc or desc"))
		return
	}

	var total int
	if err := m.app.DB().QueryRowContext(ctx,
		`SELECT count(*) FROM cart_recovery_abandonments a`+f.sql(), f.args...).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	args := append(slices.Clone(f.args), limit, offset)
	rows, err := m.app.DB().QueryContext(ctx,
		`SELECT `+abandonmentColumns+` FROM cart_recovery_abandonments a`+f.sql()+
			fmt.Sprintf(` ORDER BY %s LIMIT $%d OFFSET $%d`, dir, len(args)-1, len(args)), args...)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()

	set, err := m.loadSettings(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out := []*Abandonment{}
	for rows.Next() {
		a, err := scanAbandonment(rows)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		a.StepsTotal = len(set.Settings.automation(a.Kind).Steps)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

// Summary is the cards above the list: the same records, counted.
type Summary struct {
	Abandoned int `json:"abandoned"`
	// Potential is the value of every basket in the set, recovered or not —
	// what was left behind, which is the figure the recovered one is read
	// against.
	Potential    gocommerce.Money `json:"potential"`
	Reachable    int              `json:"reachable"`
	MessagesSent int              `json:"messages_sent"`
	Contacted    int              `json:"contacted"`
	Clicked      int              `json:"clicked"`
	Recovered    int              `json:"recovered"`
	// RecoveredRevenue is every recovered basket's order; the after-message
	// pair is the subset a message preceded, which is what the sequence can
	// claim credit for.
	RecoveredRevenue             gocommerce.Money `json:"recovered_revenue"`
	RecoveredAfterMessage        int              `json:"recovered_after_message"`
	RecoveredAfterMessageRevenue gocommerce.Money `json:"recovered_after_message_revenue"`
	// RecoveryRateBP is recovered / abandoned in basis points: an integer, so
	// no float is ever near the money it describes.
	RecoveryRateBP int `json:"recovery_rate_bp"`
}

const summaryColumns = `count(*),
	coalesce(sum(a.subtotal_minor), 0),
	count(*) FILTER (WHERE a.email IS NOT NULL),
	coalesce(sum(a.messages_sent), 0),
	count(*) FILTER (WHERE a.messages_sent > 0),
	count(*) FILTER (WHERE a.clicks > 0),
	count(*) FILTER (WHERE a.status = 'recovered'),
	coalesce(sum(a.recovered_total_minor) FILTER (WHERE a.status = 'recovered'), 0),
	count(*) FILTER (WHERE a.status = 'recovered' AND a.recovered_after_message),
	coalesce(sum(a.recovered_total_minor) FILTER (WHERE a.status = 'recovered' AND a.recovered_after_message), 0)`

func scanSummary(row scanner, currency string) (*Summary, error) {
	s := &Summary{}
	var potential, recovered, recoveredAfter int64
	if err := row.Scan(&s.Abandoned, &potential, &s.Reachable, &s.MessagesSent, &s.Contacted,
		&s.Clicked, &s.Recovered, &recovered, &s.RecoveredAfterMessage, &recoveredAfter); err != nil {
		return nil, err
	}
	s.Potential = gocommerce.Money{AmountMinor: potential, Currency: currency}
	s.RecoveredRevenue = gocommerce.Money{AmountMinor: recovered, Currency: currency}
	s.RecoveredAfterMessageRevenue = gocommerce.Money{AmountMinor: recoveredAfter, Currency: currency}
	if s.Abandoned > 0 {
		s.RecoveryRateBP = s.Recovered * 10000 / s.Abandoned
	}
	return s, nil
}

func (m *Module) currency() string { return m.app.Config().Currency }

func (m *Module) handleSummary(w http.ResponseWriter, r *http.Request) {
	f, err := parseFilter(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	s, err := scanSummary(m.app.DB().QueryRowContext(r.Context(),
		`SELECT `+summaryColumns+` FROM cart_recovery_abandonments a`+f.sql(), f.args...), m.currency())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, s)
}

// Analytics is the measuring screen: the funnel, the trend and the split.
type Analytics struct {
	Summary   *Summary      `json:"summary"`
	Funnel    []FunnelStep  `json:"funnel"`
	Trend     []TrendPoint  `json:"trend"`
	Breakdown []BreakdownBy `json:"breakdown"`
	GroupBy   string        `json:"group_by"`
	TZ        string        `json:"tz"`
}

// FunnelStep is one stage. Each is a subset of the one before, which is what
// makes a funnel readable: recovered here is recovered after a message, and
// a shopper who came back on their own is in the summary but not in the
// funnel the messages are judged by.
type FunnelStep struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Count int    `json:"count"`
}

// TrendPoint is one period, by the day a basket was abandoned.
type TrendPoint struct {
	Period           string           `json:"period"`
	Abandoned        int              `json:"abandoned"`
	Potential        gocommerce.Money `json:"potential"`
	Recovered        int              `json:"recovered"`
	RecoveredRevenue gocommerce.Money `json:"recovered_revenue"`
}

// BreakdownBy is one row of the split by kind.
type BreakdownBy struct {
	Key              string           `json:"key"`
	Abandoned        int              `json:"abandoned"`
	Recovered        int              `json:"recovered"`
	RecoveredRevenue gocommerce.Money `json:"recovered_revenue"`
	RecoveryRateBP   int              `json:"recovery_rate_bp"`
}

func (m *Module) handleAnalytics(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	f, err := parseFilter(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	groupBy := r.URL.Query().Get("group_by")
	if groupBy == "" {
		groupBy = "day"
	}
	if groupBy != "day" && groupBy != "week" && groupBy != "month" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("group_by must be day, week or month"))
		return
	}
	tz := r.URL.Query().Get("tz")
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("tz must be an IANA time zone such as Asia/Kolkata"))
		return
	}
	cur := m.currency()

	s, err := scanSummary(m.app.DB().QueryRowContext(ctx,
		`SELECT `+summaryColumns+` FROM cart_recovery_abandonments a`+f.sql(), f.args...), cur)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	out := &Analytics{Summary: s, GroupBy: groupBy, TZ: tz,
		Funnel: []FunnelStep{
			{Key: "abandoned", Label: "Abandoned", Count: s.Abandoned},
			{Key: "recoverable", Label: "Recoverable", Count: s.Reachable},
			{Key: "contacted", Label: "Recovery messages sent", Count: s.Contacted},
			{Key: "clicked", Label: "Recovery links clicked", Count: s.Clicked},
			{Key: "recovered", Label: "Recovered orders", Count: s.RecoveredAfterMessage},
		}}

	// The bucket is truncated in the store's zone and printed as a date, so a
	// basket left at 23:30 in Kolkata is counted on that Kolkata day.
	args := append(slices.Clone(f.args), groupBy, tz)
	g, z := len(args)-1, len(args)
	rows, err := m.app.DB().QueryContext(ctx, fmt.Sprintf(`
		SELECT to_char(date_trunc($%d, a.abandoned_at AT TIME ZONE $%d), 'YYYY-MM-DD') AS period,
		       count(*), coalesce(sum(a.subtotal_minor), 0),
		       count(*) FILTER (WHERE a.status = 'recovered'),
		       coalesce(sum(a.recovered_total_minor) FILTER (WHERE a.status = 'recovered'), 0)
		FROM cart_recovery_abandonments a`+f.sql()+`
		GROUP BY 1 ORDER BY 1`, g, z), args...)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()
	out.Trend = []TrendPoint{}
	for rows.Next() {
		var p TrendPoint
		var potential, recovered int64
		if err := rows.Scan(&p.Period, &p.Abandoned, &potential, &p.Recovered, &recovered); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		p.Potential = gocommerce.Money{AmountMinor: potential, Currency: cur}
		p.RecoveredRevenue = gocommerce.Money{AmountMinor: recovered, Currency: cur}
		out.Trend = append(out.Trend, p)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	krows, err := m.app.DB().QueryContext(ctx, `
		SELECT a.kind, count(*), count(*) FILTER (WHERE a.status = 'recovered'),
		       coalesce(sum(a.recovered_total_minor) FILTER (WHERE a.status = 'recovered'), 0)
		FROM cart_recovery_abandonments a`+f.sql()+`
		GROUP BY a.kind ORDER BY a.kind`, f.args...)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer krows.Close()
	out.Breakdown = []BreakdownBy{}
	for krows.Next() {
		var b BreakdownBy
		var recovered int64
		if err := krows.Scan(&b.Key, &b.Abandoned, &b.Recovered, &recovered); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		b.RecoveredRevenue = gocommerce.Money{AmountMinor: recovered, Currency: cur}
		if b.Abandoned > 0 {
			b.RecoveryRateBP = b.Recovered * 10000 / b.Abandoned
		}
		out.Breakdown = append(out.Breakdown, b)
	}
	if err := krows.Err(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

// Detail is one record with everything the detail screen draws, so the screen
// reconstructs nothing.
type Detail struct {
	*Abandonment
	Lines    []DetailLine    `json:"lines"`
	Cart     *CartState      `json:"cart"`
	Customer *Customer       `json:"customer,omitempty"`
	Timeline []TimelineEvent `json:"timeline"`
	Recovery RecoveryOptions `json:"recovery"`
}

// DetailLine sets one line as it was beside the line as it is.
type DetailLine struct {
	VariantID    int64            `json:"variant_id"`
	ProductID    int64            `json:"product_id"`
	SKU          string           `json:"sku"`
	Title        string           `json:"title"`
	VariantLabel string           `json:"variant_label,omitempty"`
	Quantity     int              `json:"quantity"`
	UnitPrice    gocommerce.Money `json:"unit_price"`
	Total        gocommerce.Money `json:"total"`
	// CurrentPrice is nil when the line is no longer in the basket — its
	// variant was deleted, which cascades the cart line away.
	CurrentPrice *gocommerce.Money `json:"current_price,omitempty"`
	// Available is -1 when the variant does not track stock.
	Available    *int `json:"available,omitempty"`
	PriceChanged bool `json:"price_changed"`
	// Stock is in_stock, insufficient, out_of_stock or unavailable.
	Stock string `json:"stock"`
}

// CartState is the basket now.
type CartState struct {
	Exists    bool       `json:"exists"`
	Status    string     `json:"status,omitempty"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Customer is what the store already knows about the address on the basket,
// from its orders — the engine has no customer record (D22), so this is a
// reading of orders by email and says so.
type Customer struct {
	Email           string           `json:"email"`
	Orders          int              `json:"orders"`
	LifetimeValue   gocommerce.Money `json:"lifetime_value"`
	LastOrderAt     *time.Time       `json:"last_order_at,omitempty"`
	LastOrderNumber string           `json:"last_order_number,omitempty"`
	LastOrderID     int64            `json:"last_order_id,omitempty"`
}

// TimelineEvent is one line of the story, with a sentence ready to show.
type TimelineEvent struct {
	At     time.Time      `json:"at"`
	Kind   string         `json:"kind"`
	Label  string         `json:"label"`
	Actor  string         `json:"actor"`
	Detail map[string]any `json:"detail,omitempty"`
}

// RecoveryOptions says what the actions on the detail screen may do, decided
// here so the panel decides nothing.
type RecoveryOptions struct {
	CanSend           bool               `json:"can_send"`
	SendBlockedReason string             `json:"send_blocked_reason,omitempty"`
	CanCopyLink       bool               `json:"can_copy_link"`
	LinkBlockedReason string             `json:"link_blocked_reason,omitempty"`
	CanSuppress       bool               `json:"can_suppress"`
	Templates         []TemplateOption   `json:"templates"`
	DefaultTemplate   string             `json:"default_template"`
	Channels          []RecoveryChannel  `json:"channels"`
	SuppressReasons   []SuppressionLabel `json:"suppress_reasons"`
}

// TemplateOption is one message a step or a manual send may use.
type TemplateOption struct {
	Event      string `json:"event"`
	Title      string `json:"title"`
	Customized bool   `json:"customized"`
}

// RecoveryChannel is a way to reach the shopper, and whether it can be used.
type RecoveryChannel struct {
	Key       string `json:"key"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}

// SuppressionLabel is a reason as the screen words it.
type SuppressionLabel struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

var suppressReasons = []SuppressionLabel{
	{"contacted_manually", "Customer contacted manually"},
	{"requested_no_contact", "Customer requested no further contact"},
	{"invalid_customer", "Invalid customer"},
	{"fraud_or_test", "Fraud or test order"},
	{"other", "Other"},
}

func suppressLabel(key string) string {
	for _, r := range suppressReasons {
		if r.Key == key {
			return r.Label
		}
	}
	return key
}

func idParam(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		return 0, gocommerce.Validationf("id must be a positive integer")
	}
	return id, nil
}

func (m *Module) get(ctx context.Context, id int64) (*Abandonment, error) {
	a, err := scanAbandonment(m.app.DB().QueryRowContext(ctx,
		`SELECT `+abandonmentColumns+` FROM cart_recovery_abandonments a WHERE a.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("abandonment %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	set, err := m.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	a.StepsTotal = len(set.Settings.automation(a.Kind).Steps)
	return a, nil
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := idParam(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	d, err := m.detail(ctx, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, d)
}

func (m *Module) detail(ctx context.Context, id int64) (*Detail, error) {
	a, err := m.get(ctx, id)
	if err != nil {
		return nil, err
	}
	set, err := m.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	var raw []byte
	if err := m.app.DB().QueryRowContext(ctx,
		`SELECT lines FROM cart_recovery_abandonments WHERE id = $1`, id).Scan(&raw); err != nil {
		return nil, err
	}
	var snap []snapLine
	if err := json.Unmarshal(raw, &snap); err != nil {
		return nil, err
	}
	cur, err := m.inspect(ctx, a.CartID)
	if err != nil {
		return nil, err
	}

	d := &Detail{Abandonment: a, Cart: &CartState{Exists: !cur.gone}}
	now := map[int64]gocommerce.CartLine{}
	if !cur.gone {
		d.Cart.Status = cur.detail.State
		u, e := cur.detail.UpdatedAt, cur.detail.ExpiresAt
		d.Cart.UpdatedAt, d.Cart.ExpiresAt = &u, &e
		for _, l := range cur.detail.Lines {
			now[l.VariantID] = l
		}
	}
	d.Lines = make([]DetailLine, 0, len(snap))
	for _, s := range snap {
		line := DetailLine{
			VariantID: s.VariantID, ProductID: s.ProductID, SKU: s.SKU, Title: s.Title,
			VariantLabel: s.VariantLabel, Quantity: s.Quantity,
			UnitPrice: gocommerce.Money{AmountMinor: s.UnitPriceMinor, Currency: a.Subtotal.Currency},
			Total:     gocommerce.Money{AmountMinor: s.TotalMinor, Currency: a.Subtotal.Currency},
			Stock:     "unavailable",
		}
		if l, ok := now[s.VariantID]; ok {
			p := l.CurrentPrice
			avail := l.Available
			line.CurrentPrice, line.Available = &p, &avail
			line.PriceChanged = p.AmountMinor != s.UnitPriceMinor
			switch {
			case l.InStock:
				line.Stock = "in_stock"
			case avail > 0:
				line.Stock = "insufficient"
			default:
				line.Stock = "out_of_stock"
			}
		}
		d.Lines = append(d.Lines, line)
	}

	if a.Email != "" {
		if d.Customer, err = m.customer(ctx, a.Email); err != nil {
			return nil, err
		}
	}
	if d.Timeline, err = m.timeline(ctx, a); err != nil {
		return nil, err
	}
	d.Recovery = m.options(ctx, set, a, cur)
	return d, nil
}

func (m *Module) customer(ctx context.Context, email string) (*Customer, error) {
	c := &Customer{Email: email}
	var ltv int64
	var last sql.NullTime
	if err := m.app.DB().QueryRowContext(ctx, `
		SELECT count(*), coalesce(sum(total_minor), 0), max(created_at)
		FROM orders WHERE lower(email) = lower($1) AND status <> 'cancelled'`, email).
		Scan(&c.Orders, &ltv, &last); err != nil {
		return nil, err
	}
	c.LifetimeValue = gocommerce.Money{AmountMinor: ltv, Currency: m.currency()}
	c.LastOrderAt = timePtr(last)
	if c.Orders > 0 {
		if err := m.app.DB().QueryRowContext(ctx, `
			SELECT id, number FROM orders
			WHERE lower(email) = lower($1) AND status <> 'cancelled'
			ORDER BY created_at DESC, id DESC LIMIT 1`, email).
			Scan(&c.LastOrderID, &c.LastOrderNumber); err != nil {
			return nil, err
		}
	}
	return c, nil
}

func (m *Module) timeline(ctx context.Context, a *Abandonment) ([]TimelineEvent, error) {
	out := []TimelineEvent{
		{At: a.CartCreated, Kind: "cart_created", Label: "Basket opened", Actor: actorShopper},
		{At: a.LastActiveAt, Kind: "last_activity", Label: "Last activity in the basket", Actor: actorShopper},
	}
	rows, err := m.app.DB().QueryContext(ctx, `
		SELECT at, kind, detail, actor FROM cart_recovery_events
		WHERE abandonment_id = $1 ORDER BY at, id`, a.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e TimelineEvent
		var raw []byte
		if err := rows.Scan(&e.At, &e.Kind, &raw, &e.Actor); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &e.Detail); err != nil {
			return nil, err
		}
		e.Label = labelOf(e.Kind, e.Detail, a.Subtotal.Currency)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Stable: the two synthesized entries can share a timestamp with stored
	// ones, and the stored order is the order things happened.
	slices.SortStableFunc(out, func(x, y TimelineEvent) int { return x.At.Compare(y.At) })
	return out, nil
}

var holdLabels = map[string]string{
	holdNoContact:          "there is no email address to write to",
	holdAutomationOff:      "the automation is switched off",
	holdNoSteps:            "the automation has no steps",
	holdBeforeInstall:      "it went idle before recovery was set up",
	holdNothingPurchasable: "nothing in the basket can be bought right now",
	holdNoProvider:         "no email provider is set up",
	holdSendFailed:         "sending kept failing",
	holdSequenceDone:       "the sequence is finished",
}

func labelOf(kind string, d map[string]any, currency string) string {
	num := func(k string) string {
		if v, ok := d[k].(float64); ok {
			return strconv.FormatInt(int64(v), 10)
		}
		return ""
	}
	str := func(k string) string { s, _ := d[k].(string); return s }
	switch kind {
	case "abandoned":
		s := "Marked abandoned after " + humanMinutes(d["idle_minutes"]) + " without activity"
		if h := holdLabels[str("hold_reason")]; h != "" && str("hold_reason") != holdSequenceDone {
			s += " — not chased: " + h
		}
		return s
	case "scheduled":
		return "Recovery email #" + num("step") + " scheduled to go out " + humanMinutes(d["wait_minutes"]) + " later"
	case "sent":
		if manual, _ := d["manual"].(bool); manual {
			return "Recovery email sent by hand to " + str("to")
		}
		return "Recovery email #" + num("step") + " sent to " + str("to")
	case "send_failed":
		return "Recovery email #" + num("step") + " failed: " + str("error")
	case "skipped":
		return "Recovery email #" + num("step") + " not sent: " + holdLabels[str("reason")]
	case "link_copied":
		return "Recovery link copied"
	case "clicked":
		return "Recovery link opened"
	case "resumed":
		return "Shopper came back to the basket"
	case "recovered":
		s := "Order " + str("order_number") + " placed — recovered"
		if after, _ := d["after_message"].(bool); !after {
			s += " without a message"
		}
		return s
	case "suppressed":
		return "Recovery suppressed: " + suppressLabel(str("reason"))
	case "expired":
		return "Basket deleted — expired"
	}
	return kind
}

func (m *Module) templateOptions(ctx context.Context) []TemplateOption {
	out := make([]TemplateOption, 0, len(templateEvents))
	list, err := m.app.NotifyTemplates().List(ctx, gocommerce.ChannelEmail)
	byEvent := map[string]gocommerce.NotifyTemplate{}
	if err == nil {
		for _, t := range list {
			byEvent[t.Event] = t
		}
	}
	for _, ev := range templateEvents {
		t := byEvent[ev]
		title := t.Title
		if title == "" {
			title = ev
		}
		out = append(out, TemplateOption{Event: ev, Title: title, Customized: t.Customized})
	}
	return out
}

func (m *Module) options(ctx context.Context, set *settingsRow, a *Abandonment, cur *cartNow) RecoveryOptions {
	o := RecoveryOptions{
		Templates:       m.templateOptions(ctx),
		DefaultTemplate: templateFirst,
		SuppressReasons: suppressReasons,
		Channels: []RecoveryChannel{
			{Key: gocommerce.ChannelEmail, Label: "Email", Available: true},
			{Key: gocommerce.ChannelSMS, Label: "SMS", Reason: "A basket carries no phone number"},
		},
	}
	steps := set.Settings.automation(a.Kind).Steps
	if a.StepsSent < len(steps) {
		o.DefaultTemplate = steps[a.StepsSent].Template
	} else if len(steps) > 0 {
		o.DefaultTemplate = steps[len(steps)-1].Template
	}
	if err := m.sendable(a, cur); err != nil {
		o.SendBlockedReason = err.Message
	} else {
		o.CanSend = true
	}
	if err := m.linkable(set, a, cur); err != nil {
		o.LinkBlockedReason = err.Message
	} else {
		o.CanCopyLink = true
	}
	o.CanSuppress = a.Status != statusRecovered && a.Status != statusSuppressed && a.Status != statusExpired
	return o
}

// conflict is a 409 the panel can branch on: details.reason is a fixed word,
// and the message is the sentence to show.
func conflict(reason, message string, extra map[string]any) *gocommerce.APIError {
	d := map[string]any{"reason": reason}
	for k, v := range extra {
		d[k] = v
	}
	return &gocommerce.APIError{Status: http.StatusConflict, Code: "conflict", Message: message, Details: d}
}

// terminal refuses an action on a record that has finished, for whichever
// reason it finished.
func terminal(a *Abandonment, cur *cartNow) *gocommerce.APIError {
	switch {
	case a.Status == statusRecovered:
		msg := "This checkout has already been recovered"
		extra := map[string]any{}
		if a.RecoveredOrder != nil {
			msg += " through order " + a.RecoveredOrder.Number
			extra["order_number"] = a.RecoveredOrder.Number
			extra["order_id"] = a.RecoveredOrder.ID
		}
		return conflict("already_recovered", msg+".", extra)
	case cur != nil && !cur.gone && cur.status == gocommerce.CartConverted:
		return conflict("already_recovered", "This basket has just become an order.", nil)
	case a.Status == statusExpired || (cur != nil && cur.gone):
		return conflict("expired", "This basket no longer exists.", nil)
	}
	return nil
}

func (m *Module) sendable(a *Abandonment, cur *cartNow) *gocommerce.APIError {
	if err := terminal(a, cur); err != nil {
		return err
	}
	switch {
	case a.Status == statusSuppressed:
		return conflict("suppressed",
			"Recovery is suppressed for this basket: "+suppressLabel(a.Suppression.Reason)+".", nil)
	case contactOf(cur.detail) == "":
		return conflict("no_contact", "There is no email address to send to.", nil)
	case len(cur.detail.Lines) == 0 || !purchasable(cur.detail):
		return conflict("nothing_purchasable", "Nothing in this basket can be bought right now.", nil)
	case !m.emailDelivers():
		return conflict("no_email_provider",
			"No email provider is set up. Add one under Notifications › Setup Email.", nil)
	}
	return nil
}

func (m *Module) linkable(set *settingsRow, a *Abandonment, cur *cartNow) *gocommerce.APIError {
	if err := terminal(a, cur); err != nil {
		return err
	}
	if m.storefront(set.Settings) == "" {
		return conflict("no_storefront",
			"Set the storefront address on the recovery automation first, so the link has somewhere to land.", nil)
	}
	return nil
}

// SendRequest is a manual send.
type SendRequest struct {
	Channel  string `json:"channel"`
	Template string `json:"template"`
}

// handleSend writes to the shopper now. Every rule is re-checked here, at the
// moment of the request, because the screen it was pressed on may be minutes
// old — the shopper may have bought in between, and saying so beats sending.
func (m *Module) handleSend(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := idParam(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var req SendRequest
	if err := gocommerce.DecodeJSON(w, r, &req); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if req.Channel == "" {
		req.Channel = gocommerce.ChannelEmail
	}
	if req.Channel != gocommerce.ChannelEmail {
		gocommerce.RespondError(w, r, gocommerce.Validationf("channel must be email: a basket carries no phone number"))
		return
	}
	if req.Template == "" {
		req.Template = templateFirst
	}
	if !slices.Contains(templateEvents, req.Template) {
		gocommerce.RespondError(w, r, gocommerce.Validationf("unknown template %q", req.Template))
		return
	}

	a, err := m.get(ctx, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	cur, err := m.inspect(ctx, a.CartID)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if apiErr := m.sendable(a, cur); apiErr != nil {
		gocommerce.RespondError(w, r, apiErr)
		return
	}
	set, err := m.loadSettings(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var linkToken string
	if err := m.app.DB().QueryRowContext(ctx,
		`SELECT link_token FROM cart_recovery_abandonments WHERE id = $1`, id).Scan(&linkToken); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	to := contactOf(cur.detail)
	// No transaction is open across the send (rule 5): the record is written
	// after the provider has answered.
	if err := m.app.Notify(ctx, gocommerce.Notification{
		Event: req.Template, Channel: gocommerce.ChannelEmail, To: to,
		Data: m.data(set.Settings, cur, to, linkToken, a.StepsSent+1),
	}); err != nil {
		gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusBadGateway,
			Code: "send_failed", Message: "The email provider refused the message: " + err.Error()})
		return
	}

	tx, err := m.app.DB().BeginTx(ctx, nil)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer tx.Rollback()
	// A manual send does not move the sequence: the operator wrote one message,
	// and the automation's next reminder is still its own to send.
	if _, err := tx.ExecContext(ctx, `
		UPDATE cart_recovery_abandonments
		SET messages_sent = messages_sent + 1, last_sent_at = now(),
		    status = CASE WHEN status IN ('abandoned', 'scheduled') THEN 'contacted' ELSE status END,
		    updated_at = now()
		WHERE id = $1`, id); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := addEvent(ctx, tx, id, "sent", map[string]any{
		"template": req.Template, "channel": gocommerce.ChannelEmail, "to": to, "manual": true,
	}, actorOf(ctx)); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	d, err := m.detail(ctx, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{
		"sent_to": to, "sent_at": time.Now().UTC(), "abandonment": d,
	})
}

// handleLink hands an operator the basket's recovery link — to paste into a
// chat with the shopper. It is a POST, and recorded, because the link opens
// the basket for whoever holds it, exactly as the cart token does.
func (m *Module) handleLink(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := idParam(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	a, err := m.get(ctx, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	cur, err := m.inspect(ctx, a.CartID)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	set, err := m.loadSettings(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if apiErr := m.linkable(set, a, cur); apiErr != nil {
		gocommerce.RespondError(w, r, apiErr)
		return
	}
	var linkToken string
	if err := m.app.DB().QueryRowContext(ctx,
		`SELECT link_token FROM cart_recovery_abandonments WHERE id = $1`, id).Scan(&linkToken); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := addEvent(ctx, m.app.DB(), id, "link_copied", map[string]any{}, actorOf(ctx)); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{
		"url":     m.link(set.Settings, linkToken, cur.token),
		"tracked": m.app.Config().PanelURL != "",
	})
}

// SuppressRequest stops the reminders for one basket.
type SuppressRequest struct {
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

func (m *Module) handleSuppress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := idParam(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var req SuppressRequest
	if err := gocommerce.DecodeJSON(w, r, &req); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if !slices.ContainsFunc(suppressReasons, func(s SuppressionLabel) bool { return s.Key == req.Reason }) {
		gocommerce.RespondError(w, r, gocommerce.Validationf(
			"reason must be contacted_manually, requested_no_contact, invalid_customer, fraud_or_test or other"))
		return
	}
	req.Note = strings.TrimSpace(req.Note)
	if req.Reason == "other" && req.Note == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("say why in the note when the reason is other"))
		return
	}
	if len(req.Note) > 500 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("note must be at most 500 characters"))
		return
	}

	a, err := m.get(ctx, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if apiErr := terminal(a, nil); apiErr != nil {
		gocommerce.RespondError(w, r, apiErr)
		return
	}
	if a.Status == statusSuppressed {
		// Already done: answering with the record makes a double click
		// harmless rather than an error.
		d, err := m.detail(ctx, id)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		gocommerce.Respond(w, http.StatusOK, d)
		return
	}

	by := actorOf(ctx)
	tx, err := m.app.DB().BeginTx(ctx, nil)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
		UPDATE cart_recovery_abandonments
		SET status = 'suppressed', suppressed_at = now(), suppression_reason = $2,
		    suppression_note = $3, suppressed_by = $4,
		    next_step_at = NULL, claimed_until = NULL, updated_at = now()
		WHERE id = $1 AND status IN ('abandoned', 'scheduled', 'contacted')`,
		id, req.Reason, nullString(req.Note), by)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// It finished between the read and the write — most likely an order.
		a, err := m.get(ctx, id)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		if apiErr := terminal(a, nil); apiErr != nil {
			gocommerce.RespondError(w, r, apiErr)
			return
		}
		gocommerce.RespondError(w, r, gocommerce.Conflictf("this basket changed while you were suppressing it; reload and try again"))
		return
	}
	if err := addEvent(ctx, tx, id, "suppressed", map[string]any{
		"reason": req.Reason, "note": req.Note,
	}, by); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := tx.Commit(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	d, err := m.detail(ctx, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, d)
}

// SettingsView is the automation screen's reading.
type SettingsView struct {
	Settings   Settings `json:"settings"`
	Customized bool     `json:"customized"`
	// Defaults are what Reset puts back.
	Defaults    Settings         `json:"defaults"`
	InstalledAt time.Time        `json:"installed_at"`
	UpdatedAt   *time.Time       `json:"updated_at,omitempty"`
	UpdatedBy   string           `json:"updated_by,omitempty"`
	Actions     []Action         `json:"actions"`
	Templates   []TemplateOption `json:"templates"`
	// StorefrontFallback is Config.StorefrontURL — what applies when the
	// screen leaves the address empty.
	StorefrontFallback string `json:"storefront_fallback,omitempty"`
	// EmailDelivers is false when no email provider is set up, which is the
	// one thing that makes every step on this screen a no-op.
	EmailDelivers bool `json:"email_delivers"`
	// Tracked says whether a link can count its clicks, which needs the
	// engine's own public address (Config.PanelURL).
	Tracked bool `json:"tracked"`
	// Guards are the rules every send re-checks, in words, for the screen to
	// show as information. The rules themselves are in sendStep.
	Guards []string `json:"guards"`
}

var guards = []string{
	"The basket has an email address",
	"The basket has not become an order",
	"The basket still exists and has something in it",
	"At least one item can still be bought",
	"The shopper has not been in the basket within the abandonment window",
	"Recovery has not been suppressed for this basket",
	"An email provider is set up",
}

func (m *Module) settingsView(ctx context.Context) (*SettingsView, error) {
	set, err := m.loadSettings(ctx)
	if err != nil {
		return nil, err
	}
	return &SettingsView{
		Settings: set.Settings, Customized: set.Customized, Defaults: DefaultSettings(),
		InstalledAt: set.InstalledAt, UpdatedAt: set.UpdatedAt, UpdatedBy: set.UpdatedBy,
		Actions: actions, Templates: m.templateOptions(ctx),
		StorefrontFallback: m.cfg.StorefrontURL, EmailDelivers: m.emailDelivers(),
		Tracked: m.app.Config().PanelURL != "", Guards: guards,
	}, nil
}

func (m *Module) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	v, err := m.settingsView(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, v)
}

func (m *Module) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var s Settings
	if err := gocommerce.DecodeJSON(w, r, &s); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := validate(&s); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := m.saveSettings(ctx, s, actorOf(ctx)); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	v, err := m.settingsView(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, v)
}

// handleClick is where a message's link lands: count it, then send the
// shopper to their basket. Public, because it is opened from an inbox; it
// reveals nothing a recipient of the email does not already hold.
func (m *Module) handleClick(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tok := r.PathValue("token")
	var id int64
	var status string
	var cartToken, cartStatus sql.NullString
	err := m.app.DB().QueryRowContext(ctx, `
		SELECT a.id, a.status, c.token, c.status
		FROM cart_recovery_abandonments a
		LEFT JOIN carts c ON c.id = a.cart_id
		WHERE a.link_token = $1`, tok).Scan(&id, &status, &cartToken, &cartStatus)
	if errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("this link is not valid"))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	set, err := m.loadSettings(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	storefront := m.storefront(set.Settings)
	if storefront == "" {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("this store has no storefront address set"))
		return
	}

	live := cartToken.Valid && (cartStatus.String == gocommerce.CartOpen || cartStatus.String == gocommerce.CartAbandoned)
	if status != statusRecovered && status != statusExpired {
		tx, err := m.app.DB().BeginTx(ctx, nil)
		if err == nil {
			_, err = tx.ExecContext(ctx, `
				UPDATE cart_recovery_abandonments
				SET clicks = clicks + 1, first_clicked_at = coalesce(first_clicked_at, now()), updated_at = now()
				WHERE id = $1`, id)
			if err == nil {
				err = addEvent(ctx, tx, id, "clicked", map[string]any{}, actorShopper)
			}
			if err == nil {
				err = tx.Commit()
			} else {
				tx.Rollback()
			}
		}
		if err != nil {
			// The shopper still gets their basket; a lost count is the cheaper
			// failure.
			m.log.Error("cart-recovery: record click", "abandonment", id, "error", err)
		}
	}

	target := storefront + "/"
	if live {
		target = storefront + "/cart/" + cartToken.String
	}
	w.Header().Set("Cache-Control", "no-store")
	// The basket's address carries its token; it should not ride on to
	// whatever the storefront links to next.
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusFound)
}

// humanMinutes words a JSON number of minutes the way a person would say it:
// "45 minutes", "3 hours", "2 days".
func humanMinutes(v any) string {
	f, _ := v.(float64)
	n := int(f)
	unit, count := "minute", n
	switch {
	case n >= 2*24*60:
		unit, count = "day", n/(24*60)
	case n >= 120, n >= 60 && n%60 == 0:
		unit, count = "hour", n/60
	}
	if count == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(count) + " " + unit + "s"
}
