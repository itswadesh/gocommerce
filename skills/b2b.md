---
name: b2b
description: Use when selling to businesses — companies, buyer roles, orders on account, credit limits, approvals or quotes — or when a trade price is reaching somebody it should not.
---

# B2B: companies, accounts, approvals and quotes

## The model

[`ext/b2b`](../ext/b2b/b2b.go) sells to businesses on top of
[`ext/identity`](../ext/identity/identity.go). A **company** has **buyers**,
each an identity account in one role — `admin`, `approver` or `buyer` — and
the company's terms: a customer group for its prices, a credit limit and net
days for buying on account, an approval threshold, and whether a purchase
order number is required. Core has no customer concept (D22), so all of this
lives in the module's own `b2b_*` tables.

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

Related: [carts](carts.md), [checkout](checkout.md), [payments](payments.md),
[team](team.md).
