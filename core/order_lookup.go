package gocommerce

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A guest opens their order on the storefront with its number and a 4-digit
// code sent to the email or phone on the order, rather than a link (D115). The
// code is exchanged for the order's access token, which is what the storefront
// already reads a guest's order with, so nothing downstream changes.
//
// Four digits are ten thousand guesses, which is why the limits are the
// design: a code lives ten minutes and takes five tries, and an order gets one
// code a minute and five a day. Twenty-five guesses a day against ten thousand.

// EventOrderLookupCode names the message, not an outbox event: the code is
// sent straight away and never stored anywhere a reader of events could see.
const EventOrderLookupCode = "order.lookup_code"

const (
	orderLookupTTL      = 10 * time.Minute
	orderLookupAttempts = 5
	orderLookupGap      = time.Minute
	orderLookupDaily    = 5
	orderLookupSendWait = 30 * time.Second
)

// orderOpensPerWindow is how many times one client (an IPv6 /64) may try an
// order number and contact in an hour. Order numbers count up, so without it
// the open route would walk a known address through every order in the shop.
const orderOpensPerWindow = 20

// lookupSends tracks the codes being delivered, so shutdown and tests can
// wait for them. Package-level because Orders is a bare handle on the app.
var lookupSends sync.WaitGroup

// OrderLookup is what a right code answers with: the order, and the token the
// storefront keeps to read it again.
type OrderLookup struct {
	Order       *Order `json:"order"`
	AccessToken string `json:"access_token"`
}

// RequestLookupCode sends a code to the order's own email or phone when contact
// is that email or phone. Whether it matched is not said: an unknown order or a
// wrong contact gets the same nil, and nothing is sent.
func (s *Orders) RequestLookupCode(ctx context.Context, number, contact string) error {
	channel := ChannelEmail
	if !strings.Contains(contact, "@") {
		channel = ChannelSMS
	}
	// Before the order is looked up, so the answer says something about the
	// shop and nothing about the order.
	if !s.app.notifier.delivers(ctx, channel) {
		other := "your phone number"
		if channel == ChannelSMS {
			other = "your email address"
		}
		return Conflictf("This shop cannot send codes by %s. Use %s instead.", channelNoun(channel), other)
	}

	var (
		id                 int64
		email, phone, lang string
	)
	err := s.app.db.QueryRowContext(ctx,
		`SELECT id, email, coalesce(phone, ''), lang FROM orders WHERE number = $1`,
		strings.TrimSpace(number)).Scan(&id, &email, &phone, &lang)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return Internalf(err, "look up order")
	}
	to := email
	if channel == ChannelSMS {
		to = phone
	}
	if to == "" || !sameContact(contact, email, phone) {
		return nil
	}

	code, err := newLookupCode()
	if err != nil {
		return Internalf(err, "generate order code")
	}
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		// Locking the order makes two requests at once count one at a time.
		if _, err := tx.ExecContext(ctx, `SELECT 1 FROM orders WHERE id = $1 FOR UPDATE`, id); err != nil {
			return err
		}
		var recent, today int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FILTER (WHERE created_at > now() - $2::interval),
			       count(*) FILTER (WHERE created_at > now() - interval '1 day')
			FROM order_lookup_codes WHERE order_id = $1`,
			id, fmt.Sprintf("%d seconds", int(orderLookupGap.Seconds()))).Scan(&recent, &today); err != nil {
			return err
		}
		if recent > 0 || today >= orderLookupDaily {
			wait := "a minute"
			if today >= orderLookupDaily {
				wait = "tomorrow"
			}
			return &APIError{Status: http.StatusTooManyRequests, Code: "too_many_requests",
				Message: "A code was sent a moment ago. Check your messages, or ask again in " + wait + "."}
		}
		// A new code replaces any still open, so only the latest one works.
		if _, err := tx.ExecContext(ctx,
			`UPDATE order_lookup_codes SET expires_at = now() WHERE order_id = $1 AND expires_at > now()`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO order_lookup_codes (order_id, code_hash, expires_at) VALUES ($1, $2, $3)`,
			id, lookupCodeHash(id, code), time.Now().Add(orderLookupTTL))
		return err
	})
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return err
	}
	if err != nil {
		return Internalf(err, "store order code")
	}

	store := ""
	if st, err := s.app.stores.Get(ctx, s.app.storeOf(ctx)); err == nil {
		store = st.Name
	}
	n := Notification{
		Event: EventOrderLookupCode, Channel: channel, To: to, Language: lang,
		Data: map[string]string{
			"code": code, "order_number": number, "store_name": store,
			"expires_in_minutes": strconv.Itoa(int(orderLookupTTL / time.Minute)),
		},
		Secrets: []string{"code"},
	}
	// Sent after the reply, so how long a provider takes says nothing about
	// whether the contact matched. The store binding travels with the context.
	sendCtx := context.WithoutCancel(ctx)
	lookupSends.Add(1)
	go func() {
		defer lookupSends.Done()
		ctx, cancel := context.WithTimeout(sendCtx, orderLookupSendWait)
		defer cancel()
		if err := s.app.notifier.send(ctx, n); err != nil {
			// The code is never logged: it opens somebody's order.
			s.app.log.Error("order code could not be sent", "order", number, "channel", channel, "error", err)
		}
	}()
	return nil
}

// VerifyLookupCode spends one try and, when code is the order's open code,
// returns the order and its access token. A wrong, spent or expired code is
// one answer, so it says nothing about which.
func (s *Orders) VerifyLookupCode(ctx context.Context, number, code string) (*OrderLookup, error) {
	wrong := &APIError{Status: http.StatusUnprocessableEntity, Code: "invalid_code",
		Message: "That code is not right, or it has expired. Check it, or ask for a new one."}
	code = strings.TrimSpace(code)
	if len(code) != 4 {
		return nil, wrong
	}
	var id int64
	err := s.app.db.QueryRowContext(ctx,
		`SELECT id FROM orders WHERE number = $1`, strings.TrimSpace(number)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, wrong
	}
	if err != nil {
		return nil, Internalf(err, "look up order")
	}
	// The try is charged and the code matched in one statement, as a password
	// reset code is (tryResetCode): a burst of guesses queues on the row lock
	// and the sixth finds the tries spent. A right code is spent with it.
	var opened bool
	err = s.app.db.QueryRowContext(ctx, `
		WITH tried AS (
			UPDATE order_lookup_codes SET attempts = attempts + 1,
			       expires_at = CASE WHEN code_hash = $2 THEN now() ELSE expires_at END
			WHERE order_id = $1 AND expires_at > now() AND attempts < $3
			RETURNING code_hash)
		SELECT true FROM tried WHERE code_hash = $2`,
		id, lookupCodeHash(id, code), orderLookupAttempts).Scan(&opened)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, wrong
	}
	if err != nil {
		return nil, Internalf(err, "check order code")
	}

	var token string
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT access_token FROM orders WHERE id = $1`, id).Scan(&token); err != nil {
		return nil, Internalf(err, "read order token")
	}
	order, err := s.GetForGuest(ctx, number, token)
	if err != nil {
		return nil, err
	}
	return &OrderLookup{Order: order, AccessToken: token}, nil
}

// OpenWithContact returns the order and its access token when contact is the
// email or phone on it: the storefront's way in without a code (owner,
// 2026-10-27, reversing D115's code). An unknown number and a wrong contact
// are one answer, so the reply says nothing about which orders exist.
func (s *Orders) OpenWithContact(ctx context.Context, number, contact string) (*OrderLookup, error) {
	none := NotFoundf("No order matches that number and email or phone. Check both and try again.")
	var (
		email, phone, token string
	)
	err := s.app.db.QueryRowContext(ctx,
		`SELECT email, coalesce(phone, ''), access_token FROM orders WHERE number = $1`,
		strings.TrimSpace(number)).Scan(&email, &phone, &token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, none
	}
	if err != nil {
		return nil, Internalf(err, "look up order")
	}
	if !sameContact(contact, email, phone) {
		return nil, none
	}
	order, err := s.GetForGuest(ctx, number, token)
	if err != nil {
		return nil, err
	}
	return &OrderLookup{Order: order, AccessToken: token}, nil
}

// sameContact is whether contact is the order's email, ignoring case and
// spaces, or its phone, by the last ten digits so a country code and spacing
// do not matter.
func sameContact(contact, email, phone string) bool {
	contact = strings.TrimSpace(contact)
	if strings.Contains(contact, "@") {
		return email != "" && strings.EqualFold(contact, strings.TrimSpace(email))
	}
	a, b := lastDigits(contact, 10), lastDigits(phone, 10)
	return len(a) >= 7 && a == b
}

func lastDigits(s string, n int) string {
	var d []byte
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			d = append(d, s[i])
		}
	}
	if len(d) > n {
		d = d[len(d)-n:]
	}
	return string(d)
}

func newLookupCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(10_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%04d", n.Int64()), nil
}

// lookupCodeHash keys a code to its order, because two orders can hold the
// same four digits.
func lookupCodeHash(orderID int64, code string) string {
	return hashToken(fmt.Sprintf("order:%d:%s", orderID, code))
}

// waitForLookupSends waits for the codes in flight, up to max.
func (a *App) waitForLookupSends(max time.Duration) {
	done := make(chan struct{})
	go func() {
		lookupSends.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(max):
	}
}

func (a *App) handleRequestLookupCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Contact string `json:"contact"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if strings.TrimSpace(in.Contact) == "" {
		RespondError(w, r, Validationf("contact is the email address or phone number on the order"))
		return
	}
	if err := a.orders.RequestLookupCode(r.Context(), r.PathValue("number"), in.Contact); err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusAccepted, map[string]int{"expires_in_minutes": int(orderLookupTTL / time.Minute)})
}

func (a *App) handleVerifyLookupCode(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code string `json:"code"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	out, err := a.orders.VerifyLookupCode(r.Context(), r.PathValue("number"), in.Code)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}

func (a *App) handleOpenOrder(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Contact string `json:"contact"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if strings.TrimSpace(in.Contact) == "" {
		RespondError(w, r, Validationf("contact is the email address or phone number on the order"))
		return
	}
	// Every well-formed try counts, matched or not: a limit on failures alone
	// would say which tries matched.
	if _, ok := a.orderOpens.take(limitKey(clientIP(r)), time.Now()); !ok {
		RespondError(w, r, &APIError{Status: http.StatusTooManyRequests, Code: "too_many_requests",
			Message: "Too many tries. Wait a while and try again."})
		return
	}
	out, err := a.orders.OpenWithContact(r.Context(), r.PathValue("number"), in.Contact)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, out)
}
