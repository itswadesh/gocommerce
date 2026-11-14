package askai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	gocommerce "github.com/misiki-in/gocommerce/core"
	aigateway "github.com/misiki-in/gocommerce/ext/ai-gateway"
)

// actionKind is the one thing this version may propose. Anything else a model
// names is refused here, whatever its words say: plugins, payment methods and
// keys, API keys, team, roles, domains, orders, money and messages to customers
// have no action, so there is nothing for an answer to reach.
const actionKind = "store.update_details"

// noProposal is what the model names when the person asked for no change.
const noProposal = "none"

// detailField is a store detail Ask AI may propose, and how it reads and writes
// the profile. The list is the allowlist.
type detailField struct {
	key, label string
	value      func(*gocommerce.StoreProfile) *string
}

var detailFields = []detailField{
	{"name", "Store name", func(p *gocommerce.StoreProfile) *string { return &p.Name }},
	{"legal_name", "Legal name", func(p *gocommerce.StoreProfile) *string { return &p.LegalName }},
	{"email", "Contact email", func(p *gocommerce.StoreProfile) *string { return &p.Email }},
	{"phone", "Contact phone", func(p *gocommerce.StoreProfile) *string { return &p.Phone }},
	{"address_line1", "Address", func(p *gocommerce.StoreProfile) *string { return &p.AddressLine1 }},
	{"address_line2", "Address line 2", func(p *gocommerce.StoreProfile) *string { return &p.AddressLine2 }},
	{"city", "City", func(p *gocommerce.StoreProfile) *string { return &p.City }},
	{"state", "State", func(p *gocommerce.StoreProfile) *string { return &p.State }},
	{"postal_code", "Postal code", func(p *gocommerce.StoreProfile) *string { return &p.PostalCode }},
	{"country", "Country", func(p *gocommerce.StoreProfile) *string { return &p.Country }},
	{"timezone", "Timezone", func(p *gocommerce.StoreProfile) *string { return &p.Timezone }},
	{"currency", "Currency", func(p *gocommerce.StoreProfile) *string { return &p.Currency }},
	{"tax_id", "Tax ID", func(p *gocommerce.StoreProfile) *string { return &p.TaxID }},
	{"support_url", "Support link", func(p *gocommerce.StoreProfile) *string { return &p.SupportURL }},
}

// pricesField is the one that is not text: "true" or "false" on the wire.
const (
	pricesField = "prices_include_tax"
	pricesLabel = "Prices include tax"
)

func fieldByKey(key string) *detailField {
	i := slices.IndexFunc(detailFields, func(f detailField) bool { return f.key == key })
	if i < 0 {
		return nil
	}
	return &detailFields[i]
}

// fieldChange is one field a proposal would set.
type fieldChange struct {
	Field string `json:"field"`
	Value string `json:"value"`
}

type actionArgs struct {
	Changes []fieldChange `json:"changes"`
}

// PreviewChange is one field as a person reviews it.
type PreviewChange struct {
	Field    string `json:"field"`
	Label    string `json:"label"`
	Current  string `json:"current"`
	Proposed string `json:"proposed"`
}

// Preview is what a proposal would do to the store as it is now. Its hash is
// what Apply is checked against, so a value that moved refuses the Apply.
type Preview struct {
	Changes  []PreviewChange `json:"changes"`
	Warnings []string        `json:"warnings"`
}

// merged lays args over the store as saved. Only the proposed fields move: the
// pictures, languages, opening hours and the rest are left absent, which the
// profile reads as "leave it".
func (m *Module) merged(ctx context.Context, raw json.RawMessage) (saved, in *gocommerce.StoreProfile, args actionArgs, err error) {
	// Arguments came from a model: anything the allowlist does not name is
	// refused rather than ignored.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&args); err != nil {
		return nil, nil, args, gocommerce.Validationf("that change could not be read")
	}
	if len(args.Changes) == 0 {
		return nil, nil, args, gocommerce.Validationf("no change was named")
	}
	if saved, err = m.app.Profile().Get(ctx); err != nil {
		return nil, nil, args, err
	}
	copied := *saved
	in = &copied
	in.LogoMediaID, in.LogoDarkMediaID, in.IconMediaID = nil, nil, nil
	in.ShowAddress, in.MapLocation, in.OpeningHours = nil, nil, nil
	in.Language, in.Languages = "", nil
	in.PricesIncludeTax = nil

	seen := map[string]bool{}
	for _, c := range args.Changes {
		if seen[c.Field] {
			return nil, nil, args, gocommerce.Validationf("%q is named twice", c.Field)
		}
		seen[c.Field] = true
		if c.Field == pricesField {
			if c.Value != "true" && c.Value != "false" {
				return nil, nil, args, gocommerce.Validationf("whether prices include tax is true or false")
			}
			v := c.Value == "true"
			in.PricesIncludeTax = &v
			continue
		}
		f := fieldByKey(c.Field)
		if f == nil {
			return nil, nil, args, gocommerce.Validationf("Ask AI cannot change %q", c.Field)
		}
		*f.value(in) = c.Value
	}
	if err := m.app.Profile().Validate(ctx, in); err != nil {
		return nil, nil, args, err
	}
	return saved, in, args, nil
}

func (m *Module) preview(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	saved, in, args, err := m.merged(ctx, raw)
	if err != nil {
		return nil, err
	}
	out := Preview{Changes: []PreviewChange{}, Warnings: []string{}}
	add := func(key, label, current, proposed string) {
		if current != proposed {
			out.Changes = append(out.Changes, PreviewChange{key, label, current, proposed})
		}
	}
	// In the order the person's words gave them, from the validated profile, so
	// "in" reads as the "IN" that would be saved.
	for _, c := range args.Changes {
		if c.Field == pricesField {
			add(pricesField, pricesLabel, boolWord(saved.PricesIncludeTax), boolWord(in.PricesIncludeTax))
			continue
		}
		f := fieldByKey(c.Field)
		add(f.key, f.label, *f.value(saved), *f.value(in))
	}
	if len(out.Changes) == 0 {
		return nil, gocommerce.Conflictf("the store already has those details")
	}
	if slices.ContainsFunc(out.Changes, func(c PreviewChange) bool { return c.Field == "currency" }) {
		out.Warnings = append(out.Warnings, fmt.Sprintf(
			"Prices keep their numbers and are not converted: a price of 500 %s becomes 500 %s.", saved.Currency, in.Currency))
		if m.cfg.Gateway != nil {
			w, err := m.cfg.Gateway.CurrencyChangeWarning(ctx, in.Currency)
			if err != nil {
				return nil, err
			}
			if w != "" {
				out.Warnings = append(out.Warnings, w)
			}
		}
	}
	return json.Marshal(out)
}

func boolWord(v *bool) string {
	if v != nil && *v {
		return "true"
	}
	return "false"
}

// execute saves the proposal as the person pressing Apply, through the same
// service as the Store settings form, which writes the audit row in the save's
// own transaction under the label the gateway put on ctx.
func (m *Module) execute(ctx context.Context, raw json.RawMessage) (json.RawMessage, error) {
	// Here rather than on the route, so the queue's own approve is refused too.
	if m.app.Demo(ctx) {
		return nil, gocommerce.Forbiddenf("this is a demo store, so its details cannot be changed")
	}
	_, in, args, err := m.merged(ctx, raw)
	if err != nil {
		return nil, err
	}
	saved, err := m.app.Profile().Set(ctx, *in, gocommerce.SuperuserFrom(ctx))
	if err != nil {
		return nil, err
	}
	// What the engine now holds, not what was asked for.
	done := Preview{Changes: []PreviewChange{}, Warnings: []string{}}
	for _, c := range args.Changes {
		if c.Field == pricesField {
			done.Changes = append(done.Changes, PreviewChange{Field: pricesField, Label: pricesLabel, Proposed: boolWord(saved.PricesIncludeTax)})
			continue
		}
		f := fieldByKey(c.Field)
		done.Changes = append(done.Changes, PreviewChange{Field: f.key, Label: f.label, Proposed: *f.value(saved)})
	}
	return json.Marshal(done)
}

func (m *Module) action() aigateway.Action {
	return aigateway.Action{
		Kind:    actionKind,
		Right:   gocommerce.RightStoreWrite,
		Label:   "Ask AI",
		Preview: m.preview,
		Execute: m.execute,
		// store.write already opens the Store settings form with these same
		// fields, so pressing Apply gains nobody anything; ai.approve would only
		// stop a store.write role from applying what it asked for.
		OwnRightSuffices: true,
	}
}
