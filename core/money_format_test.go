package gocommerce

import "testing"

func TestFormatMoneyWritesAPriceForAMessage(t *testing.T) {
	for _, c := range []struct {
		minor    int64
		currency string
		want     string
	}{
		{149900, "INR", "₹1,499.00"},
		{12345678, "INR", "₹1,23,456.78"},
		{249700, "USD", "$2,497.00"},
		{123456789, "EUR", "€1,234,567.89"},
		{500, "GBP", "£5.00"},
		{1500, "JPY", "¥1,500"},
		{1234, "KWD", "KWD 1.234"},
		{-2500, "INR", "-₹25.00"},
		{999, "", "9.99"},
	} {
		if got := FormatMoney(c.minor, c.currency); got != c.want {
			t.Errorf("FormatMoney(%d, %q) = %q, want %q", c.minor, c.currency, got, c.want)
		}
	}
}
