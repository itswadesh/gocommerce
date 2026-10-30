package gocommerce

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// A closed store can keep a password (D169): the storefront shows the
// store-close message with a password field, and a visitor who knows it gets
// in — an owner showing the shop before it launches. The password is a secret
// of the store-close plugin, so it is sealed at rest (D110) and never in
// GET /api/plugins; the storefront is told only that there is one.
//
// What the visitor keeps is a pass, not the password: the expiry and an HMAC
// of the store and that expiry. The key is the password as stored: sealed
// (D110), that is a random nonce and ciphertext nobody outside the database
// has, so a stolen pass cannot be used to guess the password offline. Saving
// the password seals it afresh, so it also ends every pass at once, which is
// how an owner takes the preview back. On an install without a secrets key
// the stored value is the password itself, and a pass is only as strong as it.

const (
	storeCloseKey = "store-close"
	// storePassTTL is how long a visitor stays in before typing it again.
	storePassTTL = 30 * 24 * time.Hour
	// storePasswordTriesPerWindow is how many passwords one client may try
	// on one store in an hour. A shop's launch password is typed by a person
	// and is often short, so without it the route is a free oracle.
	storePasswordTriesPerWindow = 20
)

// StorePass is what a right password, or a pass still good, answers with.
type StorePass struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// storePassword is the password of the store ctx is bound to, "" when the
// store is open or closed without one, and the key its passes are signed
// with: the password as it is stored.
func (a *App) storePassword(r *http.Request) (password, passKey string, err error) {
	on, err := a.plugins.Enabled(r.Context(), storeCloseKey)
	if err != nil || !on {
		return "", "", err
	}
	password = a.plugins.String(r.Context(), storeCloseKey, "password")
	if password == "" {
		return "", "", nil
	}
	if err := a.db.QueryRowContext(r.Context(),
		`SELECT coalesce(settings->>'password', '') FROM plugins WHERE key = $1`,
		storeCloseKey).Scan(&passKey); err != nil {
		return "", "", err
	}
	return password, passKey, nil
}

func storePassMAC(passKey, store string, exp int64) []byte {
	m := hmac.New(sha256.New, []byte(passKey))
	m.Write([]byte("gocommerce-store-pass/v1\x00" + store + "\x00" + strconv.FormatInt(exp, 10)))
	return m.Sum(nil)
}

func newStorePass(passKey, store string, now time.Time) StorePass {
	exp := now.Add(storePassTTL).Unix()
	return StorePass{
		Token:     strconv.FormatInt(exp, 10) + "." + base64.RawURLEncoding.EncodeToString(storePassMAC(passKey, store, exp)),
		ExpiresAt: time.Unix(exp, 0).UTC(),
	}
}

// checkStorePass is whether token is a pass for this store and password that
// has not expired.
func checkStorePass(token, passKey, store string, now time.Time) (StorePass, bool) {
	expText, sig, ok := strings.Cut(token, ".")
	if !ok {
		return StorePass{}, false
	}
	exp, err := strconv.ParseInt(expText, 10, 64)
	if err != nil || now.Unix() >= exp {
		return StorePass{}, false
	}
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(mac, storePassMAC(passKey, store, exp)) {
		return StorePass{}, false
	}
	return StorePass{Token: token, ExpiresAt: time.Unix(exp, 0).UTC()}, true
}

func (a *App) handleStorePassword(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Password string `json:"password"`
		Token    string `json:"token"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	if in.Password == "" && in.Token == "" {
		RespondError(w, r, Validationf("send the password, or the token a right password answered with"))
		return
	}
	password, passKey, err := a.storePassword(r)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if password == "" {
		RespondError(w, r, NotFoundf("This store is not behind a password."))
		return
	}
	store, now := a.storeOf(r.Context()), time.Now()
	wrong := &APIError{Status: http.StatusUnprocessableEntity, Code: "invalid_password",
		Message: "That password is not right."}

	// A pass is 256 bits nobody can guess, so checking one costs no try: the
	// storefront checks it on every page.
	if in.Password == "" {
		pass, ok := checkStorePass(in.Token, passKey, store, now)
		if !ok {
			RespondError(w, r, wrong)
			return
		}
		Respond(w, http.StatusOK, pass)
		return
	}
	// Every try counts, right or wrong, as on handleOpenOrder.
	if _, ok := a.storePasswordTries.take(store+" "+limitKey(clientIP(r)), now); !ok {
		RespondError(w, r, &APIError{Status: http.StatusTooManyRequests, Code: "too_many_requests",
			Message: "Too many tries. Wait a while and try again."})
		return
	}
	// Hashed first, so the comparison takes as long whatever the lengths.
	got, want := sha256.Sum256([]byte(in.Password)), sha256.Sum256([]byte(password))
	if subtle.ConstantTimeCompare(got[:], want[:]) != 1 {
		RespondError(w, r, wrong)
		return
	}
	Respond(w, http.StatusOK, newStorePass(passKey, store, now))
}
