/**
 * The store's setup guide (GET /api/admin/setup), shared by Home's checklist
 * and the sidebar's progress link, so the two can never disagree.
 *
 * Asked again on every navigation by the shell: finishing a step happens on
 * another screen, and the guide should have moved on by the time the owner
 * looks back at it.
 */
import { api } from "$lib/api.js";

const s = $state({ steps: [], next: "", requiredDone: true, complete: true, loaded: false });
let generation = 0;

export const setup = {
    get steps() {
        return s.steps;
    },
    get next() {
        return s.next;
    },
    get requiredDone() {
        return s.requiredDone;
    },
    get complete() {
        return s.complete;
    },
    get loaded() {
        return s.loaded;
    },
    /** Required steps done, out of required steps. */
    get progress() {
        const req = s.steps.filter((st) => st.required);
        return { done: req.filter((st) => st.done).length, total: req.length };
    },
};

export async function loadSetup() {
    const mine = generation;
    try {
        const out = await api.get("/api/admin/setup");
        if (mine !== generation) return;
        s.steps = out?.steps ?? [];
        s.next = out?.next ?? "";
        s.requiredDone = !!out?.required_done;
        s.complete = !!out?.complete;
        s.loaded = true;
    } catch {
        // A guide that cannot load is a panel without a guide, not a broken
        // panel; every screen still works.
    }
}

/** Called when the store changes or the session ends. */
export function forgetSetup() {
    generation++;
    s.steps = [];
    s.loaded = false;
    s.requiredDone = true;
    s.complete = true;
}
