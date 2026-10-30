// Package contact is the storefront's contact form, its product enquiries,
// and the inbox behind both.
//
// A shopper writes — through the contact form, or by asking about one product
// — and the store keeps the message and tells its own address by email, when
// its screen in the panel says where. Both kinds land in one inbox: an enquiry is a
// message marked as one, carrying the product as it was called when they
// asked. The inbox has three states — new, replied, archived — and a place
// for notes.
//
// An operator holding contact.reply answers from the panel. The reply goes to
// the shopper by email through the store's own provider (App.NotifyNow), from
// the provider's sender with its reply-to, so the answer comes back to the
// shop; once the provider has taken it, the reply is kept on the message as a
// thread — who sent it, when, what it said — and the message is marked
// replied, in one transaction. A reply that did not go is not recorded: the
// operator is told why and keeps the text. A public demo sends no replies.
//
// Both public forms carry a honeypot and are limited per peer address, the
// way ext/b2b limits its dealer form.
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
	"unicode/utf8"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	pluginKey  = "contact-form"
	maxBody    = 5000
	maxSubject = 200
	maxName    = 200
	maxPhone   = 40

	defaultMessagesPerMinute = 10
)

// Statuses.
const (
	StatusNew      = "new"
	StatusReplied  = "replied"
	StatusArchived = "archived"
)

// Kinds: what the shopper used to write in.
const (
	KindMessage = "message"
	KindEnquiry = "enquiry"
)

// Config configures the module.
type Config struct {
	// NotifyEmail is where a new message or enquiry is announced. The Plugins
	// screen can set it too, and wins.
	NotifyEmail string
	// MessagesPerMinute is how many messages and enquiries, counted together,
	// one address may send in a minute. Zero means ten; a negative number
	// turns the limit off. It counts by the connection's peer address, which
	// behind a proxy in the engine's TrustedProxies is the shopper's own;
	// behind a proxy the engine does not trust, every shopper shares the
	// proxy's allowance.
	MessagesPerMinute int
}

// Message is one message from a shopper.
type Message struct {
	ID          int64           `json:"id"`
	Kind        string          `json:"kind"`
	Name        string          `json:"name"`
	Email       string          `json:"email"`
	Phone       string          `json:"phone,omitempty"`
	Subject     string          `json:"subject"`
	Body        string          `json:"body"`
	OrderNumber string          `json:"order_number,omitempty"`
	Product     *EnquiryProduct `json:"product,omitempty"`
	Status      string          `json:"status"`
	Notes       string          `json:"notes,omitempty"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
	// Replies is the thread, oldest first. Only a single message carries it:
	// the list is for finding one.
	Replies []Reply `json:"replies,omitempty"`
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

// Module is the routes and the tables.
type Module struct {
	cfg   Config
	app   *gocommerce.App
	db    *sql.DB
	limit *throttle
}

// New constructs the module.
func New(cfg Config) *Module {
	if cfg.MessagesPerMinute == 0 {
		cfg.MessagesPerMinute = defaultMessagesPerMinute
	}
	return &Module{cfg: cfg, limit: newThrottle(cfg.MessagesPerMinute, time.Minute)}
}

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
	},
		gocommerce.ScopeMigration("0002_store_scope", "contact_messages"),
		{
			ID: "0003_replies",
			SQL: `
-- What the store wrote back, one row per email the provider took.
CREATE TABLE contact_replies (
    id         bigserial   PRIMARY KEY,
    store_id   text        NOT NULL,
    message_id bigint      NOT NULL,
    subject    text        NOT NULL,
    body       text        NOT NULL,
    sent_by    text        NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL DEFAULT now(),
    FOREIGN KEY (store_id, message_id) REFERENCES contact_messages (store_id, id) ON DELETE CASCADE
);
CREATE INDEX contact_replies_message_idx ON contact_replies (store_id, message_id, id);
`,
		},
		gocommerce.ScopeMigration("0004_replies_store_scope", "contact_replies"),
		{
			ID: "0005_enquiries",
			SQL: `
-- An enquiry is a message about one product. The product is kept as it was
-- called when the shopper asked, and by id with no foreign key onto the
-- catalogue: a product stays deletable, and the inbox keeps what was asked.
ALTER TABLE contact_messages
    ADD COLUMN kind          text    NOT NULL DEFAULT 'message' CHECK (kind IN ('message', 'enquiry')),
    ADD COLUMN product_id    bigint,
    ADD COLUMN product_title text    NOT NULL DEFAULT '',
    ADD COLUMN product_slug  text    NOT NULL DEFAULT '',
    ADD COLUMN variant_id    bigint,
    ADD COLUMN variant_title text    NOT NULL DEFAULT '',
    ADD COLUMN quantity      integer CHECK (quantity > 0),
    ADD CONSTRAINT contact_messages_enquiry_has_product CHECK ((kind = 'enquiry') = (product_id IS NOT NULL));
CREATE INDEX contact_messages_kind_idx ON contact_messages (store_id, kind, id DESC);
`,
		},
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.registerRights(app)
	m.app = app
	m.db = app.DB()
	// The wording of what this module sends, editable from the panel like
	// every other message. Without a template the email backend has nothing
	// to say and the announcement would be a row in the log and nothing more.
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: "contact.message", Title: "Contact form message",
		Description: "To the store's own address when someone writes through the contact form.",
		Variables:   []string{"from_name", "from_email", "subject", "body", "order_number", "message_id"},
		Subject:     "New message from {{.from_name}}{{if .subject}}: {{.subject}}{{end}}",
		Body: `{{.from_name}} <{{.from_email}}> wrote{{if .order_number}} about order {{.order_number}}{{end}}:

{{.body}}

Reply from the Contact screen, or to {{.from_email}} and mark it replied there.`,
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: "contact.enquiry", Title: "Product enquiry",
		Description: "To the store's own address when a shopper asks about a product.",
		Variables: []string{"from_name", "from_email", "phone", "product_title", "variant_title", "quantity",
			"product_link", "body", "message_id"},
		Subject: "{{.from_name}} asked about {{.product_title}}",
		Body: `{{.from_name}} <{{.from_email}}>{{if .phone}}, {{.phone}},{{end}} asked about {{.product_title}}{{if .variant_title}} ({{.variant_title}}){{end}}{{if .quantity}}, {{.quantity}} of them{{end}}:

{{.body}}
{{if .product_link}}
The product: {{.product_link}}
{{end}}
Reply from the Contact screen.`,
	})
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: "contact.reply", Title: "Reply to a contact message",
		Description: "To a shopper, when somebody answers their message or enquiry from the Contact screen.",
		Variables:   []string{"name", "subject", "reply", "original", "original_subject", "product_title", "store_name"},
		Subject:     "{{.subject}}",
		Body: `{{.reply}}
{{if .store_name}}
{{.store_name}}
{{end}}
---
{{if .product_title}}You asked about {{.product_title}}{{else}}You wrote{{end}}:

{{.original}}`,
	})
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Contact form", Icon: "ri-customer-service-2-line", Category: "storefront", DefaultEnabled: true,
		Home:        "/dash/contact",
		Description: "A contact form on the storefront, product enquiries, and the inbox behind both on the Contact screen. Each new message and enquiry is announced by email to the address below.",
		Fields: []gocommerce.PluginField{
			{Key: "notify_email", Label: "Announce new messages to", Kind: "text", Required: m.cfg.NotifyEmail == "", Help: "The store's own address, for messages and product enquiries alike. Each announcement lands in Notifications too."},
			{Key: "subjects", Label: "Subjects the form offers", Kind: "textarea", Public: true, Default: "An order\nA product\nReturns and exchanges\nSomething else", Help: "One per line; the storefront shows them as a list."},
			{Key: "thanks", Label: "After sending", Kind: "text", Public: true, Default: "Thanks — we'll reply by email."},
			{Key: "enquiries", Label: "Take product enquiries", Kind: "bool", Public: true, Default: true, Help: "Shoppers can ask about a product from its page; enquiries land in the same inbox, marked as such."},
		},
	})
	app.HandleFunc("POST /x/contact/messages", m.handleSubmit)
	app.HandleFunc("POST /x/contact/enquiries", m.handleEnquiry)

	// An inbox is customer correspondence, with rights of its own (D65):
	// reading it, writing from the store's address, and clearing it.
	app.HandleAdminFunc("GET /api/admin/x/contact/messages", m.handleList, rightContactRead)
	app.HandleAdminFunc("GET /api/admin/x/contact/messages/{id}", m.handleGet, rightContactRead)
	app.HandleAdminFunc("PATCH /api/admin/x/contact/messages/{id}", m.handleUpdate, rightContactWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/contact/messages/{id}", m.handleDelete, rightContactWrite)
	app.HandleAdminFunc("POST /api/admin/x/contact/messages/{id}/reply", m.handleReply, rightContactReply)
	return nil
}

func (m *Module) notifyEmail(ctx context.Context) string {
	if v := m.app.Plugins().String(ctx, pluginKey, "notify_email"); v != "" {
		return v
	}
	return m.cfg.NotifyEmail
}

const messageColumns = `id, kind, name, email, phone, subject, body, order_number,
	product_id, product_title, product_slug, variant_id, variant_title, quantity,
	status, notes, created_at, updated_at`

func scan(sc interface{ Scan(...any) error }) (*Message, error) {
	var (
		msg                     Message
		productID, variantID    sql.NullInt64
		quantity                sql.NullInt32
		title, slug, variantTtl string
	)
	if err := sc.Scan(&msg.ID, &msg.Kind, &msg.Name, &msg.Email, &msg.Phone, &msg.Subject, &msg.Body, &msg.OrderNumber,
		&productID, &title, &slug, &variantID, &variantTtl, &quantity,
		&msg.Status, &msg.Notes, &msg.CreatedAt, &msg.UpdatedAt); err != nil {
		return nil, err
	}
	if productID.Valid {
		msg.Product = &EnquiryProduct{ID: productID.Int64, Title: title, Slug: slug, VariantTitle: variantTtl}
		if variantID.Valid {
			msg.Product.VariantID = &variantID.Int64
		}
		if quantity.Valid {
			q := int(quantity.Int32)
			msg.Product.Quantity = &q
		}
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

// withThread is one message and its replies, for the drawer.
func (m *Module) withThread(ctx context.Context, id int64) (*Message, error) {
	msg, err := m.byID(ctx, id)
	if err != nil {
		return nil, err
	}
	if msg.Replies, err = m.replies(ctx, id); err != nil {
		return nil, err
	}
	return msg, nil
}

// ----------------------------------------------------------------- public

// sender is who wrote in, checked the same way on both forms.
type sender struct {
	name, email, phone, body string
}

func checkSender(name, email, phone, body string) (sender, error) {
	s := sender{name: strings.TrimSpace(name), phone: strings.TrimSpace(phone), body: strings.TrimSpace(body)}
	if s.name == "" {
		return s, gocommerce.Validationf("a name is required")
	}
	if utf8.RuneCountInString(s.name) > maxName {
		return s, gocommerce.Validationf("the name is too long: %d characters at most", maxName)
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil {
		return s, gocommerce.Validationf("that does not look like an email address")
	}
	s.email = strings.ToLower(addr.Address)
	if s.phone != "" && (len(s.phone) > maxPhone || !validPhone(s.phone)) {
		return s, gocommerce.Validationf("a phone number is digits, spaces and + - ( ) . only")
	}
	if s.body == "" {
		return s, gocommerce.Validationf("the message is empty")
	}
	if utf8.RuneCountInString(s.body) > maxBody {
		return s, gocommerce.Validationf("the message is too long: %d characters at most", maxBody)
	}
	return s, nil
}

func validPhone(s string) bool {
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case strings.ContainsRune("+-(). ", r):
		default:
			return false
		}
	}
	return digits >= 5
}

// open answers 404 while the contact form is switched off, and 429 for an
// address that has written too often this minute. Every attempt counts, valid
// or not: what is protected is the store's inbox, and a malformed request
// still costs a validation.
func (m *Module) open(w http.ResponseWriter, r *http.Request) bool {
	if on, _ := m.app.Plugins().Enabled(r.Context(), pluginKey); !on {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("the contact form is not open on this store"))
		return false
	}
	store, _ := gocommerce.StoreFrom(r.Context())
	if wait, ok := m.limit.allow(store+" "+peerAddr(r), time.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusTooManyRequests,
			Code: "too_many_attempts", Message: "too many messages from this address; try again in a minute"})
		return false
	}
	return true
}

func (m *Module) handleSubmit(w http.ResponseWriter, r *http.Request) {
	if !m.open(w, r) {
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
	s, err := checkSender(in.Name, in.Email, in.Phone, in.Body)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	subject, order := strings.TrimSpace(in.Subject), strings.TrimSpace(in.OrderNumber)
	if utf8.RuneCountInString(subject) > maxSubject {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the subject is too long: %d characters at most", maxSubject))
		return
	}
	var id int64
	if err := m.db.QueryRowContext(r.Context(), `
		INSERT INTO contact_messages (name, email, phone, subject, body, order_number)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		s.name, s.email, s.phone, subject, s.body, order).Scan(&id); err != nil {
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
				"message_id": strconv.FormatInt(id, 10), "from_name": s.name, "from_email": s.email,
				"subject": subject, "body": s.body, "order_number": order,
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
	if kind := q.Get("kind"); kind != "" {
		if kind != KindMessage && kind != KindEnquiry {
			gocommerce.RespondError(w, r, gocommerce.Validationf("kind must be message or enquiry"))
			return
		}
		args = append(args, kind)
		where = append(where, fmt.Sprintf("kind = $%d", len(args)))
	}
	if s := strings.TrimSpace(q.Get("q")); s != "" {
		// A fragment for the people, the subject and the product asked about,
		// the exact value for an order number: "42" must not sweep in every
		// order with a 42 in it. Not the address where it is masked (a demo
		// store, or an agent key): a contains-match would read it back a
		// character at a time.
		args = append(args, "%"+strings.ToLower(s)+"%", s)
		email := fmt.Sprintf(" OR email LIKE $%d", len(args)-1)
		if m.app.Masks(r.Context()) {
			email = ""
		}
		where = append(where, fmt.Sprintf("(lower(name) LIKE $%d%s OR lower(subject) LIKE $%d OR lower(product_title) LIKE $%d OR order_number = $%d)",
			len(args)-1, email, len(args)-1, len(args)-1, len(args)))
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
		m.mask(r.Context(), msg)
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
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
	msg, err := m.withThread(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.mask(r.Context(), msg)
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
	msg, err := m.withThread(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.mask(r.Context(), msg)
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
// Read and write were core's until D65 — customers.read and store.operate —
// which meant the permission could not be given without the thing it was
// borrowed from, and the screens had no row of their own on the Roles grid.
// Their Default names the roles that held the old right, so nobody's access
// moved.
//
// Reply is new rather than lifted: writing to a customer from the store's
// own address is more than reading the inbox and less than clearing it, so it
// is a manager's by default (an owner holds every right). Staff read; a store
// that wants them answering grants it on the Roles screen.
const (
	rightContactRead  gocommerce.Right = "contact.read"  // was customers.read
	rightContactWrite gocommerce.Right = "contact.write" // was store.operate
	rightContactReply gocommerce.Right = "contact.reply"
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightContactRead,
		Label:   "Read the contact inbox",
		Scope:   "Messages and product enquiries the storefront collected — personal data",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightContactWrite,
		Label:   "Clear the contact inbox",
		Scope:   "Marking messages handled, and deleting them",
		Default: []string(nil),
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightContactReply,
		Label:   "Reply to contact messages",
		Scope:   "Answering a message or an enquiry by email from the store's address",
		Default: []string{gocommerce.RoleManager},
	})
}

// mask hides how to reach whoever wrote in, and who on the team answered. A
// demo store's inbox is readable by anybody who signs in, and a contact
// message is somebody's address and their number sitting beside whatever they
// wanted to say.
func (m *Module) mask(ctx context.Context, msg *Message) {
	if msg == nil {
		return
	}
	msg.Email = m.app.MaskEmail(ctx, msg.Email)
	msg.Phone = m.app.MaskPhone(ctx, msg.Phone)
	for i := range msg.Replies {
		msg.Replies[i].SentBy = m.app.MaskEmail(ctx, msg.Replies[i].SentBy)
	}
}
