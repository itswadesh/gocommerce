package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
)

// Brands (D111). One install — one database, one deployment — can answer under
// several names, each on its own admin domain. The panel is the same product
// with a different name; what differs is what the panel and the emails to
// staff call it, where those emails' links lead, and which stores it shows.
//
// Each store belongs to one brand (stores.brand_host). Its address is
// <slug>.<its brand's base domain>, and nothing under another brand's; on a
// brand's admin domain a person sees and enters only that brand's stores. A
// person is one account across brands, and holds stores in several if they
// opened or joined them there. Slugs stay unique across the install, so two
// brands cannot both have a store called "mystore".
//
// A request's brand is chosen from its host, among the brands configured, and
// a link is only ever built from a configured brand's PanelURL. The host picks
// from an allow-list; it is never copied into a link, which is the property
// the reset link has always had (D95). Choosing the brand never changes which
// store a request is bound to: a brand's panel host may also be a store's own
// domain, and the store binding is then exactly what it was.

// Brand is one name the install answers under.
type Brand struct {
	// Name is what the panel and the emails to staff call the product.
	Name string `json:"name"`
	// PanelURL is where this brand's panel is reached, such as
	// https://admin.example.com: the base of every link an email to staff
	// carries, and the host a request is matched against. Its host is what a
	// store and a person record as their brand, since a name can be changed.
	PanelURL string `json:"panel_url"`
	// BaseDomain gives each of this brand's stores an address,
	// <slug>.<BaseDomain>: the one the panel shows and every link uses. Empty,
	// the brand gives its stores no address of their own.
	BaseDomain string `json:"base_domain"`
	// AliasBaseDomains are older base domains this brand's stores still
	// answer under, so an address handed out before a move keeps working.
	// Nothing the engine hands out uses them.
	AliasBaseDomains []string `json:"alias_base_domains,omitempty"`
	// LogoURL is a picture the panel shows where its mark is. Empty keeps the
	// mark.
	LogoURL string `json:"logo_url"`
	// Reserved are more names no store may take, beside the platform's own
	// (D93) and the first label of this brand's panel host, which are always
	// kept: with stores at <slug>.<apex>, every other host under the apex the
	// brand uses — a status page, a blog — is a name a store must not have.
	Reserved []string `json:"reserved,omitempty"`
}

// DefaultBrandName is what an install that configures no brands is called.
const DefaultBrandName = "GoCommerce"

// BrandsEnv is the environment variable `gocommerce serve` reads brands from,
// a JSON array of Brand.
const BrandsEnv = "GOCOMMERCE_BRANDS"

// migration0073StoreBrand records the brand each store belongs to, by its
// brand's panel host. NULL until the install configures brands; at start
// every store without one is given the default brand (assignStoreBrands).
const migration0073StoreBrand = `
ALTER TABLE stores ADD COLUMN brand_host text;
`

// ParseBrands reads brands from BrandsEnv's JSON. Empty is none, and the
// install is one brand made of its older settings. A key it does not know is
// refused rather than dropped: "panelurl" for "panel_url" would otherwise
// start a brand whose links go nowhere.
func ParseBrands(raw string) ([]Brand, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	bad := func(why string) error {
		return fmt.Errorf("gocommerce: %s %s; it is a JSON array such as "+
			`[{"name":"Alpha","panel_url":"https://admin.alpha.example","base_domain":"alpha.example","logo_url":""}]`,
			BrandsEnv, why)
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var out []Brand
	if err := dec.Decode(&out); err != nil {
		return nil, bad("does not read: " + err.Error())
	}
	if dec.More() {
		return nil, bad("has more after its array")
	}
	if len(out) == 0 {
		return nil, bad("lists no brands (leave it unset for one)")
	}
	return out, nil
}

// applyBrands tidies and checks the brands, and lets the first stand in for
// PanelURL and BaseDomain where those are unset, so a module that reads them
// (cart recovery's tracked links) still finds the install's address. Set as
// well, they must name one of the brands: a base domain the brands left out
// would take every store's address under it offline without a word.
func (c *Config) applyBrands() error {
	if len(c.Brands) == 0 {
		return nil
	}
	panels := map[string]string{}
	domains := map[string]string{}
	for i := range c.Brands {
		b := &c.Brands[i]
		b.Name = strings.TrimSpace(b.Name)
		if b.Name == "" {
			return fmt.Errorf("gocommerce: brand %d has no name", i+1)
		}
		fail := func(format string, args ...any) error {
			return fmt.Errorf("gocommerce: brand %d (%s): %s", i+1, b.Name, fmt.Sprintf(format, args...))
		}
		panel, err := brandURL(b.PanelURL, c.Dev)
		if err != nil {
			return fail("panel_url %s", err)
		}
		u, _ := url.Parse(panel)
		if u.Path != "" {
			return fail("panel_url %q must be the panel's address with no path, such as https://admin.example.com", b.PanelURL)
		}
		b.PanelURL = panel
		host := hostName(u.Host)
		if other, ok := panels[host]; ok {
			return fail("its panel is on %s, which is %s's already", host, other)
		}
		panels[host] = b.Name
		// A logo is cosmetic: one that cannot be shown is dropped and said,
		// rather than stopping every brand on the install over a picture.
		if b.LogoURL != "" {
			logo, err := brandURL(b.LogoURL, c.Dev)
			if err != nil {
				c.brandWarnings = append(c.brandWarnings,
					fmt.Sprintf("brand %d (%s): logo_url %s, so the panel shows the mark instead", i+1, b.Name, err))
			}
			b.LogoURL = logo
		}

		if b.BaseDomain == "" && len(b.AliasBaseDomains) > 0 {
			return fail("alias_base_domains need a base_domain, which is the address its stores are given")
		}
		own := append([]string{b.BaseDomain}, b.AliasBaseDomains...)
		for j, raw := range own {
			if j == 0 && raw == "" {
				continue
			}
			d, ok := baseDomainName(raw)
			if !ok {
				return fail("%q is not a domain name; give it as shops.example.com", raw)
			}
			if other, taken := domains[d]; taken {
				return fail("the base domain %s is %s's already", d, other)
			}
			domains[d] = b.Name
			if j == 0 {
				b.BaseDomain = d
			} else {
				b.AliasBaseDomains[j-1] = d
			}
		}

		for j, name := range b.Reserved {
			name = strings.ToLower(strings.TrimSpace(name))
			if !storeSlugRE.MatchString(name) {
				return fail("reserved name %q is not a store address: lowercase letters, digits and hyphens", b.Reserved[j])
			}
			b.Reserved[j] = name
		}
	}

	if c.PanelURL == "" {
		c.PanelURL = c.Brands[0].PanelURL
	} else if u, err := url.Parse(c.PanelURL); err != nil || panels[hostName(u.Host)] == "" {
		return fmt.Errorf("gocommerce: GOCOMMERCE_PANEL_URL (Config.PanelURL) %s is no brand's panel; "+
			"unset it, or make it one of the brands' panel_url", c.PanelURL)
	}
	if c.BaseDomain == "" {
		c.BaseDomain = c.Brands[0].BaseDomain
	} else if base, _ := baseDomainName(c.BaseDomain); domains[base] == "" {
		return fmt.Errorf("gocommerce: GOCOMMERCE_BASE_DOMAIN (Config.BaseDomain) %s is no brand's base_domain "+
			"or alias, so the stores' addresses under it would stop answering; add it to a brand, or unset it", c.BaseDomain)
	}
	return nil
}

// brandURL checks an absolute https address, http only in Dev, and returns it
// without a trailing slash. The error completes a sentence that names the
// field.
func brandURL(raw string, dev bool) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	switch {
	case raw == "":
		return "", fmt.Errorf("is required")
	case err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil:
		return "", fmt.Errorf("%q is not an absolute address such as https://admin.example.com", raw)
	case u.Scheme == "https", u.Scheme == "http" && dev:
	default:
		return "", fmt.Errorf("%q must start with https:// (plain http only in Dev)", raw)
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// baseDomainName is a base domain as hosts are compared: lower case, no
// trailing dot, labels of letters, digits and hyphens.
func baseDomainName(raw string) (string, bool) {
	d := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(raw)), ".")
	if d == "" || len(d) > 253 {
		return "", false
	}
	for _, l := range strings.Split(d, ".") {
		if !hostLabelRE.MatchString(l) {
			return "", false
		}
	}
	return d, true
}

// hostName is a Host header as compared with a configured host: lower case,
// with no port and no trailing dot.
func hostName(host string) string {
	host = strings.ToLower(host)
	if i := strings.LastIndexByte(host, ':'); i >= 0 && !strings.Contains(host[i:], "]") {
		host = host[:i]
	}
	return strings.TrimSuffix(host, ".")
}

// panelHost is the host a brand's panel answers on, or "" with no panel URL.
func panelHost(panelURL string) string {
	u, err := url.Parse(panelURL)
	if err != nil {
		return ""
	}
	return hostName(u.Host)
}

// sameBrand compares brands by what tells them apart: two configured brands
// never share a panel.
func sameBrand(a, b Brand) bool { return a.Name == b.Name && a.PanelURL == b.PanelURL }

// storeDomains are the domains this brand's stores answer under, its base
// domain first.
func (b Brand) storeDomains() []string {
	if b.BaseDomain == "" {
		return nil
	}
	return append([]string{b.BaseDomain}, b.AliasBaseDomains...)
}

// Brands are the names this install answers under, the first being the one a
// request is given when its host names none. An install that configures none
// is one brand made of Config.PanelURL and Config.BaseDomain, read as they are
// now, so it behaves exactly as it did before brands.
func (a *App) Brands() []Brand {
	return slices.Clone(a.brands())
}

func (a *App) brands() []Brand {
	if len(a.cfg.Brands) > 0 {
		return a.cfg.Brands
	}
	base, _ := baseDomainName(a.cfg.BaseDomain)
	return []Brand{{Name: DefaultBrandName, PanelURL: a.cfg.PanelURL, BaseDomain: base}}
}

// underStoreDomain is the most specific brand domain host is under (or is),
// and whose it is. The most specific, because a brand on the apex may keep
// an older shops.<apex> as an alias.
func (a *App) underStoreDomain(host string) (domain string, owner Brand, ok bool) {
	for _, b := range a.brands() {
		for _, d := range b.storeDomains() {
			if (host == d || strings.HasSuffix(host, "."+d)) && len(d) > len(domain) {
				domain, owner, ok = d, b, true
			}
		}
	}
	return domain, owner, ok
}

// brandForHost is the brand a request's host belongs to: the brand whose
// panel is on it, else the brand whose store domain it is under, else the
// brand of the store whose own domain it is, else the default brand. A
// store's own domain is looked up only after the configured hosts, so the
// common case costs no query.
func (a *App) brandForHost(ctx context.Context, host string) Brand {
	h := hostName(host)
	if b, ok := a.brandByPanelHost(h); ok {
		return b
	}
	if _, b, ok := a.underStoreDomain(h); ok {
		return b
	}
	if len(a.cfg.Brands) > 0 && h != "" {
		var brandHost string
		err := a.db.QueryRowContext(ctx, `SELECT coalesce(s.brand_host, '') FROM store_domains d
			JOIN stores s ON s.id = d.store_id WHERE d.host = $1 AND d.verified_at IS NOT NULL`, h).Scan(&brandHost)
		if err == nil {
			return a.storeBrand(brandHost)
		}
	}
	return a.defaultBrand()
}

// brandByPanelHost is the brand whose panel is on host.
func (a *App) brandByPanelHost(host string) (Brand, bool) {
	for _, b := range a.brands() {
		if host != "" && panelHost(b.PanelURL) == host {
			return b, true
		}
	}
	return Brand{}, false
}

// storeBrand is the brand of a store recording host as its brand: that brand,
// or the default one for a store with none, or with one the install no longer
// configures (the doctor names those).
func (a *App) storeBrand(host string) Brand {
	if b, ok := a.brandByPanelHost(host); ok {
		return b
	}
	return a.defaultBrand()
}

// defaultBrand is the brand of a store that has none, of a host nothing else
// names, and of work no request started: the one whose base domain or alias
// is Config.BaseDomain, which is where stores answered before there were
// brands, else the first.
func (a *App) defaultBrand() Brand {
	brands := a.brands()
	if base, ok := baseDomainName(a.cfg.BaseDomain); ok {
		for _, b := range brands {
			if slices.Contains(b.storeDomains(), base) {
				return b
			}
		}
	}
	return brands[0]
}

// assignStoreBrands gives every store without a brand the default one, once
// the install configures brands, so a store's brand stays put when the list
// is later reordered. Without configured brands nothing is written: there is
// one brand, and every store is in it.
func (a *App) assignStoreBrands(ctx context.Context) error {
	if len(a.cfg.Brands) == 0 {
		return nil
	}
	host := panelHost(a.defaultBrand().PanelURL)
	if _, err := a.db.ExecContext(ctx, `UPDATE stores SET brand_host = $1 WHERE brand_host IS NULL`, host); err != nil {
		return fmt.Errorf("gocommerce: give the stores their brand: %w", err)
	}
	// Said, not repaired: which brand those stores belong to is the owner's
	// call, and guessing would move them a second time.
	if stray, err := a.strayBrandHosts(ctx); err == nil && len(stray) > 0 {
		a.log.Error("stores belong to panel hosts no brand has; they are shown under the default brand until moved",
			"hosts", strayList(stray), "default_brand", a.defaultBrand().Name,
			"fix", "gocommerce brands rename-host <old host> <new host>, or gocommerce store brand <slug> <panel host>")
	}
	return nil
}

// newStoreBrandHost is what a store opened under ctx records as its brand: the
// panel host of the brand it is opened from. NULL without configured brands,
// for the reason assignStoreBrands writes nothing then.
func (a *App) newStoreBrandHost(ctx context.Context) sql.NullString {
	if len(a.cfg.Brands) == 0 {
		return sql.NullString{}
	}
	return sql.NullString{String: panelHost(a.Brand(ctx).PanelURL), Valid: true}
}

type brandKey struct{}

func withBrand(ctx context.Context, b Brand) context.Context {
	return context.WithValue(ctx, brandKey{}, b)
}

// BrandFrom is the brand of the request ctx belongs to, set from its host
// before anything else runs. The second result is false for work no request
// started — a job, a command, a module's background pass — which has no
// request's brand and decides by whom the work is for instead.
func BrandFrom(ctx context.Context) (Brand, bool) {
	b, ok := ctx.Value(brandKey{}).(Brand)
	return b, ok
}

// Brand is ctx's request's brand, or the default brand when no request started
// the work.
func (a *App) Brand(ctx context.Context) Brand {
	if b, ok := BrandFrom(ctx); ok {
		return b
	}
	return a.defaultBrand()
}

// StoreBrand is the brand of the store ctx is bound to (D111): what its
// emails to staff are named, and whose panel address its links go through.
// Bound to no store, it is the request's brand, else the first.
func (a *App) StoreBrand(ctx context.Context) Brand {
	if id := a.storeOf(ctx); id != "" && id != installScope {
		if st, err := a.stores.Get(ctx, id); err == nil {
			return a.storeBrand(st.BrandHost)
		}
	}
	return a.Brand(ctx)
}

type foreignStoreKey struct{}

// foreignStore says the store a request is bound to belongs to another brand
// than the admin domain it came through. A person is then answered as though
// not on its team (bearerAuth): on one brand's panel the stores of another
// are not there to be named.
func foreignStore(ctx context.Context) bool {
	v, _ := ctx.Value(foreignStoreKey{}).(bool)
	return v
}

// reservedSlug says no store may have slug: one the platform keeps (D93), the
// first label of a brand's panel host, one a brand lists in Reserved, or the
// label of a store domain that sits directly under another. Slugs are one
// namespace across brands, so a name kept by one brand is kept by all.
func (a *App) reservedSlug(slug string) bool {
	if reservedSlugs[slug] {
		return true
	}
	if len(a.cfg.Brands) == 0 {
		return false
	}
	var domains []string
	for _, b := range a.cfg.Brands {
		if first, _, _ := strings.Cut(panelHost(b.PanelURL), "."); first == slug {
			return true
		}
		if slices.Contains(b.Reserved, slug) {
			return true
		}
		domains = append(domains, b.storeDomains()...)
	}
	for _, d := range domains {
		if slices.Contains(domains, slug+"."+d) {
			return true
		}
	}
	return false
}

// storeSlugOfHost is the store address host is, under the most specific of
// the brands' store domains, and that domain's brand. A brand's own panel
// host and a reserved name are no store's.
func (a *App) storeSlugOfHost(host string) (string, Brand, bool) {
	if _, isPanel := a.brandByPanelHost(host); isPanel {
		return "", Brand{}, false
	}
	d, b, ok := a.underStoreDomain(host)
	if !ok {
		return "", Brand{}, false
	}
	slug, under := strings.CutSuffix(host, "."+d)
	if !under || slug == "" || strings.Contains(slug, ".") || a.reservedSlug(slug) {
		return "", Brand{}, false
	}
	return slug, b, true
}

// SetBrand moves store id to the brand whose panel is on panelHost. Its
// address moves with it, to <slug>.<that brand's base domain>; its domains of
// its own, its team and its data do not change. It is the install owner's
// act (`gocommerce store brand`), audited in the store.
func (s *Stores) SetBrand(ctx context.Context, id, panel string) (Brand, error) {
	to, ok := s.app.brandByPanelHost(hostName(panel))
	if !ok || len(s.app.cfg.Brands) == 0 {
		var hosts []string
		for _, b := range s.app.cfg.Brands {
			hosts = append(hosts, panelHost(b.PanelURL))
		}
		if len(hosts) == 0 {
			return Brand{}, Validationf("this install configures no brands (%s), so there is nowhere to move a store", BrandsEnv)
		}
		return Brand{}, Validationf("%q is no brand's panel host; one of %s", panel, strings.Join(hosts, ", "))
	}
	st, err := s.Get(ctx, id)
	if err != nil {
		return Brand{}, err
	}
	from := s.app.storeBrand(st.BrandHost)
	// Already there: nothing moves, so the trail says nothing either. The
	// people who joined through the store keep the brand they arrived
	// through; a move changes where the store is, not who they are.
	if sameBrand(from, to) && st.BrandHost != "" {
		return to, nil
	}
	bound := WithStore(ctx, st.ID)
	err = InTx(bound, s.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(bound, `UPDATE stores SET brand_host = $2 WHERE id = $1`,
			st.ID, panelHost(to.PanelURL)); err != nil {
			return Internalf(err, "move the store to its brand")
		}
		return writeAudit(bound, tx, auditRecord{
			Action: AuditStoreSetBrand, Entity: AuditEntityStore, Key: st.ID, Label: st.Slug,
			Summary: fmt.Sprintf("Moved from %s to %s", from.Name, to.Name),
			Before:  map[string]any{"brand": from.Name, "brand_host": panelHost(from.PanelURL)},
			After:   map[string]any{"brand": to.Name, "brand_host": panelHost(to.PanelURL)},
		})
	})
	return to, err
}

// checkBrands lists the names the install answers under and where, and
// re-checks them, since an embedder can hand the App brands that never passed
// through New. It notes a brand's panel host that is also a store's own
// domain, which is allowed — the panel still knows its brand, and requests
// there are bound to that store as before. It warns about a store whose brand
// the install no longer configures, and one whose address is a name the
// platform keeps, which does not answer under its base domain.
func (a *App) checkBrands(ctx context.Context) Diagnostic {
	d := Diagnostic{Name: "brands", Status: StatusOK}
	warn := func(detail, hint string) {
		d.Detail += "; " + detail
		if d.Status != StatusFail {
			d.Status, d.Hint = StatusWarn, hint
		}
	}

	if len(a.cfg.Brands) > 0 {
		check := Config{Brands: cloneBrands(a.cfg.Brands), Dev: true}
		if err := check.applyBrands(); err != nil {
			d.Status = StatusFail
			d.Detail = strings.TrimPrefix(err.Error(), "gocommerce: ")
			d.Hint = "fix " + BrandsEnv + ": each brand needs a name and a panel_url of its own, and no two share a base domain"
			return d
		}
	}

	counts := map[string]int{}
	stray := map[string]int{}
	rows, err := a.db.QueryContext(ctx, `SELECT slug, coalesce(brand_host, '') FROM stores ORDER BY slug`)
	if err != nil {
		d.Status, d.Detail, d.Cause = StatusWarn, "could not read the stores' brands", err.Error()
		return d
	}
	for rows.Next() {
		var slug, host string
		if rows.Scan(&slug, &host) != nil {
			continue
		}
		if _, ok := a.brandByPanelHost(host); host != "" && !ok {
			stray[host]++
		}
		counts[panelHost(a.storeBrand(host).PanelURL)]++
	}
	rows.Close()

	lines := []string{}
	for _, b := range a.brands() {
		panel, stores := "no panel address", "no store addresses"
		if b.PanelURL != "" {
			panel = "panel at " + b.PanelURL
		}
		if b.BaseDomain != "" {
			stores = "stores at <slug>." + b.BaseDomain
			for _, alias := range b.AliasBaseDomains {
				stores += ", also <slug>." + alias
			}
		}
		line := fmt.Sprintf("%s (%s, %s, %d store(s)", b.Name, panel, stores, counts[panelHost(b.PanelURL)])
		if len(b.Reserved) > 0 {
			line += ", keeps " + strings.Join(b.Reserved, ", ")
		}
		lines = append(lines, line+")")
	}
	d.Detail = strings.Join(lines, "; ")

	for _, w := range a.cfg.brandWarnings {
		warn(w, "fix the entry in "+BrandsEnv)
	}
	for _, b := range a.cfg.Brands {
		if strings.HasPrefix(b.PanelURL, "http://") {
			warn(fmt.Sprintf("%s's panel is plain http, so its sign-ins and links travel unencrypted", b.Name),
				"serve each brand's panel on https; plain http is only for a development machine")
		}
	}

	for _, b := range a.brands() {
		host := panelHost(b.PanelURL)
		if host == "" {
			continue
		}
		var slug string
		err := a.db.QueryRowContext(ctx, `SELECT s.slug FROM store_domains d JOIN stores s ON s.id = d.store_id
			WHERE d.host = $1 AND d.verified_at IS NOT NULL`, host).Scan(&slug)
		if err == nil {
			d.Detail += fmt.Sprintf("; %s is the %s panel and also the store %s's own domain, so a request there that names no store is that store's", host, b.Name, slug)
		} else if !errors.Is(err, sql.ErrNoRows) {
			d.Cause = err.Error()
		}
	}

	if len(stray) > 0 {
		d.Status = StatusFail
		d.Detail += fmt.Sprintf("; stores belong to panel hosts no brand has, and are shown under %s until moved: %s",
			a.defaultBrand().Name, strayList(stray))
		d.Hint = "if a brand's panel_url changed, move its stores and people with gocommerce brands rename-host <old host> <new host>; " +
			"to move one store, gocommerce store brand <slug> <panel host>"
	}

	var kept []string
	for slug := range reservedSlugs {
		kept = append(kept, slug)
	}
	for _, b := range a.cfg.Brands {
		first, _, _ := strings.Cut(panelHost(b.PanelURL), ".")
		kept = append(kept, first)
		kept = append(kept, b.Reserved...)
	}
	var domains []string
	for _, b := range a.cfg.Brands {
		domains = append(domains, b.storeDomains()...)
	}
	for _, inner := range domains {
		for _, outer := range domains {
			if label, ok := strings.CutSuffix(inner, "."+outer); ok && !strings.Contains(label, ".") {
				kept = append(kept, label)
			}
		}
	}
	sort.Strings(kept)
	reserved, err := a.db.QueryContext(ctx, `SELECT slug FROM stores WHERE slug = ANY($1) ORDER BY slug`, kept)
	if err != nil {
		d.Cause = err.Error()
		return d
	}
	defer reserved.Close()
	for reserved.Next() {
		var slug string
		if reserved.Scan(&slug) == nil {
			warn(fmt.Sprintf("the store %s has an address the platform keeps, so it does not answer at %s.<base domain>", slug, slug),
				"give that store a domain of its own (Settings › Domains); its address was taken before the name was kept")
		}
	}
	return d
}

func cloneBrands(in []Brand) []Brand {
	out := make([]Brand, len(in))
	for i, b := range in {
		b.AliasBaseDomains = slices.Clone(b.AliasBaseDomains)
		b.Reserved = slices.Clone(b.Reserved)
		out[i] = b
	}
	return out
}

// strayBrandHosts counts the stores whose brand is a panel host no configured
// brand has. A panel_url renamed in GOCOMMERCE_BRANDS leaves exactly this
// behind: the stores read as the default brand's, so they would move without
// a word unless something says so.
func (a *App) strayBrandHosts(ctx context.Context) (map[string]int, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT brand_host, count(*) FROM stores WHERE brand_host IS NOT NULL GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var host string
		var n int
		if err := rows.Scan(&host, &n); err != nil {
			return nil, err
		}
		if _, ok := a.brandByPanelHost(host); !ok {
			out[host] = n
		}
	}
	return out, rows.Err()
}

// strayList is strayBrandHosts as a sentence part: "host (n), host (n)".
func strayList(stray map[string]int) string {
	hosts := make([]string, 0, len(stray))
	for h := range stray {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	for i, h := range hosts {
		hosts[i] = fmt.Sprintf("%s (%d)", h, stray[h])
	}
	return strings.Join(hosts, ", ")
}

// RenameBrandHost moves every store and person recorded under a panel host no
// brand has any more to the brand whose panel is on newHost, in one
// transaction, with a row in each moved store's audit trail. It is what to run
// after changing a brand's panel_url (`gocommerce brands rename-host`); moving
// one store between two brands that both exist is Stores.SetBrand.
func (a *App) RenameBrandHost(ctx context.Context, oldHost, newHost string) (stores, people int, err error) {
	oldHost, newHost = hostName(strings.TrimSpace(oldHost)), hostName(strings.TrimSpace(newHost))
	to, ok := a.brandByPanelHost(newHost)
	if !ok || len(a.cfg.Brands) == 0 {
		return 0, 0, Validationf("%q is no brand's panel host", newHost)
	}
	if b, taken := a.brandByPanelHost(oldHost); taken {
		return 0, 0, Validationf("%s is still %s's panel; to move a store between brands, use gocommerce store brand", oldHost, b.Name)
	}
	type moved struct{ id, slug string }
	var list []moved
	rows, err := a.db.QueryContext(ctx, `SELECT id, slug FROM stores WHERE brand_host = $1 ORDER BY created_at, id`, oldHost)
	if err != nil {
		return 0, 0, Internalf(err, "find the stores under %s", oldHost)
	}
	for rows.Next() {
		var m moved
		if err := rows.Scan(&m.id, &m.slug); err != nil {
			rows.Close()
			return 0, 0, Internalf(err, "find the stores under %s", oldHost)
		}
		list = append(list, m)
	}
	rows.Close()

	err = InTx(ctx, a.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE stores SET brand_host = $2 WHERE brand_host = $1`, oldHost, newHost); err != nil {
			return Internalf(err, "move the stores")
		}
		res, err := tx.ExecContext(ctx, `UPDATE superusers SET brand_host = $2 WHERE brand_host = $1`, oldHost, newHost)
		if err != nil {
			return Internalf(err, "move the people")
		}
		n, _ := res.RowsAffected()
		people = int(n)
		for _, m := range list {
			// The audit trail is each store's own, so each row is written
			// bound to its store.
			bound := WithStore(ctx, m.id)
			if err := writeAudit(bound, tx, auditRecord{
				Action: AuditStoreSetBrand, Entity: AuditEntityStore, Key: m.id, Label: m.slug,
				Summary: fmt.Sprintf("%s's panel moved from %s to %s", to.Name, oldHost, newHost),
				Before:  map[string]any{"brand_host": oldHost},
				After:   map[string]any{"brand": to.Name, "brand_host": newHost},
			}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return len(list), people, nil
}

// ValidateBrands checks brands as the engine would at start, with the older
// settings beside them, without changing what it is given. One bad entry stops
// the whole install — every brand — so `gocommerce doctor` runs this on the
// environment's value before anything is served with it.
func ValidateBrands(brands []Brand, panelURL, baseDomain string, dev bool) error {
	c := Config{Brands: cloneBrands(brands), PanelURL: strings.TrimRight(panelURL, "/"), BaseDomain: baseDomain, Dev: dev}
	return c.applyBrands()
}
