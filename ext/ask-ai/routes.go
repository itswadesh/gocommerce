package askai

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	gocommerce "github.com/misiki-in/gocommerce/core"
	aigateway "github.com/misiki-in/gocommerce/ext/ai-gateway"
)

const (
	// maxMessages is ten turns, a person's message and its answer each.
	maxMessages = 20
	// maxMessageRunes bounds one message either way; the panel's box stops at
	// 1,000 and trims an answer it sends back to this.
	maxMessageRunes = 2000
	maxScreens      = 200
	writeDeadline   = 2 * time.Minute

	codeNotReady = "ai_not_ready"
	codeCredit   = "ai_credit_exhausted"
	codeBudget   = "ai_budget_exhausted"
	codePolicy   = "ai_policy_blocked"
	codeProvider = "ai_provider_failed"
	codeNoAnswer = "ai_no_answer"
)

// Message is one line of the conversation, which the browser holds.
type Message struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// Screen is a screen of the panel the person can reach.
type Screen struct {
	Href  string `json:"href"`
	Label string `json:"label"`
}

// Link is a link an answer offers, already on the allowlist.
type Link struct {
	Label string `json:"label"`
	Href  string `json:"href"`
}

// ProposalView is a filed proposal as the panel draws it.
type ProposalView struct {
	ID          int64           `json:"id"`
	PreviewHash string          `json:"preview_hash"`
	Changes     []PreviewChange `json:"changes"`
	Warnings    []string        `json:"warnings"`
}

// TurnResult answers one question. Notice says why something asked for could
// not be proposed.
type TurnResult struct {
	Answer         string        `json:"answer"`
	Links          []Link        `json:"links"`
	Proposal       *ProposalView `json:"proposal"`
	Notice         string        `json:"notice,omitempty"`
	CostMicroMinor int64         `json:"cost_micro_minor"`
	Currency       string        `json:"currency"`
}

// screenHrefRE is an address inside the panel: no scheme, no host, no way out.
var screenHrefRE = regexp.MustCompile(`^/dash(/[A-Za-z0-9_\-/]*)?$`)

// plainRefusal says why there is no answer in the words a shop owner reads.
// These are the engine's own sentences, so no provider's error text or model
// name reaches the screen. The one taken from the gateway is the credit's: it
// tells a trial spent from a top-up spent from credit stranded by a currency.
// All are 409: the envelope replaces the words of any 5xx with "internal error".
func plainRefusal(err error) error {
	if errors.Is(err, aigateway.ErrNotReady) {
		return &gocommerce.APIError{Status: http.StatusConflict, Code: codeNotReady,
			Message: "AI isn't set up for this store yet, so Ask AI can't answer."}
	}
	var re *aigateway.RunError
	if !errors.As(err, &re) {
		return err
	}
	switch re.Outcome {
	case aigateway.OutcomeCreditExhausted:
		return &gocommerce.APIError{Status: http.StatusConflict, Code: codeCredit, Message: re.Reason}
	case aigateway.OutcomeBudgetExhausted:
		return &gocommerce.APIError{Status: http.StatusConflict, Code: codeBudget,
			Message: "This month's AI spending cap is used up, so Ask AI can't answer until next month or until the cap is raised."}
	case aigateway.OutcomeNoPrice:
		return &gocommerce.APIError{Status: http.StatusConflict, Code: codeNotReady,
			Message: "AI isn't set up for this store yet, so Ask AI can't answer."}
	case aigateway.OutcomePolicyBlocked:
		return &gocommerce.APIError{Status: http.StatusConflict, Code: codePolicy,
			Message: "This store's data-sharing settings keep back what Ask AI needs."}
	case aigateway.OutcomeProviderError:
		return &gocommerce.APIError{Status: http.StatusConflict, Code: codeProvider,
			Message: "The AI service didn't answer. Try again in a moment."}
	}
	return &gocommerce.APIError{Status: http.StatusConflict, Code: codeNoAnswer,
		Message: "Ask AI couldn't work that out. Try asking another way."}
}

func (m *Module) ready(ctx context.Context) error {
	if m.cfg.Gateway == nil {
		return plainRefusal(aigateway.ErrNotReady)
	}
	return plainRefusal(m.cfg.Gateway.Ready(ctx))
}

func (m *Module) handleStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"ready": true}
	if err := m.ready(r.Context()); err != nil {
		var apiErr *gocommerce.APIError
		if !errors.As(err, &apiErr) {
			gocommerce.RespondError(w, r, err)
			return
		}
		out["ready"], out["reason"] = false, apiErr.Message
	}
	gocommerce.Respond(w, http.StatusOK, out)
}

func (m *Module) handleTurn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// A person, because the limit below and the Apply that follows are a
	// person's. An API key carries no ID (aigateway.person), and agent work has
	// the queue directly.
	su := gocommerce.SuperuserFrom(ctx)
	if su == nil || su.ID <= 0 {
		gocommerce.RespondError(w, r, gocommerce.Forbiddenf("Ask AI answers a person signed in to the panel, not an API key or the admin token"))
		return
	}
	var in struct {
		Messages []Message `json:"messages"`
		Screens  []Screen  `json:"screens"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := checkMessages(in.Messages); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	if err := m.ready(ctx); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	store, _ := gocommerce.StoreFrom(ctx)
	if wait, ok := m.allowTurn(limitKey{store, su.ID}, time.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
		gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusTooManyRequests, Code: "rate_limited",
			Message: "That's a lot of questions in a few minutes. Wait a little and ask again."})
		return
	}
	input, allowed, err := m.input(ctx, su, in.Messages, in.Screens)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	k := newMasker()
	for i := range input.Messages {
		input.Messages[i].Text = k.mask(input.Messages[i].Text)
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(writeDeadline)); err != nil {
		m.app.Log().Warn("could not extend the write deadline for an ask-ai turn", "error", err)
	}
	// Detached from the request, so a closed tab still leaves the run's record
	// and its true cost. No transaction is open across this (rule 5).
	runCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeDeadline)
	defer cancel()
	res, err := m.cfg.Gateway.Run(runCtx, taskName, input)
	if err != nil {
		gocommerce.RespondError(w, r, plainRefusal(err))
		return
	}
	var out turnOutput
	if err := json.Unmarshal(res.Output, &out); err != nil {
		gocommerce.RespondError(w, r, plainRefusal(&aigateway.RunError{Outcome: aigateway.OutcomeInvalidOutput}))
		return
	}
	result := TurnResult{Answer: k.unmask(out.Answer), Links: keepLinks(out.Links, allowed, k),
		CostMicroMinor: res.CostMicroMinor, Currency: res.Currency}
	result.Proposal, result.Notice = m.file(runCtx, su, out.Proposal, k)
	gocommerce.Respond(w, http.StatusOK, result)
}

// checkMessages holds the conversation the browser sent to its limits. They are
// checked here as well as in the panel because they are what a turn costs.
func checkMessages(list []Message) error {
	if len(list) == 0 || len(list) > maxMessages {
		return gocommerce.Validationf("send between 1 and %d messages", maxMessages)
	}
	if list[len(list)-1].Role != "user" {
		return gocommerce.Validationf("the last message is the person's question")
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

// input builds what the model is shown and the links it may offer: the setup
// guide's screens and the ones the panel says this person can reach.
func (m *Module) input(ctx context.Context, su *gocommerce.Superuser, messages []Message, screens []Screen) (*turnInput, map[string]string, error) {
	profile, err := m.app.Profile().Get(ctx)
	if err != nil {
		return nil, nil, err
	}
	setup, err := m.app.Setup(ctx)
	if err != nil {
		return nil, nil, err
	}
	in := &turnInput{Messages: messages, Setup: []setupFact{}, Screens: []Screen{},
		Store: storeFacts{
			Name: profile.Name, Country: profile.Country, Currency: profile.Currency,
			CurrencyFixed: profile.CurrencyLocked, Timezone: profile.Timezone,
			PricesIncludeTax: profile.PricesIncludeTax != nil && *profile.PricesIncludeTax,
			Filled: filled{
				LegalName: profile.LegalName != "", Email: profile.Email != "", Phone: profile.Phone != "",
				Address: profile.AddressLine1 != "", TaxID: profile.TaxID != "", SupportURL: profile.SupportURL != "",
			},
			PersonMayChange: su.Has(gocommerce.RightStoreWrite),
		},
	}
	allowed := map[string]string{}
	for _, s := range setup.Steps {
		in.Setup = append(in.Setup, setupFact{s.Key, s.Title, s.Why, s.Href, s.Required, s.Done, s.Locked})
		allowed[s.Href] = s.Title
	}
	for i, s := range screens {
		// The browser says what it can reach, and a browser can be told anything:
		// an address that is not a panel path is dropped before anybody sees it.
		if i >= maxScreens {
			break
		}
		label := strings.TrimSpace(s.Label)
		if !screenHrefRE.MatchString(s.Href) || label == "" || utf8.RuneCountInString(label) > 80 {
			continue
		}
		in.Screens = append(in.Screens, Screen{Href: s.Href, Label: label})
		allowed[s.Href] = label
	}
	return in, allowed, nil
}

// keepLinks drops every link that is not on the allowlist. A dropped link is
// simply absent: the person sees the words, never an address nobody vouched for.
func keepLinks(links []Link, allowed map[string]string, k *masker) []Link {
	out := []Link{}
	seen := map[string]bool{}
	for _, l := range links {
		fallback, ok := allowed[l.Href]
		if !ok || seen[l.Href] {
			continue
		}
		seen[l.Href] = true
		label := strings.TrimSpace(k.unmask(l.Label))
		if label == "" {
			label = fallback
		}
		out = append(out, Link{Label: label, Href: l.Href})
	}
	return out
}

// file turns what the model suggested into a proposal in the queue, or says why
// not. Nothing is written to the store here.
func (m *Module) file(ctx context.Context, su *gocommerce.Superuser, p *modelProposal, k *masker) (*ProposalView, string) {
	// "none" rather than a null proposal, so the schema asks for no nullable
	// object, which only some providers' structured output takes.
	if p == nil || p.Action == noProposal {
		return nil, ""
	}
	if p.Action != actionKind {
		return nil, "Ask AI can only change the store's details, so nothing was proposed. You can make that change yourself in the panel."
	}
	if !su.Has(gocommerce.RightStoreWrite) {
		return nil, "Your role can't change the store's details, so nothing was proposed. Ask the store's owner."
	}
	args := actionArgs{Changes: make([]fieldChange, 0, len(p.Changes))}
	for _, c := range p.Changes {
		args.Changes = append(args.Changes, fieldChange{Field: c.Field, Value: k.unmask(c.Value)})
	}
	key := make([]byte, 16)
	_, _ = rand.Read(key)
	// A key of its own each time: asking twice is two proposals, and an old one
	// that was applied or dismissed must not answer for the new one.
	a, err := m.cfg.Gateway.Propose(ctx, actionKind, args, "ask-ai:"+hex.EncodeToString(key))
	if err != nil {
		var api *gocommerce.APIError
		if errors.As(err, &api) {
			return nil, "That change can't be made: " + api.Message
		}
		m.app.Log().Error("ask-ai could not file a proposal", "error", err)
		return nil, "That change could not be prepared. Try again in a moment."
	}
	view, err := viewOf(a)
	if err != nil {
		m.app.Log().Error("ask-ai could not read its own preview", "error", err)
		return nil, "That change could not be prepared. Try again in a moment."
	}
	return view, ""
}

func viewOf(a *aigateway.Approval) (*ProposalView, error) {
	var p Preview
	if err := json.Unmarshal(a.Preview, &p); err != nil {
		return nil, err
	}
	return &ProposalView{ID: a.ID, PreviewHash: a.PreviewHash, Changes: p.Changes, Warnings: p.Warnings}, nil
}

// proposal reads the proposal a route names, as long as it is one of Ask AI's:
// these routes are behind store.write, not the queue's ai.approve, so they must
// not reach any other action that is ever registered.
func (m *Module) proposal(r *http.Request) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, gocommerce.NotFoundf("no proposal %q", r.PathValue("id"))
	}
	if m.cfg.Gateway == nil {
		return 0, plainRefusal(aigateway.ErrNotReady)
	}
	a, err := m.cfg.Gateway.Approval(r.Context(), id)
	if err != nil {
		return 0, err
	}
	if a.Kind != actionKind {
		return 0, gocommerce.NotFoundf("no proposal %d", id)
	}
	return id, nil
}

func (m *Module) handleApply(w http.ResponseWriter, r *http.Request) {
	id, err := m.proposal(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	var in struct {
		PreviewHash string `json:"preview_hash"`
	}
	if err := gocommerce.DecodeJSON(w, r, &in); err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	a, err := m.cfg.Gateway.Approve(r.Context(), id, in.PreviewHash)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	// The queue records a failed action as a decided proposal, and answers 200
	// for it; to the person pressing Apply it is a refusal, in the service's words.
	if a.Status != aigateway.ApprovalExecuted {
		gocommerce.RespondError(w, r, &gocommerce.APIError{Status: http.StatusConflict, Code: "ask_ai_not_applied", Message: a.Error})
		return
	}
	var done Preview
	if err := json.Unmarshal(a.Result, &done); err != nil {
		gocommerce.RespondError(w, r, gocommerce.Internalf(err, "read what was applied"))
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"status": a.Status, "changes": done.Changes})
}

func (m *Module) handleDismiss(w http.ResponseWriter, r *http.Request) {
	id, err := m.proposal(r)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	a, err := m.cfg.Gateway.Reject(r.Context(), id)
	if err != nil {
		gocommerce.RespondError(w, r, err)
		return
	}
	gocommerce.Respond(w, http.StatusOK, map[string]any{"status": a.Status})
}
