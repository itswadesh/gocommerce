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

// A closed store can keep a password (D168): the storefront shows the
// store-close message with a password field, and a visitor who knows it gets
// in — an owner showing the shop before it launches. The password is a secret
// of the store-close plugin, so it is sealed at rest (D110) and never in
// GET /api/plugins; the storefront is told only that there is one.
//
// What the visitor keeps is a pass, not the password: the expiry and an HMAC
// of the store and that expiry, keyed by the password. It needs no table and
// no install key, and changing the password ends every pass at once, which is
// how an owner takes the preview back.
//
// ponytail: the HMAC key is the password itself, so a stolen pass lets its
// thief guess the password offline. Somebody holding a pass was let in
// already; derive the key from an install secret if the password ever guards
// more than a preview.

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
// store is open or closed without one.
func (a *App) storePassword(r *http.Request) (string, error) {
	on, err := a.plugins.Enabled(r.Context(), storeCloseKey)
	if err != nil || !on {
		return "", err
	}
	return a.plugins.String(r.Context(), storeCloseKey, "password"), nil
}

func storePassMAC(password, store string, exp int64) []byte {
	m := hmac.New(sha256.New, []byte(password))
	m.Write([]byte("gocommerce-store-pass/v1\x00" + store + "\x00" + strconv.FormatInt(exp, 10)))
	return m.Sum(nil)
}

func newStorePass(password, store string, now time.Time) StorePass {
	exp := now.Add(storePassTTL).Unix()
	return StorePass{
		Token:     strconv.FormatInt(exp, 10) + "." + base64.RawURLEncoding.EncodeToString(storePassMAC(password, store, exp)),
		ExpiresAt: time.Unix(exp, 0).UTC(),
	}
}

// checkStorePass is whether token is a pass for this store and password that
// has not expired.
func checkStorePass(token, password, store string, now time.Time) (StorePass, bool) {
	expText, sig, ok := strings.Cut(token, ".")
	if !ok {
		return StorePass{}, false
	}
	exp, err := strconv.ParseInt(expText, 10, 64)
	if err != nil || now.Unix() >= exp {
		return StorePass{}, false
	}
	mac, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(mac, storePassMAC(password, store, exp)) {
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
	password, err := a.storePassword(r)
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
		pass, ok := checkStorePass(in.Token, password, store, now)
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
	Respond(w, http.StatusOK, newStorePass(password, store, now))
}
