---
name: b2b
description: Use when selling to businesses — companies, buyer roles, orders on account, credit limits, approvals, quotes, quick and repeat orders, partial checkout, catalogues, statements and terms history, or dealer territories and lead routing — or when a trade price is reaching somebody it should not.
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

Four engine decisions carry it, and each is why something here looks the way
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
- **A company's delivery and tax are its group's too (D76).** A shipping rate
  that names the company's customer group replaces the public rates in that
  zone for its buyers, and a tax-exempt group sells without tax; both are read
  from the same proven address as the prices. They are core's, configured on
  the Shipping and Customer groups screens, so nothing in this module sets
  them — see [checkout](checkout.md).

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
5. The store chases payment from `GET /api/admin/x/b2b/receivables` — every
   company's orders on account, `?overdue=true` for the late ones, `?q=` for a
   PO or order number — and reads one order's place in the ledger, due date
   and all, at `GET /api/admin/x/b2b/orders/{order_id}`. Payment is recorded on
   the order itself, with core's mark-paid.

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
  `insufficient_stock`, `invalid`, `not_in_catalogue` — and the rest still go in; the answer is
  200 either way. The same route takes a spreadsheet: `Content-Type: text/csv`,
  the basket in `?cart_id=`, a header naming `quantity` and `sku` or
  `variant_id` in any case, other columns ignored. It is read by core's
  `CSVReader`, so a byte-order mark and an export's escaping apostrophe are
  taken off (D62), and each rejected line carries its `row` — the header is
  row 1, as a spreadsheet numbers it. A file with no such header is a 400.
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

## Catalogues

A company may be held to a **catalogue** (D75): categories, each with every
product filed under it or under the categories beneath it, and products one by
one. A company with none buys everything. The store manages them at
`/api/admin/x/b2b/catalogues` (`companies.read` to read, `companies.write` to
change) and holds a company to one with `catalogue_id` on the company, which
the terms history records. One a company is held to cannot be deleted.

It is held where the order is made. The checkout guard refuses a basket with a
line outside the catalogue — `403 not_in_catalogue`, each such line in
`details` — before the approval threshold, so it is never filed for an
approver, and an approval or an accepted quote placed later is checked again.
Quick order, a file and a repeat order leave such a line out with reason
`not_in_catalogue`; a quote request, the store's pricing of a quote and its
sending refuse one. The subtree is read from core's category tree when the
check runs, so moving a category moves its products in or out.

`GET /x/b2b/catalogue?q=&category_id=&page=&per_page=` lists what the buyer's
company may buy, by title, each active variant with `price_minor` and
`currency`: core's `Pricing.PriceInChannel` for the address the company's
group holds for the buyer, on the default storefront a quick order's basket is
opened on — what that basket would charge for one. `q` matches a title or a
SKU; `per_page` is `limit`.

## Statements and the terms history

A company's **statement** is its account over a period (D74):
`GET /api/admin/x/b2b/companies/{id}/statement?from=YYYY-MM-DD&to=YYYY-MM-DD`
under `companies.read`, and `GET /x/b2b/statement` for the signed-in buyer's
own company — its admins and approvers only; a plain buyer gets 403. `from` is
the first day and `to` the day after the last, exclusive as every range in the
engine is, both civil dates in the store's time zone (its profile's; UTC when
it has none). Leaving both out is the month to date. It answers the opening
balance, every order placed on account (a debit, with its PO number and due
date), every payment (a credit), a payment taken back with core's mark-unpaid
(`payment_reversed`, a debit), an order cancelled while still owed (a credit),
the running and closing balances, and `aging` — what is owed at the end by
whole days past due in the store's calendar. `format=csv` answers the same
through core's CSV writer: one table, the opening balance its first row and
the closing balance and aging buckets its last, each named in `kind`.

**Where a payment's date comes from.** Core keeps an order's payment status and
no time it was paid. The module subscribes to `order.paid`, `order.unpaid` and
`order.cancelled` and files each order on account in `b2b_account_entries` at
the event's `At` — the outbox row's `created_at`, the `now()` of the
transaction that paid it — however late the event is delivered. Before a
statement is read, and in the hourly pass, any order whose entries disagree
with what core says of it now is read back from `Orders.Timeline`, whose audit
rows and events carry the same instants. Only a transition with no record left
anywhere — an order imported already paid — is filed at the moment the module
noticed it, with `date_source: noticed`: it happened at or before then. Every
other line is `recorded`.

The **terms history** is `GET /api/admin/x/b2b/companies/{id}/history`
(`companies.read`, newest first): a row per term that moved — status, credit
limit, net days, approval threshold, PO rule, customer group, catalogue —
written in the
same transaction as the change, with the old and new value as the API takes
them and who made it: `operator` (with their address), `token`, `buyer` (a
company admin) or `system`. Creating a company records its first terms as
`action: created`.

## Dealers and leads

The store gives a dealer territories (`POST /api/admin/x/b2b/companies/{id}/territories`,
under `companies.write`): a country, a state in it, a postcode prefix in that.
A territory has one dealer — the area is unique — and both state and prefix are
matched case-blind, the prefix with spaces and hyphens ignored. A state in the
United States, Canada, Australia or India is read by its code or its name —
"California", "ca" and "US-CA" are all `CA` — on both sides, and a territory
given a name is stored as its code. This is core's rule for every state (D72,
[`core/subdivisions.go`](../core/subdivisions.go)), the same one tax rates and
shipping zones follow; anywhere else a state is matched as written.

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
`GET /api/admin/x/b2b/leads` (`leads.read`; `?unrouted=true` is its own,
`?routed_by=` and `?q=` narrow it) and one at `GET /api/admin/x/b2b/leads/{id}`, and
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
- **A statement closes on the outstanding figure.** Owed is what the credit
  check counts — on account, neither paid nor cancelled — and the statement
  files whatever it missed before it is read, so a statement ending today and
  `GET …/credit` never disagree.
- **A catalogue is a control, not a filter.** The guard refuses what it leaves
  out whatever filled the basket — the public cart routes included — and
  approvals and quotes are checked again when they are placed.
- **A term changes with its record or not at all.** `UpdateCompany` reads the
  terms under the row's lock and writes the history in the same transaction.
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
- **Dating a payment from `orders.updated_at`, or from when an event was
  delivered.** The first moves on every edit and the second on every retry.
  The event's `At` and the order's timeline are when it was recorded; with
  neither, the line says `noticed`.
- **Expecting a catalogue to hide products on the storefront.** The public
  listing knows nothing of companies (D22); a held buyer can see and add
  anything there, and is refused at `POST /x/b2b/checkout`. A storefront that
  wants the narrowed range lists `GET /x/b2b/catalogue` instead.
- **Pricing the catalogue listing from the price lists yourself.**
  `Pricing.PriceInChannel` is core's resolution — group, channel, quantity
  break, base price — and a second copy of it drifts.
- **Parsing an uploaded order with `encoding/csv` directly.** A SKU exported
  as `'-RED` would arrive with its apostrophe and match nothing, and a header
  saved by Excel would start with a byte-order mark. `gocommerce.NewCSVReader`
  handles both.
- **Reading `to` as the last day.** It is the day after, as in the reports and
  the order list; a September statement is `from=2026-09-01&to=2026-10-01`.
- **Placing a repeat order at the old prices.** The old order may have been a
  quote's prices or a list since changed. Reorder builds a basket; the buyer
  checks it out.
- **Leaving the lead limit at ten behind a proxy.** The form is limited per
  peer address, never `X-Forwarded-For`, so behind a reverse proxy every
  visitor shares one budget. Raise `Config.LeadsPerMinute`.
- **Expecting a state-scoped territory to catch a form that asks no state.** A
  territory naming a state matches only an enquiry naming it.
- **Expecting a state name outside the four known countries to match its
  code.** "Bayern" and "BY" are two states to the engine. Add the country to
  `core/subdivisions.go` — which changes tax and shipping matching too — or have
  the storefront send the form's state the way the territory was written.

Related: [carts](carts.md), [checkout](checkout.md), [payments](payments.md),
[team](team.md).

## The trade portal

A store running `-b2b` serves its buyers and dealers a portal at `/portal` on
its own host (D77): the admin panel's build, with a shell of its own named by
`GET /api/store`, signed in to with the buyer's identity account. Point the
emails at it — `GOCOMMERCE_B2B_INVITE_URL=https://shop.example.com/portal/invitation?token={token}`,
and, unless a storefront handles accounts, the identity reset and confirmation
URLs at `/portal/reset-password?token={token}` and
`/portal/confirm-email?token={token}`. On a platform, write `{domain}` for the
host and one setting serves every store.

What a buyer sees follows their role, because the routes above already refuse
the rest: every member orders — typed, pasted, uploaded as a CSV, or from the
catalogue — quotes and reads approvals; approvers and admins decide requests,
see every buyer's orders and read the statement; only admins see Team; Leads
appear for admins and approvers of a company with territories or with leads the
store handed it. Some routes exist for the portal:
`GET /x/b2b/orders/{order_id}` (an order's lines, by the order list's rule),
`cart_id` on the reorder body, so a repeat fills the basket already open, and
`GET /x/b2b/catalogue/categories` — the categories holding something the
company may buy, each counting what is under it at any depth, so a company held
to a catalogue is never offered a branch that can only filter to nothing. The
catalogue lists only products with a variant on sale, and each variant says
`in_stock`, since `available` is 0 for one whose stock is not counted.

The portal's client is `admin/src/lib/trade.svelte.js`, and it never calls
`/api/admin`; the screens are `admin/src/routes/portal/`. A new screen is a
route there and a line in `NAV`. The statement's CSV is fetched with the
buyer's token and saved from a blob, because the token is a header and not a
cookie; a CSV quick order is sent the same way, as the request body.
