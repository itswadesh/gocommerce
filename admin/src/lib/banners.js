/*
 * The Banners screen's arithmetic (D99): each storefront page as it reads, and
 * a banner moved one place within its kind.
 */

const byPosition = (a, b) => a.position - b.position || a.id - b.id;

/**
 * groupBanners turns the engine's flat list into pages — the home page
 * first, then the rest in the order they first appear — each with its slides
 * and its grids, a grid being every tile under one heading, in the order its
 * first tile appears: the order the storefront shows them in.
 */
export function groupBanners(list) {
    const pages = [];
    const at = new Map();
    for (const banner of [...list].sort(byPosition)) {
        if (!at.has(banner.page)) {
            at.set(banner.page, pages.length);
            pages.push({ page: banner.page, slides: [], grids: [], all: [] });
        }
        const page = pages[at.get(banner.page)];
        page.all.push(banner);
        if (banner.placement === "grid") {
            let grid = page.grids.find((g) => g.title === banner.group_title);
            if (!grid) {
                grid = { title: banner.group_title, banners: [] };
                page.grids.push(grid);
            }
            grid.banners.push(banner);
        } else {
            page.slides.push(banner);
        }
    }
    const home = pages.findIndex((p) => p.page === "home");
    if (home > 0) pages.unshift(...pages.splice(home, 1));
    return pages;
}

/**
 * movedOrder is the page's whole order, by id, after banner id moves one
 * place up (dir -1) or down (+1) among the banners of its kind — the slides,
 * or the tiles of its grid — or null when it is already at that end.
 */
export function movedOrder(list, id, dir) {
    const banner = list.find((b) => b.id === id);
    if (!banner) return null;
    const page = list.filter((b) => b.page === banner.page).sort(byPosition);
    const kin = page.filter(
        (b) =>
            b.placement === banner.placement &&
            (banner.placement !== "grid" || b.group_title === banner.group_title),
    );
    const i = kin.findIndex((b) => b.id === id);
    const other = kin[i + dir];
    if (!other) return null;
    const ids = page.map((b) => b.id);
    const a = ids.indexOf(id);
    const z = ids.indexOf(other.id);
    [ids[a], ids[z]] = [ids[z], ids[a]];
    return ids;
}

/** pageLabel names a storefront page the way a merchant thinks of it. */
export function pageLabel(page) {
    return page === "home" ? "Home page" : `Page: ${page}`;
}
