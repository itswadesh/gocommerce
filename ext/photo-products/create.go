package photoproducts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	gocommerce "github.com/itswadesh/gocommerce/core"
	catalogai "github.com/itswadesh/gocommerce/ext/catalog-ai"
)

// MetaKey is the product metadata a drafted product carries: which batch and
// proposal it came from, and the suggestions a person has yet to act on.
const MetaKey = "ai_drafted"

// CreateResult is what pressing Create did.
type CreateResult struct {
	Created int      `json:"created"`
	Failed  int      `json:"failed"`
	Batch   *Batch   `json:"batch"`
	Errors  []string `json:"errors"`
}

// create makes a product of every proposal that has none yet. The claim on
// the batch is the status: only one Create runs at a time, and one that
// stopped half way leaves the products it made recorded against their
// proposals, so pressing Create again makes only the rest.
func (m *Module) create(ctx context.Context, id int64) (*CreateResult, error) {
	res, err := m.db.ExecContext(ctx, `
		UPDATE photo_products_batches SET status = 'creating', message = 'Creating the products', updated_at = now()
		WHERE id = $1 AND status = 'review'`, id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		b, err := m.batch(ctx, id, false)
		if err != nil {
			return nil, err
		}
		return nil, gocommerce.Conflictf("this batch is %s, and products are created from a batch under review", b.Status)
	}

	// Whatever stops this early hands the batch back for review, rather
	// than leaving it creating until the next restart.
	finished := false
	defer func() {
		if !finished {
			m.setStatus(ctx, id, StatusReview, "Ready to review")
		}
	}()

	out := &CreateResult{Errors: []string{}}
	items, err := m.items(ctx, id)
	if err != nil {
		return nil, err
	}
	photos, err := m.batch(ctx, id, true)
	if err != nil {
		return nil, err
	}
	mediaOf := map[int64]int64{}
	for _, p := range photos.Photos {
		mediaOf[p.ID] = p.MediaID
	}
	for _, it := range items {
		if it.ProductID != nil {
			continue
		}
		pid, err := m.createOne(ctx, id, it, mediaOf)
		if err != nil {
			out.Failed++
			msg := fmt.Sprintf("%s: %s", it.Draft.Title, err.Error())
			out.Errors = append(out.Errors, msg)
			_, _ = m.db.ExecContext(ctx, `UPDATE photo_products_items SET error = $2 WHERE id = $1`, it.ID, err.Error())
			continue
		}
		out.Created++
		res, err := m.db.ExecContext(ctx, `UPDATE photo_products_items SET product_id = $2, error = '' WHERE id = $1`, it.ID, pid)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return nil, fmt.Errorf("proposal %d went while its product %d was being made", it.ID, pid)
		}
	}
	finished = true

	if out.Failed == 0 {
		// Every product exists and holds its own pictures in the media
		// library; the copies the model was shown are no longer needed.
		m.setStatus(ctx, id, StatusDone, fmt.Sprintf("Created %d draft products", len(items)))
		if _, err := m.db.ExecContext(ctx, `UPDATE photo_products_photos SET thumb = NULL, image = NULL WHERE batch_id = $1`, id); err != nil {
			m.log.Warn("could not clear a finished batch's copies", "batch", id, "error", err)
		}
	} else {
		m.setStatus(ctx, id, StatusReview, fmt.Sprintf("%d products could not be created", out.Failed))
	}
	if out.Batch, err = m.batch(ctx, id, true); err != nil {
		return nil, err
	}
	return out, nil
}

// createOne makes one draft product through the catalogue service, then
// points it and each variant at their pictures.
func (m *Module) createOne(ctx context.Context, batchID int64, it Item, mediaOf map[int64]int64) (int64, error) {
	d := it.Draft
	marker := fmt.Sprintf("%d/%d", batchID, it.ID)
	// The proposal's mark on the product it made. Found here, the product
	// was created by a run that stopped before it could record it, and is
	// recorded now rather than made twice.
	var existing int64
	var product *gocommerce.Product
	err := m.db.QueryRowContext(ctx, `SELECT id FROM products WHERE metadata -> $1 ->> 'item' = $2 LIMIT 1`, MetaKey, marker).Scan(&existing)
	switch {
	case err == nil:
		// Its pictures are set again below: the run that made it may have
		// stopped before it got to them.
		if product, err = m.app.Products().GetProduct(ctx, existing); err != nil {
			return 0, err
		}
	case !errors.Is(err, sql.ErrNoRows):
		return 0, err
	}

	in, err := productInput(d, marker, batchID)
	if err != nil {
		return 0, err
	}
	for attempt := 1; product == nil; attempt++ {
		product, err = m.app.Products().CreateProduct(ctx, in)
		if err == nil {
			break
		}
		// Two products a shop called the same thing: the second gets the
		// proposal's number on its address rather than being refused.
		if attempt < 3 && strings.Contains(err.Error(), "slug") {
			in.Slug = slugify(d.Title) + "-" + strconv.FormatInt(it.ID, 10)
			if attempt == 2 {
				in.Slug += "-" + strconv.FormatInt(batchID, 10)
			}
			continue
		}
		return 0, err
	}

	// Pictures: the product holds every photo of the proposal, in variant
	// order, and each variant the photos of its own value. A failure here is
	// reported on the item but the product stands: a shop fixes a picture
	// in the editor more easily than a duplicate product.
	var all []int64
	byValue := map[string][]int64{}
	for _, v := range d.Variants {
		for _, pid := range v.Photos {
			if media, ok := mediaOf[pid]; ok {
				all = append(all, media)
				key := strings.ToLower(strings.TrimSpace(v.Value))
				byValue[key] = append(byValue[key], media)
				if alt := d.Alts[strconv.FormatInt(pid, 10)]; alt != "" {
					if _, err := m.app.MediaLibrary().SetAlt(ctx, media, alt); err != nil {
						m.log.Warn("could not set a photo's alt text", "media", media, "error", err)
					}
				}
			}
		}
	}
	if len(all) > 0 {
		if err := m.app.MediaLibrary().SetProductMedia(ctx, product.ID, all); err != nil {
			m.log.Warn("could not attach a drafted product's pictures", "product", product.ID, "error", err)
		}
	}
	if len(d.Variants) > 1 {
		for _, v := range product.Variants {
			if len(v.Options) == 0 {
				continue
			}
			if ids := byValue[strings.ToLower(strings.TrimSpace(v.Options[0]))]; len(ids) > 0 {
				if err := m.app.MediaLibrary().SetVariantMedia(ctx, v.ID, ids); err != nil {
					m.log.Warn("could not set a variant's pictures", "variant", v.ID, "error", err)
				}
			}
		}
	}
	return product.ID, nil
}

// productInput is the draft as the catalogue takes it: a draft product with
// every variant at 0 and nothing in stock. 0 is not a price anybody chose;
// it is what "Needs a price" on the products list finds, and the product is
// a draft so no shopper can buy it at 0.
func productInput(d Draft, marker string, batchID int64) (gocommerce.ProductInput, error) {
	if strings.TrimSpace(d.Title) == "" {
		return gocommerce.ProductInput{}, gocommerce.Validationf("a product needs a title")
	}
	if len(d.Variants) == 0 {
		return gocommerce.ProductInput{}, gocommerce.Validationf("%s has no photos", d.Title)
	}
	meta := map[string]any{"batch": batchID, "item": marker}
	if d.SuggestedCategory != "" {
		meta["suggested_category"] = d.SuggestedCategory
	}
	if d.HSCode != "" {
		meta["hs_code_suggestion"] = d.HSCode
	}
	metadata := gocommerce.Metadata{MetaKey: meta}
	if len(d.Highlights) > 0 {
		metadata[gocommerce.MetaHighlights] = d.Highlights
	}
	in := gocommerce.ProductInput{
		Title: strings.TrimSpace(d.Title), Description: catalogai.Paragraphs(d.Description), Status: gocommerce.ProductDraft,
		ProductType: d.ProductType, Tags: d.Tags, CategoryID: d.CategoryID,
		SEOTitle: d.SEOTitle, SEODescription: d.SEODescription, Metadata: metadata,
	}
	if in.Tags == nil {
		in.Tags = []string{}
	}

	var values []string
	for _, v := range d.Variants {
		values = append(values, v.Value)
	}
	values, sizes := distinct(values), distinct(d.Sizes)
	optionName := strings.TrimSpace(d.OptionName)
	if len(values) > 1 && optionName == "" {
		optionName = "Style"
	}
	colours := len(values) > 1
	if colours {
		in.Options = append(in.Options, gocommerce.OptionInput{Name: optionName, Values: values})
	}
	if len(sizes) > 0 {
		in.Options = append(in.Options, gocommerce.OptionInput{Name: "Size", Values: sizes})
	}

	zero := 0
	if len(in.Options) == 0 {
		var price int64
		in.PriceMinor, in.Stock = &price, &zero
		return in, nil
	}
	// SKUs are left to the catalogue, which derives them from the address
	// and the option values (FLORAL-HOOPS-GOLD-M) and steps past any taken.
	add := func(opts ...string) {
		in.Variants = append(in.Variants, gocommerce.VariantInput{PriceMinor: 0, StockOnHand: &zero, Options: opts})
	}
	switch {
	case colours && len(sizes) > 0:
		for _, v := range values {
			for _, s := range sizes {
				add(v, s)
			}
		}
	case colours:
		for _, v := range values {
			add(v)
		}
	default:
		for _, s := range sizes {
			add(s)
		}
	}
	return in, nil
}

// distinct is the non-empty values, trimmed, each once whatever its case.
func distinct(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if k := strings.ToLower(s); s != "" && !seen[k] {
			seen[k] = true
			out = append(out, s)
		}
	}
	return out
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	if out := strings.Trim(b.String(), "-"); out != "" {
		return out
	}
	return "product"
}

// tidyDraft holds a draft the shop edited to the limits the catalogue
// enforces, as applyDescription holds the model's, so a long line typed in
// review is cut rather than failing the product at Create.
func tidyDraft(d Draft) Draft {
	d.Title = clip(oneLine(d.Title), 120)
	d.Description = clip(d.Description, 4000)
	d.ProductType = clip(oneLine(d.ProductType), 60)
	d.SEOTitle = clip(oneLine(d.SEOTitle), 70)
	d.SEODescription = clip(oneLine(d.SEODescription), 160)
	d.OptionName = clip(oneLine(d.OptionName), 40)
	var highlights, tags []string
	for _, h := range d.Highlights {
		if h = clip(oneLine(h), 80); h != "" && len(highlights) < 8 {
			highlights = append(highlights, h)
		}
	}
	for _, t := range distinct(d.Tags) {
		if t = clip(strings.ToLower(oneLine(t)), 40); t != "" && len(tags) < 8 {
			tags = append(tags, t)
		}
	}
	d.Highlights, d.Tags, d.Sizes = highlights, tags, distinct(d.Sizes)
	for i := range d.Variants {
		d.Variants[i].Value = clip(oneLine(d.Variants[i].Value), 60)
	}
	if d.Alts == nil {
		d.Alts = map[string]string{}
	}
	return d
}
