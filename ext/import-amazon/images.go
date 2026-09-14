package amazon

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"io"
	"net/http"
	"strings"

	// Decoders register themselves; Amazon serves JPEG, and a PNG or GIF from
	// a seller's own upload is not unheard of.
	_ "image/gif"
	_ "image/png"
)

// The picture step: fetch each image, adjust it, re-encode it, hand the bytes
// to the media library. Everything is the standard library's image packages,
// which is why the operations are the ones they can do exactly — a lighting
// change per pixel and a rotation by right angles — and not a resize, which
// wants an interpolating resampler this repository does not carry.
//
// One thing said plainly, because it should not be discovered later: none of
// this changes whose picture it is. A mirrored, brightened photograph is the
// same photograph under copyright; the adjustments exist so a store's pages
// are not pixel-identical to the marketplace's, not to make the images the
// store's own.

// imageOptions is what to do to every picture in one import.
type imageOptions struct {
	// Brightness is added, as a fraction of full scale: 0.06 lifts every
	// channel by about 15 of 255. Zero leaves it alone.
	Brightness float64 `json:"brightness"`
	// Contrast scales distance from mid-grey: 1.05 spreads it by five per
	// cent, 1 leaves it alone.
	Contrast float64 `json:"contrast"`
	// Flip is "horizontal", "vertical" or "" — and note that a horizontal flip
	// reverses any text in the picture.
	Flip string `json:"flip"`
	// Rotate is 0, 90, 180 or 270, clockwise.
	Rotate int `json:"rotate"`
}

func (o imageOptions) validate() error {
	switch o.Flip {
	case "", "none", "horizontal", "vertical":
	default:
		return fmt.Errorf("flip must be horizontal, vertical or none, not %q", o.Flip)
	}
	switch o.Rotate {
	case 0, 90, 180, 270:
	default:
		return fmt.Errorf("rotate must be 0, 90, 180 or 270, not %d", o.Rotate)
	}
	if o.Brightness < -1 || o.Brightness > 1 {
		return fmt.Errorf("brightness must be between -1 and 1, not %v", o.Brightness)
	}
	if o.Contrast < 0 || o.Contrast > 4 {
		return fmt.Errorf("contrast must be between 0 and 4, not %v", o.Contrast)
	}
	return nil
}

// noop reports whether the options would leave a picture byte-for-byte alone,
// in which case re-encoding it would only lose JPEG quality for nothing.
func (o imageOptions) noop() bool {
	return o.Brightness == 0 && (o.Contrast == 0 || o.Contrast == 1) &&
		(o.Flip == "" || o.Flip == "none") && o.Rotate == 0
}

// processImage applies the options and returns a JPEG.
func processImage(data []byte, o imageOptions) (out []byte, width, height int, err error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, fmt.Errorf("decode: %w", err)
	}
	adjusted := adjust(img, o)
	var buf bytes.Buffer
	// 90: visibly lossless for a product photo, a third the size of 100.
	if err := jpeg.Encode(&buf, adjusted, &jpeg.Options{Quality: 90}); err != nil {
		return nil, 0, 0, fmt.Errorf("encode: %w", err)
	}
	b := adjusted.Bounds()
	return buf.Bytes(), b.Dx(), b.Dy(), nil
}

// adjust applies lighting first and orientation second. The order does not
// change the result — both are per-pixel and geometric respectively — but
// doing the lighting on the original bounds keeps the loop simple.
func adjust(img image.Image, o imageOptions) *image.RGBA {
	src := toRGBA(img)
	if o.Brightness != 0 || (o.Contrast != 0 && o.Contrast != 1) {
		src = relight(src, o.Brightness, o.Contrast)
	}
	switch o.Flip {
	case "horizontal":
		src = flipH(src)
	case "vertical":
		src = flipV(src)
	}
	switch o.Rotate {
	case 90:
		src = rotate90(src)
	case 180:
		src = rotate90(rotate90(src))
	case 270:
		src = rotate90(rotate90(rotate90(src)))
	}
	return src
}

func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok {
		return rgba
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// relight moves every channel by brightness and scales it about mid-grey by
// contrast, clamped. Alpha is left alone: lighting is not transparency.
func relight(src *image.RGBA, brightness, contrast float64) *image.RGBA {
	if contrast == 0 {
		contrast = 1
	}
	offset := brightness * 255
	// A lookup table, because the same 256 answers are asked a million times.
	var table [256]uint8
	for v := range table {
		f := (float64(v)-128)*contrast + 128 + offset
		table[v] = uint8(min(255, max(0, f+0.5)))
	}
	out := image.NewRGBA(src.Bounds())
	pix := src.Pix
	for i := 0; i+3 < len(pix); i += 4 {
		out.Pix[i] = table[pix[i]]
		out.Pix[i+1] = table[pix[i+1]]
		out.Pix[i+2] = table[pix[i+2]]
		out.Pix[i+3] = pix[i+3]
	}
	return out
}

func flipH(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out.SetRGBA(b.Max.X-1-(x-b.Min.X), y, src.RGBAAt(x, y))
		}
	}
	return out
}

func flipV(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			out.SetRGBA(x, b.Max.Y-1-(y-b.Min.Y), src.RGBAAt(x, y))
		}
	}
	return out
}

// rotate90 turns the picture a quarter turn clockwise: the top row becomes
// the right column.
func rotate90(src *image.RGBA) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	out := image.NewRGBA(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			out.SetRGBA(h-1-y, x, src.RGBAAt(b.Min.X+x, b.Min.Y+y))
		}
	}
	return out
}

// fetchImage downloads one picture. Amazon's image host answers a plain GET
// with a browser-ish user agent; it does not sit behind the robot check the
// product pages do, which is why this does not go through Chrome.
func fetchImage(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0 Safari/537.36")
	req.Header.Set("Accept", "image/avif,image/webp,image/apng,image/*,*/*;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s", resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "image/") {
		return nil, errors.New("not an image: " + ct)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 25<<20))
}

// luma is the perceived brightness of a pixel, used by the tests to prove a
// picture got lighter without reading every channel.
func luma(c color.RGBA) float64 {
	return 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
}
