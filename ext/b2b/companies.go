package b2b

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// optionalInt64 tells a field the client left out from one it set to null:
// a patch that clears a credit limit and one that does not mention it are
// different requests.
type optionalInt64 struct {
	Set   bool
	Value *int64
}

func (o *optionalInt64) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v int64
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// CompanyInput creates a company or, with every field optional, patches one.
type CompanyInput struct {
	Code    *string       `json:"code"`
	Name    *string       `json:"name"`
	TaxID   *string       `json:"tax_id"`
	Status  *string       `json:"status"`
	GroupID optionalInt64 `json:"group_id"`
	// Minor units of the store currency. null clears it: no account, or no
	// approval rule.
	CreditLimitMinor       optionalInt64       `json:"credit_limit_minor"`
	NetDays                *int                `json:"net_days"`
	ApprovalThresholdMinor optionalInt64       `json:"approval_threshold_minor"`
	RequirePO              *bool               `json:"require_po"`
	Notes                  *string             `json:"notes"`
	Metadata               gocommerce.Metadata `json:"metadata"`
}

const companyColumns = `c.id, c.code, c.name, c.tax_id, c.status, c.group_id,
	c.credit_limit_minor, c.net_days, c.approval_threshold_minor, c.require_po,
	c.notes, c.metadata, c.created_at, c.updated_at,
	(SELECT count(*) FROM b2b_members bm WHERE bm.company_id = c.id),
	(SELECT count(*) FROM b2b_territories bt WHERE bt.company_id = c.id)`

type rowScanner interface{ Scan(...any) error }

func (m *Module) scanCompany(row rowScanner) (*Company, error) {
	c := &Company{}
	var group, limit, threshold sql.NullInt64
	var meta []byte
	if err := row.Scan(&c.ID, &c.Code, &c.Name, &c.TaxID, &c.Status, &group,
		&limit, &c.NetDays, &threshold, &c.RequirePO, &c.Notes, &meta,
		&c.CreatedAt, &c.UpdatedAt, &c.MemberCount, &c.TerritoryCount); err != nil {
		return nil, err
	}
	if group.Valid {
		g := group.Int64
		c.GroupID = &g
	}
	c.CreditLimit = m.moneyPtr(limit)
	c.ApprovalThreshold = m.moneyPtr(threshold)
	c.Metadata = gocommerce.Metadata{}
	if len(meta) > 0 {
		if err := json.Unmarshal(meta, &c.Metadata); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Company reads one company.
func (m *Module) Company(ctx context.Context, id int64) (*Company, error) {
	c, err := m.scanCompany(m.db.QueryRowContext(ctx,
		`SELECT `+companyColumns+` FROM b2b_companies c WHERE c.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("company %d does not exist", id)
	}
	return c, err
}

// CompanyQuery narrows a list of companies.
type CompanyQuery struct {
	// Search matches part of the name, in any case, or part of the code.
	Search string
	// Dealers keeps only companies with at least one territory.
	Dealers       bool
	Limit, Offset int
}

// Companies lists companies, newest first.
func (m *Module) Companies(ctx context.Context, q CompanyQuery) ([]*Company, int, error) {
	conds, args := []string{"true"}, []any{}
	if s := strings.ToLower(strings.TrimSpace(q.Search)); s != "" {
		args = append(args, "%"+s+"%")
		conds = append(conds, "(lower(c.name) LIKE $1 OR c.code LIKE $1)")
	}
	if q.Dealers {
		conds = append(conds, "EXISTS (SELECT 1 FROM b2b_territories bt WHERE bt.company_id = c.id)")
	}
	where := strings.Join(conds, " AND ")
	limit, offset := q.Limit, q.Offset
	var total int
	if err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM b2b_companies c WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(ctx, `SELECT `+companyColumns+` FROM b2b_companies c
		WHERE `+where+` ORDER BY c.id DESC
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Company{}
	for rows.Next() {
		c, err := m.scanCompany(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}

// CreateCompany adds a business customer.
func (m *Module) CreateCompany(ctx context.Context, in CompanyInput) (*Company, error) {
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		return nil, gocommerce.Validationf("a company needs a name")
	}
	code := ""
	if in.Code != nil {
		code = strings.TrimSpace(*in.Code)
	}
	if code == "" {
		code = handleize(*in.Name)
	}
	if err := m.validateCompany(ctx, in); err != nil {
		return nil, err
	}
	meta, err := json.Marshal(nonNil(in.Metadata))
	if err != nil {
		return nil, gocommerce.Validationf("metadata is not valid JSON: %v", err)
	}
	status := StatusActive
	if in.Status != nil {
		status = *in.Status
	}
	netDays := 30
	if in.NetDays != nil {
		netDays = *in.NetDays
	}
	var id int64
	err = m.db.QueryRowContext(ctx, `
		INSERT INTO b2b_companies (code, name, tax_id, status, group_id, credit_limit_minor,
		                           net_days, approval_threshold_minor, require_po, notes, metadata)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING id`,
		code, strings.TrimSpace(*in.Name), deref(in.TaxID), status, in.GroupID.Value,
		in.CreditLimitMinor.Value, netDays, in.ApprovalThresholdMinor.Value,
		in.RequirePO != nil && *in.RequirePO, deref(in.Notes), meta).Scan(&id)
	if err != nil {
		return nil, translateErr(err)
	}
	return m.Company(ctx, id)
}

// UpdateCompany patches a company. Changing its group moves every buyer's
// address from the old group to the new one, so their carts are priced on
// the new terms from their next checkout.
func (m *Module) UpdateCompany(ctx context.Context, id int64, in CompanyInput) (*Company, error) {
	before, err := m.Company(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Name != nil && strings.TrimSpace(*in.Name) == "" {
		return nil, gocommerce.Validationf("a company needs a name")
	}
	if err := m.validateCompany(ctx, in); err != nil {
		return nil, err
	}
	sets, args := []string{}, []any{id}
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
	}
	if in.Code != nil {
		set("code", strings.TrimSpace(*in.Code))
	}
	if in.Name != nil {
		set("name", strings.TrimSpace(*in.Name))
	}
	if in.TaxID != nil {
		set("tax_id", strings.TrimSpace(*in.TaxID))
	}
	if in.Status != nil {
		set("status", *in.Status)
	}
	if in.GroupID.Set {
		set("group_id", in.GroupID.Value)
	}
	if in.CreditLimitMinor.Set {
		set("credit_limit_minor", in.CreditLimitMinor.Value)
	}
	if in.NetDays != nil {
		set("net_days", *in.NetDays)
	}
	if in.ApprovalThresholdMinor.Set {
		set("approval_threshold_minor", in.ApprovalThresholdMinor.Value)
	}
	if in.RequirePO != nil {
		set("require_po", *in.RequirePO)
	}
	if in.Notes != nil {
		set("notes", *in.Notes)
	}
	if in.Metadata != nil {
		meta, err := json.Marshal(in.Metadata)
		if err != nil {
			return nil, gocommerce.Validationf("metadata is not valid JSON: %v", err)
		}
		set("metadata", meta)
	}
	if len(sets) == 0 {
		return before, nil
	}
	if _, err := m.db.ExecContext(ctx, `UPDATE b2b_companies SET `+strings.Join(sets, ", ")+
		`, updated_at = now() WHERE id = $1`, args...); err != nil {
		return nil, translateErr(err)
	}
	after, err := m.Company(ctx, id)
	if err != nil {
		return nil, err
	}
	if !sameID(before.GroupID, after.GroupID) {
		if err := m.moveGroup(ctx, id, before.GroupID, after.GroupID); err != nil {
			return nil, err
		}
	}
	return after, nil
}

// DeleteCompany removes a company and takes its buyers' addresses out of its
// group. A company with orders on its books is refused: the ledger would lose
// whose orders they were.
func (m *Module) DeleteCompany(ctx context.Context, id int64) error {
	c, err := m.Company(ctx, id)
	if err != nil {
		return err
	}
	var orders int
	if err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM b2b_orders WHERE company_id = $1`, id).Scan(&orders); err != nil {
		return err
	}
	if orders > 0 {
		return gocommerce.Conflictf("%s has %d order(s) on its books; close it instead of deleting it", c.Name, orders)
	}
	members, err := m.Members(ctx, id)
	if err != nil {
		return err
	}
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		// A dealer's leads go back to the store rather than vanish with it:
		// the customer still asked, and somebody still has to answer.
		if _, err := tx.ExecContext(ctx, `
			UPDATE b2b_leads SET company_id = NULL, routed_by = 'unrouted', updated_at = now()
			WHERE company_id = $1`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM b2b_companies WHERE id = $1`, id)
		return err
	})
	if err != nil {
		return err
	}
	if c.GroupID != nil {
		for _, mem := range members {
			if mem.Email == "" {
				continue
			}
			if err := m.app.Pricing().RemoveMember(ctx, *c.GroupID, mem.Email); err != nil {
				m.app.Log().Warn("b2b: could not take an address out of a deleted company's group",
					"company", id, "error", err)
			}
		}
	}
	return nil
}

func (m *Module) validateCompany(ctx context.Context, in CompanyInput) error {
	if in.Code != nil {
		if code := strings.TrimSpace(*in.Code); code != "" && handleize(code) != code {
			return gocommerce.Validationf("a company code is lower-case letters, digits and single hyphens, like %q", handleize(code))
		}
	}
	if in.Status != nil {
		switch *in.Status {
		case StatusActive, StatusOnHold, StatusClosed:
		default:
			return gocommerce.Validationf("status must be active, on_hold or closed")
		}
	}
	if in.NetDays != nil && (*in.NetDays < 0 || *in.NetDays > 365) {
		return gocommerce.Validationf("net_days runs from 0 to 365")
	}
	for name, v := range map[string]optionalInt64{
		"credit_limit_minor": in.CreditLimitMinor, "approval_threshold_minor": in.ApprovalThresholdMinor,
	} {
		if v.Value != nil && *v.Value < 0 {
			return gocommerce.Validationf("%s must not be negative", name)
		}
	}
	if in.GroupID.Value != nil {
		if _, err := m.app.Pricing().Group(ctx, *in.GroupID.Value); err != nil {
			return gocommerce.Validationf("customer group %d does not exist", *in.GroupID.Value)
		}
	}
	return nil
}

// ------------------------------------------------------------------ members

// Members lists a company's buyers, names read from their accounts.
func (m *Module) Members(ctx context.Context, companyID int64) ([]*Member, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT company_id, customer_id, email, role, created_at
		FROM b2b_members WHERE company_id = $1 ORDER BY created_at, customer_id`, companyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Member{}
	for rows.Next() {
		mem := &Member{}
		if err := rows.Scan(&mem.CompanyID, &mem.CustomerID, &mem.Email, &mem.Role, &mem.CreatedAt); err != nil {
			return nil, err
		}
		mem.Confirmed = mem.Email != ""
		out = append(out, mem)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, mem := range out {
		if acct, err := m.accounts.CustomerByID(ctx, mem.CustomerID); err == nil {
			mem.Name = acct.Name
		}
	}
	return out, nil
}

// MemberOf answers which company an account buys for, and as what.
func (m *Module) MemberOf(ctx context.Context, customerID int64) (*Member, error) {
	return m.memberWhere(ctx, m.db, `customer_id = $1`, customerID)
}

func (m *Module) memberWhere(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, where string, arg any) (*Member, error) {
	mem := &Member{}
	err := q.QueryRowContext(ctx, `
		SELECT company_id, customer_id, email, role, created_at
		FROM b2b_members WHERE `+where, arg).
		Scan(&mem.CompanyID, &mem.CustomerID, &mem.Email, &mem.Role, &mem.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("that account is not a buyer for any company")
	}
	mem.Confirmed = mem.Email != ""
	return mem, err
}

func validRole(role string) bool {
	switch role {
	case RoleAdmin, RoleApprover, RoleBuyer:
		return true
	}
	return false
}

// addMember makes an account a buyer. The address must already be proven —
// by identity's own confirmation or by the invitation this is accepting —
// because it is about to be given the company's prices.
func (m *Module) addMember(ctx context.Context, companyID, customerID int64, email, role string) (*Member, error) {
	if !validRole(role) {
		return nil, gocommerce.Validationf("role must be admin, approver or buyer")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	_, err := m.db.ExecContext(ctx, `
		INSERT INTO b2b_members (customer_id, company_id, email, role) VALUES ($1, $2, $3, $4)`,
		customerID, companyID, email, role)
	if err != nil {
		if isUnique(err) {
			if existing, lookErr := m.MemberOf(ctx, customerID); lookErr == nil && existing.CompanyID == companyID {
				return existing, nil
			}
			return nil, gocommerce.Conflictf("that account already buys for another company")
		}
		return nil, translateErr(err)
	}
	c, err := m.Company(ctx, companyID)
	if err != nil {
		return nil, err
	}
	if c.GroupID != nil {
		if err := m.app.Pricing().AddMember(ctx, *c.GroupID, email); err != nil {
			return nil, err
		}
	}
	mem, err := m.MemberOf(ctx, customerID)
	if err != nil {
		return nil, err
	}
	if acct, err := m.accounts.CustomerByID(ctx, customerID); err == nil {
		mem.Name = acct.Name
	}
	return mem, nil
}

// SetRole changes a buyer's role. The last admin cannot be demoted: a company
// nobody can administer is one only the store can rescue.
func (m *Module) SetRole(ctx context.Context, companyID, customerID int64, role string) (*Member, error) {
	if !validRole(role) {
		return nil, gocommerce.Validationf("role must be admin, approver or buyer")
	}
	mem, err := m.MemberOf(ctx, customerID)
	if err != nil || mem.CompanyID != companyID {
		return nil, gocommerce.NotFoundf("that account is not a buyer for this company")
	}
	if mem.Role == RoleAdmin && role != RoleAdmin {
		if err := m.refuseLastAdmin(ctx, companyID); err != nil {
			return nil, err
		}
	}
	if _, err := m.db.ExecContext(ctx,
		`UPDATE b2b_members SET role = $2 WHERE customer_id = $1`, customerID, role); err != nil {
		return nil, err
	}
	return m.MemberOf(ctx, customerID)
}

// RemoveMember takes a buyer out of a company, and their address out of its
// group so their next cart prices as anybody's.
func (m *Module) RemoveMember(ctx context.Context, companyID, customerID int64) error {
	mem, err := m.MemberOf(ctx, customerID)
	if err != nil || mem.CompanyID != companyID {
		return gocommerce.NotFoundf("that account is not a buyer for this company")
	}
	if mem.Role == RoleAdmin {
		if err := m.refuseLastAdmin(ctx, companyID); err != nil {
			return err
		}
	}
	return m.dropMember(ctx, mem)
}

func (m *Module) dropMember(ctx context.Context, mem *Member) error {
	if _, err := m.db.ExecContext(ctx,
		`DELETE FROM b2b_members WHERE customer_id = $1`, mem.CustomerID); err != nil {
		return err
	}
	c, err := m.Company(ctx, mem.CompanyID)
	if err != nil {
		return err
	}
	if c.GroupID != nil && mem.Email != "" {
		return m.app.Pricing().RemoveMember(ctx, *c.GroupID, mem.Email)
	}
	return nil
}

func (m *Module) refuseLastAdmin(ctx context.Context, companyID int64) error {
	var admins int
	if err := m.db.QueryRowContext(ctx,
		`SELECT count(*) FROM b2b_members WHERE company_id = $1 AND role = 'admin'`, companyID).Scan(&admins); err != nil {
		return err
	}
	if admins <= 1 {
		return gocommerce.Conflictf("a company needs at least one admin; make somebody else admin first")
	}
	return nil
}

// moveGroup re-files every buyer's address when a company changes group.
func (m *Module) moveGroup(ctx context.Context, companyID int64, from, to *int64) error {
	members, err := m.Members(ctx, companyID)
	if err != nil {
		return err
	}
	for _, mem := range members {
		if mem.Email == "" {
			continue
		}
		if from != nil {
			if err := m.app.Pricing().RemoveMember(ctx, *from, mem.Email); err != nil {
				return err
			}
		}
		if to != nil {
			if err := m.app.Pricing().AddMember(ctx, *to, mem.Email); err != nil {
				return err
			}
		}
	}
	return nil
}

// syncMember brings a buyer's group membership in line with their account
// as it is now. An account that changed its address must not leave the old
// one in the group — whoever holds that mailbox next could confirm it and buy
// on the company's terms — and its new address joins only once confirmed.
//
// b2b_members.email is the address this module put into the group, or empty
// while there is none: an account that moved to an address it has not yet
// confirmed. Recording the new address before it was confirmed would leave it
// out of the group for good — the two would already agree, and nothing would
// ever add it.
func (m *Module) syncMember(ctx context.Context, mem *Member, email string, verified bool) error {
	email = strings.ToLower(email)
	want := ""
	if verified {
		want = email
	}
	if mem.Email == want {
		return nil
	}
	c, err := m.Company(ctx, mem.CompanyID)
	if err != nil {
		return err
	}
	if c.GroupID != nil {
		if mem.Email != "" {
			if err := m.app.Pricing().RemoveMember(ctx, *c.GroupID, mem.Email); err != nil {
				return err
			}
		}
		if want != "" {
			if err := m.app.Pricing().AddMember(ctx, *c.GroupID, want); err != nil {
				return err
			}
		}
	}
	if _, err := m.db.ExecContext(ctx,
		`UPDATE b2b_members SET email = $2 WHERE customer_id = $1`, mem.CustomerID, want); err != nil {
		return err
	}
	mem.Email = want
	return nil
}

// reconcile walks every buyer and asks identity about them: a deleted account
// leaves the company and its address leaves the group, and a changed address
// is re-filed. Identity announces neither, so this is how they are noticed.
func (m *Module) reconcile(ctx context.Context) error {
	rows, err := m.db.QueryContext(ctx,
		`SELECT company_id, customer_id, email, role, created_at FROM b2b_members`)
	if err != nil {
		return err
	}
	var members []*Member
	for rows.Next() {
		mem := &Member{}
		if err := rows.Scan(&mem.CompanyID, &mem.CustomerID, &mem.Email, &mem.Role, &mem.CreatedAt); err != nil {
			rows.Close()
			return err
		}
		members = append(members, mem)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, mem := range members {
		acct, err := m.accounts.CustomerByID(ctx, mem.CustomerID)
		if err != nil {
			var apiErr *gocommerce.APIError
			if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
				if err := m.dropMember(ctx, mem); err != nil {
					return err
				}
				continue
			}
			return err
		}
		if err := m.syncMember(ctx, mem, acct.Email, acct.EmailVerified); err != nil {
			return err
		}
	}
	if err := m.reconcileOrders(ctx); err != nil {
		return err
	}
	if err := m.reconcilePartials(ctx); err != nil {
		return err
	}
	return m.reconcileApprovals(ctx)
}

// -------------------------------------------------------------- invitations

func newToken() (string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	return tok, hashToken(tok), nil
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// Invite asks an address to join a company, by email. by names who asked,
// for the invitation's own record and the email's wording.
func (m *Module) Invite(ctx context.Context, companyID int64, email, role, by string) (*Invitation, error) {
	if !validRole(role) {
		return nil, gocommerce.Validationf("role must be admin, approver or buyer")
	}
	email = strings.ToLower(strings.TrimSpace(email))
	if !strings.Contains(email, "@") {
		return nil, gocommerce.Validationf("a valid email is required")
	}
	c, err := m.Company(ctx, companyID)
	if err != nil {
		return nil, err
	}
	tok, hash, err := newToken()
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(m.cfg.InviteTTL)
	var id int64
	// An address has one open invitation per company: a second replaces the
	// first, so only the newest link in somebody's inbox works.
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM b2b_invitations
			WHERE company_id = $1 AND email = $2 AND accepted_at IS NULL`, companyID, email); err != nil {
			return err
		}
		return tx.QueryRowContext(ctx, `
			INSERT INTO b2b_invitations (company_id, email, role, token_hash, invited_by, expires_at)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			companyID, email, role, hash, by, expires).Scan(&id)
	})
	if err != nil {
		return nil, err
	}
	m.notifyInvitation(ctx, c, email, role, by, tok)
	return m.invitation(ctx, id)
}

func (m *Module) invitation(ctx context.Context, id int64) (*Invitation, error) {
	inv := &Invitation{}
	var accepted sql.NullTime
	err := m.db.QueryRowContext(ctx, `
		SELECT id, company_id, email, role, invited_by, expires_at, accepted_at, created_at
		FROM b2b_invitations WHERE id = $1`, id).
		Scan(&inv.ID, &inv.CompanyID, &inv.Email, &inv.Role, &inv.InvitedBy, &inv.ExpiresAt, &accepted, &inv.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("invitation %d does not exist", id)
	}
	if accepted.Valid {
		at := accepted.Time
		inv.AcceptedAt = &at
	}
	return inv, err
}

// Invitations lists a company's open invitations.
func (m *Module) Invitations(ctx context.Context, companyID int64) ([]*Invitation, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id FROM b2b_invitations
		WHERE company_id = $1 AND accepted_at IS NULL AND expires_at > now()
		ORDER BY id DESC`, companyID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []*Invitation{}
	for _, id := range ids {
		inv, err := m.invitation(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, inv)
	}
	return out, rows.Err()
}

// RevokeInvitation withdraws an open invitation.
func (m *Module) RevokeInvitation(ctx context.Context, companyID, id int64) error {
	res, err := m.db.ExecContext(ctx, `
		DELETE FROM b2b_invitations WHERE id = $1 AND company_id = $2 AND accepted_at IS NULL`, id, companyID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return gocommerce.NotFoundf("no open invitation %d for this company", id)
	}
	return nil
}

// AcceptInvitation joins the signed-in account to the company that invited
// its address. The token arrived in that mailbox, so accepting it is proof
// the account reads it — identity is told so, which is what lets the
// account's carts be priced as the company's (D66).
func (m *Module) AcceptInvitation(ctx context.Context, customerID int64, token string) (*Member, error) {
	acct, err := m.accounts.CustomerByID(ctx, customerID)
	if err != nil {
		return nil, err
	}
	inv := &Invitation{}
	err = m.db.QueryRowContext(ctx, `
		SELECT id, company_id, email, role FROM b2b_invitations
		WHERE token_hash = $1 AND accepted_at IS NULL AND expires_at > now()`, hashToken(strings.TrimSpace(token))).
		Scan(&inv.ID, &inv.CompanyID, &inv.Email, &inv.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &gocommerce.APIError{Status: http.StatusBadRequest, Code: "invalid_token",
			Message: "that invitation is not valid; it may have expired or already been used"}
	}
	if err != nil {
		return nil, err
	}
	// The invitation was for an address, not for whoever holds the link. An
	// account under another address accepting it would be the forwarded-email
	// hole the whole verification exists to close.
	if strings.ToLower(acct.Email) != inv.Email {
		return nil, gocommerce.Forbiddenf("this invitation was sent to another address; sign in as %s to accept it",
			m.app.MaskEmail(inv.Email))
	}
	if _, err := m.accounts.MarkEmailVerified(ctx, customerID, inv.Email); err != nil {
		return nil, err
	}
	mem, err := m.addMember(ctx, inv.CompanyID, customerID, inv.Email, inv.Role)
	if err != nil {
		return nil, err
	}
	if _, err := m.db.ExecContext(ctx,
		`UPDATE b2b_invitations SET accepted_at = now() WHERE id = $1`, inv.ID); err != nil {
		return nil, err
	}
	return mem, nil
}

// AddOrInvite is the operator's way to put somebody in a company. An account
// whose address identity has already confirmed joins at once; anybody else
// is invited, because the address has not been proven yet.
func (m *Module) AddOrInvite(ctx context.Context, companyID int64, email, role, by string) (*Member, *Invitation, error) {
	if _, err := m.Company(ctx, companyID); err != nil {
		return nil, nil, err
	}
	if !validRole(role) {
		return nil, nil, gocommerce.Validationf("role must be admin, approver or buyer")
	}
	if acct, err := m.accounts.CustomerByEmail(ctx, email); err == nil && acct.EmailVerified {
		mem, err := m.addMember(ctx, companyID, acct.ID, acct.Email, role)
		return mem, nil, err
	}
	inv, err := m.Invite(ctx, companyID, email, role, by)
	return nil, inv, err
}

// ------------------------------------------------------------------ helpers

func handleize(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	return strings.TrimRight(b.String(), "-")
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func nonNil(md gocommerce.Metadata) gocommerce.Metadata {
	if md == nil {
		return gocommerce.Metadata{}
	}
	return md
}

// sameID compares two optional ids, absent equalling only absent.
func sameID(a, b *int64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func isUnique(err error) bool {
	return err != nil && strings.Contains(err.Error(), "SQLSTATE 23505")
}

func translateErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "b2b_companies_code_key"):
		return gocommerce.Conflictf("a company with that code already exists")
	case strings.Contains(msg, "b2b_companies_code_check"):
		return gocommerce.Validationf("a company code is lower-case letters, digits and single hyphens")
	case strings.Contains(msg, "b2b_companies_name_check"):
		return gocommerce.Validationf("a company needs a name")
	}
	return err
}
