package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"slices"
	"time"
)

// The install owner's list of every store, with who owns each (D215). It is
// the one read in the engine that crosses stores on a person's behalf, so it
// answers to Config.PlatformOwnerPhone rather than to any role: an owner of one
// store is a stranger to all the others.

// DirectoryOwner is how to reach one owner of a store. A person has an email,
// a phone or both (D191), and no name: the engine does not ask for one.
type DirectoryOwner struct {
	// ID is what the email to this owner is addressed by (D221).
	ID    int64  `json:"id"`
	Email string `json:"email"`
	Phone string `json:"phone"`
	// EmailVerified and PhoneVerified say a code or an emailed link proved the
	// address (M105) or the number. Signing up proves neither (D191).
	EmailVerified bool `json:"email_verified"`
	PhoneVerified bool `json:"phone_verified"`
	// EmailSent is the email asking them to verify, while it is still waiting
	// on them (D225).
	EmailSent *OwnerEmailSent `json:"email_sent,omitempty"`
}

// DirectoryStore is one row of the install owner's list.
type DirectoryStore struct {
	ID     string `json:"id"`
	Slug   string `json:"slug"`
	Name   string `json:"name"`
	Status string `json:"status"`
	// Domain is the store's verified domain, primary first, else its subdomain,
	// else empty.
	Domain string `json:"domain"`
	// LegalName is what the store typed as its registered business, empty when
	// it has not.
	LegalName string           `json:"legal_name"`
	Owners    []DirectoryOwner `json:"owners"`
	Products  int              `json:"products"`
	Orders    int              `json:"orders"`
	CreatedAt time.Time        `json:"created_at"`
	// LastActivity is the newest thing the audit trail or the event log holds:
	// a sentence for what staff did, an event name such as order.placed for what
	// a shopper did. Empty, with a nil time, when the store has neither.
	LastActivity   string     `json:"last_activity"`
	LastActivityAt *time.Time `json:"last_activity_at"`
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

// storeDirectory lists every store, suspended ones included, the most recently
// active first: the list is read to see who is using the product. A store that
// has done nothing goes after those, newest first, so a new one is still easy
// to find.
//
// ponytail: one round of statements per store, because row security only shows
// a statement the store it is bound to. Fine for hundreds of stores; thousands
// want the counts kept in a table a sweep refreshes.
func (a *App) storeDirectory(ctx context.Context) ([]DirectoryStore, error) {
	stores, err := a.stores.list(ctx, "")
	if err != nil {
		return nil, err
	}
	slices.Reverse(stores)
	owners, err := a.storeOwners(ctx)
	if err != nil {
		return nil, err
	}
	domains, err := a.storeHosts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]DirectoryStore, 0, len(stores))
	for _, st := range stores {
		row := DirectoryStore{ID: st.ID, Slug: st.Slug, Name: st.Name, Status: st.Status, Owners: owners[st.ID], CreatedAt: st.CreatedAt}
		if row.Owners == nil {
			row.Owners = []DirectoryOwner{}
		}
		row.Domain = domains[st.ID]
		if base := a.storeBrand(st.BrandHost).BaseDomain; row.Domain == "" && base != "" {
			row.Domain = st.Slug + "." + base
		}
		// One action can write both logs in one transaction, so at the same
		// instant: the audit row wins the tie, because its sentence reads better.
		var at sql.NullTime
		if err := a.db.QueryRowContext(WithStore(ctx, st.ID), `SELECT
			coalesce((SELECT legal_name FROM store_profile LIMIT 1), ''),
			(SELECT count(*) FROM products),
			(SELECT count(*) FROM orders),
			coalesce(latest.activity, ''), latest.happened
			FROM (SELECT 1) AS one LEFT JOIN LATERAL (
				SELECT activity, happened FROM (
					(SELECT coalesce(nullif(summary, ''), action) AS activity, created_at AS happened, 0 AS tie
						FROM admin_audit ORDER BY created_at DESC, id DESC LIMIT 1)
					UNION ALL
					(SELECT event_name, created_at, 1 FROM outbox_events ORDER BY created_at DESC, id DESC LIMIT 1)
				) newest ORDER BY happened DESC, tie LIMIT 1
			) latest ON true`).Scan(&row.LegalName, &row.Products, &row.Orders, &row.LastActivity, &at); err != nil {
			return nil, Internalf(err, "count store %s", st.ID)
		}
		if at.Valid {
			row.LastActivityAt = &at.Time
		}
		out = append(out, row)
	}
	slices.SortStableFunc(out, func(x, y DirectoryStore) int {
		switch {
		case x.LastActivityAt == nil && y.LastActivityAt == nil:
			return 0
		case x.LastActivityAt == nil:
			return 1
		case y.LastActivityAt == nil:
			return -1
		}
		return y.LastActivityAt.Compare(*x.LastActivityAt)
	})
	return out, nil
}

func (a *App) storeOwners(ctx context.Context) (map[string][]DirectoryOwner, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT m.store_id, su.id, coalesce(su.email, ''), coalesce(su.phone, ''),
			coalesce(su.email = su.email_proved, false),
			su.phone IS NOT NULL AND su.phone_proved_at IS NOT NULL,
			CASE WHEN su.email = su.owner_email_sent->>'email' AND su.email IS DISTINCT FROM su.email_proved
				THEN su.owner_email_sent::text END
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
		var sent sql.NullString
		if err := rows.Scan(&id, &o.ID, &o.Email, &o.Phone, &o.EmailVerified, &o.PhoneVerified, &sent); err != nil {
			return nil, Internalf(err, "scan store owner")
		}
		if sent.Valid {
			o.EmailSent = &OwnerEmailSent{}
			if err := json.Unmarshal([]byte(sent.String), o.EmailSent); err != nil {
				return nil, Internalf(err, "read the email sent to a store owner")
			}
		}
		out[id] = append(out[id], o)
	}
	return out, rows.Err()
}

// storeHosts is each store's verified domain, chosen as myStores chooses one.
func (a *App) storeHosts(ctx context.Context) (map[string]string, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT DISTINCT ON (store_id) store_id, host FROM store_domains
		WHERE verified_at IS NOT NULL ORDER BY store_id, is_primary DESC, created_at, host`)
	if err != nil {
		return nil, Internalf(err, "list store domains")
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, host string
		if err := rows.Scan(&id, &host); err != nil {
			return nil, Internalf(err, "scan store domain")
		}
		out[id] = host
	}
	return out, rows.Err()
}
