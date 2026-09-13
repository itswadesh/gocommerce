package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Shipping: where this store delivers, and what it charges to.
//
// Two things are worth knowing before reading the rest, and they are the two
// things tax says about itself, because this is the same shape of problem.
//
// The first is that the most specific zone wins. A zone naming a country and a
// state beats one naming only the country, which beats the one naming nowhere
// in particular — and only the winner's rates are offered. That is what lets a
// store write "anywhere: 999" once and then say something different about the
// one state it has a courier in, without the two competing.
//
// The second is that a method is a name and a band, not a name and a price.
// "Standard 49, free over 2000" is one method the shopper recognises and two
// rows that cannot both match, which is why `Quote` can return each name once
// without choosing between rows on the shopper's behalf.
//
// What this deliberately does not do is talk to a carrier. D52 keeps that
// split: core prices the delivery and records what was sold, and booking the
// parcel is `Ship()` and a module. `Shipping()` is the price; `Ship()` is the
// parcel.
type Shipping struct {
	app *App
}

// Shipping returns the shipping-rate service. Not to be confused with `Ship()`,
// which books parcels: this one decides what the shopper is charged.
func (a *App) Shipping() *Shipping { return a.shipping }

// ShippingZone is a set of places that share a price list.
type ShippingZone struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Countries and States are ISO codes. Both empty is the catch-all zone,
	// which every more specific zone beats.
	Countries []string `json:"countries"`
	States    []string `json:"states"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

// ShippingRate is one named method, priced over a band of basket subtotals.
type ShippingRate struct {
	ID     int64  `json:"id"`
	ZoneID int64  `json:"zone_id"`
	Name   string `json:"name"`
	Price  Money  `json:"price"`
	// MinSubtotalMinor is inclusive and MaxSubtotalMinor is exclusive, so two
	// bands that meet at a number cannot both claim it.
	MinSubtotalMinor int64  `json:"min_subtotal_minor"`
	MaxSubtotalMinor *int64 `json:"max_subtotal_minor,omitempty"`
	Active           bool   `json:"active"`
	Position         int    `json:"position"`
}

// ShippingQuery is a destination and a basket: everything a price depends on.
type ShippingQuery struct {
	Country       string
	State         string
	SubtotalMinor int64
}

// ShippingQuote is one option a shopper may choose.
type ShippingQuote struct {
	RateID int64  `json:"rate_id"`
	Name   string `json:"name"`
	Price  Money  `json:"price"`
	ZoneID int64  `json:"zone_id"`
}

// ShippingZoneInput creates or changes a zone.
type ShippingZoneInput struct {
	Name      string   `json:"name"`
	Countries []string `json:"countries"`
	States    []string `json:"states"`
}

// ShippingRateInput creates or changes a rate.
type ShippingRateInput struct {
	ZoneID           int64  `json:"zone_id"`
	Name             string `json:"name"`
	PriceMinor       int64  `json:"price_minor"`
	MinSubtotalMinor int64  `json:"min_subtotal_minor"`
	MaxSubtotalMinor *int64 `json:"max_subtotal_minor"`
	Active           *bool  `json:"active"`
	Position         int    `json:"position"`
}

const zoneColumns = `id, name, to_jsonb(countries), to_jsonb(states), created_at, updated_at`

// scanZone reads the arrays through to_jsonb for the reason catalog.go gives:
// pgx's database/sql driver returns a text[] as its raw PostgreSQL literal, and
// parsing quoted array syntax is not this package's job.
func scanZone(row interface{ Scan(...any) error }) (*ShippingZone, error) {
	var z ShippingZone
	var countries, states []byte
	if err := row.Scan(&z.ID, &z.Name, &countries, &states, &z.CreatedAt, &z.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(countries, &z.Countries); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(states, &z.States); err != nil {
		return nil, err
	}
	return &z, nil
}

const rateColumns = `id, zone_id, name, price_minor, min_subtotal_minor,
	max_subtotal_minor, active, position`

func scanRate(row interface{ Scan(...any) error }, currency string) (*ShippingRate, error) {
	var r ShippingRate
	var max sql.NullInt64
	if err := row.Scan(&r.ID, &r.ZoneID, &r.Name, &r.Price.AmountMinor,
		&r.MinSubtotalMinor, &max, &r.Active, &r.Position); err != nil {
		return nil, err
	}
	if max.Valid {
		v := max.Int64
		r.MaxSubtotalMinor = &v
	}
	r.Price.Currency = currency
	return &r, nil
}

// CreateZone adds a zone.
func (s *Shipping) CreateZone(ctx context.Context, in ShippingZoneInput) (*ShippingZone, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, Validationf("a zone needs a name")
	}
	row := s.app.db.QueryRowContext(ctx, `
		INSERT INTO shipping_zones (name, countries, states)
		VALUES ($1, $2::text[], $3::text[])
		RETURNING `+zoneColumns,
		name, stringArray(upperAll(in.Countries)), stringArray(upperAll(in.States)))
	return scanZone(row)
}

// UpdateZone changes one.
func (s *Shipping) UpdateZone(ctx context.Context, id int64, in ShippingZoneInput) (*ShippingZone, error) {
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, Validationf("a zone needs a name")
	}
	row := s.app.db.QueryRowContext(ctx, `
		UPDATE shipping_zones
		SET name = $2, countries = $3::text[], states = $4::text[], updated_at = now()
		WHERE id = $1
		RETURNING `+zoneColumns,
		id, name, stringArray(upperAll(in.Countries)), stringArray(upperAll(in.States)))
	z, err := scanZone(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("no shipping zone %d", id)
	}
	return z, err
}

// DeleteZone removes a zone and, by cascade, its rates.
func (s *Shipping) DeleteZone(ctx context.Context, id int64) error {
	res, err := s.app.db.ExecContext(ctx, `DELETE FROM shipping_zones WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return NotFoundf("no shipping zone %d", id)
	}
	return nil
}

// Zones lists every zone, with its rates.
func (s *Shipping) Zones(ctx context.Context) ([]*ShippingZone, map[int64][]*ShippingRate, error) {
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT `+zoneColumns+` FROM shipping_zones ORDER BY id`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	zones := []*ShippingZone{}
	for rows.Next() {
		z, err := scanZone(rows)
		if err != nil {
			return nil, nil, err
		}
		zones = append(zones, z)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	rateRows, err := s.app.db.QueryContext(ctx,
		`SELECT `+rateColumns+` FROM shipping_rates ORDER BY zone_id, position, id`)
	if err != nil {
		return nil, nil, err
	}
	defer rateRows.Close()

	byZone := map[int64][]*ShippingRate{}
	for rateRows.Next() {
		r, err := scanRate(rateRows, s.app.cfg.Currency)
		if err != nil {
			return nil, nil, err
		}
		byZone[r.ZoneID] = append(byZone[r.ZoneID], r)
	}
	return zones, byZone, rateRows.Err()
}

// CreateRate adds a rate to a zone.
func (s *Shipping) CreateRate(ctx context.Context, in ShippingRateInput) (*ShippingRate, error) {
	if err := s.validateRate(ctx, in, 0); err != nil {
		return nil, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	row := s.app.db.QueryRowContext(ctx, `
		INSERT INTO shipping_rates
		    (zone_id, name, price_minor, min_subtotal_minor, max_subtotal_minor, active, position)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING `+rateColumns,
		in.ZoneID, strings.TrimSpace(in.Name), in.PriceMinor,
		in.MinSubtotalMinor, in.MaxSubtotalMinor, active, in.Position)
	r, err := scanRate(row, s.app.cfg.Currency)
	if isForeignKeyViolation(err) {
		return nil, Validationf("no shipping zone %d", in.ZoneID)
	}
	return r, err
}

// UpdateRate changes one.
func (s *Shipping) UpdateRate(ctx context.Context, id int64, in ShippingRateInput) (*ShippingRate, error) {
	if err := s.validateRate(ctx, in, id); err != nil {
		return nil, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	row := s.app.db.QueryRowContext(ctx, `
		UPDATE shipping_rates
		SET zone_id = $2, name = $3, price_minor = $4, min_subtotal_minor = $5,
		    max_subtotal_minor = $6, active = $7, position = $8, updated_at = now()
		WHERE id = $1
		RETURNING `+rateColumns,
		id, in.ZoneID, strings.TrimSpace(in.Name), in.PriceMinor,
		in.MinSubtotalMinor, in.MaxSubtotalMinor, active, in.Position)
	r, err := scanRate(row, s.app.cfg.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("no shipping rate %d", id)
	}
	return r, err
}

// DeleteRate removes one.
func (s *Shipping) DeleteRate(ctx context.Context, id int64) error {
	res, err := s.app.db.ExecContext(ctx, `DELETE FROM shipping_rates WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return NotFoundf("no shipping rate %d", id)
	}
	return nil
}

// validateRate refuses what the database's own CHECKs cannot express, and the
// overlap rule is the one that matters: two bands of one method that both claim
// a basket would offer the shopper the same method at two prices, and there is
// no correct way to pick between them at the moment it happens. The right place
// to say no is here, where somebody is configuring it and can fix it.
func (s *Shipping) validateRate(ctx context.Context, in ShippingRateInput, excludeID int64) error {
	name := strings.TrimSpace(in.Name)
	switch {
	case in.ZoneID <= 0:
		return Validationf("a rate belongs to a zone")
	case name == "":
		return Validationf("a rate needs a name, which is what the shopper sees")
	case in.PriceMinor < 0:
		return Validationf("a price cannot be negative")
	case in.MinSubtotalMinor < 0:
		return Validationf("a band cannot start below zero")
	case in.MaxSubtotalMinor != nil && *in.MaxSubtotalMinor <= in.MinSubtotalMinor:
		return Validationf("a band that ends at or before it starts matches nothing")
	}

	rows, err := s.app.db.QueryContext(ctx, `
		SELECT min_subtotal_minor, max_subtotal_minor
		FROM shipping_rates
		WHERE zone_id = $1 AND lower(name) = lower($2) AND id <> $3`,
		in.ZoneID, name, excludeID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var min int64
		var max sql.NullInt64
		if err := rows.Scan(&min, &max); err != nil {
			return err
		}
		if bandsOverlap(in.MinSubtotalMinor, in.MaxSubtotalMinor, min, max) {
			return Validationf(
				"%q already has a band covering part of that range in this zone; "+
					"bands of one method must not overlap, or a basket would be offered it twice", name)
		}
	}
	return rows.Err()
}

// bandsOverlap treats both bands as half-open [min, max), so two that meet at a
// number do not collide.
func bandsOverlap(aMin int64, aMax *int64, bMin int64, bMax sql.NullInt64) bool {
	aEnd, bEnd := int64(-1), int64(-1) // -1 stands for "no ceiling"
	if aMax != nil {
		aEnd = *aMax
	}
	if bMax.Valid {
		bEnd = bMax.Int64
	}
	if aEnd >= 0 && aEnd <= bMin {
		return false
	}
	if bEnd >= 0 && bEnd <= aMin {
		return false
	}
	return true
}

// Quote returns what this store will carry the basket for, to this place.
//
// An empty result is an answer, not an error: it means nowhere covers that
// destination, and what to do about it belongs to the caller — the checkout
// refuses the sale, a storefront says "we do not ship there yet".
func (s *Shipping) Quote(ctx context.Context, q ShippingQuery) ([]ShippingQuote, error) {
	return s.quote(ctx, s.app.db, q)
}

// quote takes its querier so the checkout can run it inside the transaction
// that is already holding every price and reservation still. A rate re-read
// under that lock is a rate that cannot move between the quote and the charge.
func (s *Shipping) quote(ctx context.Context, q rowQuerier, in ShippingQuery) ([]ShippingQuote, error) {
	country := strings.ToUpper(strings.TrimSpace(in.Country))
	state := strings.ToUpper(strings.TrimSpace(in.State))

	// The winning zone, by the same specificity tax uses: a state match beats a
	// country match beats the catch-all. One row out, so the rates below can be
	// read without a second decision about which zone they came from.
	var zoneID int64
	rows, qerr := q.QueryContext(ctx, `
		SELECT id
		FROM shipping_zones
		WHERE (cardinality(countries) = 0 OR $1 = ANY(countries))
		  AND (cardinality(states) = 0 OR $2 = ANY(states))
		ORDER BY (cardinality(states) > 0) DESC, (cardinality(countries) > 0) DESC, id
		LIMIT 1`, country, state)
	if qerr != nil {
		return nil, qerr
	}
	found := false
	for rows.Next() {
		if err := rows.Scan(&zoneID); err != nil {
			rows.Close()
			return nil, err
		}
		found = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if !found {
		return nil, nil
	}

	rateRows, err2 := q.QueryContext(ctx, `
		SELECT `+rateColumns+`
		FROM shipping_rates
		WHERE zone_id = $1 AND active
		  AND min_subtotal_minor <= $2
		  AND (max_subtotal_minor IS NULL OR max_subtotal_minor > $2)
		ORDER BY position, price_minor, id`, zoneID, in.SubtotalMinor)
	if err2 != nil {
		return nil, err2
	}
	defer rateRows.Close()

	var out []ShippingQuote
	for rateRows.Next() {
		r, err := scanRate(rateRows, s.app.cfg.Currency)
		if err != nil {
			return nil, err
		}
		out = append(out, ShippingQuote{
			RateID: r.ID, Name: r.Name, Price: r.Price, ZoneID: r.ZoneID,
		})
	}
	return out, rateRows.Err()
}

// configured reports whether this store has any shipping rates at all.
//
// It exists so that adding this feature changes nothing for a store that has
// not used it: with no rates, checkout keeps charging Config.FlatShippingMinor
// exactly as it did before, and no client has to start sending a choice.
func (s *Shipping) configured(ctx context.Context, q rowQuerier) (bool, error) {
	rows, err := q.QueryContext(ctx, `SELECT 1 FROM shipping_rates WHERE active LIMIT 1`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

func upperAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.ToUpper(strings.TrimSpace(v)); v != "" {
			out = append(out, v)
		}
	}
	return out
}
