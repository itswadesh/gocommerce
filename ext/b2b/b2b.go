// Package b2b sells to businesses: companies with buyers in roles, orders
// placed on account against a credit limit and payment terms, purchase-order
// numbers, approval before a junior buyer spends the company's money, and
// quotes a merchant prices for one buyer.
//
//	accounts := identity.New(identity.Config{...})
//	app, err := gocommerce.New(cfg, accounts, b2b.New(b2b.Config{Accounts: accounts}))
//
// A buyer is a shopper account from ext/identity, so identity is listed first
// and handed in. Core has no customer concept and D22 keeps it that way; a
// company, and who buys for it, live here.
//
// Three engine rules shape what this module is allowed to be, and each is why
// something below looks the way it does.
//
// A company's prices are a customer group's (core/pricing.go), and a group
// price reaches a cart only through its verified address (D66). So joining a
// company puts the buyer's address into the company's group, and a buyer's
// cart is priced as them only once identity has confirmed that address — an
// invitation emailed to it and accepted is that confirmation. Without it,
// anybody who registered a dealer's address before the dealer did would have
// inherited the dealer's terms.
//
// Limits are enforced by a checkout guard (D67), inside the checkout's own
// transaction, with the order's final total and under a per-company advisory
// lock — so a credit limit is refused before the order exists rather than
// cancelled after, and two buyers cannot both fit under a limit only one of
// them fits under.
//
// A quote is a price agreed for one basket (D68), placed through
// Orders.Create, never a price list a whole group would reach.
package b2b

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/ext/identity"
)

// Company roles. What a buyer may do inside their company is the company's
// business, not the store's, so these are not operator rights.
const (
	// RoleAdmin runs the company's account: members, invitations, approvals.
	RoleAdmin = "admin"
	// RoleApprover places orders without approval and decides other people's.
	RoleApprover = "approver"
	// RoleBuyer places orders; one over the company's approval threshold
	// waits for an approver.
	RoleBuyer = "buyer"
)

// Company states.
const (
	StatusActive = "active"
	// StatusOnHold stops new orders on account and nothing else: the company
	// still buys, it just pays up front until somebody lifts the hold.
	StatusOnHold = "on_hold"
	// StatusClosed stops the company ordering at all.
	StatusClosed = "closed"
)

// CodeOnAccount is the payment method an order on the company's account is
// placed with.
const CodeOnAccount = "on_account"

// Notification events this module sends.
const (
	EventInvitation       = "b2b.invitation"
	EventApprovalRequest  = "b2b.approval_requested"
	EventApprovalDecision = "b2b.approval_decided"
	EventQuoteReady       = "b2b.quote_ready"
)

// metaKey is the order metadata key this module writes, and reserves: an
// order placed anywhere else that names it is refused, so the key can be
// trusted when it is read back.
const metaKey = "b2b"

const (
	rightCompaniesRead  gocommerce.Right = "companies.read"
	rightCompaniesWrite gocommerce.Right = "companies.write"
	rightQuotesRead     gocommerce.Right = "quotes.read"
	rightQuotesWrite    gocommerce.Right = "quotes.write"
)

const (
	defaultInviteTTL = 7 * 24 * time.Hour
	defaultQuoteTTL  = 30 * 24 * time.Hour
	reconcileEvery   = time.Hour
)

// Accounts is what this module needs from the shopper-accounts module.
// *identity.Module satisfies it; the interface is here so the dependency is
// written down in one place rather than spread across every call.
type Accounts interface {
	Resolve(ctx context.Context, token string) (*identity.Customer, bool)
	CustomerByID(ctx context.Context, id int64) (*identity.Customer, error)
	CustomerByEmail(ctx context.Context, email string) (*identity.Customer, error)
	MarkEmailVerified(ctx context.Context, customerID int64, email string) (*identity.Customer, error)
	ClaimCart(ctx context.Context, customerID int64, cartToken string) (*gocommerce.Cart, error)
}

// Config configures the module.
type Config struct {
	// Accounts is the store's shopper-accounts module. Required: a buyer is an
	// account.
	Accounts Accounts
	// InviteURL is the storefront page an invitation email links to, with
	// "{token}" where the token goes. Configured here and never taken from a
	// request, for the reason identity's ResetURL is: a link a client can
	// choose is a link a phisher can choose. Empty sends the token alone.
	InviteURL string
	// InviteTTL is how long an invitation stays open. Defaults to a week.
	InviteTTL time.Duration
	// QuoteTTL is how long a quote stays open when the merchant sends it
	// without an expiry of their own. Defaults to thirty days.
	QuoteTTL time.Duration
}

// Module is the b2b module.
type Module struct {
	cfg      Config
	app      *gocommerce.App
	db       *sql.DB
	accounts Accounts

	stop chan struct{}
	done sync.WaitGroup
}

// New constructs the module.
func New(cfg Config) *Module {
	if cfg.InviteTTL <= 0 {
		cfg.InviteTTL = defaultInviteTTL
	}
	if cfg.QuoteTTL <= 0 {
		cfg.QuoteTTL = defaultQuoteTTL
	}
	return &Module{cfg: cfg, accounts: cfg.Accounts}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "b2b" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_b2b",
		SQL: `
			-- A business that buys from this store.
			--
			-- group_id names a core customer group and has no foreign key: a
			-- module's table must never be able to refuse a core delete. A
			-- company whose group was deleted simply prices as everybody does.
			--
			-- credit_limit_minor NULL is "no account": the company pays up
			-- front like anybody else. approval_threshold_minor NULL is "a buyer
			-- never needs approval".
			CREATE TABLE b2b_companies (
			    id                       bigserial   PRIMARY KEY,
			    code                     text        NOT NULL UNIQUE
			                             CHECK (code ~ '^[a-z0-9]+(-[a-z0-9]+)*$'),
			    name                     text        NOT NULL CHECK (name <> ''),
			    tax_id                   text        NOT NULL DEFAULT '',
			    status                   text        NOT NULL DEFAULT 'active'
			                             CHECK (status IN ('active', 'on_hold', 'closed')),
			    group_id                 bigint,
			    credit_limit_minor       bigint      CHECK (credit_limit_minor >= 0),
			    net_days                 integer     NOT NULL DEFAULT 30
			                             CHECK (net_days BETWEEN 0 AND 365),
			    approval_threshold_minor bigint      CHECK (approval_threshold_minor >= 0),
			    require_po               boolean     NOT NULL DEFAULT false,
			    notes                    text        NOT NULL DEFAULT '',
			    metadata                 jsonb       NOT NULL DEFAULT '{}',
			    created_at               timestamptz NOT NULL DEFAULT now(),
			    updated_at               timestamptz NOT NULL DEFAULT now()
			);

			-- Who buys for a company. One company per account: a buyer's cart is
			-- priced as one set of terms, and an account in two companies would
			-- need a rule for which, at every checkout.
			--
			-- email is the address this module put into the company's group,
			-- kept so it can be taken out again when the account changes its
			-- address or is deleted — the reconciler compares the two. No
			-- foreign key onto identity's table for the same reason: a deleted
			-- account must leave this row behind long enough for its address to
			-- be removed from the group.
			CREATE TABLE b2b_members (
			    customer_id bigint      PRIMARY KEY,
			    company_id  bigint      NOT NULL REFERENCES b2b_companies (id) ON DELETE CASCADE,
			    email       text        NOT NULL CHECK (email = lower(email)),
			    role        text        NOT NULL CHECK (role IN ('admin', 'approver', 'buyer')),
			    created_at  timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX b2b_members_company_idx ON b2b_members (company_id);

			-- An invitation is mailed to an address and accepted by the account
			-- that holds it. Accepting proves the mailbox, which is the proof a
			-- group price needs (D66).
			CREATE TABLE b2b_invitations (
			    id          bigserial   PRIMARY KEY,
			    company_id  bigint      NOT NULL REFERENCES b2b_companies (id) ON DELETE CASCADE,
			    email       text        NOT NULL CHECK (email = lower(email)),
			    role        text        NOT NULL CHECK (role IN ('admin', 'approver', 'buyer')),
			    token_hash  text        NOT NULL UNIQUE,
			    invited_by  text        NOT NULL DEFAULT '',
			    expires_at  timestamptz NOT NULL,
			    accepted_at timestamptz,
			    created_at  timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX b2b_invitations_company_idx ON b2b_invitations (company_id, id);

			-- Which orders were placed for a company, with what the receivables
			-- ledger needs. No foreign key onto orders, as identity_orders has
			-- none. The order's own metadata carries the same company id, in the
			-- same transaction as the order — this row is written after, and the
			-- reconciler fills it in from that metadata if a crash got between.
			--
			-- company_id has no ON DELETE: a company with orders on its books
			-- is not something to delete, and the refusal says so.
			CREATE TABLE b2b_orders (
			    order_id     bigint      PRIMARY KEY,
			    order_number text        NOT NULL,
			    company_id   bigint      NOT NULL REFERENCES b2b_companies (id),
			    customer_id  bigint,
			    po_number    text        NOT NULL DEFAULT '',
			    on_account   boolean     NOT NULL,
			    due_at       timestamptz,
			    approval_id  bigint,
			    quote_id     bigint,
			    created_at   timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX b2b_orders_company_idx ON b2b_orders (company_id, order_id DESC);

			-- A basket waiting for somebody to say yes. lines and checkout are
			-- a snapshot: an approver approves what they were shown, at the
			-- prices they were shown, and the order is placed from the snapshot
			-- rather than from a cart the buyer could still be changing.
			--
			-- requested_by_email is who asked, as they were when they asked: the
			-- request is a record, and a buyer who later changes address or leaves
			-- the company still asked for it.
			CREATE TABLE b2b_approvals (
			    id                 bigserial   PRIMARY KEY,
			    company_id         bigint      NOT NULL REFERENCES b2b_companies (id) ON DELETE CASCADE,
			    requested_by       bigint      NOT NULL,
			    requested_by_email text        NOT NULL DEFAULT '',
			    kind               text        NOT NULL CHECK (kind IN ('cart', 'quote')),
			    quote_id        bigint,
			    status          text        NOT NULL DEFAULT 'pending'
			                    CHECK (status IN ('pending', 'placing', 'approved', 'rejected', 'cancelled')),
			    currency        text        NOT NULL,
			    lines           jsonb       NOT NULL,
			    subtotal_minor  bigint      NOT NULL,
			    total_minor     bigint      NOT NULL,
			    payment_method  text        NOT NULL,
			    po_number       text        NOT NULL DEFAULT '',
			    checkout        jsonb       NOT NULL,
			    idempotency_key text,
			    decided_by      bigint,
			    decided_at      timestamptz,
			    reason          text        NOT NULL DEFAULT '',
			    last_error      text        NOT NULL DEFAULT '',
			    order_id        bigint,
			    order_number    text,
			    created_at      timestamptz NOT NULL DEFAULT now(),
			    updated_at      timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX b2b_approvals_company_idx ON b2b_approvals (company_id, id DESC);
			-- A retried submission returns the request it already made.
			CREATE UNIQUE INDEX b2b_approvals_idem_idx ON b2b_approvals (requested_by, idempotency_key)
			    WHERE idempotency_key IS NOT NULL;

			-- A request for a price, and the price that answers it.
			--
			-- awaiting_approval is a quote a buyer has accepted that is over
			-- their approval limit. It is claimed, so it cannot be accepted
			-- twice; a rejected or withdrawn approval puts it back to quoted.
			CREATE TABLE b2b_quotes (
			    id                 bigserial   PRIMARY KEY,
			    company_id         bigint      NOT NULL REFERENCES b2b_companies (id) ON DELETE CASCADE,
			    requested_by       bigint      NOT NULL,
			    requested_by_email text        NOT NULL DEFAULT '',
			    status             text        NOT NULL DEFAULT 'requested'
			                       CHECK (status IN ('requested', 'quoted', 'awaiting_approval', 'accepted', 'declined')),
			    currency     text        NOT NULL,
			    note         text        NOT NULL DEFAULT '',
			    reply        text        NOT NULL DEFAULT '',
			    declined_by  text        NOT NULL DEFAULT '',
			    expires_at   timestamptz,
			    quoted_at    timestamptz,
			    accepted_at  timestamptz,
			    order_id     bigint,
			    order_number text,
			    created_at   timestamptz NOT NULL DEFAULT now(),
			    updated_at   timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX b2b_quotes_company_idx ON b2b_quotes (company_id, id DESC);
			CREATE INDEX b2b_quotes_status_idx ON b2b_quotes (status, id DESC);

			-- unit_price_minor is NULL until the merchant prices the line; a
			-- quote is sent only when every line has one.
			CREATE TABLE b2b_quote_lines (
			    id               bigserial PRIMARY KEY,
			    quote_id         bigint    NOT NULL REFERENCES b2b_quotes (id) ON DELETE CASCADE,
			    variant_id       bigint    NOT NULL,
			    sku              text      NOT NULL DEFAULT '',
			    title            text      NOT NULL DEFAULT '',
			    quantity         integer   NOT NULL CHECK (quantity > 0),
			    unit_price_minor bigint    CHECK (unit_price_minor >= 0),
			    position         integer   NOT NULL DEFAULT 0,
			    UNIQUE (quote_id, variant_id)
			);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	if m.accounts == nil {
		return errors.New("b2b: Config.Accounts is required — pass the store's identity module, listed before this one")
	}
	m.app = app
	m.db = app.DB()
	m.registerRights(app)
	app.RegisterPayment(&onAccount{m: m})
	app.RegisterCheckoutGuard(m.guard)
	m.registerTemplates(app)
	m.mountRoutes(app)
	app.OnStart(m.start)
	app.OnStop(m.halt)
	return nil
}

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightCompaniesRead,
		Label:   "See business customers",
		Scope:   "Companies, their buyers, their orders on account and what they owe",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	// Manager only: setting a credit limit is lending the store's money.
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightCompaniesWrite,
		Label:   "Manage business customers",
		Scope:   "Create companies, set credit limits, terms and approval rules, add and remove buyers",
		Default: []string{gocommerce.RoleManager},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightQuotesRead,
		Label:   "See quote requests",
		Scope:   "What business customers asked to be quoted, and what they were",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	// Manager only, the default pricing.write has: a quote is a price.
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightQuotesWrite,
		Label:   "Price quotes",
		Scope:   "Set the prices a quote offers, send it, or decline it",
		Default: []string{gocommerce.RoleManager},
	})
}

// start runs the reconciler once at boot and then hourly. It is what keeps a
// company's customer group honest when an account changes its address or is
// deleted — identity sends no event for either.
func (m *Module) start(ctx context.Context) error {
	m.stop = make(chan struct{})
	if err := m.reconcile(ctx); err != nil {
		m.app.Log().Warn("b2b reconcile failed", "error", err)
	}
	m.done.Add(1)
	go func() {
		defer m.done.Done()
		t := time.NewTicker(reconcileEvery)
		defer t.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-t.C:
				if err := m.reconcile(context.Background()); err != nil {
					m.app.Log().Warn("b2b reconcile failed", "error", err)
				}
			}
		}
	}()
	return nil
}

func (m *Module) halt(context.Context) error {
	if m.stop != nil {
		close(m.stop)
		m.done.Wait()
		m.stop = nil
	}
	return nil
}

// ------------------------------------------------------------------- types

// Company is a business that buys from the store.
type Company struct {
	ID      int64  `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	TaxID   string `json:"tax_id"`
	Status  string `json:"status"`
	GroupID *int64 `json:"group_id"`
	// CreditLimit absent means the company has no account: it pays up front.
	CreditLimit *gocommerce.Money `json:"credit_limit"`
	NetDays     int               `json:"net_days"`
	// ApprovalThreshold absent means a buyer never waits for approval.
	ApprovalThreshold *gocommerce.Money   `json:"approval_threshold"`
	RequirePO         bool                `json:"require_po"`
	Notes             string              `json:"notes"`
	Metadata          gocommerce.Metadata `json:"metadata"`
	MemberCount       int                 `json:"member_count"`
	CreatedAt         time.Time           `json:"created_at"`
	UpdatedAt         time.Time           `json:"updated_at"`
}

// Member is one buyer and their role in a company.
//
// Email is the address the company's group holds for them, and empty while
// they have moved to an address they have not yet confirmed — Confirmed says
// which, so a screen can say "awaiting confirmation" rather than show a blank.
type Member struct {
	CompanyID  int64     `json:"company_id"`
	CustomerID int64     `json:"customer_id"`
	Email      string    `json:"email"`
	Confirmed  bool      `json:"confirmed"`
	Name       string    `json:"name"`
	Role       string    `json:"role"`
	CreatedAt  time.Time `json:"created_at"`
}

// Invitation is an open offer to join a company. The token is never in it:
// it went to the mailbox, and only the mailbox should have it.
type Invitation struct {
	ID         int64      `json:"id"`
	CompanyID  int64      `json:"company_id"`
	Email      string     `json:"email"`
	Role       string     `json:"role"`
	InvitedBy  string     `json:"invited_by"`
	ExpiresAt  time.Time  `json:"expires_at"`
	AcceptedAt *time.Time `json:"accepted_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Credit is where a company stands on its account.
type Credit struct {
	// Limit absent: the company has no account.
	Limit       *gocommerce.Money `json:"limit"`
	Outstanding gocommerce.Money  `json:"outstanding"`
	// Available is the limit less what is outstanding, never below zero.
	Available     *gocommerce.Money `json:"available"`
	Overdue       gocommerce.Money  `json:"overdue"`
	OverdueOrders int               `json:"overdue_orders"`
	NetDays       int               `json:"net_days"`
}

// CompanyOrder is an order placed for a company, as its ledger reads it.
type CompanyOrder struct {
	OrderID       int64            `json:"order_id"`
	Number        string           `json:"number"`
	Status        string           `json:"status"`
	PaymentStatus string           `json:"payment_status"`
	Total         gocommerce.Money `json:"total"`
	PONumber      string           `json:"po_number"`
	PlacedBy      string           `json:"placed_by"`
	OnAccount     bool             `json:"on_account"`
	DueAt         *time.Time       `json:"due_at,omitempty"`
	Overdue       bool             `json:"overdue"`
	CompanyID     int64            `json:"company_id"`
	CompanyName   string           `json:"company_name"`
	CreatedAt     time.Time        `json:"created_at"`
}

// ApprovalLine is one line of a basket awaiting approval, at the price the
// approver is asked to approve.
type ApprovalLine struct {
	VariantID int64            `json:"variant_id"`
	SKU       string           `json:"sku"`
	Quantity  int              `json:"quantity"`
	UnitPrice gocommerce.Money `json:"unit_price"`
}

// Approval is a basket waiting for an approver.
type Approval struct {
	ID            int64            `json:"id"`
	CompanyID     int64            `json:"company_id"`
	Kind          string           `json:"kind"`
	QuoteID       *int64           `json:"quote_id,omitempty"`
	Status        string           `json:"status"`
	RequestedBy   int64            `json:"requested_by"`
	RequesterMail string           `json:"requested_by_email"`
	Lines         []ApprovalLine   `json:"lines"`
	Subtotal      gocommerce.Money `json:"subtotal"`
	// Total is what the order came to when it was submitted, delivery and tax
	// included. Placing it re-prices delivery and tax as of the approval.
	Total         gocommerce.Money `json:"total"`
	PaymentMethod string           `json:"payment_method"`
	PONumber      string           `json:"po_number"`
	DecidedBy     *int64           `json:"decided_by,omitempty"`
	DecidedAt     *time.Time       `json:"decided_at,omitempty"`
	Reason        string           `json:"reason,omitempty"`
	LastError     string           `json:"last_error,omitempty"`
	OrderID       *int64           `json:"order_id,omitempty"`
	OrderNumber   string           `json:"order_number,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	UpdatedAt     time.Time        `json:"updated_at"`
}

// QuoteLine is one line of a quote. UnitPrice is absent until the merchant
// prices it.
type QuoteLine struct {
	VariantID int64             `json:"variant_id"`
	SKU       string            `json:"sku"`
	Title     string            `json:"title"`
	Quantity  int               `json:"quantity"`
	UnitPrice *gocommerce.Money `json:"unit_price"`
}

// Quote is a request for a price and, once sent, the price.
type Quote struct {
	ID            int64             `json:"id"`
	Number        string            `json:"number"`
	CompanyID     int64             `json:"company_id"`
	CompanyName   string            `json:"company_name"`
	Status        string            `json:"status"`
	RequestedBy   int64             `json:"requested_by"`
	RequesterMail string            `json:"requested_by_email"`
	Note          string            `json:"note"`
	Reply         string            `json:"reply"`
	DeclinedBy    string            `json:"declined_by,omitempty"`
	Lines         []QuoteLine       `json:"lines"`
	Total         *gocommerce.Money `json:"total"`
	ExpiresAt     *time.Time        `json:"expires_at,omitempty"`
	QuotedAt      *time.Time        `json:"quoted_at,omitempty"`
	AcceptedAt    *time.Time        `json:"accepted_at,omitempty"`
	OrderID       *int64            `json:"order_id,omitempty"`
	OrderNumber   string            `json:"order_number,omitempty"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

// quoteNumber is derived rather than stored: the id is already unique, and a
// second column could only disagree with it.
func quoteNumber(id int64) string { return fmt.Sprintf("Q-%06d", id) }

func (m *Module) money(amount int64) gocommerce.Money {
	return gocommerce.Money{AmountMinor: amount, Currency: m.app.Config().Currency}
}

func (m *Module) moneyPtr(v sql.NullInt64) *gocommerce.Money {
	if !v.Valid {
		return nil
	}
	mo := m.money(v.Int64)
	return &mo
}
