---
name: development
description: Use when setting up, building, running or testing this repository day to day — the dev scripts, the PostgreSQL test setup, `gctest`, the admin panel, and the checks to run before finishing.
---

# Development

## Prerequisites

**Go** (`go.mod` says `go 1.27.0`) and **PostgreSQL**. Nothing else — there is
no cgo anywhere, so `go build` needs no C toolchain, and `-race` is unavailable
on a host without one. **Node.js is optional**: `admin/build` is committed, so
`go build` and `go install` work without it; you need it only to change the
panel.

Where Go and PostgreSQL actually live varies by machine, and the facts of the
one you are on are in [`CLAUDE.md`](../CLAUDE.md) rather than repeated here —
a hard-coded path in two places is a path that goes stale in one of them.

## Tests need a real database

There is no mock and no in-memory substitute. The engine's correctness lives in
`FOR UPDATE SKIP LOCKED`, advisory locks and CHECK constraints, none of which a
fake reproduces — so a passing suite against a fake would prove nothing about
the parts most likely to be wrong.

`GOCOMMERCE_TEST_DB` points at the cluster. Two guards make it safe to hand
someone:

- **The database name must contain `test`.** The helpers create and drop
  schemas; anything else is refused with a `t.Fatalf`.
- **Every test gets its own schema.** `requireDB` (in `helpers_test.go`) and
  `gctest.IsolatedDSN` create `gctest_<random>` and drop it on cleanup, pinning
  the pool with libpq's per-connection `options=-csearch_path=…` — a bare
  `SET search_path` would not survive a new pooled connection. Isolation is per
  test rather than "empty the database on entry" because `go test ./...` runs
  each package as its own binary, concurrently, and a shared database that
  every test wipes means one package deleting another's tables mid-run.

With `GOCOMMERCE_TEST_DB` unset, database-backed tests **skip**. A contributor
without PostgreSQL can still run the pure-logic suite; CI always sets it, so
the database path is never untested where it counts.

```powershell
go test ./... -count=1 -timeout 60m        # the whole suite
go test ./core -run TestCheckout -count=1  # one pattern
go build -tags no_admin ./...              # the API-only build
go test -tags no_admin ./core -count=1     # ...and its tests
```

`-count=1` because a cached pass on a schema that no longer exists is not a
pass. `-timeout` because Go's default is ten minutes and the core package alone
is past that: every database-backed test gets its own schema and runs every
migration into it, so the suite grows with both the test count and the
migration count, and CI's `-race` makes each test slower again. Left on the
default, a slow run reports a panic rather than a failure, which sends the
reader hunting a deadlock that is not there. The figure is deliberately far
above the real one — an over-generous timeout costs nothing.

The `no_admin` tag swaps `admin/embed.go` for `admin/embed_no_admin.go`:
no `go:embed`, no panel routes, panel tests skipping themselves. Run both — it
is the build that breaks when core accidentally starts depending on the panel
being mounted.

CI ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) runs the full
suite with `-race` against `postgres:18-alpine` on Linux, the build plus the
pure-logic suite on Windows, and the D23 dependency check.

## The scripts

All under `scripts/`, all PowerShell, all safe to re-run.

**`.\scripts\dev.ps1`** — creates `gocommerce_dev` if needed, builds
`gocommerce.exe`, and serves on `127.0.0.1:8080`.

```powershell
.\scripts\dev.ps1            # foreground, Ctrl+C to stop
.\scripts\dev.ps1 -Seed      # ...and load the demo catalog first
.\scripts\dev.ps1 -Reset     # ...from an empty database
```

Parameters worth knowing: `-Port`, `-Database`, `-Token`, `-Email`,
`-Password`, `-PgHost`, `-PgPort`. The dev store runs on **its own database**,
so nothing it does can touch your test database. Two credentials exist for two
audiences: `dev-token` is the static admin token for scripts and curl;
`admin@example.com` / `devpassword` is the superuser the panel signs in as,
created through `GOCOMMERCE_ADMIN_EMAIL`/`GOCOMMERCE_ADMIN_PASSWORD` bootstrap,
which is create-only and therefore safe on every start.

**`.\scripts\seed.ps1`** — a small catalog over the API: a multi-variant tee
(one variant deliberately out of stock, so checkout's conflict path is easy to
try), a single-variant mug, a gift card with `track_inventory` off, and a draft
product. It matches on slug and skips what already exists.

**`.\scripts\smoke.ps1`** — walks a running store through a complete sale,
checking what each step should have changed: catalog, cart limits, guest
checkout, idempotent replay, stock movement, order lookup by access token,
mark-paid/ship/deliver, refund refusal, admin auth, CSV round trip, pagination,
superuser sign-in and session revocation, contract coverage. It creates and
deletes its own uniquely-SKU'd product, so it is safe against a store with real
data, and exits 0 when every check passes.

Both read `$env:GC_BASE` and `$env:GC_TOKEN`, or take `-BaseUrl` / `-Token`.
`scripts/gc.ps1` holds the shared helpers — dot-source it, don't run it; its
`Invoke-GC` unwraps the JSON envelope and re-throws the engine's own error
message, so a failure reads "only 2 left in stock" rather than "409".

**`.\scripts\build.ps1`** — see [infrastructure](infrastructure.md).

## `gctest`

[`gctest/gctest.go`](../gctest/gctest.go) is the module author's test kit, so
nobody has to re-derive the same harness.

```go
func TestMyProvider(t *testing.T) {
    stub := gctest.StubHTTP(t, fakeVendorHandler)     // never call the real vendor
    app := gctest.New(t, mymodule.New(mymodule.Config{BaseURL: stub.URL}))

    result := gctest.PlaceOrder(t, app, "acme")       // product → cart → checkout
    gctest.DrainOutbox(t, app)

    rec := gctest.AdminRequest(t, app, "GET", "/api/admin/x/acme/things", nil)
    var things []Thing
    gctest.DecodeData(t, rec, &things)
}
```

- `New` / `NewWithConfig` boot on an isolated schema and close on cleanup;
  `AdminToken` is `"gctest-admin-token"`.
- `Request` / `AdminRequest` go through the **full** middleware chain, so a
  test exercises the real envelope, language negotiation and auth.
  `DecodeData` unwraps `{"data": …}` and fails loudly on an error envelope.
- `CreateProduct` and `PlaceOrder` are the fixtures; `RecordingNotifier`,
  `RecordingEvents` and `StubHTTP` are the doubles.

**The one thing that catches everyone: the outbox dispatcher is not running.**
It starts from an `OnStart` hook, and only `ListenAndServe` runs those — a
`gctest` app never serves. Call `gctest.DrainOutbox(t, app)` before asserting
on anything a subscriber or notifier was supposed to do, and
`gctest.AssertOutboxEmpty(t, app)` to prove nothing was left undelivered.

## The admin panel

Full design notes in [`docs/admin-panel.md`](../docs/admin-panel.md). The rules
that matter while editing:

**PocketBase's CSS is verbatim.** Everything in `admin/src/lib/styles/` except
the two sheets below — `vars.css`, `base.css`, `form.css`, `layout.css`,
`table.css`, `modal.css`, `toast.css`, `list.css`, `grid.css`, `dropdown.css`,
`accordion.css`, `bulkbar.css`, `tabs.css`, `tooltip.css`, `animations.css`,
`fonts.css` — is copied from PocketBase's `ui/src/css` and imported in
PocketBase's own order. **Do not hand-edit them.** To take an upstream change,
re-copy from a PocketBase checkout so the diff stays readable.

**Two sheets are ours**: `gocommerce.css` and `fonts-inter.css`. Their
deliberate deviations are listed at the top of each — the `letter-spacing`
reset for Inter, the self-hosted variable fonts under a `font-src 'self'` CSP,
the `prefers-reduced-motion` handling PocketBase has none of. Those are
requested choices, not drift; do not "fix" them.

**Use PocketBase's class vocabulary and nothing else.** There is no `.badge`
(`.label`, four colour variants), no `.btn.accent` (plain `.btn` *is* the
primary), no `.table-wrapper` (`.page-table-wrapper`), and no `.panel`. Grep
the stylesheets before inventing a class. Anything genuinely new goes in
`gocommerce.css`, built from PocketBase's existing tokens — no new colour,
radius or duration.

```powershell
.\scripts\dev.ps1               # the API on :8080
cd admin; npm run dev           # the panel on :5173, proxying /api /health /doc
```

**`admin/build` is committed**, so `go build` works with no Node — which means
a change under `admin/src` is only real once you run `.\scripts\build.ps1`, and
the rebuilt `admin/build` is part of the change.

The panel talks to the same public API as any other client. If a screen needs
an endpoint the API does not have, the API is incomplete; add it there rather
than inventing a private route. And reading the code does not verify a panel
change — a page can return 200 for every asset and still render nothing. Drive
it in a real browser (`CLAUDE.md` has the Playwright harness for this host),
more than once.

## Before you finish

```powershell
gofmt -l .                      # must print nothing
go vet ./...
go test ./... -count=1 -timeout 60m   # needs GOCOMMERCE_TEST_DB
go build -tags no_admin ./...
go test -tags no_admin ./core -count=1
.\scripts\build.ps1             # required after any admin/src change
.\scripts\smoke.ps1             # against a running store
.\gocommerce.exe doctor         # the operational checks
```

And the two that are easy to skip and expensive to miss: a new route needs its
path in `openapi.json` (a test and `doctor` both check), and a new decision that
breaks a rule in [`AGENTS.md`](../AGENTS.md) needs recording in
[`PLAN.md`](../PLAN.md) rather than landing quietly.

## Building an ORDER BY

Nothing from a request is ever concatenated into SQL. A listing that can be
ordered declares a `SortSpec` in `core/sorting.go`'s vocabulary beside the query
it serves; the wire carries a key, the map turns that key into a literal, and
`ParseSort(r, spec)` refuses anything else with a 400 before the first query
runs. To add a sortable field, add a row to that spec's `Columns` — never widen
the resolver, and never accept a column name.

Two rules about the SQL in those rows, both of which fail silently when broken:

- **Both directions are written out in full**, and NULLS placement is pinned per
  field rather than inherited. An expression that can be NULL says `NULLS LAST`
  in *both* directions, so the rows with no value stay at the bottom instead of
  jumping to the top on the operator's second click. An expression that cannot
  be NULL says nothing at all — and must not, because an ascending `(expr, id)`
  btree read backwards yields `DESC NULLS FIRST`, which an explicit
  `DESC NULLS LAST` does not match, so adding one as tidying quietly stops the
  index serving that direction. Only the second kind can be backed by a plain
  ascending index.
- **Any new ORDER BY on a paged query ends with a key unique in the result set**
  — a primary key, or the grouping key of an aggregate listing. `SortSpec.Clause`
  appends it for every sorted request, and a hand-written default clause has to
  carry one itself. Without it PostgreSQL may order tied rows differently in two
  executions of the same statement, so `LIMIT`/`OFFSET` stops being a partition:
  one row arrives on page 1 and again on page 2 while another arrives on
  neither, with a `meta.total` that says nothing is missing.

A sort may not introduce a join. Every listing counts from the same FROM its
page query uses, so a join in the ORDER BY would multiply rows and make
`meta.total` disagree with what it describes; a correlated scalar subquery
cannot, which is why the product listing's `price` and `available` are written
that way.

## Common mistakes

- **Running tests without `GOCOMMERCE_TEST_DB`.** They skip. A green run that
  tested nothing is worse than a red one.
- **Pointing it at the dev database.** The name guard refuses anything without
  `test` in it, and that guard is all that stands between the harness and
  someone's data.
- **Dropping `-count=1`.** Go caches results; the schema they ran against is
  long gone.
- **Forgetting `-tags no_admin`.** It is a separate build and it does break on
  its own.
- **Editing `admin/src` and not running `build.ps1`.** The binary embeds
  `admin/build`, so the change simply is not there.
- **Hand-editing a PocketBase stylesheet** instead of adding to
  `gocommerce.css`. It makes the next upstream re-copy unreadable.
- **Asserting on a notifier or subscriber without draining the outbox.**
  Nothing has been delivered yet.
- **Expecting `-race` locally.** No cgo toolchain on this host; CI runs it.
- **Adding a sortable field and only writing one direction of it**, or adding
  `NULLS LAST` to an expression that cannot be NULL. Neither fails a query; the
  first moves the empty rows on the second click, the second quietly stops using
  the index.
