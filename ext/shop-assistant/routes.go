package shopassistant

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	gocommerce "github.com/itswadesh/gocommerce/core"
	aigateway "github.com/itswadesh/gocommerce/ext/ai-gateway"
)

const (
	// maxMessages is ten turns, a shopper's message and its answer each.
	maxMessages     = 20
	maxMessageRunes = 1000
	writeDeadline   = 90 * time.Second

	// Byte budgets for what a turn sends, which is what bounds its cost for a
	// caller nobody knows. Bytes because the gateway's estimate charges three
	// bytes of Devanagari about what it charges a rune of it; with task.go's
	// instructions they stay under MaxInputTokens in any script.
	maxConversationBytes = 6000
	maxFacetBytes        = 6000
	maxFAQBytes          = 12000
	maxFAQAnswerRunes    = 500

	maxReplyRunes = 400
	maxCards      = 6
	maxFAQs       = 3
	maxQueryWords = 4
	// candidates is how many products one search reads before its words are
	// checked. ponytail: past this a shop's older matches are not looked at;
	// page through them if a shop's results ever feel thin.
	candidates = 100

	codeOff         = "shop_assistant_off"
	codeUnavailable = "shop_assistant_unavailable"
	codeLimited     = "rate_limited"
)

// The server's own sentences, used wherever the model's cannot be: nothing the
// model said can make them claim a product exists.
const (
	replyFound     = "Here is what I found."
	replyNoMatch   = "I could not find that in this shop. Try other words, or ask the shop."
	replyAnswers   = "This should help."
	replyFallback  = "I can look for products and answer questions about this shop. What are you looking for?"
	sayUnavailable = "The shop's assistant can't answer just now. Please try again later, or contact the shop."
)

// Message is one line of the conversation, which the browser holds.
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Card is a product as core's catalogue has it, field by field: this is what
// keeps a cost, a draft or a model's words out of it. VariantID is set only when
// one press of Add to cart can be right; otherwise the storefront opens the
// product.
type Card struct {
	ProductID int64             `json:"product_id"`
	Slug      string            `json:"slug"`
	Title     string            `json:"title"`
	ImageURL  string            `json:"image_url,omitempty"`
	Price     gocommerce.Money  `json:"price"`
	CompareAt *gocommerce.Money `json:"compare_at_price,omitempty"`
	VariantID *int64            `json:"variant_id"`
}

// FAQ is one of the store's answers, in the store's own words.
type FAQ struct {
	ID       int64  `json:"id"`
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// TurnResult answers one message. Closest says the cards match only part of what
// was asked for. Handover says to offer the shop's contact form, and is true only
// while the store has one.
type TurnResult struct {
	Reply    string `json:"reply"`
	Products []Card `json:"products"`
	Closest  bool   `json:"closest"`
	FAQs     []FAQ  `json:"faqs"`
	Handover bool   `json:"handover"`
}

func refuseUnavailable() error {
	return &gocommerce.APIError{Status: http.StatusConflict, Code: codeUnavailable, Message: sayUnavailable}
}

// usable says whether this store's assistant can answer. The reasons are the
// owner's, not a shopper's, so none of them reaches the shopper: what the store
// has switched off, what credit it has left and what it shares are not their
// business.
func (m *Module) usable(ctx context.Context) (on, ready bool, err error) {
	if on, err = m.app.Plugins().Enabled(ctx, PluginKey); err != nil || !on || m.cfg.Gateway == nil {
		return on, false, err
	}
	if err := m.cfg.Gateway.Ready(ctx); err != nil {
		if errors.Is(err, aigateway.ErrNotReady) {
			return true, false, nil
		}
		return true, false, err
	}
	allowed, err := m.cfg.Gateway.Allows(ctx, aigateway.DataMessageBodies)
	return true, allowed, err
}

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	on, ready, err := m.usable(r.Context())
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"enabled": on && ready})
}

func (m *Module) handleTurn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	on, ready, err := m.usable(ctx)
	switch {
	case err != nil:
		gocommerce.RespondError(w, r, err)
		return
	case !on:
		gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusNotFound, Code: codeOff, Message: "this store has no shop assistant"})
		return
	case !ready:
		gocommerce.RespondError(w, r, refuseUnavailable())
		return
	}
	var in struct {
		Messages []Message `json:"messages"`
		Channel  string    `json:"channel"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := checkMessages(in.Messages); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// The visitor first, so one visitor past their limit costs no database write
	// and cannot use up the store's day by asking on.
	store, _ := gocommerce.StoreFrom(ctx)
	if wait, ok := m.visitors.allow(visitorOf(store, r.RemoteAddr), time.Now()); !ok {
		respondLimited(w, r, wait, "That's a lot of questions in a few minutes. Wait a little and ask again.")
		return
	}
	if ok, err := m.countTurn(ctx); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	} else if !ok {
		now := time.Now().UTC()
		respondLimited(w, r, now.Truncate(24*time.Hour).Add(24*time.Hour).Sub(now),
			"The shop's assistant has answered all it can today. Please try again tomorrow, or contact the shop.")
		return
	}

	query := gocommerce.ProductQuery{Status: gocommerce.ProductActive}
	channel, err := m.app.Channels().Resolve(ctx, in.Channel)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if channel != nil {
		query.ChannelID = channel.ID
	}
	facets, err := m.app.Products().ProductFacets(ctx, query)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	faqs, err := m.faqs(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	profile, err := m.app.Profile().Get(ctx)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}

	k := newMasker()
	input := turnInput{
		Conversation: trimConversation(in.Messages, k),
		Store:        storeFact{Name: profile.Name, Currency: m.app.Currency(ctx)},
		Facets:       facetsFor(facets),
		FAQs:         faqFacts(faqs),
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil {
		m.app.Log().Warn("could not extend the write deadline for a shop-assistant turn", "error", err)
	}
	// Detached from the request, so a closed tab still leaves the run's record
	// and its true cost. No transaction is open across this (rule 5).
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeDeadline)
	defer cancel()
	res, err := m.cfg.Gateway.Run(runCtx, taskName, input)
	if err != nil {
		// Every way this ends without an answer is the store's to look at on its
		// AI usage screen, which has the run's record. Credit left, the monthly
		// cap and the provider's words are not a shopper's to read.
		m.app.Log().Warn("a shop-assistant turn had no answer", "error", err)
		gocommerce.RespondError(w, r, refuseUnavailable())
		return
	}
	var out turnOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		gocommerce.RespondError(w, r, refuseUnavailable())
		return
	}

	result := TurnResult{Products: []Card{}, FAQs: keepFAQs(out.FAQIDs, faqs), Handover: out.Handover}
	filters := keepFilters(out.Filters, facets, m.app.Currency(ctx), query)
	searched := out.Filters.any()
	if filters.usable() {
		result.Products, result.Closest, err = m.search(r, filters)
		if err != nil {
			gocommerce.RespondError(w, r, err)
			return
		}
	}
	result.Reply = chooseReply(out.Reply, k, searched, len(result.Products), len(result.FAQs))
	if searched && len(result.Products) == 0 && len(result.FAQs) == 0 {
		result.Handover = true
	}
	if result.Handover {
		// The offer exists only while the form does.
		result.Handover, _ = m.app.Plugins().Enabled(ctx, "contact-form")
	}
	gocommerce.Respond(w, http.StatusOK, result)
}

func respondLimited(w http.ResponseWriter, r *http.Request, wait time.Duration, message string) {
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusTooManyRequests, Code: codeLimited, Message: message})
}

// checkMessages holds the conversation the browser sent to its limits.
func checkMessages(list []Message) error {
	if len(list) == 0 || len(list) > maxMessages {
		return gocommerce.Validationf("send between 1 and %d messages", maxMessages)
	}
	if list[len(list)-1].Role != "user" {
		return gocommerce.Validationf("the last message is the shopper's")
	}
	for _, msg := range list {
		if msg.Role != "user" && msg.Role != "assistant" {
			return gocommerce.Validationf("a message's role is user or assistant")
		}
		if strings.TrimSpace(msg.Text) == "" {
			return gocommerce.Validationf("a message cannot be empty")
		}
		if utf8.RuneCountInString(msg.Text) > maxMessageRunes {
			return gocommerce.Validationf("a message is at most %d characters", maxMessageRunes)
		}
	}
	return nil
}

// trimConversation masks each message and keeps the newest that fit the budget:
// the last always, because a whole message is at most 4,000 bytes. A long chat
// forgets its start rather than costing more per turn the longer it goes on.
func trimConversation(list []Message, k *masker) []Message {
	masked := make([]Message, len(list))
	for i, msg := range list {
		masked[i] = Message{Role: msg.Role, Text: k.mask(msg.Text)}
	}
	used, from := 0, len(masked)
	for from > 0 && used+len(masked[from-1].Text) <= maxConversationBytes {
		from--
		used += len(masked[from].Text)
	}
	return masked[from:]
}

// facetsFor is the store's shape, each group commonest first as core counted it,
// with whatever is past the budget left off.
func facetsFor(f *gocommerce.ProductFacets) facetList {
	budget := maxFacetBytes
	take := func(s string) bool {
		if len(s)+4 > budget {
			return false
		}
		budget -= len(s) + 4
		return true
	}
	out := facetList{Categories: []categoryFact{}, ProductTypes: []string{}, Vendors: []string{}, Tags: []string{}, Attributes: map[string][]string{}}
	for _, c := range f.Categories {
		if take(c.Slug + c.Title + `{"slug":"","title":""}`) {
			out.Categories = append(out.Categories, categoryFact{c.Slug, c.Title})
		}
	}
	values := func(from []gocommerce.FacetValue) []string {
		list := []string{}
		for _, v := range from {
			if take(v.Value) {
				list = append(list, v.Value)
			}
		}
		return list
	}
	out.ProductTypes = values(f.ProductTypes)
	out.Vendors = values(f.Vendors)
	out.Tags = values(f.Tags)
	for key, list := range f.Attributes {
		if take(key) {
			out.Attributes[key] = values(list)
		}
	}
	return out
}

// faqs reads the store's published answers, or none without the FAQ module.
func (m *Module) faqs(ctx context.Context) ([]FAQ, error) {
	out := []FAQ{}
	if m.cfg.FAQ == nil {
		return out, nil
	}
	entries, err := m.cfg.FAQ.Published(ctx)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		out = append(out, FAQ{e.ID, e.Question, e.Answer})
	}
	return out, nil
}

// faqFacts is what the model reads of the answers: each one cut short, because
// the shopper is shown the whole of it from the table and the model only has to
// recognise it. Entries past the budget are not offered.
func faqFacts(faqs []FAQ) []faqFact {
	out := []faqFact{}
	budget := maxFAQBytes
	for _, f := range faqs {
		answer := f.Answer
		if utf8.RuneCountInString(answer) > maxFAQAnswerRunes {
			answer = string([]rune(answer)[:maxFAQAnswerRunes])
		}
		cost := len(f.Question) + len(answer) + 40
		if cost > budget {
			break
		}
		budget -= cost
		out = append(out, faqFact{f.ID, f.Question, answer})
	}
	return out
}

// keepFAQs answers with only entries that exist and are published, in the store's
// own words, so an id the model invented is simply absent.
func keepFAQs(ids []int64, faqs []FAQ) []FAQ {
	out := []FAQ{}
	seen := map[int64]bool{}
	for _, id := range ids {
		if seen[id] || len(out) == maxFAQs {
			continue
		}
		seen[id] = true
		if i := slices.IndexFunc(faqs, func(f FAQ) bool { return f.ID == id }); i >= 0 {
			out = append(out, faqs[i])
		}
	}
	return out
}

// checkedFilters is what survived: a catalogue query holding only values that
// exist, and the words to look for.
type checkedFilters struct {
	query  gocommerce.ProductQuery
	words  []string
	narrow bool
}

// usable is whether anything survived. With nothing, there is no search at all:
// a model's invented value must not turn into the whole catalogue.
func (f checkedFilters) usable() bool { return f.narrow || len(f.words) > 0 }

// any says whether the model meant to search, whatever survives of it.
func (in modelFilters) any() bool {
	return strings.TrimSpace(in.Query) != "" || in.Category != "" || len(in.ProductTypes) > 0 || len(in.Vendors) > 0 ||
		len(in.Tags) > 0 || len(in.Attributes) > 0 || in.PriceMin != nil || in.PriceMax != nil
}

// keepFilters holds every value the model named to the store's own facets. The
// spelling used is the store's, so a case difference still matches core's exact
// comparison, and an invented value is dropped rather than emptying the listing.
func keepFilters(in modelFilters, f *gocommerce.ProductFacets, currency string, base gocommerce.ProductQuery) checkedFilters {
	out := checkedFilters{query: base, words: queryWords(in.Query)}
	if i := slices.IndexFunc(f.Categories, func(c gocommerce.CategoryFacet) bool { return c.Slug == in.Category }); i >= 0 {
		out.query.CategoryID = f.Categories[i].ID
		out.narrow = true
	}
	known := func(asked []string, values []gocommerce.FacetValue) []string {
		var kept []string
		for _, v := range asked {
			i := slices.IndexFunc(values, func(fv gocommerce.FacetValue) bool { return strings.EqualFold(fv.Value, strings.TrimSpace(v)) })
			if i >= 0 && !slices.Contains(kept, values[i].Value) {
				kept = append(kept, values[i].Value)
				out.narrow = true
			}
		}
		return kept
	}
	out.query.ProductTypes = known(in.ProductTypes, f.ProductTypes)
	out.query.Vendors = known(in.Vendors, f.Vendors)
	out.query.Tags = known(in.Tags, f.Tags)
	for _, a := range in.Attributes {
		values, ok := f.Attributes[a.Key]
		if !ok {
			continue
		}
		if kept := known(a.Values, values); len(kept) > 0 {
			out.query.Attributes = append(out.query.Attributes, gocommerce.AttributeFilter{Key: a.Key, Values: kept})
		}
	}
	lo, hi := toMinor(in.PriceMin, currency), toMinor(in.PriceMax, currency)
	if lo != nil && hi != nil && *lo > *hi {
		lo, hi = nil, nil
	}
	out.query.PriceMinMinor, out.query.PriceMaxMinor = lo, hi
	if lo != nil || hi != nil {
		out.narrow = true
	}
	return out
}

// toMinor turns whole units of the currency into minor units. The model is asked
// for whole units because that is what a shopper says, and multiplying here is
// one mistake it cannot make. Anything nonsensical is no bound at all.
func toMinor(whole *int64, currency string) *int64 {
	if whole == nil || *whole < 0 || *whole > 1_000_000_000 {
		return nil
	}
	v := *whole
	for range gocommerce.CurrencyExponent(currency) {
		v *= 10
	}
	return &v
}

// queryWords splits what the model suggests searching for. A word holding one of
// the masker's placeholders is the shopper's phone number, not something sold.
func queryWords(q string) []string {
	var out []string
	for _, w := range strings.Fields(strings.ToLower(q)) {
		w = strings.Trim(w, `.,!?;:"'()`)
		if w == "" || strings.ContainsAny(w, "[]") || slices.Contains(out, singular(w)) {
			continue
		}
		out = append(out, singular(w))
		if len(out) == maxQueryWords {
			break
		}
	}
	return out
}

// singular takes a plural back to what a title more often says: core's search is
// a substring, so "saree" finds "sarees" and not the other way round.
func singular(w string) string {
	for _, end := range []string{"sses", "shes", "ches", "xes"} {
		if strings.HasSuffix(w, end) {
			return strings.TrimSuffix(w, "es")
		}
	}
	if strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss") && utf8.RuneCountInString(w) > 3 {
		return strings.TrimSuffix(w, "s")
	}
	return w
}

// search asks the core catalogue, the service behind GET /api/products.
//
// Core's search is one substring over a title and description, which is too
// loose and too strict for what a shopper means. "red kurta" as a phrase finds
// "embroidered kurta" and misses "Kurta in red". So the first word goes to core
// and each product's own text is then held to every word at the start of a word.
// When nothing has them all, the last word goes, then the next, and the cards say
// they are the closest: the word that goes is the one the model ranked lowest.
// Products that say it in their title come first, because there are only six
// cards, and a jacket whose description mentions a kurta should not take a kurta's.
func (m *Module) search(r *http.Request, f checkedFilters) ([]Card, bool, error) {
	ctx := r.Context()
	q := f.query
	q.Limit = maxCards
	if len(f.words) == 0 {
		products, _, err := m.app.Products().ListProducts(ctx, q)
		if err != nil {
			return nil, false, err
		}
		m.app.TranslateProducts(r, products)
		return cards(products, m.app.Currency(ctx)), false, nil
	}
	q.Limit = candidates
	q.Search = f.words[0]
	products, _, err := m.app.Products().ListProducts(ctx, q)
	if err != nil {
		return nil, false, err
	}
	for keep := len(f.words); keep > 0; keep-- {
		var titled, described []*gocommerce.Product
		for _, p := range products {
			switch words := f.words[:keep]; {
			case hasAll(p.Title, words):
				titled = append(titled, p)
			case hasAll(p.Title+"\n"+p.Description, words):
				described = append(described, p)
			}
		}
		hits := append(titled, described...)
		if len(hits) > 0 {
			hits = hits[:min(len(hits), maxCards)]
			m.app.TranslateProducts(r, hits)
			return cards(hits, m.app.Currency(ctx)), keep < len(f.words), nil
		}
	}
	return []Card{}, false, nil
}

func hasAll(text string, words []string) bool {
	text = strings.ToLower(text)
	return !slices.ContainsFunc(words, func(w string) bool { return !hasWord(text, w) })
}

// hasWord says whether word starts a word in text. Written out because Go's \b
// knows only ASCII, and a shop may sell in Hindi.
func hasWord(text, word string) bool {
	for at := 0; ; {
		i := strings.Index(text[at:], word)
		if i < 0 {
			return false
		}
		i += at
		before, _ := utf8.DecodeLastRuneInString(text[:i])
		if i == 0 || !(unicode.IsLetter(before) || unicode.IsDigit(before)) {
			return true
		}
		at = i + len(word)
	}
}

// cards draws each product from core's own row. A price is the cheapest active
// variant, the same "from" a listing shows.
func cards(products []*gocommerce.Product, currency string) []Card {
	out := []Card{}
	for _, p := range products {
		var cheapest *gocommerce.Variant
		active := 0
		for i := range p.Variants {
			v := &p.Variants[i]
			if !v.Active {
				continue
			}
			active++
			if cheapest == nil || v.Price.AmountMinor < cheapest.Price.AmountMinor {
				cheapest = v
			}
		}
		if cheapest == nil {
			continue
		}
		card := Card{ProductID: p.ID, Slug: p.Slug, Title: p.Title, ImageURL: p.ImageURL, Price: cheapest.Price, CompareAt: cheapest.CompareAtPrice}
		if card.Price.Currency == "" {
			card.Price.Currency = currency
		}
		// One press of Add to cart is right only with nothing left to choose.
		if active == 1 && !p.Designable && cheapest.InStock(1) {
			id := cheapest.ID
			card.VariantID = &id
		}
		out = append(out, card)
		if len(out) == maxCards {
			break
		}
	}
	return out
}

// What a reply may not carry. The rule is shapes rather than words, and it errs
// towards dropping a good sentence: the catalogue and the FAQ cards say everything
// a number could, so a reply loses nothing it needs. A number of three digits or
// more is a price, a phone number or an invention; so is a money sign beside any
// number, in any script's digits. Markup is refused too, as the widget would show it as stray text.
var (
	unsafeLinkRE  = regexp.MustCompile(`(?i)[a-z][a-z0-9+.\-]*://|\bwww\.|\]\(|\b[a-z0-9\-]+\.(com|in|net|org|io|co|shop|store|app|xyz|link|me|ly|info|biz)\b`)
	unsafePriceRE = regexp.MustCompile(`(?i)\p{Nd}[\p{Nd},.]{2,}|(₹|\$|€|£|\brs\.?|\binr\b|\busd\b|\brupees?\b|\bdollars?\b)\s*\p{Nd}|\p{Nd}\s*(₹|\$|€|£|rs\b|inr\b|usd\b|rupees?\b|dollars?\b|/-)|\b(price[sd]?|costs?|only|just)\b\P{Nd}{0,12}\p{Nd}`)
	unsafeHTMLRE  = regexp.MustCompile(`<[a-zA-Z/!]`)
)

// chooseReply decides what the shopper reads. The model's sentence stands unless
// it breaks a rule above; the server's own takes over when nothing was found,
// because a model that never sees the results cannot know that.
func chooseReply(reply string, k *masker, searched bool, products, faqs int) string {
	reply = strings.TrimSpace(reply)
	if searched && products == 0 && faqs == 0 {
		return replyNoMatch
	}
	if unsafeLinkRE.MatchString(reply) || unsafePriceRE.MatchString(reply) || unsafeHTMLRE.MatchString(reply) {
		switch {
		case products > 0:
			return replyFound
		case faqs > 0:
			return replyAnswers
		}
		return replyFallback
	}
	if utf8.RuneCountInString(reply) > maxReplyRunes {
		reply = strings.TrimSpace(string([]rune(reply)[:maxReplyRunes])) + "…"
	}
	return k.unmask(reply)
}
