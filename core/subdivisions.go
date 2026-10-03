package gocommerce

import "strings"

// A state as a rule stores it and an address is matched against it.
//
// A checkout address carries whatever its form sent, which is "California" as
// often as "CA", while an operator writing a tax rate, a shipping zone or a
// dealer's territory types the code. Matched as text, every address that
// spelled its state out silently missed the state's rule and fell to the
// country's: the wrong tax, the wrong zone, the wrong dealer, and nothing said
// so (D72). So a rule's state is stored as its code, and an address is matched
// by every spelling the engine knows for it.
//
// Only the countries in the table below are known by name. Anywhere else a
// state is matched as written, case and spacing aside, which is what every
// state was before. The table is not ISO 3166-2: it is the countries this
// engine's stores sell in, and adding one is adding a map.

// StateCode is a state as rules store it: the code, for a first-level
// subdivision of a country the engine knows by name ("California", "ca" and
// "US-CA" are all CA), otherwise the text upper-cased with its spaces closed up.
func StateCode(country, state string) string {
	country = strings.ToUpper(strings.TrimSpace(country))
	norm := normalizeState(state)
	if norm == "" {
		return ""
	}
	// "US-CA" is ISO 3166-2's own spelling, and what a form built from a
	// subdivision list tends to send.
	norm = strings.TrimPrefix(norm, country+"-")
	if code, ok := stateCodes[country][subdivisionKey(norm)]; ok {
		return code
	}
	return norm
}

// StateSpellings is every stored form that means the state an address gives:
// its code and each name the engine knows for it, plus the text as written.
// A rule saved before codes were stored may hold a name, and matching against
// all of these finds it without rewriting a row nobody asked to change.
func StateSpellings(country, state string) []string {
	if strings.TrimSpace(state) == "" {
		return []string{}
	}
	country = strings.ToUpper(strings.TrimSpace(country))
	code := StateCode(country, state)
	seen := map[string]bool{}
	out := []string{}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(code)
	add(normalizeState(state))
	add(strings.ToUpper(strings.TrimSpace(state)))
	for _, s := range stateNames[country][code] {
		add(s)
	}
	return out
}

func normalizeState(s string) string { return strings.ToUpper(strings.Join(strings.Fields(s), " ")) }

// subdivisionKey is a normalised state with the punctuation people vary on
// taken out: "Washington, D.C." and "Washington DC" are one key, as are
// "Jammu & Kashmir" and "Jammu and Kashmir".
func subdivisionKey(state string) string {
	key := strings.NewReplacer("&", " AND ", ".", " ", ",", " ").Replace(state)
	return strings.Join(strings.Fields(key), " ")
}

// subdivisions is each known country's codes and the names, and superseded
// codes, that mean them.
var subdivisions = map[string]map[string][]string{
	"US": {
		"AL": {"Alabama"}, "AK": {"Alaska"}, "AZ": {"Arizona"}, "AR": {"Arkansas"},
		"CA": {"California"}, "CO": {"Colorado"}, "CT": {"Connecticut"}, "DE": {"Delaware"},
		"DC": {"District of Columbia", "Washington DC", "Washington D C"},
		"FL": {"Florida"}, "GA": {"Georgia"}, "HI": {"Hawaii"}, "ID": {"Idaho"},
		"IL": {"Illinois"}, "IN": {"Indiana"}, "IA": {"Iowa"}, "KS": {"Kansas"},
		"KY": {"Kentucky"}, "LA": {"Louisiana"}, "ME": {"Maine"}, "MD": {"Maryland"},
		"MA": {"Massachusetts"}, "MI": {"Michigan"}, "MN": {"Minnesota"}, "MS": {"Mississippi"},
		"MO": {"Missouri"}, "MT": {"Montana"}, "NE": {"Nebraska"}, "NV": {"Nevada"},
		"NH": {"New Hampshire"}, "NJ": {"New Jersey"}, "NM": {"New Mexico"}, "NY": {"New York"},
		"NC": {"North Carolina"}, "ND": {"North Dakota"}, "OH": {"Ohio"}, "OK": {"Oklahoma"},
		"OR": {"Oregon"}, "PA": {"Pennsylvania"}, "RI": {"Rhode Island"}, "SC": {"South Carolina"},
		"SD": {"South Dakota"}, "TN": {"Tennessee"}, "TX": {"Texas"}, "UT": {"Utah"},
		"VT": {"Vermont"}, "VA": {"Virginia"}, "WA": {"Washington"}, "WV": {"West Virginia"},
		"WI": {"Wisconsin"}, "WY": {"Wyoming"},
		"AS": {"American Samoa"}, "GU": {"Guam"}, "MP": {"Northern Mariana Islands"},
		"PR": {"Puerto Rico"}, "VI": {"US Virgin Islands", "U S Virgin Islands", "Virgin Islands"},
	},
	"CA": {
		"AB": {"Alberta"}, "BC": {"British Columbia"}, "MB": {"Manitoba"}, "NB": {"New Brunswick"},
		"NL": {"Newfoundland and Labrador", "Newfoundland"}, "NS": {"Nova Scotia"},
		"NT": {"Northwest Territories"}, "NU": {"Nunavut"}, "ON": {"Ontario"},
		"PE": {"Prince Edward Island"}, "QC": {"Quebec", "Québec"}, "SK": {"Saskatchewan"},
		"YT": {"Yukon"},
	},
	"AU": {
		"ACT": {"Australian Capital Territory"}, "NSW": {"New South Wales"},
		"NT": {"Northern Territory"}, "QLD": {"Queensland"}, "SA": {"South Australia"},
		"TAS": {"Tasmania"}, "VIC": {"Victoria"}, "WA": {"Western Australia"},
	},
	// India's codes as ISO revised them in 2023 (CG, OD, TS, UK); the codes
	// they replaced are names here, so a form still sending OR reaches the
	// dealer for OD.
	"IN": {
		"AP": {"Andhra Pradesh"}, "AR": {"Arunachal Pradesh"}, "AS": {"Assam"}, "BR": {"Bihar"},
		"CG": {"Chhattisgarh", "CT"}, "GA": {"Goa"}, "GJ": {"Gujarat"}, "HR": {"Haryana"},
		"HP": {"Himachal Pradesh"}, "JH": {"Jharkhand"}, "KA": {"Karnataka"}, "KL": {"Kerala"},
		"MP": {"Madhya Pradesh"}, "MH": {"Maharashtra"}, "MN": {"Manipur"}, "ML": {"Meghalaya"},
		"MZ": {"Mizoram"}, "NL": {"Nagaland"}, "OD": {"Odisha", "Orissa", "OR"}, "PB": {"Punjab"},
		"RJ": {"Rajasthan"}, "SK": {"Sikkim"}, "TN": {"Tamil Nadu"}, "TS": {"Telangana", "TG"},
		"TR": {"Tripura"}, "UP": {"Uttar Pradesh"}, "UK": {"Uttarakhand", "Uttaranchal", "UT"},
		"WB": {"West Bengal"},
		"AN": {"Andaman and Nicobar Islands"}, "CH": {"Chandigarh"},
		"DH": {"Dadra and Nagar Haveli and Daman and Diu", "DN", "DD"},
		"DL": {"Delhi", "NCT of Delhi", "National Capital Territory of Delhi"},
		"JK": {"Jammu and Kashmir"}, "LA": {"Ladakh"}, "LD": {"Lakshadweep"},
		"PY": {"Puducherry", "Pondicherry"},
	},
}

// stateCodes maps each country's names and codes, keyed as subdivisionKey
// keys them, to the code a rule is stored under. stateNames is the other way:
// every stored form a code may have been saved as before D72, for matching
// rows written then.
var stateCodes, stateNames = indexSubdivisions(subdivisions)

func indexSubdivisions(table map[string]map[string][]string) (map[string]map[string]string, map[string]map[string][]string) {
	codes := map[string]map[string]string{}
	names := map[string]map[string][]string{}
	for country, byCode := range table {
		codes[country] = map[string]string{}
		names[country] = map[string][]string{}
		for code, aliases := range byCode {
			codes[country][code] = code
			for _, a := range aliases {
				n := normalizeState(a)
				codes[country][subdivisionKey(n)] = code
				names[country][code] = append(names[country][code], n)
				if k := subdivisionKey(n); k != n {
					names[country][code] = append(names[country][code], k)
				}
			}
		}
	}
	return codes, names
}
