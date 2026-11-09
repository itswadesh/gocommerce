// Package digital sells files a buyer downloads once the order is paid (D254):
// ebooks, templates, presets, fonts, printables.
//
// A product is digital when it is not sent (the variant's requires_shipping is
// off) and it has files here. When its order is paid the module grants each
// paid line every file it has, hands those lines over through its own
// fulfillment provider so the order reads as fulfilled, and tells the buyer.
// The buyer reaches the files with the order's access token, through a link
// minted on click that lasts minutes; the bytes never sit at a URL of their own.
//
//	app, err := gocommerce.New(cfg, digital.New(digital.Config{}))
package digital

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Config configures the module. The zero value is the intended one.
type Config struct {
	// MaxFileBytes caps one file. Defaults to 100 MiB: the files are kept in
	// PostgreSQL, and a store selling video needs object storage instead.
	MaxFileBytes int64
	// LinkTTL is how long a download link lasts once minted. Defaults to ten
	// minutes: long enough for a slow phone to start, short enough that a
	// link pasted into a chat is dead by the time anyone else taps it.
	LinkTTL time.Duration
}

// ProviderCode is the fulfillment provider the module hands files over by.
const ProviderCode = "digital"

// EventReady is the module's own message: the buyer's downloads are open.
const EventReady = "digital.ready"

// Why a grant was closed.
const (
	reasonRefunded  = "refunded"
	reasonCancelled = "cancelled"
	reasonUnpaid    = "unpaid"
	reasonOperator  = "operator"
)

// Module sells downloads.
type Module struct {
	cfg Config
	app *gocommerce.App
	db  *sql.DB
	log *slog.Logger
	now func() time.Time
}

// New constructs the module.
func New(cfg Config) *Module {
	if cfg.MaxFileBytes <= 0 {
		cfg.MaxFileBytes = 100 << 20
	}
	if cfg.LinkTTL <= 0 {
		cfg.LinkTTL = 10 * time.Minute
	}
	return &Module{cfg: cfg, now: time.Now}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "digital" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{
		{
			ID: "0001_digital",
			SQL: `
-- product_id and variant_id are not foreign keys: a buyer keeps the files they
-- paid for after the product is deleted, and a cascade would take them away.
-- variant_id NULL is a file for every variant of the product.
CREATE TABLE digital_files (
    id           bigserial   PRIMARY KEY,
    product_id   bigint      NOT NULL,
    variant_id   bigint,
    filename     text        NOT NULL CHECK (filename <> ''),
    content_type text        NOT NULL,
    size_bytes   bigint      NOT NULL CHECK (size_bytes >= 0),
    sha256       text        NOT NULL,
    -- Replacing a file writes its chunks under the next version and drops the
    -- old ones, so a download that started before the swap fails rather than
    -- splicing two files together.
    version      integer     NOT NULL DEFAULT 1,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX digital_files_product_idx ON digital_files (product_id);

CREATE TABLE digital_chunks (
    file_id bigint  NOT NULL REFERENCES digital_files (id) ON DELETE CASCADE,
    version integer NOT NULL,
    seq     integer NOT NULL CHECK (seq >= 0),
    data    bytea   NOT NULL,
    PRIMARY KEY (file_id, version, seq)
);

-- Per product; no row is no limit and no expiry.
CREATE TABLE digital_rules (
    product_id     bigint      PRIMARY KEY,
    download_limit integer     CHECK (download_limit > 0),
    expiry_days    integer     CHECK (expiry_days > 0),
    updated_at     timestamptz NOT NULL DEFAULT now()
);

-- One file of one paid order line. The limit and expiry are copied from the
-- rule at payment: changing the rule later changes what new buyers get, not
-- what an earlier buyer was sold.
CREATE TABLE digital_grants (
    id              bigserial   PRIMARY KEY,
    order_id        bigint      NOT NULL,
    order_line_id   bigint      NOT NULL,
    file_id         bigint      NOT NULL REFERENCES digital_files (id) ON DELETE CASCADE,
    download_limit  integer     CHECK (download_limit > 0),
    downloads       integer     NOT NULL DEFAULT 0 CHECK (downloads >= 0),
    expires_at      timestamptz,
    revoked_at      timestamptz,
    revoked_reason  text        NOT NULL DEFAULT '',
    announced       boolean     NOT NULL DEFAULT false,
    -- The live link: its SHA-256, when it dies, and whether it has counted.
    link_hash       bytea       UNIQUE,
    link_expires_at timestamptz,
    link_counted    boolean     NOT NULL DEFAULT false,
    created_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (order_line_id, file_id)
);
CREATE INDEX digital_grants_order_idx ON digital_grants (order_id);`,
		},
		gocommerce.ScopeMigration("0002_store_scope", "digital_files", "digital_chunks", "digital_rules", "digital_grants"),
		// The link is a bearer credential, and the bytes are the thing being
		// sold: neither belongs in a report staff can run.
		gocommerce.WithholdFromReports("0003_withhold_from_reports", "digital_chunks", "digital_grants.link_hash"),
	}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	m.db = app.DB()
	m.log = app.Log()

	app.RegisterFulfillment(provider{m: m})
	app.RegisterCheckoutGuard(m.guard)
	app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
		Channel: gocommerce.ChannelEmail, Event: EventReady, Title: "Downloads ready",
		Description: "To the buyer when an order with files to download is paid.",
		Variables:   []string{"order_number", "customer_name", "downloads_url", "files_summary", "file_count"},
		Subject:     "Your downloads for order {{.order_number}} are ready",
		Body: `Hello {{.customer_name}},

Thanks for your order {{.order_number}}. Your files are ready to download:

{{.files_summary}}
{{if .downloads_url}}
Download them from your order page: {{.downloads_url}}{{else}}
Download them from your order page, from the link on your order confirmation.{{end}}`,
	})

	app.Subscribe(gocommerce.EventOrderPaid, m.onPaid)
	app.Subscribe(gocommerce.EventOrderCancelled, m.onClosed(reasonCancelled))
	app.Subscribe(gocommerce.EventOrderUnpaid, m.onClosed(reasonUnpaid))
	app.Subscribe(gocommerce.EventOrderRefunded, m.onRefunded)

	m.routes(app)

	// At-least-once delivery can still miss an event that was lost before the
	// module ran, and a paid buyer without their files is the worst outcome
	// here, so paid orders are checked at start as invoices does.
	app.OnStart(func(ctx context.Context) error { return app.ForEachStore(ctx, m.reconcile) })
	return nil
}

// ---------------------------------------------------------------- delivery

func (m *Module) onPaid(ctx context.Context, e gocommerce.Event) error {
	var ev gocommerce.OrderEvent
	if err := e.Decode(&ev); err != nil {
		return err
	}
	return m.deliver(ctx, ev.OrderID)
}

// deliver grants, hands over and announces. Each step is idempotent, because
// the event can arrive twice and reconcile can run beside it.
func (m *Module) deliver(ctx context.Context, orderID int64) error {
	var paid, sample bool
	err := m.db.QueryRowContext(ctx,
		`SELECT payment_status = 'paid', sample FROM orders WHERE id = $1`, orderID).Scan(&paid, &sample)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && (!paid || sample)) {
		return nil
	}
	if err != nil {
		return err
	}

	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		// Paid again after "unpaid" opens what that closed, and nothing else:
		// a refund or an operator's revoke stays.
		if _, err := tx.ExecContext(ctx, `
			UPDATE digital_grants SET revoked_at = NULL, revoked_reason = ''
			WHERE order_id = $1 AND revoked_reason = $2`, orderID, reasonUnpaid); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO digital_grants (order_id, order_line_id, file_id, download_limit, expires_at)
			SELECT l.order_id, l.id, f.id, r.download_limit,
			       CASE WHEN r.expiry_days IS NULL THEN NULL ELSE now() + make_interval(days => r.expiry_days) END
			FROM order_lines l
			JOIN digital_files f ON f.product_id = l.product_id AND (f.variant_id IS NULL OR f.variant_id = l.variant_id)
			LEFT JOIN digital_rules r ON r.product_id = l.product_id
			WHERE l.order_id = $1
			ON CONFLICT (store_id, order_line_id, file_id) DO NOTHING`, orderID)
		return err
	})
	if err != nil {
		return err
	}
	m.handOver(ctx, orderID)
	return m.announce(ctx, orderID)
}

// handOver ships the granted lines through the digital provider and marks the
// parcel delivered, so the order reads as fulfilled. A refusal is logged and
// not retried: the buyer already has the files, and an operator who shipped
// those lines by hand has said the same thing another way.
func (m *Module) handOver(ctx context.Context, orderID int64) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT l.id, l.quantity - coalesce((SELECT sum(fl.quantity) FROM fulfillment_lines fl
		                                    JOIN fulfillments f ON f.id = fl.fulfillment_id
		                                    WHERE fl.order_line_id = l.id AND f.status <> 'cancelled'), 0)
		FROM order_lines l
		JOIN variants v ON v.id = l.variant_id AND NOT v.requires_shipping
		WHERE l.order_id = $1 AND EXISTS (SELECT 1 FROM digital_grants g WHERE g.order_line_id = l.id)
		ORDER BY l.id`, orderID)
	if err != nil {
		m.log.Error("digital: read the lines to hand over", "order_id", orderID, "error", err)
		return
	}
	var lines []gocommerce.ShipLine
	for rows.Next() {
		var l gocommerce.ShipLine
		if err := rows.Scan(&l.OrderLineID, &l.Quantity); err != nil {
			rows.Close()
			m.log.Error("digital: read the lines to hand over", "order_id", orderID, "error", err)
			return
		}
		if l.Quantity > 0 {
			lines = append(lines, l)
		}
	}
	rows.Close()
	if len(lines) == 0 {
		return
	}
	order, err := m.app.Ship().Create(ctx, orderID, ProviderCode, gocommerce.ShipRequest{Lines: lines})
	if err != nil {
		m.log.Warn("digital: the files are granted but the lines could not be marked fulfilled", "order_id", orderID, "error", err)
		return
	}
	for _, f := range order.Fulfillments {
		if f.Provider == ProviderCode && f.Status == "shipped" {
			if _, err := m.app.Ship().Track(ctx, f.ID, gocommerce.TrackingUpdate{Status: gocommerce.TrackingDelivered}); err != nil {
				m.log.Warn("digital: could not mark the download delivered", "order_id", orderID, "error", err)
			}
		}
	}
}

// announce emails the buyer once, after the grants exist. The flag is set only
// once the message went, so a failed send is retried with the event.
func (m *Module) announce(ctx context.Context, orderID int64) error {
	var number, email, name, token string
	if err := m.db.QueryRowContext(ctx,
		`SELECT number, coalesce(email, ''), coalesce(name, ''), access_token FROM orders WHERE id = $1`, orderID).
		Scan(&number, &email, &name, &token); err != nil {
		return err
	}
	rows, err := m.db.QueryContext(ctx, `
		SELECT f.filename FROM digital_grants g JOIN digital_files f ON f.id = g.file_id
		WHERE g.order_id = $1 AND NOT g.announced AND g.revoked_at IS NULL ORDER BY g.id`, orderID)
	if err != nil {
		return err
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		names = append(names, "- "+n)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	if strings.TrimSpace(email) != "" {
		link := ""
		if base := m.app.StorefrontURL(ctx); base != "" {
			link = base + "/order-tracking?order=" + url.QueryEscape(number) + "&key=" + url.QueryEscape(token)
		}
		if err := m.app.Notify(ctx, gocommerce.Notification{
			Event: EventReady, Channel: gocommerce.ChannelEmail, To: email,
			Data: map[string]string{
				"order_number": number, "customer_name": name, "downloads_url": link,
				"files_summary": strings.Join(names, "\n"), "file_count": strconv.Itoa(len(names)),
			},
			// The link carries the order's access token.
			Secrets: []string{"downloads_url"},
		}); err != nil {
			return fmt.Errorf("digital: email the downloads for order %s: %w", number, err)
		}
	}
	_, err = m.db.ExecContext(ctx, `UPDATE digital_grants SET announced = true WHERE order_id = $1`, orderID)
	return err
}

func (m *Module) onClosed(reason string) gocommerce.EventHandler {
	return func(ctx context.Context, e gocommerce.Event) error {
		var ev gocommerce.OrderEvent
		if err := e.Decode(&ev); err != nil {
			return err
		}
		return m.revokeOrder(ctx, ev.OrderID, reason)
	}
}

// onRefunded closes the downloads once the whole order has gone back. A
// partial refund names an amount, not a line, so it closes nothing.
func (m *Module) onRefunded(ctx context.Context, e gocommerce.Event) error {
	var ev gocommerce.OrderEvent
	if err := e.Decode(&ev); err != nil {
		return err
	}
	if ev.PaymentStatus != gocommerce.PaymentRefunded {
		return nil
	}
	return m.revokeOrder(ctx, ev.OrderID, reasonRefunded)
}

func (m *Module) revokeOrder(ctx context.Context, orderID int64, reason string) error {
	_, err := m.db.ExecContext(ctx, `
		UPDATE digital_grants SET revoked_at = now(), revoked_reason = $2, link_hash = NULL
		WHERE order_id = $1 AND revoked_at IS NULL`, orderID, reason)
	return err
}

// reconcile delivers paid orders that bought files and were granted none —
// the event lost before the module ran. Only files that existed when the
// order was placed count, so uploading a file later does not mail every
// buyer the product ever had.
func (m *Module) reconcile(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `
		SELECT DISTINCT o.id FROM orders o
		JOIN order_lines l ON l.order_id = o.id
		JOIN digital_files f ON f.product_id = l.product_id AND (f.variant_id IS NULL OR f.variant_id = l.variant_id)
		WHERE o.payment_status = 'paid' AND NOT o.sample AND f.created_at <= o.created_at
		  AND NOT EXISTS (SELECT 1 FROM digital_grants g WHERE g.order_id = o.id)
		ORDER BY o.id LIMIT 500`)
	if err != nil {
		return err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := m.deliver(ctx, id); err != nil {
			m.log.Error("digital: could not deliver a paid order's files", "order_id", id, "error", err)
		}
	}
	return nil
}

// ---------------------------------------------------------------- checkout

// guard refuses cash on delivery for a basket with nothing to deliver: there
// is no doorstep to collect the cash at, and the files would wait on a payment
// that has no moment to happen. A mixed basket may still pay on delivery. An
// operator placing the order is let through: that is the seller who was paid
// by UPI or bank transfer, recording the sale to mark it paid.
func (m *Module) guard(ctx context.Context, a *gocommerce.CheckoutAttempt) error {
	if a.Method != gocommerce.CodeCOD || a.ByOperator || len(a.Lines) == 0 {
		return nil
	}
	ids := make([]int64, len(a.Lines))
	for i, l := range a.Lines {
		ids[i] = l.VariantID
	}
	var physical, files bool
	if err := a.Tx.QueryRowContext(ctx, `
		SELECT coalesce(bool_or(v.requires_shipping), false),
		       EXISTS (SELECT 1 FROM digital_files f WHERE f.product_id = ANY (SELECT product_id FROM variants WHERE id = ANY ($1)))
		FROM variants v WHERE v.id = ANY ($1)`, int64s(ids)).Scan(&physical, &files); err != nil {
		return err
	}
	if !physical && files {
		return gocommerce.Validationf("cash on delivery is not available for downloads; please pay online")
	}
	return nil
}

// ---------------------------------------------------------------- provider

// provider is how the module hands files over: no carrier, no label, and its
// own message to the buyer rather than the engine's "on its way".
type provider struct{ m *Module }

func (provider) Code() string          { return ProviderCode }
func (provider) AnnouncesItself() bool { return true }

// Ship refuses a line with no files granted, so the option cannot be used to
// mark a parcel sent that nobody sent.
func (p provider) Ship(ctx context.Context, o *gocommerce.Order, req gocommerce.ShipRequest) (gocommerce.Shipment, error) {
	ids := make([]int64, len(req.Lines))
	for i, l := range req.Lines {
		ids[i] = l.OrderLineID
	}
	var granted int
	if err := p.m.db.QueryRowContext(ctx, `
		SELECT count(DISTINCT g.order_line_id) FROM digital_grants g
		JOIN order_lines l ON l.id = g.order_line_id
		JOIN variants v ON v.id = l.variant_id AND NOT v.requires_shipping
		WHERE g.order_id = $1 AND g.order_line_id = ANY ($2)`, o.ID, int64s(ids)).Scan(&granted); err != nil {
		return gocommerce.Shipment{}, err
	}
	if granted != len(ids) {
		return gocommerce.Shipment{}, gocommerce.Validationf("only lines that are not sent and whose files have been granted are handed over as downloads")
	}
	return gocommerce.Shipment{Provider: ProviderCode}, nil
}

// int64s renders ids as a PostgreSQL array literal, which pgx's stdlib driver
// takes for a bigint[] parameter.
func int64s(ids []int64) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return "{" + strings.Join(parts, ",") + "}"
}
