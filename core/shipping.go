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
// The third is that a rate may be a customer group's (D76), and then it is read
// the way a group price is: from the cart's verified address and never from an
// address somebody typed (D66). A group with a rate for this basket and this
// place is offered its own rates instead of the public ones, so a dealer sees
// the freight that was negotiated and not consumer express beside it. Where
// the group has nothing to say, the public answer stands.
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
	// GroupID nil is everybody. A rate naming a group is offered only to a cart
	// whose verified address is in it, and replaces the public rates there.
	GroupID   *int64 `json:"group_id,omitempty"`
	GroupCode string `json:"group_code,omitempty"`
	GroupName string `json:"group_name,omitempty"`
}

// ShippingQuery is a destination and a basket: everything a price depends on.
type ShippingQuery struct {
	Country       string
	State         string
	SubtotalMinor int64
	// Email is the address whose customer groups' rates apply, and the caller
	// is the one vouching for it, exactly as with Pricing.PriceFor: a cart
	// passes its verified_email and never the email typed onto it (D66). Empty
	// is anybody, who is offered only the rates that name no group.
	Email string
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
	// GroupID is who sees the rate. Absent from a create, or null, is
	// everybody. Absent from an update leaves the rate's audience alone — the
	// one field here that does not follow the replace-everything rule, because
	// a client written before groups would otherwise turn a dealer's free
	// freight into everybody's by editing its price.
	GroupID NullableID `json:"group_id"`
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

// rateColumns names the group through scalar subqueries rather than a join, so
// that it reads the same after INSERT … RETURNING, where there is nothing to
// join to.
const rateColumns = `id, zone_id, name, price_minor, min_subtotal_minor,
	max_subtotal_minor, active, position, customer_group_id,
	coalesce((SELECT g.code FROM customer_groups g WHERE g.id = customer_group_id), ''),
	coalesce((SELECT g.name FROM customer_groups g WHERE g.id = customer_group_id), '')`

func scanRate(row interface{ Scan(...any) error }, currency string) (*ShippingRate, error) {
	var r ShippingRate
	var max, group sql.NullInt64
	if err := row.Scan(&r.ID, &r.ZoneID, &r.Name, &r.Price.AmountMinor,
		&r.MinSubtotalMinor, &max, &r.Active, &r.Position,
		&group, &r.GroupCode, &r.GroupName); err != nil {
		return nil, err
	}
	if max.Valid {
		v := max.Int64
		r.MaxSubtotalMinor = &v
	}
	if group.Valid {
		v := group.Int64
		r.GroupID = &v
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
		name, stringArray(upperAll(in.Countries)), stringArray(zoneStates(in.Countries, in.States)))
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
		id, name, stringArray(upperAll(in.Countries)), stringArray(zoneStates(in.Countries, in.States)))
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
	group := in.GroupID.Value
	if err := s.validateRate(ctx, in, group, 0); err != nil {
		return nil, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	row := s.app.db.QueryRowContext(ctx, `
		INSERT INTO shipping_rates
		    (zone_id, name, price_minor, min_subtotal_minor, max_subtotal_minor, active, position,
		     customer_group_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING `+rateColumns,
		in.ZoneID, strings.TrimSpace(in.Name), in.PriceMinor,
		in.MinSubtotalMinor, in.MaxSubtotalMinor, active, in.Position, group)
	r, err := scanRate(row, s.app.cfg.Currency)
	if err != nil {
		return nil, translateRateErr(err, in.ZoneID, group)
	}
	return r, nil
}

// UpdateRate changes one.
func (s *Shipping) UpdateRate(ctx context.Context, id int64, in ShippingRateInput) (*ShippingRate, error) {
	group := in.GroupID.Value
	if !in.GroupID.Present {
		// Left alone, so the overlap check below has to know who the rate is
		// for now rather than assume it is everybody's. A rate that does not
		// exist is left to the UPDATE below to report, so a bad body on a
		// missing id answers as it always has.
		var stored sql.NullInt64
		err := s.app.db.QueryRowContext(ctx,
			`SELECT customer_group_id FROM shipping_rates WHERE id = $1`, id).Scan(&stored)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		if stored.Valid {
			group = &stored.Int64
		}
	}
	if err := s.validateRate(ctx, in, group, id); err != nil {
		return nil, err
	}
	active := true
	if in.Active != nil {
		active = *in.Active
	}
	row := s.app.db.QueryRowContext(ctx, `
		UPDATE shipping_rates
		SET zone_id = $2, name = $3, price_minor = $4, min_subtotal_minor = $5,
		    max_subtotal_minor = $6, active = $7, position = $8,
		    customer_group_id = $9, updated_at = now()
		WHERE id = $1
		RETURNING `+rateColumns,
		id, in.ZoneID, strings.TrimSpace(in.Name), in.PriceMinor,
		in.MinSubtotalMinor, in.MaxSubtotalMinor, active, in.Position, group)
	r, err := scanRate(row, s.app.cfg.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("no shipping rate %d", id)
	}
	if err != nil {
		return nil, translateRateErr(err, in.ZoneID, group)
	}
	return r, nil
}

// translateRateErr names which of a rate's two references was not there: the
// zone it is priced in, or the group it is offered to.
func translateRateErr(err error, zoneID int64, group *int64) error {
	if !isForeignKeyViolation(err) {
		return err
	}
	if strings.Contains(err.Error(), "customer_group_id") && group != nil {
		return Validationf("no customer group %d", *group)
	}
	return Validationf("no shipping zone %d", zoneID)
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
//
// Only bands offered to the same people can collide. A group's "Standard" and
// everybody's "Standard" are never both offered to one cart in one zone — a
// group's rates replace the public ones wherever it has any — so the two are
// not an overlap; they are the point.
func (s *Shipping) validateRate(ctx context.Context, in ShippingRateInput, group *int64, excludeID int64) error {
	name := strings.TrimSpace(in.Name)
	switch {
	case group != nil && *group <= 0:
		return Validationf("group_id must be a positive integer, or null for everybody")
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
		WHERE zone_id = $1 AND lower(name) = lower($2) AND id <> $3
		  AND customer_group_id IS NOT DISTINCT FROM $4`,
		in.ZoneID, name, excludeID, group)
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
				"%q already has a band covering part of that range in this zone, for the same customers; "+
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
	quotes, _, err := s.options(ctx, s.app.db, q)
	return quotes, err
}

// options is what a basket may be offered, and whether rates decide its
// delivery at all — false is a store still on Config.FlatShippingMinor for
// this cart.
//
// It takes its querier so the checkout can run it inside the transaction that
// is already holding every price and reservation still. A rate re-read under
// that lock is a rate that cannot move between the quote and the charge, and
// that includes a group rate the address has since left the group of.
//
// The groups go first and win outright (D76). Adding a group's rates to the
// public ones would show a dealer consumer express beside the freight they
// negotiated, and the store would have no way to withdraw an option from the
// people it negotiated a different one with. Where none of the address's
// groups has a rate for this basket and this place, it is offered exactly what
// anybody is — so a group with one special rate in one state is not stranded
// everywhere else.
func (s *Shipping) options(ctx context.Context, q rowQuerier, in ShippingQuery) ([]ShippingQuote, bool, error) {
	country := strings.ToUpper(strings.TrimSpace(in.Country))
	// Every spelling of the state, for zones saved with a name before D72.
	states := stringArray(StateSpellings(country, in.State))

	if email := strings.ToLower(strings.TrimSpace(in.Email)); email != "" {
		mine, err := s.groupQuotes(ctx, q, country, states, in.SubtotalMinor, email)
		if err != nil {
			return nil, false, err
		}
		if len(mine) > 0 {
			return mine, true, nil
		}
	}

	rated, err := s.configured(ctx, q)
	if err != nil || !rated {
		return nil, false, err
	}
	public, err := s.publicQuotes(ctx, q, country, states, in.SubtotalMinor)
	return public, true, err
}

// groupQuotes is the offer an address's groups have for this basket here.
//
// Each group is judged on its own, by the zone rule everybody's rates follow
// with one difference: among the zones that cover the address, the most
// specific one *in which the group has a rate for this basket* wins, rather
// than the most specific zone outright. A group's rates are exceptions written
// into whichever zones the store already has, and "dealers ship free anywhere"
// written once on the catch-all must still reach the dealer in the one state
// that has a courier of its own for everybody else.
//
// An address in several groups is offered all of their offers. Two of them
// naming the same method is a collision nobody configured on purpose, and it
// is settled the way two price lists covering a line are: the cheaper stands,
// because that is the one that cannot become a complaint.
func (s *Shipping) groupQuotes(ctx context.Context, q rowQuerier, country, states string, subtotal int64, email string) ([]ShippingQuote, error) {
	rows, err := q.QueryContext(ctx, `
		WITH fits AS (
		    SELECT r.id,
		           rank() OVER (
		               PARTITION BY r.customer_group_id
		               ORDER BY (cardinality(z.states) > 0) DESC,
		                        (cardinality(z.countries) > 0) DESC, z.id) AS place
		    FROM shipping_rates r
		    JOIN shipping_zones z ON z.id = r.zone_id
		    WHERE r.customer_group_id IN (
		              SELECT m.group_id FROM customer_group_members m WHERE m.email = $3)
		      AND r.active
		      AND r.min_subtotal_minor <= $4
		      AND (r.max_subtotal_minor IS NULL OR r.max_subtotal_minor > $4)
		      AND (cardinality(z.countries) = 0 OR $1 = ANY(z.countries))
		      AND (cardinality(z.states) = 0 OR z.states && $2::text[])
		)
		SELECT `+rateColumns+`
		FROM shipping_rates
		WHERE id IN (SELECT id FROM fits WHERE place = 1)
		ORDER BY position, price_minor, id`, country, states, email, subtotal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []ShippingQuote
	cheapest := map[string]int{}
	for rows.Next() {
		r, err := scanRate(rows, s.app.cfg.Currency)
		if err != nil {
			return nil, err
		}
		key := strings.ToLower(r.Name)
		if at, seen := cheapest[key]; seen && all[at].Price.AmountMinor <= r.Price.AmountMinor {
			continue
		}
		cheapest[key] = len(all)
		all = append(all, ShippingQuote{RateID: r.ID, Name: r.Name, Price: r.Price, ZoneID: r.ZoneID})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Keep only the row each name settled on, in the order the store listed.
	out := make([]ShippingQuote, 0, len(cheapest))
	for i, quote := range all {
		if cheapest[strings.ToLower(quote.Name)] == i {
			out = append(out, quote)
		}
	}
	return out, nil
}

// publicQuotes is what anybody is offered: the rule D52 shipped, unchanged for
// a store that has never named a group on a rate.
func (s *Shipping) publicQuotes(ctx context.Context, q rowQuerier, country, states string, subtotal int64) ([]ShippingQuote, error) {
	// The winning zone, by the same specificity tax uses: a state match beats a
	// country match beats the catch-all. One row out, so the rates below can be
	// read without a second decision about which zone they came from.
	//
	// A zone that holds rates and none of them for everybody was written for
	// customer groups, and for everybody else it is not there. Left in, the
	// store's first "dealers in Karnataka" zone would beat the country's for
	// every consumer in Karnataka and offer them nothing. A zone with no rates
	// at all still wins, as it always has — that is how a store says it does
	// not deliver to one state of a country it otherwise covers.
	var zoneID int64
	rows, qerr := q.QueryContext(ctx, `
		SELECT z.id
		FROM shipping_zones z
		WHERE (cardinality(z.countries) = 0 OR $1 = ANY(z.countries))
		  AND (cardinality(z.states) = 0 OR z.states && $2::text[])
		  AND (NOT EXISTS (SELECT 1 FROM shipping_rates r WHERE r.zone_id = z.id)
		       OR EXISTS (SELECT 1 FROM shipping_rates r
		                  WHERE r.zone_id = z.id AND r.customer_group_id IS NULL))
		ORDER BY (cardinality(z.states) > 0) DESC, (cardinality(z.countries) > 0) DESC, z.id
		LIMIT 1`, country, states)
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
		WHERE zone_id = $1 AND active AND customer_group_id IS NULL
		  AND min_subtotal_minor <= $2
		  AND (max_subtotal_minor IS NULL OR max_subtotal_minor > $2)
		ORDER BY position, price_minor, id`, zoneID, subtotal)
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

// configured reports whether this store has any shipping rates for everybody.
//
// It exists so that adding this feature changes nothing for a store that has
// not used it: with no rates, checkout keeps charging Config.FlatShippingMinor
// exactly as it did before, and no client has to start sending a choice.
//
// Group rates do not count. A store that has priced dealer freight and nothing
// else has not said where it delivers to everybody, and refusing every
// consumer's order the moment the first dealer rate was saved would be the
// feature breaking the shop.
func (s *Shipping) configured(ctx context.Context, q rowQuerier) (bool, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT 1 FROM shipping_rates WHERE active AND customer_group_id IS NULL LIMIT 1`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	return rows.Next(), rows.Err()
}

// zoneStates stores each of a zone's states as its code. A zone can name
// several countries and a state name belongs to one of them, so each state is
// read against each country until one of them knows it; a state none of them
// knows is kept as written.
func zoneStates(countries, states []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range states {
		norm := normalizeState(s)
		if norm == "" {
			continue
		}
		code := norm
		for _, c := range countries {
			if got := StateCode(c, s); got != norm {
				code = got
				break
			}
		}
		if !seen[code] {
			seen[code] = true
			out = append(out, code)
		}
	}
	return out
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
