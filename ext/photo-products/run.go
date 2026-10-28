package photoproducts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

// notReady says what the store must set before a batch can run, or nil.
func (m *Module) notReady(ctx context.Context) error {
	if m.cfg.Gateway == nil {
		return gocommerce.Conflictf("this store was built without the AI gateway (-ai-gateway), which creating products from photos runs through")
	}
	if err := m.cfg.Gateway.Ready(ctx); err != nil {
		return gocommerce.Conflictf("%s", err.Error())
	}
	ok, err := m.cfg.Gateway.Allows(ctx, aigateway.DataDocuments)
	if err != nil {
		return err
	}
	if !ok {
		return gocommerce.Conflictf("the AI gateway keeps pictures in this store: switch on Share documents under Settings › Plugins › AI gateway")
	}
	return nil
}

// launch runs a batch in the background, once per process however many
// times it is asked.
func (m *Module) launch(store string, id int64) { m.spawn(store, id, true) }

// spawn is launch; again allows one more run for a Start that arrived while
// this one was finishing, and only one, so a status that cannot be written
// does not set the batch running for ever.
func (m *Module) spawn(store string, id int64, again bool) {
	m.mu.Lock()
	if m.running[id] {
		m.mu.Unlock()
		return
	}
	m.running[id] = true
	m.mu.Unlock()
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ctx := m.ctx
		if store != "" {
			ctx = gocommerce.WithStore(ctx, store)
		}
		m.work(ctx, id)
		m.mu.Lock()
		delete(m.running, id)
		m.mu.Unlock()
		// A Start that came while this run was writing its last status was
		// refused a second worker; it is honoured now.
		var status string
		if again && ctx.Err() == nil && m.db.QueryRowContext(ctx, `SELECT status FROM photo_products_batches WHERE id = $1`, id).Scan(&status) == nil &&
			(status == StatusGrouping || status == StatusDescribing) {
			m.spawn(store, id, false)
		}
	}()
}

// resume picks up, after a restart, the batches that were working when the
// process stopped. A batch that was creating goes back to review: the items
// it had created carry their product, and Create again makes only the rest.
func (m *Module) resume(ctx context.Context) error {
	if _, err := m.db.ExecContext(ctx, `
		UPDATE photo_products_batches SET status = 'review', updated_at = now() WHERE status = 'creating'`); err != nil {
		return err
	}
	rows, err := m.db.QueryContext(ctx, `SELECT id FROM photo_products_batches WHERE status IN ('grouping', 'describing')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		ids = append(ids, id)
	}
	store, _ := gocommerce.StoreFrom(ctx)
	for _, id := range ids {
		m.launch(store, id)
	}
	return rows.Err()
}

// work is one batch from photos to proposals. Each step is skipped when its
// result is already recorded, so running a batch again after a failure, a
// restart or a spent cap repeats only what did not finish.
func (m *Module) work(ctx context.Context, id int64) {
	items, err := m.items(ctx, id)
	if err != nil {
		m.setStatus(ctx, id, StatusFailed, "Could not read the batch: "+err.Error())
		return
	}
	if len(items) == 0 {
		m.setStatus(ctx, id, StatusGrouping, "Sorting the photos into products")
		if err := m.group(ctx, id); err != nil {
			m.setStatus(ctx, id, StatusFailed, "Could not sort the photos: "+reason(err))
			return
		}
		if items, err = m.items(ctx, id); err != nil {
			m.setStatus(ctx, id, StatusFailed, "Could not read the batch: "+err.Error())
			return
		}
	}

	var todo []Item
	for _, it := range items {
		if !it.Described && it.ProductID == nil {
			todo = append(todo, it)
		}
	}
	if _, err := m.db.ExecContext(ctx, `
		UPDATE photo_products_batches SET status = 'describing', step_total = $2, step_done = $3,
		       message = $4, updated_at = now() WHERE id = $1`,
		id, len(items), len(items)-len(todo), fmt.Sprintf("Writing %d product listings", len(todo))); err != nil {
		m.log.Warn("could not record a photo batch's progress", "batch", id, "error", err)
	}

	queue := make(chan Item)
	var wg sync.WaitGroup
	for range min(describers, max(1, len(todo))) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range queue {
				m.describe(ctx, id, it)
			}
		}()
	}
	for _, it := range todo {
		if ctx.Err() != nil {
			break
		}
		queue <- it
	}
	close(queue)
	wg.Wait()
	if ctx.Err() != nil {
		// Stopping, not failing: the next start resumes it.
		return
	}
	m.setStatus(ctx, id, StatusReview, "Ready to review")
}

// reason is what a failed run says to a person. A gateway refusal carries its
// own sentence; the outcome code in front of it is for the record, not them.
func reason(err error) string {
	var re *aigateway.RunError
	if errors.As(err, &re) {
		return re.Reason
	}
	return err.Error()
}

// group asks the model to sort the photos and records the products it
// proposes. A photo the answer leaves out, or names twice, is not lost: each
// left-out photo becomes a product of its own for the shop to merge.
func (m *Module) group(ctx context.Context, id int64) error {
	b, err := m.batch(ctx, id, false)
	if err != nil {
		return err
	}
	photos, err := m.photos(ctx, id, true)
	if err != nil {
		return err
	}
	if len(photos) == 0 {
		return errors.New("the batch has no photos")
	}
	res, err := m.cfg.Gateway.Run(ctx, groupTaskName, groupInput{BatchID: id})
	m.addCost(ctx, id, res)
	if err != nil {
		return err
	}
	var out groupOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		return err
	}

	used := map[int]bool{}
	var drafts []Draft
	for _, p := range out.Products {
		d := Draft{Title: clip(p.Name, 120), OptionName: clip(p.OptionName, 40), Sizes: b.Sizes, Alts: map[string]string{}}
		seen := map[string]bool{}
		for _, v := range p.Variants {
			var ids []int64
			for _, n := range v.Photos {
				if n >= 1 && n <= len(photos) && !used[n] {
					used[n] = true
					ids = append(ids, photos[n-1].id)
				}
			}
			value := clip(v.Value, 60)
			key := strings.ToLower(value)
			if len(ids) == 0 || seen[key] {
				// A variant with no photo of its own, or a second variant of
				// the same name, would be a variant nobody can see; its
				// photos join the first of that name.
				for i := range d.Variants {
					if strings.EqualFold(d.Variants[i].Value, value) {
						d.Variants[i].Photos = append(d.Variants[i].Photos, ids...)
					}
				}
				continue
			}
			seen[key] = true
			d.Variants = append(d.Variants, DraftVariant{Value: value, Photos: ids})
		}
		if len(d.Variants) == 0 {
			continue
		}
		if len(d.Variants) == 1 {
			d.OptionName, d.Variants[0].Value = "", ""
		}
		if d.Title == "" {
			d.Title = "Untitled product"
		}
		drafts = append(drafts, d)
	}
	for i, p := range photos {
		if !used[i+1] {
			drafts = append(drafts, Draft{Title: "Unsorted photo", Sizes: b.Sizes, Alts: map[string]string{},
				Variants: []DraftVariant{{Photos: []int64{p.id}}}})
		}
	}

	return gocommerce.InTx(ctx, m.db, func(tx *sql.Tx) error {
		// Another run of the same batch may have got here first; its
		// proposals stand.
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM photo_products_items WHERE batch_id = $1`, id).Scan(&n); err != nil || n > 0 {
			return err
		}
		for i, d := range drafts {
			encoded, err := json.Marshal(d)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO photo_products_items (batch_id, position, draft) VALUES ($1, $2, $3)`,
				id, i, encoded); err != nil {
				return err
			}
		}
		return nil
	})
}

// describe writes one product's listing. A failure is the item's, not the
// batch's: the shop sees which product is missing its words and why, and can
// run the batch again for just those.
func (m *Module) describe(ctx context.Context, batchID int64, it Item) {
	defer func() {
		if _, err := m.db.ExecContext(ctx, `
			UPDATE photo_products_batches SET step_done = least(step_done + 1, step_total), updated_at = now() WHERE id = $1`,
			batchID); err != nil {
			m.log.Warn("could not record a photo batch's progress", "batch", batchID, "error", err)
		}
	}()
	res, err := m.cfg.Gateway.Run(ctx, describeTaskName, describeInput{BatchID: batchID, ItemID: it.ID})
	m.addCost(ctx, batchID, res)
	if err != nil {
		if ctx.Err() == nil {
			_ = m.saveDraft(ctx, it.ID, it.Draft, false, reason(err))
		}
		return
	}
	var out describeOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		_ = m.saveDraft(ctx, it.ID, it.Draft, false, err.Error())
		return
	}
	d, err := m.applyDescription(ctx, it.Draft, out)
	if err != nil {
		_ = m.saveDraft(ctx, it.ID, it.Draft, false, err.Error())
		return
	}
	if err := m.saveDraft(ctx, it.ID, d, true, ""); err != nil {
		m.log.Warn("could not save a described product", "item", it.ID, "error", err)
	}
}

// applyDescription folds the model's listing into the draft, held to the
// limits the catalogue enforces, so the shop's Create never fails over a
// line the model wrote one character too long.
func (m *Module) applyDescription(ctx context.Context, d Draft, out describeOutput) (Draft, error) {
	d.Title = clip(out.Title, 120)
	d.Description = clip(out.Description, 2000)
	d.Highlights = nil
	for _, h := range out.Highlights {
		if h = clip(oneLine(h), 80); h != "" && len(d.Highlights) < 8 {
			d.Highlights = append(d.Highlights, h)
		}
	}
	d.ProductType = clip(out.ProductType, 60)
	d.Tags = nil
	for _, t := range out.Tags {
		if t = clip(strings.ToLower(oneLine(t)), 40); t != "" && len(d.Tags) < 8 {
			d.Tags = append(d.Tags, t)
		}
	}
	d.SEOTitle = clip(out.SEOTitle, 70)
	d.SEODescription = clip(out.SEODescription, 160)
	d.CategoryID = nil
	if out.CategoryID != nil {
		// Only a category the store has: an id the model made up files
		// nothing, and a name is never turned into a new category here.
		if _, err := m.app.Categories().Get(ctx, *out.CategoryID); err == nil {
			d.CategoryID = out.CategoryID
		}
	}
	d.SuggestedCategory = ""
	if out.SuggestedCategory != nil && d.CategoryID == nil {
		d.SuggestedCategory = clip(*out.SuggestedCategory, 80)
	}
	d.HSCode = ""
	if out.HSCode != nil && m.indianGST(ctx) {
		d.HSCode = clip(digitsOnly(*out.HSCode), 8)
	}
	if len(d.Variants) > 1 {
		if name := clip(out.OptionName, 40); name != "" {
			d.OptionName = name
		}
		if len(out.VariantValues) == len(d.Variants) {
			seen := map[string]bool{}
			ok := true
			for _, v := range out.VariantValues {
				key := strings.ToLower(clip(v, 60))
				ok = ok && key != "" && !seen[key]
				seen[key] = true
			}
			// Better names, but never two variants of one name.
			if ok {
				for i, v := range out.VariantValues {
					d.Variants[i].Value = clip(v, 60)
				}
			}
		}
	}
	ids := shownIDs(d, func(int64) bool { return true })
	if d.Alts == nil {
		d.Alts = map[string]string{}
	}
	for _, a := range out.AltTexts {
		if a.Photo >= 1 && a.Photo <= len(ids) {
			d.Alts[strconv.FormatInt(ids[a.Photo-1], 10)] = clip(oneLine(a.Alt), 125)
		}
	}
	return d, nil
}

// addCost adds what a run cost to the batch, so the shop sees what a batch
// spent beside what it made.
func (m *Module) addCost(ctx context.Context, id int64, res aigateway.Result) {
	if res.CostMicroMinor == 0 {
		return
	}
	if _, err := m.db.ExecContext(ctx, `
		UPDATE photo_products_batches SET cost_micro_minor = cost_micro_minor + $2, currency = $3 WHERE id = $1`,
		id, res.CostMicroMinor, res.Currency); err != nil {
		m.log.Warn("could not record a photo batch's cost", "batch", id, "error", err)
	}
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return strings.TrimSpace(string(r[:n]))
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
