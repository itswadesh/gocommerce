package whatsapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Template is one of the account's templates as the screen shows it.
type Template struct {
	Name     string `json:"name"`
	Language string `json:"language"`
	Status   string `json:"status"`
	Category string `json:"category"`
	Body     string `json:"body"`
	// Variables are the body's {{…}}, in order.
	Variables []string `json:"variables"`
	// QuickReplies are its quick-reply buttons' labels, in order.
	QuickReplies []string `json:"quick_replies"`
	// Usable is whether a slot can send it at all: approved by Meta, with no
	// header this module cannot fill. Reason says why not.
	Usable bool   `json:"usable"`
	Reason string `json:"reason,omitempty"`
}

type graphTemplate struct {
	Name       string `json:"name"`
	Language   string `json:"language"`
	Status     string `json:"status"`
	Category   string `json:"category"`
	Components []struct {
		Type    string `json:"type"`
		Format  string `json:"format"`
		Text    string `json:"text"`
		Buttons []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"buttons"`
	} `json:"components"`
}

// templates reads the account's templates from Meta, by its Message Templates
// API: GET /<WABA_ID>/message_templates, followed a few pages.
func (m *Module) templates(ctx context.Context, acct account) ([]Template, error) {
	q := url.Values{"fields": {"name,status,category,language,components"}, "limit": {"100"}}
	var out []Template
	for page := 0; page < 5; page++ {
		var resp struct {
			Data   []graphTemplate `json:"data"`
			Paging struct {
				Cursors struct {
					After string `json:"after"`
				} `json:"cursors"`
				Next string `json:"next"`
			} `json:"paging"`
		}
		if err := m.graph(ctx, acct.token, http.MethodGet, "/"+url.PathEscape(acct.wabaID)+"/message_templates", q, nil, &resp); err != nil {
			return nil, err
		}
		for _, g := range resp.Data {
			out = append(out, readTemplate(g))
		}
		if resp.Paging.Next == "" || resp.Paging.Cursors.After == "" {
			break
		}
		q.Set("after", resp.Paging.Cursors.After)
	}
	return out, nil
}

func readTemplate(g graphTemplate) Template {
	t := Template{Name: g.Name, Language: g.Language, Status: g.Status, Category: g.Category,
		Variables: []string{}, QuickReplies: []string{}, Usable: true}
	for _, c := range g.Components {
		switch strings.ToUpper(c.Type) {
		case "BODY":
			t.Body = c.Text
			t.Variables = variablesIn(c.Text)
		case "HEADER":
			if strings.ToUpper(c.Format) != "TEXT" || len(variablesIn(c.Text)) > 0 {
				t.Usable, t.Reason = false, "Its header needs a value or a file GoCommerce cannot fill"
			}
		case "BUTTONS":
			for _, b := range c.Buttons {
				if strings.ToUpper(b.Type) == "QUICK_REPLY" {
					t.QuickReplies = append(t.QuickReplies, b.Text)
				}
			}
		}
	}
	if t.Status != "APPROVED" {
		t.Usable, t.Reason = false, "Meta has not approved it ("+strings.ToLower(t.Status)+")"
	}
	return t
}

// checkMapping is whether a slot may send this template with these values.
func checkMapping(s *Slot, t *Template, vars []Variable) error {
	if !t.Usable {
		return gocommerce.Validationf("%s cannot be sent: %s", t.Name, t.Reason)
	}
	want := "UTILITY"
	if s.Marketing {
		want = "MARKETING"
	}
	if t.Category != want {
		return gocommerce.Validationf("%s is a %s template; %s needs a %s one",
			t.Name, strings.ToLower(t.Category), strings.ToLower(s.Label), strings.ToLower(want))
	}
	if len(t.QuickReplies) < len(s.Buttons) {
		return gocommerce.Validationf("%s needs %d quick-reply buttons, %s first", s.Label, len(s.Buttons), strings.Join(s.Buttons, " then "))
	}
	// The payloads go by position, so the labels must say the same thing in
	// the same order: a template whose first button reads Cancel would cancel
	// orders on a tap meant to confirm them.
	for i, meaning := range s.Buttons {
		if !strings.Contains(strings.ToLower(t.QuickReplies[i]), meaning) {
			return gocommerce.Validationf("%s's button %d reads %q; it must say %q, the buttons in the order %s",
				t.Name, i+1, t.QuickReplies[i], meaning, strings.Join(s.Buttons, " then "))
		}
	}
	got := map[string]string{}
	for _, v := range vars {
		got[v.Name] = v.Key
	}
	if len(got) != len(t.Variables) {
		return gocommerce.Validationf("give each of the template's %d variables a value", len(t.Variables))
	}
	for _, name := range t.Variables {
		key, ok := got[name]
		if !ok {
			return gocommerce.Validationf("{{%s}} has no value", name)
		}
		if !contains(s.Keys, key) {
			return gocommerce.Validationf("{{%s}}: %q is not a value %s can fill", name, key, strings.ToLower(s.Label))
		}
	}
	return nil
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// ------------------------------------------------------------------ routes

// Status is the screen's state. The access token, app secret and verify
// token are never in it.
type Status struct {
	Enabled    bool `json:"enabled"`
	Configured bool `json:"configured"`
	// WebhookURL is what the Meta app's webhook setup takes, beside the verify
	// token the store chose.
	WebhookURL    string         `json:"webhook_url"`
	WebhookSecret bool           `json:"webhook_ready"`
	CODCancel     bool           `json:"cod_cancel"`
	Slots         []SlotState    `json:"slots"`
	Counts        map[string]int `json:"counts"`
	LastError     string         `json:"last_error,omitempty"`
	LastSent      *time.Time     `json:"last_sent,omitempty"`
	Consents      map[string]int `json:"consents"`
}

// SlotState is a slot, what it sends, and the wording GoCommerce suggests.
type SlotState struct {
	Slot
	Template  *mapping    `json:"template"`
	Suggested *Suggestion `json:"suggested"`
}

func (m *Module) webhookURL(ctx context.Context) string {
	base := m.app.StoreBrand(ctx).PanelURL
	if base == "" {
		base = m.app.Config().PanelURL
	}
	store, ok := gocommerce.StoreFrom(ctx)
	if !ok {
		store = m.app.DefaultStore()
	}
	return strings.TrimRight(base, "/") + "/x/notify-whatsapp/webhook?store=" + url.QueryEscape(store)
}

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	acct := m.account(ctx)
	st := Status{
		Enabled: acct.on, Configured: acct.ready(), WebhookURL: m.webhookURL(ctx),
		WebhookSecret: acct.appSecret != "" && acct.verifyToken != "", CODCancel: acct.codCancel,
		Counts: map[string]int{}, Consents: map[string]int{},
	}
	maps, err := m.mappings(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read whatsapp templates"))
		return
	}
	for _, s := range Slots {
		st.Slots = append(st.Slots, SlotState{Slot: s, Template: maps[s.Key], Suggested: suggestionFor(s.Key)})
	}
	rows, err := m.db.QueryContext(ctx, `SELECT status, count(*) FROM whatsapp_messages GROUP BY status`)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "count whatsapp messages"))
		return
	}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err == nil {
			st.Counts[s] = n
		}
	}
	rows.Close()
	var lastErr sql.NullString
	_ = m.db.QueryRowContext(ctx, `
		SELECT (SELECT last_error FROM whatsapp_messages WHERE last_error IS NOT NULL ORDER BY updated_at DESC LIMIT 1),
		       (SELECT max(created_at) FROM whatsapp_messages WHERE status <> 'queued' AND status <> 'failed')`).
		Scan(&lastErr, &st.LastSent)
	st.LastError = lastErr.String
	var orders, marketing, out int
	_ = m.db.QueryRowContext(ctx, `
		SELECT count(*) FILTER (WHERE order_updates AND opted_out_at IS NULL),
		       count(*) FILTER (WHERE marketing AND opted_out_at IS NULL),
		       count(*) FILTER (WHERE opted_out_at IS NOT NULL)
		FROM whatsapp_contacts`).Scan(&orders, &marketing, &out)
	st.Consents["order_updates"], st.Consents["marketing"], st.Consents["opted_out"] = orders, marketing, out
	gocommerce.Respond(w, http.StatusOK, st)
}

// requireAccount answers for a screen that needs Meta: the connection must be
// saved first.
func (m *Module) requireAccount(w http.ResponseWriter, r *http.Request) (account, bool) {
	acct := m.account(r.Context())
	if !acct.ready() {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("connect WhatsApp first: switch it on and save the phone number ID, account ID and access token"))
		return acct, false
	}
	return acct, true
}

// metaFailure turns a Graph error into the panel's: Meta's own words, the
// token already scrubbed, as a 502 since it is Meta's answer rather than ours.
func metaFailure(err error) error {
	var me *metaError
	if errors.As(err, &me) {
		return &gocommerce.APIError{Status: http.StatusBadGateway, Code: "upstream", Message: "Meta: " + me.message}
	}
	return &gocommerce.APIError{Status: http.StatusBadGateway, Code: "upstream", Message: "Meta could not be reached: " + err.Error()}
}

func (m *Module) handleTemplates(w http.ResponseWriter, r *http.Request) {
	acct, ok := m.requireAccount(w, r)
	if !ok {
		return
	}
	ts, err := m.templates(r.Context(), acct)
	if err != nil {
		gocommerce.RespondError(w, r, metaFailure(err))
		return
	}
	if ts == nil {
		ts = []Template{}
	}
	gocommerce.Respond(w, http.StatusOK, ts)
}

// MapInput chooses a slot's template.
type MapInput struct {
	Name      string     `json:"name"`
	Language  string     `json:"language"`
	Variables []Variable `json:"variables"`
}

func (m *Module) handleMapSlot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	s := slotByKey(r.PathValue("slot"))
	if s == nil {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no slot is called %q", r.PathValue("slot")))
		return
	}
	var in MapInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	acct, ok := m.requireAccount(w, r)
	if !ok {
		return
	}
	// Judged against what Meta says now, so a template that was paused or
	// rejected since the screen loaded cannot be chosen.
	ts, err := m.templates(ctx, acct)
	if err != nil {
		gocommerce.RespondError(w, r, metaFailure(err))
		return
	}
	var t *Template
	for i := range ts {
		if ts[i].Name == in.Name && ts[i].Language == in.Language {
			t = &ts[i]
		}
	}
	if t == nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the account has no template %s in %s", in.Name, in.Language))
		return
	}
	if in.Variables == nil {
		in.Variables = []Variable{}
	}
	if err := checkMapping(s, t, in.Variables); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	vars, _ := json.Marshal(in.Variables)
	by := "token"
	if su := gocommerce.SuperuserFrom(ctx); su != nil {
		by = su.Email
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO whatsapp_templates (slot, name, language, category, variables, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (store_id, slot) DO UPDATE
		SET name = $2, language = $3, category = $4, variables = $5, updated_by = $6, updated_at = now()`,
		s.Key, t.Name, t.Language, t.Category, vars, by); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "save the whatsapp template"))
		return
	}
	tpl, _ := m.mapping(ctx, s.Key)
	gocommerce.Respond(w, http.StatusOK, tpl)
}

func (m *Module) handleUnmapSlot(w http.ResponseWriter, r *http.Request) {
	if _, err := m.db.ExecContext(r.Context(), `DELETE FROM whatsapp_templates WHERE slot = $1`, r.PathValue("slot")); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "clear the whatsapp template"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSuggest submits GoCommerce's suggested wording for a slot to Meta for
// review (POST /<WABA_ID>/message_templates). Meta decides; the answer is the
// status it gave, normally PENDING, and the screen lists the template as
// choosable only once Meta approves it.
func (m *Module) handleSuggest(w http.ResponseWriter, r *http.Request) {
	sg := suggestionFor(r.PathValue("slot"))
	if sg == nil {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no slot is called %q", r.PathValue("slot")))
		return
	}
	acct, ok := m.requireAccount(w, r)
	if !ok {
		return
	}
	var out struct {
		ID       string `json:"id"`
		Status   string `json:"status"`
		Category string `json:"category"`
	}
	if err := m.graph(r.Context(), acct.token, http.MethodPost, "/"+url.PathEscape(acct.wabaID)+"/message_templates",
		nil, sg.request(), &out); err != nil {
		gocommerce.RespondError(w, r, metaFailure(err))
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]string{
		"name": sg.Name, "language": sg.Language, "status": out.Status, "category": out.Category,
	})
}

// Message is one row of the screen's recent messages.
type Message struct {
	ID          int64     `json:"id"`
	Slot        string    `json:"slot"`
	Phone       string    `json:"phone"`
	OrderID     *int64    `json:"order_id,omitempty"`
	OrderNumber string    `json:"order_number,omitempty"`
	Status      string    `json:"status"`
	Reply       string    `json:"reply,omitempty"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

func (m *Module) handleMessages(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := "TRUE"
	if r.URL.Query().Get("reply") == "cancel_requested" {
		where = "reply = 'cancel_requested'"
	}
	rows, err := m.db.QueryContext(r.Context(), `
		SELECT id, slot, phone, order_id, coalesce(order_number, ''), status, coalesce(reply, ''),
		       coalesce(last_error, ''), created_at
		FROM whatsapp_messages WHERE `+where+` ORDER BY id DESC LIMIT $1`, limit)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list whatsapp messages"))
		return
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var msg Message
		var orderID sql.NullInt64
		if err := rows.Scan(&msg.ID, &msg.Slot, &msg.Phone, &orderID, &msg.OrderNumber, &msg.Status,
			&msg.Reply, &msg.Error, &msg.CreatedAt); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read whatsapp messages"))
			return
		}
		if orderID.Valid {
			msg.OrderID = &orderID.Int64
		}
		out = append(out, msg)
	}
	gocommerce.Respond(w, http.StatusOK, out)
}
