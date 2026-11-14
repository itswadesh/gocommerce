package digital

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode"

	gocommerce "github.com/misiki-in/gocommerce/core"
)

// chunkSize is one row of digital_chunks. A megabyte keeps a 100 MiB file to a
// hundred rows and one row well inside what a statement should carry.
//
// ponytail: files live in PostgreSQL, capped by Config.MaxFileBytes; object
// storage behind the same digital_files rows is the upgrade when a store sells
// files too big for that.
const chunkSize = 1 << 20

// writeChunks stores r as version of fileID and returns its size and SHA-256.
// The caller has the bytes on local disk already (a multipart upload spooled
// there), so this transaction reads a file, not the network (rule 5).
func writeChunks(ctx context.Context, tx *sql.Tx, fileID int64, version int, r io.Reader, max int64) (int64, string, error) {
	h := sha256.New()
	r = io.TeeReader(io.LimitReader(r, max+1), h)
	buf := make([]byte, chunkSize)
	var size int64
	for seq := 0; ; seq++ {
		n, err := io.ReadFull(r, buf)
		if n > 0 {
			size += int64(n)
			if size > max {
				return 0, "", gocommerce.Validationf("a file can be at most %d MB", max>>20)
			}
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO digital_chunks (file_id, version, seq, data) VALUES ($1, $2, $3, $4)`,
				fileID, version, seq, buf[:n]); err != nil {
				return 0, "", err
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return 0, "", err
		}
	}
	if size == 0 {
		return 0, "", gocommerce.Validationf("the file is empty")
	}
	return size, hex.EncodeToString(h.Sum(nil)), nil
}

// chunkReader reads one version of a stored file, a chunk at a time, and seeks
// so http.ServeContent can answer a range: a download that broke at 80% on a
// phone resumes rather than starting again.
type chunkReader struct {
	ctx     context.Context
	db      *sql.DB
	fileID  int64
	version int
	size    int64
	off     int64
	seq     int
	buf     []byte
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.off >= c.size {
		return 0, io.EOF
	}
	seq := int(c.off / chunkSize)
	if c.buf == nil || c.seq != seq {
		var data []byte
		err := c.db.QueryRowContext(c.ctx,
			`SELECT data FROM digital_chunks WHERE file_id = $1 AND version = $2 AND seq = $3`,
			c.fileID, c.version, seq).Scan(&data)
		if errors.Is(err, sql.ErrNoRows) {
			// Replaced or removed mid-download: fail rather than splice.
			return 0, fmt.Errorf("digital: file %d changed while it was being read", c.fileID)
		}
		if err != nil {
			return 0, err
		}
		c.buf, c.seq = data, seq
	}
	start := c.off - int64(seq)*chunkSize
	if start >= int64(len(c.buf)) {
		return 0, io.ErrUnexpectedEOF
	}
	n := copy(p, c.buf[start:])
	c.off += int64(n)
	return n, nil
}

func (c *chunkReader) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += c.off
	case io.SeekEnd:
		offset += c.size
	default:
		return 0, errors.New("digital: bad whence")
	}
	if offset < 0 {
		return 0, errors.New("digital: negative position")
	}
	c.off = offset
	return offset, nil
}

// cleanFilename is the name a buyer's browser saves the file as. It arrived
// from a browser, so only its last element is kept, and nothing that could
// break a header or a path survives.
func cleanFilename(name string) string {
	name = path.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		// Format characters too: a right-to-left override makes "x202Efdp.exe"
		// read as an innocent PDF. The rest are what Windows refuses in a name.
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || strings.ContainsRune(`"/<>:|?*`, r) {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(name)
	if name == "." || name == ".." {
		name = ""
	}
	if r := []rune(name); len(r) > 200 {
		name = string(r[len(r)-200:])
	}
	return name
}

// servedType is the Content-Type a file goes out with: its own when it is a
// kind people sell and no browser runs, and plain bytes otherwise. A list of
// what is allowed rather than of what is not, so a type nobody thought of
// (text/xsl, say) is bytes too; attachment, nosniff and the sandbox already
// stop it running, and this is the layer under them.
func servedType(stored string) string {
	t := strings.ToLower(strings.TrimSpace(strings.SplitN(stored, ";", 2)[0]))
	switch t {
	case "application/pdf", "application/zip", "application/x-zip-compressed", "application/epub+zip",
		"application/msword", "application/vnd.ms-excel", "application/vnd.ms-powerpoint",
		"text/plain", "text/csv", "image/png", "image/jpeg", "image/gif", "image/webp":
		return t
	}
	for _, prefix := range []string{"audio/", "video/", "font/", "application/vnd.openxmlformats-officedocument."} {
		if strings.HasPrefix(t, prefix) {
			return t
		}
	}
	return "application/octet-stream"
}
