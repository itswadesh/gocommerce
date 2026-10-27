import { redirect } from "@sveltejs/kit";
import { base } from "$app/paths";

// The homepage editor moved under Themes, where the homepage is one element of
// the store's theme; an address saved before the move still lands on it.
export function load({ url }) {
    redirect(308, `${base}/dash/themes/homepage${url.search}`);
}
