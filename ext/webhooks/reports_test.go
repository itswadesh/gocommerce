package webhooks

import (
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

// Staff may run any saved report. An endpoint's signing secret lets whoever
// holds it sign a delivery the receiver will trust, and a hook's address often
// carries its own key.
func TestAReportReadsNoEndpointSecretOrAddress(t *testing.T) {
	app := gctest.NewWithReports(t, New(Config{}))
	gctest.AssertReportsCannotRead(t, app, "webhook_endpoints", "secret", "url")
}
