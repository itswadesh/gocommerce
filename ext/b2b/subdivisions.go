package b2b

import "strings"

// stateIn is a state as territories store it and routing compares it: the
// code, for a first-level subdivision of a country this file knows by name.
//
// A storefront's dealer form sends whatever its address field holds, and that
// is "California" as often as "CA" — while the operator giving a dealer a
// territory types the code. Matching them as text sent every enquiry that
// spelled its state out past the state's dealer to the country's, silently.
// So both sides go through here: "California", "ca" and "US-CA" are all CA,
// and a territory added as "California" is stored as CA.
//
// Only the countries below are known; anywhere else the state is matched as
// it was written, case and spacing aside, which is what every state was
// before. The list is not an attempt at ISO 3166-2: it is the countries this
// store's dealer networks are drawn in, and adding one is adding a map.
func stateIn(country, s string) string {
	state := normState(s)
	if state == "" {
		return ""
	}
	// "US-CA" is ISO 3166-2's own spelling, and what a form built from a
	// subdivision list tends to send.
	state = strings.TrimPrefix(state, country+"-")
	names := subdivisions[country]
	if names == nil {
		return state
	}
	if code, ok := names[subdivisionKey(state)]; ok {
		return code
	}
	return state
}

// subdivisions maps each known name — and each superseded code — to the code
// a territory is stored under. A current code maps to itself, so a lookup of
// any spelling lands on one key.
var subdivisions = map[string]map[string]string{
	"US": codes(map[string][]string{
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
	}),
	"CA": codes(map[string][]string{
		"AB": {"Alberta"}, "BC": {"British Columbia"}, "MB": {"Manitoba"}, "NB": {"New Brunswick"},
		"NL": {"Newfoundland and Labrador", "Newfoundland"}, "NS": {"Nova Scotia"},
		"NT": {"Northwest Territories"}, "NU": {"Nunavut"}, "ON": {"Ontario"},
		"PE": {"Prince Edward Island"}, "QC": {"Quebec", "Québec"}, "SK": {"Saskatchewan"},
		"YT": {"Yukon"},
	}),
	"AU": codes(map[string][]string{
		"ACT": {"Australian Capital Territory"}, "NSW": {"New South Wales"},
		"NT": {"Northern Territory"}, "QLD": {"Queensland"}, "SA": {"South Australia"},
		"TAS": {"Tasmania"}, "VIC": {"Victoria"}, "WA": {"Western Australia"},
	}),
	// India's codes as ISO revised them in 2023 (CG, OD, TS, UK); the codes
	// they replaced are names here, so a form still sending OR reaches the
	// dealer for OD.
	"IN": codes(map[string][]string{
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
	}),
}

// codes turns code → names into name → code, keyed as stateIn keys a lookup.
func codes(byCode map[string][]string) map[string]string {
	out := make(map[string]string, len(byCode)*2)
	for code, names := range byCode {
		out[code] = code
		for _, n := range names {
			out[subdivisionKey(normState(n))] = code
		}
	}
	return out
}

// subdivisionKey is a normalised state with the punctuation people vary on
// taken out: "Washington, D.C." and "Washington DC" are one key, as are
// "Jammu & Kashmir" and "Jammu and Kashmir".
func subdivisionKey(state string) string {
	key := strings.NewReplacer("&", " AND ", ".", " ", ",", " ").Replace(state)
	return strings.Join(strings.Fields(key), " ")
}
