---
name: b2b
description: Use when selling to businesses — companies, buyer roles, orders on account, credit limits, approvals, quotes, quick and repeat orders, partial checkout, or dealer territories and lead routing — or when a trade price is reaching somebody it should not.
---

# B2B: companies, accounts, approvals, quotes and dealers

## The model

[`ext/b2b`](../ext/b2b/b2b.go) sells to businesses on top of
[`ext/identity`](../ext/identity/identity.go). A **company** has **buyers**,
each an identity account in one role — `admin`, `approver` or `buyer` — and
the company's terms: a customer group for its prices, a credit limit and net
days for buying on account, an approval threshold, and whether a purchase
order number is required. Core has no customer concept (D22), so all of this
lives in the module's own `b2b_*` tables. A company can also be a **dealer**:
it has **territories**, and a consumer's enquiry from the address one covers is
routed to it as a **lead**.

Three engine decisions carry it, and each is why something here looks the way
it does.

- **A company's prices are a customer group's, and a group price needs a
  proven address (D66).** Joining a company puts the buyer's address into the
  company's group, and the buyer's cart is priced as them only through
  `POST /x/identity/me/carts`, which refuses an unconfirmed address. Accepting
  an invitation is the confirmation: the token went to that mailbox.
- **Limits are a checkout guard (D67).** The module's guard runs inside the
  checkout transaction with the order's final total, under a per-company
  advisory lock. A credit limit or an approval threshold is refused before the
  order exists — never cancelled after.
- **A quote is an agreed price (D68),** placed through `Orders.Create` with
  `NewOrderLine.UnitPriceMinor`, never a price list a whole group would reach.

## How a buyer orders

1. The store creates the company (`POST /api/admin/x/b2b/companies`) and adds
   buyers (`POST /api/admin/x/b2b/companies/{id}/members`). An address
   identity has already confirmed joins at once (`201 member`); any other is
   invited (`202 invitation`, an email with a token).
2. The buyer signs in through identity and accepts:
   `POST /x/b2b/invitations/accept {"token": …}`. The signed-in account's
   address must be the invited one.
3. The buyer checks out their cart with `POST /x/b2b/checkout` — not the
   public checkout, which refuses a company-priced basket with
   `403 company_checkout_required`. `on_account` is the default method when the
   company has an account: the order is confirmed with payment pending, due
   `net_days` later, until somebody marks it paid.
4. Over the company's approval threshold, a `buyer`'s checkout answers
   `202 {approval}` instead of an order. An `approver` or `admin` places it with
   `POST /x/b2b/approvals/{id}/approve`, at the prices in the request.

Quotes: a buyer asks (`POST /x/b2b/quotes`, quantities only), the store prices
and sends (`PUT /api/admin/x/b2b/quotes/{id}`, then `…/send`, under
`quotes.write`), and the buyer accepts (`POST /x/b2b/quotes/{id}/accept`) — an
order at the quoted prices, or an approval when it is over the buyer's limit.

## Filling a basket, and checking out part of one

Three routes put things in a basket, and none of them sets a price. Each claims
the basket for the buyer first and then adds every line through
`app.Cart().AddLine`, so a line costs what it costs anywhere else today, and the
basket is checked out through `POST /x/b2b/checkout` like any other.

- **Quick order:** `POST /x/b2b/cart/lines` with up to 500 lines by `sku` or
  `variant_id`, into `cart_id` or a new basket. A line that cannot go in comes
  back in `rejected` with a reason — `not_found`, `inactive`,
  `insufficient_stock`, `invalid` — and the rest still go in; the answer is
  200 either way.
- **Repeat order:** `POST /x/b2b/orders/{order_id}/reorder` copies an order's
  lines into a new basket at today's prices, with the same `rejected` list. The
  order must be in the company's ledger and visible to the caller by the rule
  `GET /x/b2b/orders` applies. It never places anything.
- **Partial checkout:** `line_ids` on `POST /x/b2b/checkout`. The chosen lines
  are copied into a basket of their own — same storefront, priced as the
  buyer, the same discount code — and that basket is checked out, so the guard
  judges exactly those lines. They leave the buyer's basket only once they are
  an order; a request for approval filed instead leaves it untouched. Every
  line chosen is just a checkout of the whole basket.

## Dealers and leads

The store gives a dealer territories (`POST /api/admin/x/b2b/companies/{id}/territories`,
under `companies.write`): a country, a state in it, a postcode prefix in that.
A territory has one dealer — the area is unique — and both state and prefix are
matched case-blind, the prefix with spaces and hyphens ignored.

A storefront's dealer form posts to `POST /x/b2b/leads`, which needs no
session. The lead goes to the most specific territory that covers the address:
one naming a state outranks one that does not, and within that the longest
prefix wins. A closed dealer is skipped for the next one that covers the
address; an on-hold dealer still sells, so it still receives. Nobody covering
it leaves the lead with the store (`routed_by: unrouted`). The dealer's admins
and approvers are emailed `b2b.lead_routed`, at the address their membership
holds.

The dealer works its leads at `GET /x/b2b/leads` and reports with
`PATCH /x/b2b/leads/{id}`. The store sees every lead at
`GET /api/admin/x/b2b/leads` (`leads.read`; `?unrouted=true` is its own) and
hands one to a dealer, or takes it back, with
`PATCH /api/admin/x/b2b/leads/{id}` (`leads.write`), which emails the new
dealer and starts the lead again at `new`.

## Invariants

- **An address in a company's group is a proven, current one.** The module
  takes the old address out when an account changes it, and drops a deleted
  account's membership; identity announces neither, so the buyer's own next
  request and the hourly reconciler are what notice. A member's `email` is the
  address the group holds — empty, with `confirmed: false`, while they have
  moved to one they have not confirmed. Recording the new address before it
  was confirmed would leave it out of the group for good.
- **`on_account` exists only inside this module.** Its `Configured` is true
  only with the module's placement in the context, so it is absent from
  `GET /api/checkout`, from the public checkout and from a phone order.
- **`metadata.b2b` on an order is the module's,** written in the order's own
  transaction. The guard refuses it from anywhere else, which is what lets the
  credit query read it back.
- **What a company owes is read from the orders,** not from `b2b_orders`:
  orders on account, unpaid, not cancelled. The ledger row is written after
  the order commits and the reconciler fills a missed one in.
- **A partial checkout is one row in `b2b_partial_checkouts`, claimed before
  anything is built.** Only one of a basket is in flight at a time — the
  guarantee core's row lock gives a whole basket — and a retry under the same
  `Idempotency-Key` checks out the basket the first attempt built, which core
  then replays. The copy is emptied when it does not become an order, so the
  abandoned-cart sweep never writes about a basket the buyer never saw.
- **The lead form says nothing about where a lead went.** It answers
  `202 {"accepted": true}` whoever got it and whether anybody did; anything
  more maps the dealer network a postcode at a time.

## Common mistakes

- **Adding an unconfirmed account to a company directly.** That is the hole
  D66 closed: whoever registered a dealer's address first would inherit the
  dealer's terms. `AddOrInvite` invites anybody identity has not confirmed.
- **Checking a limit in an event handler.** It runs after the order exists.
  The guard is the only place a refusal leaves nothing behind.
- **Testing the credit lock with one product.** Two checkouts of the same
  variant are serialised by the stock row lock, so a concurrency test passes
  with the advisory lock removed. Use two products, as
  `TestTwoCheckoutsCannotBothFitUnderOneLimit` does.
- **Expecting shipping or tax to be frozen in an approval.** The request keeps
  the lines and their prices; delivery and tax are worked out again when the
  approver places it, against the address in the request.
- **Placing a repeat order at the old prices.** The old order may have been a
  quote's prices or a list since changed. Reorder builds a basket; the buyer
  checks it out.
- **Leaving the lead limit at ten behind a proxy.** The form is limited per
  peer address, never `X-Forwarded-For`, so behind a reverse proxy every
  visitor shares one budget. Raise `Config.LeadsPerMinute`.
- **Expecting a state-scoped territory to catch a form that asks no state.** A
  territory naming a state matches only an enquiry naming it.

Related: [carts](carts.md), [checkout](checkout.md), [payments](payments.md),
[team](team.md).
