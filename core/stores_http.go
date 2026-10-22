package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// The /api/stores routes belong to a person rather than to one store: which
// stores they work in, moving into one, and opening a new one. storeMW leaves
// them unbound (storeMW, personPath) — a store the panel remembered may since
// have gone, and these routes are how it finds out.

// MyStore is one store in a person's list, with their standing there.
type MyStore struct {
	ID     string `json:"id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Role   string `json:"role"`
	// Demo says the store masks personal data (Stores.SetDemo).
	Demo bool `json:"demo"`
	// Domain is where the store's shop is: its primary verified domain, else
	// its first verified one, else its Subdomain, else empty when neither
	// exists.
	Domain string `json:"domain"`
	// Subdomain is <slug>.<base domain>, the address a store has without
	// adding a domain of its own, under its brand's base domain (D111); empty
	// when that brand has none.
	Subdomain string `json:"subdomain"`
}

// storeCookie only says which store a new tab opens in. The panel keeps each
// tab's own store and names it on every request (D83), so the cookie is the
// last resort in storeMW's order, after the header.
const storeCookie = "litekart_store_id"

func (a *App) mountStoreRoutes() {
	a.Handle("GET /api/stores/my", a.personAuth(http.HandlerFunc(a.handleMyStores)))
	a.Handle("POST /api/stores/switch", a.personAuth(http.HandlerFunc(a.handleSwitchStore)))
	a.Handle("POST /api/stores/create", a.personAuth(http.HandlerFunc(a.handleCreateStore)))
	a.Handle("GET /api/stores/suggest", a.personAuth(http.HandlerFunc(a.handleSuggestStoreName)))
	a.Handle("POST /api/stores/delete", a.personAuth(http.HandlerFunc(a.handleDeleteStore)))
}

// personPath is a route about the person, never resolved to a store.
func personPath(p string) bool { return strings.HasPrefix(p, "/api/stores/") }

// personAuth admits a signed-in person or a static admin token. bearerAuth
// cannot: it answers for the bound store, and these routes have none. An API
// key belongs to one store and has no business listing or opening others.
func (a *App) personAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := bearerToken(r)
		switch {
		case !ok:
			a.denyAdmin(w, r)
		case isAPIKeyShaped(token):
			RespondError(w, r, Forbiddenf("an API key belongs to one store; sign in as a person to change stores"))
		case a.validAdminToken(token):
			next.ServeHTTP(w, withAdminToken(r))
		default:
			su, ok := a.superusers.Resolve(r.Context(), token)
			if !ok {
				a.denyAdmin(w, r)
				return
			}
			next.ServeHTTP(w, withSuperuser(r, su))
		}
	})
}

func isAPIKeyShaped(token string) bool {
	_, ok := apiKeyPrefixOf(token)
	return ok
}

// myStores lists the stores the caller may enter: a person's, in the brand
// the request came through (D111), or, for a static admin token, which is the
// install's operator, every store, as owner.
func (a *App) myStores(ctx context.Context) ([]MyStore, error) {
	const domain = `(SELECT host FROM store_domains d WHERE d.store_id = st.id AND d.verified_at IS NOT NULL
		ORDER BY is_primary DESC, created_at, host LIMIT 1)`
	q := `SELECT st.id, st.slug, st.name, st.status, st.demo, m.role, ` + domain + `, coalesce(st.brand_host, '')
		FROM store_members m JOIN stores st ON st.id = m.store_id
		WHERE m.superuser_id = $1 ORDER BY st.created_at, st.id`
	var args []any
	su := SuperuserFrom(ctx)
	if su != nil {
		args = append(args, su.ID)
	} else {
		q = `SELECT st.id, st.slug, st.name, st.status, st.demo, '` + RoleOwner + `', ` + domain + `, coalesce(st.brand_host, '')
			FROM stores st ORDER BY st.created_at, st.id`
	}
	rows, err := a.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, Internalf(err, "list the person's stores")
	}
	defer rows.Close()
	out := []MyStore{}
	here := a.Brand(ctx)
	for rows.Next() {
		var s MyStore
		var host sql.NullString
		var brandHost string
		if err := rows.Scan(&s.ID, &s.Slug, &s.Name, &s.Status, &s.Demo, &s.Role, &host, &brandHost); err != nil {
			return nil, Internalf(err, "scan store")
		}
		brand := a.storeBrand(brandHost)
		if su != nil && !sameBrand(brand, here) {
			continue
		}
		if brand.BaseDomain != "" {
			s.Subdomain = s.Slug + "." + brand.BaseDomain
		}
		s.Domain = host.String
		if s.Domain == "" {
			s.Domain = s.Subdomain
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (a *App) handleMyStores(w http.ResponseWriter, r *http.Request) {
	mine, err := a.myStores(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, mine)
}

// storeEntry is what switching or creating answers with: the store and the
// caller as that store sees them, role and rights, so the panel can redraw
// its menu in the same frame. Record is nil for a static admin token.
type storeEntry struct {
	Store  *Store     `json:"store"`
	Record *Superuser `json:"record"`
}

func (a *App) handleSwitchStore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		StoreID string `json:"store_id"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	mine, err := a.myStores(ctx)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// Matched against the caller's own list, so an unknown store and somebody
	// else's read the same: the answer does not say which stores exist.
	want := strings.ToLower(strings.TrimSpace(in.StoreID))
	var hit *MyStore
	for i := range mine {
		if strings.EqualFold(mine[i].ID, want) || mine[i].Slug == want {
			hit = &mine[i]
			break
		}
	}
	if hit == nil {
		RespondError(w, r, ErrNotAMember)
		return
	}
	if hit.Status != "active" {
		RespondError(w, r, ErrStoreSuspended)
		return
	}
	entry, err := a.enterStore(ctx, hit.ID)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.setStoreCookie(w, r, hit.ID)
	Respond(w, http.StatusOK, entry)
}

// handleSuggestStoreName is a name for the create form to offer, with an
// address nobody has.
func (a *App) handleSuggestStoreName(w http.ResponseWriter, r *http.Request) {
	name, slug, err := a.stores.SuggestName(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, map[string]string{"name": name, "slug": slug})
}

// handleCreateStore opens a store with the caller as its owner. Any signed-in
// person may: somebody with no store yet has nowhere else to start, and the
// new store is theirs alone.
func (a *App) handleCreateStore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
		// Currency is what the store sells in; empty is the install's (D85).
		Currency string `json:"currency"`
		// Country is where the store is: its profile's country, and what its
		// default shipping zone covers. Empty is a zone covering everywhere.
		Country string `json:"country"`
		// SampleContent starts the store with the sample shop (D96).
		SampleContent bool `json:"sample_content"`
		// SampleProducts is SampleContent's name before the samples grew past
		// products (D94), still taken so a caller written then keeps working.
		SampleProducts bool `json:"sample_products"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	// Checked before anything is created, so a bad code opens no store.
	if in.Currency != "" {
		code, err := normalizeCurrency(in.Currency)
		if err != nil {
			RespondError(w, r, err)
			return
		}
		in.Currency = code
	}
	in.Country = strings.ToUpper(strings.TrimSpace(in.Country))
	if in.Country != "" && !countryRE.MatchString(in.Country) {
		RespondError(w, r, Validationf("country is a two-letter ISO code, such as %q", "IN"))
		return
	}
	ctx := r.Context()
	var owner int64
	if su := SuperuserFrom(ctx); su != nil {
		owner = su.ID
		// Where anybody can make an account, one of them opens stores at a
		// measured pace: each is a row of seeds, a sequence and a host. The
		// install's own token is not a stranger, and is not counted.
		if a.cfg.Signup {
			if wait, ok := a.storeOpens.take(strconv.FormatInt(su.ID, 10), time.Now()); !ok {
				RespondError(w, r, tooManyStoreOpens(wait))
				return
			}
		}
	}
	st, err := a.stores.CreateFor(ctx, in.Name, in.Slug, owner)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if in.Currency != "" {
		// A new store has nothing priced yet, so nothing to relabel.
		if _, err := a.db.ExecContext(ctx, `UPDATE stores SET currency = $1 WHERE id = $2`, in.Currency, st.ID); err != nil {
			RespondError(w, r, Internalf(err, "set the new store's currency"))
			return
		}
	}
	bound := WithStore(ctx, st.ID)
	if in.Country != "" {
		if _, err := a.db.ExecContext(bound, `UPDATE store_profile SET country = $1`, in.Country); err != nil {
			RespondError(w, r, Internalf(err, "set the new store's country"))
			return
		}
	}
	// A store opened here starts with the default shipping zone in its own
	// country, so it can quote a delivery from its first product (the
	// owner's choice).
	if err := a.shipping.SeedDefaults(bound, in.Country); err != nil {
		RespondError(w, r, Internalf(err, "seed the new store's shipping"))
		return
	}
	// The samples come after the currency, which prices them. A failure is
	// reported beside the store rather than failing the request: the store
	// is made and usable without them, and a client retrying a failed create
	// would open a second store.
	var samples *sampleContentResult
	if in.SampleContent || in.SampleProducts {
		added, err := a.AddSampleShop(bound)
		samples = &sampleContentResult{Added: added}
		if err != nil {
			a.log.Error("add the sample shop", "store", st.ID, "err", err)
			samples.Error = "The store was opened, but not all of the sample shop could be added."
		}
	}
	entry, err := a.enterStore(ctx, st.ID)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.log.Info("store created", "store", st.ID, "slug", st.Slug)
	a.setStoreCookie(w, r, st.ID)
	Respond(w, http.StatusCreated, createdStore{storeEntry: entry, SampleContent: samples})
}

// createdStore is what opening a store answers with: the store entry, and the
// sample shop when it was asked for.
type createdStore struct {
	*storeEntry
	SampleContent *sampleContentResult `json:"sample_content,omitempty"`
}

// sampleContentResult says what of the sample shop a new store got, and why
// not all of it when adding failed part way.
type sampleContentResult struct {
	Added SampleCounts `json:"added"`
	Error string       `json:"error,omitempty"`
}

// handleDeleteStore deletes a store for good. Its owner only, and only with
// the store's slug typed back as confirmation: there is no undo. The install's
// last store stays, since the engine has nothing to serve without one, and a
// demo store cannot be deleted from inside — its visitors are signed in as
// its owners.
func (a *App) handleDeleteStore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		StoreID string `json:"store_id"`
		Confirm string `json:"confirm"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	mine, err := a.myStores(ctx)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	want := strings.ToLower(strings.TrimSpace(in.StoreID))
	var hit *MyStore
	for i := range mine {
		if strings.EqualFold(mine[i].ID, want) || mine[i].Slug == want {
			hit = &mine[i]
			break
		}
	}
	switch {
	case hit == nil:
		RespondError(w, r, ErrNotAMember)
		return
	case hit.Role != RoleOwner:
		RespondError(w, r, Forbiddenf("only an owner of %s can delete it", hit.Slug))
		return
	case hit.Demo || a.cfg.Demo:
		RespondError(w, r, &APIError{Status: http.StatusForbidden, Code: "demo_frozen",
			Message: "a demo store cannot be deleted from its panel; use the command line"})
		return
	case strings.TrimSpace(in.Confirm) != hit.Slug:
		RespondError(w, r, Validationf("type the store's address name, %q, to confirm", hit.Slug))
		return
	}
	n, err := a.stores.Count(ctx)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if n <= 1 {
		RespondError(w, r, Conflictf("this is the install's only store, and the engine needs one"))
		return
	}
	if err := a.stores.Delete(ctx, hit.ID); err != nil {
		RespondError(w, r, err)
		return
	}
	a.log.Info("store deleted", "store", hit.ID, "slug", hit.Slug)
	if c, err := r.Cookie(storeCookie); err == nil && c.Value == hit.ID {
		a.clearStoreCookie(w, r)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) enterStore(ctx context.Context, id string) (*storeEntry, error) {
	st, err := a.stores.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	entry := &storeEntry{Store: st}
	if su := SuperuserFrom(ctx); su != nil {
		here, err := a.superusers.Get(WithStore(ctx, id), su.ID)
		if errors.Is(err, ErrNotFound) {
			return nil, ErrNotAMember
		}
		if err != nil {
			return nil, err
		}
		entry.Record = here
	}
	return entry, nil
}

func (a *App) setStoreCookie(w http.ResponseWriter, r *http.Request, id string) {
	a.writeStoreCookie(w, r, id, int((365 * 24 * time.Hour).Seconds()))
}

func (a *App) clearStoreCookie(w http.ResponseWriter, r *http.Request) {
	a.writeStoreCookie(w, r, "", -1)
}

func (a *App) writeStoreCookie(w http.ResponseWriter, r *http.Request, id string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     storeCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		// The operator's word, not a header: X-Forwarded-Proto is whatever
		// the client sent unless a proxy rewrote it, and nothing here knows
		// which. Behind a TLS-terminating proxy, the panel URL of the brand
		// the request came through says https.
		Secure:   r.TLS != nil || strings.HasPrefix(strings.ToLower(a.Brand(r.Context()).PanelURL), "https://"),
		SameSite: http.SameSiteLaxMode,
	})
}
