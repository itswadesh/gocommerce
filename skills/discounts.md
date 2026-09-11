---
name: discounts
description: Use when writing a promotion, aiming one at products, collections or categories, or working out the arithmetic an order's discount went through.
---

# Discounts

## The model

A discount is two things that must not be confused.

**The rule** is the row in `discounts` an operator maintains: a code, a kind, a
value, a window, a limit, a scope. It can be edited. It can be deleted.

**The snapshot** is the row in `order_discounts` an order keeps: the code, the
title, the kind and the amount, by value. It exists for the same reason
`order_lines` snapshots a price rather than pointing at a variant — a promotion
that ended may be deleted, and an order from last winter must still say what it
was given and what it was called. When the rule is deleted the snapshot's
`discount_id` goes null and the rest stays.

`discount_targets` is the third table: what a scoped rule points at, as
`(discount_id, kind, target_id)`.

## The arithmetic, and all of it

```
applyDiscount(rule, base) -> amount
```

`base` is the part of the basket the rule covers. Everything else follows from
that one substitution:

* **One rounding for the whole of it**, half up. Per-line rounding drifts by a
  minor unit per line and leaves a total nobody can reconcile against the lines
  above it.
* **The amount never exceeds the base.** A fixed discount larger than what it
  covers takes all of it. The alternative is a negative total, which the order's
  own CHECK would refuse anyway, at a point far from the cause.
* **Order scope is unchanged by construction**: its base *is* the subtotal, so
  every figure a store had before scopes existed is bit-identical.

## The four scopes

`scope` names the kind of thing a rule points at. The scope is plural and the
target kind it selects is singular, because a scope describes a rule and a kind
describes one row:

| `scope` | `discount_targets.kind` | What it covers |
|---|---|---|
| `order` | — | The whole basket. No targets; sending any is refused. |
| `products` | `product` | Lines whose product is named. |
| `collections` | `collection` | Lines whose product is a member *now*. |
| `categories` | `category` | Lines filed in a named category **or anywhere beneath it**. |

The category rule is the subtree, bounded by `MaxCategoryDepth` — the same walk
a tax rate already makes, for the same reason: an operator who picks Apparel
means the shirts, and exact-node matching would make every branch category a
promotion covering nothing.

Collection membership is read at checkout rather than copied when the rule was
written, so moving a product into the sale puts it in the promotion.

A scope with no evaluator branch is refused at the first call. It is never
treated as "applies to everything" — that is the failure this design exists to
prevent, and the schema CHECK cannot help, because a new scope const would be
added to it in the same change.

## Setting targets

Targets ride on the rule, in one request and one transaction:

```go
app.Discounts().Update(ctx, id, DiscountPatch{TargetIDs: &ids})
```

There is no `PUT .../targets` route, and a module must never write
`discount_targets` with SQL (AGENTS rule 3). A second request would mean a
window in which a scoped discount exists pointing at nothing — which *is* the
broken row — and a PUT that never arrives (closed tab, dead script) would leave
it there for good.

The kind is derived from the scope and never sent. Letting a client send both
would make a scope/kind disagreement representable: a rule scoped to products
holding collection targets, which the matcher would silently ignore.

The rules the service enforces, each with its own message:

* A scoped rule must name at least one target, at create and at patch.
* An order-wide rule must name none.
* Changing the scope requires sending `target_ids` in the same body — except
  moving to `order`, which clears them.
* `free_shipping` cannot be scoped at all. Shipping is not part of any line, so
  no scope can select it.

A target whose product, collection or category has since been deleted is kept,
not cascaded away: it comes back `missing: true`, draws as a red chip, and is
counted by `gocommerce doctor`. The matcher only ever matches live ids, so a
dangling row narrows the rule rather than widening it — and an operator is told
the promotion narrowed instead of finding out at the till.

## The minimum is not the base

`min_subtotal_minor` is measured against the **whole basket**, even for a scoped
rule. It is the price of entry to a promotion, not the thing discounted: "spend
5000, get 10% off shoes" is the ordinary retail promotion, and re-pointing the
column at the targeted lines would make it inexpressible.

The floor on the targeted side is expressed differently — a basket whose covered
total is zero is refused, with a message that distinguishes the two cases: a rule
that points at nothing ("it applies to chosen products and none are chosen")
from a basket that holds none of what it points at ("that discount does not apply
to anything in this basket").

## Order of operations at checkout

Inside the one transaction, under the lock that just re-checked every price and
every reservation:

1. Look the code up `FOR UPDATE`.
2. Judge eligibility — active, window, limit, minimum, scope, targets, email.
3. **Then** claim the use, with a conditional `UPDATE ... WHERE used_count <
   usage_limit`.
4. Apply the arithmetic to the covered base.

Step 2 before step 3 is what stops a rule that matches nothing from burning a
use. A refused code costs nothing.

`Preview` (behind `PUT /api/carts/{token}/discount`) answers with the same
arithmetic and takes no locks and no uses. It is handed the whole cart rather
than a subtotal, because a scoped rule cannot be answered from one number — and
the promise it makes is that the figure a shopper is shown is the figure they are
charged.

## Tax is masked

`allocateDiscount` is told which lines the discount came off. A discount taken
off the shoes must not lower the tax base of the hats: the base a line is taxed
on is the base that line was actually charged, and with per-category rates the
order's *total* tax — not merely its split — depends on getting that right. The
figure is stored on `order_lines.tax_minor` and printed on an invoice, so it is
durable and auditable.

The same mask reaches the refundable amount a return snapshots, for the same
reason.

## Editing an order that carries one

D27, as amended by D39:

| Kind | Scope | What an edit does |
|---|---|---|
| percentage | order | Taken again, on the new subtotal. |
| percentage | scoped | Taken again, on the lines it still covers. |
| fixed | order | Survives — it was never a function of the basket. |
| fixed | scoped | Clamped down to what is left of the lines it covered. |
| any | rule deleted | Stands as recorded, clamped to the subtotal. |

An order that has fallen below the minimum its discount required is refused, and
so is one edited until the rule covers nothing. Choosing between removing the
discount and abandoning the edit is an operator's call, not a silent one.

The recompute reads the rule's *current* value and *current* targets, not the
order's copy of them: a promotion the operator has since changed is the
promotion the store is running.

## Diagnostics

`gocommerce doctor`'s `discounts` check counts the states the service now refuses
to create but a database can still hold: a scoped rule with no targets (fail), a
target naming something deleted (warn), a target row whose kind disagrees with
its rule's scope (warn), and a free-shipping rule carrying a scope (warn).

## See also

* [checkout](checkout.md) — where a discount is judged and claimed.
* [orders](orders.md) — what an edit does to an order's totals.
