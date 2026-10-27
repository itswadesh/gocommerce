package gocommerce

import (
	"regexp"
	"strings"
)

// India's GST registration number and the state codes inside it.
//
// They live in core, not in ext/invoices, because the store profile is core's
// and has to refuse a mistyped GSTIN when it is saved, and core cannot import a
// module. Core gets the parser and the table; what GST does with them — which
// tax heads an invoice prints, how it splits — stays in ext/invoices.

// GSTIN is a 15-character GST registration number that has passed its format,
// its state code and its check character. ParseGSTIN is the only way to make
// one, so a value of this type is always valid.
type GSTIN string

// GSTState is one state or union territory as GST numbers it.
type GSTState struct {
	// Code is the two-digit GST code, the first two characters of a GSTIN.
	Code string
	// ISO is the code StateCode returns for the same place, empty for "Other
	// Territory", which has no ISO subdivision.
	ISO  string
	Name string
	// UT is a union territory without a legislature, where UTGST takes the
	// place of SGST. Delhi, Puducherry and Jammu and Kashmir have legislatures
	// and charge SGST.
	UT bool
}

// gstStates is the one list of GST state codes; every lookup is derived from it.
var gstStates = []GSTState{
	{"01", "JK", "Jammu and Kashmir", false},
	{"02", "HP", "Himachal Pradesh", false},
	{"03", "PB", "Punjab", false},
	{"04", "CH", "Chandigarh", true},
	{"05", "UK", "Uttarakhand", false},
	{"06", "HR", "Haryana", false},
	{"07", "DL", "Delhi", false},
	{"08", "RJ", "Rajasthan", false},
	{"09", "UP", "Uttar Pradesh", false},
	{"10", "BR", "Bihar", false},
	{"11", "SK", "Sikkim", false},
	{"12", "AR", "Arunachal Pradesh", false},
	{"13", "NL", "Nagaland", false},
	{"14", "MN", "Manipur", false},
	{"15", "MZ", "Mizoram", false},
	{"16", "TR", "Tripura", false},
	{"17", "ML", "Meghalaya", false},
	{"18", "AS", "Assam", false},
	{"19", "WB", "West Bengal", false},
	{"20", "JH", "Jharkhand", false},
	{"21", "OD", "Odisha", false},
	{"22", "CG", "Chhattisgarh", false},
	{"23", "MP", "Madhya Pradesh", false},
	{"24", "GJ", "Gujarat", false},
	{"26", "DH", "Dadra and Nagar Haveli and Daman and Diu", true},
	{"27", "MH", "Maharashtra", false},
	{"29", "KA", "Karnataka", false},
	{"30", "GA", "Goa", false},
	{"31", "LD", "Lakshadweep", true},
	{"32", "KL", "Kerala", false},
	{"33", "TN", "Tamil Nadu", false},
	{"34", "PY", "Puducherry", false},
	{"35", "AN", "Andaman and Nicobar Islands", true},
	{"36", "TS", "Telangana", false},
	{"37", "AP", "Andhra Pradesh", false},
	{"38", "LA", "Ladakh", true},
	// The CGST Act counts "other territory" (offshore areas) as a union
	// territory, so UTGST.
	{"97", "", "Other Territory", true},
}

// retiredGSTCodes are prefixes still printed on GSTINs issued before a state
// was merged or split: 25 was Daman and Diu before it joined 26, and 28 was
// Andhra Pradesh before Telangana was carved out and AP took 37.
var retiredGSTCodes = map[string]string{"25": "26", "28": "37"}

var gstByCode, gstByISO = indexGSTStates(gstStates)

func indexGSTStates(states []GSTState) (map[string]GSTState, map[string]GSTState) {
	byCode, byISO := map[string]GSTState{}, map[string]GSTState{}
	for _, s := range states {
		byCode[s.Code] = s
		if s.ISO != "" {
			byISO[s.ISO] = s
		}
	}
	for old, now := range retiredGSTCodes {
		byCode[old] = byCode[now]
	}
	return byCode, byISO
}

var gstinPattern = regexp.MustCompile(`^[0-9]{2}[A-Z]{5}[0-9]{4}[A-Z][1-9A-Z]Z[0-9A-Z]$`)

const gstinAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// ParseGSTIN trims and upper-cases s and checks it is a GSTIN: the pattern,
// a state code GST knows, and the check character. The error says which part
// is wrong, in words an operator or a buyer can act on.
func ParseGSTIN(s string) (GSTIN, error) {
	g := strings.ToUpper(strings.TrimSpace(s))
	if len(g) != 15 {
		return "", Validationf("%q is not a valid GSTIN: a GSTIN is 15 characters, like 27AAPFU0939F1ZV", s)
	}
	if !gstinPattern.MatchString(g) {
		return "", Validationf("%q is not a valid GSTIN: it should be 2 digits, a 10-character PAN, an entity number, Z and a check character, like 27AAPFU0939F1ZV", s)
	}
	if _, ok := gstByCode[g[:2]]; !ok {
		return "", Validationf("%q is not a valid GSTIN: %s is not a GST state code", s, g[:2])
	}
	if c := gstinCheck(g); g[14] != c {
		return "", Validationf("%q is not a valid GSTIN: the last character should be %c", s, c)
	}
	return GSTIN(g), nil
}

// gstinCheck is the GSTIN check character: a mod-36 sum over the first 14
// characters with every second one doubled, each product folded to base 36.
func gstinCheck(g string) byte {
	sum := 0
	for i := 0; i < 14; i++ {
		p := strings.IndexByte(gstinAlphabet, g[i]) * (1 + i%2)
		sum += p/36 + p%36
	}
	return gstinAlphabet[(36-sum%36)%36]
}

// State is where the GSTIN is registered, from its first two digits.
func (g GSTIN) State() GSTState { return gstByCode[string(g)[:2]] }

// PAN is the holder's income-tax number, characters 3 to 12.
func (g GSTIN) PAN() string { return string(g)[2:12] }

// GSTStateByCode looks up a two-digit GST code, retired ones included.
func GSTStateByCode(code string) (GSTState, bool) {
	s, ok := gstByCode[code]
	return s, ok
}

// GSTStateOfAddress is the GST state an Indian address is in, by any spelling
// StateCode knows ("Odisha", "OR", "IN-OD"). False outside India and for a
// state it cannot place.
func GSTStateOfAddress(a Address) (GSTState, bool) {
	country := strings.ToUpper(strings.TrimSpace(a.Country))
	if country != "IN" {
		return GSTState{}, false
	}
	s, ok := gstByISO[StateCode(country, a.State)]
	return s, ok
}
