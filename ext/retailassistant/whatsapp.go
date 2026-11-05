package retailassistant

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	maxDocumentBytes = 15 << 20
	linkCodeTTL      = 24 * time.Hour
	maxCodeAttempts  = 5
	maxWorkAttempts  = 5
	claimVisibility  = 2 * time.Minute
	replyTimeout     = 5 * time.Second
)

var (
	linkCommand  = regexp.MustCompile(`(?i)^\s*link\s+(\d{6})\s*$`)
	storeCommand = regexp.MustCompile(`(?i)^\s*store\s+(\S+)\s*$`)
)

func (m *Module) handleVerify(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("hub.mode") != "subscribe" || m.cfg.VerifyToken == "" ||
		subtle.ConstantTimeCompare([]byte(q.Get("hub.verify_token")), []byte(m.cfg.VerifyToken)) != 1 {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("the verify token does not match"))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = io.WriteString(w, q.Get("hub.challenge"))
}

func validSignature(secret string, body []byte, header string) bool {
	sig, ok := strings.CutPrefix(header, "sha256=")
	if !ok || secret == "" {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

type notification struct {
	Entry []struct {
		Changes []struct {
			Field string `json:"field"`
			Value struct {
				Metadata struct {
					PhoneNumberID string `json:"phone_number_id"`
				} `json:"metadata"`
				Messages []inbound `json:"messages"`
			} `json:"value"`
		} `json:"changes"`
	} `json:"entry"`
}

type inbound struct {
	From string `json:"from"`
	ID   string `json:"id"`
	Type string `json:"type"`
	Text struct {
		Body string `json:"body"`
	} `json:"text"`
	Document struct {
		ID       string `json:"id"`
		MimeType string `json:"mime_type"`
		Filename string `json:"filename"`
	} `json:"document"`
	Image struct {
		ID       string `json:"id"`
		MimeType string `json:"mime_type"`
	} `json:"image"`
}

func (m *Module) handleWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the body could not be read"))
		return
	}
	// The body's own words about who sent it mean nothing until this passes.
	if !validSignature(m.cfg.AppSecret, body, r.Header.Get("X-Hub-Signature-256")) {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("the signature does not match"))
		return
	}
	var n notification
	if err := json.Unmarshal(body, &n); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Validationf("the body is not a WhatsApp notification"))
		return
	}
	// A fresh context, not the request's: the request was bound to whichever
	// store the address named, and carries that store's currency and languages,
	// which must not follow a message into another store.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, e := range n.Entry {
		for _, c := range e.Changes {
			if c.Field != "messages" || c.Value.Metadata.PhoneNumberID != m.cfg.PhoneNumberID {
				continue
			}
			for _, msg := range c.Value.Messages {
				// A database failure is a 500, which Meta retries; the message
				// id makes the retry safe.
				if err := m.receive(ctx, msg); err != nil {
					gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record a whatsapp message"))
					return
				}
			}
		}
	}
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"received": true})
}

// linkedStore is one store a sender's number is linked to.
type linkedStore struct {
	id       string
	selected sql.NullTime
}

// receive decides whose message this is and records it there.
//
// ponytail: one query per store per message, because a table every store can
// be searched through cannot be behind the store policy (AGENTS.md rule 15).
// Fine for hundreds of stores; a routing table would need a reviewed exemption
// from that rule when it stops being fine.
func (m *Module) receive(ctx context.Context, msg inbound) error {
	sender := digits(msg.From)
	if sender == "" || msg.ID == "" {
		return nil
	}
	code := ""
	if g := linkCommand.FindStringSubmatch(msg.Text.Body); msg.Type == "text" && g != nil {
		code = g[1]
	}
	var (
		mu     sync.Mutex
		seen   bool
		linked []linkedStore
		proved string
	)
	err := m.app.ForEachStore(ctx, func(ctx context.Context) error {
		if !m.enabled(ctx) {
			return nil
		}
		store, _ := gocommerce.StoreFrom(ctx)
		var dup bool
		if err := m.db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM retailassistant_messages WHERE provider_message_id = $1)`, msg.ID).Scan(&dup); err != nil {
			return err
		}
		var l linkedStore
		err := m.db.QueryRowContext(ctx, `
			SELECT selected_at FROM retailassistant_phone_links
			WHERE phone = $1 AND verified_at IS NOT NULL AND revoked_at IS NULL`, sender).Scan(&l.selected)
		found := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		ok := false
		if code != "" && !dup {
			if ok, err = m.proveLink(ctx, sender, code, msg.ID); err != nil {
				return err
			}
		}
		mu.Lock()
		defer mu.Unlock()
		seen = seen || dup
		if found {
			l.id = store
			linked = append(linked, l)
		}
		if ok {
			proved = store
		}
		return nil
	})
	if err != nil {
		return err
	}
	switch {
	case seen:
		return nil
	case proved != "":
		m.say(ctx, sender, fmt.Sprintf("This number is now linked to %s. Send a supplier bill or a payment proof here and it will wait for review in your dashboard.", m.storeName(ctx, proved)))
		return nil
	case len(linked) == 0:
		// Nothing is kept: a file from a stranger has no store to belong to, and
		// what is in it says nothing about whose it is.
		if m.cfg.QuietToStrangers {
			return nil
		}
		m.say(ctx, sender, "This number is not linked to a store yet, so nothing you sent was kept. Ask your store's owner to add this number under Retail assistant, then send the code they give you.")
		return nil
	}
	if g := storeCommand.FindStringSubmatch(msg.Text.Body); msg.Type == "text" && g != nil {
		return m.selectStore(ctx, sender, g[1], linked)
	}
	target, ok := chooseStore(linked)
	if !ok {
		var names []string
		for _, l := range linked {
			if st, err := m.app.Stores().Get(ctx, l.id); err == nil {
				names = append(names, st.Slug)
			}
		}
		m.say(ctx, sender, "This number works for several stores, so nothing you sent was kept. Reply STORE and the store's name, then send it again: "+strings.Join(names, ", "))
		return nil
	}
	return m.record(gocommerce.WithStore(ctx, target), sender, msg)
}

// chooseStore is the one store, or the one this sender last chose. Several with
// no choice made is no answer: a bill is never posted into a store by guesswork.
func chooseStore(linked []linkedStore) (string, bool) {
	if len(linked) == 1 {
		return linked[0].id, true
	}
	best := ""
	var when time.Time
	for _, l := range linked {
		if l.selected.Valid && l.selected.Time.After(when) {
			best, when = l.id, l.selected.Time
		}
	}
	return best, best != ""
}

func (m *Module) selectStore(ctx context.Context, sender, slug string, linked []linkedStore) error {
	for _, l := range linked {
		st, err := m.app.Stores().Get(ctx, l.id)
		if err != nil || !strings.EqualFold(st.Slug, slug) {
			continue
		}
		if _, err := m.db.ExecContext(gocommerce.WithStore(ctx, l.id),
			`UPDATE retailassistant_phone_links SET selected_at = now() WHERE phone = $1`, sender); err != nil {
			return err
		}
		m.say(ctx, sender, "What you send now goes to "+st.Name+".")
		return nil
	}
	m.say(ctx, sender, "This number is not linked to a store called "+slug+".")
	return nil
}

func (m *Module) storeName(ctx context.Context, id string) string {
	if st, err := m.app.Stores().Get(ctx, id); err == nil {
		return st.Name
	}
	return "your store"
}

// proveLink takes a code from the number it was issued for. Only that number
// can reach this row, since Meta vouches for the sender, so the attempts cap
// guards a number being tried by whoever holds it, not the whole world.
func (m *Module) proveLink(ctx context.Context, sender, code, messageID string) (bool, error) {
	store, _ := gocommerce.StoreFrom(ctx)
	proved := false
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var id int64
		var hash string
		var attempts int
		err := tx.QueryRowContext(ctx, `
			SELECT id, code_hash, code_attempts FROM retailassistant_phone_links
			WHERE phone = $1 AND verified_at IS NULL AND revoked_at IS NULL
			  AND code_hash IS NOT NULL AND code_expires_at > now()
			FOR UPDATE`, sender).Scan(&id, &hash, &attempts)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if attempts >= maxCodeAttempts || subtle.ConstantTimeCompare([]byte(hash), []byte(codeHash(store, sender, code))) != 1 {
			_, err := tx.ExecContext(ctx, `UPDATE retailassistant_phone_links SET code_attempts = code_attempts + 1 WHERE id = $1`, id)
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE retailassistant_phone_links
			SET verified_at = now(), code_hash = NULL, code_expires_at = NULL WHERE id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO retailassistant_messages (provider_message_id, sender, type) VALUES ($1, $2, 'text')
			ON CONFLICT (store_id, provider_message_id) DO NOTHING`, messageID, sender); err != nil {
			return err
		}
		proved = true
		return nil
	})
	return proved, err
}

func codeHash(store, phone, code string) string {
	sum := sha256.Sum256([]byte(store + "\x00" + phone + "\x00" + code))
	return hex.EncodeToString(sum[:])
}

// record keeps a message in the store it belongs to. The bytes are fetched
// later, by the worker: a webhook answers quickly and holds no transaction
// across a network call (AGENTS.md rule 5).
func (m *Module) record(ctx context.Context, sender string, msg inbound) error {
	media, mime, filename := msg.Document.ID, msg.Document.MimeType, msg.Document.Filename
	if msg.Type == "image" {
		media, mime = msg.Image.ID, msg.Image.MimeType
	}
	queued := false
	err := gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var messageID int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO retailassistant_messages (provider_message_id, sender, type) VALUES ($1, $2, $3)
			ON CONFLICT (store_id, provider_message_id) DO NOTHING RETURNING id`, msg.ID, sender, msg.Type).Scan(&messageID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if media == "" {
			return nil
		}
		queued = true
		_, err = tx.ExecContext(ctx, `
			INSERT INTO retailassistant_documents (message_id, sender, media_id, filename, declared_mime)
			VALUES ($1, $2, $3, $4, $5)`, messageID, sender, media, filename, mime)
		return err
	})
	if err != nil {
		return err
	}
	if queued {
		m.nudge()
	} else if media == "" {
		m.say(ctx, sender, "Send a supplier bill or a payment proof as a file or a photo, and it will wait for review in your dashboard.")
	}
	return nil
}

// say answers a message that just arrived, inside the conversation it opened.
// Best effort: a reply lost here costs nothing that the dashboard does not show.
func (m *Module) say(ctx context.Context, to, text string) {
	ctx, cancel := context.WithTimeout(ctx, replyTimeout)
	defer cancel()
	if err := m.send(ctx, to, text); err != nil {
		m.log.Warn("retail assistant: a reply was not sent", "error", err)
	}
}

func (m *Module) send(ctx context.Context, to, text string) error {
	raw, err := json.Marshal(map[string]any{
		"messaging_product": "whatsapp", "to": to, "type": "text", "text": map[string]string{"body": text},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.cfg.BaseURL+"/"+graphVersion+"/"+m.cfg.PhoneNumberID+"/messages", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+m.cfg.AccessToken)
	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("WhatsApp answered %d: %s", resp.StatusCode, body)
	}
	return nil
}

// download fetches a document's bytes: Meta gives a short-lived address for the
// id, and that address needs the token too.
func (m *Module) download(ctx context.Context, mediaID string) ([]byte, error) {
	get := func(addr string) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, addr, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+m.cfg.AccessToken)
		resp, err := m.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			resp.Body.Close()
			return nil, fmt.Errorf("WhatsApp answered %d for a file", resp.StatusCode)
		}
		return resp, nil
	}
	resp, err := get(m.cfg.BaseURL + "/" + graphVersion + "/" + mediaID)
	if err != nil {
		return nil, err
	}
	var meta struct {
		URL string `json:"url"`
	}
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&meta)
	resp.Body.Close()
	if err != nil || meta.URL == "" {
		return nil, errors.New("WhatsApp gave no address for the file")
	}
	file, err := get(meta.URL)
	if err != nil {
		return nil, err
	}
	defer file.Body.Close()
	data, err := io.ReadAll(io.LimitReader(file.Body, maxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocumentBytes {
		return nil, errPermanent{fmt.Sprintf("the file is larger than %d MB", maxDocumentBytes>>20)}
	}
	return data, nil
}

// errPermanent is a failure another try cannot change.
type errPermanent struct{ reason string }

func (e errPermanent) Error() string { return e.reason }

func (m *Module) nudge() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Module) start(ctx context.Context) error {
	go func() {
		t := time.NewTicker(m.cfg.Poll)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			case <-m.wake:
			}
			err := m.app.ForEachStore(ctx, func(ctx context.Context) error {
				if !m.enabled(ctx) {
					return nil
				}
				return m.WorkPass(ctx)
			})
			if err != nil && !errors.Is(err, context.Canceled) {
				m.log.Error("retail assistant pass failed", "error", err)
			}
		}
	}()
	return nil
}

// WorkPass downloads and reads one batch of this store's documents and sends
// its queued messages. Exported for the reason the engine exports DrainOutbox:
// a test can run a pass rather than wait for the ticker.
func (m *Module) WorkPass(ctx context.Context) error {
	if err := m.documentPass(ctx); err != nil {
		return err
	}
	return m.replyPass(ctx)
}

func (m *Module) documentPass(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `
		WITH claimed AS MATERIALIZED (
			SELECT id FROM retailassistant_documents
			WHERE status IN ('received', 'parsing') AND available_at <= now()
			ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 10
		)
		UPDATE retailassistant_documents d
		SET status = 'parsing', attempts = d.attempts + 1, available_at = now() + make_interval(secs => $1)
		FROM claimed WHERE d.id = claimed.id
		RETURNING d.id, d.media_id, d.attempts, d.sender, d.filename`, claimVisibility.Seconds())
	if err != nil {
		return err
	}
	type job struct {
		id               int64
		media            string
		attempts         int
		sender, filename string
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.media, &j.attempts, &j.sender, &j.filename); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, j := range jobs {
		data, err := m.download(ctx, j.media)
		if err != nil {
			var perm errPermanent
			permanent := errors.As(err, &perm)
			if _, uerr := m.db.ExecContext(ctx, `
				UPDATE retailassistant_documents
				SET status = CASE WHEN $2 THEN 'failed' ELSE 'parsing' END,
				    last_error = $3, available_at = now() + make_interval(secs => $4)
				WHERE id = $1`, j.id, permanent || j.attempts >= maxWorkAttempts, err.Error(), j.attempts*30); uerr != nil {
				return uerr
			}
			continue
		}
		if err := m.keepDocument(ctx, j.id, j.sender, j.filename, data); err != nil {
			return err
		}
	}
	return nil
}

// keepDocument stores what arrived and what could be read from it.
//
// ponytail: the bytes live in a bytea column, behind the store policy like every
// other row, because the engine's media store serves public addresses and a
// supplier's bill must not have one. Move to object storage when volume hurts
// the database.
func (m *Module) keepDocument(ctx context.Context, id int64, sender, filename string, data []byte) error {
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	mime, draft := Extract(data, filename, m.app.Currency(ctx))
	return gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var other sql.NullInt64
		if err := tx.QueryRowContext(ctx, `
			SELECT min(id) FROM retailassistant_documents
			WHERE sha256 = $1 AND id <> $2 AND status <> 'rejected'`, hash, id).Scan(&other); err != nil {
			return err
		}
		if other.Valid {
			draft.Exceptions = append(draft.Exceptions, "This is the same file as document "+strconv.FormatInt(other.Int64, 10)+".")
		}
		raw, err := json.Marshal(draft)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE retailassistant_documents
			SET status = 'needs_review', content = $2, sha256 = $3, detected_mime = $4,
			    extraction = $5, draft = $5, last_error = ''
			WHERE id = $1`, id, data, hash, mime, raw); err != nil {
			return err
		}
		name := filename
		if name == "" {
			name = "your file"
		}
		return m.tell(ctx, tx, "received:"+strconv.FormatInt(id, 10), sender, "Got "+name+". It is waiting for review in your dashboard.")
	})
}

// tell queues a message for a sender who opted in. Queued in the transaction of
// the fact it reports, and sent after commit by the worker. It is free text, so
// outside WhatsApp's customer-service window Meta refuses it, and the row says
// so; template messages are not built yet.
func (m *Module) tell(ctx context.Context, tx *sql.Tx, key, to, text string) error {
	var notify bool
	err := tx.QueryRowContext(ctx, `
		SELECT notify FROM retailassistant_phone_links
		WHERE phone = $1 AND verified_at IS NOT NULL AND revoked_at IS NULL`, to).Scan(&notify)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !notify) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO retailassistant_replies (dedupe_key, recipient, body) VALUES ($1, $2, $3)
		ON CONFLICT (store_id, dedupe_key) DO NOTHING`, key, to, text)
	return err
}

func (m *Module) replyPass(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx, `
		WITH claimed AS MATERIALIZED (
			SELECT id FROM retailassistant_replies
			WHERE sent_at IS NULL AND NOT dead AND available_at <= now()
			ORDER BY id FOR UPDATE SKIP LOCKED LIMIT 20
		)
		UPDATE retailassistant_replies r
		SET attempts = r.attempts + 1, available_at = now() + make_interval(secs => $1)
		FROM claimed WHERE r.id = claimed.id
		RETURNING r.id, r.recipient, r.body, r.attempts`, claimVisibility.Seconds())
	if err != nil {
		return err
	}
	type reply struct {
		id       int64
		to, body string
		attempts int
	}
	var batch []reply
	for rows.Next() {
		var x reply
		if err := rows.Scan(&x.id, &x.to, &x.body, &x.attempts); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, x)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, x := range batch {
		sendCtx, cancel := context.WithTimeout(ctx, replyTimeout)
		err := m.send(sendCtx, x.to, x.body)
		cancel()
		if err == nil {
			if _, err := m.db.ExecContext(ctx, `UPDATE retailassistant_replies SET sent_at = now(), last_error = '' WHERE id = $1`, x.id); err != nil {
				return err
			}
			continue
		}
		if _, uerr := m.db.ExecContext(ctx, `
			UPDATE retailassistant_replies SET last_error = $2, dead = $3 WHERE id = $1`,
			x.id, err.Error(), x.attempts >= maxWorkAttempts); uerr != nil {
			return uerr
		}
	}
	return nil
}

// digits is a phone as WhatsApp names it: country code and number, nothing else.
func digits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
}

func newLinkCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// LinkPhone is a number a store has named.
type PhoneLink struct {
	ID         int64      `json:"id"`
	Phone      string     `json:"phone"`
	VerifiedAt *time.Time `json:"verified_at"`
	Notify     bool       `json:"notify"`
	CreatedAt  time.Time  `json:"created_at"`
	// Code is the one time it is ever shown, when it is issued.
	Code string `json:"code,omitempty"`
}

func (m *Module) handleListLinks(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.QueryContext(r.Context(), `
		SELECT id, phone, verified_at, notify, created_at FROM retailassistant_phone_links
		WHERE revoked_at IS NULL ORDER BY id`)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list phone links"))
		return
	}
	defer rows.Close()
	out := []PhoneLink{}
	for rows.Next() {
		var l PhoneLink
		if err := rows.Scan(&l.ID, &l.Phone, &l.VerifiedAt, &l.Notify, &l.CreatedAt); err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list phone links"))
			return
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list phone links"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

// handleCreateLink names a number and issues its code. Naming it grants nothing:
// the number has to send the code back from itself, so an owner's typo cannot
// point a stranger's phone at the store, and nobody can claim a store from
// WhatsApp alone. Doing it again issues a fresh code.
func (m *Module) handleCreateLink(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Phone string `json:"phone"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	phone := digits(in.Phone)
	if !strings.HasPrefix(strings.TrimSpace(in.Phone), "+") || len(phone) < 8 || len(phone) > 15 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("enter the phone number with its country code, such as +91 98765 43210"))
		return
	}
	ctx := r.Context()
	store, _ := gocommerce.StoreFrom(ctx)
	code, err := newLinkCode()
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "make a link code"))
		return
	}
	var l PhoneLink
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, `
			INSERT INTO retailassistant_phone_links (phone, code_hash, code_expires_at, created_by)
			VALUES ($1, $2, $3, $4)
			ON CONFLICT (store_id, phone) DO UPDATE
			SET code_hash = $2, code_expires_at = $3, code_attempts = 0,
			    verified_at = NULL, revoked_at = NULL, selected_at = NULL, created_by = $4
			RETURNING id, phone, verified_at, notify, created_at`,
			phone, codeHash(store, phone, code), time.Now().Add(linkCodeTTL), actor(ctx)).
			Scan(&l.ID, &l.Phone, &l.VerifiedAt, &l.Notify, &l.CreatedAt)
		if err != nil {
			return err
		}
		return m.audit(ctx, tx, "phone.named", "phone_link", l.ID, map[string]string{"phone": phone})
	})
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "name a phone"))
		return
	}
	l.Code = code
	gocommerce.Respond(w, http.StatusCreated, l)
}

func (m *Module) handleUpdateLink(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		Notify bool `json:"notify"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.db.ExecContext(r.Context(), `
		UPDATE retailassistant_phone_links SET notify = $2 WHERE id = $1 AND revoked_at IS NULL`, id, in.Notify)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "change a phone link"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no phone link %d", id))
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"notify": in.Notify})
}

func (m *Module) handleRevokeLink(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE retailassistant_phone_links SET revoked_at = now(), code_hash = NULL
			WHERE id = $1 AND revoked_at IS NULL`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return gocommerce.NotFoundf("no phone link %d", id)
		}
		return m.audit(ctx, tx, "phone.revoked", "phone_link", id, nil)
	})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"revoked": true})
}
