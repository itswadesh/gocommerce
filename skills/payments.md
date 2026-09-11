---
name: payments
description: Use when writing or changing a payment provider — implementing PaymentProvider, handling a gateway webhook, refunding, or deciding where a settlement belongs.
---

# Payments

## The model

`Payments` owns payment state; a provider owns a conversation with a gateway.
The seam between them is three small interfaces in `ports.go`:

```go
type PaymentProvider interface {
    Code() string
    Initiate(ctx context.Context, order *Order, opts PayOptions) (PaymentIntent, error)
}

type WebhookProvider interface{ Webhook() http.Handler }              // optional
type Refunder interface {                                             // optional
    Refund(ctx context.Context, order *Order, amountMinor int64) error
}
type Named interface{ DisplayName() string }                          // optional
```

Implement `Named` when the code is an abbreviation: `razorpay` already reads as
Razorpay, `cod` does not, and the fallback is the code itself rather than a
guess at what it stands for. The engine has no other source for the name — a
store composes its own providers in `main()` (D5), so a label table in the panel
could never name a method this build does not ship. `GET /api/checkout` answers
with both `payment_methods` (the array of codes every storefront already reads)
and `methods` (the same set as `{code, name}`); `GET /api/admin/settings`
answers with `{code, name, module}` for the same providers.

`Code()` is the URL segment: `POST /api/checkout/{code}` and
`POST /api/checkout/{code}/webhook`. Both routes are **core-owned** — a payment
module mounts nothing, and the webhook route hands over the raw body with no
body-consuming middleware and no engine-imposed size limit, because signature
verification needs the bytes exactly as they were sent.

`Initiate` returns a `PaymentIntent` telling the client what to do next:

| `kind` | Meaning | Checkout's response |
|---|---|---|
| `none` | Nothing further — cash on delivery | The order is **confirmed** immediately and its stock leaves the shelf; `payment_status` stays `pending` |
| `client_action` | Finish in the page using `client_data` (Stripe's `client_secret`) | Order stays `pending` |
| `redirect` | Send the shopper away, back to `PayOptions.ReturnURL` | Order stays `pending` |

`PayOptions{ReturnURL, Data}` carries the client's `return_url` and
`payment_data` through unchanged, so a gateway gets what it needs without the
checkout body growing a field per integration.

## Invariants

**`Payments.MarkPaid` is the only settlement path — for every provider.** Not a
convention: it is the single place that writes `payment_status = 'paid'`,
commits the reserved stock, moves a `pending` order to `confirmed`, and writes
`order.paid` to the outbox, all in one transaction. A webhook's whole job is
translating external reality into that one call. A module that runs
`UPDATE orders SET payment_status = 'paid'` produces a paid order with stock
still merely reserved and nothing downstream ever told — no invoice, no email,
no export. That is AGENTS.md rule 3, and this is the case it was written for.

Confirming inside `MarkPaid` is what makes a gateway order shippable. Without
it a paid order would sit at `pending` forever, waiting for a step nobody
performs.

**`MarkPaid` is idempotent by design.** An already-`paid` order returns
unchanged and emits nothing, because a gateway *will* deliver the same event
twice and committing stock twice is unrecoverable. A cancelled order is a 409:
money for a cancelled order is a refund problem, and silently reviving it would
resurrect a reservation nobody is holding.

**`Initiate` runs after the checkout transaction has committed.** Rule 5 — see
[checkout](checkout.md) for what happens when it fails (the order stands unpaid;
an `Idempotency-Key` retry resumes payment on it).

**A refund is three steps, and the middle one holds nothing.** Rule 5 forbids a
transaction across the gateway call, and the amount still has to be reserved
against a second refund — so `Payments.Refund` commits a `pending` row in
`order_refunds` (that committed row *is* the reservation), calls the provider
with nothing held, then moves `refunded_minor`, `payment_status` and the
`order.refunded` event together in one transition. A provider that declines
leaves a `failed` row carrying what it said; a provider that never answers
leaves the `pending` row, which the doctor warns about and
`POST /api/admin/orders/{id}/refunds/{refund_id}/settle` closes.

**A partial refund leaves the order `paid`.** `payment_status` becomes
`refunded` only when the refunds add up to the total, so `refunded`/
`refunded_minor` is the figure to read and the status is not (D36). A provider
that cannot refund does not implement `Refunder`, and the engine says so plainly
instead of pretending — which is why refunding is not a required method on every
provider. `Refunder` itself is unchanged by M24, so an existing provider module
needs no edit.

**An `ext/` payment module adds zero third-party dependencies** (AGENTS.md rule
2). Stripe is one form POST and one HMAC; an SDK would buy little and put its
whole transitive tree into the graph of every store that installs the module. A
provider that genuinely needs one ships as its own repository.

**Amounts are minor units.** Pass `order.Total.AmountMinor` and
`order.Total.Currency` through as integers; never format, never divide by 100.

## How to write a payment provider

The worked example below is `ext/payments-stripe/stripe.go` with the names
changed. Read that file alongside it; it is the reference implementation.

**Own one table, for deduplication** — the gateway will redeliver, and settling
twice must be impossible rather than unlikely.

```go
// Package acme takes payments through Acme. It speaks Acme's REST API over
// net/http rather than through acme-go: one form POST and one HMAC.
package acme

type Config struct {
    SecretKey     string // required
    WebhookSecret string // required: without it, anyone could mark orders paid
    BaseURL       string // overridable, for tests
}

type Module struct {
    cfg Config
    db  *sql.DB
    pay *gocommerce.Payments
    log *slog.Logger
}

func New(cfg Config) *Module   { return &Module{cfg: cfg} }
func (m *Module) Name() string { return "payments-acme" }

func (m *Module) Migrations() []gocommerce.Migration {
    return []gocommerce.Migration{{
        ID: "0001_events",
        SQL: `CREATE TABLE payments_acme_events (
                  id          text        PRIMARY KEY,
                  type        text        NOT NULL,
                  order_id    bigint,
                  received_at timestamptz NOT NULL DEFAULT now()
              )`,
    }}
}
```

**Registering the provider is the whole installation.** The webhook route comes
free; there is nothing else to wire.

```go
func (m *Module) Register(app *gocommerce.App) error {
    if strings.TrimSpace(m.cfg.WebhookSecret) == "" {
        return errors.New("acme: WebhookSecret is required — without it, anyone could mark orders paid")
    }
    m.log, m.db, m.pay = app.Log(), app.DB(), app.Pay()
    app.RegisterPayment(m) // serves /api/checkout/acme and /api/checkout/acme/webhook
    return nil
}

func (m *Module) Code() string { return "acme" }

func (m *Module) Initiate(ctx context.Context, o *gocommerce.Order,
    opts gocommerce.PayOptions) (gocommerce.PaymentIntent, error) {

    form := url.Values{}
    form.Set("amount", strconv.FormatInt(o.Total.AmountMinor, 10))
    form.Set("currency", strings.ToLower(o.Total.Currency))
    form.Set("metadata[order_id]", strconv.FormatInt(o.ID, 10))
    for k, v := range opts.Data {
        // Namespace client-supplied extras so they cannot overwrite the fields
        // this module depends on.
        form.Set("metadata[client_"+k+"]", v)
    }

    var intent struct{ ID, ClientSecret string }
    if err := m.post(ctx, "/v1/intents", form, &intent); err != nil {
        return gocommerce.PaymentIntent{}, err
    }
    return gocommerce.PaymentIntent{
        Kind:       gocommerce.IntentClientAction,
        Provider:   m.Code(),
        Reference:  intent.ID, // stored as orders.payment_reference
        ClientData: map[string]string{"client_secret": intent.ClientSecret},
    }, nil
}
```

**The webhook does four things, in this order.**

```go
func (m *Module) Webhook() http.Handler { return http.HandlerFunc(m.handle) }

func (m *Module) handle(w http.ResponseWriter, r *http.Request) {
    body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))

    // 1. Verify the signature over the RAW bytes, and check its timestamp —
    //    a valid signature over an old body is a replay.
    if err := verifySignature(body, r.Header.Get("Acme-Signature"), m.cfg.WebhookSecret, time.Now()); err != nil {
        http.Error(w, "invalid signature", http.StatusBadRequest)
        return
    }

    var ev acmeEvent
    _ = json.Unmarshal(body, &ev)
    orderID, _ := strconv.ParseInt(ev.Metadata["order_id"], 10, 64)

    // 2. Claim the event id atomically, so a redelivery — or a second
    //    application instance — finds the work already taken.
    res, err := m.db.ExecContext(r.Context(), `
        INSERT INTO payments_acme_events (id, type, order_id)
        VALUES ($1, $2, nullif($3, 0)) ON CONFLICT (id) DO NOTHING`,
        ev.ID, ev.Type, orderID)
    if err != nil {
        http.Error(w, "storage error", http.StatusInternalServerError)
        return
    }
    if n, _ := res.RowsAffected(); n == 0 {
        w.WriteHeader(http.StatusOK) // already handled; 200 stops the retries
        return
    }

    // 3. Translate into the one settlement call. Never touch `orders`.
    switch ev.Type {
    case "intent.succeeded":
        if _, err := m.pay.MarkPaid(r.Context(), orderID, ev.PaymentID); err != nil {
            // 4. Release the claim so the gateway's retry finds work to do.
            m.releaseClaim(r.Context(), ev.ID)
            http.Error(w, "could not apply payment", http.StatusInternalServerError)
            return
        }
    case "intent.failed":
        _, _ = m.pay.MarkFailed(r.Context(), orderID, "acme reported a failed payment")
    }
    w.WriteHeader(http.StatusOK)
}
```

`MarkFailed` leaves the order `pending` on purpose, so the shopper can try
again; if nobody does, `SweepUnpaid` eventually cancels it and returns the
stock — which it can only do because the sweep and the doctor both count a
failed payment as unsettled (M23). It writes no event.

An operator does the same thing from
`POST /api/admin/orders/{id}/mark-payment-failed` (`orders.write`). The `reason`
goes to the store's log and is never stored on the order or returned, so there
is nothing to hunt for afterwards. It is a silent 200 no-op on an order that is
cancelled (checked first, so a late webhook for an order the sweeper already
cancelled is not a 409 retried forever) or already failed, and a 409 on one that
is paid or refunded. `POST /api/admin/orders/{id}/mark-unpaid` clears a recorded
failure, moving the payment back to `pending` and dropping the reference,
without an event.

**Add `Refunder` only if the gateway can refund.**

```go
func (m *Module) Refund(ctx context.Context, o *gocommerce.Order, amountMinor int64) error {
    if o.PaymentReference == "" {
        return errors.New("acme: this order has no payment reference to refund")
    }
    form := url.Values{}
    form.Set("intent", o.PaymentReference)
    form.Set("amount", strconv.FormatInt(amountMinor, 10))
    return m.post(ctx, "/v1/refunds", form, nil)
}
```

If the gateway answers with an id for the refund, implement
`ReferencedRefunder` as well and keep `Refund` as a two-line wrapper over it.
That id is what somebody reconciles against a bank statement, and the engine
records it on the refund row:

```go
func (m *Module) Refund(ctx context.Context, o *gocommerce.Order, amountMinor int64) error {
    _, err := m.RefundWithReference(ctx, o, amountMinor)
    return err
}

func (m *Module) RefundWithReference(ctx context.Context, o *gocommerce.Order, amountMinor int64) (string, error) {
    // ... as above, returning the gateway's refund id
}
```

Install it with one argument — no core code changes:

```go
app, err := gocommerce.New(cfg, acme.New(acme.Config{
    SecretKey:     os.Getenv("ACME_SECRET_KEY"),
    WebhookSecret: os.Getenv("ACME_WEBHOOK_SECRET"),
}))
```

Two modules claiming the same code is a **startup error**, because silently
picking one would make the winner depend on argument order. Taking over a
built-in code (`cod`) is allowed, since core is not a competing module.

## How to settle by hand

Cash on delivery has no webhook; an operator settles it:

```http
POST /api/admin/orders/42/mark-paid
{"reference":"cash-2026-08-27"}

POST /api/admin/orders/42/refund
{"amount_minor":2500,"reason":"one item returned"}
```

Both bodies are optional; an omitted `amount_minor` refunds everything that has
not been refunded yet, counting a refund still in flight. These are the same
`MarkPaid` and `Refund` a gateway module calls. `Refund` refuses an unpaid order
with 409, an over-refund with 400 whose message names what is still refundable,
and a 409 when nothing is left to refund.

## Common mistakes

- **Writing `orders` from a webhook.** Call `MarkPaid`. Everything else follows.
- **Reading the body before verifying.** Verify the raw bytes; a re-encoded
  body will not match the HMAC.
- **Skipping the timestamp check.** A replayed old payload has a perfectly valid
  signature.
- **Skipping deduplication**, or acknowledging with 200 before the work
  succeeded. Release the claim when `MarkPaid` fails, or the retry will be told
  the event was already handled.
- **Returning 500 for a permanent rejection.** The gateway will retry it until
  it gives up. 400 says "do not send this again".
- **Pulling in the vendor SDK.** Rule 2. If it is unavoidable, the module ships
  as its own repository.
- **Formatting money.** `{"amount_minor": 2500, "currency": "USD"}`, always.

Related: [checkout](checkout.md), [orders](orders.md), [events](events.md),
[integrations](integrations.md).
