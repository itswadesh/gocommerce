package gocommerce

import (
	"context"
	"database/sql"
	"errors"
)

// A vendor's own login.
//
// Deliberately the same table as every other operator: the same password
// hashing, the same sessions, the same rights machinery, the same password
// reset. A second identity system beside `superusers` would be a second place
// for a login bug to live, and login bugs are the expensive kind.
//
// What makes a vendor account different is not how it authenticates but what it
// can see, and that is a scope rather than a right — see VendorScopeOf.

// CreateForVendor opens an account belonging to one seller.
//
// A separate method rather than another argument on Create, because the role
// and the attachment are not independent: a vendor account without a vendor is
// the row every scoping rule fails open on, and a signature that lets a caller
// supply one without the other invites exactly that. The database refuses it
// too (M48), which is the backstop rather than the rule.
func (s *Superusers) CreateForVendor(ctx context.Context, vendorID int64, email, password string) (*Superuser, error) {
	if err := validateCredentials(email, password); err != nil {
		return nil, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return nil, Internalf(err, "hash password")
	}

	var su *Superuser
	err = InTx(ctx, s.db, func(tx *sql.Tx) error {
		// Read the seller inside the transaction so the account cannot be
		// attached to one that is being deleted as this runs, and so the
		// refusal names the vendor rather than a foreign key.
		var name string
		err := tx.QueryRowContext(ctx, `SELECT name FROM vendors WHERE id = $1`, vendorID).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("vendor %d does not exist", vendorID)
		}
		if err != nil {
			return Internalf(err, "read vendor")
		}

		row := tx.QueryRowContext(ctx, `
			INSERT INTO superusers (email, password_hash, role, vendor_id)
			VALUES ($1, $2, $3, $4)
			RETURNING `+superuserColumns, normalizeEmail(email), hash, RoleVendor, vendorID)
		var serr error
		if su, serr = scanSuperuser(row); serr != nil {
			if isUniqueViolation(serr) {
				return Conflictf("a superuser with email %q already exists", normalizeEmail(email))
			}
			return Internalf(serr, "create vendor account")
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditSuperuserCreate, Entity: AuditEntitySuperuser,
			ID: su.ID, Label: su.Email,
			Summary: "Added " + su.Email + " as a login for " + name,
			After:   map[string]any{"email": su.Email, "role": su.Role, "vendor_id": vendorID},
		})
	})
	if err != nil {
		return nil, err
	}
	su.Rights = DefaultRightsOf(su.Role)
	return su, nil
}

// Get returns one operator by id.
func (s *Superusers) Get(ctx context.Context, id int64) (*Superuser, error) {
	su, err := scanSuperuser(s.db.QueryRowContext(ctx,
		`SELECT `+superuserColumns+` FROM superusers WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("superuser %d not found", id)
	}
	if err != nil {
		return nil, Internalf(err, "read superuser")
	}
	rights, err := s.app.Roles().Of(ctx, su.Role)
	if err != nil {
		return nil, err
	}
	su.Rights = rights
	return su, nil
}

// WithSuperuser puts an operator on a context.
//
// The request path has its own (withSuperuser, on an *http.Request). This is
// the same thing for callers that have a context and no request: the engine’s own
// background work, and the tests that check what a scope resolves to without
// standing up an HTTP round trip.
func WithSuperuser(ctx context.Context, su *Superuser) context.Context {
	return context.WithValue(ctx, ctxKeySuperuser, su)
}

// VendorScopeOf returns the seller this caller is limited to, or nil for the
// whole store.
//
// The one place that decides. Every listing that can carry a vendor's rows asks
// this and narrows, so "which rows may you see" has a single answer rather than
// one per query — a rule copied into thirty places is a rule that is wrong in
// at least one of them, and the wrong one leaks a competitor's data.
//
// Nil means unscoped, and that is right for all three of the callers that get
// it: an owner, a static admin token, and the engine's own background work. A
// vendor account is the only thing in this engine that is not trusted with the
// whole store, which is exactly why it is the only thing that returns non-nil.
func VendorScopeOf(ctx context.Context) *int64 {
	su := SuperuserFrom(ctx)
	if su == nil {
		return nil
	}
	// Both halves, not either: the role without the attachment is the state
	// M48's CHECK exists to make unreachable, and if it is ever reached anyway
	// this must not fall back to "see everything".
	if su.Role != RoleVendor || su.VendorID == nil {
		return nil
	}
	id := *su.VendorID
	return &id
}

// IsVendorAccount reports whether this caller is a seller rather than somebody
// running the store.
//
// Separate from VendorScopeOf because a vendor whose attachment is somehow
// missing must still be refused rather than treated as an owner: the scope
// answers "whose rows", this answers "are you staff", and the unsafe direction
// is different for each.
func IsVendorAccount(ctx context.Context) bool {
	su := SuperuserFrom(ctx)
	return su != nil && su.Role == RoleVendor
}
