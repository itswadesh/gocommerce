---
name: inventory
description: Use when receiving stock, running a stock take, moving stock between locations, chasing a low-stock report, or reasoning about how a reservation becomes a sale.
---

# Inventory

## The model

Stock belongs to a [variant](variants.md) *at a place*. The variant is the
sellable SKU; the place is where the units physically are. Since M17 the truth
is one `variant_stock` row per pair:

```
variant_stock (variant_id, location_id)
    on_hand    physically on that shelf, including units promised to open orders
    reserved   promised to orders in flight but not yet taken off that shelf
```

The variant still reports one number for each, and they mean exactly what they
always did — the store has this many:

```
stock_on_hand    = sum(on_hand)  over the variant's locations
stock_reserved   = sum(reserved) over the variant's locations
available        = stock_on_hand - stock_reserved   (derived, never stored)
```

A store with one location never has to think about any of this. Every store gets
a `default` location when its schema is created, opening stock lands there, and
every stock call takes `0` to mean it.

Five movements, all in `inventory.go`, all single UPDATE statements so the check
and the write happen under the same row lock, and all against one location:

| movement | effect | ledger kind | when |
| --- | --- | --- | --- |
| `reserveStock` | `reserved += qty` | `reserve` | [checkout](checkout.md) creates the order |
| `commitStock` | `reserved -= qty`, `on_hand -= qty` | `commit` | payment confirms the order |
| `releaseStock` | `reserved -= qty` | `release` | a pending order is cancelled or swept |
| `restockStock` | `on_hand += qty` | `restock` | a *confirmed* order is cancelled, or goods come back on a return |
| `sellStock` | `on_hand -= qty` | `sell` | a committed order is edited upward, or a return is withdrawn |

Since M26 every one of them also writes a row to `stock_movements` inside the
same transaction — see [How to read what happened](#how-to-read-what-happened).

Which of the last two is correct depends on how far the order got: a pending
order only ever reserved stock, while a confirmed one has already taken it off
the shelf. `Orders.Cancel` picks with `stockCommitted(o.Status)` — getting that
choice wrong double-counts inventory in one direction or the other.

Which *place* they act on is not a fresh decision. `pickLocation` chooses once,
at checkout, and the answer is written to `order_lines.location_id`; everything
afterwards reads it back through `lineLocation`. That is what makes a
cancellation put the units back on the shelf they left rather than on whichever
shelf is default that week. A return goes back the same way unless the operator
names another location — a returns desk, say — which must be active, because
stock parked on a shelf nobody counts is stock the store has lost track of.

The public service is `app.Stock()`, returning `*Inventory`, with `Adjust`,
`SetOnHand`, `Move`, `ByLocation`, `AtLocation`, `LowStock` and `Movements`. Places are `app.Places()`,
returning `*Locations`. The five movement functions above are unexported: they
only ever run inside the order transaction that justifies them.

## Invariants

- **The reserved-within-on-hand invariant is now the service's, not a CHECK's.**
  It used to be `variants_reserved_within_on_hand`. That constraint was
  conditioned on `continue_selling`, which lives on the variant and not on a
  stock row, and copying the flag onto every row to keep the CHECK would have
  been a stored duplicate — the same mistake this codebase refuses for category
  paths. What enforces it now is `reserveStock`'s conditional UPDATE, which was
  always doing the real work, plus a `doctor` check that counts any row where it
  has drifted. `variant_stock.reserved >= 0` is still a CHECK, because that one
  needs nothing from the variant.
- **Reservation is one statement, never read-then-write.** `reserveStock`'s
  `WHERE` carries `vs.on_hand - vs.reserved >= $3`, so two concurrent checkouts
  for the last unit cannot both pass: the second re-evaluates the condition after
  the first commits and matches zero rows, which becomes `errInsufficientStock`.
  A `SELECT` followed by an `UPDATE` would sell the same unit twice under load
  and pass every test that runs serially.
- **A line comes off one shelf.** `pickLocation` takes the first active location,
  in priority order, that can cover the whole quantity. It does not split a line
  across two places: doing so would promise a shopper one parcel and hand the
  warehouse two picks, which is a bigger commitment than this engine makes.
- **The floor is per location, not per store.** Five spare units in the warehouse
  do not entitle the shop to go below what is reserved there, because the order
  being picked from the shop cannot be filled from the warehouse.
- **A location holding stock cannot be closed.** Its units would still be counted
  in `stock_on_hand` while `pickLocation` skipped it, so the store would believe
  it could sell something it could not reach. `Locations.Update` and
  `Locations.Delete` both refuse, and the message names how many units to move.
  Both take the location row `FOR UPDATE` before they count, so a transfer
  cannot commit between the count and the write.
- **An operator may not receive stock at a closed location.** `Move`'s
  destination, a positive `Adjust`, a `SetOnHand` that raises a count and a CSV
  `stock_on_hand:<code>` cell that raises one are refused with a 409. Every
  *decrease* is allowed, including at a closed location, because emptying a
  closed shelf is the only way one is ever cleared — and `Move`'s **source** is
  never checked, which is what makes a store with already-stranded units able to
  free them. Returning already-sold units is exempt: a cancelled or reduced order
  line restocks to the shelf recorded on it, closed or not, because those units
  have already left the shelf and refusing would lose them. See D44.
- **`track_inventory = false` means unlimited, not zero.** Every movement is
  wrapped in `CASE WHEN track_inventory THEN $2 ELSE 0 END`, and `reserveStock`
  succeeds for such a variant while reserving nothing. That is how digital goods
  and made-to-order items work without a second code path.
- **Stock never moves through a patch.** `VariantPatch` has no stock field.
  "Set the stock to 7" is not a safe operation when another request may have
  sold one in between, so the API offers a delta (`adjust`) and a stock take
  (`set`) — both evaluated against the current row, not against what the caller
  last read.
- **Every change to a balance writes a movement, in the same transaction.**
  `stock_movements` (M26) is append-only, and the row commits with the balance
  it explains — so a rolled-back checkout leaves no phantom movement, and a
  committed one can always be accounted for.
- **The ledger reconciles.** `sum(on_hand_delta)` for a (variant, location) pair
  equals that pair's `on_hand`, and the same for `reserved`. It holds because
  every writer records what its statement *applied* — not what the caller asked
  for — and `gocommerce doctor`'s `stock ledger` check asserts it on every run.
- **A movement that changes nothing is not recorded — except a stock take.** An
  untracked variant's reservation moved nothing by design and a re-run of an
  unchanged import changed nothing; rows saying so would hide the rows that
  matter. An operator who counted a shelf and found the number already there has
  produced evidence, which is the other half of what the ledger exists for.
- **The ledger has no CHECK on its balances.** `continue_selling` makes a
  negative `on_hand` legal, and an audit trail that refused to record a movement
  the shelf accepted would be worse than no audit trail.
- **A reservation has a deadline.** Checkout stamps
  `orders.reservation_expires_at` at `now() + Config.OrderTTL` (24h default);
  `Orders.SweepUnpaid` cancels what is past it — whether the payment is still
  pending or was recorded as failed — and releases the stock. Without
  it an abandoned payment takes units out of sale forever — invisible until the
  day it sells out something that is actually on the shelf. `gocommerce doctor`
  reports stale reservations for exactly this reason.

## How to receive stock or run a stock take

`Adjust` moves the count by a delta; `SetOnHand` replaces it. Both return the
refreshed variant:

```go
v, err := app.Stock().Adjust(ctx, variantID, 0, 25, "delivery from Acme")
v, err := app.Stock().SetOnHand(ctx, variantID, 0, 18, "quarterly count")
```

The third argument is the location. `0` means the default one, which is what a
single-location store always passes; a real id says which shelf. The last is the
operator's reason, recorded verbatim in the ledger — it may be empty, and a
script that leaves it empty produces the one row nobody can interpret.

Both refuse to drop that location's on-hand below what is reserved there, and
`explainStockFailure` turns the zero-row update into
`409 — "stock cannot go below the N unit(s) already reserved for open orders"`.
A location id that does not exist is a 404 rather than a silent fallback to the
default: putting stock somewhere the caller did not name is worse than telling
them they were wrong.

```http
POST /api/admin/variants/77/inventory
Authorization: Bearer <admin token>

{"adjust": 25, "reason": "delivery"}            → 200, the variant
{"set": 18, "location_id": 3, "reason": "count"} → 200, the variant
{"adjust": 5, "set": 18}                       → 400, "send either adjust or set, not both"
```

Both keys omitted is also a 400. Receiving stock and counting a shelf are
different acts, and the endpoint makes you say which one you performed.

## How to work with more than one location

```go
places, err := app.Places().List(ctx)
rows, err := app.Stock().ByLocation(ctx, variantID)       // where it actually is
v, err := app.Stock().Move(ctx, variantID, fromID, toID, 10, "rebalancing")
```

```http
GET  /api/admin/locations
POST /api/admin/locations              {"code": "shop", "name": "The shop", "priority": 1}
POST /api/admin/locations/3/default
GET  /api/admin/variants/77/stock
POST /api/admin/variants/77/stock/transfer
     {"from_location_id": 1, "to_location_id": 3, "quantity": 10}
```

`Move` is one statement out and one in, in a single transaction, so the store's
total never changes even for an instant. Reserved units do not travel: they are
promised to orders that will be picked from where they are, and moving them
would send a picker to the wrong shelf.

`priority` orders the search — lower is tried first. `from_location_id` has no
default, because emptying a shelf nobody named is not something anyone means to
do. A closed `to_location_id` is a 409; a closed `from_location_id` is fine.

## What one location holds

```go
rows, total, err := app.Stock().AtLocation(ctx, locationID,
    gocommerce.LocationStockQuery{NonZero: true})
```

```http
GET /api/admin/locations/3/stock
GET /api/admin/locations/3/stock?nonzero=0
```

`ByLocation` is one variant across every place; this is every variant at one
place — the read behind "this location still holds 43 unit(s) across 7 SKU(s);
move them before it is closed". A shelf counts as *held* when `on_hand` or
`reserved` is non-zero, which is the same test that refuses the close, so the
listing and the refusal can never disagree about what is there. `?nonzero=0`
adds the shelves sitting at zero.

Only variants the location already has a `variant_stock` row for appear. A row
exists from the moment anything moves there and survives going to zero, so a
shop that has sold out of something it carries is listed — which is the
restocking case — while the catalog it has never carried is not listed against
it.

Each row is the usual `Variant`, with the store-wide `stock_on_hand` /
`stock_reserved` / `available` it always had, plus a new `at_location` block
carrying this shelf's `on_hand`, `reserved` and `available`. Both are true and
they answer different questions. Clearing a shelf is the transfer route above —
there is no separate "empty this location" call.

## How to read what happened

Every movement since M26 is a row in `stock_movements`, and nothing ever edits
or deletes one. A row carries two signed deltas (`on_hand_delta`,
`reserved_delta`) rather than one number — a commit takes units out of the
reservation and off the shelf at once, and one column could not say which — the
two balances *after*, a `kind`, a `source`, the operator's `reason`, the order
behind it, and snapshots of the SKU, the location code, the order number and the
actor's email so the row still reads after a SKU is deleted, a location closes or
an employee leaves.

The eleven kinds: `opening`, `adjust`, `stock_take`, `import`,
`transfer_out`, `transfer_in`, `reserve`, `commit`, `release`, `restock`,
`sell`. The six sources name the *path* rather than the person — `admin`,
`checkout`, `order`, `sweeper`, `import`, `migration` — which is what makes a
row with no operator a fact rather than a hole; who it was, when it was anybody,
is `superuser_id` and `actor_email`.

**The ledger records what the shelf did, not what you asked it to do.** Five of
the movement helpers wrap their quantity in `CASE WHEN track_inventory`, one
floors a release at zero, and two write absolutely — so a row's delta is read
back from the statement that ran, and `sum(on_hand_delta)` meets `on_hand` for
every pair, forever. That sum is the only thing that can catch a writer
bypassing `app.Stock()`.

```go
rows, total, err := app.Stock().Movements(ctx, gocommerce.MovementQuery{
    VariantID: variantID,
    Kinds:     []string{"adjust", "stock_take"},
    Limit:     50,
})
```

```http
GET /api/admin/variants/77/movements?kind=adjust,stock_take&limit=25
GET /api/admin/locations/3/movements?from=2026-01-01&to=2026-02-01
```

Both are `inventory.read` — reading how a count got there is reading stock — and
both take `limit`/`offset`/`page`, `from` and `to`, `kind` (comma-separated or
repeated), `order_id`, and the other axis (`location_id` on the variant route,
`variant_id` on the location one). Newest first, ordered by id rather than by
timestamp: a transfer writes both of its rows in one transaction and they share
`now()` exactly.

A deleted location's rows stay readable on the variant route with their
`location_code` intact; a deleted variant's stay readable on the location route
with their `sku`.

## How stock travels through a CSV

The product export carries one stock column per location, named by code:

```
product_slug,...,stock_on_hand:default,stock_on_hand:shop,...
tee,...,4,3,...
```

A store with one location gets the plain `stock_on_hand` column instead, exactly
as it did before locations existed — which is what keeps every file already
sitting in somebody's folder importing unchanged. On import the bare column
means the default location.

Three rules make the format safe to hand-edit:

- **An absent column says nothing about that location.** A file naming two of
  five locations leaves the other three alone. So does a blank cell: an empty
  cell is not a zero.
- **A misspelt code fails the whole file**, naming the code. It is one mistake in
  one header cell affecting every line, so it is worth one sentence rather than a
  row error per line burying it.
- **A count cannot drop below what is reserved there.** The import writes
  `greatest(count, reserved)`, because a count taken on the shop floor does not
  know about the order that arrived while it was being taken.
- **A cell may not raise a count at a closed location.** That row fails, naming
  the code — stock moves out of a closed location, never into it (D44). A cell at
  or below the current count is accepted, so an unedited export of a closed
  location's zeros still imports.

Mixing `stock_on_hand` and `stock_on_hand:<code>` in one file is refused: there
is no way to tell which one a row means.

### The inventory file

For the day the count is the only thing changing, there is a file that is
nothing but counts: one row per variant per location, whether or not the
shelf has a row yet.

```http
GET  /api/admin/export/admin-inventory                 # sku,product_title,variant_label,location,on_hand,reserved,available
GET  /api/admin/export/admin-inventory?format=shopify  # Handle,Title,Option1 Name … SKU,HS Code,COO,Location,Incoming,Unavailable,Committed,Available,On hand
POST /api/admin/import/inventory                       # the header says which dialect
```

The store's own layout names the location by code, blank for the default;
Shopify's names it by name, and its `Committed` is what is reserved here.
On import, `on_hand` (`On hand`) is the count, and when it is blank
`available` (`Available`) is read against what is reserved at that location
right now. Each row is its own transaction under the rules above — a count
cannot drop below what is reserved, a closed location takes nothing in —
and a row naming a SKU or location the store does not have is a row error,
not a failed file. Shopify's `not stocked` leaves the row alone.

## How to find what is running out

```go
variants, total, err := app.Stock().LowStock(ctx, gocommerce.LowStockQuery{
    Threshold: 5, Limit: 50,
})
```

```http
GET /api/admin/inventory/low-stock?threshold=3&limit=20&page=2
```

The threshold defaults to 5 and compares against *available*, not on-hand, so
units already promised to open orders count as gone. Only variants with
`track_inventory` are considered — an unlimited variant is never low. Results
are ordered by availability ascending, so the most urgent row is first.

`sort` re-orders it: `sku`, `on_hand`, `reserved`, `available` or `price`, with
`order=asc|desc`, allow-listed the way every other listing's is (D46). The three
stock keys follow `location_id` — without it they order by the store-wide sums
on the row, and with it by that one location's own numbers — so the column being
sorted is always the column being read. Anything else is a 400 naming what
exists.

Without `location_id` the threshold is against the **store's total across every
location**. That is what a single-location store means and what a multi-location
store often does not: a variant with one unit in each of five shops is not low by
that reading, even though every shelf looks it — so a shop that is empty while
the warehouse is full never appears.

```http
GET /api/admin/inventory/low-stock?threshold=3&location_id=2
```

With `location_id` the threshold is against that shelf alone, each row carries
`at_location`, and an unknown location is a 404 rather than an empty page. Only
variants the location already has a stock row for are considered, because a SKU
it has never carried is not a shelf it can restock.

Pagination is the engine's standard contract: `limit` with either `offset` or
`page`, and **`page` wins when both are sent**. The `meta` block carries
`total`, `limit`, `offset`, `page` and `total_pages`.

## How to make an item that never runs out

Set `track_inventory` to false on the variant; the quantity columns are then
ignored by every movement.

```go
tracks := false
v, err := app.Products().UpdateVariant(ctx, variantID,
    gocommerce.VariantPatch{TrackInventory: &tracks})
```

Cart lines for such a variant report `"available": -1`, which is the wire
signal for "not tracked" — a storefront must not render it as a quantity.

## Common mistakes

- **`UPDATE variant_stock SET on_hand = …` from a module or a script.** It skips
  the reserved-quantity check, the row lock, the event *and the ledger row* — so
  the balance it leaves is one nothing in the store can explain, and
  `gocommerce doctor`'s `stock ledger` check is the only thing that will ever
  tell you. Rule 3 in
  [`AGENTS.md`](../AGENTS.md): reading with SQL is fine, writing is not. Use
  `app.Stock()`. (`variants.stock_on_hand` is not a column at all any more — M17
  dropped it. A query naming it fails loudly, which is the right outcome.)
- **Treating `stock_on_hand` on a variant as a column.** It is a sum across
  locations, computed on the way out. Filtering or ordering on it in your own SQL
  means repeating the subquery — `variantOnHand` and `variantAvailable` in
  `catalog.go` are there to be reused so the two cannot drift.
- **Restocking to the default instead of to where the units came from.** The
  order line records its location for exactly this reason. `lineLocation` reads
  it back; a movement that hardcodes the default silently teleports stock between
  shelves.
- **Reporting `stock_on_hand` as "in stock" in a storefront.** On-hand includes
  units already promised. The sellable number is `available`, and
  `Variant.InStock(qty)` is the predicate that also handles the untracked case.
- **Adding stock by calling `SetOnHand` with what you think the new total is.**
  Between your read and your write, a sale may have committed. `Adjust` with the
  delta you actually received is the operation that survives concurrency.
- **Restocking a cancelled *pending* order.** It never left the shelf; only the
  reservation needs releasing. `Orders.Cancel` already picks correctly — do not
  "help" it with a manual adjustment afterwards.
- **Putting returned goods back with `Stock().Adjust`.** Its ledger row says an
  operator adjusted the count, which is true and not the point: nothing ties it
  to the order the goods came off, nothing caps it at what actually went out,
  and nothing stops the same units being put back twice. `Order().Return` is the
  operation that does, per line and per quantity — and its movement carries the
  order number.
- **Reading the ledger to answer "what is on the shelf".** `variant_stock` is
  the truth; `stock_movements` explains it. Summing the ledger to get a count is
  slower, and it is the answer that goes wrong first if anything ever did bypass
  the service.
- **Calling `Adjust` from a script with an empty reason.** It is accepted — the
  engine requires no reason from any client, deliberately, because demanding one
  would break every script written before M26 — and it produces the row an
  operator finds three weeks later and cannot interpret. The panel insists on a
  reason for a count and for a write-off; a script should hold itself to the
  same rule.
- **Assuming a failed checkout leaves stock reserved.** The reservation is made
  inside the order transaction; a conflict rolls the whole thing back. Only a
  *created but unsettled* order holds stock — payment pending, or recorded as
  failed — and the sweeper is what eventually frees it.
