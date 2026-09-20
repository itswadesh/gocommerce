package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
)

// Vendors are the sellers on this store.
//
// Not brands. `products.vendor` is free text holding "Anker" or "Amazon
// Essentials" — the manufacturer, which is what the Google feed sends as
// `brand` — and it is untouched by any of this. A brand is who made the thing;
// a vendor is who is selling it here. On a single-merchant store there is one
// vendor and it is the shop itself, which is why none of this is required to
// sell anything: the variant's own price and stock are the store's own offer.
//
// See M47 for the shape and for what it deliberately does not do yet.
type Vendors struct {
	app *App
}

// Vendors returns the vendor service.
func (a *App) Vendors() *Vendors { return &Vendors{app: a} }

// VendorStatus is where a seller is in the moderation the marketplace does.
type VendorStatus string

const (
	// VendorPending is a seller who has arrived and not been let in. It is the
	// default because a marketplace that lists whoever signs up is not doing
	// moderation, it is hosting whatever turns up.
	VendorPending VendorStatus = "pending"
	// VendorApproved is a seller the storefront will show.
	VendorApproved VendorStatus = "approved"
	// VendorSuspended keeps the row, the history and the offers, and stops
	// showing any of it. Deleting would take the history with it.
	VendorSuspended VendorStatus = "suspended"
)

func validVendorStatus(s VendorStatus) bool {
	switch s {
	case VendorPending, VendorApproved, VendorSuspended:
		return true
	}
	return false
}

// OfferStatus is whether a seller is currently offering the thing.
type OfferStatus string

const (
	OfferActive OfferStatus = "active"
	// OfferPaused is "not right now" — a seller out of stock for a fortnight
	// keeps the price and the row rather than retyping both later.
	OfferPaused OfferStatus = "paused"
)

func validOfferStatus(s OfferStatus) bool {
	return s == OfferActive || s == OfferPaused
}

// Vendor is a seller.
type Vendor struct {
	ID          int64         `json:"id"`
	Slug        string        `json:"slug"`
	Name        string        `json:"name"`
	LegalName   string        `json:"legal_name"`
	Email       string        `json:"email"`
	Phone       string        `json:"phone"`
	Website     string        `json:"website"`
	About       string        `json:"about"`
	LogoMediaID *int64        `json:"logo_media_id"`
	Address     VendorAddress `json:"address"`
	TaxID       string        `json:"tax_id"`
	Status      VendorStatus  `json:"status"`
	// CommissionBP is what this store takes, in basis points: 250 is 2.5%.
	// Recorded, not settled — see M47 and PLAN.md §39.10.
	CommissionBP int      `json:"commission_bp"`
	Metadata     Metadata `json:"metadata"`
	CreatedAt    string   `json:"created_at"`
	UpdatedAt    string   `json:"updated_at"`
}

// VendorAddress is where the seller trades from, for the invoice and for tax.
type VendorAddress struct {
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

// Offer is one seller's price and stock for one variant.
type Offer struct {
	ID             int64       `json:"id"`
	VendorID       int64       `json:"vendor_id"`
	VariantID      int64       `json:"variant_id"`
	Price          Money       `json:"price"`
	PriceMinor     int64       `json:"-"`
	StockOnHand    int         `json:"stock_on_hand"`
	StockReserved  int         `json:"stock_reserved"`
	Available      int         `json:"available"`
	TrackInventory bool        `json:"track_inventory"`
	Status         OfferStatus `json:"status"`
	Metadata       Metadata    `json:"metadata"`
	CreatedAt      string      `json:"created_at"`
	UpdatedAt      string      `json:"updated_at"`
}

// VendorInput creates a seller.
type VendorInput struct {
	Slug         string        `json:"slug"`
	Name         string        `json:"name"`
	LegalName    string        `json:"legal_name"`
	Email        string        `json:"email"`
	Phone        string        `json:"phone"`
	Website      string        `json:"website"`
	About        string        `json:"about"`
	LogoMediaID  *int64        `json:"logo_media_id"`
	Address      VendorAddress `json:"address"`
	TaxID        string        `json:"tax_id"`
	Status       VendorStatus  `json:"status"`
	CommissionBP int           `json:"commission_bp"`
	Metadata     Metadata      `json:"metadata"`
}

// VendorPatch edits one. Every field is a pointer for the usual reason: an
// absent field is "leave it", and an empty string is "make it empty".
type VendorPatch struct {
	Name         *string        `json:"name"`
	LegalName    *string        `json:"legal_name"`
	Email        *string        `json:"email"`
	Phone        *string        `json:"phone"`
	Website      *string        `json:"website"`
	About        *string        `json:"about"`
	LogoMediaID  *int64         `json:"logo_media_id"`
	Address      *VendorAddress `json:"address"`
	TaxID        *string        `json:"tax_id"`
	Status       *VendorStatus  `json:"status"`
	CommissionBP *int           `json:"commission_bp"`
	Metadata     *Metadata      `json:"metadata"`
}

// OfferInput sets what a seller charges for a variant.
type OfferInput struct {
	VariantID      int64       `json:"variant_id"`
	PriceMinor     int64       `json:"price_minor"`
	StockOnHand    int         `json:"stock_on_hand"`
	TrackInventory *bool       `json:"track_inventory"`
	Status         OfferStatus `json:"status"`
	Metadata       Metadata    `json:"metadata"`
}

// VendorQuery filters a listing.
type VendorQuery struct {
	Search string
	Status VendorStatus
	Limit  int
	Offset int
	// OnlyID narrows the listing to one seller, which is all a seller may see
	// of this table. Nil is the whole store.
	//
	// A filter on the query rather than a slice of the result, so the count is
	// narrowed with the rows: a total that still says 40 tells a seller how
	// many competitors they have, whatever the page shows them.
	OnlyID *int64
}

const vendorColumns = `id, slug, name, legal_name, email, phone, website, about,
	logo_media_id, address_line1, address_line2, city, state, postal_code, country,
	tax_id, status, commission_bp, metadata, created_at, updated_at`

func scanVendor(row interface{ Scan(...any) error }) (*Vendor, error) {
	var v Vendor
	var meta []byte
	if err := row.Scan(
		&v.ID, &v.Slug, &v.Name, &v.LegalName, &v.Email, &v.Phone, &v.Website, &v.About,
		&v.LogoMediaID, &v.Address.Line1, &v.Address.Line2, &v.Address.City, &v.Address.State,
		&v.Address.PostalCode, &v.Address.Country, &v.TaxID, &v.Status, &v.CommissionBP,
		&meta, &v.CreatedAt, &v.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := scanMetadata(meta, &v.Metadata); err != nil {
		return nil, err
	}
	return &v, nil
}

func (s *Vendors) validate(in *VendorInput) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return Validationf("name is required")
	}
	if in.Slug = strings.TrimSpace(in.Slug); in.Slug == "" {
		in.Slug = slugify(in.Name)
	}
	if in.Slug == "" {
		return Validationf("slug could not be derived from the name; supply one")
	}
	if in.Status == "" {
		in.Status = VendorPending
	}
	if !validVendorStatus(in.Status) {
		return Validationf("status must be pending, approved or suspended")
	}
	// Basis points of a sale, so a hundred per cent is the ceiling. Somebody
	// typing 150 means 1.5% and the database CHECK would refuse it anyway —
	// this says why.
	if in.CommissionBP < 0 || in.CommissionBP > 10000 {
		return Validationf("commission_bp must be between 0 and 10000 (10000 is 100%%)")
	}
	return nil
}

// Create adds a seller. They arrive pending unless told otherwise.
func (s *Vendors) Create(ctx context.Context, in VendorInput) (*Vendor, error) {
	if err := s.validate(&in); err != nil {
		return nil, err
	}
	meta, err := in.Metadata.value()
	if err != nil {
		return nil, Validationf("metadata is not valid JSON: %v", err)
	}

	var out *Vendor
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		v, err := scanVendor(tx.QueryRowContext(ctx, `
			INSERT INTO vendors (slug, name, legal_name, email, phone, website, about,
			                     logo_media_id, address_line1, address_line2, city, state,
			                     postal_code, country, tax_id, status, commission_bp, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
			RETURNING `+vendorColumns,
			in.Slug, in.Name, in.LegalName, in.Email, in.Phone, in.Website, in.About,
			in.LogoMediaID, in.Address.Line1, in.Address.Line2, in.Address.City, in.Address.State,
			in.Address.PostalCode, in.Address.Country, in.TaxID, in.Status, in.CommissionBP, meta,
		))
		if err != nil {
			if isUniqueViolation(err) {
				return Conflictf("a vendor with the slug %q already exists", in.Slug)
			}
			return Internalf(err, "create vendor")
		}
		out = v
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditVendorCreate, Entity: AuditEntityVendor,
			ID: v.ID, Label: v.Name, Summary: "Added the vendor " + v.Name,
			After: map[string]any{
				"slug": v.Slug, "name": v.Name, "status": string(v.Status),
				"commission_bp": v.CommissionBP,
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Get returns one seller by id.
func (s *Vendors) Get(ctx context.Context, id int64) (*Vendor, error) {
	return s.one(ctx, "id = $1", id)
}

// GetBySlug returns one seller by the handle in its URL.
func (s *Vendors) GetBySlug(ctx context.Context, slug string) (*Vendor, error) {
	return s.one(ctx, "slug = $1", slug)
}

func (s *Vendors) one(ctx context.Context, where string, arg any) (*Vendor, error) {
	v, err := scanVendor(s.app.db.QueryRowContext(ctx,
		`SELECT `+vendorColumns+` FROM vendors WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("vendor not found")
	}
	if err != nil {
		return nil, Internalf(err, "read vendor")
	}
	return v, nil
}

// List returns sellers, newest first, filtered by status and by name.
func (s *Vendors) List(ctx context.Context, q VendorQuery) ([]*Vendor, int, error) {
	limit := q.Limit
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}

	where := []string{"true"}
	args := []any{}
	if q.OnlyID != nil {
		args = append(args, *q.OnlyID)
		where = append(where, "id = $"+strconv.Itoa(len(args)))
	}
	if q.Status != "" {
		if !validVendorStatus(q.Status) {
			return nil, 0, Validationf("status must be pending, approved or suspended")
		}
		args = append(args, string(q.Status))
		where = append(where, "status = $"+strconv.Itoa(len(args)))
	}
	if term := strings.TrimSpace(q.Search); term != "" {
		args = append(args, "%"+term+"%")
		// Both sides folded by PostgreSQL rather than one by each, for the
		// reason the category search gives: Go's ToLower and this cluster's
		// lower() disagree about accented capitals.
		where = append(where, "(lower(name) LIKE lower($"+strconv.Itoa(len(args))+
			") OR lower(slug) LIKE lower($"+strconv.Itoa(len(args))+"))")
	}

	clause := strings.Join(where, " AND ")
	var total int
	if err := s.app.db.QueryRowContext(ctx,
		`SELECT count(*) FROM vendors WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, Internalf(err, "count vendors")
	}

	args = append(args, limit, q.Offset)
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT `+vendorColumns+` FROM vendors WHERE `+clause+
			` ORDER BY id DESC LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, Internalf(err, "list vendors")
	}
	defer rows.Close()

	out := []*Vendor{}
	for rows.Next() {
		v, err := scanVendor(rows)
		if err != nil {
			return nil, 0, Internalf(err, "scan vendor")
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, Internalf(err, "list vendors")
	}
	return out, total, nil
}

// Update edits a seller.
//
// The slug is not in the patch on purpose. A handle is a URL somebody has
// linked to and a storefront has cached; renaming the shop should not break it,
// and an operator who really wants a new one can say so through a route that
// makes the consequence explicit rather than as a side effect of a rename.
func (s *Vendors) Update(ctx context.Context, id int64, patch VendorPatch) (*Vendor, error) {
	sets, args := []string{}, []any{}
	after := map[string]any{}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
		after[col] = v
	}

	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return nil, Validationf("name must not be empty")
		}
		add("name", name)
	}
	if patch.LegalName != nil {
		add("legal_name", strings.TrimSpace(*patch.LegalName))
	}
	if patch.Email != nil {
		add("email", strings.TrimSpace(*patch.Email))
	}
	if patch.Phone != nil {
		add("phone", strings.TrimSpace(*patch.Phone))
	}
	if patch.Website != nil {
		add("website", strings.TrimSpace(*patch.Website))
	}
	if patch.About != nil {
		add("about", *patch.About)
	}
	if patch.LogoMediaID != nil {
		add("logo_media_id", *patch.LogoMediaID)
	}
	if patch.TaxID != nil {
		add("tax_id", strings.TrimSpace(*patch.TaxID))
	}
	if patch.Address != nil {
		add("address_line1", patch.Address.Line1)
		add("address_line2", patch.Address.Line2)
		add("city", patch.Address.City)
		add("state", patch.Address.State)
		add("postal_code", patch.Address.PostalCode)
		add("country", patch.Address.Country)
	}
	if patch.Status != nil {
		if !validVendorStatus(*patch.Status) {
			return nil, Validationf("status must be pending, approved or suspended")
		}
		add("status", string(*patch.Status))
	}
	if patch.CommissionBP != nil {
		if *patch.CommissionBP < 0 || *patch.CommissionBP > 10000 {
			return nil, Validationf("commission_bp must be between 0 and 10000 (10000 is 100%%)")
		}
		add("commission_bp", *patch.CommissionBP)
	}
	if patch.Metadata != nil {
		meta, err := patch.Metadata.value()
		if err != nil {
			return nil, Validationf("metadata is not valid JSON: %v", err)
		}
		add("metadata", meta)
	}
	if len(sets) == 0 {
		return s.Get(ctx, id)
	}
	sets = append(sets, "updated_at = now()")
	args = append(args, id)

	var out *Vendor
	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		// Read first, under the lock the UPDATE takes anyway, so the audit row
		// can say what the value used to be. "Who approved this seller, and what
		// were they before" is the question this trail exists for.
		before := map[string]any{}
		var name, status string
		var commission int
		err := tx.QueryRowContext(ctx,
			`SELECT name, status, commission_bp FROM vendors WHERE id = $1 FOR UPDATE`, id,
		).Scan(&name, &status, &commission)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("vendor %d not found", id)
		}
		if err != nil {
			return Internalf(err, "read vendor")
		}
		for col, was := range map[string]any{
			"name": name, "status": status, "commission_bp": commission,
		} {
			if _, changed := after[col]; changed {
				before[col] = was
			}
		}

		v, err := scanVendor(tx.QueryRowContext(ctx,
			`UPDATE vendors SET `+strings.Join(sets, ", ")+
				` WHERE id = $`+strconv.Itoa(len(args))+` RETURNING `+vendorColumns, args...))
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("vendor %d not found", id)
		}
		if err != nil {
			if isUniqueViolation(err) {
				return Conflictf("that slug is already used")
			}
			return Internalf(err, "update vendor")
		}
		out = v
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditVendorUpdate, Entity: AuditEntityVendor,
			ID: v.ID, Label: v.Name, Summary: "Edited the vendor " + v.Name,
			Before: before, After: after,
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete removes a seller and, by cascade, everything they were offering.
//
// Not a refusal-while-offers-exist: an operator removing a seller would then
// have to hand-delete their catalogue first, and the offers are worth nothing
// without the seller anyway. Suspending is the way to stop somebody selling
// while keeping the record.
func (s *Vendors) Delete(ctx context.Context, id int64) error {
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var name string
		var offers int
		err := tx.QueryRowContext(ctx, `
			SELECT v.name, (SELECT count(*) FROM vendor_offers o WHERE o.vendor_id = v.id)
			FROM vendors v WHERE v.id = $1`, id).Scan(&name, &offers)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("vendor %d not found", id)
		}
		if err != nil {
			return Internalf(err, "read vendor")
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM vendors WHERE id = $1`, id); err != nil {
			return Internalf(err, "delete vendor")
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditVendorDelete, Entity: AuditEntityVendor,
			ID: id, Label: name, Summary: "Removed the vendor " + name,
			Before: map[string]any{"name": name, "offers_withdrawn": offers},
		})
	})
}

// ---------------------------------------------------------------- offers

const offerColumns = `id, vendor_id, variant_id, price_minor, stock_on_hand,
	stock_reserved, track_inventory, status, metadata, created_at, updated_at`

func (s *Vendors) scanOffer(row interface{ Scan(...any) error }) (*Offer, error) {
	var o Offer
	var meta []byte
	if err := row.Scan(
		&o.ID, &o.VendorID, &o.VariantID, &o.PriceMinor, &o.StockOnHand,
		&o.StockReserved, &o.TrackInventory, &o.Status, &meta, &o.CreatedAt, &o.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if err := scanMetadata(meta, &o.Metadata); err != nil {
		return nil, err
	}
	o.Price = Money{AmountMinor: o.PriceMinor, Currency: s.app.cfg.Currency}
	o.Available = o.StockOnHand - o.StockReserved
	return &o, nil
}

// SetOffer records what a seller charges for a variant, creating the offer or
// replacing the one that is there.
//
// One row per seller per variant, enforced by the table: a seller who wants two
// prices for the same thing is describing two things. So this is an upsert
// rather than a create, and calling it twice is an edit rather than a
// duplicate.
func (s *Vendors) SetOffer(ctx context.Context, vendorID int64, in OfferInput) (*Offer, error) {
	if in.VariantID <= 0 {
		return nil, Validationf("variant_id is required")
	}
	if in.PriceMinor < 0 {
		return nil, Validationf("price_minor must not be negative")
	}
	if in.StockOnHand < 0 {
		return nil, Validationf("stock_on_hand must not be negative")
	}
	if in.Status == "" {
		in.Status = OfferActive
	}
	if !validOfferStatus(in.Status) {
		return nil, Validationf("status must be active or paused")
	}
	track := true
	if in.TrackInventory != nil {
		track = *in.TrackInventory
	}
	meta, err := in.Metadata.value()
	if err != nil {
		return nil, Validationf("metadata is not valid JSON: %v", err)
	}

	var out *Offer
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var vendorName string
		err := tx.QueryRowContext(ctx, `SELECT name FROM vendors WHERE id = $1`, vendorID).Scan(&vendorName)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("vendor %d not found", vendorID)
		}
		if err != nil {
			return Internalf(err, "read vendor")
		}

		// Checked rather than left to the foreign key, so the refusal says which
		// of the two ids was wrong instead of naming a constraint.
		var sku string
		err = tx.QueryRowContext(ctx, `SELECT sku FROM variants WHERE id = $1`, in.VariantID).Scan(&sku)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("variant %d does not exist", in.VariantID)
		}
		if err != nil {
			return Internalf(err, "read variant")
		}

		o, err := s.scanOffer(tx.QueryRowContext(ctx, `
			INSERT INTO vendor_offers (vendor_id, variant_id, price_minor, stock_on_hand,
			                           track_inventory, status, metadata)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT ON CONSTRAINT vendor_offers_one_per_variant DO UPDATE SET
				price_minor = excluded.price_minor,
				stock_on_hand = excluded.stock_on_hand,
				track_inventory = excluded.track_inventory,
				status = excluded.status,
				metadata = excluded.metadata,
				updated_at = now()
			RETURNING `+offerColumns,
			vendorID, in.VariantID, in.PriceMinor, in.StockOnHand, track, in.Status, meta,
		))
		if err != nil {
			return Internalf(err, "set offer")
		}
		out = o
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditVendorOfferSet, Entity: AuditEntityVendor,
			ID: vendorID, Label: vendorName,
			Summary: vendorName + " is offering " + sku,
			After: map[string]any{
				"variant_id": in.VariantID, "sku": sku,
				"price_minor": in.PriceMinor, "stock_on_hand": in.StockOnHand,
				"status": string(in.Status),
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// OffersForVariant returns who is selling a variant, cheapest first.
//
// Every offer, not only the active ones: an operator's screen needs to see the
// paused ones too. A storefront asks for active offers through the public
// route, which filters.
func (s *Vendors) OffersForVariant(ctx context.Context, variantID int64) ([]*Offer, error) {
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT `+offerColumns+` FROM vendor_offers WHERE variant_id = $1
		 ORDER BY price_minor, id`, variantID)
	if err != nil {
		return nil, Internalf(err, "list offers")
	}
	defer rows.Close()

	out := []*Offer{}
	for rows.Next() {
		o, err := s.scanOffer(rows)
		if err != nil {
			return nil, Internalf(err, "scan offer")
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, Internalf(err, "list offers")
	}
	return out, nil
}

// OffersForVendor returns everything one seller is offering.
func (s *Vendors) OffersForVendor(ctx context.Context, vendorID int64) ([]*Offer, error) {
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT `+offerColumns+` FROM vendor_offers WHERE vendor_id = $1 ORDER BY id`, vendorID)
	if err != nil {
		return nil, Internalf(err, "list offers")
	}
	defer rows.Close()

	out := []*Offer{}
	for rows.Next() {
		o, err := s.scanOffer(rows)
		if err != nil {
			return nil, Internalf(err, "scan offer")
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// DeleteOffer withdraws one seller's offer on one variant.
func (s *Vendors) DeleteOffer(ctx context.Context, vendorID, variantID int64) error {
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var vendorName, sku string
		err := tx.QueryRowContext(ctx, `
			SELECT v.name, va.sku
			FROM vendor_offers o
			JOIN vendors v ON v.id = o.vendor_id
			JOIN variants va ON va.id = o.variant_id
			WHERE o.vendor_id = $1 AND o.variant_id = $2`, vendorID, variantID).Scan(&vendorName, &sku)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("that vendor is not offering that variant")
		}
		if err != nil {
			return Internalf(err, "read offer")
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM vendor_offers WHERE vendor_id = $1 AND variant_id = $2`,
			vendorID, variantID); err != nil {
			return Internalf(err, "delete offer")
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditVendorOfferDelete, Entity: AuditEntityVendor,
			ID: vendorID, Label: vendorName,
			Summary: vendorName + " withdrew " + sku,
			Before:  map[string]any{"variant_id": variantID, "sku": sku},
		})
	})
}
