package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
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
	// Domain is where the store's shop is: its first attached domain, else
	// <slug>.<Config.BaseDomain>, else empty when neither exists.
	Domain string `json:"domain"`
}

// storeCookie only says which store a new tab opens in. The panel keeps each
// tab's own store and names it on every request (D83), so the cookie is the
// last resort in storeMW's order, after the header.
const storeCookie = "litekart_store_id"

func (a *App) mountStoreRoutes() {
	a.Handle("GET /api/stores/my", a.personAuth(http.HandlerFunc(a.handleMyStores)))
	a.Handle("POST /api/stores/switch", a.personAuth(http.HandlerFunc(a.handleSwitchStore)))
	a.Handle("POST /api/stores/create", a.personAuth(http.HandlerFunc(a.handleCreateStore)))
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

// myStores lists the stores the caller may enter. A static admin token is the
// install's operator, so it sees every store, as owner.
func (a *App) myStores(ctx context.Context) ([]MyStore, error) {
	const domain = `(SELECT host FROM store_domains d WHERE d.store_id = st.id ORDER BY created_at, host LIMIT 1)`
	q := `SELECT st.id, st.slug, st.name, st.status, st.demo, m.role, ` + domain + `
		FROM store_members m JOIN stores st ON st.id = m.store_id
		WHERE m.superuser_id = $1 ORDER BY st.created_at, st.id`
	var args []any
	if su := SuperuserFrom(ctx); su != nil {
		args = append(args, su.ID)
	} else {
		q = `SELECT st.id, st.slug, st.name, st.status, st.demo, '` + RoleOwner + `', ` + domain + `
			FROM stores st ORDER BY st.created_at, st.id`
	}
	rows, err := a.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, Internalf(err, "list the person's stores")
	}
	defer rows.Close()
	out := []MyStore{}
	for rows.Next() {
		var s MyStore
		var host sql.NullString
		if err := rows.Scan(&s.ID, &s.Slug, &s.Name, &s.Status, &s.Demo, &s.Role, &host); err != nil {
			return nil, Internalf(err, "scan store")
		}
		s.Domain = host.String
		if s.Domain == "" && a.cfg.BaseDomain != "" {
			s.Domain = s.Slug + "." + strings.ToLower(a.cfg.BaseDomain)
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
	setStoreCookie(w, r, hit.ID)
	Respond(w, http.StatusOK, entry)
}

// handleCreateStore opens a store with the caller as its owner. Any signed-in
// person may: somebody with no store yet has nowhere else to start, and the
// new store is theirs alone.
func (a *App) handleCreateStore(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
		Slug string `json:"slug"`
	}
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	st, err := a.stores.Create(ctx, in.Name, in.Slug)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if su := SuperuserFrom(ctx); su != nil {
		if _, err := a.db.ExecContext(ctx, `
			INSERT INTO store_members (store_id, superuser_id, role) VALUES ($1, $2, $3)`,
			st.ID, su.ID, RoleOwner); err != nil {
			// A store nobody can enter is worse than none: take it back out.
			_, _ = a.db.ExecContext(context.WithoutCancel(ctx), `DELETE FROM stores WHERE id = $1`, st.ID)
			RespondError(w, r, Internalf(err, "make the creator the store's owner"))
			return
		}
	}
	entry, err := a.enterStore(ctx, st.ID)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.log.Info("store created", "store", st.ID, "slug", st.Slug)
	setStoreCookie(w, r, st.ID)
	Respond(w, http.StatusCreated, entry)
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

func setStoreCookie(w http.ResponseWriter, r *http.Request, id string) {
	writeStoreCookie(w, r, id, int((365 * 24 * time.Hour).Seconds()))
}

func clearStoreCookie(w http.ResponseWriter, r *http.Request) {
	writeStoreCookie(w, r, "", -1)
}

func writeStoreCookie(w http.ResponseWriter, r *http.Request, id string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     storeCookie,
		Value:    id,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		SameSite: http.SameSiteLaxMode,
	})
}
