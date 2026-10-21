package gocommerce

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Secrets at rest (D110).
//
// A few credentials have to be read back in full rather than checked against
// a hash: a payment gateway's secret key, to call the gateway; the copy of an
// API key the owner can see again (M46); a webhook endpoint's signing secret,
// to sign. They are sealed with AES-256-GCM under a key the install keeps in
// its environment and never in the database, so a copy of the database — a
// backup, a dump handed to somebody, a replica — holds ciphertext.
//
// A sealed value names the key that sealed it, so a value from before a
// rotation is still found under the previous key. Its additional data names
// the store and the field, so the same ciphertext pasted into another store's
// row, or into another field, fails authentication instead of opening.
//
// Without a key nothing changes: values are stored readable, as they always
// were, and doctor counts them. A value without the "sealed:" prefix is read
// as itself, which is what lets an install switch a key on with no migration
// and seal what it already has afterwards (`gocommerce secrets seal`).

// The environment variables the keys come from. Never a flag: a flag shows in
// the process list.
const (
	SecretsKeyEnv         = "GOCOMMERCE_SECRETS_KEY"
	SecretsKeyPreviousEnv = "GOCOMMERCE_SECRETS_KEY_PREVIOUS"
)

const (
	sealedPrefix = "sealed:"
	sealedV1     = "sealed:v1:"
	// sealVersion leads the additional data, so a later envelope that binds
	// something else can never be mistaken for this one.
	sealVersion = "gocommerce-secret/v1"
)

// ErrSecretUnreadable is a sealed value this install cannot open: sealed
// under a key it does not have, or for another store or field, or damaged.
// Every reader in the engine treats it as unset.
var ErrSecretUnreadable = errors.New("the stored secret cannot be opened")

type sealKey struct {
	// id is the first 8 hex characters of the key's SHA-256: enough to tell
	// two keys apart in an envelope, nothing an attacker can use.
	id   string
	aead cipher.AEAD
}

// secretKeys are the install's keys. A zero value has none, and seals
// nothing.
type secretKeys struct {
	current, previous *sealKey
}

func newSecretKeys(current, previous string) (secretKeys, error) {
	current, previous = strings.TrimSpace(current), strings.TrimSpace(previous)
	var k secretKeys
	if current == "" {
		if previous != "" {
			return k, fmt.Errorf("gocommerce: %s is set without %s: the previous key only opens what it sealed, so set the new key beside it",
				SecretsKeyPreviousEnv, SecretsKeyEnv)
		}
		return k, nil
	}
	var err error
	if k.current, err = parseSecretsKey(SecretsKeyEnv+" (Config.SecretsKey)", current); err != nil {
		return secretKeys{}, err
	}
	if previous != "" {
		if k.previous, err = parseSecretsKey(SecretsKeyPreviousEnv+" (Config.SecretsKeyPrevious)", previous); err != nil {
			return secretKeys{}, err
		}
	}
	return k, nil
}

// parseSecretsKey accepts standard or URL-safe base64, padded or not, because
// the tools that make a key disagree. The value is never part of the error.
func parseSecretsKey(name, value string) (*sealKey, error) {
	var raw []byte
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(value); err == nil && len(b) == 32 {
			raw = b
			break
		}
	}
	if raw == nil {
		return nil, fmt.Errorf("gocommerce: %s must be 32 random bytes in base64, and what is set does not decode to that; make one with `openssl rand -base64 32`",
			name)
	}
	block, err := aes.NewCipher(raw)
	if err != nil {
		return nil, fmt.Errorf("gocommerce: %s: %w", name, err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gocommerce: %s: %w", name, err)
	}
	sum := sha256.Sum256(raw)
	return &sealKey{id: hex.EncodeToString(sum[:4]), aead: aead}, nil
}

func (k secretKeys) byID(id string) *sealKey {
	switch {
	case k.current != nil && k.current.id == id:
		return k.current
	case k.previous != nil && k.previous.id == id:
		return k.previous
	}
	return nil
}

// sealAdditionalData is what a value is bound to. NUL separates the parts
// because neither a store id nor a field label can contain one, so no two
// pairs run together into the same bytes.
func sealAdditionalData(store, field string) []byte {
	return []byte(sealVersion + "\x00" + store + "\x00" + field)
}

func (k secretKeys) seal(store, field, plain string) (string, error) {
	key := k.current
	nonce := make([]byte, key.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := key.aead.Seal(nonce, nonce, []byte(plain), sealAdditionalData(store, field))
	return sealedV1 + key.id + ":" + base64.RawURLEncoding.EncodeToString(out), nil
}

// open returns a readable value as it is, and a sealed one opened together
// with the key that opened it.
func (k secretKeys) open(store, field, stored string) (string, *sealKey, error) {
	if !isSealed(stored) {
		return stored, nil, nil
	}
	rest, ok := strings.CutPrefix(stored, sealedV1)
	if !ok {
		return "", nil, fmt.Errorf("%w: it is sealed in a format this version does not know", ErrSecretUnreadable)
	}
	// Only a well-formed id is ever repeated in an error, because errors are
	// logged: a value that merely starts with the prefix is somebody's typing,
	// and what follows it may be the secret itself.
	id, body, ok := strings.Cut(rest, ":")
	if !ok || !isKeyID(id) {
		return "", nil, fmt.Errorf("%w: it is malformed", ErrSecretUnreadable)
	}
	key := k.byID(id)
	if key == nil {
		return "", nil, fmt.Errorf("%w: it was sealed under key %s, which this install does not have", ErrSecretUnreadable, id)
	}
	data, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil || len(data) < key.aead.NonceSize() {
		return "", nil, fmt.Errorf("%w: it is damaged", ErrSecretUnreadable)
	}
	n := key.aead.NonceSize()
	plain, err := key.aead.Open(nil, data[:n], data[n:], sealAdditionalData(store, field))
	if err != nil {
		return "", nil, fmt.Errorf("%w: under key %s it does not open for this store and field", ErrSecretUnreadable, id)
	}
	return string(plain), key, nil
}

func isSealed(s string) bool { return strings.HasPrefix(s, sealedPrefix) }

// isKeyID is whether s has the shape of a key id: 8 lowercase hex characters.
func isKeyID(s string) bool {
	if len(s) != 8 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// SealSecret seals plain for the store ctx is bound to and the field named —
// a stable label such as "plugins/stripe/secret_key" — or returns it as it is
// when the install has no key. An empty value stays empty: it means unset.
//
// A module that keeps a credential it must read back seals it here before it
// writes it, opens it with OpenSecret when it reads it, and registers the
// column with RegisterSecretColumn so `gocommerce secrets seal` and the doctor
// can reach it.
func (a *App) SealSecret(ctx context.Context, field, plain string) (string, error) {
	// Refused with a key or without: stored readable it would be read back as
	// a sealed value that does not open, and sealed it would open to
	// something that looks sealed again.
	if isSealed(plain) {
		return "", Validationf("a secret cannot start with %q: the engine uses that to mark a value it encrypted", sealedPrefix)
	}
	if plain == "" || a.secrets.current == nil {
		return plain, nil
	}
	store := a.storeOf(ctx)
	if store == "" {
		return "", Internalf(errors.New("no store is bound"), "seal a secret")
	}
	sealed, err := a.secrets.seal(store, field, plain)
	if err != nil {
		return "", Internalf(err, "seal a secret")
	}
	return sealed, nil
}

// OpenSecret is the reverse of SealSecret. A value that was never sealed comes
// back unchanged. One that cannot be opened returns ErrSecretUnreadable, which
// a caller treats as unset — a plugin missing it shows as not configured —
// rather than as a failure of whatever it was doing.
func (a *App) OpenSecret(ctx context.Context, field, stored string) (string, error) {
	plain, _, err := a.secrets.open(a.storeOf(ctx), field, stored)
	return plain, err
}

// openSecretOrUnset is how the engine's own readers open: a value that will
// not open reads as unset and is logged with where it is, never what it is.
//
// Once an hour per store and field, because the readers are hot: the
// storefront lists plugins on every page, and a line per page view would bury
// the log while saying nothing the first line did not. The doctor counts them
// all, every time.
func (a *App) openSecretOrUnset(ctx context.Context, field, stored string) (string, bool) {
	plain, err := a.OpenSecret(ctx, field, stored)
	if err == nil {
		return plain, true
	}
	store := a.storeOf(ctx)
	where := store + "\x00" + field
	if last, ok := a.secretWarnings.Load(where); !ok || time.Since(last.(time.Time)) >= time.Hour {
		a.secretWarnings.Store(where, time.Now())
		a.log.Warn("a stored secret cannot be opened and reads as unset",
			"store", store, "field", field, "reason", err.Error())
	}
	return "", false
}
