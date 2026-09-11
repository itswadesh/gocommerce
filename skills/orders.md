---
name: orders
description: Use when reading or changing order state — the confirm/ship/deliver/cancel transitions, order line snapshots, guest order lookup, or the unpaid sweeper.
---

# Orders

## The model

`Orders` owns the state machine. Every transition lives in `orders.go` — no
module, script or integration gets to invent its own version of confirming or
cancelling (AGENTS.md rule 3).

Two independent statuses, both `CHECK`-constrained in `schema.go`:

```
status:         pending → confirmed → partial → shipped → delivered
                   ↓          ↓
                     cancelled
payment_status: pending → paid → refunded
                   ↓
                 failed
```

`paid` is also where an order sits while part of its money has gone back:
`refunded` means *all* of it has. The number to read is `refunded` on the order
(`orders.refunded_minor`), never the status — see **Refunds** below.

`partial` is what the shipped sum landing between nothing and everything is
called. It is skipped entirely when one shipment covers everything owed, which
is what most orders do, and it is derived from `fulfillment_lines` rather than
set by a caller.

| Transition | Call | HTTP | Event |
|---|---|---|---|
| pending → confirmed | `Order().Confirm(ctx, id)` | *(no route — it happens via payment or COD checkout)* | none |
| payment → paid, and pending → confirmed | `Pay().MarkPaid(ctx, id, ref)` | `POST /api/admin/orders/{id}/mark-paid` | `order.paid` |
| payment → failed | `Pay().MarkFailed(ctx, id, reason)` | `POST /api/admin/orders/{id}/mark-payment-failed` | none |
| payment failed → pending | `Pay().MarkUnpaid(ctx, id)` | `POST /api/admin/orders/{id}/mark-unpaid` | none |
| confirmed → partial | `Ship().Create(ctx, id, code, req)` with `req.Lines` | `POST /api/admin/create-fulfillment` | `order.shipped` |
| confirmed\|partial → shipped | `Ship().Create(ctx, id, code, req)` | `POST /api/admin/create-fulfillment` | `order.shipped` |
| shipped → delivered | `Order().MarkDelivered(ctx, id)` | `POST /api/admin/orders/{id}/deliver` | `order.delivered` |
| pending\|confirmed → cancelled | `Order().Cancel(ctx, id, reason)` | `POST /api/admin/orders/{id}/cancel` | `order.cancelled` |
| paid → paid \| refunded | `Pay().Refund(ctx, id, RefundRequest{...}, by)` | `POST /api/admin/orders/{id}/refund` | `order.refunded` |
| a stranded refund → settled | `Pay().SettleRefund(ctx, id, refundID, RefundSettlement{...}, by)` | `POST /api/admin/orders/{id}/refunds/{refund_id}/settle` | `order.refunded`, or none when it never went out |
| partial\|shipped\|delivered, goods back | `Order().Return(ctx, id, in)` | `POST /api/admin/orders/{id}/returns` | `order.returned` |
| a return withdrawn | `Order().WithdrawReturn(ctx, id, returnID)` | `DELETE /api/admin/orders/{id}/returns/{returnId}` | `order.unreturned` |

Recording a failed payment leaves the order `pending` — the sale is not over,
and the shopper may try again — and leaves its reservation in the sweeper's
reach, so an attempt nobody retries still gives the stock back on its own. The
`reason` goes to the store's log and is stored nowhere, which is why the panel
sends a fixed string rather than prompting for prose it would discard.
`mark-unpaid` takes the failure back, silently: the failure was never announced,
so its reversal has nothing to correct. On a cancelled order `MarkFailed`
records nothing and says so with a 200, checked before the paid guard — a 409
there would be a gateway webhook retried forever.

Every one of them runs through `Orders.transition`, which opens `InTx`, reads
the order `FOR UPDATE`, loads its lines, runs the callback, and writes the
callback's event to the outbox **in the same transaction**. That is what makes
the change and the event inseparable (AGENTS.md rule 4).

What these statuses mean for *revenue* is defined once, in
[reports](reports.md): a sale is `stockCommitted` — confirmed, partial, shipped
or delivered — and the report reuses that predicate rather than restating it.
Change the state machine and read that page, because the two are only safe while
they agree.

## Invariants

**A transition that changes nothing announces nothing.** The callback returns an
empty event name to stay quiet. Confirming an already-confirmed order, marking a
paid order paid, cancelling a cancelled order — all no-ops, all silent, because
gateways replay webhooks and an event per replay would be a lie about how many
times the world changed.

There is deliberately no `order.confirmed`. Confirmation always coincides with
`order.created` (cash on delivery) or `order.paid` (everything else), so a
separate name would carry no information and be frozen forever.

**Inventory movement is derived from status, never stored.** `stockCommitted`
reads `status`, so the two cannot disagree. Cancelling therefore does different
things depending on how far the order got: a `pending` order only ever
*reserved*, so `releaseStock` drops the reservation; a `confirmed` order already
took the units off the shelf, so `restockStock` puts them back — onto the shelf
the line records, not onto the default. Getting this
backwards silently invents or destroys inventory. An order that has shipped
cannot be cancelled at all — that is a return, and 409 says so; **Returns**
below is where that sentence now leads.

**What has shipped is derived the same way, from `fulfillment_lines`.**
`settleOrderShipping` is the only writer of the shipping axis of `status`, so
`partial` cannot be set directly and cannot disagree with the parcels behind it.
A `partial` order is stock-committed exactly like `confirmed` and `shipped` —
its units left the shelf at confirmation, including the ones still in the
stockroom — which is why `Cancel` and `EditLines` refuse on it rather than doing
arithmetic over half an order. Shipping itself moves no stock at all.

**`order_lines` are snapshots with nullable foreign keys.** `product_id` and
`variant_id` are `REFERENCES … ON DELETE SET NULL`, while `sku`, `title`,
`variant_label`, `unit_price_minor` and `total_minor` are `NOT NULL` copies. A
two-year-old order stays readable — and legally meaningful — after the product
is deleted or renamed and the price has moved four times. The same applies to
`email`, `name`, `phone` and the `address` jsonb on `orders` itself.

The cost is real and worth knowing: stock movements skip lines whose
`variant_id` is NULL, because there is nothing left to move. Cancelling an order
whose variant was deleted restocks nothing, correctly.

**Guest checkout is permanent** (AGENTS.md rule 8). There is no `customer_id`.
An order is reachable by `orders.access_token`, returned once at checkout.
`GetForGuest` compares it in constant time and reports a mismatch as
**not found**, so the endpoint cannot be walked to discover which order numbers
exist.

**Money is `*_minor` integers plus a currency.** `Money{AmountMinor, Currency}`
serializes as `{"amount_minor": 2500, "currency": "USD"}` — never a formatted
string, because decimal places belong to the currency and symbols to the
reader's locale.

### Refunds

**A refund is a row, and `refunded_minor` is the number.** `order_refunds`
records each one — amount, reason, provider, the gateway's own refund id, the
operator, a status — and `orders.refunded_minor` carries the running total, out
on the Order as `refunded` and `refunds[]`. `payment_status` stays `paid` while
the store holds any of the money and reaches `refunded` only when the parts add
up to the total (D36), so code asking "has money gone back" reads
`Refunded.AmountMinor > 0` and never the status. Five guards already do —
`MarkPaid`, `MarkUnpaid`, `MarkFailed`, `EditLines` and `Update`'s provider
clause — and `lockOrder` loads the figure for every transition, so the right
field is always already to hand.

**Refunding is three steps** because rule 5 forbids a transaction across the
gateway call: a `pending` row is committed first and *is* the reservation
against a concurrent second refund; the provider is asked with nothing held;
then one transition moves `refunded_minor`, `payment_status` and
`order.refunded` together. A declined refund leaves a `failed` row carrying what
the provider said. A refund the engine never heard back about stays `pending`,
blocking that amount — the doctor warns past fifteen minutes and
`POST /api/admin/orders/{id}/refunds/{refund_id}/settle` is how an operator says
what happened, so clearing it is never SQL against a core table.

### Returns

**Goods coming back are their own record, and they move no money.**
`order_returns` and `order_return_lines` (M25) hold what came back off a
**partly shipped, shipped or delivered** order: per line, a quantity, a restock
decision, the shelf the units went to, and what they were worth. `Orders.Return`
runs inside `Orders.transition`, so the rows, the `restockStock` movement and
`order.returned` commit together and there is no network call anywhere in the
path. Refunding stays `Pay().Refund` — separate right, separate record,
separate event — which is why the refund row above still says nothing about
stock, and why the refund route's own description now points here.

The quantity is **per line and cumulative across returns**: the cap is what went
out (on a partly shipped order, what actually shipped) less what has already
come back on returns that still stand. No CHECK can express a sum across rows,
so the cap lives in the statement that inserts the line, under the order's row
lock — and `gocommerce doctor`'s `returns` check exists to notice a row that
got in some other way.

The restock decision is per line too, and defaults to **false** on the API: a
client that forgets the field under-counts stock, which is the safe direction to
be wrong in. A damaged item is still returned; it simply moves no stock. The
units go back to the shelf the line was picked from unless the request names
another, which must be active.

The order's `status` and `payment_status` are untouched — `delivered` stays
true, because the parcel really did arrive. Whether anything came back is
`returns` on the order, and `returned_quantity` on each line.

**`Cancel` and `EditLines` refuse an order with an active return**, because its
lines no longer describe what left the store. That state is reachable in three
clicks: undeliver a returned order and delete its last shipment, and
`restockOrder` would put every line back at its full quantity on top of what has
already come back. A return recorded in error is **withdrawn**, not deleted —
the units come off the shelf again through `sellStock`, the row survives saying
`withdrawn`, and the order becomes cancellable and editable again.

## How to read an order

```go
o, err := app.Order().Get(ctx, 42)                       // by id
o, err := app.Order().GetByNumber(ctx, "GC-000042")      // by human number
o, err := app.Order().GetForGuest(ctx, "GC-000042", tok) // by access token
```

```http
GET /api/orders/GC-000042?token=…            # the guest's own order
GET /api/admin/orders?status=confirmed&payment_status=paid&sort=total&order=desc&limit=50
GET /api/admin/orders/42
```

`OrderQuery` filters on `Status`, `PaymentStatus`, `Email` (case-insensitive),
`From`/`To` over `created_at`, plus `Sort` and `Limit`/`Offset`; `List` returns
the page and the total. `access_token` is `omitempty` and never populated by a
listing.

`Sort` is an allow-listed key and a direction, parsed by `ParseSort(r,
orderSorts)` the way `Page(r)` parses a window — see
[products](products.md#ordering) for the rule. Three of its keys are worth a
sentence each. `total` orders by `total_minor`, never by a formatted amount.
`number` orders by the id the number is built from (`%s%06d`, checkout.go), which
stays true past six digits where a text sort would put `GC-1000000` before
`GC-999999`. `name` uses `nullif` so an order nobody named sits at the bottom in
both directions rather than forming a block of blanks at whichever end is
currently the top.

The CSV export ignores the sort. `ExportOrders` builds its own statement and
never calls `List`, deliberately: a file people diff should not reorder because
a screen was sorted.

## How to move an order forward

```go
// Settlement — the only path, for every provider. See payments.md.
order, err := app.Pay().MarkPaid(ctx, 42, "pi_3Ox…")

// Ship it. The carrier call happens before the transaction opens; the
// transaction then re-checks status, because the world may have moved.
order, err := app.Ship().Create(ctx, 42, gocommerce.ProviderManual,
    gocommerce.ShipRequest{Tracking: "1Z999AA1"})

// Part of it. Empty Lines means everything the order still owes, so the call
// above ships the whole order the first time and the remainder the second.
order, err := app.Ship().Create(ctx, 42, gocommerce.ProviderManual,
    gocommerce.ShipRequest{Tracking: "1Z999AA1", Lines: []gocommerce.ShipLine{
        {OrderLineID: order.Lines[0].ID, Quantity: 1},
    }})

order, err := app.Order().MarkDelivered(ctx, 42)
order, err := app.Order().Cancel(ctx, 42, "customer changed their mind")
```

```http
POST /api/admin/create-fulfillment
{"order_id":42,"provider":"manual","tracking":"1Z999AA1"}

# One line of it. The order lands on `partial`; the same request without
# `lines`, sent again, ships whatever is left and lands it on `shipped`.
POST /api/admin/create-fulfillment
{"order_id":42,"tracking":"1Z999AA1","lines":[{"order_line_id":101,"quantity":1}]}

POST /api/admin/orders/42/cancel
{"reason":"customer changed their mind"}
```

Out-of-order calls are refused with 409, not tolerated: delivering an order that
never shipped or has only partly shipped, shipping one that is still `pending`,
cancelling or editing one that has shipped in whole or in part.

## How to reclaim abandoned inventory

`Orders.SweepUnpaid` cancels `pending` orders whose payment is `pending` or
`failed` and whose `reservation_expires_at` has passed, 200 at a time, and
returns their stock. A recorded failure is included because it is the one
nobody is coming back for; before M23 it was excluded, so marking a payment
failed held its stock out of sale forever. It
runs every five minutes from `App.runSweepers` alongside the cart sweeper, so
you rarely call it directly — but call it in a test that asserts a reservation
is released. Without it an abandoned redirect holds inventory out of sale
forever, which is invisible until the day it sells out a product that is
actually in stock.

## Common mistakes

- **`UPDATE orders SET status = …` from a module or a script.** Rule 3. The
  status changes, the stock does not move, and nothing is told. Use the service.
- **Adding a transition outside `Orders.transition`.** You lose the row lock,
  the same-transaction event, or both.
- **Emitting an event from an idempotent no-op.** Return `""`.
- **Reading `payment_status` to decide shippability.** `shippableOrder` reads
  `status`; a COD order ships while `payment_status` is still `pending`.
- **Reading `status == "shipped"` to mean anything has gone out.** `partial` is
  the one that has left the building without emptying the order. Use
  `shipped_quantity` on the lines, or `status IN ('partial','shipped')`.
- **Assuming `cancelled` means unpaid.** `MarkPaid` on a cancelled order is a
  409 telling you it needs a refund, not a confirmation — reviving the order
  would resurrect a reservation nobody is holding.
- **Building a customer record on `email`.** Identity is a module's job through
  `Config.AdminAuth`; core stays guest-only.
- **Walking a delivered order backwards to put goods back.** Undeliver, delete
  the shipment, cancel: that restocks every line at its full quantity and
  rewrites a completed sale as a cancellation. `Order().Return` is the
  operation, and the walk-back is refused once a return exists.

Related: [checkout](checkout.md), [payments](payments.md), [events](events.md),
[inventory](inventory.md).
