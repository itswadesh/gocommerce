# ShopNx AI: foundation audit

Ticket 1 of the [ShopNx AI plan](shopnx-ai-plan.md): every service and feature
the plan names, set against the code on `main` at `1545a9b2` (29 October 2026),
with what to reuse and what is missing. It answers the plan's technical
verification gate. It reads code, not the hosted install: which stores have
the gateway switched on, with which key and cap, is in each store's plugin
settings and is not visible from here.

Status words: **Built** (on `main`, in the deployed binary), **Partial** (a base
exists, the AI part does not), **Missing**.

## Foundations (plan §10)

| Plan component | Status | What exists | Gap |
|---|---|---|---|
| Model gateway | Built | `ext/ai-gateway` (D107): named Go tasks with a strict JSON schema, a small → strong model ladder, escalation on a failed check or low confidence, a per-store answer cache, per-store data policy (catalogue, order lines, contact details, message bodies, documents) with contact details scrubbed by shape, a monthly cap reserved before each call, and one `ai_executions` row per run with tokens and integer cost. Providers: Anthropic and the OpenAI format. In `docker-compose.yml` as `-ai-gateway`, idle until a store sets a key, a cap and prices under Plugins. | Daily and per-task caps; a cost estimate shown before a batch (the gateway already computes a run's ceiling from its token limits); no image-generation provider; the default small model is `claude-haiku-4-5`, a generation behind Haiku 5.5. AI credits, below. |
| Job processing | Built | `ai_jobs` (D106): written in the caller's transaction, claimed with `FOR UPDATE SKIP LOCKED`, renewed while running, retried with backoff for transient failures only, stores served in turns. AI › Jobs lists them with retry and cancel. | Per-row progress for a merchant's batch: `ext/photo-products` keeps its own batch rows, and bulk enrichment needs the same. |
| Approval service | Built, unused | The approvals queue (D105): a proposal filed once per idempotency key, its preview computed when filed and recomputed before the decision, a person's decision recorded, AI › Approvals in the panel. | No action is registered with it yet. MCP's writes were to arrive in spec step 5. |
| Tool registry | Partial | `ext/mcp`: 15 tools. Reads: products, orders, customers, low stock, a sales report. Writes: create product, create discount, set a variant's price, mark paid, mark delivered, ship, cancel, refund. Contact details are masked. Read-only agent keys exist (D105) and the panel has Agent activity. | `ext/mcp` is in `examples/store` only, not in `cmd/gocommerce`, so no deployed store has it. Its writes act directly under the caller's rights instead of filing proposals, so they must move onto the approvals queue before it ships. |
| Store knowledge | Missing | The data is there: FAQ, CMS pages (the six standard pages, D145), products, orders, shipments with tracking links. | `ext/context` and `ext/memory` (spec steps 6–7) are not built. |
| Observability | Partial | Executions with model, tokens, cost, outcome and every step tried; AI › Usage per month. | Merchant feedback on an answer (`ai_feedback`); accepted and corrected field counts; the token benchmark (spec step 9). |
| Security | Built | Store isolation by the row policy on every AI table (D104), rights `ai.read`, `ai.configure`, `ai.approve`, sealed secrets (D110), outbound address guard (D109), store content sent as tagged data apart from the instructions, the [threat model](threat-model.md), agent keys rate limited per minute. | None found for the first features. |

### The plan's tables and routes, mapped

The plan's names are illustrative and it asks to reuse what exists. Rule 9
puts module routes under `/api/admin/x/<module>/`, so there is no general
`/api/admin/ai/tasks`: each feature is a module route that runs a gateway task.

| Plan name | Existing equivalent |
|---|---|
| `ai_task_runs`, `ai_usage` | `ai_executions` and the monthly budget rows |
| `ai_proposals`, `ai_approvals` | the approvals queue |
| `ai_job_items` | `ai_jobs`, plus a module's own batch rows |
| `ai_feedback` | missing |
| `ai_knowledge_sources` | `ext/context`, `ext/memory`, not built |
| `support_threads`, `support_messages` | missing; the spec's `ext/sales-inbox` |
| `campaign_drafts` | missing |
| `tenant_id` | `store_id` (D104) |

## Features

### Catalogue (plan §3)

| ID | Status | Base | Next |
|---|---|---|---|
| CAT-01 | Built for new products | `ext/photo-products` (D144): up to 60 photos become draft products and variants, described from what the photos show, unpriced. | The edit path, "Improve with AI" on an existing product, is being built as D151. |
| CAT-02 | Missing | `ai_jobs`; the products list's bulk bar. | A batch on the D151 task with per-row results and a cost estimate. |
| CAT-03 | Partial | The Shopify taxonomy is the master and a store activates nodes (D146); photo-products picks a store category. | A task that proposes a taxonomy node and its attributes. |
| CAT-04 | Partial | The SEO panel; photo-products writes SEO copy and alt text for new products. | Existing products in D151; collections and pages after. |
| CAT-05 | Partial | `ext/translations` stores a translation per language and serves it. | A task that drafts translations into it for review. |
| CAT-06 | Partial | "Needs a price" filter (#162). | Rules first: no image, no description, duplicate titles. |
| CAT-07, CAT-08, MKT-11 | Missing | The engine scales a store's pictures on request (D142). The sample fashion catalogue's colours were made by hand in ChatGPT, not by the engine. | Needs an image provider, which is an owner decision. |
| CAT-09, CAT-10 | Missing | Collections, CMS pages, FAQ, video showcases, the home page composer. | Later tasks on the same gateway. |
| Media alt text (owner's addition) | Missing | The media library's alt field. | A batch like CAT-02 over pictures with no alt text. |

### Marketing (plan §4)

| ID | Status | Base | Next |
|---|---|---|---|
| MKT-01 | Missing | Email through Resend or SendGrid, SMS through MSG91 or Twilio, Klaviyo, the newsletter, message previews. | A copy task with the store's tone. |
| MKT-02 | Partial | Recovery automations as workflows (D92) with fixed messages, suppression and a send dialog. A basket has no phone number, so recovery is email only. | Drafted copy inside the existing send step. |
| MKT-03, BI-05 | Partial | Customer groups, which today set prices. | RFM and lifecycle groups in SQL; no model. |
| MKT-04 | Partial | Variants carry a cost (`cost_minor`), so margin is computable where the shop fills it in; stock and sales are there. | A deterministic picker; the model only writes copy. |
| MKT-05 | See CAT-04 | | |
| MKT-06 | Partial | Recovery automations, basket triggers only. | A post-purchase and lapsed-customer trigger. |
| MKT-07 to MKT-10, BI-06 | Missing | A Meta catalogue feed and a Pixel setting. Carts and orders record no UTM, `fbclid` or `gclid`, so attribution has nothing to read. | Capture campaign parameters first. |

### Sales and B2B (plan §5)

| ID | Status | Base | Next |
|---|---|---|---|
| SAL-01, SAL-10 | Missing | Chat plugins link to Tidio, tawk.to or WhatsApp. | Needs store knowledge (`ext/context`). |
| SAL-02 | Partial | Hand-picked upsells and basket suggestions (D134). | Co-purchase rules, no model on page views. |
| SAL-03 | Partial | Abandoned checkouts and carts lists, the recovery report. | A metrics summary like BI-01. |
| SAL-04, SAL-05 | Partial | `ext/b2b`: quotes, price lists, companies, terms (D69–D74). A drafted order is a quote a person opens (D108). | The spec's AI order desk on the gateway. |
| SAL-06 | Partial | Leads with routing and territories. | A summary task. |
| SAL-07 to SAL-09 | Missing | | |

### Support (plan §6)

All twelve are missing. There is no inbox. The parts to build on: contact
messages (`ext/contact`), the FAQ, the standard policy pages, the order
timeline, shipments with carrier tracking links, the notifications log, and
customer sign-in (`ext/identity`) for checking who is asking. The spec's
`ext/sales-inbox` and `ext/whatsapp-cloud` are the planned home. WhatsApp and
Instagram messaging need the store's own Meta business accounts.

### Business intelligence (plan §7)

| ID | Status | Base | Next |
|---|---|---|---|
| BI-01 | Partial | Sales and payouts reports, the sales chart, MCP's sales report. | Fixed metric queries with a short explanation; no model-written SQL. |
| BI-02, BI-04 | Partial | Inventory, stock history, low-stock tool. No supplier or lead time exists. | Rules first. |
| BI-03 (review summaries, an owner's addition) | Missing | `ext/reviews`. | A summary task per product. |
| BI-07 | Partial | Telegram alerts. | |
| BI-08 | Missing | | |

### Copilot (plan §8)

Missing as a panel feature. Its tool layer is `ext/mcp` (above), which has to
reach the deployed binary with writes filed as proposals first.

### Theme colours from the logo (owner's addition)

In review as #169 (D149). It reads the logo's colours in the browser with no
model call, so it spends no AI credit.

## Decisions taken in this audit

1. **No second AI backend.** Every plan feature is a module task on
   `ext/ai-gateway` (D103, D107). The plan's routes become module routes and
   its tables map onto the ones above.
2. **In the panel, the person's own Save is the approval.** An AI answer fills
   the form and the normal save checks rights, validates and records the
   change. The approvals queue is for agents, bulk changes and sends.
3. **Rules before models.** Segments, margins, eligibility, reorder points and
   recommendations are SQL; a model writes words and reads pictures.
4. **Order of work**, adapting the plan's §15:
   1. this audit;
   2. "Improve with AI" on an existing product (CAT-01 edit, CAT-04), D151;
   3. bulk enrichment and media alt text on the same task (CAT-02);
   4. review summaries (BI-03);
   5. recovery and campaign copy inside the existing send steps (MKT-01,
      MKT-02);
   6. sales summary on fixed metrics (BI-01, SAL-03);
   7. RFQ to quote (SAL-04, SAL-05) on D108;
   8. `ext/mcp` in the binary, writes as proposals, then the copilot;
   9. the support inbox, once the owner has chosen its channels.

## What only the owner can decide

- **AI credits.** The owner said on 29 October 2026 that AI features are the
  paid part and consume AI credit. Today each store brings its own model key
  and sets its own monthly cap; a platform-wide key is off by default and,
  switched on, metered against each store's cap (D107). Credits need a price
  per credit, who holds the provider key, and how a store tops up.
- **Image generation.** Which provider makes and edits pictures (CAT-07,
  CAT-08). The owner's standing rule is Google Gemini; the owner allowed
  ChatGPT for the sample catalogue's colour edits on 28 October 2026.
- **Support channels.** Whether the inbox starts with contact messages and
  email only, or with WhatsApp, which needs a Meta business account per store.
- **The small model.** Moving the default from `claude-haiku-4-5` to Haiku 5.5
  changes every store's cost per run.
