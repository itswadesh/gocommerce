package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
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
	OrderPrefix *string `json:"order_prefix"`
	// Demo masks personal data on this store's admin routes and freezes who
	// can sign in to it, as Config.Demo does for the whole install.
	Demo      bool      `json:"demo"`
	CreatedAt time.Time `json:"created_at"`
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
// own, else the default. Row security does the filtering (D83); this is for
// the few places that need the id itself, such as a link that names the
// store or the sequence a store's order numbers come from.
func (a *App) storeOf(ctx context.Context) string {
	if s, ok := StoreFrom(ctx); ok {
		return s
	}
	return a.defaultStore
}

// NoDefaultStore, as Config.DefaultStore, runs the engine as `serve` does,
// with no default store, where a caller that fills in a default (gctest)
// would otherwise choose one.
const NoDefaultStore = "none"

func (a *App) resolveDefaultStore(ctx context.Context) error {
	a.defaultStore = a.cfg.DefaultStore
	if a.defaultStore == NoDefaultStore {
		a.defaultStore = ""
	}
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

const storeColumns = `id, slug, name, status, order_prefix, demo, created_at`

// storeSlugRE is the shape the stores table's CHECK allows.
var storeSlugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func scanStore(row interface{ Scan(...any) error }) (*Store, error) {
	var st Store
	if err := row.Scan(&st.ID, &st.Slug, &st.Name, &st.Status, &st.OrderPrefix, &st.Demo, &st.CreatedAt); err != nil {
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

// SetDemo marks a store as a demo, or a live store again. It is the install
// operator's switch (gocommerce store demo), with no route of its own: a demo
// store's visitors are signed in to it, and a switch they could reach would
// let the first of them turn the masking off.
func (s *Stores) SetDemo(ctx context.Context, id string, on bool) error {
	res, err := s.db.ExecContext(ctx, `UPDATE stores SET demo = $2 WHERE id = $1`, id, on)
	if err != nil {
		return Internalf(err, "mark the store as a demo")
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return NotFoundf("store not found")
	}
	return nil
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

// Seed gives a new store the rows the migrations gave store #1: the four
// built-in roles, an empty store profile and the default location. Without
// the location a product cannot be stocked, and without the roles nobody can
// be made its owner.
func (s *Stores) Seed(ctx context.Context, storeID string) error {
	ctx = WithStore(ctx, storeID)
	if err := InTx(ctx, s.db, func(tx *sql.Tx) error {
		return seedCore(ctx, tx, storeID)
	}); err != nil {
		return err
	}
	return s.seedModules(ctx)
}

// seedCore writes the rows a store cannot run without, inside the caller's
// transaction so Create can write them with the store itself.
func seedCore(ctx context.Context, tx *sql.Tx, storeID string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO roles (store_id, key, builtin)
		VALUES ($1, 'owner', true), ($1, 'manager', true), ($1, 'staff', true), ($1, 'vendor', true)
		ON CONFLICT (store_id, key) DO NOTHING`, storeID); err != nil {
		return Internalf(err, "seed roles")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO store_profile (store_id, name)
		SELECT $1, name FROM stores WHERE id = $1
		ON CONFLICT (store_id) DO NOTHING`, storeID); err != nil {
		return Internalf(err, "seed store profile")
	}
	if _, err := tx.ExecContext(ctx,
		`CREATE SEQUENCE IF NOT EXISTS `+orderSequence(storeID)); err != nil {
		return Internalf(err, "seed order numbers")
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO locations (store_id, code, name, is_default, priority)
		VALUES ($1, 'default', 'Main location', true, 0)
		ON CONFLICT DO NOTHING`, storeID); err != nil {
		return Internalf(err, "seed location")
	}
	return nil
}

// seedModules writes each module's own starting rows, for modules that have
// any, bound to the store ctx is.
func (s *Stores) seedModules(ctx context.Context) error {
	for _, mod := range s.app.modules {
		if seeder, ok := mod.(StoreSeeder); ok {
			if err := seeder.SeedStore(ctx); err != nil {
				return Internalf(err, "seed %s", mod.Name())
			}
		}
	}
	return nil
}

// orderSequence names the sequence a store's order numbers come from. Store
// ids are [0-9A-Z] after "store_", so the name needs no quoting.
func orderSequence(storeID string) string {
	return "order_number_" + strings.ToLower(storeID)
}

// StoreDeleter is a module that keeps something of a store's outside the
// database — documents in a search index, files on disk — and removes it when
// the store is deleted. Rows need no hook: they cascade with the store.
type StoreDeleter interface {
	DeleteStore(ctx context.Context, storeID string) error
}

// Delete removes a store and everything it owns. Every store table references
// stores(id) with ON DELETE CASCADE, and a cascade is a referential action, so
// it reaches all of the store's rows whatever row security would let a query
// see. Its order sequence goes in the same transaction. Modules then clear
// what they keep elsewhere; a failure there is logged rather than undoing a
// delete that has already happened.
func (s *Stores) Delete(ctx context.Context, id string) error {
	bound := WithStore(ctx, id)
	// Its files, read while its rows still name them: after the delete
	// nothing says which files were the store's.
	keys, err := s.mediaKeys(bound)
	if err != nil {
		return err
	}
	err = InTx(bound, s.db, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(bound, `DELETE FROM stores WHERE id = $1`, id)
		if err != nil {
			return Internalf(err, "delete store")
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return NotFoundf("store not found")
		}
		if _, err := tx.ExecContext(bound, `DROP SEQUENCE IF EXISTS `+orderSequence(id)); err != nil {
			return Internalf(err, "drop the store's order numbers")
		}
		return nil
	})
	if err != nil {
		return err
	}
	// No media store, no files to remove — rows can outlive the
	// configuration that wrote them.
	if s.app.mediaStore == nil {
		keys = nil
	}
	for _, key := range keys {
		if err := s.app.mediaStore.Delete(bound, key); err != nil {
			s.app.log.Error("could not delete a deleted store's file", "store", id, "key", key, "err", err)
		}
	}
	if d, ok := s.app.mediaStore.(StoreDeleter); ok {
		if err := d.DeleteStore(bound, id); err != nil {
			s.app.log.Error("could not remove a deleted store's media folder", "store", id, "err", err)
		}
	}
	for _, mod := range s.app.modules {
		if d, ok := mod.(StoreDeleter); ok {
			if err := d.DeleteStore(bound, id); err != nil {
				s.app.log.Error("a module could not clear a deleted store", "module", mod.Name(), "store", id, "err", err)
			}
		}
	}
	return nil
}

// mediaKeys lists the storage keys of the files ctx's store holds.
func (s *Stores) mediaKeys(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT storage_key FROM media WHERE storage_key <> ''")
	if err != nil {
		return nil, Internalf(err, "list the store's files")
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, Internalf(err, "list the store's files")
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

// StoreSeeder is a module with rows a new store starts with — what its
// migrations seeded for the stores that existed when they ran. SeedStore is
// called once per store opened afterwards, bound to that store.
type StoreSeeder interface {
	SeedStore(ctx context.Context) error
}

// Create opens a new store and seeds it (Seed), so it can stock and sell from
// its first minute. The slug is how the store is named in a header and in its
// host, so it is checked here rather than left to the table's CHECK.
func (s *Stores) Create(ctx context.Context, name, slug string) (*Store, error) {
	return s.CreateFor(ctx, name, slug, 0)
}

// CreateFor is Create with ownerID (when not zero) made the store's owner. The
// store row, the rows it cannot run without and its owner are one
// transaction: a store nobody can enter is never left behind. Module seeds
// follow, and a failure there takes the store back out.
func (s *Stores) CreateFor(ctx context.Context, name, slug string, ownerID int64) (*Store, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	name = strings.TrimSpace(name)
	if !storeSlugRE.MatchString(slug) {
		return nil, Validationf("a store's slug is lowercase letters, digits and hyphens, starting with a letter or digit")
	}
	if name == "" {
		name = slug
	}
	id, err := NewStoreID()
	if err != nil {
		return nil, Internalf(err, "make a store id")
	}
	bound := WithStore(ctx, id)
	err = InTx(bound, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(bound,
			`INSERT INTO stores (id, slug, name) VALUES ($1, $2, $3)`, id, slug, name); err != nil {
			if isUniqueViolation(err) {
				return Conflictf("there is already a store called %q", slug)
			}
			return Internalf(err, "create store")
		}
		if err := seedCore(bound, tx, id); err != nil {
			return err
		}
		if ownerID == 0 {
			return nil
		}
		if _, err := tx.ExecContext(bound, `
			INSERT INTO store_members (store_id, superuser_id, role) VALUES ($1, $2, $3)`,
			id, ownerID, RoleOwner); err != nil {
			return Internalf(err, "make the creator the store's owner")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := s.seedModules(bound); err != nil {
		// Without its modules' rows the store is half made; take it back out.
		_, _ = s.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM stores WHERE id = $1`, id)
		return nil, err
	}
	return s.Get(ctx, id)
}
