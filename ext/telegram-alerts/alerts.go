package telegramalerts

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// alertDef is one event the owner can be told about, with the wording it has
// until the owner writes their own.
type alertDef struct {
	Event       string
	Title       string
	Description string
	Template    string
	// Variables is what the editor offers. Documentation only: a template may
	// name any key the data carries.
	Variables []string
}

// What every order alert knows. The engine's own keys (OrderNotificationData)
// first, then the ones only a message to a person needs: the total written
// for reading, a first name to greet, a link into the panel.
var orderVariables = []string{
	"order_number", "total", "customer_first_name", "customer_name", "customer_email", "customer_phone",
	"items_summary", "item_count", "payment_method", "payment_status", "order_status",
	"order_url", "store_name", "total_minor", "currency", "order_id",
}

func withVariables(extra ...string) []string {
	return append(append([]string{}, orderVariables...), extra...)
}

// The defaults are plain text, one fact to a line, short enough to read on a
// lock screen. {{if}} around every key an event may lack, so a store with no
// panel URL gets no dangling "Open:".
var catalogue = []alertDef{
	{
		Event: gocommerce.EventOrderCreated, Title: "New order",
		Description: "When a shopper places an order, paid or not yet.",
		Variables:   orderVariables,
		Template: `New order {{.order_number}}: {{.total}}
{{.items_summary}}{{if .customer_first_name}}
Customer: {{.customer_first_name}}{{end}}
Payment: {{.payment_method}}, {{.payment_status}}{{if .order_url}}
{{.order_url}}{{end}}`,
	},
	{
		Event: gocommerce.EventOrderPaid, Title: "Payment received",
		Description: "When an order's payment is confirmed.",
		Variables:   orderVariables,
		Template: `Paid: order {{.order_number}}, {{.total}}{{if .customer_first_name}}, from {{.customer_first_name}}{{end}}{{if .order_url}}
{{.order_url}}{{end}}`,
	},
	{
		Event: gocommerce.EventOrderCancelled, Title: "Order cancelled",
		Description: "When an order is cancelled, by the team or because it was not paid in time.",
		Variables:   withVariables("reason"),
		Template: `Cancelled: order {{.order_number}}, {{.total}}{{if .reason}}
Reason: {{.reason}}{{end}}{{if .order_url}}
{{.order_url}}{{end}}`,
	},
	{
		Event: gocommerce.EventOrderReturned, Title: "Return received",
		Description: "When goods come back from an order. Money is a separate decision, made on the order.",
		Variables:   withVariables("return_units", "return_value", "reason"),
		Template: `Returned: {{.return_units}} item(s) from order {{.order_number}}, worth {{.return_value}}{{if .reason}}
Reason: {{.reason}}{{end}}{{if .order_url}}
{{.order_url}}{{end}}`,
	},
}

func definition(event string) (alertDef, bool) {
	for _, def := range catalogue {
		if def.Event == event {
			return def, true
		}
	}
	return alertDef{}, false
}

func eventNames() string {
	names := make([]string, len(catalogue))
	for i, def := range catalogue {
		names[i] = def.Event
	}
	return strings.Join(names, ", ")
}

// Alert is one event's alert as it stands: the owner's switch and wording, or
// the defaults where they have not changed them.
type Alert struct {
	Event       string `json:"event"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Template    string `json:"template"`
	Active      bool   `json:"active"`
	// Customized is whether the wording differs from the default, which is
	// what restoring the default would undo.
	Customized      bool       `json:"customized"`
	DefaultTemplate string     `json:"default_template"`
	Variables       []string   `json:"variables"`
	UpdatedAt       *time.Time `json:"updated_at,omitempty"`
}

type storedAlert struct {
	template  string
	active    bool
	updatedAt time.Time
}

func effective(def alertDef, s storedAlert, ok bool) Alert {
	a := Alert{
		Event: def.Event, Title: def.Title, Description: def.Description,
		Template: def.Template, Active: true, DefaultTemplate: def.Template, Variables: def.Variables,
	}
	if ok {
		a.Template, a.Active = s.template, s.active
		at := s.updatedAt
		a.UpdatedAt = &at
	}
	a.Customized = a.Template != def.Template
	return a
}

func (m *Module) stored(ctx context.Context) (map[string]storedAlert, error) {
	rows, err := m.db.QueryContext(ctx, `SELECT event, template, active, updated_at FROM telegram_alerts`)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the telegram alerts")
	}
	defer rows.Close()
	out := map[string]storedAlert{}
	for rows.Next() {
		var event string
		var s storedAlert
		if err := rows.Scan(&event, &s.template, &s.active, &s.updatedAt); err != nil {
			return nil, gocommerce.Internalf(err, "read the telegram alerts")
		}
		out[event] = s
	}
	return out, rows.Err()
}

// alerts is every alert, in the catalogue's order.
func (m *Module) alerts(ctx context.Context) ([]Alert, error) {
	stored, err := m.stored(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Alert, 0, len(catalogue))
	for _, def := range catalogue {
		s, ok := stored[def.Event]
		out = append(out, effective(def, s, ok))
	}
	return out, nil
}

// alert is one event's alert; NotFound for an event this module does not send.
func (m *Module) alert(ctx context.Context, event string) (Alert, error) {
	def, ok := definition(event)
	if !ok {
		return Alert{}, gocommerce.NotFoundf("no Telegram alert is sent for %q; there is one for each of %s", event, eventNames())
	}
	var s storedAlert
	err := m.db.QueryRowContext(ctx,
		`SELECT template, active, updated_at FROM telegram_alerts WHERE event = $1`, event).
		Scan(&s.template, &s.active, &s.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return effective(def, s, false), nil
	}
	if err != nil {
		return Alert{}, gocommerce.Internalf(err, "read the telegram alert")
	}
	return effective(def, s, true), nil
}

// ------------------------------------------------------------------ the data

// alertData is what an alert's wording is filled with: the engine's flat order
// data, plus what a message to a person needs that an API never should — the
// total written for reading (rule 6 keeps formatting out of responses, not out
// of messages), a first name and a link into the panel.
func (m *Module) alertData(ctx context.Context, ev *gocommerce.OrderEvent) map[string]string {
	data := gocommerce.OrderNotificationData(ev)
	data["total"] = formatMoney(ev.TotalMinor, ev.Currency)
	data["customer_first_name"] = firstName(ev.Name)
	data["customer_phone"] = ev.Phone
	data["order_url"] = m.orderURL(ev.OrderID)
	data["store_name"] = m.storeName(ctx)
	if ev.Return != nil {
		data["return_value"] = formatMoney(ev.Return.RefundableMinor, ev.Currency)
	}
	// Every key the editor offers is present, empty where this event has none:
	// text/template prints "<no value>" for a missing key, which is not a thing
	// to send to anybody's phone.
	for _, def := range catalogue {
		for _, key := range def.Variables {
			if _, ok := data[key]; !ok {
				data[key] = ""
			}
		}
	}
	return data
}

// orderURL is the order's page in the panel, built only from the panel's
// configured address. Without one there is no link rather than a guessed one.
func (m *Module) orderURL(id int64) string {
	base := strings.TrimRight(m.app.Config().PanelURL, "/")
	if base == "" || id <= 0 {
		return ""
	}
	return base + "/dash/orders/" + strconv.FormatInt(id, 10)
}

func (m *Module) storeName(ctx context.Context) string {
	p, err := m.app.Profile().Get(ctx)
	if err != nil {
		return ""
	}
	return p.Name
}

func firstName(name string) string {
	if f := strings.Fields(name); len(f) > 0 {
		return f[0]
	}
	return ""
}

// maxText is under Telegram's 4096-character limit on a message with room to
// spare: Telegram counts UTF-16 units, and a character outside the basic plane
// is two of them.
const maxText = 3500

// maxTemplate bounds what an owner may store, well past any alert anybody
// reads on a phone.
const maxTemplate = 2000

// render fills a wording with an alert's data, trimmed, and cut to what one
// message can carry.
func render(text string, data map[string]string) (string, error) {
	out, err := gocommerce.RenderNotifyText(text, data)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if utf8.RuneCountInString(out) > maxText {
		out = string([]rune(out)[:maxText-1]) + "…"
	}
	return out, nil
}

// sample is a made-up order for the preview and for checking an owner's
// wording as it is saved: the same data a real event would carry, so what the
// preview shows is what the phone will.
func (m *Module) sample(ctx context.Context, event string) *gocommerce.OrderEvent {
	ev := &gocommerce.OrderEvent{
		OrderID: 1042, Number: "GC-001042", Status: "pending", PaymentStatus: "pending",
		Provider: "cod", Currency: m.app.Currency(ctx), TotalMinor: 129900,
		Email: "asha@example.com", Phone: "+1 555 0100", Name: "Asha Rao",
		Lines: []gocommerce.OrderEventLine{
			{Title: "Cotton kurta", VariantLabel: "M / Indigo", Quantity: 1, UnitPriceMinor: 79900, TotalMinor: 79900},
			{Title: "Silk dupatta", Quantity: 1, UnitPriceMinor: 50000, TotalMinor: 50000},
		},
	}
	switch event {
	case gocommerce.EventOrderPaid:
		ev.Status, ev.PaymentStatus, ev.Provider = "confirmed", "paid", "razorpay"
	case gocommerce.EventOrderCancelled:
		ev.Status, ev.Reason = "cancelled", "payment not completed in time"
	case gocommerce.EventOrderReturned:
		ev.Status, ev.PaymentStatus, ev.Reason = "delivered", "paid", "Too small"
		ev.Return = &gocommerce.ReturnEvent{ReturnID: 7, Status: "received", Reason: "Too small",
			Units: 1, RestockedUnits: 1, RefundableMinor: 79900}
	}
	return ev
}

// checkTemplate refuses wording that would not send, as it is typed: a typo in
// {{.order_nmber}} found here rather than on the next sale.
func (m *Module) checkTemplate(ctx context.Context, event, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", gocommerce.Validationf("The message cannot be empty. To go back to the default wording, restore it.")
	}
	if len(text) > maxTemplate {
		return "", gocommerce.Validationf("The message is longer than %d characters; an alert is read on a phone.", maxTemplate)
	}
	out, err := render(text, m.alertData(ctx, m.sample(ctx, event)))
	if err != nil {
		return "", gocommerce.Validationf("The message does not work: %v", err)
	}
	if out == "" {
		return "", gocommerce.Validationf("The message comes out empty for an order like the preview's. Write something it always says.")
	}
	return text, nil
}

// ------------------------------------------------------------------ handlers

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	out, err := m.alerts(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	a, err := m.alert(r.Context(), r.PathValue("event"))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, a)
}

// AlertInput creates an event's alert.
type AlertInput struct {
	Event string `json:"event"`
	// Template is the wording; empty keeps the default.
	Template string `json:"template"`
	Active   *bool  `json:"active"`
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in AlertInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	def, ok := definition(strings.TrimSpace(in.Event))
	if !ok {
		gocommerce.RespondError(w, r, gocommerce.Validationf(
			"event must be one of %s: those are the events a Telegram alert is sent for", eventNames()))
		return
	}
	text := def.Template
	if in.Template != "" {
		var err error
		if text, err = m.checkTemplate(ctx, def.Event, in.Template); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	var s storedAlert
	err := m.db.QueryRowContext(ctx, `
		INSERT INTO telegram_alerts (event, template, active) VALUES ($1, $2, $3)
		ON CONFLICT (store_id, event) DO NOTHING
		RETURNING template, active, updated_at`, def.Event, text, active).Scan(&s.template, &s.active, &s.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, gocommerce.Conflictf(
			"%s already has its alert; change it with PATCH /api/admin/x/telegram-alerts/alerts/%s", def.Event, def.Event))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "create the telegram alert"))
		return
	}
	gocommerce.Respond(w, http.StatusCreated, effective(def, s, true))
}

// AlertPatch changes an event's switch, its wording, or both.
type AlertPatch struct {
	Template *string `json:"template"`
	Active   *bool   `json:"active"`
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	def, ok := definition(r.PathValue("event"))
	if !ok {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf(
			"no Telegram alert is sent for %q; there is one for each of %s", r.PathValue("event"), eventNames()))
		return
	}
	var in AlertPatch
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if in.Template == nil && in.Active == nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("send template, active or both"))
		return
	}
	var text *string
	if in.Template != nil {
		checked, err := m.checkTemplate(ctx, def.Event, *in.Template)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		text = &checked
	}
	// One statement, so a switch flipped while somebody saves the wording
	// keeps both: each column takes the patch's value or keeps its own.
	var s storedAlert
	err := m.db.QueryRowContext(ctx, `
		INSERT INTO telegram_alerts (event, template, active)
		VALUES ($1, coalesce($2, $4), coalesce($3, true))
		ON CONFLICT (store_id, event) DO UPDATE SET
		    template = coalesce($2, telegram_alerts.template),
		    active = coalesce($3, telegram_alerts.active),
		    updated_at = now()
		RETURNING template, active, updated_at`,
		def.Event, text, in.Active, def.Template).Scan(&s.template, &s.active, &s.updatedAt)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "save the telegram alert"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, effective(def, s, true))
}

// handleReset forgets the owner's row, which puts the default switch and
// wording back. Nothing to forget is the same answer: the default stands.
func (m *Module) handleReset(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	def, ok := definition(r.PathValue("event"))
	if !ok {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf(
			"no Telegram alert is sent for %q; there is one for each of %s", r.PathValue("event"), eventNames()))
		return
	}
	if _, err := m.db.ExecContext(ctx, `DELETE FROM telegram_alerts WHERE event = $1`, def.Event); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "reset the telegram alert"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, effective(def, storedAlert{}, false))
}

// PreviewInput is what to preview: an event, and wording to try in place of
// the alert's own.
type PreviewInput struct {
	Event    string  `json:"event"`
	Template *string `json:"template"`
}

func (m *Module) handlePreview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in PreviewInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	a, err := m.alert(ctx, strings.TrimSpace(in.Event))
	if err != nil {
		if errors.Is(err, gocommerce.ErrNotFound) {
			err = gocommerce.Validationf("event must be one of %s", eventNames())
		}
		gocommerce.RespondError(w, r, err)
		return
	}
	text := a.Template
	if in.Template != nil {
		text = *in.Template
	}
	// Wording that does not render is an answer, not a failed request: the
	// panel previews as the owner types, and every {{ passes through a broken
	// state on the way to a finished one. Saving it is still refused.
	out := previewResult{Event: a.Event}
	if out.Text, err = render(text, m.alertData(ctx, m.sample(ctx, a.Event))); err != nil {
		out.Text, out.Error = "", "The message does not work: "+err.Error()
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

type previewResult struct {
	Event string `json:"event"`
	Text  string `json:"text"`
	// Error says why the wording does not render; Text is empty with it.
	Error string `json:"error,omitempty"`
}
