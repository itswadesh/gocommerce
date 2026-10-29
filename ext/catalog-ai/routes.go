package catalogai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

// Proposal is the answer to "improve this product": each change beside what
// the product holds now, and what the shop must check before it saves.
type Proposal struct {
	ProductID int64    `json:"product_id"`
	Changes   []Change `json:"changes"`
	// SuggestedCategory is a category the store does not have that would
	// fit. It is never created here.
	SuggestedCategory string   `json:"suggested_category,omitempty"`
	Verify            []string `json:"verify"`
	// Pictures is how many of the product's pictures the model saw, and
	// PicturesNote why it saw fewer, or none.
	Pictures       int    `json:"pictures"`
	PicturesNote   string `json:"pictures_note,omitempty"`
	ExecutionID    int64  `json:"execution_id"`
	Model          string `json:"model"`
	CostMicroMinor int64  `json:"cost_micro_minor"`
	Currency       string `json:"currency"`
}

// Change is one field the model would change. Current and Proposed are the
// field's own shape as the product update takes it: a string, a list of
// lines, or for the category {id, name}. A description is HTML on both sides.
type Change struct {
	Field string `json:"field"`
	// MediaID is the picture an "alt" change is for.
	MediaID  int64  `json:"media_id,omitempty"`
	Current  any    `json:"current"`
	Proposed any    `json:"proposed"`
	Reason   string `json:"reason"`
}

// The codes a refusal carries, which the panel picks its state by.
const (
	codeNotReady = "ai_not_ready"
	codeBudget   = "ai_budget_exhausted"
	codeCredit   = "ai_credit_exhausted"
	codePolicy   = "ai_policy_blocked"
	codeProvider = "ai_provider_failed"
	codeNoAnswer = "ai_no_proposal"
)

// improveBudget is how long one improve request may take: two model calls
// and six pictures can outlast the server's write timeout.
const improveBudget = 4 * time.Minute

// refusals is what a run that ended without an answer becomes for the
// screen, by the gateway's outcome. Anything else is codeNoAnswer.
var refusals = map[string]struct {
	status int
	code   string
}{
	aigateway.OutcomeBudgetExhausted: {http.StatusConflict, codeBudget},
	aigateway.OutcomeCreditExhausted: {http.StatusConflict, codeCredit},
	aigateway.OutcomeNoPrice:         {http.StatusConflict, codeNotReady},
	aigateway.OutcomePolicyBlocked:   {http.StatusConflict, codePolicy},
	aigateway.OutcomeProviderError:   {http.StatusBadGateway, codeProvider},
}

func refusal(err error) error {
	var re *aigateway.RunError
	if !errors.As(err, &re) {
		if errors.Is(err, aigateway.ErrNotReady) {
			return &gocommerce.APIError{Status: http.StatusConflict, Code: codeNotReady, Message: err.Error()}
		}
		return err
	}
	r, ok := refusals[re.Outcome]
	if !ok {
		r.status, r.code = http.StatusBadGateway, codeNoAnswer
	}
	return &gocommerce.APIError{Status: r.status, Code: r.code, Message: re.Reason}
}

// ready says what the store must set before a product can be improved.
func (m *Module) ready(ctx context.Context) error {
	if m.cfg.Gateway == nil {
		return &gocommerce.APIError{Status: http.StatusConflict, Code: codeNotReady,
			Message: "this store was built without the AI gateway (-ai-gateway), which improving a product runs through"}
	}
	return refusal(m.cfg.Gateway.Ready(ctx))
}

// handleStatus says whether a product can be improved here, and whether its
// pictures would be sent, before anybody presses the button.
func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"ready": true, "pictures": false}
	if err := m.ready(r.Context()); err != nil {
		var apiErr *gocommerce.APIError
		if !errors.As(err, &apiErr) {
			gocommerce.RespondError(w, r, err)
			return
		}
		out["ready"], out["reason"] = false, apiErr.Message
	} else {
		ok, err := m.cfg.Gateway.Allows(r.Context(), aigateway.DataDocuments)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
		out["pictures"] = ok
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handleImprove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		gocommerce.RespondError(w, r, gocommerce.NotFoundf("no product %q", r.PathValue("id")))
		return
	}
	if err := m.ready(ctx); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	p, err := m.app.Products().GetProduct(ctx, id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(improveBudget)); err != nil {
		m.app.Log().Warn("could not extend the write deadline for an improve run", "error", err)
	}
	in, note, err := m.subject(ctx, p)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	res, err := m.cfg.Gateway.Run(ctx, improveTaskName, in)
	if err != nil {
		gocommerce.RespondError(w, r, refusal(err))
		return
	}
	var out improveOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read the proposal"))
		return
	}
	prop, err := m.propose(ctx, p, in, out)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	prop.PicturesNote = note
	prop.ExecutionID, prop.Model = res.ExecutionID, res.Model
	prop.CostMicroMinor, prop.Currency = res.CostMicroMinor, res.Currency
	gocommerce.Respond(w, http.StatusOK, prop)
}

// subject is the product as the model is shown it, and a note when it is
// shown fewer pictures than the product has.
func (m *Module) subject(ctx context.Context, p *gocommerce.Product) (improveInput, string, error) {
	in := improveInput{ProductID: p.ID, Product: listingOf(p)}
	cats, err := m.app.Categories().List(ctx)
	if err != nil {
		return in, "", err
	}
	for _, c := range cats[:min(len(cats), maxCategories)] {
		in.Categories = append(in.Categories, categoryRef{c.ID, c.FullName})
	}

	shared, err := m.cfg.Gateway.Allows(ctx, aigateway.DataDocuments)
	if err != nil {
		return in, "", err
	}
	if !shared {
		return in, "Pictures were not sent: this store keeps documents in. Switch on Share documents under " +
			"Settings › Plugins › AI gateway to include them.", nil
	}
	media, err := m.app.MediaLibrary().ForProduct(ctx, p.ID)
	if err != nil {
		return in, "", err
	}
	unread := 0
	for _, item := range media {
		if item.Kind != "image" || len(in.Pictures) == maxPictures {
			continue
		}
		b, err := m.app.MediaLibrary().Picture(ctx, item.ID, pictureWidth)
		if err != nil {
			m.app.Log().Info("a picture was left out of an improve run", "media", item.ID, "error", err)
			unread++
			continue
		}
		in.Pictures = append(in.Pictures, picture{MediaID: item.ID, Alt: item.Alt, MediaType: http.DetectContentType(b), Bytes: b})
	}
	switch {
	case unread == 1:
		return in, "One picture could not be read and was left out.", nil
	case unread > 1:
		return in, fmt.Sprintf("%d pictures could not be read and were left out.", unread), nil
	case len(in.Pictures) == 0:
		return in, "This product has no pictures, so the proposal is from its words alone.", nil
	}
	return in, "", nil
}

func listingOf(p *gocommerce.Product) listing {
	l := listing{
		Title: p.Title, Description: p.Description, Highlights: highlightsOf(p), Tags: p.Tags,
		ProductType: p.ProductType, Vendor: p.Vendor, Attributes: p.Metadata["category"],
		SEOTitle: p.SEOTitle, SEODescription: p.SEODescription,
	}
	if p.Category != nil {
		l.Category = p.Category.FullName
	}
	for _, o := range p.Options {
		opt := option{Name: o.Name}
		for _, v := range o.Values {
			opt.Values = append(opt.Values, v.Value)
		}
		l.Options = append(l.Options, opt)
	}
	return l
}

func highlightsOf(p *gocommerce.Product) []string {
	raw, _ := p.Metadata[gocommerce.MetaHighlights].([]any)
	out := []string{}
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// propose lays the answer beside the product: only what would change, held
// once more to what the catalogue takes, so a value the model repeated or a
// picture it made up never reaches the screen.
func (m *Module) propose(ctx context.Context, p *gocommerce.Product, in improveInput, o improveOutput) (*Proposal, error) {
	why := map[string]string{}
	for _, r := range o.Reasons {
		why[r.Field] = strings.TrimSpace(r.Reason)
	}
	prop := &Proposal{ProductID: p.ID, Changes: []Change{}, Verify: []string{}, Pictures: len(in.Pictures)}
	add := func(field string, current, proposed any) {
		prop.Changes = append(prop.Changes, Change{Field: field, Current: current, Proposed: proposed, Reason: why[field]})
	}
	text := func(field, current string, proposed *string) {
		if proposed != nil && oneLine(*proposed) != oneLine(current) {
			add(field, current, oneLine(*proposed))
		}
	}
	text("title", p.Title, o.Title)
	if o.Description != nil {
		if html := Paragraphs(*o.Description); html != p.Description {
			add("description", p.Description, html)
		}
	}
	if hl := trimmed(o.Highlights); len(hl) > 0 && !slices.Equal(hl, highlightsOf(p)) {
		add("highlights", highlightsOf(p), hl)
	}
	if tags := trimmed(o.Tags); len(tags) > 0 && !sameTags(tags, p.Tags) {
		add("tags", append([]string{}, p.Tags...), tags)
	}
	text("product_type", p.ProductType, o.ProductType)
	if o.CategoryID != nil && (p.Category == nil || p.Category.ID != *o.CategoryID) {
		c, err := m.app.Categories().Get(ctx, *o.CategoryID)
		if err != nil {
			return nil, err
		}
		var current any
		if p.Category != nil {
			current = categoryRef{p.Category.ID, p.Category.FullName}
		}
		add("category", current, categoryRef{c.ID, c.FullName})
	}
	if o.CategoryID == nil && o.SuggestedCategory != nil && p.Category == nil {
		prop.SuggestedCategory = oneLine(*o.SuggestedCategory)
	}
	text("seo_title", p.SEOTitle, o.SEOTitle)
	text("seo_description", p.SEODescription, o.SEODescription)
	for _, a := range o.AltTexts {
		if a.Picture < 1 || a.Picture > len(in.Pictures) {
			continue
		}
		pic := in.Pictures[a.Picture-1]
		if alt := oneLine(a.Alt); alt != oneLine(pic.Alt) {
			prop.Changes = append(prop.Changes, Change{Field: "alt", MediaID: pic.MediaID, Current: pic.Alt, Proposed: alt, Reason: why["alt_text"]})
		}
	}
	for _, v := range o.Verify {
		if v = oneLine(v); v != "" {
			prop.Verify = append(prop.Verify, v)
		}
	}
	return prop, nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func trimmed(list []string) []string {
	out := []string{}
	for _, s := range list {
		if s = oneLine(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// sameTags compares as the catalogue stores tags: a set, case folded.
func sameTags(a, b []string) bool {
	fold := func(list []string) []string {
		out := make([]string, 0, len(list))
		for _, s := range list {
			if s = strings.ToLower(strings.TrimSpace(s)); s != "" && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(fold(a), fold(b))
}
