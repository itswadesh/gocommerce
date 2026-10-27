package gocommerce

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// PIN codes (D134). A shopper in India types six digits and the checkout
// fills the city and the state, as the stores they buy from elsewhere do.
//
// The answer comes from India Post's own directory, the file data.gov.in
// publishes under the Government Open Data Licence, read from disk at start
// (Config.PostcodesFile). Not a lookup service: a checkout that waits on a
// stranger's API for every keystroke is slower and leaks every address typed
// into it, and the directory changes a few times a year. Not compiled in:
// the file is the government's to publish and the install's to fetch, and
// an install that sells nowhere near India carries none of it.
//
// Every number a guess would be is left out. The directory names a district,
// not a town, so the city is the district, which is what the post office
// prints and what a courier sorts by; the shopper can still change it.

// Postcode is what a postcode tells a checkout.
type Postcode struct {
	Country  string `json:"country"`
	Postcode string `json:"postcode"`
	City     string `json:"city"`
	State    string `json:"state"`
	// StateCode is the state as tax rates and shipping zones store it (D72),
	// empty for one the engine has no code for.
	StateCode string `json:"state_code"`
}

// bom is the byte-order mark a spreadsheet saving as UTF-8 puts before the
// first header.
var bom = string(rune(0xFEFF))

// Postcodes is a loaded directory, keyed by PIN code. India only.
type Postcodes struct {
	in map[string]Postcode
}

// LoadPostcodes reads the directory CSV. The columns are found by their
// header names (pincode, district, statename), so a reordered or widened
// export still reads. One PIN covers several post offices, which nearly
// always agree on the district; where they do not, the one most of them
// name wins, and the first named breaks a tie.
func LoadPostcodes(path string) (*Postcodes, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	r.ReuseRecord = true
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read the header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, bom)))] = i
	}
	pinCol, okPin := col["pincode"]
	districtCol, okDistrict := col["district"]
	stateCol, okState := col["statename"]
	if !okState {
		stateCol, okState = col["state"]
	}
	if !okPin || !okDistrict || !okState {
		return nil, errors.New("the header needs pincode, district and statename columns")
	}
	need := max(pinCol, districtCol, stateCol)

	type place struct{ district, state string }
	votes := map[string]map[place]int{}
	order := map[string][]place{}
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			var parse *csv.ParseError
			if errors.As(err, &parse) {
				continue
			}
			return nil, err
		}
		if len(rec) <= need {
			continue
		}
		pin := strings.TrimSpace(rec[pinCol])
		pl := place{strings.TrimSpace(rec[districtCol]), strings.TrimSpace(rec[stateCol])}
		if !validPIN(pin) || pl.district == "" || pl.state == "" {
			continue
		}
		if votes[pin] == nil {
			votes[pin] = map[place]int{}
		}
		if votes[pin][pl] == 0 {
			order[pin] = append(order[pin], pl)
		}
		votes[pin][pl]++
	}
	if len(votes) == 0 {
		return nil, errors.New("no PIN codes in the file")
	}

	out := &Postcodes{in: make(map[string]Postcode, len(votes))}
	for pin, places := range order {
		best := places[0]
		for _, pl := range places[1:] {
			if votes[pin][pl] > votes[pin][best] {
				best = pl
			}
		}
		out.in[pin] = indianPostcode(pin, best.district, best.state)
	}
	return out, nil
}

func indianPostcode(pin, district, state string) Postcode {
	// The directory spells a few union territories with a leading "THE".
	raw := strings.TrimSpace(state)
	if strings.HasPrefix(strings.ToUpper(raw), "THE ") {
		raw = strings.TrimSpace(raw[4:])
	}
	p := Postcode{Country: "IN", Postcode: pin, City: titleWords(district), State: titleWords(raw)}
	code := StateCode("IN", raw)
	if names := subdivisions["IN"][code]; len(names) > 0 {
		p.StateCode = code
		p.State = names[0]
	}
	return p
}

// titleWords turns the directory's capitals into a name as an address prints
// it: "BENGALURU URBAN" is "Bengaluru Urban", "JAMMU AND KASHMIR" is "Jammu and
// Kashmir".
func titleWords(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		if i > 0 && (w == "and" || w == "of") {
			continue
		}
		words[i] = strings.ToUpper(w[:1]) + w[1:]
	}
	return strings.Join(words, " ")
}

// validPIN is six digits not starting with 0: India Post's first digit is a
// postal zone, one to nine.
func validPIN(s string) bool {
	if len(s) != 6 || s[0] < '1' || s[0] > '9' {
		return false
	}
	for i := 1; i < 6; i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Lookup answers a postcode, and false for one the directory does not have,
// a country it does not cover, or no directory at all.
func (p *Postcodes) Lookup(country, code string) (Postcode, bool) {
	if p == nil || !strings.EqualFold(strings.TrimSpace(country), "IN") {
		return Postcode{}, false
	}
	got, ok := p.in[strings.TrimSpace(code)]
	return got, ok
}

func (a *App) mountPostcodeRoutes() {
	a.HandleFunc("GET /api/postcodes/{country}/{code}", a.handleGetPostcode)
}

func (a *App) handleGetPostcode(w http.ResponseWriter, r *http.Request) {
	country, code := strings.ToUpper(r.PathValue("country")), r.PathValue("code")
	if country == "IN" && !validPIN(code) {
		RespondError(w, r, Validationf("a PIN code is six digits and does not start with 0"))
		return
	}
	got, ok := a.postcodes.Lookup(country, code)
	if !ok {
		RespondError(w, r, NotFoundf("no address is known for that postcode"))
		return
	}
	// The directory changes when the install restarts with a new file, which
	// is rare; a day saves a checkout asking twice for the same six digits.
	w.Header().Set("Cache-Control", "public, max-age=86400")
	Respond(w, http.StatusOK, got)
}
