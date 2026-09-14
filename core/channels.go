package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"
)

// Channels are storefronts over one catalogue.
//
// A channel differs from another in what is published to it and what that
// costs. It deliberately does not differ in currency: D14 settled one
// settlement currency per store and snapshots it onto every order, and
// reopening that on the money path is a much larger decision than this.
//
// A store that never creates one pays nothing. Every read below short-circuits
// when no channel is in play, which is the same bargain the translator makes.
type Channels struct {
	app *App
}

// Channels returns the storefront service.
func (a *App) Channels() *Channels { return &Channels{app: a} }

// Channel is one storefront.
type Channel struct {
	ID     int64  `json:"id"`
	Code   string `json:"code"`
	Name   string `json:"name"`
	Active bool   `json:"active"`
	// Default is the channel a request that names none is served from. Exactly
	// one channel carries it, enforced by a partial unique index.
	Default   bool   `json:"default"`
	Products  int    `json:"products"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// ChannelInput creates a channel.
type ChannelInput struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Active  *bool  `json:"active"`
	Default bool   `json:"default"`
}

// ChannelPatch updates one. A nil field is left alone.
type ChannelPatch struct {
	Code    *string `json:"code"`
	Name    *string `json:"name"`
	Active  *bool   `json:"active"`
	Default *bool   `json:"default"`
}

const channelColumns = `c.id, c.code, c.name, c.active, c.is_default,
	(SELECT count(*) FROM product_channels pc WHERE pc.channel_id = c.id),
	c.created_at, c.updated_at`

func scanChannel(row interface{ Scan(...any) error }) (*Channel, error) {
	c := &Channel{}
	if err := row.Scan(&c.ID, &c.Code, &c.Name, &c.Active, &c.Default,
		&c.Products, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Channels) Create(ctx context.Context, in ChannelInput) (*Channel, error) {
	code, err := normalizeHandle(in.Code)
	if err != nil {
		return nil, Validationf("a channel needs a code: %v", err)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, Validationf("a channel needs a name, which is what an operator sees")
	}

	var id int64
	err = InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		if in.Default {
			// Moving the default rather than adding a second one. Two defaults
			// is a state with no meaning: a request that names no channel would
			// have no answer, and the partial unique index would refuse the
			// insert anyway — clearing first is what makes "make this the
			// default" mean what an operator expects.
			if _, err := tx.ExecContext(ctx,
				`UPDATE channels SET is_default = false WHERE is_default`); err != nil {
				return err
			}
		}
		return tx.QueryRowContext(ctx, `
			INSERT INTO channels (code, name, active, is_default)
			VALUES ($1, $2, $3, $4) RETURNING id`,
			code, name, boolOr(in.Active, true), in.Default).Scan(&id)
	})
	if err != nil {
		return nil, translateChannelErr(err)
	}
	return s.Get(ctx, id)
}

func (s *Channels) Get(ctx context.Context, id int64) (*Channel, error) {
	c, err := scanChannel(s.app.db.QueryRowContext(ctx,
		`SELECT `+channelColumns+` FROM channels c WHERE c.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("channel %d does not exist", id)
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *Channels) List(ctx context.Context) ([]*Channel, error) {
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT `+channelColumns+` FROM channels c ORDER BY c.is_default DESC, c.name, c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Channel{}
	for rows.Next() {
		c, err := scanChannel(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Channels) Update(ctx context.Context, id int64, patch ChannelPatch) (*Channel, error) {
	sets, args := []string{}, []any{}
	add := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
	}
	if patch.Code != nil {
		code, err := normalizeHandle(*patch.Code)
		if err != nil {
			return nil, Validationf("a channel needs a code: %v", err)
		}
		add("code", code)
	}
	if patch.Name != nil {
		name := strings.TrimSpace(*patch.Name)
		if name == "" {
			return nil, Validationf("a channel needs a name")
		}
		add("name", name)
	}
	if patch.Active != nil {
		add("active", *patch.Active)
	}
	if patch.Default != nil {
		add("is_default", *patch.Default)
	}
	if len(sets) == 0 {
		return s.Get(ctx, id)
	}
	add("updated_at", time.Now())
	args = append(args, id)

	err := InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		if patch.Default != nil && *patch.Default {
			if _, err := tx.ExecContext(ctx,
				`UPDATE channels SET is_default = false WHERE is_default AND id <> $1`, id); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx,
			`UPDATE channels SET `+strings.Join(sets, ", ")+
				` WHERE id = $`+strconv.Itoa(len(args)), args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return NotFoundf("channel %d does not exist", id)
		}
		return nil
	})
	if err != nil {
		return nil, translateChannelErr(err)
	}
	return s.Get(ctx, id)
}

// Delete removes a channel. Its publication rows go with it, which widens the
// products that named it back to every channel — the state they were in before
// somebody narrowed them, and the only honest answer once the narrowing is
// gone. Carts and orders keep their history and lose only the reference.
func (s *Channels) Delete(ctx context.Context, id int64) error {
	res, err := s.app.db.ExecContext(ctx, `DELETE FROM channels WHERE id = $1`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return NotFoundf("channel %d does not exist", id)
	}
	return nil
}

// Publish narrows a product to the given channels. An empty list clears the
// narrowing, which puts the product back in every channel rather than in none —
// see M35 for why that is the only safe reading of an empty set.
func (s *Channels) Publish(ctx context.Context, productID int64, channelIDs []int64) error {
	return InTx(ctx, s.app.db, func(tx *sql.Tx) error {
		var exists bool
		if err := tx.QueryRowContext(ctx,
			`SELECT true FROM products WHERE id = $1`, productID).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return NotFoundf("product %d does not exist", productID)
			}
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM product_channels WHERE product_id = $1`, productID); err != nil {
			return err
		}
		for _, id := range channelIDs {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO product_channels (product_id, channel_id) VALUES ($1, $2)
				ON CONFLICT DO NOTHING`, productID, id); err != nil {
				return translateChannelErr(err)
			}
		}
		return nil
	})
}

// ChannelsOf returns the channels a product has been narrowed to. Empty means
// every channel.
func (s *Channels) ChannelsOf(ctx context.Context, productID int64) ([]int64, error) {
	rows, err := s.app.db.QueryContext(ctx,
		`SELECT channel_id FROM product_channels WHERE product_id = $1 ORDER BY channel_id`, productID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// ------------------------------------------------------------- resolution

// Resolve turns the code a request carried into the channel it will be served
// from.
//
// An empty code takes the default; a store with no channels at all resolves to
// nothing and every read behaves exactly as it did before channels existed,
// which is the property that makes this safe to ship to stores that will never
// use it.
//
// An unrecognised or inactive code is refused rather than quietly served the
// default. A typo in a storefront's configuration should fail at its first
// request, not sell the wrong catalogue until somebody reads the figures.
func (s *Channels) Resolve(ctx context.Context, code string) (*Channel, error) {
	code = strings.TrimSpace(strings.ToLower(code))

	if code == "" {
		c, err := scanChannel(s.app.db.QueryRowContext(ctx,
			`SELECT `+channelColumns+` FROM channels c WHERE c.is_default AND c.active`))
		if errors.Is(err, sql.ErrNoRows) {
			// No default, or no channels at all. Either way there is nothing to
			// narrow by, and the caller reads the whole catalogue.
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return c, nil
	}

	c, err := scanChannel(s.app.db.QueryRowContext(ctx,
		`SELECT `+channelColumns+` FROM channels c WHERE c.code = $1`, code))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, Validationf("%q is not a channel of this store", code)
	}
	if err != nil {
		return nil, err
	}
	if !c.Active {
		// A refusal rather than an empty catalogue: an empty shop reads as a
		// broken import, and somebody would go looking in the wrong place.
		return nil, Validationf("the %q channel is not selling at the moment", code)
	}
	return c, nil
}

// channelFilter is the publication rule as SQL: a product is in a channel when
// it names that channel, or names none at all.
//
// Written as NOT EXISTS rather than a join so that the "names none" half cannot
// be lost — a join would quietly drop every product nobody has narrowed, which
// is most of the catalogue in a store that has just adopted channels.
func channelFilter(param string) string {
	return `(
		EXISTS (SELECT 1 FROM product_channels pc
		        WHERE pc.product_id = p.id AND pc.channel_id = ` + param + `)
	 OR NOT EXISTS (SELECT 1 FROM product_channels pc WHERE pc.product_id = p.id)
	)`
}

func translateChannelErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "channels_code_key"):
		return Conflictf("a channel with that code already exists")
	case strings.Contains(msg, "channels_one_default_idx"):
		return Conflictf("another channel is already the default")
	case strings.Contains(msg, "product_channels_channel_id_fkey"):
		return Validationf("that channel does not exist")
	}
	return err
}
