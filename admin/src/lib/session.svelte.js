/**
 * The signed-in operator, as reactive state.
 *
 * It lives apart from api.js for one reason: Svelte compiles runes only in a
 * `.svelte.js` module, and api.js is plain `.js` imported by twenty-two files.
 * api.js re-exports every name below, so `import { can } from "$lib/api.js"`
 * stays the one documented path and nothing else had to move.
 *
 * Why it has to be a rune at all: `can()` used to read localStorage, which is
 * not a reactive source, from a layout that never remounts — so the nav and the
 * team gate were decided once at mount and could not answer a role that had
 * just been re-cut. Two screens had grown a full document reload to work
 * around that, and a third documented the defect as its reason for a hard
 * navigation.
 *
 * This module must never import api.js. api.js imports it, and the cycle would
 * be real rather than academic.
 */

import { browser } from "$app/environment";
import { untrack } from "svelte";

const TOKEN_KEY = "gocommerce_admin_token";
const RECORD_KEY = "gocommerce_admin_record";
/*
 * The auth response has always carried `expires_at` and the panel threw it
 * away. Keeping it is what lets the shell say "your session ended" instead of
 * showing a login form with no explanation of why it is back.
 */
const EXPIRES_KEY = "gocommerce_admin_expires";

function read(key) {
    if (!browser) return "";
    try {
        return localStorage.getItem(key) || "";
    } catch {
        return "";
    }
}

function write(key, value) {
    if (!browser) return;
    try {
        if (value) localStorage.setItem(key, value);
        else localStorage.removeItem(key);
    } catch {
        /* storage disabled; the session simply will not survive a reload */
    }
}

function storedRecord() {
    try {
        return JSON.parse(read(RECORD_KEY) || "null");
    } catch {
        return null;
    }
}

const s = $state({
    token: read(TOKEN_KEY),
    record: storedRecord(),
    expiresAt: read(EXPIRES_KEY),
    // Set only by endSession(), i.e. by a 401. It is the difference between
    // "you are signed out" and "you were signed out while you were working",
    // and only the second one owes the operator an explanation.
    expired: false,
});

/** session is the reactive surface: read it from a `$derived` or an `$effect`. */
export const session = {
    get token() {
        return s.token;
    },
    get record() {
        return s.record;
    },
    get expiresAt() {
        return s.expiresAt;
    },
    get authenticated() {
        return !!s.token;
    },
    get expired() {
        return s.expired;
    },
    /** The rights, or null for a static admin token — see can(). */
    get rights() {
        return Array.isArray(s.record?.rights) ? s.record.rights : null;
    },
};

/**
 * getToken and getRecord are the imperative half, and are deliberately
 * untracked.
 *
 * `request()` calls getToken() synchronously, and nearly every screen calls
 * `request()` from inside an `$effect` — so a dependency here would re-run
 * every screen's load the moment a refresh rewrote the token, and the boot
 * effect that refreshes on mount would re-trigger itself. Reactive readers use
 * `session` or `can()`, both of which track.
 */
export function getToken() {
    return untrack(() => s.token);
}

/** getRecord returns the signed-in operator, as last seen from the server. */
export function getRecord() {
    return untrack(() => s.record);
}

export function setToken(token, expiresAt) {
    s.token = token || "";
    write(TOKEN_KEY, s.token);
    // Undefined means "not stated", which is not the same as "none": callers
    // that only rotate a token must not silently drop the expiry with it.
    if (expiresAt !== undefined) {
        s.expiresAt = expiresAt || "";
        write(EXPIRES_KEY, s.expiresAt);
    }
}

export function setRecord(record) {
    s.record = record ?? null;
    write(RECORD_KEY, record ? JSON.stringify(record) : "");
}

export function clearToken() {
    setToken("", "");
    setRecord(null);
}

/**
 * beginSession is where every successful sign-in lands — password, invitation,
 * install and refresh alike — so the three stored values can never disagree
 * and `expired` is cleared by the act that makes it untrue.
 */
export function beginSession(result) {
    s.expired = false;
    setToken(result?.token || "", result?.expires_at || "");
    setRecord(result?.record ?? null);
    return s.record;
}

/**
 * endSession is a 401 and nothing else: the credential no longer resolves.
 *
 * It keeps `expired` set so the shell can explain itself. An expired session
 * and an unknown token are deliberately indistinguishable from here — the
 * engine filters on `expires_at > now()` and then answers a generic
 * `unauthorized` — so this says the honest thing either way.
 */
export function endSession() {
    clearToken();
    s.expired = true;
}

/**
 * What the signed-in operator may do.
 *
 * The record carries `rights`, spelled out by the engine from their role, so
 * the panel never keeps its own copy of the permission table — see rights.go.
 *
 * A missing rights list means the credential is a static admin token, which
 * carries everything: scripts and the bootstrap operator have no role to read.
 * Erring that way keeps a token-authenticated panel fully usable, and the
 * engine refuses anything this is wrong about.
 */
export function can(right) {
    const record = s.record;
    if (!record || !Array.isArray(record.rights)) return true;
    return record.rights.includes(right);
}

/** canAny gates a screen that any one of several rights reaches. */
export function canAny(...list) {
    const wanted = list.flat();
    return wanted.length === 0 || wanted.some((right) => can(right));
}

/** canAll gates a control that needs every one of them. */
export function canAll(...list) {
    return list.flat().every((right) => can(right));
}

/** The rights, so a screen can test several without re-reading the record. */
export function rights() {
    return Array.isArray(s.record?.rights) ? s.record.rights : null;
}
