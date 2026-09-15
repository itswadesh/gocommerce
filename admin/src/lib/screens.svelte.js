/**
 * Screens a module contributed, fetched once for the whole panel.
 *
 * A module may mount its own admin screen and tell the panel about it through
 * `GET /api/admin/screens`; until now only the settings sub-sidebar asked, and
 * the entries appeared nowhere else. With that sidebar gone the main nav is the
 * only navigation there is, so the answer lives here — one fetch, one `$state`,
 * read by `nav.js` the way `hasModule` is.
 *
 * The engine already filters the listing by the caller's rights, so an entry
 * that arrives is one this operator may use. `nav.js` still tests the right it
 * names, because a right can be withdrawn while a panel is open and the nav
 * should lose the entry without a reload.
 */
import { api } from "$lib/api.js";

const s = $state({ list: [], loaded: false });

/** Called by the shell once the operator is known. */
export function loadScreens() {
    api.get("/api/admin/screens")
        .then((result) => {
            s.list = result.data ?? [];
            s.loaded = true;
        })
        // A panel that cannot list module screens is a panel with the built-in
        // ones, not a broken panel.
        .catch(() => {
            s.list = [];
            s.loaded = true;
        });
}

/** The module screens, as nav entries. MUST be read reactively. */
export function moduleScreens() {
    return s.list;
}

/** Called by the shell on sign-out: the next operator may be another store. */
export function forgetScreens() {
    s.list = [];
    s.loaded = false;
}
