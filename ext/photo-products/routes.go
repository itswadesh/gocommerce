package photoproducts

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg" // the panel sends JPEG; decoding the header proves it is one
	_ "image/png"
	"net/http"
	"strconv"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

func (m *Module) mountRoutes(app *gocommerce.App) {
	// catalog.write throughout: every route here is a step towards writing
	// products, and reading a batch is reading proposals for them.
	w := gocommerce.RightCatalogWrite
	app.HandleAdminFunc("GET /api/admin/x/photo-products/status", m.handleStatus, w)
	app.HandleAdminFunc("GET /api/admin/x/photo-products/batches", m.handleList, w)
	app.HandleAdminFunc("POST /api/admin/x/photo-products/batches", m.handleNew, w)
	app.HandleAdminFunc("GET /api/admin/x/photo-products/batches/{id}", m.handleGet, w)
	app.HandleAdminFunc("POST /api/admin/x/photo-products/batches/{id}/photos", m.handleAddPhoto, w)
	app.HandleAdminFunc("POST /api/admin/x/photo-products/batches/{id}/start", m.handleStart, w)
	app.HandleAdminFunc("PUT /api/admin/x/photo-products/batches/{id}/items", m.handleSetItems, w)
	app.HandleAdminFunc("POST /api/admin/x/photo-products/batches/{id}/create", m.handleCreate, w)
}

// handleStatus says whether batches can run here, and if not what to set, so
// the screen can say so before anybody uploads sixty photos.
func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"ready": true, "max_photos": MaxPhotos}
	if err := m.notReady(r.Context()); err != nil {
		var apiErr *gocommerce.APIError
		if !errors.As(err, &apiErr) {
			gocommerce.RespondError(w, r, err)
			return
		}
		out["ready"], out["reason"] = false, apiErr.Message
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handleList(w http.ResponseWriter, r *http.Request) {
	rows, err := m.db.QueryContext(r.Context(), `SELECT `+batchColumns+` FROM photo_products_batches b ORDER BY b.id DESC LIMIT 10`)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "list photo batches"))
		return
	}
	defer rows.Close()
	out := []Batch{}
	for rows.Next() {
		b, err := scanBatch(rows)
		if err != nil {
			gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read a photo batch"))
			return
		}
		out = append(out, *b)
	}
	gocommerce.RespondList(w, out, gocommerce.ListMeta{Total: len(out), Limit: 10})
}

func (m *Module) handleNew(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Sizes []string `json:"sizes"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	sizes := distinct(in.Sizes)
	if len(sizes) > 20 {
		gocommerce.RespondError(w, r, gocommerce.Validationf("list at most 20 sizes"))
		return
	}
	for _, s := range sizes {
		if len([]rune(s)) > 30 {
			gocommerce.RespondError(w, r, gocommerce.Validationf("a size is a short label, such as M or 7"))
			return
		}
	}
	if sizes == nil {
		sizes = []string{}
	}
	encoded, _ := json.Marshal(sizes)
	var id int64
	if err := m.db.QueryRowContext(r.Context(), `
		INSERT INTO photo_products_batches (sizes, currency) VALUES ($1, $2) RETURNING id`,
		encoded, m.app.Currency(r.Context())).Scan(&id); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "record the batch"))
		return
	}
	m.respondBatch(w, r, http.StatusCreated, id)
}

func (m *Module) handleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if ok {
		m.respondBatch(w, r, http.StatusOK, id)
	}
}

func (m *Module) respondBatch(w http.ResponseWriter, r *http.Request, status int, id int64) {
	b, err := m.batch(r.Context(), id, true)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, status, b)
}

// handleAddPhoto records one photo: the media library item the panel has
// already uploaded, and the two small copies the model is shown. Adding the
// same media item again changes nothing, so a panel retrying a dropped
// request does not double a photo.
func (m *Module) handleAddPhoto(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		MediaID int64  `json:"media_id"`
		Thumb   []byte `json:"thumb"`
		Image   []byte `json:"image"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	if err := checkPicture("thumb", in.Thumb, maxThumbBytes, 1024); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := checkPicture("image", in.Image, maxImageBytes, 2048); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	media, err := m.app.MediaLibrary().Get(ctx, in.MediaID)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// Under the batch row's lock: a Start, or another upload racing for the
	// last place, waits until this one is counted.
	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		if err := lockBatch(ctx, tx, id, StatusUploading, "this batch has started; photos are added before it does"); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM photo_products_photos WHERE batch_id = $1 AND media_id <> $2`, id, media.ID).Scan(&n); err != nil {
			return err
		}
		if n >= MaxPhotos {
			return gocommerce.Validationf("a batch takes at most %d photos; start another batch for the rest", MaxPhotos)
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO photo_products_photos (batch_id, media_id, url, thumb, image) VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (store_id, batch_id, media_id) DO NOTHING`,
			id, media.ID, media.URL, in.Thumb, in.Image)
		return err
	})
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	m.respondBatch(w, r, http.StatusOK, id)
}

// lockBatch takes the batch row's lock for the rest of tx and refuses unless
// the batch is in the status given. Every step that moves a batch on runs
// behind it, so two of them never interleave.
func lockBatch(ctx context.Context, tx *sql.Tx, id int64, want, refusal string) error {
	var status string
	err := tx.QueryRowContext(ctx, `SELECT status FROM photo_products_batches WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return gocommerce.NotFoundf("no photo batch %d", id)
	}
	if err != nil {
		return err
	}
	if status != want {
		return gocommerce.Conflictf("%s", refusal)
	}
	return nil
}

// checkPicture holds a copy to the bounds the model calls were sized for.
func checkPicture(field string, b []byte, maxBytes, maxSide int) error {
	if len(b) == 0 {
		return gocommerce.Validationf("%s is required", field)
	}
	if len(b) > maxBytes {
		return gocommerce.Validationf("%s is %d KB; send at most %d KB", field, len(b)>>10, maxBytes>>10)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || format != "jpeg" {
		return gocommerce.Validationf("%s must be a JPEG picture", field)
	}
	if cfg.Width > maxSide || cfg.Height > maxSide {
		return gocommerce.Validationf("%s is %dx%d; send at most %d px on the long side", field, cfg.Width, cfg.Height, maxSide)
	}
	return nil
}

// handleStart sets a batch working: from uploading, or again after a failure
// or with proposals still undescribed, which repeats only what is missing.
func (m *Module) handleStart(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if err := m.notReady(ctx); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.db.ExecContext(ctx, `
		UPDATE photo_products_batches SET status = 'grouping', message = 'Starting', updated_at = now()
		WHERE id = $1 AND status IN ('uploading', 'failed', 'review')
		  AND EXISTS (SELECT 1 FROM photo_products_photos WHERE batch_id = $1 AND thumb IS NOT NULL)`, id)
	if err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "start the batch"))
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		b, err := m.batch(ctx, id, false)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		switch b.Status {
		case StatusGrouping, StatusDescribing:
		case StatusUploading:
			gocommerce.RespondError(w, r, gocommerce.Validationf("add photos before starting"))
			return
		default:
			gocommerce.RespondError(w, r, gocommerce.Conflictf("this batch is %s", b.Status))
			return
		}
	}
	// Items that failed are tried again; the shop's edits to the others stand.
	if _, err := m.db.ExecContext(ctx, `UPDATE photo_products_items SET error = '' WHERE batch_id = $1 AND NOT described AND product_id IS NULL`, id); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "start the batch"))
		return
	}
	store, _ := gocommerce.StoreFrom(ctx)
	m.launch(store, id)
	m.respondBatch(w, r, http.StatusAccepted, id)
}

// handleSetItems replaces the proposals with the shop's edited ones: renamed,
// merged, split, photos moved, some dropped. A proposal already created is
// kept as it is, whatever the request says.
func (m *Module) handleSetItems(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Items []struct {
			ID    *int64 `json:"id"`
			Draft Draft  `json:"draft"`
		} `json:"items"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	ctx := r.Context()
	b, err := m.batch(ctx, id, true)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	known := map[int64]bool{}
	for _, p := range b.Photos {
		known[p.ID] = true
	}
	current := map[int64]Item{}
	for _, it := range b.Items {
		current[it.ID] = it
	}
	placed := map[int64]bool{}
	for i, it := range in.Items {
		d := it.Draft
		if strings.TrimSpace(d.Title) == "" {
			gocommerce.RespondError(w, r, gocommerce.Validationf("items[%d] needs a title", i))
			return
		}
		for _, v := range d.Variants {
			for _, p := range v.Photos {
				if !known[p] {
					gocommerce.RespondError(w, r, gocommerce.Validationf("items[%d] names photo %d, which is not in this batch", i, p))
					return
				}
				if placed[p] {
					gocommerce.RespondError(w, r, gocommerce.Validationf("photo %d is in two products; a photo belongs to one", p))
					return
				}
				placed[p] = true
			}
		}
		if it.ID != nil {
			if _, ok := current[*it.ID]; !ok {
				gocommerce.RespondError(w, r, gocommerce.Validationf("items[%d] is not a proposal of this batch", i))
				return
			}
		}
	}

	err = gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		// Checked under the lock, so a Create or a Start cannot be reading
		// the proposals this replaces.
		if err := lockBatch(ctx, tx, id, StatusReview, "proposals are edited while the batch is under review"); err != nil {
			return err
		}
		keep := map[int64]bool{}
		for i, it := range in.Items {
			d := tidyDraft(it.Draft)
			encoded, err := json.Marshal(d)
			if err != nil {
				return err
			}
			if it.ID == nil {
				// A product the shop split off: marked described, so running
				// the batch again never writes over the shop's own words.
				if _, err := tx.ExecContext(ctx, `INSERT INTO photo_products_items (batch_id, position, draft, described) VALUES ($1, $2, $3, true)`,
					id, i, encoded); err != nil {
					return err
				}
				continue
			}
			keep[*it.ID] = true
			// Words the shop wrote are the shop's: an item it retitled or
			// described is not written over by running the batch again.
			was := current[*it.ID].Draft
			edited := d.Title != was.Title || d.Description != was.Description
			if _, err := tx.ExecContext(ctx, `UPDATE photo_products_items SET position = $3, draft = $4, described = described OR $5
				WHERE batch_id = $1 AND id = $2 AND product_id IS NULL`, id, *it.ID, i, encoded, edited); err != nil {
				return err
			}
		}
		for itemID, it := range current {
			if !keep[itemID] && it.ProductID == nil {
				if _, err := tx.ExecContext(ctx, `DELETE FROM photo_products_items WHERE id = $1`, itemID); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		var apiErr *gocommerce.APIError
		if !errors.As(err, &apiErr) {
			err = gocommerce.Internalf(err, "save the proposals")
		}
		gocommerce.RespondError(w, r, err)
		return
	}
	m.respondBatch(w, r, http.StatusOK, id)
}

func (m *Module) handleCreate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	out, err := m.create(context.WithoutCancel(r.Context()), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no photo batch %q", r.PathValue("id")))
		return 0, false
	}
	return id, true
}
