package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Signing up is how a person makes their own account, on an install that
// hosts stores for other people (Config.Signup). The account belongs to no
// store: the panel's next screen is the create form every signed-in person
// already has, so POST /api/stores/create stays the one way a store is opened.
//
// Unlike the login and reset routes, this one says when an address is taken.
// An open sign-up cannot refuse an address without saying why, short of making
// its first step an email; that would make opening a store wait on the mail,
// and an account made with somebody else's address becomes theirs the moment
// they reset its password. That last part holds only where a reset email can
// reach them, so sign-up stays shut until the install has email of its own
// (signupOpen). What is left of the oracle is rationed by Superusers.signups,
// which counts every attempt that could make an account or name a taken one.

const (
	// signupsPerWindow over signupWindow, per client address (an IPv6 /64).
	// Behind a proxy that does not pass the peer through, every client is the
	// proxy, so this is then the install's rate rather than one person's.
	signupsPerWindow = 20
	signupWindow     = time.Hour

	// storeOpensPerDay is how many stores one person may open in a day where
	// strangers can make accounts.
	storeOpensPerDay = 10
)

// signupOpen says whether the sign-in screen may offer an account, and
// whether POST /api/admin/signup makes one. Config.Signup is the install's
// choice; the rest is what that choice needs to be safe (signupSafe), and an
// owner: a fresh install belongs to whoever runs the installer, and a stranger
// who signed up first and opened a store would shut it (Bootstrap), leaving
// the first store with nobody in charge.
func (a *App) signupOpen(ctx context.Context) bool {
	if !a.cfg.Signup || !a.signupSafe(ctx) {
		return false
	}
	n, err := a.superusers.CountPeople(ctx)
	return err == nil && n > 0
}

// signupSafe is what an open sign-up needs of the install:
//
//   - Not on a demo, which signs every visitor in as its one account, so an
//     account of your own could never be signed back in to.
//   - Email of the install's own. A new person works in no store, or in one
//     they opened, so their reset goes out on the install's channel
//     (identityMailScope), and a reset is the only way an address a stranger
//     signed up with comes back to its owner.
func (a *App) signupSafe(ctx context.Context) bool {
	return !a.cfg.Demo && a.notifier.delivers(WithStore(context.WithoutCancel(ctx), installScope), ChannelEmail)
}

func signupClosed() *APIError {
	return &APIError{
		Status:  http.StatusForbidden,
		Code:    "signup_closed",
		Message: "this install does not take sign-ups; ask a store's owner to invite you",
	}
}

func emailTaken(email string) *APIError {
	return &APIError{
		Status:  http.StatusConflict,
		Code:    "email_taken",
		Message: fmt.Sprintf("%s already has an account; sign in, or reset the password if it is forgotten", email),
	}
}

// inMinutes is a wait as a person counts it: whole minutes, rounded up, so a
// wait of twenty seconds is never "0s".
func inMinutes(wait time.Duration) string {
	m := int((wait + time.Minute - 1) / time.Minute)
	if m <= 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", m)
}

func tooManySignups(retryAfter time.Duration) *APIError {
	return &APIError{
		Status:  http.StatusTooManyRequests,
		Code:    "too_many_attempts",
		Message: "too many sign-ups from this address; try again in " + inMinutes(retryAfter),
	}
}

func tooManyStoreOpens(retryAfter time.Duration) *APIError {
	return &APIError{
		Status:  http.StatusTooManyRequests,
		Code:    "too_many_attempts",
		Message: fmt.Sprintf("%d stores is a day's limit for one account; try again in %s", storeOpensPerDay, inMinutes(retryAfter)),
	}
}

// SignUp makes a person with no store and signs them in.
func (s *Superusers) SignUp(ctx context.Context, email, password, clientIP string) (*Superuser, *Session, error) {
	if !s.app.signupOpen(ctx) {
		return nil, nil, signupClosed()
	}
	// The shape is checked before the limit is charged: an address that is not
	// one, or a password that is too short, tells nobody anything about who has
	// an account, and charging for it would let a stream of empty bodies shut
	// the door on everybody behind one proxy.
	if err := validateCredentials(email, password); err != nil {
		return nil, nil, err
	}
	if wait, ok := s.signups.take(limitKey(clientIP), time.Now()); !ok {
		return nil, nil, tooManySignups(wait)
	}
	email = normalizeEmail(email)
	hash, err := hashPassword(password)
	if err != nil {
		return nil, nil, Internalf(err, "hash password")
	}

	// No membership row: whatever store the request reached by its host, the
	// person is staff of none until they open their own. They are recorded as
	// arriving through the brand they signed up on (D111).
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO superusers (email, password_hash, brand_host) VALUES ($1, $2, $3)
		ON CONFLICT (email) DO NOTHING
		RETURNING `+superuserColumns, email, hash, s.app.arrivalBrandHost(ctx))
	su, err := scanSuperuser(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, emailTaken(email)
	}
	if err != nil {
		return nil, nil, Internalf(err, "create superuser")
	}
	// In no store, so no role and nothing it grants. Empty rather than nil, so
	// the record reads the same as an operator's whose role grants nothing.
	su.Rights = []Right{}

	sess, err := s.issue(ctx, su.ID)
	if err != nil {
		// The account exists and its password works; only this sign-in failed.
		return nil, nil, err
	}
	return su, sess, nil
}

func (a *App) handleSignUp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	su, sess, err := a.superusers.SignUp(r.Context(), in.Email, in.Password, clientIP(r))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.log.Info("superuser created", "email", su.Email, "via", "signup")
	Respond(w, http.StatusCreated, authResponse{
		Token:     sess.Token,
		ExpiresAt: sess.ExpiresAt.UTC(),
		Record:    su,
	})
}

// limitKey is the client a limit counts: the address itself for IPv4, and its
// /64 for IPv6, which is what one subscriber is handed and can walk at will.
func limitKey(clientIP string) string {
	ip := net.ParseIP(clientIP)
	if ip == nil || ip.To4() != nil {
		return clientIP
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// windowLimit allows n events per key in any span of per. In-process, like
// loginThrottle, and for the same reason. It is a window rather than that
// throttle's growing delay because a sign-up is not a failure: an address
// that opened three stores yesterday has done nothing to be slowed for today.
type windowLimit struct {
	mu    sync.Mutex
	n     int
	per   time.Duration
	hits  map[string][]time.Time
	swept time.Time
}

func newWindowLimit(n int, per time.Duration) *windowLimit {
	return &windowLimit{n: n, per: per, hits: map[string][]time.Time{}}
}

// take records an event for key at now, or says how long until one is
// allowed again.
func (l *windowLimit) take(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-l.per)
	// The other keys are swept at most once a window, which is all it takes
	// to keep the map to the clients seen in the last two.
	if now.Sub(l.swept) > l.per {
		for k, times := range l.hits {
			if !times[len(times)-1].After(cutoff) {
				delete(l.hits, k)
			}
		}
		l.swept = now
	}
	times := l.hits[key]
	kept := times[:0]
	for _, at := range times {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= l.n {
		l.hits[key] = kept
		return kept[0].Add(l.per).Sub(now), false
	}
	l.hits[key] = append(kept, now)
	return 0, true
}
