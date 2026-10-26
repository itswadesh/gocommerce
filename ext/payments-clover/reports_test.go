package clover

import (
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

// Staff may run any saved report, and a session id opens that order's hosted
// checkout page.
func TestAReportReadsNoCheckoutSession(t *testing.T) {
	app := gctest.NewWithReports(t, New(Config{}))
	gctest.AssertReportsCannotRead(t, app, "payments_clover_sessions", "session_id")
}
