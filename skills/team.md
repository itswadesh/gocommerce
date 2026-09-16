---
name: team
description: Use when adding a right, gating a route, changing what a role may do, inviting somebody to the panel, or reasoning about how an operator gets and loses access.
---

# The team: roles, rights and access

## The model

One enumerated list of rights, three fixed roles drawn from it, and one place
that decides — [`rights.go`](../core/rights.go). **Nothing outside that file may
invent a right.** What each role *carries* is the store's to change
([`roles.go`](../core/roles.go)); the list itself is not.

```
catalog.read      products, variants, categories, collections, media
catalog.write     editing any of them
inventory.read    stock levels, the low-stock report and the movement ledger
inventory.write   stock takes, adjustments and transfers
discounts.read    discount codes and what they take off
discounts.write   creating, editing and ending them
taxes.read        the rates orders are charged at
taxes.write       changing what every future order collects
locations.read    the places stock lives
locations.write   opening, closing, and choosing the default
orders.read       seeing orders and customers' purchases
orders.write      placing, editing, cancelling, settling payment
orders.fulfill    moving the goods — fulfillments and shipping
orders.refund     sending money back out — separate for exactly that reason
customers.read    orders grouped by who placed them, which is personal data
team.read         who is on the team, and who has been invited
team.write        inviting, removing, changing a role — so, granting anything
roles.write       the matrix itself: what holding a role means
data.export       the catalog or every order, as a file
data.import       changing prices and stock in bulk, from a file
store.operate     the store as a running system rather than as a shop
shipping.read     the zones and rates a buyer is charged delivery under
shipping.write    what every future order collects for delivery
channels.read     where the catalogue is sold
channels.write    opening and closing those places
plugins.read      which integrations this build carries, and which are on
plugins.write     their settings, where a gateway's live keys live
notifications.read   the catalogue of messages the store sends
notifications.write  their wording, on every channel
```

The last four pairs were `store.operate` until they were not, and the split
is worth naming. `store.operate` describes itself below as changing "no
configuration, no product and no price" — but a shipping rate decides what a
buyer pays to receive an order, a channel decides where the catalogue is
sold, a plugin's settings hold a gateway's live API key, and a template is
the wording of every message the store sends. Each had been filed under the
nearest right that would take it, and the sentence had quietly become false.

The cost was not tidiness. `store.operate` also carries the outbox, the audit
feed and `ext/mcp`'s dispatch endpoint, so letting somebody edit a shipping
rate meant handing them all of that — and in practice nobody below owner was
given any of it. **The defaults did not move**: all eight came off a right
only owner held, so owner holds them and no other role gained anything. A
store that wants a manager setting delivery prices can now grant
`shipping.write` without also handing over a page of buyers' addresses.

`store.operate` is what is left, and it is worth spelling
out what it reaches: the health report (`GET /api/admin/diagnostics`) and the
three maintenance passes that act on it (`POST /api/admin/maintenance/sweep-carts`,
`/sweep-unpaid`, `/drain-outbox`), the store-wide audit feed, the outbox and its
dead letters (`GET /api/admin/events`, and the retries that go with it),
`ext/mcp`'s dispatch endpoint, the `last_error` on an order's timeline, and —
paired with `customers.read` — deleting an account in `ext/identity`.

One of those is a personal-data surface and is the reason the right is
owner-only by default rather than merely tidy: an outbox payload is the event
as a consumer received it, so a page of them is a page of buyers' email
addresses, phone numbers, names and every line they bought. `customers.read`
does not gate that surface. A store that wants a manager on call grants it in
the matrix, deliberately, which is what configurable sets are for.

| Role | Carries |
|---|---|
| `owner` | everything, including deciding who else can — and, alone by default, `store.operate` and the four configuration pairs above |
| `manager` | the catalog, discounts, orders, refunds, stock, customers — not tax or location writes, not the team, not import/export, not `store.operate`, and none of shipping, channels, plugins or notifications |
| `staff` | sees the shop and moves orders along; no money out, no prices, no access |

The rights are coarse on purpose — one per area a person could plausibly be
kept out of, not one per button, because a permission system nobody can hold in
their head is one nobody configures correctly.

They are not, however, *lumped*. The first cut had eight and drew none of the
lines a real store asks for: discounts were written with `catalog.write`, tax
rates were read with `catalog.read` but written with `settings.write`, and
`settings.write` alone covered the team, the roles matrix, the locations, the
tax rates and the export of the whole database. `settings.write` no longer
exists; the four jobs that shared it have their own rights.

Growing the list changed nobody's access — the defaults above are the eight-right
sets spelled out against the finer list. Staff can still see tax rates and
discount codes because staff always could; what is new is that a store can say
otherwise.

That still holds with `store.operate`, and it was a decision rather than an
oversight. Manager is the role that runs the shop, so the operator who would be
told to press "release the stock nobody is paying for" is a manager — but the
right those buttons live behind also reads every colleague's actions by name,
hands an agent the whole domain-tool surface, and completes the pair that erases
a customer's account. A default reaches every store that has never opened the
matrix, so granting it to manager would widen those four surfaces on upgrade for
stores that asked for none of them. Owner carries it because owner carries
everything; a store that wants a manager on call grants it in the matrix, which
is what configurable sets are for.

The table above is the **default**. `roleRights` being a map rather than a set
of conditionals is what let M19 make the sets configurable by changing one
lookup instead of finding every place a permission is decided.

## Rights a module brings

`rights.go` says nothing outside it may invent a right. That was written
when every screen was core's, and it stopped being true the moment a module
shipped one: reviews, pages, menus, the FAQ, the contact inbox, the
newsletter list, wishlists, invoices and webhooks each added a surface an
operator can be kept out of, and each borrowed the nearest right that
already existed.

The cost was that a permission could not be given without the thing it was
borrowed from — moderating reviews meant the whole catalogue, reading the
newsletter meant the outbox — and none of those screens had a row on the
Roles grid at all, because the grid draws rights and they had none.

So a module declares its own, from `Register`, beside its plugin and its
routes:

```go
app.RegisterRight(gocommerce.RightSpec{
    Right:   "reviews.moderate",
    Label:   "Moderate reviews",
    Scope:   "Approving, rejecting, replying and deleting",
    Default: []string{gocommerce.RoleManager},
})
```

The rule `rights.go` states survives in the form that mattered: a right is
declared in exactly one place, by the code that owns the routes it gates.
What a module may not do is widen an existing role — `Default` names the
roles that carry it before the store says otherwise, and the safe value is
exactly the roles that held the right it was lifted off. Anything wider
hands every store on upgrade a permission nobody granted.

Three things follow, and each was a bug before it was a rule:

- **Owner carries every right this build has**, not every right core knows
  about. A module right owner did not hold would be a screen the owner is
  refused from in their own store.
- **A stored grant is intersected with the build, not with core.** Filtering
  against `AllRights` dropped a module's right on the way out of the table,
  so the store saved the permission, reported it saved, and refused the
  screen anyway.
- **A right from a module this binary lacks is refused** rather than stored.
  The row would gate nothing and would come back as a checkbox for a screen
  that does not exist.

`Label` and `Scope` travel with the right because the panel cannot have a
table for a module it was never built beside; the roles matrix carries them
in `catalogue` and the Roles screen reads them from there.

## Renaming a role

The set of roles is fixed and the *key* of each is an identifier — `owner`, `manager` and `staff` are written on every superuser row and in
`role_rights`, so renaming one would orphan accounts. What a store may change
is what it **calls** a role, and what it says the role is for:

```http
PATCH /api/admin/roles/{role}    # { "title": "Fulfilment", "description": "Packs and ships." }
```

In Go it is `app.Roles().SetProfile`. Both fields are optional and a blank one
restores the engine's own words (`DefaultTitleOf`, `DefaultDescriptionOf`), so there
is no separate reset verb: clearing the name is the reset. Clearing both drops
the row and the role goes back to tracking the defaults, exactly as a reset
right-set does — improving a shipped sentence then reaches every store that
never edited it (M41).

All three roles are renameable, `owner` included. That is not an inconsistency with
the rule below that owner's rights are unstorable: that rule exists so a store
cannot narrow its own way back in, and a title locks nobody out of anything.

## Re-cutting a role

A store may widen or narrow `manager` and `staff` — Settings → Roles in the
panel, or:

```http
GET    /api/admin/roles           # the matrix: every role, the catalogue, the floor
PUT    /api/admin/roles/{role}    # the whole set the role should carry
DELETE /api/admin/roles/{role}    # drop the override; the role tracks defaults again
```

In Go it is `app.Roles()` — `Matrix`, `Of`, `Set`, `Reset`.

Four rules, all enforced in `RoleRights.Set`:

- **Owner is fixed** and always carries every right — including any right added
  in a later release. It is the way back into a store that has been configured
  into a corner, so it is not storable at all: the `role_rights` CHECK does not
  accept it.
- **Every role keeps `catalog.read`** (`RequiredRights`). A role stripped past
  the floor is not a narrower role; it is an account that can sign in and see
  nothing, and removing the person says that honestly.
- **A set equal to the default is stored as no rows**, so the role goes on
  tracking a default that a later release may widen. `Reset` is therefore not
  the same as saving the defaults back — though it lands in the same state.
- **You cannot remove `roles.write` from your own role.** Owner is immune
  because it is not configurable; the case that bites is a `manager` who was
  handed `roles.write`. A static admin token has no role, so it is exempt — and
  it is what undoes this kind of mistake.

It is deliberately **not** an escalation guard: a role with `roles.write` can
widen itself, and one with `team.write` can hand out the owner role outright.
Those two rights *are* what granting access means. Handing them out is the
decision; a check here that read like a boundary without being one would be
worse than none.

`role_rights` has no foreign key to the rights — they live in Go — so a right
dropped in a later release leaves rows that resolve to nothing. The lookup
intersects with `AllRights`, which is the safe direction to be wrong in.

## Gating a route

```go
a.HandleAdminFunc("POST /api/admin/tax-rates", a.handleCreateTaxRate, RightSettingsWrite)
```

The rights are **variadic**, so forgetting them is silent: the route mounts,
authentication still runs, and every signed-in operator reaches it whatever
their role. That is how the discount and tax routes once shipped ungated, where
a staff account — deliberately denied `catalog.write` — could have created a
hundred-percent-off code. `TestEveryAdminRouteDeclaresRights` is the guard, and
its exemption list is named rather than pattern-matched so that adding one is a
decision somebody writes down.

`requireRights` checks the operator's **resolved** set, not the role's defaults:
authentication already applied the store's matrix, so the middleware costs no
query and cannot disagree with what the panel was told. It runs after
authentication and refuses by name
(`403 — "your role (staff) does not carry orders.refund"`), because an operator
told only "forbidden" has to guess, and whoever sets roles needs to know what to
grant.

**A static admin token carries every right.** It is the bootstrap credential and
the one scripts use; narrowing what a script may do is a decision about who holds
the token, not about the route.

## Adding a right

Three edits and no migration — `role_rights` has no foreign key to the rights,
because the rights live in Go. Declare the constant in `rights.go`, **append** it
to `AllRights` (that slice is the order the Roles screen draws its grid in,
down the rows and across each one, so inserting one silently moves a pill an
operator has learned the position of), and decide which default sets carry it.

The panel needs no edit for it. A right is `resource.verb`, and the Roles
screen splits on the dot: a new verb appears as a pill on its resource's row, a
new resource appears as a row of its own, and `admin/src/lib/rights.js` only
supplies the label — an unlabelled one falls back to its own name rather than
disappearing. Worth adding the label and the scope sentence in the same commit
all the same: a row that says `orders.refund` and nothing else asks the person
granting it to already know what it covers.

Two tests keep it honest in opposite directions, and they are the reason the
right and the route have to land in one commit:

- `TestEveryRightGatesSomething` fails on a right in `AllRights` that gates no
  route — nothing can be denied by it, so it is a checkbox that does nothing.
- `TestRouteRightsExist` fails on a route asking for a right the engine does not
  have, which would otherwise deny everybody quietly.

The panel glosses a right in one place, `admin/src/lib/rights.js`
(`RIGHT_ORDER`, `RIGHT_LABELS`, `RIGHT_SCOPES`), and a Go test parses that file
and fails if it drifts from `AllRights`. A right the panel has not caught up with
still renders — the matrix falls through to an "Other" group and both lookups
fall back to the identifier — but as a dotted name nobody can act on.

## How somebody joins

Invite them. `POST /api/admin/invitations` returns a `token` and an `accept_url`
**once** — only the SHA-256 is stored, exactly as with a session — and the
invitee sets their own password at `/accept-invite/<token>`, which signs them in.

The alternative, which this engine did until M18 and still supports for the cases
invitations cannot serve, is an owner choosing somebody else's password and then
telling it to them. That password is known to two people from the moment it
exists and is almost never changed.

- Re-inviting an address **replaces** the outstanding invitation, so the previous
  link stops working. A partial unique index enforces one open invitation per
  address; two live links means revoking the one you can see does not close the
  door.
- Accepting is one transaction and claims the invitation with a conditional
  `UPDATE`, so two people opening one link cannot both become operators.
- An address already on the team is refused, pointing at the role endpoint —
  which cannot be used to hand out a fresh password.

## The two lockouts

Both are the same failure: nobody left who carries `team.write`, which is the
only right that can hand out a role. The survivors cannot promote anybody,
including themselves, and the way back in is a database client.

- **Demoting the last owner** — `Superusers.SetRole` refuses. The owner rows are
  locked and then counted (PostgreSQL refuses `count(*) … FOR UPDATE`), so two
  requests each demoting one of the last two owners serialise.
- **Deleting the last owner** — `Superusers.Delete` refuses, and separately
  refuses to delete the last superuser at all. Deleting is not gentler than
  demoting; it is the same door, and it was open until M18.

## Losing access

A role is read from the row on every request, and the rights that role carries
are resolved with it, so **a demotion or a narrowed role takes effect on the
operator's next request** — no waiting for a session to expire, and nothing to
revoke. Nothing is cached anywhere, which is the point: a second server holding
last week's answer to "who may refund" is not a cache, it is a hole. A session has to
be ended explicitly:

```http
POST /api/admin/superusers/{id}/revoke-sessions   # team.write — before removing somebody
POST /api/admin/me/revoke-sessions                # your own, including this browser
```

Both report how many sessions ended, because 0 and 4 mean quite different things
to somebody who has lost a laptop. `GET /api/admin/superusers` carries the same
two numbers per operator — `sessions` and `newest_session` — so that question is
answerable **before** the button rather than in the toast afterwards. They ride
on the listing row and not on `Superuser` itself: that record is returned
directly by login, refresh, create, update and set-role, and a `"sessions": 0` on
a successful sign-in response would be a fact that is both false and unfixable
from those paths.

`newest_session` is when the newest **live** session started, and must never be
shown as a last sign-in. Expired rows are hard-deleted on every issue, and a
revoke or a password change deletes them outright, so `null` means *nobody is
signed in now* and never *has never signed in*. This store keeps no sign-in
history and these fields do not invent one.

Deleting an operator cascades their sessions away with them.

## Changing your own password

`PATCH /api/admin/me` — and deliberately **not** behind `team.write`, which is
also the right to change everybody's role. Gate it there and a staff member
who suspects their password is known must ask an owner to choose a new one for
them, which is the practice invitations exist to end.

`current_password` is required for either change: a session left open on an
unlocked laptop should not be enough to take the account over. Changing the
password ends every **other** session and keeps the caller's — signing somebody
out of the browser they are typing in, as a reward for improving their password,
teaches them not to.

## Common mistakes

- **Adding a right anywhere but `rights.go`.** `TestRouteRightsExist` catches a
  route asking for one that no role can hold, which would otherwise be a route
  nobody can reach and nobody finds out about until somebody tries.
- **Gating a self-service route.** If it acts on the caller and nobody else, the
  session already identifies them; a right would only decide *which* people may
  administer *themselves*, which is not a real question.
- **Assuming the panel's `can()` is enforcement.** It hides nav items that would
  only lead to a 403. The engine is what refuses.
- **Reading a role from a stored session record.** `Resolve` re-reads it per
  request for a reason — see the demotion note above.
- **Calling `DefaultRightsOf` or `DefaultCan` when you mean what an operator may
  do.** They answer for the engine's defaults, which in a store that has re-cut
  the role is the wrong answer. The names are long on purpose; use
  `(*Superuser).Has` for a request, or `app.Roles().Of` for a role.
