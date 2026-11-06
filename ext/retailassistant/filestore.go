package retailassistant

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

const (
	scanTimeout    = 60 * time.Second
	scanChunk      = 64 << 10
	retentionEvery = time.Hour
)

// loadFileKey reads the key files are sealed with. A bad key is held rather than
// returned from Register: a mistyped secret would otherwise crash-loop the engine
// and take every store on the install down with it, for a feature one module has.
// Documents then fail one by one with the reason, and nothing is stored in clear.
func (m *Module) loadFileKey() {
	if m.cfg.FileKey == "" {
		return
	}
	key, err := base64.StdEncoding.DecodeString(m.cfg.FileKey)
	if err != nil || len(key) != 32 {
		m.fileKeyBad = true
		m.log.Error("retail assistant: RETAIL_ASSISTANT_FILE_KEY is not 32 bytes of base64, so no file will be kept until it is fixed")
		return
	}
	m.fileKey = key
}

func (m *Module) fileAEAD() (cipher.AEAD, error) {
	block, err := aes.NewCipher(m.fileKey)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// fileLabel ties a sealed file to its row, so bytes copied from one document or
// store into another fail to open instead of showing the wrong bill.
func fileLabel(ctx context.Context, id int64) []byte {
	store, _ := gocommerce.StoreFrom(ctx)
	return []byte(store + "\x00" + strconv.FormatInt(id, 10))
}

// sealFile returns what to store and whether it is sealed.
//
// ponytail: one key and no rotation. Rotating means re-sealing every row; add a
// key id column and a re-seal pass the first time a key has to change.
func (m *Module) sealFile(ctx context.Context, id int64, data []byte) ([]byte, bool, error) {
	if m.fileKeyBad {
		return nil, false, errPermanent{"RETAIL_ASSISTANT_FILE_KEY is not valid, so the file was not kept"}
	}
	if m.fileKey == nil {
		return data, false, nil
	}
	aead, err := m.fileAEAD()
	if err != nil {
		return nil, false, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, false, err
	}
	return aead.Seal(nonce, nonce, data, fileLabel(ctx, id)), true, nil
}

func (m *Module) openFile(ctx context.Context, id int64, stored []byte) ([]byte, error) {
	if m.fileKey == nil {
		return nil, gocommerce.Conflictf("this file is encrypted, and the install's RETAIL_ASSISTANT_FILE_KEY is missing or not valid")
	}
	aead, err := m.fileAEAD()
	if err != nil {
		return nil, err
	}
	if len(stored) < aead.NonceSize() {
		return nil, gocommerce.Conflictf("this file cannot be opened with the install's key")
	}
	data, err := aead.Open(nil, stored[:aead.NonceSize()], stored[aead.NonceSize():], fileLabel(ctx, id))
	if err != nil {
		return nil, gocommerce.Conflictf("this file cannot be opened with the install's key")
	}
	return data, nil
}

// scanFile asks clamd about a file, when the install runs one. A scanner that is
// down is an ordinary failure, retried and then failed: once an owner has asked
// for scanning, a file does not get kept unscanned because the scanner was away.
func (m *Module) scanFile(ctx context.Context, data []byte) error {
	if m.cfg.ClamdAddr == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", m.cfg.ClamdAddr)
	if err != nil {
		return fmt.Errorf("the virus scanner could not be reached: %w", err)
	}
	defer conn.Close()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	reply, err := clamdInstream(conn, data)
	if err != nil {
		return fmt.Errorf("the virus scanner did not answer: %w", err)
	}
	if name, found := strings.CutSuffix(reply, " FOUND"); found {
		return errPermanent{"The virus scan flagged this file (" + strings.TrimPrefix(name, "stream: ") + "), so it was not kept."}
	}
	if reply != "stream: OK" {
		return fmt.Errorf("the virus scanner answered %q", reply)
	}
	return nil
}

// clamdInstream speaks clamd's INSTREAM command: length-prefixed chunks, ended by
// a zero length, answered by one NUL-terminated line.
func clamdInstream(conn io.ReadWriter, data []byte) (string, error) {
	if _, err := conn.Write([]byte("zINSTREAM\x00")); err != nil {
		return "", err
	}
	var size [4]byte
	for len(data) > 0 {
		n := min(len(data), scanChunk)
		binary.BigEndian.PutUint32(size[:], uint32(n))
		if _, err := conn.Write(size[:]); err != nil {
			return "", err
		}
		if _, err := conn.Write(data[:n]); err != nil {
			return "", err
		}
		data = data[n:]
	}
	binary.BigEndian.PutUint32(size[:], 0)
	if _, err := conn.Write(size[:]); err != nil {
		return "", err
	}
	reply, err := io.ReadAll(io.LimitReader(conn, 1024))
	if err != nil && !bytes.Contains(reply, []byte{0}) {
		return "", err
	}
	return strings.TrimSpace(strings.TrimRight(string(reply), "\x00")), nil
}

// RetentionPass applies this store's retention setting: past it, a closed
// document loses its file, and a rejected or failed one loses what was read from
// it too. A posted document keeps its draft and the ledger keeps every row,
// because a balance has to stay explainable after the paper is gone. Exported for
// the reason WorkPass is.
func (m *Module) RetentionPass(ctx context.Context) error {
	var s struct {
		Days int `plugin:"retention_days"`
	}
	if err := m.app.Plugins().Fill(ctx, pluginKey, &s); err != nil {
		return err
	}
	if s.Days <= 0 {
		return nil
	}
	return gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			WITH gone AS (
				UPDATE retailassistant_documents
				SET content = NULL, content_sealed = false, content_removed_at = now(),
				    draft = CASE WHEN status = 'posted' THEN draft END,
				    extraction = CASE WHEN status = 'posted' THEN extraction END
				WHERE content_removed_at IS NULL AND status IN ('posted', 'rejected', 'failed')
				  AND COALESCE(reviewed_at, created_at) < now() - make_interval(days => $1)
				RETURNING id
			)
			INSERT INTO retailassistant_audit (actor, event, entity, entity_id)
			SELECT 'retention rule', 'document.file_removed', 'document', id FROM gone`, s.Days); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			DELETE FROM retailassistant_replies
			WHERE (sent_at IS NOT NULL OR dead) AND created_at < now() - make_interval(days => $1)`, s.Days)
		return err
	})
}

// handleRemoveFile deletes one document's file on request. A document still in
// review keeps it, since the reviewer has to check the figures against it.
func (m *Module) handleRemoveFile(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		var status string
		var removed sql.NullTime
		err := tx.QueryRowContext(ctx, `SELECT status, content_removed_at FROM retailassistant_documents WHERE id = $1 FOR UPDATE`, id).Scan(&status, &removed)
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.NotFoundf("no document %d", id)
		}
		if err != nil {
			return err
		}
		switch status {
		case "posted", "rejected", "failed":
		default:
			return gocommerce.Conflictf("this document is %s; finish reviewing it before deleting its file", status)
		}
		if removed.Valid {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE retailassistant_documents SET content = NULL, content_sealed = false, content_removed_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		return m.audit(ctx, tx, "document.file_removed", "document", id, nil)
	})
	if err != nil {
		respondTx(w, r, err, "delete a document's file")
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]bool{"removed": true})
}
