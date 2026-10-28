package gocommerce

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

// The shop's own details: what it is called, where it is, and how to reach it.
//
// Everything else on the Store settings screen comes from the Config the binary
// was started with, and is read-only for a good reason — changing the
// settlement currency mid-flight would change what every order in flight means.
// None of that applies here. A shop's name, address, contact email and tax
// registration are facts about a business that change without a redeploy, so
// they live in a table and are edited from the panel (M42, D66).
//
// They had lived nowhere before this. ext/invoices took the seller's name and
// address from its own module Config, so a shop that moved premises needed new
// environment variables and a restart, and any other module wanting the same
// facts had to be handed them separately.

// StoreProfile is the shop as a business rather than as a configuration.
type StoreProfile struct {
	// Name is the trading name — what a customer would call the shop.
	Name string `json:"name"`
	// LegalName is the registered entity when it differs, which is what has to
	// appear on an invoice in most places.
	LegalName string `json:"legal_name"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`

	AddressLine1 string `json:"address_line1"`
	AddressLine2 string `json:"address_line2"`
	City         string `json:"city"`
	State        string `json:"state"`
	PostalCode   string `json:"postal_code"`
	// Country is an ISO 3166-1 alpha-2 code, the same as every other country
	// on the wire here.
	Country string `json:"country"`

	// TaxID is the VAT, GST or equivalent registration number.
	TaxID string `json:"tax_id"`

	// Timezone is an IANA name — "Europe/London", "Asia/Kolkata". It decides
	// what "today" means on an invoice and in a report: a shop in Auckland on
	// a UTC server otherwise closes its day seventeen hours late. Empty is
	// UTC, which is what the server was doing anyway.
	Timezone string `json:"timezone"`
	// Language is the store's main language (D87): what a request that asks
	// for none is served in, what its catalogue is written in, and what a
	// customer is written to in when nothing says otherwise. It is the first
	// of Languages. Saving one the store does not sell in is refused; empty
	// leaves it as it is.
	Language string `json:"language"`
	// Languages is every language the store sells in, the main one first.
	// Empty leaves them as they are, so a client that does not send them
	// cannot reset them.
	Languages []string `json:"languages"`
	// SupportURL is where a buyer is sent for help, for the foot of an email.
	SupportURL string `json:"support_url"`

	// Currency is what the store sells in, an ISO 4217 code (D85). Saving a
	// different one is refused once the store has an order; empty leaves it
	// as it is, so a client that does not send it cannot reset it.
	Currency string `json:"currency"`
	// CurrencyLocked is true once the store has an order. Read only.
	CurrencyLocked bool `json:"currency_locked"`
}

// MaxProfileField bounds every text field. One limit rather than a different
// one per field: none of these is prose, and a shop that needs more than this
// for its address is describing something else.
const MaxProfileField = 200

// StoreProfiles reads and writes the shop's details.
type StoreProfiles struct{ app *App }

// Profile returns the service.
func (a *App) Profile() *StoreProfiles { return a.profile }

// Get reads the shop's details. The row is created by the migration, so a
// store that has never filled anything in gets blanks rather than an error.
func (p *StoreProfiles) Get(ctx context.Context) (*StoreProfile, error) {
	var out StoreProfile
	err := p.app.db.QueryRowContext(ctx, `
		SELECT name, legal_name, email, phone,
		       address_line1, address_line2, city, state, postal_code, country,
		       tax_id, support_url, timezone
		FROM store_profile WHERE id = 1`).Scan(
		&out.Name, &out.LegalName, &out.Email, &out.Phone,
		&out.AddressLine1, &out.AddressLine2, &out.City, &out.State,
		&out.PostalCode, &out.Country, &out.TaxID, &out.SupportURL,
		&out.Timezone)
	if err == sql.ErrNoRows {
		// Unreachable while the migration's INSERT stands, and harmless if a
		// later one ever changes that: an empty shop is a real answer.
		return &StoreProfile{}, nil
	}
	if err != nil {
		return nil, Internalf(err, "read the store profile")
	}
	out.Languages = p.app.Languages(ctx)
	out.Language = out.Languages[0]
	out.Currency = p.app.Currency(ctx)
	if out.CurrencyLocked, err = p.app.hasOrders(ctx, p.app.db); err != nil {
		return nil, Internalf(err, "read whether the store has orders")
	}
	return &out, nil
}

// Set replaces the shop's details.
//
// The whole profile rather than a patch: it is one short form on one screen,
// and a partial update would need a way to say "clear this field" that an
// empty string already means here.
func (p *StoreProfiles) Set(ctx context.Context, in StoreProfile, by *Superuser) (*StoreProfile, error) {
	fields := map[string]*string{
		"name":         &in.Name,
		"legal name":   &in.LegalName,
		"email":        &in.Email,
		"phone":        &in.Phone,
		"address":      &in.AddressLine1,
		"address line": &in.AddressLine2,
		"city":         &in.City,
		"state":        &in.State,
		"postal code":  &in.PostalCode,
		"country":      &in.Country,
		"tax id":       &in.TaxID,
		"support URL":  &in.SupportURL,
	}
	for name, value := range fields {
		*value = strings.TrimSpace(*value)
		if len([]rune(*value)) > MaxProfileField {
			return nil, Validationf("the %s is at most %d characters", name, MaxProfileField)
		}
	}
	// Upper-cased rather than refused: a country typed in lower case is the
	// right country, and every other country on the wire here is alpha-2.
	in.Country = strings.ToUpper(in.Country)
	if in.Country != "" && len(in.Country) != 2 {
		return nil, Validationf("country is a two-letter ISO code, such as %q", "GB")
	}
	if in.Email != "" && !strings.Contains(in.Email, "@") {
		return nil, Validationf("that does not look like an email address")
	}
	// An Indian store's tax id is its GSTIN, printed on every tax invoice
	// (D137), so a mistyped one is refused as it is typed. Only a changed one
	// is refused: an old value nobody is editing must not block an edit to
	// the phone number.
	if in.Country == "IN" && in.TaxID != "" {
		var stored string
		if err := p.app.db.QueryRowContext(ctx, `SELECT tax_id FROM store_profile WHERE id = 1`).
			Scan(&stored); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, Internalf(err, "read the store profile")
		}
		g, err := ParseGSTIN(in.TaxID)
		if err != nil && in.TaxID != stored {
			return nil, err
		}
		if err == nil {
			in.TaxID = string(g)
			reg := g.State()
			if st, ok := GSTStateOfAddress(Address{Country: "IN", State: in.State}); ok && st.Code != reg.Code {
				return nil, Validationf("the GSTIN %s is registered in %s (%s), but the store's state is %s (%s)",
					g, reg.Name, reg.Code, st.Name, st.Code)
			}
		}
	}
	// Loaded, not pattern-matched: the only useful question about a timezone
	// is whether this machine can resolve it, and a name that looks right and
	// does not load prints the wrong date on every invoice for a year before
	// anybody notices.
	in.Timezone = strings.TrimSpace(in.Timezone)
	if in.Timezone != "" {
		if _, err := time.LoadLocation(in.Timezone); err != nil {
			return nil, Validationf("%q is not a timezone this server can read; use an IANA name such as %q", in.Timezone, "Europe/London")
		}
	}
	// The languages: folded rather than refused, the way the country code is
	// upper-cased — "EN" is the right language typed the wrong way. A main
	// language the store does not sell in is refused rather than added: it
	// would be served to every shopper who asks for nothing.
	var languages []string
	if len(in.Languages) > 0 || strings.TrimSpace(in.Language) != "" {
		current := p.app.Languages(ctx)
		list := in.Languages
		if len(list) == 0 {
			list = current
		}
		langs, err := normalizeLanguages(list, in.Language)
		if err != nil {
			return nil, err
		}
		if !slices.Equal(langs, current) {
			languages = langs
		}
	}

	// The currency: refused, not ignored, once there is an order, because an
	// owner who chose INR and got USD would find out from a customer.
	currency := ""
	if strings.TrimSpace(in.Currency) != "" {
		code, err := normalizeCurrency(in.Currency)
		if err != nil {
			return nil, err
		}
		if code != p.app.Currency(ctx) {
			currency = code
		}
	}

	var updatedBy *int64
	if by != nil {
		updatedBy = &by.ID
	}
	err := InTx(ctx, p.app.db, func(tx *sql.Tx) error {
		if currency != "" {
			locked, err := p.app.hasOrders(ctx, tx)
			if err != nil {
				return err
			}
			if locked {
				return Conflictf("this store has orders that were paid or not cancelled, so its currency is fixed: changing it would change what their amounts mean")
			}
			if err := p.app.setCurrency(ctx, tx, currency); err != nil {
				return err
			}
		}
		if languages != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE stores SET languages = $1::text[] WHERE id = $2`,
				stringArray(languages), p.app.storeOf(ctx)); err != nil {
				return err
			}
			if oldMain := p.app.mainLanguage(ctx); languages[0] != oldMain {
				if err := p.app.notifyTemplates.keepOldWording(ctx, tx, oldMain); err != nil {
					return err
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `
		UPDATE store_profile SET
		    name = $1, legal_name = $2, email = $3, phone = $4,
		    address_line1 = $5, address_line2 = $6, city = $7, state = $8,
		    postal_code = $9, country = $10, tax_id = $11, support_url = $12,
		    timezone = $13, updated_at = now(), updated_by = $14
		WHERE id = 1`,
			in.Name, in.LegalName, in.Email, in.Phone,
			in.AddressLine1, in.AddressLine2, in.City, in.State,
			in.PostalCode, in.Country, in.TaxID, in.SupportURL,
			in.Timezone, updatedBy); err != nil {
			return err
		}
		// A store has one name: this one, which is what the store list and
		// the account menu show (D83). Blank leaves the old one, since a
		// store listed as nothing cannot be picked out.
		if in.Name == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE stores SET name = $1 WHERE id = $2`, in.Name, p.app.storeOf(ctx))
		return err
	})
	var api *APIError
	if errors.As(err, &api) {
		return nil, api
	}
	if err != nil {
		return nil, Internalf(err, "save the store profile")
	}
	if currency != "" {
		ctx = withStoreCurrency(ctx, currency)
	}
	if languages != nil {
		ctx = withStoreLanguages(ctx, languages)
	}
	return p.Get(ctx)
}

// LocationOf is the store's clock, or UTC.
//
// It never returns nil and never fails: a stored timezone was loadable when it
// was saved, and if the machine's zone database has since lost it, printing a
// date in UTC is a better answer than refusing to print the invoice.
func (p *StoreProfiles) LocationOf(ctx context.Context) *time.Location {
	profile, err := p.Get(ctx)
	if err != nil || profile.Timezone == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(profile.Timezone)
	if err != nil {
		return time.UTC
	}
	return loc
}

// MessageLanguage is the language to write a customer in.
//
// What the order recorded about this customer, while the store still sells in
// it, else the store's main language. The customer's own preference wins over
// the shop's — it is the one fact here about the person being written to — but
// a language the store has since dropped is one nobody is keeping its wording
// up to date in. A regional tag matches its language, so an order placed in
// "fr-CA" is written to in a store's "fr".
func (p *StoreProfiles) MessageLanguage(ctx context.Context, preferred string) string {
	langs := p.app.Languages(ctx)
	if m, ok := matchLanguage(langs, strings.TrimSpace(preferred)); ok {
		return m
	}
	return langs[0]
}

// ------------------------------------------------------------------- routes

func (a *App) mountStoreProfileRoutes() {
	// Reading carries no right, the way GET /api/admin/settings does not: a
	// shop's own name and address is not a secret from its own staff, and
	// several screens want it — an invoice, an email footer, a packing slip.
	a.HandleAdminFunc("GET /api/admin/store", a.handleGetStoreProfile)
	// Writing is store.write: this is the shop's legal identity, and it is
	// what appears on documents a buyer keeps.
	a.HandleAdminFunc("PATCH /api/admin/store", a.handleSetStoreProfile, RightStoreWrite)
	// The public face, for a page a buyer opens before signing in — the trade
	// portal's sign-in (D77), a storefront's footer. Without it the portal
	// could only greet a buyer with the engine's name, which they have never
	// heard of.
	a.HandleFunc("GET /api/store", a.handlePublicStore)
}

// publicStore is what anyone may read about the shop: what it is called and
// how a buyer reaches it, which is what the foot of every email it sends
// already says. The address, legal name and tax number stay behind the admin
// route: a page that names a shop has no need to publish where it is.
//
// Its languages are public too, so a storefront can offer the ones it sells in.
type publicStore struct {
	Name       string   `json:"name"`
	Email      string   `json:"email"`
	Phone      string   `json:"phone"`
	SupportURL string   `json:"support_url"`
	Language   string   `json:"language"`
	Languages  []string `json:"languages"`
	// ImagesPath is where this store's pictures are served resized (D142), on
	// the engine's own origin, for a storefront to put in its image CDN setting.
	ImagesPath string `json:"images_path"`
}

func (a *App) handlePublicStore(w http.ResponseWriter, r *http.Request) {
	p, err := a.profile.Get(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, publicStore{Name: p.Name, Email: p.Email, Phone: p.Phone, SupportURL: p.SupportURL,
		Language: p.Language, Languages: p.Languages, ImagesPath: ImagesPath(a.storeOf(r.Context()))})
}

func (a *App) handleGetStoreProfile(w http.ResponseWriter, r *http.Request) {
	profile, err := a.profile.Get(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	Respond(w, http.StatusOK, profile)
}

func (a *App) handleSetStoreProfile(w http.ResponseWriter, r *http.Request) {
	var in StoreProfile
	if err := DecodeJSON(w, r, &in); err != nil {
		RespondError(w, r, err)
		return
	}
	saved, err := a.profile.Set(r.Context(), in, SuperuserFrom(r.Context()))
	if err != nil {
		RespondError(w, r, err)
		return
	}
	a.log.Info("store profile changed", "name", saved.Name)
	Respond(w, http.StatusOK, saved)
}
