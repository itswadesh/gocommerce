/*
 * The Banners screen (D99) shows each storefront page as it reads — its
 * slides, then its grids under their headings — and moves a banner one place
 * at a time within the page, sending the page's whole order.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { groupBanners, movedOrder, pageLabel } from "../src/lib/banners.js";

const b = (id, page, placement, group_title = "", position = id) => ({ id, page, placement, group_title, position });

test("banners group by page, slides first, then each grid in the order its first tile appears", () => {
    const pages = groupBanners([
        b(1, "home", "slide"),
        b(2, "home", "grid", "Shop by category"),
        b(3, "home", "slide"),
        b(4, "home", "grid", "Gifts"),
        b(5, "home", "grid", "Shop by category"),
        b(6, "about", "slide"),
    ]);
    assert.deepEqual(
        pages.map((p) => p.page),
        ["home", "about"],
    );
    const home = pages[0];
    assert.deepEqual(
        home.slides.map((x) => x.id),
        [1, 3],
    );
    assert.deepEqual(
        home.grids.map((g) => [g.title, g.banners.map((x) => x.id)]),
        [
            ["Shop by category", [2, 5]],
            ["Gifts", [4]],
        ],
    );
});

test("the home page leads, whatever was added first", () => {
    const pages = groupBanners([b(1, "about", "slide"), b(2, "home", "slide")]);
    assert.deepEqual(
        pages.map((p) => p.page),
        ["home", "about"],
    );
});

test("moving a banner swaps it with its neighbour of the same kind, and the page's whole order is sent", () => {
    const all = [b(1, "home", "slide"), b(2, "home", "grid", "Tiles"), b(3, "home", "slide"), b(9, "about", "slide")];
    // Slide 3 up passes over the grid tile and lands before slide 1.
    assert.deepEqual(movedOrder(all, 3, -1), [3, 2, 1]);
    // Already first among the slides: nothing to send.
    assert.equal(movedOrder(all, 1, -1), null);
    // A grid tile alone in its grid cannot move.
    assert.equal(movedOrder(all, 2, 1), null);
});

test("a page is named the way a merchant thinks of it", () => {
    assert.equal(pageLabel("home"), "Home page");
    assert.equal(pageLabel("about"), "Page: about");
});
