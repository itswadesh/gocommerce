# The admin panel

One executable serves the API and the dashboard. Run it, open
`http://localhost:8080/`, and there is nothing else to install or configure.

## What it is

A SvelteKit single-page app, built to static files and embedded in the Go
binary with `go:embed`. It talks to the same public API as any other client:
no private endpoints, no server-side rendering, no backend of its own.

That constraint is deliberate. If the panel needed an endpoint the API did not
have, the API would be incomplete — and the next person to automate something
would find the gap the hard way.

## Design

The look is not *modelled on* the [PocketBase](https://github.com/pocketbase/pocketbase)
dashboard — it **is** PocketBase's stylesheets. `admin/src/lib/styles/` holds
`vars.css`, `base.css`, `form.css`, `layout.css`, `table.css`, `modal.css`,
`toast.css`, `list.css`, `grid.css`, `dropdown.css`, `accordion.css`,
`bulkbar.css`, `tabs.css`, `tooltip.css` and `animations.css` copied verbatim
from PocketBase's `ui/src/css`, imported in PocketBase's own order.

Do not hand-edit those files. To take an upstream change, re-copy them from a
PocketBase checkout, so the diff stays readable. Two stylesheets are ours —
`gocommerce.css` and `fonts-inter.css` — and the first covers only what
PocketBase has no counterpart to: dashboard stat cards, a section heading,
money columns, and the report chart. It is built from PocketBase's own tokens, so it introduces no
new colour, radius or duration.

**`gocommerce.css` is written by appending.** A screen adds one commented
section at the end of the file, immediately above the `@media print` block,
which stays the last block in the file permanently. That is not tidiness: the
print rules hide the shell without `!important`, so anything of equal
specificity placed after them wins on source order and puts the navigation
back on the printed page. Do not reach up into an existing rule to make a new
screen look right either — a selector two screens share is a regression
waiting for whichever of them changes first.

Two places are edited in place, and only two. The nav-accent blocks — the
`.nav-*` pairs carrying an icon, ground and text colour per navigation item,
in their light and dark forms — gain a line whenever a screen gains a
navigation entry, because they are a lookup table rather than a section. And
the `@media print` sentinel itself is edited when something new has to be kept
off a printed page; it is the one block a new section may not be appended
after.

The markup follows suit. Pages own their `.page` wrapper, the primary
navigation is horizontal in the accent header (`.app-main-nav`), a page title
is `<nav class="breadcrumbs">` rather than a heading, and secondary navigation
is a `.page-sidebar` of `<details class="nav-group">` groups — which is why
Settings has a sidebar and the list screens do not.

A consequence worth knowing: the class vocabulary is PocketBase's and nothing
else. There is no `.badge` (`.label`, with four colour variants), no
`.btn.accent` (plain `.btn` *is* the accent), no `.table-wrapper`
(`.page-table-wrapper`), and no `.panel`. When adding a screen, grep the
stylesheets before inventing a class.

Everything below is therefore a description of PocketBase's design, not of a
reimplementation of it.

**Typography.** **Inter** for text, IBM Plex Mono for SKUs, order numbers and
code — Inter has no monospace companion, and a column of order numbers has to
align. Body text is 14px on a 22px line. Headings are *sized*, not bolded:
weight comes from `<strong>` and from the few components that ask for it.

Inter replaces PocketBase's IBM Plex Sans, so two things follow. PocketBase
sets `letter-spacing: 0.004em` on the body and its own comment says why — it
normalises *Plex Sans* between Chrome and Firefox; `gocommerce.css` resets it
to `normal`, because Inter ships with its own metrics and the extra tracking
reads loose. And the faces are self-hosted in `admin/static/fonts/inter/`
rather than linked from Google, because the panel runs under a CSP with
`font-src 'self'` — and a dashboard should not phone a third party to render
its own text.

Four files, not eight: Google serves Inter as a variable font, so one file per
subset carries the whole weight axis (the 400 and 600 downloads were
byte-identical). With `unicode-range` splitting latin from latin-ext, an
English page fetches about 100KB of the 277KB total. Dropping the now-unused
Plex Sans binaries made the panel *smaller* than it was with Plex.

**Icons.** Remix Icon 4.6.0 as an icon font, so an icon inherits the colour and
size of whatever it sits in. 3,058 of them are available; `<i class="ri-truck-line">`
is the whole API.

**Colour.** One accent, four semantic colours, and a five-step surface ramp
from `#fff` to `#ccd4db`. Almost every other shade is derived with
`color-mix()` rather than hand-picked, which is why the dark theme needed only
the ramp redefined. Components reference roles (`--surfaceAlt2Color`,
`--inputFocusColor`), never raw hex — that is what lets the header be an accent
"surface" while reusing every component unchanged.

The accent is **olive** (`#89a24c`), not PocketBase's blue. That is not a
one-token swap, and the reason is worth knowing before anyone changes it again.

PocketBase's blue is dark enough to carry white text, so `vars.css` hardcodes
`#fff` for everything sitting on the accent. White on `#89a24c` measures 2.9:1
— below the 4.5:1 floor — so `gocommerce.css` inverts the accent's *foreground*
instead: `--accentTxtColor` and the header's `--surfaceTxtColor` become a dark
olive, and `--selectionColor` follows, because a focus ring derived from a
lightened green disappears into the bar.

The hint step needs naming rather than deriving. `vars.css` produces it by
fading the text toward transparent, which works on a light accent and fails on
a mid-tone one: 40% of dark text over this green lands near 2.6:1, and the idle
nav items become decoration. An explicit `--accentTxtHintColor` keeps it at
4.6:1.

`.txt-accent` has the opposite problem — the accent as *foreground* on white is
also 2.9:1 — so light mode uses a deepened moss while dark mode can use the
accent itself.

Measured in the browser: 5.6:1 active nav, 4.6:1 idle nav, 5.7:1 accent text on
white and 6.0:1 on the dark surface. If you change the accent again, re-measure
rather than assuming — the two failure directions are independent, and a
mid-tone hue fails both.

The header wordmark is `logo_header.svg` — named for its role rather than its
colour, since it is dark on the accent and `logo_white.svg` would have lied.

**Motion.** The rule the whole panel follows: hover and focus move one surface
step over 150ms; a press moves two steps over 70ms. The asymmetry is most of
why it feels crisp — a click answers immediately while a hover glides. There is
no ripple and nothing scales under the cursor: a control that moves when you
aim at it makes double-clicks miss.

Specific motions:

| Element | Behaviour |
|---|---|
| Drawer | Full-height, right-anchored, slides 30px in over 200ms |
| Confirmation | Centred, scales 0.98 to 1 — it belongs to the button you pressed |
| Toast | Rises 10px from the bottom centre; hovering pauses its timer |
| Dropdown | Falls 3px with a fade |
| Tooltip | Fades only, no movement |
| Row actions | Hidden until row hover, sliding 5px in — always visible on touch |
| Loading button | Label fades, spinner takes its place, width never changes |

**Fields.** The distinctive PocketBase input: a filled box with no border and
the label *inside* it. Focus darkens the whole field one step rather than
drawing a ring, and an error re-tints the field instead of adding red furniture
around it.

Route changes animate the `.page-content`, not the `.page`: moving between two
Settings screens re-mounts an identical sidebar, and animating the whole page
would make that stationary element flicker while the part that actually changed
sat still. The motion is PocketBase's own `slideTop` — 3px and a fade over
150ms.

PocketBase's stylesheets carry no `prefers-reduced-motion` handling, so
`gocommerce.css` adds it for the motion that travels: page transitions, row
fades, toasts and the drawer slide. The loading spinner is exempt, because a
frozen spinner reads as a hung page.

**Sortable column headers.** `th.sort-handle`, its `.asc` / `.desc` arrows and
its hover, focus and active states are PocketBase's, and had been sitting in
`table.css` unused since the panel was copied — so `SortHeader.svelte` adds
behaviour and not one line of CSS. That is the class-vocabulary rule paying for
itself: grep before inventing.

Six things about it are decisions rather than details.

The header cycles **ascending, descending, then off**. The third state is the
engine's own order — newest orders first, newest products first — which no
`sort`/`order` pair can express, and which an operator who sorted by title to
find one row needs back. A two-state toggle leaves no way to it but editing the
URL.

The `<th>` itself carries the click, the `tabindex` and the `aria-sort`, with no
button inside it. The stylesheet styles `:focus-visible` on the th, so a nested
button would draw the focus ring in the wrong place; and `aria-sort` is only
meaningful on a columnheader, so `role="button"` would cancel the attribute that
carries the meaning. Enter and Space activate it.

**PocketBase draws `.asc` as a down arrow and `.desc` as an up one** — the "A at
the top, reading downwards" convention rather than the "values rising" one. The
classes stay semantic, because relabelling them means editing a verbatim file;
`aria-sort` and the header's tooltip say which way it is in words. This is a
deliberate deviation, not drift.

The state lives in the address bar, through the same `listState` that holds the
screen's filters and page — not in a second mechanism of its own. That is what
makes `set()` reset the page to 1 on every ordering change, which windowed
paging needs: page 4 of a new ordering is not page 4 of the old one. Each list
screen also carries a two-line request-generation guard (`const mine = ++reqId`
… `if (mine !== reqId) return`), because a fast second header click leaves two
replies in flight and the table must settle on the header that is lit rather
than on whichever arrived last.

Headers that do **not** sort stay plain `<th>`, with no hover, no cursor and no
arrow, so the absence reads as a fact: Variants and Items count an embedded
array rather than a column; Location is one line of an address snapshot; and a
discount's State is a phase derived from five columns at once while Takes off is
basis points on one row and minor units on the next.

Products, Orders and Customers gave up their **Load more** buttons to get this.
An accumulator holding pages 1 to 4 cannot honestly write `page=4` in the
address bar, and appending page 4 of a new ordering onto three pages of the old
one interleaves two orderings in one table — which reads as corrupted data
rather than as a bug. They page through the shared `Pager` now, the way
Inventory, Carts and Locations already did.

The one exception to all of it is `MediaZone`'s library picker, which sorts
through a dropdown beside its other filters and keeps that choice local. It is a
grid of thumbnails with no column headers, and it lives in a drawer: a drawer
that writes to the address bar leaves a stale `?sort=` behind when it closes and
fights the host screen's own parameters.

## Reports, and a chart with no charting library

The Reports screen asks the engine two questions —
`GET /api/admin/reports/sales` and `GET /api/admin/reports/top-products` — and
draws what comes back. Every figure on it is added up by PostgreSQL over the
whole window; nothing on the screen sums anything, which is the difference
between this and the dashboard figure it replaces (a client-side sum over the
eight most recent orders, presented as revenue).

The chart is one stacked CSS bar per bucket: a flex row, a `--h` custom property
for the column and a `--seg` for each segment. There is no charting library and
there will not be one — the engine has a single production dependency, the panel
has no build-time budget for a second, and what this draws is a row of stacked
bars. An SVG viewBox was the alternative and it is worse on a phone: it scales
its own axis text down with the drawing, where these bars reflow and the labels
stay 13px.

Each bar stacks collected, awaiting payment and refunded against that bucket's
own total, so the three always fill the column exactly — the engine guarantees
the split partitions the sale set. For a cash-on-delivery store that is the
whole question answered inside the bar: how much of this month has actually
arrived.

The three segment colours are the panel's status tokens (success, info, danger)
rather than two steps of the accent, because two steps of one hue is a
sequential encoding and these are three categories; the trio was checked for
colour-vision separation in both schemes. The `<figure>` is `aria-hidden` and
the table beneath it is its accessible equivalent, carrying every number in the
same order — which is also why no fact on the screen is ever colour-only.

The window, the grain, the currency and the best-seller page live in the URL, so
a report can be sent to somebody. `to` is exclusive everywhere in this API, so
the presets send the day *after* the last day wanted and the footer restates the
resolved window in words.

## Carts, and why the screen cannot touch one

The Carts screen lists the baskets that have not become orders yet —
`GET /api/admin/carts`, with the drawer asking `GET /api/admin/carts/{id}` for
what is in one. Both are gated on `orders.read`, the right Orders already
carries, because a cart is an order that has not happened.

It is read-only, and there is no admin write route to call even if a button
wanted one. A cart’s token is the shopper’s credential — possessing it
authorises adding to, emptying and checking out the basket — so the API
withholds it exactly as an order’s access token is withheld, and a cart is
addressed here by its numeric row id instead. An operator can therefore see a
basket and cannot act on one; acting means placing the order. The drawer says
so in as many words, and the only thing it offers is a `mailto:` link, which
needs no route at all.

The state column is derived from the status column AND `expires_at`, so a cart
past its TTL that the five-minute sweeper has not reached yet reads
`abandoned` with an “awaiting the sweeper” note under it rather than reading
`live` and looking like a bug. Each line carries both price coordinates, so a
`price changed` or `out of stock` chip is what tells an operator whether the
basket is still worth chasing.

It opens on abandoned baskets with something in them, because that is the
question it exists to answer, and both filters are rendered in their active
state rather than applied silently — the engine applies no default of its own,
and a filter that quietly drops rows is worse than a noisy one. Filters and the
page live in the URL through `listState`, and the footer is the shared
`Pager`.

## Stock history

`StockHistory.svelte` renders the stock ledger
(`GET /api/admin/variants/{id}/movements` and
`GET /api/admin/locations/{id}/movements`), and it is one component because two
screens ask the same question from opposite ends. It appears in three places:
the inventory drawer's fourth mode, reached by the History button; a compact
three-row block under the form in the drawer's editing modes, because the
person about to change a count is exactly the person who needs to see what
happened last time; and a second drawer on the Locations row, opened by the
clock icon, which says what has moved there.

Nothing in that table is clickable, and `.stock-history` deliberately leaves
out `.stock-locations`' picker rules for that reason. It pages in place with a
`load-more-btn` rather than through the shared `Pager`: it lives inside a
drawer, and a drawer that writes `?page=` into the address bar leaves a stale
parameter behind when it closes.

## What a location holds

The three number columns on the Locations row — On hand, Reserved, SKUs — are
buttons, and they open a drawer listing what that place is actually holding
(`GET /api/admin/locations/{id}/stock`). So does the stack icon in the row's
meta cell, for a row whose figures are all zero, and so does a link beside the
save error, which is where "this location still holds 43 unit(s) across 7
SKU(s)" finally names the seven. That refusal was the whole reason to come here
and it led nowhere.

Three choices in it are deliberate rather than drift:

- **It filters to non-zero shelves by default**, with a visible checkbox that
  widens it. Every variant ever created has a row at the default location, so an
  unfiltered listing of the default location is the whole catalog — which is the
  Inventory screen, not this one. The filter's definition is the engine's: a
  shelf is held when `on_hand` **or** `reserved` is non-zero, the same test that
  refuses a close, so the drawer and the refusal cannot disagree.
- **The footer reads the location record, never the rows on screen.** It is
  `refuseIfHolding`'s own arithmetic from one source, so a location with more
  than one page of SKUs cannot show a footer that contradicts the sentence that
  sent the operator here.
- **`.drawer-table` exists beside `.stock-locations`** because one is a picker
  and one is a report, and only one of them may claim a click. They share the
  frame, padding, header and separators through an `:is()`; the cursor, hover,
  `.selected` background and accent bar stay on the picker alone.

The per-row **Move** button opens a small inline form in an expanded row rather
than a third stacked drawer. Two `<Drawer>`s open at once is actively broken,
not merely unspecified: each mounts its own window-level Escape handler, so
Escape closes both, and a click inside the upper one is outside the lower one's
node and dismisses it along with its unsaved form. Opening this drawer closes
the edit drawer for the same reason.

Closed locations appear in that form's destination select **disabled with a
reason** ("— closed") rather than hidden, matching how the Locations and Taxes
screens already state a fact instead of removing a row: the engine refuses stock
arriving at a closed location, and a silently shorter list explains nothing. The
inventory drawer's own move select, its destination auto-pick and its source
seeding follow the same rule — a shelf whose only units are stranded at a closed
location is not pre-selected, or the drawer's default action would be a 409 with
no in-panel explanation.

## Low stock is a store-wide question unless asked otherwise

The Inventory screen's threshold is measured against the store's total across
every location, which is what a single-location store means and what a
multi-location store often does not. The screen now says so in its info alert,
and carries a location picker — hidden entirely below two locations, so a
single-location store never learns the word — that re-asks the question of one
shelf. When one is chosen the column headers gain "here" and the Available cell
carries "N in the store" underneath: both numbers, both labelled. The dashboard
card and the product page's variant matrix stay store-wide by design, and both
now say "across the whole store" / "across every location" rather than making
the unqualified claim.

## The team screen counts sessions

The team listing carries a Sessions column, because an owner who can end
somebody's sessions has to be able to see whether there are any — "0" and "4"
mean quite different things to somebody who has lost a laptop, and that was
only discoverable from the toast *after* pressing the button. "Sign out
everywhere" stays enabled when the count is zero: a number read a few minutes
ago must never disable a security control, so only its tooltip changes.

A line under the table says what the column is not. Expired sessions are
deleted, so a dash means nobody is signed in at the moment and never that nobody
ever has been; the panel must not let the column be read as a last sign-in,
because the data cannot support that.

## Trying a discount, and what one cost

The discount drawer gained two sections below the form. "Try it" posts a basket
to `POST /api/admin/discounts/{id}/preview` and renders the answer — a
refusal comes back as `applies: false` with the engine's own sentence and is
drawn as a warning, not as an error, because the test ran correctly (D43). The
free-shipping branch is separate from the amount branch on purpose: such a rule
applies with an amount of zero, and "takes off 0.00" is the wrong sentence for a
rule that is working.

"Where it has been used" lists the orders, and names all three counts in one
place — what was given away, how many live orders carry it, and how many rows
the list holds including cancelled ones — because they legitimately disagree and
a screen showing two of them invites a bug report. The whole section is behind
`can("orders.read")`: the route needs both rights, so a re-cut role that lost the
second must not be shown a section that will 403.

**The reason field exists only on the inventory drawer.** The panel requires
one for a stock take and for a negative adjustment — those are the two
movements somebody asks about later — and accepts a blank for a positive
adjustment and a transfer, because stock arriving explains itself. That is the
panel's rule and not the API's: the engine accepts a blank reason from any
client, and a script or the MCP tool may well send one. The product form's
save-time stock take and the variant matrix's setters deliberately send **no**
reason at all rather than a canned string: a sentence in a human-labelled audit
field that no human typed is worse than a blank, because a reader cannot tell
the two apart. `kind`, `source`, the operator's email and the timestamp already
say everything true about those rows.

## Authentication

Two kinds of credential exist, because scripts and people want different
things. Both arrive as `Authorization: Bearer <x>` and both satisfy the same
middleware, so no handler has to care which one it got.

**A superuser signs in with an email and a password**, as in PocketBase. The
server issues a session token, the panel keeps it in `localStorage`, and it
expires after 14 days. This is what a person uses.

**A static admin token** from `Config.AdminTokens` is what a script uses. It
has no session and no expiry, which is exactly right for CI and curl and
exactly wrong for a browser. Several can be configured at once, so one can be
rotated without downtime.

Static tokens are checked first, because that check is a memory compare while a
session costs a query — an unauthenticated flood therefore never reaches the
database.

### Creating the first operator

A fresh database has none, and the panel asks before it renders a login form
nobody could satisfy (`GET /api/admin/auth-state`). Three ways to create one:

```powershell
# 1. From the panel. Open it on a fresh database and it offers the form.

# 2. From the CLI.
gocommerce superuser create you@example.com "a good password"
gocommerce superuser update you@example.com "a new password"
gocommerce superuser list

# 3. From the environment, for an unattended deploy.
$env:GOCOMMERCE_ADMIN_EMAIL = 'you@example.com'
$env:GOCOMMERCE_ADMIN_PASSWORD = '...'
gocommerce serve
```

The environment path is create-only: if an operator already exists, a stale
variable must not silently reset their password.

### How passwords are stored

PBKDF2-HMAC-SHA256 at 600,000 iterations with a per-user salt, from the
standard library's `crypto/pbkdf2` — chosen over bcrypt because the engine's
claim to slimness is that it has exactly one production dependency.

The stored hash is self-describing (`pbkdf2-sha256$<iterations>$<salt>$<key>`),
so the cost can be raised later without a migration and without a flag day: an
old hash keeps verifying against its own recorded parameters.

Session tokens are stored as a SHA-256 hash, never in the clear, so a leaked
database yields no usable session.

### What the login endpoint refuses to tell you

A wrong password and an unknown account return the identical error, and take
the same time — the unknown-account path deliberately performs a real
verification against an existing hash so that the *cost* matches too. Between
them, the endpoint cannot be used to discover who has an account.

Repeated failures are throttled with exponential backoff on two counters: one
per (account, address) pair, so nobody can lock a real operator out by failing
on their behalf; and one per address with a larger allowance, so a single host
spraying one password across many accounts is still slowed. The throttle is
in-process and therefore per-replica; it raises the cost of online guessing,
which is what it is for, and is not a substitute for a strong password.

Changing a password ends every session that operator holds. If they are
changing their own, the response carries a replacement token, so the security
property holds without kicking them out of the flow they are in.

`Config.AdminAuth` still replaces the whole middleware, so an identity module
can take over from either scheme.

## How it is served

Mounted at the **root**. The API lives entirely under `/api`, `/health`, `/doc`
and a module's `/x/`, so nothing competes for `/` — and the store's address is
the dashboard's address.

- `GET /{path...}` serves the embedded files. Go's `ServeMux` prefers the most
  specific pattern, so every real API route still wins over this catch-all.
- An unknown path with no file extension returns `index.html`, so refreshing on
  `/orders` works. A missing *asset* returns 404 — answering a missing `.js`
  with HTML would turn a build problem into a baffling syntax error.
- **An unmatched path under an API namespace returns the JSON 404**, not the
  panel. This is the one guard the root mount makes necessary: without it,
  `GET /api/typo` would answer with an index.html, and a client decoding JSON
  would report a syntax error instead of reading the message. It is covered by
  its own test.
- `/_` and `/_/…` — the panel's previous home, matching PocketBase — redirect
  to `/`, so an old bookmark still works.
- Hashed assets under `_app/immutable` are cached for a year; `index.html` is
  never cached, or a deploy would not reach anyone.
- A strict CSP locks the panel to its own origin.

Panel routes are marked `UI` in the route table so the OpenAPI coverage test
skips them: a spec describing a file server would be noise.

### The trade

Owning the root means this binary cannot also serve a storefront there. That is
the right call for a headless engine — the storefront is a separate application
with its own deployment — but it is a real constraint. If you need both on one
origin, put a reverse proxy in front, or rebuild the panel with a different
`paths.base` in `admin/svelte.config.js` and change `AdminPanelPath` to match.
The base is fixed at build time, which is why it is not a runtime setting.

## Building

```powershell
.\scripts\build.ps1              # build the panel, embed it, compile
.\scripts\build.ps1 -SkipPanel   # reuse the committed panel build
.\scripts\build.ps1 -NoPanel     # API only
.\scripts\build.ps1 -All         # every platform, into dist/
```

`admin/build` is **committed**, so `go build` and `go install` work on a machine
with no Node.js — the same trade PocketBase makes, for the same reason. Rebuild
it whenever you change anything under `admin/src`.

Cross-compilation works everywhere because there is no cgo: `CGO_ENABLED=0`
with `GOOS`/`GOARCH` produces a Linux binary from Windows and vice versa.

Sizes: about 13 MB stripped with the panel, 12 MB without. The panel itself is
roughly 830 KB, most of it the icon font and the four Plex faces.

## Developing the panel

```powershell
.\scripts\dev.ps1               # the API on :8080
cd admin; npm run dev           # the panel on :5173, proxying to :8080
```

Vite's dev server gives hot reload and proxies `/api`, `/health` and `/doc` to
the running store, so the panel talks to real data. In production both are the
same binary and no proxy exists.

## Where the access token is shown

Once, on the New order drawer's success state, and nowhere else in the panel or
the API. `POST /api/admin/orders` returns `data.order.access_token`; every other
admin read omits it, and `ext/identity` and `ext/mcp` blank it on their own
reads. So the drawer holds open after placing an order rather than closing over
the response — a toast would be dismissible, unselectable and gone, and this
string has exactly one appearance. An operator who closes it cannot get the
token back; nothing in the product can reissue one.

The same state reads out the `PaymentIntent`, for an order placed against a
gateway. It is a readout and not a link: `PaymentIntent` carries no redirect URL
(`kind`, `provider`, `reference`, `client_data`), and there is no storefront base
URL anywhere in the engine to build one from. The alert says plainly that the
payment cannot be completed from the panel, rather than implying it can.

## What it does not do yet

Honest gaps, rather than a roadmap:

- **Reports do not net off refunds.** The engine records how much came back
  and the screen shows it — `Collected` is what the store still holds — but
  `Net sales` is what was sold, refunds included. Reading it as money kept is
  wrong by exactly the refunds in the window.
- **Report buckets are keyed on when an order was placed.** There is no
  `confirmed_at`, so marking an old order paid, editing it or cancelling it
  changes a past bucket. A report is a current reading of history, not a closed
  period.
- **An operator cannot collect a gateway payment from the panel.** The New order
  success state shows what the gateway said to do next; acting on it needs a
  `return_url` on the create request and a way to re-start a payment, which is
  an API change rather than a screen.
- **The New order drawer cannot preview a discount.** A code is typed and judged
  when the order is placed, so an ineligible one refuses the whole submit. The
  line above the Place order button is therefore labelled *Items subtotal*: it
  sums the lines, while the checkout adds shipping, any discount and then tax.
- **The discount drawer offers percentage and fixed, not free shipping**, which
  the engine has supported since M15. It is an adjacent gap rather than a
  hazard: while it is out, the panel cannot produce the one combination the
  engine refuses outright, a free-shipping rule carrying a scope.
- **A scoped discount whose every target has been deleted still draws `live`.**
  The listing carries target ids and not their `missing` flags — resolving two
  hundred titles into a page of fifty rows is the trade that buys the drawer its
  race-free open — so a rule pointing at nothing shows as `no targets`, but one
  pointing only at deleted things does not. `gocommerce doctor` counts those.
- **The discount target picker reaches the first 200 collections.** Products are
  searched at the store and categories arrive flat, so only collections are
  capped; the field help says so rather than pretending otherwise.
- **The categories screen has no sortable header.** The API accepts `sort` on a
  category *search*, and the screen browses the tree instead — there is no
  search box on it to reach the branch that can be ordered. The tree's own order
  is the `position` an operator curated, which is why sorting it is refused
  rather than ignored.
- **`first_order_at` on customers is an allow-listed key with no column to
  click.** It works from the API and from a hand-typed URL; the screen shows the
  four numbers an operator asked for, and a fifth date column would cost more
  width than it earns.
- **Sortable headers are out of reach on a phone.** `.responsive-table` hides
  the header row below 900px and labels each cell instead, so a sort has to
  arrive in the URL. Ordering a list is a desk task, and the alternative is a
  sort control that exists only at one width.

- **An option axis cannot be removed or renamed** once it exists, and a
  variant's option combination cannot be changed — the API has no route for
  either, so the drawer shows them read-only and tells you the remedy (add the
  combination you want, delete the one you don't). SKU, price and active are
  editable.
- **Variants in the drawer are unpaginated.** They arrive embedded in the
  product, which has no limit; the paginated route
  (`GET /api/variants?product_id=`) returns variants without the product, so
  using it would mean reconciling two sources. A product with hundreds of
  variants renders all of them.
- **A new variant starts at zero on hand** and is unsellable until someone
  visits Inventory. The form says so. Stock deliberately stays there, so every
  movement is a transactional adjustment rather than an overwrite — and every
  one of them is recorded in the variant's stock history, with who made it.
- **Not exposed in the variant form** — two surfaces, and they differ. The
  single-variant Pricing and Inventory card exposes everything but `position`,
  including barcode, `track_inventory` and the compare-at price, which can now
  be *cleared* as well as set (send `null`; the panel sends it when the box is
  emptied). The matrix drawer, for a product with options, still omits barcode,
  the compare-at price and `position`, and shows `track_inventory` without
  letting anyone change it. All of them are patchable through the API.
- **Duplicate option values across axes** (a `Size: Small` and a
  `Cup: Small` on one product) are caught in the panel, not the engine:
  `insertOptions` compares values only within a single call, and the value
  lookup keys on the lowercased string, so the engine would resolve the
  collision silently. That is an engine gap the panel is papering over.
- **No collection browser.** PocketBase's dashboard is generic over
  user-defined collections; this one is specific to a commerce domain that
  already knows what a product and an order are.
- **No screen curates a collection yet.** The engine has the read and the
  ordered write — `GET` and `PUT /api/admin/collections/{id}/products` — and
  there is no `admin/src/routes/collections` directory to drive them from, so
  today they are reachable by curl and MCP only. The product list's
  `?collection_id=` filter is in the same position: the endpoint takes it, the
  control belongs to the product-list filters work.
- **No log viewer or SQL console.**
- **No bulk actions** on the list screens.
