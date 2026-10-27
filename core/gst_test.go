package gocommerce

import (
	"strings"
	"testing"
)

func TestGSTINChecksum(t *testing.T) {
	t.Parallel()
	for _, ok := range []string{"27AAPFU0939F1ZV", "29AAGCB7383J1Z4", " 27aapfu0939f1zv "} {
		g, err := ParseGSTIN(ok)
		if err != nil {
			t.Errorf("ParseGSTIN(%q) = %v, want valid", ok, err)
			continue
		}
		if strings.TrimSpace(strings.ToUpper(ok)) != string(g) {
			t.Errorf("ParseGSTIN(%q) = %q, want it upper-cased and trimmed", ok, g)
		}
	}
	for _, bad := range []struct{ in, says string }{
		{"27AAPFU0939F1ZW", "the last character should be V"},
		{"22AAAAA0000A1Z5", "the last character should be"},
		{"00AAPFU0939F1ZV", "00 is not a GST state code"},
		{"99AAPFU0939F1ZV", "99 is not a GST state code"},
		{"27AAPFU0939F1Z", "15 characters"},
		{"27AAPFU0939F1XV", "2 digits, a 10-character PAN"},
	} {
		_, err := ParseGSTIN(bad.in)
		if err == nil || !strings.Contains(err.Error(), bad.says) {
			t.Errorf("ParseGSTIN(%q) = %v, want an error saying %q", bad.in, err, bad.says)
		}
	}
	g, _ := ParseGSTIN("27AAPFU0939F1ZV")
	if g.State().Name != "Maharashtra" {
		t.Errorf("state %q, want Maharashtra", g.State().Name)
	}
}

func TestRetiredGSTPrefixesStillParse(t *testing.T) {
	t.Parallel()
	for prefix, iso := range map[string]string{"25": "DH", "28": "AP"} {
		body := prefix + "AAPFU0939F1Z"
		g, err := ParseGSTIN(body + string(gstinCheck(body+"0")))
		if err != nil {
			t.Fatalf("prefix %s: %v", prefix, err)
		}
		if g.State().ISO != iso {
			t.Errorf("prefix %s is in %q, want %s", prefix, g.State().ISO, iso)
		}
	}
}

func TestGSTStateOfAddress(t *testing.T) {
	t.Parallel()
	for _, spelling := range []string{"Odisha", "OR", "IN-OD", "orissa"} {
		s, ok := GSTStateOfAddress(Address{Country: "in", State: spelling})
		if !ok || s.Code != "21" {
			t.Errorf("%q is %+v %v, want code 21", spelling, s, ok)
		}
	}
	if s, _ := GSTStateOfAddress(Address{Country: "IN", State: "Chandigarh"}); !s.UT {
		t.Error("Chandigarh is a union territory with UTGST")
	}
	if s, _ := GSTStateOfAddress(Address{Country: "IN", State: "Delhi"}); s.UT {
		t.Error("Delhi has a legislature and charges SGST")
	}
	if _, ok := GSTStateOfAddress(Address{Country: "GB", State: "OD"}); ok {
		t.Error("an address outside India has no GST state")
	}
	if _, ok := GSTStateOfAddress(Address{Country: "IN", State: "Atlantis"}); ok {
		t.Error("an unknown state has no GST state")
	}
}

// The GST table and the ISO table must name the same places, or an address
// that resolves for shipping and tax would not resolve for an invoice.
func TestEveryIndianStateHasAGSTCode(t *testing.T) {
	t.Parallel()
	for iso := range subdivisions["IN"] {
		if _, ok := gstByISO[iso]; !ok {
			t.Errorf("IN-%s has no GST code", iso)
		}
	}
	for _, s := range gstStates {
		if _, ok := subdivisions["IN"][s.ISO]; s.ISO != "" && !ok {
			t.Errorf("GST code %s names IN-%s, which subdivisions.go does not know", s.Code, s.ISO)
		}
	}
}
