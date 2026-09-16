package gocommerce

import (
	"context"
	"database/sql"
	"net/http"
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
	// Language is which one a customer is written to in when the order
	// recorded no preference. It is not Config.DefaultLanguage, which answers
	// a different question — what to serve a request that asked for no
	// particular language (D21). This one is the store's choice about its own
	// outgoing messages, so it is a setting rather than a start-up decision,
	// and it is constrained to the languages the binary actually has content
	// for. Empty means the binary's.
	Language string `json:"language"`
	// SupportURL is where a buyer is sent for help, for the foot of an email.
	SupportURL string `json:"support_url"`
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
		       tax_id, support_url, timezone, language
		FROM store_profile WHERE id = 1`).Scan(
		&out.Name, &out.LegalName, &out.Email, &out.Phone,
		&out.AddressLine1, &out.AddressLine2, &out.City, &out.State,
		&out.PostalCode, &out.Country, &out.TaxID, &out.SupportURL,
		&out.Timezone, &out.Language)
	if err == sql.ErrNoRows {
		// Unreachable while the migration's INSERT stands, and harmless if a
		// later one ever changes that: an empty shop is a real answer.
		return &StoreProfile{}, nil
	}
	if err != nil {
		return nil, Internalf(err, "read the store profile")
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
	// Folded rather than refused, the way the country code is upper-cased:
	// "EN" is the right language typed the wrong way.
	in.Language = strings.ToLower(strings.TrimSpace(in.Language))
	if in.Language != "" && !containsFold(p.app.cfg.Languages, in.Language) {
		// Refused rather than accepted quietly, because choosing a language
		// nothing is translated into sends every customer a message in it.
		return nil, Validationf("this store has no content in %q; it was started with %s",
			in.Language, strings.Join(p.app.cfg.Languages, ", "))
	}

	var updatedBy *int64
	if by != nil {
		updatedBy = &by.ID
	}
	_, err := p.app.db.ExecContext(ctx, `
		UPDATE store_profile SET
		    name = $1, legal_name = $2, email = $3, phone = $4,
		    address_line1 = $5, address_line2 = $6, city = $7, state = $8,
		    postal_code = $9, country = $10, tax_id = $11, support_url = $12,
		    timezone = $13, language = $14,
		    updated_at = now(), updated_by = $15
		WHERE id = 1`,
		in.Name, in.LegalName, in.Email, in.Phone,
		in.AddressLine1, in.AddressLine2, in.City, in.State,
		in.PostalCode, in.Country, in.TaxID, in.SupportURL,
		in.Timezone, in.Language, updatedBy)
	if err != nil {
		return nil, Internalf(err, "save the store profile")
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
// Three answers in order of who is nearest: what the order recorded about this
// customer, then what the store chose, then what the binary was started with.
// The customer's own preference wins over the shop's — it is the one fact here
// about the person being written to.
func (p *StoreProfiles) MessageLanguage(ctx context.Context, preferred string) string {
	if preferred = strings.TrimSpace(preferred); preferred != "" {
		return preferred
	}
	if profile, err := p.Get(ctx); err == nil && profile.Language != "" {
		return profile.Language
	}
	return p.app.cfg.DefaultLanguage
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
