/**
 * The stores this person works in, and which one this tab is in (D83).
 *
 * The id itself lives in store-scope.js, which request() reads on every call;
 * this is the reactive half the sidebar draws from. `current` is kept here as
 * well as there so the store's name in the sidebar changes in the same frame
 * as the switch, not on the next read of storage.
 */
import { stores as storesAPI } from "$lib/api.js";
import { setRecord } from "$lib/session.svelte.js";
import { forgetStore, getStore, setStore } from "$lib/store-scope.js";

/*
 * `state` is where the tab stands: "unknown" until asked, "pending" while
 * asking, then "ok" (in a store), "choose" (several, none remembered), "none"
 * (no store at all) or "error" (the engine could not be reached).
 */
const s = $state({ list: [], loaded: false, current: getStore(), state: "unknown" });

export const myStores = {
    get list() {
        return s.list;
    },
    get loaded() {
        return s.loaded;
    },
    get state() {
        return s.state;
    },
    /** The store this tab is in, as listed, or null before the list arrives. */
    get current() {
        return s.list.find((st) => st.id === s.current || st.slug === s.current) ?? null;
    },
};

export async function loadStores() {
    // A store opened before stores had names (store #1 of an upgraded
    // install) is called by its slug rather than drawn as a blank row.
    s.list = (await storesAPI.mine()).map((st) => ({ ...st, name: st.name || st.slug }));
    s.loaded = true;
    return s.list;
}

/** Called by the shell on sign-out: the next person has stores of their own. */
export function forgetStores() {
    s.list = [];
    s.loaded = false;
    s.state = "unknown";
}

/** markStores lets the shell record "pending", "error" and a retry's "unknown". */
export function markStores(state) {
    s.state = state;
}

function apply(entry) {
    setStore(entry.store.id);
    s.current = entry.store.id;
    s.state = "ok";
    // A static admin token has no record; it keeps the none it had.
    if (entry.record) setRecord(entry.record);
}

/**
 * ensureStore settles which store this tab is in before anything that needs
 * one is asked: the remembered one if it is still theirs, their only one if
 * they have one, else nothing — and the shell sends them to /select-store.
 *
 * @returns {Promise<"ok" | "choose" | "none">}
 */
export async function ensureStore() {
    const list = await loadStores();
    const want = getStore();
    const active = list.filter((st) => st.status === "active");
    const kept = active.find((st) => st.id === want || st.slug === want);
    if (kept) {
        if (kept.id !== want) setStore(kept.id);
        s.current = kept.id;
        s.state = "ok";
        return "ok";
    }
    if (active.length === 1) {
        apply(await storesAPI.enter(active[0].id));
        return "ok";
    }
    // Remembered but gone, suspended or never theirs: naming it would 403
    // every request this tab makes.
    forgetStore();
    s.current = "";
    s.state = active.length === 0 ? "none" : "choose";
    return s.state;
}

/** enterStore moves this tab into a store. The caller clears what was cached. */
export async function enterStore(id) {
    const entry = await storesAPI.enter(id);
    apply(entry);
    return entry;
}

/** createStore opens a store with this person as owner and moves into it. */
export async function createStore(name, slug) {
    const entry = await storesAPI.create(name, slug);
    apply(entry);
    await loadStores();
    return entry;
}
