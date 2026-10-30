package whatsapp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	maxAttempts  = 12
	maxBackoff   = 15 * time.Minute
	visibility   = 60 * time.Second
	deliverBatch = 20
)

// queue writes one message for the worker. A message the outbox delivers
// twice — the same event, recipient and values — is the same row.
func (m *Module) queue(ctx context.Context, event, slot, phone string, data map[string]string, body map[string]any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	var orderID *int64
	if id, err := strconv.ParseInt(data["order_id"], 10, 64); err == nil {
		orderID = &id
	}
	_, err = m.db.ExecContext(ctx, `
		INSERT INTO whatsapp_messages (dedupe, event, slot, phone, order_id, order_number, payload)
		VALUES ($1, $2, $3, $4, $5, nullif($6, ''), $7)
		ON CONFLICT (store_id, dedupe) DO NOTHING`,
		dedupe(event, phone, data), event, slot, phone, orderID, data["order_number"], payload)
	return err
}

func dedupe(event, phone string, data map[string]string) string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s", event, phone)
	for _, k := range keys {
		fmt.Fprintf(h, "\x00%s=%s", k, data[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

type pending struct {
	id          int64
	slot, phone string
	orderID     *int64
	payload     []byte
	attempts    int
}

// DeliverPass sends one batch of this store's queued messages and returns how
// many it attempted. Exported, like DrainOutbox, so a test or an operator in a
// hurry need not wait for the ticker.
func (m *Module) DeliverPass(ctx context.Context) (int, error) {
	var due bool
	if err := m.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM whatsapp_messages WHERE status = 'queued' AND available_at <= now())`).Scan(&due); err != nil || !due {
		return 0, err
	}
	acct := m.account(ctx)
	if !acct.ready() {
		return 0, nil
	}
	rows, err := m.db.QueryContext(ctx, `
		WITH claimed AS MATERIALIZED (
			SELECT id FROM whatsapp_messages
			WHERE status = 'queued' AND available_at <= now()
			ORDER BY id
			FOR UPDATE SKIP LOCKED
			LIMIT $1
		)
		UPDATE whatsapp_messages w
		SET attempts = w.attempts + 1, available_at = now() + make_interval(secs => $2)
		FROM claimed WHERE w.id = claimed.id
		RETURNING w.id, w.slot, w.phone, w.order_id, w.payload, w.attempts`,
		deliverBatch, visibility.Seconds())
	if err != nil {
		return 0, fmt.Errorf("claim whatsapp messages: %w", err)
	}
	var batch []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.slot, &p.phone, &p.orderID, &p.payload, &p.attempts); err != nil {
			rows.Close()
			return 0, err
		}
		batch = append(batch, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for i, p := range batch {
		// A reply that is not a template outside the 24-hour window would be
		// refused by Meta; it is given up here with the reason instead.
		if p.slot == slotReply && !m.windowOpen(ctx, p.phone) {
			if err := m.fail(ctx, p, errors.New("outside the 24-hour customer service window")); err != nil {
				return len(batch), err
			}
			continue
		}
		wamid, err := m.post(ctx, acct, p.payload)
		if err == nil {
			if _, err := m.db.ExecContext(ctx, `
				UPDATE whatsapp_messages SET status = 'sent', wamid = $2, last_error = NULL, updated_at = now()
				WHERE id = $1`, p.id, nullIfEmpty(wamid)); err != nil {
				return len(batch), err
			}
			m.onOrder(ctx, p.orderID, slotLabel(p.slot)+" sent on WhatsApp to "+p.phone)
			continue
		}
		var me *metaError
		if errors.As(err, &me) && me.account() {
			return len(batch), m.holdForAccount(ctx, batch[i:], err)
		}
		if err := m.retryOrFail(ctx, p, err); err != nil {
			return len(batch), err
		}
	}
	return len(batch), nil
}

// slotReply is a free-form answer to something the shopper wrote.
const slotReply = "reply"

func slotLabel(slot string) string {
	if s := slotByKey(slot); s != nil {
		return s.Label
	}
	return "Reply"
}

// onOrder puts a line on the order's history, through the order service, as
// WhatsApp. A history that could not be written is logged; the message stands.
func (m *Module) onOrder(ctx context.Context, orderID *int64, summary string) {
	if orderID == nil {
		return
	}
	ctx = gocommerce.WithActorLabel(ctx, "WhatsApp")
	if err := m.app.Order().RecordMessage(ctx, *orderID, summary); err != nil && !errors.Is(err, gocommerce.ErrNotFound) {
		m.log.Error("whatsapp: order history not written", "order", *orderID, "error", err)
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (m *Module) retryOrFail(ctx context.Context, p pending, cause error) error {
	var me *metaError
	if (errors.As(cause, &me) && me.final()) || p.attempts >= maxAttempts {
		return m.fail(ctx, p, cause)
	}
	_, err := m.db.ExecContext(ctx, `
		UPDATE whatsapp_messages SET available_at = now() + make_interval(secs => $2), last_error = $3, updated_at = now()
		WHERE id = $1`, p.id, backoff(p.attempts).Seconds(), cause.Error())
	return err
}

func (m *Module) fail(ctx context.Context, p pending, cause error) error {
	if _, err := m.db.ExecContext(ctx, `
		UPDATE whatsapp_messages SET status = 'failed', last_error = $2, updated_at = now() WHERE id = $1`,
		p.id, cause.Error()); err != nil {
		return err
	}
	m.onOrder(ctx, p.orderID, slotLabel(p.slot)+" on WhatsApp could not be sent: "+cause.Error())
	return nil
}

// holdForAccount puts the rest of a batch back, their attempts returned, while
// Meta refuses the store's token or number: every message would be refused
// the same way until the owner fixes it.
func (m *Module) holdForAccount(ctx context.Context, rest []pending, cause error) error {
	for _, p := range rest {
		if _, err := m.db.ExecContext(ctx, `
			UPDATE whatsapp_messages
			SET attempts = attempts - 1, available_at = now() + make_interval(secs => $2), last_error = $3, updated_at = now()
			WHERE id = $1`, p.id, maxBackoff.Seconds(), cause.Error()); err != nil {
			return err
		}
	}
	store, _ := gocommerce.StoreFrom(ctx)
	m.log.Warn("whatsapp: Meta refused the store's token or number; messages wait until it is fixed",
		"store", store, "waiting", len(rest), "reason", cause.Error())
	return nil
}

func backoff(attempts int) time.Duration {
	d := time.Duration(math.Pow(2, float64(attempts))) * time.Second
	if d > maxBackoff || d <= 0 {
		return maxBackoff
	}
	return d
}

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
		err := m.app.ForEachStore(ctx, func(ctx context.Context) error {
			_, err := m.DeliverPass(ctx)
			return err
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			m.log.Error("whatsapp pass failed", "error", err)
		}
	}
}

// ------------------------------------------------------------- Graph API

// metaError is Meta's refusal, in its words, with the access token scrubbed.
type metaError struct {
	status, code, subcode int
	message               string
	transient             bool
}

func (e *metaError) Error() string { return fmt.Sprintf("meta answered %d: %s", e.status, e.message) }

func (e *metaError) final() bool {
	return e.status >= 400 && e.status < 500 && e.status != http.StatusTooManyRequests && !e.transient
}

// account is a refusal of the store's setup rather than of the message: a
// token that is wrong, expired or lacks the permission (190, 10, 200–299).
func (e *metaError) account() bool {
	return e.status == http.StatusUnauthorized || e.code == 190 || e.code == 10 || (e.code >= 200 && e.code <= 299)
}

// graph makes one Graph API call. The token goes in the Authorization header,
// never in the address, which a proxy or an error would repeat.
func (m *Module) graph(ctx context.Context, token, method, path string, query url.Values, body any, out any) error {
	var rdr io.Reader
	if body != nil {
		switch b := body.(type) {
		case []byte:
			rdr = bytes.NewReader(b)
		default:
			enc, err := json.Marshal(b)
			if err != nil {
				return err
			}
			rdr = bytes.NewReader(enc)
		}
	}
	u := m.cfg.BaseURL + "/" + graphVersion + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if rdr != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = fmt.Errorf("%s: %w", ue.Op, ue.Err)
		}
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if out != nil {
			return json.Unmarshal(raw, out)
		}
		return nil
	}
	var fail struct {
		Error struct {
			Message   string `json:"message"`
			Code      int    `json:"code"`
			Subcode   int    `json:"error_subcode"`
			Transient bool   `json:"is_transient"`
			Data      struct {
				Details string `json:"details"`
			} `json:"error_data"`
		} `json:"error"`
	}
	msg := resp.Status
	if json.Unmarshal(raw, &fail) == nil && fail.Error.Message != "" {
		msg = fail.Error.Message
		if d := fail.Error.Data.Details; d != "" {
			msg += ": " + d
		}
	}
	if token != "" {
		msg = strings.ReplaceAll(msg, token, "[access token]")
	}
	return &metaError{status: resp.StatusCode, code: fail.Error.Code, subcode: fail.Error.Subcode,
		message: msg, transient: fail.Error.Transient}
}

// post sends one message and answers the id Meta gave it (wamid), which its
// status webhooks name.
func (m *Module) post(ctx context.Context, acct account, payload []byte) (string, error) {
	var out struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := m.graph(ctx, acct.token, http.MethodPost, "/"+url.PathEscape(acct.phoneID)+"/messages", nil, payload, &out); err != nil {
		return "", err
	}
	if len(out.Messages) == 0 {
		return "", nil
	}
	return out.Messages[0].ID, nil
}
