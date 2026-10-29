// Package aigateway is the one place in the engine that calls a model.
//
//	gw := aigateway.New(aigateway.Config{})
//	app, err := gocommerce.New(cfg, gw, amazon.New(amazon.Config{Gateway: gw}))
//
// On an install with a key of its own (Config.PlatformAnthropicKey) every
// store has AI with nothing to set up: its calls go out on the platform's
// key, at the platform's models and prices, paid from its AI credit (D151).
// A store may enter a key of its own instead, and then pays its provider
// directly. Without a platform key the gateway installs idle: a store sees
// nothing until its owner switches the plugin on under Settings › Plugins and
// gives it three things: a provider key, a monthly cap in the store's
// currency, and the price of each model. Without all three no call is made,
// because without them the cap could not be kept.
//
// # Tasks, not prompts
//
// Model work is a named Task written in Go (D107): its instructions, the
// JSON Schema its answer must satisfy, the deterministic step tried first,
// whether an answer may be reused, the ladder of models to try and when to
// climb it. Another module registers its tasks and then either runs one now
// (Run, for a worker of its own) or enqueues it in the transaction that
// caused it (Enqueue, ADR 0004), and this module's runner does the rest.
//
// # One run
//
// Every run goes the same way and leaves one row in ai_executions: the
// deterministic step, the cache, then each model on the ladder. Before each
// call the ceiling of what it could cost is reserved against the month's cap
// with one conditional UPDATE, so concurrent runs cannot spend past it
// together; after the call the reservation is settled to what the provider
// reported. The call itself holds no transaction (rule 5). The answer is
// checked against the task's schema, with unknown fields refused, and then by
// the task's own Check; an answer that fails moves up the ladder with the
// reason written down.
//
// # What leaves the store
//
// A request carries one store's data (D104). What a task puts in it is
// tagged with its class — catalogue, order lines, contact details, message
// bodies, documents — and a class the owner has not allowed is left out
// before anything is sent. What is sent is fenced in <data> blocks behind
// instructions that say to treat it as data (ADR 0005 §7).
//
// # Money
//
// Prices are minor units per million tokens, so a run's cost is tokens times
// price in millionths of a minor unit, exact with no division (rule 6). The
// cap is minor units of the store's own currency.
package aigateway

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	gocommerce "github.com/itswadesh/gocommerce/core"
)

// The providers a store may choose. Each one's API host is a constant in
// its own file: an address an operator could change would be an address an
// operator could point at the store's own network, and a custom host waits
// for the outbound guard (D109).
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

// PluginKey is the plugin whose settings configure each store's gateway.
const PluginKey = "ai-gateway"

// The defaults ADR 0005 names: current models, on the Anthropic provider.
const (
	defaultSmallModel  = "claude-haiku-5-5"
	defaultStrongModel = "claude-sonnet-5-5"
)

// Job policy. The attempts and backoff are the outbox's (D9, D49); only
// transient failures spend them.
const (
	maxAttempts = 12
	maxBackoff  = 15 * time.Minute
	// visibilityMargin is how much longer than one call a claim is hidden
	// from other workers. The lock is renewed before each call, so a live
	// worker never loses its job to another however long CallTimeout is.
	visibilityMargin = time.Minute
	// jobsPerPass bounds one store's share of a pass. Stores take turns, a
	// job each, so one store's backlog does not hold the others back.
	jobsPerPass = 10

	defaultCallTimeout  = 60 * time.Second
	defaultPollInterval = 5 * time.Second
)

// Config is what the program tells this module. Everything a store chooses
// is a plugin setting instead.
type Config struct {
	// Client sends every provider request. Leave it nil; tests give one
	// that answers in the providers' place. The hosts are constants either
	// way.
	Client *http.Client
	// CallTimeout bounds one call to a provider. Defaults to 60 seconds.
	CallTimeout time.Duration
	// PollInterval is how often the runner looks for due jobs. Defaults to
	// five seconds.
	PollInterval time.Duration
	// FreeTrialMinor is the AI credit a store gets to try AI before an
	// operator tops it up, in minor units of the store's currency. Zero or
	// less uses DefaultFreeTrialMinor. This is the platform operator's knob;
	// the figure shipped is a placeholder (see DefaultFreeTrialMinor).
	FreeTrialMinor int64

	// PlatformAnthropicKey is the install's own Anthropic key
	// (ANTHROPIC_API_KEY). A store that has entered no key of its own makes
	// its calls on it, at the platform's models and prices below, paid from its
	// AI credit; the store never sees it. Empty: only a store with a key of its
	// own has AI.
	PlatformAnthropicKey string
	// PlatformSmallModel and PlatformStrongModel are the models platform calls
	// use. Empty is the gateway's default for the tier.
	PlatformSmallModel, PlatformStrongModel string
	// PlatformSmallPrices and PlatformStrongPrices are what Anthropic charges
	// for each model, in US dollars per million tokens: "input,output,cache
	// read,cache write", such as "0.10,0.50,0.01,0.125". Empty is Anthropic's
	// published price for a default model; a model with none is not called on
	// the platform's key.
	PlatformSmallPrices, PlatformStrongPrices string
	// PlatformUSDRates turns those dollars into a store's currency: units of
	// each currency to one US dollar, "INR=88.50,EUR=0.92". A store in US
	// dollars needs none; a store in a currency with no rate cannot use the
	// platform's key, because no rate is assumed.
	PlatformUSDRates string
}

// Module is the AI gateway.
type Module struct {
	cfg    Config
	app    *gocommerce.App
	db     *sql.DB
	log    *slog.Logger
	client *http.Client
	// platform is the install's own key, models and prices; nil without a key.
	platform *platform

	mu      sync.RWMutex
	tasks   map[string]*Task
	actions map[string]*Action

	// executeTimeout bounds an approved action; a field so a test can
	// shorten it.
	executeTimeout time.Duration
}

// New constructs the module. Register tasks with RegisterTask before the
// engine starts serving.
func New(cfg Config) *Module {
	return &Module{cfg: cfg, tasks: map[string]*Task{}, actions: map[string]*Action{}, executeTimeout: executeTimeout}
}

// Name implements gocommerce.Module.
func (m *Module) Name() string { return "ai-gateway" }

// Register implements gocommerce.Module.
func (m *Module) Register(app *gocommerce.App) error {
	m.app = app
	m.db = app.DB()
	m.log = app.Log()
	if m.cfg.CallTimeout <= 0 {
		m.cfg.CallTimeout = defaultCallTimeout
	}
	if m.cfg.PollInterval <= 0 {
		m.cfg.PollInterval = defaultPollInterval
	}
	m.client = m.cfg.Client
	if m.client == nil {
		m.client = &http.Client{
			Timeout: m.cfg.CallTimeout + 5*time.Second,
			// A provider that redirects is not answering; following it would
			// send the store's key and data to an address nobody named.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	m.platform = newPlatform(m.cfg, m.log)

	m.registerRights(app)
	m.registerPlugin(app)
	m.mountRoutes(app)
	m.mountApprovalRoutes(app)
	app.OnStart(m.start)
	return nil
}

func (m *Module) registerPlugin(app *gocommerce.App) {
	// On an install with a key of its own, a store needs nothing but credit:
	// the gateway is on until the store switches it off, its own key and cap
	// are optional, and pictures may be sent, so "Create from photos" works as
	// it is. Every call is still started by a person pressing an AI button;
	// contact details and message bodies stay in until the owner says.
	platform := m.platform != nil
	own, capNote, keyLabel, keyHelp := "", "", "API key",
		"This store's own key, from console.anthropic.com or platform.openai.com. Calls are billed to it."
	description := "Every model call this store makes, recorded and capped: your own provider key, a monthly cap in the store's currency, " +
		"the price of each model, and which kinds of data may leave the store. Nothing is called until a key, a cap and prices are set."
	if platform {
		own, keyLabel = " Used only with your own key.", "Use your own key instead"
		capNote = " Optional on the platform's AI, where the store's AI credit is the limit."
		keyHelp = "Leave empty to use the platform's AI; your use is paid from AI credits. A key of your own, from console.anthropic.com " +
			"or platform.openai.com, is billed by your provider instead and draws no credit; the cap, models and prices below are then yours to set."
		description = "Every model call this store makes, recorded and capped. AI runs on the platform's key and is paid from the store's AI credit, " +
			"with nothing to set up; or enter a key of your own and pay your provider directly. Choose here which kinds of data may leave the store."
	}
	price := func(key, label, help string) gocommerce.PluginField {
		return gocommerce.PluginField{Key: key, Label: label, Kind: "number", Help: help + own}
	}
	app.RegisterPlugin(gocommerce.PluginDef{
		Key: PluginKey, Title: "AI gateway", Icon: "ri-robot-2-line", Category: "ai",
		Description:    description,
		DefaultEnabled: platform,
		Fields: []gocommerce.PluginField{
			{Key: "provider", Label: "Provider", Kind: "select", Options: []string{ProviderAnthropic, ProviderOpenAI}, Default: ProviderAnthropic,
				Help: "Whose models answer. Each provider's own API address is used; there is no custom address in this version." + own},
			{Key: "api_key", Label: keyLabel, Kind: "secret", Required: !platform, Help: keyHelp},
			{Key: "small_model", Label: "Small model", Kind: "text", Default: defaultSmallModel,
				Help: "Tried first. Name a model your provider offers; Check connection on the AI usage screen says whether it does." + own},
			{Key: "strong_model", Label: "Strong model", Kind: "text", Default: defaultStrongModel,
				Help: "Asked when the small model's answer fails its checks." + own},
			{Key: "monthly_cap", Label: "Monthly cap", Kind: "number", Required: !platform,
				Help: "The most this store spends on models in a calendar month, in minor units of its currency: 5000 is 50.00 in a two-decimal currency. Work that would pass it waits for a person." + capNote},
			price("small_price_input", "Small model: input price", "Minor units per million input tokens, from your provider's price list: $1.00 per million is 100. A model with no input or output price, or a price of 0, is never called: what costs nothing could not be held to the cap."),
			price("small_price_output", "Small model: output price", "Minor units per million output tokens."),
			price("small_price_cache_read", "Small model: cache read price", "Minor units per million tokens read from the provider's cache. Empty or 0 means the input price."),
			price("small_price_cache_write", "Small model: cache write price", "Minor units per million tokens written to the provider's cache. Empty or 0 means the input price."),
			price("strong_price_input", "Strong model: input price", "Minor units per million input tokens."),
			price("strong_price_output", "Strong model: output price", "Minor units per million output tokens."),
			price("strong_price_cache_read", "Strong model: cache read price", "Empty or 0 means the input price."),
			price("strong_price_cache_write", "Strong model: cache write price", "Empty or 0 means the input price."),
			{Key: "share_catalogue", Label: "Share the catalogue", Kind: "bool", Default: true,
				Help: "Product titles, descriptions, attributes and prices may be sent to the provider."},
			{Key: "share_order_lines", Label: "Share order lines", Kind: "bool", Default: true,
				Help: "What was bought, how many and the totals. Never names or addresses."},
			{Key: "share_contact_details", Label: "Share contact details", Kind: "bool", Default: false,
				Help: "Customers' names, emails, phone numbers and addresses."},
			{Key: "share_message_bodies", Label: "Share message bodies", Kind: "bool", Default: false,
				Help: "The text of customers' messages."},
			{Key: "share_documents", Label: "Share documents", Kind: "bool", Default: platform,
				Help: "PDFs and images, sent to the provider as files."},
		},
	})
}

// The rights this module's screens are gated on (D65). The settings
// themselves are a plugin's, under plugins.write like every other.
const (
	RightRead      gocommerce.Right = "ai.read"
	RightConfigure gocommerce.Right = "ai.configure"
)

func (m *Module) registerRights(app *gocommerce.App) {
	app.RegisterRight(gocommerce.RightSpec{
		Right:   RightRead,
		Label:   "See AI usage",
		Scope:   "Model calls and what they cost against the monthly cap, and the AI job queue",
		Default: []string{gocommerce.RoleManager},
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   RightConfigure,
		Label:   "Run the AI jobs",
		Scope:   "Retrying and cancelling AI jobs, and checking the provider connection",
		Default: []string(nil),
	})
	app.RegisterRight(gocommerce.RightSpec{
		Right:   RightApprove,
		Label:   "Decide AI proposals",
		Scope:   "The approval queue: approving or rejecting what an agent proposed. Approving an action also needs that action's own right",
		Default: []string{gocommerce.RoleManager},
	})
}

// ------------------------------------------------------------- settings

// Tier is a rung of a task's ladder.
type Tier string

const (
	TierSmall  Tier = "small"
	TierStrong Tier = "strong"
)

// prices are one model's, in minor units per million tokens.
type prices struct {
	input, output, cacheRead, cacheWrite int64
	// complete is whether input and output were both given: the two every
	// call is charged, without which the ceiling could not be known.
	complete bool
}

// ceiling is the most a call can cost: every input token at the dearest of
// the input-side prices, which is what a cache write charges for a prompt
// written to the cache whole, and every output token.
func (p prices) ceiling(maxIn, maxOut int) int64 {
	in := max(p.input, p.cacheRead, p.cacheWrite)
	return int64(maxIn)*in + int64(maxOut)*p.output
}

// cost is tokens times price per million tokens, in millionths of a minor
// unit: exact, with no division.
func (p prices) cost(u Usage) int64 {
	return u.InputTokens*p.input + u.OutputTokens*p.output +
		u.CacheReadTokens*p.cacheRead + u.CacheWriteTokens*p.cacheWrite
}

// settings is one store's gateway as its owner configured it.
type settings struct {
	enabled bool
	// platform is a store with no key of its own on an install with one: the
	// provider, key, models and prices are the platform's, and its calls draw
	// the store's AI credit. A store's own key draws none, because the store
	// pays its provider itself (D151, 2026-10-29; the owner's to flip).
	platform bool
	// unpriced is the store's currency when the platform has no rate for it.
	unpriced string
	provider string
	apiKey   string
	models   map[Tier]string
	prices   map[Tier]prices
	capMinor *int64
	policy   Policy
}

// Where a store's calls are billed, as the usage screen shows it.
const (
	KeyPlatform = "platform"
	KeyOwn      = "own"
	KeyNone     = "none"
)

func (s *settings) keySource() string {
	switch {
	case s.platform:
		return KeyPlatform
	case s.apiKey != "":
		return KeyOwn
	}
	return KeyNone
}

// ErrNotReady is a store whose gateway cannot make calls: switched off, or
// missing its key or its cap. No call is made and nothing is recorded; the
// error says which.
var ErrNotReady = errors.New("the AI gateway cannot make calls for this store")

type notReady struct{ reason string }

func (e *notReady) Error() string        { return e.reason }
func (e *notReady) Is(target error) bool { return target == ErrNotReady }

func (s *settings) ready() error {
	switch {
	case !s.enabled:
		return &notReady{"the AI gateway is switched off for this store: switch it on under Settings › Plugins"}
	case s.apiKey == "":
		return &notReady{"AI isn't set up for this store: add an API key under Settings › Plugins › AI gateway"}
	case s.unpriced != "":
		return &notReady{fmt.Sprintf("the platform's AI is not priced in %s yet: ask whoever runs this platform, "+
			"or enter a key of your own under Settings › Plugins › AI gateway", s.unpriced)}
	case s.capMinor == nil && !s.platform:
		return &notReady{"the AI gateway has no monthly cap: set one under Settings › Plugins › AI gateway"}
	}
	return nil
}

// settings reads the store's configuration as it stands now, so a key or a
// cap typed into the panel a moment ago counts without a restart.
func (m *Module) settings(ctx context.Context) (*settings, error) {
	enabled, err := m.app.Plugins().Enabled(ctx, PluginKey)
	if err != nil {
		return nil, err
	}
	raw, err := m.app.Plugins().Settings(ctx, PluginKey)
	if err != nil {
		return nil, err
	}
	text := func(key string) string {
		s, _ := raw[key].(string)
		return strings.TrimSpace(s)
	}
	flag := func(key string) bool {
		b, _ := raw[key].(bool)
		return b
	}
	s := &settings{
		enabled:  enabled,
		provider: text("provider"),
		apiKey:   text("api_key"),
		models:   map[Tier]string{TierSmall: text("small_model"), TierStrong: text("strong_model")},
		prices:   map[Tier]prices{},
		policy: Policy{
			Catalogue:      flag("share_catalogue"),
			OrderLines:     flag("share_order_lines"),
			ContactDetails: flag("share_contact_details"),
			MessageBodies:  flag("share_message_bodies"),
			Documents:      flag("share_documents"),
		},
	}
	if s.provider == "" {
		s.provider = ProviderAnthropic
	}
	// The cap rounds down and a price rounds up, so a fraction typed into
	// the panel can only make the cap stricter, never let spend past it.
	if c, ok := number(raw["monthly_cap"], math.Floor); ok {
		s.capMinor = &c
	}
	// A price of nothing is no price: a model that costs nothing could never
	// be held to the cap, so one priced at 0 is not called, and a cache price
	// of 0 falls back to the input price as an empty one does.
	price := func(key string) (int64, bool) {
		v, ok := number(raw[key], math.Ceil)
		return v, ok && v > 0
	}
	for _, tier := range []Tier{TierSmall, TierStrong} {
		p := prices{}
		in, hasIn := price(string(tier) + "_price_input")
		out, hasOut := price(string(tier) + "_price_output")
		p.input, p.output, p.complete = in, out, hasIn && hasOut
		p.cacheRead, p.cacheWrite = in, in
		if v, ok := price(string(tier) + "_price_cache_read"); ok {
			p.cacheRead = v
		}
		if v, ok := price(string(tier) + "_price_cache_write"); ok {
			p.cacheWrite = v
		}
		s.prices[tier] = p
	}
	// A key of the store's own that will not open, sealed under a secrets key
	// this install no longer has, is still the store's choice: it is not
	// quietly swapped for the platform's and charged to the store's credit.
	unopened := []string{}
	if s.apiKey == "" && m.platform != nil {
		if unopened, err = m.app.Plugins().Unopened(ctx, PluginKey); err != nil {
			return nil, err
		}
	}
	// No key of the store's own: the platform's, with everything that decides
	// what a call costs taken from the platform too. Only the cap and the data
	// policy stay the store's.
	if s.apiKey == "" && m.platform != nil && !slices.Contains(unopened, "api_key") {
		s.platform, s.provider, s.apiKey = true, ProviderAnthropic, m.platform.key
		s.models = maps.Clone(m.platform.models)
		currency := m.app.Currency(ctx)
		var ok bool
		if s.prices, ok = m.platform.prices(currency); !ok {
			s.prices, s.unpriced = map[Tier]prices{}, currency
		}
	}
	return s, nil
}

// number reads a stored number setting as whole minor units. A negative
// value is no value: neither a cap nor a price can be below nothing.
func number(v any, round func(float64) float64) (int64, bool) {
	f, ok := v.(float64)
	if !ok || f < 0 || math.IsNaN(f) || f > math.MaxInt64/1e6 {
		return 0, false
	}
	return int64(round(f)), true
}

// Ready says whether ctx's store can make calls now, and if not, why.
// Another module asks before choosing the gateway over a path of its own.
func (m *Module) Ready(ctx context.Context) error {
	s, err := m.settings(ctx)
	if err != nil {
		return err
	}
	return s.ready()
}

// Allows says whether ctx's store lets a class of data leave it, so a module
// whose work cannot be done without that class can say so before a person
// spends ten minutes on the screen that starts it.
func (m *Module) Allows(ctx context.Context, c DataClass) (bool, error) {
	s, err := m.settings(ctx)
	if err != nil {
		return false, err
	}
	return s.policy.Allows(c), nil
}

// Migrations implements gocommerce.Module.
//
// Five tables, all the store's (D104), each put behind the row policy by the
// migration after the one that creates it. They name store_id themselves,
// rather than leaving ScopeTables to add it, so that every index can lead
// with it: every query here is within one store, and the row policy reads
// the store first. The tables carry ai_ rather than the module's own
// prefix: the gateway's are the AI records every later AI module reads, and
// the approval queue beside them is approval_, as ADR 0003 names it.
func (m *Module) Migrations() []gocommerce.Migration {
	return []gocommerce.Migration{{
		ID: "0001_ai",
		SQL: `
CREATE TABLE ai_jobs (
    id           bigserial PRIMARY KEY,
    store_id     text NOT NULL,
    task         text NOT NULL,
    input        jsonb NOT NULL,
    -- NULL is no key, and never matches another.
    idem_key     text,
    status       text NOT NULL DEFAULT 'queued'
                 CHECK (status IN ('queued', 'running', 'done', 'failed', 'dead', 'cancelled')),
    attempts     integer NOT NULL DEFAULT 0,
    next_run_at  timestamptz NOT NULL DEFAULT now(),
    locked_until timestamptz,
    -- Which claim holds the job: a worker writes its outcome only while this
    -- is still its own.
    claim_token  text,
    last_error   text,
    trace_id     text NOT NULL DEFAULT '',
    result       jsonb,
    execution_id bigint,
    created_at   timestamptz NOT NULL DEFAULT now(),
    finished_at  timestamptz,
    UNIQUE (store_id, idem_key)
);
-- The runner's claim: what is waiting, oldest due first.
CREATE INDEX ai_jobs_due ON ai_jobs (store_id, next_run_at) WHERE status IN ('queued', 'running');
CREATE INDEX ai_jobs_status ON ai_jobs (store_id, status, id DESC);

CREATE TABLE ai_executions (
    id                 bigserial PRIMARY KEY,
    store_id           text NOT NULL,
    task               text NOT NULL,
    task_version       integer NOT NULL,
    job_id             bigint REFERENCES ai_jobs (id) ON DELETE SET NULL,
    trace_id           text NOT NULL DEFAULT '',
    steps              jsonb NOT NULL DEFAULT '[]',
    provider           text NOT NULL DEFAULT '',
    model              text NOT NULL DEFAULT '',
    model_version      text NOT NULL DEFAULT '',
    input_tokens       bigint NOT NULL DEFAULT 0,
    output_tokens      bigint NOT NULL DEFAULT 0,
    cache_read_tokens  bigint NOT NULL DEFAULT 0,
    cache_write_tokens bigint NOT NULL DEFAULT 0,
    latency_ms         bigint NOT NULL DEFAULT 0,
    outcome            text NOT NULL CHECK (outcome IN ('ok', 'deterministic', 'cached', 'invalid_output',
                                                         'refused', 'provider_error', 'budget_exhausted', 'no_price',
                                                         'task_error', 'policy_blocked')),
    review_status      text NOT NULL DEFAULT 'none' CHECK (review_status IN ('none', 'pending', 'approved', 'rejected')),
    confidence         smallint CHECK (confidence BETWEEN 0 AND 100),
    source_refs        jsonb NOT NULL DEFAULT '[]',
    policy             jsonb NOT NULL DEFAULT '{}',
    -- Tokens times minor units per million tokens: exact, never rounded.
    cost_micro_minor   bigint NOT NULL DEFAULT 0 CHECK (cost_micro_minor >= 0),
    -- A call that was sent and reported nothing back is charged at its
    -- ceiling, and the figure says it is an estimate.
    cost_estimated     boolean NOT NULL DEFAULT false,
    currency           text NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_executions_created ON ai_executions (store_id, created_at);
CREATE INDEX ai_executions_job ON ai_executions (store_id, job_id);

ALTER TABLE ai_jobs ADD CONSTRAINT ai_jobs_execution_fkey
    FOREIGN KEY (execution_id) REFERENCES ai_executions (id) ON DELETE SET NULL;

-- One row per store and month: the cap as last read, what is held for calls
-- in flight, and what was spent.
CREATE TABLE ai_budgets (
    store_id       text NOT NULL,
    month          date NOT NULL,
    cap_minor      bigint NOT NULL CHECK (cap_minor >= 0),
    reserved_micro bigint NOT NULL DEFAULT 0 CHECK (reserved_micro >= 0),
    spent_micro    bigint NOT NULL DEFAULT 0 CHECK (spent_micro >= 0),
    updated_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (store_id, month)
);

-- Answers a task allows to be reused. The key leads with the store, so two
-- stores asking the same question never share an answer.
CREATE TABLE ai_cache (
    store_id     text NOT NULL,
    task         text NOT NULL,
    task_version integer NOT NULL,
    input_hash   text NOT NULL,
    output       jsonb NOT NULL,
    execution_id bigint REFERENCES ai_executions (id) ON DELETE SET NULL,
    expires_at   timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (store_id, task, task_version, input_hash)
);
CREATE INDEX ai_cache_expires ON ai_cache (store_id, expires_at);`,
	},
		gocommerce.ScopeMigration("0002_store_scope", "ai_jobs", "ai_executions", "ai_budgets", "ai_cache"),
		{
			ID: "0003_approvals",
			SQL: `
-- What an agent proposed and a person decides (D105). decided_by is a
-- superuser id, kept with the email it had, because the people table is the
-- install's rather than the store's.
CREATE TABLE approval_requests (
    id                  bigserial PRIMARY KEY,
    store_id            text NOT NULL,
    kind                text NOT NULL,
    args                jsonb NOT NULL,
    preview             jsonb NOT NULL,
    idem_key            text NOT NULL,
    requested_by_key_id bigint,
    requested_by_label  text NOT NULL DEFAULT '',
    trace_id            text NOT NULL DEFAULT '',
    status              text NOT NULL DEFAULT 'pending'
                        CHECK (status IN ('pending', 'approved', 'executed', 'failed', 'rejected', 'expired')),
    expires_at          timestamptz NOT NULL,
    decided_by          bigint,
    decided_by_email    text NOT NULL DEFAULT '',
    decided_at          timestamptz,
    result              jsonb,
    error               text,
    created_at          timestamptz NOT NULL DEFAULT now(),
    UNIQUE (store_id, idem_key)
);
CREATE INDEX approval_requests_status ON approval_requests (store_id, status, id DESC);`,
		},
		gocommerce.ScopeMigration("0004_approvals_scope", "approval_requests"),
		{
			ID: "0005_credits",
			SQL: `
-- A store's prepaid AI credit (D151). AI features draw it down; every other
-- feature is free. One row per store. Amounts are micro-minor, the millionths
-- of a minor unit the gateway already records cost in, so a run's cost
-- subtracts without converting.
--
-- free_allowance is the free trial: what a new store may spend before anyone
-- tops it up. granted is what an operator has topped up by hand (no payment
-- gateway yet). consumed is what AI calls have cost; a call draws free first,
-- then granted, which is arithmetic rather than two counters. reserved is held
-- for calls in flight, as the budget's is.
CREATE TABLE ai_credits (
    store_id             text NOT NULL PRIMARY KEY,
    free_allowance_micro bigint NOT NULL DEFAULT 0 CHECK (free_allowance_micro >= 0),
    granted_micro        bigint NOT NULL DEFAULT 0 CHECK (granted_micro >= 0),
    consumed_micro       bigint NOT NULL DEFAULT 0 CHECK (consumed_micro >= 0),
    reserved_micro       bigint NOT NULL DEFAULT 0 CHECK (reserved_micro >= 0),
    currency             text NOT NULL,
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- Who changed a store's credit, and when: an operator's hand grant or a new
-- free allowance. Audits the change, as other admin changes are audited;
-- amount_minor is what was added for a grant, the new figure for an allowance.
CREATE TABLE ai_credit_ledger (
    id           bigserial PRIMARY KEY,
    store_id     text NOT NULL,
    kind         text NOT NULL CHECK (kind IN ('grant', 'free_allowance')),
    amount_minor bigint NOT NULL,
    actor_id     bigint,
    actor_email  text NOT NULL DEFAULT '',
    note         text NOT NULL DEFAULT '',
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ai_credit_ledger_store ON ai_credit_ledger (store_id, id DESC);

-- A call with no credit left is recorded like one over the cap, so the usage
-- screen can tell the two apart. The constraint is replaced, not altered, so
-- the new value is append-only beside the old ones.
ALTER TABLE ai_executions DROP CONSTRAINT ai_executions_outcome_check;
ALTER TABLE ai_executions ADD CONSTRAINT ai_executions_outcome_check
    CHECK (outcome IN ('ok', 'deterministic', 'cached', 'invalid_output', 'refused',
                       'provider_error', 'budget_exhausted', 'no_price', 'task_error',
                       'policy_blocked', 'credit_exhausted'));`,
		},
		gocommerce.ScopeMigration("0006_credits_scope", "ai_credits", "ai_credit_ledger"),
	}
}
