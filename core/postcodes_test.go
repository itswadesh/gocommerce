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

func TestPostcodesWithoutADirectoryAnswerNothing(t *testing.T) {
	app := newTestApp(t)
	if rec := do(t, app, http.MethodGet, "/api/postcodes/IN/400001"); rec.Code != http.StatusNotFound {
		t.Errorf("without a directory: %d, want 404", rec.Code)
	}
}
