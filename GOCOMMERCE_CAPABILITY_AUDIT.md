# Capability audit for the retail assistant

What the engine already had for the retail assistant specification, checked
against the source on 2026-11-05 at `af34dd34`, and what the first slice did about
it. The decision is D192 in [`PLAN.md`](PLAN.md); operating it is
[`docs/retail-assistant.md`](docs/retail-assistant.md).

## Implemented, and reused

| Need | Where | Used by this slice |
|---|---|---|
| Module lifecycle | `core/module.go`: `Module`, `App.Handle`, `App.HandleAdmin`; `App.RegisterPlugin` in `core/plugins.go` | Yes |
| Store isolation | `core/scope.go`: `ScopeMigration`; `App.ForEachStore` in `core/foreachstore.go`; `WithStore` | Yes, every table |
| Report withholding | `WithholdFromReports`; `gctest.AssertReportsCannotRead` | Yes |
| Rights | `App.RegisterRight`, `core/rights_registry.go`; `gctest.AssertAdminRoutesDeclareRights` | Yes |
| Contract | `OpenAPI()` fragment; `gctest.AssertSpecCoversModuleRoutes` | Yes |
| Stock | `App.Stock()`, `Inventory.Adjust` in `core/inventory.go` | Yes, for goods received |
| Operator login and roles | `core/superusers.go`, `/api/admin/auth-with-password`, `core/stores_http.go` | Yes, unchanged |
| Webhook signature and Meta request shapes | `ext/notify-whatsapp/webhook.go`, `ext/notify-whatsapp/whatsapp.go` | Copied the approach. It is per store and outbound-focused, so it does not serve a shared number |
| Panel screens | `admin/src/lib/nav.js`, `admin/src/lib/modules.svelte.js`, `FeatureSwitch.svelte`, `Drawer.svelte` | Yes |

## Implemented, and not used yet

These exist and are the right place for later phases. Nothing here was exercised.

| Need | Where |
|---|---|
| Catalogue and variants | `App.Products()`, `core/catalog.go` |
| Checkout and orders | `App.Cart()`, `App.Order()`, `core/checkout.go` |
| Price lists | `core/pricing.go` |
| B2B companies and quotes | `ext/b2b` |
| Sale invoices | `ext/invoices` |
| AI budgets and consent | `ext/ai-gateway` |

## Partial

| Need | What exists | Gap |
|---|---|---|
| Barcodes | Not audited for a variant barcode field | Decide when the checkout phase starts |
| Exactly-once stock movement | `Inventory.Adjust` has no idempotency key | Worked around: the receipt row commits first. A crash leaves a pending row for a person |
| Shared webhook on a many-store install | `core/storeresolve.go` exempts listed paths only, and no `/x/retailassistant` | Worked around: the address carries `?store=` |
| Operator phone sign-in | `ext/identity/phone_login.go` signs in shoppers, not operators | Not used |

## Missing, and built here

Inbound WhatsApp on a shared number, verified phone links, document intake and
review, CSV, XLSX and DOCX reading, suppliers, purchase invoices, supplier payments,
allocations, credit notes and reversals. All in `ext/retailassistant`, under
`retailassistant_*` tables and `/api/admin/x/retailassistant/` routes.

## Missing, and left out

| Need | Why |
|---|---|
| WhatsApp sign-in | `App` gives a module no way to issue an operator session. It would need a reviewed core change, so the existing login stays |
| A public webhook with no store named | Needs an entry in `storeExempt`, a core change. `?store=` does the job |
| OCR, vision and PDF text | No such library is possible under AGENTS.md rule 2 without a decision, and PDF text extraction by hand loses columns |
| Quarantine of unknown senders | A store-owned row needs a store, and `ext/alltest` refuses an unscoped table. An unknown sender is answered and nothing is kept |
| CSV export, retention rules, encryption at rest, virus scan | Not in the first pass's definition of done |
| Template messages | Need wording approved by Meta |
| Barcode checkout, wholesale quotes, insights | Later phases, by the specification's own delivery rule |

## Differences from the specification's tables

| Specification | Built | Why |
|---|---|---|
| `retailassistant_extractions` | `extraction` and `draft` columns on documents | One row per document, and corrections are audited |
| `role` on a phone link | None | A link only lets a phone send documents. It grants no access to anything |
| `confidence` on a draft | `exceptions` and per-field `source.provenance` | A deterministic reader has no honest number to give |
| `retailassistant_sessions`, `_otp_challenges` | Not built | No sign-in. The link code is on `retailassistant_phone_links` |
| `_barcode_aliases`, `_pricing_rules`, `_wholesale_imports`, `_recommendations`, `_onboarding` | Not built | Later phases. Onboarding is the owner adding a phone in the panel |

## Revised checklist

- [x] Module, migrations, rights, contract and plugin switch
- [x] Signed webhook, message id dedupe, sender routing, verified links
- [x] Intake and reading of CSV, XLSX and DOCX; PDF and pictures left for a person
- [x] Review, correction, approval and rejection, with an audit trail
- [x] Supplier ledger: partial payments, advances, credit notes and reversals
- [x] Goods receipt through the stock service, once per line, with no screen yet
- [x] Opt-in confirmations through a queue, after commit
- [x] Two panel screens: bills and phones; suppliers and balances
- [x] PostgreSQL tests for acceptance tests 1 to 6 and 9, with the module off, across two stores
- [ ] A real call against Meta, and a template for messages outside the service window
- [ ] 20 to 50 real documents read, with consent, and the reader's labels tuned
- [ ] Retention, deletion and encryption at rest, before any real merchant's documents
- [ ] CSV export
- [ ] A screen for booking goods in
- [ ] Hindi
- [ ] A reviewed decision on OCR, and on WhatsApp sign-in
