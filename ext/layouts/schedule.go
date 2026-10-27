package layouts

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// maxScheduleAhead bounds how far ahead a page may be set to publish: a year
// covers any season's sale, and a date past it is a typo for one inside it.
const maxScheduleAhead = 366 * 24 * time.Hour

// SchedulePublish sets the page's saved draft at version to publish at the
// given time (D129). The draft is copied as it is now: later edits keep
// saving to the draft and are not in what goes live, which the state says
// (Outdated). A page has one schedule; setting another replaces it. Manual
// publishing, rolling back and resetting leave it alone, because each is its
// own intent: fixing a typo on today's page today does not call off
// Friday's sale page.
func (m *Module) SchedulePublish(ctx context.Context, page string, version int, at time.Time) (*State, error) {
	now := time.Now()
	if !at.After(now) {
		return nil, gocommerce.Validationf("choose a time in the future to publish at")
	}
	if at.Sub(now) > maxScheduleAhead {
		return nil, gocommerce.Validationf("a page can be scheduled at most a year ahead")
	}
	var out *State
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var raw []byte
		var current int
		err := tx.QueryRowContext(ctx, `SELECT draft, version FROM layout_pages WHERE page = $1 FOR UPDATE`, page).Scan(&raw, &current)
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.Conflictf("Save the page before scheduling it.")
		}
		if err != nil {
			return gocommerce.Internalf(err, "read the page")
		}
		if current != version {
			return stale()
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO layout_schedules (page, document, draft_version, publish_at, scheduled_by)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (store_id, page) DO UPDATE SET document = EXCLUDED.document, draft_version = EXCLUDED.draft_version,
			    publish_at = EXCLUDED.publish_at, scheduled_at = now(), scheduled_by = EXCLUDED.scheduled_by`,
			page, raw, version, at, actor(ctx)); err != nil {
			return gocommerce.Internalf(err, "schedule the page")
		}
		out, err = m.state(ctx, tx, page)
		return err
	})
	return out, err
}

// CancelSchedule calls off the page's scheduled publish. With none there is
// nothing to call off, and the answer is the same: the page has no schedule.
func (m *Module) CancelSchedule(ctx context.Context, page string) (*State, error) {
	if _, err := m.db.ExecContext(ctx, `DELETE FROM layout_schedules WHERE page = $1`, page); err != nil {
		return nil, gocommerce.Internalf(err, "cancel the schedule")
	}
	return m.state(ctx, m.db, page)
}

// PublishDue publishes every page of the context's store whose time has
// come, each in its own transaction through the same publish as the button,
// and says how many. A row is claimed with SKIP LOCKED and deleted in the
// transaction that publishes it, so two servers running the pass publish a
// schedule once between them. Exported for the reason cart recovery's Pass
// is: a test, or an operator in a hurry, need not wait out a tick.
func (m *Module) PublishDue(ctx context.Context) (int, error) {
	n := 0
	for {
		done := false
		err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
			var (
				id   int64
				page string
				raw  []byte
				by   string
			)
			err := tx.QueryRowContext(ctx, `
				SELECT id, page, document, scheduled_by FROM layout_schedules
				WHERE publish_at <= now() ORDER BY publish_at LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&id, &page, &raw, &by)
			if errors.Is(err, sql.ErrNoRows) {
				done = true
				return nil
			}
			if err != nil {
				return gocommerce.Internalf(err, "read the due schedules")
			}
			if err := m.publish(ctx, tx, page, raw, publishing{by: by, scheduled: true}); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM layout_schedules WHERE id = $1`, id); err != nil {
				return gocommerce.Internalf(err, "clear the schedule")
			}
			return nil
		})
		if err != nil {
			return n, err
		}
		if done {
			return n, nil
		}
		n++
	}
}

// schedule is the page's pending publish, or nil.
func (m *Module) schedule(ctx context.Context, q queryer, page string) (*Schedule, error) {
	s := &Schedule{}
	err := q.QueryRowContext(ctx, `
		SELECT publish_at, scheduled_at, scheduled_by, draft_version FROM layout_schedules WHERE page = $1`, page).
		Scan(&s.PublishAt, &s.ScheduledAt, &s.ScheduledBy, &s.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the page's schedule")
	}
	return s, nil
}

func (m *Module) handleSchedule(w http.ResponseWriter, r *http.Request) {
	page, err := pageKey(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Version   int       `json:"version"`
		PublishAt time.Time `json:"publish_at"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	st, err := m.SchedulePublish(r.Context(), page, in.Version, in.PublishAt)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, st)
}

func (m *Module) handleCancelSchedule(w http.ResponseWriter, r *http.Request) {
	page, err := pageKey(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	st, err := m.CancelSchedule(r.Context(), page)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, st)
}
