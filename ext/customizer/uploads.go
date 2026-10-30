package customizer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	maxUploadBytes  = 15 << 20
	maxUploadPixels = 40_000_000
	maxUploadSide   = 16384
	// maxScans is the most scans Skia, which decodes JPEGs for Chrome and
	// Android, reads (SkJpegDecoderMgr.cpp); libjpeg writes 6 to 18. The
	// decoder passes over every block of the picture once a scan.
	maxScans      = 99
	uploadQuality = 92
	decodeBudget  = 256 << 20
	decodeSlots   = 1
	// decodeQueue is how many uploads may wait for a turn to decode, each
	// keeping its file on disk while it does.
	decodeQueue  = 8
	keepWithin   = 30 * time.Second
	answerWithin = 10 * time.Second
	// pngWriterState is what png.Encode's compress/flate writer holds
	// whatever the picture: 1.1 MB in Go 1.27.
	pngWriterState            = 1200 << 10
	spoolPrefix               = "customizer-upload-"
	spoolAbandonedAfter       = time.Hour
	minUploadBytesPerSecond   = 8 << 10
	statusClientClosedRequest = 499
)

var (
	// uploadWithin lets the largest file arrive at 0.7 Mbit/s.
	uploadIdle   = 15 * time.Second
	uploadWithin = 3 * time.Minute
	// decodeWait is eight uploads ahead at the second or two a large photo
	// takes, and then some.
	decodeWait = 30 * time.Second
)

const (
	jpegMarker      = 0xFF
	jpegStart       = 0xD8
	jpegEnd         = 0xD9
	jpegRestart0    = 0xD0
	jpegRestart7    = 0xD7
	jpegBaseline    = 0xC0
	jpegExtended    = 0xC1
	jpegProgressive = 0xC2
	jpegHuffman     = 0xC4
	jpegScan        = 0xDA
	jpegQuant       = 0xDB
	jpegInterval    = 0xDD
	jpegApp0        = 0xE0
	jpegApp1        = 0xE1
	jpegApp15       = 0xEF
	jpegComment     = 0xFE
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
		Message: fmt.Sprintf("a photo can be at most %d megapixels and %d pixels a side", maxUploadPixels/1_000_000, maxUploadSide)}
	errTooManyScans = &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "too_many_scans",
		Message: "this photo is saved in more passes than a camera or photo editor writes; save it again as an ordinary JPEG"}
	errTooSlow = &gocommerce.APIError{Status: http.StatusBadRequest, Code: "upload_too_slow",
		Message: "the file took too long to arrive; try again on a steadier connection"}
	errBusy = &gocommerce.APIError{Status: http.StatusServiceUnavailable, Code: "busy",
		Message: "too many photos are being uploaded right now; try again in a few seconds"}
	errClientClosedRequest = &gocommerce.APIError{Status: statusClientClosedRequest, Code: "client_closed_request",
		Message: "the request was closed before the upload was kept"}
)

func (m *Module) handleUpload(w http.ResponseWriter, r *http.Request) {
	if !m.enabled(r.Context()) {
		gocommerce.RespondError(w, r, errNotMadeToOrder)
		return
	}
	if !m.allow(w, r, m.uploads, "uploads") {
		return
	}
	counted := time.Now()
	rc := http.NewResponseController(w)
	body := &arriving{ReadCloser: r.Body, rc: rc, start: time.Now()}
	if err := body.extend(); err != nil && !errors.Is(err, http.ErrNotSupported) {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "time the upload"))
		return
	}
	r.Body = body
	file, err := receive(w, r)
	// The server's WriteTimeout runs from when the request arrived. The
	// upload is kept only while this deadline leaves time to say so.
	keepBy := time.Now().Add(decodeWait + keepWithin)
	_ = rc.SetWriteDeadline(keepBy.Add(answerWithin))
	if err != nil {
		if errors.Is(err, errTooSlow) {
			m.uploads.refund(throttleKey(r), counted)
		}
		gocommerce.RespondError(w, r, err)
		return
	}
	// A read timing out from here would cancel the request waiting to be
	// decoded. On a body that failed the deadline stays, as the server drains
	// the rest under it before answering.
	_ = rc.SetReadDeadline(time.Time{})
	defer discard(file)
	ctx, cancel := context.WithDeadline(r.Context(), keepBy)
	defer cancel()
	up, err := m.keep(ctx, file)
	if errors.Is(err, errBusy) {
		m.uploads.refund(throttleKey(r), counted)
		m.busy(w)
		return
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			err = errClientClosedRequest
		}
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, up)
}

func (m *Module) busy(w http.ResponseWriter) {
	m.turnedAway.Add(1)
	now := time.Now().UnixNano()
	if last := m.warnedAtUnixNano.Load(); now-last >= int64(time.Minute) && m.warnedAtUnixNano.CompareAndSwap(last, now) {
		m.app.Log().Warn("customizer: uploads turned away while the decoder was busy", "uploads", m.turnedAway.Swap(0))
	}
	w.Header().Set("Retry-After", "10")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(errBusy.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": errBusy})
}

type arriving struct {
	io.ReadCloser
	rc    *http.ResponseController
	start time.Time
	n     int64
}

func (a *arriving) Read(p []byte) (int, error) {
	n, err := a.ReadCloser.Read(p)
	if n > 0 {
		a.n += int64(n)
		_ = a.extend()
	}
	return n, err
}

func (a *arriving) extend() error {
	return a.rc.SetReadDeadline(readDeadline(a.start, time.Now(), a.n))
}

func readDeadline(start, now time.Time, n int64) time.Time {
	idle := now.Sub(start) + uploadIdle
	fallenBehind := uploadIdle + time.Duration(n)*time.Second/minUploadBytesPerSecond
	return start.Add(min(idle, fallenBehind, uploadWithin))
}

func receive(w http.ResponseWriter, r *http.Request) (*os.File, error) {
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
			return nil, unreadable(err)
		}
		if part.FormName() != "file" {
			part.Close()
			continue
		}
		f, err := spool(part)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			discard(f)
			return nil, unreadable(err)
		}
		return f, nil
	}
}

func spool(part *multipart.Part) (*os.File, error) {
	defer part.Close()
	f, err := os.CreateTemp("", spoolPrefix+"*")
	if err != nil {
		return nil, gocommerce.Internalf(err, "hold the upload")
	}
	n, err := io.Copy(f, io.LimitReader(part, maxUploadBytes+1))
	if err != nil || n > maxUploadBytes {
		discard(f)
		if err == nil {
			return nil, errTooBig
		}
		return nil, unreadable(err)
	}
	return f, nil
}

func sweepSpool() (removed int) {
	dir := os.TempDir()
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		info, err := f.Info()
		if err == nil && strings.HasPrefix(f.Name(), spoolPrefix) && time.Since(info.ModTime()) > spoolAbandonedAfter &&
			os.Remove(filepath.Join(dir, f.Name())) == nil {
			removed++
		}
	}
	return removed
}

func discard(f *os.File) {
	_ = f.Close()
	_ = os.Remove(f.Name())
}

func unreadable(err error) error {
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxErr):
		return errTooBig
	case errors.Is(err, os.ErrDeadlineExceeded):
		return errTooSlow
	}
	return gocommerce.Validationf("the upload could not be read")
}

func (m *Module) keep(ctx context.Context, file *os.File) (*Upload, error) {
	store := m.app.MediaStore()
	if store == nil {
		return nil, errNoUploads
	}
	size, mime, bounds, err := m.rewriteInTurn(ctx, file)
	if err != nil {
		return nil, err
	}

	id, err := randomID()
	if err != nil {
		return nil, gocommerce.Internalf(err, "name the upload")
	}
	ext := ".png"
	if mime == "image/jpeg" {
		ext = ".jpg"
	}
	key, url, err := store.Put(ctx, "upload"+ext, mime, io.NewSectionReader(file, 0, size))
	if err != nil {
		return nil, gocommerce.Internalf(err, "store the upload")
	}
	if _, err := m.db.ExecContext(ctx, `
		INSERT INTO customizer_uploads (id, storage_key, url, mime, width, height, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, key, url, mime, bounds.Dx(), bounds.Dy(), size); err != nil {
		_ = store.Delete(context.WithoutCancel(ctx), key)
		return nil, gocommerce.Internalf(err, "record the upload")
	}
	return &Upload{ID: id, URL: url, Width: bounds.Dx(), Height: bounds.Dy()}, nil
}

// rewriteInTurn rewrites file once no other upload is decoding, so the
// pictures in memory at once stay within decodeSlots × decodeBudget.
func (m *Module) rewriteInTurn(ctx context.Context, file *os.File) (int64, string, image.Rectangle, error) {
	if err := m.awaitTurn(ctx); err != nil {
		return 0, "", image.Rectangle{}, err
	}
	defer func() { <-m.decodes }()
	return rewrite(file)
}

func (m *Module) awaitTurn(ctx context.Context) error {
	select {
	case m.waiting <- struct{}{}:
	default:
		return errBusy
	}
	defer func() { <-m.waiting }()
	wait := time.NewTimer(decodeWait)
	defer wait.Stop()
	select {
	case m.decodes <- struct{}{}:
		return nil
	case <-wait.C:
		return errBusy
	case <-ctx.Done():
		return ctx.Err()
	}
}

func rewrite(file *os.File) (size int64, mime string, bounds image.Rectangle, err error) {
	body, err := os.ReadFile(file.Name())
	if err == nil {
		_, err = file.Seek(0, io.SeekStart)
	}
	if err == nil {
		err = file.Truncate(0)
	}
	if err != nil {
		return 0, "", bounds, gocommerce.Internalf(err, "read the upload back")
	}
	if mime, bounds, err = redraw(body, file); err != nil {
		return 0, "", bounds, err
	}
	if size, err = file.Seek(0, io.SeekCurrent); err != nil {
		return 0, "", bounds, gocommerce.Internalf(err, "write the photo again")
	}
	return size, mime, bounds, nil
}

func redraw(body []byte, out io.Writer) (mime string, bounds image.Rectangle, err error) {
	mime = http.DetectContentType(body)
	if mime != "image/jpeg" && mime != "image/png" {
		return "", bounds, errNotAPicture
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return "", bounds, errNotAPicture
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxUploadSide || cfg.Height > maxUploadSide ||
		int64(cfg.Width)*int64(cfg.Height) > maxUploadPixels {
		return "", bounds, errTooManyPixels
	}
	orientation := 1
	if mime == "image/jpeg" {
		var f jpegHead
		if f, orientation = jpegHeader(body); f.scans > f.scanLimit() {
			return "", bounds, errTooManyScans
		}
	}
	if cost, advice := decodeCost(body, mime, cfg, orientation); cost > decodeBudget {
		return "", bounds, &gocommerce.APIError{Status: http.StatusUnprocessableEntity, Code: "image_too_large", Message: advice}
	}

	img, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return "", bounds, errNotAPicture
	}
	img = encodable(img)
	if mime == "image/jpeg" {
		img = orient(img, orientation)
		err = jpeg.Encode(out, img, &jpeg.Options{Quality: uploadQuality})
	} else {
		err = png.Encode(out, img)
	}
	if err != nil {
		return "", bounds, gocommerce.Internalf(err, "write the photo again")
	}
	return mime, img.Bounds(), nil
}

// image/png's and image/jpeg's encoders (Go 1.27, writer.go) read a Gray16,
// RGBA64, NRGBA64 or CMYK picture through an interface, a pixel at a time,
// which allocates for every pixel.
func encodable(img image.Image) image.Image {
	switch m := img.(type) {
	case *image.Gray16:
		return &image.Gray{Pix: highBytes(m.Pix), Stride: m.Stride / 2, Rect: m.Rect}
	case *image.RGBA64:
		return &image.RGBA{Pix: highBytes(m.Pix), Stride: m.Stride / 2, Rect: m.Rect}
	case *image.NRGBA64:
		return &image.NRGBA{Pix: highBytes(m.Pix), Stride: m.Stride / 2, Rect: m.Rect}
	case *image.CMYK:
		for i := 0; i+4 <= len(m.Pix); i += 4 {
			m.Pix[i], m.Pix[i+1], m.Pix[i+2] = color.CMYKToRGB(m.Pix[i], m.Pix[i+1], m.Pix[i+2], m.Pix[i+3])
			m.Pix[i+3] = 0xFF
		}
		return &image.RGBA{Pix: m.Pix, Stride: m.Stride, Rect: m.Rect}
	}
	return img
}

func highBytes(p []byte) []byte {
	for i := range len(p) / 2 {
		p[i] = p[2*i]
	}
	return p[:len(p)/2]
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

func decodeCost(body []byte, mime string, cfg image.Config, orientation int) (cost int64, advice string) {
	w, h := int64(cfg.Width), int64(cfg.Height)
	if mime == "image/png" {
		advice = "this picture is too large to read; make it smaller"
		if body[24] == 16 {
			advice = "this picture is a 16-bit PNG too large to read; save it as an ordinary 8-bit picture"
		}
		return int64(len(body)) + pngCost(body, w, h), advice
	}
	f, _ := jpegHeader(body)
	if f.scans == 0 || f.width != cfg.Width || f.height != cfg.Height || len(f.components) != components(cfg.ColorModel) {
		f = dearestFrame(cfg)
	}
	cost = int64(len(body)) + jpegCost(f)
	if orientation >= 2 {
		cost += 4 * w * h
	}
	if f.progressive {
		return cost, "this photo is a progressive JPEG too large to read; save it as an ordinary JPEG, or make it smaller"
	}
	return cost, "this photo is too large to read; make it smaller"
}

func dearestFrame(cfg image.Config) jpegHead {
	return jpegHead{progressive: true, width: cfg.Width, height: cfg.Height, components: []sampling{{1, 1}, {1, 1}, {1, 1}, {1, 1}}}
}

// pngCost follows image/png (Go 1.27). The decoder (reader.go
// readImagePass) makes a grey or palette picture a byte a pixel and one with
// colour or transparency four, twice that at 16 bits, and an interlaced one
// decodes each of its seven passes into a picture of its own beside the
// whole. Each pass is read through two rows of the file's own bits, none
// wider than the picture. The encoder (writer.go writeImage) fills six rows
// of the 8-bit picture encodable leaves, beside its compressor's state.
func pngCost(body []byte, w, h int64) int64 {
	depth, colour, interlaced := int64(body[24]), body[25], body[28] == 1
	per := int64(4)
	if colour == 3 || colour == 0 && !pngMayBeTransparent(body) {
		per = 1
	}
	written := pngWriterState + 6*(1+per*w)
	if depth == 16 {
		per *= 2
	}
	picture, passes := per*w*h, int64(1)
	if interlaced {
		picture, passes = 2*picture, 7
	}
	row := 1 + (depth*pngChannels[colour]*w+7)/8
	return picture + 2*passes*row + written
}

var pngChannels = map[byte]int64{0: 1, 2: 3, 3: 1, 4: 2, 6: 4}

func pngMayBeTransparent(b []byte) bool {
	for b = b[8:]; len(b) >= 8; {
		switch string(b[4:8]) {
		case "tRNS":
			return true
		case "IDAT":
			return false
		}
		next := 12 + uint64(binary.BigEndian.Uint32(b))
		if next > uint64(len(b)) {
			break
		}
		b = b[next:]
	}
	return true
}

func components(m color.Model) int {
	switch m {
	case color.GrayModel:
		return 1
	case color.YCbCrModel, color.RGBAModel:
		return 3
	case color.CMYKModel:
		return 4
	}
	return 0
}

type sampling struct{ h, v int }

type jpegHead struct {
	progressive   bool
	width, height int
	components    []sampling
	scans         int
}

// A sequential JPEG has a scan for each component at most, as libjpeg's
// encoder insists (jcmaster.c validate_script), and image/jpeg decodes each
// of its scans whole.
func (f jpegHead) scanLimit() int {
	if f.progressive {
		return maxScans
	}
	return len(f.components)
}

// jpegHeader walks a JPEG's markers the way image/jpeg's decoder does (Go
// 1.27, reader.go decoder.decode): a byte before a marker that is not 0xFF, a
// 0xFF fill byte, "\xff\x00" and a restart marker are skipped without a
// length, and every other segment by its length. The data of a scan is such
// bytes, so the walk goes on to the end, counting the scans. Where it stops
// short of the end, every "ÿÚ" after counts as a scan too: in a scan
// with restarts, the decoder's findRST (scan.go) skips bytes the walk takes
// for a marker, and reads on.
func jpegHeader(b []byte) (f jpegHead, orientation int) {
	orientation = 1
	exif := false
	if len(b) < 2 || b[0] != jpegMarker || b[1] != jpegStart {
		return f, orientation
	}
	at := 2
	stop := func() (jpegHead, int) {
		f.scans += bytes.Count(b[at:], []byte{jpegMarker, jpegScan})
		return f, orientation
	}
	for i := 2; ; at = i {
		if i+2 > len(b) {
			return stop()
		}
		prev, marker := b[i], b[i+1]
		i += 2
		for prev != jpegMarker {
			if i >= len(b) {
				return stop()
			}
			prev, marker = marker, b[i]
			i++
		}
		if marker == 0 {
			continue
		}
		for marker == jpegMarker {
			if i >= len(b) {
				return stop()
			}
			marker = b[i]
			i++
		}
		if marker == jpegEnd {
			return f, orientation
		}
		if jpegRestart0 <= marker && marker <= jpegRestart7 {
			continue
		}
		if i+2 > len(b) {
			return stop()
		}
		n := int(binary.BigEndian.Uint16(b[i:])) - 2
		i += 2
		if n < 0 || i+n > len(b) {
			return stop()
		}
		seg := b[i : i+n]
		i += n
		switch {
		case marker == jpegBaseline || marker == jpegExtended || marker == jpegProgressive:
			if f.components != nil {
				return stop()
			}
			var ok bool
			if f, ok = jpegFrame(marker, seg); !ok {
				return stop()
			}
		case marker == jpegScan:
			if f.components == nil {
				return stop()
			}
			f.scans++
		case marker == jpegApp1:
			if !exif && f.scans == 0 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
				exif, orientation = true, tiffOrientation(seg[6:])
			}
		case marker == jpegHuffman || marker == jpegQuant || marker == jpegInterval ||
			jpegApp0 <= marker && marker <= jpegApp15 || marker == jpegComment:
		default:
			return stop()
		}
	}
}

// A one-component picture's sampling counts as 1 × 1 whatever it says, as
// image/jpeg's processSOF takes it.
func jpegFrame(marker byte, seg []byte) (jpegHead, bool) {
	f := jpegHead{progressive: marker == jpegProgressive}
	if len(seg) < 6 || seg[0] != 8 {
		return f, false
	}
	n := int(seg[5])
	if n != 1 && n != 3 && n != 4 || len(seg) != 6+3*n {
		return f, false
	}
	f.height, f.width = int(binary.BigEndian.Uint16(seg[1:])), int(binary.BigEndian.Uint16(seg[3:]))
	for c := range n {
		s := sampling{int(seg[7+3*c] >> 4), int(seg[7+3*c] & 15)}
		if s.h < 1 || s.h > 4 || s.v < 1 || s.v > 4 || s.h == 3 || s.v == 3 {
			return f, false
		}
		if n == 1 {
			s = sampling{1, 1}
		}
		f.components = append(f.components, s)
	}
	return f, true
}

// jpegCost follows what image/jpeg allocates for a frame (Go 1.27, scan.go
// makeImg and processSOS, reader.go applyBlack and convertToRGB): the picture
// in whole blocks, grey or YCbCr, full size in every channel when the chroma
// is not at one of the six usual ratios; for four components a black channel
// and then a CMYK picture; for three an RGB picture, since a marker after the
// first scan can still ask for one; and for a progressive picture every
// coefficient, four bytes a sample, until the last scan.
func jpegCost(f jpegHead) int64 {
	maxH, maxV := 1, 1
	for _, c := range f.components {
		maxH, maxV = max(maxH, c.h), max(maxV, c.v)
	}
	mxx, myy := int64((f.width+8*maxH-1)/(8*maxH)), int64((f.height+8*maxV-1)/(8*maxV))
	pw, ph := 8*int64(maxH)*mxx, 8*int64(maxV)*myy
	pixels := int64(f.width) * int64(f.height)
	cost := pw * ph
	if len(f.components) > 1 {
		cw, ch := pw, ph
		if hr, vr, ok := chromaRatio(f.components, maxH, maxV); ok {
			cw, ch = pw/int64(hr), ph/int64(vr)
		}
		cost += 2 * cw * ch
	}
	switch len(f.components) {
	case 3:
		cost += 4 * pixels
	case 4:
		cost += pw*ph + 4*pixels
	}
	if f.progressive {
		for _, c := range f.components {
			cost += mxx * myy * int64(c.h*c.v) * 64 * 4
		}
	}
	return cost
}

func chromaRatio(c []sampling, maxH, maxV int) (hr, vr int, ok bool) {
	if c[1] != c[2] || c[0].h != maxH || c[0].v != maxV {
		return 0, 0, false
	}
	hr, vr = maxH/c[1].h, maxV/c[1].v
	switch hr<<4 | vr {
	case 0x11, 0x12, 0x21, 0x22, 0x41, 0x42:
		return hr, vr, true
	}
	return 0, 0, false
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
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	out := image.NewRGBA(image.Rect(0, 0, dw, dh))
	// One row at a time through a row of RGBA, so turning the picture costs
	// one copy of it rather than two.
	in := image.NewRGBA(image.Rect(0, 0, w, 1))
	for y := 0; y < h; y++ {
		draw.Draw(in, in.Bounds(), src, image.Pt(b.Min.X, b.Min.Y+y), draw.Src)
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
			copy(out.Pix[dy*out.Stride+dx*4:dy*out.Stride+dx*4+4], in.Pix[x*4:x*4+4])
		}
	}
	return out
}

func (m *Module) allow(w http.ResponseWriter, r *http.Request, t *throttle, what string) bool {
	wait, ok := t.allow(throttleKey(r), time.Now())
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

func (t *throttle) refund(key string, counted time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if w := t.seen[key]; w != nil && !w.start.After(counted) && w.count > 0 {
		w.count--
	}
}

func throttleKey(r *http.Request) string {
	store, _ := gocommerce.StoreFrom(r.Context())
	return store + " " + peerAddr(r)
}

// peerAddr is the request's address as the engine's outermost handler left
// it: the connection's, or the shopper's when the install trusts the proxy
// in front (GOCOMMERCE_TRUSTED_PROXIES). X-Forwarded-For is never read here:
// from anyone else it is whatever the client wrote.
func peerAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
