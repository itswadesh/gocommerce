package cartrecovery

import (
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

// Staff may run any saved report, and a reminder's link token opens the
// shopper's basket, so a report reads the reminder but not the token.
func TestAReportReadsNoRemindersLinkToken(t *testing.T) {
	app := gctest.NewWithReports(t, New(Config{}))
	gctest.AssertReportsCannotRead(t, app, "cart_recovery_abandonments", "link_token")
}
