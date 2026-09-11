package gocommerce

import "net/http"

// The store settings route. Read-only, authenticated, and gated by no right.
//
// Read-only because everything it serves is a decision the binary was started
// with. A store that could rewrite its settlement currency from a browser
// would be rewriting what every order in flight means, which is the same
// argument Config.PricesIncludeTax makes about tax: these are restarts with a
// different Config, not a form.
//
// No right because every screen formats money before it can draw anything. An
// operator who could not read this would read prices in the wrong currency and
// the wrong number of decimals — a correctness failure dressed as a permission
// one. It is the reasoning the /me routes are exempt under: the response names
// nobody, carries no credential, and says only what the store already is.
//
// That exemption only stays safe if the payload stays what it is, so the rule
// rather than the hope: nothing may be added here that is not "what this store
// is configured as" — not a key, not a secret, not a count of anything. Every
// role can read it, so a field added here is a field nobody re-decided the gate
// for. Diagnostics belong behind store.operate on their own route.
//
// "notifier_channels" is the closest thing here to a diagnostic and belongs
// anyway: what it reports is which delivery backends the binary was composed
// with, which is the same class of fact as payment_methods. That it doubles as
// a warning — a channel carrying only the built-in logger sends nothing while
// reporting success — is a property of the configuration, not a measurement of
// the running store.
func (a *App) mountSettingsRoutes() {
	a.HandleAdminFunc("GET /api/admin/settings", a.handleSettings)
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	Respond(w, http.StatusOK, a.Settings())
}
