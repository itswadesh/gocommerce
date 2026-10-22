package telegramalerts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Delivery is one alert on its way to the owner's chat.
type Delivery struct {
	ID        int64  `json:"id"`
	EventID   string `json:"event_id"`
	EventName string `json:"event_name"`
	OrderID   int64  `json:"order_id,omitempty"`
	// OrderNumber is what the panel and the shopper call the order.
	OrderNumber string `json:"order_number,omitempty"`
	// State is derived from the worker's own columns, so there is no second
	// place for it to disagree with them: pending, delivered, or dead once
	// Telegram refused it or the attempts ran out.
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

// onEvent words one event's alert and queues it, and returns.
//
// It sends nothing, and that is the design (D51): a Telegram that is slow or
// down must not hold the outbox, and an error here would ask the outbox to run
// every other consumer of the event again. So the only error it returns is a
// database one, where nothing was written and a second try is exactly right.
func (m *Module) onEvent(ctx context.Context, e gocommerce.Event) error {
	if !m.enabled(ctx) {
		return nil
	}
	a, err := m.alert(ctx, e.Name)
	if err != nil {
		return err
	}
	if !a.Active {
		return nil
	}
	var ev gocommerce.OrderEvent
	if err := e.Decode(&ev); err != nil {
		// Retrying will not make it readable, and the event's other consumers
		// have nothing to do with it.
		m.log.Error("telegram alert: the event cannot be read", "event", e.Name, "event_id", e.ID, "error", err)
		return nil
	}

	text, renderErr := render(a.Template, m.alertData(ctx, &ev))
	if renderErr == nil && text == "" {
		return nil
	}
	// Wording that will not render is queued dead, with the reason, rather
	// than dropped: the owner finds out why an order did not reach them on
	// the screen where they wrote it.
	dead, lastError := false, ""
	if renderErr != nil {
		dead, lastError = true, "the alert's wording does not render: "+renderErr.Error()
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO telegram_alerts_deliveries (event_id, event_name, order_id, order_number, text, dead, last_error)
		VALUES ($1, $2, nullif($3::bigint, 0), $7, $4, $5, nullif($6, ''))
		ON CONFLICT (store_id, event_id) DO NOTHING`,
		e.ID, e.Name, e.AggregateID, text, dead, lastError, ev.Number); err != nil {
		return err
	}
	m.nudge()
	return nil
}

func (m *Module) nudge() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

type pending struct {
	id        int64
	eventName string
	text      string
	attempts  int
}

// DeliverPass sends one batch of this store's alerts and returns how many it
// attempted. Exported for the reason the engine exports DrainOutbox: a test,
// and an operator in a hurry, can run a pass rather than wait for the ticker.
func (m *Module) DeliverPass(ctx context.Context) (int, error) {
	// Switched off, nothing goes — and what is queued waits for the switch
	// rather than being thrown away.
	if !m.enabled(ctx) {
		return 0, nil
	}
	rows, err := m.db.QueryContext(ctx, `
		WITH claimed AS MATERIALIZED (
			SELECT id FROM telegram_alerts_deliveries
			WHERE delivered_at IS NULL AND NOT dead AND available_at <= now()
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE telegram_alerts_deliveries d
		SET attempts = d.attempts + 1,
		    available_at = now() + make_interval(secs => $2)
		FROM claimed
		WHERE d.id = claimed.id
		RETURNING d.id, d.event_name, d.text, d.attempts`,
		deliverBatch, visibility.Seconds())
	if err != nil {
		return 0, fmt.Errorf("claim telegram alerts: %w", err)
	}
	var batch []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.eventName, &p.text, &p.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, p)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()

	// Read once per pass, not per alert: the settings cannot be half-changed
	// halfway through a batch.
	b := m.bot(ctx)
	for _, p := range batch {
		err := m.sendTo(ctx, b, p.text)
		if err == nil {
			if markErr := m.markDelivered(ctx, p.id); markErr != nil {
				return len(batch), markErr
			}
			continue
		}
		if markErr := m.markFailed(ctx, p, err); markErr != nil {
			return len(batch), markErr
		}
	}
	return len(batch), nil
}

func (m *Module) markDelivered(ctx context.Context, id int64) error {
	_, err := m.db.ExecContext(ctx, `
		UPDATE telegram_alerts_deliveries
		SET delivered_at = now(), last_status = 200, last_error = NULL
		WHERE id = $1`, id)
	return err
}

// markFailed schedules the next try, or gives up: at once when Telegram said
// no, after maxAttempts when it could not be reached.
func (m *Module) markFailed(ctx context.Context, p pending, cause error) error {
	var te *telegramError
	if !errors.As(cause, &te) {
		te = &telegramError{description: cause.Error()}
	}
	reason := explain(te)
	if te.refused() || p.attempts >= maxAttempts {
		_, err := m.db.ExecContext(ctx, `
			UPDATE telegram_alerts_deliveries
			SET dead = true, last_status = nullif($2, 0), last_error = $3
			WHERE id = $1`, p.id, te.status, reason)
		if err == nil {
			store, _ := gocommerce.StoreFrom(ctx)
			m.log.Warn("telegram alert gave up", "store", store, "delivery", p.id,
				"event", p.eventName, "attempts", p.attempts, "reason", reason)
		}
		return err
	}
	wait := backoff(p.attempts)
	if te.retryAfter > wait {
		wait = te.retryAfter
	}
	_, err := m.db.ExecContext(ctx, `
		UPDATE telegram_alerts_deliveries
		SET available_at = now() + make_interval(secs => $2),
		    last_status = nullif($3, 0), last_error = $4
		WHERE id = $1`, p.id, wait.Seconds(), te.status, reason)
	return err
}

// backoff doubles from two seconds, capped, as the outbox's does.
func backoff(attempts int) time.Duration {
	d := time.Duration(math.Pow(2, float64(attempts))) * time.Second
	if d > maxBackoff || d <= 0 {
		return maxBackoff
	}
	return d
}

// start launches the worker and returns: OnStart runs before the listener
// accepts traffic, so a hook that loops is a store that never serves.
func (m *Module) start(ctx context.Context) error {
	go m.run(ctx)
	return nil
}

func (m *Module) run(ctx context.Context) {
	t := time.NewTicker(m.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.wake:
		}
		// One pass per store, each bound to its own (D83).
		err := m.app.ForEachStore(ctx, func(ctx context.Context) error {
			_, err := m.DeliverPass(ctx)
			return err
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			m.log.Error("telegram alert pass failed", "error", err)
		}
	}
}

// ------------------------------------------------------------------ the log

const deliveryColumns = `id, event_id, event_name, coalesce(order_id, 0), order_number, attempts,
	coalesce(last_status, 0), coalesce(last_error, ''), available_at, delivered_at, dead, created_at`

func scanDelivery(row interface{ Scan(...any) error }) (*Delivery, error) {
	var d Delivery
	var dead bool
	if err := row.Scan(&d.ID, &d.EventID, &d.EventName, &d.OrderID, &d.OrderNumber, &d.Attempts,
		&d.LastStatus, &d.LastError, &d.AvailableAt, &d.DeliveredAt, &dead, &d.CreatedAt); err != nil {
		return nil, err
	}
	switch {
	case d.DeliveredAt != nil:
		d.State = StateDelivered
	case dead:
		d.State = StateDead
	default:
		d.State = StatePending
	}
	return &d, nil
}

// handleDeliveries is the log, newest first. Without the message text: it
// carries a customer's name, and the log's question is whether alerts are
// arriving, not what they said.
func (m *Module) handleDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	where := "TRUE"
	switch state := r.URL.Query().Get("state"); state {
	case "":
	case StatePending:
		where = "delivered_at IS NULL AND NOT dead"
	case StateDelivered:
		where = "delivered_at IS NOT NULL"
	case StateDead:
		where = "dead"
	default:
		gocommerce.RespondError(w, r, gocommerce.Validationf(
			"state must be %q, %q or %q", StatePending, StateDelivered, StateDead))
		return
	}
	var total int
	if err := m.db.QueryRowContext(r.Context(),
		`SELECT count(*) FROM telegram_alerts_deliveries WHERE `+where).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "count telegram alerts"))
		return
	}
	rows, err := m.db.QueryContext(r.Context(), `SELECT `+deliveryColumns+
		` FROM telegram_alerts_deliveries WHERE `+where+` ORDER BY id DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list telegram alerts"))
		return
	}
	defer rows.Close()
	out := []*Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list telegram alerts"))
			return
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list telegram alerts"))
		return
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

// handleRetry puts an alert back in the queue with a fresh set of attempts:
// one the owner asked for after fixing the chat is a new start, not attempt
// thirteen of twelve.
func (m *Module) handleRetry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the id must be a number"))
		return
	}
	res, err := m.db.ExecContext(r.Context(), `
		UPDATE telegram_alerts_deliveries
		SET dead = false, attempts = 0, available_at = now(), last_error = NULL
		WHERE id = $1 AND delivered_at IS NULL`, id)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "retry the telegram alert"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no undelivered Telegram alert %d", id))
		return
	}
	m.nudge()
	gocommerce.Respond(w, http.StatusOK, map[string]any{"queued": true})
}
