# Gap analysis: the AI-Native B2B spec against GoCommerce today

Audited on 21 October 2026 against `main` at `e5fc9ea6` (v1.2.40). Every
statement below was read in the code; file references are to that commit.

**The short version.** The transactional core the spec wants to keep
authoritative is already here and already strict: one state machine,
row-level security per store, an outbox that commits with every state change,
rights on every admin route, and B2B companies with credit, catalogues, quotes
and statements. What is missing is almost everything that makes an *agent*
safe to let in: there is no credential that says "this is an agent", no
approval step between an agent's proposal and a mutation, no budget or rate
limit, no record of what a model was sent or what it cost, and no draft that a
seller can prepare before a buyer sees it. Those are M0/M1 work, and they come
before any model is called.

Status words: **Exists** (usable as it is), **Partial** (a base to build on,
with the gap named), **Missing**.

## 1. Foundations the spec assumes (§2, §13)

| Spec requirement | Status | What is there | Gap |
|---|---|---|---|
| Core stays authoritative; AI never writes rows directly | Exists | Rule 3: modules never write core tables; every transition goes through a service (`app.Pay().MarkPaid`, `app.Order().Cancel`, `app.Stock().Adjust`). `ext/mcp` tools wrap service methods and never take SQL. | — |
| PostgreSQL for operational state | Exists | D2. `pgx` is the only production dependency (rule 1). | — |
| pgvector for retrieval | Missing | No extension is loaded anywhere; Compose runs stock `postgres:17-alpine`, CI `postgres:18-alpine`. | A Postgres image with pgvector, and a decision ([ADR 0002](adr/0002-tenancy.md)). |
| Redis where already supported | Missing | D10 names Redis Streams as an optional transport, but no Redis code exists. | None needed: the spec says "where already supported", and it is not ([ADR 0004](adr/0004-events-and-jobs.md)). |
| Durable jobs, retries, dead letters | Partial | The outbox (D8, D9): same-transaction write, `FOR UPDATE SKIP LOCKED`, exponential backoff to 15 minutes, dead after 12 attempts, an operator screen to retry (D49). | No general job table; modules cannot publish events (`outbox.write` is unexported). |
| Tenant isolation on every query | Exists | A store is the tenant (D83). Store tables carry `store_id` under a forced row policy bound per statement (`core/scope.go`, `core/storedriver.go`); `ext/alltest` fails any module table left out; `doctor` fails a login that bypasses row security. | — |
| …on every vector namespace, cache key, log and tool call | Partial | Module caches are keyed by store (`sync.Map` per store in payment modules). MCP over HTTP is bound to the request's store; over stdio to `Config.DefaultStore` or the only active store, refusing when there are several. | The request log line has no store and no actor (`core/httpx.go:404`). No vectors exist yet. |
| Versioned domain APIs | Partial | Unversioned `/api/...` by decision (D16); events carry `event_version`; every route is in the merged OpenAPI document (`core/openapi.json` plus each module's fragment), enforced by tests and by `doctor`. | No versioning for module routes; see [ADR 0001](adr/0001-service-boundaries.md). |
| Typed tool schemas | Exists | MCP tools declare JSON Schema inputs (`ext/mcp/tools.go`). | MCP speaks protocol revision `2024-11-05`; no resources or prompts. |
| RBAC | Exists | 50 core rights (`core/rights.go`), custom roles (D80), module rights (D65), checked per route and per MCP tool. | — |
| Tenant-scoped credentials | Partial | API keys are bound to one store and carry one of four built-in roles. | Keys cannot carry a custom role or a rights subset, have no expiry, and keep a readable copy of the secret (M46, behind `apikeys.write`). The static admin token reaches every store with every right. |
| Agents authenticated apart from developers | Missing | An API key resolves to a `Superuser` with id 0; MCP documents the admin token or a session. | [ADR 0003](adr/0003-identity-rights-approvals.md). |
| Approval gates | Missing (store side) | Approvals exist only inside a buying company (`b2b_approvals`, D69). | Beyond the caller's rights, no approval step stands between an agent and `refund_order`, `set_variant_price` or `create_discount`; `Config.ReadOnly`, which withholds every mutating tool, is the only other brake. |
| Audit trail | Partial | `admin_audit` is written in the same transaction as the change, with actor kind, id, role and label (`core/audit.go`). | Modules cannot write to it; its vocabulary is closed; every API key is recorded as operator id 0, told apart only by its "api key: name" label, and the actor filter merges them all; `mcp_audit` has no actor column, records no reads, and is written after the call, outside the transaction. |
| Idempotency keys on mutations | Partial | `Idempotency-Key` on `POST /api/checkout/{code}` (D15), reused by B2B checkout and approvals. | No admin or MCP route takes one; the table is never pruned. |
| Webhook verification | Exists (inbound) | Each payment provider verifies its signature and claims `(store_id, event id)` in its own table before acting. | No Meta signature check yet. |
| Secrets in a vault, encrypted at rest | Missing | Plugin secrets, API key copies and webhook secrets are plain text in Postgres. Plugin secrets are masked on every read; an API key's copy can be read in full with `apikeys.write`; a webhook secret is shown in full when created or rotated. | Envelope encryption with a key outside the database; [threat model](threat-model.md). |
| Observability | Partial | Structured `slog` text logs; error tracking per store (D86); 17 `doctor` checks, also as JSON. | No request or trace ids, no metrics endpoint, no JSON log option. |
| Rate limits, budgets, cost limits | Missing | Only in-process throttles on sign-in, sign-up, resets and a few public forms. | Per-store and per-key limits and spend budgets, reserved atomically. |
| Retention and deletion | Partial | Store deletion cascades; shopper accounts can be erased (`accounts.erase`); newsletter keeps consent and unsubscribes. | Orders and outbox payloads keep personal data after erasure; nothing prunes audit, outbox or idempotency rows; no per-person export. |
| Feature flags | Partial | Plugins are per-store switches with typed settings (D57, D59), audited. | Enough for "behind feature flags" (§15.4): every AI module installs idle and is switched on per store. |

## 2. AI foundation (§3)

| Spec item | Status | What is there | Gap |
|---|---|---|---|
| 3.1 Context Engine | Partial | Sources exist: the merged OpenAPI document (`App.Spec()`, `GET /doc`), `App.Routes()` with each route's rights, `RightsCatalogue()`, `doctor -json`, 16 `skills/` pages with front matter, `docs/writing-a-module.md`, the decision table, event constants in `core/events.go`. `check-docs.ps1` is a working drift check. | No search index; no operationIds or per-operation rights in the spec; core right labels live only in panel code; no machine-readable event catalogue. Some docs have drifted (below), and a context engine would serve the drift as fact. |
| 3.2 MCP server | Partial | `ext/mcp`: JSON-RPC at `POST /api/admin/x/mcp` and `ServeStdio`; 19 tools (9 read, 10 mutating), each with rights; `Config.ReadOnly`; `Config.Tools` for tools from other modules; `mcp_audit` with a screen. | The shipped binary (`cmd/gocommerce`) does not include it. Agents get full emails, phones and addresses on a real store (masking runs only on demo stores). No tools for companies, quotes, price lists or drafts, and no read of stock per location. |
| 3.3 AI Gateway | Missing | One model caller exists: `ext/import-amazon/optimize.go`, a plain `net/http` client for Anthropic's Messages API and the OpenAI chat format (OpenAI, Gemini's compatible endpoint, local servers). | No shared provider interface, no per-store keys (keys are process-wide env vars), no usage or cost recorded, no budgets, no routing between model sizes, no output validation layer. [ADR 0005](adr/0005-model-providers.md). |
| 3.4 Memory | Missing | Building blocks only: order notes in `orders.metadata.notes` (D48), the store profile, plugin settings, saved reports. | A store-scoped memory table with scope, source, review status, expiry and revocation; vectors later. |
| 3.5 Extension SDK | Partial | The `Module` interface, enforced route namespaces, ports (payment, fulfilment, notifier, translator), `Subscribe`, the checkout guard (D67), `PluginDef`, `RegisterRight`, `gctest` fixtures (`New`, `CreateProduct`, `PlaceOrder`, `Buy`, `Ship`, `StubHTTP`, `AssertSpecCoversModuleRoutes`, `AssertAdminRoutesDeclareRights`), `examples/store`, `ext/alltest`. | No manifest, module version or engine-compatibility check. Runtime code loading is ruled out by D57, so "sandbox execution" means building and testing a generated module in its own process, never inside the engine. |

## 3. Reference data contracts (§11)

Core is guest-first and has no customer on an order (rule 8, D22). Companies
and accounts live in `ext/identity` and `ext/b2b`.

| Object | Status | Where | Gap |
|---|---|---|---|
| Tenant | Exists | `stores`, `store_domains`, `store_members` | — |
| Customer | Partial | Core groups orders by email (`GET /api/admin/customers`); accounts are `identity_customers` | No admin route to create one; `orders` has no index on email. |
| Account | Exists | `b2b_companies`: tax id, group, credit limit, net days, approval threshold, PO rule, catalogue; `b2b_members` with admin, approver and buyer roles | — |
| Product, Variant | Exists | `products`, `variants`; lookup by SKU | — |
| PriceList | Exists | `customer_groups`, `price_lists`, `price_list_prices` with quantity breaks; agreed line prices win (D66, D68) | — |
| InventoryPosition | Exists | `variant_stock` per variant and location; `stock_movements` ledger | — |
| Conversation | Missing | — | Threads for the sales inbox. |
| Message | Partial | `contact_messages` (single web-form posts); `notifications` (outbound send log) | Inbound channel messages with media. |
| Lead | Partial | `b2b_leads` routes consumer enquiries to dealers | Not a sales-pipeline lead. |
| RFQ | Partial | A `b2b_quotes` row in `requested` | Only a signed-in buyer can raise one, and every line must already be a variant: a free-text item cannot be asked for. |
| Quote | Exists | `b2b_quotes`, priced, sent, accepted into an order at agreed prices, 30-day default expiry | Buyer-initiated only. |
| DraftOrder | Missing | Orders have no draft status; `POST /api/admin/orders` reserves stock and emits `order.created` at once | See §5 below. |
| Order | Exists | `orders`, `order_lines`, timeline | — |
| SupplierCatalogImport | Partial | CSV with `?dry_run=`, Shopify and Amazon importers with job tables | No approval step, no per-row diff, no lineage for CSV runs; `product.import` is declared in the audit vocabulary but never written. |
| CampaignMetric | Missing | Showcase and cart-recovery stats only | — |
| AIExecution | Missing | — | [ADR 0005](adr/0005-model-providers.md). |
| ApprovalRequest | Partial | `b2b_approvals` (buyer side) | Store-side approvals of agent proposals: [ADR 0003](adr/0003-identity-rights-approvals.md). |
| MemoryRecord | Missing | — | M1. |
| AuditEvent | Partial | `admin_audit`, `mcp_audit`, `b2b_company_history` | See §1. |

## 4. Sales inbox and channels (§4)

| Need | Status | What is there |
|---|---|---|
| WhatsApp Cloud API, coexistence onboarding | Missing | The only WhatsApp code is a storefront chat-button plugin and a disabled recovery action. |
| Inbound messages, webhooks, signature checks | Missing | Twilio and MSG91 send SMS only and mount no routes. |
| Conversation threads, timeline, classification | Missing | — |
| Seller-approved replies | Missing | — |
| Messaging windows, templates, consent | Partial | Notification templates (D58) and "who may send" (D50) exist for email and SMS; consent exists only as newsletter email subscriptions. Nothing records phone or WhatsApp consent. |

## 5. AI Order Desk (§5)

The validations the spec wants done "using core APIs" are mostly there, but
scattered, and the object a seller would approve does not exist yet.

| Step | Status | What is there | Gap |
|---|---|---|---|
| Ingest WhatsApp, email, PDF, spreadsheet | Missing | CSV parsing for catalogue import | No intake object; no PDF reader (the standard library has none; see [ADR 0001](adr/0001-service-boundaries.md)). XLSX can be read with `archive/zip` and `encoding/xml`. |
| Resolve customer identity | Partial | Companies, members, verified email on carts (D66) | No phone-to-company mapping; no record of a buyer's own part numbers. |
| Previous orders | Partial | `GET /api/admin/x/b2b/companies/{id}/orders`, indexed by company | No "lines this company buys" query. |
| Catalogue candidates, SKU mapping with confidence | Missing | Exact SKU lookup; title search is `lower(title) LIKE` (`core/catalog.go:1351`); optional Meilisearch module | No cross-reference table, fuzzy or semantic search. |
| MOQ and units of measure | Missing | Only quantity price breaks (`price_list_prices.min_quantity`) | Minimum quantity, pack size, case quantity and increments per variant. |
| Account price, tax, availability, delivery | Partial | Whose price applies is decided in one place (`core/pricing.go`); catalogue eligibility, credit headroom and stock each have a service | No single dry run that prices a basket with tax and shipping without creating a cart: tax is computed only inside checkout. |
| Seller-approved draft → quote or order | Missing | A quote converts into an order at agreed prices | The seller cannot start a quote; recommended shape: let the seller open a company quote with the agent's lines, approve it, send it, and let it convert as today. That reuses the agreed-price path rather than adding a draft state to core's order machine (S4). |
| Provenance per inferred line, exceptions queue | Missing | — | Part of the order-desk module's own tables. |

## 6–9. Procurement, sales, marketing, Meta Ads, finance

| Area | Status | Base to build on | Gap |
|---|---|---|---|
| Supplier onboarding, RFQ, comparison, substitution (§6) | Missing | Catalogue import, taxonomy and attribute dictionary (D41, D42) | No supplier object, no purchasing side at all: GoCommerce sells; it does not buy. Everything in §6 is new. |
| Opportunity, churn, cross-sell, lost sales, demand (§7) | Partial | Customers sorted by last order; quote states including declined and expired; read-only reports on a login that can reach one store (D78, D84) | No inactivity filter for companies; no lost reasons on quotes; no metric catalogue between a question and SQL. Custom reports run owner-written SQL, which is not what §7's "no arbitrary SQL" allows an agent to do. |
| Follow-up suggestions | Partial | Recovery automations (D92): fixed basket triggers and an email action | A new trigger type and a suggest-only action. |
| Meta Ads (§8) | Partial | A Meta catalogue feed (`/x/feeds/meta.csv`) and a client-side Pixel ID setting | No Conversions API, no Marketing API, no capture of UTM, `fbclid` or `gclid` on carts and orders. |
| Accounts receivable (§9) | Partial | Credit limits, net days, statements with ageing buckets (D74), receivables list | No partial payments, promises to pay or disputes; invoices are issued only when an order is paid. |
| Setup assistant, integration builder (§10) | Partial | Every setting already goes through a validated, audited route | Nothing conversational yet; that is M6. |

## Drift to fix before a context engine serves it

A context engine that answers from these would answer wrongly:

- `AGENTS.md` rule 9 says "a module never invents a right"; D65 and
  `core/rights_registry.go` let modules register rights.
- `AGENTS.md` and `skills/integrations.md` say MCP has twelve tools; it has 19.
  `skills/integrations.md` also gives the wrong right for its routes.
- `docs/events.md` says there is no `product.*` family; `core/events.go`
  defines `product.created`, `product.updated`, `product.deleted` and
  `collection.updated`, and the doc's taxonomy table omits `order.edited`, `order.unpaid` and
  `order.undelivered`.
- `ext/mcp` documents `invoices.Tools()`, which does not exist.

## Defects found along the way

- MCP's `sales_report` labels its figures with the install's currency
  (`m.app.Config().Currency`, `ext/mcp/tools.go:590`), not the store's
  (`app.Currency(ctx)`, D85). On a store selling in another currency the
  numbers are labelled wrongly.
- The agent settings screen gates on `store.operate`, not the module's own
  `agent.read`.

## What M0 still owes after these documents

The spec's M0 also asks for stable domain contracts, test fixtures and a
token and cost baseline. Those are the next step, after the decisions below
are accepted, because the contracts depend on them: the approval object
([ADR 0003](adr/0003-identity-rights-approvals.md)), the execution record
([ADR 0005](adr/0005-model-providers.md)), and where a draft lives (§5 above).
