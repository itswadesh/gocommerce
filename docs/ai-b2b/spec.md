# GoCommerce AI-Native B2B — Master Technical Specification

Version 1.0. Status: proposed implementation baseline. The 3D Customizer is
complete and excluded from this scope.

This is the owner's specification, kept here unchanged in substance so the
[gap analysis](gap-analysis.md), the [decision records](README.md#decision-records)
and the [threat model](threat-model.md) can cite its sections by number.

## 1. Product thesis

Open-source, production-grade B2B commerce engine with deterministic
transactional core and opt-in AI extensions. Developers should extend commerce
workflows with Claude Code/Cursor through stable APIs and MCP instead of
regenerating commerce primitives. AI is invoked only for ambiguity,
unstructured inputs, semantic interpretation, and reasoning; conventional code
handles validations, arithmetic, inventory, permissions and execution.

No unverified claim of 100x token savings. Benchmark actual costs, tokens,
latency, and task success against a defined baseline.

## 2. Non-negotiable architecture

- Go services for orchestration and domain APIs; existing GoCommerce core
  remains authoritative. SvelteKit frontend where appropriate.
- PostgreSQL for operational state; pgvector for semantic retrieval, never as
  source of truth for price, inventory, or entitlements.
- Redis for caching and queues where already supported; durable job/outbox for
  retries, idempotency, and auditability.
- Multi-tenant isolation on every query, vector namespace, cache key, log, and
  tool invocation.
- Versioned domain APIs, typed tool schemas, webhooks, RBAC, tenant-scoped
  credentials, approval gates, and contract tests.
- Separate services/modules: ai-gateway, commerce-context, mcp-server, memory,
  integrations, order-intelligence, sales-inbox, procurement-intelligence,
  sales-intelligence, marketing-intelligence, extension-sdk.
- AI cannot directly write database rows, send customer messages, issue
  refunds, alter prices, or publish changes without authorized domain tools and
  policy checks.

## 3. AI foundation

### 3.1 Commerce Context Engine

Curated machine-readable resources: entity schemas, API contracts, workflow
graphs, extension points, examples, catalog taxonomy, merchant policies, and
tenant-specific configuration. Provide `get_entity_schema`,
`search_capabilities`, `get_workflow`, `find_extension_point`,
`get_integration_contract`, `search_docs`, and `get_policy` endpoints. Return
bounded, versioned context snippets rather than whole repositories.

### 3.2 MCP Server

Read-only tools by default; mutating tools require explicit scopes and
approvals. Tool examples: `search_products`, `get_customer_order_history`,
`get_inventory`, `calculate_quote`, `create_draft_order`, `validate_extension`,
`run_contract_tests`. Authenticate developers and agents separately; maintain
audit trails and enforce tenant isolation.

### 3.3 AI Gateway

Pipeline: intent/rules → cached response or deterministic tool → retrieval →
small model → stronger model only if confidence or complexity demands. Record
tokens, estimated cost, latency, model, success, and escalation reason per
task/tenant. Prompt-injection defenses, PII minimization, redaction, strict
output schemas, rate limits, budgets, retries, and fallback.
Merchant-selectable providers and BYOK optional.

### 3.4 Memory

Use PostgreSQL structured memory for stable project decisions and
customer-approved preferences; pgvector for embeddings of documentation,
catalog descriptions, and relevant past interactions with TTL and consent.
Version, scope, revoke, and delete memories. Never persist unverified AI
guesses as facts.

### 3.5 Extension SDK

Go interfaces, event hooks, typed APIs, test fixtures, sandbox execution and
versioned manifest. AI extension generator proposes code and tests; developer
reviews before deployment. CI validates permissions, compatibility,
performance and regression tests. Marketplace is later-stage, not
prerequisite.

## 4. AI Sales Inbox — confirmed

- Connect an eligible existing WhatsApp Business App number using official
  Meta onboarding/coexistence where available. Support fallback to supported
  API migration or a dedicated number when coexistence unavailable, only with
  merchant consent.
- Verify current Meta requirements, event coverage, onboarding and messaging
  rules during implementation; do not assume all existing chats or historical
  messages are available.
- Receive webhook events; verify signatures; deduplicate; normalize inbound
  messages and media; associate with customer using verified identifiers.
- Classify enquiry, quote, order, support, payment question, opt-out; surface
  thread timeline and suggested replies.
- Seller approval mandatory for outbound AI replies in initial release; no
  promotional campaigns or broadcasts.
- Enforce messaging windows, templates, applicable consent, privacy and
  retention policies. Do not claim that an inbound message grants blanket
  permission for later outreach.
- KPIs: first response time, draft-to-order conversion, manual minutes saved,
  cost per conversation.

## 5. AI Order Desk — flagship

Input: WhatsApp messages, email, PDFs, spreadsheets. Workflow: ingest → parse
structured line items and references → resolve customer identity → retrieve
previous orders and relevant catalog candidates → map SKU with confidence →
validate MOQ/UOM, account pricing, tax, availability and delivery rules using
core APIs → flag ambiguities → seller-approved draft order → quote/order
conversion. Store provenance for every inferred line item and original
attachment. Never silently substitute or confirm price. Handle multilingual
messages and duplicate PO numbers. Add human-in-the-loop exceptions queue.

## 6. Procurement and catalog intelligence

- Supplier Catalog Onboarding: map supplier XLSX/CSV/PDF into canonical
  schema, suggest missing attributes, detect duplicates, preview and approve
  imports, record lineage.
- RFQ Assistant: extract buyer requirements and prepare supplier-specific RFQ
  drafts; approvals before sending.
- RFQ Comparison: normalize currencies, units, taxes, shipping, lead time, MOQ,
  and commercial terms; compute landed cost deterministically; AI interprets
  nonstandard clauses with citations.
- Product Substitution: semantic shortlist plus hard compatibility rules
  (dimensions, material, voltage, standards, etc.), confidence labels,
  explicit buyer/seller approval.
- Product Enrichment / Semantic Search / Data Quality: controlled generation
  with source references and quality review.
- Procurement Copilot / Supplier Selection: rank according to configurable
  objective and explain tradeoffs; no autonomous purchasing.

## 7. Sales and marketing intelligence

- Opportunity Detector: reorder-cycle deviation, inactive accounts, expiring
  quotes and buying signals; explain trigger with data.
- Sales Follow-up Assistant: suggested messages and next actions, approved by
  seller and compliant with channel rules.
- Churn Risk: interpretable risk signals, not opaque certainty.
- Cross-sell/Upsell: product compatibility, account pricing, historical basket
  patterns and margin constraints.
- Personalized B2B Catalog: customer group, permissions, contracts, purchase
  patterns.
- Lost Sales Analyzer: quote funnel, documented lost reasons, uncertainty
  clearly identified.
- Demand Intelligence: forecast with seasonality, lead times, confidence
  intervals and backtesting.
- Campaign Intelligence: segment recommendations only, with consent and manual
  activation.
- AI Commerce Analytics: natural-language questions compiled to safe read-only
  metrics queries; no arbitrary SQL execution.
- Technical Sales Engineer: guided spec intake and evidence-backed
  recommendations.

## 8. Meta Ads — analysis-only scope confirmed

- Merchant connects existing Meta Ads account via authorized OAuth
  permissions.
- Import campaign/ad-set/ad metrics and map to GoCommerce orders, inquiries
  and RFQs where attribution data and permissions allow.
- Implement Meta Pixel/Conversions API event pipeline with deduplication,
  consent controls and data minimization. Respect attribution limitations,
  privacy changes and unmatched leads.
- Dashboard: spend, attributed revenue, ROAS (with methodology), RFQ-to-order
  funnel, gross-margin-aware insights, underperformers, recommendation queue.
- Excluded now: campaign creation, creative publishing, automated bidding,
  automated budget changes.

## 9. Finance intelligence

Accounts Receivable Assistant: summarize aging, identify customer
promises-to-pay and invoice disputes from permitted messages, suggest
collection follow-ups for approval. Financial ledger and invoice balances
remain deterministic. Never auto-charge or auto-write-off.

## 10. Merchant setup and integrations

Conversational setup assistant configures company profile, tax/shipping
settings, customer groups and catalog import through validated forms/tools.
AI Integration Builder scaffolds ERP/CRM connectors from documented contracts
and test fixtures, never deploying unreviewed code. Business Skills
Marketplace deferred until SDK maturity.

## 11. Reference data contracts

Core objects: Tenant, Customer, Account, Product, Variant, PriceList,
InventoryPosition, Conversation, Message, Lead, RFQ, Quote, DraftOrder, Order,
SupplierCatalogImport, CampaignMetric, AIExecution, ApprovalRequest,
MemoryRecord, AuditEvent.

All AI outcomes must carry `tenant_id`, `source_refs`, `confidence` (where
applicable), `model_version`, `created_at`, `review_status`, and `trace_id`.
Mutations use idempotency keys.

Suggested API surface (adapt to existing GoCommerce conventions):

- `POST /v1/integrations/whatsapp/connect`, `POST /v1/webhooks/meta/whatsapp`
- `GET /v1/sales-inbox/conversations`, `POST /v1/sales-inbox/replies/{id}/approve`
- `POST /v1/ai/order-intake`, `GET /v1/ai/order-intake/{jobId}`,
  `POST /v1/draft-orders/{id}/approve`
- `POST /v1/ai/catalog-imports`, `POST /v1/ai/rfq-comparisons`
- `GET /v1/ai/sales-opportunities`, `POST /v1/ai/followups/{id}/approve`
- `POST /v1/integrations/meta-ads/connect`, `GET /v1/analytics/meta-ads`
- `GET /v1/ai/usage`, `GET /v1/ai/audit`

## 12. Implementation roadmap and acceptance gates

- **M0 (foundation, prerequisite):** Audit current codebase and define stable
  domain contracts, tenant isolation, test fixtures and baseline token/cost
  metrics. Deliver ADRs and threat model.
- **M1 (AI foundation):** Context Engine, read-only MCP, AI Gateway, memory,
  cost dashboard, SDK skeleton. Gate: permission tests, context correctness,
  token benchmark reproducibility, tool audit.
- **M2 (revenue flagship):** WhatsApp coexistence onboarding where eligible,
  inbox, PO extraction, catalog matching, draft orders, seller approvals.
  Gate: representative end-to-end demo and measured accuracy with
  manual-review handling.
- **M3 (procurement):** Supplier import, RFQ assistant/comparison, product
  substitution and catalog enrichment. Gate: verified units, prices, specs,
  provenance, and approval workflows.
- **M4 (sales intelligence):** Opportunity detector, follow-ups, churn,
  cross-sell, lost-sales analytics, demand insights. Gate: explainability, no
  unauthorized outreach, backtests.
- **M5 (Meta Ads):** Read-only campaign connection, commerce attribution
  dashboard, Conversions API, recommendations. Gate: consent, event
  deduplication, clear attribution caveats, no campaign mutations.
- **M6 (developer ecosystem):** Extension generator, integration builder,
  setup assistant, richer SDK and skills packaging. Gate: generated extensions
  pass sandbox and contract tests.

Timelines and estimates require repository audit, integration eligibility and
test data; do not promise dates without those inputs.

## 13. Security and operational requirements

Encryption in transit/at rest, secret vault, scoped tokens, webhook
verification, least privilege, data retention/deletion, GDPR and local privacy
controls where applicable, configurable model data-sharing policy,
tenant-safe retrieval, human approval, observability, retries and dead-letter
queues, cost limits and incident response.

## 14. KPIs and benchmarking

Compare against manual processing and generic AI coding baseline. Measure: PO
line-item exact-match accuracy, exception rate, median order-processing time,
human approval burden, token usage per completed task, cost per approved
order, sales conversion, quote turnaround, integration success, and p95
latency. Benchmark on identical task sets; never market arbitrary savings
multipliers.

## 15. Claude Code execution instructions

1. Inspect existing GoCommerce repository first; do not rewrite functioning
   core services.
2. Produce a gap analysis mapping existing APIs/data models to this spec.
3. Draft ADRs for service boundaries, tenancy, auth, event transport, and
   model provider interface.
4. Implement M0 and M1 behind feature flags, with migrations, tests,
   observability and sample fixtures.
5. Pause at each milestone gate for review; do not implement outbound
   messaging or monetary mutations without explicit approval flows.
6. Provide working examples, deployment documentation, API specs and
   reproducible token benchmarks.

## 16. Confirmed exclusions and decisions

- 3D Customizer already built; not part of this workstream.
- Existing WhatsApp Business number preferred; actual coexistence depends on
  Meta eligibility.
- No promotional WhatsApp messaging in initial release.
- No Meta campaign creation or autonomous budget changes.
- Seller approval for AI-generated customer replies and drafts initially.
- Core commerce remains authoritative; AI does not replace transactional
  rules.
