package gocommerce

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
)

// A public demo: anybody signs in, personal data is masked, and the routes
// that decide who can sign in are frozen.
//
// A demo store is one made to be looked at, so everything its panel draws is
// published — and with Config.DemoAccount set it is one anyone can sign into,
// any email and any password, see Superusers.Authenticate. The masking happens
// here — on the way out of an admin route — rather than in the panel, because
// the panel is not the only client of these routes: an operator who can read a
// screen can read the JSON behind it, and a mask a curl steps around is
// decoration rather than protection.
//
// Masking a response is only half of it. The other half is that no route may
// hand out something that reads around the mask — a credential, or a query the
// engine cannot see the shape of — which is what demoFrozenAny is for, and what
// keeps ?q= off the email column on a demo.
//
// Only admin responses are masked. A shopper reading their own order at
// /api/orders/{number} still sees their own address, which is why the masking
// sits at the handler and not in the row builders: Order and Superuser are each
// built once and served twice, and GET /api/admin/me has to name the account
// you signed in as or you cannot tell whose session you are in.
//
// Names and street addresses are deliberately untouched. They are personal too,
// but a demo with no names in it shows nothing about how the panel reads, and
// the two fields a store gets spammed through are these.

// demoFrozen lists the admin route families a demo refuses to write to: the
// ones that decide who can sign in and what they may do. A visitor who is the
// owner for the afternoon could otherwise change the owner's password, hand
// themselves an API key that outlives the session, or strip a role of the
// rights the next visitor needs — and a demo that can be locked from the
// outside is a demo that will be.
//
// Reads stay open: the Team and Roles screens are part of what a demo shows.
// The match is by path prefix so a route added to one of these families later
// is frozen without anybody remembering this list.
var demoFrozen = []string{
	"/api/admin/me",
	"/api/admin/superusers",
	"/api/admin/invitations",
	"/api/admin/password-reset",
	"/api/admin/api-keys",
	"/api/admin/roles",
}

// demoFrozenAny freezes a route for *every* method, a GET included, because
// what it answers with is not a screen: it is a credential, or a read with no
// fixed shape for masking to act on. Freezing only writes is not enough for
// these, and each one was a way around the mask before it was listed here.
//
//   - An API key's secret is stored as presented and handed back whole, so
//     reading one is taking it. Minting was already frozen, which is the half
//     that looked like the whole.
//   - An order's access token is the guest's credential, and the guest's own
//     view of an order is deliberately unmasked — so revealing the token is
//     handing over a door into the very fields this file exists to hide.
//   - A custom report runs an operator's own SELECT. It has no columns the
//     engine knows the meaning of, and it can read the superuser and API key
//     tables, so there is nothing for maskByFieldName to be right about.
//   - Creating a vendor's login writes a row to superusers with a password the
//     caller chose, which is the frozen families' whole point reached by
//     another door.
//
// One '*' stands for exactly one path segment, so the id in the middle needs no
// pattern matching and no regexp.
var demoFrozenAny = []string{
	"/api/admin/api-keys/*/secret",
	"/api/admin/orders/*/access-token",
	"/api/admin/reports/custom/run",
	"/api/admin/reports/custom/*/run",
	"/api/admin/vendors/*/users",
}

// demoGuardMW refuses the writes demoFrozen names and every method on the
// routes demoFrozenAny names, on a demo only. It wraps the mux directly, inside
// the JSON fallback, so the refusal is an ordinary API error and the log line
// looks like any other 403.
func (a *App) demoGuardMW(next http.Handler) http.Handler {
	if !a.cfg.Demo {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if reason := demoRefusal(r.Method, r.URL.Path); reason != "" {
			RespondError(w, r, &APIError{
				Status:  http.StatusForbidden,
				Code:    "demo_frozen",
				Message: reason,
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// demoRefusal names why a demo refuses this request, or "" to let it through.
// Split out from the middleware so the rule is testable without a server.
func demoRefusal(method, path string) string {
	for _, p := range demoFrozenAny {
		if demoPathMatch(p, path) {
			return "this is a public demo: API key secrets, order access tokens, " +
				"ad-hoc reports and vendor logins are not available"
		}
	}
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return ""
	}
	for _, p := range demoFrozen {
		if path == p || strings.HasPrefix(path, p+"/") {
			return "this is a public demo: operators, invitations, password resets, " +
				"API keys and roles cannot be changed"
		}
	}
	return ""
}

// demoPathMatch matches a path against a pattern in which '*' is exactly one
// segment. Segment-wise rather than a prefix test, so "*" cannot swallow a
// deeper path and freeze something nobody listed.
func demoPathMatch(pattern, path string) bool {
	p, q := strings.Split(pattern, "/"), strings.Split(path, "/")
	if len(p) != len(q) {
		return false
	}
	for i := range p {
		if p[i] != "*" && p[i] != q[i] {
			return false
		}
	}
	return true
}

// maskRune stands in for every character a mask removes.
//
// It is U+2022 rather than '*' because no address, anywhere, contains one: that
// is what lets [App.dropMasked] recognise a store's own mask coming back on a
// form submission without first reading the row it would overwrite, and what
// lets [maskedEmailPattern] tell a mask from a search term.
const maskRune = '•'

// maskBullets is fixed-width on purpose. A mask that grew with the local part
// would publish the length of every address it hid, which is most of the way to
// publishing the address.
const maskBullets = "•••"

// MaskEmail returns an address with everything identifying removed —
// "jane.doe@gmail.com" becomes "j•••@g•••.com".
//
// On a store that is not a demo it returns s unchanged, so call sites need no
// condition of their own. Modules showing people call it for the same reason
// core does: one rule, in one place, whatever screen it reaches.
func (a *App) MaskEmail(s string) string {
	if !a.cfg.Demo {
		return s
	}
	return maskEmail(s)
}

// MaskPhone returns a number with its middle removed — "+31 20 555 1242"
// becomes "+31 •• ••• ••42". The country code and the last two digits survive
// because a demo is a thing people look at: a column of identical bullets says
// nothing about what the screen is for, and neither of those two facts reaches
// anybody.
//
// On a store that is not a demo it returns s unchanged.
func (a *App) MaskPhone(s string) string {
	if !a.cfg.Demo {
		return s
	}
	return maskPhone(s)
}

// Demo reports whether this store masks personal data — what a module asks when
// it has something to hide that is neither an email nor a phone number.
//
// Deliberately not on GET /api/admin/settings. Nothing reads it there yet, and
// that route's rule is that a field added to it is a field nobody re-decided the
// gate for; a panel that wants to say "this is a demo" can be given it then.
func (a *App) Demo() bool { return a.cfg.Demo }

func maskEmail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	at := strings.LastIndexByte(s, '@')
	// Anything that is not shaped like an address is masked whole rather than
	// passed through: the one thing this function must never do is decide a
	// value is harmless because it could not parse it.
	if at <= 0 || at == len(s)-1 {
		return maskBullets
	}
	local, domain := s[:at], s[at+1:]
	return firstRune(local) + maskBullets + "@" + maskDomain(domain)
}

// maskDomain keeps the last label — the ".com" — and hides the rest. The suffix
// is worth keeping because it is the part that is not personal and the part
// that makes the column still read as a list of addresses.
func maskDomain(domain string) string {
	dot := strings.LastIndexByte(domain, '.')
	if dot <= 0 || dot == len(domain)-1 {
		return firstRune(domain) + maskBullets
	}
	return firstRune(domain[:dot]) + maskBullets + domain[dot:]
}

func maskPhone(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	digits := 0
	for _, r := range s {
		if isDigit(r) {
			digits++
		}
	}
	// Two digits is not a number anybody can be reached on, so there is nothing
	// to trade away by hiding all of it. Without this, the rule below would hand
	// such a value back untouched.
	if digits <= 2 {
		return maskAllDigits(s)
	}
	// keep is the length of the leading run of digits, and only when the number
	// is written in international form: "+31" is a country, not a person, and
	// without the plus the leading digits are the start of somebody's number.
	keep := 0
	if strings.HasPrefix(s, "+") {
		for _, r := range s[1:] {
			if !isDigit(r) {
				break
			}
			keep++
		}
		// A number written as one unbroken group is all country code by this
		// reading, which would mask nothing at all.
		if keep >= digits-2 {
			keep = 0
		}
	}

	var b strings.Builder
	seen := 0
	for _, r := range s {
		if !isDigit(r) {
			// Letters go too. Masking only digits meant a "phone" column holding
			// words — an address, a note, somebody's email — came out verbatim,
			// and what a store actually has in that column is not this
			// function's decision to make. Separators stay, so a real number
			// still reads as one.
			if unicode.IsLetter(r) {
				b.WriteRune(maskRune)
			} else {
				b.WriteRune(r) // spaces, dashes and brackets keep the shape readable
			}
			continue
		}
		if seen < keep || seen >= digits-2 {
			b.WriteRune(r)
		} else {
			b.WriteRune(maskRune)
		}
		seen++
	}
	return b.String()
}

// maskedEmailPattern turns a mask back into a SQL LIKE pattern that matches the
// addresses it could have come from — "j•••@g•••.com" into "j%@g%.com".
//
// It exists because the panel has no customer id to hold: a customer is the
// orders that share an email (see customers.go), so the address itself is the
// identity, and the Customers screen hands it straight back on
// GET /api/admin/orders?email= to open somebody's drawer. Masking the column
// without this would leave every one of those links pointing at nobody.
//
// Two customers whose addresses mask alike therefore open one another's orders
// on a demo store. That is the cost of having no identifier other than the
// thing being hidden, and it is charged only where the data is already fake.
//
// The empty string means "this was not a mask", which is what a store with
// masking switched off always answers.
func maskedEmailPattern(s string) string {
	if !strings.ContainsRune(s, maskRune) {
		return ""
	}
	var b strings.Builder
	wild := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		if r != maskRune {
			wild = false
		}
		switch r {
		case maskRune:
			// One % for a whole run of bullets: "%%%" matches the same rows but
			// reads like a mistake.
			if !wild {
				b.WriteByte('%')
				wild = true
			}
		case '%', '_', '\\':
			b.WriteByte('\\') // a literal, not a wildcard the caller did not ask for
			b.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// dropMasked removes a patch field whose value is a mask this store handed out.
//
// It is the other half of masking a field somebody can edit. The panel fills
// its form from the same JSON it displays, so an operator who opens a customer
// on a demo store and presses Save submits "j•••@g•••.com" in good faith; left
// alone, the mask would be written over the address it was hiding and the real
// one would be gone. Treating it as "no change" is both safe and what the
// operator meant, since they did not touch the field.
//
// No lookup of the stored value is needed, and none is wanted: maskRune cannot
// occur in a real address or phone number, so the test is the value itself.
func (a *App) dropMasked(p **string) {
	if a.cfg.Demo && *p != nil && strings.ContainsRune(**p, maskRune) {
		*p = nil
	}
}

// refuseMaskedInput is the answer where the value being written IS the thing
// masked, so there is no field to leave alone and no stored value to restore.
// A customer group's membership is the address itself: dropping it silently
// would report success for a removal that did nothing, and letting it through
// would file a row of bullets as a member.
func (a *App) refuseMaskedInput(s, what string) error {
	if !a.maskedInput(s) {
		return nil
	}
	return Validationf(
		"this is a public demo, so %s is masked and cannot be used here — type it in full", what)
}

// maskedInput reports whether a submitted value is a mask rather than something
// the operator typed — dropMasked for the fields that are not pointers.
func (a *App) maskedInput(s string) bool {
	return a.cfg.Demo && strings.ContainsRune(s, maskRune)
}

// The types that carry somebody's address or number, each masked in one place
// for the reason [Order.Redact] is one method: a rule spelled out again at every
// handler is a rule one handler will spell differently. Every one of them is a
// no-op off a demo store, so call sites carry no condition and a screen added
// later cannot forget the flag — only the call.

// MaskOrder hides the contact details on an order. Exported because a module
// serving its own view of an order — an invoice, a picking list — owes the same
// rule as the screens core serves.
func (a *App) MaskOrder(o *Order) {
	if !a.cfg.Demo || o == nil {
		return
	}
	o.Email = maskEmail(o.Email)
	o.Phone = maskPhone(o.Phone)
	o.Address.Phone = maskPhone(o.Address.Phone)
}

func (a *App) maskOrders(os []*Order) {
	for _, o := range os {
		a.MaskOrder(o)
	}
}

func (a *App) maskCustomers(cs []*Customer) {
	if !a.cfg.Demo {
		return
	}
	for _, c := range cs {
		c.Email = maskEmail(c.Email)
		c.Phone = maskPhone(c.Phone)
		c.Address.Phone = maskPhone(c.Address.Phone)
	}
}

// maskCartSummary covers the admin cart screens. [Cart] itself is never masked:
// the routes that serve it are the shopper's own, where the address on the
// basket is the one they typed a moment ago.
func (a *App) maskCartSummary(c *CartSummary) {
	if !a.cfg.Demo || c == nil {
		return
	}
	c.Email = maskEmail(c.Email)
}

func (a *App) maskCartSummaries(cs []*CartSummary) {
	for _, c := range cs {
		a.maskCartSummary(c)
	}
}

// maskNotifications hides who a message went to. The log says an order
// confirmation was delivered and to which channel, which is the operational
// fact; the address it reached is the customer's.
func (a *App) maskNotifications(ns []*NotificationRecord) {
	for _, n := range ns {
		a.maskNotification(n)
	}
}

func (a *App) maskNotification(n *NotificationRecord) {
	if !a.cfg.Demo || n == nil {
		return
	}
	if n.Channel == ChannelSMS {
		n.To = maskPhone(n.To)
	} else {
		n.To = maskEmail(n.To)
	}
	// Data is the template's variables, and templates are written by whoever
	// runs the store — so the safe reading is that any key named like a contact
	// detail holds one.
	for k, v := range n.Data {
		if masked, ok := maskByFieldName(k, v); ok {
			n.Data[k] = masked
		}
	}
}

// maskSuperuserRows covers the Team screen. Everybody's address is masked
// except the caller's own, so an operator can still find themselves in the list
// they are reading.
func (a *App) maskSuperuserRows(ctx context.Context, rows []*SuperuserRow) {
	if !a.cfg.Demo {
		return
	}
	for _, row := range rows {
		a.maskSuperuserExceptSelf(ctx, row.Superuser)
	}
}

// maskSuperuserExceptSelf masks an operator's record unless it is the caller's
// own. Nobody may be left unable to tell which account they are signed in as —
// it is the same reasoning that keeps GET /api/admin/me unmasked, applied to
// the routes that can answer with either your record or somebody else's.
func (a *App) maskSuperuserExceptSelf(ctx context.Context, s *Superuser) {
	if !a.cfg.Demo || s == nil {
		return
	}
	if caller := SuperuserFrom(ctx); caller != nil && caller.ID == s.ID {
		return
	}
	s.Email = maskEmail(s.Email)
}

func (a *App) maskInvitations(is []*Invitation) {
	if !a.cfg.Demo {
		return
	}
	for _, i := range is {
		i.Email = maskEmail(i.Email)
	}
}

func (a *App) maskVendor(v *Vendor) {
	if !a.cfg.Demo || v == nil {
		return
	}
	v.Email = maskEmail(v.Email)
	v.Phone = maskPhone(v.Phone)
}

func (a *App) maskVendors(vs []*Vendor) {
	for _, v := range vs {
		a.maskVendor(v)
	}
}

func (a *App) maskRedemptions(rs []DiscountRedemption) {
	if !a.cfg.Demo {
		return
	}
	for i := range rs {
		rs[i].Email = maskEmail(rs[i].Email)
	}
}

// maskEmails masks a bare list of addresses — a customer group's membership,
// which is stored as the addresses themselves.
func (a *App) maskEmails(es []string) []string {
	if !a.cfg.Demo {
		return es
	}
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = maskEmail(e)
	}
	return out
}

func (a *App) maskAuditEntries(es []*AuditEntry) {
	if !a.cfg.Demo {
		return
	}
	for _, e := range es {
		e.ActorEmail = maskEmail(e.ActorEmail)
		maskAuditChanges(&e.Changes)
	}
}

func (a *App) maskAuditActors(as []AuditActor) {
	if !a.cfg.Demo {
		return
	}
	for i := range as {
		as[i].Email = maskEmail(as[i].Email)
	}
}

// maskMovements covers the stock ledger, which names the operator who moved
// each unit. It is the same operator address the Team screen masks, reached
// through the weakest read right in the store.
func (a *App) maskMovements(ms []StockMovement) {
	if !a.cfg.Demo {
		return
	}
	for i := range ms {
		ms[i].ActorEmail = maskEmail(ms[i].ActorEmail)
	}
}

// maskTimeline covers the order drawer's history. Before and After are the
// audit values, and the one act they most often record on an order is a
// corrected contact detail — so masking the actor alone left the timeline
// publishing both the old address and the new one, on the same screen whose
// order body masks them.
func (a *App) maskTimeline(es []OrderTimelineEntry) {
	if !a.cfg.Demo {
		return
	}
	for i := range es {
		es[i].ActorEmail = maskEmail(es[i].ActorEmail)
		maskAny(es[i].Before)
		maskAny(es[i].After)
	}
}

// maskAuditChanges covers the trail's before-and-after. Correcting a customer
// means correcting an order (customers.go), so "changed email from X to Y" is a
// recorded act on a demo store like any other — and it would otherwise publish
// both addresses on the one screen built to show exactly that.
// It walks the whole object with maskAny rather than the top level only: an
// address is recorded as a nested object, so "changed the address" holds the
// phone one level down, and a top-level-only pass published it.
func maskAuditChanges(c *AuditChanges) {
	maskAny(c.Before)
	maskAny(c.After)
}

// maskOutboxEvents covers the Events screen, which serves an event's payload
// verbatim — and an order.placed payload is the shopper's address and number
// (events.go). It is the one admin surface where masking cannot read a field,
// because the payload's shape belongs to whoever published it.
func (a *App) maskOutboxEvents(es []*OutboxEvent) {
	for _, e := range es {
		a.maskOutboxEvent(e)
	}
}

func (a *App) maskOutboxEvent(e *OutboxEvent) {
	if !a.cfg.Demo || e == nil || len(e.Payload) == 0 {
		return
	}
	e.Payload = maskJSON(e.Payload)
}

// maskJSON rewrites every string held under a contact-shaped key, at any depth.
//
// A payload that will not parse is replaced rather than passed along: this runs
// only on a demo store, where publishing something nobody could read is the one
// outcome worth refusing outright.
func maskJSON(raw json.RawMessage) json.RawMessage {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return json.RawMessage(`{"masked":"this payload could not be read, so it was withheld"}`)
	}
	out, err := json.Marshal(maskAny(v))
	if err != nil {
		return json.RawMessage(`{"masked":"this payload could not be rewritten, so it was withheld"}`)
	}
	return out
}

func maskAny(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, inner := range t {
			if s, ok := inner.(string); ok {
				if masked, hit := maskByFieldName(k, s); hit {
					t[k] = masked
					continue
				}
			}
			t[k] = maskAny(inner)
		}
		return t
	case []any:
		for i, inner := range t {
			t[i] = maskAny(inner)
		}
		return t
	}
	return v
}

// maskByFieldName masks a value held under a key rather than in a typed field.
// It reports whether the key was one it recognised, so a caller can leave
// everything else exactly as it found it.
func maskByFieldName(key, value string) (string, bool) {
	if value == "" {
		return value, false
	}
	switch key {
	case "email", "actor_email", "customer_email", "to", "recipient", "reply_to":
		return maskEmail(value), true
	case "phone", "customer_phone", "mobile", "address_phone":
		return maskPhone(value), true
	}
	return value, false
}

// maskAllDigits hides every digit and every letter, keeping only separators.
// It is the answer for a value too short to be a number — and, because a value
// with no digits at all lands here, for a "phone" column holding words.
func maskAllDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if isDigit(r) || unicode.IsLetter(r) {
			b.WriteRune(maskRune)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func firstRune(s string) string {
	for _, r := range s {
		return string(r)
	}
	return ""
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }
