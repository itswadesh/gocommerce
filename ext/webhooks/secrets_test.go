package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// A signing secret is one the store must read back — it signs with it — so
// it is sealed under the install's key like any other (D110).

func secretsKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

func storedSecret(t *testing.T, app *gocommerce.App, id int64) string {
	t.Helper()
	var s string
	if err := app.DB().QueryRowContext(context.Background(),
		`SELECT secret FROM webhook_endpoints WHERE id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func verifies(t *testing.T, secret, header, body string) bool {
	t.Helper()
	ts, v1 := parseSignature(t, header)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(ts + "." + body))
	return hmac.Equal([]byte(v1), []byte(hex.EncodeToString(mac.Sum(nil))))
}

func TestASealedSigningSecretStillSigns(t *testing.T) {
	cap := &captured{}
	srv := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.WriteHeader(http.StatusOK)
	})
	mod := New(Config{})
	app := gctest.NewWithConfig(t, gocommerce.Config{SecretsKey: secretsKey(t), AllowPrivateURLs: true}, mod)

	e := create(t, app, map[string]any{"url": srv.URL, "events": []string{"order.*"}})
	if !strings.HasPrefix(e.Secret, "whsec_") {
		t.Fatalf("create returned %q, want the secret itself", e.Secret)
	}
	if s := storedSecret(t, app, e.ID); !strings.HasPrefix(s, "sealed:v1:") || strings.Contains(s, e.Secret) {
		t.Errorf("stored secret = %q, want it sealed", s)
	}
	// The hint is the secret's tail, not the ciphertext's.
	rec := gctest.AdminRequest(t, app, "GET", "/api/admin/x/webhooks/endpoints/"+itoa(e.ID), nil)
	var got Endpoint
	gctest.DecodeData(t, rec, &got)
	if got.SecretHint != e.Secret[len(e.Secret)-4:] || got.Secret != "" {
		t.Errorf("read back hint %q secret %q, want the secret's last four and no secret", got.SecretHint, got.Secret)
	}

	gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)
	cap.mu.Lock()
	if len(cap.sigs) == 0 || !verifies(t, e.Secret, cap.sigs[0], cap.bodies[0]) {
		t.Error("a delivery signed with a sealed secret does not verify against the secret")
	}
	seen := len(cap.sigs)
	cap.mu.Unlock()

	// Rotating seals the new one too, and the next delivery signs with it.
	rec = gctest.AdminRequest(t, app, "POST", "/api/admin/x/webhooks/endpoints/"+itoa(e.ID)+"/rotate-secret", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("rotate: %d %s", rec.Code, rec.Body.String())
	}
	var rotated Endpoint
	gctest.DecodeData(t, rec, &rotated)
	if s := storedSecret(t, app, e.ID); !strings.HasPrefix(s, "sealed:v1:") || rotated.Secret == e.Secret {
		t.Errorf("after rotating the secret is stored as %q", s)
	}
	again := gctest.CreateProduct(t, app, "ROTATED-1", 1000, 5)
	gctest.Buy(t, app, gocommerce.CodeCOD, again.DefaultVariant().ID, 1)
	gctest.DrainOutbox(t, app)
	deliverOnce(t, mod)
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.sigs) <= seen || !verifies(t, rotated.Secret, cap.sigs[len(cap.sigs)-1], cap.bodies[len(cap.bodies)-1]) {
		t.Error("after rotating, a delivery does not verify against the new secret")
	}
}

func TestWithoutAKeyTheSigningSecretIsStoredAsBefore(t *testing.T) {
	app := newApp(t)
	e := create(t, app, map[string]any{"url": "https://merchant.example.com/hooks", "events": []string{"order.*"}})
	if s := storedSecret(t, app, e.ID); s != e.Secret {
		t.Errorf("stored without a key = %q, want the secret as issued", s)
	}
}

// A secret no key here opens cannot sign, and an unsigned post would be one
// the merchant has to trust blindly. So the delivery fails, says why, and
// waits for the operator to rotate the secret.
func TestASigningSecretUnderAnUnknownKeyIsNotSentUnsigned(t *testing.T) {
	cap := &captured{}
	srv := gctest.StubHTTP(t, func(w http.ResponseWriter, r *http.Request) {
		cap.record(r)
		w.WriteHeader(http.StatusOK)
	})
	first := gctest.NewWithConfig(t, gocommerce.Config{SecretsKey: secretsKey(t), AllowPrivateURLs: true}, New(Config{}))
	e := create(t, first, map[string]any{"url": srv.URL, "events": []string{"order.*"}})
	gctest.PlaceOrder(t, first, gocommerce.CodeCOD)
	gctest.DrainOutbox(t, first)

	mod := New(Config{})
	lost := gctest.NewWithConfig(t, gocommerce.Config{DBURL: first.Config().DBURL, SecretsKey: secretsKey(t), AllowPrivateURLs: true}, mod)
	deliverOnce(t, mod)
	if cap.calls() != 0 {
		t.Fatalf("the endpoint was called %d time(s) without a secret to sign with", cap.calls())
	}
	ds := deliveries(t, lost)
	if len(ds) == 0 {
		t.Fatal("no deliveries")
	}
	if ds[0].State != StatePending || !strings.Contains(ds[0].LastError, "rotate") {
		t.Errorf("delivery = %+v, want it pending with an error that says to rotate the secret", ds[0])
	}

	rec := gctest.AdminRequest(t, lost, "GET", "/api/admin/x/webhooks/endpoints/"+itoa(e.ID), nil)
	var got Endpoint
	gctest.DecodeData(t, rec, &got)
	if got.SecretHint != "" {
		t.Errorf("an unreadable secret shows the hint %q", got.SecretHint)
	}
}

// `gocommerce secrets seal` reaches the module's own table because the module
// named the column.
func TestSecretsSealReachesSigningSecrets(t *testing.T) {
	bare := newApp(t)
	e := create(t, bare, map[string]any{"url": "https://merchant.example.com/hooks", "events": []string{"order.*"}})

	keyed := gctest.NewWithConfig(t, gocommerce.Config{DBURL: bare.Config().DBURL, SecretsKey: secretsKey(t), AllowPrivateURLs: true}, New(Config{}))
	rep, err := keyed.SealSecrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range rep.Counts {
		if c.Label == "webhook signing secrets" {
			found = true
			if c.Readable != 1 {
				t.Errorf("webhook signing secrets = %+v, want the one readable secret sealed", c)
			}
		}
	}
	if !found {
		t.Fatalf("the seal did not reach the webhook secrets: %+v", rep.Counts)
	}
	if s := storedSecret(t, keyed, e.ID); !strings.HasPrefix(s, "sealed:v1:") {
		t.Errorf("after the seal the secret is stored as %q", s)
	}
	plain, err := keyed.OpenSecret(context.Background(), secretField, storedSecret(t, keyed, e.ID))
	if err != nil || plain != e.Secret {
		t.Error("the sealed secret does not open to the one the merchant holds")
	}
}

// Starting the engine with a key seals the signing secrets already stored,
// because the module named its column: a hosted install never has to run
// `gocommerce secrets seal`.
func TestStartingWithAKeySealsSigningSecrets(t *testing.T) {
	bare := newApp(t)
	e := create(t, bare, map[string]any{"url": "https://merchant.example.com/hooks", "events": []string{"order.*"}})

	keyed := gctest.NewWithConfig(t, gocommerce.Config{DBURL: bare.Config().DBURL, SecretsKey: secretsKey(t)}, New(Config{}))
	if err := keyed.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The pass runs in the background so serving is not held up.
	deadline := time.Now().Add(20 * time.Second)
	for !strings.HasPrefix(storedSecret(t, keyed, e.ID), "sealed:v1:") {
		if time.Now().After(deadline) {
			t.Fatalf("after starting with a key the signing secret is still stored readable")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if plain, err := keyed.OpenSecret(context.Background(), secretField, storedSecret(t, keyed, e.ID)); err != nil || plain != e.Secret {
		t.Error("the sealed secret does not open to the one the merchant holds")
	}
}
