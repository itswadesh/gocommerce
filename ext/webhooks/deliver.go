package webhooks

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

// onEvent turns one event into one row per endpoint that asked for it, and
// returns.
//
// It does not send anything, and that is the whole design (D51). Returning an
// error here would ask the core outbox to retry the event, and the outbox
// retries an event rather than a recipient: every other subscriber would run
// again, and every endpoint that already received this event would receive it
// twice. So the only failure this reports is one that means the row was not
// written at all — a database problem, where retrying the event is exactly
// right because nothing downstream happened either.
func (m *Module) onEvent(ctx context.Context, e gocommerce.Event) error {
	payload, err := json.Marshal(envelope{
		ID: e.ID, Name: e.Name, Version: e.Version, At: e.At,
		AggregateType: e.AggregateType, AggregateID: e.AggregateID,
		Data: e.Data,
	})
	if err != nil {
		return err
	}

	// One statement: the endpoints that want this event, and a delivery row for
	// each. Matching lives in SQL because the alternative is reading every
	// endpoint into Go on every event.
	//
	// ON CONFLICT DO NOTHING makes a redelivery of the same event idempotent,
	// which it has to be: the outbox's guarantee is at-least-once, and a second
	// dispatch of one event must not become a second POST.
	_, err = m.db.ExecContext(ctx, `
		INSERT INTO webhook_deliveries (endpoint_id, event_id, event_name, payload)
		SELECT e.id, $1, $2, $3::jsonb
		FROM webhook_endpoints e
		WHERE e.active AND EXISTS (
		    SELECT 1 FROM unnest(e.events) AS pattern
		    WHERE pattern = '*'
		       OR pattern = $2
		       OR (right(pattern, 2) = '.*' AND $2 LIKE left(pattern, -1) || '%')
		)
		ON CONFLICT (endpoint_id, event_id) DO NOTHING`,
		e.ID, e.Name, payload)
	return err
}

// envelope is what a merchant's server receives. It is the engine's own event
// shape rather than a translation of it: a consumer reading `/doc` for the
// event contract should find the same field names arriving in the body.
type envelope struct {
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	Version       int             `json:"v"`
	At            time.Time       `json:"at"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   int64           `json:"aggregate_id"`
	Data          json.RawMessage `json:"data"`
}

const deliveryColumns = `id, endpoint_id, event_id, event_name, attempts,
	coalesce(last_status, 0), coalesce(last_error, ''), available_at, delivered_at, dead, created_at`

func scanDelivery(row interface{ Scan(...any) error }) (*Delivery, error) {
	var d Delivery
	var dead bool
	if err := row.Scan(&d.ID, &d.EndpointID, &d.EventID, &d.EventName, &d.Attempts,
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

func (m *Module) handleDeliveries(w http.ResponseWriter, r *http.Request) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	where := "TRUE"
	args := []any{}
	if raw := r.URL.Query().Get("endpoint_id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			gocommerce.RespondError(w, r, gocommerce.Validationf("endpoint_id must be an integer"))
			return
		}
		args = append(args, id)
		where += " AND endpoint_id = $" + strconv.Itoa(len(args))
	}
	// The question an operator actually asks of this screen is "what is not
	// getting through", so state is the filter that matters.
	switch state := r.URL.Query().Get("state"); state {
	case "":
	case StatePending:
		where += " AND delivered_at IS NULL AND NOT dead"
	case StateDelivered:
		where += " AND delivered_at IS NOT NULL"
	case StateDead:
		where += " AND dead"
	default:
		gocommerce.RespondError(w, r, gocommerce.Validationf(
			"state must be %q, %q or %q", StatePending, StateDelivered, StateDead))
		return
	}

	var total int
	if err := m.db.QueryRowContext(r.Context(),
		`SELECT count(*) FROM webhook_deliveries WHERE `+where, args...).Scan(&total); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(r.Context(),
		`SELECT `+deliveryColumns+` FROM webhook_deliveries WHERE `+where+
			` ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)),
		args...)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()

	out := []*Delivery{}
	for rows.Next() {
		d, err := scanDelivery(rows)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}
