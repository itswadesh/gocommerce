package digital

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/multipart"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

func (m *Module) routes(app *gocommerce.App) {
	app.HandleFunc("GET /x/digital/products", m.handleProducts)
	app.HandleFunc("GET /x/digital/orders/{number}", m.handleOrderDownloads)
	app.HandleFunc("POST /x/digital/orders/{number}/downloads/{id}", m.handleMintLink)
	app.HandleFunc("GET /x/digital/download/{link}", m.handleDownload)

	app.HandleAdminFunc("GET /api/admin/x/digital/products/{id}/files", m.handleListFiles, gocommerce.RightCatalogRead)
	app.HandleAdminFunc("POST /api/admin/x/digital/products/{id}/files", m.handleUpload, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("PUT /api/admin/x/digital/products/{id}/rule", m.handleSetRule, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("GET /api/admin/x/digital/files/{id}/content", m.handleAdminContent, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("PUT /api/admin/x/digital/files/{id}/content", m.handleReplace, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("DELETE /api/admin/x/digital/files/{id}", m.handleDeleteFile, gocommerce.RightCatalogWrite)
	app.HandleAdminFunc("GET /api/admin/x/digital/orders/{id}/grants", m.handleListGrants, gocommerce.RightOrdersRead)
	app.HandleAdminFunc("POST /api/admin/x/digital/grants/{id}/revoke", m.handleRevoke, gocommerce.RightOrdersWrite)
	app.HandleAdminFunc("POST /api/admin/x/digital/grants/{id}/restore", m.handleRestore, gocommerce.RightOrdersWrite)
}

// ---------------------------------------------------------------- shopper

// ProductFiles is what a product page may say about a product's files: how
// many and of what kinds, never their names or where they are.
type ProductFiles struct {
	Files   int      `json:"files"`
	Formats []string `json:"formats"`
}

func (m *Module) handleProducts(w http.ResponseWriter, r *http.Request) {
	var ids []int64
	for _, s := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if s = strings.TrimSpace(s); s == "" {
			continue
		}
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil || id <= 0 {
			gocommerce.RespondError(w, r, gocommerce.Validationf("ids must be product ids separated by commas"))
			return
		}
		ids = append(ids, id)
	}
	if len(ids) > 100 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("at most 100 ids at a time"))
		return
	}
	out := map[string]*ProductFiles{}
	if len(ids) > 0 {
		// Only products a shopper can see: a draft's files are nobody's business yet.
		rows, err := m.db.QueryContext(r.Context(), `
			SELECT f.product_id, f.filename FROM digital_files f
			JOIN products p ON p.id = f.product_id AND p.status = 'active'
			WHERE f.product_id = ANY ($1) ORDER BY f.id`, int64s(ids))
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		defer rows.Close()
		for rows.Next() {
			var pid int64
			var name string
			if err := rows.Scan(&pid, &name); err != nil {
				gocommerce.RespondError(w, r, err)
				return
			}
			key := strconv.FormatInt(pid, 10)
			pf := out[key]
			if pf == nil {
				pf = &ProductFiles{Formats: []string{}}
				out[key] = pf
			}
			pf.Files++
			if f := format(name); f != "" && !slices.Contains(pf.Formats, f) {
				pf.Formats = append(pf.Formats, f)
			}
		}
		if err := rows.Err(); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

// Download is one file of one order line as its buyer sees it.
type Download struct {
	ID          int64      `json:"id"`
	OrderLineID int64      `json:"order_line_id"`
	Title       string     `json:"title"`
	Filename    string     `json:"filename"`
	SizeBytes   int64      `json:"size_bytes"`
	Downloads   int        `json:"downloads"`
	Limit       *int       `json:"download_limit"`
	ExpiresAt   *time.Time `json:"expires_at"`
	// Status is ready, expired, used_up or revoked.
	Status string `json:"status"`
}

// guestOrder is the order for the holder of its access token, answered as an
// order that does not exist otherwise. An empty token never matches, even an
// order whose own is somehow empty.
func (m *Module) guestOrder(r *http.Request) (*gocommerce.Order, error) {
	token := r.URL.Query().Get("token")
	if token == "" {
		return nil, gocommerce.NotFoundf("order not found")
	}
	return m.app.Order().GetForGuest(r.Context(), r.PathValue("number"), token)
}

func (m *Module) handleOrderDownloads(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	order, err := m.guestOrder(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	list, err := m.downloads(r, order.ID, 0)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"downloads": list})
}

// downloads lists an order's grants, or the one named, with their state now.
func (m *Module) downloads(r *http.Request, orderID, grantID int64) ([]Download, error) {
	rows, err := m.db.QueryContext(r.Context(), `
		SELECT g.id, g.order_line_id, coalesce(l.title, ''), f.filename, f.size_bytes, g.downloads,
		       g.download_limit, g.expires_at, g.revoked_at IS NOT NULL
		FROM digital_grants g
		JOIN digital_files f ON f.id = g.file_id
		LEFT JOIN order_lines l ON l.id = g.order_line_id
		WHERE g.order_id = $1 AND ($2 = 0 OR g.id = $2)
		ORDER BY g.order_line_id, f.id`, orderID, grantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	now := m.now()
	list := []Download{}
	for rows.Next() {
		var d Download
		var limit sql.NullInt64
		var expires sql.NullTime
		var revoked bool
		if err := rows.Scan(&d.ID, &d.OrderLineID, &d.Title, &d.Filename, &d.SizeBytes, &d.Downloads,
			&limit, &expires, &revoked); err != nil {
			return nil, err
		}
		if limit.Valid {
			n := int(limit.Int64)
			d.Limit = &n
		}
		if expires.Valid {
			d.ExpiresAt = &expires.Time
		}
		d.Status = status(revoked, d.ExpiresAt, d.Limit, d.Downloads, now)
		list = append(list, d)
	}
	return list, rows.Err()
}

func status(revoked bool, expires *time.Time, limit *int, used int, now time.Time) string {
	switch {
	case revoked:
		return "revoked"
	case expires != nil && !now.Before(*expires):
		return "expired"
	case limit != nil && used >= *limit:
		return "used_up"
	}
	return "ready"
}

// The refusals a buyer can meet, each with its own code so a storefront can
// word it, and each saying what to do next.
var (
	errRevoked = &gocommerce.APIError{Status: http.StatusGone, Code: "download_revoked",
		Message: "this download is no longer available for this order; contact the store if you think that is wrong"}
	errExpired = &gocommerce.APIError{Status: http.StatusGone, Code: "download_expired",
		Message: "the time to download this file has run out; contact the store for another chance"}
	errUsedUp = &gocommerce.APIError{Status: http.StatusGone, Code: "download_limit_reached",
		Message: "this file has been downloaded as many times as the store allows; contact the store for another chance"}
	errLinkDead = &gocommerce.APIError{Status: http.StatusGone, Code: "download_link_expired",
		Message: "this download link has run out; open your order page again for a fresh one"}
)

func refusal(state string) error {
	switch state {
	case "revoked":
		return errRevoked
	case "expired":
		return errExpired
	case "used_up":
		return errUsedUp
	}
	return nil
}

// handleMintLink gives the holder of the order's token a fresh link to one of
// its files. The link replaces any earlier one for that grant.
func (m *Module) handleMintLink(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	order, err := m.guestOrder(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("download not found"))
		return
	}
	// Scoped by the order as well as the id: a grant of somebody else's order
	// is not found, whoever's token this is.
	list, err := m.downloads(r, order.ID, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if len(list) == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("download not found"))
		return
	}
	if err := refusal(list[0].Status); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	link := hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(link))
	expires := m.now().Add(m.cfg.LinkTTL)
	// The state is checked again in the write: two mints racing past the read
	// above would otherwise each get an uncounted link, one past the limit.
	res, err := m.db.ExecContext(r.Context(), `
		UPDATE digital_grants SET link_hash = $2, link_expires_at = $3, link_counted = false
		WHERE id = $1 AND order_id = $4 AND revoked_at IS NULL
		  AND (expires_at IS NULL OR expires_at > $5)
		  AND (download_limit IS NULL OR downloads < download_limit)`, id, sum[:], expires, order.ID, m.now())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		err := gocommerce.Conflictf("this download changed just now; open your order page again")
		if list, rerr := m.downloads(r, order.ID, id); rerr == nil && len(list) == 1 {
			if refused := refusal(list[0].Status); refused != nil {
				err = refused.(*gocommerce.APIError)
			}
		}
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusCreated, map[string]any{
		"url":        "/x/digital/download/" + link,
		"expires_at": expires,
	})
}

// handleDownload serves the file a live link names. The link is the whole
// credential: 32 random bytes, of which only the hash is kept.
func (m *Module) handleDownload(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	link := r.PathValue("link")
	if len(link) != 64 {
		gocommerce.RespondError(w, r, errLinkDead)
		return
	}
	sum := sha256.Sum256([]byte(link))
	var (
		fileID, size int64
		version      int
		name, ctype  string
		sum256       string
	)
	err := gocommerce.InTx(r.Context(), m.db, func(tx *sql.Tx) error {
		var (
			grantID     int64
			used        int
			limit       sql.NullInt64
			expires     sql.NullTime
			linkExpires time.Time
			counted     bool
			revoked     bool
		)
		err := tx.QueryRowContext(r.Context(), `
			SELECT g.id, g.downloads, g.download_limit, g.expires_at, g.link_expires_at, g.link_counted,
			       g.revoked_at IS NOT NULL, f.id, f.version, f.size_bytes, f.filename, f.content_type, f.sha256
			FROM digital_grants g JOIN digital_files f ON f.id = g.file_id
			WHERE g.link_hash = $1
			FOR UPDATE OF g`, sum[:]).Scan(&grantID, &used, &limit, &expires, &linkExpires, &counted,
			&revoked, &fileID, &version, &size, &name, &ctype, &sum256)
		if errors.Is(err, sql.ErrNoRows) {
			return errLinkDead
		}
		if err != nil {
			return err
		}
		now := m.now()
		if !now.Before(linkExpires) {
			return errLinkDead
		}
		var lim *int
		if limit.Valid {
			n := int(limit.Int64)
			lim = &n
		}
		var exp *time.Time
		if expires.Valid {
			exp = &expires.Time
		}
		// A link counts once. The browser asking again on the same link — a
		// resumed download, Android's download manager fetching it a second
		// time — is the same download, so it is not refused for the limit the
		// first request just reached.
		if counted {
			if revoked {
				return errRevoked
			}
			if exp != nil && !now.Before(*exp) {
				return errExpired
			}
			return nil
		}
		if err := refusal(status(revoked, exp, lim, used, now)); err != nil {
			return err
		}
		// A HEAD is a probe — a download manager, a link preview — not a download.
		if r.Method == http.MethodHead {
			return nil
		}
		_, err = tx.ExecContext(r.Context(),
			`UPDATE digital_grants SET downloads = downloads + 1, link_counted = true WHERE id = $1`, grantID)
		return err
	})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.serveFile(w, r, fileID, version, size, name, ctype, sum256)
}

func (m *Module) serveFile(w http.ResponseWriter, r *http.Request, fileID int64, version int, size int64, name, ctype, sum string) {
	// The server's write timeout is a minute; a 100 MB file on a slow phone
	// connection takes far longer than that.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Hour))
	h := w.Header()
	h.Set("Content-Type", servedType(ctype))
	h.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	// Names this version of the bytes, so a download resumed with If-Range
	// after a Replace starts again instead of splicing two files.
	h.Set("ETag", fmt.Sprintf(`"%s-%d"`, sum, version))
	http.ServeContent(w, r, "", time.Time{}, &chunkReader{
		ctx: r.Context(), db: m.db, fileID: fileID, version: version, size: size,
	})
}

// noStore keeps a response carrying or reached by a credential out of every
// cache and every Referer.
func noStore(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}

// ---------------------------------------------------------------- operator

// File is a stored file as an operator sees it.
type File struct {
	ID          int64     `json:"id"`
	ProductID   int64     `json:"product_id"`
	VariantID   *int64    `json:"variant_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// Rule is a product's download limit and the days downloads stay open, each
// nil for none.
type Rule struct {
	DownloadLimit *int `json:"download_limit"`
	ExpiryDays    *int `json:"expiry_days"`
}

func pathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.Validationf("%s must be a positive integer", name)
	}
	return id, nil
}

func (m *Module) handleListFiles(w http.ResponseWriter, r *http.Request) {
	pid, err := pathID(r, "id")
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	files, err := m.files(r, `WHERE product_id = $1`, pid)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var rule Rule
	var limit, days sql.NullInt64
	err = m.db.QueryRowContext(r.Context(),
		`SELECT download_limit, expiry_days FROM digital_rules WHERE product_id = $1`, pid).Scan(&limit, &days)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, err)
		return
	}
	rule.DownloadLimit, rule.ExpiryDays = intPtr(limit), intPtr(days)
	gocommerce.Respond(w, http.StatusOK, map[string]any{"files": files, "rule": rule})
}

func (m *Module) files(r *http.Request, where string, arg any) ([]File, error) {
	rows, err := m.db.QueryContext(r.Context(), `
		SELECT id, product_id, variant_id, filename, content_type, size_bytes, sha256, created_at, updated_at
		FROM digital_files `+where+` ORDER BY id`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []File{}
	for rows.Next() {
		var f File
		var variant sql.NullInt64
		if err := rows.Scan(&f.ID, &f.ProductID, &variant, &f.Filename, &f.ContentType, &f.SizeBytes,
			&f.SHA256, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, err
		}
		if variant.Valid {
			f.VariantID = &variant.Int64
		}
		list = append(list, f)
	}
	return list, rows.Err()
}

// upload reads the one file of a multipart body, spooled to disk past 8 MB so
// the transaction that stores it never waits on the uploader's connection.
func (m *Module) upload(w http.ResponseWriter, r *http.Request) (*uploaded, error) {
	// A big file on a slow connection outlasts the server's read timeout.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(time.Hour))
	r.Body = http.MaxBytesReader(w, r.Body, m.cfg.MaxFileBytes+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, gocommerce.Validationf("a file can be at most %d MB", m.cfg.MaxFileBytes>>20)
		}
		return nil, gocommerce.Validationf("expected a multipart upload with a %q field", "file")
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		return nil, gocommerce.Validationf("no %q field in the upload", "file")
	}
	name := cleanFilename(header.Filename)
	if name == "" {
		file.Close()
		return nil, gocommerce.Validationf("the file needs a name")
	}
	// A browser sends application/octet-stream for any type it does not know,
	// and a phone then cannot tell which app opens the file: the extension knows
	// better.
	ctype := header.Header.Get("Content-Type")
	if ctype == "" || ctype == "application/octet-stream" {
		if byExt := mime.TypeByExtension(strings.ToLower(extOf(name))); byExt != "" {
			ctype = byExt
		}
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return &uploaded{file: file, name: name, ctype: ctype}, nil
}

type uploaded struct {
	file        multipart.File
	name, ctype string
}

func (m *Module) handleUpload(w http.ResponseWriter, r *http.Request) {
	pid, err := pathID(r, "id")
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	up, err := m.upload(w, r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer up.file.Close()
	defer r.MultipartForm.RemoveAll()

	var variant *int64
	if s := strings.TrimSpace(r.FormValue("variant_id")); s != "" {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil || v <= 0 {
			gocommerce.RespondError(w, r, gocommerce.Validationf("variant_id must be a positive integer"))
			return
		}
		variant = &v
	}
	var id int64
	err = gocommerce.InTx(r.Context(), m.db, func(tx *sql.Tx) error {
		var ok bool
		if err := tx.QueryRowContext(r.Context(), `
			SELECT EXISTS (SELECT 1 FROM products WHERE id = $1)
			   AND ($2::bigint IS NULL OR EXISTS (SELECT 1 FROM variants WHERE id = $2 AND product_id = $1))`,
			pid, variant).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return gocommerce.NotFoundf("no such product, or the variant is not one of its own")
		}
		if err := tx.QueryRowContext(r.Context(), `
			INSERT INTO digital_files (product_id, variant_id, filename, content_type, size_bytes, sha256)
			VALUES ($1, $2, $3, $4, 0, '') RETURNING id`, pid, variant, up.name, up.ctype).Scan(&id); err != nil {
			return err
		}
		size, sum, err := writeChunks(r.Context(), tx, id, 1, up.file, m.cfg.MaxFileBytes)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(r.Context(),
			`UPDATE digital_files SET size_bytes = $2, sha256 = $3 WHERE id = $1`, id, size, sum)
		return err
	})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.respondFile(w, r, http.StatusCreated, id)
}

// handleReplace swaps a file's bytes and keeps its id, so every buyer who
// already has it gets the new version through the grant they hold.
func (m *Module) handleReplace(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	up, err := m.upload(w, r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer up.file.Close()
	defer r.MultipartForm.RemoveAll()

	err = gocommerce.InTx(r.Context(), m.db, func(tx *sql.Tx) error {
		var version int
		err := tx.QueryRowContext(r.Context(),
			`SELECT version FROM digital_files WHERE id = $1 FOR UPDATE`, id).Scan(&version)
		if errors.Is(err, sql.ErrNoRows) {
			return gocommerce.NotFoundf("file not found")
		}
		if err != nil {
			return err
		}
		size, sum, err := writeChunks(r.Context(), tx, id, version+1, up.file, m.cfg.MaxFileBytes)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(r.Context(),
			`DELETE FROM digital_chunks WHERE file_id = $1 AND version = $2`, id, version); err != nil {
			return err
		}
		_, err = tx.ExecContext(r.Context(), `
			UPDATE digital_files SET version = $2, filename = $3, content_type = $4, size_bytes = $5,
			       sha256 = $6, updated_at = now()
			WHERE id = $1`, id, version+1, up.name, up.ctype, size, sum)
		return err
	})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.respondFile(w, r, http.StatusOK, id)
}

func (m *Module) respondFile(w http.ResponseWriter, r *http.Request, status int, id int64) {
	files, err := m.files(r, `WHERE id = $1`, id)
	if err != nil || len(files) == 0 {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, status, files[0])
}

// handleDeleteFile removes a file, and with it every buyer's access to it.
func (m *Module) handleDeleteFile(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.db.ExecContext(r.Context(), `DELETE FROM digital_files WHERE id = $1`, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("file not found"))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAdminContent lets an operator check what buyers will get.
func (m *Module) handleAdminContent(w http.ResponseWriter, r *http.Request) {
	noStore(w)
	id, err := pathID(r, "id")
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var version int
	var size int64
	var name, ctype string
	var sum string
	err = m.db.QueryRowContext(r.Context(),
		`SELECT version, size_bytes, filename, content_type, sha256 FROM digital_files WHERE id = $1`, id).
		Scan(&version, &size, &name, &ctype, &sum)
	if errors.Is(err, sql.ErrNoRows) {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("file not found"))
		return
	}
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.serveFile(w, r, id, version, size, name, ctype, sum)
}

func (m *Module) handleSetRule(w http.ResponseWriter, r *http.Request) {
	pid, err := pathID(r, "id")
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in Rule
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if in.DownloadLimit != nil && (*in.DownloadLimit < 1 || *in.DownloadLimit > 1000) {
		gocommerce.RespondError(w, r, gocommerce.Validationf("download_limit is between 1 and 1000, or null for no limit"))
		return
	}
	if in.ExpiryDays != nil && (*in.ExpiryDays < 1 || *in.ExpiryDays > 3650) {
		gocommerce.RespondError(w, r, gocommerce.Validationf("expiry_days is between 1 and 3650, or null for never"))
		return
	}
	var exists bool
	if err := m.db.QueryRowContext(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM products WHERE id = $1)`, pid).Scan(&exists); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if !exists {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("product not found"))
		return
	}
	if _, err := m.db.ExecContext(r.Context(), `
		INSERT INTO digital_rules (product_id, download_limit, expiry_days) VALUES ($1, $2, $3)
		ON CONFLICT (store_id, product_id) DO UPDATE
		SET download_limit = excluded.download_limit, expiry_days = excluded.expiry_days, updated_at = now()`,
		pid, in.DownloadLimit, in.ExpiryDays); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, in)
}

// Grant is a buyer's access to one file, as an operator sees it.
type Grant struct {
	Download
	RevokedReason string `json:"revoked_reason,omitempty"`
}

func (m *Module) handleListGrants(w http.ResponseWriter, r *http.Request) {
	oid, err := pathID(r, "id")
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	list, err := m.downloads(r, oid, 0)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	reasons := map[int64]string{}
	rows, err := m.db.QueryContext(r.Context(),
		`SELECT id, revoked_reason FROM digital_grants WHERE order_id = $1 AND revoked_at IS NOT NULL`, oid)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var reason string
		if err := rows.Scan(&id, &reason); err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		reasons[id] = reason
	}
	out := make([]Grant, len(list))
	for i, d := range list {
		out[i] = Grant{Download: d, RevokedReason: reasons[d.ID]}
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handleRevoke(w http.ResponseWriter, r *http.Request) {
	m.setRevoked(w, r, `UPDATE digital_grants SET revoked_at = now(), revoked_reason = '`+reasonOperator+`', link_hash = NULL
		WHERE id = $1 AND revoked_at IS NULL`)
}

// handleRestore opens a grant again with its count back at zero: the support
// answer to "my download failed" and to a refund that was reversed.
func (m *Module) handleRestore(w http.ResponseWriter, r *http.Request) {
	m.setRevoked(w, r, `UPDATE digital_grants SET revoked_at = NULL, revoked_reason = '', downloads = 0,
		expires_at = CASE WHEN expires_at IS NOT NULL AND expires_at <= now() THEN now() + interval '7 days' ELSE expires_at END
		WHERE id = $1 AND EXISTS (SELECT 1 FROM orders o WHERE o.id = digital_grants.order_id AND o.payment_status = 'paid')`,
		"only a paid order's downloads can be opened again; record the payment first")
}

// setRevoked runs query on one grant. A refusal, when given, is the answer to
// the query changing nothing.
func (m *Module) setRevoked(w http.ResponseWriter, r *http.Request, query string, refusal ...string) {
	id, err := pathID(r, "id")
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var orderID int64
	if err := m.db.QueryRowContext(r.Context(),
		`SELECT order_id FROM digital_grants WHERE id = $1`, id).Scan(&orderID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = gocommerce.NotFoundf("grant not found")
		}
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.db.ExecContext(r.Context(), query, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 && len(refusal) > 0 {
		gocommerce.RespondError(w, r, gocommerce.Conflictf("%s", refusal[0]))
		return
	}
	list, err := m.downloads(r, orderID, id)
	if err != nil || len(list) == 0 {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, list[0])
}

// ---------------------------------------------------------------- helpers

func intPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

func extOf(name string) string {
	if i := strings.LastIndexByte(name, '.'); i > 0 {
		return name[i:]
	}
	return ""
}

// format is a file's kind as a shopper reads it: "PDF", "ZIP".
func format(name string) string {
	ext := strings.TrimPrefix(extOf(name), ".")
	if ext == "" || len(ext) > 5 {
		return ""
	}
	return strings.ToUpper(ext)
}
