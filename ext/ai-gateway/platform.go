package aigateway

import (
	"cmp"
	"fmt"
	"log/slog"
	"maps"
	"math/big"
	"slices"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The platform's own AI (D151, amended 2026-10-29). An install may give the
// gateway an Anthropic key of its own. A store that has entered no key makes
// its calls on it, at the models and prices the install names, and its AI
// credit pays for them. A store's own key, models and prices are never mixed
// with the platform's: a store could otherwise price the platform's calls at
// nothing, or name a dearer model than the one it is charged for.

// defaultPlatformPrices are each tier's default model and its published
// prices, in US dollars per million tokens: input, output, cache read,
// five-minute cache write (the gateway's cache_control is the five-minute
// kind). From platform.claude.com/docs/en/about-claude/pricing, read
// 2026-10-29. A price applies only to its own tier running its own model:
// Claude Haiku 5.5's is for prompts up to 100,000 tokens, which RegisterTask
// holds the small tier to (D154) and nothing holds the strong tier to. Any
// other model or tier has no default, so the install names its prices or the
// platform's key is not used for it. The model is named here rather than taken
// from the default constants, so moving a default cannot carry a price with it.
var defaultPlatformPrices = map[Tier]struct{ model, prices string }{
	TierSmall:  {"claude-haiku-5-5", "0.10,0.50,0.01,0.125"},
	TierStrong: {"claude-sonnet-5-5", "2,10,0.10,2.50"},
}

// platform is the install's key, models and prices, read once at Register.
type platform struct {
	key    string
	models map[Tier]string
	// usd is each tier's input, output, cache read and cache write price in US
	// dollars per million tokens. A tier missing here is never called.
	usd map[Tier][4]*big.Rat
	// rates are units of each currency to one US dollar.
	rates map[string]*big.Rat
}

// newPlatform reads Config's platform settings; nil when the install has no
// key. A value that does not read is logged and left out, so the tier or the
// currency it was for is refused rather than charged at a guess, and a typo in
// an AI price does not stop every store's checkout from starting.
func newPlatform(cfg Config, log *slog.Logger) *platform {
	// Trimmed: a key pasted into a host's environment tab with a newline
	// after it would be refused on every call.
	key := strings.TrimSpace(cfg.PlatformAnthropicKey)
	if key == "" {
		return nil
	}
	p := &platform{
		key:    key,
		models: map[Tier]string{},
		usd:    map[Tier][4]*big.Rat{},
		rates:  map[string]*big.Rat{"USD": big.NewRat(1, 1)},
	}
	for _, t := range []struct {
		tier                    Tier
		model, prices, fallback string
	}{
		{TierSmall, cfg.PlatformSmallModel, cfg.PlatformSmallPrices, defaultSmallModel},
		{TierStrong, cfg.PlatformStrongModel, cfg.PlatformStrongPrices, defaultStrongModel},
	} {
		model := cmp.Or(strings.TrimSpace(t.model), t.fallback)
		p.models[t.tier] = model
		text := strings.TrimSpace(t.prices)
		if d := defaultPlatformPrices[t.tier]; text == "" && d.model == model {
			text = d.prices
		}
		if text == "" {
			log.Error("ai gateway: the platform's model has no price, so it is not called on the platform's key", "tier", t.tier, "model", model)
			continue
		}
		usd, err := parseUSDPrices(text)
		if err != nil {
			log.Error("ai gateway: the platform's prices do not read, so the model is not called on the platform's key", "tier", t.tier, "model", model, "error", err)
			continue
		}
		p.usd[t.tier] = usd
	}
	// One rate that does not read drops them all: "INR=88,50", written with a
	// decimal comma, would otherwise charge at 88 with nothing said about it.
	for _, pair := range strings.Split(cfg.PlatformUSDRates, ",") {
		if strings.TrimSpace(pair) == "" {
			continue
		}
		code, value, _ := strings.Cut(pair, "=")
		code = strings.ToUpper(strings.TrimSpace(code))
		rate, ok := new(big.Rat).SetString(strings.TrimSpace(value))
		if len(code) != 3 || code == "USD" || !ok || rate.Sign() <= 0 {
			log.Error("ai gateway: the rates to the US dollar do not read, so only stores in US dollars can use the platform's key",
				"rates", cfg.PlatformUSDRates, "first_bad", strings.TrimSpace(pair))
			p.rates = map[string]*big.Rat{"USD": big.NewRat(1, 1)}
			break
		}
		p.rates[code] = rate
	}
	// Says the key arrived, never what it is: the one way an operator sees
	// that the environment reached the engine.
	log.Info("ai gateway: stores with no key of their own run on the platform's key",
		"small_model", p.models[TierSmall], "strong_model", p.models[TierStrong], "currencies", slices.Sorted(maps.Keys(p.rates)))
	return p
}

// parseUSDPrices reads "input,output,cache read,cache write", each a positive
// number of US dollars per million tokens.
func parseUSDPrices(text string) ([4]*big.Rat, error) {
	var out [4]*big.Rat
	parts := strings.Split(text, ",")
	if len(parts) != len(out) {
		return out, fmt.Errorf("want four prices (input, output, cache read, cache write), got %d", len(parts))
	}
	for i, s := range parts {
		r, ok := new(big.Rat).SetString(strings.TrimSpace(s))
		if !ok || r.Sign() <= 0 {
			return out, fmt.Errorf("%q is not a price above zero", strings.TrimSpace(s))
		}
		out[i] = r
	}
	return out, nil
}

// prices are the platform's prices in currency's minor units per million
// tokens, each rounded up, so a fraction of a minor unit is the store's to
// pay rather than the platform's to lose. ok is false for a currency with no
// rate.
func (p *platform) prices(currency string) (out map[Tier]prices, ok bool) {
	rate, ok := p.rates[currency]
	if !ok {
		return nil, false
	}
	scale := new(big.Rat).Mul(rate, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(gocommerce.CurrencyExponent(currency))), nil)))
	out = map[Tier]prices{}
	for tier, usd := range p.usd {
		var v [4]int64
		complete := true
		for i, r := range usd {
			n, fits := ceilRat(new(big.Rat).Mul(r, scale))
			v[i], complete = n, complete && fits
		}
		if complete {
			out[tier] = prices{input: v[0], output: v[1], cacheRead: v[2], cacheWrite: v[3], complete: true}
		}
	}
	return out, true
}

// ceilRat rounds a positive r up to a whole number, false when it is past what
// a price may be: the bound a store's own prices are held to (number).
func ceilRat(r *big.Rat) (int64, bool) {
	q, rem := new(big.Int).QuoRem(r.Num(), r.Denom(), new(big.Int))
	if rem.Sign() > 0 {
		q.Add(q, big.NewInt(1))
	}
	return q.Int64(), q.IsInt64() && q.Int64() <= maxGrantMinor
}
