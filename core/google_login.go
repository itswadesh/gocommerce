package gocommerce

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Signing in to the panel with Google (D237).
//
// The panel sends the browser to Google and is sent back with a code. It keeps
// the state, nonce and PKCE verifier in the tab, and posts them here with the
// code; only this side holds the client secret. So no third-party script runs on
// the sign-in screen, no cookie is added, and no session token passes through an
// address. It ends in the answer the other sign-in routes give.

const googleTokenEndpoint = "https://oauth2.googleapis.com/token"

// googleSignIn is what the panel brings back from Google.
type googleSignIn struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	Nonce        string `json:"nonce"`
	RedirectURI  string `json:"redirect_uri"`
}

// googleClaims is the part of Google's ID token this route reads.
type googleClaims struct {
	Issuer        string `json:"iss"`
	Audience      string `json:"aud"`
	Expires       int64  `json:"exp"`
	Nonce         string `json:"nonce"`
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

// googleClientID is what auth-state hands the panel, and empty is what hides the
// button: not set up, half set up, or a demo, which signs every visitor in as
// its one account.
func (a *App) googleClientID() string {
	if a.cfg.Demo || a.cfg.GoogleClientSecret == "" {
		return ""
	}
	return a.cfg.GoogleClientID
}

func googleRefused() *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "google_refused",
		Message: "Google did not confirm that sign-in; try again"}
}

// googleNoAccount also answers for a disabled person, as a wrong phone code does.
func googleNoAccount() *APIError {
	return &APIError{Status: http.StatusForbidden, Code: "google_no_account",
		Message: "there is no account for that Google address here; ask a store's owner to invite you"}
}

// exchangeGoogleCode swaps the code for the person's claims.
//
// The token's signature is not checked, and need not be: OpenID Connect Core
// 3.1.3.7 allows that when it comes straight from the token endpoint over TLS,
// as it does here. The claims are still held to this client and this attempt.
func (a *App) exchangeGoogleCode(ctx context.Context, in googleSignIn) (*googleClaims, error) {
	endpoint := a.cfg.GoogleTokenURL
	if endpoint == "" {
		endpoint = googleTokenEndpoint
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {in.Code},
		"code_verifier": {in.CodeVerifier},
		"redirect_uri":  {in.RedirectURI},
		"client_id":     {a.cfg.GoogleClientID},
		"client_secret": {a.cfg.GoogleClientSecret},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, Internalf(err, "build the Google request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 10 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return nil, &APIError{Status: http.StatusBadGateway, Code: "google_unreachable",
			Message: "could not reach Google; try again"}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, googleRefused()
	}
	if res.StatusCode >= 500 {
		return nil, &APIError{Status: http.StatusBadGateway, Code: "google_unreachable",
			Message: "could not reach Google; try again"}
	}
	if res.StatusCode != http.StatusOK {
		return nil, googleRefused()
	}
	var answer struct {
		IDToken string `json:"id_token"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, googleRefused()
	}
	claims, err := readGoogleToken(answer.IDToken)
	if err != nil {
		return nil, err
	}
	if claims.Issuer != "https://accounts.google.com" && claims.Issuer != "accounts.google.com" ||
		claims.Audience != a.cfg.GoogleClientID ||
		claims.Nonce == "" || claims.Nonce != in.Nonce ||
		time.Now().Unix() >= claims.Expires ||
		claims.Email == "" || !claims.EmailVerified {
		return nil, googleRefused()
	}
	return claims, nil
}

func readGoogleToken(token string) (*googleClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, googleRefused()
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, googleRefused()
	}
	var claims googleClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, googleRefused()
	}
	return &claims, nil
}

// SignInWithGoogle signs in the person on the address Google vouches for, or
// makes one where sign-up is open; the bool says it made one.
func (s *Superusers) SignInWithGoogle(ctx context.Context, in googleSignIn) (*Superuser, *Session, bool, error) {
	if s.app.googleClientID() == "" {
		return nil, nil, false, &APIError{Status: http.StatusConflict, Code: "google_unavailable",
			Message: "this install does not offer signing in with Google"}
	}
	if in.Code == "" || in.CodeVerifier == "" || in.Nonce == "" || in.RedirectURI == "" {
		return nil, nil, false, Validationf("code, code_verifier, nonce and redirect_uri are all needed")
	}
	claims, err := s.app.exchangeGoogleCode(ctx, in)
	if err != nil {
		return nil, nil, false, err
	}
	email := normalizeEmail(claims.Email)
	su, err := s.personOnEmail(ctx, email)
	if err != nil {
		return nil, nil, false, err
	}
	created := false
	if su == nil {
		if !s.app.signupOpen(ctx) {
			return nil, nil, false, googleNoAccount()
		}
		if su, created, err = s.signUpGoogle(ctx, email); err != nil {
			return nil, nil, false, err
		}
	}
	if !su.disabled {
		if err := s.claimEmailAccount(ctx, su); err != nil {
			return nil, nil, false, err
		}
	}
	sess, err := s.issue(ctx, su.ID)
	if errors.Is(err, errPersonDisabled) {
		return nil, nil, false, googleNoAccount()
	}
	if err != nil {
		return nil, nil, false, err
	}
	return su, sess, created, nil
}

func (s *Superusers) personOnEmail(ctx context.Context, email string) (*Superuser, error) {
	su, err := s.scan(ctx, s.db.QueryRowContext(ctx,
		`SELECT `+superuserColumns+` FROM superusers WHERE email = $1`, email))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, Internalf(err, "look up email")
	}
	return su, nil
}

// signUpGoogle makes a person with no store and no password (an empty hash,
// which verifyPassword refuses), whose address is already proved. Two tabs can
// race here, so the loser reads the row the winner made.
func (s *Superusers) signUpGoogle(ctx context.Context, email string) (*Superuser, bool, error) {
	su, err := scanSuperuser(s.db.QueryRowContext(ctx, `
		INSERT INTO superusers (email, email_proved, password_hash, brand_host) VALUES ($1, $1, '', $2)
		ON CONFLICT DO NOTHING
		RETURNING `+superuserColumns, email, s.app.arrivalBrandHost(ctx)))
	if errors.Is(err, sql.ErrNoRows) {
		su, err = s.personOnEmail(ctx, email)
		if err == nil && su == nil {
			err = Internalf(sql.ErrNoRows, "read the account just made")
		}
		return su, false, err
	}
	if err != nil {
		return nil, false, Internalf(err, "create superuser")
	}
	su.Rights = []Right{}
	return su, true, nil
}

// claimEmailAccount is Google's proof reaching an address nobody had proved,
// where anybody may have typed it at sign-up. As with a password reset to that
// address, whoever typed it loses their way in: the password, any unproved
// phone, every session and every reset link. The honest person gets a password
// back through a reset. An address already proved is left exactly as it is.
//
// A proved phone is refused rather than wiped. Its holder is somebody real, who
// may be the platform's owner (D215), and one click should not cost them that;
// the address's owner, if that is someone else, still has the password reset.
func (s *Superusers) claimEmailAccount(ctx context.Context, su *Superuser) error {
	return InTx(ctx, s.db, func(tx *sql.Tx) error {
		var unproved, phoneProved bool
		if err := tx.QueryRowContext(ctx, `SELECT email_proved IS DISTINCT FROM email, phone_proved_at IS NOT NULL
			FROM superusers WHERE id = $1 FOR UPDATE`, su.ID).Scan(&unproved, &phoneProved); err != nil {
			return Internalf(err, "read the account")
		}
		if !unproved {
			return nil
		}
		if phoneProved {
			return &APIError{Status: http.StatusConflict, Code: "google_confirm_email",
				Message: "this account's email is not confirmed yet; sign in with your phone, then confirm it under your account"}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE superusers
			SET email_proved = email, password_hash = '', phone = NULL, updated_at = now()
			WHERE id = $1`, su.ID); err != nil {
			return Internalf(err, "prove email")
		}
		for _, q := range []string{`DELETE FROM superuser_sessions WHERE superuser_id = $1`,
			`DELETE FROM superuser_password_resets WHERE superuser_id = $1`} {
			if _, err := tx.ExecContext(ctx, q, su.ID); err != nil {
				return Internalf(err, "end the account's other ways in")
			}
		}
		su.Phone, su.passwordHash = "", ""
		return nil
	})
}

func (a *App) handleAuthWithGoogle(w http.ResponseWriter, r *http.Request) {
	var in googleSignIn
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	su, sess, created, err := a.superusers.SignInWithGoogle(r.Context(), in)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		a.log.Info("superuser created", "id", su.ID, "via", "google")
		status = http.StatusCreated
	}
	a.respondSignedIn(w, r, status, su, sess)
}
