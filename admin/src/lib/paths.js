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
    // The engine's own pages beside the panel: the API reference and liveness.
    "/docs", "/doc", "/health",
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
    const moved = dashPath(path) ?? path;
    return under(moved, DASH) ? moved + rest : DASH;
}
