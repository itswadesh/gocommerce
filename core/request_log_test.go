package gocommerce

import "testing"

func TestRequestLinesCoverTheAPIAndWebhooksOnly(t *testing.T) {
	for path, want := range map[string]bool{
		"/api/products":                       true,
		"/api/checkout/stripe/webhook":        true,
		"/x/notify-whatsapp/webhook":          true,
		"/x/retailassistant/whatsapp/webhook": true,
		"/x/telegram-alerts/webhook":          true,
		"/x/banners":                          false,
		"/internal/traefik":                   false,
		"/_panel/theme.js":                    false,
		"/webhook":                            false,
	} {
		if got := logsRequestLine(path); got != want {
			t.Errorf("%s: %v, want %v", path, got, want)
		}
	}
}
