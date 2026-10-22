/**
 * The brand this panel is opened under (D111). One install can answer under
 * several names, each on its own admin domain, and the engine says which one
 * an address is in `GET /api/admin/auth-state`: its name, and a logo that
 * takes the mark's place when there is one.
 *
 * Until that answer arrives the name is the last one this address showed,
 * kept in localStorage — which is per origin, so per admin domain, so it is
 * this brand's — or empty on a first visit. Empty rather than a default
 * name: a panel at one brand's address must never flash another's.
 */
import { auth } from "$lib/api.js";

const KEY = "gocommerce.brand";

function remembered() {
    try {
        const saved = JSON.parse(localStorage.getItem(KEY) || "null");
        return saved && typeof saved.name === "string" ? saved : null;
    } catch {
        return null;
    }
}

const start = typeof localStorage === "undefined" ? null : remembered();

export const brand = $state({
    name: start?.name ?? "",
    logo_url: start?.logo_url ?? "",
});

/**
 * titled is a page's title with the brand after it — "Orders · Alpha" — or
 * the page's name alone while the brand is not known yet.
 */
export function titled(what) {
    return brand.name ? `${what} · ${brand.name}` : what;
}

/** applyBrand takes the brand from an auth-state answer. */
export function applyBrand(state) {
    const b = state?.brand;
    if (!b || typeof b.name !== "string") return;
    brand.name = b.name;
    brand.logo_url = b.logo_url || "";
    try {
        localStorage.setItem(KEY, JSON.stringify({ name: brand.name, logo_url: brand.logo_url }));
    } catch {
        // A private window or blocked storage only costs the next load its
        // head start.
    }
}

let asked;

/** loadBrand asks the engine once per page load. */
export function loadBrand() {
    asked ??= auth
        .state()
        .then(applyBrand)
        .catch(() => {});
    return asked;
}
