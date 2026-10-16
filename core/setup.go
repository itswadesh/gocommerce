package gocommerce

import (
	"context"
	"net/http"
	"strings"
)

// The setup guide: what a new store has to do before it can sell, in the
// order it makes sense to do it, read from what the store actually has.
//
// Each step's state is a question asked of the store's own data, never a box
// somebody ticked, so the guide cannot drift from the truth: delete the only
// product and "Add a product" is open again. A required step locks every step
// after it until it is done — a new owner sees one thing to do next, not a
// panel of twenty screens. Recommended steps come last and never block.

// SetupStep is one step of the guide.
type SetupStep struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	// Why is one sentence on what the step is for, in the owner's terms.
	Why string `json:"why"`
	// Href is the panel screen where the step is done.
	Href     string `json:"href"`
	Required bool   `json:"required"`
	Done     bool   `json:"done"`
	// Locked is true while an earlier required step is not done.
	Locked bool `json:"locked"`
}

// Setup is the whole guide for the store a request is bound to.
type Setup struct {
	Steps []SetupStep `json:"steps"`
	// Next is the first step not yet done, or "" when every step is.
	Next string `json:"next"`
	// RequiredDone is true once the store can take an order end to end.
	RequiredDone bool `json:"required_done"`
	// Complete is true when every step, recommended ones too, is done.
	Complete bool `json:"complete"`
}

func (a *App) mountSetupRoutes() {
	a.HandleAdminFunc("GET /api/admin/setup", a.handleSetup)
}

func (a *App) handleSetup(w http.ResponseWriter, r *http.Request) {
	s, err := a.Setup(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, s)
}

// Setup works out the guide for ctx's store.
func (a *App) Setup(ctx context.Context) (*Setup, error) {
	profile, err := a.profile.Get(ctx)
	if err != nil {
		return nil, err
	}
	exists := func(q string, args ...any) (bool, error) {
		var ok bool
		if err := a.db.QueryRowContext(ctx, `SELECT EXISTS (`+q+`)`, args...).Scan(&ok); err != nil {
			return false, Internalf(err, "read the store's setup")
		}
		return ok, nil
	}
	hasProduct, err := exists(`SELECT 1 FROM products WHERE status = 'active'`)
	if err != nil {
		return nil, err
	}
	hasShipping, err := exists(`SELECT 1 FROM shipping_rates r JOIN shipping_zones z ON z.id = r.zone_id WHERE r.active AND z.active`)
	if err != nil {
		return nil, err
	}
	hasDomain, err := exists(`SELECT 1 FROM store_domains WHERE store_id = $1 AND verified_at IS NOT NULL`, a.storeOf(ctx))
	if err != nil {
		return nil, err
	}
	filled := func(s string) bool { return strings.TrimSpace(s) != "" }

	steps := []SetupStep{
		{
			Key: "details", Title: "Name your store and say how to reach it",
			Why:  "Your shop's name, email and country go on every order email and invoice.",
			Href: "/dash/settings", Required: true,
			Done: filled(profile.Name) && filled(profile.Email) && filled(profile.Country),
		},
		{
			Key: "products", Title: "Add your first product",
			Why:  "Something to sell: a title, a price and how many you have.",
			Href: "/dash/products", Required: true,
			Done: hasProduct,
		},
		{
			Key: "payments", Title: "Choose how customers pay",
			Why:  "Cash on delivery works from the start; add a card or UPI provider to take payment online.",
			Href: "/dash/settings/payments", Required: true,
			Done: len(a.payments.MethodsFor(ctx)) > 0,
		},
		{
			Key: "shipping", Title: "Set where you deliver and what it costs",
			Why:  "At least one delivery zone with a rate, so checkout can price a delivery.",
			Href: "/dash/shipping-settings", Required: true,
			Done: hasShipping,
		},
		{
			Key: "email", Title: "Send order emails",
			Why:  "Connect an email provider so customers get their confirmation and updates.",
			Href: "/dash/notifications/email",
			Done: a.notifier.delivers(ctx, ChannelEmail),
		},
		{
			Key: "domain", Title: "Use your own domain",
			Why:  "Shoppers reach the store at an address you own, such as shop.example.com.",
			Href: "/dash/settings/domains",
			Done: hasDomain,
		},
	}

	out := &Setup{Steps: steps, RequiredDone: true, Complete: true}
	blocked := false
	for i := range out.Steps {
		st := &out.Steps[i]
		// Finished is finished: a step already done is shown done, never
		// locked behind one that is not.
		st.Locked = blocked && !st.Done
		if !st.Done {
			out.Complete = false
			if out.Next == "" {
				out.Next = st.Key
			}
			if st.Required {
				out.RequiredDone = false
				blocked = true
			}
		}
	}
	return out, nil
}
