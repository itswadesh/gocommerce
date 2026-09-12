// Package webhooks delivers this store's events to somebody else's server.
//
// The engine's events are durable and in-process; this is the door for a
// consumer that is not written in Go. An operator registers a URL and the
// events it cares about, and every matching event is POSTed to it, signed, with
// retries and a delivery log an operator can read.
//
// Why a second delivery table rather than the core outbox (D51): the outbox
// retries an *event*, not a recipient. `eventBus.dispatch` joins every
// subscriber's failures and the outbox re-runs the whole event, so reporting
// one unreachable merchant from the subscriber would re-issue invoices, re-fire
// notifications and re-POST to the endpoints that already succeeded. And
// `runHandler` bounds each handler with a timeout, so waiting on somebody
// else's HTTP inside the dispatcher stalls every consumer queued behind it.
//
// So the subscriber does one thing — write a row per matching endpoint and
// return — and a worker owns the sending. The shape of that worker is the
// outbox's own, deliberately: same claim, same backoff, same dead-lettering.
package webhooks

import (
	"database/sql"
	"log/slog"
	"net/url"
	"strings"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

// Delivery policy. The numbers are core's, because a merchant's server being
// down for an afternoon is the same problem as a consumer being down for one,
// and answering it differently would only be a second thing to remember.
const (
	maxAttempts    = 12
	maxBackoff     = 15 * time.Minute
	visibility     = 60 * time.Second
	deliverBatch   = 20
	pollInterval   = 5 * time.Second
	defaultTimeout = 10 * time.Second
)

// Config is what the store tells this module.
type Config struct {
	// Timeout bounds one POST to one endpoint. Deliberately short: a merchant
	// that takes longer than this to acknowledge a webhook is a merchant whose
	// handler is doing work it should have queued, and waiting on it holds a
	// worker slot the rest of the queue needs.
	Timeout time.Duration
}

// Endpoint is somewhere this store sends its events.
type Endpoint struct {
	ID     int64    `json:"id"`
	URL    string   `json:"url"`
	Events []string `json:"events"`
	Active bool     `json:"active"`
	// Secret is returned in full exactly twice in an endpoint's life: by the
	// call that creates it and by the one that rotates it. A store has to hold
	// it recoverably — it signs with it — but there is no reason to hand it back
	// on every read, and every read that does is another copy in a log.
	Secret string `json:"secret,omitempty"`
	// SecretHint is the last four characters, which is enough to tell two
	// endpoints apart in a list and not enough to sign anything.
	SecretHint string    `json:"secret_hint,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// Delivery is one attempt to tell one endpoint about one event.
type Delivery struct {
	ID         int64  `json:"id"`
	EndpointID int64  `json:"endpoint_id"`
	EventID    string `json:"event_id"`
	EventName  string `json:"event_name"`
	// State is derived rather than stored: pending, delivered or dead. The
	// columns it reads are the ones the worker writes, so there is no second
	// place for it to disagree with.
	State       string     `json:"state"`
	Attempts    int        `json:"attempts"`
	LastStatus  int        `json:"last_status,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	AvailableAt time.Time  `json:"available_at"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
}

// Delivery states.
const (
	StatePending   = "pending"
	StateDelivered = "delivered"
	StateDead      = "dead"
)

// Module is the webhook sender.
type Module struct {
	cfg  Config
	db   *sql.DB
	app  *gocommerce.App
	log  *slog.Logger
	poll time.Duration
}

// New constructs the module.
func New(cfg Config) *Module { return &Module{cfg: cfg} }

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "webhooks" }

// Migrations implements gocommerce.Module.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_endpoints",
		SQL: `
CREATE TABLE webhook_endpoints (
    id         bigserial PRIMARY KEY,
    url        text NOT NULL,
    secret     text NOT NULL,
    events     text[] NOT NULL CHECK (cardinality(events) > 0),
    active     boolean NOT NULL DEFAULT true,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- One row per endpoint per event. The columns after payload are the worker's,
-- and they are the outbox's columns by the same names, because this is the
-- outbox's problem one layer out: claim a batch, back off, give up visibly.
CREATE TABLE webhook_deliveries (
    id           bigserial PRIMARY KEY,
    endpoint_id  bigint NOT NULL REFERENCES webhook_endpoints(id) ON DELETE CASCADE,
    event_id     text NOT NULL,
    event_name   text NOT NULL,
    payload      jsonb NOT NULL,
    attempts     integer NOT NULL DEFAULT 0,
    available_at timestamptz NOT NULL DEFAULT now(),
    delivered_at timestamptz,
    dead         boolean NOT NULL DEFAULT false,
    last_status  integer,
    last_error   text,
    created_at   timestamptz NOT NULL DEFAULT now(),

    -- An event reaching one endpoint twice is the outbox's at-least-once
    -- guarantee arriving here, and a redelivery must not become a second POST.
    UNIQUE (endpoint_id, event_id)
);

-- The worker's claim: undelivered, not dead, due now, oldest first.
CREATE INDEX webhook_deliveries_claim
    ON webhook_deliveries (available_at)
    WHERE delivered_at IS NULL AND NOT dead;

CREATE INDEX webhook_deliveries_endpoint ON webhook_deliveries (endpoint_id, id DESC);`,
	}}
}

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	if m.cfg.Timeout <= 0 {
		m.cfg.Timeout = defaultTimeout
	}
	m.db = app.DB()
	m.app = app
	m.log = app.Log()
	m.poll = pollInterval

	// "*" rather than a list: which events an endpoint wants is the endpoint's
	// business and it is stored per row, so filtering here would only mean two
	// places to keep in step.
	app.Subscribe("*", m.onEvent)

	// store.operate, the right D49 gave the outbox screen. A webhook endpoint
	// is the same kind of thing: it is not a part of the catalogue or the
	// orders, it is how this store is wired to the outside.
	app.HandleAdminFunc("POST /api/admin/x/webhooks/endpoints", m.handleCreate, gocommerce.RightStoreOperate)
	app.HandleAdminFunc("GET /api/admin/x/webhooks/endpoints", m.handleList, gocommerce.RightStoreOperate)
	app.HandleAdminFunc("GET /api/admin/x/webhooks/endpoints/{id}", m.handleGet, gocommerce.RightStoreOperate)
	app.HandleAdminFunc("PATCH /api/admin/x/webhooks/endpoints/{id}", m.handleUpdate, gocommerce.RightStoreOperate)
	app.HandleAdminFunc("DELETE /api/admin/x/webhooks/endpoints/{id}", m.handleDelete, gocommerce.RightStoreOperate)
	app.HandleAdminFunc("POST /api/admin/x/webhooks/endpoints/{id}/rotate-secret", m.handleRotate, gocommerce.RightStoreOperate)
	app.HandleAdminFunc("GET /api/admin/x/webhooks/deliveries", m.handleDeliveries, gocommerce.RightStoreOperate)
	app.HandleAdminFunc("POST /api/admin/x/webhooks/deliveries/{id}/retry", m.handleRetry, gocommerce.RightStoreOperate)

	// The sending is background work, and OnStart is where background work
	// belongs: it runs once the store is ready to serve, and its context is
	// cancelled on shutdown.
	app.OnStart(m.start)

	return nil
}

// validURL refuses what cannot be posted to. Scheme and host only: whether the
// far end answers is the delivery log's question, not this one's.
func validURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return gocommerce.Validationf("url is not a URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return gocommerce.Validationf("url must be http or https")
	}
	if u.Host == "" {
		return gocommerce.Validationf("url must include a host")
	}
	return nil
}

func cleanEvents(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, e := range in {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, gocommerce.Validationf("events must name at least one event, such as \"order.*\"")
	}
	return out, nil
}

// hint is the tail of a secret: enough to recognise, useless to sign with.
func hint(secret string) string {
	if len(secret) <= 4 {
		return secret
	}
	return secret[len(secret)-4:]
}
