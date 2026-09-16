---
name: reports
description: Use when asking what the store sold — the sales and best-seller reports, what counts as a sale and why, the arithmetic identities the response guarantees, and what the figures deliberately do not measure.
---

# Reports

`core/reports.go` answers one question: how much did we sell. Two routes, both
behind `orders.read`, both a reading of tables the engine already owns:

| Route | Service call |
|---|---|
| `GET /api/admin/reports/sales` | `app.Reports().Sales(ctx, SalesQuery{…})` |
| `GET /api/admin/reports/top-products` | `app.Reports().TopProducts(ctx, TopProductsQuery{…})` |

No table, no column, no migration, no event, no dependency. `Reports` owns
nothing, writes nothing, and opens no transaction — each answer is one
statement, and a read needs no transaction to be consistent with itself.

## What counts as a sale

**A sale is an order whose stock has left the shelf**:

```
status IN ('confirmed', 'partial', 'shipped', 'delivered')
```

That is `stockCommitted` in [orders](orders.md), reused rather than restated —
`saleStatuses` is the same set written as data, and a test walks the whole
status enum asserting the two agree. When a seventh status arrives, that test is
what stops one of them learning about it alone.

Two definitions that look reasonable and are wrong for this engine:

- **`payment_status = 'paid'`.** Cash on delivery is the only payment method
  core ships, and it confirms the order at checkout while the payment stays
  `pending` — the money arrives when the parcel does. A paid-only report shows a
  stock GoCommerce store zero revenue beside a non-zero order count.
- **`status <> 'cancelled'`.** That counts in-flight `pending` checkouts, which
  is exactly the set `orders_unpaid_idx` exists to find and `SweepUnpaid`
  cancels an `OrderTTL` later. Today's takings inflate and tomorrow's deflate
  with nobody touching anything.

`partial` is in the set deliberately. A partly shipped order reached that state
from `confirmed`; every unit is off the shelf, including the ones still waiting
for the second parcel. Leaving it out would make an order vanish from revenue at
the moment the first parcel went out.

| `status` | in the report |
|---|---|
| `pending` | no — reported in `excluded.pending` |
| `confirmed`, `partial`, `shipped`, `delivered` | **yes** |
| `cancelled` | no — reported in `excluded.cancelled` |

`excluded` sits beside the totals and never inside them. It is what lets an
operator explain why the report disagrees with a count of the Orders list.

## Payment splits the same money

Payment never decides whether a sale happened. It cuts the sale set into three,
and `payment_status` is `CHECK`-constrained to exactly four values, so the three
slices partition it with nothing left over:

| Slice | `payment_status` |
|---|---|
| `paid` | `paid` |
| `outstanding` | `pending` or `failed` |
| `refunded` | `refunded` |

A partly refunded order stays `paid` (D36) — partiality is a number, not a
status — so `paid` also carries two figures the other two do not:

- `paid.refunded` is `refunded_minor` summed over exactly those orders: money
  that has gone back out of orders the store is still recorded as holding.
- `paid.net_collected` is `paid.total − paid.refunded`. It is the only figure
  that answers "how much of this is actually ours".

Under `payment_status`, `refunded` means **fully** refunded; `refunded_minor` is
the money. Read the first as a count of orders and the second as an amount.

## The identities

Every bucket and every window total satisfies both, exactly, and both are pinned
by tests:

```
total = net + tax + shipping
paid.total + outstanding.total + refunded.total = total
```

`net` is written off the source columns rather than derived from `total`:

```
net = subtotal_minor − discount_minor − (tax_minor when the order was tax-inclusive)
```

`orders.tax_inclusive` is snapshotted per order, because a store may switch, so
the branch is per row and in SQL. Choosing `net` this way is what makes
`total = net + tax + shipping` hold under **both** tax modes — a bare
`sum(subtotal_minor)` called "gross" means two different things either side of
that switch.

Refunds are reported and never netted off `net`. A report that silently
subtracted them would answer a different question from the one its label asks,
and the money that came back is already on the response twice over —
`paid.refunded` for partial refunds, `refunded.total` for whole ones.

The window totals are added up **in Go from the buckets**, not queried a second
time, so a stat card and the chart beneath it cannot disagree.

## The window and the buckets

```
GET /api/admin/reports/sales?from=2026-09-01&to=2026-10-01&group_by=day&tz=Asia/Kolkata
```

- `from` is inclusive, `to` is **exclusive** — the rule `GET /api/admin/orders`
  already uses. A range ending on the 30th asks for `to=` the 1st.
- Both accept `YYYY-MM-DD` or RFC 3339. A bare date is midnight **in `tz`**.
- `tz` is an IANA name, default `UTC`, validated against PostgreSQL's own
  `pg_timezone_names` and echoed back canonicalised. The tz database lives in
  the database; a second copy in the binary could only drift from it.
- The window's edges and the bucket boundaries are cut in the same zone. Cut
  against different midnights, the first and last buckets are quietly partial
  and nobody can tell.
- `group_by` is `day`, `week` or `month`. Weeks are ISO — Monday — because that
  is what `date_trunc` means by a week, and there is no `week_start` parameter.
- Every bucket in the window is present, zero-filled where nothing sold, and
  `bucket[n].end == bucket[n+1].start` for every n, DST included.
- A first or last bucket the window clips carries `partial: true`.
- More than 400 buckets at the requested grain is refused, naming a coarser one.
  A report is a single reading, not a page: there is no cursor here.

Figures are grouped by `orders.currency` into one block per currency and nothing
is ever summed across them. The store's configured currency is always present,
zero-filled, so a quiet window looks quiet rather than broken.

## Best sellers

```
GET /api/admin/reports/top-products?by=variant&sort=units&currency=USD&limit=10
```

The order lines of the same sale set, grouped by product (default) or variant,
within one currency, paged with the standard `limit` / `offset` / `page`.

What `revenue` is: line value **excluding tax under both tax modes**, because
`order_lines.total_minor` is `unit_price × quantity` and under inclusive pricing
that price contains the tax.

What `revenue` is **not**: net of order-level discounts. A discount is recorded
once per order and never allocated to lines (D28), so apportioning it here would
be an invention — and summing these rows exceeds the sales report's `net` by
exactly the discounts in the window. The panel says so above the table.

Title, SKU and variant label come from the newest line, so a renamed product
reports under the name somebody recognises. `product_id` is null once the
product is deleted (`ON DELETE SET NULL`, because an order line is a snapshot);
the group key falls back to the SKU, so two deleted products stay two rows
instead of collapsing into one invented bestseller.

## Custom reports: a saved SELECT

Everything above answers "how much did we sell", which every shop asks, and it
lives on the dashboard. `/reports` is the other kind: the questions only this
shop has — which wholesale customers have not ordered since March, what the
Tuesday promotion actually cost — written as SQL once and run by whoever needs
the answer (M45).

```
GET    /api/admin/reports/custom           reports.read   the saved list
POST   /api/admin/reports/custom           reports.write  save one
PATCH  /api/admin/reports/custom/{id}      reports.write  change one
DELETE /api/admin/reports/custom/{id}      reports.write  delete one
POST   /api/admin/reports/custom/{id}/run  reports.read   run a saved one
POST   /api/admin/reports/custom/run       reports.write  run without saving
```

It is the one place a person's own SQL reaches the database, so what matters is
what it refuses, and there are two layers.

- **A read-only transaction**, which is what actually holds. Every run is
  `BEGIN` … `SET TRANSACTION READ ONLY` with a 15-second `statement_timeout`,
  always rolled back. PostgreSQL refuses every write and every DDL inside it, so
  a bug in the layer below is a bad error message rather than a lost table. A
  test proves it by calling `runInReadOnlyTx` directly with an `UPDATE`.
- **A parse**, which exists for the error message. It strips comments first —
  so a verb cannot hide behind one — then requires a single statement beginning
  `SELECT` or `WITH`, and rejects a writing verb anywhere in it, which is how
  a CTE smuggles a `DELETE` past a first-word check. An operator who pastes an
  `UPDATE` reads "a report may only read" instead of a driver error.

What it does **not** do is sandbox reading. A report selects anything the
engine's database user can, which is everything.

- **`reports.write` is owner's alone.** Saving a report is deciding what
  everybody with `reports.read` may look at, including tables holding buyers'
  addresses. Running a *saved* report is `reports.read` on purpose — the point
  of saving one is that somebody who does not write SQL can get the answer, and
  its text was reviewed by whoever could save it.
- **1000 rows, and it says when it cut.** `truncated` is on the result;
  without it a thousand-row prefix looks exactly like a thousand-row answer.
- **The SQL is never interpolated.** There are no parameters, because a report
  an operator can parameterise is one they can rewrite at call time, and the
  point of saving one is that what runs is what was reviewed.

## What these numbers do not measure

- **Refund value is reported, never netted.** See above.
- **Buckets key on `orders.created_at`.** There is no `confirmed_at`, so marking
  an old order paid, editing it, or cancelling it changes a past bucket. A
  report is a current reading of history, not a closed period.
- **Imported orders carry no tax.** `ImportOrders` writes neither `tax_minor`
  nor `tax_inclusive`, so a backfilled history reports zero tax collected.
- **No rollup table.** A stored copy of a sum is a number that can be wrong, and
  it would have to be rebuilt on `EditLines`, `MarkPaid`, `MarkUnpaid`,
  `Cancel`, `Refund` and `RecalculateOrderDiscount`.

## Do not

- Do not redefine a sale as paid-only, or as everything that is not cancelled.
  Both are explained above, and both are money bugs.
- Do not add a rollup table, or an event subscriber that maintains one.
- Do not sum across currencies. Minor units of two currencies add up to a number
  that is not money.
- Do not net refunds off `net` until the response, the panel and the docs say so
  together.
- Do not add a charting library to the panel. The chart is stacked CSS bars in
  `admin/src/lib/components/SalesChart.svelte`, and the engine's
  one-production-dependency rule is not a budget the panel gets to spend.
