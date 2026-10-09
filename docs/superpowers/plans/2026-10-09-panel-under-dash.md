# Panel under /dash Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move every store screen of the admin panel under `/dash/`, sign-in to `/admin/auth/login`, with every old path redirecting to its new one.

**Architecture:** One pure function, `dashPath(oldPath)` in `admin/src/lib/paths.js`, is the whole mapping. A one-off codemod uses it to rewrite every link in the panel source after the route folders are moved; the root layout uses it at run time to redirect old bookmarks and emailed links. The Go side changes only the two links it writes into the panel (invitation and password reset).

**Tech Stack:** SvelteKit 2 + Svelte 5 (adapter-static SPA, `ssr = false`), `node --test` for panel unit tests, Go 1.27 with tests against PostgreSQL, Playwright (browsers already installed under `%LOCALAPPDATA%\ms-playwright`) for the browser check.

**Spec:** `docs/superpowers/specs/2026-10-09-store-switcher-and-custom-roles-design.md`, section **URLs**. This plan is the first of three; parts 2 and 1 get their own plans.

## Global Constraints

- Store screens live under `/dash/` and keep GoCommerce's names, except exactly these renames:
  `/shipping` → `/dash/shipping-settings`, `/shipping/providers` → `/dash/shipping-settings/providers`,
  `/carts` → `/dash/checkouts`, `/locations` → `/dash/warehouses`,
  `/settings/superusers` → `/dash/settings/teams`.
- `/` → `/dash`. Sign-in → `/admin/auth/login`. `/reset-password/[[token]]` → `/admin/auth/reset-password/[[token]]`. `/accept-invite/[token]` → `/admin/auth/accept-invite/[token]`.
- `/platform` and `/portal` do not move.
- Every old path redirects to its new one with the query string kept.
- Do not hand-edit `admin/src/lib/styles/*.css` (PocketBase's files). `admin/build` is committed: run `.\scripts\build.ps1` after any `admin/src` change and commit the build.
- Environment (this machine): `$env:Path += ';C:\tools\go\bin'`; `$env:GOCOMMERCE_TEST_DB = 'postgres://gocommerce@127.0.0.1:5460/gocommerce_test?sslmode=disable'`; dev store `.\scripts\dev.ps1 -Port 8090 -PgPort 5460 -Seed`. Never 5433 or 8080.
- Another session may have uncommitted work in this tree (custom reports, `PLAN.md`, `platform/`). Stage only files this plan touches; never `git add -A` or `git commit -a`.
- Commits: subject written for the public changelog; this whole plan ships as one feature, so intermediate commits carry `Update: skip` and the last one does not.

## Review Focus

1. A static asset path (`/images/logo.svg`, `/fonts/...`, `/favicon.svg`, `/theme.js`, `/_app/...`) rewritten to `/dash/...` — the logo and fonts would 404. `dashPath` must return `null` for them (Task 1 test).
2. An old link with a query string or hash (`/orders?email=a@b.c`, a reset link `/reset-password/abc`) losing it on redirect (Task 1 and Task 3 tests).
3. A redirect loop: `/dash/...`, `/admin/auth/...`, `/platform`, `/portal` must map to `null`, never to a new path (Task 1 test).
4. A link built at run time from a variable (`` `${base}/${section}/...` ``) that the codemod cannot see — caught by the guard test in Task 2 and by the browser walk in Task 5.
5. Signing in from `/admin/auth/login?next=/dash/orders?status=open` must land on that exact screen and filter; `next` pointing off-site (`//evil.example`, `https://...`) must be ignored (Task 3 test).

---

### Task 1: The mapping, `dashPath`

**Files:**
- Create: `admin/src/lib/paths.js`
- Test: `admin/tests/paths.test.js`

**Interfaces:**
- Produces: `export const DASH = "/dash"`; `export function dashPath(pathname: string): string | null` — the new path for an old one (query and hash are the caller's to carry), or `null` when the path is already new, is not a panel screen, or is an asset; `export function safeNext(next: string | null): string` — a same-origin `/dash...` path to go to after sign-in, else `"/dash"`.

- [ ] **Step 1: Write the failing test**

```js
// admin/tests/paths.test.js
import { test } from "node:test";
import assert from "node:assert/strict";
import { dashPath, safeNext } from "../src/lib/paths.js";

test("plain screens move under /dash with their names", () => {
    assert.equal(dashPath("/"), "/dash");
    assert.equal(dashPath("/orders"), "/dash/orders");
    assert.equal(dashPath("/orders/42"), "/dash/orders/42");
    assert.equal(dashPath("/discounts"), "/dash/discounts");
    assert.equal(dashPath("/cms/7"), "/dash/cms/7");
    assert.equal(dashPath("/settings/api-keys"), "/dash/settings/api-keys");
    assert.equal(dashPath("/settings/roles/manager"), "/dash/settings/roles/manager");
    assert.equal(dashPath("/b2b/companies/3"), "/dash/b2b/companies/3");
    assert.equal(dashPath("/x/agent"), "/dash/x/agent");
});

test("the renamed screens take their new names", () => {
    assert.equal(dashPath("/shipping"), "/dash/shipping-settings");
    assert.equal(dashPath("/shipping/providers"), "/dash/shipping-settings/providers");
    assert.equal(dashPath("/carts"), "/dash/checkouts");
    assert.equal(dashPath("/locations"), "/dash/warehouses");
    assert.equal(dashPath("/settings/superusers"), "/dash/settings/teams");
    // A prefix match is a whole segment, never part of one.
    assert.equal(dashPath("/shippingzones"), "/dash/shippingzones");
});

test("auth screens move under /admin/auth", () => {
    assert.equal(dashPath("/reset-password"), "/admin/auth/reset-password");
    assert.equal(dashPath("/reset-password/abc"), "/admin/auth/reset-password/abc");
    assert.equal(dashPath("/accept-invite/tok"), "/admin/auth/accept-invite/tok");
});

test("new paths, other apps and assets are left alone", () => {
    for (const p of [
        "/dash", "/dash/orders", "/admin/auth/login", "/select-store", "/stores/create",
        "/platform", "/platform/acme", "/portal", "/portal/orders",
        "/_app/immutable/x.js", "/images/logo.svg", "/fonts/inter.woff2",
        "/favicon.svg", "/theme.js", "/api/admin/orders",
    ]) {
        assert.equal(dashPath(p), null, p);
    }
});

test("safeNext only ever returns a path inside /dash on this origin", () => {
    assert.equal(safeNext("/dash/orders?status=open"), "/dash/orders?status=open");
    assert.equal(safeNext(null), "/dash");
    assert.equal(safeNext(""), "/dash");
    assert.equal(safeNext("//evil.example/dash"), "/dash");
    assert.equal(safeNext("https://evil.example/dash"), "/dash");
    assert.equal(safeNext("/platform"), "/dash");
    assert.equal(safeNext("/orders"), "/dash/orders");
    assert.equal(safeNext("/dash/orders?a=1#top"), "/dash/orders?a=1#top");
});
```

- [ ] **Step 2: Run it to make sure it fails**

Run (in `admin/`): `node --test tests/paths.test.js`
Expected: FAIL — `Cannot find module '../src/lib/paths.js'`.

- [ ] **Step 3: Write the implementation**

```js
// admin/src/lib/paths.js
/*
 * Where every screen lives, and where it used to.
 *
 * The panel's screens sit under /dash, as the KitCommerce admin's do, so a
 * person moving between the two finds an order at the same address. One
 * function holds the whole mapping because two things need it to agree: the
 * codemod that moved the links, and the layout that redirects a bookmark or
 * an emailed link written before the move.
 */

export const DASH = "/dash";

// Whole-segment renames, checked before the general rule. Longest first, so
// /shipping/providers is not caught by /shipping.
const RENAMED = [
    ["/settings/superusers", "/dash/settings/teams"],
    ["/shipping/providers", "/dash/shipping-settings/providers"],
    ["/shipping", "/dash/shipping-settings"],
    ["/carts", "/dash/checkouts"],
    ["/locations", "/dash/warehouses"],
    ["/reset-password", "/admin/auth/reset-password"],
    ["/accept-invite", "/admin/auth/accept-invite"],
];

// Paths that are already where they belong, belong to another app (the
// platform console, the trade portal), or are files rather than screens.
// Returning null for these is what keeps the redirect from looping.
const STAYS = [
    "/dash", "/admin", "/select-store", "/stores", "/platform", "/portal",
    "/_app", "/images", "/fonts", "/favicon.svg", "/theme.js", "/api",
];

function under(path, prefix) {
    return path === prefix || path.startsWith(prefix + "/");
}

/** @param {string} pathname @returns {string | null} */
export function dashPath(pathname) {
    const p = pathname || "/";
    if (p === "/") return DASH;
    if (STAYS.some((s) => under(p, s))) return null;
    for (const [from, to] of RENAMED) {
        if (under(p, from)) return to + p.slice(from.length);
    }
    return DASH + p;
}

/**
 * Where to go after signing in. Only a path on this origin, and only a store
 * screen: `next` arrives in the address bar, so anything else in it is a
 * stranger's choice of where to send a person who has just signed in.
 * @param {string | null} next
 */
export function safeNext(next) {
    if (!next || !next.startsWith("/") || next.startsWith("//")) return DASH;
    const cut = next.search(/[?#]/);
    const path = cut < 0 ? next : next.slice(0, cut);
    const rest = cut < 0 ? "" : next.slice(cut);
    const moved = dashPath(path);
    const target = (moved ?? path) + rest;
    return under(moved ?? path, DASH) ? target : DASH;
}
```

- [ ] **Step 4: Run the tests and make sure they pass**

Run (in `admin/`): `node --test tests/paths.test.js`
Expected: PASS, 5 tests.

- [ ] **Step 5: Commit**

```powershell
git add admin/src/lib/paths.js admin/tests/paths.test.js
git commit -m "Map the panel's old addresses to /dash" -m "Update: skip"
```

---

### Task 2: Move the screens and rewrite the links

**Files:**
- Move: every folder in `admin/src/routes/` except `platform`, `portal`, `accept-invite`, `reset-password` → `admin/src/routes/dash/…` (renamed per the Global Constraints)
- Move: `admin/src/routes/+page.svelte` (Home) → `admin/src/routes/dash/+page.svelte`
- Create: `admin/src/routes/+page.svelte` (a redirect), `admin/tests/no-legacy-links.test.js`
- Modify (by codemod): every `.svelte`/`.js` under `admin/src` that links into the panel, including `admin/src/lib/nav.js`
- Codemod lives outside the repo: `<scratchpad>/move-to-dash.mjs`

**Interfaces:**
- Consumes: `dashPath` from Task 1.
- Produces: routes at the new paths; `nav.js` items whose `href` starts with `/dash`.

- [ ] **Step 1: Write the guard test** (fails today, passes once every link is moved)

```js
// admin/tests/no-legacy-links.test.js
/*
 * No link in the panel may point at a pre-/dash address. The redirect would
 * catch it, but a link that works only by redirect is a page load spent on
 * every click, and the next rename would turn it into a 404.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { dashPath } from "../src/lib/paths.js";

function files(dir) {
    return readdirSync(dir).flatMap((name) => {
        const p = join(dir, name);
        if (statSync(p).isDirectory()) return name === "styles" ? [] : files(p);
        return /\.(svelte|js)$/.test(name) ? [p] : [];
    });
}

// {base}/x, ${base}/x, base + "/x", href: "/x", goto("/x")
const LINK = /(?:\{base\}|\$\{base\}|base \+ ["'`]|href: ["'`]|goto\(["'`])(\/[a-z0-9\-_/]*)/g;

test("every panel link points at its /dash address", () => {
    const stale = [];
    for (const file of files(fileURLToPath(new URL("../src", import.meta.url)))) {
        const src = readFileSync(file, "utf8");
        for (const m of src.matchAll(LINK)) {
            const path = m[1].replace(/\/$/, "") || "/";
            if (dashPath(path) !== null) stale.push(`${file}: ${m[0]}`);
        }
    }
    assert.deepEqual(stale, []);
});
```

- [ ] **Step 2: Run it to make sure it fails**

Run (in `admin/`): `node --test tests/no-legacy-links.test.js`
Expected: FAIL, listing roughly 190 links across about 73 files.

- [ ] **Step 3: Move the route folders with git**

```powershell
cd C:\projects\gocommerce\admin\src\routes
New-Item -ItemType Directory dash | Out-Null
git mv +page.svelte dash/+page.svelte
$keep = 'platform','portal','accept-invite','reset-password','dash'
$renames = @{ shipping = 'shipping-settings'; carts = 'checkouts'; locations = 'warehouses' }
Get-ChildItem -Directory | Where-Object { $keep -notcontains $_.Name } | ForEach-Object {
    $to = if ($renames.ContainsKey($_.Name)) { $renames[$_.Name] } else { $_.Name }
    git mv $_.Name "dash/$to"
}
git mv dash/settings/superusers dash/settings/teams
```

`/shipping/providers` comes along inside `shipping-settings`, which is what the mapping says.

- [ ] **Step 4: Write the codemod in the scratchpad and run it**

```js
// <scratchpad>/move-to-dash.mjs — run: node move-to-dash.mjs C:\projects\gocommerce\admin\src
import { readFileSync, writeFileSync, readdirSync, statSync } from "node:fs";
import { join } from "node:path";
import { pathToFileURL } from "node:url";

const src = process.argv[2];
const { dashPath } = await import(pathToFileURL(join(src, "lib", "paths.js")).href);

function files(dir) {
    return readdirSync(dir).flatMap((name) => {
        const p = join(dir, name);
        if (statSync(p).isDirectory()) return name === "styles" ? [] : files(p);
        return /\.(svelte|js)$/.test(name) ? [p] : [];
    });
}

// Same shapes the guard test looks for. The captured path stops at anything
// that is not a path character, so "{base}/orders/{id}" rewrites "/orders"
// and leaves "/{id}" where it was.
const LINK = /(\{base\}|\$\{base\}|base \+ ["'`]|href: ["'`]|goto\(["'`])(\/[a-z0-9\-_/]*)/g;

let changed = 0;
for (const file of files(src)) {
    if (file.endsWith(join("lib", "paths.js"))) continue;
    const before = readFileSync(file, "utf8");
    const after = before.replace(LINK, (whole, lead, path) => {
        const trail = path.endsWith("/") && path !== "/" ? "/" : "";
        const moved = dashPath(path.replace(/\/$/, "") || "/");
        return moved === null ? whole : lead + moved + trail;
    });
    if (after !== before) { writeFileSync(file, after); changed++; }
}
console.log(`rewrote links in ${changed} files`);
```

Run: `node <scratchpad>\move-to-dash.mjs C:\projects\gocommerce\admin\src`
Expected: `rewrote links in ~75 files`.

- [ ] **Step 5: Fix what the codemod cannot see**

Search for links built from variables and route checks that compare pathnames:

```powershell
cd C:\projects\gocommerce\admin
Select-String -Path (Get-ChildItem src -Recurse -Include *.svelte,*.js) -Pattern 'pathname\.(startsWith|replace)|=== "/|\$\{base\}/\$\{|"/x/"|`/x/' |
  ForEach-Object { "$($_.Path):$($_.LineNumber): $($_.Line.Trim())" }
```

For each hit, make it compare or build against the `/dash` path. Known places:
- `admin/src/routes/+layout.svelte` line ~166: `const path = page.url.pathname.replace(base, "") || "/";` — the nav's active-item match. `nav.js` hrefs are now `/dash/...`, so this keeps working; check the Home item, whose `href` became `/dash` and has `exact: true`.
- Module screens: wherever the panel builds `/x/${slug}`, it must build `/dash/x/${slug}` (the route folder is now `dash/x/[slug]`).

- [ ] **Step 6: Make `/` send people to `/dash`**

```svelte
<!-- admin/src/routes/+page.svelte -->
<script>
    /*
     * The panel's home is /dash, as the KitCommerce admin's is. The root
     * stays a route only so typing the store's bare address still lands
     * somewhere: the layout sends a signed-out visitor to sign in first.
     */
    import { goto } from "$app/navigation";
    import { base } from "$app/paths";
    import { onMount } from "svelte";

    onMount(() => goto(`${base}/dash`, { replaceState: true }));
</script>
```

- [ ] **Step 7: Run the guard test and the whole panel suite**

Run (in `admin/`): `node --test tests/no-legacy-links.test.js` → PASS.
Run: `npm test` → all pass. Tests that read component source by path (e.g. `tests/roles.test.js`, `tests/picking.test.js`, `tests/clientpage.test.js`) need their file paths updated to the moved routes; update the paths only, not the assertions.
Run: `npm run check` → no new errors.

- [ ] **Step 8: Commit**

```powershell
cd C:\projects\gocommerce
git add admin/src admin/tests
git commit -m "Move the panel's screens under /dash" -m "Update: skip"
```

---

### Task 3: Sign-in at /admin/auth/login, and redirects from old addresses

**Files:**
- Create: `admin/src/routes/admin/auth/login/+page.svelte`
- Move: `admin/src/routes/reset-password` → `admin/src/routes/admin/auth/reset-password`; `admin/src/routes/accept-invite` → `admin/src/routes/admin/auth/accept-invite`
- Modify: `admin/src/routes/+layout.svelte` (public prefixes, signed-out redirect, sign-in on the login route), `admin/src/routes/+layout.js` (old-address redirect)
- Test: `admin/tests/layout-auth.test.js`

**Interfaces:**
- Consumes: `dashPath`, `safeNext`, `DASH` from Task 1.
- Produces: `/admin/auth/login?next=<path>`; signed-out visits to any `/dash` path land there.

- [ ] **Step 1: Write the failing test** (source-reading, the style `tests/roles.test.js` uses, because `node:test` cannot import `.svelte`)

```js
// admin/tests/layout-auth.test.js
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";

const read = (p) => readFileSync(new URL(p, import.meta.url), "utf8");

test("auth screens live under /admin/auth", () => {
    assert.ok(existsSync(new URL("../src/routes/admin/auth/login/+page.svelte", import.meta.url)));
    assert.ok(existsSync(new URL("../src/routes/admin/auth/reset-password/[[token]]/+page.svelte", import.meta.url)));
    assert.ok(existsSync(new URL("../src/routes/admin/auth/accept-invite/[token]/+page.svelte", import.meta.url)));
});

test("the layout treats /admin/auth as public and redirects signed-out visitors there", () => {
    const layout = read("../src/routes/+layout.svelte");
    assert.match(layout, /PUBLIC_PREFIXES = \[[^\]]*"\/admin\/auth"/);
    assert.match(layout, /admin\/auth\/login\?next=/);
});

test("old addresses redirect through dashPath, keeping the query", () => {
    const load = read("../src/routes/+layout.js");
    assert.match(load, /dashPath\(/);
    assert.match(load, /url\.search/);
});
```

- [ ] **Step 2: Run it to make sure it fails**

Run (in `admin/`): `node --test tests/layout-auth.test.js` → FAIL.

- [ ] **Step 3: Move the two auth routes**

```powershell
cd C:\projects\gocommerce\admin\src\routes
New-Item -ItemType Directory admin\auth -Force | Out-Null
git mv reset-password admin/auth/reset-password
git mv accept-invite admin/auth/accept-invite
```

Inside those two pages, any `goto("/")` or `{base}/` after success becomes `/dash` (the codemod already rewrote literal links; check `goto(` calls by hand).

- [ ] **Step 4: Redirect old addresses in `+layout.js`**

```js
// admin/src/routes/+layout.js
import { redirect } from "@sveltejs/kit";
import { base } from "$app/paths";
import { dashPath } from "$lib/paths.js";

// A single-page app: no server rendering and nothing to prerender, because
// every screen depends on a running store and an admin token.
export const ssr = false;
export const prerender = false;
export const trailingSlash = "never";

// Bookmarks and emailed links written before the screens moved under /dash.
// The root path is left to its own page, which does the same thing once the
// layout has decided whether the visitor is signed in.
export function load({ url }) {
    const path = url.pathname.slice(base.length) || "/";
    if (path === "/") return;
    const moved = dashPath(path);
    if (moved) redirect(308, base + moved + url.search + url.hash);
}
```

- [ ] **Step 5: Update the layout**

In `admin/src/routes/+layout.svelte`:

1. `const PUBLIC_PREFIXES = ["/admin/auth", "/platform", "/portal"];` and extend the comment above it: the `/admin/auth` screens are where a person without a session signs in, resets a password or accepts an invitation.
2. Where the layout currently renders `<Login onauthenticated={onAuthenticated} />` for a signed-out visitor on a store screen (line ~590): instead, on mount/effect, `goto(\`${base}/admin/auth/login?next=${encodeURIComponent(page.url.pathname.slice(base.length) + page.url.search)}\`, { replaceState: true })`.
3. On `/admin/auth/login` itself, the layout renders `<Login onauthenticated={signedIn} />` with no shell, where `signedIn` runs the existing `onAuthenticated()` and then `goto(base + safeNext(page.url.searchParams.get("next")), { replaceState: true })`. Keeping it in the layout keeps `onAuthenticated` — which clears and reloads the module and screen caches — in one place.
4. A signed-in visitor who opens `/admin/auth/login` goes straight to `safeNext(next)`.

`admin/src/routes/admin/auth/login/+page.svelte` exists so the route resolves; its body is empty with a comment saying the layout draws the form, and why.

- [ ] **Step 6: Run the tests**

Run (in `admin/`): `node --test tests/layout-auth.test.js tests/paths.test.js tests/no-legacy-links.test.js` → PASS. `npm test` → PASS. `npm run check` → no new errors.

- [ ] **Step 7: Commit**

```powershell
cd C:\projects\gocommerce
git add admin/src admin/tests
git commit -m "Sign in at /admin/auth/login and redirect old panel addresses" -m "Update: skip"
```

---

### Task 4: Links the engine writes

**Files:**
- Modify: `core/invitations_http.go:92`, `core/passwordreset.go:343`
- Test: `core/passwordreset_test.go:224`, and the invitations test that checks `accept_url` (find it with `Select-String -Path core\*_test.go -Pattern 'accept_url|accept-invite'`)

**Interfaces:**
- Produces: `accept_url` = `<scheme>://<host>/admin/auth/accept-invite/<token>`; reset link = `<PanelURL>/admin/auth/reset-password/<token>`.

- [ ] **Step 1: Update the tests first**

In `core/passwordreset_test.go:224`:

```go
if !strings.HasPrefix(link, "https://shop.example/admin/auth/reset-password/") {
```

In the invitations test, the expected `accept_url` prefix becomes `/admin/auth/accept-invite/`. If no test asserts the prefix, add one beside the existing invite test:

```go
if !strings.Contains(out.AcceptURL, "/admin/auth/accept-invite/") {
    t.Fatalf("accept_url = %q, want it under /admin/auth/accept-invite/", out.AcceptURL)
}
```

(Use the field name the existing test reads the response into.)

- [ ] **Step 2: Run them to make sure they fail**

```powershell
$env:Path += ';C:\tools\go\bin'
$env:GOCOMMERCE_TEST_DB = 'postgres://gocommerce@127.0.0.1:5460/gocommerce_test?sslmode=disable'
go test ./core -run 'PasswordReset|Invit' -count=1
```

Expected: FAIL on the two prefixes.

- [ ] **Step 3: Change the two builders**

`core/invitations_http.go:92`:

```go
return scheme + "://" + host + "/admin/auth/accept-invite/" + token
```

`core/passwordreset.go:343`:

```go
return s.app.cfg.PanelURL + "/admin/auth/reset-password/" + token
```

Both pages moved under /admin/auth with the rest of the panel's sign-in screens; links already sent still open, because the panel redirects the old paths.

- [ ] **Step 4: Run them to make sure they pass**

Run: `go test ./core -run 'PasswordReset|Invit' -count=1` → PASS.

- [ ] **Step 5: Commit**

```powershell
git add core/invitations_http.go core/passwordreset.go core/passwordreset_test.go core/invitations_test.go
git commit -m "Point invitation and reset links at /admin/auth" -m "Update: skip"
```

(Stage the invitations test file actually changed; its name may differ.)

---

### Task 5: Docs, build, and the browser walk

**Files:**
- Modify: any doc naming a panel path — find with
  `Select-String -Path skills\*.md,docs\*.md,README.md -Pattern '\]\(/(orders|settings|shipping|carts|locations)|panel.{0,40}`/(orders|settings|products|shipping|carts|locations)'`
- Modify: `docs/admin-panel.md` — one short paragraph: screens live under `/dash`, why (the KitCommerce admin's addresses), and that old addresses redirect.
- Rebuild: `admin/build/**` via `.\scripts\build.ps1`
- Browser check: `<scratchpad>/verify/dash-walk.mjs` (not committed)

- [ ] **Step 1: Update the docs**

Change each panel path found to its `/dash` form. Add the paragraph to `docs/admin-panel.md`. Run `.\scripts\check-docs.ps1` → "docs agree with the code".

- [ ] **Step 2: Build and run the full Go checks**

```powershell
cd C:\projects\gocommerce
$env:Path += ';C:\tools\go\bin'
$env:GOCOMMERCE_TEST_DB = 'postgres://gocommerce@127.0.0.1:5460/gocommerce_test?sslmode=disable'
.\scripts\build.ps1
gofmt -l .            # must print nothing
go vet ./...
go test ./... -count=1 -timeout 60m
```

All must pass. A failure in a file this plan did not touch, caused by the other session's uncommitted work, is reported, not fixed here.

- [ ] **Step 3: Start the dev store**

```powershell
.\scripts\dev.ps1 -Port 8090 -PgPort 5460 -Seed
```

(Runs in the foreground; start it in the background.)

- [ ] **Step 4: Walk it in a browser**

```js
// <scratchpad>/verify/dash-walk.mjs — the playwright library under plain node:
//   cd <scratchpad>\verify; npm init -y; npm i playwright@1; node dash-walk.mjs http://127.0.0.1:8090
import { chromium } from "playwright";

const base = process.argv[2];
const problems = [];
const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
page.on("pageerror", (e) => problems.push(`pageerror: ${e.message}`));
page.on("console", (m) => m.type() === "error" && problems.push(`console: ${m.text()}`));
page.on("response", (r) => r.status() >= 400 && !r.url().includes("/api/") && problems.push(`${r.status()} ${r.url()}`));

// Signed out: a deep link lands on sign-in with next, and comes back after.
await page.goto(`${base}/orders?status=open`);
await page.waitForURL(/\/admin\/auth\/login\?next=/);
await page.fill('input[type="email"]', "admin@example.com");
await page.fill('input[type="password"]', "devpassword");
await page.click('button[type="submit"]');
await page.waitForURL(`${base}/dash/orders?status=open`);

// Every old address lands on its new one, query kept.
const cases = [
    ["/", "/dash"], ["/products", "/dash/products"], ["/discounts", "/dash/discounts"],
    ["/shipping", "/dash/shipping-settings"], ["/carts", "/dash/checkouts"],
    ["/locations", "/dash/warehouses"], ["/settings/superusers", "/dash/settings/teams"],
    ["/settings/roles", "/dash/settings/roles"], ["/cms", "/dash/cms"],
    ["/customers?q=a", "/dash/customers?q=a"],
];
for (const [from, to] of cases) {
    await page.goto(base + from);
    await page.waitForURL(base + to);
}

// Every nav link opens a screen that renders something, with no overflow.
const hrefs = await page.$$eval("nav a[href^='/dash']", (as) => [...new Set(as.map((a) => a.getAttribute("href")))]);
for (const href of hrefs) {
    await page.goto(base + href);
    await page.waitForLoadState("networkidle");
    const blank = await page.evaluate(() => document.querySelector("main")?.innerText.trim().length === 0);
    if (blank) problems.push(`blank: ${href}`);
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
    if (overflow) problems.push(`overflow: ${href}`);
}
for (const vp of [{ width: 390, height: 844 }]) {
    await page.setViewportSize(vp);
    await page.goto(`${base}/dash/orders`);
    await page.screenshot({ path: "dash-orders-mobile.png" });
}
await page.emulateMedia({ colorScheme: "dark" });
await page.goto(`${base}/dash`);
await page.screenshot({ path: "dash-dark.png" });

await browser.close();
console.log(problems.length ? problems.join("\n") : `ok: ${hrefs.length} screens, ${cases.length} redirects`);
process.exit(problems.length ? 1 : 0);
```

Run it twice (CLAUDE.md: the worst bug found this way was intermittent). Expected both times: `ok: N screens, 10 redirects`. Look at the two screenshots. Adjust the sign-in selectors to the Login component's actual inputs if they differ.

- [ ] **Step 5: Smoke test and doctor**

```powershell
.\scripts\smoke.ps1 -BaseUrl http://127.0.0.1:8090
.\gocommerce.exe doctor
```

Both must pass. Stop the dev store afterwards.

- [ ] **Step 6: Commit**

```powershell
git add admin/build docs skills README.md
git commit -m "Put the admin panel's screens under /dash, as the KitCommerce admin does" -m "Every store screen now lives under /dash — /dash/orders, /dash/settings/teams, /dash/shipping-settings — and sign-in is /admin/auth/login. Old addresses redirect, so bookmarks and links already sent keep working."
```

This is the one commit readers see on the updates page; it is not marked `Update: skip`. Do not push until the user asks: a push to `main` deploys admin.kitcommerce.store.
