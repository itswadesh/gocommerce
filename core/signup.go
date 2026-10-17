package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
// its first step an email; that would make opening a store depend on the
// install's mail, and an account made with somebody else's address becomes
// theirs the moment they reset its password. What is left of the oracle is
// rationed by signupLimit, which counts every attempt.

const (
	// signupsPerWindow over signupWindow, per client address. Behind a proxy
	// that does not pass the peer through, every client is the proxy, so this
	// is the install's rate rather than one person's; generous enough for a
	// store a minute, tight enough that the 409 is no way to walk a list.
	signupsPerWindow = 20
	signupWindow     = time.Hour
)

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

func tooManySignups(retryAfter time.Duration) *APIError {
	return &APIError{
		Status:  http.StatusTooManyRequests,
		Code:    "too_many_attempts",
		Message: fmt.Sprintf("too many sign-ups from this address; try again in %s", retryAfter.Round(time.Minute)),
	}
}

// SignUp makes a person with no store and signs them in.
func (s *Superusers) SignUp(ctx context.Context, email, password, clientIP string) (*Superuser, *Session, error) {
	if !s.app.cfg.Signup {
		return nil, nil, signupClosed()
	}
	if wait, ok := s.signups.take(clientIP, time.Now()); !ok {
		return nil, nil, tooManySignups(wait)
	}
	if err := validateCredentials(email, password); err != nil {
		return nil, nil, err
	}
	email = normalizeEmail(email)
	hash, err := hashPassword(password)
	if err != nil {
		return nil, nil, Internalf(err, "hash password")
	}

	// No membership row: whatever store the request reached by its host, the
	// person is staff of none until they open their own.
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO superusers (email, password_hash) VALUES ($1, $2)
		ON CONFLICT (email) DO NOTHING
		RETURNING `+superuserColumns, email, hash)
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

// windowLimit allows n events per key in any span of per. In-process, like
// loginThrottle, and for the same reason. It is a window rather than that
// throttle's growing delay because a sign-up is not a failure: an address
// that opened three stores yesterday has done nothing to be slowed for today.
type windowLimit struct {
	mu   sync.Mutex
	n    int
	per  time.Duration
	hits map[string][]time.Time
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
	// Every key is pruned on every call: the map is small (one entry per
	// client in the last window) and this keeps it from growing for ever.
	for k, times := range l.hits {
		kept := times[:0]
		for _, at := range times {
			if at.After(cutoff) {
				kept = append(kept, at)
			}
		}
		if len(kept) == 0 {
			delete(l.hits, k)
		} else {
			l.hits[k] = kept
		}
	}
	times := l.hits[key]
	if len(times) >= l.n {
		return times[0].Add(l.per).Sub(now), false
	}
	l.hits[key] = append(times, now)
	return 0, true
}
