import { redirect } from "@sveltejs/kit";
import { base } from "$app/paths";

// Banners moved under Themes, where they are one element of the store's theme
// (D132); an address saved before the move still lands on them.
export function load({ url }) {
    redirect(308, `${base}/dash/themes/banners${url.search}`);
}
