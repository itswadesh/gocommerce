# One sign-in across stores, and custom roles

Status: design, awaiting review. Built and shipped in this order: the URL
move to `/dash/`; part 2 (custom roles and the team flow), because it stands
on a single store and is smaller; part 1 (accounts and the store switcher).

The reference is the KitCommerce admin (`kitcommerce-admin`): one sign-in, a
"Select Store" dialog, a switch in the account menu, roles a store names
itself, and a team added by email. Where this design departs from it, the
departure is named and the reason given.

## What was asked, and what was assumed

Asked:

- Sign in once at **admin.kitcommerce.store** and change store from a
  dropdown, as Shopify's admin does.
- The chosen store is **remembered, not in the path**.
- **The panel's URLs are kitcommerce-admin's**: `admin.kitcommerce.store/dash/orders`,
  as `admin.varnijewels.com/dash/orders` — every store screen under `/dash/`,
  keeping GoCommerce's screen names (see *URLs* below).
- **Custom roles over today's rights**: a store creates, renames and deletes
  its own roles; each is a set of the rights the engine already has.
- Team and permissions **the kitcommerce-admin way**.

Assumed, from "the kitcommerce-admin way" — correct any of these:

- The picker offers **Create New Store**, so a signed-in merchant can open a
  store without the platform console.
- **Select Store appears after every sign-in**, as in kitcommerce-admin, with
  the last store preselected; it is not skipped for a person with one store.
- Each store's own domain stops serving the panel and sends it to the admin
  host. A store's static admin token still works on its own domain, for
  scripts.

Kept from GoCommerce, deliberately not copied:

- **Every admin route keeps its server-side right check.** kitcommerce-admin
  hides sidebar items and checks nothing else in the client; here the engine
  refuses, and the panel hides what the engine would refuse.
- Rights stay **read/write pairs** (plus `orders.fulfill`, `orders.refund`
  and the like), not kitcommerce-admin's `list/view/save/del` per module.
- **owner** stays a fixed role that holds every right and cannot be edited,
  and the last owner of a store still cannot be removed or demoted.

## URLs (built first, before either part)

Every store screen moves under `/dash/`, as kitcommerce-admin's do, and keeps
GoCommerce's own name there; Settings screens stay under `/dash/settings/`.
Three screens take kitcommerce-admin's name instead, as asked: shipping,
carts and locations. The move is its own change, shipped before part 2, so
the two parts land on the final paths.

| Today | Becomes |
|---|---|
| `/` (Home) | `/dash` |
| `/shipping` | `/dash/shipping-settings` |
| `/shipping/providers` | `/dash/shipping-settings/providers` |
| `/carts` | `/dash/checkouts` |
| `/locations` | `/dash/warehouses` |
| `/settings/superusers` | `/dash/settings/teams` |
| `/settings/roles`, `/settings/roles/[role]` | `/dash/settings/roles`, `/dash/settings/roles/[role]` |
| every other screen — `/orders`, `/discounts`, `/cms`, `/settings/api-keys`, `/b2b/companies`, `/x/[slug]` and the rest | the same path under `/dash/`: `/dash/orders`, `/dash/discounts`, `/dash/cms`, `/dash/settings/api-keys`, `/dash/b2b/companies`, `/dash/x/[slug]` |
| sign-in (the layout's `<Login>`) | `/admin/auth/login` |
| `/reset-password/[[token]]` | `/admin/auth/reset-password` |
| `/accept-invite/[token]` | `/admin/auth/accept-invite/[token]` (kitcommerce-admin has no equivalent) |
| — (part 1) | `/select-store`, `/stores/create` |

`/platform` and `/portal` do not move: they are other people's screens.

- Every old path redirects to its new one, query string kept, so bookmarks
  and links already sent keep working.
- Links the server writes — the invitation `accept_url`, password-reset
  emails, notification emails that link into the panel, a module's
  `Screen` paths (D57), `nav.js` — move with it. Module screens keep their
  module-chosen slug under `/dash/x/[slug]`.
- Root `/` redirects to `/dash` when signed in and to `/admin/auth/login`
  when not.

## Part 2 — custom roles, and the team flow

### Roles (D81, replacing D24's refusal of custom roles)

D24 declined custom roles because a fixed set kept the matrix legible and the
role names stable for code that switched on them. D64 (role keys are
identifiers; titles are the store's) and D65 (modules declare rights) removed
both reasons: no code needs to name a role other than `owner` and the vendor
scoping, and the title a person reads was already the store's.

- A new core migration (`custom_roles`, the next free number):
  - `roles` table per store schema: `key` (identifier, `^[a-z][a-z0-9_]{0,39}$`,
    never changes), `vendor_scoped boolean`, `created_at`, `created_by`.
    Titles and descriptions stay in `role_profiles` (D64).
  - Seeded with `owner`, `manager`, `staff`, `vendor` (`vendor_scoped` true).
  - `superusers.role` and `role_rights.role` gain foreign keys to `roles.key`
    and lose their CHECK lists; `superuser_invitations.role` likewise.
- `owner` is the only built-in that cannot be edited or deleted. `manager`,
  `staff` and `vendor` become starting roles: editable, deletable.
- Deleting a role is refused while any operator or open invitation holds it
  (409 `role_in_use`, naming the count).
- Vendor row-scoping (M48) applies to any role with `vendor_scoped`, not to
  the name `vendor`; such a role still requires `superusers.vendor_id`.
- Defaults: a role created with no rights gets `RequiredRights`
  (`catalog.read`). "Reset to defaults" exists only for the three seeded
  roles, which have defaults; a store's own role has none.
- Rights resolve on every request, as now. The existing guards hold: you
  cannot remove `roles.write` from your own role, and a role holds only rights
  the build has.

Routes (all `roles.write`, added to `core/openapi.json`):

- `POST /api/admin/roles` `{key, title, description, rights, vendor_scoped}`
- `DELETE /api/admin/roles/{role}` — today this resets to defaults; it
  becomes delete, and reset moves to `POST /api/admin/roles/{role}/reset`.
  A breaking change to a documented route, so the release notes say so.
- `GET`, `PUT /{role}`, `PATCH /{role}` unchanged.

Panel (`settings/roles`):

- List gains **Add role** and a **Holders** column; delete is offered only at
  zero holders.
- New-role drawer: Name, Description, Vendor-scoped toggle. Saving opens the
  role's page, where the existing rights matrix (a row per area, a toggle per
  verb) grants rights — the same two steps kitcommerce-admin takes.
- The Team screen's role picker reads the store's roles from the API instead
  of the hard-coded owner/manager/staff list.

### Team flow

Matches kitcommerce-admin's `/dash/teams` flow, on the existing Team screen:

- **Add team member**: Email and Role.
- Single store: if the email is already an operator, refuse as now ("already
  on the team"). Otherwise the dialog **User Account Not Found** offers:
  - **Create Account** — name and password, creates the operator now;
  - **Send Invite** — today's invitation, and the email is sent when the
    store can send email (the link is still shown once to copy, as now).
- On a platform (after part 1): an email with a platform account joins the
  store at once ("Email added to team"); without one, the same dialog, where
  Create Account makes the platform account and Send Invite's link opens the
  admin host.

## Part 1 — accounts and the store switcher (D80)

D70 keeps two kinds of administrator apart — platform operators and a store's
operators — and that stays. What is new is a third thing that was never
there: one **person** who is an operator of several stores. Today the same
email on two stores is two unrelated accounts.

### Where things live

- `gocommerce_platform.accounts`: `id`, `email` (unique, lower-cased),
  `password_hash` (PBKDF2, as `superusers`), `name`, timestamps.
- `gocommerce_platform.account_sessions`: hashed tokens, 14-day sliding
  expiry, as `superuser_sessions`.
- Each store's `superusers` row stays the membership: role, `vendor_id`,
  audit identity. It gains `account_id bigint` (nullable; no cross-schema
  foreign key, the platform enforces it). A store's password columns go
  unused for linked rows.
- Rights checks, audit rows and owner rules inside a store are unchanged:
  the platform hands the store a `*Superuser`, exactly as a session does now.

### The admin host

New flag `-admin-host` (`GOCOMMERCE_ADMIN_HOST`), e.g. `admin.kitcommerce.store`.

- `GET /`, the panel's files and SPA routes: served from the panel build.
- `/api/account/*` (new, in `platform/openapi.json`):
  - `POST /auth-with-password`, `POST /auth-refresh`, `POST /auth-logout`
  - `GET /me`, `PATCH /me` (name, email, password)
  - `GET /stores` — the stores this account belongs to: slug, name, primary
    host, the account's role title in each, status
  - `POST /stores` — **Create New Store**: name, slug, currency; the account
    becomes its owner. Uses `Platform.Provision`.
  - `GET`/`POST /invitations/accept/{token}`
- `/api/admin/*` with `X-Store: <slug>`: the platform resolves the account
  session, finds the store, finds the `superusers` row with that
  `account_id`, and serves the request through the store's handler as that
  operator. No membership → 403 `not_a_member`; unknown store → 404;
  suspended → 503 as today. A request without `X-Store` → 400.
- Core gains one exported entry point for this, e.g.
  `(*App).ServeAs(w, r, *Superuser)`, rather than exposing the context key.
  Static tokens and API keys are not accepted on the admin host; they belong
  to the store's own domain.

### Panel

- A fourth client beside store, platform and trade: `$lib/account.svelte.js`,
  session in `localStorage` (as a store session), the selected slug in
  `localStorage` under `gocommerce_store`. `api.js` adds `X-Store` when it is
  set; a single store (`serve`) never sets it and behaves as today.
- After sign-in: **Select Store** at `/select-store`, a dialog that cannot be dismissed — title "Select Store", "Select a store to
  continue to your dashboard.", a "Search stores..." box, one row per store
  (initial tile, name, address, role), the last store preselected, then
  **Create New Store** and **Logout**. Zero stores: "You are not part of any
  store. Ask an owner to add you." plus Create New Store.
- Sidebar: the brand becomes a dropdown — current store name, then the
  account's stores with a search box when there are more than five, then
  **Create New Store**. Choosing one sets the slug, clears the module and
  screen caches (as `onAuthenticated` does now) and reloads the panel.
- Account menu adds **Switch store** (opens Select Store).
- Motion: dialog and dropdown open/close at 150–200 ms (opacity and
  transform), hover/press/focus feedback on every row, a spinner on the row
  being entered, and a `prefers-reduced-motion` path that drops movement and
  keeps the feedback.

### Existing operators

Migration `0002_accounts` in the platform schema, plus a one-time link at boot:
for each store, each `superusers` row with no `account_id` is linked to the
account with its email, creating the account from that row's password hash
when none exists. When one email has rows in several stores, the account takes
the most recently updated password, so the password that person set last is
the one that works. Logged as one line per linked row.

### Store domains

On a store's own domain, the panel's routes redirect to the admin host with
`?store=<slug>`, which preselects that store. `/api/admin/*` there still
accepts the store's static token and its API keys, for scripts, and refuses
session sign-in with 409 `use_admin_host`.

### The demo

- The demo store moves to `demo.shops.kitcommerce.store`; `go.misiki.tech`
  stays attached. admin.kitcommerce.store becomes the admin host.
- The public demo account is a platform account and a member of the demo
  store only; demo mode's frozen routes extend to `/api/account/me` and
  `POST /api/account/stores` for that account.
- The landing page's demo link keeps pointing at admin.kitcommerce.store.

## Testing

- Go, against the real database (rule 13):
  - roles: create, rename, delete, delete-in-use refused, vendor scoping by
    flag, own-`roles.write` guard, owner immutable;
  - accounts: sign-in, refresh, logout, sessions expire;
  - membership: an account never reaches a store it is not a member of, on
    any route, with any `X-Store` spelling (slug, host, mixed case);
  - the link migration, including one email in two stores;
  - Create New Store: the account becomes owner; a failed provision leaves
    nothing behind.
- Playwright (`verify/check.mjs`, run more than once): sign-in, Select Store,
  switch from the dropdown, Create New Store, Team add-by-email with both
  dialog paths, Roles add/delete; desktop, dark and mobile; no console
  errors, no horizontal overflow.
- `scripts/smoke.ps1` unchanged and passing on a single store.
- URLs: every row of the table above opens its screen at the new path, and
  its old path redirects there with the query string kept; the invitation
  and reset links the server writes open the new paths.

## Out of scope

- Merchant self-signup (an account created without an invitation or an
  owner). Create New Store needs a signed-in account.
- kitcommerce-admin's `list/view/save/del` rights split.
- Transferring ownership as its own action (make another owner, then change
  your own role — as today).
