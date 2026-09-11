package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strconv"
	"time"
)

// Password resets are how an operator gets back in without going through
// another operator.
//
// The token lives in its own hash-only table rather than in
// superuser_invitations, because accepting an invitation INSERTs a superuser:
// one token namespace with two redeemers, one of which mints accounts, is a
// hazard no `kind` column removes. The invitation table would also fight this
// flow on its own terms — its unique index over open invitations would make a
// reset and a pending invite for one address mutually exclusive, and Invite
// deletes the outstanding row for an address, which would silently destroy a
// live reset link.
//
// Nothing here writes an outbox event, and that is deliberate. Outbox rows are
// never deleted — markPublished only stamps published_at — so an event carrying
// the link would keep a working reset token in a jsonb column forever, which is
// exactly what storing only the SHA-256 exists to prevent. The token cannot be
// omitted and re-derived either: the store never knows it again after the send.

const (
	// An invitation gets a week because an invitee has to find a moment. A
	// reset is asked for by somebody standing at a login form, and the link
	// points at an account that already has power over the store.
	passwordResetTTL = time.Hour

	// The delivery goroutine's own bound, and deliberately not
	// Config.HandlerTimeout: that defaults to 10s while the shipped SendGrid
	// backend carries a 15s client timeout, so a handler-timeout parent would
	// cancel the only in-repo notifier mid-flight.
	resetNotifyTimeout = 30 * time.Second

	// EventSuperuserPasswordReset names the notification, not an outbox event.
	// It is declared here beside the flow rather than in events.go, which is the
	// frozen outbox taxonomy: this never reaches the bus or outbox_events, and
	// listing it there would promise consumers an audit trail that the sibling
	// operator changes — Update, UpdateSelf, SetRole, Delete — do not emit.
	EventSuperuserPasswordReset = "superuser.password_reset"

	// What a 202 says about this store's ability to send anything at all.
	DeliveryEmail = "email"
	DeliveryNone  = "none"
)

// invalidResetToken is the single answer for a token that is unknown, expired,
// already spent or empty. The remedy for all four is the same button, and
// telling them apart would say whether a token ever existed.
func invalidResetToken() *APIError {
	return &APIError{
		Status:  http.StatusBadRequest,
		Code:    "invalid_token",
		Message: "this password reset link is invalid or has expired",
	}
}

func tooManyResetAttempts(retryAfter time.Duration) *APIError {
	return &APIError{
		Status:  http.StatusTooManyRequests,
		Code:    "too_many_attempts",
		Message: fmt.Sprintf("too many failed attempts; try again in %s", retryAfter.Round(time.Second)),
	}
}

// ResetRequest is the answer to "send me a link". Accepted is always true: the
// route cannot say whether the address belongs to anybody. Delivery is a fact
// about the store's configuration, constant for every caller.
type ResetRequest struct {
	Accepted bool   `json:"accepted"`
	Delivery string `json:"delivery"`
}

// ResetTarget is everything a lookup will say about a link: when it dies.
// Deliberately not the address — an endpoint that turned a token into an email
// would be the one place in this flow that confirms whose account it is.
type ResetTarget struct {
	ExpiresAt time.Time `json:"expires_at"`
}

// RequestReset issues a reset link for an address and mails it.
//
// The answer is identical whether or not the address belongs to an operator,
// and a caller MUST NOT branch on anything but the error: the login endpoint
// already refuses to be an account oracle, and a reset route that leaked the
// same fact would undo it. The email itself is sent from a goroutine after this
// returns, so a real address does not pay a vendor's round trip that an unknown
// one skips — the timing would say what the body will not.
func (s *Superusers) RequestReset(ctx context.Context, email, clientIP string) (ResetRequest, error) {
	email = normalizeEmail(email)
	if email == "" {
		return ResetRequest{}, Validationf("email is required")
	}
	// A property of the string that was typed, not of the account behind it, so
	// answering it is not an oracle.
	if _, err := mail.ParseAddress(email); err != nil {
		return ResetRequest{}, Validationf("%q is not a valid email address", email)
	}

	if retryAfter, ok := s.resets.blocked(email, clientIP); ok {
		return ResetRequest{}, tooManyResetAttempts(retryAfter)
	}
	// Charged before the lookup and never cleared: there is no outcome here
	// that vouches for the requester, and a charge that depended on whether the
	// address existed would be the oracle by another route.
	s.resets.fail(email, clientIP)

	// A link nobody can be sent is an hour of live credential bought for
	// nothing. The branch is on configuration, which is the same for every
	// caller, so it enumerates nothing.
	if !s.app.notifier.delivers(ChannelEmail) {
		return ResetRequest{Accepted: true, Delivery: DeliveryNone}, nil
	}

	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM superusers WHERE email = $1`, email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Nothing written, nothing sent, same answer.
		return ResetRequest{Accepted: true, Delivery: DeliveryEmail}, nil
	}
	if err != nil {
		return ResetRequest{}, Internalf(err, "look up superuser")
	}

	token, err := s.issueReset(ctx, id)
	if err != nil {
		return ResetRequest{}, err
	}
	s.deliverReset(email, token)
	return ResetRequest{Accepted: true, Delivery: DeliveryEmail}, nil
}

// RequestResetFor mails a link to an operator the caller named by id: the
// team.write door, which replaces an owner choosing somebody's password for
// them. It is not an escalation — team.write can already set that password
// outright — and it is the practice invitations exist to end, applied to
// getting back in rather than to joining.
//
// Unlike the public route this one says plainly that the operator is not there:
// a caller who can reach it can already list the team, so there is nothing to
// enumerate, and no throttle for the same reason.
func (s *Superusers) RequestResetFor(ctx context.Context, id int64) (ResetRequest, error) {
	var email string
	err := s.db.QueryRowContext(ctx,
		`SELECT email FROM superusers WHERE id = $1`, id).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return ResetRequest{}, NotFoundf("superuser %d not found", id)
	}
	if err != nil {
		return ResetRequest{}, Internalf(err, "look up superuser")
	}

	if !s.app.notifier.delivers(ChannelEmail) {
		return ResetRequest{Accepted: true, Delivery: DeliveryNone}, nil
	}

	token, err := s.issueReset(ctx, id)
	if err != nil {
		return ResetRequest{}, err
	}
	s.deliverReset(email, token)
	return ResetRequest{Accepted: true, Delivery: DeliveryEmail}, nil
}

// LookupReset says whether a link is still alive, so the reset screen can show
// a dead link before anybody types a password twice — the same lookup-then-
// confirm shape the invitation screen uses.
//
// It returns only the expiry. An invitation has a name and a rights list worth
// displaying; a reset has nothing to show that is not also an answer to "does
// this account exist".
func (s *Superusers) LookupReset(ctx context.Context, token, clientIP string) (*ResetTarget, error) {
	if retryAfter, ok := s.resets.blocked("", clientIP); ok {
		return nil, tooManyResetAttempts(retryAfter)
	}
	if token == "" {
		return nil, invalidResetToken()
	}
	var expires time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT expires_at FROM superuser_password_resets
		WHERE token_hash = $1 AND expires_at > now()`, hashToken(token)).Scan(&expires)
	if errors.Is(err, sql.ErrNoRows) {
		s.resets.fail("", clientIP)
		return nil, invalidResetToken()
	}
	if err != nil {
		return nil, Internalf(err, "read password reset")
	}
	return &ResetTarget{ExpiresAt: expires.UTC()}, nil
}

// ConfirmReset spends a link: a new password, every session ended, every other
// link that operator holds dropped, and a fresh session handed back.
//
// It is one transaction, and the password is validated and hashed before that
// transaction opens. Both matter. A two-transaction shape — claim the token,
// then set the password — has a real window where a crash between the two burns
// the operator's only link and leaves the old password standing, which for the
// last owner of a store is the lockout this whole feature exists to end. And
// hashPassword runs PBKDF2 at six hundred thousand iterations, which is far too
// long to hold a row lock on superusers; that is the same argument the engine
// makes about never holding a transaction across a gateway call.
//
// Signing them in is Invitations.Accept's argument verbatim: somebody who has
// just typed a new password twice should not then be shown a login form, where
// fumbling it looks like a broken link. The session is issued after the commit,
// so it postdates the delete that ends every session they had.
func (s *Superusers) ConfirmReset(ctx context.Context, token, password, clientIP string) (*Superuser, *Session, error) {
	if retryAfter, ok := s.resets.blocked("", clientIP); ok {
		return nil, nil, tooManyResetAttempts(retryAfter)
	}
	if token == "" {
		return nil, nil, invalidResetToken()
	}
	// Before the token is touched, so a typo does not burn the link.
	if err := validatePassword(password); err != nil {
		return nil, nil, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, nil, Internalf(err, "hash password")
	}
	// Resolved before the transaction opens: see the note on scan.
	rights, err := s.roles.All(ctx)
	if err != nil {
		return nil, nil, err
	}

	var su *Superuser
	err = InTx(ctx, s.db, func(tx *sql.Tx) error {
		// The spend IS the claim, so two people opening one link cannot both
		// win: under Read Committed the second blocks on the row lock,
		// re-evaluates the predicate, and finds nothing. The expiry is checked
		// here rather than by the sweeper, so an unswept store stays correct.
		var superuserID int64
		err := tx.QueryRowContext(ctx, `
			DELETE FROM superuser_password_resets
			WHERE token_hash = $1 AND expires_at > now()
			RETURNING superuser_id`, hashToken(token)).Scan(&superuserID)
		if errors.Is(err, sql.ErrNoRows) {
			return invalidResetToken()
		}
		if err != nil {
			return Internalf(err, "claim password reset")
		}

		// Revoking the link you can see closes the door: every other link that
		// operator holds dies with the one that was used.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM superuser_password_resets WHERE superuser_id = $1`,
			superuserID); err != nil {
			return Internalf(err, "revoke password reset links")
		}

		// scanSuperuser rather than the resolving scan: this is inside a
		// transaction, and that one would take a second pool connection.
		row := tx.QueryRowContext(ctx, `
			UPDATE superusers SET password_hash = $2, updated_at = now()
			WHERE id = $1 RETURNING `+superuserColumns, superuserID, hash)
		if su, err = scanSuperuser(row); err != nil {
			return Internalf(err, "update superuser")
		}

		// Every session, with no keep-this-browser exception: a reset is how
		// somebody answers a compromise, and the session an intruder is holding
		// is precisely the one that must not survive. There is no caller
		// session to preserve here in any case.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM superuser_sessions WHERE superuser_id = $1`, superuserID); err != nil {
			return Internalf(err, "revoke sessions")
		}
		return nil
	})
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == "invalid_token" {
			// Guessing a token is the only thing worth charging for here.
			s.resets.fail("", clientIP)
		}
		return nil, nil, err
	}

	su.Rights = rights[su.Role]
	sess, err := s.issue(ctx, su.ID)
	if err != nil {
		// The password has already changed and the state is correct; only this
		// message is wrong, and signing in with the new password works.
		return nil, nil, err
	}
	return su, sess, nil
}

// issueReset writes one link and returns the only copy of its token that will
// ever exist.
func (s *Superusers) issueReset(ctx context.Context, superuserID int64) (string, error) {
	token, err := newSessionToken()
	if err != nil {
		return "", Internalf(err, "generate password reset token")
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO superuser_password_resets (token_hash, superuser_id, expires_at)
		VALUES ($1, $2, $3)`,
		hashToken(token), superuserID, time.Now().Add(passwordResetTTL)); err != nil {
		return "", Internalf(err, "store password reset")
	}
	// Opportunistic sweep, as Superusers.issue does for sessions. A dedicated
	// janitor for a table this small would be more machinery than the problem
	// deserves, and expiry is enforced by the predicate regardless.
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM superuser_password_resets WHERE expires_at < now()`)
	return token, nil
}

// resetURL is where the link points, built from configuration and never from
// the request.
//
// acceptURL derives its host from r.Host and X-Forwarded-Host, which is safe
// there only because its one caller is an authenticated owner who reads the URL
// out of the response himself. A reset inverts every one of those properties —
// unauthenticated, triggered by whoever wants the reset, delivered to a third
// party — so a derived link would let `X-Forwarded-Host: evil.example` mail a
// real operator a genuine email that hands their live token to a host the
// attacker chose. This function takes no *http.Request precisely so that there
// is nothing for a later refactor to reach for.
//
// With PanelURL unset it returns "", and the email carries the bare token to
// paste instead; the panel's reset route accepts one.
func (s *Superusers) resetURL(token string) string {
	if s.app.cfg.PanelURL == "" {
		return ""
	}
	return s.app.cfg.PanelURL + "/reset-password/" + token
}

// deliverReset sends the email off the request goroutine.
//
// Off it for two reasons: an inline vendor call would make a request for a real
// address measurably slower than one for an address that does not exist, which
// reopens the enumeration oracle the identical 202 exists to close; and the
// request's context is cancelled the moment that 202 is written, so the send
// needs a detached one with a bound of its own.
//
// A failure is logged and never returned. The row is written and the operator
// can ask again; what must not happen is a 500 that says the address exists.
func (s *Superusers) deliverReset(email, token string) {
	data := map[string]string{
		// Deliberately not customer_email or customer_name: the recipient is an
		// operator, and reusing the order vocabulary would let an order
		// template render for them.
		"operator_email":     email,
		"reset_token":        token,
		"expires_in_minutes": strconv.Itoa(int(passwordResetTTL / time.Minute)),
	}
	// The absence of reset_url is meaningful, and a template branches on it.
	if link := s.resetURL(token); link != "" {
		data["reset_url"] = link
	}

	s.deliveries.Add(1)
	go func() {
		defer s.deliveries.Done()
		ctx, cancel := context.WithTimeout(context.Background(), resetNotifyTimeout)
		defer cancel()
		if err := s.app.notifier.send(ctx, Notification{
			Event:    EventSuperuserPasswordReset,
			Channel:  ChannelEmail,
			To:       email,
			Language: s.app.cfg.DefaultLanguage,
			Data:     data,
		}); err != nil {
			// The token is never logged: the log is not a place a live
			// credential belongs.
			s.app.log.Error("superuser password reset email failed", "error", err)
		}
	}()
}

// waitForResetDelivery waits for the emails in flight, up to max.
//
// Capped, because shutdown must not hang on a stuck vendor. Tests use it as a
// deterministic seam, the way the outbox's delivered channel serves there.
func (s *Superusers) waitForResetDelivery(max time.Duration) {
	done := make(chan struct{})
	go func() {
		s.deliveries.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(max):
	}
}
