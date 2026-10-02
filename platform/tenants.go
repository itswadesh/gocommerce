package platform

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// ProvisionInput creates a store.
type ProvisionInput struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Currency and Languages override the platform's template for this
	// store. A store's currency is fixed once it has orders, as the engine's
	// always is, so choose it here.
	Currency  string   `json:"currency"`
	Languages []string `json:"languages"`
	// Domains are custom hosts to attach at once; <slug>.<BaseDomain> is
	// always attached besides.
	Domains []string `json:"domains"`
	// The store's first operator, an owner. With no password one is
	// generated and returned once.
	OwnerEmail    string `json:"owner_email"`
	OwnerPassword string `json:"owner_password"`
}

// Provisioned is what creating a store hands back, once: the credentials are
// not kept anywhere they can be read again.
type Provisioned struct {
	Tenant *Tenant `json:"tenant"`
	// OwnerPassword is set only when the platform generated it.
	OwnerEmail    string `json:"owner_email"`
	OwnerPassword string `json:"owner_password,omitempty"`
	// AdminToken is the store's own static API token, for the platform's
	// automation and support. It opens this store's admin API and no other.
	AdminToken string `json:"admin_token"`
}

// The two lists come back as JSON: database/sql cannot scan a text[] into a
// []string, and JSON needs nothing beyond the standard library to read.
const tenantColumns = `t.id, t.slug, t.name, t.status, t.currency, to_json(t.languages), t.schema_name, t.admin_token,
	t.created_at, t.updated_at,
	coalesce((SELECT json_agg(d.domain ORDER BY d.position, d.created_at) FROM domains d WHERE d.tenant_id = t.id), '[]'::json)`

func scanTenant(row interface{ Scan(...any) error }) (*Tenant, error) {
	t := &Tenant{}
	var langsJSON, domainsJSON []byte
	if err := row.Scan(&t.ID, &t.Slug, &t.Name, &t.Status, &t.Currency, &langsJSON, &t.schema,
		&t.adminToken, &t.CreatedAt, &t.UpdatedAt, &domainsJSON); err != nil {
		return nil, err
	}
	var langs, domains []string
	if err := json.Unmarshal(langsJSON, &langs); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(domainsJSON, &domains); err != nil {
		return nil, err
	}
	t.Languages, t.Domains = nonNilStrings(langs), nonNilStrings(domains)
	return t, nil
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (p *Platform) readTenants(ctx context.Context, slug string) ([]*Tenant, error) {
	q := `SELECT ` + tenantColumns + ` FROM tenants t`
	args := []any{}
	if slug != "" {
		q += ` WHERE t.slug = $1`
		args = append(args, slug)
	}
	rows, err := p.db.QueryContext(ctx, q+` ORDER BY t.id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Tenant{}
	for rows.Next() {
		t, err := scanTenant(rows)
		if err != nil {
			return nil, err
		}
		t.Host = p.primaryHost(t)
		out = append(out, t)
	}
	return out, rows.Err()
}

// Tenants lists every store, oldest first.
func (p *Platform) Tenants(ctx context.Context) ([]*Tenant, error) { return p.readTenants(ctx, "") }

// Tenant reads one store.
func (p *Platform) Tenant(ctx context.Context, slug string) (*Tenant, error) {
	list, err := p.readTenants(ctx, slug)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, gocommerce.NotFoundf("there is no store %q", slug)
	}
	return list[0], nil
}

// Provision creates a store: its schema, its engine with every migration
// applied, its owner and its domains. Anything that fails part-way is undone,
// so a slug is never left half-taken.
func (p *Platform) Provision(ctx context.Context, in ProvisionInput) (*Provisioned, error) {
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Name = strings.TrimSpace(in.Name)
	in.OwnerEmail = strings.ToLower(strings.TrimSpace(in.OwnerEmail))
	switch {
	case !slugRE.MatchString(in.Slug) || len(in.Slug) > 40:
		return nil, gocommerce.Validationf("slug is lower-case letters, digits and single hyphens, at most 40 characters")
	case in.Name == "":
		return nil, gocommerce.Validationf("a store needs a name")
	case !strings.Contains(in.OwnerEmail, "@"):
		return nil, gocommerce.Validationf("owner_email is required")
	case in.Currency != "" && len(strings.TrimSpace(in.Currency)) != 3:
		return nil, gocommerce.Validationf("currency is an ISO 4217 code, like USD")
	}
	reserved := map[string]bool{"platform": true, "api": true, "www": true, "admin": true}
	if reserved[in.Slug] {
		return nil, gocommerce.Validationf("%q is reserved for the platform's own hosts", in.Slug)
	}
	domains, err := p.cleanDomains(in.Domains)
	if err != nil {
		return nil, err
	}
	password := in.OwnerPassword
	generated := password == ""
	if generated {
		if password, err = newSecret(18); err != nil {
			return nil, err
		}
	}
	token, err := newSecret(32)
	if err != nil {
		return nil, err
	}
	schema := p.schemaFor(in.Slug)

	var id int64
	err = gocommerce.InTx(ctx, p.db, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO tenants (slug, name, schema_name, currency, languages, admin_token)
			VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
			in.Slug, in.Name, schema, strings.ToUpper(strings.TrimSpace(in.Currency)),
			nonNilStrings(in.Languages), token).Scan(&id); err != nil {
			if strings.Contains(err.Error(), "tenants_slug_key") {
				return gocommerce.Conflictf("a store called %q already exists", in.Slug)
			}
			return err
		}
		for i, d := range domains {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO domains (domain, tenant_id, position) VALUES ($1, $2, $3)`, d, id, i); err != nil {
				if strings.Contains(err.Error(), "domains_pkey") {
					return gocommerce.Conflictf("%s already belongs to another store", d)
				}
				return err
			}
		}
		_, err := tx.ExecContext(ctx, `CREATE SCHEMA `+schema)
		return err
	})
	if err != nil {
		return nil, err
	}
	undo := func() {
		p.retire(in.Slug)
		p.forget(in.Slug)
		if _, err := p.db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+schema+` CASCADE`); err != nil {
			p.log.Error("platform: could not drop a half-made store's schema", "store", in.Slug, "error", err)
		}
		if _, err := p.db.ExecContext(context.Background(), `DELETE FROM tenants WHERE id = $1`, id); err != nil {
			p.log.Error("platform: could not delete a half-made store", "store", in.Slug, "error", err)
		}
	}

	t, err := p.Tenant(ctx, in.Slug)
	if err != nil {
		undo()
		return nil, err
	}
	if err := p.boot(ctx, t); err != nil {
		undo()
		return nil, fmt.Errorf("platform: boot %s: %w", in.Slug, err)
	}
	if _, err := p.App(in.Slug).Superusers().Create(ctx, in.OwnerEmail, password, gocommerce.RoleOwner); err != nil {
		undo()
		return nil, err
	}
	p.remember(t)
	p.log.Info("platform: store created", "store", in.Slug, "schema", schema)
	out := &Provisioned{Tenant: t, OwnerEmail: in.OwnerEmail, AdminToken: token}
	if generated {
		out.OwnerPassword = password
	}
	return out, nil
}

func (p *Platform) cleanDomains(in []string) ([]string, error) {
	out := []string{}
	seen := map[string]bool{}
	for _, d := range in {
		d = normalizeHost(d)
		if d == "" || seen[d] {
			continue
		}
		if strings.ContainsAny(d, "/: ") || !strings.Contains(d, ".") {
			return nil, gocommerce.Validationf("%q is not a host name", d)
		}
		if contains(p.cfg.PlatformHosts, d) || contains(p.cfg.APIHosts, d) {
			return nil, gocommerce.Validationf("%s is one of the platform's own hosts", d)
		}
		if p.cfg.BaseDomain != "" && strings.HasSuffix(d, "."+p.cfg.BaseDomain) {
			return nil, gocommerce.Validationf("%s is under the platform's base domain; every store already has <slug>.%s", d, p.cfg.BaseDomain)
		}
		seen[d] = true
		out = append(out, d)
	}
	return out, nil
}

// SetStatus suspends or resumes a store. Suspending stops its engine and its
// background work, and its hosts answer 503; its data is untouched. Resuming
// boots it again.
func (p *Platform) SetStatus(ctx context.Context, slug, status string) (*Tenant, error) {
	if status != StatusActive && status != StatusSuspended {
		return nil, gocommerce.Validationf("status is active or suspended")
	}
	if _, err := p.Tenant(ctx, slug); err != nil {
		return nil, err
	}
	if _, err := p.db.ExecContext(ctx,
		`UPDATE tenants SET status = $2, updated_at = now() WHERE slug = $1`, slug, status); err != nil {
		return nil, err
	}
	t, err := p.Tenant(ctx, slug)
	if err != nil {
		return nil, err
	}
	p.remember(t)
	if status == StatusSuspended {
		p.retire(slug)
	} else if p.App(slug) == nil {
		if err := p.boot(ctx, t); err != nil {
			return nil, fmt.Errorf("platform: resume %s: %w", slug, err)
		}
	}
	return t, nil
}

// Rename changes a store's display name.
func (p *Platform) Rename(ctx context.Context, slug, name string) (*Tenant, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, gocommerce.Validationf("a store needs a name")
	}
	res, err := p.db.ExecContext(ctx,
		`UPDATE tenants SET name = $2, updated_at = now() WHERE slug = $1`, slug, name)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, gocommerce.NotFoundf("there is no store %q", slug)
	}
	t, err := p.Tenant(ctx, slug)
	if err == nil {
		p.remember(t)
	}
	return t, err
}

// Delete removes a store and everything in it: its schema, its domains and
// its uploads. It must be suspended first, so deleting is always a second,
// separate decision rather than one request that cannot be taken back.
func (p *Platform) Delete(ctx context.Context, slug string) error {
	t, err := p.Tenant(ctx, slug)
	if err != nil {
		return err
	}
	if t.Status != StatusSuspended {
		return gocommerce.Conflictf("suspend %s before deleting it", slug)
	}
	p.retire(slug)
	if _, err := p.db.ExecContext(ctx, `DROP SCHEMA IF EXISTS `+t.schema+` CASCADE`); err != nil {
		return err
	}
	if _, err := p.db.ExecContext(ctx, `DELETE FROM tenants WHERE id = $1`, t.ID); err != nil {
		return err
	}
	p.forget(slug)
	if p.cfg.MediaRoot != "" {
		// The slug is [a-z0-9-] only, so this cannot climb out of the root.
		if err := os.RemoveAll(filepath.Join(p.cfg.MediaRoot, slug)); err != nil {
			p.log.Warn("platform: could not remove a deleted store's uploads", "store", slug, "error", err)
		}
	}
	p.log.Info("platform: store deleted", "store", slug)
	return nil
}

// AddDomain attaches a custom host to a store. The store answers on it at
// once; serving it over TLS is the proxy's job, which asks TLSAllowed first.
func (p *Platform) AddDomain(ctx context.Context, slug, domain string) (*Tenant, error) {
	clean, err := p.cleanDomains([]string{domain})
	if err != nil {
		return nil, err
	}
	if len(clean) == 0 {
		return nil, gocommerce.Validationf("domain is required")
	}
	t, err := p.Tenant(ctx, slug)
	if err != nil {
		return nil, err
	}
	if _, err := p.db.ExecContext(ctx, `
		INSERT INTO domains (domain, tenant_id, position)
		VALUES ($1, $2, (SELECT coalesce(max(position), -1) + 1 FROM domains WHERE tenant_id = $2))`,
		clean[0], t.ID); err != nil {
		if strings.Contains(err.Error(), "domains_pkey") {
			return nil, gocommerce.Conflictf("%s already belongs to a store", clean[0])
		}
		return nil, err
	}
	t, err = p.Tenant(ctx, slug)
	if err == nil {
		p.remember(t)
	}
	return t, err
}

// RemoveDomain detaches a custom host.
func (p *Platform) RemoveDomain(ctx context.Context, slug, domain string) (*Tenant, error) {
	t, err := p.Tenant(ctx, slug)
	if err != nil {
		return nil, err
	}
	res, err := p.db.ExecContext(ctx,
		`DELETE FROM domains WHERE domain = $1 AND tenant_id = $2`, normalizeHost(domain), t.ID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, gocommerce.NotFoundf("%s is not attached to %s", domain, slug)
	}
	t, err = p.Tenant(ctx, slug)
	if err == nil {
		p.remember(t)
	}
	return t, err
}

// TLSAllowed says whether a certificate should be issued for a host: one of
// the platform's, or one a store answers on. A proxy issuing certificates on
// demand asks this first, so nobody can make it fetch one for a domain that
// merely points here.
func (p *Platform) TLSAllowed(host string) bool {
	host = normalizeHost(host)
	if contains(p.cfg.PlatformHosts, host) || contains(p.cfg.APIHosts, host) {
		return true
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.hosts[host]
	return ok
}

// AddOwner gives somebody an owner's login on a store — the platform's way
// into a store whose operators are locked out, or a second owner at a
// customer's request. With no password one is generated and returned.
func (p *Platform) AddOwner(ctx context.Context, slug, email, password string) (string, error) {
	app := p.App(slug)
	if app == nil {
		if _, err := p.Tenant(ctx, slug); err != nil {
			return "", err
		}
		return "", gocommerce.Conflictf("%s is suspended; resume it first", slug)
	}
	generated := password == ""
	if generated {
		var err error
		if password, err = newSecret(18); err != nil {
			return "", err
		}
	}
	if _, err := app.Superusers().Create(ctx, strings.ToLower(strings.TrimSpace(email)), password, gocommerce.RoleOwner); err != nil {
		return "", err
	}
	if generated {
		return password, nil
	}
	return "", nil
}

// RotateToken replaces a store's static admin token, restarting its engine
// with the new one. The old token stops working when this returns.
func (p *Platform) RotateToken(ctx context.Context, slug string) (string, error) {
	t, err := p.Tenant(ctx, slug)
	if err != nil {
		return "", err
	}
	token, err := newSecret(32)
	if err != nil {
		return "", err
	}
	if _, err := p.db.ExecContext(ctx,
		`UPDATE tenants SET admin_token = $2, updated_at = now() WHERE id = $1`, t.ID, token); err != nil {
		return "", err
	}
	if t.Status == StatusActive {
		p.retire(slug)
		t.adminToken = token
		if err := p.boot(ctx, t); err != nil {
			return "", fmt.Errorf("platform: restart %s with its new token: %w", slug, err)
		}
	}
	return token, nil
}
