package gocommerce

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
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

	// The store's pictures (D148), each an image from its own media library,
	// held by id as a vendor's logo is: the logo a storefront's header shows,
	// one for where the logo sits on a dark background, and a square icon for
	// the browser tab and a phone's home screen. Absent leaves one as it is,
	// as the currency does, so the details form and a script that predates
	// them cannot wipe a logo; 0 removes it.
	LogoMediaID     *int64 `json:"logo_media_id"`
	LogoDarkMediaID *int64 `json:"logo_dark_media_id"`
	IconMediaID     *int64 `json:"icon_media_id"`
	// Their addresses, as the media library serves them. Read only.
	LogoURL     string `json:"logo_url"`
	LogoDarkURL string `json:"logo_dark_url"`
	IconURL     string `json:"icon_url"`

	// What a storefront's contact page shows (D156). Each is absent-leaves-it
	// like the pictures, so a script that predates them cannot wipe them.
	//
	// ShowAddress publishes the address on GET /api/store. Off by default: it
	// is the address an invoice prints, and for a shop run from home that is
	// somebody's front door.
	ShowAddress *bool `json:"show_address"`
	// PricesIncludeTax says the catalog's prices already contain tax, so
	// checkout works the tax out of them rather than adding it (D177). Read,
	// it is the store's answer, the install's default until the store has
	// given one. Absent on a save leaves it as it is.
	PricesIncludeTax *bool `json:"prices_include_tax"`
	// MapLocation pins the shop when the address alone would put the pin in
	// the wrong place: a Google Maps link, or "latitude,longitude". It exists
	// only for the storefront, so it is public whenever it is set.
	MapLocation *string `json:"map_location"`
	// OpeningHours is one range per open day, Monday first, on the store's
	// own clock. A day not listed is closed, and an empty list says nothing.
	// Null leaves them as they are; [] clears them.
	OpeningHours []OpeningHours `json:"opening_hours"`
}

// OpeningHours is when the shop is open on one day of the week.
//
// ponytail: one range per day, closing the same day. A lunch break or a shop
// open past midnight needs a list per day; add it when a shop asks.
type OpeningHours struct {
	// Day is mon, tue, wed, thu, fri, sat or sun.
	Day string `json:"day"`
	// Opens and Closes are 24-hour HH:MM; Closes is the later.
	Opens  string `json:"opens"`
	Closes string `json:"closes"`
}

// weekdays is the order opening hours are kept and shown in.
var weekdays = []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}

// MaxMapLocation bounds a map location. Longer than the other fields: a
// Google Maps place link carries its coordinates and a good deal else.
const MaxMapLocation = 2000

// googleMapsHost is a Google host that serves maps under /maps: google.com,
// a country's google.co.in or google.de, with or without www.
var googleMapsHost = regexp.MustCompile(`^(www\.)?google\.(com|[a-z]{2}|co\.[a-z]{2}|com\.[a-z]{2})$`)

// latLong is "latitude,longitude" in decimal degrees, spaces allowed.
var latLong = regexp.MustCompile(`^(-?\d{1,3}(?:\.\d+)?)\s*,\s*(-?\d{1,3}(?:\.\d+)?)$`)

// iframeSrc is the address in the snippet Google's "Embed a map" hands out,
// which is what an owner copies.
var iframeSrc = regexp.MustCompile(`(?i)^<iframe\b[^>]*\bsrc\s*=\s*["']([^"']+)["']`)

// normalizeMapLocation keeps a location a storefront can draw a map from or
// link to, and nothing else, because it puts it in a frame on its contact
// page: a pair of coordinates, or an https link to Google Maps or to a
// Business Profile.
func normalizeMapLocation(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if m := iframeSrc.FindStringSubmatch(s); m != nil {
		s = html.UnescapeString(m[1])
	}
	bad := Validationf("the map location is a Google Maps link or a latitude and longitude, such as %q", "20.2961, 85.8245")
	if len(s) > MaxMapLocation {
		return "", Validationf("the map location is at most %d characters", MaxMapLocation)
	}
	if m := latLong.FindStringSubmatch(s); m != nil {
		lat, _ := strconv.ParseFloat(m[1], 64)
		lng, _ := strconv.ParseFloat(m[2], 64)
		if lat < -90 || lat > 90 || lng < -180 || lng > 180 {
			return "", bad
		}
		return m[1] + "," + m[2], nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" {
		return "", bad
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "maps.app.goo.gl", host == "g.page":
	case host == "goo.gl" && strings.HasPrefix(u.Path, "/maps"):
	case host == "g.co" && strings.HasPrefix(u.Path, "/kgs/"):
	case strings.HasPrefix(host, "maps.") && googleMapsHost.MatchString(strings.TrimPrefix(host, "maps.")):
	case googleMapsHost.MatchString(host) && strings.HasPrefix(u.Path, "/maps"):
	default:
		return "", bad
	}
	return s, nil
}

// normalizeOpeningHours folds the days, pads the times and sorts the week, so
// every reader can print the list as it comes.
func normalizeOpeningHours(in []OpeningHours) ([]OpeningHours, error) {
	out := make([]OpeningHours, 0, len(in))
	seen := map[string]bool{}
	for _, h := range in {
		day := strings.ToLower(strings.TrimSpace(h.Day))
		if !slices.Contains(weekdays, day) {
			return nil, Validationf("opening hours: %q is not a day; use mon, tue, wed, thu, fri, sat or sun", h.Day)
		}
		if seen[day] {
			return nil, Validationf("opening hours: %s is listed twice; give each day one range", day)
		}
		seen[day] = true
		opens, err1 := time.Parse("15:04", strings.TrimSpace(h.Opens))
		closes, err2 := time.Parse("15:04", strings.TrimSpace(h.Closes))
		if err1 != nil || err2 != nil {
			return nil, Validationf("opening hours: %s needs an opening and a closing time, such as 09:30 and 18:00", day)
		}
		if !closes.After(opens) {
			return nil, Validationf("opening hours: on %s the shop closes before it opens", day)
		}
		out = append(out, OpeningHours{Day: day, Opens: opens.Format("15:04"), Closes: closes.Format("15:04")})
	}
	slices.SortFunc(out, func(a, b OpeningHours) int {
		return slices.Index(weekdays, a.Day) - slices.Index(weekdays, b.Day)
	})
	return out, nil
}

// MaxProfileField bounds every text field. One limit rather than a different
// one per field: none of these is prose, and a shop that needs more than this
// for its address is describing something else.
const MaxProfileField = 200

// MaxStoreImageBytes bounds a logo or icon. Every page of the storefront
// loads it, on a phone, so a photograph-sized file there is a slow shop.
const MaxStoreImageBytes = 2 << 20

// StoreProfiles reads and writes the shop's details.
type StoreProfiles struct{ app *App }

// Profile returns the service.
func (a *App) Profile() *StoreProfiles { return a.profile }

// Get reads the shop's details. The row is created by the migration, so a
// store that has never filled anything in gets blanks rather than an error.
func (p *StoreProfiles) Get(ctx context.Context) (*StoreProfile, error) {
	var (
		out         StoreProfile
		showAddress bool
		mapLocation string
		hours       []byte
	)
	err := p.app.db.QueryRowContext(ctx, `
		SELECT p.name, p.legal_name, p.email, p.phone,
		       p.address_line1, p.address_line2, p.city, p.state, p.postal_code, p.country,
		       p.tax_id, p.support_url, p.timezone,
		       p.logo_media_id, p.logo_dark_media_id, p.icon_media_id,
		       coalesce(l.url, ''), coalesce(d.url, ''), coalesce(i.url, ''),
		       p.show_address, p.map_location, p.opening_hours
		FROM store_profile p
		LEFT JOIN media l ON l.id = p.logo_media_id
		LEFT JOIN media d ON d.id = p.logo_dark_media_id
		LEFT JOIN media i ON i.id = p.icon_media_id
		WHERE p.id = 1`).Scan(
		&out.Name, &out.LegalName, &out.Email, &out.Phone,
		&out.AddressLine1, &out.AddressLine2, &out.City, &out.State,
		&out.PostalCode, &out.Country, &out.TaxID, &out.SupportURL,
		&out.Timezone,
		&out.LogoMediaID, &out.LogoDarkMediaID, &out.IconMediaID,
		&out.LogoURL, &out.LogoDarkURL, &out.IconURL,
		&showAddress, &mapLocation, &hours)
	if err == sql.ErrNoRows {
		// Unreachable while the migration's INSERT stands, and harmless if a
		// later one ever changes that: an empty shop is a real answer.
		return &StoreProfile{ShowAddress: new(bool), MapLocation: new(string), OpeningHours: []OpeningHours{}}, nil
	}
	if err != nil {
		return nil, Internalf(err, "read the store profile")
	}
	out.ShowAddress, out.MapLocation = &showAddress, &mapLocation
	inclusive, err := p.app.pricesIncludeTax(ctx, p.app.db)
	if err != nil {
		return nil, Internalf(err, "read whether prices include tax")
	}
	out.PricesIncludeTax = &inclusive
	if err := json.Unmarshal(hours, &out.OpeningHours); err != nil {
		return nil, Internalf(err, "read the store's opening hours")
	}
	if out.OpeningHours == nil {
		out.OpeningHours = []OpeningHours{}
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
	// Nil leaves each as it is, which the UPDATE's coalesce does; so the
	// hours travel as JSON only when they were sent.
	if in.MapLocation != nil {
		loc, err := normalizeMapLocation(*in.MapLocation)
		if err != nil {
			return nil, err
		}
		in.MapLocation = &loc
	}
	var hours *string
	if in.OpeningHours != nil {
		list, err := normalizeOpeningHours(in.OpeningHours)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(list)
		if err != nil {
			return nil, Internalf(err, "write the store's opening hours")
		}
		h := string(b)
		hours = &h
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

	images := []struct {
		column, name string
		id           *int64
	}{
		{"logo_media_id", "logo", in.LogoMediaID},
		{"logo_dark_media_id", "logo for dark backgrounds", in.LogoDarkMediaID},
		{"icon_media_id", "icon", in.IconMediaID},
	}
	for _, img := range images {
		if img.id == nil || *img.id == 0 {
			continue
		}
		item, err := p.app.MediaLibrary().Get(ctx, *img.id)
		if err != nil {
			return nil, Validationf("the %s is not a file in this store's media library", img.name)
		}
		if item.Kind != MediaImage {
			return nil, Validationf("the %s has to be a picture; %s is not one", img.name, mediaAuditLabel(item))
		}
		if item.SizeBytes > MaxStoreImageBytes {
			return nil, Validationf("the %s is %s; every page loads it, so the limit is %s",
				img.name, humanBytes(item.SizeBytes), humanBytes(MaxStoreImageBytes))
		}
	}

	var updatedBy *int64
	if by != nil {
		updatedBy = &by.ID
	}
	err := InTx(ctx, p.app.db, func(tx *sql.Tx) error {
		if err := p.setImages(ctx, tx, images[0].id, images[1].id, images[2].id); err != nil {
			return err
		}
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
		    timezone = $13, updated_at = now(), updated_by = $14,
		    show_address = coalesce($15, show_address),
		    map_location = coalesce($16, map_location),
		    opening_hours = coalesce($17::jsonb, opening_hours),
		    prices_include_tax = coalesce($18, prices_include_tax)
		WHERE id = 1`,
			in.Name, in.LegalName, in.Email, in.Phone,
			in.AddressLine1, in.AddressLine2, in.City, in.State,
			in.PostalCode, in.Country, in.TaxID, in.SupportURL,
			in.Timezone, updatedBy,
			in.ShowAddress, in.MapLocation, hours, in.PricesIncludeTax); err != nil {
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

// setImages writes the pictures a save names and audits the change. Nil
// leaves one as it is and 0 removes it.
func (p *StoreProfiles) setImages(ctx context.Context, tx *sql.Tx, logo, dark, icon *int64) error {
	var was [3]*int64
	if err := tx.QueryRowContext(ctx, `
		SELECT logo_media_id, logo_dark_media_id, icon_media_id
		FROM store_profile WHERE id = 1 FOR UPDATE`).Scan(&was[0], &was[1], &was[2]); err != nil {
		return err
	}
	now := was
	for i, in := range []*int64{logo, dark, icon} {
		switch {
		case in == nil:
		case *in == 0:
			now[i] = nil
		default:
			now[i] = in
		}
	}
	same := func(a, b *int64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }
	if same(was[0], now[0]) && same(was[1], now[1]) && same(was[2], now[2]) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE store_profile SET logo_media_id = $1, logo_dark_media_id = $2, icon_media_id = $3
		WHERE id = 1`, now[0], now[1], now[2]); err != nil {
		return err
	}
	fields := func(v [3]*int64) map[string]any {
		return map[string]any{"logo_media_id": v[0], "logo_dark_media_id": v[1], "icon_media_id": v[2]}
	}
	return writeAudit(ctx, tx, auditRecord{
		Action: AuditStoreSetImages, Entity: AuditEntityStore, Key: p.app.storeOf(ctx),
		Summary: "Changed the store's logo or icon",
		Before:  fields(was), After: fields(now),
	})
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
	// A closed store's password (D170): a right one, or a pass still good,
	// answers with the pass the storefront keeps.
	a.HandleFunc("POST /api/store/password", a.handleStorePassword)
}

// publicStore is what anyone may read about the shop: what it is called and
// how a buyer reaches it, which is what the foot of every email it sends
// already says. The legal name and tax number stay behind the admin route, and
// so does the address until the store says to show it (D156): a page that
// names a shop has no need to publish where it is, and a shop with a door to
// walk through says so.
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
	// The store's logo, its logo for dark backgrounds and its square icon
	// (D148), as the media library serves them; empty when it has none, so a
	// storefront keeps its own.
	LogoURL     string `json:"logo_url"`
	LogoDarkURL string `json:"logo_dark_url"`
	IconURL     string `json:"icon_url"`
	// For a contact page (D156): the address only while the store shows it,
	// else null; the map location and the opening hours whenever they are set.
	// The country is public on its own, so a storefront can write the phone
	// number with its country code rather than guess one; it is not a door.
	Country      string         `json:"country"`
	Address      *publicAddress `json:"address"`
	MapLocation  string         `json:"map_location"`
	OpeningHours []OpeningHours `json:"opening_hours"`
	// PricesIncludeTax is the store's answer (D177), so a storefront says
	// "inclusive of all taxes" only where it is true. Every price the
	// storefront shows already tells a shopper which, so it is no secret.
	PricesIncludeTax bool `json:"prices_include_tax"`
}

// publicAddress is the shape a vendor's address has on the wire.
type publicAddress struct {
	Line1      string `json:"line1"`
	Line2      string `json:"line2"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

func (a *App) handlePublicStore(w http.ResponseWriter, r *http.Request) {
	p, err := a.profile.Get(r.Context())
	if err != nil {
		RespondError(w, r, err)
		return
	}
	out := publicStore{Name: p.Name, Email: p.Email, Phone: p.Phone, SupportURL: p.SupportURL,
		Language: p.Language, Languages: p.Languages, ImagesPath: ImagesPath(a.storeOf(r.Context())),
		LogoURL: p.LogoURL, LogoDarkURL: p.LogoDarkURL, IconURL: p.IconURL,
		Country: p.Country, MapLocation: *p.MapLocation, OpeningHours: p.OpeningHours,
		PricesIncludeTax: *p.PricesIncludeTax}
	// A country on its own is no address to show.
	if *p.ShowAddress && p.AddressLine1+p.AddressLine2+p.City+p.State+p.PostalCode != "" {
		out.Address = &publicAddress{Line1: p.AddressLine1, Line2: p.AddressLine2, City: p.City,
			State: p.State, PostalCode: p.PostalCode, Country: p.Country}
	}
	Respond(w, http.StatusOK, out)
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

// pricesIncludeTax is whether the catalog prices of the store ctx is bound to
// already contain tax (D177): the store's own answer, else the install's
// Config.PricesIncludeTax. q is the caller's transaction, so checkout reads it
// under the lock it prices under.
func (a *App) pricesIncludeTax(ctx context.Context, q rowQuerier) (bool, error) {
	var own sql.NullBool
	err := q.QueryRowContext(ctx, `SELECT prices_include_tax FROM store_profile WHERE id = 1`).Scan(&own)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !own.Valid) {
		return a.cfg.PricesIncludeTax, nil
	}
	if err != nil {
		return false, err
	}
	return own.Bool, nil
}
