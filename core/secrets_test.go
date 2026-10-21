package gocommerce

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

// Secrets the engine must read back are sealed under a key the install keeps
// in its environment (D110). These tests are the seal itself: what it binds a
// value to, what opens it, and what an install without a key does.

// newSecretsKey is 32 random bytes in base64, the shape the environment holds.
// A test never prints one.
func newSecretsKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// sealer is an App with keys and nothing else: sealing touches no database.
func sealer(t *testing.T, current, previous string) *App {
	t.Helper()
	keys, err := newSecretKeys(current, previous)
	if err != nil {
		t.Fatalf("keys: %v", err)
	}
	return &App{secrets: keys, log: quietLogger()}
}

const (
	storeA = "store_01J0000000000000000000000A"
	storeB = "store_01J0000000000000000000000B"
)

func TestASecretIsSealedAndOpened(t *testing.T) {
	app := sealer(t, newSecretsKey(t), "")
	ctx := WithStore(context.Background(), storeA)

	sealed, err := app.SealSecret(ctx, "plugins/fake-mail/api_key", "sk-secret-1234")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if !strings.HasPrefix(sealed, "sealed:v1:"+app.secrets.current.id+":") {
		t.Errorf("sealed = %q, want the v1 envelope naming the key", sealed)
	}
	if strings.Contains(sealed, "sk-secret-1234") {
		t.Error("the sealed value carries the secret")
	}
	if len(app.secrets.current.id) != 8 {
		t.Errorf("key id %q, want 8 hex characters", app.secrets.current.id)
	}
	again, err := app.SealSecret(ctx, "plugins/fake-mail/api_key", "sk-secret-1234")
	if err != nil {
		t.Fatal(err)
	}
	if again == sealed {
		t.Error("two seals of one value are identical; the nonce is not fresh")
	}

	for _, s := range []string{sealed, again} {
		plain, err := app.OpenSecret(ctx, "plugins/fake-mail/api_key", s)
		if err != nil || plain != "sk-secret-1234" {
			t.Errorf("open = %q, %v; want the secret back", plain, err)
		}
	}
}

// The additional data names the store and the field, so a value copied into
// another store's row, or into another field, does not open.
func TestASealedSecretOpensOnlyWhereItWasSealed(t *testing.T) {
	app := sealer(t, newSecretsKey(t), "")
	inA := WithStore(context.Background(), storeA)
	sealed, err := app.SealSecret(inA, "plugins/stripe/secret_key", "sk_live_x")
	if err != nil {
		t.Fatal(err)
	}

	// One character of the ciphertext changed, in the middle, where every bit
	// is data rather than base64 padding.
	mid := len(sealed) - 20
	swap := byte('A')
	if sealed[mid] == 'A' {
		swap = 'B'
	}
	tampered := sealed[:mid] + string(swap) + sealed[mid+1:]

	cases := map[string]struct {
		ctx    context.Context
		field  string
		stored string
	}{
		"another store":  {WithStore(context.Background(), storeB), "plugins/stripe/secret_key", sealed},
		"another field":  {inA, "plugins/razorpay/key_secret", sealed},
		"no store":       {context.Background(), "plugins/stripe/secret_key", sealed},
		"tampered":       {inA, "plugins/stripe/secret_key", tampered},
		"truncated":      {inA, "plugins/stripe/secret_key", sealed[:len("sealed:v1:")+12]},
		"future version": {inA, "plugins/stripe/secret_key", strings.Replace(sealed, ":v1:", ":v9:", 1)},
	}
	for name, c := range cases {
		plain, err := app.OpenSecret(c.ctx, c.field, c.stored)
		if !errors.Is(err, ErrSecretUnreadable) {
			t.Errorf("%s: open = %q, %v; want ErrSecretUnreadable", name, plain, err)
		}
		if plain != "" {
			t.Errorf("%s: an unreadable secret came back as %q", name, plain)
		}
	}
}

// A value stored before the install had a key has no prefix and is read as
// itself, which is what lets a key be switched on without a migration.
func TestAReadableValueOpensAsItself(t *testing.T) {
	app := sealer(t, newSecretsKey(t), "")
	ctx := WithStore(context.Background(), storeA)
	for _, v := range []string{"sk_live_plain", "", "whsec_abc"} {
		if got, err := app.OpenSecret(ctx, "api_keys/secret", v); err != nil || got != v {
			t.Errorf("open %q = %q, %v; want it unchanged", v, got, err)
		}
	}
	// Nothing to seal stays nothing, so an unset field does not become a
	// ciphertext of the empty string.
	if got, err := app.SealSecret(ctx, "api_keys/secret", ""); err != nil || got != "" {
		t.Errorf("seal empty = %q, %v", got, err)
	}
}

func TestWithoutAKeySecretsStayReadable(t *testing.T) {
	keyed := sealer(t, newSecretsKey(t), "")
	ctx := WithStore(context.Background(), storeA)
	sealed, err := keyed.SealSecret(ctx, "api_keys/secret", "gck_x")
	if err != nil {
		t.Fatal(err)
	}

	bare := sealer(t, "", "")
	if got, err := bare.SealSecret(ctx, "api_keys/secret", "gck_x"); err != nil || got != "gck_x" {
		t.Errorf("seal without a key = %q, %v; want the value as it was", got, err)
	}
	if _, err := bare.OpenSecret(ctx, "api_keys/secret", sealed); !errors.Is(err, ErrSecretUnreadable) {
		t.Errorf("a sealed value with no key = %v, want ErrSecretUnreadable", err)
	}
}

// Rotation: the previous key still opens what it sealed, and nothing else
// does once it is gone.
func TestThePreviousKeyStillOpens(t *testing.T) {
	oldKey, newKey := newSecretsKey(t), newSecretsKey(t)
	ctx := WithStore(context.Background(), storeA)
	sealed, err := sealer(t, oldKey, "").SealSecret(ctx, "webhooks/endpoint_secret", "whsec_1")
	if err != nil {
		t.Fatal(err)
	}

	rotating := sealer(t, newKey, oldKey)
	if got, err := rotating.OpenSecret(ctx, "webhooks/endpoint_secret", sealed); err != nil || got != "whsec_1" {
		t.Errorf("open with the previous key = %q, %v", got, err)
	}
	// What it seals now is under the new key.
	fresh, err := rotating.SealSecret(ctx, "webhooks/endpoint_secret", "whsec_2")
	if err != nil || !strings.HasPrefix(fresh, "sealed:v1:"+rotating.secrets.current.id+":") {
		t.Errorf("seal while rotating = %q, %v; want the current key's id", fresh, err)
	}

	if _, err := sealer(t, newKey, "").OpenSecret(ctx, "webhooks/endpoint_secret", sealed); !errors.Is(err, ErrSecretUnreadable) {
		t.Errorf("open after the previous key is gone = %v, want ErrSecretUnreadable", err)
	}
}

// A key is 32 bytes of base64, standard or URL-safe, padded or not. Anything
// else stops the engine with the variable named and the value never echoed.
func TestASecretsKeyMustBe32BytesOfBase64(t *testing.T) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding} {
		if _, err := newSecretKeys(" "+enc.EncodeToString(raw)+"\n", ""); err != nil {
			t.Errorf("a key in %v was refused: %v", enc, err)
		}
	}
	// The four spellings are one key, so a value sealed under one opens under
	// the others.
	a, _ := newSecretKeys(base64.StdEncoding.EncodeToString(raw), "")
	b, _ := newSecretKeys(base64.RawURLEncoding.EncodeToString(raw), "")
	if a.current.id != b.current.id {
		t.Error("one key spelled two ways has two ids")
	}

	short := base64.StdEncoding.EncodeToString(raw[:16])
	for name, c := range map[string]struct{ current, previous, wantVar, value string }{
		"short":           {short, "", "GOCOMMERCE_SECRETS_KEY", short},
		"not base64":      {"this is not a key at all, not even close!!", "", "GOCOMMERCE_SECRETS_KEY", "this is not a key"},
		"bad previous":    {newSecretsKey(t), short, "GOCOMMERCE_SECRETS_KEY_PREVIOUS", short},
		"previous alone":  {"", newSecretsKey(t), "GOCOMMERCE_SECRETS_KEY_PREVIOUS", ""},
		"whitespace only": {"   ", "", "", ""},
	} {
		_, err := newSecretKeys(c.current, c.previous)
		if c.wantVar == "" {
			if err != nil {
				t.Errorf("%s: %v, want no key and no error", name, err)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: accepted", name)
			continue
		}
		if !strings.Contains(err.Error(), c.wantVar) {
			t.Errorf("%s: %q does not name %s", name, err, c.wantVar)
		}
		if c.value != "" && strings.Contains(err.Error(), c.value) {
			t.Errorf("%s: the error echoes the value", name)
		}
	}
}

// New refuses a bad key before it opens the database, so a typo in the
// environment is a clear stop rather than a store that cannot read its keys.
func TestNewRefusesABadSecretsKey(t *testing.T) {
	_, err := New(Config{
		DBURL:       "postgres://nobody@127.0.0.1:1/none?sslmode=disable&connect_timeout=1",
		AdminTokens: []string{"t"},
		Logger:      quietLogger(),
		SecretsKey:  "too-short",
	})
	if err == nil || !strings.Contains(err.Error(), "GOCOMMERCE_SECRETS_KEY") {
		t.Fatalf("New with a bad key = %v, want it refused naming the variable", err)
	}
}
