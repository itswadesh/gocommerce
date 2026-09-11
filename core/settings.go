package gocommerce

import "time"

// ProviderInfo is one installed provider, as an operator should see it: the
// code the API speaks, the label a human reads, and the module that put it
// there. The module answers "who installed this", which is the question a
// store with four gateways actually has.
type ProviderInfo struct {
	Code string `json:"code"`
	// Name falls back to Code when the provider implements no Named.
	Name string `json:"name"`
	// Module is "core" for a built-in, otherwise the module that registered it.
	Module string `json:"module"`
}

// StoreSettings is what this store is configured as.
//
// It is a hand-written whitelist over Config and has to stay one. Config
// carries DBURL and AdminTokens — marshalling it would publish both — and then
// AdminAuth, which is a func and makes json.Marshal fail outright. Tagging
// Config for JSON instead would make the wire shape a property of the
// configuration struct, so the next field added to Config would be served
// without anyone deciding it should be. A field reaches this response because
// somebody put it here.
//
// Everything on it is a start-up decision, which is why there is no write
// counterpart: changing the settlement currency mid-flight would change what
// every order in flight means (D14 snapshots currency per order), so it is a
// restart with a different Config rather than a form.
//
// Reserved names, to be added additively by the modules/providers readout and
// deliberately not served yet: "modules" and "notifier_channels".
type StoreSettings struct {
	Version          string   `json:"version"`
	Currency         string   `json:"currency"`
	DefaultLanguage  string   `json:"default_language"`
	Languages        []string `json:"languages"`
	PricesIncludeTax bool     `json:"prices_include_tax"`
	// FlatShipping crosses as Money and not as a bare integer, for the reason
	// every other amount does: the number is meaningless without the code that
	// says how many decimals it has.
	FlatShipping Money  `json:"flat_shipping"`
	OrderPrefix  string `json:"order_prefix"`
	// The TTLs carry their unit in the name. A field called cart_ttl holding
	// 2592000 is ambiguous, and a time.Duration marshals as nanoseconds, which
	// no reader guesses right.
	CartTTLSeconds       int64          `json:"cart_ttl_seconds"`
	OrderTTLSeconds      int64          `json:"order_ttl_seconds"`
	MediaUploadsEnabled  bool           `json:"media_uploads_enabled"`
	PaymentMethods       []ProviderInfo `json:"payment_methods"`
	FulfillmentProviders []ProviderInfo `json:"fulfillment_providers"`
}

// Settings is the composed view of the store a module or a screen reads when
// it wants the currency and what else is installed, rather than re-deriving it
// from Config.
//
// A value snapshot like Config(), with no ctx and no error: it touches no
// database, so it cannot fail.
func (a *App) Settings() StoreSettings {
	// Copied rather than shared: a caller that sorted or appended to the
	// returned slice would be editing the running config through the snapshot.
	// make with a concrete length also keeps the JSON [] rather than null in
	// the empty case, which applyDefaults makes impossible but a client should
	// not have to trust.
	langs := make([]string, len(a.cfg.Languages))
	copy(langs, a.cfg.Languages)

	return StoreSettings{
		Version:          Version,
		Currency:         a.cfg.Currency,
		DefaultLanguage:  a.cfg.DefaultLanguage,
		Languages:        langs,
		PricesIncludeTax: a.cfg.PricesIncludeTax,
		FlatShipping:     money(a.cfg.FlatShippingMinor, a.cfg.Currency),
		OrderPrefix:      a.cfg.OrderPrefix,
		// applyDefaults guarantees both are positive, so the division is safe
		// and the answer is never a misleading zero.
		CartTTLSeconds:       int64(a.cfg.CartTTL / time.Second),
		OrderTTLSeconds:      int64(a.cfg.OrderTTL / time.Second),
		MediaUploadsEnabled:  a.mediaUploadsEnabled(),
		PaymentMethods:       a.paymentInfo(),
		FulfillmentProviders: a.fulfillmentInfo(),
	}
}

// paymentInfo describes the installed payment methods.
//
// It reads a.payments.providers and a.paymentOwners without a lock, which is
// safe for the same reason checkProviders does it: both maps are written only
// while New runs, before the server accepts a request. That is an invariant a
// hot-reloadable provider would break, so it is written down rather than left
// to be inferred from the absence of a mutex.
func (a *App) paymentInfo() []ProviderInfo {
	codes := a.payments.Methods()
	out := make([]ProviderInfo, 0, len(codes))
	for _, code := range codes {
		// No fallback for a missing owner: the provider and its owner are
		// written on adjacent lines in RegisterPayment, so a blank module is a
		// wiring bug and inventing "core" for it would hide one.
		info := ProviderInfo{Code: code, Name: code, Module: a.paymentOwners[code]}
		if named, ok := a.payments.providers[code].(Named); ok && named.DisplayName() != "" {
			info.Name = named.DisplayName()
		}
		out = append(out, info)
	}
	return out
}

// fulfillmentInfo is the twin of paymentInfo over the shipping backends.
//
// Two explicit walks rather than one generic helper over an interface: this
// engine has no reflection and no container, and the second reader of either
// method wants to see which map it reads.
func (a *App) fulfillmentInfo() []ProviderInfo {
	codes := a.fulfillment.Providers()
	out := make([]ProviderInfo, 0, len(codes))
	for _, code := range codes {
		info := ProviderInfo{Code: code, Name: code, Module: a.fulfillmentOwners[code]}
		if named, ok := a.fulfillment.providers[code].(Named); ok && named.DisplayName() != "" {
			info.Name = named.DisplayName()
		}
		out = append(out, info)
	}
	return out
}
