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

/** storeHeaders is the header naming this tab's store, if it has one. */
export function storeHeaders() {
    const store = getStore();
    return store ? { "x-litekart-store": store } : {};
}
