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
| `PaymentProvider` | `Code`, `Initiate` | `RegisterPayment` | built-in `cod`; `ext/payments-{stripe,razorpay,paddle,lemonsqueezy,adyen,hyperswitch,helcim,revenuecat}` |
| `WebhookProvider` | `Webhook` | *optional, detected* | every gateway above except `cod` |
| `Refunder` | `Refund` | *optional, detected* | every gateway except `cod` and `revenuecat` — RevenueCat has no refund API for Web Billing, so it does not claim one |
| `ReferencedRefunder` | `RefundWithReference` | *optional, detected; a `Refunder` as well* | stripe, razorpay, paddle, lemonsqueezy, adyen, hyperswitch, helcim — it returns the gateway's own refund id, which is recorded on the refund |
| `FulfillmentProvider` | `Code`, `Ship` | `RegisterFulfillment` | built-in `manual`; `ext/fulfill-{shiprocket,delhivery,nimbuspost,indiapost,shippo,shipstation,easyship,shippit,usps,onfleet,veeqo}` |
| `Notifier` | `Notify` | `RegisterNotifier(channel, n)` | built-in log notifier; `ext/notify-sendgrid` (email), `ext/notify-msg91` (SMS) |
| *(sending one)* | — | `App.Notify(ctx, n)` | D50. The engine delivers `order.*` itself and nothing else; a module that wants to write to a shopper about anything else calls this. `ext/cart-recovery` is the first caller |
| `Translator` | `Translate` | `RegisterTranslator` | `ext/translations` — catalogue content in the language a shopper asked for |

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
or `Run` set, and frozen once it ships.

**Wire it, and let the mount decide the auth.**

```go
func (m *Module) Register(app *gocommerce.App) error {
    m.db = app.DB()
    m.defaultLang = app.Config().DefaultLanguage

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
than parsing `Accept-Language` again.

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

What a plugin is not: code that arrives at runtime. The Plugins screen
lists what this binary can do; adding to it is adding a module.

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

## MCP: the same state machine, a different door

`ext/mcp` mounts `POST /api/admin/x/mcp` through `HandleAdmin`, so the store's
admin credential is the agent's credential and the module writes no
authentication of its own. Both its routes name `store.operate` — a second
operating surface onto the store, handed to an agent, is what that right means —
and the reply is JSON-RPC rather than the engine's `{data}` envelope, which is a
standing exception recorded in AGENTS rule 10. `mcp.ServeStdio(app, m)` runs the same dispatcher
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
   parsed, so one route dispatching twelve tools either locks out a read-only
   agent or hands a catalog-only role `mark_order_paid`. `callTool` checks
   them against the operator on the context and answers a refusal as a JSON-RPC
   error (`-32600`) naming the missing right, which is also written to
   `mcp_audit` when the tool mutates. A static admin token and `ServeStdio`
   carry no operator and are exempt, exactly as they are on a core route.

Other modules contribute tools explicitly — `mcp.New(mcp.Config{Tools:
invoices.Tools()})`. There is no discovery: if you want a module's tools
exposed, you say so in `main()`.

## Common mistakes

- **Mounting outside your namespace.** `/api/admin/cms/pages` is a startup
  error; the admin path is `/api/admin/x/cms/pages`.
- **Writing an auth check.** `HandleAdmin` already did it. A hand-rolled one is
  the beginning of a second auth scheme.
- **Editing a shipped migration.** Every existing database already ran it.
- **Returning an error from a notifier for a permanent failure.** That buys
  twelve delivery attempts learning the same thing. Retry transient vendor
  failures; log and return nil for a malformed address.
- **Forgetting the webhook is a raw-body route.** The engine applies no
  body-consuming middleware there precisely so you can verify the signature
  against the exact bytes — and then deduplicate on the gateway's event id.
- **Adding a route and not the spec.** A test and `gocommerce doctor` both
  check every served route is documented; add the path to `openapi.json` or to
  your `OpenAPI()` fragment.
- **Assuming the dispatcher runs in a test.** It starts from an `OnStart` hook,
  which only `ListenAndServe` runs. Use `gctest.DrainOutbox` — see
  [development](development.md).
