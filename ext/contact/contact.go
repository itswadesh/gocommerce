// Package contact is the storefront's contact form and the inbox behind it.
//
// A shopper writes; the store keeps the message, tells the operator by
// email if the Plugins screen says where, and shows it in an inbox with
// three states — new, replied, archived — and a place for notes. Replying
// itself happens in the operator's own mail: the inbox is the record of
// what came in and what was done about it, not a mail client.
package contact

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey  = "contact-form"
	maxBody    = 5000
	maxSubject = 200
)

// Statuses.
const (
	StatusNew      = "new"
	StatusReplied  = "replied"
	StatusArchived = "archived"
)

// Config configures the module.
type Config struct {
	// NotifyEmail is where a new message is announced. The Plugins screen
	// can set it too, and wins.
	NotifyEmail string
}

// Message is one message from a shopper.
type Message struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Email       string    `json:"email"`
	Phone       string    `json:"phone,omitempty"`
	Subject     string    `json:"subject"`
	Body        string    `json:"body"`
	OrderNumber string    `json:"order_number,omitempty"`
	Status      string    `json:"status"`
	Notes       string    `json:"notes,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// MessageInput is what the storefront posts.
type MessageInput struct {
	Name        string `json:"name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Subject     string `json:"subject"`
	Body        string `json:"body"`
	OrderNumber string `json:"order_number"`
	// Website is a honeypot: a field no person sees.
	Website string `json:"website"`
}

// MessagePatch is what an operator changes.
type MessagePatch struct {
	Status *string `json:"status"`
	Notes  *string `json:"notes"`
}

// Module is the routes and the table.
type Module struct {
	cfg Config
	app *gocommerce.App
	db  *sql.DB
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "contact" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_messages",
		SQL: `
CREATE TABLE contact_messages (
    id           bigserial   PRIMARY KEY,
    name         text        NOT NULL,
    email        text        NOT NULL,
    phone        text        NOT NULL DEFAULT '',
    subject      text        NOT NULL DEFAULT '',
    body         text        NOT NULL,
    order_number text        NOT NULL DEFAULT '',
    status       text        NOT NULL DEFAULT 'new' CHECK (status IN ('new', 'replied', 'archived')),
    notes        text        NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX contact_messages_status_idx ON contact_messages (status, id DESC);
`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.registerRights(app)
	m.app = app
	m.db = app.DB()
	// The announcement's wording, editable from the panel like every other
	// message. Without a template the email backend has nothing to say and
	// the announcement would be a row in the log and nothing more.
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: "contact.message", Title: "Contact form message",
		Description: "To the store's own address when someone writes through the contact form.",
		Variables:   []string{"from_name", "from_email", "subject", "body", "order_number", "message_id"},
		Subject:     "New message from {{.from_name}}{{if .subject}}: {{.subject}}{{end}}",
		Body: `{{.from_name}} <{{.from_email}}> wrote{{if .order_number}} about order {{.order_number}}{{end}}:

{{.body}}

Reply to {{.from_email}}, then mark it replied on the Contact screen.`,
	})
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Contact form", Category: "storefront", DefaultEnabled: true,
		Description: "A contact form on the storefront, and the inbox behind it on the Contact screen. A new message is announced by email to the address below.",
		Fields: []gocommerce.PluginField{
			{Key: "notify_email", Label: "Announce new messages to", Kind: "text", Required: m.cfg.NotifyEmail == "", Help: "The store's own address. Each announcement lands in Notifications too."},
			{Key: "subjects", Label: "Subjects the form offers", Kind: "textarea", Public: true, Default: "An order\nA product\nReturns and exchanges\nSomething else", Help: "One per line; the storefront shows them as a list."},
			{Key: "thanks", Label: "After sending", Kind: "text", Public: true, Default: "Thanks — we'll reply by email."},
		},
	})
	app.HandleFunc("POST /x/contact/messages", m.handleSubmit)

	// An inbox is customer correspondence: the customers' right to read,
	// store.operate to act on.
	app.HandleAdminFunc("GET /api/admin/x/contact/messages", m.handleList, rightContactRead)
	app.HandleAdminFunc("GET /api/admin/x/contact/messages/{id}", m.handleGet, rightContactRead)
	app.HandleAdminFunc("PATCH /api/admin/x/contact/messages/{id}", m.handleUpdate, rightContactWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/contact/messages/{id}", m.handleDelete, rightContactWrite)
	return nil
}

func (m *Module) notifyEmail(ctx context.Context) string {
	if v := m.app.Plugins().String(ctx, pluginKey, "notify_email"); v != "" {
		return v
	}
	return m.cfg.NotifyEmail
}

const messageColumns = `id, name, email, phone, subject, body, order_number, status, notes, created_at, updated_at`

func scan(sc interface{ Scan(...any) error }) (*Message, error) {
	var msg Message
	if err := sc.Scan(&msg.ID, &msg.Name, &msg.Email, &msg.Phone, &msg.Subject, &msg.Body, &msg.OrderNumber,
		&msg.Status, &msg.Notes, &msg.CreatedAt, &msg.UpdatedAt); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (m *Module) byID(ctx context.Context, id int64) (*Message, error) {
	msg, err := scan(m.db.QueryRowContext(ctx, `SELECT `+messageColumns+` FROM contact_messages WHERE id = $1`, id))
	if err == sql.ErrNoRows {
		return nil, gocommerce.NotFoundf("message %d does not exist", id)
	}
	return msg, err
}

// ----------------------------------------------------------------- public

func (m *Module) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if on, _ := m.app.Plugins().Enabled(r.Context(), pluginKey); !on {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("the contact form is not open on this store"))
		return
	}
	var in MessageInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if strings.TrimSpace(in.Website) != "" {
		gocommerce.Respond(w, http.StatusCreated, map[string]string{"status": StatusNew})
		return
	}
	in.Name, in.Body, in.Subject = strings.TrimSpace(in.Name), strings.TrimSpace(in.Body), strings.TrimSpace(in.Subject)
	if in.Name == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("a name is required"))
		return
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(in.Email))
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("that does not look like an email address"))
		return
	}
	email := strings.ToLower(addr.Address)
	if in.Body == "" {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the message is empty"))
		return
	}
	if len(in.Body) > maxBody || len(in.Subject) > maxSubject {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the message is too long: %d characters at most", maxBody))
		return
	}
	var id int64
	if err := m.db.QueryRowContext(r.Context(), `
		INSERT INTO contact_messages (name, email, phone, subject, body, order_number)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		in.Name, email, strings.TrimSpace(in.Phone), in.Subject, in.Body, strings.TrimSpace(in.OrderNumber)).Scan(&id); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// The operator hears about it through the store's own channel, so the
	// announcement is in the notification log like everything else sent.
	// Best effort: a mail backend being down must not fail the form.
	if to := m.notifyEmail(r.Context()); to != "" {
		_ = m.app.Notify(r.Context(), gocommerce.Notification{
			Event: "contact.message", Channel: gocommerce.ChannelEmail, To: to,
			Data: map[string]string{
				"message_id": strconv.FormatInt(id, 10), "from_name": in.Name, "from_email": email,
				"subject": in.Subject, "body": in.Body, "order_number": strings.TrimSpace(in.OrderNumber),
			},
		})
	}
	gocommerce.Respond(w, http.StatusCreated, map[string]string{"status": StatusNew})
}

// ------------------------------------------------------------------ admin

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	q := r.URL.Query()
	where, args := []string{"1 = 1"}, []any{}
	if status := q.Get("status"); status != "" {
		if status != StatusNew && status != StatusReplied && status != StatusArchived {
			gocommerce.RespondError(w, r, gocommerce.Validationf("status must be new, replied or archived"))
			return
		}
		args = append(args, status)
		where = append(where, fmt.Sprintf("status = $%d", len(args)))
	}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		// A fragment for the people and the subject, the exact value for an
		// order number: "42" must not sweep in every order with a 42 in it.
		args = append(args, "%"+strings.ToLower(s)+"%", s)
		where = append(where, fmt.Sprintf("(lower(name) LIKE $%d OR email LIKE $%d OR lower(subject) LIKE $%d OR order_number = $%d)",
			len(args)-1, len(args)-1, len(args)-1, len(args)))
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := m.db.QueryRowContext(r.Context(), `SELECT count(*) FROM contact_messages WHERE `+clause, args...).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(r.Context(), `SELECT `+messageColumns+` FROM contact_messages WHERE `+clause+
		fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()
	out := []*Message{}
	for rows.Next() {
		msg, err := scan(rows)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		out = append(out, msg)
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func pathID(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.Validationf("id must be a positive integer")
	}
	return id, nil
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	msg, err := m.byID(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, msg)
}

func (m *Module) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var patch MessagePatch
	if err := gocommerce.DecodeJSON(w, r, &patch); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	sets, args := []string{"updated_at = now()"}, []any{}
	if patch.Status != nil {
		if *patch.Status != StatusNew && *patch.Status != StatusReplied && *patch.Status != StatusArchived {
			gocommerce.RespondError(w, r, gocommerce.Validationf("status must be new, replied or archived"))
			return
		}
		args = append(args, *patch.Status)
		sets = append(sets, fmt.Sprintf("status = $%d", len(args)))
	}
	if patch.Notes != nil {
		args = append(args, strings.TrimSpace(*patch.Notes))
		sets = append(sets, fmt.Sprintf("notes = $%d", len(args)))
	}
	args = append(args, id)
	res, err := m.db.ExecContext(r.Context(),
		"UPDATE contact_messages SET "+strings.Join(sets, ", ")+fmt.Sprintf(" WHERE id = $%d", len(args)), args...)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("message %d does not exist", id))
		return
	}
	msg, err := m.byID(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, msg)
}

func (m *Module) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.db.ExecContext(r.Context(), `DELETE FROM contact_messages WHERE id = $1`, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("message %d does not exist", id))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// The rights this module's own screens are gated on.
//
// They were core's until now — customers.read and store.operate —
// which meant the permission could not be given without the thing it was
// borrowed from, and the screens had no row of their own on the Roles grid.
// Default names the roles that held the old right, so nobody's access moves.
const (
	rightContactRead  gocommerce.Right = "contact.read"  // was customers.read
	rightContactWrite gocommerce.Right = "contact.write" // was store.operate
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightContactRead,
		Label:   "Read the contact inbox",
		Scope:   "Messages the storefront's form collected — personal data",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightContactWrite,
		Label:   "Clear the contact inbox",
		Scope:   "Marking messages handled, and deleting them",
		Default: []string(nil),
	})
}
