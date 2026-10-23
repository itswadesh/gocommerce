package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// A store's own domains (D83, Part 4). A domain is added, proven by a DNS TXT
// record the engine looks up itself, and only then routes requests to the
// store (Stores.ByHost). Whether a domain is verified, and which one is
// primary, are the engine's to say: neither is ever read from a request.
//
// store_domains is a global table — a host belongs to exactly one store across
// the install — so these handlers name the store in every statement rather
// than leaning on row security, which does not cover it.

// Domain is one of a store's domains, with the record that proves it.
type Domain struct {
	Host       string     `json:"host"`
	Verified   bool       `json:"verified"`
	VerifiedAt *time.Time `json:"verified_at"`
	Primary    bool       `json:"primary"`
	// TXTName and TXTValue are the record to create at the domain's DNS
	// host: verifying looks for TXTValue among the TXT records at TXTName.
	TXTName   string    `json:"txt_name"`
	TXTValue  string    `json:"txt_value"`
	CreatedAt time.Time `json:"created_at"`
	// PointType, PointName and PointValue are the record that sends the
	// domain's visitors to this server — A, AAAA or CNAME — when the install
	// names its address (Config.DomainTarget).
	PointType  string `json:"point_type,omitempty"`
	PointName  string `json:"point_name,omitempty"`
	PointValue string `json:"point_value,omitempty"`
}

const (
	txtPrefix      = "_gocommerce."
	txtValuePrefix = "gocommerce-verify="
	// How long a verification waits on DNS before saying it found nothing.
	txtLookupTimeout = 10 * time.Second
)

// errNotVerified is what a domain without its record gets: the record was not
// found, or the domain was asked to do what only a verified one may.
var errNotVerified = func(msg string) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "not_verified", Message: msg}
}

// migration0059StoreDomainVerification gives a domain its proof: the token its
// TXT record must carry, when it was found, and which domain is the store's
// primary. Domains already attached were routing before verification existed,
// so they count as verified rather than going dark on upgrade.
const migration0059StoreDomainVerification = `
ALTER TABLE store_domains
    ADD COLUMN verify_token text        NOT NULL DEFAULT '',
    ADD COLUMN verified_at  timestamptz,
    ADD COLUMN is_primary   boolean     NOT NULL DEFAULT false;
UPDATE store_domains SET verified_at = created_at;
CREATE UNIQUE INDEX store_domains_one_primary ON store_domains (store_id) WHERE is_primary;
`

var hostLabelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// normalizeHost is a domain as a host name a browser would send: lower case,
// no trailing dot, labels of letters, digits and hyphens. Anything with a
// scheme, a port, a path, an IP address, or no dot at all is refused.
func (a *App) normalizeHost(raw string) (string, error) {
	h := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	bad := func() (string, error) {
		return "", Validationf("%q is not a domain name; give it as shop.example.com, without https:// or a path", raw)
	}
	if h == "" || len(h) > 253 || strings.ContainsAny(h, ":/ ") || net.ParseIP(h) != nil {
		return bad()
	}
	labels := strings.Split(h, ".")
	if len(labels) < 2 {
		return bad()
	}
	for _, l := range labels {
		if !hostLabelRE.MatchString(l) {
			return bad()
		}
	}
	return h, nil
}

// underBaseDomain refuses an address under the install's base domain: each
// store already answers at <slug>.<base domain>, so the useful answer names
// this store's own.
func (a *App) underBaseDomain(ctx context.Context, host string) *APIError {
	base := strings.ToLower(a.cfg.BaseDomain)
	if base == "" || (host != base && !strings.HasSuffix(host, "."+base)) {
		return nil
	}
	st, err := a.Stores().Get(ctx, a.storeOf(ctx))
	if err != nil {
		return Validationf("addresses under %s come with each store; add a domain you own instead, such as shop.example.com", base)
	}
	own := st.Slug + "." + base
	if host == own {
		return Validationf("%s is already this store's address, so there is nothing to add; this is for a domain you own, such as shop.example.com", host)
	}
	return Validationf("%s cannot be added: addresses under %s come with each store, and this store's is %s; add a domain you own instead, such as shop.example.com", host, base, own)
}

func (a *App) mountDomainRoutes() {
	a.HandleFunc("GET "+TraefikConfigPath, a.handleTraefikConfig)
	a.HandleAdminFunc("GET /api/admin/domains", a.handleListDomains)
	a.HandleAdminFunc("POST /api/admin/domains", a.handleAddDomain, RightStoreWrite)
	a.HandleAdminFunc("POST /api/admin/domains/{host}/verify", a.handleVerifyDomain, RightStoreWrite)
	a.HandleAdminFunc("POST /api/admin/domains/{host}/primary", a.handlePrimaryDomain, RightStoreWrite)
	a.HandleAdminFunc("DELETE /api/admin/domains/{host}", a.handleRemoveDomain, RightStoreWrite)
}

const domainColumns = `host, verify_token, verified_at, is_primary, created_at`

func scanDomain(row interface{ Scan(...any) error }) (*Domain, error) {
	var d Domain
	var token string
	if err := row.Scan(&d.Host, &token, &d.VerifiedAt, &d.Primary, &d.CreatedAt); err != nil {
		return nil, err
	}
	d.Verified = d.VerifiedAt != nil
	d.TXTName = txtPrefix + d.Host
	d.TXTValue = txtValuePrefix + token
	return &d, nil
}

// domain is one of ctx's store's domains, or a 404 — another store's domain
// reads the same as no domain.
func (a *App) domain(ctx context.Context, host string) (*Domain, error) {
	d, err := scanDomain(a.db.QueryRowContext(ctx,
		`SELECT `+domainColumns+` FROM store_domains WHERE host = $1 AND store_id = $2`,
		strings.ToLower(host), a.storeOf(ctx)))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, NotFoundf("this store has no domain %s", host)
	}
	if err != nil {
		return nil, Internalf(err, "read domain")
	}
	a.pointAt(d)
	return d, nil
}

func (a *App) handleListDomains(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rows, err := a.db.QueryContext(ctx,
		`SELECT `+domainColumns+` FROM store_domains WHERE store_id = $1 ORDER BY is_primary DESC, created_at, host`,
		a.storeOf(ctx))
	if err != nil {
		RespondError(w, r, Internalf(err, "list domains"))
		return
	}
	defer rows.Close()
	out := []*Domain{}
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			RespondError(w, r, Internalf(err, "list domains"))
			return
		}
		a.pointAt(d)
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		RespondError(w, r, Internalf(err, "list domains"))
		return
	}
	Respond(w, http.StatusOK, out)
}

// handleAddDomain takes the host and nothing else: verified and primary are
// the engine's, so a body that sets them is read for its host alone.
func (a *App) handleAddDomain(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Host string `json:"host"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	host, err := a.normalizeHost(in.Host)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if err := a.underBaseDomain(r.Context(), host); err != nil {
		RespondError(w, r, err)
		return
	}
	token, err := randomToken(32)
	if err != nil {
		RespondError(w, r, Internalf(err, "make a verification token"))
		return
	}
	ctx := r.Context()
	store := a.storeOf(ctx)
	if _, err := a.db.ExecContext(ctx,
		`INSERT INTO store_domains (host, store_id, verify_token) VALUES ($1, $2, $3)`, host, store, token); err != nil {
		if !isUniqueViolation(err) {
			RespondError(w, r, Internalf(err, "add domain"))
			return
		}
		var owner string
		_ = a.db.QueryRowContext(ctx, `SELECT store_id FROM store_domains WHERE host = $1`, host).Scan(&owner)
		if owner == store {
			RespondError(w, r, Conflictf("%s is already one of this store's domains", host))
		} else {
			RespondError(w, r, Conflictf("%s is attached to another store", host))
		}
		return
	}
	d, err := a.domain(ctx, host)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusCreated, d)
}

// handleVerifyDomain looks the TXT record up now and waits for the answer, so
// whoever pressed Verify learns the result rather than a promise of one.
func (a *App) handleVerifyDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := a.domain(ctx, r.PathValue("host"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if !d.Verified {
		lookup, cancel := context.WithTimeout(ctx, txtLookupTimeout)
		records, _ := a.lookupTXT(lookup, d.TXTName)
		cancel()
		found := false
		for _, rec := range records {
			if strings.TrimSpace(rec) == d.TXTValue {
				found = true
				break
			}
		}
		if !found {
			msg := "no TXT record at " + d.TXTName + " yet; DNS changes can take a while to appear"
			if len(records) > 0 {
				msg = "the TXT records at " + d.TXTName + " do not include " + d.TXTValue +
					" (found: " + strings.Join(records, ", ") + ")"
			}
			RespondError(w, r, errNotVerified(msg))
			return
		}
		if _, err := a.db.ExecContext(ctx,
			`UPDATE store_domains SET verified_at = now() WHERE host = $1 AND store_id = $2`,
			d.Host, a.storeOf(ctx)); err != nil {
			RespondError(w, r, Internalf(err, "mark the domain verified"))
			return
		}
		a.log.Info("domain verified", "store", a.storeOf(ctx), "host", d.Host)
	}
	d, err = a.domain(ctx, d.Host)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, d)
}

// handlePrimaryDomain makes a verified domain the one the store is known by.
func (a *App) handlePrimaryDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	d, err := a.domain(ctx, r.PathValue("host"))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if !d.Verified {
		RespondError(w, r, errNotVerified("verify "+d.Host+" before making it the store's primary domain"))
		return
	}
	store := a.storeOf(ctx)
	if err := InTx(ctx, a.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`UPDATE store_domains SET is_primary = false WHERE store_id = $1 AND is_primary`, store); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE store_domains SET is_primary = true WHERE host = $1 AND store_id = $2`, d.Host, store)
		return err
	}); err != nil {
		RespondError(w, r, Internalf(err, "set the primary domain"))
		return
	}
	d, err = a.domain(ctx, d.Host)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, d)
}

func (a *App) handleRemoveDomain(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	res, err := a.db.ExecContext(ctx,
		`DELETE FROM store_domains WHERE host = $1 AND store_id = $2`,
		strings.ToLower(r.PathValue("host")), a.storeOf(ctx))
	if err != nil {
		RespondError(w, r, Internalf(err, "remove domain"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		RespondError(w, r, NotFoundf("this store has no domain %s", r.PathValue("host")))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
