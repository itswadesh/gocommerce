# M1 step 4: the AI gateway

The design for `ext/ai-gateway`, built from D103–D107 (ADR 0001, 0002, 0004
and 0005). It is the one place in the engine that calls a model. Nothing in
this step talks to a customer, and the approval queue it adds has no actions
until MCP registers them in step 5.

## What a store gets

- A plugin, **AI gateway**, that installs idle. Its settings choose a
  provider, hold the store's own key, name the small and strong models, set a
  monthly cap and the price of each model, and say which kinds of data may
  leave the store.
- A record of every model call: what task asked, which model answered, the
  tokens, the cost in the store's currency, how long it took and whether the
  answer passed its checks.
- A usage screen: spend this month against the cap, by day, task and model,
  and the recent runs.
- A job list: queued, running, failed and dead work, with retry and cancel.
- An approval queue: proposals waiting for a person, each with a preview that
  is recomputed when it is opened and again when it is approved.

## The module

`ext/ai-gateway` (package `aigateway`), switched on with `-ai-gateway`. A
plugin, `ai-gateway`, category `ai`, installs idle, so a store sees nothing
until its owner turns it on (D103).

Rights, registered by the module (D65):

| Right | Grants |
|---|---|
| `ai.read` | Usage, executions, jobs and the budget |
| `ai.configure` | Retrying and cancelling jobs, and the connection check. The settings themselves stay under `plugins.write`, as every plugin's do |
| `ai.approve` | The approval queue. Approving an action also needs that action's own right |

Defaults: owner gets all three; manager gets `ai.read` and `ai.approve`;
staff and vendor get none.

### Settings

| Field | Kind | Default | Notes |
|---|---|---|---|
| `provider` | select: `anthropic`, `openai` | `anthropic` | Each provider's own API host, fixed in code. A custom host (an OpenAI-compatible server) waits for D109's outbound guard |
| `api_key` | secret | none, required | Sealed at rest once D110 lands; nothing in this module changes for that |
| `small_model` | text | `claude-haiku-4-5` | |
| `strong_model` | text | `claude-sonnet-5-5` | |
| `monthly_cap` | number | none, required | In minor units of the store's currency (`app.Currency(ctx)`). No cap, no calls |
| `small_price_input`, `small_price_output`, `small_price_cache_read`, `small_price_cache_write` | number | none | Minor units per million tokens. A model with no input or output price is not called, because the cap could not be enforced; cache prices default to the input price when empty |
| `strong_price_input`, `strong_price_output`, `strong_price_cache_read`, `strong_price_cache_write` | number | none | As above |
| `share_catalogue` | bool | on | Product titles, descriptions, attributes, prices |
| `share_order_lines` | bool | on | Lines, quantities, totals; never names or addresses |
| `share_contact_details` | bool | off | Names, emails, phones, addresses |
| `share_message_bodies` | bool | off | The text of customers' messages |
| `share_documents` | bool | off | PDFs and images sent to the provider as files |

The policy in force is copied onto every execution record, so a later reader
knows what was allowed when the call was made.

## Tables

All are store tables behind the row policy (`ScopeMigration`, D104), created by
the module's migrations, prefixed `ai_` and `approval_` as listed.

**`ai_jobs`**: durable work (D106).
`store_id`, `id`, `task`, `input jsonb`, `idem_key` (unique per store),
`status` (`queued`, `running`, `done`, `failed`, `dead`, `cancelled`),
`attempts`, `next_run_at`, `locked_until`, `last_error`, `trace_id`,
`result jsonb`, `execution_id`, `created_at`, `finished_at`.
Claimed with `FOR UPDATE SKIP LOCKED` across stores the way the outbox runner
claims, with a 2-minute visibility timeout. Backoff and the dead state follow
the outbox's numbers (D9, D49), except that only transient failures retry:
a timeout, a 429, a 5xx or 529 from the provider. A refused request (4xx),
an answer that fails its checks on the strong model, an exhausted budget or a
missing price goes straight to `failed` with the reason, and stays there for a
person: that is the task's human queue.

**`ai_executions`**: one row per run (ADR 0005, the spec's AIExecution).
`store_id`, `id`, `task`, `task_version`, `job_id`, `trace_id`,
`steps jsonb` (each step tried: `deterministic`, `cache`, `small`, `strong`,
with its outcome and, where it escalated, why), `provider`, `model`,
`model_version` (what the provider reported), `input_tokens`,
`output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `latency_ms`,
`outcome` (`ok`, `deterministic`, `cached`, `invalid_output`, `refused`,
`provider_error`, `budget_exhausted`, `no_price`), `review_status` (`none`,
`pending`, `approved`, `rejected`), `confidence` (0–100, nullable),
`source_refs jsonb`, `policy jsonb`, `cost_micro_minor bigint`, `currency`,
`created_at`.

**`ai_budgets`**: one row per store and month.
`store_id`, `month date`, `cap_minor`, `reserved_micro`, `spent_micro`.
Money is integers throughout (rule 6). `cost_micro_minor` is tokens times
minor units per million tokens, so it is exact with no division.

**`ai_cache`**: answers a task allows to be reused.
`store_id`, `task`, `task_version`, `input_hash`, `output jsonb`,
`execution_id`, `expires_at`. The key begins with the store (D104): two stores
asking the same question never share an answer.

**`approval_requests`**: the store side's one approval object (D105).
`store_id`, `id`, `kind`, `args jsonb`, `preview jsonb`, `idem_key` (unique
per store), `requested_by_key_id` (nullable; the agent key once step 2 has
landed), `requested_by_label`, `trace_id`, `status` (`pending`, `approved`,
`executed`, `failed`, `rejected`, `expired`), `expires_at` (7 days),
`decided_by`, `decided_by_email`, `decided_at`, `result jsonb`, `error`,
`created_at`.

## The Go API other modules use

```go
// A task is Go code: its types, its checks, its ladder (D107).
type Task struct {
	Name, System   string          // "product.rewrite"; instructions, the same for every store
	Version        int             // bumped when System, Schema or Build change
	Schema         json.RawMessage // JSON Schema the answer must satisfy
	MaxInputTokens int             // a request built larger than this is refused, not sent
	MaxOutputTokens int
	Ladder         []Tier          // TierSmall, then TierStrong
	EscalateBelow  int             // confidence under this moves up the ladder
	CacheFor       time.Duration   // 0: never reuse an answer
	Deterministic  func(ctx context.Context, input json.RawMessage) (json.RawMessage, bool, error)
	Build          func(ctx context.Context, input json.RawMessage, p Policy) (Request, []SourceRef, error)
	Check          func(ctx context.Context, output json.RawMessage) (confidence int, err error)
}

func (m *Module) RegisterTask(t Task) error
// Ready is nil when the store can make calls, else ErrNotReady with the reason.
func (m *Module) Ready(ctx context.Context) error
// Enqueue writes the job in the caller's transaction (D106).
func (m *Module) Enqueue(ctx context.Context, tx *sql.Tx, task string, input any, idemKey string) (int64, error)
// Run does the work now, for background code that holds no transaction and
// serves no request (an importer's own worker). It reserves, calls, settles
// and records exactly as the job runner does.
func (m *Module) Run(ctx context.Context, task string, input any) (Result, error)

// An action a person may approve (D105).
type Action struct {
	Kind    string
	Right   gocommerce.Right
	Preview func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
	Execute func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
}

func (m *Module) RegisterAction(a Action) error
func (m *Module) Propose(ctx context.Context, kind string, args any, idemKey string) (*Approval, error)
```

`Request`, `Response`, `Provider` and `Embedder` are ADR 0005's. The
`Embedder` interface is declared and unused in this step. A `Message` carries
the task's own words in `Text` and store content in `Data`, each piece tagged
with its class (`DataCatalogue`, `DataOrderLines`, `DataContactDetails`,
`DataMessageBodies`); the gateway leaves out the classes the policy does not
allow and fences the rest. `Response` gains `Refusal`, what a model said when
it declined. A run that ends without an answer returns a `*RunError` with its
outcome, its reason and whether it is worth trying again.

### One run

1. Look up the task, read the store's settings and policy. Plugin off, no key,
   no cap: the run stops with the reason, and no call is made.
2. `Deterministic`, if the task has one. An answer ends the run with outcome
   `deterministic` and no cost.
3. The cache, if the task allows it.
4. For each tier of the ladder:
   - Pick the model; no input or output price means outcome `no_price`, stop.
   - Build the request under the policy. Untrusted content goes only inside
     fenced data blocks, which the instructions say to treat as data
     (ADR 0005 §7). A request over `MaxInputTokens` is refused.
   - Reserve the ceiling (`MaxInputTokens` and `MaxOutputTokens` at this
     model's prices) with one conditional `UPDATE` on `ai_budgets`; refused
     means outcome `budget_exhausted`, stop.
   - Call the provider with the guarded deadline, outside any transaction
     (rule 5).
   - Settle the reservation to the real cost in a new transaction.
   - Decode the answer into the schema with unknown fields refused, then
     `Check`. A failure, or a confidence under `EscalateBelow`, moves to the
     next tier with the reason; no tier left means outcome `invalid_output`.
5. Write the execution record; on a job, mark it done or failed in the same
   transaction as the record.

### Providers

REST over `net/http`, no SDK (rule 2), starting from `ext/import-amazon`'s
client.

- **Anthropic:** Messages API, `anthropic-version: 2023-06-01`. The answer is
  forced through a single tool whose `input_schema` is the task's schema
  (`tool_choice: {type: "tool"}`), which every current model supports. The
  instructions and schema come first and carry `cache_control`, so they are
  cached across stores while nothing store-specific is (D104). Usage maps
  `input_tokens`, `output_tokens`, `cache_read_input_tokens` and
  `cache_creation_input_tokens`.
- **OpenAI:** Chat Completions with `response_format: {type: "json_schema",
  strict: true}`. Usage maps `prompt_tokens` (less cached), `completion_tokens`
  and `prompt_tokens_details.cached_tokens`.

Transient: timeouts, 429, 5xx, Anthropic's 529. Everything else is a refusal.
Error text from the provider is kept in the job and the execution, never the
request body, and never the key.

### Approvals

- Routes require `ai.approve`. Approving also requires the action's own right,
  and the caller must be a person: a session, not an API key of either kind,
  and not the static token.
- Opening a proposal recomputes its preview. Approving recomputes it again and
  runs `Execute` with the approver on the context and
  `WithActorLabel("approval #<id>")`, so core writes its own audit row and
  event in its own transaction, as when a person clicks the button (rule 3).
- The status moves from `pending` with one conditional `UPDATE`, so two
  approvals of the same proposal run it once. `Propose` is idempotent on its
  key.
- A proposal past `expires_at` reads as expired and cannot run.

## Routes

All under `/api/admin/x/ai-gateway/` (rule 9), all in the module's OpenAPI
document (rule 11).

| Route | Right |
|---|---|
| `GET usage?from=&to=&by=day\|task\|model` | `ai.read` |
| `GET budget` | `ai.read` |
| `GET executions`, `GET executions/{id}` | `ai.read` |
| `GET jobs?status=` | `ai.read` |
| `POST jobs/{id}/retry`, `POST jobs/{id}/cancel` | `ai.configure` |
| `POST check`: asks the provider's model list with the store's key (no tokens spent) and says whether the key works | `ai.configure` |
| `GET approvals?status=`, `GET approvals/{id}` | `ai.approve` |
| `POST approvals/{id}/approve`, `POST approvals/{id}/reject` | `ai.approve` plus the action's right |

## The panel

A new **AI** group in the navigation, shown to whoever holds `ai.read` or
`ai.approve`:

- **Usage**: this month's spend against the cap, with a bar; spend by day
  for the month; tables by task and by model; the recent runs, each opening
  its steps, tokens, cost and outcome.
- **Jobs**: the list by status, with retry and cancel.
- **Approvals**: the queue, each with its preview, who asked, when it expires,
  and approve and reject.

Settings stay on the plugin's own screen under Settings › Plugins. The screens
follow the panel's components, dark mode, and the owner's motion rule: 150 to
300 ms feedback on every control and state change, transform and opacity
only, and a reduced-motion path.

## Moving import-amazon onto the gateway

`ext/import-amazon` registers a `product.rewrite` task. When the gateway
module is present and switched on for the store, the rewrite goes through
`Run`, so it is counted, capped and recorded. Otherwise it uses its
environment-configured client as today, so nothing changes for an install
without the gateway.

## Trace ids

Each request gets a trace id: one supplied in `X-Trace-Id` that is 8 to 64
characters of letters, digits, `-` or `_` is kept; anything else is replaced.
It is echoed in the response header, put on the request log line with the
store (D104), carried into jobs, executions and proposals, and readable by
modules through `gocommerce.TraceID(ctx)`.

## How it is proven (the M1 gate's share)

- **Isolation:** two stores side by side; neither sees the other's jobs,
  executions, budget, cache entries or proposals, through the Go API or the
  routes.
- **Cost:** known token counts at known prices give the exact
  `cost_micro_minor`. Concurrent reservations never take spend past the cap.
- **Ladder:** an invalid small answer escalates to the strong model with the
  reason recorded; an invalid strong answer fails without retrying.
- **Failures:** a 529 retries with backoff; a 400 fails at once; a job past its
  attempts is dead and listed.
- **Policy:** with contact details off, a task's request carries no email,
  phone or address, checked on the bytes sent.
- **Approvals:** refused for a key caller and for a person missing the
  action's right; the preview is recomputed; a double approval runs once; an
  expired proposal cannot run.
- **Providers:** each against a recorded response served by `httptest`,
  including usage mapping and refusals.

## Rulings made in building it

Where the design above was silent, or a detail did not survive the code:

- **Claiming.** Jobs are claimed one at a time per store, each store bound in
  turn, as the outbox runner does (`ForEachStore`, then `FOR UPDATE SKIP
  LOCKED` inside the store), at most ten per store per pass so one backlog
  does not starve the rest. The two-minute lock is renewed before each call,
  and a job's outcome is written only while the row still carries the attempt
  the worker claimed, so a worker that lost its lock and comes back late
  changes nothing.
- **Settings are read on every run,** so a key or cap typed a moment ago
  counts. A fraction typed into the panel rounds the cap down and a price up:
  both can only make the cap stricter.
- **The ceiling** reserved before a call prices every input token at the
  dearest of the input, cache-read and cache-write prices, so a prompt
  written to the cache whole cannot cost more than was reserved. The real cost
  is charged at settlement even if it passes the reservation, because the
  money was spent.
- **Months and days are UTC.** One budget row per UTC calendar month; usage
  windows are UTC days, and `by=day` returns every day of the window, empty
  ones included.
- **What leaves the store** is decided by class, not by trusting each task:
  store content goes in `Message.Data` tagged with its class, the gateway
  drops the classes the policy forbids before rendering, and records which
  were dropped on the step. With contact details off, an email address, or a
  phone number in international form, that slipped into another class is
  replaced by its shape. A request carrying documents when documents are off
  is refused rather than sent without the file it is about.
- **The fence** is a `<data label class>` block holding the value as JSON. Go's
  encoder escapes `<`, `>` and `&`, so nothing in a value can close it.
- **Token estimate:** a request is counted at one token per three bytes plus
  512 for what the provider adds, which over-counts English on purpose.
- **Schemas** must be closed objects with every property required, the shape
  OpenAI's strict mode accepts, using types, `properties`, `required`,
  `additionalProperties: false`, `items`, `enum` and the length and range
  bounds. A keyword outside that set is refused at registration rather than
  silently not checked.
- **A declined or cut-off answer** is an invalid one: it moves up the ladder
  and costs what it used. A transient provider failure does not climb the
  ladder; the job retries it.
- **The deterministic step:** an error ends the run as `refused`; an answer
  that fails the task's own schema falls through to the model, with the reason
  on the step.
- **Not ready is not a run.** A store with the gateway off, or with no key or
  cap, gets `ErrNotReady` with the reason and no record; a job in that state
  goes to `failed` with the reason. A database failure mid-run writes no
  record and is retried like a provider outage.
- **The record** carries the run's totals: tokens and cost of every call, and
  the model that last answered or was asked. Each step carries its own call's
  model, tokens, cost and latency. Cost is never null: a run that made no call
  cost nothing, and a model without prices is never called.
- **The budget route** also says whether the store can make calls, why not,
  the provider and models and whether each is priced, because that is what
  heads the usage screen.
- **The check** answers 200 with `ok` and a message in every case. A key that
  does not begin the way its provider's keys do (`sk-ant-`, `sk-`) is caught
  before anything leaves the store; a key that works is reported with whether
  each named model is in the provider's list.
- **Jobs:** cancel applies to queued, running, failed and dead jobs (a running
  job's call is not stopped; its outcome is discarded); retry applies to
  failed, dead and cancelled ones and keeps the last error until the next run
  replaces it. The job list does not return a job's input. OpenAI's 429
  `insufficient_quota` is not transient: waiting does not refill an account.
- **The cache key** is the task, its version, and the input together with the
  policy it ran under. Expired entries are cleared on each runner pass.
- **Redirects** from a provider are not followed.
- **Rights' defaults:** `ai.configure` is the owner's alone until a store
  grants it.

The approval queue:

- **`approved` is a state,** as ADR 0003 lists it: the moment between the
  decision and the action's end. The one conditional `UPDATE` moves a
  proposal from `pending` to `approved`; the action's end moves it to
  `executed` or `failed`. A failed action is not put back to `pending`: the
  store has moved on, and a person files or approves again.
- **Expiry is read, not swept.** A pending proposal past `expires_at` reads
  as `expired` everywhere, and the `expired` filter includes it.
- **Who may decide.** Approving and rejecting both need a person and the
  action's own right. A person is a signed-in operator with a superusers row:
  an API key resolves to one with no id, and agent keys will carry their key
  id beside that zero rather than in it, so the test stays right when they
  land. The static token cannot decide either. A proposal whose action this
  build does not have cannot be approved, and can be rejected with
  `ai.approve` alone, so it can be cleared.
- **Previews.** `Propose` computes the preview when filing and refuses to
  file one whose preview fails. Opening a pending proposal returns the
  preview recomputed now beside the one filed, with `changed` when they
  differ; approving recomputes it again, stores that as what was approved,
  and runs against the store as it is.
- **Running.** Once claimed, the action runs to its end whatever happens to
  the request, bounded at 30 seconds, as the approver and labelled
  `approval #<id>`. An action that fails answers 200 with the proposal
  `failed` and the reason: the decision was made, the act did not succeed.
- **Who asked** is the caller's name as every trail prints it (`api key:
  <name>` for a key) or "the engine"; `requested_by_key_id` stays empty until
  agent keys exist. The decider's email is kept beside their id, because the
  people table is the install's, not the store's.

The Amazon importer:

- **The gateway's answer is final for a store that switched it on.** When the
  gateway refuses a rewrite (a cap that cannot cover it, catalogue sharing
  off, an answer that failed its checks), the import keeps the listing's own
  copy with the reason as a warning, and does not fall back to the
  environment's key: that key is not a way round the store's own cap.
- **`Run`, not `Enqueue`.** An import is already a worker of its own holding
  no transaction, so the rewrite runs now and the job waits for it. The
  import carries its request's trace into the run.
- **The listing is catalogue data,** so a store that has switched catalogue
  sharing off gets no rewrite, said in so many words.

## Not in this step

- Custom provider hosts (after D109's guard has merged).
- A platform-wide key (D107: off by default; a later step).
- Embeddings and vector search (D104: when semantic search is built).
- Any action in the approval queue (step 5 registers MCP's).
