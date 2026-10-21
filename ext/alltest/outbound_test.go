package alltest

import (
	"context"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

// Every address a module's plugin lets a store type for the server itself to
// call is checked as it is typed (PluginField.Outbound, D109). Every url
// setting is one unless it is listed below as handed out; a setting of
// another kind is one when it is marked, like Shopify's shop domain. A new
// gateway that forgot the mark would still be refused by its client, but
// only at the first sale that needed it, which is the worst moment to find
// out.
func TestEveryAddressTheServerCallsIsCheckedAsItIsSaved(t *testing.T) {
	app := gctest.New(t, everyModule()...)
	ctx := context.Background()

	// Settings the store hands out or builds links from, never connects to.
	handedOut := map[string]bool{
		"payments-revenuecat/base_url":    true, // a purchase link for the shopper
		"payments-adyen/return_url":       true,
		"payments-clover/return_url":      true,
		"payments-creem/success_url":      true,
		"payments-hyperswitch/return_url": true,
		"product-feeds/storefront_url":    true,
		"sitemap/storefront_url":          true,
		"hello-bar/link":                  true,
		"umami/script_url":                true,
		"plausible/script_url":            true,
	}
	// Settings of another kind that hold an address the server connects to.
	mustCheck := []string{"import-shopify/shop"}

	checked := map[string]bool{}
	for _, def := range app.Plugins().Defs() {
		for _, f := range def.Fields {
			name := def.Key + "/" + f.Key
			if (f.Kind != "url" && !f.Outbound) || handedOut[name] {
				continue
			}
			checked[name] = true
			_, err := app.Plugins().Update(ctx, def.Key, gocommerce.PluginPatch{
				Settings: map[string]any{f.Key: "http://127.0.0.1:9"},
			})
			if err == nil || !strings.Contains(err.Error(), "not a public address") {
				t.Errorf("%s took a loopback address (err %v); mark it Outbound, or list it here as handed out", name, err)
			}
		}
	}
	if len(checked) < 20 {
		t.Errorf("only %d addresses were checked; the module list has lost some providers", len(checked))
	}
	for _, name := range mustCheck {
		if !checked[name] {
			t.Errorf("%s holds an address the server connects to and was not checked; mark it Outbound", name)
		}
	}
}
