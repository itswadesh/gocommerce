// Package retailassistant keeps a shop's supplier accounts from what it forwards
// on WhatsApp (D192): a bill arrives on one number shared by every store, is
// read as far as a deterministic reader honestly can, waits for a person to
// review it, and then becomes a payable; payments and credit notes settle it.
//
// Three boundaries are the design.
//
// A supplier's bill is not an order and paying a supplier is not a shopper
// paying. So the ledger here is its own, and it touches nothing in core except
// stock, through Stock().Adjust, when goods physically arrive. Receiving a PDF
// never moves stock.
//
// Nothing is posted unread. Every document stops at needs_review however well
// it parsed, and a figure the reader could not find stays null: nothing here
// guesses an amount. A model may read a PDF or a photo through the AI gateway when
// the store has agreed to that, and what it reads still stops there.
//
// The number is shared and the records are not. A message names no store, so
// the sender decides, and only a number the store's owner named and that then
// proved itself by sending the code from that number. An unknown sender's file
// is not kept anywhere: a store-owned table has no row without a store.
package retailassistant

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
	"github.com/itswadesh/gocommerce/ext/b2b"
)

const (
	moduleName = "retailassistant"
	pluginKey  = "retailassistant"
	adminBase  = "/api/admin/x/retailassistant"

	defaultBaseURL = "https://graph.facebook.com"
	// The version ext/notify-whatsapp calls, so the install meets one API.
	graphVersion = "v26.0"

	rightRead  gocommerce.Right = "retailassistant.read"
	rightWrite gocommerce.Right = "retailassistant.write"
	rightLinks gocommerce.Right = "retailassistant.links"
)

// Config is the install's one WhatsApp number. It is the platform's rather than
// a store's, so it comes from the environment and not from a store's settings;
// a store's own account for order messages is ext/notify-whatsapp's.
type Config struct {
	AppSecret     string
	VerifyToken   string
	AccessToken   string
	PhoneNumberID string
	// DisplayNumber is the number as a person dials it, shown when a store links
	// a phone. Empty, the screen says "the store's WhatsApp number".
	DisplayNumber string
	// QuietToStrangers answers nobody a store has not linked. It is for a number
	// shared with another service, whose customers write to it about something
	// else and must not be told about store linking.
	QuietToStrangers bool
	// BaseURL replaces Meta's Graph API, and Client the HTTP client. Tests set
	// them; the address is the server owner's, so an ordinary client is right.
	BaseURL string
	Client  *http.Client
	// FileKey is 32 bytes of base64 that seal documents in the database. Empty,
	// they are kept in clear behind the store policy.
	FileKey string
	// ClamdAddr is a clamd's host:port. Empty, files are not scanned.
	ClamdAddr string
	// TemplateName is the Meta-approved template that carries a message outside
	// WhatsApp's customer-service window: one body variable, in each language a
	// phone may choose. Empty, such a message is marked undelivered.
	TemplateName string
	// LoginTemplate is the Meta-approved authentication template that carries a
	// panel sign-in code. Empty, sign-in codes go as they did.
	LoginTemplate string
	// B2B makes wholesale quotes. Nil, there are none.
	B2B *b2b.Module
	// Gateway reads PDFs and photos with a model. Nil, a person enters them.
	Gateway *aigateway.Module
	// Poll is how often the worker looks for work; 0 is five seconds.
	Poll time.Duration
}

// Module is the retail assistant.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	db     *sql.DB
	log    *slog.Logger
	client *http.Client
	wake   chan struct{}

	fileKey    []byte
	fileKeyBad bool
	tasks      sync.Once
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg, wake: make(chan struct{}, 1)} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return moduleName }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{
		{ID: "0001_tables", SQL: `
CREATE TABLE retailassistant_phone_links (
    id              bigserial PRIMARY KEY,
    phone           text NOT NULL,
    code_hash       text,
    code_expires_at timestamptz,
    code_attempts   integer NOT NULL DEFAULT 0,
    verified_at     timestamptz,
    selected_at     timestamptz,
    revoked_at      timestamptz,
    notify          boolean NOT NULL DEFAULT false,
    created_by      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE retailassistant_messages (
    id                  bigserial PRIMARY KEY,
    provider_message_id text NOT NULL,
    sender              text NOT NULL,
    type                text NOT NULL,
    received_at         timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE retailassistant_documents (
    id            bigserial PRIMARY KEY,
    message_id    bigint REFERENCES retailassistant_messages (id),
    sender        text NOT NULL,
    media_id      text NOT NULL,
    filename      text NOT NULL DEFAULT '',
    declared_mime text NOT NULL DEFAULT '',
    detected_mime text NOT NULL DEFAULT '',
    sha256        text NOT NULL DEFAULT '',
    content       bytea,
    status        text NOT NULL DEFAULT 'received'
        CHECK (status IN ('received', 'parsing', 'needs_review', 'failed', 'posted', 'rejected')),
    extraction    jsonb,
    draft         jsonb,
    version       integer NOT NULL DEFAULT 1,
    attempts      integer NOT NULL DEFAULT 0,
    available_at  timestamptz NOT NULL DEFAULT now(),
    last_error    text NOT NULL DEFAULT '',
    reviewed_by   text NOT NULL DEFAULT '',
    reviewed_at   timestamptz,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE retailassistant_suppliers (
    id              bigserial PRIMARY KEY,
    name            text NOT NULL,
    normalized_name text NOT NULL,
    gstin           text,
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE retailassistant_purchase_invoices (
    id              bigserial PRIMARY KEY,
    supplier_id     bigint NOT NULL REFERENCES retailassistant_suppliers (id),
    document_id     bigint REFERENCES retailassistant_documents (id),
    invoice_no      text NOT NULL,
    invoice_date    date NOT NULL,
    subtotal_minor  bigint,
    tax_minor       bigint,
    total_minor     bigint NOT NULL CHECK (total_minor > 0),
    currency        text NOT NULL,
    reversed_at     timestamptz,
    reversed_by     text NOT NULL DEFAULT '',
    reversal_reason text NOT NULL DEFAULT '',
    created_by      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE retailassistant_purchase_lines (
    id               bigserial PRIMARY KEY,
    invoice_id       bigint NOT NULL REFERENCES retailassistant_purchase_invoices (id),
    description      text NOT NULL DEFAULT '',
    sku              text,
    barcode          text,
    hsn              text,
    quantity         text,
    unit             text,
    unit_price_minor bigint,
    tax_rate_bps     integer,
    mrp_minor        bigint,
    line_total_minor bigint
);
-- A row with reverses_id is the compensating entry for the row it names, with
-- the opposite amount. Rows are never edited to undo money, so a balance can
-- always be worked back from what was recorded.
CREATE TABLE retailassistant_supplier_payments (
    id                 bigserial PRIMARY KEY,
    supplier_id        bigint NOT NULL REFERENCES retailassistant_suppliers (id),
    amount_minor       bigint NOT NULL CHECK (amount_minor <> 0),
    currency           text NOT NULL,
    method             text NOT NULL DEFAULT '',
    paid_on            date NOT NULL,
    reference          text NOT NULL DEFAULT '',
    status             text NOT NULL CHECK (status IN ('provisional', 'verified')),
    source_document_id bigint REFERENCES retailassistant_documents (id),
    reverses_id        bigint REFERENCES retailassistant_supplier_payments (id),
    idempotency_key    text,
    verified_by        text NOT NULL DEFAULT '',
    verified_at        timestamptz,
    created_by         text NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE retailassistant_supplier_credits (
    id                 bigserial PRIMARY KEY,
    supplier_id        bigint NOT NULL REFERENCES retailassistant_suppliers (id),
    amount_minor       bigint NOT NULL CHECK (amount_minor <> 0),
    currency           text NOT NULL,
    -- Goods sent back and goods written off as damaged are different facts, so
    -- they stay distinct from the first bill onward.
    kind               text NOT NULL CHECK (kind IN ('return', 'damaged', 'other')),
    credit_no          text NOT NULL DEFAULT '',
    reason             text NOT NULL DEFAULT '',
    source_document_id bigint REFERENCES retailassistant_documents (id),
    reverses_id        bigint REFERENCES retailassistant_supplier_credits (id),
    idempotency_key    text,
    created_by         text NOT NULL DEFAULT '',
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE retailassistant_payment_allocations (
    id              bigserial PRIMARY KEY,
    invoice_id      bigint NOT NULL REFERENCES retailassistant_purchase_invoices (id),
    payment_id      bigint REFERENCES retailassistant_supplier_payments (id),
    credit_id       bigint REFERENCES retailassistant_supplier_credits (id),
    amount_minor    bigint NOT NULL CHECK (amount_minor <> 0),
    reverses_id     bigint REFERENCES retailassistant_payment_allocations (id),
    idempotency_key text,
    created_by      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    CHECK (num_nonnulls(payment_id, credit_id) = 1)
);
CREATE TABLE retailassistant_stock_receipts (
    id          bigserial PRIMARY KEY,
    line_id     bigint NOT NULL REFERENCES retailassistant_purchase_lines (id),
    variant_id  bigint NOT NULL,
    location_id bigint NOT NULL DEFAULT 0,
    quantity    integer NOT NULL CHECK (quantity > 0),
    status      text NOT NULL CHECK (status IN ('pending', 'posted')),
    created_by  text NOT NULL DEFAULT '',
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE retailassistant_replies (
    id           bigserial PRIMARY KEY,
    dedupe_key   text NOT NULL,
    recipient    text NOT NULL,
    body         text NOT NULL,
    attempts     integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    sent_at      timestamptz,
    dead         boolean NOT NULL DEFAULT false,
    last_error   text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE retailassistant_audit (
    id         bigserial PRIMARY KEY,
    actor      text NOT NULL,
    event      text NOT NULL,
    entity     text NOT NULL,
    entity_id  bigint NOT NULL,
    detail     jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);`},
		gocommerce.ScopeMigration("0002_store_scope",
			"retailassistant_phone_links", "retailassistant_messages", "retailassistant_documents",
			"retailassistant_suppliers", "retailassistant_purchase_invoices", "retailassistant_purchase_lines",
			"retailassistant_supplier_payments", "retailassistant_supplier_credits",
			"retailassistant_payment_allocations", "retailassistant_stock_receipts",
			"retailassistant_replies", "retailassistant_audit"),
		{ID: "0003_keys", SQL: `
CREATE UNIQUE INDEX retailassistant_phone_links_phone ON retailassistant_phone_links (store_id, phone);
CREATE UNIQUE INDEX retailassistant_messages_provider ON retailassistant_messages (store_id, provider_message_id);
CREATE INDEX retailassistant_documents_claim ON retailassistant_documents (store_id, available_at)
    WHERE status IN ('received', 'parsing');
CREATE INDEX retailassistant_documents_list ON retailassistant_documents (store_id, id DESC);
CREATE INDEX retailassistant_documents_sha ON retailassistant_documents (store_id, sha256);
CREATE UNIQUE INDEX retailassistant_suppliers_gstin ON retailassistant_suppliers (store_id, gstin) WHERE gstin IS NOT NULL;
CREATE UNIQUE INDEX retailassistant_suppliers_name ON retailassistant_suppliers (store_id, normalized_name);
-- One bill from one supplier is one liability, however often it is sent. Two
-- suppliers may each have an invoice numbered 109.
CREATE UNIQUE INDEX retailassistant_invoices_number ON retailassistant_purchase_invoices (store_id, supplier_id, lower(invoice_no))
    WHERE reversed_at IS NULL;
CREATE UNIQUE INDEX retailassistant_invoices_document ON retailassistant_purchase_invoices (store_id, document_id)
    WHERE document_id IS NOT NULL AND reversed_at IS NULL;
CREATE INDEX retailassistant_lines_invoice ON retailassistant_purchase_lines (store_id, invoice_id);
CREATE UNIQUE INDEX retailassistant_payments_source ON retailassistant_supplier_payments (store_id, source_document_id)
    WHERE source_document_id IS NOT NULL AND reverses_id IS NULL;
CREATE UNIQUE INDEX retailassistant_payments_reverses ON retailassistant_supplier_payments (store_id, reverses_id) WHERE reverses_id IS NOT NULL;
CREATE UNIQUE INDEX retailassistant_payments_key ON retailassistant_supplier_payments (store_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX retailassistant_credits_source ON retailassistant_supplier_credits (store_id, source_document_id)
    WHERE source_document_id IS NOT NULL AND reverses_id IS NULL;
CREATE UNIQUE INDEX retailassistant_credits_reverses ON retailassistant_supplier_credits (store_id, reverses_id) WHERE reverses_id IS NOT NULL;
CREATE UNIQUE INDEX retailassistant_credits_key ON retailassistant_supplier_credits (store_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE UNIQUE INDEX retailassistant_allocations_reverses ON retailassistant_payment_allocations (store_id, reverses_id) WHERE reverses_id IS NOT NULL;
CREATE UNIQUE INDEX retailassistant_allocations_key ON retailassistant_payment_allocations (store_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX retailassistant_allocations_invoice ON retailassistant_payment_allocations (store_id, invoice_id);
-- What makes a goods receipt happen once: the row is committed before stock moves.
CREATE UNIQUE INDEX retailassistant_receipts_line ON retailassistant_stock_receipts (store_id, line_id);
CREATE UNIQUE INDEX retailassistant_replies_key ON retailassistant_replies (store_id, dedupe_key);
CREATE INDEX retailassistant_replies_claim ON retailassistant_replies (store_id, available_at)
    WHERE sent_at IS NULL AND NOT dead;`},
		// A link code is a key to a store's inbox, and a supplier's bill is the
		// shop's business, not reporting data for whoever may run a report.
		gocommerce.WithholdFromReports("0004_withhold_from_reports",
			"retailassistant_phone_links.code_hash", "retailassistant_documents.content",
			"retailassistant_supplier_payments.idempotency_key", "retailassistant_supplier_credits.idempotency_key",
			"retailassistant_payment_allocations.idempotency_key", "retailassistant_replies.dedupe_key"),
		{ID: "0005_file_handling", SQL: `
ALTER TABLE retailassistant_documents
    ADD COLUMN content_sealed boolean NOT NULL DEFAULT false,
    ADD COLUMN content_removed_at timestamptz;`},
		{ID: "0006_messaging", SQL: `
ALTER TABLE retailassistant_phone_links
    ADD COLUMN language text NOT NULL DEFAULT 'en' CHECK (language IN ('en', 'hi')),
    ADD COLUMN opted_out_at timestamptz;
ALTER TABLE retailassistant_replies
    ADD COLUMN language text NOT NULL DEFAULT 'en';
CREATE INDEX retailassistant_messages_window ON retailassistant_messages (store_id, sender, received_at DESC);`},
		{ID: "0007_bank_lines", SQL: `
CREATE TABLE retailassistant_bank_lines (
    id                bigserial PRIMARY KEY,
    line_date         date NOT NULL,
    narration         text NOT NULL DEFAULT '',
    reference         text NOT NULL DEFAULT '',
    amount_minor      bigint NOT NULL CHECK (amount_minor > 0),
    currency          text NOT NULL,
    line_hash         text NOT NULL,
    status            text NOT NULL DEFAULT 'unmatched' CHECK (status IN ('unmatched', 'matched', 'ignored')),
    payment_id        bigint REFERENCES retailassistant_supplier_payments (id),
    -- Whether this match is what verified the payment, so undoing it can undo that.
    verified_by_match boolean NOT NULL DEFAULT false,
    imported_by       text NOT NULL DEFAULT '',
    reviewed_by       text NOT NULL DEFAULT '',
    reviewed_at       timestamptz,
    created_at        timestamptz NOT NULL DEFAULT now()
);`},
		gocommerce.ScopeMigration("0008_bank_store_scope", "retailassistant_bank_lines"),
		{ID: "0010_wholesale_quotes", SQL: `
-- What a wholesale quote was worked from, for the audit trail. Lines hold what the
-- wholesaler paid, which is why they live here and not on ext/b2b's quote.
CREATE TABLE retailassistant_wholesale_quotes (
    id              bigserial PRIMARY KEY,
    document_id     bigint NOT NULL REFERENCES retailassistant_documents (id),
    company_id      bigint NOT NULL,
    quote_id        bigint,
    mode            text NOT NULL,
    percent_bps     integer NOT NULL,
    lines           jsonb NOT NULL,
    below_cost      boolean NOT NULL DEFAULT false,
    overridden      boolean NOT NULL DEFAULT false,
    idempotency_key text,
    created_by      text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now()
);`},
		gocommerce.ScopeMigration("0011_wholesale_store_scope", "retailassistant_wholesale_quotes"),
		{ID: "0012_wholesale_keys", SQL: `
CREATE UNIQUE INDEX retailassistant_wholesale_key ON retailassistant_wholesale_quotes (store_id, idempotency_key) WHERE idempotency_key IS NOT NULL;`},
		gocommerce.WithholdFromReports("0013_withhold_wholesale",
			"retailassistant_wholesale_quotes.lines", "retailassistant_wholesale_quotes.idempotency_key"),
		{ID: "0009_bank_keys", SQL: `
CREATE UNIQUE INDEX retailassistant_bank_lines_hash ON retailassistant_bank_lines (store_id, line_hash);
CREATE UNIQUE INDEX retailassistant_bank_lines_payment ON retailassistant_bank_lines (store_id, payment_id) WHERE payment_id IS NOT NULL;
CREATE INDEX retailassistant_bank_lines_list ON retailassistant_bank_lines (store_id, status, line_date DESC);`},
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app, m.db, m.log = app, app.DB(), app.Log()
	if m.cfg.BaseURL == "" {
		m.cfg.BaseURL = defaultBaseURL
	}
	m.cfg.BaseURL = strings.TrimRight(m.cfg.BaseURL, "/")
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{Timeout: 60 * time.Second}
	}
	if m.cfg.Poll <= 0 {
		m.cfg.Poll = 5 * time.Second
	}
	m.loadFileKey()
	if m.cfg.Gateway != nil {
		var err error
		m.tasks.Do(func() { err = m.cfg.Gateway.RegisterTask(m.readTask()) })
		if err != nil {
			return err
		}
	}

	if m.cfg.LoginTemplate != "" {
		app.RegisterNotifier(gocommerce.ChannelSMS, loginCodes{m})
	}
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightRead, Label: "See supplier accounts",
		Scope:   "Bills forwarded on WhatsApp, suppliers, what is owed to them and what was paid",
		Default: []string{gocommerce.RoleManager},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightWrite, Label: "Change supplier accounts",
		Scope:   "Approving bills, recording and allocating payments and credit notes, and receiving goods",
		Default: []string{gocommerce.RoleManager},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right: rightLinks, Label: "Link WhatsApp numbers",
		Scope:   "Which phones may send documents into this store",
		Default: nil,
	})
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: pluginKey, Title: "Retail assistant", Icon: "ri-whatsapp-line", Category: "operations", DefaultEnabled: false,
		Home:        "/dash/retail-assistant",
		Description: "Forward supplier bills and payment proofs on WhatsApp, review what was read from them, and see what you owe each supplier.",
		Fields: []gocommerce.PluginField{
			{Key: "retention_days", Label: "Delete files after this many days", Kind: "number",
				Help: "Counted from when a document was reviewed. Your supplier accounts stay as they are; only the original files go. Leave empty to keep files. Check with your accountant how long you must keep bills before setting this."},
			{Key: "morning_summary", Label: "Send a morning summary", Kind: "bool", Default: false,
				Help: "Around 8 in the morning, India time, each phone that takes confirmations gets what you owe and what is waiting. It may cost a WhatsApp template message a day."},
			{Key: "ai_reading", Label: "Read PDFs and photos with AI", Kind: "bool", Default: false,
				Help: "Sends each PDF and photo to the AI provider set up in the AI gateway, and fills in the review form from what it reads. You still compare every figure with the file before approving. It uses your AI credit."},
		},
	})

	// Meta's callback names no store and cannot be made to, so the address
	// carries ?store= only to get past the engine's store binding; the sender
	// picks the real one (whatsapp.go).
	app.HandleFunc("GET /x/retailassistant/whatsapp/webhook", m.handleVerify)
	app.HandleFunc("POST /x/retailassistant/whatsapp/webhook", m.handleWebhook)

	read := func(pattern string, h http.HandlerFunc) {
		app.HandleAdminFunc(pattern, m.on(h), rightRead)
	}
	write := func(pattern string, h http.HandlerFunc) {
		app.HandleAdminFunc(pattern, m.on(h), rightWrite)
	}
	// Not behind the switch: it is how the screen learns the switch's state.
	app.HandleAdminFunc("GET "+adminBase+"/status", m.handleStatus, rightRead)

	app.HandleAdminFunc("GET "+adminBase+"/phone-links", m.on(m.handleListLinks), rightLinks)
	app.HandleAdminFunc("POST "+adminBase+"/phone-links", m.on(m.handleCreateLink), rightLinks)
	app.HandleAdminFunc("PATCH "+adminBase+"/phone-links/{id}", m.on(m.handleUpdateLink), rightLinks)
	app.HandleAdminFunc("DELETE "+adminBase+"/phone-links/{id}", m.on(m.handleRevokeLink), rightLinks)

	read("GET "+adminBase+"/documents", m.handleListDocuments)
	read("GET "+adminBase+"/documents/{id}", m.handleGetDocument)
	read("GET "+adminBase+"/documents/{id}/file", m.handleDocumentFile)
	write("POST "+adminBase+"/documents/{id}/correct", m.handleCorrect)
	write("POST "+adminBase+"/documents/{id}/approve", m.handleApprove)
	write("POST "+adminBase+"/documents/{id}/reject", m.handleReject)
	write("POST "+adminBase+"/documents/{id}/retry", m.handleRetry)
	write("POST "+adminBase+"/documents/{id}/remove-file", m.handleRemoveFile)
	write("POST "+adminBase+"/documents/{id}/read", m.handleRead)
	read("GET "+adminBase+"/export/{kind}", m.handleExport)

	read("GET "+adminBase+"/suppliers", m.handleSuppliers)
	read("GET "+adminBase+"/payables", m.handlePayables)
	read("GET "+adminBase+"/payments", m.handleListPayments)
	read("GET "+adminBase+"/credits", m.handleListCredits)
	write("POST "+adminBase+"/payments", m.handleCreatePayment)
	write("POST "+adminBase+"/payments/{id}/verify", m.handleVerifyPayment)
	write("POST "+adminBase+"/payments/{id}/allocate", m.handleAllocatePayment)
	write("POST "+adminBase+"/payments/{id}/reverse", m.handleReversePayment)
	write("POST "+adminBase+"/credits", m.handleCreateCredit)
	write("POST "+adminBase+"/credits/{id}/allocate", m.handleAllocateCredit)
	write("POST "+adminBase+"/credits/{id}/reverse", m.handleReverseCredit)
	write("POST "+adminBase+"/allocations/{id}/reverse", m.handleReverseAllocation)
	write("POST "+adminBase+"/invoices/{id}/reverse", m.handleReverseInvoice)
	read("GET "+adminBase+"/invoices/{id}/lines", m.handleInvoiceLines)
	read("GET "+adminBase+"/insights", m.handleInsights)
	write("POST "+adminBase+"/wholesale/preview", m.handleWholesalePreview)
	write("POST "+adminBase+"/wholesale/quotes", m.handleWholesaleQuote)
	read("GET "+adminBase+"/bank-lines", m.handleBankLines)
	write("POST "+adminBase+"/bank-statements", m.handleImportStatement)
	write("POST "+adminBase+"/bank-lines/{id}/match", m.handleMatchLine)
	write("POST "+adminBase+"/bank-lines/{id}/record", m.handleRecordFromLine)
	write("POST "+adminBase+"/bank-lines/{id}/ignore", m.handleIgnoreLine)
	write("POST "+adminBase+"/bank-lines/{id}/undo", m.handleUndoLine)
	// Receiving goods moves stock, so it takes the inventory right as well.
	app.HandleAdminFunc("POST "+adminBase+"/invoices/{id}/receive", m.on(m.handleReceive), rightWrite, gocommerce.RightInventoryWrite)

	app.OnStart(m.start)
	return nil
}

func (m *Module) enabled(ctx context.Context) bool {
	on, err := m.app.Plugins().Enabled(ctx, pluginKey)
	return err == nil && on
}

// on keeps a route to the stores that opted in. The module being installed
// must change nothing for a store that did not.
func (m *Module) on(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !m.enabled(r.Context()) {
			gocommerce.RespondError(w, r, &gocommerce.APIError{
				Status: http.StatusNotFound, Code: "feature_off",
				Message: "the retail assistant is switched off for this store",
			})
			return
		}
		h(w, r)
	}
}

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	reading, why := m.aiAvailable(r.Context())
	gocommerce.Respond(w, http.StatusOK, map[string]any{
		"wholesale":      m.cfg.B2B != nil,
		"ai_reading":     reading,
		"ai_reading_why": why,
		"enabled":        m.enabled(r.Context()),
		"channel_ready":  m.channelReady(),
		"display_number": m.cfg.DisplayNumber,
		"files_sealed":   m.fileKey != nil,
		"files_scanned":  m.cfg.ClamdAddr != "",
		"template_ready": m.cfg.TemplateName != "",
	})
}

func (m *Module) channelReady() bool {
	return m.cfg.AppSecret != "" && m.cfg.VerifyToken != "" && m.cfg.AccessToken != "" && m.cfg.PhoneNumberID != ""
}

// actor is who to write down: the person, or the install's script token.
func actor(ctx context.Context) string {
	if su := gocommerce.SuperuserFrom(ctx); su != nil {
		return su.Contact()
	}
	return "admin token"
}

// audit is one line in the same transaction as what it describes.
func (m *Module) audit(ctx context.Context, tx *sql.Tx, event, entity string, id int64, detail any) error {
	raw, err := marshalDetail(detail)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO retailassistant_audit (actor, event, entity, entity_id, detail)
		VALUES ($1, $2, $3, $4, $5)`, actor(ctx), event, entity, id, raw)
	return err
}
