package revenuecat

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

func secretsKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// The signing secret is optional, so "unset" means "do not check". A secret
// that is set but sealed under a key the install no longer has must not read
// as that: the webhook is refused until it is entered again, rather than
// accepted with the check quietly off.
func TestAnUnreadableSigningSecretRefusesWebhooks(t *testing.T) {
	ctx := context.Background()
	on := true
	first := gctest.NewWithConfig(t, gocommerce.Config{SecretsKey: secretsKey(t)}, New(Config{}))
	if _, err := first.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Enabled: &on, Settings: map[string]any{
		"link_token": testLinkToken, "webhook_authorization": testAuthorization, "signing_secret": testSigningSecret,
	}}); err != nil {
		t.Fatal(err)
	}

	// Under a new key the authorization is typed in again; the signing secret
	// is not, so it still sits sealed under the old one.
	app := gctest.NewWithConfig(t, gocommerce.Config{DBURL: first.Config().DBURL, SecretsKey: secretsKey(t)}, New(Config{}))
	if _, err := app.Plugins().Update(ctx, PluginKey, gocommerce.PluginPatch{Settings: map[string]any{
		"webhook_authorization": testAuthorization,
	}}); err != nil {
		t.Fatal(err)
	}

	result := gctest.PlaceOrder(t, app, "revenuecat")
	body := purchase("evt_unopened", "NON_RENEWING_PURCHASE", result.Order.Number,
		float64(result.Order.Total.AmountMinor)/100, result.Order.Total.Currency, "txn_unopened")
	if rec := postWebhook(t, app, body, testAuthorization, false); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("an unsigned webhook while the signing secret cannot be read = %d, want 503", rec.Code)
	}
	order, err := app.Order().Get(ctx, result.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	if order.PaymentStatus == gocommerce.PaymentPaid {
		t.Error("an unsigned webhook marked the order paid while the signing secret could not be read")
	}
}
