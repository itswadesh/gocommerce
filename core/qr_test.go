package gocommerce

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The expected symbols come from segno 1.6.1, an independent encoder, made
// with the same text, level M and a forced mask: segno.make(text, error="m",
// mask=m, mode="byte", boost_error=False), hashed row by row as '1' dark and
// '0' light with no quiet zone. Each mask, and each version feature drawn —
// one block and several, short and long blocks, the version information from
// 7 and the 16-bit length from 10 — is covered by one of them.
//
// Each text fills its version exactly. segno pads a bit stream that already
// ends on a byte with a whole zero byte, which ISO/IEC 18004 §7.4.10 does not
// ask for and this encoder does not write; a full symbol has no room for it.
func TestQRCodeMatchesAnIndependentEncoder(t *testing.T) {
	for _, c := range []struct {
		length, mask, version, size int
		sha                         string
	}{
		{14, 0, 1, 21, "fa5837982f0085ff1687b6d3ff248625af8188e4397c669866f538022cb448ea"},
		{14, 1, 1, 21, "b71ac04668a843666c99fca8d085ebfef8563735319b521d3b90aec23f51cf5f"},
		{14, 2, 1, 21, "00968807a93408b2c3dbe32f882666df3f403ce7b816758c9ad410d6f0d1f192"},
		{14, 3, 1, 21, "3759d7cc201df614f5bdbe40deb32fafc83be48189621eb34b630f730465f3b1"},
		{14, 4, 1, 21, "be34ecc23023bb1443f58f47b67c5857325bff8fc71c47e9f41be77232a30f21"},
		{14, 5, 1, 21, "1e4174b2d79d91d0c429a6f07fa870734cf943258959c5db2902a8c47e501fce"},
		{14, 6, 1, 21, "1c8fbcaa2add4e591a5c73f7f83f106b7f4ef576d3f6f4208f0268e37384e55c"},
		{14, 7, 1, 21, "715100b9cb443abf75fed2601f8877cd9b1c635f690cb82d5259cbfa7c6568d8"},
		{42, 5, 3, 29, "23702f205698344a12357260729fa1f002ba51342ce5b4ace36547d2b7fa5597"},
		{62, 4, 4, 33, "f9f1b44cc9f462ff5dea980041f30c9348d879b2e3dfc69e6791fb3445d0fea7"},
		{122, 2, 7, 45, "fde33875723bf84c9a3cf00748b99ce0f542ea488eed70145d20791753b2690d"},
		{152, 1, 8, 49, "5e79031f610ae93c74fe370a77e382786ca6dba60ab76dc2ee586fd30ab8a2af"},
		{213, 6, 10, 57, "20a823c6e86fee1b67a092c0af66abc9018f9ae586bf76ceeea7de0dc71fd156"},
	} {
		text := qrTestText(c.length)
		m, err := qrEncode([]byte(text), c.mask)
		if err != nil {
			t.Fatalf("%d bytes: %v", c.length, err)
		}
		if len(m) != c.size {
			t.Fatalf("%d bytes: %d modules a side, want %d (version %d)", c.length, len(m), c.size, c.version)
		}
		var bits strings.Builder
		for _, row := range m {
			for _, dark := range row {
				bits.WriteByte(map[bool]byte{true: '1', false: '0'}[dark])
			}
		}
		sum := sha256.Sum256([]byte(bits.String()))
		if got := hex.EncodeToString(sum[:]); got != c.sha {
			t.Errorf("%d bytes, mask %d: the symbol differs from segno's", c.length, c.mask)
		}
	}
}

// qrTestText is the reference script's text of n bytes.
func qrTestText(n int) string {
	if n == 14 {
		return "https://a.in/o"
	}
	base := "https://example.com/order?item="
	return base + strings.Repeat("a1b2c3d4e5", 30)[:n-len(base)]
}

// A bit stream that ends on a byte goes straight to the pad codewords.
func TestQRDataIsPaddedOnlyToTheNextByte(t *testing.T) {
	got := qrDataBytes(3, []byte("https://rasoi-bakery.shopnx.in/order"))
	if len(got) != 44 || !strings.HasSuffix(hex.EncodeToString(got), "465720ec11ec11ec11") {
		t.Fatalf("data codewords = % x", got)
	}
}

func TestQRCodeSVGDrawsAQuietZoneAndRefusesTooMuch(t *testing.T) {
	svg, err := QRCodeSVG("https://rasoi-bakery.shopnx.in/order")
	if err != nil {
		t.Fatal(err)
	}
	// Version 3 is 29 modules; four of quiet zone each side make 37.
	if !strings.HasPrefix(svg, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 37 37"`) || !strings.Contains(svg, `<path fill="#000" d="M4 4h7v1h-7z`) {
		t.Fatalf("svg = %.160s…", svg)
	}
	if _, err := QRCodeSVG(strings.Repeat("x", qrCapacity()+1)); !isValidation(err) {
		t.Fatalf("%d bytes: err = %v, want a validation error", qrCapacity()+1, err)
	}
	if _, err := QRCodeSVG(strings.Repeat("x", qrCapacity())); err != nil {
		t.Fatalf("%d bytes should fit: %v", qrCapacity(), err)
	}
}
