package main

import (
	"errors"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// A GOCOMMERCE_BRANDS that would stop the engine is reported by doctor as a
// failed check rather than stopping doctor too (D114 review), so a value can
// be tried before it is deployed.
func TestDoctorReportsBrandsThatWouldStopTheEngine(t *testing.T) {
	rep := gocommerce.Report{OK: true, Checks: []gocommerce.Diagnostic{
		{Name: "database", Status: gocommerce.StatusOK},
		{Name: "brands", Status: gocommerce.StatusOK, Detail: "GoCommerce (no panel address)"},
	}}
	markBrandsProblem(&rep, errors.New("gocommerce: brand 2 (Beta): panel_url is required"))
	var brands gocommerce.Diagnostic
	for _, c := range rep.Checks {
		if c.Name == "brands" {
			brands = c
		}
	}
	if rep.OK || brands.Status != gocommerce.StatusFail || !strings.Contains(brands.Detail, "brand 2 (Beta)") ||
		!strings.Contains(brands.Detail, "every brand") || brands.Hint == "" {
		t.Fatalf("report = %+v", rep)
	}

	for raw, wantErr := range map[string]bool{
		`[{"name":"Alpha","panel_url":"https://admin.alpha.example"}]`: false,
		`[{"name":"Alpha","panel_url":"http://admin.alpha.example"}]`:  true,
		`[{"name":"Alpha"`: true,
		``:                 false,
	} {
		_, err := brandsFromEnv(raw, "", "", false)
		if (err != nil) != wantErr {
			t.Errorf("brandsFromEnv(%s) = %v, want error %v", raw, err, wantErr)
		}
	}
}
