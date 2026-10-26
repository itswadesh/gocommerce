package wishlist

import (
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

// Staff may run any saved report, and a guest's wishlist token is the only
// key to that list.
func TestAReportReadsNoWishlistToken(t *testing.T) {
	app := gctest.NewWithReports(t, New(Config{}))
	gctest.AssertReportsCannotRead(t, app, "wishlists", "token")
}
