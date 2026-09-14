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
	"math"
	"net/http"
	"strconv"
	"strings"

	// Decoders register themselves; Amazon serves JPEG, and a PNG or GIF from
	// a seller's own upload is not unheard of.
	_ "image/gif"
	_ "image/png"
)

// The picture step: fetch each image, remake it, re-encode it, hand the bytes
// to the media library.
//
// "Remake" is more than a mirror. A marketplace product photo is a subject on
// a plain background, and the two are treated separately: the background is
// found by flood-filling in from the borders across the near-uniform edge
// colour, and repainted — a soft gradient, or a colour the store chose — with
// a shadow under the subject so the result reads as photographed rather than
// cut out; the subject is scaled a little for margin, tilted if asked, and its
// tone moved — warmth, saturation, lighting. A photo with no plain background
// (a lifestyle shot) is recognised by its uneven border and gets the tone
// changes only, rather than a half-painted patch.
//
// Everything is the standard library's image packages. The scaling and the
// tilt are a bilinear resampler written here, because that is forty lines and
// the alternative is a dependency the ext/ rule forbids.
//
// One thing said plainly, because it should not be discovered later: none of
// this changes whose picture it is. A recomposed photograph is the same
// photograph under copyright; the changes exist so a store's pages are not
// pixel-identical to the marketplace's, not to make the images the store's
// own.

// imageOptions is what to do to every picture in one import. Every field is
// optional; the zero value of each means "leave that alone".
type imageOptions struct {
	// Brightness is added, as a fraction of full scale: 0.06 lifts every
	// channel by about 15 of 255.
	Brightness float64 `json:"brightness"`
	// Contrast scales distance from mid-grey: 1.05 spreads it by five per
	// cent; 0 or 1 leaves it alone.
	Contrast float64 `json:"contrast"`
	// Warmth shifts the colour balance: positive towards amber, negative
	// towards blue, on a scale of -1 to 1 where 0.06 is noticeable and 0.2
	// is a filter.
	Warmth float64 `json:"warmth"`
	// Saturation scales colour away from grey: 1.05 is a little richer; 0 or
	// 1 leaves it alone.
	Saturation float64 `json:"saturation"`
	// Background is what happens to a plain background: "keep", "color"
	// (BackgroundColor everywhere) or "gradient" (BackgroundColor at the top
	// shading to BackgroundTo at the bottom). Empty means keep.
	Background      string `json:"background"`
	BackgroundColor string `json:"background_color"`
	BackgroundTo    string `json:"background_to"`
	// Scale shrinks the subject about the centre, so a new background shows
	// around it: 0.92 leaves a four per cent margin each side. 0 or 1 leaves
	// it alone.
	Scale float64 `json:"scale"`
	// Tilt turns the subject by a few degrees, clockwise positive.
	Tilt float64 `json:"tilt"`
	// Shadow paints a soft shadow under the subject onto a new background.
	Shadow bool `json:"shadow"`
	// Flip is "horizontal", "vertical" or "" — and note that a horizontal flip
	// reverses any text in the picture.
	Flip string `json:"flip"`
	// Rotate is 0, 90, 180 or 270, clockwise, applied to the whole picture.
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
	switch o.Background {
	case "", "keep", "color", "gradient":
	default:
		return fmt.Errorf("background must be keep, color or gradient, not %q", o.Background)
	}
	if o.Brightness < -1 || o.Brightness > 1 {
		return fmt.Errorf("brightness must be between -1 and 1, not %v", o.Brightness)
	}
	if o.Contrast < 0 || o.Contrast > 4 {
		return fmt.Errorf("contrast must be between 0 and 4, not %v", o.Contrast)
	}
	if o.Warmth < -1 || o.Warmth > 1 {
		return fmt.Errorf("warmth must be between -1 and 1, not %v", o.Warmth)
	}
	if o.Saturation < 0 || o.Saturation > 3 {
		return fmt.Errorf("saturation must be between 0 and 3, not %v", o.Saturation)
	}
	if o.Scale != 0 && (o.Scale < 0.5 || o.Scale > 1) {
		return fmt.Errorf("scale must be between 0.5 and 1, not %v", o.Scale)
	}
	if o.Tilt < -15 || o.Tilt > 15 {
		return fmt.Errorf("tilt must be between -15 and 15 degrees, not %v", o.Tilt)
	}
	for _, hex := range []string{o.BackgroundColor, o.BackgroundTo} {
		if hex != "" {
			if _, err := parseHex(hex); err != nil {
				return err
			}
		}
	}
	return nil
}

func (o imageOptions) replacesBackground() bool {
	return o.Background == "color" || o.Background == "gradient"
}

func (o imageOptions) changesTone() bool {
	return o.Brightness != 0 || (o.Contrast != 0 && o.Contrast != 1) ||
		o.Warmth != 0 || (o.Saturation != 0 && o.Saturation != 1)
}

func (o imageOptions) changesGeometry() bool {
	return (o.Scale != 0 && o.Scale != 1) || o.Tilt != 0
}

// noop reports whether the options would leave a picture byte-for-byte alone,
// in which case re-encoding it would only lose JPEG quality for nothing.
func (o imageOptions) noop() bool {
	return !o.changesTone() && !o.replacesBackground() && !o.changesGeometry() &&
		(o.Flip == "" || o.Flip == "none") && o.Rotate == 0
}

// treatment is what adjust decided a picture could take.
type treatment int

const (
	// treatedFully: the background was cut away and replaced, and the subject
	// moved as asked.
	treatedFully treatment = iota
	// treatedAround: the subject could not be separated from its background
	// — a white product on white — so the background was blended toward the
	// new one only around the picture's edges, and the subject was not
	// moved. Nothing in the middle of the picture changed.
	treatedAround
	// treatedToneOnly: no plain background to replace at all.
	treatedToneOnly
)

// processImage applies the options and returns a JPEG, and says how far the
// options could be applied to this particular picture.
func processImage(data []byte, o imageOptions) (out []byte, width, height int, how treatment, err error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, 0, 0, treatedToneOnly, fmt.Errorf("decode: %w", err)
	}
	adjusted, how := adjust(img, o)
	var buf bytes.Buffer
	// 90: visibly lossless for a product photo, a third the size of 100.
	if err := jpeg.Encode(&buf, adjusted, &jpeg.Options{Quality: 90}); err != nil {
		return nil, 0, 0, how, fmt.Errorf("encode: %w", err)
	}
	b := adjusted.Bounds()
	return buf.Bytes(), b.Dx(), b.Dy(), how, nil
}

// adjust is the whole pipeline, in the order that gives each step honest
// input: tone first on the untouched pixels, then the subject is separated
// from a plain background, moved, and composed onto the new one, and only
// then is the whole picture flipped or turned.
func adjust(img image.Image, o imageOptions) (*image.RGBA, treatment) {
	src := toRGBA(img)
	if o.changesTone() {
		src = retone(src, o)
	}
	how := treatedToneOnly

	if o.replacesBackground() || o.changesGeometry() {
		b := src.Bounds()
		w, h := b.Dx(), b.Dy()
		edge, plain := borderColour(src)
		if plain {
			mask := subjectMask(src, edge)
			if leaked(mask, w, h) {
				// The flood reached inside the product: it is the colour of
				// its own background — a white shirt on white — and no cut
				// along colour can separate them. So nothing is cut. The
				// background is blended toward the new one only out at the
				// edges, well away from the product, and the product is not
				// moved: a tilt would swing the white it sits on with it.
				how = treatedAround
				if o.replacesBackground() {
					src = vignette(src, mask, background(w, h, o), w, h)
				}
			} else {
				how = treatedFully
				feather(mask, w, h)
				if o.changesGeometry() {
					src, mask = transform(src, mask, o.Scale, o.Tilt)
				}
				if o.replacesBackground() {
					bg := background(w, h, o)
					if o.Shadow {
						dropShadow(bg, mask, w, h)
					}
					src = composite(bg, src, mask)
				}
			}
		}
		// With no plain background there is nothing to fill a margin with,
		// so a picture that is not plain keeps its composition as well.
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
	return src, how
}

// leaked says whether the flood got inside the subject. It measures how much
// "background" lies within the box the subject spans: a solid object on a
// plain ground leaves only its own corners uncovered, while a white shirt on
// white leaves the whole torso — the head, the hands and the shorts survive
// as islands and the box between them is hollow.
func leaked(mask []float32, w, h int) bool {
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if mask[y*w+x] > 0.5 {
				minX, maxX = min(minX, x), max(maxX, x)
				minY, maxY = min(minY, y), max(maxY, y)
			}
		}
	}
	if maxX < 0 {
		return true // nothing survived: the whole picture was the edge colour
	}
	var hollow, area int
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			area++
			if mask[y*w+x] <= 0.5 {
				hollow++
			}
		}
	}
	// A round object leaves a fifth of its box uncovered; a hollow half is a
	// product the flood went through.
	return hollow*10 > area*4
}

// vignette blends the picture toward the new background out at its edges,
// far from the subject, and leaves the box the subject spans — widened by a
// margin and softened over a broad band — exactly as it was.
func vignette(src *image.RGBA, mask []float32, bg *image.RGBA, w, h int) *image.RGBA {
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if mask[y*w+x] > 0.5 {
				minX, maxX = min(minX, x), max(maxX, x)
				minY, maxY = min(minY, y), max(maxY, y)
			}
		}
	}
	marginX, marginY := w/12, h/12
	keep := make([]float32, w*h)
	for y := max(0, minY-marginY); y <= min(h-1, maxY+marginY); y++ {
		for x := max(0, minX-marginX); x <= min(w-1, maxX+marginX); x++ {
			keep[y*w+x] = 1
		}
	}
	radius := max(4, min(w, h)/10)
	boxBlur(keep, w, h, radius)
	boxBlur(keep, w, h, radius)
	boxBlur(keep, w, h, radius)
	// The blur pulled the box's edge down to a half; anything the box covered
	// is held at one, so the product's own surroundings do not shift.
	for y := max(0, minY-marginY); y <= min(h-1, maxY+marginY); y++ {
		for x := max(0, minX-marginX); x <= min(w-1, maxX+marginX); x++ {
			keep[y*w+x] = 1
		}
	}
	return composite(bg, src, keep)
}

func toRGBA(img image.Image) *image.RGBA {
	if rgba, ok := img.(*image.RGBA); ok && rgba.Bounds().Min == (image.Point{}) {
		return rgba
	}
	b := img.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(out, out.Bounds(), img, b.Min, draw.Src)
	return out
}

// ------------------------------------------------------------------- tone

// retone applies lighting, warmth and saturation per pixel. Alpha is left
// alone: tone is not transparency.
func retone(src *image.RGBA, o imageOptions) *image.RGBA {
	contrast := o.Contrast
	if contrast == 0 {
		contrast = 1
	}
	sat := o.Saturation
	if sat == 0 {
		sat = 1
	}
	offset := o.Brightness * 255
	// Warmth as channel gains: amber lifts red and drops blue, and 0.06 is a
	// shift a viewer feels rather than sees.
	rGain, bGain := 1+0.18*o.Warmth, 1-0.18*o.Warmth

	out := image.NewRGBA(src.Bounds())
	pix, dst := src.Pix, out.Pix
	for i := 0; i+3 < len(pix); i += 4 {
		r, g, b := float64(pix[i]), float64(pix[i+1]), float64(pix[i+2])
		if sat != 1 {
			l := 0.299*r + 0.587*g + 0.114*b
			r, g, b = l+(r-l)*sat, l+(g-l)*sat, l+(b-l)*sat
		}
		r *= rGain
		b *= bGain
		r = (r-128)*contrast + 128 + offset
		g = (g-128)*contrast + 128 + offset
		b = (b-128)*contrast + 128 + offset
		dst[i] = clamp8(r)
		dst[i+1] = clamp8(g)
		dst[i+2] = clamp8(b)
		dst[i+3] = pix[i+3]
	}
	return out
}

func clamp8(v float64) uint8 {
	return uint8(min(255, max(0, v+0.5)))
}

// ------------------------------------------------------------- background

// borderTolerance is how far a pixel may sit from the edge colour and still
// be background — wide enough for JPEG ringing round a white product shot,
// narrow enough that a pale product is not painted over.
const borderTolerance = 28

// borderColour samples the picture's edge and says whether it is plain: the
// mean of the border pixels, and whether nearly all of them sit close to it.
// A lifestyle photograph fails the second test, and that is how it is told
// apart from a product on white without asking anybody.
func borderColour(src *image.RGBA) (color.RGBA, bool) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	var sr, sg, sb, n int
	visit := func(x, y int) {
		c := src.RGBAAt(x, y)
		sr, sg, sb = sr+int(c.R), sg+int(c.G), sb+int(c.B)
		n++
	}
	for x := 0; x < w; x++ {
		visit(x, 0)
		visit(x, h-1)
	}
	for y := 1; y < h-1; y++ {
		visit(0, y)
		visit(w-1, y)
	}
	if n == 0 {
		return color.RGBA{}, false
	}
	mean := color.RGBA{uint8(sr / n), uint8(sg / n), uint8(sb / n), 255}

	var near int
	check := func(x, y int) {
		if closeTo(src.RGBAAt(x, y), mean, borderTolerance) {
			near++
		}
	}
	for x := 0; x < w; x++ {
		check(x, 0)
		check(x, h-1)
	}
	for y := 1; y < h-1; y++ {
		check(0, y)
		check(w-1, y)
	}
	// Nine in ten: a product that touches one edge is still on a plain
	// background; a room, a table and a window are not.
	return mean, near*10 >= n*9
}

func closeTo(c, ref color.RGBA, tol int) bool {
	d := func(a, b uint8) int {
		if a > b {
			return int(a - b)
		}
		return int(b - a)
	}
	return d(c.R, ref.R) <= tol && d(c.G, ref.G) <= tol && d(c.B, ref.B) <= tol
}

// subjectMask is 1 where the subject is and 0 where the background is, found
// by flooding in from every border pixel across colours close to the edge's.
// Flooding, rather than testing every pixel against the colour, is what keeps
// a white label on a dark product from being cut out: it is not connected to
// the edge.
func subjectMask(src *image.RGBA, edge color.RGBA) []float32 {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	mask := make([]float32, w*h)
	for i := range mask {
		mask[i] = 1
	}
	queue := make([]int, 0, 2*(w+h))
	push := func(x, y int) {
		i := y*w + x
		if mask[i] == 0 || !closeTo(src.RGBAAt(x, y), edge, borderTolerance) {
			return
		}
		mask[i] = 0
		queue = append(queue, i)
	}
	for x := 0; x < w; x++ {
		push(x, 0)
		push(x, h-1)
	}
	for y := 0; y < h; y++ {
		push(0, y)
		push(w-1, y)
	}
	for len(queue) > 0 {
		i := queue[0]
		queue = queue[1:]
		x, y := i%w, i/w
		if x > 0 {
			push(x-1, y)
		}
		if x < w-1 {
			push(x+1, y)
		}
		if y > 0 {
			push(x, y-1)
		}
		if y < h-1 {
			push(x, y+1)
		}
	}
	return mask
}

// feather softens the mask's edge by a couple of pixels, so the subject's
// outline is blended onto the new background rather than cut with scissors.
func feather(mask []float32, w, h int) {
	boxBlur(mask, w, h, 1)
	boxBlur(mask, w, h, 1)
}

// boxBlur is a separable running-sum blur of the given radius — three of them
// are a gaussian to the eye, and each is linear in the number of pixels.
func boxBlur(a []float32, w, h, r int) {
	tmp := make([]float32, len(a))
	span := float32(2*r + 1)
	for y := 0; y < h; y++ {
		row := a[y*w : (y+1)*w]
		var sum float32
		for x := -r; x <= r; x++ {
			sum += row[clampi(x, 0, w-1)]
		}
		for x := 0; x < w; x++ {
			tmp[y*w+x] = sum / span
			sum += row[clampi(x+r+1, 0, w-1)] - row[clampi(x-r, 0, w-1)]
		}
	}
	for x := 0; x < w; x++ {
		var sum float32
		for y := -r; y <= r; y++ {
			sum += tmp[clampi(y, 0, h-1)*w+x]
		}
		for y := 0; y < h; y++ {
			a[y*w+x] = sum / span
			sum += tmp[clampi(y+r+1, 0, h-1)*w+x] - tmp[clampi(y-r, 0, h-1)*w+x]
		}
	}
}

func clampi(v, lo, hi int) int {
	return min(hi, max(lo, v))
}

// transform scales the subject about the centre and tilts it, on a canvas of
// the same size, by bilinear sampling of both the pixels and the mask.
// Outside the source the mask is 0 and the colour does not matter, because
// the background is painted under it afterwards.
func transform(src *image.RGBA, mask []float32, scale, tilt float64) (*image.RGBA, []float32) {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if scale == 0 {
		scale = 1
	}
	theta := -tilt * math.Pi / 180
	cosT, sinT := math.Cos(theta), math.Sin(theta)
	cx, cy := float64(w-1)/2, float64(h-1)/2

	out := image.NewRGBA(image.Rect(0, 0, w, h))
	outMask := make([]float32, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dx, dy := float64(x)-cx, float64(y)-cy
			sx := cx + (dx*cosT-dy*sinT)/scale
			sy := cy + (dx*sinT+dy*cosT)/scale
			if sx < 0 || sy < 0 || sx > float64(w-1) || sy > float64(h-1) {
				continue
			}
			c, m := sample(src, mask, sx, sy, w, h)
			out.SetRGBA(x, y, c)
			outMask[y*w+x] = m
		}
	}
	return out, outMask
}

// sample reads a colour and a mask value at a fractional position, blending
// the four neighbours by distance.
func sample(src *image.RGBA, mask []float32, fx, fy float64, w, h int) (color.RGBA, float32) {
	x0, y0 := int(fx), int(fy)
	x1, y1 := min(x0+1, w-1), min(y0+1, h-1)
	tx, ty := float32(fx-float64(x0)), float32(fy-float64(y0))
	weight := [4]float32{(1 - tx) * (1 - ty), tx * (1 - ty), (1 - tx) * ty, tx * ty}
	at := [4][2]int{{x0, y0}, {x1, y0}, {x0, y1}, {x1, y1}}
	var r, g, b, m float32
	for i, p := range at {
		c := src.RGBAAt(p[0], p[1])
		r += weight[i] * float32(c.R)
		g += weight[i] * float32(c.G)
		b += weight[i] * float32(c.B)
		if mask != nil {
			m += weight[i] * mask[p[1]*w+p[0]]
		} else {
			m += weight[i]
		}
	}
	return color.RGBA{uint8(r + 0.5), uint8(g + 0.5), uint8(b + 0.5), 255}, m
}

// background paints the new ground: one colour, or a vertical gradient from
// BackgroundColor to BackgroundTo.
func background(w, h int, o imageOptions) *image.RGBA {
	top, err := parseHex(o.BackgroundColor)
	if err != nil {
		top = color.RGBA{246, 247, 249, 255}
	}
	bottom := top
	if o.Background == "gradient" {
		if c, err := parseHex(o.BackgroundTo); err == nil {
			bottom = c
		} else {
			bottom = color.RGBA{230, 233, 238, 255}
		}
	}
	bg := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		t := 0.0
		if h > 1 {
			t = float64(y) / float64(h-1)
		}
		c := color.RGBA{
			R: uint8(float64(top.R)*(1-t) + float64(bottom.R)*t + 0.5),
			G: uint8(float64(top.G)*(1-t) + float64(bottom.G)*t + 0.5),
			B: uint8(float64(top.B)*(1-t) + float64(bottom.B)*t + 0.5),
			A: 255,
		}
		for x := 0; x < w; x++ {
			bg.SetRGBA(x, y, c)
		}
	}
	return bg
}

// dropShadow darkens the background under and slightly below the subject: the
// mask shifted down, blurred wide, and multiplied in at low strength.
func dropShadow(bg *image.RGBA, mask []float32, w, h int) {
	offset := max(2, h/50)
	shadow := make([]float32, w*h)
	for y := 0; y+offset < h; y++ {
		copy(shadow[(y+offset)*w:(y+offset+1)*w], mask[y*w:(y+1)*w])
	}
	radius := max(2, w/80)
	boxBlur(shadow, w, h, radius)
	boxBlur(shadow, w, h, radius)
	boxBlur(shadow, w, h, radius)
	for i, s := range shadow {
		if s <= 0.001 {
			continue
		}
		k := 1 - 0.30*float64(s)
		p := i * 4
		bg.Pix[p] = uint8(float64(bg.Pix[p])*k + 0.5)
		bg.Pix[p+1] = uint8(float64(bg.Pix[p+1])*k + 0.5)
		bg.Pix[p+2] = uint8(float64(bg.Pix[p+2])*k + 0.5)
	}
}

// composite lays the subject over the background by the mask.
func composite(bg, src *image.RGBA, mask []float32) *image.RGBA {
	out := image.NewRGBA(bg.Bounds())
	for i := range mask {
		a := float64(mask[i])
		p := i * 4
		for c := 0; c < 3; c++ {
			out.Pix[p+c] = uint8(float64(bg.Pix[p+c])*(1-a) + float64(src.Pix[p+c])*a + 0.5)
		}
		out.Pix[p+3] = 255
	}
	return out
}

func parseHex(s string) (color.RGBA, error) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return color.RGBA{}, fmt.Errorf("%q is not a #RRGGBB colour", s)
	}
	v, err := strconv.ParseUint(s, 16, 32)
	if err != nil {
		return color.RGBA{}, fmt.Errorf("%q is not a #RRGGBB colour", s)
	}
	return color.RGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 255}, nil
}

// ------------------------------------------------------------ orientation

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

// ------------------------------------------------------------------ fetch

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
