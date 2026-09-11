---
name: carts
description: Use when opening a guest cart, adding or changing line items, or explaining why a cart's prices are snapshots that checkout refuses to override.
---

# Carts

## The model

A cart is a guest's basket, and its token is the only credential involved. No
column here references a customer, and none ever will: guest checkout is a
permanent guarantee (D22), not a stage the project grows out of. Possessing the
token *is* the authorisation, which is why `token()` mints an unguessable one.

The JSON `id` of a cart is that token — `Cart.ID` (the row id) is tagged
`json:"-"` and never leaves the process. Everything else on the wire is
`status` (`open` / `converted` / `abandoned`), `currency`, `email`,
`line_items`, `item_count`, `subtotal`, `metadata` and the three timestamps.

`cart_line_items` is `(cart_id, variant_id)`-unique and stores `quantity` plus
`unit_price_minor` — the price as it was when the line was added. Reading a cart
joins the live variant, so each line reports both coordinates:

```
unit_price / total        the snapshot, and quantity × snapshot
current_price             what the variant costs right now
price_changed             the two disagree; checkout will refuse
available                 on_hand − reserved, or -1 when not tracked
in_stock                  variant is active and can cover this quantity
```

A storefront that renders `price_changed` and `in_stock` warns the shopper on
the cart page instead of surprising them at the end of checkout.

## Invariants

- **Prices are snapshotted at add-time, and [checkout](checkout.md) refuses to
  silently reprice.** If a line's `unit_price_minor` no longer matches the
  variant's `price_minor`, the whole checkout is rejected with `409` and
  per-line detail (`{"variant_id", "sku", "reason": "price_changed",
  "current_price_minor"}`). A shopper agrees to a total, not to a moving one —
  and the alternative, charging whatever the catalog says at the moment the
  button was pressed, is how a store quietly overcharges people. After the
  refusal, `refreshCartPrices` re-snapshots the cart to current prices so the
  next attempt succeeds against numbers the shopper can now see.
- **`cart_line_items.variant_id` is `ON DELETE CASCADE`; `order_lines.variant_id`
  is `ON DELETE SET NULL`.** The asymmetry is deliberate. A cart line for a
  deleted variant cannot be bought, so it should disappear with it — and
  `RESTRICT` would let one abandoned guest cart block an operator from ever
  removing a product. An order line is the opposite case: it is an immutable
  snapshot of a sale, carrying its own sku, title, label, quantity and price, and
  it must stay readable for accounting and support long after the catalog has
  moved on. So it loses the reference and keeps the record.
- **Only an `open` cart can be modified — and an `abandoned` one revives.**
  `openCartID` selects `FOR UPDATE`; an `open` cart proceeds, an `abandoned` one
  is put back to `open` with a fresh TTL under that same lock, and anything else
  is `409 — "this cart has already been checked out"`. Only `converted` reaches
  that branch now, so the message is true for the first time. The lock is what
  stops two tabs adding the last unit at once, and it is also what makes reviving
  atomic with the mutation that triggered it.
- **Money is minor units plus a currency code.** `subtotal`, `unit_price`,
  `total` and `current_price` are all `{"amount_minor": 2499, "currency":
  "USD"}`. The cart's currency comes from `Config.Currency`.
- **A cart expires, and what happens then depends on whether anything is in
  it.** `expires_at` is pushed forward by `touchCart` on every mutation
  (`Config.CartTTL`, 720h by default). Past it, the five-minute sweeper runs
  three phases with disjoint predicates: `Abandon` marks an expired cart that
  holds at least one line `abandoned`, stamps `abandoned_at` and emits
  `cart.abandoned`; `SweepExpired` deletes an expired cart that holds nothing;
  `PurgeAbandoned` deletes an abandoned cart once `Config.CartRetention` (720h
  by default, clocked off `abandoned_at`) has passed. `POST /api/carts` is a
  public row-creating endpoint, so the table still needs a bound — it now has
  two, and what survives between them is "baskets somebody actually filled, for
  a fixed window".

## How to open a cart and add lines

```go
cart, err := app.Cart().Create(ctx, "" /* or the shopper's email */)
cart, err = app.Cart().AddLine(ctx, cart.Token, variantID, 2)
```

```http
POST /api/carts                       → 201 {"data": {"id": "<token>", …}}
POST /api/carts/<token>/line-items    {"variant_id": 77, "quantity": 2}   → 200, the whole cart
```

The create body is optional — shopping starts before a shopper has told you
anything about themselves — and `quantity` defaults to 1 when omitted or zero.
Every line-item route returns the full refreshed cart, so a client never has to
stitch a response into local state.

`AddLine` on a variant already in the cart sums the quantities, and checks
availability against `existing + qty` before doing so:
`409 — "only %d left in stock"`. An inactive variant is
`409 — "that variant is not available"`.

## How to change or remove a line

```go
cart, err := app.Cart().UpdateLine(ctx, token, lineID, 3)
cart, err := app.Cart().RemoveLine(ctx, token, lineID)
```

```http
PATCH  /api/carts/<token>/line-items/12   {"quantity": 3}   → 200
PATCH  /api/carts/<token>/line-items/12   {"quantity": 0}   → 200, line removed
DELETE /api/carts/<token>/line-items/12                     → 200
```

Quantity `0` removes the line rather than erroring, because that is what a
quantity stepper stepping down to nothing means. A line id belonging to another
cart is `404 — "line item %d is not in this cart"`, never someone else's data.

## How to handle a price change before checkout

Read the cart and act on the flags rather than waiting for the 409:

```go
cart, err := app.Cart().GetByToken(ctx, token)
for _, l := range cart.Lines {
    if l.PriceChanged || !l.InStock {
        // show the line, its CurrentPrice and Available, and ask for confirmation
    }
}
```

If checkout has already refused, the cart has been re-snapshotted for you:
show the new totals, get confirmation, and post the same checkout again.

## Abandonment and recovery

A cart has four fates, and three of them are the sweeper's:

```
open ──checkout──────────────► converted          (terminal)
open ──expired, has lines────► abandoned          (Abandon, + cart.abandoned)
open ──expired, empty────────► deleted            (SweepExpired)
abandoned ──any mutation─────► open, fresh TTL    (revive)
abandoned ──CartRetention────► deleted            (PurgeAbandoned)
```

Abandonment is the store's observation about a shopper, not a decision by the
shopper, so it never destroys the basket: the first mutation on an abandoned
cart — a line added, a quantity changed, an email set, a discount code applied,
a checkout — revives it to `open` under the lock that mutation already takes.
Nothing new is validated on the way back, because checkout re-reads the live
price and refuses with `price_changed`, re-reads stock, and re-decides the
discount code whatever status the cart arrived in.

`status` and `abandoned_at` cannot drift apart: `carts_abandoned_at_matches_status`
is a biconditional CHECK, so a marked row always carries a timestamp and a
revived one always loses it. A marked row with no timestamp would match no purge
predicate and live forever with somebody's email in it; a stale timestamp
surviving a revival would purge a basket out from under a live shopper.

`cart.abandoned` carries a `CartEvent`: the cart id, the **token** (a
credential — put it in a recovery link and nowhere else), currency, email,
item count, subtotal in minor units, the discount code, the lines as
`OrderEventLine`, and three times — `created_at`, `last_active_at` (the
shopper's last touch, which the sweep deliberately does not overwrite) and
`abandoned_at`. Core's notifier is **not** subscribed to it: when and how often
to chase an abandoned basket is a marketing decision, not an engine default. A
recovery module subscribes and owns the schedule.

## What an operator can see

Two read-only admin routes, both under `orders.read`:

```http
GET /api/admin/carts?state=abandoned&has_lines=true&has_email=true   → 200 {"data": [CartSummary], "meta": …}
GET /api/admin/carts/42                                             → 200 {"data": CartDetail}
```

They address a cart by its numeric row id, never by token, and **neither
returns the token** — exactly as an order's `access_token` never appears in an
admin read. An operator can see a basket and cannot touch one; there is no
admin write route, and acting on a basket means placing the order at
`POST /api/admin/orders`.

`state` is derived from `status` AND `expires_at` — `live`, `abandoned` or
`converted` — so a cart past its TTL that the sweeper has not reached yet reads
as `abandoned` while its `status` still says `open`. Both are reported, and the
filter uses the derived predicate, so the screen cannot give a different answer
depending on where the five-minute ticker happens to be. The other filters are
`has_email`, `has_lines`, `min_value_minor` (minor units, filtering on the sum
of quantity × snapshot price) and `from`/`to` over `updated_at`.

## Common mistakes

- **Treating the cart's `id` as a row id.** It is the token — an opaque string,
  and the only credential. Do not log it, and do not put it in a URL a third
  party will see in a `Referer` header.
- **Expecting an admin cart route to hand you the token, or to write.** Neither
  admin route returns one and neither writes: `GET /api/admin/carts` and
  `GET /api/admin/carts/{id}` are read-only, keyed by row id, and gated on
  `orders.read`. Every mutation of a cart is still the shopper's, taken under
  their token.
- **Looking for a DELETE that clears the email.** There is none, because the
  setter clears: `PUT /api/carts/<token>/email` with `{"email": ""}` is the
  shopper's own withdrawal path. A non-empty value with no `@` in it is
  `400 — "a valid email is required"`, and the route extends the cart's TTL like
  every other mutation.
- **Expecting `AddLine` to reprice the line.** It does not, deliberately: the
  `ON CONFLICT` clause adds to `quantity` and leaves `unit_price_minor` at the
  value the first add captured. It used to overwrite it, which quietly moved
  units already in the basket to today's price and erased the evidence
  [checkout](checkout.md) needs to notice a change at all. `UpdateLine` takes
  an absolute quantity and is the clearer call when you mean "make it three".
- **Assuming `GetByToken` enforces expiry, or that an expired cart is gone.** It
  enforces nothing, and the row survives: an expired cart with lines now reads
  back with `status: "abandoned"` where the row used to have been deleted, and
  the next mutation revives it. A storefront that branches on
  `status === "open"` before rendering will show an empty basket where it used
  to open a fresh cart — check `expires_at` and `status` deliberately, or simply
  add the line, which recovers the basket on its own.
- **Rendering `available: -1` as a quantity.** It means the variant does not
  track inventory — see [inventory](inventory.md).
