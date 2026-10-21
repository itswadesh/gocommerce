# ADR 0005: One provider interface, called only through the gateway

Status: accepted, 21 October 2026, as D107 in `PLAN.md` §5.

## Context

The spec's gateway (§3.3) does the following:

- tries rules, a cache and deterministic tools before any model;
- escalates from a small model to a stronger one only when needed;
- records tokens, estimated cost, latency, model, success and the reason for
  escalating, per task and tenant;
- enforces prompt-injection defences, minimal personal data, strict output
  schemas, rate limits, budgets, retries and fallback;
- lets merchants pick a provider and bring their own key.

Today one module calls models: `ext/import-amazon/optimize.go`, a plain
`net/http` client for Anthropic's Messages API and for the OpenAI chat format
(which also reaches Gemini's compatible endpoint and local servers). Its keys
are process-wide environment variables, and it records no usage. Rule 2 means
every provider here is REST over `net/http`, with no SDK.

## Decision

1. **The interface lives in `ext/ai-gateway`, and nothing else calls a model.**

   ```go
   type Provider interface {
       Name() string
       Generate(ctx context.Context, req Request) (Response, error)
   }

   type Embedder interface {
       Embed(ctx context.Context, model string, texts []string) ([][]float32, Usage, error)
   }

   type Request struct {
       Model     string
       System    string     // instructions: the same for every store
       Messages  []Message  // untrusted content is marked as data, never as instructions
       Documents []Document // PDFs and images, only where the data-sharing policy allows
       Schema    []byte     // JSON Schema the answer must satisfy
       MaxTokens int
   }

   type Response struct {
       JSON       []byte // decoded and checked by the task, never trusted raw
       Model      string // the version the provider reports, kept as model_version
       Usage      Usage  // input, output, cache-read and cache-write tokens
       StopReason string
   }
   ```

   The first two providers are Anthropic's Messages API and the OpenAI chat
   format, starting from import-amazon's client. That module then moves onto
   the gateway, so its calls are counted like every other.

2. **Work is done by named tasks, not free prompts.** A task is Go code that
   declares:
   - its input and output types;
   - the deterministic step tried first (rules, lookups, core service calls);
   - whether a cached answer may be reused and for how long;
   - what it may retrieve;
   - its model ladder;
   - when to escalate: an output that fails its checks, or a confidence below
     the task's threshold;
   - its cost ceiling per run.

   `po.extract`, `sku.match`, `message.classify` and `reply.draft` are
   examples. The model's answer is decoded into the task's Go type, unknown
   fields are refused, and every value is checked in code before anything
   uses it.

3. **Each run writes one execution record, `ai_executions`**, which is the
   spec's AIExecution:
   - store, task, trace id, job id;
   - each step tried (rule, cache, tool, small model, strong model) and why it
     escalated;
   - provider, `model_version`, input, output and cache tokens, latency,
     outcome, `review_status`;
   - `source_refs` to the rows and documents it read;
   - estimated cost.

   **Cost is integer money.** Prices are configured per model as minor units
   per million tokens (USD 3.00 per million is `300`), so a run's cost is
   `tokens × price` in millionths of a minor unit (`cost_micro_minor`), exact
   with no floats (rule 6). With no price configured, the cost is recorded as
   unknown, never as zero.

4. **Budgets are reserved before a call, not counted after.**
   - Each store has a monthly cap in minor units.
   - A run reserves its ceiling with one conditional `UPDATE`, and settles to
     the real cost afterwards.
   - A store over its cap gets no model calls: its work goes to the human
     queue, and the panel says why.
   - Per-store and per-key request rates are limited the same way, in
     Postgres, so the limit holds across replicas.

5. **Keys, models and data-sharing are per-store plugin settings:**
   - provider, key (a secret field), small model, strong model, monthly cap;
   - a data-sharing policy listing which classes of data may leave the
     store: catalogue text, order lines, customer contact details, message
     bodies, documents.

   Contact details and message bodies are off until the owner switches them
   on. Fields a task does not need are removed before the request is built,
   and each execution records the policy it ran under.

   On a hosted install the platform may also offer its own key. That is off by
   default, metered per store against the store's cap, and switched on by the
   install's owner, not by a store.

6. **Defaults name current models and stay configurable:** Claude Haiku 4.5
   as the small model and Claude Sonnet 5.5 as the strong one, on the
   Anthropic provider. A store may choose another provider or model; the
   execution record keeps what actually ran.

7. **Untrusted content is data.** Messages, documents, supplier files and
   product text reach a model only inside fenced data blocks the instructions
   tell it to treat as data. Models get no tools that write. A model's answer
   becomes a proposal ([ADR 0003](0003-identity-rights-approvals.md)) or a
   staff-only record, never an action.

8. **Retries and fallback:**
   - Transient provider errors retry inside the job with backoff
     ([ADR 0004](0004-events-and-jobs.md)).
   - A store may name a fallback provider.
   - A refused request, an output that fails its checks twice, or an
     exhausted budget stops, and the work goes to a person.

## Consequences

- The cost dashboard (M1) is a report over `ai_executions`: per task, per
  model, per day, with escalation rates and failures.
- The token benchmark the spec asks for (§14) runs the same task set through
  this path and reads the same table. It is reproducible because tasks,
  prompts and models are versioned in code and recorded per run.
- A provider is a small file. Adding one needs no change to any task.
- Moving import-amazon onto the gateway changes where its key is configured
  (environment variable to plugin setting); the environment variable stays as
  the install-wide default during the move.

## Alternatives considered

- **Each module calls providers itself.** That is today's shape. It cannot
  enforce one budget, one data-sharing policy or one record of cost.
- **A third-party gateway service or SDK.** Rule 2 rules out an SDK in `ext/`,
  and an outside gateway would see every store's data, which is exactly what
  the data-sharing policy exists to limit.
