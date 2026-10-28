package gocommerce

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// Resized pictures for a storefront (D139).
//
// A product card 180px wide was downloading the 925px original. The storefront
// already asks for a srcset of fixed widths when its store record turns
// `imageCdn` on, in Cloudflare's URL shape — `<base>/cdn-cgi/image/width=320,
// quality=80,format=auto/<the original's path>` — so the engine answers that
// shape and the storefront needs no change of its own.
//
// The original's host is not in that URL, only its path, so the path is looked
// up among the pictures the store itself shows: its media library and its
// category pictures. That lookup is the allow-list. A path no picture of this
// store has is a 404, which is what keeps this from being a proxy anyone can
// point at any address.

// imageWidths are the only widths made, the storefront's srcset candidates. A
// request for any other width gets the next one up, so the cache holds at most
// eight renditions of a picture whatever a caller asks for.
var imageWidths = []int{160, 240, 320, 480, 640, 768, 1024, 1280}

const (
	// A product photograph is a few hundred kilobytes; twenty megabytes is
	// already a camera's raw output.
	maxImageSourceBytes = 20 << 20
	// Decoding allocates four bytes a pixel before anything is scaled, so a
	// small file that claims to be 50 000 × 50 000 is refused on its header.
	maxImagePixels    = 40_000_000
	imageFetchTimeout = 15 * time.Second
	imageJPEGQuality  = 80
	// What a resized picture is cached for by a browser or a CDN. The URL names
	// the width and the original, and an original is never edited in place.
	imageCacheControl = "public, max-age=31536000, immutable"
)

// imageSlots caps resizes running at once: each holds a decoded picture, up to
// 160 MB at the pixel cap.
var imageSlots = make(chan struct{}, max(1, min(4, runtime.NumCPU())))

func (a *App) mountImageRoutes() {
	a.HandleFunc("GET "+imagesPrefix+"{store}/cdn-cgi/image/{options}/{path...}", a.handleImage)
}

const imagesPrefix = "/images/"

// ImagesPath is where a store's resized pictures are served, for the store's
// public record: the storefront puts the Cloudflare-shaped suffix after it.
func ImagesPath(storeID string) string { return imagesPrefix + storeID }

// imageCacheDir is beside the uploads when the engine keeps any, so it is
// backed up and kept with them, and in the system's temporary directory when
// it does not. Either way it is a cache: deleting it costs only the time to
// make each size again.
func (a *App) imageCacheDir() string {
	if a.cfg.MediaDir != "" {
		return filepath.Join(a.cfg.MediaDir, ".cache", "images")
	}
	return filepath.Join(os.TempDir(), "gocommerce-images")
}

// imageSource is a picture the store shows: a file in its own uploads, or an
// address it was added by.
type imageSource struct {
	file string // a path on disk, for an upload
	url  string // what the store recorded
}

func (a *App) handleImage(w http.ResponseWriter, r *http.Request) {
	st, err := a.lookupStore(r.Context(), []string{r.PathValue("store")})
	if err != nil || st == nil || st.Status != "active" {
		RespondError(w, r, NotFoundf("no such store"))
		return
	}
	ctx := WithStore(r.Context(), st.ID)
	width := imageWidth(r.PathValue("options"))

	src, err := a.findImageSource(ctx, "/"+r.PathValue("path"), r.URL.RawQuery)
	if err != nil {
		RespondError(w, r, err)
		return
	}
	if src == nil {
		RespondError(w, r, NotFoundf("this store shows no picture at that address"))
		return
	}

	sum := sha256.Sum256([]byte(st.ID + "\x00" + src.url + "\x00" + strconv.Itoa(width)))
	cached := filepath.Join(a.imageCacheDir(), st.ID, hex.EncodeToString(sum[:16]))
	if _, err := os.Stat(cached); err != nil {
		if err := a.makeImage(ctx, src, width, cached); err != nil {
			var unsupported errUnsupportedImage
			if errors.As(err, &unsupported) {
				// WebP, AVIF and GIF are not decoded here (no dependency for them,
				// D139). The original is still the right picture, just heavier.
				w.Header().Set("Cache-Control", "public, max-age=86400")
				http.Redirect(w, r, src.url, http.StatusFound)
				return
			}
			RespondError(w, r, err)
			return
		}
	}
	serveCachedImage(w, r, cached)
}

// imageWidth reads width= from Cloudflare's comma-separated options and
// rounds it up to a width this engine makes. Anything else in them — height,
// quality, format — is ignored: the aspect ratio is the original's, and the
// format is the one the original's transparency calls for.
func imageWidth(options string) int {
	want := imageWidths[len(imageWidths)-1]
	for _, opt := range strings.Split(options, ",") {
		k, v, _ := strings.Cut(opt, "=")
		if k == "width" || k == "w" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				want = n
			}
		}
	}
	for _, w := range imageWidths {
		if w >= want {
			return w
		}
	}
	return imageWidths[len(imageWidths)-1]
}

// findImageSource finds the picture of this store whose address has the given
// path and query, whatever its host — the storefront's URL has dropped it.
// Only the store's own pictures are candidates; nil means none matched.
func (a *App) findImageSource(ctx context.Context, wantPath, wantQuery string) (*imageSource, error) {
	like := "%" + likeEscape(path.Base(wantPath)) + "%"
	// ponytail: a LIKE scan of this store's media rows on every cache miss,
	// fine at a library's size; an index on the url's path if one grows huge.
	rows, err := a.db.QueryContext(ctx, `
		(SELECT url FROM media WHERE kind = 'image' AND url LIKE $1 ESCAPE '\' LIMIT 50)
		UNION ALL
		(SELECT metadata->>'image' FROM categories WHERE metadata->>'image' LIKE $1 ESCAPE '\' LIMIT 50)`, like)
	if err != nil {
		return nil, Internalf(err, "look up the picture")
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, Internalf(err, "read the picture")
		}
		u, err := url.Parse(strings.TrimSpace(raw))
		if err != nil || u.Path != wantPath || u.RawQuery != wantQuery {
			continue
		}
		if u.Scheme == "" && u.Host == "" {
			// A site-relative address is one of this engine's own uploads.
			local, ok := a.mediaStore.(*LocalMediaStore)
			if !ok {
				continue
			}
			key, under := strings.CutPrefix(u.Path, strings.TrimRight(local.Prefix, "/")+"/")
			if file, ok := local.pathOf(key); ok && under {
				return &imageSource{file: file, url: u.String()}, nil
			}
			continue
		}
		if u.Scheme == "http" || u.Scheme == "https" {
			return &imageSource{url: u.String()}, nil
		}
	}
	return nil, rows.Err()
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

type errUnsupportedImage struct{ kind string }

func (e errUnsupportedImage) Error() string { return e.kind + " is not resized" }

// makeImage reads the original, scales it to width and writes it to dst. An
// original no wider than width is kept as it is: a picture is never enlarged.
func (a *App) makeImage(ctx context.Context, src *imageSource, width int, dst string) error {
	select {
	case imageSlots <- struct{}{}:
		defer func() { <-imageSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}

	body, err := a.readImageSource(ctx, src)
	if err != nil {
		return err
	}
	out, err := resizeImage(body, width)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return Internalf(err, "create the image cache")
	}
	// Written aside and renamed, so a second request for the same size never
	// serves half a file.
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".part-*")
	if err != nil {
		return Internalf(err, "create the image cache")
	}
	_, werr := tmp.Write(out)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp.Name())
		return Internalf(errors.Join(werr, cerr), "write the image cache")
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		os.Remove(tmp.Name())
		return Internalf(err, "write the image cache")
	}
	return nil
}

var errImageTooLarge = &APIError{Status: http.StatusUnprocessableEntity, Code: "image_too_large",
	Message: fmt.Sprintf("the original is over %d MB or %d megapixels", maxImageSourceBytes>>20, maxImagePixels/1_000_000)}

// readImageSource reads at most maxImageSourceBytes of the original. A remote
// one is fetched through the outbound client, so an address inside the
// server's own network is refused unless the install allows it (D109).
func (a *App) readImageSource(ctx context.Context, src *imageSource) ([]byte, error) {
	var r io.Reader
	if src.file != "" {
		f, err := os.Open(src.file)
		if err != nil {
			return nil, NotFoundf("no such media")
		}
		defer f.Close()
		r = f
	} else {
		ctx, cancel := context.WithTimeout(ctx, imageFetchTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, src.url, nil)
		if err != nil {
			return nil, NotFoundf("no such picture")
		}
		// Ask for what can be decoded: a CDN that negotiates would otherwise
		// answer WebP to the same address.
		req.Header.Set("Accept", "image/jpeg,image/png;q=0.9,*/*;q=0.1")
		res, err := a.OutboundClient(imageFetchTimeout).Do(req)
		if err != nil {
			if errors.Is(err, ErrNotPublic) {
				return nil, Forbiddenf("%v", err)
			}
			return nil, &APIError{Status: http.StatusBadGateway, Code: "image_unreachable", Message: "the original could not be fetched"}
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return nil, &APIError{Status: http.StatusBadGateway, Code: "image_unreachable",
				Message: fmt.Sprintf("the original answered %d", res.StatusCode)}
		}
		r = res.Body
	}
	body, err := io.ReadAll(io.LimitReader(r, maxImageSourceBytes+1))
	if err != nil {
		return nil, &APIError{Status: http.StatusBadGateway, Code: "image_unreachable", Message: "the original could not be read"}
	}
	if len(body) > maxImageSourceBytes {
		return nil, errImageTooLarge
	}
	return body, nil
}

// resizeImage scales a JPEG or PNG to width, keeping its aspect ratio. One
// already that narrow comes back unchanged. A picture with transparency stays
// PNG; everything else becomes a JPEG.
func resizeImage(body []byte, width int) ([]byte, error) {
	kind := http.DetectContentType(body)
	if kind != "image/jpeg" && kind != "image/png" {
		return nil, errUnsupportedImage{kind}
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, Validationf("the original is not a readable picture")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > maxImagePixels {
		return nil, errImageTooLarge
	}
	if cfg.Width <= width {
		return body, nil
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, Validationf("the original is not a readable picture")
	}
	height := max(1, int(math.Round(float64(cfg.Height)*float64(width)/float64(cfg.Width))))
	scaled := scaleBox(img, width, height)

	var out bytes.Buffer
	if kind == "image/png" && !scaled.Opaque() {
		err = png.Encode(&out, scaled)
	} else {
		err = jpeg.Encode(&out, scaled, &jpeg.Options{Quality: imageJPEGQuality})
	}
	if err != nil {
		return nil, Internalf(err, "encode the picture")
	}
	return out.Bytes(), nil
}

// scaleBox shrinks src to w×h by averaging the source pixels each target pixel
// covers, fractions at the edges included. For shrinking — the only direction
// this goes — that is as sharp as a bicubic filter without its ringing, and it
// needs nothing outside the standard library (D139). Premultiplied RGBA
// averages correctly across transparent edges.
func scaleBox(src image.Image, w, h int) *image.RGBA {
	b := src.Bounds()
	in := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(in, in.Bounds(), src, b.Min, draw.Src)
	sw, sh := b.Dx(), b.Dy()

	// Horizontal pass into a float buffer of w × sh, then vertical into w × h.
	xs := boxWeights(sw, w)
	mid := make([]float32, w*sh*4)
	for y := 0; y < sh; y++ {
		row := in.Pix[y*in.Stride:]
		for x, taps := range xs {
			var acc [4]float32
			for _, t := range taps {
				p := row[t.i*4 : t.i*4+4]
				for c := range acc {
					acc[c] += float32(p[c]) * t.w
				}
			}
			copy(mid[(y*w+x)*4:], acc[:])
		}
	}
	ys := boxWeights(sh, h)
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	for y, taps := range ys {
		for x := 0; x < w; x++ {
			var acc [4]float32
			for _, t := range taps {
				p := mid[(t.i*w+x)*4:]
				for c := range acc {
					acc[c] += p[c] * t.w
				}
			}
			o := out.Pix[y*out.Stride+x*4:]
			for c := range acc {
				o[c] = uint8(min(255, max(0, acc[c]+0.5)))
			}
		}
	}
	return out
}

type tap struct {
	i int
	w float32
}

// boxWeights says, for each of n target pixels, which of the src source pixels
// it covers and by how much, the weights of each summing to one.
func boxWeights(src, n int) [][]tap {
	scale := float64(src) / float64(n)
	out := make([][]tap, n)
	for d := range out {
		lo, hi := float64(d)*scale, float64(d+1)*scale
		for s := int(lo); s < src && float64(s) < hi; s++ {
			cover := math.Min(hi, float64(s+1)) - math.Max(lo, float64(s))
			if cover > 0 {
				out[d] = append(out[d], tap{s, float32(cover / scale)})
			}
		}
	}
	return out
}

func serveCachedImage(w http.ResponseWriter, r *http.Request, file string) {
	f, err := os.Open(file)
	if err != nil {
		RespondError(w, r, NotFoundf("no such picture"))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		RespondError(w, r, NotFoundf("no such picture"))
		return
	}
	var head [512]byte
	n, _ := io.ReadFull(f, head[:])
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		RespondError(w, r, Internalf(err, "read the picture"))
		return
	}
	// The file's name is a hash of the store, the original and the width, and
	// a rendition never changes, so the name is the ETag.
	w.Header().Set("ETag", `"`+filepath.Base(file)+`"`)
	w.Header().Set("Cache-Control", imageCacheControl)
	w.Header().Set("Content-Type", http.DetectContentType(head[:n]))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, "", info.ModTime(), f)
}
