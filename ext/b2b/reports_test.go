package b2b

import (
	"testing"

	"github.com/itswadesh/gocommerce/ext/identity"
	"github.com/itswadesh/gocommerce/gctest"
)

// Staff may run any saved report, and a partial checkout keeps the token of
// the basket it is placing, which opens that basket.
func TestAReportReadsNoPartialCheckoutsBasketToken(t *testing.T) {
	accounts := identity.New(identity.Config{})
	app := gctest.NewWithReports(t, accounts, New(Config{Accounts: accounts}))
	gctest.AssertReportsCannotRead(t, app, "b2b_partial_checkouts", "cart_token")
}
