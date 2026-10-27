/*
 * Plugins are add-ons; built-in features live on their own screens (D132).
 *
 * The engine names each built-in feature's screen (`home` on the plugin), and
 * the Plugins screen leaves those out. Each of those screens must then carry
 * the feature's switch and settings, or a store that had one off would have no
 * way left to switch it on. Banners moved from Content to Themes, with its old
 * address redirecting.
 *
 * node:test cannot import a .svelte file, so these read the source, as
 * accounts.test.js does.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { existsSync, readFileSync } from "node:fs";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const blank = (m) => m.replace(/[^\n]/g, " ");
const code = (src) =>
    src.replace(/<!--[\s\S]*?-->/g, blank).replace(/\/\*[\s\S]*?\*\//g, blank).replace(/\/\/.*$/gm, blank);

test("the Plugins screen lists only the plugins with no home of their own", () => {
    const screen = code(read("../src/routes/dash/plugins/+page.svelte"));
    assert.match(screen, /filter\(\(p\) => !p\.home\)/);
    assert.ok(!screen.includes("homeOf"), "the panel's own map of homes is gone; the engine says");
    assert.ok(!existsSync(new URL("../src/lib/plugin-homes.js", import.meta.url)));
});

// Every home the engine names (ext/alltest/plugins_home_test.go), and the
// plugin each screen switches. Screens that already switched their feature
// before D132 (Telegram, Sitemap, Setup Email/SMS, the provider screens) are
// not listed.
const SWITCHES = {
    "themes/banners": ["banners"],
    "themes/extras": ["hello-bar", "news-ticker", "trust-badges", "social-sharing"],
    themes: ["layouts"],
    menus: ["navigation"],
    faq: ["faq"],
    showcases: ["showcases"],
    contact: ["contact-form"],
    newsletter: ["newsletter"],
    reviews: ["product-reviews"],
    wishlists: ["wishlist"],
    settings: ["identity", "guest-checkout", "store-close", "enquiry-mode"],
};

for (const [route, keys] of Object.entries(SWITCHES)) {
    test(`/dash/${route} carries the switch for ${keys.join(", ")}`, () => {
        const screen = code(read(`../src/routes/dash/${route}/+page.svelte`));
        for (const key of keys) {
            const one = new RegExp(`<FeatureSwitch\\s[^>]*pluginKey="${key}"`);
            const listed = screen.includes(`"${key}"`) && /<FeatureSwitch\s[^>]*pluginKey=\{key\}/.test(screen);
            assert.ok(one.test(screen) || listed, `${key} has no switch on /dash/${route}`);
        }
    });
}

test("Banners is under Themes, and its old address redirects there", () => {
    const nav = code(read("../src/lib/nav.js"));
    assert.ok(!nav.includes('"/dash/banners"'), "Content still lists Banners");
    const themes = nav.match(/\{[^{}]*href:\s*"\/dash\/themes"[^{}]*\}/)?.[0] ?? "";
    assert.match(themes, /anyRight:\s*\[[^\]]*"banners\.read"/, "Themes is reachable by whoever may read banners");

    const list = code(read("../src/routes/dash/themes/+page.svelte"));
    assert.match(list, /href="\{base\}\/dash\/themes\/banners"/);
    assert.match(list, /href="\{base\}\/dash\/themes\/extras"/);

    const redirect = code(read("../src/routes/dash/banners/+page.js"));
    assert.match(redirect, /redirect\(308, `\$\{base\}\/dash\/themes\/banners\$\{url\.search\}`\)/);
    assert.ok(!existsSync(new URL("../src/routes/dash/banners/+page.svelte", import.meta.url)));
});

test("a feature with no switch shows none, unless the store had it off", () => {
    const card = code(read("../src/lib/components/FeatureSwitch.svelte"));
    assert.match(card, /showSwitch = \$derived\(!!plugin && \(!plugin\.always_on \|\| !plugin\.enabled\)\)/);
});

test("nothing links to the Plugins screen for a feature that left it", () => {
    for (const path of ["../src/lib/components/CustomerSignInCard.svelte"]) {
        assert.ok(!code(read(path)).includes("/dash/plugins"), path);
    }
});
