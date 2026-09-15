package gocommerce

import (
	"context"
	"time"
)

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
	// Configured is whether the provider can be used right now. False for a
	// module installed idle whose key has not been typed into the Plugins
	// screen yet; such a provider is listed here and nowhere a shopper or an
	// operator could pick it.
	Configured bool `json:"configured"`
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
// No names are reserved on it any more: "modules" and "notifier_channels" were
// held for the readout below and are now served.
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
	// Modules is names and nothing else. A client's question is "was this
	// binary built with cms", which a string answers; anything richer here
	// would be a description of the running process rather than of the store's
	// configuration, and this response is readable by every role.
	Modules []string `json:"modules"`
	// NotifierChannels is the one entry on this response that can be bad news.
	// Every other field describes a choice; this one can say that order
	// confirmations are being written to a log file nobody reads, which is a
	// configuration that looks healthy from every other angle.
	NotifierChannels []NotifierChannelInfo `json:"notifier_channels"`
}

// NotifierBackend is one delivery backend registered on a channel.
type NotifierBackend struct {
	// Name is what the backend calls itself when it implements Named, the
	// module that registered it otherwise, and "log" for the built-in logger.
	// A notifier has no code the way a payment provider does — nothing
	// addresses one by name — so there is nothing more canonical to fall back
	// to.
	Name string `json:"name"`
	// Module is "core" for the built-in logger, otherwise the module that
	// registered it.
	Module string `json:"module"`
	// Delivers is false for the built-in logger, which writes one line to the
	// process log and reports success. Without this field a channel with a
	// backend and a channel that sends are indistinguishable.
	Delivers bool `json:"delivers"`
}

// NotifierChannelInfo is one notification channel and what stands behind it.
type NotifierChannelInfo struct {
	Channel string `json:"channel"`
	// Delivers is true when at least one backend on this channel is not the
	// built-in logger. False is the trap: sending succeeds, the outbox records
	// a delivered event, and nobody receives anything.
	Delivers bool              `json:"delivers"`
	Backends []NotifierBackend `json:"backends"`
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
		Modules:              a.Modules(),
		NotifierChannels:     a.NotifierChannels(),
	}
}

// Modules names the modules this binary was composed with, in the order they
// were passed to New.
//
// Registration order rather than alphabetical: it is the order migrations run
// in and the order a collision is resolved in, so it is the only ordering that
// means something, and it is stable across reads of the same binary.
func (a *App) Modules() []string {
	out := make([]string, 0, len(a.modules))
	for _, m := range a.modules {
		out = append(out, m.Name())
	}
	return out
}

// NotifierChannels reports each notification channel and the backends behind
// it.
//
// The channel set is the engine's closed one rather than the keys of the map,
// and in a fixed order: a channel with no backend at all still has to appear,
// because "sms is not configured" is an answer and a missing row is not.
func (a *App) NotifierChannels() []NotifierChannelInfo {
	channels := []string{ChannelEmail, ChannelSMS}
	out := make([]NotifierChannelInfo, 0, len(channels))
	for _, c := range channels {
		out = append(out, a.notifier.describe(c))
	}
	return out
}

// paymentInfo describes the installed payment methods.
//
// It reads a.payments.providers and a.paymentOwners without a lock, which is
// safe for the same reason checkProviders does it: both maps are written only
// while New runs, before the server accepts a request. That is an invariant a
// hot-reloadable provider would break, so it is written down rather than left
// to be inferred from the absence of a mutex.
func (a *App) paymentInfo() []ProviderInfo {
	// installed rather than Methods: the settings list every gateway this
	// build carries, ready or not, which is what a screen that sets them up
	// needs; Methods is the shopper's list and leaves the idle ones out.
	codes := a.payments.installed()
	out := make([]ProviderInfo, 0, len(codes))
	for _, code := range codes {
		// No fallback for a missing owner: the provider and its owner are
		// written on adjacent lines in RegisterPayment, so a blank module is a
		// wiring bug and inventing "core" for it would hide one.
		info := ProviderInfo{Code: code, Name: code, Module: a.paymentOwners[code], Configured: configured(context.Background(), a.payments.providers[code])}
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
		info := ProviderInfo{Code: code, Name: code, Module: a.fulfillmentOwners[code], Configured: configured(context.Background(), a.fulfillment.providers[code])}
		if named, ok := a.fulfillment.providers[code].(Named); ok && named.DisplayName() != "" {
			info.Name = named.DisplayName()
		}
		out = append(out, info)
	}
	return out
}
