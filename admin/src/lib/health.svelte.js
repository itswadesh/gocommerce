/**
 * One health report, shared by everything that draws it.
 *
 * The shell needs a yes/no to decide whether to show a badge, and the
 * Diagnostics screen needs the rows. They have to agree — two independent
 * fetches would disagree the moment a sweep fixed something on one of them —
 * and a prop cannot carry it, because the screen is `{@render children}` inside
 * the layout rather than a child component of it. Hence a rune module, in the
 * shape of toast.svelte.js: `$state` behind getters.
 *
 * It never raises a toast. This polls on every page, and a store whose database
 * has gone away must not announce it every five minutes; the screen is where
 * failures are read, and `error` is there for it to draw.
 *
 * And it holds module-scope state, which outlives a session. Without an
 * explicit clear, signing out as an owner with a failing check and back in as
 * staff would leave `failing` true, and the layout's Settings entry carries no
 * right of its own — so staff would see a red dot on a section whose
 * Diagnostics link is correctly hidden from them. That is why `refresh()`
 * clears rather than skips when the right is absent, why `watch()`'s teardown
 * clears, and why every badge renders on `visible` rather than on `failing`.
 */
import { ops, can } from "$lib/api.js";

const RIGHT = "store.operate";

/*
 * The sweepers' own ticker. A fresher answer would report on work that has not
 * happened yet — "1 order awaiting the sweeper" thirty seconds after checkout
 * is not a finding, it is the engine being on time.
 */
const INTERVAL = 5 * 60 * 1000;

const state = $state({ report: null, loading: false, error: null });

function clear() {
    state.report = null;
    state.error = null;
}

export const health = {
    get report() {
        return state.report;
    },
    get checks() {
        return state.report?.checks ?? [];
    },
    get loading() {
        return state.loading;
    },
    get error() {
        return state.error;
    },
    /*
     * Unknown counts as well: a badge about a store nobody has asked about is a
     * false alarm, and an unreachable store already announces itself by every
     * other screen failing.
     */
    get failing() {
        return state.report ? !state.report.ok : false;
    },
    get warnings() {
        return this.checks.filter((c) => c.status === "warn").length;
    },
    /*
     * What every badge renders on. Gating on the verdict alone would show an
     * operator a red dot about a store they may not be told about, on a section
     * whose Diagnostics link they cannot see.
     */
    get visible() {
        return can(RIGHT) && this.failing;
    },

    async refresh() {
        if (!can(RIGHT)) {
            clear();
            return null;
        }
        state.loading = true;
        try {
            state.report = await ops.diagnostics();
            state.error = null;
        } catch (err) {
            state.error = err;
            state.report = null;
        } finally {
            state.loading = false;
        }
        return state.report;
    },

    /**
     * watch polls until the returned teardown runs. Svelte calls that on sign-out,
     * which is what stops the poll and clears the verdict with the session.
     */
    watch() {
        if (!can(RIGHT)) {
            clear();
            return () => {};
        }
        this.refresh();
        const id = setInterval(() => this.refresh(), INTERVAL);
        return () => {
            clearInterval(id);
            clear();
        };
    },
};
