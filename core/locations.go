package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Locations are the places stock physically is: a shop, a warehouse, a shelf in
// somebody's garage. Every store has at least one and most have exactly one,
// which is the case this service is shaped around — a single-location store
// should never have to think about locations, and the default one exists so it
// does not have to.
//
// What a location is *for* is answering two questions the single number could
// not: can I ship this today from somewhere near the buyer, and which box does
// this order come out of. Both are answered at reservation time, once, and
// recorded on the order line — so a cancellation puts the units back where they
// came from rather than wherever the default happens to be that week.
type Locations struct {
	app *App
}

// Places returns the locations service.
func (a *App) Places() *Locations { return a.locations }

// Location is one place stock can be.
type Location struct {
	ID   int64  `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
	// Address is where it is, when that matters — a pickup point a shopper is
	// sent to, or the origin on a customs form. A stockroom nobody visits can
	// leave it empty.
	Address *Address `json:"address,omitempty"`
	// Priority orders the search for stock to reserve: lower is preferred. Two
	// locations at the same priority are tried oldest first, which is arbitrary
	// but stable, and stability is what makes a reservation reproducible.
	Priority int `json:"priority"`
	// Active is whether new orders may be filled from here. Stock is not
	// allowed to sit at an inactive location — see Update — so deactivating is
	// a statement that the place is empty, not a way to hide what is in it.
	Active    bool     `json:"active"`
	IsDefault bool     `json:"is_default"`
	Metadata  Metadata `json:"metadata"`
	// OnHand and Reserved are what this location is holding across every
	// variant. Summed on the way out rather than stored, for the same reason
	// the variant totals are.
	OnHand   int `json:"on_hand"`
	Reserved int `json:"reserved"`
	// SKUs counts the rows this place is holding, where holding means either
	// number is non-zero. That is refuseIfHolding's definition and not a
	// narrower one: a location holding nothing but reserved units used to report
	// skus 0 while Update and Delete refused, naming a count nobody could see.
	SKUs      int       `json:"skus"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// LocationInput creates a location.
type LocationInput struct {
	Code     string   `json:"code"`
	Name     string   `json:"name"`
	Address  *Address `json:"address"`
	Priority *int     `json:"priority"`
	Active   *bool    `json:"active"`
	Metadata Metadata `json:"metadata"`
}

// LocationPatch changes one. Every field is optional; an omitted field is left
// alone, which is what lets the panel send only what the operator touched.
type LocationPatch struct {
	Name     *string  `json:"name"`
	Address  *Address `json:"address"`
	Priority *int     `json:"priority"`
	Active   *bool    `json:"active"`
	Metadata Metadata `json:"metadata"`
}

// VariantStock is one variant's holding at one location.
type VariantStock struct {
	VariantID    int64  `json:"variant_id"`
	LocationID   int64  `json:"location_id"`
	LocationCode string `json:"location_code"`
	LocationName string `json:"location_name"`
	Active       bool   `json:"active"`
	OnHand       int    `json:"on_hand"`
	Reserved     int    `json:"reserved"`
	Available    int    `json:"available"`
}

const locationColumns = `l.id, l.code, l.name, l.address, l.priority, l.active,
	l.is_default, l.metadata, l.created_at, l.updated_at,
	coalesce((SELECT sum(vs.on_hand)  FROM variant_stock vs WHERE vs.location_id = l.id), 0),
	coalesce((SELECT sum(vs.reserved) FROM variant_stock vs WHERE vs.location_id = l.id), 0),
	(SELECT count(*) FROM variant_stock vs WHERE vs.location_id = l.id
	   AND (vs.on_hand <> 0 OR vs.reserved <> 0))`

// List returns every location in the order stock is drawn from them.
func (s *Locations) List(ctx context.Context) ([]*Location, error) {
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT `+locationColumns+` FROM locations l ORDER BY l.priority, l.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Location{}
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get returns one location.
func (s *Locations) Get(ctx context.Context, id int64) (*Location, error) {
	row := s.app.db.QueryRowContext(ctx,
		`SELECT `+locationColumns+` FROM locations l WHERE l.id = $1`, id)
	l, err := scanLocation(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("location %d does not exist", id)
	}
	return l, err
}

type scanner interface{ Scan(dest ...any) error }

func scanLocation(row scanner) (*Location, error) {
	l := &Location{}
	var addr, meta []byte
	if err := row.Scan(&l.ID, &l.Code, &l.Name, &addr, &l.Priority, &l.Active,
		&l.IsDefault, &meta, &l.CreatedAt, &l.UpdatedAt,
		&l.OnHand, &l.Reserved, &l.SKUs); err != nil {
		return nil, err
	}
	if err := scanMetadata(meta, &l.Metadata); err != nil {
		return nil, err
	}
	// An empty object and a null both mean "no address"; neither should turn
	// into a struct full of empty strings that a client has to test field by
	// field.
	var a Address
	if len(addr) > 0 {
		if err := json.Unmarshal(addr, &a); err != nil {
			return nil, err
		}
		if a != (Address{}) {
			l.Address = &a
		}
	}
	return l, nil
}

// Create adds a location. It is not made default — that is SetDefault's job,
// and doing it here would silently redirect every reservation in the store as a
// side effect of adding a shelf.
func (s *Locations) Create(ctx context.Context, in LocationInput) (*Location, error) {
	code := strings.ToLower(strings.TrimSpace(in.Code))
	name := strings.TrimSpace(in.Name)
	if code == "" {
		return nil, Validationf("code is required")
	}
	if name == "" {
		return nil, Validationf("name is required")
	}
	meta, err := in.Metadata.value()
	if err != nil {
		return nil, Validationf("location metadata is not valid JSON: %v", err)
	}
	addr, err := marshalAddress(in.Address)
	if err != nil {
		return nil, err
	}
	priority := 0
	if in.Priority != nil {
		priority = *in.Priority
	}

	var id int64
	active := boolOr(in.Active, true)
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO locations (code, name, address, priority, active, metadata)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			code, name, addr, priority, active, meta).Scan(&id); err != nil {
			return translateLocationErr(err, code)
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditLocationCreate, Entity: AuditEntityLocation,
			ID: id, Label: name, Summary: "Added the location " + name,
			After: map[string]any{
				"code": code, "name": name, "priority": priority, "active": active,
			},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Update changes a location.
//
// Deactivating one is the only interesting case. An inactive location that
// still holds stock would report units as available that nothing can reserve —
// the totals count them, the picker skips them — so the stock has to go
// somewhere first. Refusing here is what keeps `available` meaning what it says.
//
// What holds that answer still is the row lock below, taken before the stock is
// counted: the refusal and the engine's own "stock never arrives at a closed
// location" guard (D44) meet on the locations row, so a transfer committing
// between the count and the UPDATE is impossible rather than merely unlikely.
func (s *Locations) Update(ctx context.Context, id int64, patch LocationPatch) (*Location, error) {
	sets := []string{"updated_at = now()"}
	args := []any{id}
	after := map[string]any{}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
		after[col] = v
	}
	if patch.Name != nil {
		if strings.TrimSpace(*patch.Name) == "" {
			return nil, Validationf("name must not be empty")
		}
		add("name", strings.TrimSpace(*patch.Name))
	}
	if patch.Address != nil {
		addr, err := marshalAddress(patch.Address)
		if err != nil {
			return nil, err
		}
		add("address", addr)
	}
	if patch.Priority != nil {
		add("priority", *patch.Priority)
	}
	if patch.Metadata != nil {
		meta, err := patch.Metadata.value()
		if err != nil {
			return nil, Validationf("location metadata is not valid JSON: %v", err)
		}
		add("metadata", meta)
	}
	if patch.Active != nil {
		add("active", *patch.Active)
	}
	if len(sets) == 1 {
		return s.Get(ctx, id)
	}

	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		// The guard moved inside the transaction with the wrap that made room
		// for the audit row, which is strictly better: it used to read outside
		// the write it guards, so a reservation landing between the two slipped
		// past it.
		//
		// The locations row is locked FIRST and the stock read second, in that
		// order and not the other. resolveShelf takes the same two tables the
		// same way round (locations FOR SHARE, then variant_stock), so a transfer
		// arriving here and a deactivation closing the place cannot pass each
		// other and cannot deadlock: one of them waits, and whichever wakes up
		// second is refused.
		before := map[string]any{}
		var wasName string
		var wasPriority int
		var wasActive bool
		err := tx.QueryRowContext(ctx,
			`SELECT name, priority, active FROM locations WHERE id = $1 FOR UPDATE`, id,
		).Scan(&wasName, &wasPriority, &wasActive)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("location %d does not exist", id)
		}
		if err != nil {
			return err
		}
		if patch.Active != nil && !*patch.Active {
			if err := refuseIfHolding(ctx, tx, id, "deactivated"); err != nil {
				return err
			}
		}
		for col, v := range map[string]any{
			"name": wasName, "priority": wasPriority, "active": wasActive,
		} {
			if _, changed := after[col]; changed {
				before[col] = v
			}
		}

		var name string
		err = tx.QueryRowContext(ctx,
			`UPDATE locations SET `+strings.Join(sets, ", ")+` WHERE id = $1 RETURNING name`,
			args...).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("location %d does not exist", id)
		}
		if err != nil {
			return translateLocationErr(err, "")
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditLocationUpdate, Entity: AuditEntityLocation,
			ID: id, Label: name, Summary: "Edited the location " + name,
			Before: before, After: after,
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// SetDefault moves the default, which is where stock lands when nobody says
// otherwise. Two statements in one transaction, because the unique index allows
// exactly one default row and the old one has to stand down before the new one
// can stand up.
func (s *Locations) SetDefault(ctx context.Context, id int64) (*Location, error) {
	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var active bool
		var name string
		err := tx.QueryRowContext(ctx,
			`SELECT active, name FROM locations WHERE id = $1`, id).Scan(&active, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("location %d does not exist", id)
		}
		if err != nil {
			return err
		}
		if !active {
			return Conflictf("an inactive location cannot be the default")
		}
		var wasDefault string
		// The name of the outgoing default, read before it stands down: "the
		// default moved from A to B" is the fact, and after the next statement
		// nothing can say what A was.
		_ = tx.QueryRowContext(ctx,
			`SELECT name FROM locations WHERE is_default`).Scan(&wasDefault)
		if _, err := tx.ExecContext(ctx,
			`UPDATE locations SET is_default = false, updated_at = now() WHERE is_default`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE locations SET is_default = true, updated_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditLocationSetDefault, Entity: AuditEntityLocation,
			ID: id, Label: name,
			// Spelled out, because it is the consequence an operator may not
			// realise they just chose.
			Summary: "Made " + name + " the default — new stock lands here",
			Before:  map[string]any{"default": wasDefault},
			After:   map[string]any{"default": name},
		})
	})
	if err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

// Delete removes a location. The default cannot go — something has to be the
// answer to "where does this land" — and neither can one that still holds
// stock, which the foreign key would refuse anyway in a sentence about a
// constraint rather than about the shelf.
// The FOR UPDATE below is taken before refuseIfHolding counts, and is what makes
// the DELETE FROM variant_stock further down safe: a transfer arriving between
// the count and that statement would not merely be stranded, it would have its
// units deleted. resolveShelf locks the same row the same way round (D44).
// The wrap also closes a pre-existing window: the bookkeeping rows and the
// location itself used to be two separate statements on the pool, so a crash
// between them left the variant_stock rows gone and the location standing.
// There is no network I/O in the method, so rule 5 is untouched.
func (s *Locations) Delete(ctx context.Context, id int64) error {
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var isDefault bool
		var code, name string
		err := tx.QueryRowContext(ctx,
			`SELECT is_default, code, name FROM locations WHERE id = $1 FOR UPDATE`,
			id).Scan(&isDefault, &code, &name)
		if errors.Is(err, sql.ErrNoRows) {
			return NotFoundf("location %d does not exist", id)
		}
		if err != nil {
			return err
		}
		if isDefault {
			return Conflictf("this is the default location; make another one default first")
		}
		if err := refuseIfHolding(ctx, tx, id, "deleted"); err != nil {
			return err
		}
		// Rows at zero are bookkeeping, not stock, and holding up a deletion for
		// them would make the refusal above unclearable.
		//
		// No ledger row either, for the same reason: refuseIfHolding has just
		// proved every row here is 0/0, so nothing moves and nothing is a
		// movement. The location's history survives the FK's ON DELETE SET NULL
		// with location_code intact, and because the deleted rows netted to
		// zero, every surviving pair still reconciles.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM variant_stock WHERE location_id = $1`, id); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM locations WHERE id = $1`, id)
		if err != nil {
			return translateLocationErr(err, "")
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return NotFoundf("location %d does not exist", id)
		}
		return writeAudit(ctx, tx, auditRecord{
			Action: AuditLocationDelete, Entity: AuditEntityLocation,
			ID: id, Label: name, Summary: "Deleted the location " + name,
			Before: map[string]any{"code": code, "name": name},
		})
	})
}

// refuseIfHolding blocks a change that would strand stock, and names the amount
// so the operator knows how much to move rather than having to go and count.
//
// It takes the transaction rather than the pool, because it used to read
// outside the write it guards and a reservation landing between the two would
// slip past it.
func refuseIfHolding(ctx context.Context, tx *sql.Tx, id int64, verb string) error {
	var units, skus int
	if err := tx.QueryRowContext(ctx, `
		SELECT coalesce(sum(on_hand + reserved), 0), count(*) FILTER (WHERE on_hand <> 0 OR reserved <> 0)
		FROM variant_stock WHERE location_id = $1`, id).Scan(&units, &skus); err != nil {
		return err
	}
	if units != 0 || skus != 0 {
		return Conflictf(
			"this location still holds %d unit(s) across %d SKU(s); move them before it is %s",
			units, skus, verb)
	}
	return nil
}

func marshalAddress(a *Address) ([]byte, error) {
	if a == nil {
		return []byte(`{}`), nil
	}
	b, err := json.Marshal(a)
	if err != nil {
		return nil, Validationf("address is not valid: %v", err)
	}
	return b, nil
}

func translateLocationErr(err error, code string) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "locations_code_key"):
		return Conflictf("a location with the code %q already exists", code)
	case strings.Contains(msg, "locations_code_check"):
		return Validationf("code must not be empty")
	case strings.Contains(msg, "locations_name_check"):
		return Validationf("name must not be empty")
	case strings.Contains(msg, "variant_stock_location_id_fkey"):
		return Conflictf("this location still holds stock; move it before the location goes")
	}
	return err
}
