package gocommerce

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
)

// PIN codes (D134). A shopper in India types six digits and the checkout
// fills the city and the state, as the stores they buy from elsewhere do.
//
// The answer comes from India Post's own directory, the file data.gov.in
// publishes under the Government Open Data Licence. Not a lookup service: a
// checkout that waits on a stranger's API for every keystroke is slower and
// leaks every address typed into it, and the directory changes a few times a
// year. It is built in (D136), reduced to one row a PIN and compressed, so
// every store in India fills the city with nothing to set up; an install can
// point Config.PostcodesFile at a newer copy of the government's file, or at
// "off". `go run ./scripts/pincodes` rebuilds the built-in one.
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
// A path ending in .gz is read through gzip, which is how the compact form
// is kept.
func LoadPostcodes(path string) (*Postcodes, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var in io.Reader = f
	if strings.HasSuffix(strings.ToLower(path), ".gz") {
		z, err := gzip.NewReader(f)
		if err != nil {
			return nil, err
		}
		defer z.Close()
		in = z
	}
	return readPostcodes(in)
}

func readPostcodes(in io.Reader) (*Postcodes, error) {
	r := csv.NewReader(in)
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
		if !validPIN(pin) || unstated(pl.district) || unstated(pl.state) {
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
// Kashmir". A letter after a hyphen, a bracket or a full stop starts a word
// too: "Janjgir-Champa", "Kaimur (Bhabua)", "S.A.S Nagar".
func titleWords(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		if i > 0 && (w == "and" || w == "of") {
			continue
		}
		b := []byte(w)
		start := true
		for j, c := range b {
			if start && c >= 'a' && c <= 'z' {
				b[j] = c - 'a' + 'A'
			}
			start = c == '-' || c == '(' || c == '.'
		}
		words[i] = string(b)
	}
	return strings.Join(words, " ")
}

// unstated is a field India Post left blank, which its file writes as "NA".
// A few hundred post offices have no district or state, and counting their
// rows would let a blank outvote the offices that do say.
func unstated(s string) bool {
	return s == "" || strings.EqualFold(s, "NA") || strings.EqualFold(s, "N.A.")
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

// Len is how many PIN codes the directory knows.
func (p *Postcodes) Len() int {
	if p == nil {
		return 0
	}
	return len(p.in)
}

// each visits every entry in PIN order.
func (p *Postcodes) each(fn func(Postcode)) {
	pins := make([]string, 0, len(p.in))
	for pin := range p.in {
		pins = append(pins, pin)
	}
	sort.Strings(pins)
	for _, pin := range pins {
		fn(p.in[pin])
	}
}

// WriteCompact writes the directory as the built-in one is kept: gzip, a
// header LoadPostcodes reads, then one row a PIN in order, so rebuilding it
// from the same government file gives the same bytes.
func (p *Postcodes) WriteCompact(w io.Writer) error {
	z, err := gzip.NewWriterLevel(w, gzip.BestCompression)
	if err != nil {
		return err
	}
	c := csv.NewWriter(z)
	if err := c.Write([]string{"pincode", "district", "statename"}); err != nil {
		return err
	}
	p.each(func(e Postcode) {
		if err == nil {
			err = c.Write([]string{e.Postcode, e.City, e.State})
		}
	})
	if err != nil {
		return err
	}
	c.Flush()
	if err := c.Error(); err != nil {
		return err
	}
	return z.Close()
}

//go:embed postcodes_in.csv.gz
var builtinPostcodes []byte

var (
	builtinOnce sync.Once
	builtin     *Postcodes
)

// BuiltinPostcodes is the directory compiled into the engine, read on first
// use rather than at start: some 19,000 rows is a few milliseconds, but most
// of what calls New never looks up a PIN.
func BuiltinPostcodes() *Postcodes {
	builtinOnce.Do(func() {
		z, err := gzip.NewReader(bytes.NewReader(builtinPostcodes))
		if err != nil {
			panic("gocommerce: the built-in PIN code directory is unreadable: " + err.Error())
		}
		if builtin, err = readPostcodes(z); err != nil {
			panic("gocommerce: the built-in PIN code directory is unreadable: " + err.Error())
		}
	})
	return builtin
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
	got, ok := a.postcodes().Lookup(country, code)
	if !ok {
		RespondError(w, r, NotFoundf("no address is known for that postcode"))
		return
	}
	// The directory changes with a release or a new file, which is rare; a day saves a checkout asking twice for the same six digits.
	w.Header().Set("Cache-Control", "public, max-age=86400")
	Respond(w, http.StatusOK, got)
}
