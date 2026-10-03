// Package b2b sells to businesses: companies with buyers in roles, orders
// placed on account against a credit limit and payment terms, purchase-order
// numbers, approval before a junior buyer spends the company's money, and
// quotes a merchant prices for one buyer. A buyer can paste a whole order by
// SKU, repeat a past one, or check out part of a basket; and a supplier with a
// dealer network gives each dealer territories, so a consumer's enquiry goes
// to the dealer who covers them.
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
//
// Everything else that puts things in a basket — a pasted order, a repeat
// order, the part of a basket being checked out — does it through core's own
// Carts.AddLine on a basket priced as the buyer, so a line here costs what it
// costs anywhere else today, and is checked out through the same guard.
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
	EventLeadRouted       = "b2b.lead_routed"
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
	rightLeadsRead      gocommerce.Right = "leads.read"
	rightLeadsWrite     gocommerce.Right = "leads.write"
)

const (
	defaultInviteTTL      = 7 * 24 * time.Hour
	defaultQuoteTTL       = 30 * 24 * time.Hour
	defaultLeadsPerMinute = 10
	reconcileEvery        = time.Hour
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
	// LeadsPerMinute is how many enquiries one address may send through the
	// public dealer form in a minute. Zero means ten; a negative number turns
	// the limit off. It counts by the connection's peer address and never by
	// X-Forwarded-For, which anybody can write — so behind a reverse proxy
	// every visitor shares the proxy's budget, and this wants raising.
	LeadsPerMinute int
}

// Module is the b2b module.
type Module struct {
	cfg      Config
	app      *gocommerce.App
	db       *sql.DB
	accounts Accounts
	leads    *leadThrottle

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
	if cfg.LeadsPerMinute == 0 {
		cfg.LeadsPerMinute = defaultLeadsPerMinute
	}
	return &Module{cfg: cfg, accounts: cfg.Accounts, leads: newLeadThrottle(cfg.LeadsPerMinute, time.Minute)}
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
	}, {
		ID: "0002_b2b_partial_checkouts_and_dealers",
		SQL: `
			-- Part of a buyer's basket being checked out, through a basket built
			-- from the chosen lines so the rest stay where they were. The row is
			-- what makes that safe to retry and impossible to place twice: a
			-- retry under the same Idempotency-Key checks out the basket the
			-- first attempt built, which core then answers from the key, and only
			-- one partial checkout of a basket is in flight at a time — the
			-- guarantee core's row lock gives a checkout of the whole basket.
			--
			-- Both columns hold cart tokens. cart_token is kept whole because it
			-- is what a retry checks out; once placed, the basket it names is
			-- converted and nothing can change it.
			CREATE TABLE b2b_partial_checkouts (
			    id              bigserial   PRIMARY KEY,
			    customer_id     bigint      NOT NULL,
			    idempotency_key text,
			    source_cart     text        NOT NULL,
			    line_ids        jsonb       NOT NULL,
			    cart_token      text        NOT NULL DEFAULT '',
			    status          text        NOT NULL DEFAULT 'placing'
			                    CHECK (status IN ('placing', 'placed')),
			    created_at      timestamptz NOT NULL DEFAULT now(),
			    updated_at      timestamptz NOT NULL DEFAULT now()
			);
			CREATE UNIQUE INDEX b2b_partial_checkouts_idem_idx
			    ON b2b_partial_checkouts (customer_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
			CREATE UNIQUE INDEX b2b_partial_checkouts_source_idx
			    ON b2b_partial_checkouts (source_cart) WHERE status = 'placing';

			-- An area a dealer covers. The unique key is the area alone, not the
			-- area and the dealer: a territory has one dealer, so routing never
			-- has to choose between two who both claim the same postcode.
			--
			-- state and postal_prefix are '' rather than NULL so that key holds —
			-- NULLs are never equal, and two dealers could each claim the whole
			-- of a country. Both are stored normalised, upper case and a prefix
			-- without spaces or hyphens, and an enquiry's address is normalised
			-- the same way before it is matched. The state is upper-cased in Go
			-- and not checked here: outside ASCII, what upper() does depends on
			-- the database's locale, and a check that disagreed with Go would
			-- refuse a state Go had already normalised.
			CREATE TABLE b2b_territories (
			    id            bigserial   PRIMARY KEY,
			    company_id    bigint      NOT NULL REFERENCES b2b_companies (id) ON DELETE CASCADE,
			    country       char(2)     NOT NULL CHECK (country ~ '^[A-Z]{2}$'),
			    state         text        NOT NULL DEFAULT '',
			    postal_prefix text        NOT NULL DEFAULT '' CHECK (postal_prefix ~ '^[A-Z0-9]*$'),
			    created_at    timestamptz NOT NULL DEFAULT now(),
			    UNIQUE (country, state, postal_prefix)
			);
			CREATE INDEX b2b_territories_company_idx ON b2b_territories (company_id);

			-- An enquiry from the storefront's dealer form, and who has it.
			-- company_id NULL is the store's own; routed_by says how it got where
			-- it is — matched to a territory, handed over by the store, or with
			-- nobody yet. A deleted dealer's leads go back to the store, marked
			-- unrouted by DeleteCompany in the same transaction; the SET NULL is
			-- the backstop.
			--
			-- variant_id and product_id have no foreign key, as nothing in this
			-- module points into core: a product must stay deletable after
			-- somebody once asked about it.
			CREATE TABLE b2b_leads (
			    id          bigserial   PRIMARY KEY,
			    name        text        NOT NULL DEFAULT '',
			    email       text        NOT NULL DEFAULT '',
			    phone       text        NOT NULL DEFAULT '',
			    message     text        NOT NULL DEFAULT '',
			    country     text        NOT NULL DEFAULT '' CHECK (country ~ '^([A-Z]{2})?$'),
			    state       text        NOT NULL DEFAULT '',
			    postal_code text        NOT NULL DEFAULT '',
			    variant_id  bigint,
			    product_id  bigint,
			    status      text        NOT NULL DEFAULT 'new'
			                CHECK (status IN ('new', 'contacted', 'won', 'lost')),
			    company_id  bigint      REFERENCES b2b_companies (id) ON DELETE SET NULL,
			    routed_by   text        NOT NULL CHECK (routed_by IN ('territory', 'store', 'unrouted')),
			    source      text        NOT NULL DEFAULT '',
			    created_at  timestamptz NOT NULL DEFAULT now(),
			    updated_at  timestamptz NOT NULL DEFAULT now(),
			    CHECK (email <> '' OR phone <> '')
			);
			CREATE INDEX b2b_leads_company_idx ON b2b_leads (company_id, id DESC);
			CREATE INDEX b2b_leads_status_idx ON b2b_leads (status, id DESC);`,
	}, {
		ID: "0003_b2b_terms_history_and_account_entries",
		SQL: `
			-- Every change to a company's terms, written in the transaction that
			-- made it. A credit limit is the store lending money, and "who raised
			-- it, and when" is asked after something has gone wrong — when the
			-- only honest answer is one recorded at the time.
			--
			-- old_value and new_value are JSON so a value keeps its type and a
			-- limit taken off (JSON null) stays distinct from a row that had no
			-- earlier value at all: action 'created' rows carry SQL NULL there.
			-- actor_id is the operator's or the buyer's id, NULL for a token or
			-- the engine itself; actor_email is kept as it was, because the
			-- record must still say who after the account is gone.
			CREATE TABLE b2b_company_history (
			    id          bigserial   PRIMARY KEY,
			    company_id  bigint      NOT NULL REFERENCES b2b_companies (id) ON DELETE CASCADE,
			    field       text        NOT NULL,
			    action      text        NOT NULL CHECK (action IN ('created', 'changed')),
			    old_value   jsonb,
			    new_value   jsonb,
			    actor_kind  text        NOT NULL CHECK (actor_kind IN ('operator', 'token', 'buyer', 'system')),
			    actor_id    bigint,
			    actor_email text        NOT NULL DEFAULT '',
			    changed_at  timestamptz NOT NULL DEFAULT now()
			);
			CREATE INDEX b2b_company_history_company_idx ON b2b_company_history (company_id, id DESC);

			-- What happened to an order on account after it was placed, and
			-- when. Core keeps an order's payment status and no time it was paid,
			-- so a statement could not otherwise say when a debt was settled.
			--
			-- at is read from core's own record of the transition — the event
			-- written in the transaction that made it, delivered to this module
			-- or read back from the order's history — and date_source says so:
			-- 'recorded'. A transition with no surviving record is dated when this
			-- module first saw it and marked 'noticed': it happened at or before
			-- that moment, and the statement says which rather than guess.
			--
			-- The natural key is the order, the kind and the instant: the event
			-- and its audit row share their transaction's now(), so the event
			-- handler and a read of the history file one transition once.
			-- No foreign key onto orders, as b2b_orders has none.
			CREATE TABLE b2b_account_entries (
			    id          bigserial   PRIMARY KEY,
			    order_id    bigint      NOT NULL,
			    kind        text        NOT NULL CHECK (kind IN ('payment', 'payment_reversed', 'cancellation')),
			    at          timestamptz NOT NULL,
			    date_source text        NOT NULL CHECK (date_source IN ('recorded', 'noticed')),
			    event_id    text,
			    created_at  timestamptz NOT NULL DEFAULT now(),
			    UNIQUE (order_id, kind, at)
			);`,
	}, {
		ID: "0004_b2b_catalogues",
		SQL: `
			-- What a company may buy: categories, each with everything under it,
			-- and products one by one. A company with no catalogue buys
			-- everything, as before there were catalogues.
			--
			-- category_id and product_id have no foreign key, for the reason
			-- group_id has none: a module's table must never refuse a core
			-- delete. A deleted category or product simply allows nothing, and
			-- the subtree is read from core's tree as it is at the moment of the
			-- check, so moving a category moves its products in or out.
			CREATE TABLE b2b_catalogues (
			    id          bigserial   PRIMARY KEY,
			    name        text        NOT NULL UNIQUE CHECK (name <> ''),
			    description text        NOT NULL DEFAULT '',
			    created_at  timestamptz NOT NULL DEFAULT now(),
			    updated_at  timestamptz NOT NULL DEFAULT now()
			);
			CREATE TABLE b2b_catalogue_categories (
			    catalogue_id bigint NOT NULL REFERENCES b2b_catalogues (id) ON DELETE CASCADE,
			    category_id  bigint NOT NULL,
			    PRIMARY KEY (catalogue_id, category_id)
			);
			CREATE TABLE b2b_catalogue_products (
			    catalogue_id bigint NOT NULL REFERENCES b2b_catalogues (id) ON DELETE CASCADE,
			    product_id   bigint NOT NULL,
			    PRIMARY KEY (catalogue_id, product_id)
			);

			-- No ON DELETE: a catalogue a company is held to is refused deletion
			-- rather than dropped from under it, which would quietly let the
			-- company buy everything.
			ALTER TABLE b2b_companies ADD COLUMN catalogue_id bigint REFERENCES b2b_catalogues (id);
			CREATE INDEX b2b_companies_catalogue_idx ON b2b_companies (catalogue_id)
			    WHERE catalogue_id IS NOT NULL;`,
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
	// The three transitions that move what a company owes after its order is
	// placed. Each is filed with the time core wrote it, which is the only
	// record of when an order on account was paid (statement.go).
	for _, name := range []string{gocommerce.EventOrderPaid, gocommerce.EventOrderUnpaid, gocommerce.EventOrderCancelled} {
		app.Subscribe(name, m.onAccountEvent)
	}
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
	// Rights of their own rather than companies.*: a lead is a consumer's
	// name, address and phone number, not a business customer's terms, and a
	// store may well want somebody triaging enquiries who must not see what
	// its dealers owe (D65).
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightLeadsRead,
		Label:   "See dealer leads",
		Scope:   "Enquiries from the storefront's dealer form, who they were routed to and how they stand",
		Default: []string{gocommerce.RoleManager, gocommerce.RoleStaff},
	})
	// Manager only: handing a lead to a dealer sends a member of the public's
	// contact details to another business.
	app.RegisterRight(gocommerce.RightSpec{
		Right:   rightLeadsWrite,
		Label:   "Route dealer leads",
		Scope:   "Hand an enquiry to a dealer, take it back, or change how it stands",
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
	ApprovalThreshold *gocommerce.Money `json:"approval_threshold"`
	RequirePO         bool              `json:"require_po"`
	// CatalogueID absent means the company may buy everything; CatalogueName
	// names the one it is held to.
	CatalogueID   *int64              `json:"catalogue_id"`
	CatalogueName string              `json:"catalogue_name,omitempty"`
	Notes         string              `json:"notes"`
	Metadata      gocommerce.Metadata `json:"metadata"`
	MemberCount   int                 `json:"member_count"`
	// TerritoryCount is how many areas it serves as a dealer; zero is a
	// company that is not one, or not yet.
	TerritoryCount int       `json:"territory_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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
	// ApprovalID and QuoteID are the request an approver said yes to and the
	// quote the order was placed from, when it came either way.
	ApprovalID *int64    `json:"approval_id,omitempty"`
	QuoteID    *int64    `json:"quote_id,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
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

// Territory is an area one dealer covers. An empty State or PostalPrefix
// covers all of it: {US, CA, ""} is the whole of California.
type Territory struct {
	ID           int64     `json:"id"`
	CompanyID    int64     `json:"company_id"`
	CompanyName  string    `json:"company_name"`
	Country      string    `json:"country"`
	State        string    `json:"state"`
	PostalPrefix string    `json:"postal_prefix"`
	CreatedAt    time.Time `json:"created_at"`
}

// Lead is a consumer's enquiry from the storefront's dealer form, and the
// dealer it went to. CompanyID absent is the store's own.
type Lead struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	Message    string `json:"message"`
	Country    string `json:"country"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
	VariantID  *int64 `json:"variant_id"`
	ProductID  *int64 `json:"product_id"`
	// ProductTitle and VariantSKU name what the enquiry was about, read from
	// the catalogue as it is now; both are absent once it has been deleted.
	ProductTitle string    `json:"product_title,omitempty"`
	VariantSKU   string    `json:"variant_sku,omitempty"`
	Status       string    `json:"status"`
	CompanyID    *int64    `json:"company_id"`
	CompanyName  string    `json:"company_name,omitempty"`
	RoutedBy     string    `json:"routed_by"`
	Source       string    `json:"source"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
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
