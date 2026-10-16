package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"
)

// Errors a request can meet before it reaches its route.
var (
	ErrStoreRequired = &APIError{Status: http.StatusBadRequest, Code: "store_required",
		Message: "name the store: an x-litekart-store header, a ?store= parameter or the store's own host"}
	ErrStoreNotFound  = &APIError{Status: http.StatusNotFound, Code: "store_not_found", Message: "no such store"}
	ErrStoreSuspended = &APIError{Status: http.StatusServiceUnavailable, Code: "store_unavailable",
		Message: "this store is suspended"}
	ErrNotAMember = &APIError{Status: http.StatusForbidden, Code: "not_a_member",
		Message: "you are not on this store's team"}
)

// storeExempt are API paths that work before a store is known: signing in and
// out of the person (who is not one store's) and the password reset that goes
// with it. They are still bound to a store when the request names one — a
// sign-in that names its store answers with the role there — but naming none
// is not an error. The /api/stores routes go further and are never bound
// (personPath).
var storeExempt = []string{
	"/api/admin/auth-state",
	"/api/admin/auth-with-password",
	"/api/admin/auth-logout",
	"/api/admin/password-reset",
}

func storeExemptPath(p string) bool {
	for _, e := range storeExempt {
		if p == e || strings.HasPrefix(p, e+"/") || (strings.HasSuffix(e, "/") && strings.HasPrefix(p, e)) {
			return true
		}
	}
	return false
}

// storePath reports whether a path belongs to a store: the engine's API and
// every module's public routes, which are mounted under /x/ (a storefront's
// search, pages, wishlists, feeds). The panel's own files, /health and /docs
// belong to no store and are served without asking.
func storePath(p string) bool {
	return strings.HasPrefix(p, "/api/") || strings.HasPrefix(p, "/x/")
}

// storeMW finds the store an API request is for and binds the request's
// context to it (WithStore), so every statement the handler runs sees that
// store's rows and no other's. Only store paths are resolved (storePath).
//
// First match wins: the API key's own store; the x-litekart-store header (or
// X-Store, slug or id); ?store=; the litekart_store_id cookie; the host; the
// signed-in person's only store; the install's only store. Naming a store
// grants nothing — the admin routes still check that the caller is on its
// team (bearerAuth).
func (a *App) storeMW(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !storePath(r.URL.Path) || personPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		st, err := a.resolveStore(r)
		switch {
		case err != nil:
			RespondError(w, r, err)
			return
		case st == nil && storeExemptPath(r.URL.Path):
			next.ServeHTTP(w, r)
			return
		case st == nil:
			RespondError(w, r, ErrStoreRequired)
			return
		case st.Status != "active":
			RespondError(w, r, ErrStoreSuspended)
			return
		}
		noteStore(r.Context(), st.ID)
		ctx := withStoreDemo(WithStore(r.Context(), st.ID), st.Demo)
		next.ServeHTTP(w, r.WithContext(withStoreCurrency(ctx, a.currencyOf(st))))
	})
}

func (a *App) resolveStore(r *http.Request) (*Store, error) {
	ctx := r.Context()
	keyStore, err := a.apiKeyStore(ctx, r)
	if err != nil {
		return nil, err
	}
	named, err := a.namedStore(ctx, r)
	if err != nil {
		return nil, err
	}
	switch {
	case keyStore != nil && named != nil && keyStore.ID != named.ID:
		return nil, Forbiddenf("this API key belongs to another store")
	case keyStore != nil:
		return keyStore, nil
	case named != nil:
		return named, nil
	}
	if st, err := a.stores.ByHost(ctx, r.Host); err == nil {
		return st, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	if st, err := a.onlyStoreOfSession(ctx, r); err != nil || st != nil {
		return st, err
	}
	if active, err := a.stores.Active(ctx); err != nil {
		return nil, err
	} else if len(active) == 1 {
		return &active[0], nil
	}
	if a.defaultStore != "" {
		return a.stores.Get(ctx, a.defaultStore)
	}
	return nil, nil
}

// namedStore is the store the request names outright: header, then query,
// then cookie. A name that matches no store is a 404, not a fall-through: a
// client that names a store and gets another's answer would be worse.
//
// The cookie is the exception. It is only a reminder of which store a new tab
// opens in, set by a switch long ago, and the panel cannot clear it (HttpOnly)
// — so one naming a store that has since gone or been suspended is passed
// over rather than refusing every request, sign-in included.
func (a *App) namedStore(ctx context.Context, r *http.Request) (*Store, error) {
	candidates := []string{
		r.Header.Get("x-litekart-store"),
		r.Header.Get("X-Store"),
		r.URL.Query().Get("store"),
	}
	if st, err := a.lookupStore(ctx, candidates); st != nil || err != nil {
		return st, err
	}
	c, err := r.Cookie(storeCookie)
	if err != nil {
		return nil, nil
	}
	st, err := a.lookupStore(ctx, []string{c.Value})
	if err != nil && !errors.Is(err, ErrNotFound) && !errors.Is(err, ErrStoreNotFound) {
		return nil, err
	}
	if st == nil || st.Status != "active" {
		return nil, nil
	}
	return st, nil
}

// lookupStore returns the store the first non-empty name matches.
func (a *App) lookupStore(ctx context.Context, candidates []string) (*Store, error) {
	for _, v := range candidates {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if ValidStoreID(v) {
			return a.stores.Get(ctx, v)
		}
		if storeSlugRE.MatchString(strings.ToLower(v)) {
			return a.stores.BySlug(ctx, v)
		}
		return nil, ErrStoreNotFound
	}
	return nil, nil
}

// apiKeyStore is the store an API key presented as the bearer belongs to.
// The prefix only routes; the key itself is checked later, inside its store.
func (a *App) apiKeyStore(ctx context.Context, r *http.Request) (*Store, error) {
	token, ok := bearerToken(r)
	if !ok {
		return nil, nil
	}
	prefix, ok := apiKeyPrefixOf(token)
	if !ok {
		return nil, nil
	}
	var id string
	err := a.db.QueryRowContext(ctx, `SELECT store_id FROM api_key_index WHERE prefix = $1`, prefix).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil // an unknown key fails authentication, not resolution
	}
	if err != nil {
		return nil, Internalf(err, "route the API key")
	}
	return a.stores.Get(ctx, id)
}

// onlyStoreOfSession is the store a signed-in person works in, when they
// work in exactly one.
func (a *App) onlyStoreOfSession(ctx context.Context, r *http.Request) (*Store, error) {
	token, ok := bearerToken(r)
	if !ok {
		return nil, nil
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT m.store_id FROM superuser_sessions s
		JOIN store_members m ON m.superuser_id = s.superuser_id
		JOIN stores st ON st.id = m.store_id
		WHERE s.token_hash = $1 AND s.expires_at > now()
		LIMIT 2`, hashToken(token))
	if err != nil {
		return nil, Internalf(err, "find the person's stores")
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, Internalf(err, "find the person's stores")
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) != 1 {
		return nil, rows.Err()
	}
	return a.stores.Get(ctx, ids[0])
}
