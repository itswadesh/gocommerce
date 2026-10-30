package customizer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	maxUploadBytes  = 15 << 20
	maxUploadPixels = 40_000_000
	uploadQuality   = 92
	// Each decode holds up to four bytes a pixel at maxUploadPixels.
	decodeSlots = 2
)

// The JPEG and EXIF markers jpegOrientation walks.
const (
	jpegMarker      = 0xFF
	jpegStart       = 0xD8
	jpegApp1        = 0xE1
	jpegScan        = 0xDA
	jpegEnd         = 0xD9
	exifOrientation = 0x0112
)

// Upload is a stored file as the storefront refers to it.
type Upload struct {
	ID     string `json:"upload_id"`
	URL    string `json:"url"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

var (
	errNoUploads = &gocommerce.APIError{Status: http.StatusNotImplemented, Code: "media_store_unconfigured",
		Message: "this store has nowhere to keep uploads"}
	errTooBig = &gocommerce.APIError{Status: http.StatusRequestEntityTooLarge, Code: "file_too_large",
		Message: fmt.Sprintf("a photo can be at most %d MB", maxUploadBytes>>20)}
	errNotAPicture = &gocommerce.APIError{Status: http.StatusUnsupportedMediaType, Code: "unsupported_image",
		Message: "send a JPEG or a PNG; the editor converts other photos before uploading them"}
	errTooManyPixels = &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "image_too_large",
		Message: fmt.Sprintf("a photo can be at most %d megapixels", maxUploadPixels/1_000_000)}
)

func (m *Module) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !m.enabled(r.Context()) {
		gocommerce.RespondError(w, r, errNotMadeToOrder)
		return
	}
	if !m.allow(w, r, m.uploads, "uploads") {
		return
	}
	body, err := readFilePart(w, r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	up, err := m.Store(r.Context(), body)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, up)
}

func readFilePart(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return nil, gocommerce.Validationf("send the photo as multipart/form-data in a field called file")
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return nil, gocommerce.Validationf("send the photo in a field called file")
		}
		if err != nil {
			return nil, tooBigOr(err, gocommerce.Validationf("the upload could not be read"))
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		return readPart(part)
	}
}

func readPart(part *multipart.Part) ([]byte, error) {
	defer part.Close()
	body, err := io.ReadAll(io.LimitReader(part, maxUploadBytes+1))
	if err != nil {
		return nil, tooBigOr(err, gocommerce.Validationf("the upload could not be read"))
	}
	if len(body) > maxUploadBytes {
		return nil, errTooBig
	}
	return body, nil
}

func tooBigOr(err error, otherwise error) error {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return errTooBig
	}
	return otherwise
}

// Store decodes a photo, turns it the way its camera said it was held, and
// writes it again, which leaves behind every byte that was not the picture:
// EXIF and its GPS position, comments, and anything appended to the file.
// Only JPEG and PNG are taken, because those are what the standard library
// decodes (rule 1); the storefront's editor converts a WebP or HEIC photo in
// the browser before sending it.
func (m *Module) Store(ctx context.Context, body []byte) (*Upload, error) {
	store := m.app.MediaStore()
	if store == nil {
		return nil, errNoUploads
	}
	kind := http.DetectContentType(body)
	if kind != "image/jpeg" && kind != "image/png" {
		return nil, errNotAPicture
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, errNotAPicture
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > maxUploadPixels {
		return nil, errTooManyPixels
	}

	select {
	case m.decodes <- struct{}{}:
		defer func() { <-m.decodes }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, errNotAPicture
	}
	var out bytes.Buffer
	ext, mime := ".png", "image/png"
	if kind == "image/jpeg" {
		img = orient(img, jpegOrientation(body))
		ext, mime = ".jpg", "image/jpeg"
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: uploadQuality})
	} else {
		err = png.Encode(&out, img)
	}
	if err != nil {
		return nil, gocommerce.Internalf(err, "encode the photo")
	}
	b := img.Bounds()

	id, err := randomID()
	if err != nil {
		return nil, gocommerce.Internalf(err, "name the upload")
	}
	size := int64(out.Len())
	key, url, err := store.Put(ctx, "upload"+ext, mime, &out)
	if err != nil {
		return nil, gocommerce.Internalf(err, "store the upload")
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO customizer_uploads (id, storage_key, url, mime, width, height, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, key, url, mime, b.Dx(), b.Dy(), size); err != nil {
		_ = store.Delete(ctx, key)
		return nil, gocommerce.Internalf(err, "record the upload")
	}
	return &Upload{ID: id, URL: url, Width: b.Dx(), Height: b.Dy()}, nil
}

func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func validID(s string) bool {
	if len(s) != 32 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// jpegOrientation reads the EXIF orientation of a JPEG, 1 (as stored) when it
// has none. A phone saves a photo the way the sensor sits and records how it
// was held; throwing the EXIF away without applying it would turn every
// portrait photo on its side.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != jpegMarker || b[1] != jpegStart {
		return 1
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != jpegMarker {
			return 1
		}
		marker := b[i+1]
		if marker == jpegScan || marker == jpegEnd {
			return 1
		}
		size := int(binary.BigEndian.Uint16(b[i+2:]))
		if size < 2 || i+2+size > len(b) {
			return 1
		}
		seg := b[i+4 : i+2+size]
		if marker == jpegApp1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return tiffOrientation(seg[6:])
		}
		i += 2 + size
	}
	return 1
}

func tiffOrientation(t []byte) int {
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := int(order.Uint32(t[4:8]))
	if ifd < 8 || ifd+2 > len(t) {
		return 1
	}
	n := int(order.Uint16(t[ifd:]))
	for e := ifd + 2; e+12 <= len(t) && n > 0; e, n = e+12, n-1 {
		if order.Uint16(t[e:]) == exifOrientation {
			if v := int(order.Uint16(t[e+8:])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

func orient(src image.Image, o int) image.Image {
	if o < 2 || o > 8 {
		return src
	}
	b := src.Bounds()
	in := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(in, in.Bounds(), src, b.Min, draw.Src)
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			copy(out.Pix[dy*out.Stride+dx*4:dy*out.Stride+dx*4+4], in.Pix[y*in.Stride+x*4:y*in.Stride+x*4+4])
		}
	}
	return out
}

func (m *Module) allow(w http.ResponseWriter, r *http.Request, t *throttle, what string) bool {
	store, _ := gocommerce.StoreFrom(r.Context())
	wait, ok := t.allow(store+" "+peerAddr(r), time.Now())
	if ok {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(int((wait+time.Second-1)/time.Second)))
	gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusTooManyRequests,
		Code: "too_many_attempts", Message: "too many " + what + " from this address; try again later"})
	return false
}

type throttle struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	seen   map[string]*window
	swept  time.Time
}

type window struct {
	start time.Time
	count int
}

func newThrottle(limit int, per time.Duration) *throttle {
	return &throttle{limit: limit, window: per, seen: map[string]*window{}}
}

func (t *throttle) allow(key string, now time.Time) (time.Duration, bool) {
	if t.limit < 0 {
		return 0, true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if now.Sub(t.swept) >= t.window {
		for k, w := range t.seen {
			if now.Sub(w.start) >= t.window {
				delete(t.seen, k)
			}
		}
		t.swept = now
	}
	w := t.seen[key]
	if w == nil || now.Sub(w.start) >= t.window {
		t.seen[key] = &window{start: now, count: 1}
		return 0, true
	}
	if w.count >= t.limit {
		return w.start.Add(t.window).Sub(now), false
	}
	w.count++
	return 0, true
}

// peerAddr is the connection's own address, never X-Forwarded-For: behind no
// proxy that header is whatever the client wrote.
func peerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
