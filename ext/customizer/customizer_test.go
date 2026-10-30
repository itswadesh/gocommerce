package customizer

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gocommerce "github.com/itswadesh/gocommerce/core"
	"github.com/itswadesh/gocommerce/gctest"
)

type harness struct {
	t   *testing.T
	app *gocommerce.App
	mod *Module
	ctx context.Context
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	mod := New(cfg)
	app := gctest.NewWithConfig(t, gocommerce.Config{MediaDir: t.TempDir()}, mod)
	h := &harness{t: t, app: app, mod: mod, ctx: context.Background()}
	h.switchOn(h.ctx)
	return h
}

func (h *harness) switchOn(ctx context.Context) {
	h.t.Helper()
	on := true
	if _, err := h.app.Plugins().Update(ctx, pluginKey, gocommerce.PluginPatch{Enabled: &on}); err != nil {
		h.t.Fatal(err)
	}
}

func pngOf(w, h int, alpha bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			a := uint8(255)
			if alpha && x < w/2 {
				a = 0
			}
			img.Set(x, y, color.NRGBA{uint8(x), uint8(y), 90, a})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func jpegRedTop(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.RGBA{0, 0, 255, 255}
			if y < h/4 {
				c = color.RGBA{255, 0, 0, 255}
			}
			img.Set(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 95})
	return b.Bytes()
}

func withExif(j []byte, orientation uint16, secret string) []byte {
	var tiff bytes.Buffer
	tiff.WriteString("MM\x00\x2a")
	_ = binary.Write(&tiff, binary.BigEndian, uint32(8))
	_ = binary.Write(&tiff, binary.BigEndian, uint16(1))
	_ = binary.Write(&tiff, binary.BigEndian, []uint16{0x0112, 3})
	_ = binary.Write(&tiff, binary.BigEndian, uint32(1))
	_ = binary.Write(&tiff, binary.BigEndian, []uint16{orientation, 0})
	_ = binary.Write(&tiff, binary.BigEndian, uint32(0))
	tiff.WriteString(secret)
	seg := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	out := []byte{0xFF, 0xD8, 0xFF, 0xE1, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)}
	out = append(out, seg...)
	return append(out, j[2:]...)
}

// pngBomb is a PNG header claiming a w × h picture of colour and alpha at
// depth bits a channel, with no picture behind it.
func pngBomb(w, h uint32, depth byte) []byte {
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = depth, 6
	chunk := append([]byte("IHDR"), ihdr...)
	out := []byte("\x89PNG\r\n\x1a\n")
	out = binary.BigEndian.AppendUint32(out, 13)
	out = append(out, chunk...)
	return binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(chunk))
}

// jpegBomb is a JPEG header claiming a w × h colour photo, its brightness
// sampled as luma says (0x22 for 4:2:0, 0x11 for full resolution), with no
// picture behind it.
func jpegBomb(w, h uint16, marker, luma byte) []byte {
	return []byte{0xFF, 0xD8, 0xFF, marker, 0, 17, 8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 3,
		1, luma, 0, 2, 0x11, 1, 3, 0x11, 1, 0xFF, 0xDA, 0, 2}
}

func (h *harness) media(ctx context.Context, name, mime string, body []byte) *gocommerce.MediaItem {
	h.t.Helper()
	item, err := h.app.MediaLibrary().Upload(ctx, name, mime, bytes.NewReader(body), int64(len(body)))
	if err != nil {
		h.t.Fatal(err)
	}
	return item
}

type caseProduct struct {
	product *gocommerce.Product
	variant int64
	mockup  *gocommerce.MediaItem
	overlay *gocommerce.MediaItem
	sticker *gocommerce.MediaItem
	in      SetupInput
}

func (h *harness) phoneCase(ctx context.Context, sku string) caseProduct {
	h.t.Helper()
	price, stock := int64(49900), 50
	p, err := h.app.Products().CreateProduct(ctx, gocommerce.ProductInput{
		Title: "Case " + sku, Status: "active", SKU: sku, PriceMinor: &price, Stock: &stock,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	c := caseProduct{product: p, variant: p.DefaultVariant().ID}
	c.mockup = h.media(ctx, "mockup.png", "image/png", pngOf(200, 400, false))
	c.overlay = h.media(ctx, "overlay.png", "image/png", pngOf(200, 400, true))
	c.sticker = h.media(ctx, "star.png", "image/png", pngOf(64, 48, true))
	overlay := c.overlay.URL
	c.in = SetupInput{
		Enabled:         true,
		Mockup:          &Picture{URL: c.mockup.URL, Width: 200, Height: 400},
		OverlayURL:      &overlay,
		PrintArea:       &PrintArea{X: 20, Y: 20, Width: 160, Height: 320, Radius: 16},
		PrintSize:       &PrintSize{WidthMM: 70, HeightMM: 140, DPI: 300},
		StickerMediaIDs: []int64{c.sticker.ID},
	}
	return c
}

func (h *harness) save(ctx context.Context, c caseProduct) {
	h.t.Helper()
	if _, err := h.mod.Save(ctx, c.product.ID, c.in); err != nil {
		h.t.Fatalf("save a valid setup: %v", err)
	}
}

func (h *harness) do(method, target, store, contentType string, body []byte, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if store != "" {
		req.Header.Set("X-Store", store)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.app.Handler().ServeHTTP(rec, req)
	return rec
}

func (h *harness) send(method, target, store string, v any) *httptest.ResponseRecorder {
	h.t.Helper()
	body, err := json.Marshal(v)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.do(method, target, store, "application/json", body, "")
}

func (h *harness) uploadRaw(store, field string, file []byte) *httptest.ResponseRecorder {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, _ := w.CreateFormFile(field, "photo")
	_, _ = part.Write(file)
	_ = w.Close()
	return h.do(http.MethodPost, "/x/customizer/uploads", store, w.FormDataContentType(), b.Bytes(), "")
}

func (h *harness) upload(store string, file []byte) Upload {
	h.t.Helper()
	rec := h.uploadRaw(store, "file", file)
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("upload = %d %s", rec.Code, rec.Body)
	}
	var up Upload
	gctest.DecodeData(h.t, rec, &up)
	return up
}

func (h *harness) design(store string, c caseProduct, variant int64, photo string) *httptest.ResponseRecorder {
	h.t.Helper()
	preview := h.upload(store, pngOf(160, 320, false))
	print := h.upload(store, pngOf(160, 320, false))
	return h.send(http.MethodPost, "/x/customizer/designs", store, map[string]any{
		"product_id": c.product.ID, "variant_id": variant,
		"preview_upload_id": preview.ID, "print_upload_id": print.ID,
		"document": map[string]any{"version": 1, "background": "#112233", "layers": []map[string]any{
			{"id": "photo", "type": "image", "x": 0, "y": 0, "width": 160, "height": 320, "rotation": 0, "opacity": 1, "upload_id": photo},
			{"id": "name", "type": "text", "x": 10, "y": 280, "width": 140, "height": 30, "rotation": -5, "opacity": 1,
				"text": "Asha", "font": "poppins", "color": "#ffffff", "font_size": 24, "align": "center"},
		}},
	})
}

func (h *harness) saved(store string, c caseProduct, photo string) Saved {
	h.t.Helper()
	rec := h.design(store, c, c.variant, photo)
	if rec.Code != http.StatusCreated {
		h.t.Fatalf("save a design = %d %s", rec.Code, rec.Body)
	}
	var s Saved
	gctest.DecodeData(h.t, rec, &s)
	return s
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return env.Error.Code + ": " + env.Error.Message
}

func TestASetupIsSavedReadAndServedToTheStorefront(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.phoneCase(h.ctx, "CASE-1")

	rec := gctest.AdminRequest(t, h.app, http.MethodGet, fmt.Sprintf("/api/admin/x/customizer/products/%d", c.product.ID), nil)
	var fresh Setup
	gctest.DecodeData(t, rec, &fresh)
	if fresh.Enabled || fresh.Mockup != nil || fresh.PrintArea != nil || fresh.PrintSize.DPI != 300 || fresh.MinPhotoPx != 1000 ||
		fresh.Tools != (Tools{Upload: true, Text: true, Shapes: true, Background: true}) || len(fresh.Fonts) != 5 {
		t.Errorf("a product never set up = %+v, want it off with the defaults", fresh)
	}
	if rec := h.send(http.MethodGet, fmt.Sprintf("/x/customizer/products/%d", c.product.ID), "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("public setup of a product not set up = %d, want 404", rec.Code)
	}

	body, _ := json.Marshal(c.in)
	rec = h.do(http.MethodPut, fmt.Sprintf("/api/admin/x/customizer/products/%d", c.product.ID), "", "application/json", body, gctest.AdminToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("save = %d %s", rec.Code, rec.Body)
	}
	var stored Setup
	gctest.DecodeData(t, rec, &stored)
	if !stored.Tools.Stickers || stored.Fonts[0] != "inter" || stored.PrintSize.DPI != 300 {
		t.Errorf("saved = %+v, want stickers on because one was chosen, and the default fonts", stored)
	}

	rec = h.send(http.MethodGet, fmt.Sprintf("/x/customizer/products/%d", c.product.ID), "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("public setup = %d %s", rec.Code, rec.Body)
	}
	var pub map[string]any
	gctest.DecodeData(t, rec, &pub)
	if _, leaked := pub["sticker_media_ids"]; leaked {
		t.Error("the public setup carries the sticker ids; it carries the stickers")
	}
	if _, leaked := pub["enabled"]; leaked {
		t.Error("the public setup carries the switch")
	}
	stickers := pub["stickers"].([]any)
	if len(stickers) != 1 || stickers[0].(map[string]any)["width"].(float64) != 64 || stickers[0].(map[string]any)["url"] != c.sticker.URL {
		t.Errorf("stickers = %v, want the one chosen at 64 × 48 with its address", stickers)
	}
	fonts := pub["fonts"].([]any)
	if fonts[1].(map[string]any)["family"] != "Poppins" {
		t.Errorf("fonts = %v, want each with its family", fonts)
	}
	if pub["consent"] != DefaultConsent {
		t.Errorf("consent = %v, want the default line", pub["consent"])
	}

	off := false
	if _, err := h.app.Plugins().Update(h.ctx, pluginKey, gocommerce.PluginPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if rec := h.send(http.MethodGet, fmt.Sprintf("/x/customizer/products/%d", c.product.ID), "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("public setup with the plugin off = %d, want 404", rec.Code)
	}
	if rec := h.uploadRaw("", "file", jpegRedTop(20, 20)); rec.Code != http.StatusNotFound {
		t.Errorf("an upload with the plugin off = %d, want 404", rec.Code)
	}
	h.switchOn(h.ctx)

	draft := "draft"
	if _, err := h.app.Products().UpdateProduct(h.ctx, c.product.ID, gocommerce.ProductPatch{Status: &draft}); err != nil {
		t.Fatal(err)
	}
	if rec := h.send(http.MethodGet, fmt.Sprintf("/x/customizer/products/%d", c.product.ID), "", nil); rec.Code != http.StatusNotFound {
		t.Errorf("public setup of a draft product = %d, want 404", rec.Code)
	}
}

func TestEveryRefusalOfASetup(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.phoneCase(h.ctx, "CASE-V")
	jpegMockup := h.media(h.ctx, "flat.jpg", "image/jpeg", jpegRedTop(200, 400))
	notInLibrary := "/media/elsewhere.png"
	jpegOverlay := jpegMockup.URL
	small := h.media(h.ctx, "small.png", "image/png", pngOf(100, 200, true)).URL

	cases := []struct {
		name string
		edit func(*SetupInput)
		want string
	}{
		{"mockup not in the library", func(s *SetupInput) { s.Mockup.URL = notInLibrary }, "media library"},
		{"mockup size wrong", func(s *SetupInput) { s.Mockup.Width = 201; s.PrintArea, s.Enabled = nil, false }, "200 × 400 pixels"},
		{"mockup too big", func(s *SetupInput) { s.Mockup.Width = 9000 }, "between 1 and 8000"},
		{"overlay a JPEG", func(s *SetupInput) { s.OverlayURL = &jpegOverlay }, "must be a PNG"},
		{"overlay another size", func(s *SetupInput) { s.OverlayURL = &small }, "mockup's size"},
		{"print area outside", func(s *SetupInput) { s.PrintArea.X = 60 }, "inside the 200 × 400"},
		{"print area negative", func(s *SetupInput) { s.PrintArea.Y = -1 }, "inside the 200 × 400"},
		{"print area empty", func(s *SetupInput) { s.PrintArea.Width = 0 }, "at least one pixel"},
		{"radius too big", func(s *SetupInput) { s.PrintArea.Radius = 81 }, "corner radius"},
		{"print area without mockup", func(s *SetupInput) { s.Mockup = nil; s.OverlayURL = nil }, "mockup before the print area"},
		{"shapes differ", func(s *SetupInput) { s.PrintSize.HeightMM = 100 }, "same shape"},
		{"print too big", func(s *SetupInput) { s.PrintSize.WidthMM = 1001 }, "between 0 and 1000 mm"},
		{"dpi too low", func(s *SetupInput) { s.PrintSize.DPI = 71 }, "between 72 and 600 dpi"},
		{"dpi too high", func(s *SetupInput) { s.PrintSize.DPI = 601 }, "between 72 and 600 dpi"},
		{"print file too many pixels", func(s *SetupInput) {
			s.PrintSize = &PrintSize{WidthMM: 500, HeightMM: 1000, DPI: 600}
		}, "lower the resolution"},
		{"unknown font", func(s *SetupInput) { s.Fonts = []string{"comic-sans"} }, "not a font"},
		{"font twice", func(s *SetupInput) { s.Fonts = []string{"inter", "inter"} }, "chosen twice"},
		{"words with no font", func(s *SetupInput) { s.Fonts = []string{} }, "choose a font"},
		{"stickers with none", func(s *SetupInput) {
			s.StickerMediaIDs = nil
			s.Tools = &Tools{Upload: true, Stickers: true}
		}, "choose a sticker"},
		{"sticker not in the library", func(s *SetupInput) { s.StickerMediaIDs = []int64{c.sticker.ID + 1000} }, "sticker"},
		{"min photo too big", func(s *SetupInput) { n := 10001; s.MinPhotoPx = &n }, "smallest photo"},
		{"on with no tool", func(s *SetupInput) { s.Tools = &Tools{} }, "at least one tool"},
		{"on with no mockup", func(s *SetupInput) { s.Mockup, s.PrintArea, s.OverlayURL = nil, nil, nil }, "choose the mockup"},
		{"on with no print area", func(s *SetupInput) { s.PrintArea = nil }, "mark the print area"},
		{"on with no print size", func(s *SetupInput) { s.PrintSize = &PrintSize{DPI: 300} }, "give the print size"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := c.in
			mockup, area, size := *c.in.Mockup, *c.in.PrintArea, *c.in.PrintSize
			in.Mockup, in.PrintArea, in.PrintSize = &mockup, &area, &size
			tc.edit(&in)
			_, err := h.mod.Save(h.ctx, c.product.ID, in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want a refusal saying %q", err, tc.want)
			}
		})
	}

	if _, err := h.mod.Save(h.ctx, c.product.ID+999, c.in); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Errorf("a missing product = %v, want 404", err)
	}
	draft := SetupInput{Mockup: &Picture{URL: jpegMockup.URL, Width: 200, Height: 400}}
	if _, err := h.mod.Save(h.ctx, c.product.ID, draft); err != nil {
		t.Errorf("an unfinished setup kept switched off = %v, want it saved", err)
	}
}

func TestUploadsAreRewrittenAndLimited(t *testing.T) {
	h := newHarness(t, Config{UploadsPerHour: 11})

	photo := withExif(jpegRedTop(40, 80), 6, "GPS 12.9716N 77.5946E")
	up := h.upload("", photo)
	if up.Width != 80 || up.Height != 40 || !validID(up.ID) {
		t.Fatalf("upload = %+v, want an id and the photo turned as it was held: 80 × 40", up)
	}
	rec := h.do(http.MethodGet, up.URL, "", "", nil, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("read the stored photo = %d", rec.Code)
	}
	stored := rec.Body.Bytes()
	if bytes.Contains(stored, []byte("Exif")) || bytes.Contains(stored, []byte("12.9716N")) {
		t.Error("the stored photo still carries its EXIF")
	}
	img, err := jpeg.Decode(bytes.NewReader(stored))
	if err != nil {
		t.Fatal(err)
	}
	if r, _, b, _ := img.At(78, 20).RGBA(); r>>8 < 200 || b>>8 > 60 {
		t.Errorf("right edge = %v, want the red that was the top", img.At(78, 20))
	}
	if r, _, _, _ := img.At(2, 20).RGBA(); r>>8 > 60 {
		t.Errorf("left edge = %v, want blue", img.At(2, 20))
	}
	if png := h.upload("", pngOf(30, 30, true)); png.Width != 30 || !strings.HasSuffix(png.URL, ".png") {
		t.Errorf("a PNG = %+v, want it kept a PNG", png)
	}

	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	for name, tc := range map[string]struct {
		field string
		body  []byte
		want  int
	}{
		"text":        {"file", []byte("<html><script>alert(1)</script></html>"), http.StatusUnsupportedMediaType},
		"gif":         {"file", gif, http.StatusUnsupportedMediaType},
		"broken jpeg": {"file", []byte("\xff\xd8\xff\xe0 not really a jpeg"), http.StatusUnsupportedMediaType},
		"bomb":        {"file", pngBomb(8000, 6000, 8), http.StatusUnprocessableEntity},
		"no file":     {"photo", jpegRedTop(10, 10), http.StatusBadRequest},
		// 36 megapixels at eight bytes a pixel is over the decode budget and
		// at four is not, so that one gets as far as decoding, which finds no
		// picture. The same for 24 megapixels at 15 bytes and at 1.5.
		"16-bit PNG":                   {"file", pngBomb(6000, 6000, 16), http.StatusUnprocessableEntity},
		"8-bit PNG":                    {"file", pngBomb(6000, 6000, 8), http.StatusUnsupportedMediaType},
		"progressive full-colour JPEG": {"file", jpegBomb(6000, 4000, jpegProgressive, 0x11), http.StatusUnprocessableEntity},
		"baseline 4:2:0 JPEG":          {"file", jpegBomb(6000, 4000, jpegBaseline, 0x22), http.StatusUnsupportedMediaType},
	} {
		rec := h.uploadRaw("", tc.field, tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s = %d %s, want %d", name, rec.Code, rec.Body, tc.want)
		}
		if advice := map[string]string{"16-bit PNG": "8-bit", "progressive full-colour JPEG": "ordinary JPEG"}[name]; advice != "" &&
			!strings.Contains(errorCode(t, rec), advice) {
			t.Errorf("%s = %s, want it told to save the picture another way", name, errorCode(t, rec))
		}
	}
	if rec := h.uploadRaw("", "file", make([]byte, maxUploadBytes+1)); rec.Code != http.StatusTooManyRequests {
		t.Errorf("the twelfth upload this hour = %d, want 429", rec.Code)
	}

	big := newHarness(t, Config{})
	tooBig := append(jpegRedTop(10, 10), make([]byte, maxUploadBytes)...)
	if rec := big.uploadRaw("", "file", tooBig); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a file over 15 MB = %d %s, want 413", rec.Code, rec.Body)
	}
}

func TestADesignIsCheckedAgainstItsProduct(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.phoneCase(h.ctx, "CASE-D")
	h.save(h.ctx, c)
	other := h.phoneCase(h.ctx, "CASE-OTHER")
	photo := h.upload("", jpegRedTop(40, 40))

	saved := h.saved("", c, photo.ID)
	if !validID(saved.ID) || saved.PreviewURL == "" {
		t.Fatalf("saved = %+v, want an id and the preview", saved)
	}

	if rec := h.design("", c, other.variant, photo.ID); rec.Code != http.StatusBadRequest {
		t.Errorf("a design for another product's variant = %d, want 400", rec.Code)
	}

	second, err := h.app.Stores().Create(h.ctx, "Second", "second")
	if err != nil {
		t.Fatal(err)
	}
	b := gocommerce.WithStore(h.ctx, second.ID)
	h.switchOn(b)
	theirs := h.upload(second.ID, jpegRedTop(40, 40))
	if rec := h.design("", c, c.variant, theirs.ID); rec.Code != http.StatusBadRequest || !strings.Contains(errorCode(t, rec), "not one of this store's") {
		t.Errorf("a design naming another store's upload = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := h.send(http.MethodGet, fmt.Sprintf("/x/customizer/products/%d", c.product.ID), second.ID, nil); rec.Code != http.StatusNotFound {
		t.Errorf("store B reading store A's product setup = %d, want 404", rec.Code)
	}
	if rec := h.design(second.ID, c, c.variant, theirs.ID); rec.Code != http.StatusNotFound {
		t.Errorf("store B designing store A's product = %d, want 404", rec.Code)
	}

	preview := h.upload("", pngOf(10, 10, false))
	base := func(layers []map[string]any) map[string]any {
		return map[string]any{
			"product_id": c.product.ID, "variant_id": c.variant, "preview_upload_id": preview.ID, "print_upload_id": preview.ID,
			"document": map[string]any{"version": 1, "layers": layers},
		}
	}
	shape := func(i int) map[string]any {
		return map[string]any{"id": fmt.Sprintf("s%d", i), "type": "shape", "x": 1, "y": 1, "width": 10, "height": 10,
			"rotation": 0, "opacity": 1, "shape": "heart", "color": "#ff0000"}
	}
	many := []map[string]any{}
	for i := 0; i < 51; i++ {
		many = append(many, shape(i))
	}
	long := map[string]any{"id": "t", "type": "text", "x": 1, "y": 1, "width": 10, "height": 10, "rotation": 0, "opacity": 1,
		"text": strings.Repeat("a", 201), "font": "inter", "color": "#000000", "font_size": 12}
	wrongField := shape(1)
	wrongField["upload_id"] = photo.ID
	sticker := map[string]any{"id": "st", "type": "sticker", "x": 1, "y": 1, "width": 10, "height": 10, "rotation": 0, "opacity": 1,
		"sticker_media_id": other.sticker.ID}
	oversize := base([]map[string]any{shape(0)})
	oversize["document"].(map[string]any)["padding"] = strings.Repeat("x", maxDocumentBytes)
	for name, tc := range map[string]struct {
		body map[string]any
		want string
	}{
		"51 layers":            {base(many), "at most 50 layers"},
		"over 64 KB":           {oversize, "at most 64 KB"},
		"201 characters":       {base([]map[string]any{long}), "1 to 200 characters"},
		"a field of another":   {base([]map[string]any{wrongField}), "has no upload_id"},
		"another's sticker":    {base([]map[string]any{sticker}), "this product's stickers"},
		"an unknown key":       {base([]map[string]any{{"id": "x", "type": "image", "script": "1"}}), "not a version 1 document"},
		"version 2":            {map[string]any{"product_id": c.product.ID, "variant_id": c.variant, "preview_upload_id": preview.ID, "print_upload_id": preview.ID, "document": map[string]any{"version": 2, "layers": []any{}}}, "version must be 1"},
		"two layers, one name": {base([]map[string]any{shape(1), shape(1)}), "two layers"},
	} {
		rec := h.send(http.MethodPost, "/x/customizer/designs", "", tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(errorCode(t, rec), tc.want) {
			t.Errorf("%s = %d %s, want 400 saying %q", name, rec.Code, rec.Body, tc.want)
		}
	}

	noText := c.in
	noText.Tools = &Tools{Upload: true, Background: true}
	if _, err := h.mod.Save(h.ctx, c.product.ID, noText); err != nil {
		t.Fatal(err)
	}
	if rec := h.design("", c, c.variant, photo.ID); rec.Code != http.StatusBadRequest || !strings.Contains(errorCode(t, rec), "does not take words") {
		t.Errorf("words on a product that offers none = %d %s, want 400", rec.Code, rec.Body)
	}
}

func (h *harness) addDesign(cart string, variant int64, design string, qty int) *httptest.ResponseRecorder {
	return h.send(http.MethodPost, "/api/carts/"+cart+"/line-items", "",
		map[string]any{"variant_id": variant, "design_id": design, "quantity": qty})
}

func TestDesignedLinesAreTheirOwnAndCheckoutKeepsTheDesign(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.phoneCase(h.ctx, "CASE-C")
	h.save(h.ctx, c)
	other := h.phoneCase(h.ctx, "CASE-C2")
	h.save(h.ctx, other)
	photo := h.upload("", withExif(jpegRedTop(60, 30), 1, ""))
	first := h.saved("", c, photo.ID)
	second := h.saved("", c, photo.ID)

	cart, err := h.app.Cart().Create(h.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Cart().AddLine(h.ctx, cart.Token, c.variant, 1); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{first.ID, second.ID, first.ID} {
		if rec := h.addDesign(cart.Token, c.variant, d, 1); rec.Code != http.StatusOK {
			t.Fatalf("add a design = %d %s", rec.Code, rec.Body)
		}
	}
	got, err := h.app.Cart().GetByToken(h.ctx, cart.Token)
	if err != nil {
		t.Fatal(err)
	}
	byDesign := map[string]gocommerce.CartLine{}
	for _, l := range got.Lines {
		byDesign[l.DesignID] = l
	}
	if len(got.Lines) != 3 || byDesign[""].Quantity != 1 || byDesign[first.ID].Quantity != 2 || byDesign[second.ID].Quantity != 1 {
		t.Fatalf("lines = %+v, want the plain case, two of the first design and one of the second", got.Lines)
	}
	if byDesign[first.ID].DesignPreviewURL != first.PreviewURL {
		t.Errorf("preview = %q, want %q", byDesign[first.ID].DesignPreviewURL, first.PreviewURL)
	}

	if _, err := h.app.Cart().UpdateLine(h.ctx, cart.Token, byDesign[second.ID].ID, 3); err != nil {
		t.Fatal(err)
	}
	got, _ = h.app.Cart().GetByToken(h.ctx, cart.Token)
	for _, l := range got.Lines {
		if l.ID == byDesign[second.ID].ID && (l.Quantity != 3 || l.DesignID != second.ID) {
			t.Errorf("after a quantity change = %+v, want three, still the second design", l)
		}
	}

	if rec := h.addDesign(cart.Token, other.variant, first.ID, 1); rec.Code != http.StatusBadRequest {
		t.Errorf("a design added as another product = %d %s, want 400", rec.Code, rec.Body)
	}
	if rec := h.addDesign(cart.Token, c.variant, strings.Repeat("ab", 16), 1); rec.Code != http.StatusNotFound {
		t.Errorf("a design that does not exist = %d, want 404", rec.Code)
	}
	if rec := h.send(http.MethodPost, "/api/carts/"+cart.Token+"/line-items", "",
		map[string]any{"variant_id": c.variant, "design_id": first.ID, "add_on_id": 1}); rec.Code != http.StatusBadRequest {
		t.Errorf("an add-on made to a design = %d, want 400", rec.Code)
	}

	result, err := h.app.Order().Checkout(h.ctx, "cod", gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "asha@example.com", Name: "Asha",
		Address: gocommerce.Address{Line1: "1 MG Road", City: "Bengaluru", State: "KA", PostalCode: "560001", Country: "IN", Phone: "+919800000000"},
	}, "")
	if err != nil {
		t.Fatalf("checkout: %v", err)
	}
	order, err := h.app.Order().Get(h.ctx, result.Order.ID)
	if err != nil {
		t.Fatal(err)
	}
	designs := map[string]map[string]any{}
	for _, l := range order.Lines {
		if d, ok := l.Metadata["design"].(map[string]any); ok {
			designs[d["id"].(string)] = d
		}
	}
	d := designs[first.ID]
	if len(designs) != 2 || d == nil || d["preview_url"] != first.PreviewURL || d["print_url"] == "" {
		t.Fatalf("order lines' designs = %v, want both, each with its preview and print file", designs)
	}
	doc := d["document"].(map[string]any)
	if doc["background"] != "#112233" || len(doc["layers"].([]any)) != 2 {
		t.Errorf("document = %v, want the design as saved", doc)
	}
	if fonts := d["fonts"].([]any); len(fonts) != 1 || fonts[0].(map[string]any)["family"] != "Poppins" {
		t.Errorf("fonts = %v, want the one the words are set in, by family", fonts)
	}
	photos := d["photos"].([]any)
	if len(photos) != 1 || photos[0].(map[string]any)["url"] != photo.URL {
		t.Errorf("photos = %v, want the original photo", photos)
	}

	again, _ := h.app.Cart().Create(h.ctx, "")
	if rec := h.addDesign(again.Token, c.variant, first.ID, 1); rec.Code != http.StatusConflict {
		t.Errorf("a design already ordered = %d %s, want 409", rec.Code, rec.Body)
	}
}

func TestTheSweepKeepsWhatIsBoughtOrInABasket(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.phoneCase(h.ctx, "CASE-S")
	h.save(h.ctx, c)
	orderedPhoto := h.upload("", jpegRedTop(20, 20))
	basketPhoto := h.upload("", jpegRedTop(20, 20))
	stalePhoto := h.upload("", jpegRedTop(20, 20))
	freshPhoto := h.upload("", jpegRedTop(20, 20))
	orphan := h.upload("", jpegRedTop(20, 20))
	ordered := h.saved("", c, orderedPhoto.ID)
	inBasket := h.saved("", c, basketPhoto.ID)
	stale := h.saved("", c, stalePhoto.ID)
	fresh := h.saved("", c, freshPhoto.ID)

	cart, _ := h.app.Cart().Create(h.ctx, "")
	if rec := h.addDesign(cart.Token, c.variant, ordered.ID, 1); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	if _, err := h.app.Order().Checkout(h.ctx, "cod", gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "s@example.com", Name: "S",
		Address: gocommerce.Address{Line1: "1 Road", City: "Pune", State: "MH", PostalCode: "411001", Country: "IN"},
	}, ""); err != nil {
		t.Fatal(err)
	}
	basket, _ := h.app.Cart().Create(h.ctx, "")
	if rec := h.addDesign(basket.Token, c.variant, inBasket.ID, 1); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}

	db := h.app.DB()
	for _, q := range []string{
		`UPDATE customizer_designs SET created_at = now() - interval '31 days', vouched_at = now() - interval '31 days' WHERE id <> $1`,
		`UPDATE customizer_uploads SET created_at = now() - interval '31 days' WHERE id <> $1`,
	} {
		if _, err := db.ExecContext(h.ctx, q, fresh.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(h.ctx, `UPDATE customizer_uploads SET created_at = now() WHERE id = $1`, freshPhoto.ID); err != nil {
		t.Fatal(err)
	}

	designs, uploads, err := h.mod.Sweep(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	staleFiles := len([]string{"stale photo", "stale preview", "stale print file", "orphan"})
	if designs != 1 || uploads != staleFiles {
		t.Errorf("swept %d designs and %d uploads, want 1 and %d", designs, uploads, staleFiles)
	}
	exists := func(table, id string) bool {
		var ok bool
		if err := db.QueryRowContext(h.ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, id).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}
	for name, kept := range map[string]bool{
		"ordered design": exists("customizer_designs", ordered.ID), "basket design": exists("customizer_designs", inBasket.ID),
		"fresh design": exists("customizer_designs", fresh.ID), "ordered photo": exists("customizer_uploads", orderedPhoto.ID),
		"basket photo": exists("customizer_uploads", basketPhoto.ID), "fresh photo": exists("customizer_uploads", freshPhoto.ID),
	} {
		if !kept {
			t.Errorf("the sweep took the %s", name)
		}
	}
	if exists("customizer_designs", stale.ID) || exists("customizer_uploads", stalePhoto.ID) || exists("customizer_uploads", orphan.ID) {
		t.Error("the stale design, its photo or the orphan upload is still there")
	}
	if rec := h.do(http.MethodGet, stalePhoto.URL, "", "", nil, ""); rec.Code != http.StatusNotFound {
		t.Errorf("the stale photo's file = %d, want it deleted", rec.Code)
	}
	if rec := h.do(http.MethodGet, orderedPhoto.URL, "", "", nil, ""); rec.Code != http.StatusOK {
		t.Errorf("the ordered photo's file = %d, want it kept", rec.Code)
	}
	if again, _, err := h.mod.Sweep(h.ctx); err != nil || again != 0 {
		t.Errorf("a second sweep = %d, %v; want nothing left to do", again, err)
	}
}

func TestStaffSeeTheSetupAndManagersChangeIt(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.phoneCase(h.ctx, "CASE-R")
	staff := gctest.OperatorToken(t, h.app, "staff@example.com", gocommerce.RoleStaff)
	manager := gctest.OperatorToken(t, h.app, "manager@example.com", gocommerce.RoleManager)
	path := fmt.Sprintf("/api/admin/x/customizer/products/%d", c.product.ID)
	if rec := gctest.SessionRequest(t, h.app, staff, http.MethodGet, path, nil); rec.Code != http.StatusOK {
		t.Errorf("staff reading = %d", rec.Code)
	}
	if rec := gctest.SessionRequest(t, h.app, staff, http.MethodPut, path, c.in); rec.Code != http.StatusForbidden {
		t.Errorf("staff saving = %d, want 403", rec.Code)
	}
	if rec := gctest.SessionRequest(t, h.app, manager, http.MethodPut, path, c.in); rec.Code != http.StatusOK {
		t.Errorf("a manager saving = %d %s", rec.Code, rec.Body)
	}
	if rec := gctest.SessionRequest(t, h.app, staff, http.MethodGet, "/api/admin/x/customizer/fonts", nil); rec.Code != http.StatusOK {
		t.Errorf("staff reading the fonts = %d", rec.Code)
	}
	gctest.AssertAdminRoutesDeclareRights(t, h.app, "customizer")
	gctest.AssertSpecCoversModuleRoutes(t, h.app, "customizer")
}

func TestReportsCannotReadShoppersPhotos(t *testing.T) {
	app := gctest.NewWithReports(t, New(Config{}))
	for _, table := range []string{"customizer_uploads", "customizer_designs", "customizer_design_uploads"} {
		gctest.AssertReportsCannotRead(t, app, table)
	}
}

func TestOrientationReadsAndTurns(t *testing.T) {
	for o := 1; o <= 8; o++ {
		if got := jpegOrientation(withExif(jpegRedTop(4, 2), uint16(o), "")); got != o {
			t.Errorf("orientation %d read as %d", o, got)
		}
		src := image.NewRGBA(image.Rect(0, 0, 3, 2))
		src.Set(0, 0, color.RGBA{255, 0, 0, 255})
		out := orient(src, o)
		want := map[int]image.Point{1: {0, 0}, 2: {2, 0}, 3: {2, 1}, 4: {0, 1}, 5: {0, 0}, 6: {1, 0}, 7: {1, 2}, 8: {0, 2}}[o]
		if r, _, _, _ := out.At(want.X, want.Y).RGBA(); r>>8 != 255 {
			t.Errorf("orientation %d: the top-left pixel is not at %v", o, want)
		}
	}
	if jpegOrientation(jpegRedTop(4, 4)) != 1 || jpegOrientation([]byte("nope")) != 1 {
		t.Error("a JPEG without EXIF, or not a JPEG, should read as stored")
	}
}
