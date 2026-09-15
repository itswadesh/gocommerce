package gocommerce

import "context"

// A provider that is installed but not yet set up.
//
// Every gateway and carrier module used to demand its credentials in Config
// and refuse to register without them, which made "which providers can this
// store use" a question answered by whoever wrote main(). Litekart's admin
// answers it on a screen: every provider the build knows, each with an
// Activate button and a settings form. For that to be honest the engine has
// to know the difference between installed and ready — a gateway whose key
// has not been typed in yet must not be offered at checkout, and a carrier
// without an account must not be offered on the ship dialog (D59).
//
// So a provider whose credentials can come from the Plugins screen says
// whether it is ready, and the engine treats an unready provider as absent
// everywhere a shopper or an operator could pick it, while the settings and
// the two provider screens still list it as installed. A provider that does
// not implement this is what it always was: ready by construction.

// Configurable is a provider or backend whose credentials come from the
// Plugins screen rather than from Config. Configured is asked before every
// use; it reads the plugin's row, so a key typed in a moment ago counts.
type Configurable interface {
	Configured(ctx context.Context) bool
}

// configured is whether a provider can be used right now: true for one that
// is not Configurable, its own answer otherwise.
func configured(ctx context.Context, v any) bool {
	if c, ok := v.(Configurable); ok {
		return c.Configured(ctx)
	}
	return true
}
