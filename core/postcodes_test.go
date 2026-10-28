package gocommerce

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/itswadesh/gocommerce/internal/testdb"
)

// PIN codes (D134): the city and state from India Post's directory.

const postcodeFixture = `circlename,regionname,divisionname,officename,pincode,officetype,delivery,district,statename,latitude,longitude
Delhi Circle,Delhi Region,New Delhi Central Division,Connaught Place S.O,110001,S.O,Delivery,NEW DELHI,DELHI,28.63,77.21
Delhi Circle,Delhi Region,New Delhi Central Division,Parliament House S.O,110001,S.O,Non Delivery,NEW DELHI,DELHI,NA,NA
Maharashtra Circle,Mumbai Region,Mumbai GPO Division,Mumbai G.P.O.,400001,H.O,Delivery,MUMBAI,MAHARASHTRA,18.93,72.83
Karnataka Circle,Bangalore HQ Region,Bangalore GPO Division,Bangalore G.P.O.,560001,H.O,Delivery,BENGALURU URBAN,KARNATAKA,12.97,77.59
Karnataka Circle,Bangalore HQ Region,Bangalore GPO Division,Some B.O,560001,B.O,Delivery,BENGALURU URBAN,KARNATAKA,NA,NA
Karnataka Circle,Bangalore HQ Region,Bangalore GPO Division,Odd B.O,560001,B.O,Delivery,BENGALURU RURAL,KARNATAKA,NA,NA
Maharashtra Circle,Mumbai Region,Mumbai GPO Division,Blank B.O,400001,B.O,Delivery,NA,NA,NA,NA
Maharashtra Circle,Mumbai Region,Mumbai GPO Division,Blank B.O 2,400001,B.O,Delivery,NA,NA,NA,NA
Chattisgarh Circle,Raipur Region,Raipur Division,Unknown B.O,492099,B.O,Delivery,NA,NA,NA,NA
Gujarat Circle,Vadodara Region,Valsad Division,Daman S.O,396210,S.O,Delivery,DAMAN,THE DADRA AND NAGAR HAVELI AND DAMAN AND DIU,NA,NA
Broken,row,without,enough,columns
`

func TestPostcodeDirectoryFillsCityAndState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pincodes.csv")
	if err := os.WriteFile(path, []byte(postcodeFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := LoadPostcodes(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, c := range []struct{ pin, city, state, code string }{
		{"110001", "New Delhi", "Delhi", "DL"},
		{"400001", "Mumbai", "Maharashtra", "MH"},
		// Most of the post offices under one PIN agree on the district.
		{"560001", "Bengaluru Urban", "Karnataka", "KA"},
		{"396210", "Daman", "Dadra and Nagar Haveli and Daman and Diu", "DH"},
	} {
		got, ok := directory.Lookup("IN", c.pin)
		if !ok || got.City != c.city || got.State != c.state || got.StateCode != c.code {
			t.Errorf("Lookup(%s) = %+v %v, want %s, %s (%s)", c.pin, got, ok, c.city, c.state, c.code)
		}
	}
	// India Post writes "NA" where it has no district or state; such a row
	// says nothing, and a PIN with only those rows is not known.
	if got, ok := directory.Lookup("IN", "492099"); ok {
		t.Errorf("a PIN with only NA rows = %+v, want unknown", got)
	}
	if _, ok := directory.Lookup("IN", "999999"); ok {
		t.Error("an unknown PIN was found")
	}
	if _, ok := directory.Lookup("US", "110001"); ok {
		t.Error("a PIN was found for another country")
	}

	dsn := testdb.Clone(t, "core:"+moduleKey(nil), func(dsn string) error {
		app, err := New(testConfig(dsn))
		if err != nil {
			return err
		}
		return app.Close()
	})
	cfg := testConfig(dsn)
	cfg.PostcodesFile = path
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	rec := do(t, app, http.MethodGet, "/api/postcodes/IN/400001")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET postcode: %d %s", rec.Code, rec.Body)
	}
	var env struct {
		Data Postcode `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	if env.Data.City != "Mumbai" || env.Data.StateCode != "MH" || env.Data.Country != "IN" || env.Data.Postcode != "400001" {
		t.Errorf("postcode = %+v", env.Data)
	}
	for target, want := range map[string]int{
		"/api/postcodes/IN/999999": http.StatusNotFound,
		"/api/postcodes/IN/4000":   http.StatusBadRequest,
		"/api/postcodes/IN/040001": http.StatusBadRequest,
		"/api/postcodes/US/10001":  http.StatusNotFound,
	} {
		if rec := do(t, app, http.MethodGet, target); rec.Code != want {
			t.Errorf("GET %s = %d, want %d", target, rec.Code, want)
		}
	}
}

// With no file named, the engine answers from the directory built into it,
// so every store in India fills the city without anyone fetching anything;
// "off" turns the lookup off.
func TestPostcodesComeFromTheBuiltInDirectoryUnlessTurnedOff(t *testing.T) {
	app := newTestApp(t)
	rec := do(t, app, http.MethodGet, "/api/postcodes/IN/400001")
	if rec.Code != http.StatusOK {
		t.Fatalf("built in: %d %s, want 200", rec.Code, rec.Body)
	}

	dsn := testdb.Clone(t, "core:"+moduleKey(nil), func(dsn string) error {
		app, err := New(testConfig(dsn))
		if err != nil {
			return err
		}
		return app.Close()
	})
	cfg := testConfig(dsn)
	cfg.PostcodesFile = "off"
	off, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = off.Close() })
	if rec := do(t, off, http.MethodGet, "/api/postcodes/IN/400001"); rec.Code != http.StatusNotFound {
		t.Errorf("turned off: %d, want 404", rec.Code)
	}
}

// The built-in directory is India Post's whole file, reduced to one row a
// PIN, and every state in it is one the engine has a code for: a state the
// tax rates and shipping zones cannot match would fill a field that then
// matches nothing.
func TestTheBuiltInDirectoryCoversIndia(t *testing.T) {
	d := BuiltinPostcodes()
	if n := d.Len(); n < 19000 {
		t.Fatalf("built-in directory has %d PIN codes, want India's ~19,000", n)
	}
	for _, c := range []struct{ pin, state, code string }{
		{"110001", "Delhi", "DL"},
		{"400001", "Maharashtra", "MH"},
		{"560001", "Karnataka", "KA"},
		{"403001", "Goa", "GA"},
		{"605001", "Puducherry", "PY"},
		{"744101", "Andaman and Nicobar Islands", "AN"},
		{"190001", "Jammu and Kashmir", "JK"},
		{"194101", "Ladakh", "LA"},
	} {
		got, ok := d.Lookup("IN", c.pin)
		if !ok || got.State != c.state || got.StateCode != c.code || got.City == "" {
			t.Errorf("Lookup(%s) = %+v %v, want %s (%s)", c.pin, got, ok, c.state, c.code)
		}
	}
	unknown := map[string]int{}
	d.each(func(p Postcode) {
		if p.StateCode == "" {
			unknown[p.State]++
		}
	})
	for state, n := range unknown {
		t.Errorf("%d PIN codes name %q, which has no state code", n, state)
	}
}

// The compact form the engine carries reads back exactly as the government's
// file did.
func TestTheCompactDirectoryReadsBackAsTheFullOne(t *testing.T) {
	dir := t.TempDir()
	full := filepath.Join(dir, "pincodes.csv")
	if err := os.WriteFile(full, []byte(postcodeFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	want, err := LoadPostcodes(full)
	if err != nil {
		t.Fatal(err)
	}
	compact := filepath.Join(dir, "pincodes.csv.gz")
	f, err := os.Create(compact)
	if err != nil {
		t.Fatal(err)
	}
	if err := want.WriteCompact(f); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := LoadPostcodes(compact)
	if err != nil {
		t.Fatalf("load compact: %v", err)
	}
	if got.Len() != want.Len() {
		t.Fatalf("compact has %d PIN codes, full has %d", got.Len(), want.Len())
	}
	want.each(func(p Postcode) {
		if q, ok := got.Lookup("IN", p.Postcode); !ok || q != p {
			t.Errorf("%s: compact %+v, full %+v", p.Postcode, q, p)
		}
	})
}

// A district's name is printed on the address, so its capitals are the ones a
// person would write, including after a hyphen, a bracket or an initial.
func TestDistrictNamesAreWrittenAsAnAddressPrintsThem(t *testing.T) {
	for in, want := range map[string]string{
		"BENGALURU URBAN":   "Bengaluru Urban",
		"JAMMU AND KASHMIR": "Jammu and Kashmir",
		"JANJGIR-CHAMPA":    "Janjgir-Champa",
		"KAIMUR (BHABUA)":   "Kaimur (Bhabua)",
		"S.A.S NAGAR":       "S.A.S Nagar",
		"Y.S.R.":            "Y.S.R.",
	} {
		if got := titleWords(in); got != want {
			t.Errorf("titleWords(%q) = %q, want %q", in, got, want)
		}
	}
}
