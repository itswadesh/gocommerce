package newsletter

import (
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

// Staff may run any saved report, and a subscriber's token is what their
// unsubscribe link carries.
func TestAReportReadsNoUnsubscribeToken(t *testing.T) {
	app := gctest.NewWithReports(t, New(Config{}))
	gctest.AssertReportsCannotRead(t, app, "newsletter_subscriptions", "token")
}
