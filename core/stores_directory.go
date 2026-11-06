package gocommerce

import (
	"context"
	"net/http"
)

// The install owner's list of every store, with who owns each (D215). It is
// the one read in the engine that crosses stores on a person's behalf, so it
// answers to Config.PlatformOwnerPhone rather than to any role: an owner of one
// store is a stranger to all the others.

// DirectoryOwner is how to reach one owner of a store. A person has an email,
// a phone or both (D191), and no name: the engine does not ask for one.
type DirectoryOwner struct {
	Email string `json:"email"`
	Phone string `json:"phone"`
}

// DirectoryStore is one row of the install owner's list.
type DirectoryStore struct {
	ID     string `json:"id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// LegalName is what the store typed as its registered business, empty when
	// it has not.
	LegalName string           `json:"legal_name"`
	Owners    []DirectoryOwner `json:"owners"`
	Products  int              `json:"products"`
	Orders    int              `json:"orders"`
}

// isPlatformOwner asks for a proved number because a number given at sign-up is
// not checked (D191): unproved, whoever typed this one first would hold every
// store's owners and counts. A static admin token has no phone, so it is out
// too: it is passed around to scripts, and this list is for one person.
func (a *App) isPlatformOwner(su *Superuser) bool {
	if su == nil || !su.phoneProved {
		return false
	}
	want, err := NormalizePhone(a.cfg.PlatformOwnerPhone)
	return err == nil && su.Phone == want
}

func (a *App) handleStoreDirectoryAllowed(w http.ResponseWriter, r *http.Request) {
	Respond(w, http.StatusOK, map[string]bool{"allowed": a.isPlatformOwner(SuperuserFrom(r.Context()))})
}

func (a *App) handleStoreDirectory(w http.ResponseWriter, r *http.Request) {
	if !a.isPlatformOwner(SuperuserFrom(r.Context())) {
		RespondError(w, r, Forbiddenf("this list is for the install's owner"))
		return
	}
	list, err := a.storeDirectory(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	// Other people's phone numbers and addresses: not for a shared cache.
	w.Header().Set("Cache-Control", "no-store")
	Respond(w, http.StatusOK, list)
}

// storeDirectory lists every store oldest first, suspended ones included.
//
// ponytail: one round of statements per store, because row security only shows
// a statement the store it is bound to. Fine for hundreds of stores; thousands
// want the counts kept in a table a sweep refreshes.
func (a *App) storeDirectory(ctx context.Context) ([]DirectoryStore, error) {
	stores, err := a.stores.list(ctx, "")
	if err != nil {
		return nil, err
	}
	owners, err := a.storeOwners(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DirectoryStore, 0, len(stores))
	for _, st := range stores {
		row := DirectoryStore{ID: st.ID, Slug: st.Slug, Name: st.Name, Status: st.Status, Owners: owners[st.ID]}
		if row.Owners == nil {
			row.Owners = []DirectoryOwner{}
		}
		if err := a.db.QueryRowContext(WithStore(ctx, st.ID), `SELECT
			coalesce((SELECT legal_name FROM store_profile LIMIT 1), ''),
			(SELECT count(*) FROM products),
			(SELECT count(*) FROM orders)`).Scan(&row.LegalName, &row.Products, &row.Orders); err != nil {
			return nil, Internalf(err, "count store %s", st.ID)
		}
		out = append(out, row)
	}
	return out, nil
}

func (a *App) storeOwners(ctx context.Context) (map[string][]DirectoryOwner, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT m.store_id, coalesce(su.email, ''), coalesce(su.phone, '')
		FROM store_members m JOIN superusers su ON su.id = m.superuser_id
		WHERE m.role = $1 ORDER BY m.created_at, su.id`, RoleOwner)
	if err != nil {
		return nil, Internalf(err, "list store owners")
	}
	defer rows.Close()
	out := map[string][]DirectoryOwner{}
	for rows.Next() {
		var id string
		var o DirectoryOwner
		if err := rows.Scan(&id, &o.Email, &o.Phone); err != nil {
			return nil, Internalf(err, "scan store owner")
		}
		out[id] = append(out[id], o)
	}
	return out, rows.Err()
}
