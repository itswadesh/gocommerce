package photoproducts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// Batch statuses.
const (
	StatusUploading  = "uploading"
	StatusGrouping   = "grouping"
	StatusDescribing = "describing"
	StatusReview     = "review"
	StatusCreating   = "creating"
	StatusDone       = "done"
	StatusFailed     = "failed"
)

// Draft is one proposed product as the shop reviews and edits it. Photos are
// named by their row in this batch, not by media id, so a photo cannot be
// moved into a product from somewhere else.
type Draft struct {
	Title          string   `json:"title"`
	Description    string   `json:"description"`
	Highlights     []string `json:"highlights"`
	ProductType    string   `json:"product_type"`
	Tags           []string `json:"tags"`
	CategoryID     *int64   `json:"category_id"`
	SEOTitle       string   `json:"seo_title"`
	SEODescription string   `json:"seo_description"`
	// SuggestedCategory and HSCode are suggestions for a person, never
	// applied: a category is not created silently, and a tariff code a model
	// guessed is not one a shop should file without reading.
	SuggestedCategory string `json:"suggested_category"`
	HSCode            string `json:"hs_code"`
	OptionName        string `json:"option_name"`
	// Sizes become a second option. They come from the shop, never from the
	// model: a photo does not show what sizes something is sold in.
	Sizes    []string       `json:"sizes"`
	Variants []DraftVariant `json:"variants"`
	// Alts is alt text by photo id.
	Alts map[string]string `json:"alts"`
}

// DraftVariant is one variant: its value on the option and its photos.
type DraftVariant struct {
	Value  string  `json:"value"`
	Photos []int64 `json:"photos"`
}

// Batch is one upload, as the panel reads it.
type Batch struct {
	ID             int64     `json:"id"`
	Status         string    `json:"status"`
	Sizes          []string  `json:"sizes"`
	StepDone       int       `json:"step_done"`
	StepTotal      int       `json:"step_total"`
	Message        string    `json:"message"`
	CostMicroMinor int64     `json:"cost_micro_minor"`
	Currency       string    `json:"currency"`
	PhotoCount     int       `json:"photo_count"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Photos         []Photo   `json:"photos,omitempty"`
	Items          []Item    `json:"items,omitempty"`
}

// Photo is one uploaded photo.
type Photo struct {
	ID      int64  `json:"id"`
	MediaID int64  `json:"media_id"`
	URL     string `json:"url"`
}

// Item is one proposed product.
type Item struct {
	ID        int64  `json:"id"`
	Position  int    `json:"position"`
	Draft     Draft  `json:"draft"`
	Described bool   `json:"described"`
	Error     string `json:"error,omitempty"`
	ProductID *int64 `json:"product_id,omitempty"`
}

// photo is a photo with the bytes a model is shown.
type photo struct {
	id           int64
	mediaID      int64
	thumb, image []byte
}

const batchColumns = `b.id, b.status, b.sizes, b.step_done, b.step_total, b.message, b.cost_micro_minor, b.currency,
	(SELECT count(*) FROM photo_products_photos p WHERE p.batch_id = b.id), b.created_at, b.updated_at`

func scanBatch(row interface{ Scan(...any) error }) (*Batch, error) {
	var b Batch
	var sizes []byte
	if err := row.Scan(&b.ID, &b.Status, &sizes, &b.StepDone, &b.StepTotal, &b.Message, &b.CostMicroMinor,
		&b.Currency, &b.PhotoCount, &b.CreatedAt, &b.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(sizes, &b.Sizes); err != nil {
		return nil, err
	}
	return &b, nil
}

// batch reads one batch, with its photos and items when full.
func (m *Module) batch(ctx context.Context, id int64, full bool) (*Batch, error) {
	b, err := scanBatch(m.db.QueryRowContext(ctx, `SELECT `+batchColumns+` FROM photo_products_batches b WHERE b.id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no photo batch %d", id)
	}
	if err != nil || !full {
		return b, err
	}
	rows, err := m.db.QueryContext(ctx, `SELECT id, media_id, url FROM photo_products_photos WHERE batch_id = $1 ORDER BY id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	b.Photos = []Photo{}
	for rows.Next() {
		var p Photo
		if err := rows.Scan(&p.ID, &p.MediaID, &p.URL); err != nil {
			return nil, err
		}
		b.Photos = append(b.Photos, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if b.Items, err = m.items(ctx, id); err != nil {
		return nil, err
	}
	return b, nil
}

func (m *Module) items(ctx context.Context, batchID int64) ([]Item, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, position, draft, described, error, product_id
		FROM photo_products_items WHERE batch_id = $1 ORDER BY position, id`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *it)
	}
	return out, rows.Err()
}

func scanItem(row interface{ Scan(...any) error }) (*Item, error) {
	var it Item
	var draft []byte
	if err := row.Scan(&it.ID, &it.Position, &draft, &it.Described, &it.Error, &it.ProductID); err != nil {
		return nil, err
	}
	return &it, json.Unmarshal(draft, &it.Draft)
}

func (m *Module) item(ctx context.Context, batchID, itemID int64) (*Item, error) {
	it, err := scanItem(m.db.QueryRowContext(ctx, `
		SELECT id, position, draft, described, error, product_id
		FROM photo_products_items WHERE batch_id = $1 AND id = $2`, batchID, itemID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, gocommerce.NotFoundf("no proposed product %d in batch %d", itemID, batchID)
	}
	return it, err
}

// photos reads a batch's photos in upload order, with the thumbnails or the
// larger pictures.
func (m *Module) photos(ctx context.Context, batchID int64, thumbs bool) ([]photo, error) {
	col := "image"
	if thumbs {
		col = "thumb"
	}
	rows, err := m.db.QueryContext(ctx, `SELECT id, media_id, `+col+` FROM photo_products_photos
		WHERE batch_id = $1 AND `+col+` IS NOT NULL ORDER BY id`, batchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []photo
	for rows.Next() {
		var p photo
		var b []byte
		if err := rows.Scan(&p.id, &p.mediaID, &b); err != nil {
			return nil, err
		}
		if thumbs {
			p.thumb = b
		} else {
			p.image = b
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (m *Module) setStatus(ctx context.Context, id int64, status, message string) {
	if _, err := m.db.ExecContext(ctx, `
		UPDATE photo_products_batches SET status = $2, message = $3, updated_at = now() WHERE id = $1`,
		id, status, message); err != nil {
		m.log.Warn("could not record a photo batch's progress", "batch", id, "error", err)
	}
}

func (m *Module) saveDraft(ctx context.Context, itemID int64, d Draft, described bool, errText string) error {
	encoded, err := json.Marshal(d)
	if err != nil {
		return err
	}
	_, err = m.db.ExecContext(ctx, `UPDATE photo_products_items SET draft = $2, described = $3, error = $4 WHERE id = $1`,
		itemID, encoded, described, errText)
	return err
}
