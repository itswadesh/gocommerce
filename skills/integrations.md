---
name: integrations
description: Use when writing or changing a module — a payment gateway, carrier, notifier, translator, MCP tool or anything else that plugs into the engine through a port.
---

# Integrations

[`docs/writing-a-module.md`](../docs/writing-a-module.md) is the tutorial: the
smallest module, a payment method, a notifier, a webhook. Read it first. This
file covers what it does not — the exact lifecycle order, every port and its
implementations, and how the MCP surface is extended without widening it.

## The interface, and when each method runs

```go
type Module interface {
    Name() string                 // [a-z0-9-]+, unique within an app
    Migrations() []Migration      // applied after core's, in argument order
    Register(app *App) error      // called once, after every migration
}
```

`New` fixes the order and it is total:

1. Defaults applied, module names validated (`[a-z0-9-]+`, unique).
2. Database opened, services built, built-in providers registered.
3. **Every** migration applied — core's, then each module's in argument order.
4. Core routes mounted, so a core route always wins a conflict.
5. Each module's `Register`, in argument order. **Your tables already exist.**
6. OpenAPI fragments merged; background work scheduled.
7. `ListenAndServe` runs `OnStart` hooks, then accepts traffic.
8. Shutdown runs `OnStop` hooks in **reverse** order, then closes the pool.

`Register` is where everything is wired. Store `app.DB()`, `app.Log()` and the
bits of `app.Config()` you need on your struct.

Startup failures are surfaced, never swallowed. `Handle`, `RegisterPayment` and
friends cannot return an error, so they record one in `App.regErr` and `New`
returns it — a bad route or a colliding provider code fails the boot with a
message naming the module.

## The ports

Everything in `ports.go`. Nothing else in the engine is abstracted.

| Port | Method(s) | Registered with | Implemented by |
|---|---|---|---|
| `PaymentProvider` | `Code`, `Initiate` | `RegisterPayment` | built-in `cod`, switched by the built-in plugin `payments-cod` (D102); `ext/payments-{stripe,razorpay,cashfree,paddle,lemonsqueezy,creem,adyen,hyperswitch,helcim,clover,revenuecat}` |
| `WebhookProvider` | `Webhook` | *optional, detected* | every gateway above except `cod` |
| `Refunder` | `Refund` | *optional, detected* | every gateway except `cod` and `revenuecat` — RevenueCat has no refund API for Web Billing, so it does not claim one. `creem` claims it but refunds in full only: it refuses a partial request rather than returning the whole order while the books record a fraction |
| `ReferencedRefunder` | `RefundWithReference` | *optional, detected; a `Refunder` as well* | stripe, razorpay, cashfree, paddle, lemonsqueezy, adyen, hyperswitch, helcim, clover, creem — it returns the gateway's own refund id, which is recorded on the refund |
| `PaymentStatusReader` | `Status` | *optional, detected* | stripe, razorpay, cashfree — it reads back what became of an order's payment, for `POST /api/admin/orders/{id}/check-payment` (D112); the settings mark such a method `checks_payments` |
| `FulfillmentProvider` | `Code`, `Ship` | `RegisterFulfillment` | built-in `manual`; `ext/fulfill-{shiprocket,delhivery,nimbuspost,indiapost,shippo,shipstation,easyship,shippit,usps,onfleet,veeqo}`. A carrier quotes on `Ship().Parcel` before booking and reports a parcel it called off with `Ship().Cancel`, which keeps the shipment on the order as cancelled; `ext/fulfill-shiprocket` does both |
| `TrackingReceiver` | `TrackingHook`, `Tracking` | *optional on a `FulfillmentProvider`, detected* | `ext/fulfill-shiprocket` (hook `courier-aggregator`). Served at `POST /api/tracking-updates/{hook}` with no store bound: the handler binds the store from what authenticates the call, then reports through `Ship().Track`, which records the carrier's status, delivers the order with its last parcel and cancels a parcel the carrier called off |
| `Notifier` | `Notify` | `RegisterNotifier(channel, n)` | built-in log notifier; `ext/notify-resend` and `ext/notify-sendgrid` (email), `ext/notify-twilio` and `ext/notify-msg91` (SMS) |
| *(sending one)* | — | `App.Notify(ctx, n)` | D50. The engine delivers `order.*` itself and nothing else; a module that wants to write to a shopper about anything else calls this. `ext/cart-recovery` is the first caller |
| `Translator` | `Translate` | `RegisterTranslator` | `ext/translations` — products, and the names of categories and collections, in the language a shopper asked for (kinds `product`, `category`, `collection`, D87) |

One more optional capability lives in `openapi.go`: implement
`OpenAPI() []byte` and your fragment's paths and component schemas are merged
into the document served at `/doc`. A duplicate path or schema name is a startup
error rather than a silent overwrite.

Registration rules worth knowing before they bite:

- **Codes collide loudly.** A module may replace a built-in by reusing its
  code; two *modules* claiming one code is a startup error, because silently
  picking a winner would make it depend on argument order.
- **Notifiers append**, so a store can send email and mirror it to an audit
  sink; the built-in log notifier stands down for any channel that gets a real
  one. Channels are a closed set (`ChannelEmail`, `ChannelSMS`) because the
  dispatcher has to know which recipient field feeds one — a novel channel
  subscribes to the bus directly instead.
- **Only one translator.** Merging overlapping translations from several
  sources has no obviously correct answer, so the engine declines to guess.
- **`Subscribe` patterns** are an exact name (`order.paid`), a prefix
  (`order.*`) or `*`. Handlers must be idempotent: delivery is at-least-once.

## Worked example: `ext/cms`

[`ext/cms/cms.go`](../ext/cms/cms.go) is the whole shape in one file — a table,
public routes, admin routes, and not one line that touches commerce state.

**Own a table, prefixed with the module name.** `Migrations()` returns one
`Migration{ID: "0001_pages", SQL: "CREATE TABLE cms_pages (…)"}` — `ID` matching
`[a-z0-9_]+`, recorded in the ledger as `cms/0001_pages`, exactly one of `SQL`
or `Run` set, and frozen once it ships. Then keep the table to its store (D83)
with one more: `gocommerce.ScopeMigration("0002_store_scope", "cms_pages")`
adds `store_id`, puts it at the front of the table's keys and turns on the
row policy, so every query the module runs with its request's `ctx` sees only
that store's pages.

**Wire it, and let the mount decide the auth.**

```go
func (m *Module) Register(app *gocommerce.App) error {
    m.app = app
    m.db = app.DB()

    app.HandleFunc("GET /x/cms/pages/{slug}", m.handleGetPublic)
    app.HandleAdminFunc("POST /api/admin/x/cms/pages", m.handleCreate)
    // ...
    return nil
}
```

The pattern is a complete `net/http` ServeMux pattern including the method. The
engine validates the prefix and never rewrites the path, so what you write is
what gets served.

**Use the engine's HTTP vocabulary, not your own.** Every response is the JSON
envelope, including errors:

```go
limit, offset, err := gocommerce.Page(r)          // ?limit / ?offset / ?page
gocommerce.RespondList(w, pages, gocommerce.ListMeta{Total: total, Limit: limit, Offset: offset})
gocommerce.Respond(w, http.StatusCreated, page)
gocommerce.RespondError(w, r, gocommerce.NotFoundf("no page at %q", slug))
```

`NotFoundf`, `Validationf`, `Conflictf` and `Internalf` build `*APIError` values
with the taxonomy's status and code; `DecodeJSON` applies the body limit and
returns a validation error for malformed input. Never write a bare
`http.Error` — a client decoding JSON must never get Go's plain-text default.
Read the negotiated language with `gocommerce.Language(r.Context())` rather
than parsing `Accept-Language` again. A store's own languages are
`app.Languages(ctx)`, its main language first (D87); `Config.DefaultLanguage`
is only what a store that never chose any sells in, so a module never caches it.

## Plugins: a switch and its settings

A plugin is a feature an operator switches on from the Plugins screen rather
than from a config file (D57). It is a descriptor plus one row: the
descriptor says what the plugin is called and what settings it takes, the
row says whether it is on and what the values are. Core ships the
descriptors for storefront features that are only settings a storefront
reads; a module registers its own from `Register`:

```go
app.RegisterPlugin(gocommerce.PluginDef{
    Key: "klaviyo", Title: "Klaviyo", Category: "marketing",
    Description:    "…",
    DefaultEnabled: cfg.PrivateKey != "",
    Fields: []gocommerce.PluginField{
        {Key: "private_key", Label: "Private API key", Kind: "secret", Required: cfg.PrivateKey == ""},
        {Key: "public_key", Label: "Public API key", Kind: "text", Public: true},
    },
})
```

Then, wherever the module acts:

```go
enabled, _ := app.Plugins().Enabled(ctx, "klaviyo")   // the switch
settings, _ := app.Plugins().Settings(ctx, "klaviyo") // unmasked, defaults filled in
key := app.Plugins().String(ctx, "klaviyo", "private_key")
```

Rules that keep it honest: a `secret` field is masked on every read and
never public, whatever `Public` says; a `Public` field is handed to the
storefront through `GET /api/plugins`; the screen's value wins over the
module's `Config`, which stays as the fallback for an environment-first
store; `DefaultEnabled` is the state before an operator touches it, so a
module given its key by the environment is on without a click. Every
change lands in the audit trail as `plugin.update`.

A `secret` field is also sealed at rest when the install has
`GOCOMMERCE_SECRETS_KEY` (D110): `Update` seals it under
`plugins/<plugin>/<field>` for its store, and `Settings`, `String` and `Fill`
open it, so a module reads plain text exactly as before and never sees the
envelope. One that will not open — sealed under a key the install no longer
has — reads as unset, so the plugin shows as not configured; the panel reads it
as the mask and names it in the plugin's `unopened` list, so an untouched save
keeps it and only an empty value clears it. That is right for
a required key and wrong for an optional one that switches a check on, such as
a webhook signing secret: such a module asks
`app.Plugins().Unopened(ctx, key)` and refuses while its field is listed there,
as `ext/payments-revenuecat` does, rather than run with the check off. A module that
keeps a credential it must read back in a table of its own does the same by
hand: `SealSecret(ctx, field, plain)` before it writes, `OpenSecret(ctx, field,
stored)` when it reads (a value never sealed comes back as it is), and
`RegisterSecretColumn` from `Register`, so the engine's start-up seal,
`gocommerce secrets seal` and the doctor reach the column. `ext/webhooks` is the worked example.

What a plugin is not: code that arrives at runtime. The Plugins screen
lists what this binary can do; adding to it is adding a module.

The ad and analytics tags — Google Analytics, Meta pixel, Microsoft Clarity,
Hotjar, the TikTok, Pinterest and Snap pixels — are core descriptors with one
public ID each (D160). The engine holds the switch; the storefront owns the
vendor's snippet, when it loads, the shopper's consent and which shopping
events it hears. `ext/meta-capi` is the one with code behind it: it reads the
pixel ID off `meta-pixel` with `app.Plugins().String` rather than asking for
it twice, so the browser and the server can never name different pixels, and
it delivers through a queue of its own like `ext/webhooks`.

Not every descriptor is an add-on (D132). A plugin on the Plugins screen is an
optional third-party integration or extra: an analytics or chat tag, a
marketing or search integration, an import. A feature that is part of the
product — menus, banners, a payment gateway, a notification sender, the
sign-in method — sets `Home` to the panel screen it is set up on, a path
under `/dash/`. The Plugins screen leaves it out, and that screen shows its
switch and settings (the panel's `FeatureSwitch` component reads
`GET /api/admin/plugins/{key}` and opens the same settings drawer). Nothing
else changes: the row, the secrets, `Fill` and `GET /api/plugins` treat it
like any plugin. `AlwaysOn` goes further — no switch at all: on with no row,
and a patch may not turn it off, though a row stored off before keeps the
store's storefront as it was until the store turns it on once. Banners is the
one. `ext/alltest` names every plugin as an add-on or a built-in feature, so a
new module's plugin is a decision rather than a default.

## Notification templates: the words are the store's

A notifier module carries messages; it does not own their wording (D58).
The engine keeps a catalogue of every message on every channel — the order
events, the operator password reset — with a default subject and body, and
an operator rewords any of them under Notifications › Setup Email / Setup
SMS. A module that introduces an event registers its wording from
`Register`, and asks for the effective text when it sends:

```go
app.RegisterNotifyTemplate(gocommerce.NotifyTemplate{
    Channel: gocommerce.ChannelEmail, Event: "contact.message", Title: "Contact form message",
    Description: "To the store's own address when someone writes through the contact form.",
    Variables:   []string{"from_name", "from_email", "subject", "body"},
    Subject:     "New message from {{.from_name}}",
    Body:        "{{.from_name}} <{{.from_email}}> wrote:\n\n{{.body}}",
})

// In Notify:
tpl, ok, err := app.NotifyTemplates().Get(ctx, gocommerce.ChannelEmail, n.Event)
if err != nil || !ok {
    return err // !ok: nobody registered wording for this event — nothing to say
}
subject, _ := gocommerce.RenderNotifyText(tpl.Subject, n.Data)
body, _ := gocommerce.RenderNotifyText(tpl.Body, n.Data)
```

`Render` does both steps in one call. A stored row overrides the default
and a delete restores it, so the default in code is never lost; both texts
are parsed when saved, so a typo is refused on the screen rather than on
the next sale. A provider that sends its own pre-approved templates (MSG91's
DLT flows) ignores the body and takes its template ids from its plugin
settings instead.

A backend whose key comes from the Plugins screen implements
`ConfigurableNotifier` — `Configured(ctx) bool` — and the engine treats an
unready backend as absent: the funnel skips it, the settings and the doctor
say the channel does not deliver, and the moment the key is typed in the
next message goes out. `ext/notify-sendgrid` and `ext/notify-msg91` are the
worked examples.

### Sending while somebody waits: `NotifyNow`

`app.Notify` is shaped for an outbox handler, and two of its answers mean
"delivered" only to one: a refusal (a bad address, an unverified sending
domain) is dropped from its error, because a retry would be refused again,
and a store with no provider that delivers "sends" to the log and returns
nil. An operator who pressed Send and is about to tell a customer "we've
replied" needs the other answer, so a module sending on a person's behalf
calls `app.NotifyNow` instead. It refuses with 409 when nothing on the
channel delivers, and answers 422 with the provider's own words when it
refused (`provider_refused`) or could not be reached
(`provider_unreachable`) — 4xx because a 5xx's message never leaves the
server. Every attempt is recorded, so it is on the Notifications screen
either way.

Send first, then record, with no transaction open across the send (rule 5).
`ext/contact`'s reply is the worked example:
`POST /api/admin/x/contact/messages/{id}/reply` calls `NotifyNow`, and only
once the provider has taken the message does it insert the reply and mark
the message replied, in one `InTx`. A reply that did not go leaves nothing
behind, and the operator keeps the text to send again.

## Providers install idle: the panel sets them up

A payment gateway or a carrier module registers whatever Config it was
given — including none — and says whether it is ready (D59). Its
credentials come from a plugin whose fields mirror Config by tag, and the
engine asks `Configured` before every pick: an unready gateway is left out
of `Payments.Methods()` and refused at checkout as if unknown, an unready
carrier is refused on the ship dialog, and the settings mark it
`configured: false` so the Payment methods and Shipping providers screens
show it as Inactive with an Activate button.

```go
type Config struct {
    APIKey string `plugin:"api_key"`
    From   Address                  // nested structs are walked
}
type Address struct {
    Line1 string `plugin:"from_line1"`
}

const PluginKey = "payments-acme"

func (m *Module) Register(app *gocommerce.App) error {
    m.app = app
    inCode := m.complete(&m.cfg)
    app.RegisterPlugin(gocommerce.PluginDef{
        Key: PluginKey, Title: "Acme Pay", Category: "payments", DefaultEnabled: inCode,
        Fields: []gocommerce.PluginField{
            {Key: "api_key", Label: "API key", Kind: "secret", Required: !inCode},
            {Key: "from_line1", Label: "From: address", Kind: "text"},
        },
    })
    app.RegisterPayment(m)
    return nil
}

// Before each use: Config with the panel's settings laid over it.
func (m *Module) refresh(ctx context.Context) bool {
    c := m.cfg
    on, _ := m.app.Plugins().Enabled(ctx, PluginKey)
    if on {
        _ = m.app.Plugins().Fill(ctx, PluginKey, &c)
    }
    m.live.Store(&c)             // atomic.Pointer[Config]; readers use m.conf()
    return on && m.complete(&c)
}
func (m *Module) Configured(ctx context.Context) bool { return m.refresh(ctx) }
```

`Fill` sets a string field from text, secret, select, url or textarea, a
bool from bool, an integer or float from number; a stored value wins, an
untyped field keeps Config's value, and a field's Default applies where both
are empty. `Initiate`, `Ship`, `Refund` and the webhook handler each call
`refresh` first and return "not set up" when it says no — the engine has
already refused the pick, so this is the belt to its braces. The eight
gateway and eleven carrier modules are the worked examples; `-gateways`
and `-carriers` install them all idle.

Each card on those two screens carries the provider's mark, looked up by
the code `Code()` returns in
[`admin/src/lib/provider-icons.js`](../admin/src/lib/provider-icons.js).
A carrier shows its own icon (D101): the one the company publishes on its
own website, saved as a 64-pixel PNG at
`admin/static/images/providers/<code>.png` and listed in `CARRIER_LOGOS`. A
new carrier module needs both, and the panel test fails until it has them. A
gateway that [Simple Icons](https://simpleicons.org) carries gets its mark
copied in as path data. A method that is not a company, like cash on
delivery, names a Remix glyph for what it does. Everything else gets a card
for its kind and needs no entry. Renaming a code means renaming its key and
its file too: `npm test` in `admin/` fails on an entry keyed by a code no
module registers, but CI does not run the panel's tests, so run it yourself.

## Where a module lives, and what it may not do

A module that adds **no third-party dependency** belongs in `ext/`. Every
provider here talks REST over `net/http` and verifies HMACs with `crypto/hmac`;
that is the standard, and CI enforces it (see [architecture](architecture.md)
on D23). A module that genuinely needs an SDK ships as its own repository.

The hard rule is unchanged either way: **modules never write core tables.** Go
through the service — `app.Pay().MarkPaid(...)`, `app.Order().Cancel(...)`,
`app.Stock().Adjust(...)`, `app.Ship().Create(...)`. The service performs the
transition, enforces the invariants and writes the outbox event, all in one
transaction. You get `app.DB()` for **your** tables.

`ext/identity` is the worked example of reading core without writing it: it
proves an order belongs to a shopper with `app.Order().GetForGuest(...)` — the
order's own access token — and records the link in its own `identity_orders`,
with no foreign key onto `orders` so a module table can never refuse a core
delete. It also shows a module authenticating its *own* callers: a shopper
session is the module's middleware, not the engine's admin one, so a shopper
token opens no admin route and an admin token reads no address book.

A shopper signs in one way per store (D116): the module's plugin, `identity`,
has a public `login_method` setting, `email_password` or `phone_otp`, and the
routes of the other way answer 409 `sign_in_method`. Phone sign-in is
`POST /x/identity/otp` then `/x/identity/otp/verify`; a phone with no
account opens one, found again by `login_phone`, so such an account has no
email and no password.

With `email_or_phone` the storefront asks phone first, as Myntra does
(D123): `/x/identity/otp/verify` with `ask_if_new` answers a new phone with
`{new_phone, phone_token}` instead of an account, and the shopper either signs
in with their email and password plus `phone` and `phone_token`, which adds
the phone, or opens a new account at `/x/identity/otp/new-account`.

With `email_or_phone` both ways work, and one account can carry both (D118):
signed in by email, `POST /x/identity/me/phone` and `/x/identity/me/phone/verify`
prove a phone and make it sign in to that account; signed in by phone, the
customer adds an email with `PATCH /x/identity/me` and sets a first password
with `PUT /x/identity/me/password`, which asks for no current one when there
is none. A phone already signing in to another account is refused, not moved.

## Calling a model: through the gateway, never directly

A module that wants a model's answer does not call a provider. It registers
a task with `ext/ai-gateway` — a name, instructions, the JSON Schema of the
answer, a ladder of models, a `Build` that tags each piece of store content
with its class, and a `Check` in its own Go types — and either enqueues it in
the transaction that caused it (`Enqueue`) or, from a worker of its own that
holds no transaction, runs it now (`Run`). The gateway reserves against the
store's monthly cap and, on the platform's key, its AI credit, leaves out the
data the owner has not let go, records
every call with its tokens and cost, and retries only what is worth retrying.
`ext/import-amazon`'s `product.rewrite` is the worked example, and
`ext/photo-products` shows a task with pictures (`Document.Caption` labels
each one so the answer can name it), `ext/catalog-ai`'s `catalog.improve`
shows a two-rung ladder that climbs on the model's own confidence and on a
`Check` that reads the store (the category must be one it has),
`ext/reviews`' `reviews.summarize` shows one whose answer must cite only what
it was sent, with a `Deterministic` step that answers without a model, and
[the design](../docs/ai-b2b/m1-ai-gateway.md) has the whole contract.

## MCP: the same state machine, a different door

`ext/mcp` mounts `POST /api/admin/x/mcp` through `HandleAdmin`, so the store's
admin credential is the agent's credential and the module writes no
authentication of its own. Its two routes are gated on rights the module
declares itself with `app.RegisterRight` (D65): `agent.dispatch` on
`POST /api/admin/x/mcp` and `agent.read` on `GET /api/admin/x/mcp/audit`. Both
were `store.operate` before, and both default to no role, so only the owner
holds them until a store grants them on the Roles screen. The reply is JSON-RPC
rather than the engine's `{data}` envelope, which is a standing exception
recorded in AGENTS rule 10. `mcp.ServeStdio(app, m)` runs the same dispatcher
over stdin/stdout for a desktop agent — called from `main()` in place of
`ListenAndServe`, because which mode a binary runs in is the application
author's decision, not a module's.

The safety property is structural, not a policy document: **every tool in
`tools.go` calls a domain service.** `mark_order_paid` is `app.Pay().MarkPaid`,
`cancel_order` is `app.Order().Cancel`, `update_variant_inventory` is
`app.Stock().Adjust` or `SetOnHand`. There is no SQL tool, no "run this query",
no direct table access. An agent therefore cannot reach a state a person could
not, and cannot skip a rule by coming in through a different door (PLAN §40
Rule 9).

The tools are the list `builtinTools()` returns in `ext/mcp/tools.go`, plus any
passed in through `Config.Tools`; read the list there rather than trusting a
count written down anywhere else. `tools/list` returns the same list at runtime,
less the mutating tools when `Config.ReadOnly` is on.

A tool is a struct in `builtinTools()` — a name, a description, a JSON Schema
built with the `object`/`str`/`integer`/`enumStr` helpers, a `Mutates` flag, and
a `Call` that decodes its arguments and calls one service method:

```go
Mutates: true,
Call: func(ctx context.Context, raw json.RawMessage) (any, error) {
    var args struct {
        OrderID   int64  `json:"order_id"`
        Reference string `json:"reference"`
    }
    if err := decode(raw, &args); err != nil {
        return nil, err
    }
    return m.app.Pay().MarkPaid(ctx, args.OrderID, args.Reference)
},
```

Six things to get right:

1. **Wrap a service method.** If the operation you want has no service, the
   change belongs in core first, not in a tool.
2. **Set `Mutates: true` for anything that changes state.** It is what
   `Config.ReadOnly` withholds and what gets written to `mcp_audit` — "which
   agent cancelled that order, and when" has to stay answerable.
3. **Summarise lists, return records whole.** An agent reading fifty orders does
   not need every field of each; see `summarizeOrders`.
4. **Strip credentials.** `get_order` clears `order.AccessToken` — that is the
   shopper's credential, not something an agent needs to read an order.
5. **Return a domain error, not a transport error.** A failing `Call` becomes
   tool content with `isError` set: "that order is already shipped" is
   information the agent can act on.
6. **Name the rights the tool needs**, the ones core names on the equivalent
   REST route — `Rights: []gocommerce.Right{gocommerce.RightOrdersWrite}` for
   `mark_order_paid`, `RightInventoryWrite` for `update_variant_inventory`.
   The mount cannot decide this: `requireRights` runs once before the body is
   parsed, so one route dispatching every tool either locks out a read-only
   agent or hands a catalog-only role `mark_order_paid`. `callTool` checks
   them against the operator on the context and answers a refusal as a JSON-RPC
   error (`-32600`) naming the missing right, which is also written to
   `mcp_audit` when the tool mutates. A static admin token and `ServeStdio`
   carry no operator and are exempt, exactly as they are on a core route.

Tools from outside `ext/mcp` are passed in explicitly: `Config.Tools` is a
`[]mcp.Tool` the application builds and hands over in `main()` —
`mcp.New(mcp.Config{Tools: storeTools})`. There is no discovery: if you want a
module's tools exposed, you say so in `main()`.

## Common mistakes

- **Mounting outside your namespace.** `/api/admin/cms/pages` is a startup
  error; the admin path is `/api/admin/x/cms/pages`.
- **Writing an auth check.** `HandleAdmin` already did it. A hand-rolled one is
  the beginning of a second auth scheme.
- **Editing a shipped migration.** Every existing database already ran it.
- **Returning nil, or a plain error, when the provider refuses a message.**
  Nil reads as delivered — on the notification's record and on Setup Email's
  test — and a plain error buys twelve attempts learning the same thing. Wrap
  `gocommerce.ErrNotificationRefused` for a refusal (a malformed address, an
  unverified sender, a bad key): it is recorded as failed, with your reason,
  and never retried. Return a plain error only when the vendor could not be
  reached.
- **Letting two providers text the same person.** Every backend on a channel
  gets every message unless one is scoped: implement
  `gocommerce.ScopedNotifier` (`Serves(ctx, n) bool`) and a message you serve
  is yours alone, the channel's other backends standing aside for it. MSG91
  does this for Indian numbers and India-based stores; Twilio, unscoped,
  carries the rest. Before telling somebody a code is on its way, ask
  `app.WouldDeliver(ctx, n)`: a channel can deliver and still have nobody for
  one message, as MSG91 has nobody for an event without a template.
- **Calling out with `&http.Client{}`.** A store can set where most
  modules connect, so the default client is `app.OutboundClient(timeout)`,
  which reaches only public addresses unless the install allows otherwise
  (AGENTS.md rule 16). Mark a setting the server connects to — a URL, or a
  host such as a shop domain — `Outbound: true` on its `PluginField`, so a
  private one is refused as it is typed; a return page or storefront link is
  not one.
- **Forgetting the webhook is a raw-body route.** The engine applies no
  body-consuming middleware there precisely so you can verify the signature
  against the exact bytes — and then deduplicate on the gateway's event id.
- **Adding a route and not the spec.** A test and `gocommerce doctor` both
  check every served route is documented; add the path to `openapi.json` or to
  your `OpenAPI()` fragment.
- **Assuming the dispatcher runs in a test.** It starts from an `OnStart` hook,
  which only `ListenAndServe` runs. Use `gctest.DrainOutbox` — see
  [development](development.md).
