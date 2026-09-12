package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"time"

	gocommerce "github.com/misiki/gocommerce/core"
)

// The worker is the core outbox's dispatcher, one layer out, and deliberately
// the same shape: claim a batch with FOR UPDATE SKIP LOCKED so several
// instances can run without coordinating, hide what you claimed for a
// visibility window so a process that dies mid-POST releases its work, back off
// exponentially, and park what will not go through rather than deleting it.
//
// What differs is what a failure means. The outbox is talking to code in this
// process; this is talking to somebody else's server over the internet, where
// a failure is ordinary and says nothing about the event.

type pending struct {
	id        int64
	url       string
	secret    string
	eventName string
	payload   []byte
	attempts  int
}

// DeliverPass sends one batch and returns how many rows it attempted. It is
// exported for the same reason the engine exports DrainOutbox: a test, and an
// operator with a reason to hurry, should be able to run a pass rather than
// wait out a ticker.
func (m *Module) DeliverPass(ctx context.Context) (int, error) {
	rows, err := m.db.QueryContext(ctx, `
		UPDATE webhook_deliveries d
		SET attempts = d.attempts + 1,
		    available_at = now() + make_interval(secs => $2)
		FROM (
			SELECT id FROM webhook_deliveries
			WHERE delivered_at IS NULL AND NOT dead AND available_at <= now()
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		) AS claimed
		WHERE d.id = claimed.id
		RETURNING d.id, d.event_name, d.payload, d.attempts,
		          (SELECT url FROM webhook_endpoints e WHERE e.id = d.endpoint_id),
		          (SELECT secret FROM webhook_endpoints e WHERE e.id = d.endpoint_id)`,
		deliverBatch, visibility.Seconds())
	if err != nil {
		return 0, fmt.Errorf("claim webhook batch: %w", err)
	}

	var batch []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.eventName, &p.payload, &p.attempts, &p.url, &p.secret); err != nil {
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

	for _, p := range batch {
		status, err := m.post(ctx, p)
		if err == nil {
			if markErr := m.markDelivered(ctx, p.id, status); markErr != nil {
				return len(batch), markErr
			}
			continue
		}
		if markErr := m.markFailed(ctx, p, status, err); markErr != nil {
			return len(batch), markErr
		}
	}
	return len(batch), nil
}

// post sends one delivery. A 2xx is success and everything else is not,
// including a 3xx: a webhook endpoint that redirects is misconfigured, and
// following it would send the store's signed payload somewhere the operator
// did not name.
func (m *Module) post(ctx context.Context, p pending) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, m.cfg.Timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(p.payload))
	if err != nil {
		return 0, err
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "gocommerce-webhooks/1")
	req.Header.Set("X-GoCommerce-Event", p.eventName)
	req.Header.Set("X-GoCommerce-Signature", sign(p.secret, ts, p.payload))

	client := &http.Client{
		Timeout: m.cfg.Timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("endpoint redirected; a webhook URL must be final")
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Drained so the connection can be reused, and capped because a merchant
	// returning a megabyte of HTML on error should not cost this store memory.
	io.CopyN(io.Discard, resp.Body, 4096)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return resp.StatusCode, fmt.Errorf("endpoint answered %d", resp.StatusCode)
	}
	return resp.StatusCode, nil
}

// sign is HMAC-SHA256 over "<timestamp>.<body>", rendered the way Stripe
// renders it — which is not imitation for its own sake: `ext/payments-stripe`
// already verifies exactly this shape, so anyone who has integrated one side of
// it in this codebase has read the other.
//
// The timestamp is inside the signed material rather than beside it, so a
// captured request cannot be replayed later with its clock rewritten.
func sign(secret, ts string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts))
	mac.Write([]byte("."))
	mac.Write(body)
	return "t=" + ts + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func (m *Module) markDelivered(ctx context.Context, id int64, status int) error {
	_, err := m.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		SET delivered_at = now(), last_status = $2, last_error = NULL
		WHERE id = $1`, id, status)
	return err
}

// markFailed schedules the next try, or gives up.
func (m *Module) markFailed(ctx context.Context, p pending, status int, cause error) error {
	if p.attempts >= maxAttempts {
		_, err := m.db.ExecContext(ctx, `
			UPDATE webhook_deliveries
			SET dead = true, last_status = nullif($2, 0), last_error = $3
			WHERE id = $1`, p.id, status, cause.Error())
		if err == nil {
			m.log.Warn("webhook delivery gave up",
				"delivery", p.id, "event", p.eventName, "attempts", p.attempts, "error", cause)
		}
		return err
	}
	_, err := m.db.ExecContext(ctx, `
		UPDATE webhook_deliveries
		SET available_at = now() + make_interval(secs => $2),
		    last_status = nullif($3, 0), last_error = $4
		WHERE id = $1`, p.id, backoff(p.attempts).Seconds(), status, cause.Error())
	return err
}

// backoff doubles from a second, capped. The cap matters more than the curve: a
// merchant fixing their endpoint should not wait hours for the queue to notice.
func backoff(attempts int) time.Duration {
	d := time.Duration(math.Pow(2, float64(attempts))) * time.Second
	if d > maxBackoff || d <= 0 {
		return maxBackoff
	}
	return d
}

// start spawns the worker and returns.
//
// Returning is the whole point. OnStart runs after migrations and *before* the
// listener accepts traffic, so a hook that blocks is a store that never serves:
// the first version of this looped in the hook itself and the process sat there
// logging "worker started" with nothing listening on the port. The hook's
// contract is to launch long-running work, not to be it.
func (m *Module) start(ctx context.Context) error {
	go m.run(ctx)
	return nil
}

// run is the ticker. It stops when the app stops, and it does not run passes
// concurrently with itself — one instance draining faster is what the batch
// size is for.
func (m *Module) run(ctx context.Context) {
	m.log.Info("webhook delivery worker started", "poll", m.poll)
	t := time.NewTicker(m.poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := m.DeliverPass(ctx); err != nil && !errors.Is(err, context.Canceled) {
				m.log.Error("webhook delivery pass failed", "error", err)
			}
		}
	}
}

// handleRetry puts a delivery back in the queue, which is the only thing an
// operator can usefully do with a dead one. It clears the attempt count as well
// as the flag: a retry the operator asked for is a fresh start, not attempt
// thirteen of twelve.
func (m *Module) handleRetry(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.db.ExecContext(r.Context(), `
		UPDATE webhook_deliveries
		SET dead = false, attempts = 0, available_at = now(), last_error = NULL
		WHERE id = $1 AND delivered_at IS NULL`, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf(
			"no undelivered webhook delivery %d", id))
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"queued": true})
}
