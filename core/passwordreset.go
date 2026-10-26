package gocommerce

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/mail"
	"strconv"
	"strings"
	"time"
)

// Password resets are how an operator gets back in without going through
// another operator.
//
// One row carries two credentials, and the email carries both (D95): a link
// holding a long random token, and a 6-digit code to type. Spending either
// deletes the row, so they live and die together. The token cannot be guessed;
// the code can, which is why it alone has a try count and why the public route
// caps how many codes it will issue.
//
// The row lives in its own hash-only table rather than in
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
//
// The code is the weaker of the two at rest. Anyone who can read the table can
// reverse its SHA-256 by hashing all million codes, which the token's cannot
// be. Accepted for a row that lives an hour at most and takes five guesses; a
// slow hash is the upgrade if the table is ever readable by someone who is not
// already the store.

// migration0069PasswordResetCodes puts the code beside the link token (D95).
// code_hash is nullable because rows issued before it have none, and those
// expire within the hour.
const migration0069PasswordResetCodes = `
ALTER TABLE superuser_password_resets
    ADD COLUMN code_hash text,
    ADD COLUMN attempts int NOT NULL DEFAULT 0;
`

// Six digits can be guessed, so the code is rationed per operator, wherever the
// guesses come from. Each code takes resetCodeAttempts tries. The public route
// issues nothing while the operator holds resetCodesLive unexpired rows, or has
// been sent resetCodesDaily in a day, spent-out ones included, which is why a
// row outlives its hour by a day. So someone with any number of addresses gets
// 5 codes × 5 guesses an hour but at most 10 × 5 = 50 guesses a day: a year of
// nonstop guessing opens the account about 2% of the time, against about 20%
// with the hourly cap alone. The team route is exempt from both, so an owner
// can always send a teammate a fresh email.
const (
	resetCodeAttempts = 5
	resetCodesLive    = 5
	resetCodesDaily   = 10
)

// errResetCapped says the public route has issued all it will for now. It never
// reaches a caller: the answer is the same 202 an unknown address gets.
var errResetCapped = errors.New("password reset codes capped")

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
	// EventSuperuserInvitation is the email carrying a team invitation's
	// link. Like the reset, a notification name and not an outbox event.
	EventSuperuserInvitation = "superuser.invitation"

	// What a 202 says about this store's ability to send anything at all.
	DeliveryEmail = "email"
	DeliveryNone  = "none"
)

// invalidResetToken is the single answer for a link or code that is unknown,
// expired, already spent or used up, and for a code sent with an address that
// has no account. The remedy for all of them is the same button, and telling
// them apart would say whether a reset, or an operator, ever existed.
func invalidResetToken() *APIError {
	return &APIError{
		Status:  http.StatusBadRequest,
		Code:    "invalid_token",
		Message: "this reset link or code is invalid or has expired",
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
	// Link says the email carries a link beside its code: whether the panel
	// URL of the brand the request came through is set (Config.PanelURL
	// without brands). Like Delivery it is configuration, the same for every
	// caller at one address, so the panel can say what to look for.
	Link bool `json:"link"`
}

// ResetProof is what spends a reset: the link's token, or the address the
// email went to with the code it carried. Exactly one kind; parse says which,
// or refuses.
type ResetProof struct {
	Token string
	Email string
	Code  string
}

// parse normalises a proof and refuses one that is neither kind or both. A
// refusal here is a typo said as one, and costs no try: nothing has been
// looked up.
func (p ResetProof) parse() (ResetProof, error) {
	email := normalizeEmail(p.Email)
	// Mail clients and phones paste a code with spaces in the middle.
	code := strings.Join(strings.Fields(p.Code), "")
	switch {
	case p.Token != "" && (email != "" || code != ""):
		return ResetProof{}, Validationf("send the link's token, or the email and the code, not both")
	case p.Token != "":
		return ResetProof{Token: p.Token}, nil
	case email == "" && code == "":
		return ResetProof{}, Validationf("send the link's token, or the email and the code")
	case email == "":
		return ResetProof{}, Validationf("email is required")
	case !isResetCode(code):
		return ResetProof{}, Validationf("the code is the 6 digits in the email")
	}
	return ResetProof{Email: email, Code: code}, nil
}

func isResetCode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// accepted is the one answer a reset request gets, whoever asked.
func (s *Superusers) accepted(delivery string, b Brand) ResetRequest {
	return ResetRequest{Accepted: true, Delivery: delivery, Link: b.PanelURL != ""}
}

// ResetTarget is everything a lookup will say about a link: when it dies.
// Deliberately not the address — an endpoint that turned a token into an email
// would be the one place in this flow that confirms whose account it is.
type ResetTarget struct {
	ExpiresAt time.Time `json:"expires_at"`
}

// RequestReset issues a reset link and code for an address and mails them.
//
// The answer is identical whether or not the address belongs to an operator,
// and whether or not that operator is at a cap and is sent nothing, and a
// caller MUST NOT branch on anything but the error: the login endpoint
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
	if !s.app.notifier.delivers(ctx, ChannelEmail) {
		return s.accepted(DeliveryNone, s.app.Brand(ctx)), nil
	}

	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM superusers WHERE email = $1`, email).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Nothing written, nothing sent, same answer.
		return s.accepted(DeliveryEmail, s.app.Brand(ctx)), nil
	}
	if err != nil {
		return ResetRequest{}, Internalf(err, "look up superuser")
	}

	token, code, err := s.issueReset(ctx, id, true)
	if errors.Is(err, errResetCapped) {
		return s.accepted(DeliveryEmail, s.app.Brand(ctx)), nil
	}
	if err != nil {
		return ResetRequest{}, err
	}
	s.deliverReset(ctx, id, email, token, code, s.app.personMailBrand(ctx, id))
	return s.accepted(DeliveryEmail, s.app.Brand(ctx)), nil
}

// RequestResetFor mails a link and code to an operator the caller named by id:
// the team.write door, which replaces an owner choosing somebody's password for
// them. It is not an escalation — team.write can already set that password
// outright — and it is the practice invitations exist to end, applied to
// getting back in rather than to joining.
//
// Unlike the public route this one says plainly that the operator is not there:
// a caller who can reach it can already list the team, so there is nothing to
// enumerate, and no throttle for the same reason. Nor is it capped, so an owner
// can always send a fresh email to a teammate whose codes a stranger used up.
func (s *Superusers) RequestResetFor(ctx context.Context, id int64) (ResetRequest, error) {
	var email string
	// Only this store's staff: one store's team screen has no business mailing
	// links to people who work only elsewhere.
	err := s.db.QueryRowContext(ctx,
		`SELECT email FROM superusers WHERE id = $1 AND `+memberHere, id).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return ResetRequest{}, NotFoundf("superuser %d not found", id)
	}
	if err != nil {
		return ResetRequest{}, Internalf(err, "look up superuser")
	}

	// The teammate is mailed as the store's: its brand names the email and
	// its panel is where the link leads (D114).
	brand := s.app.StoreBrand(ctx)
	if !s.app.notifier.delivers(ctx, ChannelEmail) {
		return s.accepted(DeliveryNone, brand), nil
	}

	token, code, err := s.issueReset(ctx, id, false)
	if err != nil {
		return ResetRequest{}, err
	}
	s.deliverReset(ctx, id, email, token, code, brand)
	return s.accepted(DeliveryEmail, brand), nil
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

// ConfirmReset spends a link or a code: a new password, every session ended,
// every other link and code that operator holds dropped, and a fresh session
// handed back. A code is first turned into the token of the row it opens, so
// from there both kinds take the same path.
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
func (s *Superusers) ConfirmReset(ctx context.Context, proof ResetProof, password, clientIP string) (*Superuser, *Session, error) {
	proof, err := proof.parse()
	if err != nil {
		return nil, nil, err
	}
	// A token names nobody, so its guesses are charged to the client alone; a
	// code is charged to the address it was sent with too, as a login is.
	if retryAfter, ok := s.resets.blocked(proof.Email, clientIP); ok {
		return nil, nil, tooManyResetAttempts(retryAfter)
	}
	// Before the link or code is touched, so a typo costs neither the link nor
	// one of the code's tries.
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

	tokenHash := hashToken(proof.Token)
	if proof.Code != "" {
		tokenHash, err = s.tryResetCode(ctx, proof.Email, proof.Code)
		if err != nil {
			return nil, nil, err
		}
		if tokenHash == "" {
			s.resets.fail(proof.Email, clientIP)
			return nil, nil, invalidResetToken()
		}
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
			RETURNING superuser_id`, tokenHash).Scan(&superuserID)
		if errors.Is(err, sql.ErrNoRows) {
			return invalidResetToken()
		}
		if err != nil {
			return Internalf(err, "claim password reset")
		}

		// Revoking the link you can see closes the door: every other link and
		// code that operator holds dies with the one that was used.
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
			// Guessing is the only thing worth charging for here.
			s.resets.fail(proof.Email, clientIP)
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

// tryResetCode spends one of a code's tries and returns the token hash of the
// row the code opens, or "" when it opens none.
//
// The try is taken and the code matched in one statement, committed on its own
// so a wrong code stays charged. The row lock is what makes the cap hold: a
// burst of parallel guesses queues on it, each sees the count the one before
// left, and the sixth finds attempts at the limit and matches nothing. Checking
// the count in one statement and charging it in another would let the whole
// burst be tested before any charge landed.
func (s *Superusers) tryResetCode(ctx context.Context, email, code string) (string, error) {
	// An unknown address keeps id 0 and runs the same statement, which matches
	// nothing, so it gets a wrong code's answer by a wrong code's path.
	var id int64
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM superusers WHERE email = $1`, email).Scan(&id)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", Internalf(err, "look up superuser")
	}
	var tokenHash string
	err = s.db.QueryRowContext(ctx, `
		WITH tried AS (
			UPDATE superuser_password_resets SET attempts = attempts + 1
			WHERE superuser_id = $1 AND code_hash IS NOT NULL
			  AND expires_at > now() AND attempts < $3
			RETURNING token_hash, code_hash)
		SELECT token_hash FROM tried WHERE code_hash = $2`,
		id, resetCodeHash(id, code), resetCodeAttempts).Scan(&tokenHash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", Internalf(err, "try password reset code")
	}
	return tokenHash, nil
}

// issueReset writes one row and returns the only copies of its token and code
// that will ever exist. capped is the public route's: an operator already at
// either cap gets no row, and errResetCapped.
func (s *Superusers) issueReset(ctx context.Context, superuserID int64, capped bool) (token, code string, err error) {
	if token, err = newSessionToken(); err != nil {
		return "", "", Internalf(err, "generate password reset token")
	}
	if code, err = newResetCode(); err != nil {
		return "", "", Internalf(err, "generate password reset code")
	}
	err = InTx(ctx, s.db, func(tx *sql.Tx) error {
		if capped {
			// Locking the operator makes concurrent requests count one at a
			// time; without it a burst would each see room for one more.
			if _, err := tx.ExecContext(ctx,
				`SELECT 1 FROM superusers WHERE id = $1 FOR UPDATE`, superuserID); err != nil {
				return Internalf(err, "lock superuser")
			}
			var live, today int
			if err := tx.QueryRowContext(ctx, `
				SELECT count(*) FILTER (WHERE expires_at > now()),
				       count(*) FILTER (WHERE created_at > now() - interval '1 day')
				FROM superuser_password_resets WHERE superuser_id = $1`,
				superuserID).Scan(&live, &today); err != nil {
				return Internalf(err, "count password resets")
			}
			if live >= resetCodesLive || today >= resetCodesDaily {
				return errResetCapped
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO superuser_password_resets (token_hash, code_hash, superuser_id, expires_at)
			VALUES ($1, $2, $3, $4)`,
			hashToken(token), resetCodeHash(superuserID, code), superuserID,
			time.Now().Add(passwordResetTTL)); err != nil {
			return Internalf(err, "store password reset")
		}
		return nil
	})
	if err != nil {
		return "", "", err
	}
	// Opportunistic sweep, as Superusers.issue does for sessions. A dedicated
	// janitor for a table this small would be more machinery than the problem
	// deserves. A row is kept a day past its hour, because the daily cap counts
	// it; every claim and lookup checks expires_at, so a kept row opens nothing.
	_, _ = s.db.ExecContext(ctx,
		`DELETE FROM superuser_password_resets WHERE created_at < now() - interval '1 day'`)
	return token, code, nil
}

func newResetCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// resetCodeHash keys a code to its operator, because two operators can hold the
// same six digits: a code is only ever looked up with the address it went to.
func resetCodeHash(superuserID int64, code string) string {
	return hashToken(fmt.Sprintf("%d:%s", superuserID, code))
}

// resetURL is where the link points: the panel URL of a configured brand, and
// never anything from the request.
//
// acceptURL on an install without brands derives its host from r.Host and
// X-Forwarded-Host, which is safe there only because its one caller is an
// authenticated owner who reads the URL out of the response himself. A reset
// inverts every one of those properties — unauthenticated, triggered by
// whoever wants the reset, delivered to a third party — so a derived link
// would let `X-Forwarded-Host: evil.example` mail a real operator a genuine
// email that hands their live token to a host the attacker chose. The request
// may choose which brand (BrandFrom matches its host against the brands'
// panel hosts), never what the link says. This function takes a Brand and no
// *http.Request precisely so that there is nothing for a later refactor to
// reach for.
//
// With the brand's PanelURL unset it returns "", and the email carries only
// the code.
func resetURL(b Brand, token string) string {
	if b.PanelURL == "" {
		return ""
	}
	return b.PanelURL + "/admin/auth/reset-password/" + token
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
func (s *Superusers) deliverReset(reqCtx context.Context, id int64, email, token, code string, brand Brand) {
	sendCtx := s.identityMailScope(reqCtx, id)
	data := map[string]string{
		// Deliberately not customer_email or customer_name: the recipient is an
		// operator, and reusing the order vocabulary would let an order
		// template render for them.
		"operator_email": email,
		// Still the link's token, as it always was: a store may have saved its
		// own copy of this template, and that copy must keep meaning the same.
		"reset_token":        token,
		"reset_code":         code,
		"expires_in_minutes": strconv.Itoa(int(passwordResetTTL / time.Minute)),
		// What the email calls the product, and whose panel the link opens.
		"brand_name": brand.Name,
	}
	// The absence of reset_url is meaningful, and a template branches on it.
	if link := resetURL(brand, token); link != "" {
		data["reset_url"] = link
	}

	s.deliveries.Add(1)
	go func() {
		defer s.deliveries.Done()
		// Detached from the request, which ends with the 202.
		ctx, cancel := context.WithTimeout(sendCtx, resetNotifyTimeout)
		defer cancel()
		if err := s.app.notifier.send(ctx, Notification{
			Event:    EventSuperuserPasswordReset,
			Channel:  ChannelEmail,
			To:       email,
			Language: s.app.cfg.DefaultLanguage,
			Data:     data,
			Secrets:  []string{"reset_token", "reset_code", "reset_url"},
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

// installScope binds a context to no store at all, even where a default store
// would otherwise apply: a statement run with it sees no store's rows, so a
// notification sent with it uses the built-in template and the email provider
// configured for the install (its environment), which no store can edit or
// read.
const installScope = "install"

// identityMailScope is the store an email carrying a credential for person id
// may go out under. A reset link is good for every store the person works in,
// and a store's template and provider account are its owner's to edit and
// read, so the store the request came through may carry it only when that is
// the person's one store — its owner can already do anything there. Anyone
// else's goes by the install's own channel.
func (s *Superusers) identityMailScope(reqCtx context.Context, id int64) context.Context {
	detached := context.WithoutCancel(reqCtx)
	here := s.app.storeOf(reqCtx)
	ms, err := s.app.stores.MembershipsOf(reqCtx, id)
	if err == nil && here != "" && len(ms) == 1 && ms[0].StoreID == here {
		return WithStore(detached, here)
	}
	return WithStore(detached, installScope)
}
