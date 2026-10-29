package gocommerce

import (
	"strconv"
	"strings"
)

// FormatMoney writes an amount the way a person reads it in a message:
// "₹1,499.00", "$2,497.00". For the text of an email or a text message only.
// The API never returns a formatted amount (AGENTS.md rule 6), because how an
// amount is shown is the client's business; but a message has no client after
// it. The email is where the amount is shown, so whoever words it formats it.
//
// Rupees are grouped the Indian way, in lakhs and crores (₹1,23,456.78),
// because that is how an Indian shopper writes them. A currency with no sign
// here is written with its code in front.
func FormatMoney(minor int64, currency string) string {
	code := strings.ToUpper(strings.TrimSpace(currency))
	exp := CurrencyExponent(code)
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	digits := strconv.FormatInt(minor, 10)
	if len(digits) <= exp {
		digits = strings.Repeat("0", exp-len(digits)+1) + digits
	}
	whole, frac := digits[:len(digits)-exp], digits[len(digits)-exp:]
	out := groupDigits(whole, code == "INR")
	if exp > 0 {
		out += "." + frac
	}
	switch symbol := currencySymbols[code]; {
	case symbol != "":
		return sign + symbol + out
	case code != "":
		return sign + code + " " + out
	}
	return sign + out
}

var currencySymbols = map[string]string{
	"INR": "₹", "USD": "$", "EUR": "€", "GBP": "£", "JPY": "¥",
	"AUD": "A$", "CAD": "C$", "NZD": "NZ$", "SGD": "S$",
}

// groupDigits puts thousands separators into a run of digits: every three
// from the right, or for Indian grouping three and then every two.
func groupDigits(s string, indian bool) string {
	if len(s) <= 3 {
		return s
	}
	head, tail := s[:len(s)-3], s[len(s)-3:]
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
	return strings.Join(parts, ",") + "," + tail
}
