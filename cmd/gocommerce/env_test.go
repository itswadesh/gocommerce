package main

import "testing"

// The first name that is set wins, so a store's own setting beats the
// install-wide default, and an alias is read when the usual name is not.
func TestFirstEnv(t *testing.T) {
	t.Setenv("RESEND_FROM", "")
	t.Setenv("DEFAULT_EMAIL_FROM_ADDRESS", "shop@example.com")
	if got := firstEnv("RESEND_FROM", "DEFAULT_EMAIL_FROM_ADDRESS"); got != "shop@example.com" {
		t.Fatalf("fallback: %q", got)
	}
	t.Setenv("RESEND_FROM", "orders@example.com")
	if got := firstEnv("RESEND_FROM", "DEFAULT_EMAIL_FROM_ADDRESS"); got != "orders@example.com" {
		t.Fatalf("own setting: %q", got)
	}
	if got := firstEnv("GOCOMMERCE_TEST_UNSET_1", "GOCOMMERCE_TEST_UNSET_2"); got != "" {
		t.Fatalf("none set: %q", got)
	}
}
