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
                 ├──────── payments-paddle
                 ├──────── payments-adyen
                 ├──────── payments-hyperswitch
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
| D24 | Role rights | **Fixed roles, configurable sets, twenty-one rights.** The roles stay `owner`, `manager`, `staff`; what `manager` and `staff` may do is the store's to re-cut, stored in `role_rights` as the departure from the engine's defaults (a role with no rows keeps tracking them). Owner is not storable and always carries every right. Every role keeps `catalog.read`. Rights resolve on each authentication, never from a cached copy. The catalogue is one right per area a person could be kept out of — catalog, inventory, discounts, taxes, locations, orders (read/write/fulfill/refund), customers, team (read/write), roles, data (export/import), the store itself (`store.operate`) — replacing an eight-right first cut in which `settings.write` alone covered the team, the roles matrix, the locations, the tax rates and the export of the whole database. This renames `RightsOf`/`Can` to `DefaultRightsOf`/`DefaultCan` and adds `(*Superuser).Has` — a breaking change to the exported API, taken deliberately. **Amended — `store.operate`.** The twenty-first right is the store as a running system rather than as a shop: the health report, the maintenance passes that act on it (the cart sweep, the unpaid-order sweep, the outbox drain), the outbox and its dead letters, and the record of what every operator did. It is appended last in `AllRights`, so no existing row moves in the roles matrix, and **owner alone carries it by default** — a store that has never re-cut a role is unchanged by its arrival. It is not `settings.write` returning under another name: it changes no configuration, no product and no price, and the sweeps cancel only the orders the five-minute ticker was going to cancel anyway, through the same service methods. What it does **not** carry is database-level error text: a check that fails because it could not run records its cause separately from its finding and the admin route strips it, so the reach of this right stops at the same line `httpx.go` draws for every other response. | D19 left the seam open for RBAC and rights.go was already a map so that the sets could move into a table without moving the decisions. Custom *roles* were declined: a role name is in the `superusers` CHECK, in every picker and in every invitation, and a store that wants "warehouse" almost always wants "staff, plus stock" — which re-cutting gives it. Resolving per request rather than caching means a withdrawn right is gone on the next call, with nobody signed out and no second server holding a stale copy of who may refund. `store.operate` is its own right because none of the twenty fit: `team.read` would file the store's health under who may see the staff list, `orders.write` would hand the unpaid sweep to every staff member while leaving the outbox to nobody, and splitting four surfaces across three ill-fitting rights is precisely how `settings.write` happened. Owner alone rather than owner and manager, because it is the widest thing in the catalogue: `ext/mcp` gates its single dispatch endpoint on it, the event list pages through every buyer's email, phone and line items, and `DELETE /api/admin/x/identity/customers/{id}` pairs it with `customers.read`, which manager already carries — so a manager default would quietly grant account erasure. Running the store is not merchandising, and a store that wants a manager on call re-cuts the role, which is what configurable sets are for. Adding a right widens nobody: `roleRights` enumerates manager and staff explicitly and resolution intersects with `AllRights` on the way out. (Custom roles superseded by D80 — a store now makes its own; owner stays fixed.) |
| D25 | Engine location | **The engine moves from the repo root to `core/`: import path `github.com/itswadesh/gocommerce/core`, package clause still `gocommerce`.** A clean break, no facade and no aliases: the old import path stops existing and all 17 in-repo import sites were rewritten in the same commit; an external importer (none known) fixes it with the same one-line edit, since the package identifier and every exported symbol are unchanged. go.mod stays at the root with the module path unchanged, so one module (D23) and one engine package (D6) stand; only D23's "core at the root" clause is superseded. `openapi.json` and `taxonomy/` moved beside the `go:embed` directives that read them. `check-docs.ps1` and CI now fail if a `.go` file reappears at the repo root, so the layout is enforced rather than remembered. Docs keep citing engine files by bare name (`checkout.go`) — there is exactly one package they can mean. | The root had grown to ~94 entries and the engine had no directory of its own. A facade at the old path would be a permanent second name for every exported symbol and a standing drift surface — machinery protecting importers that do not exist, when D24 already establishes that a recorded pre-1.0 break is acceptable. `core/` rather than `gocommerce/` because `.gitignore`'s `/gocommerce` and `.dockerignore`'s `gocommerce` both match the binary and would have swallowed the directory, and `gocommerce/gocommerce` stutters; keeping the package clause avoids touching 74 package clauses and every `gocommerce.X` reference in cmd, ext, gctest, examples and the tutorial. Splits stay out of this change: a leaf is cut only when it borrows nothing unexported and no interface is invented to break a cycle (S3) — `carriers.go` (stdlib-only) qualifies today as a follow-up; `taxonomy` and `options` (6 and 3 borrowed internals) do not yet, and a future taxonomy package would subsume `core/taxonomy/`'s data files, editing the embed directives then. |
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
| D55 | The panel's controls | **The controls move to the design system too, one control at a time, by detaching rather than overriding — reversing D53's "PocketBase keeps the controls".** A control that moves stops carrying PocketBase's class and is styled outright in `gocommerce.css` against the design tokens; `admin/src/lib/styles/*.css` stay verbatim either way. The select menu is the first: the eight pickers that share `.select-option` no longer pair it with `.dropdown-item`, and the popover they open is matched with `.dropdown:has(.select-option)` rather than a class added to each of the eight. Rows step 32px instead of 41, and the chosen row is marked with a tick rather than a fill. Everything not yet moved — buttons, text inputs, the side drawer — is still PocketBase's and still reused as-is. | D53 froze the controls to keep the migration screen-by-screen, and the freeze held until a control could not be fixed from outside. Two things made the menu unfixable rather than merely awkward, and both generalise. `dropdown.css` nests `.dropdown-item` inside `.dropdown`, so its 10px/12px padding carries two classes of specificity and every single-class override we wrote lost **silently** — the last attempt made the rows taller and read as a mystery rather than a defeat. And `vars.css` re-declares its entire token block on `:root, .dropdown, .base-surface`, so a popover inherits none of the tokens the screen around it changed: inside one, `--fontSize` was 14px and `--btnHeight` 45px however compact the page had become, which is why the menus never shrank when the panel did and why the filter box in a long list stood a head above the rows it filtered. Overriding a control you do not own means re-deriving its cascade every time upstream moves, which is the cost D53 was trying to avoid and had merely relocated. Detaching is cheaper and reversible: the sheet is untouched, so taking an upstream change is still a re-copy, and a control can be moved back by putting its class back. One at a time rather than wholesale for D53's own reason — thirty-five screens still render through these sheets, and a control restyled everywhere at once is a change that either works or does not. `:has()` rather than a class on each popover because eight edits are eight things that fall out of step. |
| D56 | CSV in Shopify's layout | **Shopify's CSV layouts are a second dialect of the store's own transfer model, not a second importer.** Every export (`admin-products`, `admin-orders`, `admin-customers`, and the new `admin-inventory`) takes `format=gocommerce|shopify`; every import reads the dialect off the header (a Handle, a Name beside Lineitem columns, an On hand) and `?format=` only overrides. A Shopify row is translated into the store's own columns before the one importer runs — `shopifyProductReader` and `shopifyOrderReader` in `transfer_shopify*.go` — and a Shopify file is written by a writer over the same `exportVariant`/`exportOrderLine` rows the native one uses. The native product file learned the columns Shopify's has (vendor, product_type, tags, category, seo_title, seo_description, cost_minor, taxable, weight_unit) plus pictures as URLs (`images`, `image_alts`, `variant_images`), so nothing is lost choosing either. Two rules changed with it: **a column the file does not have says nothing about that field** (a price list no longer strips barcodes, options or customs codes — the variant upsert became a select-then-update of the columns present), and a product import takes `overwrite=false` to leave existing slugs alone. Pictures are linked by URL, never downloaded; a URL the library holds (by URL, or by the site-relative path an absolute export URL ends in) is reused. Customers still have no import. | A store moving from Shopify has exactly one file in hand, and every tool an operator already uses reads that layout; asking them to rewrite headers is asking them not to migrate. Translating rows rather than duplicating the importer keeps the stock rules (reserved floor, closed locations, the movement ledger) in one place, which is the whole reason the product importer is a thousand lines. Decimal prices exist only inside the file (rule 6 holds for the API), with a currency exponent table in `transfer_shopify.go` because the file needs it and nothing else does. Absent-column-says-nothing had to come with the second dialect: Shopify writes the product's columns on the first row only, and blanks on every later row that reset fields would have wrecked every multi-variant product on import. Linking pictures rather than downloading them keeps core free of an HTTP client with a policy (size caps, timeouts, retries) that belongs to a module, and matches what `MediaLibrary.AddURL` already promises. |
| D57 | Plugins | **A plugin is a switch and its settings, held by the engine — never code that arrives at runtime.** `PluginDef` is a descriptor (key, title, category, fields with a kind, a public flag and a default); `plugins` (M38) holds one row per key with `enabled` and a settings JSON, and no row means the descriptor's default. Core ships the descriptors for storefront features that are nothing but settings (hello bar, store closed, enquiry mode, guest checkout, wishlist, trust badges, social sharing, WhatsApp, header scripts, GA/GTM/Ads, Meta pixel, Tidio, tawk.to, Umami, Plausible, Trustpilot, news ticker); a module registers its own with `app.RegisterPlugin` and reads `Plugins().Enabled` and `Plugins().Settings` back at runtime, so meilisearch, klaviyo, feeds and sitemaps are configured from the panel with the environment as the fallback. `GET /api/plugins` hands a storefront every enabled plugin's public settings; `GET/PATCH /api/admin/plugins` (store.operate) is the Plugins screen, in the main nav, with secrets masked on every read and kept when the mask is sent back; every change is audited as `plugin.update`. | The request was a marketplace where features install from the UI. Downloadable code is a different product with a different security story — dynamic loading in Go is fragile and a store running strangers' code is a store that cannot be reasoned about — and every real plugin a store of this kind wants (Litekart's list of fifty-three was the reference) is either a setting a storefront renders or an integration this repository can ship compiled in. So the marketplace's *experience* is kept — a list, a switch, a settings form, an install that is a click — and its risk is not: the list is what this binary can do. Settings live in the engine rather than in the storefront because the storefront is whatever the store built and there may be several; one row the panel edits and every storefront reads is the shape channels already have. Modules keep a Config for the environment-first store, and the screen wins where both say something, because a value an operator typed today is newer than one in a deployment file. |
| D58 | Notification templates | **The wording of what the store sends is the store's, held by the engine — never a notifier module's.** `NotifyTemplate` is one message on one channel: its event, a title and description for the screen, a subject and a body as Go `text/template` over the event's flat string data, and the keys the event carries. Core registers the engine's own messages at boot (the order events on email and SMS, the operator password reset); a module registers the events it introduces with `app.RegisterNotifyTemplate` (identity's password reset, contact's announcement), and a key already taken is a registration error. `notification_templates` (M40) holds only what an operator changed, keyed `(channel, event)`; no row means the default and a delete restores it. A notifier asks `NotifyTemplates().Get` for the effective wording at send time, so what the panel shows is what goes out; a module's `Config` overrides apply only where the operator has not spoken. `GET/PUT/DELETE /api/admin/notifications/templates` (store.operate) is Notifications › Setup Email / Setup SMS, audited as `notification_template.update`. Alongside it, `ConfigurableNotifier` lets a backend whose key comes from the Plugins screen say whether it is ready, and the funnel, the settings and the doctor treat an unready backend as absent — so SendGrid and MSG91 install idle and start sending the moment the key is typed in, with no restart. | Until now every notifier carried its own subjects and bodies, and changing "Thanks for your order" meant a Go map and a redeploy — for a store whose owner is the person who wants to change it. The words are not the transport's: the same shipping notice goes out whether SendGrid or Postmark carries it, so a second module would have meant a second copy of every template. Litekart's admin, the reference, puts providers and templates on one screen under Notifications; this is that screen with the engine holding the words. Defaults stay in code so the editor can never lose them, and a parse at save time refuses a typo before the next sale finds it. |
| D59 | Providers install idle | **A gateway or carrier module registers whatever Config it was given, and says whether it is ready.** `Configurable` — `Configured(ctx) bool` — is the interface a provider implements when its credentials can come from the Plugins screen; the engine asks it before every pick and treats an unready provider as absent: `Payments.Methods()` and `GET /api/checkout` leave it out, `Checkout` refuses its code with the same words an unknown code gets, `Fulfillments.Create` refuses it with a pointer to Settings › Shipping providers, refunds through it are refused, and `ProviderInfo.Configured` on the settings tells the two provider screens which cards are live. Every one of the eight gateway and eleven carrier modules registers a plugin (category `payments` or `shipping`) whose fields mirror its Config by `plugin:"…"` tag; `Plugins.Fill` lays the stored settings over a Config by those tags, and the module re-reads the effective Config before each use through an atomic snapshot, so a key typed a moment ago counts without a restart. Config stays the environment's fallback and a store configured in code starts switched on. `-gateways` and `-carriers` install every module idle. | The panel had grown a Payment Methods and a Shipping Providers screen in Litekart's image — every provider the build knows, each with Activate — and the modules could not honour it: each demanded its keys at Register and refused to boot without them, so "which providers can this store use" was answered by whoever wrote main(). Installing idle needs the engine to know installed from ready, because a gateway offered at checkout with no key behind it is a checkout that cannot complete, and a carrier offered on the ship dialog with no account is an order that cannot leave. The tag-and-Fill shape keeps nineteen modules to one mechanism and one line per field. |
| D60 | FAQ and wishlists | **Two more storefront things the engine holds rather than the theme: the questions a shop is asked, and the products shoppers saved.** `ext/faq` keeps entries with a section and a hand-made position, published or draft, served grouped at `GET /x/faq` in the order the shop arranged them — never alphabetical, which would reorder a page somebody wrote deliberately — and reordered whole through `PUT /api/admin/x/faq/order`, the way the Menus screen saves a tree. `ext/wishlist` keys a list by a token the storefront holds, the way a cart is keyed: a guest saves something before there is an account to save it to, so the module needs no shopper identity and no dependency on `ext/identity`, and an email may be attached later. The admin half is the interesting one — `GET /api/admin/x/wishlist/wanted` ranks products by how many lists hold them, with what is available and how many of those lists gave an email, and `out_of_stock=1` answers the question the screen exists for. Neither admin route ever returns a token. Core's built-in `wishlist` plugin descriptor is deleted: it was a switch with nothing behind it, and the module takes the same key, so a store that had the placeholder on keeps its switch and gains the feature. | Both were asked for by name, and both are the same shape of thing: content a storefront must render and an operator must edit, which every shop writes and no shop should have to model. The wishlist's token is the load-bearing choice — attaching it to a customer account would have made the feature useless to the guests who are most of a storefront's traffic, and would have coupled one `ext/` module to another, which nothing else in the tree does. The demand signal falls out for free: a product on many lists that the store cannot sell is the one fact a shop most wants and no other screen carries. |
| D61 | Payouts is the store's ledger, not the gateway's statement | **`GET /api/admin/reports/payouts` reports what each payment method charged, what was refunded on exactly those orders, the net, and what it is still owed — from the engine's own orders, counted by the day the order was placed.** It deliberately does not fetch a gateway's settlements: the engine does not know when a payout landed, what was withheld in fees, or which orders a gateway grouped into one transfer, and inventing those numbers would be worse than not having them. A sale is defined exactly as the sales report defines one, so the two screens agree. A method that took money and has since left the binary still gets a row, marked not installed. | An operator reconciling a bank statement needs the money split by gateway, which the sales report never does because a revenue chart does not care. The honest version of that is the store's own side of the ledger with the gap named on the screen — fees and settlement lag — rather than a screen that looks like a bank statement and quietly is not one. Fetching real settlements is a per-gateway job with a per-gateway API, which is a module's work (D59's shape) and not the engine's. |
| D62 | A tree travels as a path, and the escaping rule has one implementation | **Categories, reviews and menus each get a CSV pair, and the tree-shaped ones carry the trail from the root in one cell** — `Apparel / Clothing / Shirts`, `Shop / Shirts` — rather than a parent id column. The path is the identity: a row whose trail exists is updated, a row whose trail does not builds every missing step, so a second run of the same file is a no-op. **The category file deliberately cannot move or delete**, and a menu the file does not name is untouched. The three rules every file here has followed since the first one — escaping a cell that opens with `=`, `+`, `-` or `@`, matching a header case-insensitively past a byte-order mark, and letting an absent column say nothing — are now one exported reader and one exported writer in `core/csv.go` that modules use. | A parent id column cannot be kept consistent by hand in Excel, and an id from another store means nothing here; a path is legible on its own and is what the product file's `category` column already speaks. Moving is left out because one typo in a path would otherwise drag a subtree and the products filed under it somewhere else, and a spreadsheet has no undo — the screen that moves a category shows what is nested under it first. The helpers are exported because three modules hand-rolling the escape is how one of them comes to get it wrong, and a cell that runs a formula when the file is opened is a security bug, not a formatting one. |
| D63 | store.operate stopped being true about itself, so four areas left it | **Shipping zones and rates, sales channels, plugin settings and notification templates each get a read/write pair of their own**, off store.operate. That right documents itself as changing `no configuration, no product and no price` — but a shipping rate decides what a buyer pays to receive an order, a channel decides where the catalogue is sold, a plugin’s settings hold a gateway’s live API key, and a template is the wording of every message the store sends. Each had been filed under the nearest right that would take it and the sentence had quietly become false. The defaults did not move: all eight came off a right only owner held, so owner holds them and no other role gained anything on upgrade. | The cost of leaving them there was not tidiness. store.operate also carries the outbox — events as a consumer received them, so buyers’ addresses, phone numbers and every line they bought — the store-wide audit feed and ext/mcp’s dispatch endpoint. Letting somebody set delivery prices meant handing them all of that, so in practice nobody below owner was ever given any of it and the four screens were owner-only by accident rather than by decision. A store can now put a manager on delivery pricing without also opening a page of its customers’ addresses. |
| D64 | A role's key is an identifier; its name is the store's | **A store renames its roles and describes them (`PATCH /api/admin/roles/{role}`, role_profiles, M41), and the key never moves.** `owner`, `manager` and `staff` are written on every superuser row and in `role_rights`, so the string is an identifier and renaming it would orphan accounts. What changes is the display title and the description. The engine ships both, a row exists only where a store has said otherwise, and a blank field restores the engine's words — so there is no separate reset verb and improving a shipped sentence reaches every store that never edited one. All three roles are editable here, owner included. | The three roles are the store's vocabulary for its own staff, and the engine's words are guesses: "Fulfilment" reads better than "Staff" in a warehouse, and the description is where a shop writes down the rule it actually operates — who may press refund, who covers the phone at the weekend. Storing that beside the rights is what makes the Roles screen answer "what is this role" as well as "what may it do". Owner is renameable even though owner's rights are unstorable, and the two rules do not conflict: the rights rule exists so a store cannot narrow its own way back in, and a name locks nobody out of anything. |
| D65 | A module brings its own rights | **`app.RegisterRight` lets a module declare the rights its screens are gated on, and nine modules now do** — reviews, pages, menus, FAQ, contact, newsletter, wishlists, invoices and webhooks, seventeen rights between them. `AllRights` stays core's; the build's list is core's plus the modules', in registration order. A declaration names the roles that carry it by default, and the safe value is exactly the roles that held the right it was lifted off, so nobody's access moved. Owner resolves against the build rather than core, a stored grant is intersected with the build rather than core, and a right from a module this binary lacks is refused rather than stored. | rights.go's rule that nothing outside it may invent a right was written when every screen was core's, and a module shipping a screen made it false in practice rather than in principle: each borrowed the nearest right that existed, so moderating reviews required the whole catalogue and reading the newsletter list required the outbox. Worse, those screens had no row on the Roles grid at all — the grid draws rights, and a surface with no right of its own can be neither granted nor withheld. The rule survives in the form that mattered: one declaration, in the code that owns the routes. The two resolution bugs were found by the test rather than by review — filtering a stored grant against core silently saved a permission and then refused the screen. |
| D66 | Whose price a cart is charged | **A customer group's prices are read from `carts.verified_email`, which nothing public writes; `carts.email` is a contact address and never earns a group price.** `Carts.VerifyEmail` sets it, and the callers are the proof: `Orders.Create` (the operator typed the address), a module that has confirmed a signed-in account's address, and `POST /api/admin/carts/verify-email` under `groups.write` for a storefront that signs its own customers in. Vouching re-prices the basket in the same transaction; typing a different address on the public setter withdraws it and re-prices back. Existing carts start unverified (M49). | M34 keyed membership on the email because the engine has no customers table, and then read it off the only email a cart had — the one `POST /api/carts` and `PUT /api/carts/{id}/email` take from anybody. So a stranger who knew a dealer's address could type it onto an anonymous cart, see the dealer's prices in `current_price`, and check out at them under their own address, since checkout priced from the cart and shipped to the checkout's email. The cart response also answered, for anyone, whether an address was a member. Membership stays by email — that part of M34 was right, and D22 still holds — but an address only counts once something has proven it, which is the principle ext/identity already applied to order history (by claim, never by email). `groups.write` gates the admin route because adding an address to a group already grants exactly this; `orders.write` keeps the phone order at the dealer's price, as it always was. A channel-scoped list is still reachable by naming the channel, which is a storefront choice rather than an identity one and is not changed here. |
| D67 | A module's veto on a checkout | **`app.RegisterCheckoutGuard(func(ctx, *CheckoutAttempt) error)` runs inside phase A, after every line is re-priced and reserved and the total is final, and before the order number is drawn.** The attempt carries the method, the caller's input, the cart's verified address (D66), the lines at the prices they will be charged, every figure, whether `Orders.Create` placed it (`ByOperator`), and the transaction. A refusal is an `*APIError` the caller reads; any other error is the guard breaking, answered 500 without its text. A guard reads and locks with the transaction and never writes a core table with it, and it does no network I/O. | Every hook a module had ran after the order committed — a provider's `Initiate`, an event handler — so refusing meant cancelling an order that had already reserved stock and already emailed the customer. A credit limit or an approval rule enforced that way is an apology, not a control, and it races: two checkouts can both be under the limit until both have committed. Running in phase A gives the guard the exact total, rolls everything back on a refusal, and lets it take an advisory lock that serialises its own decision. Before the order number so a refused checkout burns none. Rule 5 is the guard's to keep, because it holds the cart's and every reserved variant's row lock while it runs. |
| D68 | A price somebody agreed | **`NewOrderLine.UnitPriceMinor` sets a line's price on an order placed through `Orders.Create`; it is stored as `cart_line_items.agreed_price_minor` (M50) and wins over every price list and quantity break.** Over HTTP it needs `pricing.write` on top of `orders.write`. One variant at two prices in one request is a 400. Every reader of a line's current price goes through `cartLinePriceSQL`, so an agreed line never reads as price_changed. | A quote is a price for one buyer, one basket, once, and nothing in the engine said that: a price list applies to everyone its group reaches until it is switched off, and a discount code to whoever types it. The cart a phone order builds is never handed out, so an agreed price on it reaches no token holder — which is why the setter is unexported and `Orders.Create` is the only door. `pricing.write` because an operator who may take an order is not therefore one who may set its price; otherwise anyone at the counter could sell stock at nothing. |
| D69 | Selling to businesses | **`ext/b2b`, on top of `ext/identity`: a company holds buyers (identity accounts) in one of three roles — `admin`, `approver`, `buyer` — and its own terms: a customer group for prices, a credit limit and net days, an approval threshold, and whether a PO number is required.** `on_account` is a payment method that exists only inside the module's own checkout (`Configured` reads its context). The module's checkout guard (D67) applies the terms with the final total under a per-company advisory lock; a `buyer` over the threshold gets an approval request instead of an order, which an approver places from its snapshot through `Orders.Create` at the snapshot's prices (D68). Quotes are priced by the store under `quotes.write` and placed the same way. Joining a company is by invitation to an address, and accepting it confirms that address with identity. Four rights: `companies.read`, `companies.write`, `quotes.read`, `quotes.write`. | D22 keeps companies out of core, and the engine already had the hard part — a group price per buyer — once D66 made it safe; what was missing were the controls, and they are only controls if they act before the order exists, hence D67 rather than event handlers. A company's rules reach a basket priced as its buyer wherever it is checked out, so the guard refuses a company-priced basket at the public checkout rather than letting a junior buyer route around approval. The roles are the company's, not operator rights: what a buyer may do inside their company is the company's business. Credit is read from the orders (on account, unpaid, not cancelled) through `metadata.b2b`, written in the order's own transaction and refused from anywhere else, because the module's ledger row is written after the commit. Identity sends no event when an account changes address or is deleted, so an hourly reconciler takes stale addresses out of company groups — and the buyer's own requests re-file theirs at once. Until it runs, a deleted account's address keeps the group price for whoever can prove that mailbox, which is an hour's exposure to the mailbox's own holder. Not built: multi-tenant mode (§39, item 9), per-company catalogues, shipping or tax per company, and invoices as a document separate from the order. |
| D70 | Multi-tenant mode — *superseded by D83* | **One process and one PostgreSQL database serve many stores, each an unchanged engine — a `*gocommerce.App` — in a schema of its own, routed by the request's host.** The `platform` package (and `gocommerce platform`) keeps its own `gocommerce_platform` schema of tenants and domains, gives every store `store_<slug>`, its own static admin token, media directory and module instances, and serves it at `<slug>.<base domain>`, at any custom domain attached to it, or on a shared API host by the `X-Store` header. Platform operators hold a platform token and reach `/api/platform/` on the platform's own hosts only: create a store (all or nothing), suspend, resume, delete (suspended first, slug repeated), attach domains, add an owner, rotate a store's token, and answer a TLS proxy's on-demand ask. Core gained `App.Start` and `Config.MaxOpenConns`; nothing else in core or any module changed. | §39 deferred this until a real case asked, and a supplier running a store per dealer did. The alternative was a `store_id` on every table, which makes isolation a WHERE clause in thousands of statements, now and in every module written later, where one forgotten filter shows one shop's customers to another. A schema makes the database enforce it, and the engine's own test suite is the evidence that it works: every test has always run in a schema of its own through the same `search_path` option. The costs are accepted with eyes open: a pool per store (capped at 4 by default; PgBouncer past a few hundred stores), background work per store, every store running the same compiled-in module set, and migrations applied store by store at boot. A platform token is deliberately not a store credential and a store's token opens nothing else, because two kinds of administrator is the point. |
| D71 | Dealers, and baskets a business fills in bulk | **`ext/b2b` grows four things a supplier with a trade and dealer network asked for, each built from what the engine already had rather than beside it.** A quick order (`POST /x/b2b/cart/lines`, up to 500 lines by SKU or variant id) and a repeat order (`POST /x/b2b/orders/{order_id}/reorder`) claim a basket for the buyer and add every line through `Carts.AddLine`, reporting each line that would not go in (`not_found`, `inactive`, `insufficient_stock`, `invalid`) instead of failing the request; neither places anything. A partial checkout (`line_ids` on `POST /x/b2b/checkout`) copies the chosen lines into a basket of their own — same channel, priced as the buyer, same discount code — checks that basket out through the module's own checkout, and takes the lines out of the buyer's basket only once the copy has become an order; a row per attempt in `b2b_partial_checkouts` (migration 0002) makes it the only partial checkout of that basket in flight and lets a retry under the same `Idempotency-Key` replay. Territories (`b2b_territories`: country, state, postcode prefix, the area unique so each has one dealer) route the storefront's public dealer form (`POST /x/b2b/leads`, into `b2b_leads`) to the most specific match — a territory naming a state outranks one that does not, then the longest prefix — among dealers that are not closed, and an address nobody covers stays with the store. The dealer's admins and approvers are emailed `b2b.lead_routed`; the dealer works its own leads at `/x/b2b/leads`; the store reads and reassigns them under two new rights, `leads.read` and `leads.write`. | A business orders from a spreadsheet or from last month's order, and a guarded checkout is only as useful as the ways a basket can be filled. Filling it through AddLine keeps one definition of what a line costs, so a repeat order cannot become a way to buy at a quote's expired price or a list since changed: repeating is a new order through the same limits and approvals. Checking out a copy lets the guard judge exactly the lines being bought, with no second path to an order. The record exists because the copy has a new token every time — without it a retry would be a different request to core, and a double-click would place twice, which core's row lock already prevents for a whole basket. Leads go to the most specific territory because a dealer network is drawn that way, a state dealer with city dealers inside it, and an on-hold dealer still receives because on hold stops credit, not trade. The public form answers the same 202 wherever the lead went, since naming the dealer would map the network one postcode at a time; it is bounded field by field, limited per peer address (in memory, per replica, ten a minute by default, never `X-Forwarded-For`), and mailed only to member addresses that have been confirmed, because it carries a member of the public's contact details. Leads have rights of their own rather than `companies.*` because a consumer's phone number and a dealer's credit are different things to trust somebody with (D65). |
| D72 | A state is its code | **A rule's state is stored as its code, and an address meets it by any spelling the engine knows.** `core/subdivisions.go` maps the first-level subdivisions of the United States, Canada, Australia and India — names, ISO's `US-CA` form, and India's codes superseded in 2023 — to their codes. `StateCode` is what a tax rate, a shipping zone and a dealer territory store (a zone's states are read against each of its countries, since it can name several); `StateSpellings` is what an address is matched by: its code, every name for it, and the text as written. Anywhere else a state is still matched as written, upper-cased and with its spaces closed up. Rows saved before this are not rewritten; the spellings match a rule stored as `CALIFORNIA` as surely as one stored as `CA`, and of two rules that both match only through that, the older wins by id. | A checkout address carries whatever its form sent, "California" as often as "CA", while an operator types the code — so every address that spelled its state out missed the state's rate, zone or dealer and fell to the country's, silently: the wrong tax, the wrong courier, the wrong dealer, with nothing to say it happened. A table per country was chosen over full ISO 3166-2 because the data is the decision: a few hundred names this store's markets use are reviewable in one file, and a country added is a map added. Matching every spelling, rather than a migration that rewrites stored states, keeps rows untouched by anything an operator did not do: a tax rate is audited configuration, and an UPDATE outside its service would change it with no event and no history. The panel stops telling operators to type the code, and a shipping zone's states are separated by commas, since a state can now be more than one word. |
| D73 | A console for the platform — *superseded by D83* | **The platform's operators manage stores from the admin panel's own build: the screens under `/platform`, served on the platform's hosts and signed in to with a platform token.** On a platform host, `/` redirects to `/platform`, `/platform/…` and the panel's asset files are served through core's `ServeAdminPanel` — the same files, cache rules and Content-Security-Policy a store's host sends — and every other path is still a JSON 404, never a store. The panel's root layout lets `/platform` through as it does the invitation screen, and the console brings its own shell: a token form, then a list of stores (with `GET /api/platform/health`'s running and failed-to-boot counts beside it), opening a store, and a store's page — rename, suspend and resume, attach and detach domains, add an owner, replace the admin token, and delete once suspended with the slug typed out. Generated passwords and tokens are shown once, where they were asked for. The health report gained `base_domain` so the console can show a store's platform address. The token is kept in `sessionStorage`, and the console's client (`$lib/platform.svelte.js`) is separate from the store's. Nothing else in core, the platform package or its API changed. | D70 shipped the platform as an API only, so a platform operator had curl and nothing to look at. A second app would be a second build, a second design system and a second set of controls to keep in step, so the console is the panel's own screens and components; serving them from the panel's files rather than a copy keeps one CSP and one cache rule. It is served only on platform hosts because a store's shoppers must never meet it, and a store host's /platform renders a console with nothing to talk to — the API is not there. The token lives in sessionStorage rather than localStorage, as a store session does, because it creates and deletes whole stores and never expires on its own: closing the tab is signing out, and a shared machine does not keep it. Two clients rather than one with a switch, so a platform token can never be sent to a store's API or a store session to the platform's. |
| D74 | What a company owes, over time | **`ext/b2b` keeps two records of its own beside the orders — every change to a company's terms, and the moment each order on account was paid, un-paid or cancelled — and reads a statement from them.** `b2b_company_history` (migration 0003) gets a row per term that moved — status, credit limit, net days, approval threshold, PO rule, customer group — in the transaction that moved it, with the old and new value and who: the operator on the request, `token` for a static admin token, `buyer` for a company admin, `system` for the engine; creating a company records its first terms. `b2b_account_entries` files `order.paid`, `order.unpaid` and `order.cancelled` for orders on account at the event's own `At` — the outbox row's `created_at`, the `now()` of the transaction that changed the order — keyed on order, kind and instant, so a redelivery files nothing. The hourly reconciler, and every statement before it is read, compares those entries with what core says of each order now; an order that disagrees is read back from `Orders.Timeline`, whose audit rows and events carry the same instants, and a transition with no record left is filed when it was noticed and marked `noticed`. A statement (`GET /api/admin/x/b2b/companies/{id}/statement` under `companies.read`, and `GET /x/b2b/statement` for a company's admins and approvers) runs from a civil `from` to an exclusive `to` in the store's time zone: the opening balance, each order a debit with its PO number and due date, each payment a credit, a payment taken back a debit, a cancellation of an order still owed a credit, a running balance, the closing balance, and aging by whole days past due in the store's calendar. `format=csv` writes it through core's CSV writer (D62). | Core records that an order is paid and not when, and a statement is nothing but dates, so the date had to come from somewhere, and the honest places are the two records core writes in the paying transaction itself: the event and the audit row. Subscribing reads the first; the timeline read covers a payment made before the module was installed, or an event that died in the outbox. What neither covers — an order imported already paid — has no true date anywhere, and a statement that invented one would be wrong in a way nobody could see: `noticed` says "paid by then", which is true. Owed is defined exactly as the credit check defines it, so a statement ending today closes on the figure the limit is enforced against, and reconciling before reading keeps that so while an event is still in flight. A payment and its reversal that were both missed net to nothing and appear when their events arrive. The history is written with the change rather than from an event because core announces nothing about a module's own table, and a record written afterwards is the lie rule 4 forbids. `to` is exclusive because every range in the engine is, and a client that uses two of them must not be off by a day in one. Not built: a payment against part of an order (core marks a whole order paid), and refunds, which are settled on the order and never put money back on the account. |
| D75 | What a business may buy | **A company may be held to a catalogue — categories, each with everything under it, and products one by one — and every way of filling or buying a basket keeps to it; a company with no catalogue buys everything, as before.** `b2b_catalogues` with its categories and products (migration 0004, no foreign key onto core's tables) and a nullable `catalogue_id` on the company, set like any other term and recorded in its history (D74). The store manages catalogues at `/api/admin/x/b2b/catalogues` under `companies.read` and `companies.write`; one a company is held to cannot be deleted. A category's subtree is read from core's tree as it stands at the moment of the check, walked as core walks it for a listing. The checkout guard (D67) refuses a basket holding a line outside the catalogue — `403 not_in_catalogue`, each such line in `details` — before the approval threshold, so it is never filed for an approver, and an approved basket or an accepted quote is checked again when it is placed. Quick order, a file and a repeat order leave such a line out with reason `not_in_catalogue`; a quote request, the store's pricing of a quote and its sending refuse one. `GET /x/b2b/catalogue` lists what the buyer's company may buy, each active variant priced by core's `Pricing.PriceInChannel` for the address the company's group holds for them, on the default storefront a quick order opens its basket on. The public storefront still lists every product to a signed-in company buyer: it knows nothing of companies (D22), and the guard is what holds the line. | A supplier gives a dealer of power tools the power tools, and a catalogue that only narrowed what a buyer was shown would be a suggestion: a pasted SKU, a repeat of last year's order or a quote request would reach the rest. So the line is held where the order is made, in the guard, and the ways of filling a basket keep to it only so the buyer hears at once rather than at checkout. Before the threshold, because an approver's yes to a line the company may not buy would be refused anyway. Categories with their subtrees because a range is drawn that way, and a product filed under "Power tools" next month belongs without anybody editing the catalogue; products one by one for the exceptions. Reading the tree when the check runs, rather than storing it expanded, means a moved category takes its products with it, which is what whoever moved it meant. The companies' rights rather than rights of their own (D65): what a company may buy is a term set beside what it may owe, by the same person. No foreign key onto core so a product stays deletable; a deleted one allows nothing and is shown as missing until somebody takes it out. The listing's prices are core's read rather than a copy of its rules: `PriceInChannel` resolves a price exactly as a cart line's is resolved, so the list shows what a basket would charge for one. |
| D76 | Shipping and tax per company | **A shipping rate may name a customer group, and a customer group may be tax-exempt — both read from `carts.verified_email`, the address a group price is read from (D66), so every company (D69) gets its own freight and its exemption through its group, and core learns nothing new about who a buyer is.** `shipping_rates.customer_group_id` is nullable and NULL is everybody (M51). For a cart whose verified address is in groups, each group's offer is its active rates that fit the basket, in the most specific zone covering the address in which it has one; if any group has an offer, the cart is offered the union of them **instead of** the public rates (two groups naming one method: the cheaper stands), and otherwise exactly what anybody is. A cart with no verified address only ever sees public rates, and a typed address earns nothing. Public zone choice is D52's, except that a zone whose rates all name groups is not there for anybody else, and group rates alone do not take a store off `Config.FlatShippingMinor`. Checkout re-judges the chosen rate under the cart's lock, so a cart that left its group, or never had it, cannot check out on a group rate; `NewOrderInput.ShippingRateID` lets a phone order choose, judged the same way against the typed address. `customer_groups.tax_exempt`: an address in any exempt group is charged no tax on any line. Exclusive prices add nothing; inclusive prices charge the price the cart showed and record none of it as tax. The order snapshots the group — `tax_exempt_group_id` (SET NULL), `_code`, `_name` — and a CHECK holds such an order's tax at zero; a shopper's copy keeps the name and code, not the id. Changing a group's exemption needs `taxes.write` on top of `groups.write` and is audited as `customer_group.tax_exemption`; a group rate needs only `shipping.write`. Deleting a group deletes its rates. | **Replace, not add**, because the point of a negotiated rate is that it is the one the company pays: added to the public list, a dealer is shown consumer express beside their freight and the store cannot withdraw an option from the people it negotiated a different one with. **The most specific zone where the group has a rate**, rather than the most specific zone outright, because a group's rates are exceptions written into the zones a store already has: "dealers ship free" written once on the catch-all must reach the dealer in the one state with its own courier for everybody else, and a dealer band that starts at 50000 must leave a smaller dealer order on the public rates, not on none. The zone that holds only group rates is invisible to everybody else so that the first "dealers in Karnataka" zone does not beat the country's for every consumer there and offer them nothing; a zone with no rates at all still wins, as before, because that is how a store says it does not deliver somewhere. **Inclusive prices are not stripped of their tax** for an exempt buyer, which is where this parts from Shopify: the tax inside a price depends on the delivery address, so the buyer would be charged a figure no cart ever showed them, after agreeing to the one it did — the property checkout's 409 exists to protect. The record stays true (the whole price is revenue, none of it tax), and a lower price for exempt buyers is a price list on the same group, read from the same address. Exempt rather than a group's own rate, because the request was exemption and a different rate per group is a second tax table nobody asked for. **taxes.write** because exemption decides what every future order of every member collects, which is what a tax rate decides; a manager may file customers into groups and not decide what the store collects. Membership stays `groups.write`, and that is the D66 trade-off said out loud: putting an address into an exempt group exempts it, as putting one into a priced group gives it the price, so `groups.write` on an exempt group is a tax power and the panel says so where the switch is. A group rate needs nothing beyond `shipping.write`, since it offers a group a delivery price, which that right already decides for everybody, and reaches no address the group did not already hold. **CASCADE** on a rate's group, the opposite of the order's SET NULL, because a dealer rate whose group was deleted would otherwise become everybody's free freight. Not built: an exemption with an expiry or a certificate number, a tax rate per group, and a group's own zones. |
| D77 | A portal for the trade | **A store's business buyers and dealers work from the admin panel's own build: the screens under `/portal`, served on the store's own host and signed in to with an ext/identity account.** The root layout lets `/portal` through as it does `/platform` and the invitation screen, and the portal brings its own shell, named for the store by a new public `GET /api/store` (the trading name and the buyer-facing email, phone and support URL — what the foot of every email already prints; nothing else from the profile). Its client, `$lib/trade.svelte.js`, is the third beside the store's and the platform's: it attaches the buyer's token only to `/x/` routes and refuses to call `/api/admin` at all. The session is kept in `localStorage` and refreshed with `POST /x/identity/refresh`; a 401 puts the sign-in form in place at the same address, so signing back in resumes the screen. Signed out: sign-in, forgotten and reset passwords, confirming an address, and accepting a company invitation (register or sign in as the invited address, then accept). Signed in: an overview of the company's terms, credit and what is overdue; a quick order typed, pasted from a spreadsheet or uploaded as its CSV, the refused lines kept in the form — or, from a file, listed by their row — with their reasons in plain words; the company's catalogue (D75), browsed by the categories it holds and searched, a quantity added from the row; the basket, with lines ticked for a partial checkout, a PO number, ship-to from the buyer's address book (added inline) and on account where the company has one — an order (201) and a request for approval (202) drawn as different things; orders and an order's page; repeat; the statement (D74) for admins and approvers, by period, with its aging, a date the store never recorded said as "on or before", and its CSV; approvals; quotes; the team, for company admins; leads, for the admins and approvers of a company that has territories or has been handed leads. Navigation shows only what the role and company allow. Additive buyer routes: `GET /x/b2b/orders/{order_id}` (the ledger entry and the order as its shopper is shown it), an optional `cart_id` on `POST /x/b2b/orders/{order_id}/reorder` (`ReorderInto` in Go; `Reorder` is unchanged), and `GET /x/b2b/catalogue/categories` (the categories holding something the company may buy, with what each holds at any depth); the catalogue lists only products with a variant on sale, and a variant carries `in_stock`. An invitation email opens the portal when `GOCOMMERCE_B2B_INVITE_URL` is `https://<host>/portal/invitation?token={token}`, and the reset and confirmation emails when the identity URLs point at `/portal/reset-password` and `/portal/confirm-email` — `https://{domain}/…` on a platform. | D73's reason holds for buyers too: a second app would be a second build, design system and set of controls, and the buyer's screens are the engine's B2B rules made visible, so they belong where those rules change, in this repository, rather than in a storefront kept in step from outside. On the store's own host because everything a buyer does is that store's identity session against that store's API; there is nothing to serve elsewhere. `localStorage` rather than the platform token's `sessionStorage`: that token never expires and creates and deletes whole stores, while this session expires on its own (thirty days without use), refreshes without rotating so every tab keeps working, is revoked on the server at sign-out, and can only spend inside the limits the company's credit, threshold and approvers set — and a buyer arrives from links (an invitation, an approval request), each in a new tab, which `sessionStorage` would meet with a sign-in every time. The role shapes the screens because the engine already refuses, and a button the engine will refuse is a worse answer than no button: a buyer sees no Team and no decision on a request, an approver sees what is waiting for them counted in the navigation, and Leads appear only where there is a dealer to answer them. The order route exists because a list says what an order is and not what it held, and a buyer deciding whether to repeat one needs its lines; the guest route reads an order by its access token, which the ledger never hands out, because handing it to colleagues would let them act on the order as its shopper. A repeat fills the open basket because a second basket beside the first is one the buyer cannot see. The store's name is public because the sign-in page is the first thing an invited buyer meets, and it should name the business they buy from, not the engine underneath it. The catalogue's categories are read from what the company may buy rather than from the store's tree, because a branch outside a company's catalogue is a filter that can only come back empty, and offering it advertises what the company cannot order; `in_stock` exists because `available` is 0 both for a variant sold out and for one whose stock is not counted. |
| D78 | A report runs as a role that can only read | **Custom reports run on a second connection pool that logs in as a least-privileged, read-only role — never the engine's own — and the feature fails closed when no such role is configured.** `Config.ReportsDBURL` (CLI `-reports-db`, `GOCOMMERCE_REPORTS_DATABASE_URL`) names the login; `App.reportsDB` is its pool, small and holding no idle connections, so a session advisory lock or a plain `SET` a report leaves behind dies with the connection rather than reaching the next report. `runInReadOnlyTx` uses that pool, and with it unset returns `reports_disabled` (a 4xx, so the operator sees how to turn it on) instead of falling back to `App.db`; saving and listing a report, which touch only `custom_reports`, keep using the engine's pool. The read-only transaction and the parse stay as the second and third layers. `scripts/reports-role.sql` creates the role (idempotent, injection-safe through `format(%I,%L)`), and the compose files create it on a fresh volume from `REPORTS_DB_PASSWORD`; in platform mode the platform grants the role `SELECT` on each store's schema at provision and never on the shared schema, and `gocommerce doctor` fails on a superuser reports role and warns when the feature is off or the role is the engine's own. | A report is the one place an operator's own SQL reaches the database, and a read-only transaction was being trusted to make it safe. It does not: it stops writes and DDL, but PostgreSQL's maintenance functions — `pg_terminate_backend`, `pg_create_physical_replication_slot`, `pg_read_file` — are plain `SELECT`s a read-only transaction allows, and the engine's role is a superuser in both compose files (the official image makes `POSTGRES_USER` one), so a saved report could end connections, create replication slots that fill the disk, or read files off the server. No check on the query text can close this — `SELECT` can call anything, and the catalogue of dangerous functions is not enumerable — so the barrier has to be a role the database never granted those rights to, which is a separate login because a `SET ROLE` a query can issue it can also `RESET`. Fail closed rather than fall back, because a fallback to the engine's connection is exactly the hole; a store that has not set the role up loses nothing but a feature it was not safely running anyway. A separate pool with no idle connections rather than reuse, because session state outlives a rolled-back transaction. The test that proves it asserts `is_superuser` is off and that the maintenance functions are refused, so pointing the pool back at a superuser would fail the suite. Not done: sandboxing *reading* — the role still sees every table the store has, so `reports.write` stays owner's alone (D65-style), and the panel says saving a report decides what everyone with `reports.read` may look at. |
| D79 | A report reads one store, because its login can reach one store | **In multi-tenant mode (D70) every store runs its custom reports as its OWN database login, created and granted read on only that store's schema, and a report authenticates as it — superseding D78's single shared role granted every schema.** The platform's `ReportsDBURL` becomes a template: `ensureReportsRole` (run by `boot`, before the store's migrations, idempotent) creates `<namespace>_reports_<tenant id>` with the template's password cloned on, `GRANT`s it `USAGE`/`SELECT` on the schema and `ALTER DEFAULT PRIVILEGES` for the tables migrations add next, and `boot` points the store's reports pool at that role with `withReportsUser`. The role is dropped when the store is deleted or a half-made one is undone, after its schema so it carries no grants. Setup that fails leaves the store serving with reports disabled (empty DSN), the D78 fail-closed default. A test provisions two stores and asserts one's report cannot read the other's table by qualified name, nor switch into its role with `set_config('role', …)`. | D70 rests on "a connection can only see its own store's tables", and for the engine's trusted code `search_path` delivers that. A custom report is untrusted SQL, and `search_path` only resolves *unqualified* names — it is not a privilege boundary — so one shared login granted every store's schema (what D78 set up for platform mode) let `SELECT … FROM other_store.customers` read across stores outright. The fix is not a per-store role the shared login assumes: to assume it the login must be a *member* of it, and membership is checked against the session role, so a report could `SET ROLE` — or, slipping past the one-statement parse, `SELECT set_config('role', 'other_store_role', false)` — into any store's role. Only when the *session* role itself is store-scoped is there no role to reach: a per-store login that is a member of nothing is refused every cross-store read by PostgreSQL, not by the parser, which D78 established cannot be the barrier. One cloned password keeps the operator's setup a single credential; the role name carries the namespace because roles are cluster-global, and the tenant id because a schema-derived name can pass the 63-byte limit and be truncated onto another store's. Creating the role at boot rather than at provision heals a store made before this and keeps the engine's migrations covered by the default-privilege grant that must precede them. Not changed: single-store mode, where one schema and one reports role are the same store and nothing crosses. |
| D80 | Custom roles | **A store's roles are rows in a `roles` table; the engine's four (`owner`, `manager`, `staff`, `vendor`) are seeded, and a store creates, renames and deletes its own.** A store's own role is a named set of the rights this build has, resolved as its grants plus the floor (`catalog.read`), with no engine default to track or reset to. `owner` stays fixed and undeletable, and so does `vendor`, the key vendor logins are filed under; `manager`, `staff` and `vendor` become starting roles, editable and resettable, and `manager` and `staff` deletable once nobody holds them. A role held by any operator, open invitation or unrevoked API key is refused deletion (409 `role_in_use`, counting each), and the foreign keys from `superusers` and `superuser_invitations` (RESTRICT) and `role_rights` (CASCADE) are the last line under that check; a writer that meets one because the role was deleted under it answers 400, not 500. Every service that asked the compiled `ValidRole` whether a role exists asks the store (`Roles().Exists`). Routes: `POST /api/admin/roles`, `DELETE /api/admin/roles/{role}` now deletes, reset moved to `POST /api/admin/roles/{role}/reset`, and `GET /api/admin/roles/names` behind `team.read` for the screens that hand a role out. Two things are deliberately left fixed: vendor row-scoping stays tied to the role named `vendor` (M48's CHECK), and API keys keep the four built-in roles. The panel's Team screen adds people by email and role, then offers Create Account or Send Invite, as the KitCommerce admin does. | D24 refused custom roles to keep role names stable for code that switched on them and the matrix legible. D64 made the key an identifier and the title the store's, and D65 made the rights extensible, so no code needs a role but `owner` and `vendor`, and the stores the KitCommerce admin serves expect to name their own roles. A table rather than a list in Go, because a role is now the store's data and the database is what should refuse an operator filed under one that does not exist. The foreign keys rather than the old CHECK lists, because the list is no longer known when the migration is written. Deleting a starting role is allowed because a store that does not want "Staff" should not have to keep it; owner is not, because it is the way back in. Changing DELETE's meaning is a break for any client that used it to reset, accepted because the verb now matches what it names and the release notes say so. |
| D82 | Abandoned checkouts | **`ext/cart-recovery` keeps its own record of every basket idle past a threshold the store sets in minutes, runs a sequence of reminders against it, and credits the order that brings it back — and core gains one field, `cart_id` on `order.created`.** The module owns three tables: one settings row (two sequences, `checkout` for a basket carrying a typed email and `cart` for one that does not, each an on/off switch, an idle threshold and up to five "wait N minutes, then send this template" steps; NULL means the defaults in code, and `installed_at`, set by the migration, is the install guard), `cart_recovery_abandonments` (one row per cart for its life, with a snapshot of the lines, the sequence's progress, the click count, the recovered order and the suppression) and `cart_recovery_events` (its timeline, which doubles as the audit trail of operator actions). A one-minute pass records idle baskets, notices baskets that were touched again or deleted, and sends due steps after re-checking every rule against the basket as it is now, with no transaction across the send. Reminders are three engine templates (`cart.recovery.1`–`.3`, D58). A message links to `/x/cart-recovery/r/{token}` on `Config.PanelURL`, which counts the click and redirects to the storefront basket. Admin routes under `/api/admin/x/cart-recovery/` list, count, analyse, send by hand, copy the link, suppress and configure, gated on four module rights (D65): `abandonment.read`, `.contact` and `.suppress` for manager and staff, `.automate` for manager. The reference binary installs it with `-recovery`. Core's TTL, `cart.abandoned` and the read-only Carts screen are unchanged. | Core's abandonment (D47) is a retention clock — thirty days — and a reminder that arrives a month late is not a reminder; moving that clock to minutes would expire live baskets, so the module needed one of its own, and a record per basket is what lets the screen say what was sent, clicked and bought. The old module sent one message per `cart.abandoned` and owned no table, so it could neither stop when the shopper bought nor say whether it had ever worked. `order.created` gains `cart_id` because nothing else ties an order to the basket it came from — the cart's move to `converted` announces nothing and an email can be on two baskets — and adding a field to an existing payload is the change `docs/events.md` calls safe, where a `cart.converted` name would be one more frozen contract. Kind is read from the typed email because on a guest storefront that is the only trace checkout leaves on a basket before the order exists. An unpaid order is not a recovery target here: once checkout runs, the basket is converted and there is no route that reopens it, so payment-failure recovery waits for one. A recovery counts at order creation, not payment, which over-counts an order whose payment then fails; `recovered_after_message` is reported beside it so the sequence is credited only with orders a message preceded. |
| D83 | One engine for every store | **Every store-owned row carries `store_id`, and PostgreSQL row-level security keeps each statement to the store its context names.** `gocommerce serve` serves every store from one set of tables; `gocommerce platform`, its schema-per-store package and its console are gone. A `stores` directory (`store_<ulid>` ids, slug, status, order prefix), `store_members` (a person's role and seller in each store), `store_counters`, `store_domains` and `api_key_index` are global; every other table gets `store_id` with a forced `store_scope` policy, unique keys and foreign keys that lead with it (`ScopeTables`, which modules call through `ScopeMigration`). The store travels in `context.Context`: a `database/sql` driver wrapper sets `gocommerce.store_id` on a connection whenever a statement's store differs, so every existing call site is bound without changing its signature. A request finds its store by API key, `x-litekart-store`/`X-Store`, `?store=`, the `litekart_store_id` cookie, host, the person's only store, or the install's only store; naming a store grants nothing, and staff who are not members get 403. Background work runs per store (`ForEachStore`); each store numbers its own orders; a person signs in once and works in any store they belong to. Custom SQL reports read one store each (D84). `Config.DefaultStore` binds unbound work for an embedder and for tests; `serve` never sets it. | D70 rejected a `store_id` column because one forgotten filter shows one shop's customers to another. Row security answers that the same way a schema did — the database enforces it — without a pool per store, migrations per store at boot, or a second kind of administrator: a forgotten filter now returns no rows, and an insert takes its store from the column default or fails. What it needs is that the engine's login cannot pass the policies, so `doctor` fails on a superuser or `BYPASSRLS` login, Compose and the dev script create `gocommerce_app`, and `serve` refuses to start with more than one store under such a login (one store only warns, so an existing install can upgrade first). Statements are not rewritten to name `store_id` as kitcommerce-api's do: about a thousand edits would add no isolation the policy does not already give (owner's decision, 2026-10-15). A single-store install upgrades in place as store #1 with no id changes; a 1.1/1.2 platform-mode install is not converted. |
| D84 | A report reads one store, pinned by its connection | **A custom report runs on the one reports login, under a restrictive row policy keyed on its connection rather than on a setting.** Before each run the engine records `(pg_backend_pid(), store)` in `report_runs`, which only the engine's login can write; every store table has `report_scope`, `AS RESTRICTIVE FOR SELECT TO <reports login> USING (store_id = gocommerce_report_store())`, where that function is `SECURITY DEFINER` and reads the record for the calling backend. The run sets `gocommerce.store_id` too, so the engine's permissive `store_scope` passes, and the two together admit exactly the recorded store. The reports login is granted `SELECT` on the store tables and has everything revoked on the rest, at every start (`ensureReportsAccess`), and a failure there turns reports off. A test reads with `set_config` naming another store, reads the global tables, and runs an unrecorded connection: no other store's row, permission denied, and nothing. | The spec (D83, Part 4) proposed one database login per store, as D79 did for schemas. What that buys is a session identity a query cannot change; the backend's pid is one already, and it needs no `CREATEROLE` for the engine, no password per store, and no role to create, heal and drop with each store. `set_config` can move `gocommerce.store_id` but not the pid, and the record that maps the pid is in a table the reports login cannot touch, so naming another store gains nothing. Restrictive rather than a second permissive policy, because permissive policies are OR'd: a reports-only permissive policy would sit beside `store_scope`, which a `set_config` satisfies. Fail closed twice: no record means no rows, and no setup means no reports. The global tables are revoked by their absence of `store_scope` rather than by a list, so the next one added is covered. |
| D85 | Each store's currency | **A store has its own settlement currency, `stores.currency` (NULL is `Config.Currency`), chosen when it is created or on Store settings, and fixed once the store has an order.** `App.Currency(ctx)` is the one reader; the store middleware carries the code on the request beside the store. Changing it before the first order relabels the store's products and open carts and converts nothing; after the first order `PATCH /api/admin/store` refuses it with 409. Rows a module owns (a B2B quote, a recovery record) keep the code they were made in, as every order does. `/health/ready` keeps the install's, which D31 froze. Owner's decision, 2026-10-16: "until the first order". Narrows D14 (one currency per *install* becomes one per *store*) and D31 (the currency is no longer a start-up decision; the rest of that payload still is). | D83 put many stores in one install, so D14's one currency made a shop in India sell in the install's dollars. D31's reason still holds — a currency changed under existing orders changes what they mean — which is why the line is the first order rather than never: before it nothing yet means anything, and a store being set up must be able to correct a wrong choice. Relabelling rather than converting, because there is no rate to convert at and a price typed as 2500 meant 2500 in the currency the owner had in mind. |
| D86 | Error tracking | **Every 5xx the engine answers and every panic it recovers, for a store, and every crash the panel reports, is kept in that store's `error_issues` / `error_events` (store-scoped), grouped by a fingerprint of source, kind, message with digits normalised, and culprit; an issue keeps its count and its latest 20 occurrences, and a resolved one reopens when it happens again.** Reading them is a new right, `errors.read` (and `errors.write` to resolve or ignore), held by owners. `POST /api/admin/errors` takes the panel's reports from any signed-in operator, 20 a minute per operator per store, and the panel may open at most 500 new issues a store a day. Occurrences are recorded by one worker off the request path (a bounded queue that drops when full), never inline. A path keeps the route's pattern, so a token in a URL is never stored. Shut on a demo store (`demoFrozenAny`), and closed to the custom-reports login. The response to the client is unchanged: still "internal error". Errors with no store bound stay in the log. | A shop owner was told "internal error" and the cause went to a process log nobody running a shop reads — the owner asked for a Sentry-like page. Kept in the store rather than a third-party service, because the engine ships no SDKs (rule 2) and a store's errors are its own data. Its own right rather than `store.operate`, which deliberately stops short of database-level error text: this page is exactly that text. |
| D87 | Each store's languages | **A store sells in its own languages, `stores.languages` (NULL is `Config.Languages`, default first), main language first, chosen on Store settings at any time; a request is negotiated against its store's list, its catalogue is translated into the others, and a customer is written to in the language their order recorded.** `App.Languages(ctx)` is the one reader and the store middleware carries the list on the request beside the currency, so `languageMW` now runs inside `storeMW`. The main language replaces `Config.DefaultLanguage` wherever a store is in play (the request fallback, the translator's skip, a notification with no language, store emails, CSV import, cms, identity, MCP, `/api/admin/settings`); `/health/ready` keeps the install's (D31). `store_profile.language` is no longer read: it never decided anything, since every order records its language. Translations: `ext/translations` (installed by `-translations`) adds categories (title, with the whole path) and collections (title, description) to products, applied on the public reads only, and refuses a translation into the main language or one the store does not sell in. Wording: `notification_templates` gains `language`; a message is worded in the customer's language, else the main language's, else the default, and rows from before keep an empty language that stands for the main one until the main language moves, when they are labelled with the language they were written in. MSG91's per-language DLT templates and translated default wording are out (a model's Hindi needs a native speaker's review). Owner's decision, 2026-10-16: queued as item 5, "add language to 5." Narrows D21 (languages per *store*, not per install) and D31 (the language list is no longer a start-up decision). | D83 put many stores in one install, so D21's one list made a shop in Odisha sell in whatever every other store did, and the negotiation, order language and translator port were all in place with nowhere per store to choose. One list with the main language first rather than a list and a default, so the main language cannot be one the store does not sell in; changeable at any time, unlike the currency (D85), because an order keeps the language it recorded and nothing already done changes meaning. The empty-language rows rather than a migration that stamps one, because the main language is each store's own and a migration cannot know it; the move of the main language is the moment it is known. |
| D88 | Video showcases | **`ext/showcases` (`-showcases`) keeps a store's video showcases: a YouTube or Facebook link, the products it shows (picked, up to 50, or a collection read when shown), the one being shown now, and where on the storefront it appears; a storefront reads it at `/x/showcases` and sells through the ordinary cart.** No video is hosted, uploaded or streamed: a pasted address is checked against the platform's own share formats and kept as a reference, and the player — YouTube's privacy-enhanced embed, Facebook's video plugin — is built from it on the server, so nothing a merchant types reaches a page as markup. A label is never inferred from the link: `live` only after the store presses "We're live", and for twelve hours at most (pressing it again starts them again); `scheduled` before a start time it gave; `replay` once ended or lapsed; otherwise, including a start time passed with nothing said, `video`. A buyer gets a slim product summary (no cost, stock count or metadata), only active products the request's channel carries, translated through the newly exported `App.TranslateProducts`; no customer price, since price lists apply in the cart against a proven address (D66). Views and clicks are counted per day with nothing about the buyer kept, rate-limited per store, showcase and address; an add to the cart is counted once per basket and not rate-limited, since behind a proxy every buyer shares one address; an order from a basket a showcase filled is counted once, on `order.created`. The panel's content security policy gains `frame-src` for exactly those two players, so the editor can preview the video. Rights `showcases.read` (manager, staff) and `showcases.write` (manager). Owner's request, 2026-10-16: the Video Showcase action plan, engine and panel now; the Svelte Commerce page is a follow-up. | A shop already films its products for its social pages, and a buyer trusts what they can see the shop's own people handle. Hosting video would be a second product; embedding the platform's player is what the platforms permit and costs nothing. The live label is the store's word because no link can prove a broadcast is on, and a stale "live" on a finished stream is the kind of claim the owner's standards forbid. |
| D89 | A store is never unnamed | **A store opened without a name takes a suggested one — an adjective and a place to shop, "Saffron Orchard" at `saffron-orchard` — the way GitHub offers a name for a new repository; the create form shows it under the name field and fills it in on a click, and `GET /api/stores/suggest` gives another.** A name given without an address gives the address, numbered when taken, or a suggested one when the name has no Latin letters to make one from; an address given and taken is still refused. Migration 0067 named every store that had no name in the directory (store lists, the account menu), taking the profile's name where it had one; the profile's name, which buyers see, is left to the owner, so an upgrade renames no live shop in front of its customers. Owner's request, 2026-10-16: "automatically add a random store name like github does". | A fresh install's first store had no name until its owner found Store settings, so store lists and emails showed a blank. Two short word lists rather than a dictionary: every pairing reads as a plausible shop, and sixteen hundred of them leave the number as the rare case. |
| D90 | One corner, and green means on | **Every element in the panel takes the buttons' corner, `--borderRadius` (5px): fields, cards, table frames, modals, menus, alerts and tags. Round things stay round. Green is kept for a provider that is switched on and working, and its "Active" label carries a tick; a built-in method reads "Built in" in grey.** The radius lives in `gocommerce.css`: PocketBase's input and large-radius tokens are pointed at it, and the search bar's pill and the `tw:rounded-*` utilities are overridden there. PocketBase's sheets stay verbatim. | The owner's call, 16 October 2026, on the Warehouses screen: its button corners were right, and the rest should match them. That reverses the earlier 8px field radius, which was chosen so a borderless field still read as having a corner. The panel had grown five radii side by side (5, 8, 13 and 15px, and pills). Green was the second call, on Shipping providers: once the `label-success` colours worked, built-in methods were as green as a configured aggregator, so green no longer said "switched on". |
| D91 | Abandoned checkouts read like kitcommerce-admin | **The abandoned checkouts list, its record page and its dialogs take kitcommerce-admin's layout and words: a count beside the title, six figures with icons, one filter card (live search on name, email or phone; a segmented type; a status multi-select; value and date popovers), status pills in its colours, a row menu that reaches the record, the customer, the cart and the order, and a record page with outcome banners, pictured items, a timeline grouped by day, and Customer, Recovery and Checkout summary cards.** The name and phone are the newest order's for the email, the Customers screen's reading (D22); the cart value is the subtotal; there is no payment card, because a cart never attempts a payment; and the panel's shared pager stays. | The owner asked, 16 October 2026, for the same abandoned cart and checkout screens as kitcommerce-admin. A cart here has no name, phone, address, shipping, tax or payment, so each of those is either read from where the store does know it (an earlier order) or left off, never shown as a blank or a guess. The automations list, the workflow editor and the recovery report follow in a second pull request. |
| D92 | Recovery automations are a workflow | **Each recovery automation is edited as a workflow, as in kitcommerce-admin: the trigger, the conditions, a wait and a message per step, and the end, as nodes on a dotted canvas with the selected node's settings beside it, a + between nodes to insert a step, and zoom and fit. The Automations list and the recovery report take kitcommerce-admin's layout too, and the report is called Cart recovery.** The canvas is drawn with plain Svelte and CSS, not kitcommerce-admin's graph library. | Asked for with D91, 16 October 2026. A recovery sequence is always a straight line, so a free-form graph library (dragging, detaching, drafts) would bring the panel its first runtime dependency for edits this engine cannot store; the row of nodes says the same thing. The report says "Messages sent" where kitcommerce-admin says "delivered", because the engine knows a message left, not that it arrived. |
| D93 | Signing up, and the screens before a store | **An install that hosts stores for other people opens sign-up with `-signup` / `GOCOMMERCE_SIGNUP` (off by default); `POST /api/admin/signup` makes a person in no store and signs them in, and the store is opened on the next screen by the store picker's create form, so `POST /api/stores/create` stays the one way a store is opened. It is shut, whatever the setting, on a demo and while the install has no email of its own (a provider key in the environment), and `doctor` says so. A taken address is a 409 `email_taken`; one client address (an IPv6 /64) may make 20 well-formed attempts an hour; where sign-up is on, one person may open 10 stores a day; and the addresses a platform keeps for itself (`www`, `admin`, `api`, `mail` and the like) are nobody's store. `auth-state` says `signup`; a reset request's 202 says `link` (whether `Config.PanelURL` is set), so the forgot-password screen says link or code. Every screen before a store — sign in, sign up, forgot password (now its own address), reset, invitation and the store picker — shares one phone-first layout, `AuthShell`: top-aligned with a 20px gutter on a phone, a centred card from 600px, full-size fields with 16px text, and the card alone moving between screens as a view transition.** Owner's request, 2026-10-17: "Improve mobile login page for gocommerce, + signup + forgot password. There is no way to signup and create a new store. Password reset must send an link inside email clicking which must be able to create new password." | Nobody could make their own account: a person existed only through the installer, a team screen or an invitation, so the hosted service had no front door. Off by default because a shop run for one business must never take accounts from strangers. Two steps rather than one form because the create form already holds the store's name suggestion, country and currency, and a second copy of it is how the two would drift. Unlike sign-in and reset, sign-up says an address is taken: it cannot refuse one without saying why short of making its first step an email; an account made with somebody else's address becomes theirs when they reset it, which is only true where a reset email can reach them — hence the install's own email, since a new person's reset goes out on the install's channel — and the hourly limit rations what is left of the oracle. An attempt refused for its shape costs nothing, or empty requests would shut the door on everyone behind one proxy. A demo signs every visitor in as one account, so an account of your own could never be signed back in to. Store opening is paced and the platform's own host names reserved because strangers now reach the create form: each store is seeded rows, a sequence and a host under the base domain, and one called "admin" is a page that looks like the platform's. It is a window rather than the login throttle's growing delay because a sign-up is not a failure. Behind a proxy that does not pass the client through, the limit is the install's rather than one person's, as the login throttle's already is; trusting a proxy's forwarded address is a change of its own. The reset link still comes only from `Config.PanelURL`, never the request's host, for the reason `passwordreset.go` gives; the hosted service sets `GOCOMMERCE_PANEL_URL`. The old card sat in the vertical middle (it jumped when a phone's keyboard opened), ran its fields to the edge of the glass, and kept a forgotten password as a mode with no address, so a phone's back button left the panel. |
| D94 | Sample products | **A new store may start with a sample catalogue, and erase it in one go. `POST /api/stores/create` takes `sample_products` (off by default; the store form's "Add sample products" switch); `Catalog.AddSampleProducts` makes eight active products through `CreateProduct`, four with sizes or colours, every variant stocked at the default location, no pictures, priced from a table in major units scaled by the store's currency (25 is 25.00 USD, 25 JPY, 25.000 KWD), and a slug the store already has is skipped. Each is marked by `products.sample` (migration 0068), which only the engine sets. `GET /api/admin/sample-products` counts them (catalog.read) and `DELETE` erases them all, edited or not, one `DeleteProduct` at a time (catalog.write).** The Products screen shows a banner with the count, a Sample label on each sample row, and the erase behind a confirmation. A failure to add samples is reported in the create answer rather than failing it. Owner's request, 2026-10-18: "when i create a new store give me optin to add sample rproducts, give option to erase all sample products as well". | A column rather than a `metadata` key: metadata is the merchant's own field, so a flag kept there could be edited away (stranding a sample the erase cannot find) or typed onto their own product (filing it for erasing); no input sets the column. Opt-in, because samples a merchant did not ask for are clean-up. No pictures, because a missing asset is left absent rather than faked with stock or generated images. Erasing through `DeleteProduct` gives every sample its audit row and its `product.deleted` event, and keeps order lines, as a delete by hand would; one transaction per product, so a failure part way leaves the rest for the next run. Reported rather than failed on create, because the store already exists and a retried create would open a second one. One table in major units, because a fixed minor amount reads 2,500 in yen; the face value is the same number in every currency, so it is not a market price anywhere. |
| D95 | A reset email carries a code and a link | **Every operator password-reset email carries a 6-digit code, and a link as well when `GOCOMMERCE_PANEL_URL` is set. Both are keys to one row of `superuser_password_resets` (migration 0069 adds `code_hash` and `attempts`), so spending either kills both. The link is unchanged: the long random token, built only from `Config.PanelURL`, looked up by `GET /api/admin/password-reset/{token}`, and `auth-state` and the 202 still say `reset_link` and `link`. The code is stored as the SHA-256 of the operator's id and the digits. `POST /api/admin/password-reset/confirm` takes `{token, password}` as before, or `{email, code, password}` (`identity` for `email`, as the request route takes), never both. A code allows 5 tries, each reserved and matched in one statement. The public request route sends nothing more, with the same 202, while the operator holds 5 unexpired resets or has been sent 10 in a day, so a row is kept a day past its hour; `POST /api/admin/superusers/{id}/password-reset` is exempt from both caps. Without a token, the panel's reset screen is one form: the address, the code and the new password twice, reached from the forgot-password screen with the address carried in history state rather than the URL.** Owner's words, 2026-10-18: "Keep the email code 6 digit in forgot password", "along with code, we need a verification link in email", "keep both link and code". | Six digits can be guessed where the token cannot, so the code is rationed per operator, wherever the guesses come from: 5 tries a code, and at most 5 live codes and 10 a day, which allows 50 guesses a day; a year of nonstop guessing opens the account about 2% of the time, against about 20% with the hourly cap alone. The try is reserved in the statement that matches the code, because a check in one statement and a charge in another let a burst of parallel guesses all be tested before any charge landed; the issue cap counts under the operator's row lock for the same reason. The link keeps its own long token rather than carrying the code, so wrong codes never lock anybody out of the link, and no email address ever goes in a URL. The hash includes the operator id because two operators can hold the same six digits, so a code is only ever looked up with its address; an address with no account runs the same statements and gets the same answer as a wrong code. The team route is exempt so an owner can always send a teammate a fresh email. The trade-off: without the panel URL, which is the live site today, someone who knows an operator's address can use up that day's codes with wrong guesses, and the real person then gets the same 202 and no working code; the ways back are another owner's send from Settings › Team or `gocommerce superuser update` on the server. With the panel URL set this cannot happen, because every email still carries a link that wrong codes cannot use up. The SHA-256 of six digits is reversible by anyone who can read the table, unlike the token's; that is accepted for a row that lives an hour at most and takes 5 guesses. `reset_token` keeps meaning the link's token, because a store may have saved its own copy of the template; `reset_code` is new. Such a copy, on an install without a panel URL, still mails only the token, so the panel's code field also takes a pasted token and spends it as one. Wrong codes are charged to the address as well as the client, as wrong passwords are, so a stranger guessing for an address also slows that person's own code entry for a while; the link is not slowed. The daily cap counts resets an owner sent from Settings › Team too, so ten of those in a day close the public route for that person until the next day. The customer side (`ext/identity`, `/x/identity/password-reset*`, `/portal/*`) is untouched: `@misiki/gocommerce-connector` calls those routes. |
| D96 | A sample shop, with photographs | **A store may start with a sample shop, not only sample products, and erase all of it in one go. The create form's switch (`sample_content`; `sample_products` still taken) and `POST /api/admin/sample-content` add, `GET` counts and `DELETE` erases: the eight products, now each with a photograph (free under the Unsplash License, built into the binary and served at `/sample-media/1/`, credited in docs/admin-panel.md), four categories they are filed in, two collections, two percentage discounts scoped to the sample products, twelve orders over the last month numbered SAMPLE-1001 on from six customers at example.com, and, through the optional `SampleSeeder` interface, each module's own: reviews, pages, questions and menu links. Every row is marked by a `sample` column only the engine sets (migration 0070 and one per module). Adding and erasing need the write right of every core area they touch.** The orders go through the orders importer, so they move no stock, and `outbox.write` drops every event of a sample order, so nothing ever follows one: no email, SMS, webhook or invoice, whatever is done to it later. Erasing them deletes them with their notification rows and unsent outbox events, each with an `order.erase_sample` audit row, the one way an order is ever deleted. Adding and erasing also need each module's write right (`SampleRights`). A sample category or photo the merchant uses stays and becomes theirs. The sample pages and answers promise no number of days or time to answer. Owner's requests, 2026-10-19: "sample products does not have images", then free licensed photos chosen over drawn illustrations or Gemini photos; "add sample categories, menu, banners, pages, faq, etc"; "add sample reviews, customers, orders, discounts". This overrides, for sample content only, the standing rules against stock photos and against unverified reviews: both are erased together before a store opens, and the Products banner says so. Banners wait for a banner feature, which the engine does not have yet. | A shop is judged by its full screens, and an empty order list or a product with no picture reads as broken rather than new. Photos linked from the binary rather than uploaded: every install has them, including one with nowhere to put uploads, and a store's samples cost no disk. Discounts scoped to the samples, so a forgotten one cannot discount real stock. Orders numbered SAMPLE-, at example.com and without events, so nothing reaches a real person and the number says what the order is wherever it is shown. One column per table rather than a registry of sample ids: the mark travels with the row, and no edit can clear it. A sample category holding the merchant's own product stays, because erasing it would uncategorise their product. A module joins through an interface rather than core knowing its tables (AGENTS.md rule 3). |
| D98 | Import is a button | **The Import screen runs a file from two buttons side by side, Dry run and Import, instead of one button named after a "Dry run" switch. Both are always on screen under their own names; while the box is empty they are greyed out and the line under them says why. A dry run's result says it wrote nothing and that Import applies the file.** The switch is gone, so pressing Import writes straight away; the dry run is one click away beside it, and the sentence under the buttons still says to run it first. On a phone the run buttons share a line of their own and the kind tabs wrap instead of shrinking. | The owner's report, 19 October 2026: "no buttons to import" on `/dash/data/import`. The switch started on, so the only run button read "Dry run" and the screen never showed one called Import; it sat greyed out with no reason given until a CSV was in the box, and "Choose a file…" only fills the box. Keeping the switch and renaming the button would still have hidden the real import behind a setting. |
| D99 | Banners | **The pictures on the storefront's pages are rows of a `banners` module (`-banners`), one per picture: a page (only `home` is accepted until a storefront shows banners anywhere else), a placement (`slide` across the top, or `grid`, a tile under a `group_title` heading), a page-wide position, a title that is also the alternative text, a link, a picture and an optional phone picture, and an on/off switch. The storefront reads `GET /x/banners?page=home`, split into `slides` and `grids`; the connector maps them onto the page record svelte-commerce renders (`desktopBanners`, `tabletBanners`, `mobileBanners`, `sections`). Rights `banners.read` (manager, staff) and `banners.write` (manager). The sample shop adds three slides and a Shop by collection grid, and gives its categories pictures (`metadata.image`) for the storefront's own category tiles, so the home page does not show the categories twice.** Owner's request, 2026-10-19: "add sample categories, menu, banners, pages, faq, etc", then "yes" to building banners first. | Litekart keeps a home page's banners as arrays on its page record, edited in the page editor; rows instead let a shop switch one off, reorder one, or add one without rewriting the page, and keep the engine's page record free of storefront layout. One table for slides and grid tiles, because they are the same thing — a picture and a link — placed differently. A phone picture of its own, since a 4:1 desktop slide cropped to a phone screen loses its subject; empty falls back to the desktop one, as the storefront's tiers already do. No schedule yet: Litekart has none, and a switch covers the common case. |
| D100 | Selects | **Every select in the panel is `Select.svelte`: shadcn's menu, drawn on the panel's own colours, with a search box once a list is long. No screen draws a native `<select>`; a panel test fails if one does.** Owner's request, 2026-10-20: "use shadcn select, the current one has issue in dark mode". | A native select's list is drawn by the browser, not the page, so it does not follow the panel's colours, and the owner found it wrong in dark mode; its closed box also sat differently from the selects around it, with the browser's own arrow. Nine screens still had one, including Banners, Store settings and the stock drawers. |
| D101 | Carrier icons | **Each carrier on Shipping providers shows its own icon, in its own colours, as the company publishes it on its own website (its favicon or app icon), saved as a 64-pixel PNG under `admin/static/images/providers/` and shipped inside the binary. Each sits on a white plate in both themes. Payment gateways keep their Simple Icons marks in the text colour.** Owner's request, 2026-10-20: "use their real icons". | Simple Icons carries one of the eleven carriers, so the screen was a column of trucks that told an owner nothing. A carrier's own icon is the one its customers already know; using it to name the integration claims nothing about the brand. A white plate because several are drawn for a white page (NimbusPost's navy, Shippo's black) and vanish on a dark one. Nothing is fetched at run time, for the reason the marks are copied in: a hotlinked logo tells a third party who opened the screen. Whether a carrier is on is still said by its card's edge and label, not by the icon. |
| D102 | Payments | **Cash on delivery stays built in, and a store can switch it off.** Its switch is a built-in plugin, `payments-cod`, on by default, so every store keeps taking it until its owner says otherwise; it is switched from Settings › Payment methods like a gateway, under `plugins.write`, and audited as a plugin change. Off, it is what an idle gateway is: listed on Payment methods, missing from `GET /api/checkout` (both `payment_methods` and `methods`, now answered for the store asking), refused by name at checkout, and not offered on the panel's New order form. The settings name the plugin on the provider (`plugin`), since a built-in has no module to look one up by. Switching it off with no gateway set up is allowed, and the screen says shoppers cannot check out.** Owner's request, 2026-10-20: "we must be able to disable COD /dash/settings/payments". | Many stores do not take cash at the door, and a built-in that could not be turned off was offered at every checkout regardless. A plugin row rather than a new column or table: the engine already treats a provider whose plugin is off as absent everywhere a shopper or an operator could pick it (D59), so cash on delivery joins that path instead of growing a second one, with no migration. Allowed with a warning rather than refused when nothing else is on: a gateway's key cleared or its own switch flipped reaches the same state, so a refusal on this one switch could not promise a store always has a way to pay, and a store still being set up has no use for one. The payouts report now names a method by every provider the build carries, so a method switched off is not reported as removed. |
| D103 | AI services are modules | **The services in the AI-Native B2B specification (`docs/ai-b2b/spec.md`) are `ext/` modules in the engine's own process: `ext/ai-gateway`, `ext/context`, `ext/mcp`, `ext/memory`, `ext/whatsapp-cloud`, `ext/meta-ads`, `ext/order-desk`, `ext/sales-inbox`, `ext/procurement` and `ext/sales-intelligence`, each with its own `<name>_` tables, its own rights and a plugin that installs idle. Their routes sit under `/x/<name>/` and `/api/admin/x/<name>/` (rule 9), and the spec's `/v1/...` paths map onto them as `docs/ai-b2b/adr/0001-service-boundaries.md` lists. A published module route only grows; a breaking change is a new path. A module leaves the process for one of three reasons only: it needs a third-party SDK (its own repository, rule 2), its work is heavy enough to starve checkout (a worker it calls after commit, rule 5), or it runs code nobody has reviewed (never loaded, D57). The SDK is not a module: it is `gctest`, a module manifest, docs and a generator.** Owner's answer to the M0 decisions, 2026-10-21: "goahead with your proposals". | Separate network services would cost a service-to-service auth scheme, a second deployment and tenancy enforced in several places before the first AI feature ships, for scaling nobody has asked for. In one process every AI table sits behind the row policy that already isolates orders, and the existing scope test covers them. `/v1` prefixes would put two route conventions in one API, and rule 9's check refuses them; additive change to a documented route (rule 11) is the versioning the spec asks for. Splitting a module out later stays cheap, because it talks to the rest only through Go calls and its own tables. |
| D104 | AI tenancy | **The tenant is the store: the spec's `tenant_id` is `store_id`. Every AI table (executions, approvals, jobs, memories, embeddings, conversations, intakes, usage) is a store table behind the row policy (`ScopeMigration`, rule 15, D83), and background work runs through `app.ForEachStore`. An inbound event that names no store, such as a WhatsApp message naming a phone-number id, finds its store through a global index that holds only identifiers, as `api_key_index` does, listed beside it in the scope tests' allow list. Vectors are store rows searched under the policy. Cache keys begin with the store id. AI log lines and the request log carry the store and a trace id and never a prompt, an answer or a message body. A tool call and a model request each carry one store's data. pgvector is optional and the engine runs without it; production Postgres moves to an image that ships pgvector only when semantic search is built, not for M1's gateway, MCP work or structured memory.** Owner's answer, 2026-10-21: "goahead with your proposals". | One isolation mechanism, the one D83 chose, rather than a second beside it: a schema or database per store for vectors would need migrations per store, and an external vector database would be a second place to keep consistent and to delete from. Store-first cache keys because a shared cache would let one store learn from a hit what another had asked. No prompts in logs, because logs leave the policy. pgvector waits because it is a hosting change for every install, and nothing in M1 needs it; the memory module checks for the extension and `doctor` says whether semantic search is on. |
| D105 | Agents and approvals | **Three kinds of caller are told apart in every audit row: a person (their id), an integration key (the key's id and name) and an agent (its agent key's id and name, and the run). An agent key is an API key with `kind = agent`: bound to one store, carrying an explicit list of rights that is a subset of its issuer's rather than a role, expiring after 90 days by default, unable to manage keys, roles, members, plugins or store settings whatever it lists, and kept only as a hash: it is shown once, and M46's readable copy stays for integration keys only. Agent work never runs on the static admin token. An agent's reads run within its rights, are recorded, and see personal data masked on every store unless its key is given the right to see it. Its writes split in two: records only staff ever see (drafts, notes, exception items, classified messages, proposed matches) are written directly through the owning module's service and audited as the agent; anything that reaches a customer, moves money or stock, or changes a price, a catalogue or a published page becomes a proposal in `approval_requests` (owned by `ext/ai-gateway`), which only a person holding the action's own rights can approve. Approving runs the same service method as the approver, against current state with the preview recomputed, once per proposal's key; a proposal expires after 7 days. New rights: `ai.read`, `ai.configure`, `ai.approve`, then `inbox.read` and `inbox.reply`.** Owner's answer, 2026-10-21: "goahead with your proposals". | An API key today resolves to operator 0, so "which agent did that, and who let it" had no answer; one table now answers it for every write an agent caused. A refund, a sent message or a published price cannot be undone, so approval comes before them, not undo after. Approval by the right the action needs rather than by role, because a role is a bundle that changes. No readable copy for agent keys, because they are issued to unattended software and a stored copy is one more place to leak them; an integration key is still a person's to copy again. Staff-only writes skip approval because a draft or a note reaches nobody until a person acts on it. Existing keys and scripts are untouched: only agent keys get these rules. |
| D106 | AI jobs | **Model work runs as durable jobs in Postgres, not Redis. `ext/ai-gateway` owns `ai_jobs`: claimed with `FOR UPDATE SKIP LOCKED` and a visibility timeout, retried with backoff for timeouts, rate limits and provider errors, never retried for a refused request, a failed output check or an exhausted budget, and dead after a fixed number of attempts, when it lands in its task's human queue. The transaction that creates the work enqueues its job (a stored message and its classification job commit together); the model call happens after commit and its result is written in a new transaction through the owning service (rule 5). Core events start AI work through ordinary subscriptions, keyed by the event id. Modules publish no events of their own in M1 or M2. Inbound channel webhooks verify the signature, find the store by index, claim (store, provider event id), store and enqueue in one transaction, and release the claim on failure. Every job and model call carries the trace id of the request or event that caused it.** Owner's answer, 2026-10-21: "goahead with your proposals". | The spec asks for Redis only where it is already supported, and no Redis code exists (D10). Putting model calls in outbox handlers would re-run every subscriber of an event on one failure, and the outbox's twelve attempts in about half an hour are each paid for when the handler calls a model. A table in the gateway rather than a general core queue, because no other module needs one yet, and it can move to core unchanged when a second user appears. |
| D107 | Model providers | **Only `ext/ai-gateway` calls a model, through one `Provider` interface (and `Embedder`), REST over `net/http` (rule 2): Anthropic's Messages API and the OpenAI chat format first, starting from `ext/import-amazon`'s client, which then moves onto the gateway. Work is done by named tasks in Go (such as `po.extract`, `sku.match`, `message.classify`, `reply.draft`), each declaring its types, its deterministic step tried first, caching, retrieval, model ladder, escalation rule and cost ceiling; answers are decoded into the task's type with unknown fields refused and every value checked in code. Each run writes one `ai_executions` row with the steps tried, provider, model version, tokens, latency, outcome, sources and cost. Cost is integer money: prices are minor units per million tokens and cost is `cost_micro_minor`, recorded as unknown, never zero, when no price is set (rule 6). Each store has a monthly cap that a run reserves before calling and settles after; rate limits per store and per key are kept in Postgres. Provider, key, models, cap and a data-sharing policy are per-store plugin settings; contact details and message bodies may leave the store only once its owner switches them on. Defaults: Claude Haiku 4.5 for the small model and Claude Sonnet 5.5 for the strong one. A platform-wide key on a hosted install is off by default, switched on by the install's owner, and metered against each store's cap. Untrusted content reaches a model only as fenced data; models get no tool that writes; an answer becomes a proposal (D105) or a staff-only record.** Owner's answer, 2026-10-21: "goahead with your proposals". | Today's one model caller records nothing and takes its keys from the process, so no budget, data-sharing policy or cost record could be enforced across modules. A third-party gateway or SDK would see every store's data, which is what the data-sharing policy exists to limit. Reservation before the call, because a cap counted afterwards is exceeded by every run in flight. A platform key off by default, because a store must choose to send its data to a provider rather than find that it has. |
| D108 | Draft orders are quotes | **The draft order the spec's AI Order Desk prepares is a company quote the seller opens (`ext/b2b`), carrying the agent's lines with each line's source and confidence, approved by a person and then sent and converted into an order exactly as a quote is today. Core's order machine gains no draft state.** Owner's answer, 2026-10-21: "goahead with your proposals". | A quote already converts into an order at agreed prices, so reusing it keeps one path for agreed pricing instead of a second one beside it; a draft state in core would add a branch to every order transition, every report and every export for a thing only B2B sellers use. What the quote lacks is that a seller can open one; that is the change. |
| D109 | Operator-chosen addresses are public | **An address a store operator or imported data chooses for the server to call (outgoing webhook endpoints, a provider's API base URL, an import's shop or image address) is dialled through `app.OutboundClient`, which connects only to public addresses: the check runs on the resolved address at connect time and again on every redirect, and refuses loopback, private, link-local, shared, documentation and reserved ranges, IPv4 and IPv6. New or edited webhook endpoints must be `https`. An install where every operator is trusted, such as one business's own server or a developer's machine, allows private addresses with `-allow-private-urls` / `GOCOMMERCE_ALLOW_PRIVATE_URLS`, and `doctor` warns while it is on. Addresses set by the install's own configuration, and hosts fixed in code, are not operator-chosen.** Owner's answer, 2026-10-21: "goahead with your proposals", to the recommended fix. | On an install that hosts other people's stores (D93), the operator of a store is not the owner of the server, so an address they type is a request the server makes on their behalf. Checking at connect time rather than only when the address is saved, because a name can resolve differently between the two. Webhook bodies carry order details, so they travel encrypted. Endpoints saved before this keep delivering, because refusing them now would end an integration without anyone being told. |
| D110 | Secrets at rest | **Secrets the engine must be able to read back are stored sealed: plugin settings of kind `secret`, the readable copy of an integration API key (M46) and outgoing webhook signing secrets. Sealing is AES-256-GCM (standard library, rule 2) under an install key, `GOCOMMERCE_SECRETS_KEY` (32 random bytes, base64), kept in the environment and never in the database. A sealed value names the key that sealed it, and its additional data names the store and the field, so a value copied to another store or field does not open. `GOCOMMERCE_SECRETS_KEY_PREVIOUS` still opens what the previous key sealed, so the key can be rotated, and `gocommerce secrets seal` reseals every readable or previous-key value under the current key. Without a key, secrets are stored readable as before and `doctor` warns with the count. A sealed value whose key the install no longer has reads as unset, so its plugin shows as not configured.** Owner's answer, 2026-10-21: "goahead with your proposals" (M1 step 3). | Payment and carrier keys, webhook signing secrets and the copies of API keys are the most valuable rows in the database, and a dump or a backup held them in the clear; with the key in the environment, the database alone is not enough. Opt-in, so an existing install keeps starting after an upgrade; the hosted install sets the key. A missing key reads as unset rather than failing every call, so a store can re-enter one key without checkout erroring on every payment method meanwhile. |
| D111 | Answering the contact inbox | **`ext/contact` replies from the panel and takes product enquiries into the same inbox.** `POST /api/admin/x/contact/messages/{id}/reply` under a new right, `contact.reply` (managers by default; owners hold every right), sends the `contact.reply` email to the shopper through the store's provider with the new `App.NotifyNow`, and only once the provider has taken it inserts a `contact_replies` row (subject, body, who sent it) and marks the message replied, in one transaction (migration 0003, scoped by 0004). `NotifyNow` is `Notify` for a caller with a person waiting: it answers 409 when nothing on the channel delivers and 422 with the provider's words when it refused or could not be reached, and records the attempt like any send. A public demo sends no replies (403 `demo_frozen`). `POST /x/contact/enquiries` takes a question about one product on sale, by id or slug, with an optional variant and quantity, into `contact_messages` as `kind = 'enquiry'` with the product's title and slug as they were, no foreign key onto the catalogue (migration 0005); it is announced to the contact form's one address with the `contact.enquiry` email, and the plugin's `enquiries` switch closes it. Both public forms keep the honeypot and are now limited together per store and peer address, ten a minute by default (`Config.MessagesPerMinute`), as ext/b2b limits its dealer form. The inbox filters by `kind` and searches the product's name. | The inbox recorded that somebody had replied somewhere else, so the reply itself was in one person's mail and the next person to open the message saw only "replied". Sending first and recording after is what keeps that record honest without holding a transaction across the provider (rule 5): a thread entry means the provider took the email, and a reply that did not go leaves the message as it was and the text in the box. `Notify` could not say that, because it is shaped for the outbox, which must not retry a refusal and must not fail a sale over a missing provider; a second entry point keeps that contract and gives an operator the other answer. A demo is refused because anybody may sign in to one and anybody may type an address into its storefront, which together make a relay from the store's own domain. `contact.reply` is new rather than lifted (D65): writing as the store is more than reading the inbox, which staff hold, and less than deleting from it. The product is kept as asked so the record survives a rename or a deletion, and read through the catalogue service so a draft cannot be asked about. The form was guarded by its honeypot alone; one limit for both forms, rather than one each, is what stops a script from doubling its allowance by alternating. |
| D112 | Asking a gateway about a payment | **A payment provider may implement the optional port `PaymentStatusReader`: `Status(ctx, order)` returns what the gateway says (`paid`, `failed`, the gateway's payment `reference`, and its own words in `detail`). `POST /api/admin/orders/{id}/check-payment` (`orders.write`, the right Mark paid carries) calls it outside any transaction and answers with what it said; when it says paid and the order is not, the payment is recorded through `Payments.MarkPaid` with the gateway's reference, and nothing else is ever written: no payment yet leaves the order alone, and a reported failure is answered, not recorded. A provider says paid only for the order's total in its currency, on the order's own gateway object. `GET /api/admin/settings` marks such a method `checks_payments`, and the order screen offers "Check with <gateway>" beside an unpaid payment for those alone. Stripe, Razorpay and Cashfree implement it first; Cashfree's storefront return page reaches the same check through its module's own public route.** Batch G08, 2026-10-22. | Payment state arrived only by webhook or by hand, so an order whose webhook was lost was either marked paid on faith or cancelled by the unpaid sweeper while the shopper's money sat at the gateway. A second optional interface rather than a method on `PaymentProvider`, for the reason `ReferencedRefunder` is one: a duck-typed port widened in place would silently stop matching every gateway outside this repository. Going through `MarkPaid` keeps one settlement path, so stock, confirmation and `order.paid` cannot differ between a webhook and a check. Failures are not recorded because a declined attempt is not final while the shopper can still pay, and the webhook already records the ones that matter. |
| D113 | Store domains route themselves | **A verified store domain is routed and given a certificate without anyone editing the proxy. The engine serves Traefik's HTTP provider at `GET /internal/traefik`: a router per verified domain on `websecure`, with the configured ACME resolver (default `letsencrypt`), and an http-to-https redirect on `web`, sending visitors to `GOCOMMERCE_TRAEFIK_UPSTREAM`. Unverified domains are never listed. The route is off unless `GOCOMMERCE_TRAEFIK_TOKEN` is set and wants that token, not an admin one. `GOCOMMERCE_DOMAIN_TARGET` names where a domain should point, and the panel shows it as an A, AAAA or CNAME record beside the TXT one.** Owner's answer, 2026-10-23: "build it", to setting domains up "automatically like shopify does". | Every new store domain needed the owner in Dokploy, and a domain added in the panel but not there answered with the proxy's 404 and its default certificate. Traefik's own HTTP provider rather than Caddy's on-demand certificates, because Dokploy's Traefik already holds ports 80 and 443; rather than Cloudflare for SaaS, because that is billed per domain and needs a CNAME, which an apex domain cannot have. Verified domains only, because a proxy that requested a certificate for any name sent to it would spend the install's Let's Encrypt allowance on strangers. A failed read is an error, not an empty list, because Traefik keeps its last routes on an error and drops them all on an empty answer. |
| D114 | Several brands on one install | **One install — one database, one deployment — answers under several brands, each a name on its own admin domain: `GOCOMMERCE_BRANDS` (`Config.Brands`), a JSON array of `{name, panel_url, base_domain, alias_base_domains, reserved, logo_url}`, checked at start (https panel URLs outside Dev, distinct panel hosts, base domains and aliases unique across brands, a clear error naming the entry). Unset, the install is one brand called GoCommerce made of `GOCOMMERCE_PANEL_URL` and `GOCOMMERCE_BASE_DOMAIN`, and behaves as before. A request's brand is its host matched against the brands' panel hosts, then their base domains and aliases, then a store's own domain (its store's brand), else the default brand, the one whose base domain or alias is `GOCOMMERCE_BASE_DOMAIN` or else the first (`BrandFrom(ctx)`); choosing it never changes which store the request is bound to. Each store belongs to one brand (`stores.brand_host`, the brand's panel host): it answers at `<slug>.<its brand's base domain>` and its aliases, and nowhere under another brand's; addresses the engine hands out use the base domain only. A store opened on a brand's admin domain is that brand's; at start, a store without a brand takes the default brand; the install's owner moves one with `gocommerce store brand <slug> <panel host>`, audited in the store, and after changing a brand's `panel_url` runs `gocommerce brands rename-host <old> <new>`, which moves its stores and people together — until then start-up logs an error and `doctor` fails, because stores recorded under a host no brand has would otherwise read as the default brand's without a word. One bad entry stops every brand, so `doctor` checks a `GOCOMMERCE_BRANDS` value as `serve` would and reports it rather than refusing to run; a logo that cannot be used only warns. Slugs stay unique across the install, and no store may take the platform's reserved names (D93), the first label of any brand's panel host, or a name a brand lists in `reserved`. Accounts are the install's: a person signs in at any brand's admin domain and sees and enters only that brand's stores there; a store of another brand is answered as one they are not on, at sign-in too. The static admin token keeps install-wide reach. The panel shows the brand's name (and logo) on the screens before a store, in its header and in page titles; emails to staff name the brand and link to its panel: a store's brand for a store's emails (an invitation), the brand of the request for a password reset when the person belongs to it (a store of theirs in it, or arrived through it), and otherwise, or with no request, the brand the person signed up or accepted an invitation under (`superusers.brand_host`), else that of their first store, else the default. An invitation is emailed only with a link built from configuration; without a panel URL the owner is given one built from the request to copy. A brand's panel host may also be a store's own domain; `doctor` lists the brands and notes it.** Owner's request, 2026-10-22: two admin domains that "both are same, with just difference in name", with the brand name in the panel and in emails. The first plan put every store under every brand's base domain; the owner reversed it the same day — "this will be difficult to manage" when one store answered under two brands' domains — so each store belongs to one brand. A brand may keep its stores on its own apex and still answer at the `shops.<apex>` its stores used before. | One deployment instead of one per name: the same release, one database to back up, and a person who works for stores under two names keeps one account. Links come from a configured panel URL chosen by the request's host, never from the host itself, for the reason the reset link always had (D95): a request is made by whoever wants the email, and a link built from Host would send a live token where they chose; a forged host only ever picks among the brands, or gets the first. A store in one brand rather than under all of them, because an owner would otherwise have to say which brand a shopper had reached, and two addresses for one shop split its search ranking and its customers' trust. The brand is kept on the store by panel host rather than by name, because a name can be changed and a host is what requests match. Unique slugs install-wide keep one table and one lookup; the cost is that two brands cannot both have "mystore". Existing stores take the brand of the base domain they already answered under, so no address goes dark on the upgrade, and the assignment is written once so reordering the list later moves nothing. A store from another brand reads as one the person is not on, so one brand's panel neither reaches nor confirms another's stores. A reset is in the request's brand only for someone who belongs to it, because anybody can ask for anybody's reset and would otherwise choose whose name a real person's email carries. A renamed panel host is said loudly rather than repaired, because which brand those stores belong to is the owner's call. Review fixes, the same day: a store's own domain had fallen back to the first brand, refusing a later brand's staff at their own address. |
| D115 | A guest opens their order with a code | **On the storefront a guest opens their order with its number and a 4-digit code sent to the email or phone on it (`POST /api/orders/{number}/code`, then `/code/verify`), which answers the order and its access token. The panel no longer hands out the access link.** The owner asked for codes, not links: a link in somebody's hands is the order, and a code goes only to the contact on it. Four digits are ten thousand guesses, so the limits are the design: ten minutes and five tries a code, one code a minute and five a day an order. The request answers 202 whether or not the order or contact matched, and sends nothing when they did not. The token stays the credential underneath, so the storefront reads the order as it always has. **Changed 2026-10-27 (owner: "we dont have to request a code, order_no and email is enough"): `POST /api/orders/{number}/open` answers the order and its token for the order's number with the email or phone on it, no code. An unknown number and a wrong contact are one 404, and each client (an IPv6 /64) gets 20 tries an hour, matched or not, because order numbers count up and the route would otherwise walk a known address through every order. The code routes stay for storefronts already built on them. The order emails' link carries the order's access key (`/order-tracking?order=…&key=…`), so it opens the order with nothing to type; the panel's copy button reads the key through the audited `POST /api/admin/orders/{id}/access-token` (orders.write) and copies the same link.** The way there is a link that is no credential (owner, 2026-10-27: "where can i see the order tracking link"): order emails and texts carry `order_tracking_url`, the storefront's `/order-tracking?order=…` on the store's primary verified domain, else `<slug>.<base domain>`, and nothing when it has neither; the panel's order page shows the same link with a copy button. It names the order and the code still has to come. |
| D116 | A store chooses how customers sign in | **Each store picks one way for shoppers to sign in, on Settings: an email and a password, or a phone and a 4-digit code sent by SMS. It is the identity plugin's public `login_method` setting, so the storefront draws only that form, and the engine refuses the other way (409 `sign_in_method`).** The owner asked for one or the other, not both: a shop in India often has no email from its buyers. A phone with no account opens one on its first code, kept on `login_phone`, which only a code sets, so the editable profile phone never becomes a sign-in. Codes carry D115's limits. Phone sign-in cannot be chosen until an SMS provider delivers, or nobody could sign in. |
| D117 | Checkout offers only the countries the store delivers to | **The address form's country list is the countries of the store's active shipping zones that have a rate for everybody (`GET /api/checkout/countries`); one country is shown fixed. A store with no rates, or a priced zone naming no country, rules nothing out.** The owner asked that a store shipping only within India offer only India: otherwise a shopper fills a foreign address and learns at the delivery step that nothing can be sent. A group's rate does not widen the list, by `configured`'s rule: it is an exception for that group. The engine still refuses an address no zone prices, so the list is a courtesy, not the check. |
| D118 | One account, signed in by email or by phone | **A store can allow both ways (`login_method` `email_or_phone`), and one account can carry both: signed in by email, a customer proves a phone with a code (`POST /x/identity/me/phone`, then `/verify`) and the phone then signs in to that account; signed in by phone, they add an email on the profile and set a first password, which an account with none sets without a current one.** The owner asked that a shopper who signed in either way reach the same account. A phone is linked only by its code, never matched from the typed profile phone: a mistyped number would hand somebody else the account. A phone that already opens another account is refused, not moved, because merging two accounts with orders on both is a different job. `phone_verified` is true only while the profile phone is the one that signs in. |
| D119 | A store is on the free or the Pro plan | **Each store has a plan, `free` until the install's operator runs `gocommerce store plan <slug> pro`; a feature on Pro reads it through `App.StorePlan`. The first is phone sign-in outside India: a free store texts sign-in codes to +91 numbers only, and any other country answers 403 `plan_required`.** The owner set it: phone sign-in in India is free, other countries are Pro. A command and not a panel switch, as `store demo` is, because a plan is what a store has paid for and not something its own staff choose. An unreadable plan reads as free, so a Pro feature stays shut rather than opening by accident. The shopper is told what the shop can do, never the word plan. |
| D120 | No live credential where a report can read it | **A sender names the data keys that hold a credential (`Notification.Secrets`): a reset link or code, an invitation, a confirmation link, a sign-in or order code, a basket's token. The log writes `(hidden)` in their place and in any copy of them (in the provider's error at any length, elsewhere from six characters), and refuses to resend such a message (409); migration 0078 hides them in rows already written, store by store. Store tables and columns that hold a credential are withheld from the reports login (`report_withheld`, filled by `WithholdFromReports`): `order_lookup_codes`, `identity_login_codes`, `idempotency_keys` and the error tracker's tables whole, and `carts.token`, `orders.access_token`, `api_keys.secret`, `plugins.settings`, `webhook_endpoints.secret` and `.url`, `outbox_events.payload`, `webhook_deliveries.payload`, `cart_recovery_abandonments.link_token`, `b2b_partial_checkouts.cart_token` and `.source_cart`, `wishlists.token`, `newsletter_subscriptions.token` and `identity_customers.password_hash` by column, which `ensureReportsAccess` grants column by column, one start at a time (an advisory lock). A webhook delivery's `last_error` names the operation and the cause, never the endpoint's address, and webhooks migration 0005 rewrites rows written before. `ext/alltest` fails when a report can read a column whose name looks like a credential (token, secret, password, key, code_hash, session, credential, settings) and is neither withheld nor listed with the reason it is no key. The doctor's "report withholding" check fails when the reports login can read a withheld name and warns when a withheld name no longer exists; the start logs either. Narrows D84.** Staff may run any saved report, and a report reads the tables directly, around every screen. The person a key is for has it in their inbox or browser, so the log has no use for it, and a resend would send `(hidden)`; they ask for a new one. A four-digit code's hash is guessed at once, so the code tables are withheld rather than hashed with a key. `idempotency_keys` whole because a checkout's kept answer carries the guest's order token and the provider's payment details, and nothing else in it is a report's; `api_keys.secret` and `plugins.settings` because with no `GOCOMMERCE_SECRETS_KEY` they hold the keys as they are. Event payloads, in the outbox and in each webhook delivery, because `cart.abandoned` carries the basket's token for the recovery link and both are kept after they are sent; a subscriber still receives it. A column rather than its table where reports need the rest of the row: a report on carts or orders still runs when it names its columns, and only `SELECT *` there is refused. A table, not a list in code, so a module's tables stay withheld on an install that drops the module. Withholding is by name, so a migration that renames a withheld column withholds the new name too; the doctor's warning is the backstop. An older version knows none of this and grants the whole table again when it starts: during a rolling deploy, or after a rollback, saved reports read these columns until this version starts again, and the doctor fails until it does. Reads of the log still hide by name too, for a module that has not named its keys. Writing the log hides only what each sender's `Secrets` lists, not every key named like a credential; `gctest.AssertNotificationLogHides`, which reads the stored row, is the backstop for a sender that forgets one. Migration 0078 loads one store's matching rows at a time. |
| D121 | One shopper, one reminder sequence | **Before a basket reminder goes out, a basket whose address (compared without case) has a newer basket still being chased, or one that was bought, stops with the reason `newer_basket`; the newest carries the sequence alone.** Reported by the owner on 2026-10-25: three baskets under one address sent every reminder three times in the same minute. Held rather than deleted, so the older baskets still show on the screen with the reason. |
| D122 | Basket reminders read like the jewellery store's | **The three default basket reminders take the wording of the reminders the owner's jws storefront already sends: the shopper's first name (from their newest order), each item with its price, the total, one link back and a line saying who to ask.** The owner, 2026-10-25: "The content is wrong as well. Copy the abandoned cart emails from jws." Those are designed HTML; the engine's email is plain text, so the words and order are copied and the layout is not. Amounts are written by `FormatMoney`, for message text only (rule 6 still holds for the API). |
| D123 | Phone first, then "already have an account?" | **On a store that signs in either way (`email_or_phone`), a right code for a phone with no account opens none when the storefront asks (`ask_if_new` on `/x/identity/otp/verify`): it answers a 10-minute proof (`new_phone`, `phone_token`). The shopper then signs in with their email and password plus the proof, which makes the phone a way into that account, or opens a new account with it (`/x/identity/otp/new-account`).** The owner asked for Myntra's way of handling both: Myntra asks for the phone first and, for a new number, offers "Already have an account?" to sign in the old way and attach the phone. Without it a customer who signed up by email and later signs in by phone gets a second, empty account, with no orders and no addresses, and merging two accounts is not something the engine does (D118). The question comes only after the code, so it says nothing about who has an account to somebody who does not hold the phone. The proof sits beside the phone's codes, so asking for a new code retires it. A client that does not send `ask_if_new` gets the old answer, and a phone-only store never asks: it has no email to ask about. |
| D124 | Sign-in codes fill themselves in | **The storefront names its host when it asks for a sign-in code (`domain` on `POST /x/identity/otp`); when that host is a verified domain of the store, or its subdomain of the base domain, the text ends with the line `@<host> #<code>` and the storefront asks Chrome for the code (the Web OTP API), so on Android it is filled in without typing. MSG91 gets the line as one variable, `web_otp`, beside `domain`. iPhones already offer the code from any text, because the code box is marked `one-time-code`.** The owner asked for codes to be read automatically. Chrome fills a code in only on the site the text names, so the engine names only a host it has verified as the store's: a host taken from the request as it stands would let any page have a shopper's code filled in for it. A host it does not know is left out, and the text then reads as before. With MSG91 the template decides the wording, so the line appears only once the store's DLT template ends with it. |
| D125 | The home page is a document the shop arranges | **`ext/layouts` keeps each store's home page as an ordered list of sections — announcement bar, hero, product grid, image with text, text, benefits — any type any number of times at any position, validated against a registry the panel draws its forms from. The shop edits a draft (a save names the version it read; a stale one is 409), previews it with a 30-minute key, and publishes it as an immutable revision in one transaction; rolling back publishes an earlier revision again. `GET /x/layouts/home` answers the published page with each product grid's products resolved live, and 404 while the `layouts` plugin is off or nothing is published, when the storefront draws its built-in home page. The plugin is on by default (owner, 2026-10-27: "can you make it the default"): with nothing published the storefront is unchanged, so being on costs a store nothing, and a switch the shop had to find first only stood between a publish and the page.** The owner asked for a no-code composer: any section at any position, repeated, reordered, previewed and published without a deployment (spec "ShopNX Dynamic Theme Builder & Visual Page Composer", Phase 1). This supersedes PLAN §1's "choose a theme and never touch code is a non-target", and sits beside D99 rather than against it: banners stay rows on the page record's side, and the arranged page is a separate document because its unit is the whole page — a publish swaps one page for the next, and history is whole pages. The document holds references, never commerce facts, so a price or stock level on the page is always the catalogue's. Settings are plain values of known kinds and links pass the banners' safe-URL rule: no merchant HTML, script or style reaches the storefront. Themes are named in the document but Phase 1 has one; a theme never owns the content, so switching looks later loses nothing. |
| D126 | The homepage editor saves itself and shows the real storefront | **Every edit to the home page's draft saves itself 800 ms after the last change and on leaving the screen, one save at a time, still naming the version it read (a stale one stops saving and offers a reload); there is no Save draft. `POST /api/admin/x/layouts/{page}/reset` makes the draft the published document again, or the starting page, and undo/redo walk the session's edits. On a computer the editor shows the store's own storefront in a frame (`?preview=<key>&editor=<panel origin>`), posts each edit to it before the save, and opens the section clicked there; the storefront honours `editor` only for a valid preview key, then allows framing by that origin alone, and both sides read messages only from the other's window and exact origin. The panel's CSP header lets frames in from the opened store's verified domains, its `<slug>.<base domain>`, and `GOCOMMERCE_CORS_ORIGINS`; the build-time meta policy allows any http(s) frame and leaves the narrowing to the header. A preview key before the first save shows the starting page.** The owner asked (2026-10-27) that "any change in theme must auto save, with a button to reset" and for a visual editor "like shopify". Autosave made Save draft a second, forgettable step; with it, Reset and undo are the ways back. Framing the real storefront rather than a copy in the panel keeps one renderer: what the editor shows is what shoppers get. The frame policy is per store because a policy naming every store's domains would grow without bound and widen every store's panel to every other's shop; CORS origins are included because the server's owner already named them as its storefronts. A posted draft is drawn by the same trusted components with the same link rule as a published one, so nothing new can reach a shopper, and the key is what turns the bridge on. Reset is recorded in `updated_by` like a save: modules have no writer for `admin_audit` (D26). |
| D127 | Theme settings ride with the home page; six more section types | **The store's design tokens — background, text, accent and its text, muted colours as `#rrggbb`; heading and body fonts from a fixed list of fifteen Google fonts; corner rounding none/small/medium/large — are `theme_settings` in the home page's document, validated by the same registry as sections, so one autosave, undo history, live preview, publish, rollback and reset cover both. Every one is optional and an unset one keeps the storefront's own value; nothing is invented. `GET /x/layouts/theme` answers the published tokens alone (`public, max-age=30`; a preview key's draft `no-store`) for the storefront to apply on every page as CSS custom properties over its theme; the panel warns, never refuses, when text on the background, on buttons or on the muted colour is below 4.5:1. New section types: slideshow (autoplay off by default, paused on hover and focus, never moving under reduced motion), collection list (resolved live, deleted ones dropped), video (YouTube or Vimeo by id only, loaded on press from youtube-nocookie.com or with Vimeo's do-not-track, or an MP4 from Files with controls), newsletter sign-up (offered and shown only while the `newsletter` plugin is on, through `SectionType.Requires`), questions and answers (native disclosures), columns (up to four). Plain text and the safe-URL rule everywhere.** The owner asked for the next phase of the theme editor after D126 (2026-10-27). Tokens are in the home document rather than a document of their own because the home page is the only document there is: a second draft would mean a second autosave, a second publish and two histories that can disagree about what is live, for no gain until another page template exists; when one does, the tokens move into a theme document and a migration copies them. A route of their own keeps every other page from fetching the home page's products to learn its colours. Colours are only `#rrggbb` and fonts only keys because the storefront writes them into a style sheet: nothing typed can reach it as written. Not in this phase: other page templates (product, collection), scheduled publishing, theme families and a marketplace. |
| D128 | Product picture shape and fit are theme settings | **Two more optional theme settings in a "Product pictures" group: `product_image_ratio` (square, portrait 3:4, tall 2:3, landscape 4:3) and `product_image_fit` (fill, which crops to the frame, or whole, which fits inside it). Keys only, never a CSS value, for the reason D127 gives colours and fonts.** The owner asked (2026-10-27) for the storefront's product picture shape to be set in GoCommerce rather than in the storefront's config. They ride with the other theme settings, so the panel, the publish and `GET /x/layouts/theme` carry them with no new route. |
| D129 | Product and collection pages are templates; any page can be scheduled | **`ext/layouts` keeps three pages: `home`, `product` and `collection`. The last two are templates, one per store, drawn for every product and every collection. Each holds its core once: `main-product` (the catalogue's gallery, title, price, options, buy box, specifications and description) or `main-collection` (the collection's heading and grid). The core has no settings, is refused when missing or doubled, and is saved shown. The shop moves it and arranges any section around it. `SectionType.Pages` limits a type to its pages. The product page adds reviews and related products (with an optional heading); the starting product page is core, reviews and related, and the starting collection page is its core alone, exactly the storefront's built-in pages. Theme settings stay in the home document, and a template's are dropped. `POST/DELETE /api/admin/x/layouts/{page}/schedule` sets or cancels one schedule per page. Scheduling copies the saved draft at the version named. The time must be in the future and at most a year ahead. A 30-second ticker (`OnStart`, `ForEachStore`, `SKIP LOCKED`) publishes it through the same `publish` as the button, as a revision marked `scheduled` by whoever set it. Publishing by hand, rollback and reset leave a schedule alone. Connector 0.9.0 adds `getProductLayout`, `getCollectionLayout` and `getPageLayout`. The storefront draws its own core where the template puts it, and renders exactly as before without one.** The owner asked for phase 4 of the theme editor (2026-10-27: "build"). The core is a section, not a fixed head and foot, so the shop decides what goes above the product (a banner, a promise) and what goes below. Being fixed and settingless keeps price, stock and description the catalogue's (D125). One template per store, because a per-product layout would be a second editor for each product, for a need nobody has stated. The description stays in the core: the buy box already draws it as an accordion, and a separate section would draw it twice. A schedule copies the draft rather than publishing whatever the draft holds at the time: with autosave the draft is always mid-edit, and what goes live should be what the shop looked at when it chose the time. The editor says when later edits are not in it. A schedule ignores manual publishes because each is its own intent: fixing a typo today should not call off Friday's sale page. Theme settings stay on the home page instead of moving to a theme document, as D127 planned once a second template existed. D127's reason for one document was that two drafts would mean two histories disagreeing about what is live, and that does not arise here: a template owns no tokens. Moving them would rewrite live data and the theme route for no change a shopper sees. The storefront keeps a preview's key in the tab's `sessionStorage`, because the product page rewrites its address to `?variant_id=…` as it opens and would otherwise drop the draft mid-edit. Not in this phase: per-product templates, the category listing (not a collection), theme families. |
| D130 | The storefront listing counts its own filters | **`GET /api/products?facets=1` adds `meta.facets`: counts per tag, vendor, product type, category (filed directly) and category attribute value, and the cheapest and dearest "from" price as money in minor units, over every product the listing matches rather than the page. Each group is counted without its own selection and with every other filter (disjunctive), and the price range without its own bounds. The public listing gains the filters to match: repeatable `tag`, `vendor` and `product_type` (values widen, groups narrow, as `attr` does), `category` as a slug (unknown is a 404), `price_min`/`price_max` in minor units on the cheapest variant, and `sort` over title, price, created and updated. `ProductQuery`'s `Vendor`, `ProductType` and `Tag` become the slices `Vendors`, `ProductTypes` and `Tags`, and the admin listing reads them repeated too. Each group is capped at its 100 most common values.** The owner asked (2026-10-27) for real listing filters on GoCommerce stores, with the engine counting the values itself. The storefront hid its filter rail because nothing supplied them, and a connector counting one page of hits would count the page, not the shop. Disjunctive because same-query counts collapse a group to the value just ticked, so a shopper could never tick a second vendor. Opt-in because the counts are six or more queries over the whole result set and every other caller of the listing wants none of them. Sort and the category slug arrive here so one route answers a filtered, sorted category page; until now a sort or a price range needed the Meilisearch module. Variant options (size, colour on variants) are not counted: `attr` does not filter on them, and a count that cannot be clicked is noise. |
| D131 | The header and footer are pages of the theme | **`ext/layouts` keeps two more pages, `header` and `footer`, each a document holding one fixed section of its own type and nothing else (`PageInfo.CoreOnly`), so the draft, autosave, undo, preview key, publish, revisions, rollback, reset and schedule are the ones every page has. Unlike a template's core the section has settings. The header: the announcement strip (shown or not, its words, a safe link, every page or the home page only) and the header menu. The footer: its menu, whose top-level links with links under them become columns and whose loose links share one; a short about text; the newsletter sign-up on or off; up to eight social links, each a network from a fixed list and an http(s) address; and the copyright line. A menu is named by its handle (`KindMenu`, ext/navigation's handle rule) and read live by the storefront, so editing it in Content → Menus needs no publish. `Field.Requires` gates a setting on a plugin as `SectionType.Requires` gates a section: offered only while the plugin is on, and left out of what the storefront is sent otherwise (the menus on `navigation`, the sign-up on `newsletter`). The starters are what the storefront shows today: the strip's built-in words on the home page only, over the `header` menu, and the `footer` menu. The published two are answered `public, max-age=30`. The panel lists Header and Footer under Themes and opens each in the page editor on its settings, with the home page in the frame and `preview_page` telling the storefront which part the key is for. Connector 0.10.0 adds `getHeader` and `getFooter`, and files the chosen menus under the `header` and `footer` ids the storefront already reads. Independently of the editor, the storefront's footer now prefers the store's footer menu, once it has links, over its built-in columns.** The owner asked how to change the header and footer, and chose (2026-10-27) to add them to Themes, edited in the same live-preview editor as the homepage, applied on every page and published like it. They are pages rather than a group of settings beside `theme_settings` because they are content the shop edits and publishes on its own schedule: on the home page's document a footer fix would publish half-finished home page edits with it, and a second set of tokens would need its own draft, history and schedule, which pages already have. One fixed section per page rather than free sections keeps the storefront's header — logo, search, account, basket — its own: what the shop sets is words, links and menus, never layout, and nothing typed becomes markup. One menu for the footer's columns rather than a list of menus, because the storefront already draws a menu's top-level links as columns and the Menus screen edits nesting; a second picker would be a second way to say the same thing. The menu is a handle, not copied links, so a menu fixed in Content → Menus is fixed everywhere at once. The logo's position is not a setting: the storefront draws the logo in one place, and a choice it ignores would be a promise it cannot keep. The footer menu now wins over the built-in columns because, the other way round, the menu a shop edited changed nothing a shopper saw. |
| D132 | Plugins are add-ons; built-in features live on their own screens | **A plugin is an optional third-party integration or add-on: analytics (Google Analytics, Meta pixel, Umami, Plausible), chat (Tidio, tawk.to, WhatsApp), marketing (Trustpilot, Klaviyo, IndexNow, product feeds, recent purchase popup), search (Meilisearch) and the rest (Shopify import, AI gateway, header scripts). A built-in feature is part of the product, configured on its own screen, and is not listed on the Plugins screen. `PluginDef.Home` names that screen: Banners and the theme editor under Themes, with hello bar, news ticker, trust badges and social sharing together as Themes › Storefront extras; Telegram alerts and the four notification senders under Notifications; the gateways and cash on delivery on Payment methods, the carriers on Shipping providers; menus, FAQ, video showcases, the contact form, the newsletter, reviews, wishlists and the sitemap on their own screens; customer sign-in, guest checkout, store closed and enquiry mode on Settings › Store. Each moved feature keeps its switch and settings form there. The row, sealed secrets (D110), the admin doors, `Fill`, the rights and `GET /api/plugins` are unchanged, so no store's settings or storefront change. Banners has no switch (`PluginDef.AlwaysOn`): on with no row, and a patch may not turn it off; a store whose row was off before keeps it off and gets one switch on the Banners screen to turn it on. Content → Banners moves to Themes › Banners and `/dash/banners` redirects there. Narrows D57.** The owner, 2026-10-27: "banners must be merged inside theme editor and they must not be an plugin, its inbuilt feature. remove telegram alerts from plugins list as well. cleanup plugins: there are many such modules inside plugin which is a part of the inbuilt features". D57 put every switch on one screen, and it grew to some sixty cards of which most were the product itself: a shop looking for its menus or its payment gateways found them among chat widgets. A field on the descriptor rather than separate tables, because the row is already where the switch, the settings, the secrets, the audit trail and the storefront's view meet; moving the screen is a panel change, moving the storage would be a migration of live credentials for nothing a shopper sees. Banners keeps a store's earlier "off" because taking it away would put pictures back on a storefront whose owner had hidden them; every other store simply has no switch, since each banner already has its own. The rest keep a switch, on their screen, because a store could reasonably have any of them off today. The hello bar overlaps the header page's announcement strip (D131); both stay, since stores use the hello bar today, and the extras screen says the strip is the one the theme editor previews. |
| D133 | Add-ons, suggestions, listed codes and the free-delivery bar | **A product offers up to ten add-ons, each another product's variant with an optional offer price (`product_add_ons`, `PUT /api/admin/products/{id}/add-ons`), and suggests up to 24 products (`product_cross_sells`). An add-on goes into a basket on a line of its own (`cart_line_items.add_on_id`, `POST …/line-items {"add_on_id"}`), beside any ordinary line of the same variant, and its offer price is part of `cartLinePriceSQL`: it holds while the add-on's product is in the basket and the line holds no more than there are of that product, never above the buyer's own price, and otherwise the line costs the variant's own. Adding one without its product, or more than there are of it, is a 409; every change to a basket settles its add-on lines in the same transaction, moving what no longer fits onto the ordinary line at its own price. An add-on with no offer price is added as an ordinary line. Withdrawing an offer is a price change, which checkout refuses until the shopper has seen it. A discount code the shop marks `listed` appears in `GET /api/carts/{cartId}/offers`, judged against the basket without claiming anything: what it takes off, or how much more the basket needs. `GET /api/carts/{cartId}/free-shipping` reads the shop's own rates for the destination (the store's country by default): free now, or the smallest basket at which a free rate starts. `GET /api/add-ons`, `GET /api/cross-sells` and `GET /api/carts/{cartId}/cross-sells` serve the storefront; the product page's "Complete the set" sits in the buy box and its "You may also like" is a product-page section (`cross-sells`) in the theme editor.** The owner asked (2026-10-27) for an order bump, cross-sells and the other conversion features of a reference Indian store. The price is the engine's because a checkbox on a storefront is only a request: a shopper who removes the product, or ticks one grip and buys ten, must not keep an offer made for one with each case, and only the basket knows. A line of its own rather than a flag on the variant's line, because one line has one unit price, and "one grip at the offer and two at their own" is two prices. `add_on_id` is not a foreign key so that the shop withdrawing an offer reprices baskets instead of deleting lines from them. Least of the offer and the buyer's price so that a trade buyer never pays more for taking the offer. Listing a code is the shop's opt-in per code because a list of every code would hand every shopper every code, which is why the connector had none. The free-delivery figure is read from the rates rather than a setting of its own so there is one number to keep true; a basket priced as a customer group's is told only whether it qualifies, because its group's rates are its own arrangement. The add-ons are in the buy box, not a movable section, because ticking one is part of pressing Add to cart; the suggestions are curated only, and a product with none shows nothing there, because the related products section already fills in from the category and drawing the same fill twice is noise. The basket's suggestions fall back to the first item's related products in the connector. Store-wide basket suggestions, a sold count, highlights, a free-gift line, product video, the PIN-code lookup and the saved address's label are in `docs/superpowers/specs/2026-10-27-conversion-features-design.md` as remaining gaps. |
| D134 | Highlights, a free gift, videos, the sold count, basket suggestions and PIN codes | **The second round of D133's conversion features. A product's `metadata.highlights` (up to 8 lines) and `metadata.free_gift` (one line) are the engine's own keys: plain text, 80 characters a line, trimmed, an empty one removed, anything else a 400; the panel's product editor has a Highlights card for both and the storefront draws a ticked list above the description and a gift badge under the price. The public product carries `media`, its pictures and videos in order, and the connector puts the videos after the pictures in the gallery list the storefront already plays. **Sold count** (`sold-count`, Themes › Storefront extras, off by default, threshold 25) answers `sold_at_least` on the public product: units on orders confirmed, part shipped, shipped or delivered, not refunded and not sample orders, recounted by the sweeper into `product_sales_counts` every five minutes, rounded down to tens under 100, hundreds under 1,000 and thousands above, and absent under the threshold. **Basket suggestions** (`cart-suggestions`, same screen) names one collection whose products `GET /api/carts/{cartId}/cross-sells` appends after the curated ones; plugin fields gain the `collection` kind, a picker in the panel stored as the handle. `GET /api/postcodes/IN/{pin}` answers city (India Post's district) and state with its D72 code from the post office directory the install points `GOCOMMERCE_POSTCODES_FILE` at; without one it is a 404 and nothing is filled. The storefront fills the city and state from six digits only where they are empty or still hold an earlier lookup, asks for the PIN first on the guest checkout, and the address book saves an address as Home, Work or Other (`label`). An option with more than 12 values is a searchable picker, and an option's values are judged against the options before it only.** The owner asked (2026-10-27) for the reference store's remaining features. The highlights and gift are metadata keys rather than columns because they are a few lines of the shop's words on one product, read wherever the product is, and the engine owns the keys so the limits hold for every client, not only the panel. The gift is a note, not a free line in the basket: a line needs stock and fulfilment of a zero-price item, which is an add-on at price 0 that adds itself, and can follow when a shop asks to track the gift's stock. The sold count is counted and never estimated because a made-up "10k+ sold" is a false claim on the shop's behalf; bands round down so the claim is always true, and a table refreshed by the sweeper because a page view must not add up forty thousand order lines. Its setting and the basket suggestions are built-in features on Storefront extras, not Plugins entries (D132), and neither setting is public because the engine applies both. The PIN lookup reads a file rather than a service: a stranger's API on every keystroke is slower and sees every address typed, and the directory is the government's to publish under its own licence, so the install fetches it rather than this repository shipping a copy; the state alone was not derived from the PIN's first digits because the postal circles do not follow state borders (Goa under Maharashtra, Puducherry under Tamil Nadu), and a confidently wrong state is worse than an empty one. Judging an option only against the earlier ones was needed for "Brand, then Model": against every other option, a chosen model made every other brand unavailable and the brand could never change; picking an earlier value now moves the later ones to a combination that exists, which is what the variant lookup already did. |
| D135 | Four import modes, and many files in one go | **A keyed import takes `mode=upsert|insert|update|delete` (`ImportOptions.Mode`, `ImportMode`). Products key on the slug (Handle in Shopify's file) and categories on their path; both take all four, upsert the default. Insert creates only what is new and leaves the rest untouched; update changes only what exists and reports a missing key as a row error; delete deletes what the file names through the same transaction the delete button runs (`Catalog.DeleteProduct`, with its audit row and `product.deleted` event; `Categories.Delete`, which refuses a category with subcategories or products), and a missing key is a row error. A delete file needs only the key column, and categories are deleted deepest first. Orders take insert only and inventory update only; any other mode is a 400 rather than read as some other one. Every mode holds under `dry_run`, which makes the same writes and rolls them back, and the result gains `deleted`. `overwrite=false` is read as `mode=insert` when no `mode` is sent; `ImportOptions.Overwrite` is gone. The Import screen offers the modes a kind takes and no others, runs a dry run before a delete and asks to confirm the count it found, and takes several CSV files at once, dropped or picked, imported one after another under the chosen kind and mode with a result each; a file that fails does not stop the next. Narrows D62: the category file still cannot move a category, but it can delete one.** The owner asked (2026-10-28) for "All four: Insert only, Update only, Upsert, Delete", with Delete asking first, and for several files dropped at once, "one after another… a failing file does not stop the rest". A mode rather than more switches because the four are one choice: `overwrite` was a second boolean that could only ever say two of them. Delete goes through the services' own transactions so the audit row and the event commit with the delete (rule 4), and a dry run runs that same delete and rolls back, so the count the confirmation names is the one Import will produce. Category delete refuses what is not empty because D62's worry — one typo dragging a subtree — would otherwise come back as one row deleting a branch; a typo now names a path that does not exist and deletes nothing. Files run one after another rather than at once because the importers already assume one file at a time against one catalogue, and a later file usually builds on an earlier one. |
| D136 | India Post's PIN code directory is built in | **The engine carries India Post's whole "All India Pincode Directory" (data.gov.in, Government Open Data Licence – India), reduced to one row a PIN — the district most of its post offices name, and the state — and gzipped into `core/postcodes_in.csv.gz`, read on the first lookup. `GOCOMMERCE_POSTCODES_FILE` now replaces it: a path reads a newer copy of the government's CSV or the compact form, and `off` turns `GET /api/postcodes/IN/{pin}` into a 404. `go run ./scripts/pincodes -in <csv>` rebuilds the built-in file, and the same CSV gives the same bytes. Every state in it must have a D72 code, which a test holds. Rows where India Post wrote "NA" for the district and state are skipped, so they cannot outvote the offices under the same PIN that do say; the hundred or so PINs with only such rows are not in the directory, and there the shopper types the city as before. Narrows D134.** The owner, 2026-10-28: "implement the entire india post file for pincode check". D134 left the file for each install to fetch, which in practice meant no store had the lookup: nobody downloads a government CSV to make a checkout field fill itself. The licence allows redistribution with attribution, which the rebuild script and this row give. Built in rather than fetched at start because an engine that reaches out to a government portal on boot fails when the portal does, and the portal is often down. Compact because the government's file is a row per post office, 165,627 rows and 23 MB, of which a checkout needs one district and state per PIN: 19,486 rows, 56 KB compressed. Read on first lookup rather than at start because most processes that call New, tests and one-off commands, never look one up. |
| D137 | Indian stores issue GST tax invoices | **A store whose profile is in India, whose tax registration parses as a GSTIN (pattern, state code, check character) and which sells in INR issues a GST tax invoice (CGST Rules 2017, rule 46) for every paid order instead of the plain one. It is built once at issue from what checkout charged and stored as one versioned document (`invoices_documents.tax_doc`): supplier from the store profile, buyer GSTIN from `metadata.gst` (written by `ext/b2b` from a company's valid GSTIN, checked by a checkout guard on GST stores), HSN from the variant's HS code (now 4 to 10 digits), place of supply from the delivery address, CGST + SGST (UTGST in AN, CH, DH, LD, LA) within a state and IGST between states and for exports, a rate-wise summary and totals. A tax rule may target one product, which outranks any category rule, so mixed rates (3% gold, 18% the rest) come from checkout. The serial restarts each financial year (1 April, store clock, India's by default), `{fy}` prints "26-27", and a number that breaks GST's 16 letters, digits, `/` and `-` is refused, not issued. Every other store's invoice is unchanged.** The owner asked on 27 Oct 2026 for what Indian B2B suppliers expect on an invoice. Rounding: checkout's tax per line is final; within a state CGST is the floor of half and SGST/UTGST the rest, so the odd paisa goes to the state and the heads always add up to what was charged; taxable value is the line less its discount share, less the tax when prices include it; a document whose lines do not sum to the order total, or whose Indian place of supply cannot be placed, is not issued (a wrong legal document cannot be taken back; a late one is logged and retried). Freight is printed as an untaxed line with a `freight_untaxed` warning, because core does not tax shipping and the invoice cannot show tax nobody paid; that gap stays open for the owner's CA. E-invoicing (IRN, signed QR) and e-way bills are left out: both need a GSP or IRP account, and they would attach to the frozen document later rather than rewrite it. Credit notes, bills of supply, cess and units other than NOS are not modelled. |
| D138 | Invoices export to TallyPrime as XML vouchers | **A GST store's operator downloads a period's tax invoices (dates inclusive on the store's clock, less than a year at a time) as TallyPrime "Import Data" XML in two files, ledgers then vouchers, from `GET /api/admin/x/invoices/tally` (`invoices.read`). The export reads `invoices_documents.tax_doc` and nothing else: no tax arithmetic, no order or profile lookup. Each invoice is one Sales accounting invoice (Invoice Voucher View, no stock items) with REMOTEID `gocommerce:<store>:<number>`: the party debited the total with a New Ref bill, sales (one ledger, or one per rate), CGST, SGST or UTGST, IGST and delivery credited, in rupees from integer paise; a voucher whose entries do not sum to zero is refused. A registered buyer is a Sundry Debtors ledger named legal name plus GSTIN; every unregistered buyer posts to one configured party. Ledger names are one row per store (`invoices_tally_ledgers`), set under the new `invoices.configure` right (managers by default). Other stores get 404.** Planned with D137 so a GST store's invoices reach an accountant's TallyPrime books without being typed in again. Two files because Tally imports masters before transactions and sends a voucher with a missing ledger to its exceptions report; REMOTEID because without it a second import duplicates. Tally's official help verifies the envelope, the ledger and voucher tags, the import order and the duplicates rule. It does not show the GST registration details on a party ledger or a voucher's GST tags as XML, so the party ledger carries none, the voucher's GST tags follow the R3.0 field names, and the accountant sets each tax ledger's duty head in Tally once. Tally's own exports are UTF-16; this file is UTF-8 with everything outside ASCII as character references. All three gaps close with one test import into a copy of the owner's company, which has not been done. Excel import (Tally 4.0+) and Tally's HTTP port were not used: a file the accountant imports needs no connection to their machine. |
| D139 | The engine resizes a store's own pictures | **`GET /images/{store}/cdn-cgi/image/{options}/{path}` serves one of the store's pictures scaled down, in Cloudflare Image Resizing's URL shape, and `GET /api/store` names the prefix as `images_path` (`/images/<store id>`); the connector turns the storefront's `imageCdn` plugin on with it, so the srcset the storefront already builds is answered with no change to the storefront. Only `width=` is read, rounded up to 160, 240, 320, 480, 640, 768, 1024 or 1280; the aspect ratio is kept and nothing is enlarged (an original that narrow is served as it is). JPEG and PNG are resized, a PNG with transparency stays PNG, everything else becomes JPEG at quality 80; WebP, AVIF and GIF answer 302 to the original. The original's host is not in the URL, so `path` and the query are matched exactly against the store's own image addresses — its media library and its category pictures — and nothing else is fetched: a path no picture of the store has is a 404. A remote original is fetched through `OutboundClient` (D109, 15 s), at most 20 MB, refused past 40 megapixels on its header before decoding, at most four resizes at once; an upload is read from disk. Each rendition is written once to `<MediaDir>/.cache/images/<store>/<hash>` (the system temporary directory without a media directory) and served with `Cache-Control: public, max-age=31536000, immutable` and the hash as ETag.** | A product card 180 px wide downloaded the 925 px, 223 KB original; Lighthouse put the waste at 389 KB a page. The storefront's image component asks for a resized srcset only when its store record turns `imageCdn` on, and builds Cloudflare's URL from it, so answering that shape needs no edit to the package-owned component; the generic provider was not usable, because it drops the width. Matching the path against the store's own pictures is what keeps this from being an open proxy: an address the store never showed is never fetched, and the eight widths cap the cache at eight files a picture. The scaling is a box filter written here (an area average, as sharp as bicubic for shrinking and free of its ringing), so rule 1's one production dependency holds; `golang.org/x/image` would add WebP decoding and is the step to take if WebP originals turn out to be common. Pictures that live only in a module's tables (layouts, banners) are not matched yet: core cannot read them, and a module would need a hook to vouch for its own addresses. A storefront that reaches the engine through a proxy path gets site-relative upload URLs, which the storefront's URL builder cannot parse, so uploads there are not resized; linked pictures (every imported one) are. |

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

Shipped tools — 19, each calling the domain service the equivalent REST route
calls, each naming that route's rights, and each marked `Mutates` so `ReadOnly`
can withhold it:

```text
read                            write
store_info                      update_variant_inventory
store_health                    mark_order_paid
list_products                   cancel_order
get_product                     create_fulfillment
list_low_stock_variants         mark_order_delivered
list_orders                     create_product
get_order                       update_product
list_customers                  set_variant_price
sales_report                    create_discount
                                refund_order
```

Money crosses this boundary as whole minor units and `set_variant_price`
refuses a decimal rather than truncating one: an agent sending `19.99` where
`1999` was meant would otherwise price the variant at nineteen cents, silently.

Potential later tools:

```text
create_purchase_recommendation
reconcile_orders
get_customer
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
gocommerce/                      # ONE Go module: github.com/itswadesh/gocommerce
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
    ├── payments-paddle/          # merchant of record: hosted checkout + adjustments
    ├── payments-lemonsqueezy/    # merchant of record, digital goods
    ├── payments-adyen/           # payment links + signed notifications
    ├── payments-hyperswitch/     # a router in front of many gateways
    ├── payments-helcim/          # HelcimPay.js, North America
    ├── payments-clover/          # Hosted Checkout, United States and Canada
    ├── payments-revenuecat/      # Web Billing links; no refund API exists
    ├── notify-sendgrid/   notify-msg91/
    ├── fulfill-shiprocket/      # India: aggregator, waybills
    ├── fulfill-delhivery/       # India: a JSON payload inside a form field
    ├── fulfill-nimbuspost/      # India: aggregator, 200 + status:false
    ├── fulfill-indiapost/       # records a booking; DoP has no public API
    ├── fulfill-shippo/  fulfill-shipstation/  fulfill-easyship/
    ├── fulfill-shippit/         # AU/NZ; the one API that reads metres
    ├── fulfill-usps/            # the only one that stores its own labels
    ├── fulfill-onfleet/         # your own drivers, not a carrier
    ├── fulfill-veeqo/           # pushes the shipment back into Veeqo
    ├── translations/            # the Translator port, filled in
    ├── import-amazon/           # real Chrome over CDP, stdlib WebSocket; no SDK
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
9. multi-tenant mode — decided in D70, schema per store;
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
is refused rather than silently stripping them. A variant shows some of its
product's pictures, in its own order (`variant_media`, M36), and several
variants may show the same one — every size of a colour shows that colour's
photographs — with `Variant.Image` kept as the first of the list.

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
