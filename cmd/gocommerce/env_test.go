package main

import (
	"strings"
	"testing"
)

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

// The customizer's limits are read from the environment, and one that does
// not read stops the start rather than leaving the default in place.
func TestCustomizerConfig(t *testing.T) {
	t.Setenv("GOCOMMERCE_CUSTOMIZER_UPLOADS_PER_HOUR", " 300 ")
	t.Setenv("GOCOMMERCE_CUSTOMIZER_DESIGNS_PER_HOUR", "")
	cfg, err := customizerConfig()
	if err != nil || cfg.UploadsPerHour != 300 || cfg.DesignsPerHour != 0 {
		t.Fatalf("customizerConfig() = %+v, %v; want 300 uploads and the module's default for designs", cfg, err)
	}
	for _, bad := range []string{"lots", "0", "-1", "2.5"} {
		t.Setenv("GOCOMMERCE_CUSTOMIZER_DESIGNS_PER_HOUR", bad)
		if _, err := customizerConfig(); err == nil || !strings.Contains(err.Error(), "GOCOMMERCE_CUSTOMIZER_DESIGNS_PER_HOUR") {
			t.Errorf("designs per hour %q = %v, want an error naming the variable", bad, err)
		}
	}
}
