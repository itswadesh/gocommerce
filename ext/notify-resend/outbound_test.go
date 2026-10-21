package resend

import (
	"context"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// Resend's host is a constant, but the module still sends through the
// install's guarded client: the one rule for every module is simpler to keep
// than a judgement per host, and only a test points it anywhere else.
func TestTheDefaultClientCallsOnlyPublicAddresses(t *testing.T) {
	s := &stub{}
	server := gctest.StubHTTP(t, s.handler)
	app := gctest.New(t, New(Config{APIKey: "re_test", BaseURL: server.URL}))

	err := app.Notify(context.Background(), gocommerce.Notification{
		Event: gocommerce.EventOrderShipped, Channel: gocommerce.ChannelEmail, To: "asha@example.com",
		Data: map[string]string{"order_number": "GC-7"},
	})
	if err == nil || !strings.Contains(err.Error(), "is not a public address") {
		t.Fatalf("notify through a loopback base URL = %v, want the refusal", err)
	}
	if n := len(s.all()); n != 0 {
		t.Errorf("the private address received %d messages", n)
	}
}
