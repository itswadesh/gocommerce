# GoCommerce

**Commerce primitives in Go, without a commerce monolith.**

GoCommerce is a small, composable commerce engine. It owns the hard parts —
products and variants, inventory, carts, checkout, orders, payments,
fulfillment and durable events — over a PostgreSQL database. Integrations are
ordinary Go packages you wire together in `main()`.

The project's page is [kitcommerce.store/gocommerce](https://kitcommerce.store/gocommerce/),
with every screen of the [admin panel](https://kitcommerce.store/gocommerce/admin/),
the [modules](https://kitcommerce.store/integrations/) and the
[features](https://kitcommerce.store/features/).

> **Status: 1.0.** The engine and the modules below are implemented and
> tested. The exported API and the HTTP contract are stable and breaking
> either takes a major version. See [PLAN.md](PLAN.md) for the architecture
> and the decisions behind it.

## A store is a Go program

```go
app, err := gocommerce.New(
    gocommerce.Config{
        DBURL:       os.Getenv("DATABASE_URL"),
        Currency:    "USD",
        AdminTokens: []string{os.Getenv("GOCOMMERCE_ADMIN_TOKEN")},
    },
    stripe.New(stripe.Config{
        SecretKey:     os.Getenv("STRIPE_SECRET_KEY"),
        WebhookSecret: os.Getenv("STRIPE_WEBHOOK_SECRET"),
    }),
    sendgrid.New(sendgrid.Config{
        APIKey: os.Getenv("SENDGRID_API_KEY"),
        From:   "orders@example.com",
    }),
    invoices.New(invoices.Config{SellerName: "Example Ltd"}),
)
if err != nil {
    log.Fatal(err)
}
log.Fatal(app.ListenAndServe())
```

The engine imports as `github.com/itswadesh/gocommerce/core`; the package is
still named `gocommerce`, so the code reads as above. The old root import path
no longer resolves as a package — ignore any stale pkg.go.dev page for it.

No plugin registry, no dependency-injection container, no reflection, no
configuration DSL. Everything a store runs is on that screen, and "go to
definition" works on all of it.

## The design in one page

**One binary, one PostgreSQL database.** That is the whole production
architecture. Redis, search and object storage are options you add when traffic
justifies them, never prerequisites.

**Durable events, not fire-and-forget.** A state change and the event
describing it are written in the same transaction, to an outbox table. A
process that dies between the commit and the publish loses nothing. A
dispatcher claims unsent rows with `FOR UPDATE SKIP LOCKED`, so several
instances never deliver the same event at once. Delivery is at-least-once, so
consumers are idempotent by contract — see [docs/events.md](docs/events.md).

**Variants are first class.** A variant is the sellable unit: SKU, price, stock
and order lines all hang off it. A product with no options still has one
default variant, so the simple case stays simple without a flat schema that has
to be torn up later. Two variants cannot claim the same option combination —
enforced by a unique index, not by hope.

**One state machine.** REST, a payment webhook, a CSV import, an MCP tool call
and an admin action all end up in the same domain service. No integration gets
to invent its own `MarkPaid`.

**Modules integrate; core decides.** A module owns its own tables, mounts
routes in its own namespace, and provides payments, fulfillment or
notifications. It may not write core commerce tables — it calls a service,
which performs the transition and writes the event.

**One catalogue, several prices.** A price list can narrow to a customer
group, a quantity break, a date window or a storefront — resolved in one place,
so what a shopper is charged has a single answer. Channels differ in what is
published and what it costs, never in currency: the store settles in one, and
every order snapshots it.

**Guest checkout, permanently.** A shopper buys with a cart token and an email.
The `identity` module adds accounts on top of that; it may never make one
required.

## What is in the box

The engine, plus these modules — each an ordinary package under `ext/`, each
adding **zero third-party dependencies** (they talk REST over `net/http` and
verify HMACs with `crypto/hmac`, so no vendor SDK enters your dependency
graph):

| Module | Provides |
|---|---|
| `payments-stripe` | Card payments, signed webhooks, refunds |
| `payments-razorpay` | Cards and UPI, hosted or in-page |
| `payments-paddle` | Merchant of record: hosted checkout, adjustments as refunds |
| `payments-lemonsqueezy` | Merchant of record for digital goods, refunds |
| `payments-adyen` | Payment links, HMAC-signed notifications, refunds |
| `payments-hyperswitch` | One integration in front of many gateways |
| `payments-creem` | Merchant of record for digital goods; refunds in full only |
| `payments-helcim` | HelcimPay.js in-page checkout, North America |
| `payments-revenuecat` | Web Billing purchase links; digital goods, no refund API |
| `notify-resend` | The store's email through Resend, and the one to reach for first: an API key is the only required setting, and until a domain is verified it sends from Resend's own onboarding address |
| `notify-sendgrid` | The store's email through SendGrid — the key from the environment or from Notifications › Setup Email, the wording from the panel |
| `notify-twilio` | The store's SMS through Twilio — plain text, so the wording is the store's own under Notifications › Setup SMS and nothing has to be registered with a carrier first |
| `notify-msg91` | The store's SMS through MSG91's DLT templates — the key and the template ids from the environment or from Notifications › Setup SMS |

Every gateway and carrier module installs idle: `-gateways` and `-carriers` put all of them in the binary with nothing in Config, and Settings › Payment methods and Settings › Shipping providers switch each one on and take its keys. A store that prefers the environment wires the module itself with a Config.
| `fulfill-shiprocket` | Booking shipments and waybills, India |
| `fulfill-delhivery` | Manifesting parcels with Delhivery |
| `fulfill-nimbuspost` | NimbusPost's courier aggregation |
| `fulfill-indiapost` | Recording a consignment handed to India Post |
| `fulfill-shippo` | Multi-carrier labels through Shippo |
| `fulfill-shipstation` | Multi-carrier labels through ShipStation V2 |
| `fulfill-easyship` | Cross-border labels and customs through Easyship |
| `fulfill-shippit` | Shippit's carrier allocation, Australia and NZ |
| `fulfill-usps` | USPS labels bought directly, served to print |
| `fulfill-onfleet` | Dispatching your own drivers, last mile |
| `fulfill-veeqo` | Telling Veeqo a parcel went out |
| `invoices` | Numbered, gapless invoices on payment |
| `cms` | Content pages, per language |
| `translations` | Catalogue content in the language a shopper asked for |
| `identity` | Shopper accounts: sessions, saved addresses, order history, password reset |
| `cart-recovery` | Chases an abandoned basket with a link back to it |
| `import-amazon` | A product, its variations and pictures from an Amazon URL, through a real Chrome; copy rewritten by Claude |
| `mcp` | The store as tools for an AI agent, with an audit trail |
| `search-meilisearch` | A Meilisearch index kept in step with the catalogue, and the storefront's search through it |
| `klaviyo` | Orders and abandoned carts as Klaviyo events, under the metric names its flows know |
| `feeds` | Google Merchant Center and Meta catalogue feeds, generated from the live catalogue |
| `sitemaps` | The storefront's sitemap: products, collections and content pages |
| `navigation` | The storefront's menus — trees of links to products, collections, categories and pages — edited from the panel |
| `reviews` | Product ratings and reviews, verified against the store's own orders, moderated from the panel |
| `contact` | The storefront's contact form and the inbox behind it |
| `newsletter` | The storefront's signup box, its list, and its unsubscribe link |
| `faq` | The questions a shop is asked often, grouped and ordered by hand, served at `/x/faq` |
| `wishlist` | Shoppers save products; the panel ranks what is wanted most and what of it is out of stock |

A module that ships an admin surface also ships its panel screen: install
`cms`, `invoices`, `identity` or `mcp` and the screen appears in the
navigation; leave it out and it does not.

Cash on delivery and manual fulfillment are built in, because they need no
third party — a store can sell and ship before it has integrated anything.

## API

Unversioned, JSON, and compatible with the useful overlap of the Litekart API
so existing storefronts can point at it with minimal changes.

```
GET  /health              GET /health/ready
GET  /doc                 the OpenAPI contract this build serves
GET  /docs                a browsable reference

GET  /api/products        /api/products/{id}  /api/products/slug/{slug}
                          /api/products/sku/{sku}
POST /api/carts           /api/carts/{cartId}/line-items
POST /api/checkout/{code} /api/checkout/{code}/webhook
GET  /api/orders/{number}?token=…

     /api/admin/products  /api/admin/orders  /api/admin/create-fulfillment
     /api/admin/import/…  /api/admin/export/…
```

Every response is `{"data": ...}` or `{"error": {"code", "message"}}` — including
the ones that miss, so a client decoding JSON never gets a plain-text 404
instead.

Every list paginates, either way you like: `?limit=20&offset=40` for a cursor,
`?limit=20&page=3` for a page number counting from 1. They describe the same
window, and when a request carries both, `page` wins. The `meta` block reports
both, so a UI can draw "3 of 12" without doing arithmetic:

```json
{ "data": [ ... ],
  "meta": { "total": 240, "limit": 20, "offset": 40, "page": 3, "total_pages": 12 } }
```

Money is always `{"amount_minor": 1999, "currency": "USD"}` — an integer and a
code, never a float and never a formatted string, because the decimal places
belong to the currency and the symbol belongs to the reader.

A test asserts that every route the engine mounts appears in `/doc`, so the
contract cannot quietly drift from the code.

### Storefronts

The API is the only thing a storefront needs, and anything that speaks HTTP will
do. For [Svelte Commerce](https://github.com/itswadesh/svelte-commerce) there is
a connector in this repository —
[`connectors/svelte-commerce`](connectors/svelte-commerce) — which maps the
storefront's expectations onto these routes: catalog, carts, checkout and order
lookup. It is deliberately loud about what this engine does not have (accounts,
blogs, wishlists, a search index) rather than answering those with empty lists.

## Quick start

Requires Go 1.23+ and PostgreSQL 16+.

```sh
createdb mystore
export DATABASE_URL=postgres://localhost/mystore
export GOCOMMERCE_ADMIN_TOKEN=$(openssl rand -hex 32)

go run ./cmd/gocommerce serve
```

Then sell something:

```sh
# Add a product
curl -X POST localhost:8080/api/admin/products \
  -H "Authorization: Bearer $GOCOMMERCE_ADMIN_TOKEN" \
  -d '{"title":"Cotton tee","status":"active","sku":"TEE-001","price_minor":2500,"stock":10}'

# Shop
CART=$(curl -sX POST localhost:8080/api/carts | jq -r .data.id)
curl -X POST localhost:8080/api/carts/$CART/line-items \
  -d '{"variant_id":1,"quantity":2}'

# Check out with cash on delivery
curl -X POST localhost:8080/api/checkout/cod \
  -H 'Idempotency-Key: demo-1' \
  -d '{"cart_id":"'$CART'","email":"buyer@example.com",
       "address":{"line1":"1 High St","city":"Town","postal_code":"12345","country":"US"}}'
```

## The admin panel

One executable serves both the API and a full admin panel. Run the binary,
open `http://localhost:8080/`, and you have a dashboard, product and order
management, inventory, CSV import/export for products, inventory, orders, the
category tree, reviews and menus (products, inventory and orders in the
store's own layout or Shopify's, so a Shopify export imports as it is),
settings and an events
screen for what the outbox could not deliver — with no separate process, no
Node.js on the server and no configuration beyond a database URL.

The panel owns the root, because the API is namespaced under `/api` (plus
`/health`, `/doc`, and a module's `/x/`) and nothing else wants that URL. The
consequence, stated plainly: this binary cannot also host a storefront at `/`.
A headless store's storefront is a separate application anyway — give it its
own origin, or put a proxy in front.

```powershell
.\scripts\build.ps1        # builds the panel, then embeds it in the binary
.\gocommerce.exe -db "$env:DATABASE_URL" -admin-token <token> serve
```

The panel is a SvelteKit single-page app compiled to static files and embedded
with `go:embed`. It is a client of the same public API as anything else: it has
no private endpoints, and everything it does you can do with curl. Sign-in is
the store's admin token, held in the browser and sent as a bearer token — there
is no session to expire, because there is no session.

Its design is a deliberate, close port of the
[PocketBase](https://github.com/pocketbase/pocketbase) dashboard: the same
IBM Plex Sans and Plex Mono, the same Remix Icon set, the same design tokens
(`#1055c9` accent, 5px and 15px radii, 45px controls, a 30/20 spacing scale),
and the same interaction grammar — hover moves one surface step at 150ms, a
press moves two at 70ms, panels arrive as a right-hand drawer, toasts rise from
the bottom and pause when you hover them. Fonts are self-hosted, so the panel
works on a machine that has never seen the internet.

Building `-tags no_admin` produces an API-only binary that carries no
JavaScript and leaves the root free.

## Running and testing a dev store

```powershell
.\scripts\dev.ps1 -Seed       # create the database, build, serve, load a demo catalog
.\scripts\dev.ps1 -Reset      # start again from empty
```

Then open `http://127.0.0.1:8080/` and sign in with the dev token
(`dev-token` unless you passed another).

Then pick whichever way of poking at it suits you:

**In a browser.** `http://127.0.0.1:8080/docs` renders the contract as a
browsable reference you can send requests from. `/doc` is the raw OpenAPI
document behind it.

**In your editor.** [`api.http`](api.http) is a working request collection —
open it in VS Code with the REST Client extension, or any JetBrains IDE, and
send requests inline. It threads ids from one response into the next, so the
cart and checkout blocks run in sequence, and it ends with a set of requests
that are *supposed* to fail so you can see the error shapes.

**As a script.** `.\scripts\smoke.ps1` walks a running store through a complete
sale and checks what each step should have changed — 36 assertions covering
variant uniqueness, stock movement, idempotent checkout, guest order access,
the refund refusal, CSV round-trip and contract coverage. It creates its own
product and removes it afterwards, so it is safe to re-run against a store with
data in it.

**With curl.**

```sh
curl localhost:8080/api/products

CART=$(curl -sX POST localhost:8080/api/carts | jq -r .data.id)
curl -X POST localhost:8080/api/carts/$CART/line-items -d '{"variant_id":2,"quantity":2}'

curl -X POST localhost:8080/api/checkout/cod -H 'Idempotency-Key: demo-1' \
  -d '{"cart_id":"'$CART'","email":"buyer@example.com",
       "address":{"line1":"1 High St","city":"Town","postal_code":"12345","country":"US"}}'

curl -H 'Authorization: Bearer dev-token' localhost:8080/api/admin/orders
```

## Development

```sh
go build ./...
go vet ./...
go test ./...        # database-backed tests skip without a test database
```

For the full suite, point `GOCOMMERCE_TEST_DB` at a scratch database. Each test
gets its own PostgreSQL schema, so packages running in parallel cannot tread on
each other; the database name must contain `test`:

```sh
createdb gocommerce_test
GOCOMMERCE_TEST_DB='postgres://localhost/gocommerce_test?sslmode=disable' \
  go test ./... -race -count=1
```

The suite includes the engine's reliability requirements as tests: concurrent
checkouts cannot oversell, a replayed webhook cannot double-settle, a
rolled-back transaction leaves no event, a failed consumer is retried rather
than losing one, and cancelling returns exactly the stock it should.

## Operational diagnostics

```sh
gocommerce doctor           # human-readable, exits non-zero if anything failed
gocommerce -json doctor     # the same report, for scripts and agents
```

The database and its pool, pending migrations, whether anyone can still
administer the store, outbox backlog and dead letters, stock held by orders
nobody will pay for, cart sweeping, catalog entries that cannot be bought,
provider registration, whether the served routes match `/doc`, admin routes
that name no right, orders whose status disagrees with their parcels, the
refund ledger, over-returned lines, the stock ledger and discounts that can
never apply. Every failure names what to do about it.

It is a core service (`App.Diagnose`), so the CLI, the MCP `store_health` tool
and anything else render the same report — including the panel, at
**Settings → Diagnostics**, over `GET /api/admin/diagnostics`. That screen adds
the other half: Run-now buttons for the two sweeps and the outbox drain, which
call the same passes the engine's own five-minute ticker calls. All four routes
are behind the `store.operate` right, which owner alone carries by default.

## AI-native by design

AI is a first-class developer interface here, in three layers:

- **Rules** — [AGENTS.md](AGENTS.md) holds the architectural guardrails, with
  the reason each one exists. [CLAUDE.md](CLAUDE.md) and
  [`.cursor/rules`](.cursor/rules) point at it and add only tool-specifics.
- **Skills** — [`skills/`](skills/README.md) is task-scoped procedural
  knowledge, from checkout's two-phase transaction to the outbox's delivery
  guarantee, each page saying when to reach for it.
- **MCP** — [`ext/mcp`](ext/mcp) exposes domain tools that call the same
  services REST does. It never exposes arbitrary SQL and never mutates core
  tables directly, so an agent operating a store is bound by exactly the
  invariants a person is.

## Documentation

- [PLAN.md](PLAN.md) — architecture, decisions and milestones
- [AGENTS.md](AGENTS.md) — the rules any contributor works under
- [skills/](skills/README.md) — procedural guides, one per task area
- [docs/events.md](docs/events.md) — the event contract and the outbox guarantee
- [docs/writing-a-module.md](docs/writing-a-module.md) — building an extension
- [docs/admin-panel.md](docs/admin-panel.md) — the embedded admin UI
- [docs/operations.md](docs/operations.md) — running it in production
- [`examples/store`](examples/store) — the shape of a real store's `main()`

## License

[MIT](LICENSE). Use it, fork it, sell what you build with it; keep the notice.
