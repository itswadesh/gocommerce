import { error, redirect } from "@sveltejs/kit";
import { base } from "$app/paths";
import { dashPath } from "$lib/paths.js";

// Bookmarks and emailed links written before the screens moved under /dash.
// A catch-all, because an address with no route is a 404 to the router before
// any layout's load runs; here the old address is a route, so it redirects
// cleanly and only a truly unknown one is the 404 it always was.
export function load({ url }) {
    const moved = dashPath(url.pathname.slice(base.length) || "/");
    // The fragment comes from location: SvelteKit's dev server throws when a
    // load reads it from its url, and this panel never renders on a server.
    const hash = typeof location === "undefined" ? "" : location.hash;
    if (moved) redirect(308, base + moved + url.search + hash);
    error(404, "Not found");
}
