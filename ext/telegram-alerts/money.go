package telegramalerts

import (
	"strconv"
	"strings"
)

// formatMoney writes an amount the way the person reading the alert writes
// money: the currency's sign where it has a well-known one, its code where it
// does not, its own number of decimals, and digits grouped — in lakhs and
// crores for the rupee, because that is how an Indian reader counts.
//
// The API never does this (rule 6): a client decides how its reader sees
// money. An alert is the client here, and its reader is a phone's lock screen.
func formatMoney(minor int64, currency string) string {
	code := strings.ToUpper(strings.TrimSpace(currency))
	exp := exponent(code)

	neg := minor < 0
	if neg {
		minor = -minor
	}
	scale := int64(1)
	for i := 0; i < exp; i++ {
		scale *= 10
	}
	whole := group(strconv.FormatInt(minor/scale, 10), code == "INR")
	if exp > 0 {
		frac := strconv.FormatInt(minor%scale, 10)
		whole += "." + strings.Repeat("0", exp-len(frac)) + frac
	}

	out := whole
	if sign, ok := signs[code]; ok {
		out = sign + whole
	} else if code != "" {
		out = code + " " + whole
	}
	if neg {
		out = "-" + out
	}
	return out
}

// signs are the currencies whose sign a reader recognises without the code.
// Deliberately short: "$" alone is read as the US dollar, so the other dollars
// keep their codes rather than borrowing it.
var signs = map[string]string{
	"INR": "₹", "USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥",
}

// exponent is how many decimals a currency writes: none for the yen, three
// for the dinar, two for most. The same table ext/feeds and ext/klaviyo keep.
func exponent(code string) int {
	switch code {
	case "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "VND", "VUV", "XAF", "XOF", "XPF":
		return 0
	case "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND":
		return 3
	}
	return 2
}

// group puts separators in a run of digits: every three, or for Indian
// grouping the last three and then every two (12,34,567).
func group(digits string, indian bool) string {
	if len(digits) <= 3 {
		return digits
	}
	head, tail := digits[:len(digits)-3], digits[len(digits)-3:]
	step := 3
	if indian {
		step = 2
	}
	var parts []string
	for len(head) > step {
		parts = append([]string{head[len(head)-step:]}, parts...)
		head = head[:len(head)-step]
	}
	parts = append([]string{head}, parts...)
	return strings.Join(append(parts, tail), ",")
}
