package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
)

// A name for a store nobody named.
//
// Opening a store asked for a name and an address before anything else, and a
// fresh install's first store had neither until its owner found Store
// settings, so lists and emails showed a blank. Now a store is never without
// one: the create form offers a suggestion the way GitHub offers
// "fuzzy-octo-guacamole" for a new repository, a store opened without a name
// takes it, and stores that had none in the directory were given one (0067). The words are a
// shop's — an adjective and a place to buy things — so every pairing reads as
// a plausible shop: "Saffron Orchard", "Brisk Lantern".

var storeNameAdjectives = []string{
	"Amber", "Breezy", "Bright", "Brisk", "Cedar", "Cobalt", "Coral", "Cosy", "Crimson", "Dawn",
	"Ember", "Fern", "Golden", "Hazel", "Honey", "Indigo", "Ivory", "Jade", "Juniper", "Lucky",
	"Maple", "Meadow", "Misty", "Nimble", "Olive", "Opal", "Pearl", "Plum", "Quiet", "Ruby",
	"Saffron", "Sage", "Silver", "Sunny", "Swift", "Teal", "Tidy", "Velvet", "Willow", "Zesty",
}

var storeNameNouns = []string{
	"Arcade", "Atelier", "Basket", "Bay", "Bazaar", "Boutique", "Bridge", "Cabin", "Corner", "Cove",
	"Crate", "Depot", "Emporium", "Garden", "Goods", "Grove", "Harbor", "Haven", "Hive", "Lane",
	"Lantern", "Loft", "Market", "Mill", "Nest", "Orchard", "Outpost", "Pantry", "Parlour", "Pavilion",
	"Shelf", "Shop", "Square", "Stall", "Studio", "Trader", "Trove", "Wharf", "Works", "Yard",
}

// randomStoreName is one pairing, as a name and as the address it gives.
func randomStoreName() (name, slug string) {
	adj := storeNameAdjectives[rand.IntN(len(storeNameAdjectives))]
	noun := storeNameNouns[rand.IntN(len(storeNameNouns))]
	return adj + " " + noun, strings.ToLower(adj + "-" + noun)
}

// storeSlugFrom is a name as an address: lower-case letters and digits with
// single hyphens, or "" for a name with none of them, such as one written in
// Devanagari.
func storeSlugFrom(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 63 {
		s = strings.Trim(s[:63], "-")
	}
	return s
}

// SuggestName is a name for a new store, with an address no store has.
func (s *Stores) SuggestName(ctx context.Context) (name, slug string, err error) {
	return s.suggestName(ctx, randomStoreName)
}

func (s *Stores) suggestName(ctx context.Context, pick func() (string, string)) (string, string, error) {
	// Sixteen hundred pairings; a few tries find a free one in any install a
	// person would run, and a number settles the rest.
	for range 8 {
		name, slug := pick()
		taken, err := s.slugTaken(ctx, slug)
		if err != nil {
			return "", "", err
		}
		if !taken {
			return name, slug, nil
		}
	}
	name, slug := pick()
	slug, err := s.freeSlug(ctx, slug)
	return name, slug, err
}

// freeSlug is base if no store has it, else base with a number.
func (s *Stores) freeSlug(ctx context.Context, base string) (string, error) {
	candidate := base
	for i := range 20 {
		if i > 0 {
			suffix := fmt.Sprintf("-%d", 100+rand.IntN(900))
			candidate = strings.Trim(base[:min(len(base), 63-len(suffix))], "-") + suffix
		}
		taken, err := s.slugTaken(ctx, candidate)
		if err != nil {
			return "", err
		}
		if !taken {
			return candidate, nil
		}
	}
	return "", Conflictf("could not find a free address starting %q; choose one", base)
}

func (s *Stores) slugTaken(ctx context.Context, slug string) (bool, error) {
	// A reserved address is taken by the platform, so a name or a suggestion
	// that would land on one is numbered like any other taken address.
	if reservedSlug(slug) {
		return true, nil
	}
	var taken bool
	if err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM stores WHERE slug = $1)`, slug).Scan(&taken); err != nil {
		return false, Internalf(err, "check a store address")
	}
	return taken, nil
}

// migrate0067NameUnnamedStores gives every store without a name one in the
// directory — what store lists and the account menu show. The profile's name,
// which buyers see on the storefront, invoices and emails, is left as it is: a
// live shop is not renamed in front of its customers by an upgrade. A profile
// that already has a name lends it to the directory, rather than the two
// differing; it is read bound to its store, as row security requires.
func migrate0067NameUnnamedStores(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT id FROM stores WHERE trim(name) = ''`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range ids {
		var name string
		err := tx.QueryRowContext(WithStore(ctx, id), `SELECT trim(name) FROM store_profile`).Scan(&name)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if name == "" {
			name, _ = randomStoreName()
		}
		if _, err := tx.ExecContext(ctx, `UPDATE stores SET name = $1 WHERE id = $2`, name, id); err != nil {
			return err
		}
	}
	return nil
}

// reservedSlugs are addresses no store may have. A store's address is also a
// host, <slug>.<Config.BaseDomain>, and where strangers can open stores
// (Config.Signup) one called "admin" or "login" is a page that looks like the
// platform's own; the mail and DNS names are the hosts a domain's provider
// expects to find there.
var reservedSlugs = map[string]bool{
	"www": true, "admin": true, "api": true, "app": true, "auth": true, "login": true,
	"signin": true, "signup": true, "account": true, "accounts": true, "billing": true,
	"dash": true, "dashboard": true, "panel": true, "console": true, "platform": true,
	"shops": true, "docs": true, "help": true, "support": true, "status": true, "blog": true,
	"cdn": true, "static": true, "assets": true, "media": true, "secure": true,
	"mail": true, "email": true, "webmail": true, "smtp": true, "imap": true, "pop": true,
	"mx": true, "ns1": true, "ns2": true, "ftp": true, "autodiscover": true, "localhost": true,
}

func reservedSlug(slug string) bool { return reservedSlugs[slug] }
