import { error, redirect } from "@sveltejs/kit";
import { base } from "$app/paths";
import { dashPath } from "$lib/paths.js";

// Bookmarks and emailed links written before the screens moved under /dash.
// A catch-all, because an address with no route is a 404 to the router before
// any layout's load runs; here the old address is a route, so it redirects
// cleanly and only a truly unknown one is the 404 it always was.
export function load({ url }) {
    const moved = dashPath(url.pathname.slice(base.length) || "/");
    if (moved) redirect(308, base + moved + url.search + url.hash);
    error(404, "Not found");
}
