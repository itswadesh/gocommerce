package gocommerce

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 128, 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func decodedSize(t *testing.T, body []byte) (string, int, int) {
	t.Helper()
	cfg, kind, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	return kind, cfg.Width, cfg.Height
}

func TestResizeImage(t *testing.T) {
	out, err := resizeImage(testJPEG(t, 1000, 500), 320)
	if err != nil {
		t.Fatal(err)
	}
	if kind, w, h := decodedSize(t, out); kind != "jpeg" || w != 320 || h != 160 {
		t.Errorf("1000×500 at 320 = %s %d×%d, want jpeg 320×160", kind, w, h)
	}

	small := testJPEG(t, 200, 100)
	out, err = resizeImage(small, 320)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, small) {
		t.Error("a picture narrower than the width was re-encoded, want it served as it is")
	}

	// Transparency survives: a PNG with alpha stays a PNG.
	clear := image.NewNRGBA(image.Rect(0, 0, 400, 400))
	clear.Set(10, 10, color.NRGBA{255, 0, 0, 255})
	var b bytes.Buffer
	_ = png.Encode(&b, clear)
	out, err = resizeImage(b.Bytes(), 160)
	if err != nil {
		t.Fatal(err)
	}
	if kind, w, h := decodedSize(t, out); kind != "png" || w != 160 || h != 160 {
		t.Errorf("transparent PNG at 160 = %s %d×%d, want png 160×160", kind, w, h)
	}

	// A header claiming more pixels than the cap is refused before decoding.
	if _, err := resizeImage(pngClaiming(t, 10000, 10000), 320); err != errImageTooLarge {
		t.Errorf("a 100-megapixel header = %v, want errImageTooLarge", err)
	}

	if _, err := resizeImage([]byte("GIF89a......"), 320); err == nil {
		t.Error("a GIF was resized, want it refused as unsupported")
	}
}

// pngClaiming is a 1×1 PNG whose header says w×h, with a valid checksum.
func pngClaiming(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1)))
	p := b.Bytes()
	// 8-byte signature, 4-byte length, "IHDR", then width and height.
	binary.BigEndian.PutUint32(p[16:], uint32(w))
	binary.BigEndian.PutUint32(p[20:], uint32(h))
	binary.BigEndian.PutUint32(p[29:], crc32.ChecksumIEEE(p[12:29]))
	return p
}

func TestImageWidthSnapsToTheFixedSet(t *testing.T) {
	for opts, want := range map[string]int{
		"width=320,quality=80,format=auto": 320,
		"width=179":                        240,
		"width=96,height=96":               160,
		"width=5000":                       1280,
		"quality=80":                       1280,
		"width=-4":                         1280,
	} {
		if got := imageWidth(opts); got != want {
			t.Errorf("imageWidth(%q) = %d, want %d", opts, got, want)
		}
	}
}

// imageApp is a store that keeps uploads (and so its image cache) in a
// directory of its own, allowed to fetch from loopback when private is true.
func imageApp(t *testing.T, private bool) *App {
	t.Helper()
	dsn := requireDB(t)
	resetSchema(t, dsn)
	cfg := testConfig(dsn)
	cfg.MediaDir = t.TempDir()
	cfg.AllowPrivateURLs = private
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func TestImageRouteResizesOnlyTheStoresOwnPictures(t *testing.T) {
	var fetches atomic.Int32
	photo := testJPEG(t, 925, 925)
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(photo)
	}))
	defer origin.Close()

	app := imageApp(t, true)
	ctx := WithStore(context.Background(), app.defaultStore)
	if _, err := app.media.AddURL(ctx, origin.URL+"/photos/necklace_925x.jpg", MediaImage, ""); err != nil {
		t.Fatal(err)
	}
	base := ImagesPath(app.defaultStore) + "/cdn-cgi/image/width=320,quality=80,format=auto/"

	rec := do(t, app, http.MethodGet, base+"photos/necklace_925x.jpg")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}
	if kind, w, h := decodedSize(t, rec.Body.Bytes()); kind != "jpeg" || w != 320 || h != 320 {
		t.Errorf("served %s %d×%d, want jpeg 320×320", kind, w, h)
	}
	if rec.Body.Len() >= len(photo) {
		t.Errorf("served %d bytes, no smaller than the %d-byte original", rec.Body.Len(), len(photo))
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable", cc)
	}
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}

	// Made once: the second request and a revalidation never reach the origin.
	again := do(t, app, http.MethodGet, base+"photos/necklace_925x.jpg")
	if again.Code != http.StatusOK || !bytes.Equal(again.Body.Bytes(), rec.Body.Bytes()) {
		t.Errorf("second GET = %d, want the same picture", again.Code)
	}
	revalidate := do(t, app, http.MethodGet, base+"photos/necklace_925x.jpg", func(r *http.Request) {
		r.Header.Set("If-None-Match", etag)
	})
	if revalidate.Code != http.StatusNotModified {
		t.Errorf("If-None-Match = %d, want 304", revalidate.Code)
	}
	if n := fetches.Load(); n != 1 {
		t.Errorf("origin fetched %d times, want 1", n)
	}

	// Another path on the same host is not one of the store's pictures.
	if rec := do(t, app, http.MethodGet, base+"photos/someone-elses.jpg"); rec.Code != http.StatusNotFound {
		t.Errorf("a picture the store does not show = %d, want 404", rec.Code)
	}
	if n := fetches.Load(); n != 1 {
		t.Errorf("an unknown picture reached the origin (%d fetches)", n)
	}
	if rec := do(t, app, http.MethodGet, "/images/store_NOPE/cdn-cgi/image/width=320/photos/necklace_925x.jpg"); rec.Code != http.StatusNotFound {
		t.Errorf("an unknown store = %d, want 404", rec.Code)
	}

	// The record the storefront reads points here.
	if body := do(t, app, http.MethodGet, "/api/store").Body.String(); !strings.Contains(body, `"images_path":"`+ImagesPath(app.defaultStore)+`"`) {
		t.Errorf("GET /api/store = %s, want images_path", body)
	}
}

func TestImageRouteRefusesAPrivateOrigin(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the private origin was reached")
	}))
	defer origin.Close()

	app := imageApp(t, false)
	ctx := WithStore(context.Background(), app.defaultStore)
	if _, err := app.media.AddURL(ctx, origin.URL+"/inside.jpg", MediaImage, ""); err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodGet, ImagesPath(app.defaultStore)+"/cdn-cgi/image/width=320/inside.jpg")
	if rec.Code != http.StatusForbidden {
		t.Errorf("a picture on a loopback address = %d %s, want 403", rec.Code, rec.Body)
	}
}

func TestImageRouteCapsTheOriginalsSize(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte{0xff}, maxImageSourceBytes+1))
	}))
	defer origin.Close()

	app := imageApp(t, true)
	ctx := WithStore(context.Background(), app.defaultStore)
	if _, err := app.media.AddURL(ctx, origin.URL+"/huge.jpg", MediaImage, ""); err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodGet, ImagesPath(app.defaultStore)+"/cdn-cgi/image/width=320/huge.jpg")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("a 20 MB+ original = %d, want 422", rec.Code)
	}
}

func TestImageRouteResizesAnUpload(t *testing.T) {
	app := imageApp(t, false)
	ctx := WithStore(context.Background(), app.defaultStore)
	photo := testJPEG(t, 800, 600)
	item, err := app.media.Upload(ctx, "shot.jpg", "image/jpeg", bytes.NewReader(photo), int64(len(photo)))
	if err != nil {
		t.Fatal(err)
	}
	rec := do(t, app, http.MethodGet, ImagesPath(app.defaultStore)+"/cdn-cgi/image/width=160"+item.URL)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", item.URL, rec.Code, rec.Body)
	}
	if _, w, h := decodedSize(t, rec.Body.Bytes()); w != 160 || h != 120 {
		t.Errorf("served %d×%d, want 160×120", w, h)
	}
}
