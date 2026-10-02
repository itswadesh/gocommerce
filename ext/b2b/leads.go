package b2b

import (
	"context"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Lead states: what a dealer reports back, not a sales pipeline. The pipeline
// is the dealer's own business.
const (
	LeadNew       = "new"
	LeadContacted = "contacted"
	LeadWon       = "won"
	LeadLost      = "lost"
)

// How a lead reached whoever holds it.
const (
	RoutedTerritory = "territory"
	RoutedStore     = "store"
	RoutedNone      = "unrouted"
)

// What the public form accepts, field by field. The route needs no session
// and what it stores is emailed to a dealer, so nothing in it is unbounded.
const (
	maxLeadName    = 200
	maxLeadEmail   = 254
	maxLeadPhone   = 40
	maxLeadMessage = 4000
	maxLeadState   = 100
	maxLeadPostal  = 20
	maxLeadSource  = 100
	maxPostalRoot  = 16
)

// takesLeads is which dealers an enquiry may go to. An on-hold dealer still
// sells — on hold stops orders on account and nothing else — so it still
// serves the customers in its territory. A closed one has stopped trading
// with the store, and a customer routed to it would wait for a call that is
// never made; its territory falls through to the next dealer that covers it,
// or to the store.
const takesLeads = `c.status <> 'closed'`

func validLeadStatus(s string) bool {
	switch s {
	case LeadNew, LeadContacted, LeadWon, LeadLost:
		return true
	}
	return false
}

// ------------------------------------------------------------- territories

// TerritoryInput is an area to give a dealer. An empty state or postal_prefix
// covers all of it.
type TerritoryInput struct {
	Country      string `json:"country"`
	State        string `json:"state"`
	PostalPrefix string `json:"postal_prefix"`
}

func normCountry(s string) string { return strings.ToUpper(strings.TrimSpace(s)) }

// normState upper-cases a state and closes up its spaces, so "New South
// Wales" and "new  south wales" are one territory and one match.
func normState(s string) string { return strings.ToUpper(strings.Join(strings.Fields(s), " ")) }

// normPostal keeps a postcode's letters and digits, upper-cased: "sw1a 1aa" is
// SW1A1AA, and 94105-1234 starts with 941 whichever way it was typed.
func normPostal(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validCountry(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}

const territoryColumns = `t.id, t.company_id, c.name, t.country, t.state, t.postal_prefix, t.created_at`

const territoryFrom = ` FROM b2b_territories t JOIN b2b_companies c ON c.id = t.company_id`

func scanTerritory(row rowScanner) (*Territory, error) {
	t := &Territory{}
	err := row.Scan(&t.ID, &t.CompanyID, &t.CompanyName, &t.Country, &t.State, &t.PostalPrefix, &t.CreatedAt)
	return t, err
}

// Territories lists territories, by country, state and prefix. A zero
// companyID is every dealer's; country narrows to one.
func (m *Module) Territories(ctx context.Context, companyID int64, country string, limit, offset int) ([]*Territory, int, error) {
	where, args := []string{"true"}, []any{}
	if companyID > 0 {
		args = append(args, companyID)
		where = append(where, "t.company_id = $"+strconv.Itoa(len(args)))
	}
	if c := normCountry(country); c != "" {
		if !validCountry(c) {
			return nil, 0, gocommerce.Validationf("country is a two-letter code, like \"US\"")
		}
		args = append(args, c)
		where = append(where, "t.country = $"+strconv.Itoa(len(args)))
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*)`+territoryFrom+` WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := m.db.QueryContext(ctx, `SELECT `+territoryColumns+territoryFrom+` WHERE `+clause+`
		ORDER BY t.country, t.state, t.postal_prefix
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Territory{}
	for rows.Next() {
		t, err := scanTerritory(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// AddTerritory gives a dealer an area. An area another dealer already has is
// refused, naming them, rather than shared: a territory has one dealer.
func (m *Module) AddTerritory(ctx context.Context, companyID int64, in TerritoryInput) (*Territory, error) {
	if _, err := m.Company(ctx, companyID); err != nil {
		return nil, err
	}
	country, state, prefix := normCountry(in.Country), normState(in.State), normPostal(in.PostalPrefix)
	switch {
	case !validCountry(country):
		return nil, gocommerce.Validationf("country is a two-letter ISO 3166-1 code, like \"US\"")
	case utf8.RuneCountInString(state) > maxLeadState || hasControl(state, false):
		return nil, gocommerce.Validationf("state is at most %d characters on one line", maxLeadState)
	case strings.IndexFunc(in.PostalPrefix, func(r rune) bool {
		return r != ' ' && r != '-' && !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'z') && !(r >= 'A' && r <= 'Z')
	}) >= 0:
		return nil, gocommerce.Validationf("postal_prefix is letters and digits; spaces and hyphens are ignored")
	case len(prefix) > maxPostalRoot:
		return nil, gocommerce.Validationf("postal_prefix is at most %d letters and digits", maxPostalRoot)
	}
	var id int64
	err := m.db.QueryRowContext(ctx, `
		INSERT INTO b2b_territories (company_id, country, state, postal_prefix)
		VALUES ($1, $2, $3, $4) RETURNING id`, companyID, country, state, prefix).Scan(&id)
	if isUnique(err) {
		holder := "another dealer"
		_ = m.db.QueryRowContext(ctx, `SELECT c.name`+territoryFrom+`
			WHERE t.country = $1 AND t.state = $2 AND t.postal_prefix = $3`, country, state, prefix).Scan(&holder)
		return nil, gocommerce.Conflictf("%s already covers that territory", holder)
	}
	if err != nil {
		return nil, err
	}
	return scanTerritory(m.db.QueryRowContext(ctx,
		`SELECT `+territoryColumns+territoryFrom+` WHERE t.id = $1`, id))
}

// DeleteTerritory takes an area back from a dealer.
func (m *Module) DeleteTerritory(ctx context.Context, companyID, id int64) error {
	res, err := m.db.ExecContext(ctx,
		`DELETE FROM b2b_territories WHERE id = $1 AND company_id = $2`, id, companyID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return gocommerce.NotFoundf("company %d has no territory %d", companyID, id)
	}
	return nil
}

// route is the dealer whose territory covers an address, among dealers that
// take leads; nil is nobody. The most specific match wins: a territory naming
// a state outranks one that does not, and within that the longest postcode
// prefix — so {US, CA, 941} beats {US, CA}, which beats {US, -, 9}, which
// beats {US}. A territory naming a state or a prefix matches only an enquiry
// that gives one: a form that does not ask for the state reaches the
// country's dealers and no state's.
//
// The unique key on the area means two matches can never rank equal: two
// territories can only both match by differing in specificity.
func (m *Module) route(ctx context.Context, country, state, postal string) (*Company, error) {
	if country == "" {
		return nil, nil
	}
	var id int64
	err := m.db.QueryRowContext(ctx, `
		SELECT t.company_id`+territoryFrom+`
		WHERE t.country = $1 AND `+takesLeads+`
		  AND (t.state = '' OR t.state = $2)
		  AND (t.postal_prefix = '' OR starts_with($3, t.postal_prefix))
		ORDER BY t.state <> '' DESC, length(t.postal_prefix) DESC
		LIMIT 1`, country, normState(state), normPostal(postal)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return m.Company(ctx, id)
}

// ------------------------------------------------------------------- leads

// LeadInput is an enquiry from the storefront's dealer form.
type LeadInput struct {
	Name       string `json:"name"`
	Email      string `json:"email"`
	Phone      string `json:"phone"`
	Message    string `json:"message"`
	Country    string `json:"country"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
	VariantID  *int64 `json:"variant_id"`
	ProductID  *int64 `json:"product_id"`
	// Source is where on the storefront it was sent from, in the storefront's
	// own words: "product-page", "find-a-dealer".
	Source string `json:"source"`
}

// LeadQuery narrows a list of leads.
type LeadQuery struct {
	// CompanyID is one dealer's; zero is every dealer's and the store's.
	CompanyID int64
	// Unrouted keeps only the store's own.
	Unrouted      bool
	Status        string
	Limit, Offset int
}

// LeadUpdate is the store changing a lead: how it stands, who has it, or both.
// company_id null takes it back from the dealer.
type LeadUpdate struct {
	Status    *string       `json:"status"`
	CompanyID optionalInt64 `json:"company_id"`
}

// FileLead records an enquiry and hands it to the dealer whose territory
// covers it — whose admins and approvers are emailed — or, when none does,
// keeps it for the store.
func (m *Module) FileLead(ctx context.Context, in LeadInput) (*Lead, error) {
	in, err := m.cleanLead(ctx, in)
	if err != nil {
		return nil, err
	}
	dealer, err := m.route(ctx, in.Country, in.State, in.PostalCode)
	if err != nil {
		return nil, err
	}
	routedBy, company := RoutedNone, (*int64)(nil)
	if dealer != nil {
		routedBy, company = RoutedTerritory, &dealer.ID
	}
	var id int64
	if err := m.db.QueryRowContext(ctx, `
		INSERT INTO b2b_leads (name, email, phone, message, country, state, postal_code,
		                       variant_id, product_id, company_id, routed_by, source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING id`,
		in.Name, in.Email, in.Phone, in.Message, in.Country, in.State, in.PostalCode,
		in.VariantID, in.ProductID, company, routedBy, in.Source).Scan(&id); err != nil {
		return nil, err
	}
	lead, err := m.Lead(ctx, id)
	if err != nil {
		return nil, err
	}
	if dealer != nil {
		m.notifyLead(ctx, dealer, lead)
	}
	return lead, nil
}

// cleanLead trims, bounds and checks everything a member of the public sent.
// The single-line fields refuse control characters outright: they are mailed
// to a dealer, and a line break in a name is how a header gets forged.
func (m *Module) cleanLead(ctx context.Context, in LeadInput) (LeadInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Phone = strings.TrimSpace(in.Phone)
	in.Message = strings.TrimSpace(in.Message)
	in.Country = normCountry(in.Country)
	in.State = strings.TrimSpace(in.State)
	in.PostalCode = strings.TrimSpace(in.PostalCode)
	in.Source = strings.TrimSpace(in.Source)
	for _, f := range []struct {
		name, value string
		max         int
		multiline   bool
	}{
		{"name", in.Name, maxLeadName, false},
		{"email", in.Email, maxLeadEmail, false},
		{"phone", in.Phone, maxLeadPhone, false},
		{"message", in.Message, maxLeadMessage, true},
		{"state", in.State, maxLeadState, false},
		{"postal_code", in.PostalCode, maxLeadPostal, false},
		{"source", in.Source, maxLeadSource, false},
	} {
		if n := utf8.RuneCountInString(f.value); n > f.max {
			return in, gocommerce.Validationf("%s is %d characters; at most %d", f.name, n, f.max)
		}
		if hasControl(f.value, f.multiline) {
			return in, gocommerce.Validationf("%s contains control characters", f.name)
		}
	}
	switch {
	case in.Email == "" && in.Phone == "":
		return in, gocommerce.Validationf("an email or a phone number is required, so the dealer can answer")
	case in.Email != "" && (!strings.Contains(in.Email, "@") || strings.ContainsAny(in.Email, " \t")):
		return in, gocommerce.Validationf("email is not an address")
	case in.Phone != "" && !validPhone(in.Phone):
		return in, gocommerce.Validationf("phone is digits, spaces and + - ( ) . only, with at least five digits")
	case in.Country != "" && !validCountry(in.Country):
		return in, gocommerce.Validationf("country is a two-letter ISO 3166-1 code, like \"US\"")
	}
	if in.VariantID != nil {
		v, err := m.app.Products().GetVariant(ctx, *in.VariantID)
		if errors.Is(err, gocommerce.ErrNotFound) {
			return in, gocommerce.Validationf("variant %d does not exist", *in.VariantID)
		}
		if err != nil {
			return in, err
		}
		if in.ProductID != nil && *in.ProductID != v.ProductID {
			return in, gocommerce.Validationf("variant %d is not one of product %d's", *in.VariantID, *in.ProductID)
		}
		product := v.ProductID
		in.ProductID = &product
	} else if in.ProductID != nil {
		_, err := m.app.Products().GetProduct(ctx, *in.ProductID)
		if errors.Is(err, gocommerce.ErrNotFound) {
			return in, gocommerce.Validationf("product %d does not exist", *in.ProductID)
		}
		if err != nil {
			return in, err
		}
	}
	return in, nil
}

func hasControl(s string, multiline bool) bool {
	for _, r := range s {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			continue
		}
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

func validPhone(s string) bool {
	digits := 0
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digits++
		case strings.ContainsRune("+-(). ", r):
		default:
			return false
		}
	}
	return digits >= 5
}

const leadColumns = `l.id, l.name, l.email, l.phone, l.message, l.country, l.state, l.postal_code,
	l.variant_id, l.product_id, l.status, l.company_id, coalesce(c.name, ''), l.routed_by, l.source,
	l.created_at, l.updated_at`

const leadFrom = ` FROM b2b_leads l LEFT JOIN b2b_companies c ON c.id = l.company_id`

func scanLead(row rowScanner) (*Lead, error) {
	l := &Lead{}
	var variant, product, company sql.NullInt64
	if err := row.Scan(&l.ID, &l.Name, &l.Email, &l.Phone, &l.Message, &l.Country, &l.State,
		&l.PostalCode, &variant, &product, &l.Status, &company, &l.CompanyName, &l.RoutedBy,
		&l.Source, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	for dst, src := range map[**int64]sql.NullInt64{&l.VariantID: variant, &l.ProductID: product, &l.CompanyID: company} {
		if src.Valid {
			v := src.Int64
			*dst = &v
		}
	}
	return l, nil
}

// Lead reads one lead.
func (m *Module) Lead(ctx context.Context, id int64) (*Lead, error) {
	l, err := scanLead(m.db.QueryRowContext(ctx, `SELECT `+leadColumns+leadFrom+` WHERE l.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("lead %d does not exist", id)
	}
	return l, err
}

// Leads lists leads, newest first. An unknown status is refused rather than
// answered with an empty page: a filter nobody can see is wrong is worse
// than one that says so.
func (m *Module) Leads(ctx context.Context, q LeadQuery) ([]*Lead, int, error) {
	where, args := []string{"true"}, []any{}
	if q.CompanyID > 0 {
		args = append(args, q.CompanyID)
		where = append(where, "l.company_id = $"+strconv.Itoa(len(args)))
	}
	if q.Unrouted {
		where = append(where, "l.company_id IS NULL")
	}
	if q.Status != "" {
		if !validLeadStatus(q.Status) {
			return nil, 0, gocommerce.Validationf("status must be new, contacted, won or lost")
		}
		args = append(args, q.Status)
		where = append(where, "l.status = $"+strconv.Itoa(len(args)))
	}
	clause := strings.Join(where, " AND ")
	var total int
	if err := m.db.QueryRowContext(ctx, `SELECT count(*)`+leadFrom+` WHERE `+clause, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, q.Limit, q.Offset)
	rows, err := m.db.QueryContext(ctx, `SELECT `+leadColumns+leadFrom+` WHERE `+clause+`
		ORDER BY l.id DESC
		LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*Lead{}
	for rows.Next() {
		l, err := scanLead(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}

// SetLeadStatus is a dealer reporting how one of its leads stands. Another
// dealer's lead is a 404, not a 403: dealers do not learn about each other's
// customers, even that they exist.
func (m *Module) SetLeadStatus(ctx context.Context, companyID, id int64, status string) (*Lead, error) {
	if !validLeadStatus(status) {
		return nil, gocommerce.Validationf("status must be new, contacted, won or lost")
	}
	res, err := m.db.ExecContext(ctx, `
		UPDATE b2b_leads SET status = $3, updated_at = now() WHERE id = $1 AND company_id = $2`,
		id, companyID, status)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, gocommerce.NotFoundf("lead %d does not exist", id)
	}
	return m.Lead(ctx, id)
}

// UpdateLead is the store's change to a lead. Handing it to a dealer marks it
// routed by the store and emails that dealer, as a routed lead is; null takes
// it back. A lead that changes hands starts again at new unless the same
// change says otherwise: how far the last dealer got is not how far this one
// has.
func (m *Module) UpdateLead(ctx context.Context, id int64, in LeadUpdate) (*Lead, error) {
	before, err := m.Lead(ctx, id)
	if err != nil {
		return nil, err
	}
	if in.Status == nil && !in.CompanyID.Set {
		return nil, gocommerce.Validationf("nothing to change: send status, company_id or both")
	}
	if in.Status != nil && !validLeadStatus(*in.Status) {
		return nil, gocommerce.Validationf("status must be new, contacted, won or lost")
	}
	sets, args := []string{"updated_at = now()"}, []any{id}
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, col+" = $"+strconv.Itoa(len(args)))
	}
	var dealer *Company
	moved := in.CompanyID.Set && !sameID(before.CompanyID, in.CompanyID.Value)
	if moved {
		routedBy := RoutedNone
		if in.CompanyID.Value != nil {
			dealer, err = m.Company(ctx, *in.CompanyID.Value)
			if errors.Is(err, gocommerce.ErrNotFound) {
				return nil, gocommerce.Validationf("company %d does not exist", *in.CompanyID.Value)
			}
			if err != nil {
				return nil, err
			}
			if dealer.Status == StatusClosed {
				return nil, gocommerce.Conflictf("%s is closed and takes no leads", dealer.Name)
			}
			routedBy = RoutedStore
		}
		set("company_id", in.CompanyID.Value)
		set("routed_by", routedBy)
		if in.Status == nil {
			set("status", LeadNew)
		}
	}
	if in.Status != nil {
		set("status", *in.Status)
	}
	if _, err := m.db.ExecContext(ctx,
		`UPDATE b2b_leads SET `+strings.Join(sets, ", ")+` WHERE id = $1`, args...); err != nil {
		return nil, err
	}
	lead, err := m.Lead(ctx, id)
	if err != nil {
		return nil, err
	}
	if dealer != nil {
		m.notifyLead(ctx, dealer, lead)
	}
	return lead, nil
}

// ---------------------------------------------------------------- throttle

// leadThrottle counts public enquiries per peer address in fixed windows. In
// process, so per replica — the trade identity's login throttle makes: it
// stops one host filling the dealers' inboxes without a shared store, and it
// is not a substitute for a captcha on a form that draws real abuse.
type leadThrottle struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	seen   map[string]*leadWindow
	swept  time.Time
}

type leadWindow struct {
	start time.Time
	count int
}

// newLeadThrottle allows limit attempts per window; a negative limit allows
// everything.
func newLeadThrottle(limit int, window time.Duration) *leadThrottle {
	return &leadThrottle{limit: limit, window: window, seen: map[string]*leadWindow{}}
}

// allow counts one attempt from addr and says whether it is within the limit
// — and if not, how long until it would be. Every attempt counts, valid or
// not: what is being protected is somebody's inbox and the database, and a
// malformed request still costs a validation.
func (t *leadThrottle) allow(addr string, now time.Time) (time.Duration, bool) {
	if t.limit < 0 {
		return 0, true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	// Finished windows are forgotten once a window, so the map holds only the
	// addresses heard from lately however many there have been.
	if now.Sub(t.swept) >= t.window {
		for k, w := range t.seen {
			if now.Sub(w.start) >= t.window {
				delete(t.seen, k)
			}
		}
		t.swept = now
	}
	w := t.seen[addr]
	if w == nil || now.Sub(w.start) >= t.window {
		t.seen[addr] = &leadWindow{start: now, count: 1}
		return 0, true
	}
	if w.count >= t.limit {
		return w.start.Add(t.window).Sub(now), false
	}
	w.count++
	return 0, true
}

// peerAddr is the connection's address without its port. Never
// X-Forwarded-For: behind no proxy that header is whatever the client wrote,
// and trusting it would let anyone reset their own count at will.
func peerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
