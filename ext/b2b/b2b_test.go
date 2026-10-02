package b2b

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/ext/identity"
	"github.com/itswadesh/gocommerce/gctest"
)

// mailModule installs a recording email notifier ahead of the modules under
// test, so an invitation's token can be read out of the "email".
type mailModule struct{ rec *gctest.RecordingNotifier }

func (mailModule) Name() string                       { return "testmail" }
func (mailModule) Migrations() []gocommerce.Migration { return nil }
func (m mailModule) Register(app *gocommerce.App) error {
	app.RegisterNotifier(gocommerce.ChannelEmail, m.rec)
	return nil
}

// fixture is a store with one company that has an account, an approval
// limit and its own customer group, plus a product its buyers get cheaper.
type fixture struct {
	app      *gocommerce.App
	accounts *identity.Module
	b2b      *Module
	mail     *gctest.RecordingNotifier
	variant  int64 // catalogue 10000, the company's group pays 8000
	company  *Company
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	mail := &gctest.RecordingNotifier{}
	accounts := identity.New(identity.Config{})
	mod := New(Config{Accounts: accounts})
	app := gctest.New(t, mailModule{rec: mail}, accounts, mod)
	ctx := context.Background()

	product := gctest.CreateProduct(t, app, "B2B-WIDGET", 10000, 100)
	variant := product.Variants[0].ID
	group, err := app.Pricing().CreateGroup(ctx, gocommerce.CustomerGroupInput{Code: "acme-trade", Name: "Acme trade"})
	if err != nil {
		t.Fatalf("group: %v", err)
	}
	list, err := app.Pricing().CreateList(ctx, gocommerce.PriceListInput{Name: "Acme", GroupID: &group.ID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if err := app.Pricing().SetPrice(ctx, list.ID,
		gocommerce.PriceRow{VariantID: variant, MinQuantity: 1, AmountMinor: 8000}); err != nil {
		t.Fatalf("price: %v", err)
	}

	rec := gctest.AdminRequest(t, app, http.MethodPost, "/api/admin/x/b2b/companies", map[string]any{
		"name": "Acme Distribution", "group_id": group.ID,
		"credit_limit_minor": 50000, "net_days": 30, "approval_threshold_minor": 30000,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create company = %d: %s", rec.Code, rec.Body)
	}
	var company Company
	gctest.DecodeData(t, rec, &company)
	return &fixture{app: app, accounts: accounts, b2b: mod, mail: mail, variant: variant, company: &company}
}

// account signs somebody up and returns their session token. confirmed says
// whether identity has confirmed their address.
func (f *fixture) account(t *testing.T, email string, confirmed bool) (*identity.Customer, string) {
	t.Helper()
	ctx := context.Background()
	acct, sess, err := f.accounts.Signup(ctx, email, "correct horse battery", "Buyer "+email, "")
	if err != nil {
		t.Fatalf("signup %s: %v", email, err)
	}
	if confirmed {
		if acct, err = f.accounts.MarkEmailVerified(ctx, acct.ID, email); err != nil {
			t.Fatalf("confirm %s: %v", email, err)
		}
	}
	return acct, sess.Token
}

// member makes a confirmed account a member through the operator route.
func (f *fixture) member(t *testing.T, email, role string) (*identity.Customer, string) {
	t.Helper()
	acct, tok := f.account(t, email, true)
	rec := gctest.AdminRequest(t, f.app, http.MethodPost, f.companyPath()+"/members",
		map[string]any{"email": email, "role": role})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add %s = %d: %s", email, rec.Code, rec.Body)
	}
	return acct, tok
}

func (f *fixture) companyPath() string {
	return "/api/admin/x/b2b/companies/" + strconv.FormatInt(f.company.ID, 10)
}

func (f *fixture) cart(t *testing.T, qty int) string {
	t.Helper()
	cart, err := f.app.Cart().Create(context.Background(), "")
	if err != nil {
		t.Fatalf("cart: %v", err)
	}
	if _, err := f.app.Cart().AddLine(context.Background(), cart.Token, f.variant, qty); err != nil {
		t.Fatalf("add line: %v", err)
	}
	return cart.Token
}

var address = gocommerce.Address{Line1: "1 Depot Road", City: "Testville", PostalCode: "12345", Country: "US"}

func (f *fixture) checkout(t *testing.T, token, cart string, extra map[string]any) *httptestResult {
	t.Helper()
	body := map[string]any{"cart_id": cart, "address": address}
	for k, v := range extra {
		body[k] = v
	}
	rec := gctest.SessionRequest(t, f.app, token, http.MethodPost, "/x/b2b/checkout", body)
	return &httptestResult{code: rec.Code, body: rec.Body.String(), rec: rec}
}

type httptestResult struct {
	code int
	body string
	rec  *httptest.ResponseRecorder
}

// decodeBody reads the data envelope of a body that is not an error.
func decodeBody(t *testing.T, body string, v any) {
	t.Helper()
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	if err := json.Unmarshal(env.Data, v); err != nil {
		t.Fatalf("decode data %s: %v", env.Data, err)
	}
}

func errCode(body string) string {
	i := strings.Index(body, `"code":"`)
	if i < 0 {
		return ""
	}
	rest := body[i+8:]
	return rest[:strings.IndexByte(rest, '"')]
}

// An address nobody has proven cannot be added directly: it is invited, and
// accepting the invitation — from the account at that address, and no other —
// is what proves it and joins the company.
func TestAnInvitationProvesTheAddressAndJoins(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	acct, tok := f.account(t, "newbuyer@acme.test", false)
	_, otherTok := f.account(t, "someone@else.test", true)

	rec := gctest.AdminRequest(t, f.app, http.MethodPost, f.companyPath()+"/members",
		map[string]any{"email": "newbuyer@acme.test", "role": RoleBuyer})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("add unconfirmed = %d, want 202 (invited, not added): %s", rec.Code, rec.Body)
	}
	if n := f.mail.Count(EventInvitation); n != 1 {
		t.Fatalf("invitations sent = %d, want 1", n)
	}
	token := f.mail.All()[len(f.mail.All())-1].Data["invite_token"]

	if r := gctest.SessionRequest(t, f.app, otherTok, http.MethodPost, "/x/b2b/invitations/accept",
		map[string]any{"token": token}); r.Code != http.StatusForbidden {
		t.Errorf("another account accepting = %d, want 403: a forwarded link is not the mailbox", r.Code)
	}
	if r := gctest.SessionRequest(t, f.app, tok, http.MethodPost, "/x/b2b/invitations/accept",
		map[string]any{"token": token}); r.Code != http.StatusOK {
		t.Fatalf("accept = %d: %s", r.Code, r.Body)
	}
	if r := gctest.SessionRequest(t, f.app, tok, http.MethodPost, "/x/b2b/invitations/accept",
		map[string]any{"token": token}); r.Code != http.StatusBadRequest {
		t.Errorf("accepting twice = %d, want 400", r.Code)
	}

	confirmed, err := f.accounts.CustomerByID(ctx, acct.ID)
	if err != nil || !confirmed.EmailVerified {
		t.Errorf("accepting the invitation did not confirm the address with identity (%v)", err)
	}
	members, _, err := f.app.Pricing().Members(ctx, *f.company.GroupID, 10, 0)
	if err != nil || len(members) != 1 || members[0] != "newbuyer@acme.test" {
		t.Errorf("group members = %v (%v), want the new buyer's address", members, err)
	}
}

// A buyer checks out on the company's terms: the company's price, on
// account, confirmed, payment pending until the invoice is settled.
func TestABuyerChecksOutOnAccountAtTheCompanysPrice(t *testing.T) {
	f := newFixture(t)
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)

	r := f.checkout(t, tok, f.cart(t, 2), map[string]any{"po_number": "PO-100"})
	if r.code != http.StatusCreated {
		t.Fatalf("checkout = %d: %s", r.code, r.body)
	}
	if !strings.Contains(r.body, `"payment_provider":"on_account"`) ||
		!strings.Contains(r.body, `"status":"confirmed"`) ||
		!strings.Contains(r.body, `"amount_minor":8000`) {
		t.Errorf("order = %s; want on_account, confirmed, at the company's 8000", r.body)
	}

	orders := gctest.SessionRequest(t, f.app, tok, http.MethodGet, "/x/b2b/orders", nil)
	if !strings.Contains(orders.Body.String(), `"po_number":"PO-100"`) ||
		!strings.Contains(orders.Body.String(), `"due_at"`) {
		t.Errorf("company orders = %s; want the PO and a due date", orders.Body)
	}
	me := gctest.SessionRequest(t, f.app, tok, http.MethodGet, "/x/b2b/me", nil)
	if !strings.Contains(me.Body.String(), `"outstanding":{"amount_minor":16000`) {
		t.Errorf("credit = %s; want 16000 outstanding", me.Body)
	}
}

// The limit is refused before the order exists, and settling an invoice
// gives the room back.
func TestACreditLimitIsEnforcedBeforeTheOrderExists(t *testing.T) {
	f := newFixture(t)
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)

	first := f.checkout(t, tok, f.cart(t, 3), nil) // 24000 of 50000
	if first.code != http.StatusCreated {
		t.Fatalf("first = %d: %s", first.code, first.body)
	}
	cart := f.cart(t, 4) // 32000 more: over
	second := f.checkout(t, tok, cart, nil)
	if second.code != http.StatusForbidden || errCode(second.body) != "credit_limit_exceeded" {
		t.Fatalf("second = %d %s, want 403 credit_limit_exceeded", second.code, second.body)
	}
	_, total, err := f.app.Order().List(context.Background(), gocommerce.OrderQuery{Limit: 10})
	if err != nil || total != 1 {
		t.Errorf("orders = %d (%v), want 1: a refused order must not exist", total, err)
	}

	// Paying the first invoice frees its 24000.
	var order struct {
		Order gocommerce.Order `json:"order"`
	}
	gctest.DecodeData(t, first.rec, &order)
	if rec := gctest.AdminRequest(t, f.app, http.MethodPost,
		"/api/admin/orders/"+strconv.FormatInt(order.Order.ID, 10)+"/mark-paid", map[string]any{}); rec.Code != http.StatusOK {
		t.Fatalf("mark paid = %d: %s", rec.Code, rec.Body)
	}
	if again := f.checkout(t, tok, cart, nil); again.code != http.StatusCreated {
		t.Errorf("after settling = %d %s, want 201", again.code, again.body)
	}
}

// Nobody pays on account except through a company: the method is absent
// from the public list, refused at the public checkout and at a phone order.
func TestOnAccountIsInvisibleOutsideACompany(t *testing.T) {
	f := newFixture(t)

	methods := gctest.Request(t, f.app, http.MethodGet, "/api/checkout", nil)
	if strings.Contains(methods.Body.String(), CodeOnAccount) {
		t.Errorf("GET /api/checkout lists on_account: %s", methods.Body)
	}
	pub := gctest.Request(t, f.app, http.MethodPost, "/api/checkout/"+CodeOnAccount, map[string]any{
		"cart_id": f.cart(t, 1), "email": "x@y.test", "name": "X", "address": address,
	})
	if pub.Code != http.StatusNotFound {
		t.Errorf("public on_account checkout = %d, want 404", pub.Code)
	}
	phone := gctest.AdminRequest(t, f.app, http.MethodPost, "/api/admin/orders", map[string]any{
		"payment_method": CodeOnAccount, "email": "x@y.test", "name": "X", "address": address,
		"lines": []map[string]any{{"variant_id": f.variant, "quantity": 1}},
	})
	if phone.Code != http.StatusNotFound {
		t.Errorf("phone order on account = %d, want 404", phone.Code)
	}
}

// A basket carrying a company's prices is placed through the company, so its
// rules apply; and the metadata key this module trusts cannot be forged.
func TestACompanyBasketCannotBePlacedAroundTheCompany(t *testing.T) {
	f := newFixture(t)
	acct, _ := f.member(t, "junior@acme.test", RoleBuyer)

	cart := f.cart(t, 1)
	if _, err := f.accounts.ClaimCart(context.Background(), acct.ID, cart); err != nil {
		t.Fatalf("claim: %v", err)
	}
	rec := gctest.Request(t, f.app, http.MethodPost, "/api/checkout/cod", map[string]any{
		"cart_id": cart, "email": acct.Email, "name": "Junior", "address": address,
	})
	if rec.Code != http.StatusForbidden || errCode(rec.Body.String()) != "company_checkout_required" {
		t.Errorf("public checkout of a company basket = %d %s, want 403 company_checkout_required", rec.Code, rec.Body)
	}

	forged := gctest.Request(t, f.app, http.MethodPost, "/api/checkout/cod", map[string]any{
		"cart_id": f.cart(t, 1), "email": "x@y.test", "name": "X", "address": address,
		"metadata": map[string]any{"b2b": map[string]any{"company_id": f.company.ID}},
	})
	if forged.Code != http.StatusBadRequest {
		t.Errorf("forged metadata.b2b = %d, want 400", forged.Code)
	}
}

// Over the threshold a buyer's order waits; an approver places it at the
// prices they were shown, and a buyer cannot approve their own.
func TestAnOrderOverTheThresholdWaitsForAnApprover(t *testing.T) {
	f := newFixture(t)
	_, bossTok := f.member(t, "boss@acme.test", RoleAdmin)
	_, juniorTok := f.member(t, "junior@acme.test", RoleBuyer)

	r := f.checkout(t, juniorTok, f.cart(t, 5), map[string]any{"po_number": "PO-7"}) // 40000 > 30000
	if r.code != http.StatusAccepted || !strings.Contains(r.body, `"status":"pending"`) {
		t.Fatalf("checkout over the threshold = %d %s, want 202 and a pending approval", r.code, r.body)
	}
	if _, total, _ := f.app.Order().List(context.Background(), gocommerce.OrderQuery{Limit: 10}); total != 0 {
		t.Errorf("orders = %d while waiting for approval, want 0", total)
	}
	if n := f.mail.Count(EventApprovalRequest); n != 1 {
		t.Errorf("approvers told = %d, want 1 (the admin, not the requester)", n)
	}

	var list []Approval
	gctest.DecodeData(t, gctest.SessionRequest(t, f.app, bossTok, http.MethodGet, "/x/b2b/approvals?status=pending", nil), &list)
	if len(list) != 1 {
		t.Fatalf("pending approvals = %d, want 1", len(list))
	}
	path := "/x/b2b/approvals/" + strconv.FormatInt(list[0].ID, 10)
	if rec := gctest.SessionRequest(t, f.app, juniorTok, http.MethodPost, path+"/approve", nil); rec.Code != http.StatusForbidden {
		t.Errorf("buyer approving = %d, want 403", rec.Code)
	}
	rec := gctest.SessionRequest(t, f.app, bossTok, http.MethodPost, path+"/approve", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("approve = %d: %s", rec.Code, rec.Body)
	}
	var placed struct {
		Order    gocommerce.Order `json:"order"`
		Approval Approval         `json:"approval"`
	}
	gctest.DecodeData(t, rec, &placed)
	if placed.Order.Number == "" || placed.Approval.OrderNumber != placed.Order.Number {
		t.Errorf("approve answered order %q and approval naming %q; want one order, named on both",
			placed.Order.Number, placed.Approval.OrderNumber)
	}
	if !strings.Contains(rec.Body.String(), `"status":"approved"`) ||
		!strings.Contains(rec.Body.String(), `"amount_minor":8000`) {
		t.Errorf("approved = %s; want approved and the order at 8000", rec.Body)
	}
	if n := f.mail.Count(EventApprovalDecision); n != 1 {
		t.Errorf("decisions sent = %d, want 1", n)
	}
	if again := gctest.SessionRequest(t, f.app, bossTok, http.MethodPost, path+"/approve", nil); again.Code != http.StatusConflict {
		t.Errorf("approving twice = %d, want 409: an approval places one order", again.Code)
	}
}

func TestARequestCanBeRejectedOrWithdrawn(t *testing.T) {
	f := newFixture(t)
	_, bossTok := f.member(t, "boss@acme.test", RoleAdmin)
	_, juniorTok := f.member(t, "junior@acme.test", RoleBuyer)

	var first, second struct {
		Approval Approval `json:"approval"`
	}
	r1 := f.checkout(t, juniorTok, f.cart(t, 5), nil)
	r2 := f.checkout(t, juniorTok, f.cart(t, 6), nil)
	decodeBody(t, r1.body, &first)
	decodeBody(t, r2.body, &second)

	rej := gctest.SessionRequest(t, f.app, bossTok, http.MethodPost,
		"/x/b2b/approvals/"+strconv.FormatInt(first.Approval.ID, 10)+"/reject", map[string]any{"reason": "Wrong depot"})
	if rej.Code != http.StatusOK || !strings.Contains(rej.Body.String(), `"reason":"Wrong depot"`) {
		t.Errorf("reject = %d %s", rej.Code, rej.Body)
	}
	if c := gctest.SessionRequest(t, f.app, bossTok, http.MethodPost,
		"/x/b2b/approvals/"+strconv.FormatInt(second.Approval.ID, 10)+"/cancel", nil); c.Code != http.StatusForbidden {
		t.Errorf("an approver withdrawing someone else's request = %d, want 403", c.Code)
	}
	if c := gctest.SessionRequest(t, f.app, juniorTok, http.MethodPost,
		"/x/b2b/approvals/"+strconv.FormatInt(second.Approval.ID, 10)+"/cancel", nil); c.Code != http.StatusOK {
		t.Errorf("withdrawing my own = %d: %s", c.Code, c.Body)
	}
}

// A quote is priced by the store and placed at exactly those prices — over
// the catalogue and over the company's own price list.
func TestAQuoteIsPlacedAtItsPrices(t *testing.T) {
	f := newFixture(t)
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)

	rec := gctest.SessionRequest(t, f.app, tok, http.MethodPost, "/x/b2b/quotes", map[string]any{
		"note":  "Pallet pricing please",
		"lines": []map[string]any{{"variant_id": f.variant, "quantity": 4, "unit_price_minor": 1}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("request quote = %d: %s", rec.Code, rec.Body)
	}
	var q Quote
	gctest.DecodeData(t, rec, &q)
	if q.Lines[0].UnitPrice != nil {
		t.Fatalf("a buyer's request set its own price: %+v", q.Lines[0])
	}
	path := "/api/admin/x/b2b/quotes/" + strconv.FormatInt(q.ID, 10)

	if send := gctest.AdminRequest(t, f.app, http.MethodPost, path+"/send", nil); send.Code != http.StatusBadRequest {
		t.Errorf("sending an unpriced quote = %d, want 400", send.Code)
	}
	staff := gctest.OperatorToken(t, f.app, "staff@store.test", gocommerce.RoleStaff)
	if p := gctest.SessionRequest(t, f.app, staff, http.MethodPut, path, map[string]any{}); p.Code != http.StatusForbidden {
		t.Errorf("staff pricing a quote = %d, want 403", p.Code)
	}
	priced := gctest.AdminRequest(t, f.app, http.MethodPut, path, map[string]any{
		"reply": "Agreed for this order",
		"lines": []map[string]any{{"variant_id": f.variant, "quantity": 4, "unit_price_minor": 7100}},
	})
	if priced.Code != http.StatusOK {
		t.Fatalf("price = %d: %s", priced.Code, priced.Body)
	}
	if send := gctest.AdminRequest(t, f.app, http.MethodPost, path+"/send", nil); send.Code != http.StatusOK {
		t.Fatalf("send = %d: %s", send.Code, send.Body)
	}
	if n := f.mail.Count(EventQuoteReady); n != 1 {
		t.Errorf("quote-ready mails = %d, want 1", n)
	}

	accept := gctest.SessionRequest(t, f.app, tok, http.MethodPost,
		"/x/b2b/quotes/"+strconv.FormatInt(q.ID, 10)+"/accept", map[string]any{"address": address, "po_number": "PO-Q"})
	if accept.Code != http.StatusCreated {
		t.Fatalf("accept = %d: %s", accept.Code, accept.Body)
	}
	if !strings.Contains(accept.Body.String(), `"amount_minor":7100`) {
		t.Errorf("order = %s; want the quoted 7100 over the list's 8000", accept.Body)
	}
	if again := gctest.SessionRequest(t, f.app, tok, http.MethodPost,
		"/x/b2b/quotes/"+strconv.FormatInt(q.ID, 10)+"/accept", map[string]any{"address": address}); again.Code != http.StatusConflict {
		t.Errorf("accepting twice = %d, want 409", again.Code)
	}
}

func TestAPurchaseOrderNumberCanBeRequired(t *testing.T) {
	f := newFixture(t)
	_, tok := f.member(t, "boss@acme.test", RoleAdmin)
	if rec := gctest.AdminRequest(t, f.app, http.MethodPatch, f.companyPath(),
		map[string]any{"require_po": true}); rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body)
	}
	cart := f.cart(t, 1)
	if r := f.checkout(t, tok, cart, nil); r.code != http.StatusBadRequest {
		t.Errorf("no PO = %d %s, want 400", r.code, r.body)
	}
	if r := f.checkout(t, tok, cart, map[string]any{"po_number": "PO-1"}); r.code != http.StatusCreated {
		t.Errorf("with a PO = %d %s, want 201", r.code, r.body)
	}
}

// Leaving a company, or the account going away, takes the address out of the
// company's group: whoever holds that mailbox next must not buy on its terms.
func TestLeavingTakesTheCompanysPricesAway(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, bossTok := f.member(t, "boss@acme.test", RoleAdmin)
	junior, _ := f.member(t, "junior@acme.test", RoleBuyer)
	gone, _ := f.member(t, "gone@acme.test", RoleBuyer)

	if rec := gctest.SessionRequest(t, f.app, bossTok, http.MethodDelete,
		"/x/b2b/members/"+strconv.FormatInt(junior.ID, 10), nil); rec.Code != http.StatusNoContent {
		t.Fatalf("remove = %d: %s", rec.Code, rec.Body)
	}
	if err := f.accounts.Delete(ctx, gone.ID); err != nil {
		t.Fatalf("delete account: %v", err)
	}
	if err := f.b2b.reconcile(ctx); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	members, _, err := f.app.Pricing().Members(ctx, *f.company.GroupID, 10, 0)
	if err != nil || len(members) != 1 || members[0] != "boss@acme.test" {
		t.Errorf("group = %v (%v), want only the boss left", members, err)
	}

	// And the last admin cannot be demoted or leave.
	boss, _ := f.b2b.MemberOf(ctx, mustID(t, f, "boss@acme.test"))
	if _, err := f.b2b.SetRole(ctx, f.company.ID, boss.CustomerID, RoleBuyer); err == nil {
		t.Error("the last admin was demoted")
	}
}

func mustID(t *testing.T, f *fixture, email string) int64 {
	t.Helper()
	acct, err := f.accounts.CustomerByEmail(context.Background(), email)
	if err != nil {
		t.Fatalf("lookup %s: %v", email, err)
	}
	return acct.ID
}

func TestModuleContract(t *testing.T) {
	f := newFixture(t)
	gctest.AssertAdminRoutesDeclareRights(t, f.app, "b2b")
	gctest.AssertSpecCoversModuleRoutes(t, f.app, "b2b")
}

// Two buyers checking out at once must not both fit under a limit only one
// of them fits under. Each order alone is within it; together they are not.
// The guard's per-company lock is what makes the second one see the first.
//
// The two baskets hold different products on purpose. With the same product
// the stock row lock serialises the checkouts by accident, and this test
// passed with the advisory lock removed — it was proving the wrong lock.
func TestTwoCheckoutsCannotBothFitUnderOneLimit(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a, _ := f.member(t, "first@acme.test", RoleAdmin)
	b, _ := f.member(t, "second@acme.test", RoleApprover)
	other := gctest.CreateProduct(t, f.app, "B2B-OTHER", 8000, 100).Variants[0].ID
	otherCart, err := f.app.Cart().Create(ctx, "")
	if err != nil {
		t.Fatalf("cart: %v", err)
	}
	if _, err := f.app.Cart().AddLine(ctx, otherCart.Token, other, 4); err != nil {
		t.Fatalf("add: %v", err)
	}
	carts := []string{f.cart(t, 4), otherCart.Token} // 32000 each against 50000

	type outcome struct{ err error }
	results := make(chan outcome, 2)
	start := make(chan struct{})
	for i, buyer := range []*identity.Customer{a, b} {
		go func(buyer *identity.Customer, cart string) {
			<-start
			_, _, err := f.b2b.Checkout(ctx, buyer, CheckoutRequest{CartID: cart, Address: address}, "")
			results <- outcome{err}
		}(buyer, carts[i])
	}
	close(start)
	placed, refused := 0, 0
	for range 2 {
		r := <-results
		var apiErr *gocommerce.APIError
		switch {
		case r.err == nil:
			placed++
		case errors.As(r.err, &apiErr) && apiErr.Code == "credit_limit_exceeded":
			refused++
		default:
			t.Errorf("unexpected error: %v", r.err)
		}
	}
	if placed != 1 || refused != 1 {
		t.Errorf("placed %d, refused %d; want exactly one of each", placed, refused)
	}
	credit, err := f.b2b.Credit(ctx, f.company.ID)
	if err != nil || credit.Outstanding.AmountMinor != 32000 {
		t.Errorf("outstanding = %+v (%v), want 32000", credit, err)
	}
}

// A quote past its expiry reads as expired and cannot be taken.
func TestAnExpiredQuoteCannotBeAccepted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	acct, tok := f.member(t, "boss@acme.test", RoleAdmin)

	q, err := f.b2b.RequestQuote(ctx, acct, QuoteRequest{Lines: []QuoteLineInput{{VariantID: f.variant, Quantity: 2}}})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	price := int64(7000)
	if _, err := f.b2b.PriceQuote(ctx, q.ID, QuoteReply{Lines: []QuoteLineInput{{VariantID: f.variant, Quantity: 2, UnitPriceMinor: &price}}}); err != nil {
		t.Fatalf("price: %v", err)
	}
	if _, err := f.b2b.SendQuote(ctx, q.ID); err != nil {
		t.Fatalf("send: %v", err)
	}
	if _, err := f.app.DB().ExecContext(ctx,
		`UPDATE b2b_quotes SET expires_at = now() - interval '1 minute' WHERE id = $1`, q.ID); err != nil {
		t.Fatalf("expire: %v", err)
	}

	rec := gctest.SessionRequest(t, f.app, tok, http.MethodPost,
		"/x/b2b/quotes/"+strconv.FormatInt(q.ID, 10)+"/accept", map[string]any{"address": address})
	if rec.Code != http.StatusConflict || errCode(rec.Body.String()) != "quote_expired" {
		t.Errorf("accepting an expired quote = %d %s, want 409 quote_expired", rec.Code, rec.Body)
	}
	expired, _, err := f.b2b.Quotes(ctx, f.company.ID, 0, QuoteExpired, 10, 0)
	if err != nil || len(expired) != 1 || expired[0].Status != QuoteExpired {
		t.Errorf("expired quotes = %+v (%v), want this one, reading as expired", expired, err)
	}
}

// A buyer who moves to a new address loses the company's prices until the new
// one is confirmed, and gets them back once it is — the old address leaving
// the group at once, the new one joining only when proven.
func TestAChangedAddressRejoinsOnceConfirmed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	acct, tok := f.member(t, "buyer@acme.test", RoleAdmin)
	inGroup := func() []string {
		t.Helper()
		members, _, err := f.app.Pricing().Members(ctx, *f.company.GroupID, 10, 0)
		if err != nil {
			t.Fatalf("members: %v", err)
		}
		return members
	}

	moved := "buyer@new-acme.test"
	if _, err := f.accounts.Update(ctx, acct.ID, &moved, nil, nil); err != nil {
		t.Fatalf("change address: %v", err)
	}
	if rec := gctest.SessionRequest(t, f.app, tok, http.MethodGet, "/x/b2b/me", nil); rec.Code != http.StatusOK {
		t.Fatalf("me = %d: %s", rec.Code, rec.Body)
	}
	if got := inGroup(); len(got) != 0 {
		t.Errorf("group = %v after moving to an unconfirmed address, want empty", got)
	}

	if _, err := f.accounts.MarkEmailVerified(ctx, acct.ID, moved); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if rec := gctest.SessionRequest(t, f.app, tok, http.MethodGet, "/x/b2b/me", nil); rec.Code != http.StatusOK {
		t.Fatalf("me = %d: %s", rec.Code, rec.Body)
	}
	if got := inGroup(); len(got) != 1 || got[0] != moved {
		t.Errorf("group = %v once the new address is confirmed, want [%s]", got, moved)
	}
}

// A quote is one order at most: two accepts at once place one, and an accept
// that needs approval holds the quote until the approval is decided.
func TestAQuoteBecomesOneOrder(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	boss, bossTok := f.member(t, "boss@acme.test", RoleAdmin)
	junior, juniorTok := f.member(t, "junior@acme.test", RoleBuyer)

	sent := func(requester *identity.Customer, qty int, price int64) *Quote {
		t.Helper()
		q, err := f.b2b.RequestQuote(ctx, requester, QuoteRequest{Lines: []QuoteLineInput{{VariantID: f.variant, Quantity: qty}}})
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		if _, err := f.b2b.PriceQuote(ctx, q.ID, QuoteReply{Lines: []QuoteLineInput{{VariantID: f.variant, Quantity: qty, UnitPriceMinor: &price}}}); err != nil {
			t.Fatalf("price: %v", err)
		}
		if q, err = f.b2b.SendQuote(ctx, q.ID); err != nil {
			t.Fatalf("send: %v", err)
		}
		return q
	}

	// Two accepts at the same moment.
	q := sent(boss, 2, 7000)
	results := make(chan error, 2)
	start := make(chan struct{})
	for range 2 {
		go func() {
			<-start
			_, _, err := f.b2b.AcceptQuote(ctx, boss, q.ID, QuoteAcceptance{Address: address})
			results <- err
		}()
	}
	close(start)
	placed := 0
	for range 2 {
		if err := <-results; err == nil {
			placed++
		}
	}
	if placed != 1 {
		t.Errorf("two simultaneous accepts placed %d orders, want 1", placed)
	}

	// Another buyer's quote is not this buyer's to take.
	if rec := gctest.SessionRequest(t, f.app, juniorTok, http.MethodPost,
		"/x/b2b/quotes/"+strconv.FormatInt(sent(boss, 1, 7000).ID, 10)+"/accept",
		map[string]any{"address": address}); rec.Code != http.StatusNotFound {
		t.Errorf("a buyer accepting someone else's quote = %d, want 404", rec.Code)
	}

	// Over the junior's limit: the quote waits with the approval, cannot be
	// accepted again meanwhile, and goes back on offer when it is rejected.
	big := sent(junior, 5, 7000) // 35000 > 30000
	_, approval, err := f.b2b.AcceptQuote(ctx, junior, big.ID, QuoteAcceptance{Address: address})
	if err != nil || approval == nil {
		t.Fatalf("accept over the limit = approval %v, err %v; want an approval", approval, err)
	}
	if got, _ := f.b2b.Quote(ctx, big.ID); got.Status != QuoteAwaitingApproval {
		t.Errorf("quote status = %q while its approval is open, want awaiting_approval", got.Status)
	}
	if _, _, err := f.b2b.AcceptQuote(ctx, junior, big.ID, QuoteAcceptance{Address: address}); err == nil {
		t.Error("a quote awaiting approval was accepted a second time")
	}
	if _, err := f.b2b.Reject(ctx, boss, approval.ID, "not this month"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	if got, _ := f.b2b.Quote(ctx, big.ID); got.Status != QuoteQuoted {
		t.Errorf("quote status = %q after its approval was rejected, want quoted again", got.Status)
	}
	_ = bossTok
}
