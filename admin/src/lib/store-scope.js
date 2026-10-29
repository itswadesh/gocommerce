/**
 * The store this panel works in (D83).
 *
 * One engine serves every store, and a request names its store with the
 * x-litekart-store header. Each tab keeps its own (sessionStorage), so a form
 * open in one tab saves to the store it was opened in whatever another tab
 * switched to; the last one chosen is also remembered (localStorage), so a new
 * tab opens there. Storage that is refused — a private window — leaves the
 * panel sending no store, which a single-store install never needs.
 */

const KEY = "gc_store";

function read(storage) {
    try {
        return storage?.getItem(KEY) ?? "";
    } catch {
        return "";
    }
}

function write(storage, value) {
    try {
        storage?.setItem(KEY, value);
    } catch {
        // Refused storage just means the choice is not kept.
    }
}

/** getStore is this tab's store, else the one chosen last, else "". */
export function getStore() {
    return read(globalThis.sessionStorage) || read(globalThis.localStorage);
}

/** setStore makes store this tab's, and the one a new tab opens in. */
export function setStore(store) {
    if (!store) return;
    write(globalThis.sessionStorage, store);
    write(globalThis.localStorage, store);
}

/**
 * forgetStore drops the remembered store, here and for new tabs: it is no
 * longer one this person can open, and naming it would 403 every request.
 */
export function forgetStore() {
    for (const storage of [globalThis.sessionStorage, globalThis.localStorage]) {
        try {
            storage?.removeItem(KEY);
        } catch {
            // Nothing was kept, so nothing to forget.
        }
    }
}

/**
 * pickStore is the store a tab opens in, of the person's active stores: the
 * one a link names, else the one remembered, matched by id or slug. A link to
 * a store that is not theirs is passed over rather than refused — the engine
 * would refuse every request in it.
 *
 * The link wins because it is the only one of the two the person can see. A
 * remembered store is whatever any tab chose last, and with two stores of one
 * name it put a tab opened on their own store's address in the other one.
 */
export function pickStore(active, linked, remembered) {
    const find = (name) => (name ? active.find((st) => st.id === name || st.slug === name) : undefined);
    return find(linked) ?? find(remembered) ?? null;
}

/**
 * storeNamedBy is the store an address names: its ?store=, or that of the
 * page the sign-in form was sent from, which opens once the person is in.
 */
export function storeNamedBy(url) {
    const named = url.searchParams.get("store");
    if (named) return named;
    const next = url.searchParams.get("next");
    if (!next) return "";
    try {
        return new URL(next, url.origin).searchParams.get("store") ?? "";
    } catch {
        return "";
    }
}

/** storeHeaders is the header naming this tab's store, if it has one. */
export function storeHeaders() {
    const store = getStore();
    return store ? { "x-litekart-store": store } : {};
}
