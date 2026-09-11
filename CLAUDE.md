# CLAUDE.md

The architectural guardrails for this repository live in
**[AGENTS.md](AGENTS.md)** — read it first, and treat its fourteen rules as
binding. This file adds only what is specific to working here with Claude Code.

Procedural guides are in [`skills/`](skills/README.md). Load the one that
matches the task rather than reading the whole codebase.

## The five that get broken most

Full reasoning is in AGENTS.md; these are the ones worth having in front of you:

1. **Never write core tables from a module or a script.** Go through the
   service — `app.Pay().MarkPaid(...)`, not `UPDATE orders`. The state change
   and its event have to commit together.
2. **`ext/` packages add zero third-party dependencies.** If the work needs an
   SDK, it is a separate repository.
3. **Money is `*_minor` integers plus a currency code.** No floats, and the API
   never returns a formatted string.
4. **Migrations are append-only.** A shipped migration is frozen; corrections
   are new migrations.
5. **Do not hand-edit `admin/src/lib/styles/*.css`.** They are PocketBase's
   files, verbatim. Only `gocommerce.css` and `fonts-inter.css` are ours.

## Environment on this machine

These are facts about this box, not about the repository — nothing here is
portable to another machine.

Go 1.27.1 is installed at `C:\tools\go\bin` and is not on the system PATH.
The test database is a throwaway container (`gocommerce-pg`,
`postgres:17-alpine`) published on **port 5460**:

```powershell
$env:Path += ';C:\tools\go\bin'
$env:GOCOMMERCE_TEST_DB = 'postgres://gocommerce@127.0.0.1:5460/gocommerce_test?sslmode=disable'
```

Tests need it — there is no mock. Do not reach for 5433: on this box that
port belongs to an unrelated project's database, so the wrong setting does
not fail, it runs the suite against somebody else's cluster.

**Port 8080 is taken here** by an unrelated process, so the two scripts that
assume it have to be told otherwise:

```powershell
.\scripts\dev.ps1 -Port 8090 -PgPort 5460
.\scripts\smoke.ps1 -BaseUrl http://127.0.0.1:8090
```

`-PgPort` for the same reason as the DSN above: the script still defaults to
5433, and the dev store's own database belongs on this project's cluster.

There is no cgo toolchain, so `-race` is unavailable locally; CI covers it.

## Verifying a change

```powershell
gofmt -l .                                  # must print nothing
go vet ./...
go test ./... -count=1 -timeout 40m
go build -tags no_admin ./...               # the API-only build links
go test -tags no_admin ./core -count=1
.\scripts\check-docs.ps1                    # skills and links still match the code
.\scripts\build.ps1                         # required after any admin/src change
.\scripts\smoke.ps1                         # walks a whole sale; exits non-zero on any failure
.\gocommerce.exe doctor                     # the operational checks
```

`scripts/dev.ps1 -Seed` starts a store with demo data, on :8080 unless `-Port`
says otherwise. It signs in as `admin@example.com` / `devpassword`; `dev-token`
is the static admin token for scripts.

## Verifying the admin panel

The panel cannot be checked by reading code — a page can return 200 for every
asset and still render nothing (it did, once: a CSP header blocked SvelteKit's
inline bootstrap). **Claude-in-Chrome is connected on this host** — two local
browsers answer `list_connected_browsers` — but nobody has shown it reaching a
store on `127.0.0.1` from here, and it takes an interactive browser choice
before it will do anything.

Use Playwright. A harness lives in the session scratchpad under `verify/`:
`check.mjs` signs in with a real password, walks every screen, captures
console and page errors, failed requests and horizontal overflow, and
screenshots desktop, dark and mobile:

```powershell
node check.mjs --base http://127.0.0.1:8090
```

It exits non-zero on any finding, and a screen a later wave has not built yet
is marked optional so it reports absence rather than failing. Run it more than
once — the worst bug found this way was intermittent.

## Working style

- Read `PLAN.md` §5 before proposing anything structural. The decisions have
  numbers (D1–D31) and reasons; disagree with the reason, not the conclusion.
- Comments explain **why**. The code already says what.
- Match the surrounding prose voice in docs — plain, specific, no filler.
- When something is genuinely ambiguous, say so and state the assumption you
  proceeded under, rather than picking silently.
