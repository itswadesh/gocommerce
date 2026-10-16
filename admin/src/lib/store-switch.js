/**
 * Where switching store lands (D83): the same section, in the new store.
 *
 * The rule is "keep the section, drop anything that names a row of the old
 * store". It is read off the SvelteKit route id rather than a list of pages,
 * so a screen added later is covered the day it ships: the address is cut
 * before the first dynamic segment, since `[id]` and its kind name a row, and
 * a list keeps its filters but not its place in the old store's pages.
 *
 * Pure, so it is tested under plain node; the shell supplies the route and
 * the nav.
 */

/** Paging parameters point into the old store's list; filters do not. */
const PAGING = new Set(["page", "cursor", "after", "before"]);

/**
 * Dynamic segments that name a screen rather than a row. A plugin's screen is
 * the same plugin in every store, so /dash/x/agent stays /dash/x/agent.
 */
const NAMES_A_SCREEN = new Set(["/dash/x/[slug]"]);

const isGroup = (seg) => seg.startsWith("(") && seg.endsWith(")");

/**
 * switchTarget maps the page a person is on to the one they land on.
 *
 * @param {string | null} routeId  page.route.id
 * @param {URL} url                page.url
 * @returns {{ href: string, dropped: string | null }} the address, and the
 *   row it no longer shows (the URL's value at the cut), if any
 */
export function switchTarget(routeId, url) {
    if (!routeId) return { href: "/dash", dropped: null };
    const route = routeId.split("/").filter((seg) => seg && !isGroup(seg));
    const parts = url.pathname.split("/").filter(Boolean);
    const kept = [];
    for (let i = 0; i < route.length; i++) {
        const seg = route[i];
        if (seg.startsWith("[") && !NAMES_A_SCREEN.has("/" + [...route.slice(0, i), seg].join("/"))) {
            return { href: kept.length ? `/${kept.join("/")}` : "/dash", dropped: parts[i] ?? null };
        }
        kept.push(parts[i]);
    }
    const params = new URLSearchParams(url.search);
    for (const key of [...params.keys()]) {
        if (PAGING.has(key)) params.delete(key);
    }
    const search = params.toString();
    const path = `/${kept.join("/")}`;
    return { href: search ? `${path}?${search}` : path, dropped: null };
}

function* entries(items) {
    for (const item of items) {
        yield item;
        if (item.children) yield* entries(item.children);
    }
}

/**
 * sectionFor is the nav entry a path sits under: the longest href that is the
 * path or a whole-segment prefix of it. Its right and module are what the new
 * store must grant for the page to be worth landing on.
 */
export function sectionFor(path, items) {
    let best = null;
    for (const item of entries(items)) {
        const href = item.href;
        if (path !== href && !path.startsWith(href + "/")) continue;
        if (!best || href.length > best.href.length) best = item;
    }
    return best;
}

/** droppedNote is "Order 1234 belongs to Store A". */
export function droppedNote(section, dropped, storeName) {
    if (!section) return `${dropped} belongs to ${storeName}`;
    const one = section.label.replace(/ies$/, "y").replace(/s$/, "");
    return `${one} ${dropped} belongs to ${storeName}`;
}
