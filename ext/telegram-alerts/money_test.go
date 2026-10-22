package telegramalerts

import "testing"

// An alert is read on a phone by a person, so its total is written the way
// that person writes money — which is the one place in the engine a formatted
// amount belongs (rule 6 keeps it out of the API, not out of a message).
func TestMoneyIsWrittenForReading(t *testing.T) {
	for _, c := range []struct {
		minor    int64
		currency string
		want     string
	}{
		{129900, "INR", "₹1,299.00"},
		{12345678, "INR", "₹1,23,456.78"}, // lakhs, as an Indian reader groups them
		{1000000000, "INR", "₹1,00,00,000.00"},
		{99, "INR", "₹0.99"},
		{123456, "USD", "$1,234.56"},
		{123456789, "EUR", "€1,234,567.89"},
		{500, "GBP", "£5.00"},
		{1200, "JPY", "¥1,200"},
		{1500, "KWD", "KWD 1.500"},
		{100, "xyz", "XYZ 1.00"},
		{-2550, "USD", "-$25.50"},
		{0, "", "0.00"},
	} {
		if got := formatMoney(c.minor, c.currency); got != c.want {
			t.Errorf("formatMoney(%d, %q) = %q, want %q", c.minor, c.currency, got, c.want)
		}
	}
}

func TestTheFirstNameIsTheFirstWord(t *testing.T) {
	for in, want := range map[string]string{"Asha Rao": "Asha", "  Ravi ": "Ravi", "": ""} {
		if got := firstName(in); got != want {
			t.Errorf("firstName(%q) = %q, want %q", in, got, want)
		}
	}
}
