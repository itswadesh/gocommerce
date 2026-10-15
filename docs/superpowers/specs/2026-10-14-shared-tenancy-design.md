# Shared tenancy — one engine, every store, kitcommerce-api's model

Status: design approved in conversation, awaiting written review.
Decision: **D83**. Supersedes D70 (schema per store) and the D81 draft in
[2026-10-09-store-switcher-and-custom-roles-design.md](2026-10-09-store-switcher-and-custom-roles-design.md)
(its Part 2, custom roles, shipped as D80 and stands).

## Why

GoCommerce runs in two shapes today: `serve` (one store, one database) and
`platform` (many stores, one PostgreSQL schema each). Two shapes means two
deploys, two upgrade paths, and features that land in one and lag in the
other. The owner wants one shape — multi-tenant only — built the way
kitcommerce-api (`C:\projects\lk`) is built, and running on
admin.kitcommerce.store.

**Success** is: one `gocommerce serve` serves every store from one set of
tables; a person signs in once and moves between their stores from the
sidebar without losing their place; no store can read or write another's
rows even when the code forgets a filter; an existing single-store install
upgrades in place with no id changes.

## What is copied from kitcommerce-api, and what is not

Copied:

- One shared schema; `store_id` on every store-owned table, foreign key to
  `stores`, cascade on delete.
- Store ids in the form `store_<ulid>`.
- Store-scoped unique keys (`(store_id, slug)` and so on).
- The request carries its store: header `x-litekart-store`, else cookie
  `litekart_store_id`, else 400 — with the same exempt routes (sign-in, the
  store list, store creation, health, docs).
- Global people plus per-store membership and per-store roles.
- `GET /api/stores/my`, `POST /api/stores/create`, a `/select-store` page,
  and a switch-store endpoint.
- Background jobs that loop over stores.

Not copied — these are defects in kitcommerce-api, and GoCommerce fixes them:

| kitcommerce-api | GoCommerce |
|---|---|
| A master password signs in as anyone | No master password |
| `get-role` builds SQL with `sql.raw` from a query parameter | Parameters only |
| Delete-store checks the header's store but deletes `?storeId` | One store id, checked and deleted |
| Domain create spreads client input, so `isVerified` can be set | Server-owned fields are never read from input |
| A hard-coded fallback store id | No fallback: no store is a 400 |
| `check-dns` is never awaited | Awaited; its result is stored |
| The store lives only in a cookie shared by every tab | Each tab holds its own store (see Part 3) |

`X-Store: <slug|id>` stays accepted as an alias so existing GoCommerce
clients keep working.

## Sub-projects

| Part | Contents | Ships |
|---|---|---|
| 1. Foundation | `stores`, `store_id` on the 54 core tables, store-bound database handle, request→store resolution, Row-Level Security, store #1 upgrade, removal of `platform` mode | With Part 2, one merge |
| 2. Modules | The 37 `ext/` tables, per-store settings, per-store jobs | With Part 1 |
| 3. Accounts and stores | Global sign-in, `store_members`, `/api/stores/*`, `/select-store`, the sidebar switcher | Normally |
| 4. The rest | Domains with verification, store delete, media and search per store, custom reports under RLS, docs and kitcommerce.store | Normally |

Parts 1 and 2 are built on a branch and merge to `main` together: a push to
`main` deploys admin.kitcommerce.store, and a half-scoped engine must never
go live. Each part gets its own implementation plan.

---

## Part 1 — Foundation

### The engine

- `gocommerce serve` serves every store. `gocommerce platform`, the
  `platform/` package, `docker-compose.platform.yml` and schema-per-store
  provisioning are removed. One Compose file remains.
- First run still takes one command: it creates store #1 and its owner from
  the same environment variables as today (`GOCOMMERCE_ADMIN_EMAIL` and the
  rest).
- Every non-`stores` row id stays a `bigint` from its table's sequence;
  sequences are shared across stores.

### Global tables (no `store_id`, no RLS)

| Table | Purpose |
|---|---|
| `stores` | `id text` (`store_<ulid>`), `slug` unique, `name`, `status` (`active`, `suspended`), `created_at` |
| `store_domains` | host → store (`host` unique, `store_id`); verification arrives in Part 4 |
| `store_members` | `(store_id, superuser_id)` primary key, `role` → that store's `roles (store_id, key)`, `vendor_id` (the seller a vendor login acts for). `superusers.role` and `superusers.vendor_id` move here. |
| `store_counters` | `(store_id, name)` primary key, `value bigint` — see order numbers |
| `api_key_index` | `prefix` primary key → `store_id`, written in the same transaction as the key |
| `superusers`, `superuser_sessions`, `superuser_password_resets` | People, not one store's staff |
| The migrations ledger | — |

`store_members` and `store_counters` reference `stores` with cascade.

### Store tables

Every other core table gets
`store_id text NOT NULL REFERENCES stores(id) ON DELETE CASCADE`, including
outbox events, idempotency keys, audit, API keys, invitations, roles,
notifications and custom reports. The column's default is
`NULLIF(current_setting('gocommerce.store_id', true), '')`: an insert takes the
bound store, and with no store bound it fails on `NOT NULL`.

**Uniqueness is per store.** Each of the 38 `UNIQUE` constraints and unique
indexes in `core/schema.go` gains a leading `store_id`:
`products.slug`, `variants.sku`, `collections.slug`, `categories.slug`,
`vendors.slug`, `locations.code`, `channels.code`, `customer_groups.code`,
the `lower(code)` discount index, `orders.number`, and the composite keys.
The partial "one default" indexes (`locations_one_default`,
`channels_one_default_idx`) become one default per store. Three stay global
because they are random values looked up before any store is known or are
globally unique by construction: `carts.token`, `api_keys.prefix`,
`outbox_events.event_id`.

**One-row tables become one row per store.** `store_profile` drops
`id = 1` and takes `store_id` as its primary key; every other single-row
settings table does the same.

**Foreign keys stay inside a store.** Each store table gets a unique
`(store_id, id)`; each foreign key between store tables becomes composite,
`(store_id, x_id) → (store_id, id)`. The database refuses a cross-store link
even if the code builds one.

**Order numbers count per store.** Today the number is the prefix plus the
order's id (`core/checkout.go:412`), which on a shared sequence would give a
new store's first order a number like `GC-004817`. The checkout transaction
instead bumps `store_counters (store_id, 'order')` and formats
`prefix + %06d`. The prefix is `stores.order_prefix`; while it is null the
store uses `Config.OrderPrefix` (default `GC-`), so an existing install's
configured prefix keeps working. Existing orders keep their numbers; store #1's counter starts after
its highest.

### Row-Level Security

Every store table:

```sql
ALTER TABLE t ENABLE ROW LEVEL SECURITY;
ALTER TABLE t FORCE ROW LEVEL SECURITY;
CREATE POLICY store_scope ON t
  USING      (store_id = current_setting('gocommerce.store_id', true))
  WITH CHECK (store_id = current_setting('gocommerce.store_id', true));
```

With no store set the setting is empty: reads return nothing and writes are
refused. The engine's database login must not be a superuser and must not
have `BYPASSRLS`; `doctor` fails if it is, and the Compose file creates an
ordinary login for the engine. The policy defends against the engine's own
bugs; it does not defend against SQL the engine does not control — custom
reports get a different policy (Part 4).

### From a request to the store's rows

**Resolution**, first match wins:

1. An API key: its prefix → `api_key_index` → store. If a header names a
   different store: 403.
2. Header `x-litekart-store` (id) or `X-Store` (slug or id).
3. Query parameter `store` (id or slug) — for callers that cannot set a
   header, chiefly payment providers' webhooks and invitation links.
4. Cookie `litekart_store_id`.
5. Host: `<slug>.<base domain>` or a row in `store_domains`.
6. A signed-in person who belongs to exactly one store: that store.
7. An install with exactly one store: that store. An upgraded single-store
   install therefore needs no client change at all.

Every step that names a store still passes the membership check: naming a
store never grants access to it.

Exempt routes skip resolution: sign-in and sign-out, password reset,
`/api/stores/my`, `/api/stores/create`, health, docs and the panel's static
files.

**Binding.** Middleware puts the store id in the request context
(`gocommerce.WithStore`). The engine's pool is opened through a thin
`database/sql` driver wrapper: before every statement, transaction begin and
prepared-statement run, it compares the context's store with the one last
set on that connection and, when they differ, runs
`SELECT set_config('gocommerce.store_id', $1, false)` first. A context with
no store sets the empty string. So a connection can never carry one
store's setting into another's statement, whichever pooled connection the
statement lands on, and a rolled-back transaction forgets what it set. The
~440 existing call sites keep `*sql.DB`, `InTx` and `App.DB()` unchanged:
the store travels in the `ctx` they already pass. A test fails on any
database call that does not take a context (`Query`, `Exec`, `QueryRow`,
`Begin`, `Prepare` without `Context`).

**Embedding and tests.** `Config.DefaultStore` names a store that a context
with no store is bound to. It exists for an application that embeds the
engine for one store, and for the test suite. `gocommerce serve` never sets
it, so in the product unbound work sees nothing.

**Row security is the filter.** (Revised 2026-10-15, owner's decision.) An
earlier draft also put `WHERE store_id = $n` in every statement, as
kitcommerce-api does. That meant hand-editing about 1,000 statements for no
isolation the policy does not already give: a statement sees only the bound
store's rows, an insert takes the bound store from the column default, and
the policy's predicate uses the `store_id` indexes. So statements are not
rewritten; only `ON CONFLICT` targets gain `store_id` (they must name the
rewritten keys). What keeps this safe is the rest of this section: the
engine's login cannot bypass row security (`doctor` fails if it can), every
store table is forced behind the policy (a test reads the catalog), and the
two-store isolation suite exercises the API.

**Background work.** `app.ForEachStore(ctx, fn)` binds each active store in
turn on a dedicated connection. The scheduler, cart recovery, webhook
retries and the outbox dispatcher run through it. A dispatcher pass walks
every active store and drains each; an idle store costs one indexed query
per pass, and the in-process nudge after a write still makes delivery
immediate. This is sized for hundreds of stores; past that, a global work
queue (or `LISTEN/NOTIFY` per store) is the next step and is out of scope
here.

**Errors.**

| Case | Status |
|---|---|
| No store on a store route | 400 `store required` |
| Unknown store id, slug or host | 404 |
| Signed in, not a member of the store | 403 |
| API key's store ≠ the header's store | 403 |
| Suspended store | 503, as today |

### Upgrade

New migrations only; nothing shipped is edited.

1. Create `stores` and insert store #1 (slug from the store profile name,
   else `store`).
2. Add `store_id` as nullable to every store table and fill it with store
   #1.
3. Set `NOT NULL`, swap the unique constraints, add `(store_id, id)` keys and
   the composite foreign keys, enable RLS and the policies.
4. Fill `store_members` from today's superusers and their role; fill
   `store_counters` and `api_key_index`.

A single-store install of any 1.x upgrades in place; no id changes. Every
table is rewritten once, at upgrade time — the release notes say so.

A 1.1/1.2 **platform-mode** install (stores in separate schemas) is not
upgraded automatically; the release notes say so. admin.kitcommerce.store's
demo store is re-seeded into the new tables at the merge rather than
migrated — it is demo data, and copying it would renumber every id.

Custom SQL reports are switched off (the endpoint answers 503 with a reason)
from Part 1 until Part 4.

---

## Part 2 — Modules

The same treatment for the 37 tables under `ext/`: `store_id`, per-store
unique keys, composite foreign keys, RLS. Modules call the exported `gocommerce.ScopeTables` from a
migration of their own. Module settings and
"plugin on/off" become per store. Module jobs run through
`ForEachStore`. Modules still never write core tables (AGENTS.md rule 1).

---

## Part 3 — Accounts and stores

### API

| Route | Does |
|---|---|
| `POST /api/admin/auth-with-password` | Signs a person in (unchanged route); the session carries no store |
| `GET /api/stores/my` | The person's stores: id, slug, name, role, status |
| `POST /api/stores/create` | Creates a store, makes the caller its owner, seeds roles and settings; optional sample data |
| `POST /api/stores/switch` `{store_id}` | Checks membership; returns the store and the caller's role and rights there; sets the `litekart_store_id` cookie |

### `/select-store`

Shown after sign-in when the person has more than one store and no
remembered one; lists stores with search and a "Create a store" action. One
store: straight to it. None: straight to create.

### The sidebar switcher

It lives in the existing account block at the foot of the sidebar
(`admin/src/routes/+layout.svelte`, the `account` snippet).

- The trigger shows the current store's name above the email.
- The menu lists the person's stores first — name, role there, a check on
  the current one — then the existing items (Your account, Manage the team,
  API preview, Logout). Over 7 stores, a search box heads the list.
- Below the list: "Create a store" and "All stores…" (`/select-store`).
- Arrow keys move, Enter switches, Escape closes.

**Switching keeps the page.** The rule is: keep the section, drop anything
that names a row of the old store.

| On | Lands on, in the new store |
|---|---|
| `/dash/orders` | `/dash/orders` |
| `/dash/orders?status=paid` | `/dash/orders?status=paid` |
| `/dash/orders?page=4` | `/dash/orders` |
| `/dash/orders/1234` | `/dash/orders`, note: "Order 1234 belongs to Store A" |
| `/dash/products/88/edit` | `/dash/products` |
| `/dash/settings/taxes` | `/dash/settings/taxes` |
| A section the new role cannot open | `/dash`, note: "You don't have access to Orders in Store B" |
| A plugin page the new store has off | `/dash` |

The mapping is computed from the SvelteKit route id: the path is cut before
the first dynamic segment (`[id]`, `[slug]` and the like), filter query
parameters are kept, and `page`, `cursor`, `after` and `before` are
dropped. It covers every current page and any added later without a
hand-kept list. The section's right comes from the same table the sidebar
uses to hide links.

**Sequence.** Call switch → on success clear every cache keyed to the old
store → navigate to the mapped URL. A 200 ms cross-fade (view transition)
covers the page change, and the sidebar's store name changes in the same
frame. On failure the menu stays open with the error inline and nothing
else changes. Under `prefers-reduced-motion` the change is instant; the name
update and the inline error stay. Every menu row has hover, press and focus
states of 150–200 ms.

**Each tab holds its own store.** The panel keeps the current store in
`sessionStorage` and sends `x-litekart-store` on every request. The
`litekart_store_id` cookie only says which store a new tab opens in. A tab
left on Store A keeps reading and writing Store A after another tab
switches to Store B.

---

## Part 4 — The rest

- **Domains:** add, verify by DNS TXT (awaited, result stored), set primary,
  remove. `is_verified` and `is_active` are set only by the server.
- **Delete a store:** owner only, typed confirmation, one id checked and
  deleted; cascade removes its rows.
- **Media:** object keys under `stores/<store_id>/`. **Search:** per-store
  filter on every query.
- **Custom reports:** one reporting login per store, with a policy keyed on
  `current_user` → `store_id` from a table that login cannot write, so SQL
  that calls `set_config` gains nothing. Read-only and time-limited as
  today (D78/D79).
- **Docs and site:** README, docs, skills, the kitcommerce.store
  multi-store page and an /updates entry.

---

## Testing

**Existing suite.** The test helpers (`core/helpers_test.go`, `gctest`)
create a store and bind to it; a test that never mentions stores runs inside
store #1 exactly as today. The 54k lines of tests become the proof that one
store behaves as before.

**Isolation suite**, two stores A and B in one database:

- Reads: for every store table, B cannot list A's rows or fetch one by id,
  through the public API, the admin API, or a direct service call — 404 or
  an empty list.
- Writes: B cannot update, delete or reference A's rows (a B cart line on an
  A variant, a B order with an A discount code, one case per composite
  foreign key).
- Uniqueness: the same slug, SKU, discount code and `GC-000001` exist in A
  and B.
- Requests: A's header with B's API key → 403; no store → 400; unknown store
  → 404; cookie and host fallbacks; resolution order.
- Background work: an outbox event, a scheduled job and a cart reminder in A
  run bound to A and never touch B.
- Connection reuse: on a pool of one connection, a statement for A, then one
  for B, then one with no store, each sees only its own rows (none for the
  last); a rolled-back transaction that bound A leaves the connection
  unbound.

**RLS on its own.** A raw connection as the engine's login, with no store
set, runs `SELECT` against every store table (listed from
`information_schema`, not by hand) and gets 0 rows; an insert with a
mismatched `store_id` fails. A table added later without a policy fails the
build. A test checks the engine login is not a superuser and lacks
`BYPASSRLS`; `doctor` runs the same check.

**Upgrade.** A database at the last 1.2 single-store schema with fixture
data runs the new migrations; every row belongs to store #1, no id changed,
order numbers are unchanged and the next order gets the next number, and the
admin, API keys and carts still work.

**Switcher.** Unit tests for the URL mapping (each row of the table above);
a Playwright run that switches from a list, a filtered list, a detail page
and a page the new role cannot open, in two tabs, with and without reduced
motion.

**End to end.** `scripts/smoke.ps1` walks a whole sale on two stores in
turn; the Playwright check walks every screen of each.

## Out of scope

- Billing, plans or quotas per store.
- A global work queue for thousands of stores.
- Automatic migration of 1.1/1.2 platform-mode installs.
- A shared customer account across stores (customers stay per store).
