package gocommerce

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Highlights, the free gift note and the public product's videos (D134).

func isValidation(err error) bool { return errors.Is(err, ErrValidation) }

type publicProduct struct {
	ID          int64          `json:"id"`
	Metadata    map[string]any `json:"metadata"`
	SoldAtLeast *int64         `json:"sold_at_least"`
	Media       []VariantImage `json:"media"`
}

func getPublicProduct(t *testing.T, app *App, id int64) publicProduct {
	t.Helper()
	rec := do(t, app, http.MethodGet, "/api/products/"+strconv.FormatInt(id, 10))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET product: %d %s", rec.Code, rec.Body)
	}
	var env struct {
		Data publicProduct `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

func TestHighlightsAndFreeGiftAreShortPlainLines(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := simpleProduct(t, app, "HL-1", 19900, 5)

	set := func(meta Metadata) error {
		_, err := app.Products().UpdateProduct(ctx, p.ID, ProductPatch{Metadata: &meta})
		return err
	}

	// Trimmed, the blank rows the editor leaves dropped, other keys untouched.
	if err := set(Metadata{
		"highlights": []any{"  Shockproof corners ", "", "Raised camera lip"},
		"free_gift":  " A screen guard with every case ",
		"care":       "wipe clean",
	}); err != nil {
		t.Fatalf("save highlights: %v", err)
	}
	got, err := app.Products().GetProduct(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	lines, _ := got.Metadata["highlights"].([]any)
	if len(lines) != 2 || lines[0] != "Shockproof corners" || lines[1] != "Raised camera lip" {
		t.Errorf("highlights = %#v, want the two lines trimmed", got.Metadata["highlights"])
	}
	if got.Metadata["free_gift"] != "A screen guard with every case" {
		t.Errorf("free_gift = %#v", got.Metadata["free_gift"])
	}
	if got.Metadata["care"] != "wipe clean" {
		t.Errorf("an unrelated key was changed: %#v", got.Metadata["care"])
	}

	nine := make([]any, 9)
	for i := range nine {
		nine[i] = "line " + strconv.Itoa(i)
	}
	for name, meta := range map[string]Metadata{
		"nine highlights":       {"highlights": nine},
		"a long highlight":      {"highlights": []any{strings.Repeat("x", 81)}},
		"a highlight on two":    {"highlights": []any{"one\ntwo"}},
		"highlights not a list": {"highlights": "Shockproof"},
		"a number for a line":   {"highlights": []any{7}},
		"a long gift note":      {"free_gift": strings.Repeat("x", 81)},
		"a gift that is a list": {"free_gift": []any{"a"}},
	} {
		if err := set(meta); !isValidation(err) {
			t.Errorf("%s: err = %v, want a validation error", name, err)
		}
	}

	// Emptied, the keys go rather than staying as empty values.
	if err := set(Metadata{"highlights": []any{" "}, "free_gift": ""}); err != nil {
		t.Fatal(err)
	}
	got, _ = app.Products().GetProduct(ctx, p.ID)
	if _, ok := got.Metadata["highlights"]; ok {
		t.Errorf("an empty highlight list was kept: %#v", got.Metadata)
	}
	if _, ok := got.Metadata["free_gift"]; ok {
		t.Errorf("an empty gift note was kept: %#v", got.Metadata)
	}

	// Creating is held to the same rule.
	in := ProductInput{Title: "Too many", Metadata: Metadata{"highlights": nine},
		Variants: []VariantInput{{SKU: "HL-2", PriceMinor: 100}}}
	if _, err := app.Products().CreateProduct(ctx, in); !isValidation(err) {
		t.Errorf("create with nine highlights: err = %v, want a validation error", err)
	}
}

func TestPublicProductCarriesItsVideos(t *testing.T) {
	app := newTestApp(t)
	ctx := context.Background()
	p := simpleProduct(t, app, "VID-1", 19900, 5)
	photo, err := app.MediaLibrary().AddURL(ctx, "https://cdn.example.com/case.jpg", MediaImage, "The case")
	if err != nil {
		t.Fatal(err)
	}
	clip, err := app.MediaLibrary().AddURL(ctx, "https://cdn.example.com/case.mp4", MediaVideo, "")
	if err != nil {
		t.Fatal(err)
	}
	tube, err := app.MediaLibrary().AddURL(ctx, "https://www.youtube.com/watch?v=abc123", MediaVideo, "")
	if err != nil {
		t.Fatal(err)
	}
	model, err := app.MediaLibrary().AddURL(ctx, "https://cdn.example.com/case.glb", MediaModel, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := app.MediaLibrary().SetProductMedia(ctx, p.ID, []int64{photo.ID, clip.ID, model.ID, tube.ID}); err != nil {
		t.Fatal(err)
	}

	got := getPublicProduct(t, app, p.ID)
	if len(got.Media) != 3 {
		t.Fatalf("media = %+v, want the photo and both videos", got.Media)
	}
	want := []struct{ url, kind string }{
		{photo.URL, MediaImage}, {clip.URL, MediaVideo}, {tube.URL, MediaVideo},
	}
	for i, w := range want {
		if got.Media[i].URL != w.url || got.Media[i].Kind != w.kind {
			t.Errorf("media[%d] = %+v, want %s %s", i, got.Media[i], w.kind, w.url)
		}
	}
	if got.Media[0].Alt != "The case" {
		t.Errorf("alt = %q", got.Media[0].Alt)
	}
}
