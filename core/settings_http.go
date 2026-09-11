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
// Two names are reserved on this response for the modules/providers readout and
// must not be taken by anything else: "modules" and "notifier_channels".
func (a *App) mountSettingsRoutes() {
	a.HandleAdminFunc("GET /api/admin/settings", a.handleSettings)
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request) {
	Respond(w, http.StatusOK, a.Settings())
}
