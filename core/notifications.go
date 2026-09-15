package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Every message the store sends a shopper leaves a row here: which event,
// which channel, to whom, through which backend, and whether it went. The
// question that brings an operator to this table is "did she get her
// confirmation" — and until this existed the only answer was a log line on
// a server the operator cannot read, or a shopper on the phone.
//
// Recorded at the one funnel every delivery passes through, notifierSet.send,
// after the backends have answered, and never in the way of them: a row that
// could not be written is logged and the delivery stands.

// Notification statuses.
const (
	// NotificationSent went out through a real backend.
	NotificationSent = "sent"
	// NotificationLogged found no real backend and was written to the log —
	// what a store without an email module sees on every order.
	NotificationLogged = "logged"
	// NotificationFailed was refused by a backend; the error is on the row.
	NotificationFailed = "failed"
)

// NotificationRecord is one message as the store remembers sending it.
type NotificationRecord struct {
	ID    int64  `json:"id"`
	Event string `json:"event"`
	// Title is what the event's template calls the message ("Order shipped"),
	// filled on read; "" for an event nobody registered wording for.
	Title       string            `json:"title,omitempty"`
	Channel     string            `json:"channel"`
	To          string            `json:"to"`
	Language    string            `json:"language,omitempty"`
	OrderNumber string            `json:"order_number,omitempty"`
	Backend     string            `json:"backend,omitempty"`
	Status      string            `json:"status"`
	Error       string            `json:"error,omitempty"`
	Data        map[string]string `json:"data"`
	ResendOf    *int64            `json:"resend_of,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
}

// NotificationQuery narrows the list.
type NotificationQuery struct {
	Channel string
	Status  string
	Event   string
	// Search matches the recipient or the order number.
	Search string
	Limit  int
	Offset int
}

// notificationOutcome is what the funnel learned from the backends.
type notificationOutcome struct {
	backends  []string
	delivered bool
	resendOf  *int64
}

// Notifications is the log and the resend.
type Notifications struct {
	app *App
}

// Notifications returns the notification log.
func (a *App) Notifications() *Notifications { return a.notifications }

const notificationColumns = `id, event, channel, recipient, language, order_number, backend, status, error, data, resend_of, created_at`

// record is the funnel's callback: one row per delivery, whatever happened.
// Written on a context that outlives the delivery's, because the outbox
// cancels a handler's context on timeout and a row saying "failed: timed
// out" is exactly the one that must not be lost to the timeout.
func (s *Notifications) record(ctx context.Context, note Notification, outcome notificationOutcome, sendErr error) {
	status := NotificationLogged
	switch {
	case sendErr != nil:
		status = NotificationFailed
	case outcome.delivered:
		status = NotificationSent
	}
	message := ""
	if sendErr != nil {
		message = sendErr.Error()
		if len(message) > 2000 {
			message = message[:2000]
		}
	}
	data, err := json.Marshal(note.Data)
	if err != nil {
		data = []byte("{}")
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := s.app.db.ExecContext(ctx, `
		INSERT INTO notifications (event, channel, recipient, language, order_number, backend, status, error, data, resend_of)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		note.Event, note.Channel, note.To, note.Language, note.Data["order_number"],
		strings.Join(outcome.backends, ", "), status, message, data, outcome.resendOf); err != nil {
		s.app.log.Error("notification not recorded", "event", note.Event, "channel", note.Channel, "error", err)
	}
}

// List is the log, newest first.
func (s *Notifications) List(ctx context.Context, q NotificationQuery) ([]*NotificationRecord, int, error) {
	where, args := []string{"1 = 1"}, []any{}
	add := func(expr string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(expr, len(args)))
	}
	if q.Channel != "" {
		add("channel = $%d", q.Channel)
	}
	if q.Status != "" {
		add("status = $%d", q.Status)
	}
	if q.Event != "" {
		add("event = $%d", q.Event)
	}
	if s := strings.TrimSpace(q.Search); s != "" {
		args = append(args, "%"+strings.ToLower(s)+"%", s)
		where = append(where, fmt.Sprintf("(lower(recipient) LIKE $%d OR order_number = $%d)", len(args)-1, len(args)))
	}
	clause := strings.Join(where, " AND ")

	var total int
	if err := s.app.db.QueryRowContext(ctx, `SELECT count(*) FROM notifications WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, Internalf(err, "count notifications")
	}
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	args = append(args, limit, q.Offset)
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT `+notificationColumns+` FROM notifications WHERE `+clause+
			fmt.Sprintf(` ORDER BY id DESC LIMIT $%d OFFSET $%d`, len(args)-1, len(args)), args...)
	if err != nil {
		return nil, 0, Internalf(err, "list notifications")
	}
	defer rows.Close()
	out := []*NotificationRecord{}
	for rows.Next() {
		rec, err := scanNotification(rows)
		if err != nil {
			return nil, 0, err
		}
		rec.Title = s.app.notifyTemplates.title(rec.Channel, rec.Event)
		out = append(out, rec)
	}
	return out, total, rows.Err()
}

// Get is one row.
func (s *Notifications) Get(ctx context.Context, id int64) (*NotificationRecord, error) {
	rows, err := s.app.db.QueryContext(ctx, `SELECT `+notificationColumns+` FROM notifications WHERE id = $1`, id)
	if err != nil {
		return nil, Internalf(err, "read notification")
	}
	defer rows.Close()
	if !rows.Next() {
		return nil, NotFoundf("notification %d does not exist", id)
	}
	rec, err := scanNotification(rows)
	if err != nil {
		return nil, err
	}
	rec.Title = s.app.notifyTemplates.title(rec.Channel, rec.Event)
	return rec, nil
}

// Resend delivers a recorded message again, through whatever backends the
// store has now — which is the point: the one that failed last week may be
// fixed, and the one that only reached the log may have an email module
// behind it today. The new attempt is its own row, pointing at this one.
func (s *Notifications) Resend(ctx context.Context, id int64) (*NotificationRecord, error) {
	rec, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	note := Notification{Event: rec.Event, Channel: rec.Channel, To: rec.To, Language: rec.Language, Data: rec.Data}
	sendErr := s.app.notifier.sendFrom(ctx, note, &rec.ID)
	// The row the funnel just wrote is the answer, whichever way it went.
	var latest NotificationRecord
	row := s.app.db.QueryRowContext(ctx,
		`SELECT `+notificationColumns+` FROM notifications WHERE resend_of = $1 ORDER BY id DESC LIMIT 1`, rec.ID)
	if err := scanNotificationRow(row, &latest); err != nil {
		if sendErr != nil {
			return nil, Internalf(sendErr, "resend")
		}
		return nil, err
	}
	latest.Title = s.app.notifyTemplates.title(latest.Channel, latest.Event)
	return &latest, nil
}

type notificationScanner interface {
	Scan(dest ...any) error
}

func scanNotification(rows *sql.Rows) (*NotificationRecord, error) {
	var rec NotificationRecord
	if err := scanNotificationRow(rows, &rec); err != nil {
		return nil, err
	}
	return &rec, nil
}

func scanNotificationRow(sc notificationScanner, rec *NotificationRecord) error {
	var raw []byte
	var resendOf sql.NullInt64
	if err := sc.Scan(&rec.ID, &rec.Event, &rec.Channel, &rec.To, &rec.Language, &rec.OrderNumber,
		&rec.Backend, &rec.Status, &rec.Error, &raw, &resendOf, &rec.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("notification not found")
		}
		return Internalf(err, "scan notification")
	}
	rec.Data = map[string]string{}
	_ = json.Unmarshal(raw, &rec.Data)
	if resendOf.Valid {
		rec.ResendOf = &resendOf.Int64
	}
	return nil
}
