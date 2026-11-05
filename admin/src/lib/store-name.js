/*
 * A store name typed on a marketing page, carried through sign-up or sign-in to
 * the create-store form. The address may change on the way (a login's redirect
 * drops it), so the tab holds it, and the form takes it once: a person who then
 * opens an existing store is not sent back to create another.
 */
const KEY = "gocommerce.pending-store-name";
const MAX_LENGTH = 60;

/** storeNameFrom reads ?store_name= from a query string, tidied for the form. */
export function storeNameFrom(search) {
    return new URLSearchParams(search).get("store_name")?.trim().slice(0, MAX_LENGTH) ?? "";
}

/** rememberStoreName keeps the name a query string carries. Storage can be refused, and the form still works. */
export function rememberStoreName(search, storage = globalThis.sessionStorage) {
    const name = storeNameFrom(search);
    if (!name) return;
    try {
        storage.setItem(KEY, name);
    } catch {
        // Private windows may refuse: the person types the name again.
    }
}

/** hasStoreName says one is waiting. */
export function hasStoreName(storage = globalThis.sessionStorage) {
    try {
        return !!storage.getItem(KEY);
    } catch {
        return false;
    }
}

/** takeStoreName hands the name over once. */
export function takeStoreName(storage = globalThis.sessionStorage) {
    try {
        const name = storage.getItem(KEY) ?? "";
        storage.removeItem(KEY);
        return name;
    } catch {
        return "";
    }
}
