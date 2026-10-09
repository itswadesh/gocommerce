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
