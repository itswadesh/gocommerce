/**
 * The platform console's credential, and the one client that carries it (D73).
 *
 * A platform token is not a store credential, and the two never share a
 * code path: the store's client (api.js) sends the store's session and ends it
 * on a 401, and nothing here touches either. Mixing them is how a platform
 * token would come to be sent to a store's API, or a store's session to the
 * platform's.
 *
 * The token is kept in sessionStorage, not localStorage as a store session
 * is. It creates and deletes whole stores and never expires on its own, so it
 * lives as long as the tab and no longer: closing the tab is signing out, and
 * a browser left open on a shared machine does not keep it for the next
 * person who opens the platform's address.
 */

import { browser } from "$app/environment";
import { ApiError } from "$lib/api.js";

const TOKEN_KEY = "gocommerce_platform_token";

function readToken() {
    if (!browser) return "";
    try {
        return sessionStorage.getItem(TOKEN_KEY) || "";
    } catch {
        return "";
    }
}

function writeToken(value) {
    if (!browser) return;
    try {
        if (value) sessionStorage.setItem(TOKEN_KEY, value);
        else sessionStorage.removeItem(TOKEN_KEY);
    } catch {
        /* storage disabled: the console works until the tab reloads */
    }
}

export const platform = $state({
    token: readToken(),
    // Set when a token that was working stops: the sign-in form says why it
    // is back rather than appearing for no reason.
    expired: false,
});

async function call(method, path, body, token = platform.token) {
    const init = { method, headers: {} };
    if (token) init.headers["Authorization"] = "Bearer " + token;
    if (body !== undefined) {
        init.headers["Content-Type"] = "application/json";
        init.body = JSON.stringify(body);
    }
    let response;
    try {
        response = await fetch(path, init);
    } catch {
        throw new ApiError(0, "network_error", "Could not reach the platform. Is it still running?");
    }
    if (response.status === 204) return null;
    const payload = await response.json().catch(() => null);
    if (!response.ok) {
        const e = payload?.error;
        const err = new ApiError(response.status, e?.code || "error", e?.message || response.statusText, e?.details);
        // Only the token in use is forgotten. A candidate being tried at the
        // sign-in form is not stored yet, and its refusal is the form's to show.
        if (response.status === 401 && token && token === platform.token) {
            signOut();
            platform.expired = true;
            err.handled = true;
        }
        throw err;
    }
    return payload?.data ?? payload;
}

export const platformApi = {
    get: (path) => call("GET", path),
    post: (path, body = {}) => call("POST", path, body),
    patch: (path, body) => call("PATCH", path, body),
    delete: (path) => call("DELETE", path),
};

/**
 * signIn keeps a token only once the platform has accepted it. The health
 * report is the cheapest call that needs one and changes nothing.
 */
export async function signIn(token) {
    const candidate = token.trim();
    const health = await call("GET", "/api/platform/health", undefined, candidate);
    platform.token = candidate;
    platform.expired = false;
    writeToken(candidate);
    return health;
}

export function signOut() {
    platform.token = "";
    writeToken("");
}

export const STATUSES = [
    { value: "active", label: "Open", hint: "serving its shoppers and its operators" },
    { value: "suspended", label: "Suspended", hint: "data kept, every host answers 503" },
];

export function statusLabel(status) {
    return STATUSES.find((s) => s.value === status)?.label ?? status;
}

export function statusClass(status) {
    return status === "active" ? "label-success" : "label-warning";
}

/**
 * hostURL is where a store is opened from the console. A store answers on
 * the port the platform listens on, which is this page's own port: a console
 * served on :8098 links to acme.localhost:8098, not to :443.
 */
export function hostURL(host) {
    if (!host || !browser) return "";
    const { protocol, port } = window.location;
    return `${protocol}//${host}${port ? ":" + port : ""}/`;
}
