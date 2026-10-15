package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Store is one shop the engine serves.
type Store struct {
	ID     string `json:"id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// OrderPrefix overrides Config.OrderPrefix for this store; nil means the
	// configured one.
	OrderPrefix *string   `json:"order_prefix"`
	CreatedAt   time.Time `json:"created_at"`
}

// Membership is a person's standing in one store.
type Membership struct {
	StoreID  string `json:"store_id"`
	Role     string `json:"role"`
	VendorID *int64 `json:"vendor_id,omitempty"`
}

// Stores reads the global list of stores. Every table it touches is global,
// so nothing here depends on the store a context is bound to.
type Stores struct {
	app *App
	db  *sql.DB
}

// Stores returns the store directory.
func (a *App) Stores() *Stores { return a.stores }

// DefaultStore is the store unbound work runs in: Config.DefaultStore
// resolved to an id, or empty when there is none (always, under `serve`).
func (a *App) DefaultStore() string { return a.defaultStore }

// storeOf is the store a statement run with ctx is bound to: the context's
// own, else the default. Code passes it as the explicit store_id parameter
// every statement on a store table carries.
func (a *App) storeOf(ctx context.Context) string {
	if s, ok := StoreFrom(ctx); ok {
		return s
	}
	return a.defaultStore
}

func (a *App) resolveDefaultStore(ctx context.Context) error {
	a.defaultStore = a.cfg.DefaultStore
	if a.defaultStore == "first" {
		if err := a.db.QueryRowContext(ctx,
			`SELECT id FROM stores ORDER BY created_at, id LIMIT 1`).Scan(&a.defaultStore); err != nil {
			return fmt.Errorf("gocommerce: find the first store: %w", err)
		}
	}
	if c := storeConnectorOf(a.db); c != nil {
		c.SetFallback(a.defaultStore)
	}
	return nil
}

const storeColumns = `id, slug, name, status, order_prefix, created_at`

func scanStore(row interface{ Scan(...any) error }) (*Store, error) {
	var st Store
	if err := row.Scan(&st.ID, &st.Slug, &st.Name, &st.Status, &st.OrderPrefix, &st.CreatedAt); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Stores) one(ctx context.Context, where string, arg any) (*Store, error) {
	st, err := scanStore(s.db.QueryRowContext(ctx, `SELECT `+storeColumns+` FROM stores WHERE `+where, arg))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("store not found")
	}
	if err != nil {
		return nil, Internalf(err, "read store")
	}
	return st, nil
}

// Get returns a store by id.
func (s *Stores) Get(ctx context.Context, id string) (*Store, error) {
	if !ValidStoreID(id) {
		return nil, NotFoundf("store not found")
	}
	return s.one(ctx, `id = $1`, id)
}

// BySlug returns a store by its slug.
func (s *Stores) BySlug(ctx context.Context, slug string) (*Store, error) {
	return s.one(ctx, `slug = $1`, strings.ToLower(slug))
}

// ByHost returns the store a host name belongs to: an attached domain first,
// then <slug>.<Config.BaseDomain>.
func (s *Stores) ByHost(ctx context.Context, host string) (*Store, error) {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	st, err := s.one(ctx, `id = (SELECT store_id FROM store_domains WHERE host = $1)`, host)
	if err == nil || !errors.Is(err, ErrNotFound) {
		return st, err
	}
	if base := strings.ToLower(s.app.cfg.BaseDomain); base != "" {
		if slug, ok := strings.CutSuffix(host, "."+base); ok && slug != "" && !strings.Contains(slug, ".") {
			return s.BySlug(ctx, slug)
		}
	}
	return nil, NotFoundf("store not found")
}

// Active lists the stores that are serving, oldest first.
func (s *Stores) Active(ctx context.Context) ([]Store, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+storeColumns+` FROM stores WHERE status = 'active' ORDER BY created_at, id`)
	if err != nil {
		return nil, Internalf(err, "list stores")
	}
	defer rows.Close()
	var out []Store
	for rows.Next() {
		st, err := scanStore(rows)
		if err != nil {
			return nil, Internalf(err, "scan store")
		}
		out = append(out, *st)
	}
	return out, rows.Err()
}

// Count is how many stores exist, serving or not.
func (s *Stores) Count(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM stores`).Scan(&n); err != nil {
		return 0, Internalf(err, "count stores")
	}
	return n, nil
}

// MembershipsOf lists the stores a person is staff of, oldest store first.
func (s *Stores) MembershipsOf(ctx context.Context, superuserID int64) ([]Membership, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.store_id, m.role, m.vendor_id
		FROM store_members m JOIN stores st ON st.id = m.store_id
		WHERE m.superuser_id = $1 ORDER BY st.created_at, st.id`, superuserID)
	if err != nil {
		return nil, Internalf(err, "list memberships")
	}
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		var m Membership
		if err := rows.Scan(&m.StoreID, &m.Role, &m.VendorID); err != nil {
			return nil, Internalf(err, "scan membership")
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// migrate0053Stores adds the global store tables, makes the install as it was
// into store #1, and moves each operator's role and seller from the person
// onto their membership of that store.
//
//storelint:global creates the store directory
func migrate0053Stores(ctx context.Context, tx *sql.Tx) error {
	id, err := NewStoreID()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, migration0053Stores); err != nil {
		return err
	}
	// Store #1's slug comes from the store's name when there is one, because
	// that is what its owner will recognise in a store list.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO stores (id, slug, name)
		SELECT $1,
		       left(COALESCE(NULLIF(trim(both '-' from regexp_replace(lower(p.name), '[^a-z0-9]+', '-', 'g')), ''), 'store'), 63),
		       COALESCE(p.name, '')
		FROM (SELECT (SELECT name FROM store_profile WHERE id = 1) AS name) p`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO store_members (store_id, superuser_id, role, vendor_id)
		SELECT $1, id, role, vendor_id FROM superusers`, id); err != nil {
		return err
	}
	// Dropping the columns drops the role CHECK, the role foreign key and the
	// vendor index with them; their rules now live on store_members.
	if _, err := tx.ExecContext(ctx, `
		ALTER TABLE superusers DROP COLUMN vendor_id;
		ALTER TABLE superusers DROP COLUMN role;`); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO api_key_index (prefix, store_id) SELECT prefix, $1 FROM api_keys`, id)
	return err
}

// The tables every store shares. They carry no row security: they are the
// directory a request is resolved against before any store is known.
const migration0053Stores = `
CREATE TABLE stores (
    id           text        PRIMARY KEY CHECK (id ~ '^store_[0-9A-HJKMNP-TV-Z]{26}$'),
    slug         text        NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
    name         text        NOT NULL DEFAULT '',
    status       text        NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),
    order_prefix text,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE store_domains (
    host       text        PRIMARY KEY CHECK (host = lower(host) AND host <> ''),
    store_id   text        NOT NULL REFERENCES stores (id) ON DELETE CASCADE,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX store_domains_store_idx ON store_domains (store_id);
CREATE TABLE store_counters (
    store_id text   NOT NULL REFERENCES stores (id) ON DELETE CASCADE,
    name     text   NOT NULL,
    value    bigint NOT NULL,
    PRIMARY KEY (store_id, name)
);
CREATE TABLE api_key_index (
    prefix   text PRIMARY KEY,
    store_id text NOT NULL REFERENCES stores (id) ON DELETE CASCADE
);
-- A person's standing in one store. The role and, for a seller's login, the
-- seller move here from superusers: one person can be an owner in one store
-- and staff in another.
CREATE TABLE store_members (
    store_id     text        NOT NULL REFERENCES stores (id) ON DELETE CASCADE,
    superuser_id bigint      NOT NULL REFERENCES superusers (id) ON DELETE CASCADE,
    role         text        NOT NULL,
    vendor_id    bigint,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (store_id, superuser_id),
    CONSTRAINT store_members_vendor_account CHECK ((role = 'vendor') = (vendor_id IS NOT NULL)),
    -- Named as the superusers keys were, so a role deleted mid-assignment is
    -- still reported as a missing role (roleGone), and removing a seller
    -- still takes their logins. 0054 makes both keys include the store.
    CONSTRAINT store_members_role_fkey FOREIGN KEY (role) REFERENCES roles (key) ON DELETE RESTRICT,
    CONSTRAINT store_members_vendor_fkey FOREIGN KEY (vendor_id) REFERENCES vendors (id) ON DELETE CASCADE
);
CREATE INDEX store_members_superuser_idx ON store_members (superuser_id);
`
