# Events

Events are how a store's behaviour is extended without changing its core.
An invoice module, an email notifier and a search indexer all do their work by
reacting to events, and none of them appears anywhere in the checkout code.

That only works if events are trustworthy, so they are part of correctness
rather than a notification convenience.

## The guarantee

**An event exists if and only if the change it describes was committed.**

Both halves matter. The engine writes the event to an `outbox_events` row
inside the same transaction as the state change:

```
BEGIN
  create the order
  reserve the inventory
  INSERT INTO outbox_events ...
COMMIT
```

Either all of it happens or none of it does. A process that dies immediately
after the commit has still recorded the event; a transaction that rolls back
leaves no trace of one. The failure this rules out is the expensive one: an
order that is paid for and that nobody was ever told about.

A separate dispatcher then delivers what the outbox holds, claiming rows with
`FOR UPDATE SKIP LOCKED` so several application instances can run without ever
handing the same event to two workers at once.

## Delivery is at-least-once

A handler can run, and the process can die before the delivery is recorded. On
the next pass the event is delivered again. This is normal operation, not a
bug, and it is the price of never losing an event.

**Every handler must therefore be idempotent.** In practice that means one of:

- Make the work naturally repeatable — setting a status to `paid` twice is
  harmless.
- Guard with a unique constraint. The invoices module has `UNIQUE (order_id)`,
  so a second delivery inserts nothing.
- Record what you have seen. The Stripe module claims each event id in its own
  table before acting.

Every event carries a stable `id`. If nothing else fits, remember it.

Returning an error from a handler asks for redelivery with exponential
backoff. After twelve failures the row is marked dead rather than deleted: an
event nobody could deliver is evidence, and evidence should outlive the
incident.

## The taxonomy

These names are a public contract. A name is added when something real
produces it, and is never repurposed.

| Event | When | Notes |
|---|---|---|
| `order.created` | An order was created at checkout | Inventory is reserved, or committed for cash on delivery |
| `order.paid` | Money arrived | Also confirms a pending order, so it becomes shippable |
| `order.shipped` | A parcel was booked | Fires once per parcel. Payload carries `tracking` and a `shipment` block naming what went in this one and what is still owed; `status` is `partial` while the order still owes units |
| `order.unshipped` | A shipment recorded in error was removed | Payload carries the same `shipment` block, describing the parcel that came back; `status` is what is left — `partial` or `confirmed` |
| `order.delivered` | The customer received it | |
| `order.refunded` | Money went back through the provider | Fires once per refund, so two partial refunds are two events. Payload carries a `refund` block and `reason`; `payment_status` says whether this one finished the job — `paid` while the store still holds part of it, `refunded` once the parts add up to the total |
| `order.returned` | Goods came back off a shipped, partly shipped or delivered order | Stock is returned only for the lines marked to restock, and only to the shelf they were picked from unless another was named. The order's `status` and `payment_status` are unchanged — the delivery still happened — and the payload carries a `return` block. No money moves: a refund is its own operation and its own event |
| `order.unreturned` | A return recorded in error was withdrawn | The units it put back have come off the shelf again. The row survives as `withdrawn`, so the quantity it held becomes returnable once more |
| `order.cancelled` | The order was voided | Stock has been returned; payload carries `reason`. The unpaid sweeper's population widened in M23 to orders whose payment was recorded as failed, so this now delivers for declined-gateway orders that previously sat stranded and silent |

There is deliberately no `order.confirmed`. Confirmation always coincides with
either `order.created` (cash on delivery) or `order.paid` (everything else), so
a separate event would carry no information and would be one more name frozen
forever.

There is no `product.*` family either, until something consumes it.

**Stock movements are not events.** Every change to a stock balance is recorded
in `stock_movements` (M26) — append-only, written inside the same transaction as
the balance it explains, and read back through
`GET /api/admin/variants/{id}/movements`. There is no `stock.*` name and no
second aggregate type, for the reason this page gives for `order.confirmed`: the
taxonomy is a public contract, nothing consumes a stock event, and `order.*`
already tells a consumer that an order took stock. Volume decides the rest — a
twenty-line order would put forty rows through the at-least-once dispatcher for
nobody. If you need one, the migration path is one function: add
`AggregateVariant = "variant"` beside `AggregateOrder`, emit `stock.moved` (v1)
from `recordMovement` in `movements.go`, which is already the single funnel, and
restrict it to the operator kinds — the five order-driven kinds are covered by
`order.*` already. The payload exists: it is `StockMovement`. No schema change,
no new call site. See D38.

## The payload

Every `order.*` event carries an `OrderEvent`:

```json
{
  "order_id": 42,
  "number": "GC-000042",
  "status": "confirmed",
  "payment_status": "paid",
  "payment_provider": "stripe",
  "currency": "USD",
  "total_minor": 5000,
  "refunded_minor": 2000,
  "email": "shopper@example.com",
  "phone": "",
  "name": "A Shopper",
  "language": "en",
  "lines": [
    {
      "sku": "TEE-001",
      "title": "Cotton tee",
      "variant_label": "M / Black",
      "quantity": 2,
      "unit_price_minor": 2500,
      "total_minor": 5000
    }
  ],
  "tracking": "",
  "reason": "",
  "refund": {
    "amount_minor": 2000,
    "refunded_minor": 2000,
    "remaining_minor": 3000,
    "reason": "damaged in transit",
    "provider_reference": "re_3Q…"
  },
  "shipment": {
    "fulfillment_id": 7,
    "carrier": "ups",
    "remaining_units": 3,
    "lines": [
      { "sku": "TEE-001", "title": "Cotton tee", "variant_label": "M / Black", "quantity": 2 }
    ]
  },
  "return": {
    "return_id": 3,
    "status": "received",
    "reason": "arrived damaged",
    "units": 2,
    "restocked_units": 1,
    "refundable_minor": 5000,
    "lines": [
      { "sku": "TEE-001", "title": "Cotton tee", "quantity": 2, "restocked": true, "refundable_minor": 5000 }
    ]
  }
}
```

It carries enough for a consumer to act without reading the database — a
notifier can write an email from this alone.

`refunded_minor` is the order's running refunded total and is set on **every**
`order.*` event, so a consumer of `order.cancelled` or `order.edited` can see
what had already gone back. `refund` is present only on `order.refunded` and
describes that one movement: what went back now, the running total afterwards,
and `remaining_minor`, which is what a consumer needs to tell a partial refund
from the one that finished the job without doing the arithmetic itself.

`shipment` is present only on `order.shipped` and `order.unshipped`, and it
describes one parcel: top-level `lines` is still the whole order, which is what
it has always been. `remaining_units` is what the order still owes once this
parcel is counted, so zero means the order is complete — the number rather than
a flag, because it also says how much is left. It repeats neither `tracking`
nor `extra.provider`: a payload that states one fact twice is how the two come
to disagree.

`return` is present only on `order.returned` and `order.unreturned`, and it is
the only part of the payload that says a return happened: `status` and
`payment_status` are unchanged by one, because the sale and the delivery both
still stand. `refundable_minor` is what the goods were worth — unit price times
quantity, less the line's share of the order discount, plus its share of the tax
where prices exclude it — and it is a valuation, not a refund. Nothing on this
event moved any money.

Amounts are integer minor units, as everywhere else. `language` is the
language the shopper checked out in, so a notifier can reply in it.

### Changing a payload

Adding an optional field is safe. Removing or repurposing one is not: a
consumer written last year is still running. If a payload has to change
incompatibly, publish it under a new event version — `Event.V` exists for
exactly that — and keep emitting the old one until consumers have moved.

## Subscribing

```go
func (m *Module) Register(app *gocommerce.App) error {
    app.Subscribe(gocommerce.EventOrderPaid, m.onPaid)
    return nil
}

func (m *Module) onPaid(ctx context.Context, e gocommerce.Event) error {
    var ev gocommerce.OrderEvent
    if err := e.Decode(&ev); err != nil {
        return err
    }
    // Idempotent: a unique constraint makes a redelivery a no-op.
    return m.issueInvoice(ctx, ev.OrderID)
}
```

Patterns may be an exact name, a prefix (`order.*`), or everything (`*`).

Handlers run under `Config.HandlerTimeout` (10 seconds by default). A handler
that hangs is failed and retried rather than being allowed to stall the
dispatcher.

## Who may publish

Core does. A module reacts to events and calls domain services; it does not
announce state changes itself, because an event that does not correspond to a
committed core transaction would break the guarantee this whole page rests on.

## Testing

`gctest` makes delivery synchronous so a test never sleeps:

```go
app := gctest.New(t, mymodule.New(cfg))
result := gctest.PlaceOrder(t, app, gocommerce.CodeCOD)
gctest.DrainOutbox(t, app)          // deliver everything pending
gctest.AssertOutboxEmpty(t, app)    // nothing failed or was parked
```

To prove your handler is idempotent, replay the events and assert the result
did not change:

```go
_, _ = app.DB().ExecContext(ctx,
    `UPDATE outbox_events SET published_at = NULL, available_at = now()`)
gctest.DrainOutbox(t, app)
```
