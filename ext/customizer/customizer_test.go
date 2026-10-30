package customizer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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
			img.SetNRGBA(x, y, color.NRGBA{uint8(x), uint8(y), 90, a})
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

type pngHeader struct {
	w, h          uint32
	depth, colour byte
	interlaced    bool
	transparent   bool
}

func pngBomb(p pngHeader) []byte {
	out := []byte("\x89PNG\r\n\x1a\n")
	chunk := func(typ string, data []byte) {
		out = binary.BigEndian.AppendUint32(out, uint32(len(data)))
		c := append([]byte(typ), data...)
		out = append(out, c...)
		out = binary.BigEndian.AppendUint32(out, crc32.ChecksumIEEE(c))
	}
	ihdr := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, p.w), p.h)
	ihdr = append(ihdr, p.depth, p.colour, 0, 0, 0)
	if p.interlaced {
		ihdr[12] = 1
	}
	chunk("IHDR", ihdr)
	if p.transparent {
		chunk("tRNS", make([]byte, map[byte]int{0: 2, 2: 6}[p.colour]))
	}
	chunk("IDAT", nil)
	return out
}

func jpegBomb(w, h uint16, marker byte, gadget string, hv ...byte) []byte {
	out := []byte{0xFF, jpegStart}
	if len(hv) == 4 {
		out = append(out, 0xFF, 0xEE, 0, 14, 'A', 'd', 'o', 'b', 'e', 0, 100, 0, 0, 0, 0, 0)
	}
	frame := []byte{0xFF, marker, 0, byte(8 + 3*len(hv)), 8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), byte(len(hv))}
	for c, s := range hv {
		id := byte(c + 1)
		if gadget == "rgb" {
			id = "RGB"[c]
		}
		frame = append(frame, id, s, 0)
	}
	switch gadget {
	case "stray byte":
		out = append(out, 0)
	case "restart":
		frame = frameBehindRestart(frame, w, h)
	}
	return append(append(out, frame...), 0xFF, jpegScan, 0, 2)
}

func frameBehindRestart(frame []byte, w, h uint16) []byte {
	misreadLength := int(binary.BigEndian.Uint16(frame))
	commentStart := 2 + len(frame) + 4
	greyFrame := []byte{0xFF, jpegBaseline, 0, 11, 8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), 1, 1, 0x11, 0}
	comment := make([]byte, 0xFFFF-2)
	copy(comment[2+misreadLength-commentStart:], greyFrame)
	out := append([]byte{0xFF, 0xD0}, frame...)
	return append(append(out, 0xFF, 0xFE, 0xFF, 0xFF), comment...)
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

func form(field string, file []byte) ([]byte, string) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	part, _ := w.CreateFormFile(field, "photo")
	_, _ = part.Write(file)
	_ = w.Close()
	return b.Bytes(), w.FormDataContentType()
}

func (h *harness) uploadRaw(store, field string, file []byte) *httptest.ResponseRecorder {
	body, contentType := form(field, file)
	return h.do(http.MethodPost, "/x/customizer/uploads", store, contentType, body, "")
}

func (h *harness) server(readTimeout time.Duration) *httptest.Server {
	inner := h.app.Handler()
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "example.com"
		inner.ServeHTTP(w, r)
	}))
	srv.Config.ReadTimeout = readTimeout
	srv.Start()
	h.t.Cleanup(srv.Close)
	return srv
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
	w, ht := c.in.PrintSize.PrintPixels()
	print := h.upload(store, pngOf(w, ht, false))
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

// A phone's photo used as the mockup is measured as the panel and the
// storefront draw it: turned by its EXIF orientation.
func TestAMockupIsMeasuredTheWayABrowserShowsIt(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.phoneCase(h.ctx, "CASE-O")
	sideways := h.media(h.ctx, "held.jpg", "image/jpeg", withExif(jpegRedTop(400, 200), 6, ""))
	in := c.in
	in.Mockup = &Picture{URL: sideways.URL, Width: 200, Height: 400}
	if _, err := h.mod.Save(h.ctx, c.product.ID, in); err != nil {
		t.Errorf("a 400 × 200 JPEG its EXIF turns upright, saved at 200 × 400 = %v, want it saved", err)
	}
	in.Mockup = &Picture{URL: sideways.URL, Width: 400, Height: 200}
	in.PrintArea, in.OverlayURL, in.Enabled = nil, nil, false
	if _, err := h.mod.Save(h.ctx, c.product.ID, in); err == nil || !strings.Contains(err.Error(), "200 × 400 pixels, not 400 × 200") {
		t.Errorf("the same JPEG at its stored 400 × 200 = %v, want it refused as the 200 × 400 it is shown at", err)
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
		{"print file over 4 megapixels", func(s *SetupInput) {
			s.PrintSize = &PrintSize{WidthMM: 70, HeightMM: 140, DPI: 520}
		}, "1433 × 2866 pixels, over 4 megapixels"},
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
	sharpest := c.in
	sharpest.PrintSize = &PrintSize{WidthMM: 70, HeightMM: 140, DPI: 500}
	if _, err := h.mod.Save(h.ctx, c.product.ID, sharpest); err != nil {
		t.Errorf("a 1378 × 2756 print file, under 4 megapixels = %v, want it saved", err)
	}
	draft := SetupInput{Mockup: &Picture{URL: jpegMockup.URL, Width: 200, Height: 400}}
	if _, err := h.mod.Save(h.ctx, c.product.ID, draft); err != nil {
		t.Errorf("an unfinished setup kept switched off = %v, want it saved", err)
	}
}

func TestUploadsAreRewrittenAndLimited(t *testing.T) {
	h := newHarness(t, Config{UploadsPerHour: 13})

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
	pastTheBudget := http.StatusUnsupportedMediaType
	for name, tc := range map[string]struct {
		field string
		body  []byte
		want  int
	}{
		"text":                         {"file", []byte("<html><script>alert(1)</script></html>"), http.StatusUnsupportedMediaType},
		"gif":                          {"file", gif, http.StatusUnsupportedMediaType},
		"broken jpeg":                  {"file", []byte("\xff\xd8\xff\xe0 not really a jpeg"), http.StatusUnsupportedMediaType},
		"bomb":                         {"file", pngBomb(pngHeader{w: 8000, h: 6000, depth: 8, colour: 6}), http.StatusUnprocessableEntity},
		"no file":                      {"photo", jpegRedTop(10, 10), http.StatusBadRequest},
		"16-bit PNG":                   {"file", pngBomb(pngHeader{w: 6000, h: 6000, depth: 16, colour: 6}), http.StatusUnprocessableEntity},
		"8-bit PNG":                    {"file", pngBomb(pngHeader{w: 6000, h: 6000, depth: 8, colour: 6}), pastTheBudget},
		"progressive full-colour JPEG": {"file", jpegBomb(6000, 4000, jpegProgressive, "", 0x11, 0x11, 0x11), http.StatusUnprocessableEntity},
		"baseline 4:2:0 JPEG":          {"file", jpegBomb(6000, 4000, jpegBaseline, "", 0x22, 0x11, 0x11), pastTheBudget},
		"12-megapixel photo":           {"file", withExif(jpegBomb(4032, 3024, jpegBaseline, "", 0x22, 0x11, 0x11), 6, ""), pastTheBudget},
		"12-megapixel progressive photo": {"file", withExif(jpegBomb(4032, 3024, jpegProgressive, "", 0x22, 0x11, 0x11), 6, ""),
			pastTheBudget},
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
		t.Errorf("the fourteenth upload this hour = %d, want 429", rec.Code)
	}

	big := newHarness(t, Config{})
	tooBig := append(jpegRedTop(10, 10), make([]byte, maxUploadBytes)...)
	if rec := big.uploadRaw("", "file", tooBig); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("a file over 15 MB = %d %s, want 413", rec.Code, rec.Body)
	}
}

func TestAPictureIsReckonedAsTheDecoderReadsIt(t *testing.T) {
	h := newHarness(t, Config{UploadsPerHour: -1})
	cmyk := []byte{0x11, 0x11, 0x11, 0x11}
	for name, body := range map[string][]byte{
		"CMYK behind a restart marker":              jpegBomb(8000, 5000, jpegProgressive, "restart", cmyk...),
		"colour behind a restart marker":            jpegBomb(8000, 5000, jpegProgressive, "restart", 0x11, 0x11, 0x11),
		"CMYK behind a stray byte":                  jpegBomb(5000, 3570, jpegProgressive, "stray byte", cmyk...),
		"components named R, G and B":               jpegBomb(5000, 3570, jpegProgressive, "rgb", 0x11, 0x11, 0x11),
		"16-bit grey with a transparent colour":     pngBomb(pngHeader{w: 8000, h: 5000, depth: 16, transparent: true}),
		"interlaced grey with a transparent colour": pngBomb(pngHeader{w: 8000, h: 5000, depth: 8, interlaced: true, transparent: true}),
	} {
		if rec := h.uploadRaw("", "file", body); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s = %d %s, want 422: too large to read", name, rec.Code, rec.Body)
		}
	}
	if rec := h.uploadRaw("", "file", pngBomb(pngHeader{w: 8000, h: 5000, depth: 8, interlaced: true})); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("interlaced grey, nothing transparent = %d %s, want it read, which finds no picture", rec.Code, rec.Body)
	}
}

func FuzzTheWalkFindsTheFrameTheDecoderDecodes(f *testing.F) {
	f.Add(jpegRedTop(24, 16))
	f.Add(withExif(jpegRedTop(16, 8), 6, ""))
	f.Add(jpegBomb(16, 16, jpegProgressive, "restart", 0x11, 0x11, 0x11, 0x11))
	f.Add(jpegBomb(16, 16, jpegProgressive, "stray byte", 0x11, 0x11, 0x11))
	f.Add(jpegBomb(16, 16, jpegBaseline, "rgb", 0x11, 0x11, 0x11))
	var grey bytes.Buffer
	_ = jpeg.Encode(&grey, image.NewGray(image.Rect(0, 0, 9, 9)), nil)
	f.Add(grey.Bytes())
	ratios := map[[2]int]image.YCbCrSubsampleRatio{{1, 1}: image.YCbCrSubsampleRatio444, {1, 2}: image.YCbCrSubsampleRatio440,
		{2, 1}: image.YCbCrSubsampleRatio422, {2, 2}: image.YCbCrSubsampleRatio420, {4, 1}: image.YCbCrSubsampleRatio411,
		{4, 2}: image.YCbCrSubsampleRatio410}
	f.Fuzz(func(t *testing.T, b []byte) {
		cfg, err := jpeg.DecodeConfig(bytes.NewReader(b))
		if err != nil || cfg.Width*cfg.Height > 1<<20 {
			return
		}
		img, err := jpeg.Decode(bytes.NewReader(b))
		if err != nil {
			return
		}
		frame, _, ok := jpegHeader(b)
		want := map[string]int{"*image.Gray": 1, "*image.YCbCr": 3, "*image.RGBA": 3, "*image.CMYK": 4}[fmt.Sprintf("%T", img)]
		if !ok || frame.width != cfg.Width || frame.height != cfg.Height || len(frame.components) != want {
			t.Fatalf("walked to %+v (ok %v); the decoder made a %d × %d %T", frame, ok, cfg.Width, cfg.Height, img)
		}
		if y, isYCbCr := img.(*image.YCbCr); isYCbCr {
			maxH, maxV := 1, 1
			for _, c := range frame.components {
				maxH, maxV = max(maxH, c.h), max(maxV, c.v)
			}
			ratio := image.YCbCrSubsampleRatio444
			if hr, vr, known := chromaRatio(frame.components, maxH, maxV); known {
				ratio = ratios[[2]int{hr, vr}]
			}
			if y.SubsampleRatio != ratio {
				t.Fatalf("walked to %+v, chroma at %v; the decoder kept it at %v", frame, ratio, y.SubsampleRatio)
			}
		}
	})
}

func TestAStalledUploadIsCutOffAndHoldsNoOneUp(t *testing.T) {
	h := newHarness(t, Config{UploadsPerHour: -1})
	uploadDeadline = 500 * time.Millisecond
	t.Cleanup(func() { uploadDeadline = 15 * time.Second })
	srv := h.server(0)
	body, contentType := form("file", jpegRedTop(400, 300))

	stalled := make([]net.Conn, 5)
	for i := range stalled {
		c, err := net.Dial("tcp", srv.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		fmt.Fprintf(c, "POST /x/customizer/uploads HTTP/1.1\r\nHost: shop\r\nContent-Type: %s\r\nContent-Length: %d\r\n\r\n",
			contentType, len(body))
		_, _ = c.Write(body[:len(body)/2])
		stalled[i] = c
	}
	time.Sleep(200 * time.Millisecond)
	client := srv.Client()
	client.Timeout = 5 * time.Second
	resp, err := client.Post(srv.URL+"/x/customizer/uploads", contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("an upload beside five stalled ones: %v; want it taken at once", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("an upload beside five stalled ones = %d, want 201", resp.StatusCode)
	}
	for i, c := range stalled {
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			t.Errorf("stalled upload %d was left open: %v", i, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("stalled upload %d = %d, want 400", i, resp.StatusCode)
		}
	}
}

func TestWaitingToBeDecodedCostsAnUploadNothing(t *testing.T) {
	h := newHarness(t, Config{UploadsPerHour: -1})
	srv := h.server(300 * time.Millisecond)
	body, contentType := form("file", jpegRedTop(40, 30))
	h.mod.decodes <- struct{}{}
	go func() {
		time.Sleep(time.Second)
		<-h.mod.decodes
	}()
	resp, err := srv.Client().Post(srv.URL+"/x/customizer/uploads", contentType, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("an upload that waited a second to be decoded = %d, want 201", resp.StatusCode)
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

	small := h.upload("", pngOf(825, 1654, false)).ID
	wrongSize := base([]map[string]any{shape(0)})
	wrongSize["print_upload_id"] = small
	if rec := h.send(http.MethodPost, "/x/customizer/designs", "", wrongSize); rec.Code != http.StatusBadRequest ||
		!strings.Contains(errorCode(t, rec), "the print file is 825 × 1654 pixels; this product prints at 827 × 1654") {
		t.Errorf("a print file two pixels narrow = %d %s, want 400; a rounding is one pixel, not two", rec.Code, rec.Body)
	}
	near := h.upload("", pngOf(828, 1653, false)).ID
	wrongSize["print_upload_id"] = near
	if rec := h.send(http.MethodPost, "/x/customizer/designs", "", wrongSize); rec.Code != http.StatusCreated {
		t.Errorf("a print file a pixel off each way = %d %s, want it taken as the editor's rounding", rec.Code, rec.Body)
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
	// The plain case went in before its customizer was switched on; once
	// it is on, a plain line is refused (TestAPlainLineOfADesignedProductIsRefused).
	cart, err := h.app.Cart().Create(h.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.app.Cart().AddLine(h.ctx, cart.Token, c.variant, 1); err != nil {
		t.Fatal(err)
	}
	h.save(h.ctx, c)
	other := h.phoneCase(h.ctx, "CASE-C2")
	h.save(h.ctx, other)
	photo := h.upload("", withExif(jpegRedTop(60, 30), 1, ""))
	first := h.saved("", c, photo.ID)
	second := h.saved("", c, photo.ID)
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

	// The shop changes the print after the designs were made: each order
	// line still says the size its own print file was made for.
	resized := c.in
	resized.PrintSize = &PrintSize{WidthMM: 35, HeightMM: 70, DPI: 300}
	resized.PrintArea = &PrintArea{X: 30, Y: 30, Width: 140, Height: 280}
	if _, err := h.mod.Save(h.ctx, c.product.ID, resized); err != nil {
		t.Fatal(err)
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
	if got, _ := json.Marshal(map[string]any{"size": d["print_size"], "area": d["print_area"]}); string(got) !=
		`{"area":{"height":320,"radius":16,"width":160,"x":20,"y":20},"size":{"dpi":300,"height_mm":140,"width_mm":70}}` {
		t.Errorf("print = %s, want the size and area the design was made for, not the setup's since", got)
	}

	again, _ := h.app.Cart().Create(h.ctx, "")
	if rec := h.addDesign(again.Token, c.variant, first.ID, 1); rec.Code != http.StatusConflict {
		t.Errorf("a design already ordered = %d %s, want 409", rec.Code, rec.Body)
	}
}

// TestAddLineKeepsTheOriginalPriceSnapshot's rule, for a designed line: the
// same design added again after the shop raised the price keeps the price
// the shopper saw, and checkout stops to ask.
func TestADesignAddedAgainKeepsItsPriceSnapshot(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.phoneCase(h.ctx, "CASE-P")
	h.save(h.ctx, c)
	saved := h.saved("", c, h.upload("", jpegRedTop(20, 20)).ID)
	cart, err := h.app.Cart().Create(h.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if rec := h.addDesign(cart.Token, c.variant, saved.ID, 1); rec.Code != http.StatusOK {
		t.Fatalf("add a design = %d %s", rec.Code, rec.Body)
	}
	raised := int64(59900)
	if _, err := h.app.Products().UpdateVariant(h.ctx, c.variant, gocommerce.VariantPatch{PriceMinor: &raised}); err != nil {
		t.Fatal(err)
	}
	if rec := h.addDesign(cart.Token, c.variant, saved.ID, 1); rec.Code != http.StatusOK {
		t.Fatalf("add it again = %d %s", rec.Code, rec.Body)
	}
	got, err := h.app.Cart().GetByToken(h.ctx, cart.Token)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 1 || got.Lines[0].Quantity != 2 || got.Lines[0].UnitPrice.AmountMinor != 49900 ||
		got.Lines[0].CurrentPrice.AmountMinor != raised {
		t.Fatalf("lines = %+v, want two at the 49900 first seen, beside the current 59900", got.Lines)
	}
	_, err = h.app.Order().Checkout(h.ctx, "cod", gocommerce.CheckoutInput{
		CartID: cart.Token, Email: "p@example.com", Name: "P",
		Address: gocommerce.Address{Line1: "1 Road", City: "Pune", State: "MH", PostalCode: "411001", Country: "IN"},
	}, "")
	if !errors.Is(err, gocommerce.ErrConflict) {
		t.Errorf("checkout after the price moved = %v, want a conflict naming it", err)
	}
}

// The line designer runs inside the basket's and the checkout's transaction,
// so it must ask nothing of the pool: with one connection, a second one taken
// there would wait for ever.
func TestADesignIsVouchedForWithinItsLinesTransaction(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.phoneCase(h.ctx, "CASE-TX")
	h.save(h.ctx, c)
	saved := h.saved("", c, h.upload("", jpegRedTop(20, 20)).ID)
	cart, err := h.app.Cart().Create(h.ctx, "")
	if err != nil {
		t.Fatal(err)
	}

	h.app.DB().SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(h.ctx, 5*time.Second)
	defer cancel()
	if _, err := h.app.Cart().AddDesignLine(ctx, cart.Token, c.variant, saved.ID, 1); err != nil {
		t.Fatalf("add a design on one connection: %v", err)
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
		if _, got, _ := jpegHeader(withExif(jpegRedTop(4, 2), uint16(o), "")); got != o {
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
	if _, plain, _ := jpegHeader(jpegRedTop(4, 4)); plain != 1 {
		t.Error("a JPEG without EXIF should read as stored")
	}
	if _, nope, _ := jpegHeader([]byte("nope")); nope != 1 {
		t.Error("something that is not a JPEG should read as stored")
	}
}

// A product with its customizer on says so on its payload, so a storefront
// opens the editor and offers no quick add; a plain product does not, and
// neither does any product once the plugin is off.
func TestAProductSaysItIsDesignable(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.phoneCase(h.ctx, "CASE-D")
	h.save(h.ctx, c)
	plain := h.phoneCase(h.ctx, "CASE-P")

	designable := func(id int64) bool {
		t.Helper()
		var p struct {
			Designable bool `json:"designable"`
		}
		rec := h.do(http.MethodGet, "/api/products/"+strconv.FormatInt(id, 10), "", "", nil, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET product = %d %s", rec.Code, rec.Body)
		}
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(env.Data, &p); err != nil {
			t.Fatal(err)
		}
		return p.Designable
	}
	// Sent for a plain product too, so a storefront can tell it from an
	// engine that predates the key.
	if rec := h.do(http.MethodGet, "/api/products/"+strconv.FormatInt(plain.product.ID, 10), "", "", nil, ""); !strings.Contains(rec.Body.String(), `"designable":false`) {
		t.Errorf("a plain product leaves designable out: %s", rec.Body)
	}
	if !designable(c.product.ID) || designable(plain.product.ID) {
		t.Fatalf("designable = %v / %v, want the set-up case only", designable(c.product.ID), designable(plain.product.ID))
	}
	list, _, err := h.app.Products().ListProducts(h.ctx, gocommerce.ProductQuery{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range list {
		if p.Designable != (p.ID == c.product.ID) {
			t.Errorf("listed %s designable = %v", p.Slug, p.Designable)
		}
	}

	off := false
	if _, err := h.app.Plugins().Update(h.ctx, pluginKey, gocommerce.PluginPatch{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if designable(c.product.ID) {
		t.Error("with the plugin off the case still says it is designable")
	}
}

// A quick add of a product sold only to a design is refused with its own
// code, so the storefront can say "design this one first".
func TestAPlainLineOfADesignedProductIsRefused(t *testing.T) {
	h := newHarness(t, Config{})
	c := h.phoneCase(h.ctx, "CASE-R")
	h.save(h.ctx, c)
	cart, err := h.app.Cart().Create(h.ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := h.send(http.MethodPost, "/api/carts/"+cart.Token+"/line-items", "",
		map[string]any{"variant_id": c.variant, "quantity": 1})
	if rec.Code != http.StatusConflict || !strings.HasPrefix(errorCode(t, rec), "design_required: ") {
		t.Fatalf("plain add = %d %s, want 409 design_required", rec.Code, rec.Body)
	}
	photo := h.upload("", withExif(jpegRedTop(60, 30), 1, ""))
	d := h.saved("", c, photo.ID)
	if rec := h.addDesign(cart.Token, c.variant, d.ID, 1); rec.Code != http.StatusOK {
		t.Fatalf("the designed add = %d %s", rec.Code, rec.Body)
	}
}
