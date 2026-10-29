package reviews

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
	"unicode/utf8"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

// Review summaries (BI-03, D153): what shoppers praise and what they complain
// about in one product's published reviews, as themes that each cite the
// reviews behind them. The model reads the reviews' words and ratings only;
// the average, the counts and the order of the themes are worked out here.

const (
	summarizeTask = "reviews.summarize"
	// minReviews is the fewest published reviews a summary reads. Below it a
	// theme is one shopper's opinion, so no model is asked.
	minReviews = 3
	// A summary reads the newest reviews: at least minReviews, then as many
	// as fit in maxReadBytes of the JSON they are sent as, up to
	// maxReviewsRead. The gateway counts a byte of that JSON as 0.75 of a
	// token at the most (a two-byte character is 1.5), so the request stays
	// under the task's MaxInputTokens whatever the script.
	maxReviewsRead = 50
	maxReadBytes   = 32000
	// A review's title has no limit of its own where it is posted.
	maxTitleRunes = 200
	maxThemes     = 8
	maxLabelRunes = 120
)

// The codes a summary that could not be made answers with, so the panel can
// say what to do about each.
const (
	CodeAIUnavailable    = "ai_unavailable"
	CodeAINotReady       = "ai_not_ready"
	CodeAIDataKeptIn     = "ai_data_kept_in"
	CodeAIBudgetExceeded = "ai_budget_exhausted"
	CodeAICreditExceeded = "ai_credit_exhausted"
	CodeAIFailed         = "ai_failed"
)

// The states a summary answers in.
const (
	StateSummarized = "summarized"
	StateTooFew     = "too_few"
)

// Digest is a product's published reviews summarized.
type Digest struct {
	ProductID int64  `json:"product_id"`
	Product   string `json:"product"`
	State     string `json:"state"`
	// Rating is every published review in numbers, not only those read.
	Rating *Summary `json:"rating"`
	// Read is how many of the newest reviews the summary read.
	Read       int           `json:"read"`
	Praise     []Theme       `json:"praise"`
	Complaints []Theme       `json:"complaints"`
	Reviews    []CitedReview `json:"reviews"`
	// Outcome is the gateway's: ok, cached or deterministic.
	Outcome        string `json:"outcome"`
	ExecutionID    int64  `json:"execution_id"`
	CostMicroMinor int64  `json:"cost_micro_minor"`
	Currency       string `json:"currency"`
}

// Theme is one thing shoppers say. Count is the distinct reviews it cites.
type Theme struct {
	Topic     string  `json:"topic"`
	Label     string  `json:"label"`
	Count     int     `json:"count"`
	ReviewIDs []int64 `json:"review_ids"`
}

// CitedReview is a review a theme cites, as the panel lists it.
type CitedReview struct {
	ID     int64  `json:"id"`
	Rating int    `json:"rating"`
	Title  string `json:"title,omitempty"`
	Body   string `json:"body"`
}

// summarizeInput is the task's input, and so its cache key: an unchanged set
// of reviews is answered from the cache.
type summarizeInput struct {
	ProductID int64         `json:"product_id"`
	Product   string        `json:"product"`
	Reviews   []CitedReview `json:"reviews"`
}

type themeAnswer struct {
	Topic     string  `json:"topic"`
	Label     string  `json:"label"`
	ReviewIDs []int64 `json:"review_ids"`
}

type summarizeAnswer struct {
	Praise     []themeAnswer `json:"praise"`
	Complaints []themeAnswer `json:"complaints"`
}

const summarizeInstructions = `You summarize what shoppers say in a product's reviews, for the shop that sells it.

The reviews are a JSON list: each has an id, a rating from 1 to 5, an optional title and a body. Find the themes in them: what shoppers praise, and what they complain about.

- topic is one of: fit (size and fit), quality (how it is made and how it lasts), colour (the colour or look against the photos), delivery (shipping, packaging, arrival), value (the price for what it is), other (anything else that comes up more than once).
- label is a short plain phrase the shop can scan, such as "Runs a size small" or "Soft, thick fabric": at most 12 words, sentence case, no exclamation marks, in the language most of the reviews are written in.
- review_ids is every review that says it, by its id. Cite only ids from the list. A theme no review says is not a theme.
- Prefer what several reviews share. At most 8 praise themes and 8 complaint themes.
- A mixed review may support a praise and a complaint.
- Report what shoppers say, not your own view, and give no advice.
- When nothing is praised, or nothing is complained about, that list is empty.`

var themeSchema = `{
  "type": "object", "additionalProperties": false, "required": ["topic", "label", "review_ids"],
  "properties": {
    "topic": {"type": "string", "enum": ["fit", "quality", "colour", "delivery", "value", "other"]},
    "label": {"type": "string"},
    "review_ids": {"type": "array", "items": {"type": "integer"}}
  }
}`

var summarizeSchema = json.RawMessage(`{
  "type": "object", "additionalProperties": false, "required": ["praise", "complaints"],
  "properties": {
    "praise": {"type": "array", "items": ` + themeSchema + `},
    "complaints": {"type": "array", "items": ` + themeSchema + `}
  }
}`)

// sentKey carries the ids a run sent to its Check, which sees only the
// answer.
type sentKey struct{}

func summarizeTaskDef() aigateway.Task {
	return aigateway.Task{
		Name:            summarizeTask,
		Version:         1,
		System:          summarizeInstructions,
		Schema:          summarizeSchema,
		MaxInputTokens:  30000,
		MaxOutputTokens: 3000,
		Ladder:          []aigateway.Tier{aigateway.TierSmall, aigateway.TierStrong},
		// The answer stands until a review is added, edited, published or
		// withdrawn, each of which changes the input.
		CacheFor: 30 * 24 * time.Hour,
		Deterministic: func(_ context.Context, raw json.RawMessage) (json.RawMessage, bool, error) {
			var in summarizeInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return nil, false, err
			}
			if len(in.Reviews) >= minReviews {
				return nil, false, nil
			}
			return json.RawMessage(`{"praise": [], "complaints": []}`), true, nil
		},
		Build: func(_ context.Context, raw json.RawMessage, _ aigateway.Policy) (aigateway.Request, []aigateway.SourceRef, error) {
			var in summarizeInput
			if err := json.Unmarshal(raw, &in); err != nil {
				return aigateway.Request{}, nil, err
			}
			refs := []aigateway.SourceRef{{Kind: "product", ID: strconv.FormatInt(in.ProductID, 10)}}
			for _, rv := range in.Reviews {
				refs = append(refs, aigateway.SourceRef{Kind: "review", ID: strconv.FormatInt(rv.ID, 10)})
			}
			return aigateway.Request{Messages: []aigateway.Message{{
				Role: "user",
				Text: fmt.Sprintf("Summarize these %d reviews.", len(in.Reviews)),
				Data: []aigateway.Data{
					{Class: aigateway.DataCatalogue, Label: "product", Value: in.Product},
					// A shopper wrote these words, so they are a message body:
					// published on the storefront is not leave to send them to
					// a model provider.
					{Class: aigateway.DataMessageBodies, Label: "reviews", Value: in.Reviews, Required: true},
				},
			}}}, refs, nil
		},
		Check: func(ctx context.Context, out json.RawMessage) (int, error) {
			sent, _ := ctx.Value(sentKey{}).(map[int64]bool)
			if sent == nil {
				return 0, errors.New("the reviews this run sent are not known, so its citations cannot be checked")
			}
			a, err := decodeAnswer(out)
			if err != nil {
				return 0, err
			}
			return 100, checkAnswer(a, sent)
		},
	}
}

// decodeAnswer reads an answer the gateway has already held to the schema.
func decodeAnswer(out json.RawMessage) (*summarizeAnswer, error) {
	var a summarizeAnswer
	if err := json.Unmarshal(out, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

// checkAnswer holds an answer to its grounding: every theme cites at least
// one review, and only reviews that were sent.
func checkAnswer(a *summarizeAnswer, sent map[int64]bool) error {
	if err := checkThemes("praise", a.Praise, sent); err != nil {
		return err
	}
	return checkThemes("complaint", a.Complaints, sent)
}

func checkThemes(side string, themes []themeAnswer, sent map[int64]bool) error {
	if len(themes) > maxThemes {
		return fmt.Errorf("%d %s themes; at most %d", len(themes), side, maxThemes)
	}
	for _, th := range themes {
		label := strings.TrimSpace(th.Label)
		if label == "" || utf8.RuneCountInString(label) > maxLabelRunes {
			return fmt.Errorf("a %s theme's label is empty or over %d characters", side, maxLabelRunes)
		}
		if len(th.ReviewIDs) == 0 {
			return fmt.Errorf("the %s theme %q cites no review", side, label)
		}
		for _, id := range th.ReviewIDs {
			if !sent[id] {
				return fmt.Errorf("the %s theme %q cites review %d, which was not sent", side, label, id)
			}
		}
	}
	return nil
}

// readable is the newest published reviews a summary reads, as the model is
// sent them: no name, no email.
func (m *Module) readable(ctx context.Context, productID int64) ([]CitedReview, error) {
	rows, err := m.db.QueryContext(ctx, `
		SELECT id, rating, title, body FROM reviews
		WHERE product_id = $1 AND status = 'approved'
		ORDER BY created_at DESC, id DESC LIMIT $2`, productID, maxReviewsRead)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CitedReview
	used := 0
	for rows.Next() {
		var rv CitedReview
		if err := rows.Scan(&rv.ID, &rv.Rating, &rv.Title, &rv.Body); err != nil {
			return nil, err
		}
		if t := []rune(rv.Title); len(t) > maxTitleRunes {
			rv.Title = string(t[:maxTitleRunes])
		}
		b, err := json.Marshal(rv)
		if err != nil {
			return nil, err
		}
		if len(out) >= minReviews && used+len(b) > maxReadBytes {
			break
		}
		used += len(b)
		out = append(out, rv)
	}
	return out, rows.Err()
}

// summarize reads a product's published reviews through the gateway.
func (m *Module) summarize(ctx context.Context, productID int64) (*Digest, error) {
	if m.cfg.Gateway == nil {
		return nil, aiRefusal(http.StatusConflict, CodeAIUnavailable,
			"this store was built without the AI gateway (-ai-gateway), which review summaries run through")
	}
	product, err := m.app.Products().GetProduct(ctx, productID)
	if err != nil {
		return nil, err
	}
	rating, err := m.summary(ctx, productID)
	if err != nil {
		return nil, err
	}
	reviews, err := m.readable(ctx, productID)
	if err != nil {
		return nil, err
	}
	sent := make(map[int64]bool, len(reviews))
	for _, rv := range reviews {
		sent[rv.ID] = true
	}
	// Not cancelled with the request: a person who closes the drawer has
	// still paid for the call, and its answer is recorded and cached for the
	// next time they ask.
	runCtx := context.WithValue(context.WithoutCancel(ctx), sentKey{}, sent)
	res, err := m.cfg.Gateway.Run(runCtx, summarizeTask, summarizeInput{ProductID: productID, Product: product.Title, Reviews: reviews})
	if err != nil {
		return nil, summaryError(err)
	}
	a, err := decodeAnswer(res.Output)
	if err != nil {
		return nil, gocommerce.Internalf(err, "read the summary")
	}
	d := &Digest{
		ProductID: productID, Product: product.Title, State: StateSummarized, Rating: rating, Read: len(reviews),
		Praise: themes(a.Praise), Complaints: themes(a.Complaints), Reviews: []CitedReview{},
		Outcome: res.Outcome, ExecutionID: res.ExecutionID, CostMicroMinor: res.CostMicroMinor, Currency: res.Currency,
	}
	// The deterministic step answers only when there are too few to read.
	if res.Outcome == aigateway.OutcomeDeterministic {
		d.State = StateTooFew
	}
	cited := map[int64]bool{}
	for _, th := range slices.Concat(d.Praise, d.Complaints) {
		for _, id := range th.ReviewIDs {
			cited[id] = true
		}
	}
	for _, rv := range reviews {
		if cited[rv.ID] {
			d.Reviews = append(d.Reviews, rv)
		}
	}
	return d, nil
}

// themes counts each theme's distinct reviews and puts the most cited first.
func themes(in []themeAnswer) []Theme {
	out := make([]Theme, 0, len(in))
	for _, th := range in {
		var ids []int64
		for _, id := range th.ReviewIDs {
			if !slices.Contains(ids, id) {
				ids = append(ids, id)
			}
		}
		out = append(out, Theme{Topic: th.Topic, Label: strings.TrimSpace(th.Label), Count: len(ids), ReviewIDs: ids})
	}
	slices.SortStableFunc(out, func(a, b Theme) int { return b.Count - a.Count })
	return out
}

func aiRefusal(status int, code, message string) *gocommerce.APIError {
	return &gocommerce.APIError{Status: status, Code: code, Message: message}
}

// summaryError says why a summary could not be made, in words for the shop,
// under a code the panel answers with what to do next.
func summaryError(err error) error {
	const settings = "Settings › Plugins › AI gateway"
	if errors.Is(err, aigateway.ErrNotReady) {
		return aiRefusal(http.StatusConflict, CodeAINotReady, err.Error())
	}
	var re *aigateway.RunError
	if !errors.As(err, &re) {
		return err
	}
	switch re.Outcome {
	case aigateway.OutcomePolicyBlocked:
		return aiRefusal(http.StatusConflict, CodeAIDataKeptIn,
			"this store keeps customers' messages in, and a summary needs the reviews' words: switch on Share message bodies under "+settings)
	case aigateway.OutcomeCreditExhausted:
		return aiRefusal(http.StatusConflict, CodeAICreditExceeded, re.Reason)
	case aigateway.OutcomeBudgetExhausted:
		return aiRefusal(http.StatusConflict, CodeAIBudgetExceeded,
			"this month's AI cap cannot cover a summary: raise the monthly cap under "+settings+", or wait for next month")
	case aigateway.OutcomeNoPrice:
		return aiRefusal(http.StatusConflict, CodeAINotReady, re.Reason+": set it under "+settings)
	}
	return aiRefusal(http.StatusBadGateway, CodeAIFailed, "the summary could not be made: "+re.Reason)
}

func (m *Module) handleSummarize(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	d, err := m.summarize(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, d)
}
