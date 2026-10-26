package identity

import (
	"testing"

	"github.com/itswadesh/gocommerce/gctest"
)

// Staff may run any saved report. A sign-in code's hash is four digits away
// from the code, and a password's hash is a guess away from the password, so
// a report reads neither.
func TestAReportReadsNoSignInCodeOrPassword(t *testing.T) {
	app := gctest.NewWithReports(t, New(Config{}))
	gctest.AssertReportsCannotRead(t, app, "identity_login_codes")
	gctest.AssertReportsCannotRead(t, app, "identity_customers", "password_hash")
}
