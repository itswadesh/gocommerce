# ShopNx AI — Master Feature & Implementation Plan

Platform: ShopNx Admin / GoCommerce. Document type: product requirements and
technical implementation roadmap. Date: 29 October 2026. Status: proposed
features and implementation requirements; not a statement that these AI
features are live.

This is the owner's plan, kept here unchanged in substance so the
[audit](shopnx-ai-audit.md) can cite its feature IDs. The owner added four
ideas to it the same day; they are at the end.

## 1. Product vision

Build AI into the ShopNx merchant workflow so a seller can launch products
faster, attract customers, convert more sales, resolve support requests, and
make better operating decisions.

### Design principles

1. **AI where work happens.** Put contextual actions in Products, Categories,
   Marketing, Orders, Customers, B2B and Reports. Avoid a chatbot-only design.
2. **Grounded in store data.** Read live products, stock, price lists,
   policies, orders, customers and allowed reports; never invent a price,
   tracking link, refund approval or product fact.
3. **Draft → preview → approve → execute.** AI proposes; deterministic Go
   services validate; a permitted merchant or staff member approves important
   changes.
4. **Low token cost.** SQL/rules first, small models for basic generation and
   classification, stronger models only for hard tasks; batch, deduplicate and
   cache safely.
5. **Merchant privacy and multi-tenancy.** Strict store isolation, role-based
   access, scoped tools, redaction, consent handling and audit trails.
6. **Outcome-driven.** Track time saved, publish rate, conversion/recovery
   uplift, support resolution, quote turnaround and inference cost per
   completed task.
7. **Transparent uncertainty.** Mark inferred or missing product attributes;
   ask the merchant to verify measurements, composition, certifications and
   claims.
8. **India-first, extensible globally.** Support English, Hindi and other
   regional languages, WhatsApp and COD workflows while keeping
   channels/provider integrations modular.

### Priority convention

- **P0:** first releases. Frequent tasks, clear ROI, modest cost/risk.
- **P1:** next releases. Requires more integration or store data.
- **P2:** advanced / optional. Higher engineering cost, complex evaluation, or
  longer data history.

### What is available in the current admin

The inspected GoCommerce admin navigation includes: Home, Orders, Abandoned
Checkouts, Carts, Invoices, Payouts, B2B Quotes, Approvals, Receivables,
Products, Collections, Categories, Translations, Inventory, Price Lists, B2B
Catalogues, Reviews, Customers, Groups, Companies, Leads, Contact Messages,
Newsletter, Wishlists, Discounts, Marketing Automations, Cart Recovery,
Notifications, Media, Themes, Pages, Menus, FAQ, Video Showcases, Reports, and
Settings. These are integration points, not confirmation of implemented AI
capabilities.

**Technical verification gate:** before development, confirm which AI gateway,
provider adapters, MCP tools, job workers, tenant budgets and approval
workflows are actually deployed and operational. Reuse them if present; do not
create duplicate infrastructure based solely on marketing/documentation claims.

## 2. Feature portfolio — overview

| Domain | Main outcomes | Priority |
|---|---|---|
| Catalog & content AI | Faster listings; better completeness and discoverability | P0 |
| Marketing AI | Campaigns, recovery, audience activation, retention | P0 |
| Sales AI | Conversational selling, recommendations, B2B RFQs & quotes | P0–P1 |
| Customer support AI | Faster accurate replies, triage, escalation | P0–P1 |
| Business intelligence AI | Useful answers and recommendations from actual store data | P0–P1 |
| AI Copilot & workflows | Natural-language interface with reviewable actions | P1–P2 |

Every feature below is a proposed addition until tested against production
UI/APIs.

## 3. Catalog, products and creative AI

| ID | Feature | Scope | Priority | Suggested location |
|---|---|---|---|---|
| CAT-01 | Photo-to-product draft | Analyze uploaded images; propose title, description, tags, category and visible attributes; flag uncertain facts | P0 | Products → New / Edit |
| CAT-02 | Bulk catalog enrichment | Import CSV or select rows; generate and review changes in batches; resume failed jobs | P0 | Products → Bulk actions |
| CAT-03 | Taxonomy matching | Map products to existing categories / supported taxonomy; suggest missing attributes | P0 | Products / Categories |
| CAT-04 | SEO assistant | Title, meta description, alt text, structured-data candidates, duplicate-copy warnings | P0 | Products / Collections / Pages |
| CAT-05 | Translation/localization | Translate approved content with glossary, placeholders and locale-specific units | P0 | Translations |
| CAT-06 | Duplicate/quality checks | Find near-duplicate listings, contradictory specs, missing images, unsupported claims | P1 | Products → Quality |
| CAT-07 | Product Image Studio | Background removal, lifestyle scene drafts, crop/resize, campaign creative generation | P1 | Products / Media |
| CAT-08 | Colour variant previews | Preview garment colourways while preserving print/design; clearly label generated images | P1 | Products → Media |
| CAT-09 | Collection suggestions | Propose seasonal, theme-based or complementary collections | P1 | Collections |
| CAT-10 | Content assistant | Landing-page sections, FAQs, video captions, product-linked showcase copy | P1 | Pages / FAQ / Showcases |

### CAT-01 workflow

1. Merchant uploads one or more product photos or imports a row.
2. Service extracts observable signals: item type, likely style, visible
   colour, pattern, design, neckline, sleeve length, etc.
3. LLM returns structured JSON constrained to the current product schema and
   allowed attributes.
4. Taxonomy matcher validates proposed category and dictionary values.
5. UI shows field-by-field old value → proposed value, confidence / evidence,
   and warnings.
6. Merchant selects fields and clicks Apply Draft or Save Product.
7. Backend revalidates permissions and schema; persists only approved fields,
   writes audit entry.

Never infer as fact from appearance: material composition, exact dimensions,
weight, origin, certifications, medical/health claims or performance
guarantees. Ask for manual verification.

### Bulk job requirements

- Job IDs, progress, per-row results, pause/resume, retry failed rows,
  cancellation.
- Estimate token/image costs and request approval before starting a large job.
- Field-level diff and select-all-with-exceptions review.
- Idempotent updates and optimistic concurrency: reject/refresh if a product
  changed since the draft was generated.
- Save generated media as a new asset; never silently overwrite originals.
- Record AI provenance and relevant Google Merchant feed metadata where
  required.

## 4. Marketing AI

Primary goal: generate more attributable revenue without spamming customers or
eroding profit.

| ID | Feature | Scope | Priority | Location |
|---|---|---|---|---|
| MKT-01 | Campaign Copy Studio | Generate email, WhatsApp, SMS and social messages by audience, offer and brand tone | P0 | Marketing → AI Campaigns |
| MKT-02 | Abandoned-cart assistant | Draft contextual recovery messages; segment by cart value, intent, inventory and consent | P0 | Marketing → Cart Recovery |
| MKT-03 | Customer segmentation | SQL-first RFM, first-time/repeat, dormant/VIP and category-interest groups | P0 | Customers → Groups |
| MKT-04 | Product promotion picker | Recommend qualifying products using inventory, margin, recent sales and campaign goal | P0 | Marketing → Campaign Builder |
| MKT-05 | SEO copy optimization | Store/category/product SEO copy, FAQs, internal-link suggestions | P0 | Products / Content |
| MKT-06 | Win-back lifecycle drafts | Post-purchase, replenishment, lapsed customer and repeat-buyer messaging | P1 | Marketing → Automations |
| MKT-07 | Promotion calendar | Draft festival/seasonal calendars by store geography and audience | P1 | Marketing → Calendar |
| MKT-08 | Ad Copy Studio | Draft ad headlines, descriptions, UGC scripts and creative briefs | P1 | Marketing → Creatives |
| MKT-09 | Campaign analytics explanation | Summarize revenue, CTR, conversion, holdout and channel performance | P1 | Marketing → Insights |
| MKT-10 | A/B experiment advisor | Hypothesis generation, pre-defined metrics, interpretation and guardrails | P2 | Marketing → Experiments |
| MKT-11 | Campaign creative generation | Hero banners, social images and localized versions | P1 | Content / Marketing |

### Marketing campaign workflow

Merchant prompt example: "Run a Diwali campaign for slow-moving products with
at least 50 units and acceptable margin."

1. Parse intent and propose a read-only query plan.
2. Go/SQL selects qualifying products from the merchant's own store (do not let
   the LLM execute unrestricted SQL).
3. Validate stock, price, discount ceiling, customer eligibility, consent and
   expected margin.
4. Generate channel-specific messages and optional creative drafts.
5. Show the audience estimate, selected SKUs, total discount exposure,
   anticipated send cost and schedule.
6. Merchant approves audience, assets, offers and actual send.
7. Run through approved email/SMS/WhatsApp integrations, respecting templates
   and platform rules.
8. Attribute campaign outcomes and compare to an appropriate baseline or
   holdout where feasible.

### Mandatory safeguards

- Record opt-in / opt-out and message purpose by channel; obey applicable
  anti-spam and privacy requirements.
- For WhatsApp business-initiated messages, use approved templates and the
  applicable customer-service window rules.
- Enforce frequency caps and quiet hours based on recipient locality where
  known.
- Do not generate or publish false urgency, fake reviews or unsubstantiated
  product claims.
- Do not auto-launch ads or send bulk messages merely because the text was
  generated.
- Calculate discount eligibility and margins in deterministic code, not an LLM.

## 5. Sales AI — storefront, CRM and B2B

| ID | Feature | Scope | Priority | Location |
|---|---|---|---|---|
| SAL-01 | Shopping assistant | Answer questions from current catalog, inventory, pricing and verified policies | P1 | Storefront chat |
| SAL-02 | Cross-sell & upsell | Suggest complementary, in-stock products and bundles | P1 | Product / Cart / Checkout |
| SAL-03 | Lost-checkout analysis | Explain abandonment trends and suggest UX/offer experiments | P0 | Carts / Abandoned |
| SAL-04 | B2B RFQ parser | Extract SKUs, quantities, specs, deadlines and delivery terms from inbound text/files | P0 | B2B → Quotes / Leads |
| SAL-05 | Quotation assistant | SKU match, eligible price list, tier/MOQ calculations, quote draft and follow-up copy | P0 | B2B → Quotes |
| SAL-06 | Lead summarization & scoring | Summarize inquiries, completeness and rule-based opportunity signals | P1 | B2B → Leads |
| SAL-07 | Negotiation draft | Draft counteroffers within merchant-defined margin, terms and authority | P1 | B2B → Quotes |
| SAL-08 | Next-best-action | Recommend follow-up, bundle, renewal or outreach to sales staff | P1 | Customers / Sales |
| SAL-09 | Demand/sales forecast | Time-series projections with seasonality and uncertainty ranges | P1 | Reports / Inventory |
| SAL-10 | Conversational cart builder | Assist choosing variants, then prepare a cart for customer confirmation | P2 | Storefront / Chat |

### B2B RFQ → quotation flow

1. Ingest inquiry text/email/approved upload.
2. Extract structured line items with source references and mark unknown
   fields.
3. Match products against tenant-specific SKUs, units and catalogues.
4. Query live inventory and contracted price list; calculate MOQ/tier prices,
   tax and shipping using approved rules.
5. Generate draft quote and email/WhatsApp response.
6. Highlight unmatched products, missing inputs, price overrides,
   below-floor-margin conditions.
7. Authorized salesperson approves and sends; quote versions and approvals are
   retained.

Do not allow AI to independently promise availability, accept an order,
override a contractual price, release a credit hold or confirm negotiated
terms.

### Recommendation strategy

Start with deterministic heuristics (co-purchase, category affinity, in-stock
filtering, margin), then evaluate embeddings / ranking models only if they
outperform the baseline. This avoids expensive LLM calls on every storefront
page view.

## 6. Customer support AI

Introduce a Support Inbox that links authorized website chat, contact messages,
order activity, and optional email / WhatsApp / Instagram integrations.

| ID | Feature | Scope | Priority | Location |
|---|---|---|---|---|
| SUP-01 | Grounded reply suggestions | Draft accurate responses using store-approved data and policies | P0 | Support Inbox |
| SUP-02 | Where-is-my-order assistant | Retrieve shipment status and verified carrier tracking link | P0 | Support / Orders |
| SUP-03 | Topic, urgency & sentiment triage | Categorize shipping, payments, return, pre-sale, complaint and escalation | P0 | Support Inbox |
| SUP-04 | Multilingual replies | Generate language-appropriate replies while preserving policy meaning | P0 | Support Inbox |
| SUP-05 | Conversation summary | Summarize long threads with chronology, facts and next action | P0 | Support Inbox |
| SUP-06 | FAQ/policy assistant | Answer from versioned store policies; cite source article in agent view | P0 | FAQ / Support |
| SUP-07 | Human handoff | Escalate uncertainty, distress, complaints, fraud or monetary disputes | P0 | Support Inbox |
| SUP-08 | Return/refund intake | Gather evidence, check deterministic policy eligibility and prepare request | P1 | Support / Orders |
| SUP-09 | Omnichannel inbox | Merge approved website/email/WhatsApp/Instagram conversations by verified identity | P1 | Support Inbox |
| SUP-10 | Proactive issue communication | Draft delay, failed-delivery and backorder alerts | P1 | Notifications |
| SUP-11 | Support quality reporting | Topic trends, first-response time, resolution, repeat contacts and CSAT | P1 | Reports |
| SUP-12 | Complaint-to-product signal | Cluster defects / sizing / quality feedback, alert product owners | P1 | Reviews / Products |

### Example: order tracking

Customer: "Where is order #1042?"

1. Resolve the customer identity using authenticated session or approved
   verification; do not expose order details on an unverified order number
   alone.
2. Fetch order plus latest carrier status.
3. Draft reply containing only verified state, tracking URL and estimated
   delivery date if the carrier actually supplied it.
4. If status unavailable, say so and offer an escalation instead of guessing.
5. Let merchant configure which low-risk responses may be auto-sent; default to
   staff review.

### Support guardrails

- No automatic refund, replacement, cancellation or payment collection without
  explicit policy-based authorization.
- Detect requests for a human and hand over promptly.
- Store conversation provenance and explain which order/policy evidence
  supports the reply.
- Restrict the assistant from reading unrelated stores or private staff notes.
- Never send sensitive customer information into unrestricted prompts or logs.
- Measure correct resolution, not just response speed or message volume.

## 7. Business intelligence and merchant advisory

| ID | Feature | Scope | Priority | Location |
|---|---|---|---|---|
| BI-01 | Explain my sales | Natural-language summary of revenue, orders, returns and conversion using validated metrics | P0 | Reports → Ask AI |
| BI-02 | Product opportunity report | Best sellers, low-stock winners, dead stock and high-refund SKUs | P0 | Home / Reports |
| BI-03 | Review intelligence | Consolidated complaints, requested features and product-quality patterns | P0 | Reviews |
| BI-04 | Inventory advisor | Demand trends, reorder suggestions, inventory coverage and stockout risk | P1 | Inventory |
| BI-05 | Customer lifecycle analysis | Repeat rate, cohort movement, RFM and retention opportunity | P1 | Customers / Reports |
| BI-06 | Marketing attribution insights | Campaign contribution and caveats, not invented ROI | P1 | Marketing / Reports |
| BI-07 | Anomaly alerts | Statistically unusual order, cancellation, traffic or payment events | P1 | Home / Notifications |
| BI-08 | Executive digest | Daily/weekly "what changed, why, what to do" summary | P1 | Home / Notifications |

**Query pattern:** natural language → intent and allowed metric selection →
parameterized analytics query → metric result → short AI explanation with date
range, comparison period, filters and uncertainty. Never allow arbitrary
LLM-generated SQL to run with broad DB privileges.

Examples:

- "Why are orders down this week?" Compare same-length periods and identify
  contributing products/channels; indicate incomplete or missing attribution.
- "What should I reorder?" Stock coverage + demand trend + supplier lead time +
  minimum reorder size; label recommendations as estimates.
- "Which complaints matter most?" Group validated reviews/tickets by issue and
  affected SKU; provide a link to underlying records.

## 8. AI Copilot and agent workflows

Interface: global "Ask ShopNx AI" panel plus inline contextual buttons. Copilot
must rely on registered tools, not unrestricted database access.

Initial supported intents:

- "Find products missing descriptions and generate drafts."
- "Show slow-moving products over 50 units in stock."
- "Summarize yesterday's sales compared to the previous day."
- "Draft a campaign for VIP customers who opted in."
- "Find support conversations awaiting reply and summarize them."
- "Draft a B2B quote from this RFQ."

Tool classes:

1. **Read tools:** list_products, get_product, get_inventory, get_order,
   get_verified_tracking, get_customer, query_metrics, list_quotes, get_policy,
   list_tickets.
2. **Draft tools:** propose_product_changes, propose_campaign, draft_reply,
   draft_quote, propose_inventory_reorder.
3. **Mutation tools:** only through validated, scoped APIs with permissions,
   idempotency and approval checks (e.g. apply_product_changes,
   send_approved_campaign, send_approved_reply, issue_approved_quote).

Approval policy:

| Action | Default mode |
|---|---|
| Read store metrics / propose analysis | Read-only, no approval |
| Generate unsent content or product drafts | Draft, no publication |
| Edit catalog / apply bulk changes | Preview and authorized approval |
| Publish campaign / send bulk WhatsApp or email | Explicit approval + channel consent validation |
| Send 1:1 support response | Agent approval initially; optional safe autopilot after evaluation |
| Change price, inventory, tax, credit or payout | Strong approval / existing admin authorization |
| Refund, payout, chargeback, cancellation | Existing transactional workflow and strong authorization; never free-form LLM execution |

Interaction pattern: intent → data/tool reads → plan → draft/proposal → preview
diff and cost → approval → validated execution → audit result.

Do not claim an action succeeded until backend confirmation is returned. On
partial failure, show exactly which records changed and which did not.

## 9. UX / admin information architecture

Use existing menus wherever possible:

```text
ShopNx Admin
├── Home
│   ├── AI daily insights
│   └── Suggested next actions
├── Products
│   ├── AI Product Studio (Create / Edit)
│   ├── Bulk AI Enrichment
│   └── Catalog Quality
├── Categories / Collections / Translations
│   └── Inline AI suggestions
├── Marketing
│   ├── Campaign Builder (AI)
│   ├── Audiences & Segments
│   ├── Cart Recovery (AI)
│   ├── Content & Creatives
│   └── Campaign Insights
├── Sales / B2B
│   ├── RFQ Intake
│   ├── Quotation Assistant
│   └── Lead Suggestions
├── Support Inbox                    [new]
│   ├── All Conversations
│   ├── Assigned / Pending / Escalated
│   ├── AI Reply Suggestions
│   └── Knowledge & Policies
├── Inventory
│   └── Reorder Suggestions
├── Reports
│   └── Ask AI / Insights
└── Global: Ask ShopNx AI             [persistent]
```

Reusable UI elements: AI action button, streaming draft state, source/evidence
drawer, old/new diff view, confidence warning, cost estimate, approval dialog,
job-progress panel, audit history, per-feature feedback buttons.

Mobile-first: keep suggestions short; show concise preview and expandable
reasoning/evidence; never auto-scroll away from the merchant's active form.

## 10. Suggested backend architecture

Preferred stack: existing GoCommerce Go services + PostgreSQL + existing
Svelte/SvelteKit admin; extend existing shared components rather than creating
a new AI backend.

```text
Svelte Admin / Storefront / Approved Channel Integrations
                         │
                  GoCommerce API
                         │
      ┌──────────────────┼───────────────────┐
      │                  │                   │
  AI Task API       AI Tool Registry    Approval Service
      │                  │                   │
      └───────────┬──────┘                   │
                  │                          │
          AI Gateway / Router         Validated Go APIs
         (small-first, fallback)             │
                  │                    Tenant-scoped DB
       Model Provider Adapters               │
                  │                 Existing domain logic
        Structured JSON / text
                  │
          Job Queue + Worker
                  │
         AI audit + usage + cost
```

Core implementation components:

- **Model gateway:** task-aware routing, strict output schemas, retries with
  bounded limits, usage accounting and store budgets.
- **Tool registry:** explicit schemas, read/write capability labeling,
  per-role permissions and per-tenant scoping.
- **Store knowledge:** catalog, FAQs, policies, allowed support articles and
  order status via existing APIs; use retrieval/indexing only where beneficial.
- **Job processing:** asynchronous bulk enrichment, image tasks and scheduled
  summaries with retries, dead-letter handling and job status.
- **Approval service:** immutable proposal payload, diff, actor, approver,
  timestamps and final execution result.
- **Observability:** latency, tokens, cost by task/store/model, errors, tool
  success and user feedback.
- **Security:** prompt-injection defense for external text, secret management,
  PII minimization, tenant isolation, RBAC and rate limits.

Conceptual API endpoints (adapt to existing route conventions):

```text
POST /api/admin/ai/tasks
GET  /api/admin/ai/tasks/{task_id}
GET  /api/admin/ai/usage
POST /api/admin/ai/product-drafts
POST /api/admin/ai/bulk-product-jobs
POST /api/admin/ai/campaign-drafts
POST /api/admin/ai/support-reply-drafts
POST /api/admin/ai/rfq-drafts
POST /api/admin/ai/analytics-questions
GET  /api/admin/ai/proposals/{proposal_id}
POST /api/admin/ai/proposals/{proposal_id}/approve
POST /api/admin/ai/proposals/{proposal_id}/reject
```

Endpoint names are illustrative; first inspect existing routes and avoid
duplicates.

Suggested tables (or existing equivalents):

```text
ai_task_runs           id, tenant_id, task_type, actor_id, status, model, timestamps
ai_usage               tenant_id, task_id, model, tokens_in/out, billed_cost, status
ai_proposals           id, tenant_id, entity_type/id, old_version, proposed_patch, status
ai_approvals           proposal_id, reviewer_id, decision, policy_version, timestamps
ai_job_items           job_id, entity_id, status, result, error, retry_count
ai_feedback            task_id, rating, reason, corrected_fields
ai_knowledge_sources   tenant_id, source_type/id, version, permitted_scope, indexed_at
support_threads        tenant_id, channel, verified_customer_id, status, assigned_user_id
support_messages       thread_id, sender, body_ref, consent_scope, timestamps
campaign_drafts        tenant_id, channel, audience_spec, asset_refs, approval_status
```

Never store raw sensitive data unnecessarily. Implement data retention,
deletion workflows and provider-specific controls.

## 11. Cost controls and model selection

| Task | Cheapest practical default | Upgrade only when |
|---|---|---|
| RFM segmentation / sales metrics | Deterministic SQL | No model needed to calculate |
| Category match / basic tags | Rules or small classifier | Uncertain/novel products |
| Product copy, FAQs, campaign drafts | Small text model with structured outputs | Complex constraints or failed validation |
| Support triage and summaries | Small text model | Sensitive/ambiguous or complex conversation |
| B2B RFQ extraction | Structured extraction + validation | Complex files/unmatched lines |
| Recommendations | Database rules and product affinity | Offline ranking quality justifies ML |
| Forecasting | Statistical time-series model | Enough clean data to validate improvement |
| Image creation / edits | Explicit image task, separately metered | User requests visual outputs |

Cost requirements: per-store budgets, quotas by task, estimated cost before
batch/image actions, daily/weekly caps, caching with tenant-safe keys,
prompt/version observability, deduplication, retry ceilings and a kill switch.

Do not run a general-purpose LLM for every search request, storefront page
view or cart recommendation.

## 12. Data, privacy, integrations and legal guardrails

1. **Consent:** customer marketing preferences by channel and purpose;
   unsubscribe and suppression lists; honor jurisdiction-specific privacy
   requirements (including India's applicable DPDP obligations and other
   applicable laws).
2. **Channels:** start with existing verified email/SMS providers; add WhatsApp
   Business Platform and Instagram messaging only through authorized
   integrations and appropriate messaging policies.
3. **Financial control:** discounts, taxes, margins, payment status, payouts
   and refunds are validated by existing backend rules.
4. **Identity:** verify customer authorization before disclosing order status,
   address, payments or account details.
5. **Grounding:** every customer-facing factual answer about live
   orders/catalog/inventory is backed by current records; expose uncertainty
   rather than fill gaps.
6. **Model security:** treat uploaded PDFs, customer emails, reviews, product
   feeds and web pages as untrusted data, not executable instructions.
7. **Generated media:** mark/protect AI-generated images where required;
   preserve original product imagery and brand/IP rights.
8. **Operational controls:** rate limiting, abuse monitoring, quiet hours,
   manual override, audit history, versioned prompts and rollback strategy.
9. **Accuracy:** do not claim external competitor prices, advertising ROAS or
   carrier ETAs without linked, attributable sources.
10. **Access:** staff must only see features and records that their roles
    permit.

## 13. Delivery roadmap

### Phase 0 — Audit and foundations

- Confirm existing AI gateway, model-provider configuration, usage metering,
  budget controls, tool registry, worker architecture and approval mechanisms.
- Establish common AIAction, Proposal, Job, Usage and Feedback contracts.
- Add evaluation datasets and test stores; measure baseline effort/cost before
  roll-out.
- Verify tenant isolation and scoped backend APIs.

Exit: one reliable end-to-end text draft with audit, cost reporting,
permission checks and validation.

### Phase 1 — Immediate merchant productivity (P0)

1. Product description + title + attribute + category drafting.
2. Bulk catalog enrichment and SEO assistance.
3. Marketing copy generator + cart recovery drafts.
4. SQL-based customer segmentation and product promotion eligibility.
5. AI support reply suggestions and conversation summaries.
6. RFQ extraction and B2B quote drafting.
7. Explain-sales and top-product insight cards.

Exit: all features create reviewable drafts, report costs and work with
store-scoped data; no critical mutation occurs without approval.

### Phase 2 — Integrated growth workflows (P1)

1. Campaign builder across eligible channels, audiences, assets and approval.
2. AI Image Studio and content/creative drafts.
3. Storefront shopping assistant and low-cost product recommendations.
4. Unified Support Inbox and verified order-tracking responses.
5. Reorder suggestions, lifecycle marketing, review intelligence and alerting.
6. B2B sales follow-up and assisted negotiation with hard pricing constraints.

Exit: integrated measurement of attributable orders, support resolution and
cost/benefit.

### Phase 3 — Approved agent workflows (P1–P2)

1. Global AI Copilot over registered tools.
2. Multi-step merchant tasks with plan, preview, explicit approval and
   rollback/partial-failure handling.
3. Predictive models only where data quality and measured performance justify
   them.
4. Optional low-risk support autopilot after strict evaluation and policy
   configuration.
5. Experiment assistant and advanced conversion optimization.

Exit: auditable execution, reliable human escalation, documented safety limits
and demonstrated merchant ROI.

## 14. KPIs and evaluation

| Area | Metrics |
|---|---|
| Product onboarding | Time to draft/publish, accepted field %, corrected field %, factual error rate |
| Marketing | Opted-in audience size, send success, recovery conversion, incremental revenue, unsubscribe/complaint rate |
| Sales | Conversion, AOV, attach rate, B2B quote turnaround, quote acceptance |
| Support | First response, resolution time, correctly resolved %, escalation rate, CSAT, hallucination incidents |
| Business intelligence | Metric accuracy, recommendation adoption, merchant time saved |
| Platform economics | Cost per successful task, provider errors, latency, budget overruns, retries |

Run feature evaluations on representative real merchant cases with sanitized
data. Never use engagement alone as proof that automation improves outcomes.

### Minimum acceptance criteria for each AI feature

- [ ] Works only within the authenticated store and role permissions.
- [ ] Output schema validates against GoCommerce domain rules.
- [ ] Missing/uncertain facts are visibly marked.
- [ ] Merchant can edit or reject the generated result.
- [ ] Mutations use preview, approval, version checks and an audit trail.
- [ ] Provider timeouts/failures produce safe, recoverable UI states.
- [ ] Usage and cost are attributable to tenant, task and model.
- [ ] Tests cover cross-tenant leakage, prompt injection and unauthorized
      actions.
- [ ] Evaluation confirms output quality and product-specific success metrics.
- [ ] Mobile layout and accessibility are checked.

## 15. First implementation tickets (recommended order)

| Order | Ticket | Deliverable |
|---|---|---|
| 1 | AI foundation audit | Matrix of deployed services, gaps and reuse decisions |
| 2 | Shared AI draft component | Buttons, generation state, editable results, diff and audit |
| 3 | Product AI endpoints | Structured product draft with taxonomy/attribute validation |
| 4 | Bulk jobs | Queue, progress, cost estimate, retries, field-level approval |
| 5 | Marketing AI drafts | Channel templates, brand tone, product context, consent checks |
| 6 | Cart recovery assistant | Cart context, proposed copy, offer validation, send approval |
| 7 | Support reply service | Secure order/policy retrieval, cited suggestions, escalation |
| 8 | Support Inbox MVP | Thread list, assignment, summary, reply approval |
| 9 | RFQ parser and quote service | Extract, SKU match, deterministic pricing, quote preview |
| 10 | Reports AI summary | Validated metric query + grounded narrative |
| 11 | Evaluation & cost dashboard | Quality, token usage, task completion, override and savings |
| 12 | AI Copilot MVP | Read-only commands and draft-based tool invocation |

## 16. What not to build first

- Fully autonomous campaign/ad spending.
- Unrestricted customer-support bot with independent refund authority.
- General-purpose LLM chat with full database credentials.
- Per-page-view LLM recommendations across the storefront.
- Price optimization without reliable cost, tax, market and margin inputs.
- Forecasting claims for stores with too little usable history.
- Dozens of unrelated AI toggles without common review, budgets and audit
  controls.

## 17. Suggested product positioning

ShopNx AI: create products faster. Market smarter. Sell more. Support customers
better.

Three merchant-facing promises:

1. **Launch faster:** turn photos and files into complete, reviewable product
   drafts.
2. **Grow revenue:** build campaigns, recover lost carts, assist sales and
   identify opportunities.
3. **Operate with control:** handle routine support, understand store
   performance and automate approved workflows.

Recommended first combined release: AI Product Studio + Bulk Catalog AI +
Marketing Copilot + Support Reply Assistant + B2B Quote Assistant + AI Sales
Insights.

## Source and verification note

- Admin section mapping is based on inspection of the browser-visible
  GoCommerce navigation at `https://admin.shopnx.in/dash/categories` on
  29 October 2026.
- General positioning and business workflows were also checked against
  `https://shopnx.in/`.
- All AI capability descriptions, proposed APIs, tables and phases in this file
  are recommendations, not verified live features.
- Review code, existing API contracts, data models and current integrations
  before implementation; adapt this specification to the actual repository
  state.

## The owner's additions, 29 October 2026

The owner added these four ideas to the plan:

- AI rewrites for products that already exist.
- Picture descriptions (alt text) for the whole media library.
- Review summaries.
- Theme colours from the logo, which another session had started as a premium
  feature.
