package b2b

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/ext/identity"
)

func (m *Module) mountRoutes(app *gocommerce.App) {
	// The buyer's side, behind a shopper session from ext/identity.
	app.HandleFunc("GET /x/b2b/me", m.session(m.handleMe))
	app.HandleFunc("GET /x/b2b/members", m.session(m.handleMembers))
	app.HandleFunc("PATCH /x/b2b/members/{customer_id}", m.session(m.handleSetMemberRole))
	app.HandleFunc("DELETE /x/b2b/members/{customer_id}", m.session(m.handleRemoveMember))
	app.HandleFunc("GET /x/b2b/invitations", m.session(m.handleListInvitations))
	app.HandleFunc("POST /x/b2b/invitations", m.session(m.handleInvite))
	app.HandleFunc("DELETE /x/b2b/invitations/{id}", m.session(m.handleRevokeInvitation))
	app.HandleFunc("POST /x/b2b/invitations/accept", m.session(m.handleAcceptInvitation))
	app.HandleFunc("POST /x/b2b/cart/lines", m.session(m.handleAddLines))
	app.HandleFunc("POST /x/b2b/checkout", m.session(m.handleCheckout))
	app.HandleFunc("GET /x/b2b/orders", m.session(m.handleMyOrders))
	app.HandleFunc("GET /x/b2b/orders/{order_id}", m.session(m.handleMyOrder))
	app.HandleFunc("POST /x/b2b/orders/{order_id}/reorder", m.session(m.handleReorder))
	app.HandleFunc("GET /x/b2b/approvals", m.session(m.handleMyApprovals))
	app.HandleFunc("GET /x/b2b/approvals/{id}", m.session(m.handleMyApproval))
	app.HandleFunc("POST /x/b2b/approvals/{id}/approve", m.session(m.handleApprove))
	app.HandleFunc("POST /x/b2b/approvals/{id}/reject", m.session(m.handleReject))
	app.HandleFunc("POST /x/b2b/approvals/{id}/cancel", m.session(m.handleCancelApproval))
	app.HandleFunc("GET /x/b2b/quotes", m.session(m.handleMyQuotes))
	app.HandleFunc("POST /x/b2b/quotes", m.session(m.handleRequestQuote))
	app.HandleFunc("GET /x/b2b/quotes/{id}", m.session(m.handleMyQuote))
	app.HandleFunc("POST /x/b2b/quotes/{id}/accept", m.session(m.handleAcceptQuote))
	app.HandleFunc("POST /x/b2b/quotes/{id}/decline", m.session(m.handleDeclineMyQuote))
	app.HandleFunc("GET /x/b2b/leads", m.session(m.handleMyLeads))
	app.HandleFunc("PATCH /x/b2b/leads/{id}", m.session(m.handleSetMyLead))

	// The storefront's dealer form, sent by a member of the public: the one
	// route here with no session at all.
	app.HandleFunc("POST /x/b2b/leads", m.handleFileLead)

	// The store's side.
	app.HandleAdminFunc("GET /api/admin/x/b2b/companies", m.handleAdminCompanies, rightCompaniesRead)
	app.HandleAdminFunc("POST /api/admin/x/b2b/companies", m.handleAdminCreateCompany, rightCompaniesWrite)
	app.HandleAdminFunc("GET /api/admin/x/b2b/companies/{id}", m.handleAdminCompany, rightCompaniesRead)
	app.HandleAdminFunc("PATCH /api/admin/x/b2b/companies/{id}", m.handleAdminUpdateCompany, rightCompaniesWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/b2b/companies/{id}", m.handleAdminDeleteCompany, rightCompaniesWrite)
	app.HandleAdminFunc("GET /api/admin/x/b2b/companies/{id}/credit", m.handleAdminCredit, rightCompaniesRead)
	app.HandleAdminFunc("GET /api/admin/x/b2b/companies/{id}/members", m.handleAdminMembers, rightCompaniesRead)
	app.HandleAdminFunc("POST /api/admin/x/b2b/companies/{id}/members", m.handleAdminAddMember, rightCompaniesWrite)
	app.HandleAdminFunc("PATCH /api/admin/x/b2b/companies/{id}/members/{customer_id}", m.handleAdminSetRole, rightCompaniesWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/b2b/companies/{id}/members/{customer_id}", m.handleAdminRemoveMember, rightCompaniesWrite)
	app.HandleAdminFunc("GET /api/admin/x/b2b/companies/{id}/invitations", m.handleAdminInvitations, rightCompaniesRead)
	app.HandleAdminFunc("DELETE /api/admin/x/b2b/companies/{id}/invitations/{invitation_id}", m.handleAdminRevokeInvitation, rightCompaniesWrite)
	app.HandleAdminFunc("GET /api/admin/x/b2b/companies/{id}/orders", m.handleAdminCompanyOrders, rightCompaniesRead)
	app.HandleAdminFunc("GET /api/admin/x/b2b/companies/{id}/territories", m.handleAdminTerritories, rightCompaniesRead)
	app.HandleAdminFunc("POST /api/admin/x/b2b/companies/{id}/territories", m.handleAdminAddTerritory, rightCompaniesWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/b2b/companies/{id}/territories/{territory_id}", m.handleAdminDeleteTerritory, rightCompaniesWrite)
	app.HandleAdminFunc("GET /api/admin/x/b2b/territories", m.handleAdminAllTerritories, rightCompaniesRead)
	app.HandleAdminFunc("GET /api/admin/x/b2b/leads", m.handleAdminLeads, rightLeadsRead)
	app.HandleAdminFunc("GET /api/admin/x/b2b/leads/{id}", m.handleAdminLead, rightLeadsRead)
	app.HandleAdminFunc("PATCH /api/admin/x/b2b/leads/{id}", m.handleAdminUpdateLead, rightLeadsWrite)
	app.HandleAdminFunc("GET /api/admin/x/b2b/receivables", m.handleAdminReceivables, rightCompaniesRead)
	app.HandleAdminFunc("GET /api/admin/x/b2b/orders/{order_id}", m.handleAdminCompanyOrder, rightCompaniesRead)
	app.HandleAdminFunc("GET /api/admin/x/b2b/approvals", m.handleAdminApprovals, rightCompaniesRead)
	app.HandleAdminFunc("GET /api/admin/x/b2b/quotes", m.handleAdminQuotes, rightQuotesRead)
	app.HandleAdminFunc("GET /api/admin/x/b2b/quotes/{id}", m.handleAdminQuote, rightQuotesRead)
	app.HandleAdminFunc("PUT /api/admin/x/b2b/quotes/{id}", m.handleAdminPriceQuote, rightQuotesWrite)
	app.HandleAdminFunc("POST /api/admin/x/b2b/quotes/{id}/send", m.handleAdminSendQuote, rightQuotesWrite)
	app.HandleAdminFunc("POST /api/admin/x/b2b/quotes/{id}/decline", m.handleAdminDeclineQuote, rightQuotesWrite)
}

// session resolves the shopper's bearer token through the accounts module.
// Identity's own middleware is unexported, and a module asking for "which
// account is this" is exactly what Resolve is for.
func (m *Module) session(h func(http.ResponseWriter, *http.Request, *identity.Customer)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		tok := ""
		if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
			tok = strings.TrimSpace(auth[7:])
		}
		acct, ok := (*identity.Customer)(nil), false
		if tok != "" {
			acct, ok = m.accounts.Resolve(r.Context(), tok)
		}
		if !ok {
			gocommerce.RespondError(w, r, gocommerce.ErrUnauthorized)
			return
		}
		h(w, r, acct)
	}
}

// buyerContext is a signed-in member, their company and their role.
type buyerContext struct {
	acct    *identity.Customer
	member  *Member
	company *Company
}

func (m *Module) buyer(w http.ResponseWriter, r *http.Request, acct *identity.Customer) (*buyerContext, bool) {
	mem, company, err := m.memberAndCompany(r.Context(), acct)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return nil, false
	}
	return &buyerContext{acct: acct, member: mem, company: company}, true
}

func (b *buyerContext) can(roles ...string) bool {
	for _, r := range roles {
		if b.member.Role == r {
			return true
		}
	}
	return false
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.Validationf("%s must be a positive integer", name)
	}
	return id, nil
}

func idOr400(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	id, err := pathID(r, name)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return 0, false
	}
	return id, true
}

func page(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	limit, offset, err := gocommerce.Page(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return 0, 0, false
	}
	return limit, offset, true
}

func respond(w http.ResponseWriter, r *http.Request, status int, v any, err error) {
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, status, v)
}

// ------------------------------------------------------------ buyer's side

// meResponse is a buyer's view of their company: who they are in it and where
// its account stands.
type meResponse struct {
	Company *Company `json:"company"`
	Role    string   `json:"role"`
	Credit  *Credit  `json:"credit"`
}

func (m *Module) handleMe(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	credit, err := m.Credit(r.Context(), b.company.ID)
	respond(w, r, http.StatusOK, meResponse{Company: b.company, Role: b.member.Role, Credit: credit}, err)
}

func (m *Module) handleMembers(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	members, err := m.Members(r.Context(), b.company.ID)
	respond(w, r, http.StatusOK, members, err)
}

func (m *Module) handleSetMemberRole(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	if !b.can(RoleAdmin) {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("only a company admin may change roles"))
		return
	}
	id, ok := idOr400(w, r, "customer_id")
	if !ok {
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	mem, err := m.SetRole(r.Context(), b.company.ID, id, in.Role)
	respond(w, r, http.StatusOK, mem, err)
}

// handleRemoveMember lets an admin remove anybody, and anybody remove
// themselves — a buyer who has left the company should not need an admin's
// help to stop being one.
func (m *Module) handleRemoveMember(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	id, ok := idOr400(w, r, "customer_id")
	if !ok {
		return
	}
	if id != acct.ID && !b.can(RoleAdmin) {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("only a company admin may remove another buyer"))
		return
	}
	if err := m.RemoveMember(r.Context(), b.company.ID, id); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) handleListInvitations(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	if !b.can(RoleAdmin) {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("only a company admin may see invitations"))
		return
	}
	invs, err := m.Invitations(r.Context(), b.company.ID)
	respond(w, r, http.StatusOK, invs, err)
}

type inviteInput struct {
	Email string `json:"email"`
	Role  string `json:"role"`
}

func (m *Module) handleInvite(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	if !b.can(RoleAdmin) {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("only a company admin may invite buyers"))
		return
	}
	var in inviteInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	inv, err := m.Invite(r.Context(), b.company.ID, in.Email, in.Role, acct.Email)
	respond(w, r, http.StatusCreated, inv, err)
}

func (m *Module) handleRevokeInvitation(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	if !b.can(RoleAdmin) {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("only a company admin may withdraw an invitation"))
		return
	}
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	if err := m.RevokeInvitation(r.Context(), b.company.ID, id); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAcceptInvitation is the one buyer route that does not need the
// caller to be a member yet: it is how they become one.
func (m *Module) handleAcceptInvitation(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	var in struct {
		Token string `json:"token"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	mem, err := m.AcceptInvitation(r.Context(), acct.ID, in.Token)
	respond(w, r, http.StatusOK, mem, err)
}

// checkoutResponse is either an order placed or a request filed — never both.
type checkoutResponse struct {
	*gocommerce.CheckoutResult
	Approval *Approval `json:"approval,omitempty"`
}

func (m *Module) handleCheckout(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	var in CheckoutRequest
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	result, approval, err := m.Checkout(r.Context(), acct, in, r.Header.Get("Idempotency-Key"))
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if approval != nil {
		// 202: accepted for a decision, not yet an order.
		gocommerce.Respond(w, http.StatusAccepted, checkoutResponse{Approval: approval})
		return
	}
	gocommerce.Respond(w, http.StatusCreated, checkoutResponse{CheckoutResult: result})
}

// handleAddLines answers 200 whatever was rejected: the basket exists and
// holds what could go in, and rejected says what could not.
func (m *Module) handleAddLines(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	var in BulkRequest
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	fill, err := m.AddLines(r.Context(), acct, in)
	respond(w, r, http.StatusOK, fill, err)
}

// handleReorder takes an optional body naming the basket to fill. Without one
// it opens a new basket, as it always has.
//
// The route read no body before cart_id existed, so whatever a client sends
// is read for cart_id alone: a field it does not know, or a body that is not
// JSON at all, answers as it always did rather than becoming a 400. Not
// "> 0": a chunked body reports -1, and its cart_id would be lost.
func (m *Module) handleReorder(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	id, ok := idOr400(w, r, "order_id")
	if !ok {
		return
	}
	var in struct {
		CartID string `json:"cart_id"`
	}
	if r.ContentLength != 0 {
		if body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20)); err == nil {
			_ = json.Unmarshal(body, &in)
		}
	}
	fill, err := m.ReorderInto(r.Context(), acct, id, in.CartID)
	respond(w, r, http.StatusCreated, fill, err)
}

// handleMyOrder is one order from the company's list, with what was in it:
// the list says what an order is, and a buyer deciding whether to repeat it
// needs to see what it held.
func (m *Module) handleMyOrder(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	id, ok := idOr400(w, r, "order_id")
	if !ok {
		return
	}
	o, err := m.MyOrder(r.Context(), acct, id)
	respond(w, r, http.StatusOK, o, err)
}

// handleMyOrders shows a buyer the orders placed for their company: every
// buyer's to an admin or approver, their own to a buyer.
func (m *Module) handleMyOrders(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	limit, offset, ok := page(w, r)
	if !ok {
		return
	}
	var who int64
	if !b.can(RoleAdmin, RoleApprover) {
		who = acct.ID
	}
	// The buyer's own route searches the email too: these are the buyer's
	// colleagues' orders, shown unmasked, so nothing is being read back.
	orders, total, err := m.CompanyOrders(r.Context(), CompanyOrderQuery{CompanyID: b.company.ID,
		CustomerID: who, OverdueOnly: r.URL.Query().Get("overdue") == "true",
		Search: r.URL.Query().Get("q"), Limit: limit, Offset: offset})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, orders, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleMyApprovals(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	limit, offset, ok := page(w, r)
	if !ok {
		return
	}
	var who int64
	if !b.can(RoleAdmin, RoleApprover) {
		who = acct.ID
	}
	list, total, err := m.Approvals(r.Context(), b.company.ID, who, r.URL.Query().Get("status"), limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleMyApproval(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	a, err := m.approvalFor(r.Context(), b.company.ID, id)
	if err == nil && a.RequestedBy != acct.ID && !b.can(RoleAdmin, RoleApprover) {
		err = gocommerce.NotFoundf("approval %d does not exist", id)
	}
	respond(w, r, http.StatusOK, a, err)
}

// approveResponse is the order the approval placed, in the shape checkout
// answers with, and the approval it closed.
type approveResponse struct {
	*gocommerce.CheckoutResult
	Approval *Approval `json:"approval"`
}

func (m *Module) handleApprove(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	a, result, err := m.Approve(r.Context(), acct, id)
	// 201, as every route that places an order answers.
	respond(w, r, http.StatusCreated, approveResponse{CheckoutResult: result, Approval: a}, err)
}

func (m *Module) handleReject(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Reason string `json:"reason"`
	}
	// Not "> 0": a chunked body reports -1, and its reason would be lost.
	if r.ContentLength != 0 {
		if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
	}
	a, err := m.Reject(r.Context(), acct, id, in.Reason)
	respond(w, r, http.StatusOK, a, err)
}

func (m *Module) handleCancelApproval(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	a, err := m.CancelApproval(r.Context(), acct, id)
	respond(w, r, http.StatusOK, a, err)
}

func (m *Module) handleMyQuotes(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	limit, offset, ok := page(w, r)
	if !ok {
		return
	}
	var who int64
	if !b.can(RoleAdmin, RoleApprover) {
		who = acct.ID
	}
	list, total, err := m.Quotes(r.Context(), b.company.ID, who, r.URL.Query().Get("status"), limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleRequestQuote(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	var in QuoteRequest
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	q, err := m.RequestQuote(r.Context(), acct, in)
	respond(w, r, http.StatusCreated, q, err)
}

func (m *Module) handleMyQuote(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	q, err := m.quoteFor(r.Context(), b.company.ID, id)
	if err == nil && q.RequestedBy != acct.ID && !b.can(RoleAdmin, RoleApprover) {
		err = gocommerce.NotFoundf("quote %d does not exist", id)
	}
	respond(w, r, http.StatusOK, q, err)
}

func (m *Module) handleAcceptQuote(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	var in QuoteAcceptance
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	result, approval, err := m.AcceptQuote(r.Context(), acct, id, in)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if approval != nil {
		gocommerce.Respond(w, http.StatusAccepted, checkoutResponse{Approval: approval})
		return
	}
	gocommerce.Respond(w, http.StatusCreated, checkoutResponse{CheckoutResult: result})
}

func (m *Module) handleDeclineMyQuote(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return
	}
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	q, err := m.quoteFor(r.Context(), b.company.ID, id)
	if err == nil && q.RequestedBy != acct.ID && !b.can(RoleAdmin, RoleApprover) {
		err = gocommerce.NotFoundf("quote %d does not exist", id)
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	q, err = m.DeclineQuote(r.Context(), id, acct.Email)
	respond(w, r, http.StatusOK, q, err)
}

// leadAccepted is all the public form is told. Naming the dealer, or even
// whether there was one, would let anybody walk a list of postcodes and map
// the store's dealer network.
type leadAccepted struct {
	Accepted bool `json:"accepted"`
}

// handleFileLead counts the attempt before reading it, so a stream of
// malformed requests is limited like any other.
func (m *Module) handleFileLead(w http.ResponseWriter, r *http.Request) {
	if wait, ok := m.leads.allow(peerAddr(r), time.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
		gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusTooManyRequests,
			Code: "too_many_attempts", Message: "too many enquiries from this address; try again in a minute"})
		return
	}
	var in LeadInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if _, err := m.FileLead(r.Context(), in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusAccepted, leadAccepted{Accepted: true})
}

// dealer is the caller's company, for the lead routes: an enquiry is answered
// by whoever runs the account, so a buyer does not see them.
func (m *Module) dealer(w http.ResponseWriter, r *http.Request, acct *identity.Customer) (*buyerContext, bool) {
	b, ok := m.buyer(w, r, acct)
	if !ok {
		return nil, false
	}
	if !b.can(RoleAdmin, RoleApprover) {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("only a company admin or approver may see its leads"))
		return nil, false
	}
	return b, true
}

func (m *Module) handleMyLeads(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.dealer(w, r, acct)
	if !ok {
		return
	}
	limit, offset, ok := page(w, r)
	if !ok {
		return
	}
	list, total, err := m.Leads(r.Context(), LeadQuery{CompanyID: b.company.ID,
		Status: r.URL.Query().Get("status"), Limit: limit, Offset: offset})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleSetMyLead(w http.ResponseWriter, r *http.Request, acct *identity.Customer) {
	b, ok := m.dealer(w, r, acct)
	if !ok {
		return
	}
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Status string `json:"status"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	lead, err := m.SetLeadStatus(r.Context(), b.company.ID, id, in.Status)
	respond(w, r, http.StatusOK, lead, err)
}

// ------------------------------------------------------------ store's side

// The admin side masks buyers' addresses on a demo store, as every core
// admin route does; the buyer's own routes serve their own colleagues.
func (m *Module) maskMembers(ms []*Member) {
	for _, mem := range ms {
		mem.Email = m.app.MaskEmail(mem.Email)
	}
}

func (m *Module) handleAdminCompanies(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := page(w, r)
	if !ok {
		return
	}
	list, total, err := m.Companies(r.Context(), CompanyQuery{Search: r.URL.Query().Get("q"),
		Dealers: r.URL.Query().Get("dealers") == "true", Limit: limit, Offset: offset})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleAdminCreateCompany(w http.ResponseWriter, r *http.Request) {
	var in CompanyInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	c, err := m.CreateCompany(r.Context(), in)
	respond(w, r, http.StatusCreated, c, err)
}

func (m *Module) handleAdminCompany(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	c, err := m.Company(r.Context(), id)
	respond(w, r, http.StatusOK, c, err)
}

func (m *Module) handleAdminUpdateCompany(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	var in CompanyInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	c, err := m.UpdateCompany(r.Context(), id, in)
	respond(w, r, http.StatusOK, c, err)
}

func (m *Module) handleAdminDeleteCompany(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	if err := m.DeleteCompany(r.Context(), id); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) handleAdminCredit(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	c, err := m.Credit(r.Context(), id)
	respond(w, r, http.StatusOK, c, err)
}

func (m *Module) handleAdminMembers(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	if _, err := m.Company(r.Context(), id); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	members, err := m.Members(r.Context(), id)
	if err == nil {
		m.maskMembers(members)
	}
	respond(w, r, http.StatusOK, members, err)
}

// addMemberResponse is a buyer added at once, or an invitation sent because
// their address is not yet proven — exactly one of the two.
type addMemberResponse struct {
	Member     *Member     `json:"member,omitempty"`
	Invitation *Invitation `json:"invitation,omitempty"`
}

func (m *Module) handleAdminAddMember(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	var in inviteInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	by := "the store"
	if su := gocommerce.SuperuserFrom(r.Context()); su != nil && su.Email != "" {
		by = su.Email
	}
	mem, inv, err := m.AddOrInvite(r.Context(), id, in.Email, in.Role, by)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// Masked like the member and invitation lists, on a demo store.
	if mem != nil {
		mem.Email = m.app.MaskEmail(mem.Email)
		gocommerce.Respond(w, http.StatusCreated, addMemberResponse{Member: mem})
		return
	}
	inv.Email = m.app.MaskEmail(inv.Email)
	gocommerce.Respond(w, http.StatusAccepted, addMemberResponse{Invitation: inv})
}

func (m *Module) handleAdminSetRole(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	cust, ok := idOr400(w, r, "customer_id")
	if !ok {
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	mem, err := m.SetRole(r.Context(), id, cust, in.Role)
	if err == nil {
		mem.Email = m.app.MaskEmail(mem.Email)
	}
	respond(w, r, http.StatusOK, mem, err)
}

// handleAdminRemoveMember lets the store remove anybody, the last admin
// included: the store is who rescues a company nobody can administer.
func (m *Module) handleAdminRemoveMember(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	cust, ok := idOr400(w, r, "customer_id")
	if !ok {
		return
	}
	mem, err := m.MemberOf(r.Context(), cust)
	if err != nil || mem.CompanyID != id {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("that account is not a buyer for this company"))
		return
	}
	if err := m.dropMember(r.Context(), mem); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) handleAdminInvitations(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	if _, err := m.Company(r.Context(), id); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	invs, err := m.Invitations(r.Context(), id)
	if err == nil {
		for _, inv := range invs {
			inv.Email = m.app.MaskEmail(inv.Email)
		}
	}
	respond(w, r, http.StatusOK, invs, err)
}

func (m *Module) handleAdminRevokeInvitation(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	inv, ok := idOr400(w, r, "invitation_id")
	if !ok {
		return
	}
	if err := m.RevokeInvitation(r.Context(), id, inv); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Module) listOrders(w http.ResponseWriter, r *http.Request, companyID int64, onAccountOnly bool) {
	limit, offset, ok := page(w, r)
	if !ok {
		return
	}
	orders, total, err := m.CompanyOrders(r.Context(), CompanyOrderQuery{CompanyID: companyID,
		OnAccountOnly: onAccountOnly, OverdueOnly: r.URL.Query().Get("overdue") == "true",
		Search: r.URL.Query().Get("q"), Demo: m.app.Demo(), Limit: limit, Offset: offset})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	for _, o := range orders {
		o.PlacedBy = m.app.MaskEmail(o.PlacedBy)
	}
	gocommerce.RespondList(w, orders, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleAdminCompanyOrders(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	if _, err := m.Company(r.Context(), id); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.listOrders(w, r, id, false)
}

// handleAdminCompanyOrder is one order's place in its company's ledger: whose
// it was, its PO number, whether it is on account and when it is due. The
// order screen asks for it beside the order itself.
func (m *Module) handleAdminCompanyOrder(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "order_id")
	if !ok {
		return
	}
	co, err := m.CompanyOrder(r.Context(), id)
	if err == nil {
		co.PlacedBy = m.app.MaskEmail(co.PlacedBy)
	}
	respond(w, r, http.StatusOK, co, err)
}

// handleAdminReceivables is every company's orders on account — the ledger
// a store chases payment from; an order paid at checkout is owed nothing.
// ?overdue=true narrows it to what is late.
func (m *Module) handleAdminReceivables(w http.ResponseWriter, r *http.Request) {
	m.listOrders(w, r, 0, true)
}

func (m *Module) handleAdminApprovals(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := page(w, r)
	if !ok {
		return
	}
	var company int64
	if v := r.URL.Query().Get("company_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			gocommerce.RespondError(w, r, gocommerce.Validationf("company_id must be an integer"))
			return
		}
		company = n
	}
	list, total, err := m.Approvals(r.Context(), company, 0, r.URL.Query().Get("status"), limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	for _, a := range list {
		a.RequesterMail = m.app.MaskEmail(a.RequesterMail)
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleAdminQuotes(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := page(w, r)
	if !ok {
		return
	}
	var company int64
	if v := r.URL.Query().Get("company_id"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			gocommerce.RespondError(w, r, gocommerce.Validationf("company_id must be an integer"))
			return
		}
		company = n
	}
	list, total, err := m.Quotes(r.Context(), company, 0, r.URL.Query().Get("status"), limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	for _, q := range list {
		q.RequesterMail = m.app.MaskEmail(q.RequesterMail)
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) adminQuote(w http.ResponseWriter, r *http.Request, q *Quote, err error) {
	if err == nil {
		q.RequesterMail = m.app.MaskEmail(q.RequesterMail)
	}
	respond(w, r, http.StatusOK, q, err)
}

func (m *Module) handleAdminQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	q, err := m.Quote(r.Context(), id)
	m.adminQuote(w, r, q, err)
}

func (m *Module) handleAdminPriceQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	var in QuoteReply
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	q, err := m.PriceQuote(r.Context(), id, in)
	m.adminQuote(w, r, q, err)
}

func (m *Module) handleAdminSendQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	q, err := m.SendQuote(r.Context(), id)
	m.adminQuote(w, r, q, err)
}

func (m *Module) handleAdminDeclineQuote(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	q, err := m.DeclineQuote(r.Context(), id, "store")
	m.adminQuote(w, r, q, err)
}

// companyFilter reads ?company_id, zero when absent.
func companyFilter(w http.ResponseWriter, r *http.Request) (int64, bool) {
	v := r.URL.Query().Get("company_id")
	if v == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("company_id must be a positive integer"))
		return 0, false
	}
	return n, true
}

func (m *Module) listTerritories(w http.ResponseWriter, r *http.Request, companyID int64) {
	limit, offset, ok := page(w, r)
	if !ok {
		return
	}
	list, total, err := m.Territories(r.Context(), companyID, r.URL.Query().Get("country"), limit, offset)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleAdminTerritories(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	if _, err := m.Company(r.Context(), id); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.listTerritories(w, r, id)
}

// handleAdminAllTerritories is every dealer's territories, for a map of the
// network or a check of who covers where.
func (m *Module) handleAdminAllTerritories(w http.ResponseWriter, r *http.Request) {
	company, ok := companyFilter(w, r)
	if !ok {
		return
	}
	m.listTerritories(w, r, company)
}

func (m *Module) handleAdminAddTerritory(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	var in TerritoryInput
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	t, err := m.AddTerritory(r.Context(), id, in)
	respond(w, r, http.StatusCreated, t, err)
}

func (m *Module) handleAdminDeleteTerritory(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	tid, ok := idOr400(w, r, "territory_id")
	if !ok {
		return
	}
	if err := m.DeleteTerritory(r.Context(), id, tid); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// maskLead hides a member of the public's address and number on a demo
// store, as every admin route hides a shopper's. The dealer's own routes are
// not masked: those are the dealer's customers to call.
func (m *Module) maskLead(l *Lead) {
	l.Email = m.app.MaskEmail(l.Email)
	l.Phone = m.app.MaskPhone(l.Phone)
}

func (m *Module) handleAdminLeads(w http.ResponseWriter, r *http.Request) {
	limit, offset, ok := page(w, r)
	if !ok {
		return
	}
	company, ok := companyFilter(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	q := LeadQuery{CompanyID: company, Unrouted: v.Get("unrouted") == "true", Status: v.Get("status"),
		RoutedBy: v.Get("routed_by"), Search: v.Get("q"), Demo: m.app.Demo(), Limit: limit, Offset: offset}
	// Each pair asks for two sets of leads that cannot overlap. An empty page
	// would be a true answer to a question nobody meant to ask, so it is
	// refused instead, naming the pair.
	switch {
	case q.Unrouted && q.CompanyID > 0:
		gocommerce.RespondError(w, r, gocommerce.Validationf("unrouted and company_id ask for different leads; send one"))
		return
	case q.RoutedBy == RoutedNone && q.CompanyID > 0:
		gocommerce.RespondError(w, r, gocommerce.Validationf("routed_by=unrouted and company_id ask for different leads; send one"))
		return
	case q.Unrouted && q.RoutedBy != "" && q.RoutedBy != RoutedNone:
		gocommerce.RespondError(w, r, gocommerce.Validationf("unrouted and routed_by=%s ask for different leads; send one", q.RoutedBy))
		return
	}
	list, total, err := m.Leads(r.Context(), q)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	for _, l := range list {
		m.maskLead(l)
	}
	gocommerce.RespondList(w, list, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
}

func (m *Module) handleAdminLead(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	lead, err := m.Lead(r.Context(), id)
	if err == nil {
		m.maskLead(lead)
	}
	respond(w, r, http.StatusOK, lead, err)
}

func (m *Module) handleAdminUpdateLead(w http.ResponseWriter, r *http.Request) {
	id, ok := idOr400(w, r, "id")
	if !ok {
		return
	}
	var in LeadUpdate
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	lead, err := m.UpdateLead(r.Context(), id, in)
	if err == nil {
		m.maskLead(lead)
	}
	respond(w, r, http.StatusOK, lead, err)
}
