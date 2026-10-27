/**
 * The webhook event picker's arithmetic. The engine lists the events it sends
 * (GET /api/admin/x/webhooks/events); the panel groups them for ticking and
 * turns the ticks back into what an endpoint stores.
 */

/** Events in the engine's order, grouped by their `group`, groups in first-seen order. */
export function groupEvents(catalog) {
    const groups = [];
    const byName = new Map();
    for (const e of catalog ?? []) {
        let g = byName.get(e.group);
        if (!g) {
            g = { group: e.group, events: [] };
            byName.set(e.group, g);
            groups.push(g);
        }
        g.events.push(e);
    }
    return groups;
}

/**
 * What an endpoint stores. "Everything" is `*`, which also takes events added
 * to the engine later; otherwise the exact names ticked, in the catalogue's
 * order, so the list reads the same way the picker does.
 */
export function eventsToSend({ all, picked }, catalog) {
    if (all) return ["*"];
    const want = new Set(picked);
    return (catalog ?? []).map((e) => e.name).filter((n) => want.has(n));
}

/** Ticks every event of a group, or clears them when they are all ticked already. */
export function toggleGroup(picked, group) {
    const names = group.events.map((e) => e.name);
    const set = new Set(picked);
    const allOn = names.every((n) => set.has(n));
    for (const n of names) allOn ? set.delete(n) : set.add(n);
    return [...set];
}
