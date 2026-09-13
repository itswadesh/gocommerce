# GoCommerce — Product, Architecture & Execution Plan

> **Revision R3 — 2026-08-27**
>
> This revision changes the project from a "minimal SQLite ecommerce platform"
> into a **small, composable, production-grade commerce engine for Go**.
>
> The two architectural changes that drive R3 are deliberate:
>
> 1. **PostgreSQL is the production database.** SQLite is no longer the
>    architectural spine. Simplicity comes from the application model and
>    composition model, not from choosing an embedded database at the cost of
>    transactional headroom.
> 2. **Events use a transactional outbox.** A committed order/payment change
>    must not be able to disappear because the process crashed between the DB
>    commit and event publication.
>
> Product variants are also moved into the core model from day one. Variants
> are not an optional module because they affect carts, stock, prices, order
> snapshots, imports, and product identity.
>
> **R3.1 amendments (2026-08-28)** — four decisions taken after R3 was drafted:
>
> 1. **One Go module, no monorepo** (§22). Extensions with no third-party
>    dependency are packages under `ext/`; only dependency-carrying extensions
>    become satellite repos.
> 2. **Guest checkout is a permanent guarantee** (D22), not a v1 simplification.
> 3. **Default currency is `USD`, and currency is extensible** (D14) — money
>    columns are named `*_minor`, never `*_cents`, because JPY has 0 decimal
>    places and KWD has 3.
> 4. **Default language is `en`, and language is extensible** (D21) — core
>    negotiates a request language and ships a `Translator` seam; translations
>    themselves live in a module.
>
> The prior SQLite-based plan is archived at
> `docs/PLAN-r2-sqlite-archived.md` — useful for its operational detail
> (Windows notes, CSV specifics, route inventory), superseded on architecture.

---

## 0. The product thesis

### 0.1 What GoCommerce is

**GoCommerce is a small, composable commerce engine for Go.**

It provides the business primitives that every store eventually needs:

```text
Products / Variants
        ↓
      Cart
        ↓
    Checkout
        ↓
      Order
        ↓
    Payment
        ↓
  Fulfillment
        ↓
 Events / Automation
```

Everything around those primitives is replaceable:

```text
                 ┌──────── payments-stripe
                 ├──────── payments-razorpay
                 ├──────── fulfill-shiprocket
                 ├──────── notify-sendgrid
                 ├──────── notify-msg91
                 ├──────── search-meilisearch
                 ├──────── storage-s3
                 ├──────── invoices
                 ├──────── cms
                 └──────── mcp
                         │
                         ▼
                 ┌───────────────┐
                 │  gocommerce   │
                 │  Go + Postgres│
                 └───────────────┘
```

### 0.2 What it is NOT

GoCommerce is deliberately not:

- a Shopify clone;
- a hosted SaaS store builder;
- a CMS-first platform;
- a giant plugin framework;
- a low-code admin application;
- a replacement for every commerce concern on day one.

The operator is a developer. A store is a Go program that composes the engine
with the capabilities it needs.

### 0.3 The core promise

> **Own your commerce stack. Start small. Add capabilities without replacing the engine.**

A useful mental model is:

```go
app, err := gocommerce.New(
    gocommerce.Config{DBURL: os.Getenv("DATABASE_URL")},
    stripe.New(...),
    shiprocket.New(...),
    sendgrid.New(...),
)
```

The application author can inspect the complete composition from `main()`.
There is no runtime plugin registry, dependency injection container, reflection
magic, or configuration DSL.

### 0.4 The real differentiation

The differentiation is not "Go ecommerce" by itself.

The differentiation is the combination of:

1. **Commerce primitives that are complete enough for real stores.**
2. **Compile-time composition using ordinary Go packages.**
3. **PostgreSQL-backed transactional correctness.**
4. **Durable events via a transactional outbox.**
5. **First-class product variants instead of a flat toy catalog.**
6. **OpenAPI and MCP so the store is both application-friendly and agent-ready.**
7. **Litekart-compatible API shapes where compatibility provides immediate value.**

The product should be presented as a **commerce engine**, not merely an
"ecommerce framework."

---

## 1. Who should use it

### Primary customer

A Go developer or small engineering team building:

- a single-brand ecommerce site;
- a B2B ordering portal;
- a manufacturer storefront;
- a niche marketplace foundation;
- a headless commerce backend;
- an internal commerce or ordering system;
- a custom storefront where Shopify/WooCommerce becomes restrictive.

### Secondary customer

Teams already running a Go application that need commerce primitives without
adopting a large platform.

### Especially strong use case

A company wants:

```text
Svelte / React / native app
          │
          ▼
   gocommerce API
          │
     PostgreSQL
          │
    provider modules
```

The frontend is free to be replaced. The commerce state remains owned by the
engine.

### Explicit non-target

A non-technical merchant who wants to sign up, choose a theme, add products,
and never touch code should use a hosted storefront product instead.

GoCommerce can support an admin UI later, but the engine does not depend on
one.

---

## 2. Why PostgreSQL wins in R3

The previous design made SQLite the architectural center in order to maximize
single-binary simplicity. That was elegant, but it made several future needs
artificially expensive: stronger concurrency, durable event storage, richer
queries, variants at scale, and horizontal application scaling.

R3 therefore chooses:

> **PostgreSQL for production.**

The application remains simple because Postgres is treated as infrastructure,
not because the infrastructure is removed.

### 2.1 Benefits we gain immediately

- concurrent writes without a single application-level writer bottleneck;
- robust transaction semantics;
- row-level locking for stock operations;
- transactional outbox in the same database;
- reliable unique constraints for idempotency;
- JSONB for extensible metadata where appropriate;
- stronger indexing/query options for variants and orders;
- natural path to read replicas and multiple application instances;
- straightforward cloud/self-hosted deployment.

### 2.2 What we give up

- a completely dependency-free local database;
- the "copy one `.db` file" operational story;
- the ability to truthfully describe the engine as embedded.

That trade is intentional.

The new simplicity promise is:

> **One Go binary + one PostgreSQL database.**

That is still dramatically simpler than operating a large commerce platform
with Redis, Kafka, Elasticsearch, multiple workers, and a framework-specific
plugin runtime.

### 2.3 SQLite policy

SQLite is **not** part of the production architecture in v1.

A future `gocommerce-lite` compatibility target may be added only after the
PostgreSQL domain model is stable. It must not shape the core APIs or constrain
transactional behavior.

Do not build a database abstraction prematurely just to support SQLite.

---

## 3. Product positioning

### One-line

> **A small, composable commerce engine for Go.**

### Developer-facing pitch

> Build commerce directly into your Go application. Products, variants, carts,
> checkout, orders, payments, fulfillment and durable events live in one small
> engine. Integrations are ordinary Go modules you wire together in `main()`.

### Stronger technical pitch

> **Commerce primitives, not a commerce monolith.**
>
> PostgreSQL-backed, API-first, event-driven, variant-aware, and designed for
> developers who want to own the stack.

### AI-era pitch

> **A commerce engine your application — and your agents — can operate.**
>
> OpenAPI exposes the HTTP contract. MCP exposes controlled commerce actions to
> AI agents. Both sit above the same domain services, so there is only one
> business state machine.

### What not to say

Avoid claims such as:

- "the fastest ecommerce engine";
- "Shopify killer";
- "zero infrastructure";
- "infinitely scalable";
- "microservices without the complexity".

Those claims are hard to prove and distract from the real advantage.

---

## 4. Strategic principles

### S1 — Keep the domain small, not the product fake

The core should contain the concepts that change together:

- product;
- variant;
- inventory;
- cart;
- checkout;
- order;
- payment state;
- fulfillment state;
- durable domain events;
- basic administration/authentication;
- API contract.

A feature does not belong in a module merely because it is optional. If moving
it out would make the core's invariants fragmented, it stays in core.

### S2 — Modules integrate external capabilities

Modules own:

- external payment APIs;
- carriers;
- notification vendors;
- search indexes;
- object storage;
- tax engines;
- CMS;
- invoicing;
- AI/MCP adapters.

They do not own core commerce state transitions.

### S3 — Correctness before abstraction

Do not create interfaces for things that are not actually replaceable.

There should be clear ports where composition matters:

The ports that exist in `ports.go` as built:

```text
PaymentProvider       Code() + Initiate()
WebhookProvider       optional: a provider that receives gateway callbacks
Refunder              optional: a provider that can refund
FulfillmentProvider   Code() + Ship()
Notifier              one channel's delivery ("email" | "sms")
Translator            optional: localized catalog copy
```

`OpenAPIContributor` is the seventh extension point and lives in `openapi.go`
rather than `ports.go`, because it extends the served contract rather than the
domain.

Search and storage ports were considered and **not built**: search is `LIKE`
against PostgreSQL until a store outgrows it, and images are URLs (§17). The
event bus is not a port either — the outbox replaced it (§11), which is why
`Config` has no `Bus` field. Do not create interfaces for things that are not
actually replaceable.

The database itself is not abstracted in v1.

### S4 — One state machine

Whether an action is initiated from:

- REST;
- a payment webhook;
- MCP;
- an admin command;
- an import;
- a future queue consumer;

it must eventually invoke the same domain service.

No integration gets to invent its own version of `MarkPaid`, `Ship`, or
`Cancel`.

### S5 — Durable events are part of correctness

Events are not "nice-to-have notifications." They are the integration seam
for a modern commerce application.

If the database says an order is paid, an event describing that fact must be
recoverable after a process crash.

### S6 — Variants are core, not future debt

Product variants influence:

- SKU;
- price;
- stock;
- cart lines;
- order lines;
- images;
- imports/exports;
- fulfillment metadata.

Designing a flat-product schema first and retrofitting variants later creates
avoidable migration and API breakage.

### S7 — API compatibility is tactical, not architectural

Where Litekart compatibility is useful, preserve compatible routes and
payloads.

Do not distort the domain model merely to clone legacy behavior.

Compatibility is an adapter/contract concern, not the definition of the
engine.

---

## 5. Architecture decisions

| # | Decision | Choice | Rationale |
|---|---|---|---|
| D1 | Product model | Commerce engine, not full platform | Keeps the value proposition focused. |
| D2 | Database | PostgreSQL | Removes SQLite writer ceiling and enables durable outbox + real concurrency. |
| D3 | ORM/query layer | Prefer `database/sql` with explicit SQL in v1 | Keeps data ownership visible and avoids ORM behavior becoming architecture. |
| D4 | DB abstraction | None in v1 | Supporting multiple production databases would add interfaces before they are needed. |
| D5 | Extension mechanism | Explicit module wiring | Typed, searchable, debuggable, easy for Go developers. |
| D6 | Core shape | One public `gocommerce` package | Prevents cycles and keeps module integration simple. File discipline replaces premature package fragmentation. |
| D7 | HTTP | stdlib `net/http` + Go `ServeMux` | Routing and middleware requirements remain intentionally small. |
| D8 | Events | Transactional outbox + dispatcher | Event record is committed with the business transaction; no crash window between commit and publication. |
| D9 | Event delivery | At-least-once | Consumers must be idempotent; duplicates are acceptable, lost business events are not. |
| D10 | Async transport | Core outbox dispatcher; Redis Streams as an optional module/transport | Reliable by default, scalable when needed. |
| D11 | Variants | First-class core model | Avoids a later breaking redesign of cart/order/stock. |
| D12 | Inventory | Variant-level stock, optional product-level computed availability | Commerce quantity belongs to the sellable SKU. |
| D13 | Money | Integer minor units, in `*_minor` columns | Avoids floating-point money errors. Never `*_cents`: JPY has 0 decimals, KWD has 3, so a `_cents` column is a lie the moment currency becomes extensible. |
| D14 | Currency | One settlement currency per store, `Config.Currency`, **default `USD`**; snapshotted on every order; API returns `{amount_minor, currency}` and never a formatted string | Mixed-currency checkout is out of scope, but any ISO code works today and the order-level snapshot keeps history self-describing if a store changes currency. Formatting (symbol, decimal places) is the client's job — which is what makes new currencies need no core change. |
| D15 | Idempotency | Persisted request keys + provider event IDs | Prevent double orders and repeated external callbacks. |
| D16 | API contract | Unversioned Litekart-compatible overlap + explicit GoCommerce additions | Reuse existing storefronts without freezing every future design choice. |
| D17 | API docs | OpenAPI embedded/served by core | API remains discoverable without external tooling. |
| D18 | Agent interface | MCP module | Exposes controlled domain actions without duplicating business logic. |
| D19 | Authentication | Simple admin bearer token in core + replaceable middleware seam | Good for single-store bootstrap while leaving a path to accounts/RBAC. |
| D20 | Search | Out of core | Search infrastructure varies heavily by deployment. |
| D21 | Language | `Config.DefaultLanguage` **default `en`**, `Config.Languages` default `["en"]`. Core negotiates per request (`?lang=` → `Accept-Language` → default) with hand-rolled primary-subtag matching, snapshots the language on the order, and passes it to notifiers. Content translations are OUT of core: an optional `Translator` port supplies per-language field overrides, batched by id. | A single-language store must sell without paying for i18n, but extensibility was a requirement — so the seam is built, not speculated. `golang.org/x/text` is a dependency we decline for ~25 lines of matching. |
| D22 | Guest checkout | **Permanent core guarantee.** A shopper buys with a cart token and an email; no account, ever. A future identity module may ADD authenticated checkout and order history, but may never make an account required, and core carries no path that assumes a customer record exists. | Stating it as an invariant now constrains the identity module before it exists — the only time that constraint is free. It also keeps order snapshots (§10.4) authoritative rather than deferring to mutable customer rows. |
| D23 | Repo layout | **One Go module.** Core at the root; extensions that add zero third-party dependencies are packages under `ext/`; an extension needing an SDK (`queue-redis`, `search-meilisearch`, `storage-s3`) ships as its own satellite repo. MCP was expected to be one and is not — it speaks JSON-RPC over `encoding/json` instead of taking the official SDK, so it stayed in `ext/` (§22). No `go.work`, no nested modules, no per-extension tags. (Layout clause superseded by D25 — the engine now lives in `core/`.) | Dependency isolation was the only thing multi-module bought, and most planned extensions are REST + HMAC with no SDK. Bundled extensions still see only core's exported API — Go package boundaries forbid reaching into unexported internals — so they remain a real proof of the seams while `go build ./...` covers the whole product. |
| D24 | Role rights | **Fixed roles, configurable sets, twenty-one rights.** The roles stay `owner`, `manager`, `staff`; what `manager` and `staff` may do is the store's to re-cut, stored in `role_rights` as the departure from the engine's defaults (a role with no rows keeps tracking them). Owner is not storable and always carries every right. Every role keeps `catalog.read`. Rights resolve on each authentication, never from a cached copy. The catalogue is one right per area a person could be kept out of — catalog, inventory, discounts, taxes, locations, orders (read/write/fulfill/refund), customers, team (read/write), roles, data (export/import), the store itself (`store.operate`) — replacing an eight-right first cut in which `settings.write` alone covered the team, the roles matrix, the locations, the tax rates and the export of the whole database. This renames `RightsOf`/`Can` to `DefaultRightsOf`/`DefaultCan` and adds `(*Superuser).Has` — a breaking change to the exported API, taken deliberately. **Amended — `store.operate`.** The twenty-first right is the store as a running system rather than as a shop: the health report, the maintenance passes that act on it (the cart sweep, the unpaid-order sweep, the outbox drain), the outbox and its dead letters, and the record of what every operator did. It is appended last in `AllRights`, so no existing row moves in the roles matrix, and **owner alone carries it by default** — a store that has never re-cut a role is unchanged by its arrival. It is not `settings.write` returning under another name: it changes no configuration, no product and no price, and the sweeps cancel only the orders the five-minute ticker was going to cancel anyway, through the same service methods. What it does **not** carry is database-level error text: a check that fails because it could not run records its cause separately from its finding and the admin route strips it, so the reach of this right stops at the same line `httpx.go` draws for every other response. | D19 left the seam open for RBAC and rights.go was already a map so that the sets could move into a table without moving the decisions. Custom *roles* were declined: a role name is in the `superusers` CHECK, in every picker and in every invitation, and a store that wants "warehouse" almost always wants "staff, plus stock" — which re-cutting gives it. Resolving per request rather than caching means a withdrawn right is gone on the next call, with nobody signed out and no second server holding a stale copy of who may refund. `store.operate` is its own right because none of the twenty fit: `team.read` would file the store's health under who may see the staff list, `orders.write` would hand the unpaid sweep to every staff member while leaving the outbox to nobody, and splitting four surfaces across three ill-fitting rights is precisely how `settings.write` happened. Owner alone rather than owner and manager, because it is the widest thing in the catalogue: `ext/mcp` gates its single dispatch endpoint on it, the event list pages through every buyer's email, phone and line items, and `DELETE /api/admin/x/identity/customers/{id}` pairs it with `customers.read`, which manager already carries — so a manager default would quietly grant account erasure. Running the store is not merchandising, and a store that wants a manager on call re-cuts the role, which is what configurable sets are for. Adding a right widens nobody: `roleRights` enumerates manager and staff explicitly and resolution intersects with `AllRights` on the way out. |
| D25 | Engine location | **The engine moves from the repo root to `core/`: import path `github.com/misiki/gocommerce/core`, package clause still `gocommerce`.** A clean break, no facade and no aliases: the old import path stops existing and all 17 in-repo import sites were rewritten in the same commit; an external importer (none known) fixes it with the same one-line edit, since the package identifier and every exported symbol are unchanged. go.mod stays at the root with the module path unchanged, so one module (D23) and one engine package (D6) stand; only D23's "core at the root" clause is superseded. `openapi.json` and `taxonomy/` moved beside the `go:embed` directives that read them. `check-docs.ps1` and CI now fail if a `.go` file reappears at the repo root, so the layout is enforced rather than remembered. Docs keep citing engine files by bare name (`checkout.go`) — there is exactly one package they can mean. | The root had grown to ~94 entries and the engine had no directory of its own. A facade at the old path would be a permanent second name for every exported symbol and a standing drift surface — machinery protecting importers that do not exist, when D24 already establishes that a recorded pre-1.0 break is acceptable. `core/` rather than `gocommerce/` because `.gitignore`'s `/gocommerce` and `.dockerignore`'s `gocommerce` both match the binary and would have swallowed the directory, and `gocommerce/gocommerce` stutters; keeping the package clause avoids touching 74 package clauses and every `gocommerce.X` reference in cmd, ext, gctest, examples and the tutorial. Splits stay out of this change: a leaf is cut only when it borrows nothing unexported and no interface is invented to break a cycle (S3) — `carriers.go` (stdlib-only) qualifies today as a follow-up; `taxonomy` and `options` (6 and 3 borrowed internals) do not yet, and a future taxonomy package would subsume `core/taxonomy/`'s data files, editing the embed directives then. |
| D26 | Once per email | **A usage cap keyed on the order's email address is a deterrent, not a control.** Guest checkout is permanent (D22), so there is no customer to count against and a second address defeats it in a minute. Everything that names it says so: the column is `once_per_email` rather than `once_per_customer`, the API field carries the same word, and `emailHasUsed` is a match on `lower(email)` across the orders that were not cancelled — a fact about addresses, which is all it ever was. | An operator who reads it as a control will reach for it where it matters — a single-use employee code, one per household at launch — and learn the difference from an accountant. Back-filled: `discounts.go` and `schema.go` have cited this decision since M15 and the table never gained the row, so the citation pointed at nothing. |
| D27 | A discount on an edited order | **The discount follows the basket it came off.** Editing an order's lines re-runs the rule against what is left. A fixed amount survives — it was never a function of the basket. A percentage is taken again, because the basket it was a percentage of no longer exists and keeping the old figure leaves an order whose arithmetic does not add up. An order that has fallen below the `min_subtotal_minor` its discount required is refused with a 409 rather than quietly kept, and an order whose rule has since been deleted keeps the amount it was recorded with, clamped to what there is left to discount, because an unknowable percentage is not worth guessing at. The `order_discounts` snapshot moves with the amount, so the order and its discount rows never disagree. | Recomputing reads the rule's *current* value rather than the order's copy of it: a promotion the operator has since changed is the promotion the store is running. The refusal is a refusal and not a silent removal because choosing between dropping the discount and abandoning the edit is an operator's call. On the number: D27 was free and this rule had taken D24's by mistake — `recomputeOrderDiscount` in `core/discounts.go`, the edit path in `core/orders.go` and `TestEditedOrderFollowsD24` all describe discount arithmetic while citing the decision about role rights. The rule is D27; the citations follow it. |
| D28 | Discounts per order | **One discount per order today.** `order_discounts` is keyed `UNIQUE (order_id, title)` rather than on `order_id` alone, so the table can already hold several and the one-per-order rule lives in the service that writes it. | Stacking is a rules engine — precedence, exclusivity, per-line ordering, what a fixed amount means once a percentage has been taken — and shipping the storage for it costs nothing while shipping the rules costs a lot. A unique `order_id` would have made the day we want stacking a migration against every live store instead of a service change, and migrations are append-only (rule 7). Back-filled: `schema.go` has cited this decision since M15. |
| D29 | Automatic discounts | **A discount with no code is stored and not evaluated.** `discounts.code` is nullable and NULL means "applies without anybody typing it"; nothing in checkout looks for such a rule, and the unique index on `lower(code)` is partial so codeless rows do not collide with one another. | The evaluation order for codeless rules is the same unsolved precedence problem as stacking (D28), and a reserved column costs nothing while a wrong answer at the till costs a customer. Reserving the shape is what keeps the day it is implemented additive — no migration, no re-keying — for as long as nothing pretends the behaviour already exists. Back-filled: `schema.go` has cited this decision since M15. |
| D30 | Panel shared state | **The panel's shared state has three homes and no fourth: a rune for the session, the URL for list state, and `localStorage` only as the session's durable copy.** The signed-in operator lives in `admin/src/lib/session.svelte.js` as `$state`, and `can()` reads it, so every rights-derived control invalidates when a role is re-cut. Every list screen's filters, search and page live in `page.url.searchParams`, read and written through `listState()` with `replaceState`; component-local filter state is a defect, not a style. Lists are windowed on `?page=`, never appended. A 401 — and only a 401 — ends the session, at one place inside `request()`: a 403 is a rights answer and must never sign anybody out. Money is parsed in the reader's locale and every `toMinor`/`fromMinor` call carries the store's currency; a missing currency throws rather than assuming two decimals, and an amount that will not parse is refused at the control instead of being sent as null. Modals are a stack with one live focus trap, and `@media print` is the last block in `gocommerce.css` with every new section appended above it. | The panel was reading a non-reactive source inside a layout that never remounts, so the one action whose whole purpose is changing what the UI may show could not change it, and two screens had grown full-document reloads to work around that. List state in the URL is what makes a record openable and the Back button survivable, and it is the mechanism the dashboard's stat cards already assumed existed. D14 makes formatting the client's job; parsing is the same obligation in the other direction, and doing it with `parseFloat` reads most of the world's decimal mark as a thousands separator. None of this is an API change — every parameter was already on the wire and every right was already on the record. |
| D31 | Store settings endpoint | **`GET /api/admin/settings`: read-only, authenticated, and gated by no right.** It serves a hand-written whitelist over `Config` — the currency, the default language and the language list, `prices_include_tax`, flat shipping as a `Money`, the order prefix, the two TTLs in seconds, whether uploads are possible, the installed module names — plus the payment and fulfillment inventories as `{code, name, module}`, where the name comes from a new optional `Named` port so a module owns its own label. There is no write route: a store that could change its settlement currency from a browser would be changing what every order in flight means. Nothing that is not "what this store is configured as" may be added to the payload, and `/health/ready` is deliberately not extended — its `currency` and `language` keys are frozen rather than removed, because something outside this repo may already poll them. | The panel was reading its currency off the readiness probe and its payment methods off the public checkout route — four ad-hoc probes across four screens, defaulting to `USD` in six places — while four of the values that decide what a figure on screen *means* were served nowhere at all. Read-only is not a limitation: these are start-up decisions, and D14's per-order currency snapshot is exactly what makes changing one a migration rather than a setting. A whitelist DTO rather than a marshal of `Config`, because `Config` carries the DSN and the admin tokens — which must never reach a browser — and a func field, `AdminAuth`, which makes `json.Marshal` fail outright. No right gates it because every screen formats money before it can draw anything: a role that could not read this would read prices in the wrong currency and the wrong number of decimals. |
| D32 | Partial fulfillment | **A shipment names what is in it, and the order's status is derived from the sum.** `fulfillment_lines (fulfillment_id, order_line_id, quantity)` (M22) holds one row per line per parcel; `orders.status` gains a sixth value, `partial`, which is what the shipped sum landing between nothing and everything is called, computed by one function after every shipment is written or removed and never assigned by a caller. `ShipRequest.Lines` is optional and empty means everything the order still owes, so the request every existing client already sends — the panel's default, the MCP tool, `scripts/smoke.ps1`, `api.http` — ships the whole order the first time and the remainder the second, unchanged in bytes and in behaviour. The engine resolves the lines before calling the carrier, so a provider is handed the parcel's real contents, and re-checks them under the row lock, refusing with a 409 rather than recording a parcel the carrier was not told about. `order.shipped` keeps its name and gains an optional `shipment` block. A partly shipped order refuses cancellation, line edits and delivery; removing its shipments is the way back. | §10.1 already drew a state in this slot and left it unnamed; a back-ordered line is what the slot was for, and without it the operator must hold the whole order or claim all of it went out — and `EditLines` (dropping the line) is a different fact, because the dropped units leave the order instead of staying owed. A jsonb array on `fulfillments` would make "how much of this line has gone" a parse of every prior parcel inside the order's row lock, with no foreign key behind the ids it names. Deriving the status rather than storing a count follows `stockCommitted` — two copies of one fact eventually disagree — and is what makes removing one of several parcels land on the right status for free. A separate `order.partly_shipped` event was declined: the taxonomy is frozen, it would have to *replace* `order.shipped` on a partial, and the payload's `status` already carries the distinction. |
| D33 | Recording a failed payment | **An operator may record one, it can be taken back, and a failed payment stays sweepable.** `POST /api/admin/orders/{id}/mark-payment-failed` (`orders.write`) puts `Payments.MarkFailed` behind a route; `Orders.SweepUnpaid` and the doctor's stale-reservation check widen from `payment_status = 'pending'` to `IN ('pending','failed')`, written as SQL literals so the partial index is provably usable under a cached generic plan, with M23 replacing `orders_unpaid_idx`. The reason is logged and stored nowhere — no event name and no column — so the panel sends a fixed reason rather than prompting for prose it would discard. `MarkFailed` no-ops silently on a cancelled order (checked first) and refuses a refunded one; `MarkPaid` gains that refusal's mirror, and `MarkUnpaid` now moves `failed → pending` silently, so a mis-click has an Undo. | Half the payment state machine was unreachable by a person: an operator whose gateway declined could only cancel, which releases the stock and closes the sale, when the intent was to leave the order open for a retry. The sweeper clause is not a nicety — `MarkFailed`'s own doc comment and `skills/payments.md` both claimed the sweeper collected these orders and the query never did, so setting `failed` removed an order from the sweep *and* from `gocommerce doctor` at once. With two webhooks as the only callers that was nearly harmless; with a button in the order drawer it is an operator-reachable stock leak, so the route and the fix ship together or neither ships. No event, because `events.md` already sets the rule: a name is added when something consumes it. The guard order is load-bearing — both bundled gateways answer 500 and release their idempotency claim on any error from `MarkFailed`, so a 409 on a cancelled-and-paid order would be a webhook retried forever. |
| D34 | Naming a payment method on the public route | **`GET /api/checkout` gains `methods`, an array of `{code, name}`, beside the existing `payment_methods` array of codes, which is unchanged and stays.** The name comes from the `Named` port D31 introduced, or falls back to the code itself — the same answer `GET /api/admin/settings` gives for the same provider. `Payments.Methods() []string` is untouched; `Payments.Installed() []PaymentMethod` is added beside it. `module` is deliberately omitted here: which module installed a gateway is a fact about this build, and an unauthenticated route has no business publishing it. | A storefront had nothing but the raw provider code to label a radio button with, and the obvious fix — a code-to-name map in the client — cannot work: a store composes its own providers in `main()` (D5), so a map shipped by anybody but the module could never name `giftcard` or `bank_transfer`. Changing `payment_methods` to objects breaks three in-repo consumers (`smoke.ps1`, the settings screen, the MCP `store_info` tool) plus every storefront, to add a field only a picker needs. Two shapes of one list is the honest price of an additive change to a public route, and both the OpenAPI description and this row say so, because a future tidy-up that collapses them breaks all three in one commit. |
| D35 | A discount on an order placed by hand | **`NewOrderInput` gains `discount_code`, and `Carts.SetDiscountCode` becomes the only writer of `carts.discount_code`.** The code goes onto the cart the admin path already builds internally, so the checkout judges and claims it under its own lock exactly as it does a shopper's — an expired, exhausted or ineligible code refuses the whole request with a 422, before an order exists and before stock is reserved. The two public verbs on `/api/carts/{token}/discount` are refactored onto the same setter and now answer 409 on a cart that has already been checked out, which is documented in `openapi.json`. | A phone order could not honour the store's own promotion at all, and there was no service-level setter to call: both public handlers wrote the column with raw `UPDATE`s keyed on the token. A third raw writer inside core would have been legal (core may write core tables) and still wrong. The code is not validated on the way in, because the only honest place to judge it is under the lock that claims it — a preview that passed and a checkout that refuses is the same answer twice, and the second one is the true one. Driving the public cart routes from the panel instead would have skipped the `orders.write` gate and leaked an orphan cart whenever a middle step failed. |
| D36 | Refund record | **Refunds are a ledger, and `payment_status` stays `paid` until every minor unit has come back.** `order_refunds` (M24) records each refund — amount, reason, provider, the gateway's own refund reference, the operator, a status — and `orders.refunded_minor` carries the running total, exposed as `refunded` and `refunds[]` on the Order response. The four payment statuses are unchanged: `refunded` now means *fully* refunded, and a partial refund leaves the order `paid` with `refunded_minor > 0`. Refunding is three steps, because AGENTS rule 5 forbids holding a transaction across the provider call: commit a `pending` refund row, which is what reserves the amount against a concurrent second refund; call the provider outside every transaction; then move `refunded_minor`, the status and `order.refunded` in one `Orders.transition`. A declined refund is kept as a `failed` row carrying what the provider said, and a refund the engine never heard back about is settled by an operator through `POST /api/admin/orders/{id}/refunds/{refund_id}/settle` rather than by hand. `Payments.Refund` becomes `Refund(ctx, orderID, RefundRequest, by *Superuser)` — a deliberate pre-1.0 break, like D24's — while `Refunder` is left alone and a new optional `ReferencedRefunder` carries the gateway's refund id. No new right: refunds stay behind `orders.refund`. | A fifth `partially_refunded` status was the obvious move and is the wrong one, for two reasons that are money and not ergonomics: `ext/fulfill-shiprocket` books a shipment as Prepaid only on exactly `paid`, so a partially refunded order would have the courier collect the whole total again from a customer who has already paid, and `ext/invoices` selects `WHERE payment_status = 'paid'` and would silently stop invoicing it. Beyond those it is enumerated in a shipped CHECK, in `openapi.json` twice, in `ext/mcp`, in `format.js`, in the panel's filter and in three chips that render it raw — to state a fact the response can state as a number, which is also the number the operator actually asks for. Every order that says `refunded` today was refunded in full, because the API could record nothing else, so M24's backfill is a fact rather than a guess. **The stored `refunded_minor` is a deliberate departure from M17**, which dropped `variants.stock_on_hand` on the grounds that a stored copy of a sum is a number that can be wrong: here the copy buys two things a derived sum cannot — `orders_refunded_within_total` as a real row CHECK, and a value `lockOrder` reads off the row it already holds `FOR UPDATE` rather than off a snapshot that can be one commit behind under READ COMMITTED. `doctor`'s `refunds` check reconciles the two on every run, which is the price of the departure. `Refunder` was left alone because a provider is found by a runtime type assertion with no compile-time guard anywhere in the repo, so changing it would not break an out-of-repo gateway loudly — it would leave it compiling and silently reporting "does not support refunds" on the money path. *(Numbering: this row was designed as D30, which the panel-shared-state decision had taken by the time it landed; D31–D35 were taken in the same batch, so it is D36. Renumber at merge if the batch settles differently.)* |
| D37 | Returns | **Goods coming back are their own record, and they never move money.** `order_returns` + `order_return_lines` (M25) hold what came back off a **partly shipped, shipped or delivered** order: per line, a quantity, a restock decision, the shelf the units went to and what the goods were worth. `Orders.Return` and `Orders.WithdrawReturn` are transitions on the order — they run inside `Orders.transition`, so the rows, the `restockStock` movement and the `order.returned` event commit together, and neither contains any network I/O. Refunding stays `Payments.Refund`, unchanged: the two are separate operations in the API, in the panel and in the event stream, and the panel's refund dialog now says so in one sentence rather than chaining the two writes behind one button. The order's `status` and `payment_status` are untouched — there is no `returned` status, because whether an order has had goods back is derivable from its returns. The per-line cap (what went out, less what has already come back) is a conditional `INSERT … SELECT … WHERE` in the shape of `reserveStock`, and `doctor` gains a `returns` check because no CHECK can express a sum across rows. `Cancel` and `EditLines` refuse an order with an active return. A return recorded in error is **withdrawn**, not deleted, and the units come back off the shelf through `sellStock`. No new right: `orders.write` covers it, as it covers `Cancel`, which is the same stock movement. | Welding a `restock` flag onto `POST /refund` was the cheaper shape and does not work. `Refund` requires `payment_status = paid` and a provider implementing `Refunder`, and cash on delivery — the only method core ships — satisfies neither, so the engine's own default store could not restock a single return; a test asserts exactly that. A flag also has no memory: nothing would record what has already come back, so a second refund naming the same line restocks the same units again, which is the invented-inventory disease this closes, re-created in a new place. And it would put a quantity-capped, multi-statement stock movement into the one transaction that runs *after* money has already left, where every new failure mode becomes "the money is gone and the state rolled back" (rule 5). Separating them is not extra work; it is the split the engine already makes everywhere else — `EditLines` reports a balance and refuses to settle it, and `Cancel` refuses a shipped order by saying in so many words that this is a return. A `returned` order status was declined because *delivered* stays true, because it would make `stockCommitted` ambiguous, because it has no honest answer for a partial return, and because a new enum value leaks through the CHECK, the OpenAPI enums, `ext/mcp`, the panel's formatters and every consumer's state machine. The guard on `Cancel` and `EditLines` exists because undeliver → delete the last shipment walks a returned order back to `confirmed`, where `restockOrder` restocks every line at full quantity and `moveOrderStock`'s committed branch restocks a reduced line a second time: a five-unit order with two returned would end up seven units richer. It tests for an *active* return so that withdrawing one genuinely restores the order rather than freezing it forever over a mis-key. On a partly shipped order the cap is what actually shipped rather than what was sold, because the units still in the stockroom never reached a customer who could send them back. |
| D38 | Stock history | **An append-only `stock_movements` ledger, written inside every transaction that already moves `variant_stock`, and deliberately not an event.** One row per real change (M26) carries two deltas (`on_hand_delta`, `reserved_delta`) rather than one signed number, the balances after, a `kind` from an eleven-value CHECK, a `source` naming the path (admin, checkout, order, sweeper, import, migration), the operator's `reason`, the order that caused it, and snapshots of sku, location code, order number and actor email so a row survives the deletion of its SKU, the closing of its location and the departure of its author. Every writer records what its statement APPLIED: eight of the eleven get the applied amount and the after-balances from a `RETURNING` on the statement they already ran, and only the three that clamp — `releaseStock`'s `greatest(0, …)`, `SetOnHand`'s absolute write and the CSV import's `greatest($3, reserved)` — take a `SELECT … FOR UPDATE` before-image, whose result is used for arithmetic only while every availability test stays in the UPDATE's own `WHERE`. Checkout's reserve rows are written with a null order and completed by one guarded UPDATE after the order INSERT, rather than hoisting the `nextval`. `Adjust`, `SetOnHand` and `Move` take a trailing `reason string` — a deliberate pre-1.0 break of three exported methods — while the actor is read from `auditActor(ctx)` inside the ledger writer, the same seam the audit trail uses, rather than threaded through five files of free functions. The three `stock.*` audit actions are withdrawn with this row and `GET /api/admin/variants/{id}/history` goes with them: a stock movement is recorded once, here. Reads are two routes under `inventory.read`; `gocommerce doctor` gains a `stock ledger` check asserting that the deltas sum to the balances. | Every inventory change was a destructive UPDATE whose only trace was `updated_at`, which the next movement overwrote — so "the count says 4 and the shelf has 2" had no answer anywhere in the system and a stock-take dispute could not be settled. `variant_stock` has two independent counters and `commitStock` moves both in one statement, so a single `delta` could not encode a commit and would conflate "held for an order" with "left the shelf". The ledger's whole value is that `sum(on_hand_delta)` meets the shelf — a ledger whose sum does not reconcile cannot settle the dispute it exists for — so recording the requested quantity was never an option (five writers wrap their quantity in `CASE WHEN track_inventory` and succeed while moving zero), and deriving each delta from the previous ledger row was rejected because it would fold a raw `UPDATE variant_stock` into the next legitimate movement and attribute it to that operator, making the invariant true by construction and therefore useless. No event, because `events.go` calls its taxonomy a public contract with one aggregate, nothing consumes a stock event, `order.*` already announces every order-driven movement, and a twenty-line order would put forty rows through the at-least-once dispatcher for the sake of a table in the panel; rule 4 requires that an event commit with its change, not that every change have one, and its purpose is met because the row is written inside the same `InTx`. The seam stays open at the cost of one constant: `AggregateVariant`, `stock.moved` emitted from the single `recordMovement` funnel, with no schema change. No second audit record either — `admin_audit` and `stock_movements` would have carried two reason fields for one act, which is how two records come to disagree. The `nextval` stayed where it is because hoisting it would burn an order number on the most common checkout failure there is, and gaps in order numbers are what operators report as bugs. |
| D39 | Scoped discounts | **Scope is evaluated, and the eligible part of the basket is the base.** `discounts.scope` names the kind of thing a rule points at and `discount_targets` (M15, unchanged) holds the ids, kind singular against scope's plural: `products` → `product`. A category target reaches its whole subtree, bounded by `MaxCategoryDepth`, the same walk a tax rate already does; a collection target matches current membership, read at checkout rather than copied when the rule was written. `eligible()` returns which lines a rule covers and what they add up to, and `applyDiscount` is handed that total instead of the subtotal — so scope `order` is unchanged by construction (its base *is* the subtotal and its mask is nil), one rounding still covers the whole of whatever the rule covers, and a fixed amount is clamped to what it applies to rather than to the basket. Which lines were covered is carried into `allocateDiscount`/`computeTax`, and into the refundable figure a return snapshots, because a discount that came off the shoes must not lower the tax base of the hats. `min_subtotal_minor` stays a floor on the whole basket — it is the price of entry to a promotion, not the thing discounted — and the eligible-set floor is expressed instead by refusing when the covered total is zero. Targets ride on `DiscountInput.target_ids`/`DiscountPatch.target_ids` and commit with the rule; there is no separate targets route. A scoped rule with no targets can be neither created nor patched into existence, and a row written before this — the only way one exists — is refused at the till by a message that names the missing half and shown in the panel as `no targets` rather than `live`. Free shipping cannot be scoped, refused in the service, in the evaluator and by `doctor` rather than by a CHECK. A target whose catalog row has been deleted is kept and reported (`missing: true`, a red chip, a doctor count) rather than cascaded away. `Discounts.Preview` takes the `*Cart` instead of a subtotal, a deliberate break of an exported signature with one in-repo caller. No migration: M15 shipped the table and the column. **This amends D27**: a scoped fixed amount no longer survives an edit whole — it is clamped to what is left of the lines it covered — and an order edited until the rule covers nothing is refused rather than silently keeping the money off. | The trap was a column storing a value nothing could act on: POST accepted `scope: "products"`, offered no way to say which products, reported the rule as live in the panel, and refused the code only when a customer typed it. Targets on the input rather than behind a `PUT .../targets` because a scoped rule that exists for even one request without targets IS the broken row, and one transaction closes that window for good; deriving the kind from the scope makes a target of the wrong kind unrepresentable. Rebuilding the table with typed foreign keys under RESTRICT was declined: it is the only change here that could destroy data, a migration cannot tell an empty table from one it emptied, and it would make a promotion that ended last winter block a catalog cleanup — reporting a narrowed promotion puts the fact in front of an operator instead of in front of a foreign key. The tax mask is not an optimisation: without it a scoped discount is spread pro-rata across lines that were charged in full, which under-collects on those lines and, under per-category rates, gets the order's total tax wrong — a figure an accountant finds, stored on the line and printed on an invoice. *(Numbering: designed as D30, which the panel-shared-state decision had taken by the time it landed; D30–D38 were taken in the same batch, so it is D39.)* |
| D40 | Collection curation | **`product_collections` carries two orderings, in two columns.** `position` stays what `PUT /api/admin/products/{id}/collections` writes — where a collection sits in one product's own list, which is the order its chips render in — and `member_position` (M27) is where the product sits inside one collection, which is what a collection-scoped listing orders by and what `PUT /api/admin/collections/{id}/products` writes. Neither writer touches the other's column: a membership that survives a save keeps the place it was curated into, and a membership created from either side lands at the tail of the other side's list rather than at its head. Both writes reconcile rather than delete-and-reinsert, so surviving rows keep their numbers. The migration backfills `member_position` from `ORDER BY position, product_id` — the shipped expression — so no collection's visible order moves on upgrade, the public storefront's included. Ordering a collection any other way is not a choice the listing offers: an explicit sort sent with `collection_id` is a validation error, not a silent override. | One integer could not carry both, and it was already being read both ways — `loadProductCollections` ordered a product's chips by it while `ListProducts` ordered a collection's members by it under the comment "a collection is curated by hand". The only writer wrote the product's own list index, so a collection's order was an artefact of how many other collections each of its members happened to be in: the bug was invisible because the listing looked plausible, it was just never what anyone chose. A collection-scoped writer sharing the column would have fixed that by breaking the other reading instead, and retiring the chip order would have broken a shipped, documented and tested contract for an order nothing depends on. A second integer on a three-column join table is cheaper than a permanent ambiguity. |
| D41 | Taxonomy import | **The embedded taxonomy and its attribute dictionary get an HTTP door: `POST /api/admin/import/taxonomy` and `POST /api/admin/import/category-attributes`, both `data.import`.** An empty body means the embedded set, a body is the operator's own file in the published format, and the tree route chains the attribute import only for the embedded set — the rule `gocommerce taxonomy import` already follows, because the fields match on the `taxonomy_gid` that only that source writes. Still nothing automatic: no migration imports it and no default runs it. Both handlers extend their own read and write deadlines, which required `statusWriter` to gain the `Unwrap` method stdlib's `ResponseController` walks for. | `taxonomy.go` said "so it is a command they run", which was a statement about it not being a default rather than about the shell being the only way in. A Docker or compose deployment — this repo ships both — has a browser and no shell, and a store that cannot populate a category tree cannot use the Categories screen at all. The routes sit under `/api/admin/import/` because that is where `data.import` already lives and what the panel's import client already builds. Raising the global timeouts to cover two buttons, or growing a job queue this engine does not have, were both worse than one response controller — but the controller only works on a chain that can be unwrapped, and every request passes through `logMW`'s writer, so without `Unwrap` the extension was a silent no-op on every route in the engine. |
| D42 | The attribute dictionary | **`taxonomy_attributes` becomes CRUD-able at `/api/admin/taxonomy-attributes`, read with `catalog.read` and written with `catalog.write`, and a handle is folded to lower case and is not patchable.** The dictionary is what a category's `metadata.attributes` names: a handle, a label, and the values that field offers wherever it is asked. `TaxonomyAttributePatch` carries label and choices only; moving a field to a new handle is a create, an edit of the categories that name it, and a delete. Choices keep the order they were sent in, minus blanks and exact repeats. A delete is never refused, whatever still names the handle. | The handle is the key every category's metadata holds and the table's own primary key, so renaming it here would detach every category using the field and say nothing. Case is folded because the match in `attachAttributes` is a literal `= ANY(...)` while the list search is `lower(handle) LIKE` — so "Color" and "color" are two rows that look like one, and only one of them will ever match anything. Every published handle is already lowercase ASCII, so folding moves nothing that exists; the bulk importer deliberately does not fold, because rewriting handles a store's own metadata already matches is a silent data change. Delete is safe rather than destructive: `attachAttributes` already treats a missing row as a free-text field, so removing an entry degrades a field instead of breaking one, and a 409 would refuse a safe act. |
| D43 | Dry-run answers | **An admin dry run reports a refusal as data, not as an error.** `POST /api/admin/discounts/{id}/preview` answers 200 with `{"applies": false, "reason": "that discount has expired"}`, where the public `PUT /api/carts/{token}/discount` answers 400 with the same sentence from the error taxonomy. Both call `Discounts.eligible` and neither consumes anything; what differs is the question. The taxonomy still owns the request being wrong — an unknown id is 404, a negative subtotal is 400 — and no other route takes this shape without its own decision. The same change adds the refusal `eligible` was missing: a codeless automatic rule, which `applyTx` has always skipped by short-circuiting on an empty code, now says so out loud, because id-keying is the first caller that can hold one (D29). | A storefront is *using* a code and a refusal is a failed attempt; an operator is *asking about* one, and "no, it expired on Friday" is a successful answer to that question. Returning 400 would make the panel render a failure for a test that ran correctly, and would push the reason into an error handler where it reads as a bug rather than as the finding the operator came for. |
| D44 | Closed locations | **An operator may not receive stock at a closed location.** `Inventory.Move`'s destination, `Adjust` with a positive delta, `SetOnHand` raising a count, and a CSV `stock_on_hand:<code>` cell raising a count are refused with a 409 when the location is inactive. Every decrease, and `Move`'s source, are always allowed. Returning already-sold units is exempt: `restockStock` puts a cancelled or reduced order line back on the shelf recorded on the line, and refusing would lose units that have already left the shelf. The check lives in `resolveShelf`, which reads `locations` FOR SHARE before any `variant_stock` row is touched, and `Locations.Update`/`Delete` take the same row FOR UPDATE inside their `InTx` before counting stock — one lock, one order, so the guard and the refusal cannot pass each other and cannot deadlock. | `Location.Active` already documents itself as "a statement that the place is empty, not a way to hide what is in it", and `Locations.Update`/`Delete` already refuse to close a place that holds anything — but nothing stopped the stock arriving afterwards, and an inactive location's units are counted by the variant totals while `pickLocation` skips them, so they are for sale and unsellable at once. Putting the check in `resolveLocation` would cover all three movements in one line and would also block `Move`'s source, making stranded stock permanently unmovable — the exact state the rule exists to prevent. The two guards were unsynchronised reads before this; `Delete`'s in particular deletes `variant_stock` rows unconditionally after checking, so a race destroyed units rather than stranding them. |
| D45 | Reporting | **Reports are a reading of the orders, in core, behind `orders.read`.** `core/reports.go` serves `GET /api/admin/reports/sales?from=&to=&group_by=day|week|month&tz=` and `GET /api/admin/reports/top-products` over the existing tables: no migration, no table, no column, no new right, no event, no dependency. **A sale is an order whose stock has left the shelf** — `status IN ('confirmed','partial','shipped','delivered')`, the `stockCommitted` predicate the state machine already owns — so a cash-on-delivery order counts before the money lands, a partly shipped one counts in full, and an in-flight `pending` checkout, which the unpaid sweeper may cancel within `OrderTTL`, does not. Pending and cancelled orders are reported beside the totals, never inside them. Payment does not decide whether a sale happened: it splits the same money into `paid`, `outstanding` (pending or failed) and `refunded`, which sum to `total` exactly, and the money that actually came back is reported as `paid.refunded` with `paid.net_collected` beside it rather than netted off. Every bucket satisfies `total = net + tax + shipping` in both tax modes, where `net = subtotal − discount − (tax when the order was inclusive)`. Buckets are calendar day, ISO week or month resolved in a caller-supplied IANA zone (default UTC, validated against the database's own catalogue), and the window's own edges are resolved in that same zone. Figures are grouped by `orders.currency` and never summed across currencies. | The one question every shop owner opens an admin panel to ask had no answer in the panel at all — the nearest thing was a client-side sum over the eight most recent orders, and the real answer was "export the CSV and open a spreadsheet". Core owns it rather than a module because the definition of a sale is a statement about the order state machine (rule 3): a module holding a second copy of it would drift from `orders.go` the first time a transition changed — which `partial` proved by arriving. `stockCommitted` rather than `payment_status = 'paid'` because cash on delivery is the only built-in provider and confirms the order at checkout while payment stays pending, so a paid-only report shows a stock GoCommerce store zero; and rather than `status <> 'cancelled'` because that counts the very orders `orders_unpaid_idx` exists to find and `SweepUnpaid` cancels, so the headline would inflate today and deflate tomorrow with nobody touching anything. `orders.read` rather than a new right because every figure is a sum of rows that right already returns in full, and `data.export` already streams the whole order book as CSV; a lock on a window whose door is standing open is theatre, and D24's test for a right is whether a real store would draw the line there. The zone is resolved in PostgreSQL because that is where the tz database already lives; Go's `time/tzdata` would be a second, drifting copy. Bucketing is by `created_at` because there is no `confirmed_at` and inventing one is a migration plus a backfill of a fact old orders never had; the consequence, that a late `MarkPaid` or an order edit changes a past bucket, is stated rather than hidden. |
| D46 | List ordering | **Allow-listed sort on the admin listings: `?sort=<field>&order=asc|desc`, one `SortSpec` per listing in `core/sorting.go`.** A request supplies a key, never an expression; the SQL that key maps to is a literal in that file, and the direction is a bool rendered as `ASC` or `DESC`. Both directions of every field are written out in full so NULLS placement is pinned per field — `NULLS LAST` on the nullable ones, no clause at all on the rest. Every resolved clause ends with a key unique in the result set, flipped to match the direction; the two pre-existing defaults that were not unique (customers' `max(o.created_at) DESC`, the category search's `down.depth, down.path`) gain one too. No sort may introduce a join: product `price` and `available` are correlated scalar subqueries. An unknown key, a bad direction, or a direction with no field is a 400 naming what exists — never a silent fall-back, and `sort` together with `collection_id` is refused rather than resolved. Products, orders, discounts, media, customers, category *search* and the low-stock report are covered; the category tree and the public product listing are not, and columns a row computes from another table — a variant count, an order's line count, a discount's mixed-unit value, a computed state chip — are deliberately not sortable. No new right (sorting a list is reading it) and `ListMeta` is unchanged. `Categories.Search` takes a `CategoryQuery`, a recorded pre-1.0 break which also gives that endpoint the Offset it parsed and discarded; `Inventory.LowStock` takes a `LowStockQuery` for the same reason, and the low-stock report is the one route served by **two** specs — `lowStockSorts` over the store-wide sums and `locationStockSorts` over one shelf's own columns — because the numbers a row shows change with `location_id` and an ordering that did not change with them would draw a column of figures in an order that has nothing to do with them. Their key sets are pinned equal by a test, since the route parses against one of them whichever statement will run. M29 adds five performance-only indexes, all `IF NOT EXISTS`. In the panel a header cycles ascending → descending → off, and the pair rides in the address bar through `listState` alongside the screen's filters and page. | The engine already paged both ways while offering exactly one order per listing, so a 5,000-product catalogue could only be walked newest-first. A generic `order=` expression (PostgREST's shape) was rejected: it puts SQL in a query string, and every defence against that is a parser we would then own. An allow-list is a map because a map is auditable — one file holds every string that can become an ORDER BY, and a test compares it with `openapi.json`'s enums. The tiebreaker is not tidiness: PostgreSQL may order tied rows differently in two executions of the same statement, so without it a row arrives on page 1 and again on page 2 while another arrives on neither, silently, with a total that says nothing is missing. Pinning NULLS per field rather than universally is what keeps M29's ascending indexes usable in both directions: an ASC NULLS LAST btree read backwards yields DESC NULLS FIRST, which an explicit `DESC NULLS LAST` would not match, and the panel makes descending the first click on dates and money. The no-join rule exists because `meta.total` is counted from the un-joined FROM: a JOIN in the ORDER BY would multiply a product by its variants and make the count disagree with the rows. Categories takes Offset in the same change because a listing that can be re-ordered but not paged is a lie — depth-first-first-fifty announces itself as the top of a tree, title-ascending-first-fifty looks like the complete answer with no Z in it — and honouring the offset is what makes `count(*) OVER ()`'s blind spot past the end reachable, so the count fix comes with it. The third header state exists because the engine's own order is the working order and a two-state toggle leaves no way back to it. |
| D47 | Cart abandonment | **A cart that expires with something in it becomes a record, not a deletion.** The five-minute sweeper splits into three phases with disjoint predicates: an expired cart holding at least one line is marked `abandoned`, stamped with `abandoned_at` and announced as `cart.abandoned` (aggregate `cart`, payload `CartEvent`, lines reusing `OrderEventLine`) in one transaction, claimed with `FOR UPDATE SKIP LOCKED` and capped at 500 a pass; an expired cart holding nothing is still deleted outright; an abandoned cart is deleted once `Config.CartRetention` (new, default 720h) has passed, measured from `abandoned_at`. A biconditional CHECK, `carts_abandoned_at_matches_status` (M28), makes the status and the timestamp unable to drift apart. `abandoned` is reversible — the first mutation revives it to `open` with a fresh TTL under the lock `openCartID` and `lockCartForCheckout` already take, so a recovery link lands on a live basket that can still be bought. `Carts.SweepExpired` keeps its exported signature and narrows its job; `Abandon` and `PurgeAbandoned` are new. Two read-only admin routes (`GET /api/admin/carts`, `GET /api/admin/carts/{id}`) sit under the existing `orders.read`; they address a cart by row id, report a **derived** state (live, abandoned or converted, computed from status AND `expires_at`) and never return the cart token, exactly as `orderColumns` never returns an order's `access_token`. `Carts.SetEmail`, which had no route and no caller anywhere in the repository, is mounted as the public `PUT /api/carts/{cartId}/email`. No twenty-first right. This reverses the position stated in `skills/carts.md`, that an operator has no way to browse other people's baskets. | The `abandoned` status has been in the M2 CHECK since the first migration and nothing ever wrote it, so the second-most-valuable report a store has was being destroyed on a five-minute ticker while the enum describing it sat unused. The DELETE was not laziness — `POST /api/carts` is unauthenticated, so unswept carts are an unbounded-growth vector — which is why keeping them needed two bounds rather than a one-line UPDATE swap: minting a cart costs one anonymous POST with no body, while putting a line in one costs a valid variant id and a stock check, so the flood and the report are almost exactly disjoint sets, and what survives is bounded a second time by retention. Retention runs off `abandoned_at` and not `expires_at` because draining a backlog would otherwise abandon and purge a cart inside the same pass, announcing a live recovery token for a row that no longer exists. The CHECK is biconditional because both drift directions are silent and permanent: a marked row with no timestamp never expires, and a stale timestamp surviving a revival purges a basket out from under a live shopper. A cart right was declined under D24's own test — a cart is an order that has not happened, `orders.read` already exposes every order's email and address, and no real store would withhold the two separately. The privacy position this reverses was about idle browsing; the cost of holding it was that a shopper who filled a basket and left could never be reached and the operator could not see it had happened, and the mitigation is that the admin surface is read-only and tokenless. The retention consequence is stated rather than absorbed: a cart and the email on it now live up to `CartTTL + CartRetention`, sixty days by default against thirty before. |
| D48 | Finding an order, noting it, reading its history | **One `?q=` over three columns, `orders.metadata` as the shop's own note, and one merged timeline route.** `OrderQuery.Search` matches `lower(o.number)` by prefix and `lower(o.email)`/`lower(o.name)` by contains, honoured by `Orders.List` and `Transfer.ExportOrders` from one shared clause builder; `?email=` stays exact equality beside it. `OrderPatch` gains `Metadata *Metadata`, replaced whole like every other patch that carries one, whose reserved key `notes` must be a string, is refused by `validateCheckoutInput` on the way in, and is removed by `(*Order).Redact()` on the way out along with the access token. A patch carrying nothing but metadata is accepted on a cancelled order, and emits no event when it leaves every other key as it found it — but it still writes an `order.note` audit row. `GET /api/admin/orders/{id}/timeline` (`orders.read`) is one reading of `outbox_events` and `admin_audit` together, joined on `changes.event` plus the identical `created_at` both rows take from `now()` inside one transaction, with `last_error` blanked for a caller without `store.operate`. | Three problems that all turned out to be reading problems: the column, the index and both tables were already there. **Search:** the box said "by customer email" and compared for equality, so an order number off a receipt, a surname or half an address all answered "nothing matches that" — which reads as "no such order" rather than "this box only takes a whole address". The number is anchored and the rest contained because that is how each is read; `?email=` is not widened because the Customers screen links with a whole address and means that one person, and `_` is a LIKE wildcard that is common in real addresses. `ExportOrders` honours the same field from the same function, because a field honoured by one of a struct's consumers is a trap: an operator who searches, sees three orders and clicks Export must not get the whole table. **The note:** orders were the one entity whose patch could not reach their own metadata, so a notes table would have meant a migration, a route, a right and a screen to store a string the row had room for. Silence is the part that had to be decided — every `order.*` event reaches the notifier bridge and fans out to the customer's email and phone, so a note typed through the ordinary patch would mail the shopper once per line an operator wrote to themselves. A note is not a state change and the frozen taxonomy has no name for it, so `transition`'s existing quiet path is the right one and D8's outbox guarantee is untouched: there is no state change here to pair an event with. But silence is earned rather than assumed, which is why `lockOrder` now reads the metadata it locks — a whole-object replace that also drops a module's key is a real change and announces itself — and quiet in the outbox is not quiet in the trail, because "who wrote this and what did it say before" is exactly what a colleague asks later. The cancellation guard is relaxed on the same reasoning: it protects facts that can no longer matter on a dead order, an address or a payment method, while "refunded manually by bank transfer, ref 88213" is written precisely on the order that went wrong. Reserving the key in both directions is what makes it the shop's: `GET /api/orders/{number}?token=…` returns metadata verbatim and D22 makes that token the shopper's only handle on their order, and `POST /api/checkout/{code}` is unauthenticated and stores metadata verbatim, so without the inbound refusal a storefront could write the shop's own note and an operator would read a stranger's text as a colleague's. **The timeline:** one route rather than two, because "what happened to this order" is one question and two cards under one right would have rendered every operator-caused transition twice — once as the event and once as the act. The correlation is the only one that exists: `outbox_events` carries no audit id and `admin_audit` carries no event id, but both rows are written by one `InTx` and both take `now()`, which is `transaction_timestamp()`. `last_error` is gated inside the handler rather than by gating the route, because a warehouse or support account holding `orders.read` should see that a confirmation did not go out, and a raw handler error string is not something they should inherit with it. |
| D49 | Operator surface for the outbox | **The outbox gets a read/repair API and a panel screen, not a SQL runbook.** `GET /api/admin/events` (filters: state, name, aggregate pair; paged), `GET /api/admin/events/{id}`, `POST /api/admin/events/{id}/retry` and `POST /api/admin/events/retry-dead`, all behind `store.operate`. An event's public identity on these routes is its uuid, never the table's bigserial, which is returned as `seq` for ordering only. Retry is two acts split by state under the row lock: a **dead** row is un-parked (`dead = false, attempts = 0, available_at = now()`) with a fresh attempt budget; a **pending** row is only brought forward (`available_at = now()`, attempts untouched) so the twelve-attempt bound survives; a **published** row is refused 409. Neither clears `last_error`. The single-row path takes `FOR UPDATE NOWAIT` and answers 409 on 55P03; the bulk path takes `FOR UPDATE SKIP LOCKED`, is capped at 500 rows and reports `{requeued, remaining}` counted in the same transaction. The requeue writes no event of its own. M30 adds `outbox_dead_idx` (partial on `dead AND published_at IS NULL`) and `outbox_name_idx`, both `IF NOT EXISTS`. The screen is **Settings → Platform → Events**, and the Settings nav entry's health dot covers dead letters as well as failing checks. | `DrainOutbox` cannot recover a dead letter: the claim filters `NOT dead`, so a drain wired to a “retry” button would do nothing — the operator-valuable verb is un-park-and-nudge, which is exactly the `UPDATE` docs/operations.md used to hand to psql, moved behind a service so rule 3 holds and the act is logged with a name. The uuid because it is what docs/events.md calls stable, what handlers dedupe on and what the log prints — the value an operator actually has in hand. Pending and dead are separated because `attempts` **is** the dead-letter bound: resetting it on a row that is merely backed off lets an impatient operator hold a permanently broken event in retry forever, so it never becomes the evidence the policy exists to produce; refusing a pending retry outright is no answer either, since backoff reaches fifteen minutes. `last_error` survives because the diagnosis should outlive the repair, and because a success clears it and a failure overwrites it anyway. NOWAIT rather than a plain wait because `InTx` sets no statement timeout and `WriteTimeout` does not cancel a request context, so an unbounded lock wait parks a pooled connection with nothing to show the operator; SKIP LOCKED on the bulk path so two operators clearing a backlog take disjoint sets. Neither can stall delivery: the dispatcher's own claim skips locked rows and holds its locks only while it reads a batch into memory. No event for the repair, because announcing a fix to the outbox through the outbox puts it inside the thing being repaired — a broken handler would dead-letter the announcement too. |
| D50 | Who may send a notification | **The engine owns the channels; a module owns whether to write to this person at all.** `App.Notify(ctx, Notification) error` exports what `notifierSet.send` already did, refusing an unknown channel and an empty recipient rather than reporting a delivery that never happened. The engine still subscribes `order.*` to delivery and nothing else — `subscribeNotifications` explains why, and this does not change it. `ext/cart-recovery` is the first caller: it hears `cart.abandoned`, holds the schedule and the opt-out question, and guards on its own start time so installing it cannot mail a year of stale baskets. | Without this the comment on `subscribeNotifications` described a module that could not be built: `RegisterNotifier` adds a backend and the set that drives it was unexported, so a recovery module had nothing to deliver with short of shipping a transport, which rule 2 forbids. Exporting the send is a smaller change than moving the schedule into core, and it leaves the marketing decision where that comment put it. |
| D51 | Outgoing webhooks | **A module, with a delivery table of its own, because the core outbox retries an event rather than a recipient.** `ext/webhooks` owns `webhook_endpoints` and `webhook_deliveries` (its own outbox: `FOR UPDATE SKIP LOCKED`, a visibility window, exponential backoff, dead after 12). Its subscriber fans one event into one row per matching active endpoint and returns nil immediately; an `OnStart` worker does the sending. Signature is HMAC-SHA256 over `<timestamp>.<body>` in `X-GoCommerce-Signature: t=…,v1=…`, the idiom `ext/payments-stripe` already verifies with, inverted. The secret is stored recoverably and returned in full only by the call that creates or rotates it, because a store that cannot read the secret cannot sign with it. Admin routes take `store.operate`, the right D49 gave the outbox surface. Order of delivery is not guaranteed. | Returning an error from the subscriber would be the obvious thing and it is wrong twice over: `eventBus.dispatch` joins every subscriber's failures and the outbox retries **the whole event**, so one unreachable merchant would re-issue invoices, re-fire notifications and re-POST to the endpoints that already succeeded — and `runHandler` bounds each handler with a timeout, so waiting on somebody else's HTTP inside the dispatcher stalls every consumer behind it. A second delivery table is not duplication for its own sake: it is the same problem one layer out, and the answer that already works. |
| D52 | Shipping methods and rates | **Core owns the zones, the rates and the shopper's choice; carriers stay modules.** `shipping_zones` matches a destination and `shipping_rates` price a named method over a basket-subtotal band, so "Standard 49, free over 2000" is two rates with one name and disjoint bands. The most specific zone wins — state, then country, then the catch-all — which is the rule `taxes.go` already applies to a rate's place. `GET /api/checkout/rates` quotes against a real basket rather than listing prices, because the price depends on what is in it; `CheckoutInput.ShippingRate` is re-quoted under the same lock that re-checks every price and reservation, and a rate that moved while the shopper was typing is refused exactly as a sold-out line is. The order snapshots the method's name beside `shipping_minor`, the way a line snapshots its price and its tax. Shipping stays untaxed. | The split is tax's, and for tax's reason: §30 excludes *carrier APIs* and *tax providers*, not rates. A module cannot own this — rule 3 forbids it writing `orders`, and `shipping_minor` is decided inside the checkout transaction — so "a module owns rates" really means "core grows a port and every store must install something before it can offer two options". Subtotal bands rather than weight for the first cut: `weight_grams` is on every variant and unused, but a store that prices delivery by cart value cannot express that at all, and that is what most of them sell. Untaxed because whether delivery carries tax varies by jurisdiction and guessing invents a liability — the same sentence `computeTax` has always carried, now true for a different reason. |
| D53 | The panel's design system | **The panel moves to DESIGN.md — Tailwind v4 with zero-chroma `oklch` tokens — one screen at a time, and PocketBase keeps the controls.** The two stylesheets share a document permanently rather than transitionally, because buttons, inputs, dropdowns and the side drawer stay PocketBase's by decision. So Tailwind is loaded with the `tw:` prefix and without Preflight: `tw:flex`, `tw:p-4`. Its utilities are imported **unlayered**, and its spacing base is pinned to `4px`. The dashboard is the first screen; the other thirty-five are unchanged and keep working. | Three things forced the shape, all measured rather than assumed. Unlayered CSS beats layered CSS outright, so utilities in a layer lost to every one of the sixteen unlayered PocketBase sheets — `a { text-decoration: underline }` beat `text-muted-foreground`. `--spacing` is defined by both, and `vars.css` sets it to 30px, so `p-4` computed to 80px. `.grid` is defined by both, and PocketBase's is a flex grid with negative margins, so a tile row measured 0px wide. A prefix answers all three at once and keeps answering as `.table`, `.btn`, `.label` and `--radius` come up. The cost is that DESIGN.md's literal markup does not paste in unchanged, which is the price of keeping the controls. |
| D54 | Reordering siblings | **One route that renumbers a parent's children in a single transaction, rather than a PATCH per row.** `PUT /api/admin/categories/reorder` takes a parent and the ordered ids of its children and writes every position inside one `InTx`, refusing a list that is not exactly that parent's children. The panel drags to reorder siblings only; moving a category to a different parent stays in the drawer's Parent picker. | Position is per-parent and integer, so dropping a row above three siblings renumbers four rows. Sent as four PATCHes that is four round trips and, worse, not atomic: a failure on the third leaves an order that is half old and half new, with no way for the panel to know which. There is no gap to slot into either — `ORDER BY position, id` over dense integers means the fractional-index trick degrades to renumbering anyway. Siblings only because the table renders a flattened tree: a drop between two rows at different depths has no single honest meaning, and reparenting already has a control that says exactly what it does. |

---

## 6. Core domain model

### 6.1 Product hierarchy

The fundamental model is:

```text
Product
  ├── Option: Size
  │     ├── S
  │     ├── M
  │     └── L
  ├── Option: Color
  │     ├── Black
  │     └── White
  └── Variants
        ├── SKU-001 = M / Black
        ├── SKU-002 = M / White
        └── SKU-003 = L / Black
```

A product is the merchandising concept.

A variant is the sellable unit.

### 6.2 Product fields

Core product fields:

```text
id
slug
name
description
status
base_price_minor
currency
metadata
created_at
updated_at
```

`base_price_minor` is useful for simple products and presentation, but the
**variant price is authoritative at checkout** when variants exist.

### 6.3 Variant fields

```text
id
product_id
sku
barcode nullable
price_minor
compare_at_price_minor nullable
stock_on_hand
stock_reserved
active
weight_grams nullable
metadata
created_at
updated_at
```

`available_stock` is derived as:

```text
stock_on_hand - stock_reserved
```

Stock mutations happen through an inventory service, never through arbitrary
module SQL.

### 6.4 Option model

Core stores the definitions required to understand a variant:

```text
product_options
product_option_values
variant_option_values
```

Variant combinations must be unique within a product.

For example, a product cannot contain two active variants representing exactly
`Color=Black + Size=M`.

### 6.5 Single-variant products

A product that does not need options still has one default variant.

This keeps all cart/order/inventory logic variant-centric without making a
simple product feel complicated to the API client.

The client can treat:

```text
Product -> default variant
```

as a zero-configuration case.

---

## 7. Inventory model

Inventory belongs to variants.

### 7.1 Reservation flow

Checkout must not merely decrement stock blindly.

The transaction performs:

```text
available = on_hand - reserved

require available >= requested

reserved += requested
```

The reservation is tied to the order.

### 7.2 Order confirmation

On a successful paid/COD confirmation, the reservation is converted into a
committed sale:

```text
reserved -= quantity
on_hand  -= quantity
```

### 7.3 Cancellation

Before shipment:

```text
reserved -= quantity
```

If inventory was already committed to the sale, cancellation logic must issue
an explicit restock transaction according to the order state.

The state transition rules must be tested rather than left to callers.

### 7.4 Concurrency

Stock allocation uses PostgreSQL transactional locking.

Proof test:

> 100 concurrent checkouts against 1 available unit create exactly one
> successful reservation/order and 99 conflicts; inventory never becomes
> negative.

---

## 8. Cart design

**Guest checkout is a permanent guarantee (D22)** — a cart needs a token and
an email, never an account. Everything below holds with no identity module
installed, and must keep holding after one is.

Carts support:

- guest tokens;
- optional authenticated customer ID in the future (additive, never required);
- variant-based line items;
- quantity changes;
- item removal;
- price snapshots;
- inventory availability checks;
- cart expiration;
- cart metadata.

A cart line references a variant, not merely a product:

```json
{
  "variant_id": 42,
  "qty": 2
}
```

The API may return the parent product and option selections for convenience.

### Cart price policy

Adding an item records the current display price, but checkout is authoritative.

At checkout:

1. the variant must still exist and be active;
2. the requested quantity must still be available;
3. the current authoritative price is compared with the cart snapshot;
4. a changed price produces `409 conflict`;
5. the cart is refreshed with current values;
6. the shopper confirms again.

The engine never silently reprices a confirmed order.

---

## 9. Checkout design

Checkout is divided into three logical boundaries.

### Phase A — validate and create order

Inside one PostgreSQL transaction:

1. load cart;
2. validate every variant;
3. lock relevant inventory rows;
4. re-check prices and active state;
5. reserve inventory;
6. create immutable order + order lines;
7. store idempotency state;
8. consume/close cart;
9. append domain events to the outbox;
10. commit.

### Phase B — initiate external payment

After commit:

```text
PaymentProvider.Initiate(...)
```

never executes while a transaction is holding core write locks.

If initiation fails:

- the order remains valid;
- payment status remains `pending`;
- the stored idempotency key points to the same order;
- retry resumes payment initiation instead of creating another order.

### Phase C — payment confirmation

A provider webhook invokes the provider module.

The module:

1. validates the provider signature;
2. deduplicates the provider event;
3. calls `Payments.MarkPaid(...)`;
4. core performs the state transition;
5. core records the resulting event in the outbox.

The provider never updates `orders` directly.

---

## 10. Orders

### 10.1 Order status

```text
pending
  ↓
confirmed
  ↓
partial (some of it has gone out; skipped when one shipment covers everything)
  ↓
shipped
  ↓
delivered
```

`partial` is derived from `fulfillment_lines` after every shipment is written or
removed, never set by a caller, so it cannot disagree with the parcels behind
it (D32).

Cancellation is a controlled transition, not a generic update.

### 10.2 Payment status

```text
pending → paid → refunded
       ↘ failed
```

Refunding a payment is an explicit operation.

### 10.3 Order lines

Order lines are immutable snapshots containing at minimum:

```text
product_id nullable
variant_id nullable
sku
title
variant_label
quantity
unit_price_minor
total_minor
metadata
```

Historical orders must remain understandable even when products or variants
are later deleted or changed.

### 10.4 Customer snapshot

Order stores the checkout-time customer information:

```text
email
phone
name
address snapshot
```

A future customer-account module can maintain the long-lived customer entity,
but historical orders never depend on mutable customer records for their legal
or operational meaning.

---

## 11. Transactional outbox — the major R3 upgrade

The outbox is now a **core correctness mechanism**.

### 11.1 The problem

The dangerous pattern is:

```text
BEGIN
change order
COMMIT
publish event
        ↑
      crash
```

A process failure in the gap loses the event even though the order change is
permanently committed.

That is unacceptable for events such as:

- `order.created`;
- `order.paid`;
- `order.shipped`;
- `order.delivered`;
- `order.cancelled`.

### 11.2 The R3 solution

The business transaction writes both state and an outbox record:

```text
BEGIN
  update orders
  update inventory
  INSERT outbox_events
COMMIT
```

Either everything commits, or none of it does.

### 11.3 Outbox schema

```sql
outbox_events (
    id              uuid primary key,
    event_name      text not null,
    event_version   integer not null,
    aggregate_type  text not null,
    aggregate_id    bigint not null,
    payload         jsonb not null,
    created_at      timestamptz not null,
    available_at    timestamptz not null,
    published_at    timestamptz null,
    attempts        integer not null default 0,
    last_error      text null
)
```

Indexes:

```text
(published_at, available_at, created_at)
(aggregate_type, aggregate_id, created_at)
```

### 11.4 Dispatcher

The built-in dispatcher continuously claims unpublished rows, delivers them
to registered handlers, and marks successful delivery.

PostgreSQL row locking prevents multiple application instances from processing
the same outbox row concurrently.

Conceptually:

```text
SELECT ...
FROM outbox_events
WHERE published_at IS NULL
  AND available_at <= now()
ORDER BY created_at
FOR UPDATE SKIP LOCKED
LIMIT N
```

Delivery is **at-least-once**.

A crash after a handler runs but before `published_at` is persisted can cause
a duplicate delivery. Handlers must therefore be idempotent.

### 11.5 Event identity

Every event has a stable UUID.

Consumers that perform non-idempotent work maintain a processed-event table or
provider-specific idempotency key.

### 11.6 Retry strategy

Failed delivery uses exponential backoff with a maximum retry delay and keeps:

```text
attempts
last_error
available_at
```

A dead-letter state is preferred over silently deleting a repeatedly failing
event.

### 11.7 Operational rule

The database is the source of truth for whether an event exists.

The bus is the delivery mechanism.

This separation makes the default synchronous dispatcher and future Redis/Kafka
transports implementation details rather than correctness dependencies.

---

## 12. Event contract

Initial frozen taxonomy:

```text
order.created
order.paid
order.shipped
order.delivered
order.cancelled
```

Event envelope:

```go
type Event struct {
    ID       string          `json:"id"`
    Name     string          `json:"name"`
    Version  int             `json:"v"`
    At       time.Time       `json:"at"`
    AggregateType string     `json:"aggregate_type"`
    AggregateID int64        `json:"aggregate_id"`
    Data     json.RawMessage `json:"data"`
}
```

### Event rules

1. Events are created inside the same transaction as the state change.
2. Event payloads are immutable after commit.
3. Payload schema changes require a new event version.
4. Consumers must tolerate duplicate delivery.
5. Consumers must not assume synchronous delivery.
6. Core owns event creation; modules never publish state-transition events by
   themselves.

Do not freeze speculative events such as `product.updated` until a real module
needs them.

---

## 13. Modules and extension mechanism

A module remains an ordinary Go value implementing:

```go
type Module interface {
    Name() string
    Migrations() []Migration
    Register(app *App) error
}
```

Modules are passed to `gocommerce.New(...)`.

### Allowed module capabilities

```go
RegisterPayment(...)
RegisterFulfillment(...)
RegisterNotifier(...)
RegisterTranslator(...)
Handle(...)            // validated against /x/<name>/
HandleAdmin(...)       // validated against /api/admin/x/<name>/, auth wrapped
Subscribe(...)
OnStart(...)
OnStop(...)
```

(There is no `RegisterSearch` or `RegisterStorage`; see §5's port list for why.)

### Hard rule

Modules may create and own their own PostgreSQL tables.

Modules **may not mutate core commerce tables directly**.

Instead:

```text
module
  ↓
core service
  ↓
transaction
  ↓
state change + outbox event
```

This is the invariant that keeps the system composable.

---

## 14. Payments

### Built-in COD

COD remains built into core because it is a valid payment method that requires
no external provider.

Behavior:

```text
checkout
  ↓
order confirmed
  ↓
payment_status = pending
  ↓
delivery
  ↓
admin marks paid
```

### Payment provider interface

```go
type PaymentProvider interface {
    Code() string
    Initiate(ctx context.Context, order *Order, opts PayOptions) (PaymentIntent, error)
}
```

Optional webhook capability:

```go
type WebhookProvider interface {
    Webhook() http.Handler
}
```

### First external provider

`payments-stripe`

Responsibilities:

- create PaymentIntent;
- return client secret/redirect data;
- verify webhook signatures;
- deduplicate webhook events;
- invoke `Payments.MarkPaid` / `Payments.MarkFailed`;
- implement refund operations.

`payments-razorpay` follows the same port.

---

## 15. Fulfillment

Core provides:

```text
manual fulfillment
```

External carriers are modules.

First carrier:

```text
fulfill-shiprocket
```

The provider receives an order plus a typed shipping request and returns:

```go
type Shipment struct {
    Provider string
    Tracking string
    Carrier  string
    LabelURL string
}
```

The request carries `Lines`, already resolved by the engine to explicit
quantities — an empty list means everything the order still owes — so a provider
can declare the parcel's real contents and value rather than the whole order's.
What the order still owes after it is the engine's decision, not the provider's.

Carrier webhooks never update orders directly. They call core fulfillment
services.

---

## 16. Notifications

Core defines the notification abstraction and durable event triggers.

Modules implement actual delivery:

```text
notify-sendgrid
notify-resend
notify-msg91
notify-twilio
```

Templates remain outside core.

A provider failure must not corrupt an order transaction.

Notifications are therefore downstream consumers of committed events.

---

## 17. Search

Search remains outside core.

Core owns:

```text
catalog + variants + availability + canonical state
```

Search modules own:

```text
indexing + ranking + autocomplete + faceting
```

Initial likely modules:

```text
search-meilisearch
search-typesense
```

The event/outbox layer is the integration mechanism:

```text
product/variant change
        ↓
   durable event
        ↓
 search module
        ↓
 search index
```

This makes search rebuildable and independently scalable.

---

## 18. Imports and exports

CSV support remains, but the model must now be variant-aware.

### Products export

Suggested columns:

```text
product_id
product_slug
product_title
variant_id
sku
barcode
variant_options
price_minor
compare_at_price_minor
stock_on_hand
active
weight_grams
metadata
```

### Import behavior

Products/variants upsert by stable SKU where appropriate.

The import engine supports:

- dry-run;
- streaming;
- row-level error reporting;
- formula-injection protection on export;
- lossless export/edit/import round-trip;
- explicit option/variant combination validation.

### Orders export

Orders continue to export one row per order item because this is useful for
accounting and operational workflows.

Historical order snapshots remain authoritative during import/export: an
imported order line keeps its own sku, title and price with a nullable
`product_id`, so a migrated history stays readable even where the products no
longer exist.

**Order import fires no events by default.** Migrating five thousand historical
orders must not send five thousand confirmation emails — the events are opt-in
per request (`?fire_events=1`). This is the one place where suppressing a
durable event is correct, and it is a deliberate exception to §12 rather than
an oversight.

---

## 19. API strategy

### 19.1 Compatibility

Retain the useful Litekart-compatible overlap:

```text
/api/products
/api/products/{id}
/api/products/sku/{sku}
/api/carts
/api/carts/{cartId}
/api/carts/{cartId}/line-items
/api/checkout/{code}
/api/orders/{number}
/api/admin/...
```

Where GoCommerce needs a better domain shape, add a clean endpoint rather than
bending the data model to emulate legacy behavior.

### 19.2 Variants API

Example surface:

```text
GET  /api/products/{id}/variants
GET  /api/variants/{id}
GET  /api/products/sku/{sku}
POST /api/admin/products/{id}/variants
PATCH /api/admin/variants/{id}
DELETE /api/admin/variants/{id}
```

The exact route set is frozen after the first integration implementation, not
before.

### 19.3 Idempotency

Mutating checkout requests require:

```text
Idempotency-Key: <opaque-client-key>
```

The key is scoped to the relevant operation and persisted in PostgreSQL.

Same key + same request returns the original result.

Same key + conflicting request returns a validation error.

### 19.4 API response envelope

Maintain:

```json
{"data": ...}
```

and:

```json
{"error": {"code": "...", "message": "..."}}
```

Every list endpoint paginates.

### 19.5 OpenAPI

Core serves:

```text
GET /doc
GET /docs
```

The OpenAPI contract is generated or assembled from a maintained source of
truth and validated in CI.

The important rule is not the implementation technique; it is that the served
spec cannot silently drift from the actual routes.

---

## 20. Authentication and administration

Core accepts **two kinds of admin credential**, because scripts and people want
different things. Both arrive as `Authorization: Bearer <x>` and both satisfy
the same middleware, so no handler has to know which it got.

- **Static tokens** — `Config.AdminTokens` is a slice, not a string,
  specifically so a token can be rotated without downtime: add the new one,
  deploy, retire the old. They have no session and no expiry, which is right
  for CI and curl and wrong for a browser.
- **Superuser sessions** — an operator signs in with an email and a password
  and gets a token that expires. Passwords are PBKDF2-HMAC-SHA256 from the
  standard library (600k iterations, self-describing hash so the cost can be
  raised without a migration); session tokens are stored only as a SHA-256
  hash. The login endpoint returns the same error and takes the same time for
  a wrong password and an unknown account, and throttles on two counters —
  per (account, address) and per address — so neither lockout-by-proxy nor
  password spraying is free.

`superusers` is an **operator** table. Nothing in the commerce path reads it,
so guest checkout (D22) is untouched by its existence.

```go
Config.AdminAuth
```

remains the seam for replacing the whole scheme with:

- sessions;
- JWT/OIDC;
- RBAC;
- customer accounts;
- organization membership.

A future identity module owns identity. Core continues to own commerce state.

`ext/identity` is that module for shoppers: email-and-password accounts,
bearer sessions, a saved address book, and an order history built by *claim* —
an order joins an account when the client presents the order's access token,
never because an email matched. It owns `identity_*` tables, writes no core
table, and D22 holds: checkout never asks for it. Operator identity
(`superusers`) is unchanged and separate.

---

## 21. MCP / agent strategy

MCP is a strategic feature, but it is not allowed to duplicate the domain.

The MCP module exposes controlled tools over the core services.

Initial tools:

```text
list_products
get_product
update_product
list_low_stock_variants
list_orders
get_order
mark_paid
cancel_order
create_fulfillment
ship_order
```

Potential later tools:

```text
create_product
update_variant_inventory
create_discount
create_purchase_recommendation
reconcile_orders
```

The principle is:

```text
AI agent
   ↓
   MCP
   ↓
core service
   ↓
PostgreSQL transaction
   ↓
outbox
```

The agent never receives direct database access.

This becomes a meaningful differentiator because the same state machine is
usable from humans, applications and controlled AI agents.

---

## 22. Repository layout

As built:

```text
gocommerce/                      # ONE Go module: github.com/misiki/gocommerce
├── go.mod                       # pgx is the only production dependency
├── PLAN.md  README.md  AGENTS.md  CLAUDE.md
├── core/                        # the engine (D25): import ".../core", package gocommerce
│   ├── gocommerce.go            # App, Config, New, ListenAndServe
│   ├── module.go  ports.go
│   ├── events.go  outbox.go
│   ├── db.go  migrate.go  schema.go
│   ├── httpx.go  openapi.go  openapi.json
│   ├── i18n.go                  # language negotiation + Translator application (D21)
│   ├── catalog.go  catalog_http.go  # products, options AND variants: one service
│   ├── inventory.go
│   ├── cart.go  checkout.go  orders.go  commerce_http.go
│   ├── payments.go  fulfillment.go  notify.go
│   ├── superusers.go  superusers_http.go  # operator identity (email + password)
│   ├── doctor.go                # operational diagnostics → `gocommerce doctor`
│   ├── transfer.go  transfer_http.go     # CSV import/export
│   ├── types.go                 # Money, Address, Metadata
│   └── taxonomy/                # embedded category data, beside its go:embed
├── admin/                       # SvelteKit panel, embedded (admin_http.go)
├── skills/                      # procedural guides for humans and agents
├── .cursor/rules/               # editor-level guardrails
├── gctest/                      # module-author test kit
├── cmd/gocommerce/main.go       # serve | migrate | superuser | doctor | spec
├── docs/
│   ├── events.md  writing-a-module.md  operations.md  admin-panel.md
│   ├── litekart-openapi.json    # vendored api.litekart.in/doc — the D16 reference
│   └── PLAN-r2-sqlite-archived.md
├── examples/store/              # same module, no go.mod of its own
└── ext/                         # bundled extensions: ZERO third-party deps (D23)
    ├── payments-stripe/         # package stripe — REST + HMAC, no SDK
    ├── payments-razorpay/
    ├── notify-sendgrid/   notify-msg91/
    ├── fulfill-shiprocket/
    └── invoices/   cms/   mcp/
```

**Satellite repos** — the only extensions that leave, and only because they
carry a third-party dependency:

```text
gocommerce-redis/     queue-redis (Redis client)
gocommerce-search/    search-meilisearch / search-typesense
gocommerce-storage/   storage-s3 (AWS SDK)
```

MCP was expected to be one of these and is not: rather than take the official
Go SDK, it speaks JSON-RPC 2.0 over `encoding/json` in about as much code as
the SDK's wiring would have been. It therefore satisfies D23 and lives in
`ext/mcp` — a useful datapoint for how often a protocol actually requires its
reference implementation.

Nothing about the extension mechanism changes when a package moves out — a
`Module` is a `Module`. A CI check asserts core's `go.mod` holds only the
Postgres driver and that nothing under `ext/` imports a third-party package:
D23's rule, enforced rather than remembered.

---

## 23. Configuration

The configuration surface should stay intentionally small.

Conceptually:

```go
type Config struct {
    DBURL           string
    Addr            string
    Currency        string   // default "USD" (D14)
    DefaultLanguage string   // default "en"  (D21)
    Languages       []string // default ["en"]
    AdminTokens     []string
    AdminAuth       func(http.Handler) http.Handler
    HandlerTimeout  time.Duration
    CartTTL         time.Duration
    OrderTTL        time.Duration
    OrderPrefix     string
    OutboxBatchSize int
    OutboxPoll      time.Duration
    Dev             bool
}
```

Do not turn configuration into a second programming language.

Provider configuration belongs to provider modules.

---

## 24. Operations

### Background work

Three loops run inside the process, all started as ordinary lifecycle hooks so
they stop with it:

- **The outbox dispatcher** — claims batches with `FOR UPDATE SKIP LOCKED`,
  publishes, backs off exponentially on failure, and dead-letters after
  repeated attempts. This is the one whose silence is dangerous, which is why
  `gocommerce doctor` reports its backlog age.
- **The cart sweeper** — deletes carts past their TTL. `POST /api/carts` is
  unauthenticated, so without this it is an unbounded-growth vector.
- **The reservation sweeper** — cancels orders still pending, with payment
  pending or recorded as failed, past `reservation_expires_at`, and releases
  the stock they were holding. A declined card holds exactly the stock an
  abandoned redirect does, and is the one nobody is coming back for. An
  abandoned gateway redirect must not hold inventory forever, and a store
  silently "out of stock" with full shelves is a hard failure to diagnose
  from the outside.

Running several application nodes is safe: the dispatcher's claim query and the
migration advisory lock are what make concurrency the database's problem rather
than the operator's.

### Topology

The production architecture is:

```text
                 Internet
                    │
               reverse proxy
                    │
          ┌─────────┴─────────┐
          │ one or more Go    │
          │ application nodes │
          └─────────┬─────────┘
                    │
               PostgreSQL
```

Optional later:

```text
                  PostgreSQL
                 /           \
          primary             read replica
              │
       application nodes
```

Redis is optional.

It is introduced only when an external queue is operationally useful. The
commerce engine must remain correct without Redis.

### Backups

PostgreSQL backup/restore documentation is part of the initial production
release.

Later:

- point-in-time recovery;
- managed Postgres;
- continuous backup;
- replica promotion.

The operational philosophy is:

> **Start with one database and one binary. Scale components only when traffic or reliability requirements justify them.**

---

## 25. Reliability requirements

These are now first-class proof requirements rather than documentation notes.

### Must pass

1. Two concurrent checkouts cannot oversell one variant.
2. Reusing an idempotency key cannot create a second order.
3. A payment webhook replay cannot double-settle an order.
4. A transaction rollback creates no durable business event.
5. A committed transaction always leaves its outbox event recoverable.
6. A dispatcher crash produces at-least-once delivery, not event loss.
7. Duplicate event delivery does not create duplicate invoices or notifications
   in idempotent consumers.
8. Cancelling an eligible order restores/resolves inventory correctly.
9. Historical order lines remain correct after product/variant edits.
10. Variant combinations cannot duplicate the same option selection.
11. OpenAPI contains every public/admin core route.
12. A store can run with zero optional modules.
13. Adding Stripe does not change core order/payment code.
14. Replacing the sync dispatcher with Redis does not alter the purchase path.

---

## 26. Testing strategy

### Unit tests

Focus on:

- state transitions;
- price calculations;
- variant combination validation;
- inventory rules;
- idempotency semantics;
- event creation.

### Integration tests

Use a real PostgreSQL test instance rather than pretending PostgreSQL locking
behavior is equivalent to an in-memory mock.

Test:

```text
checkout concurrency
payment webhook replay
outbox retry
multiple application workers
variant imports
order cancellation
refunds
```

### End-to-end purchase-path test

The canonical test should be:

```text
create product
create variants
add variant to cart
checkout
payment initiate
payment webhook
order paid
notification emitted
fulfillment created
shipped
outbox drained
```

The same order state transitions must be executable from the API and MCP.

---

## 27. Module SDK

`gctest` remains useful, but its goal changes from testing a SQLite application
to making third-party module development easy against PostgreSQL.

Desired helpers:

```go
gctest.New(t, cfg, mods...)
gctest.AdminRequest(...)
gctest.CreateProduct(...)
gctest.CreateVariant(...)
gctest.WaitForOutbox(...)
gctest.RecordingNotifier(...)
```

Module authors should not need to understand the entire engine to write a
payment provider.

---

## 28. Initial module set

| Module | Priority | Purpose |
|---|---:|---|
| `payments-stripe` | P0 | First real online payment proof. |
| `notify-sendgrid` | P0 | First real notification consumer. |
| `fulfill-shiprocket` | P1 | Strong India deployment story. |
| `payments-razorpay` | P1 | Important India payment option. |
| `search-meilisearch` | P1 | Real catalog search without polluting core. |
| `mcp` | P1 | Strategic AI/agent differentiation. |
| `queue-redis` | P2 | External asynchronous transport when needed. |
| `invoices` | P2 | Accounting/transaction document extension. |
| `storage-s3` | P2 | Real media/object storage. |
| `cms` | P3 | Optional content layer. |
| `i18n` | P3 | Content translations via the `Translator` port (D21). |

---

## 29. Release strategy

Do not wait until the entire ecosystem exists.

### v0.1 — real commerce engine

Ship:

- PostgreSQL;
- products;
- variants;
- inventory;
- carts;
- checkout;
- orders;
- COD;
- manual fulfillment;
- transactional outbox;
- REST API;
- OpenAPI;
- CSV import/export;
- backup/operations docs;
- strong test suite.

This is the first public release.

### v0.2 — real integrations

Ship:

- Stripe;
- email notifier;
- refunds;
- webhook idempotency;
- module author test kit;
- example store.

### v0.3 — production extensions

Ship:

- Shiprocket;
- Razorpay;
- Meilisearch;
- richer inventory metadata;
- multi-instance deployment proof.

### v0.4 — agent-ready commerce

Ship:

- MCP;
- agent integration examples;
- safe mutation tools;
- audit logging for agent actions.

### v0.5 — ecosystem hardening

Ship only extensions demanded by users:

- tax;
- discounts;
- shipping rates;
- object storage;
- customer accounts;
- additional payment/carrier providers.

Do not build a speculative feature catalogue.

---

## 30. What should NOT enter core

**The membership test**, which generates the list below rather than being
justified by it:

> *Can a cash-on-delivery store, selling in one country, in one currency,
> complete a sale without this?*

If yes, it is not core. The test is deliberately harsh — it excludes things
most shops eventually want — because the alternative is a kernel that grows by
plausible increments until nobody can hold it in their head. Everything it
excludes is still buildable; it just builds as a module, against the same
exported API a third party would use.

Applied honestly it also rules *in*: variants pass (a product with two sizes is
one sale), the outbox passes (an order that silently fails to notify is a
broken sale), and guest checkout passes by definition.

Keep these outside unless evidence shows they fundamentally change the
commerce state machine:

- Stripe/Razorpay SDKs;
- carrier APIs;
- email/SMS vendor SDKs;
- Redis clients;
- search engines;
- object storage;
- CMS;
- invoice rendering;
- customer identity providers;
- tax providers;
- advanced promotions;
- recommendation engines;
- analytics (third-party SDKs — the store's own aggregates over its own orders are core's, see D45);
- AI models;
- storefront UI.

Core owns the commerce truth. Modules own external capabilities.

---

## 31. Strategy for Litekart

Litekart compatibility remains one of the strongest tactical advantages for
this project.

The recommended relationship is:

```text
                 Litekart
                    │
          existing storefronts
                    │
                    ▼
             Litekart API shape
                    │
             ┌──────┴──────┐
             │             │
          existing      gocommerce
          backend         engine
```

Do not require Litekart to disappear.

Instead:

1. make GoCommerce implement the useful overlap of the Litekart API;
2. make Svelte-Commerce able to target GoCommerce with minimal changes;
3. use existing Litekart users as early technical adopters;
4. let GoCommerce become a separate product with its own identity.

This gives the project an unusually practical distribution path.

### Strategic advantage

You are not starting with:

```text
new engine → find users
```

You can start with:

```text
existing ecosystem → new engine → migration/compatibility story
```

That materially reduces the cold-start problem.

---

## 32. Go-to-market strategy

### Phase 1 — developer credibility

The objective is not revenue.

The objective is proof that developers understand the thesis.

Publish:

- architecture write-up;
- benchmark methodology;
- "why not a huge ecommerce framework" article;
- variant model explanation;
- transactional outbox explanation;
- "build a store in Go" tutorial;
- Litekart migration example;
- MCP demo.

### Phase 2 — reference store

Create one excellent reference implementation:

```text
Go
Postgres
gocommerce
Svelte-Commerce
Stripe
Shiprocket
SendGrid
MCP
```

It should be deployable without proprietary infrastructure.

### Phase 3 — migration story

Make it easy to answer:

> "I already have products/orders/customers — how do I get onto this?"

CSV import is useful here, but migration tooling should eventually include
provider-specific import adapters.

### Phase 4 — ecosystem

Invite Go developers to publish modules.

The project wins when the module directory begins to answer:

> "Can it integrate with my stack?"

without core changes.

---

## 33. Distribution and repository strategy

The GitHub repository should make the thesis obvious in the first screen.

README structure:

```text
hero
↓
what it is
↓
30-second architecture
↓
minimal example
↓
variant-aware commerce example
↓
module example
↓
transactional outbox explanation
↓
MCP example
↓
Litekart compatibility
↓
production deployment
↓
roadmap
```

The README should not start with a giant feature matrix.

Lead with the design decision:

> **Commerce primitives in Go, without a commerce monolith.**

---

## 34. Competitive positioning

The comparison should focus on architecture rather than pretending all
products solve exactly the same problem.

| Approach | Strength | GoCommerce position |
|---|---|---|
| Shopify | Excellent hosted merchant experience | Not competing on hosted SaaS. |
| WooCommerce | Huge ecosystem | Smaller, code-first, headless-friendly. |
| Medusa/Vendure | Mature composable commerce | Smaller Go-native engine with explicit composition. |
| Custom Go app | Maximum control | GoCommerce removes repeated commerce plumbing. |
| Large microservice stack | Scale/flexibility | GoCommerce keeps the first deployment much smaller. |
| SQLite embedded store | Operational simplicity | GoCommerce chooses Postgres for production correctness/concurrency. |

The claim is not that GoCommerce is universally better.

The claim is:

> **For Go teams that want ownership without rebuilding commerce primitives, it is a strong middle ground between a giant commerce platform and a custom implementation.**

---

## 35. Economic/product strategy

The open-source engine should remain useful by itself.

Potential commercial layers later:

```text
Open-source engine
       │
       ├── paid modules
       ├── migration services
       ├── hosted control plane
       ├── enterprise support
       └── managed deployment
```

Do not introduce a license that damages early developer adoption unless there is
a concrete reason.

License decision remains open between MIT and Apache-2.0.

---

## 36. Success metrics

Do not judge the project by GitHub stars alone.

### Technical

- successful concurrent checkout proof;
- no lost outbox events;
- zero duplicate settlement on webhook replay;
- migration/import correctness;
- predictable recovery after process crash;
- API compatibility coverage.

### Adoption

Within the first public iterations, look for:

```text
10 real stores/apps trying it
3 external module contributors
2 production deployments not controlled by the author
1 meaningful migration from an existing commerce backend
```

Those signals matter more than 10,000 stars from developers who never deploy it.

### Product-market signal

The strongest signal is:

> A developer chooses GoCommerce for a real project without being asked to.

---

## 37. What success looks like in 12 months

A successful first year does **not** require GoCommerce to replace Shopify.

A successful outcome looks like:

```text
                gocommerce
                    │
      ┌─────────────┼──────────────┐
      │             │              │
   small stores   B2B apps    custom storefronts
      │             │              │
      └─────────────┼──────────────┘
                    │
              module ecosystem
                    │
          payments / search / AI
```

The project becomes known among Go developers as the place to start when they
need commerce primitives without adopting an enormous platform.

---

## 38. Milestones

Each milestone must produce something usable.

### M0 — PostgreSQL kernel

- repository;
- PostgreSQL connection/lifecycle;
- migrations;
- `App`, `Config`, `Module`;
- request middleware;
- health/readiness;
- admin auth;
- OpenAPI skeleton;
- CI on Linux + Windows;
- integration-test PostgreSQL service.

**Proof:** clean build, database migration, health endpoints, valid OpenAPI.

### M1 — Products + variants

- products;
- options;
- option values;
- variants;
- SKU uniqueness;
- variant combination uniqueness;
- admin CRUD;
- public catalog;
- CSV import/export.

**Proof:** a product can be created and sold as both a simple single-variant
product and a multi-variant product.

### M2 — Inventory + cart

- variant inventory;
- reservation model;
- guest cart;
- line-item CRUD;
- TTL;
- price snapshots.

**Proof:** concurrent cart/stock behavior is deterministic.

### M3 — Sell

- orders;
- order snapshots;
- checkout;
- idempotency;
- COD;
- domain events;
- transactional outbox;
- outbox dispatcher.

**Proof:** commit + crash simulation never loses an event; concurrent checkout
never oversells.

### M4 — Operate

- manual fulfillment;
- deliver/cancel/mark-paid;
- backup/restore docs;
- order CSV import/export;
- observability;
- `gctest`;
- reference store.

**Release:** `v0.1.0`.

### M5 — Real payments

- Stripe module;
- webhook validation;
- webhook deduplication;
- refunds;
- email notifier;
- event retry/dead-letter behavior.

**Release:** `v0.2.0`.

### M6 — Production extensions

- Razorpay;
- Shiprocket;
- Meilisearch;
- multi-instance test;
- outbox throughput test.

**Release:** `v0.3.0`.

### M7 — AI-native commerce

- MCP module;
- read tools;
- safe mutation tools;
- agent audit records;
- scripted end-to-end agent flow.

**Release:** `v0.4.0`.

### M8+ — user-driven extensions

Only then evaluate:

- customer accounts;
- discounts;
- tax;
- shipping rates;
- media;
- additional queues;
- additional carriers;
- additional search engines;
- SQLite compatibility.

---

## 39. Deferred decisions

These should not block M0–M4:

1. customer identity model;
2. tax engine;
3. discount engine;
4. shipping-rate calculation;
5. multi-currency;
6. advanced promotion rules;
7. product bundles/kits;
8. subscriptions;
9. multi-tenant mode;
10. marketplace/vendor settlements;
11. SQLite compatibility;
12. Kafka transport.

They become priorities only when a real use case requires them.

---

## 39a. Amendment: media is stored, not only linked

R3 said image storage stays out of core — URLs only, with object storage as a
satellite module (§30). That is now amended, deliberately and with the cost
stated.

**Why it changed.** "Paste a URL" presumes the operator already runs somewhere
to host files. A small store does not, and telling someone to go set up a
bucket before they can add a photograph to a product is the kind of gap that
makes an admin panel feel unfinished rather than principled.

**What kept the original intent.** Core owns the *records* and a two-method
interface, never a storage SDK:

```go
type MediaStore interface {
    Put(ctx, filename, contentType string, r io.Reader) (key, url string, err error)
    Delete(ctx, key string) error
}
```

There is one implementation in core — the local filesystem, behind
`Config.MediaDir` — and `Config.MediaStore` replaces it wholesale. S3, GCS and
a CDN remain modules, and D23's rule that core carries one production
dependency is untouched. A store that sets neither still runs: the library
records media by URL and the panel says why upload is unavailable rather than
offering a button that fails.

**The costs, eyes open.** A directory that must be backed up alongside the
database and can fill a disk; a file-serving path that must never become a
traversal or a stored-XSS vector (hence: random flat keys, an extension
allow-list, `X-Content-Type-Options: nosniff`, and a handler that resolves
exactly one file in exactly one directory rather than using `http.FileServer`);
and orphaned files when a row insert fails after a write, which is the correct
direction to fail — wasted bytes a sweep can find beat a row pointing at
nothing.

Media is a **library**, not a per-product list, because the same photograph
belongs to several products and should be stored once. `product_media` joins
them with `ON DELETE RESTRICT`, so deleting a file six products still display
is refused rather than silently stripping them.

---

## 39b. AI-native developer experience

AI is treated as a first-class developer interface, in three layers. Each layer
answers a different question, and the split matters: rules constrain, skills
teach, and MCP acts.

**Rules — what must not be broken.** `AGENTS.md` is the canonical set of
architectural guardrails; `CLAUDE.md` and `.cursor/rules/gocommerce.mdc` point
at it and add only what is tool-specific. Each rule states the reason it
exists, because a rule whose reason is unstated gets optimised away by the next
person who finds it inconvenient.

**Skills — how to do the work.** `skills/` holds task-scoped procedural
knowledge: architecture, products, variants, carts, checkout, orders,
inventory, payments, events, integrations, infrastructure and development. Each
one carries frontmatter describing when to reach for it, so a reader — human or
agent — loads the relevant page instead of the whole codebase. They document
the invariants and *why* they hold, not the API surface, which `/doc` already
describes precisely.

**MCP — safe domain tools.** `ext/mcp` exposes tools that call the same
services REST does. It never exposes arbitrary SQL and never mutates core
tables directly, so an agent operating the store is bound by exactly the
invariants a human operator is (Rules 1 and 2 below). Every tool is a wrapper
over a service method; that is the contract, and a tool that needs to reach
past it is a missing service method, not a licence.

**Diagnostics.** `gocommerce doctor` runs the operational checks an operator
actually needs at 2am — database, migrations, admin reachability, outbox
backlog and dead letters, expired stock reservations, cart sweeping, unsellable
catalog entries, provider registration, and spec-versus-reality drift. It is a
core service (`App.Diagnose`) rather than a CLI feature, so the CLI, the MCP
`store_health` tool and the panel's Settings → Diagnostics screen all render
the same report. `-json` makes it machine-readable and a non-zero exit makes it
gateable, so an agent can act on it without parsing prose.

The report is also actionable from the panel: `GET /api/admin/diagnostics`
answers 200 even when the store is unwell — the report is the answer, not the
error — and three maintenance routes behind `store.operate` run the same
sweeps and outbox drain the engine's own five-minute ticker runs, so a hint
that ends "check that background work is running" has a button beside it
instead of only a shell prompt. A check that fails because it could not *run*
records its cause separately from its finding: the CLI prints it, the HTTP
route does not, because a pgx error names a host, a port and a user.

---

## 39c. Accepted tradeoffs (eyes open)

Every one of these is a real cost, accepted knowingly. They are listed so that
nobody has to rediscover them under pressure and mistake a decision for a bug.

1. **Compile-time composition excludes non-programmers.** There is no
   plugin-install button; every store is a Go build. Go's `plugin` package does
   not work on Windows and RPC plugins are ceremony a small team cannot afford.
   The operator is a developer, by design.
2. **PostgreSQL is a hard dependency.** No embedded mode, no SQLite fallback,
   nothing runs without a server. R2 traded this away for zero-install and R3
   bought it back deliberately (§2) — the outbox, `FOR UPDATE SKIP LOCKED` and
   advisory locks are the whole concurrency story and none of them survive the
   swap.
3. **No repository interfaces.** SQL lives in the services. Moving to another
   database is surgery, not configuration. Abstracting the datastore we chose on
   purpose would be ceremony pretending to be flexibility.
4. **One flat package.** File discipline is the only internal structure, so
   everything exported to modules is exported to everyone and the API-freeze
   burden lands early. `App` has to be guarded against god-object growth —
   every accessor request gets the membership test in §30.
5. **At-least-once event delivery.** The outbox guarantees an event is never
   lost, not that it arrives once. Every handler must be idempotent, and a
   single failing handler re-runs the ones beside it that already succeeded.
6. **JSON event payloads** trade compile-time typing for bus-swappability.
   Shared payload structs and an event version are convention, not proof.
7. **Unversioned `/api/`.** Litekart compatibility means route shapes are
   effectively frozen on first release; a breaking change needs a new path, not
   a `/v2/`.
8. **Forward-only migrations.** A bad migration in production needs a
   corrective forward migration. There is no rollback, because rollback is the
   thing that does not work under pressure.
9. **Table ownership is convention.** Route and migration namespacing are
   structurally enforced; table prefixes are review-enforced. A sloppy module
   *can* write `orders`. The discipline in §40 is the guardrail, not a sandbox.
10. **Hand-maintained OpenAPI.** The spec can drift from the code. The test
    that every served route appears in `/doc`, and `gocommerce doctor`'s
    re-check at runtime, are the honesty mechanisms — module fragments remain
    the module author's burden.
11. **The admin panel owns `/`.** One binary therefore cannot also serve a
    storefront there, and the panel's base path is fixed at build time.

---

## 40. Hard architectural rules

These rules are intentionally stronger than style guidelines.

### Rule 1
A module cannot mutate core commerce tables directly.

### Rule 2
Every business state transition goes through a core service.

### Rule 3
Every durable domain event is created inside the transaction that caused it.

### Rule 4
External network calls never execute while holding a core database transaction
unless a future design proves the call is both necessary and safely bounded.

### Rule 5
Consumers must be idempotent because event delivery is at-least-once.

### Rule 6
Variants are first-class sellable entities.

### Rule 7
The database is not abstracted merely for theoretical future portability.

### Rule 8
Litekart compatibility must not distort the domain model.

### Rule 9
MCP and REST call the same domain services.

### Rule 10
Every new core feature must answer:

> **Does this belong to the commerce state machine, or can it be a module?**

If it can be a module without weakening the invariants, it stays outside core.

---

## 41. Final product thesis

The project should now be understood as:

> **GoCommerce is a small, production-grade commerce engine for Go. It gives
> developers the hard parts of commerce — variants, inventory, carts,
> checkout, orders, payments, fulfillment and durable events — without forcing
> them into a monolithic platform. PostgreSQL provides the transactional spine;
> explicit Go modules provide the extension model; OpenAPI provides the public
> contract; MCP makes the same commerce engine operable by AI agents.**

The project wins by being **small enough to understand, complete enough to use,
and extensible enough to keep.**

That is the standard every architectural decision should be measured against.
