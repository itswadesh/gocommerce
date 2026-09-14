/**
 * Which optional modules this binary was composed with.
 *
 * A module screen is a lie in a store that does not serve the routes behind it:
 * /accounts would 404 on every request in a binary built without ext/identity,
 * and an operator who typed the URL would see four failed calls and no reason.
 * So the nav asks here before it renders one, and each screen asks again before
 * it renders a table.
 *
 * Two sources, in order.
 *
 *   1. `modules` on GET /api/admin/settings. The engine reserves that name on
 *      that response (core/settings_http.go) and does not serve it yet, so the
 *      key is read if it is there and nothing is invented if it is not. The day
 *      it ships, this file stops making requests of its own — the only line to
 *      delete is the fallback.
 *   2. One probe per module of a GET-mounted admin route. A JSON 404 means the
 *      route was never mounted, because core turns any unmatched path under
 *      /api into one before net/http's own 405 path; 200, 401 and 403 all mean
 *      it is there. 403 in particular is "installed, and you may not", which
 *      must not read as absent: the operator it would hide the screen from is
 *      the one who most needs telling why.
 *
 * The probes must be GET routes. A GET of the POST-only /api/admin/x/mcp 404s
 * whether or not mcp is installed, which is why mcp is probed at its audit
 * route instead.
 */

import { can, request } from "$lib/api.js";
import { ensureSettings, settings } from "$lib/settings.svelte.js";

/**
 * The bundled modules that mount an admin surface, where to poke, and the
 * right that poke needs.
 *
 * The right is here so a probe that is certain to be refused is not made at
 * all: an operator without store.operate cannot reach any mcp screen, so
 * asking whether mcp is installed costs a guaranteed 403 on every page load
 * and answers a question nothing will ask. What such an operator sees if they
 * type the URL is the rights explanation, which is the true answer — every
 * screen checks its right before it checks its module, for exactly that
 * reason.
 *
 * A probe that IS made treats 403 as present, because that case is different:
 * the panel thought the right was held and the store had re-cut the role, and
 * "installed, and you may not" must not read as absent.
 */
const PROBES = {
    cms: { path: "/api/admin/x/cms/pages?limit=1", right: "catalog.read" },
    identity: { path: "/api/admin/x/identity/customers?limit=1", right: "customers.read" },
    invoices: { path: "/api/admin/x/invoices?limit=1", right: "orders.read" },
    mcp: { path: "/api/admin/x/mcp/audit?limit=1", right: "store.operate" },
    webhooks: { path: "/api/admin/x/webhooks/endpoints?limit=1", right: "store.operate" },
    // Not a screen: a drawer on the products page. Probed all the same, so the
    // "Import from Amazon" button only appears in a binary that can answer it.
    "import-amazon": { path: "/api/admin/x/import-amazon/jobs", right: "catalog.write" },
};

/* `$state`, so hasModule() read inside a `$derived` re-runs when the answer
   arrives. That is the whole reason this is a .svelte.js module. */
const s = $state({ names: [], loaded: false });

/** A round in flight, so two callers in one tick share one. */
let inflight = null;

/**
 * probe asks whether one module is mounted.
 *
 * Only a 404 means absent. A 401 and a network failure mean the question was
 * not answered at all and are rethrown to abandon the round — caching "no
 * modules" off a dropped connection would hide four screens until the next
 * sign-in.
 */
async function probe(path) {
    try {
        await request("GET", path);
        return true;
    } catch (err) {
        if (err.status === 404) return false;
        if (err.status === 401 || err.status === 0) throw err;
        // 403 is installed-and-refused; a 400 or a 500 is installed and
        // unhappy. Either way the route exists, which is the only thing asked.
        return true;
    }
}

async function round() {
    // Free when the shell has already loaded the settings, and correct when it
    // has not: ensureSettings never throws, so a store that cannot answer it
    // falls through to the probes rather than leaving the panel module-blind.
    await ensureSettings();
    if (Array.isArray(settings.all?.modules)) {
        s.names = [...settings.modules];
        s.loaded = true;
        return s.names;
    }

    const names = Object.keys(PROBES).filter((name) => can(PROBES[name].right));
    const present = await Promise.all(names.map((name) => probe(PROBES[name].path)));
    s.names = names.filter((_, i) => present[i]);
    s.loaded = true;
    return s.names;
}

/**
 * loadModules answers once per sign-in and caches only a definitive answer.
 *
 * Re-entrancy is the in-flight promise rather than the `loaded` flag, so two
 * callers in the same tick share one round and a failed round still retries on
 * the next call.
 */
export async function loadModules() {
    if (s.loaded) return s.names;
    if (inflight) return inflight;
    inflight = round()
        .catch(() => s.names)
        .finally(() => {
            inflight = null;
        });
    return inflight;
}

/** Whether this store serves `name`. False until the answer arrives. */
export function hasModule(name) {
    return s.names.includes(name);
}

/**
 * Whether the answer has arrived at all.
 *
 * A screen shows its "this store was built without…" state on
 * `modulesKnown() && !hasModule(…)` rather than on `!hasModule(…)`, so the
 * first frame after a reload says nothing rather than saying the wrong thing.
 */
export function modulesKnown() {
    return s.loaded;
}

/** Called by the shell on sign-in and sign-out: the next operator on a shared
 *  browser may be signed into another store. */
export function forgetModules() {
    s.names = [];
    s.loaded = false;
    inflight = null;
}
