/**
 * The store's own settings, as reactive state.
 *
 * Every screen that shows or takes money needs the store's currency, and every
 * screen that offers an upload needs to know whether uploads are configured at
 * all. Fetching that per screen would mean a call per navigation and a dozen
 * copies of the same fallback; keeping it here means one call and one answer.
 *
 * Settings are read-only from the panel — they are decisions the binary was
 * started with — so there is no setter, only a loader.
 *
 * The getters answer before the call returns, and that is deliberate: a screen
 * must render something while the request is in flight, and the values below
 * are the engine's own defaults rather than invented ones. `loaded` says which
 * of the two you are looking at.
 *
 * Who loads it: the shell, once, as soon as there is a session to load it with —
 * `GET /api/admin/settings` is authenticated, and it is gated by no right
 * precisely so every operator can format money. A screen that needs it before
 * the shell got there calls `ensureSettings()`, which is idempotent. Signing out
 * calls `clearSettings()`, because the next operator may be looking at another
 * store.
 */

import { api } from "$lib/api.js";

const s = $state({
    data: {},
    loaded: false,
    loading: false,
    /** The last failure, kept rather than toasted: a screen decides whether a
     *  settings call it did not make is worth interrupting its operator for. */
    error: null,
});

/** A pending load, so concurrent callers share one request rather than racing. */
let inflight = null;

function list(value) {
    if (Array.isArray(value)) return value;
    return value ? [value] : [];
}

export const settings = {
    /**
     * The store's settlement currency. USD until the store answers — which is
     * what every call site assumed unconditionally before this existed, so it
     * is the one fallback that changes nothing while the request is in flight.
     */
    get currency() {
        return s.data.currency || "USD";
    },

    /**
     * The languages the catalog is written in. `/health/ready` reports a single
     * `language` and this route reports a list plus a default, so both shapes
     * are accepted rather than making every caller know which it is holding.
     */
    get languages() {
        return list(s.data.languages ?? s.data.default_language ?? s.data.language);
    },

    /** The language a field with no explicit locale is written in. */
    get defaultLanguage() {
        return s.data.default_language || this.languages[0] || "";
    },

    /** Whether displayed prices already include tax, which decides how a
     *  price field is labelled rather than how it is stored. */
    get pricesIncludeTax() {
        return !!s.data.prices_include_tax;
    },

    /**
     * Whether the store has somewhere to put a file. A store with no media
     * backend configured should offer no upload control at all, rather than one
     * that fails on drop.
     *
     * Optimistic until the call answers, because that is what the panel did
     * before this existed: hiding the control on a value nobody has sent yet
     * would make an upload disappear for the first second of every page.
     */
    get mediaUploadsEnabled() {
        return s.data.media_uploads_enabled !== false;
    },

    /** The installed payment methods, each `{ code, name, module }`. */
    get paymentMethods() {
        return list(s.data.payment_methods);
    },

    /** The installed fulfillment providers, in the same shape. */
    get fulfillmentProviders() {
        return list(s.data.fulfillment_providers);
    },

    /**
     * The modules this binary was composed with. `modules` is a name the
     * engine reserves on this response and does not serve yet, so this is
     * empty rather than wrong until it does.
     */
    get modules() {
        return list(s.data.modules);
    },

    /** Everything the engine sent, for a screen that renders the lot. */
    get all() {
        return s.data;
    },

    get loaded() {
        return s.loaded;
    },
    get loading() {
        return s.loading;
    },
    get error() {
        return s.error;
    },
};

/**
 * loadSettings fetches the settings and never throws.
 *
 * It swallows the failure on purpose: this call is made on behalf of the shell,
 * not on behalf of whatever screen happens to be mounting, and a store that
 * cannot answer it still has twelve screens that work. The failure is kept on
 * `settings.error` for anything that does care.
 */
export async function loadSettings({ force = false } = {}) {
    if (inflight) return inflight;
    if (s.loaded && !force) return s.data;

    s.loading = true;
    inflight = (async () => {
        try {
            const result = await api.get("/api/admin/settings");
            s.data = result ?? {};
            s.loaded = true;
            s.error = null;
        } catch (err) {
            s.error = err;
        } finally {
            s.loading = false;
            inflight = null;
        }
        return s.data;
    })();
    return inflight;
}

/**
 * ensureSettings is the idempotent form, safe to call from any screen's mount
 * effect: the first caller issues the request and the rest await it. A screen
 * that needs the currency can ask for it without knowing whether the shell got
 * there first.
 */
export function ensureSettings() {
    return loadSettings();
}

/** Called by the shell on sign-out. See the header. */
export function clearSettings() {
    s.data = {};
    s.loaded = false;
    s.error = null;
}
