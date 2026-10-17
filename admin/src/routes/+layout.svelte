<script>
    /**
     * PocketBase's shell: an accent header carrying the primary navigation,
     * over a rounded surface that each page fills with its own `.page`.
     *
     * The nav is horizontal and lives in the header — that is PocketBase's
     * model, and the left `.page-sidebar` is reserved for a section's own
     * sub-navigation (Settings uses one). Pages own their `.page` wrapper, so
     * a page that wants a sidebar simply renders one.
     */
    import "../app.css";
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { goto } from "$app/navigation";
    import { tick, untrack } from "svelte";
    import { auth, events, can, session, stores, errors as errorReports } from "$lib/api.js";
    import { installErrorReporting } from "$lib/error-reporting.js";
    import { clearProfile, clearSettings } from "$lib/settings.svelte.js";
    import { forgetModules, hasModule } from "$lib/modules.svelte.js";
    import { forgetScreens } from "$lib/screens.svelte.js";
    import { applyEntry, ensureStore, forgetStores, isSuperseded, markStores, myStores } from "$lib/stores.svelte.js";
    import { reloadStoreData } from "$lib/store-data.js";
    import { forgetSetup, loadSetup, setup } from "$lib/setup.svelte.js";
    import { copyText } from "$lib/clipboard.js";
    import { droppedNote, sectionFor, switchTarget } from "$lib/store-switch.js";
    import { health } from "$lib/health.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { NAV, visibleNav as allowedNav } from "$lib/nav.js";
    import { safeNext } from "$lib/paths.js";
    import { findSaveTarget, isBareKey, isTypingTarget, modalIsOpen, NEW_EVENT } from "$lib/shortcuts.js";
    import Toasts from "$lib/components/Toasts.svelte";
    import Login from "$lib/components/Login.svelte";
    import CommandPalette from "$lib/components/CommandPalette.svelte";
    import ShortcutHelp from "$lib/components/ShortcutHelp.svelte";

    let { children } = $props();

    let ready = $state(false);
    /*
     * Both read the session rune rather than a copy of it, so signing in,
     * signing out and a re-cut role all reach the shell on their own. The
     * copies were the reason two screens reloaded the document to update the
     * nav: `can()` was answering from a value nothing was watching.
     */
    const record = $derived(session.record);
    const authenticated = $derived(session.authenticated);

    /*
     * The one screen reachable without an account: an invitee has no
     * credentials yet, which is the entire point of the link they followed.
     * Holding the token is what authorises them, and the engine checks it.
     *
     * Listed by prefix rather than gated inside the page, because the shell is
     * what decides whether a login form appears — a page cannot opt out of a
     * layout that has already replaced it.
     *
     * The trade portal is the other (D77): the store's business buyers, who
     * sign in with a shopper account and have no staff credential to give.
     * Its own layout decides between its sign-in and its screens.
     *
     * The invitation, password-reset and sign-in screens all sit under
     * /admin/auth, where the KitCommerce admin keeps its own: every screen a
     * person reaches before they have a session, at one prefix.
     */
    const PUBLIC_PREFIXES = ["/admin/auth", "/portal"];
    const LOGIN = "/admin/auth/login";
    const onLogin = $derived(page.url.pathname.replace(base, "") === LOGIN);
    /* Signed in, but in no store yet: no sidebar, because every link in it
       would ask a store that has not been chosen. */
    const SELECT = "/select-store";
    const onSelect = $derived(page.url.pathname.replace(base, "") === SELECT);
    const isPublic = $derived(
        PUBLIC_PREFIXES.some((prefix) =>
            page.url.pathname.replace(base, "").startsWith(prefix),
        ),
    );

    /*
     * The nav list itself moved to $lib/nav.js. It did not move for tidiness:
     * the command palette offers the same destinations, and two copies of that
     * list is how one of them comes to be missing a screen that shipped months
     * ago. Everything that used to be written here — why each item names a
     * right, why some name a module, what `accent` is — is written there,
     * beside the data it describes.
     */
    const visibleNav = $derived(allowedNav(NAV));

    /*
     * The drawer, for widths where the sidebar cannot stay open. It closes on
     * navigation, on Escape and on a click outside — the same three ways B2B
     * Leads closes its own, minus the edge-swipe, which this panel has no touch
     * gestures anywhere else to be consistent with.
     */
    let menuOpen = $state(false);

    $effect(() => {
        // Reading the path is what subscribes this to navigation.
        page.url.pathname;
        menuOpen = false;
    });

    /*
     * The panel's keyboard layer, and the only one there is.
     *
     * It lives in the shell because every one of these is global: a shortcut
     * that works on the products screen and nowhere else is worse than none,
     * since the operator has to remember which screen they are on before they
     * can use it. What "new" MEANS is still the screen's — the shell fires an
     * event and a list screen with a create form answers it — so there is no
     * table here of which route creates what, waiting to fall out of date.
     *
     * The single-key shortcuts are all guarded twice: not while something is
     * being typed into, and not while a modal owns the keyboard. Without the
     * first, `n` eats a letter out of every form in the panel.
     */
    let paletteOpen = $state(false);
    let helpOpen = $state(false);

    /* Which glyph the trigger prints. `navigator.platform` is deprecated and
       still the only thing that answers without a permissions prompt; being
       wrong shows the wrong hint rather than breaking anything, since both
       modifiers are accepted above. */
    const appleKeys =
        typeof navigator !== "undefined" &&
        /mac|iphone|ipad/i.test(navigator.userAgentData?.platform || navigator.platform || "");

    $effect(() => {
        const onKey = (event) => {
            if (event.key === "Escape") {
                menuOpen = false;
                return;
            }
            // The one shortcut that works from inside a field, because a search
            // box is exactly where an operator realises they want a different
            // record and the whole point is not having to reach for the mouse.
            if ((event.ctrlKey || event.metaKey) && !event.altKey && event.key.toLowerCase() === "k") {
                event.preventDefault();
                paletteOpen = true;
                return;
            }
            // Save, from inside any field: that is where the operator is when
            // they want it. A showing save bar binds its own (SaveBar), so the
            // shell steps aside for it; anywhere else it presses the Save of
            // the form being worked in. The browser's "save page as" is never
            // what somebody in the panel meant, so it is prevented either way.
            if ((event.ctrlKey || event.metaKey) && !event.altKey && !event.shiftKey && event.key.toLowerCase() === "s") {
                if (document.querySelector(".save-bar:not([hidden])")) return;
                event.preventDefault();
                const target = findSaveTarget();
                if (!target) return;
                target.button.classList.add("shortcut-pressed");
                setTimeout(() => target.button.classList.remove("shortcut-pressed"), 250);
                if (target.form) target.form.requestSubmit(target.button);
                else target.button.click();
                return;
            }
            if (!isBareKey(event) || isTypingTarget(event.target) || modalIsOpen()) return;
            if (event.key === "/") {
                event.preventDefault();
                paletteOpen = true;
            } else if (event.key === "?") {
                event.preventDefault();
                helpOpen = true;
            } else if (event.key === "n") {
                // Not preventDefault: nothing in a browser owns a bare `n`, and
                // a screen with nothing to create must be left able to ignore
                // it entirely.
                window.dispatchEvent(new CustomEvent(NEW_EVENT));
            }
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    });

    /** Two letters for the avatar, from the email — there is no name to use. */
    const initials = $derived(
        (record?.email || "at")
            .replace(/@.*$/, "")
            .split(/[._-]/)
            .filter(Boolean)
            .slice(0, 2)
            .map((part) => part[0].toUpperCase())
            .join("")
            // A single-word address gives one letter, and a lone initial in a
            // 28px disc reads as a mistake; take two from the word instead.
            .padEnd(2, (record?.email || "at")[1]?.toUpperCase() ?? "")
            .slice(0, 2) || "AT",
    );

    /* A static admin token has no role, and saying so is more useful than
       leaving the line blank. */
    const roleLabel = $derived(
        record?.role ? record.role[0].toUpperCase() + record.role.slice(1) : "Admin token",
    );

    function isActive(item) {
        const path = page.url.pathname.replace(base, "") || "/";
        return item.exact ? path === item.href : path.startsWith(item.href);
    }

    /* A section unfolds while it or any of its screens is open. Its own row
       lights up only when it is the screen — when a child is, the child does,
       and two highlighted rows would say two places at once. The longest
       matching child wins, so /notifications/email lights Setup Email and
       not the section, and /shipping/providers lights Providers and not
       Shipping and delivery. */
    function childrenOf(item) {
        return item.children ? allowedNav(item.children) : [];
    }
    function activeChild(item) {
        const kids = childrenOf(item).filter(isActive);
        return kids.sort((a, b) => b.href.length - a.href.length)[0] ?? null;
    }
    function sectionOpen(item) {
        return isActive(item) || childrenOf(item).some(isActive);
    }

    /*
     * An open section brings its own screens into view.
     *
     * Settings unfolds sixteen of them, which is taller than the rail on a
     * laptop, so the last — Import / export — sat below the fold of a list
     * that scrolls, and an operator who does not think to scroll a sidebar
     * reads that as "it is not there". The whole group is scrolled into view
     * when it opens, by the smallest amount that works, so nothing moves when
     * the group already fits.
     */
    function revealSection(node) {
        const reveal = () => {
            const nav = node.closest(".app-sidebar-nav, .app-drawer");
            if (!nav) return;
            const below = node.getBoundingClientRect().bottom - nav.getBoundingClientRect().bottom;
            if (below > 0) nav.scrollTop += below + 8;
        };
        // Watched rather than measured once: the group grows after it mounts,
        // because the screens a module contributes arrive from the API a
        // moment later, and a single frame's measurement would scroll by what
        // was needed then and leave the newest rows below the fold.
        const observer = new ResizeObserver(reveal);
        observer.observe(node);
        return { destroy: () => observer.disconnect() };
    }

    $effect(() => {
        ready = true;
    });

    /*
     * Which store this tab is in is settled before anything that needs one is
     * asked (D83): a person in several stores has no store until they choose
     * one, and every store-bound request made before that is a 400. It runs on
     * every way into a session — a stored token, the login form, an accepted
     * invitation — because each ends with `authenticated` turning true.
     */
    $effect(() => {
        if (!ready || !authenticated || myStores.state !== "unknown") return;
        untrack(boot);
    });

    async function boot() {
        markStores("pending");
        let where;
        try {
            // Also the stored token's check: a 401 here has already ended the
            // session in api.js, before the operator meant to do anything.
            where = await ensureStore();
        } catch (err) {
            // Signed out while asking: the answer was the last person's.
            if (isSuperseded(err)) return;
            if (err.isAuth) {
                // The cached settings would be the previous operator's answer.
                clearSettings();
                clearProfile();
                return;
            }
            // A store that cannot be reached is not a credential that has
            // gone bad. Clearing the session here signed out an operator
            // whose token was still perfectly good.
            markStores("error");
            toast.error("Could not reach the store — you are still signed in.");
            return;
        }
        if (where !== "ok") return; // the redirect below sends them to choose
        // A stored record is a claim; the refresh turns it into this store's
        // answer before the module probes ask what it may see.
        await auth.refresh().catch(() => {});
        reloadStoreData();
    }

    /*
     * The setup guide, asked again on every navigation and every switch:
     * a step is finished on its own screen, and the sidebar's progress link
     * and Home's checklist should have moved on by the time anyone looks.
     */
    $effect(() => {
        page.url.pathname;
        storeEpoch;
        if (!authenticated || myStores.state !== "ok") {
            untrack(forgetSetup);
            return;
        }
        untrack(loadSetup);
    });

    /*
     * The health report, for the badge. Svelte runs the returned teardown when
     * `authenticated` goes false, which both stops the poll and clears the
     * verdict with the session — the report is module-scope state and would
     * otherwise greet the next person to use this browser.
     *
     * It is silent by design: watch() never toasts, because this runs on every
     * page and a store whose database has gone away must not say so every five
     * minutes. The Diagnostics screen is where failures are read.
     */
    /*
     * The panel's own crashes go to the store's error page (D86), so a screen
     * that throws in somebody's browser is a bug the owner hears about. Only
     * with a store chosen: an error is a store's, and before one is chosen
     * the report would be a 400.
     */
    $effect(() => {
        if (!authenticated || myStores.state !== "ok" || !myStores.current) return;
        return installErrorReporting((body) => errorReports.report(body));
    });

    $effect(() => {
        // Diagnostics are a store's: asked before one is chosen, they are 400s.
        // Reading the store restarts the watch on a switch, so the badge
        // reports the store on screen.
        if (!authenticated || myStores.state !== "ok" || !myStores.current) return;
        return health.watch();
    });

    /*
     * The dead-letter count, for the same badge.
     *
     * It is on the same indicator rather than beside it, and that is the whole
     * point: the health dot renders on `health.failing`, which is FAIL only, and
     * a dead-lettered event is a WARN — so the report never lights it, and the
     * one thing on these screens an operator can actually repair would be the
     * one thing nothing tells them about. Two alarms on a 13px row is worse than
     * one that knows which it means, so a failing check outranks a parked event
     * and the title says which: a store that cannot reach its database has a
     * bigger problem than one notification that did not go out.
     *
     * Read on navigation, not polled. The outbox is minutes-scale, a poll from
     * every open tab is a cost with no reader, and the count is one indexed
     * read of `meta.total` against outbox_dead_idx.
     */
    let deadLetters = $state(0);

    $effect(() => {
        // Reading the path is what subscribes this to navigation, and the
        // epoch to a switch that stays on the same address.
        page.url.pathname;
        storeEpoch;
        if (!authenticated || myStores.state !== "ok" || !can("store.operate")) {
            deadLetters = 0;
            return;
        }
        let live = true;
        events
            .list({ state: "dead", limit: 1 })
            .then((result) => {
                if (live) deadLetters = result.meta?.total ?? 0;
            })
            .catch(() => {
                // Silent, for health.watch()'s reason: this runs on every page,
                // and a store that has gone away already announces itself by
                // every other screen failing.
                if (live) deadLetters = 0;
            });
        return () => {
            live = false;
        };
    });

    const navAlert = $derived(
        health.visible
            ? { show: true, href: "/dash/settings/diagnostics", label: "A health check is failing" }
            : deadLetters > 0
              ? {
                    show: true,
                    href: "/dash/settings/events",
                    label: `${deadLetters} event${deadLetters === 1 ? "" : "s"} could not be delivered`,
                }
              : { show: false, href: "/dash/settings", label: "" },
    );

    /*
     * Sign-in has an address of its own, /admin/auth/login, as the KitCommerce
     * admin's does. A signed-out visit to a store screen goes there carrying
     * the screen in `next`; once a session exists the login address sends the
     * visitor on to it. Both directions live here, beside the session they
     * read, so no screen has to know it can be reached signed out.
     */
    $effect(() => {
        if (!ready || isPublic) return;
        if (!authenticated) {
            const here = page.url.pathname.replace(base, "") + page.url.search;
            // The fragment rides along: kitcommerce.store's demo button opens
            // /#email=…&password=…, and the login form fills itself from it.
            goto(`${base}${LOGIN}?next=${encodeURIComponent(here)}${page.url.hash}`, { replaceState: true });
        }
    });
    $effect(() => {
        if (!ready || !authenticated || !onLogin) return;
        const state = myStores.state;
        const next = safeNext(page.url.searchParams.get("next"));
        if (state === "ok") {
            goto(base + next, { replaceState: true });
        } else if (state === "choose" || state === "none") {
            goto(`${base}${SELECT}?next=${encodeURIComponent(next)}`, { replaceState: true });
        } else if (state === "error") {
            // Off the login page, where a signed-in person draws nothing, to
            // the shell's "could not reach the store" and its Try again.
            goto(base + next, { replaceState: true });
        }
    });
    $effect(() => {
        if (!ready || !authenticated || isPublic || onSelect) return;
        const state = myStores.state;
        if (state === "choose" || state === "none") {
            const here = page.url.pathname.replace(base, "") + page.url.search;
            goto(`${base}${SELECT}?next=${encodeURIComponent(here)}`, { replaceState: true });
        }
    });
    // However the session ended — signing out, or a 401 — the next one is
    // somebody's whose stores have yet to be asked for.
    $effect(() => {
        if (!authenticated) untrack(forgetStores);
    });

    function onAuthenticated() {
        // auth.login has already written the session, and the boot effect
        // takes it from there: the store first, then everything cached.
        toast.success("Signed in");
    }

    /*
     * Switching store keeps the page (D83): the same section in the new
     * store, minus anything that named a row of the old one. The mapping is
     * store-switch.js's; this is the sequence around it — enter, drop what
     * was cached, then move, under a 200 ms cross-fade.
     */
    let switching = $state("");
    let switchError = $state("");
    // Bumped as the page changes, so a switch that lands on the same address
    // still mounts the screen afresh and asks the new store for its rows.
    let storeEpoch = $state(0);
    // True while a switch has the old store's page down and the new one is
    // not up yet.
    let parked = $state(false);

    async function switchStore(target) {
        const from = myStores.current;
        if (switching || target.id === from?.id) return;
        switching = target.id;
        switchError = "";
        let entry;
        try {
            // Asked first and applied later: until the answer is in, the tab
            // is still wholly in the store it was in.
            entry = await stores.enter(target.id);
        } catch (err) {
            // The menu stays open with the reason in it, and nothing else
            // changes.
            switchError = err?.message || "Could not switch store.";
            switching = "";
            return;
        }
        for (const el of document.querySelectorAll(".logged-user-dropdown")) {
            if (el.matches(":popover-open")) el.hidePopover();
        }
        // Read before the page goes: where we are is what decides where to go.
        const target0 = switchTarget(page.route.id, page.url);
        let note = "";

        /*
         * The old store's page comes down before the store changes. Left up,
         * it reacts to the new store — new rights, new settings — by asking
         * the new store for the old store's rows, and says "not found" on
         * the way out. Under a view transition the person sees none of this:
         * the old page holds as a snapshot until the new one is ready.
         */
        const move = async () => {
            parked = true;
            try {
                await tick();
                applyEntry(entry);
                await reloadStoreData();
                let href = target0.href;
                const section = sectionFor(href.split("?")[0], NAV);
                if (target0.dropped) note = droppedNote(section, target0.dropped, from?.name ?? "the other store");
                if (section && section.href !== "/dash") {
                    if (section.right && !can(section.right)) {
                        note = `You don't have access to ${section.label} in ${target.name}`;
                        href = "/dash";
                    } else if (section.module && !hasModule(section.module)) {
                        note = `${section.label} is not available in ${target.name}`;
                        href = "/dash";
                    }
                }
                await goto(base + href);
                toast.info(note || `Switched to ${target.name}`);
            } catch (err) {
                // The store has changed by now (or the caches failed to
                // reload); whatever page is under the person stays usable.
                toast.error(err?.message || "The store changed, but the page could not be opened.");
            } finally {
                // Never left down: a parked page is a blank screen.
                storeEpoch++;
                parked = false;
                switching = "";
                await tick();
            }
        };
        const still = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches;
        if (typeof document.startViewTransition === "function" && !still) {
            const root = document.documentElement;
            root.classList.add("store-vt");
            const transition = document.startViewTransition(move);
            transition.finished.finally(() => root.classList.remove("store-vt"));
            await transition.updateCallbackDone.catch(() => {});
        } else {
            await move();
        }
    }

    /* The store list in the account menu, searchable once it outgrows a
       glance: past seven stores. */
    let storeQuery = $state("");
    const storeList = $derived.by(() => {
        const q = storeQuery.trim().toLowerCase();
        if (!q) return myStores.list;
        return myStores.list.filter((st) => st.name.toLowerCase().includes(q) || st.slug.includes(q));
    });
    const roleName = (role) => (role ? role[0].toUpperCase() + role.slice(1) : "");

    /* The check stands in for the copy icon for a moment, so the press is
       answered where the eye already is; the toast says it too. */
    let copied = $state("");
    async function copyStoreId(id) {
        if (await copyText(id)) {
            copied = id;
            toast.success("Store ID copied");
            setTimeout(() => {
                if (copied === id) copied = "";
            }, 1500);
        } else {
            toast.error("Could not copy. Select the ID and copy it yourself: " + id);
        }
    }

    /**
     * Arrow keys walk the account menu; Enter and Escape are the browser's
     * own for buttons and popovers. Opening it puts focus on the current
     * store, so the keyboard starts where the person is.
     */
    function menuKeys(node) {
        const items = () => [...node.querySelectorAll(".store-current-btn, .dropdown-item:not([disabled]), .store-menu-search")];
        const onKey = (event) => {
            if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
            event.preventDefault();
            const list = items();
            const at = list.indexOf(document.activeElement);
            const step = event.key === "ArrowDown" ? 1 : -1;
            list[(at + step + list.length) % list.length]?.focus();
        };
        const onToggle = (event) => {
            if (event.newState !== "open") {
                switchError = "";
                storeQuery = "";
                return;
            }
            (node.querySelector(".store-menu-item.current") ?? items()[0])?.focus();
        };
        node.addEventListener("keydown", onKey);
        node.addEventListener("toggle", onToggle);
        return {
            destroy() {
                node.removeEventListener("keydown", onKey);
                node.removeEventListener("toggle", onToggle);
            },
        };
    }

    async function signOut() {
        for (const el of document.querySelectorAll(".logged-user-dropdown")) {
            if (el.matches(":popover-open")) el.hidePopover();
        }
        // auth.logout is what clears the credential. The settings go with it:
        // the next operator on a shared browser may be looking at another store.
        await auth.logout();
        clearSettings();
        forgetModules();
        forgetScreens();
        forgetStores();
        toast.info("Signed out");
    }

    /**
     * The header link's pressed styling keys off `data-popover-state`, because
     * the button and the popover it opens are siblings and CSS has no way to
     * ask "is my popover showing?".
     */
    function trackPopover(node) {
        const target = document.getElementById(node.getAttribute("popovertarget"));
        if (!target) return;
        const sync = () => node.setAttribute("data-popover-state", target.matches(":popover-open"));
        target.addEventListener("toggle", sync);
        return { destroy: () => target.removeEventListener("toggle", sync) };
    }
</script>

<svelte:head>
    <title>GoCommerce</title>
</svelte:head>

{#snippet brand()}
    <!--
        A glyph and a word, rather than a picture of both.

        The logo file bakes "GoCommerce" into the SVG as a <text> element, and
        an SVG loaded through <img> is an isolated document: it cannot reach the
        page's @font-face, so the wordmark asked for Inter and got whatever
        serif the renderer had. It also carried a hardcoded near-black fill,
        which is why the dark rail needed a brightness(0) invert(1) filter to
        make it visible at all, and it was scaled to 17px tall so the lettering
        came out smaller than the nav beneath it.

        Real text fixes all three at once: it is Inter because everything here
        is, it takes the theme's colour like any other text, and it sits at the
        size the stylesheet already had a class for. The mark is remixicon, the
        icon set every other glyph in this panel comes from.
    -->
    <a href="{base}/dash" class="app-brand" aria-label="GoCommerce">
        <!-- kitcommerce.store's mark, drawn as the site draws it: from parts,
             in currentColor, so it takes the theme like the text beside it. -->
        <svg class="app-brand-mark" viewBox="0 0 24 24" width="20" height="20" aria-hidden="true" focusable="false">
            <rect x="4" y="4" width="3.6" height="16" rx="1" />
            <path d="M9.6 12 15.4 4h4.4l-5.8 8z" />
            <path d="M9.6 12h4.4l5.8 8h-4.4z" />
        </svg>
        <span class="app-brand-name">GoCommerce</span>
    </a>
{/snippet}

{#snippet setupLink()}
    <!-- Until the store can sell: how far along, and one click to the next
         step. Gone the moment the required steps are done. -->
    {#if setup.loaded && !setup.requiredDone}
        <a href="{base}/dash" class="app-setup-link">
            <span class="app-setup-text">
                <span class="txt">Set up your store</span>
                <span class="app-setup-count">{setup.progress.done}/{setup.progress.total}</span>
            </span>
            <span class="app-setup-bar" aria-hidden="true">
                <span style="transform: scaleX({setup.progress.total ? setup.progress.done / setup.progress.total : 0})"></span>
            </span>
        </a>
    {/if}
{/snippet}

{#snippet searchTrigger()}
    <!-- The omnibox, as a button rather than a box. It opens the palette, which
         owns the real field: two inputs claiming to be "search" — one in the
         shell and one in every list header — is the ambiguity the palette
         exists to end. The keys are printed on it because a shortcut nobody can
         see is a shortcut for whoever wrote it. -->
    <button type="button" class="app-search" onclick={() => (paletteOpen = true)}>
        <i class="ri-search-line" aria-hidden="true"></i>
        <span class="txt">Search the store</span>
        <kbd>{appleKeys ? "⌘" : "Ctrl"} K</kbd>
    </button>
{/snippet}

{#snippet navLinks()}
    <nav class="app-sidebar-nav">
        {#each visibleNav as item (item.href)}
            {@const lit = activeChild(item)}
            <a
                href="{base}{item.href}"
                class="app-nav-link nav-{item.accent}"
                class:active={isActive(item) && !lit}
                class:open={sectionOpen(item)}
            >
                <i class={item.icon} aria-hidden="true"></i>
                <span class="txt">{item.label}</span>
                <!-- The dot rides the item, so it appears in the sidebar and in
                     the drawer, which render this same snippet. -->
                {#if item.health && navAlert.show}
                    <span class="app-nav-dot" title={navAlert.label}></span>
                {/if}
            </a>
            <!-- A section's own screens, shown only while the section is
                 open: the list stays one line per screen the rest of the
                 time, which is what keeps it readable at twenty items. -->
            {#if item.children && sectionOpen(item)}
                <div class="app-nav-sub" use:revealSection>
                    {#each childrenOf(item) as child (child.href)}
                        <a
                            href="{base}{child.href}"
                            class="app-nav-sublink"
                            class:active={lit?.href === child.href}
                        >
                            {child.label}
                            <!-- The dot rides the screen the failing check is
                                 actually on, not only the section above it. -->
                            {#if child.health && navAlert.show}
                                <span class="app-nav-dot" title={navAlert.label}></span>
                            {/if}
                        </a>
                    {/each}
                </div>
            {/if}
        {/each}
    </nav>
{/snippet}

{#snippet account(where)}
    <!-- The account block sits at the foot of the sidebar, as it does in B2B
         Leads: avatar, the store this tab is in, and who you are there. It is
         drawn twice — the sidebar and the phone drawer — so each copy's menu
         has its own id; one id for both opened the hidden copy on a phone. -->
    {@const menuId = `logged-user-dropdown-${where}`}
    {@const hide = () => document.getElementById(menuId)?.hidePopover()}
    <div class="app-account">
        <button
            type="button"
            class="app-account-trigger"
            popovertarget={menuId}
            use:trackPopover
        >
            <span class="app-avatar" aria-hidden="true">{initials}</span>
            <span class="app-account-who">
                <span class="app-account-store txt-ellipsis" data-store-name>{myStores.current?.name ?? "GoCommerce"}</span>
                <span class="app-account-name txt-ellipsis">{record?.email || "admin token"}</span>
                <span class="app-account-role txt-ellipsis">{roleLabel}</span>
            </span>
            <i class="ri-arrow-up-s-line" aria-hidden="true"></i>
        </button>
        <div id={menuId} class="dropdown sm nowrap logged-user-dropdown" popover="auto" use:menuKeys>
            <!-- The stores first: which one this tab is in is the question the
                 menu is most often opened to answer. The block on top is the
                 current one, as the KitCommerce admin has it: its name, where
                 it lives, and its id to copy for a script or a support ticket. -->
            {#if myStores.current}
                {@const here = myStores.current}
                <div class="store-current">
                    <div class="store-current-text">
                        <span class="store-current-label">Store</span>
                        <span class="txt txt-ellipsis store-current-name">
                            {here.name}
                            {#if here.demo}<span class="store-demo-tag" title="Personal data is masked in this store">Demo</span>{/if}
                        </span>
                        {#if here.domain}
                            <span class="txt-hint txt-ellipsis store-current-domain">{here.domain}</span>
                        {/if}
                    </div>
                    <div class="store-current-actions">
                        <button
                            type="button"
                            class="store-current-btn"
                            class:done={copied === here.id}
                            aria-label="Copy store ID"
                            title="Copy store ID"
                            onclick={() => copyStoreId(here.id)}
                        >
                            <i class={copied === here.id ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
                        </button>
                        {#if here.domain}
                            <a
                                class="store-current-btn"
                                href="https://{here.domain}"
                                target="_blank"
                                rel="noreferrer"
                                aria-label="Visit store"
                                title="Visit store"
                            >
                                <i class="ri-external-link-line" aria-hidden="true"></i>
                            </a>
                        {/if}
                    </div>
                </div>
            {/if}
            {#if myStores.list.length}
                <div class="store-menu" role="group" aria-label="Your stores">
                    {#if myStores.list.length > 7}
                        <input
                            type="search"
                            class="store-menu-search"
                            placeholder="Find a store"
                            aria-label="Find a store"
                            bind:value={storeQuery}
                        />
                    {/if}
                    <div class="store-menu-list">
                        {#each storeList as st (st.id)}
                            {@const current = st.id === myStores.current?.id}
                            <button
                                type="button"
                                class="dropdown-item store-menu-item"
                                class:current
                                class:busy={switching === st.id}
                                aria-current={current ? "true" : undefined}
                                disabled={st.status !== "active"}
                                title={st.status !== "active" ? `${st.name} is ${st.status}` : undefined}
                                onclick={() => switchStore(st)}
                            >
                                <span class="store-menu-mark" aria-hidden="true">{(st.name || st.slug)[0].toUpperCase()}</span>
                                <span class="store-menu-text">
                                    <span class="txt txt-ellipsis">{st.name}</span>
                                    <span class="store-menu-role">
                                        {roleName(st.role)}{#if st.demo}<span class="store-demo-tag">Demo</span>{/if}
                                    </span>
                                </span>
                                {#if switching === st.id}
                                    <span class="loader sm" aria-label="Switching"></span>
                                {:else if current}
                                    <i class="ri-check-line store-menu-check" aria-hidden="true"></i>
                                {/if}
                            </button>
                        {:else}
                            <p class="store-menu-empty txt-hint">No store matches “{storeQuery}”.</p>
                        {/each}
                    </div>
                    {#if switchError}
                        <p class="store-menu-error" role="alert">{switchError}</p>
                    {/if}
                    <a class="dropdown-item" href="{base}{SELECT}?create=1" onclick={hide}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">Create a store</span>
                    </a>
                    <a class="dropdown-item" href="{base}{SELECT}" onclick={hide}>
                        <i class="ri-store-2-line" aria-hidden="true"></i>
                        <span class="txt">All stores…</span>
                    </a>
                </div>
                <hr />
            {/if}
            <!-- Unconditional: changing your own password needs no right, and
                 this is the shortest way to it. -->
            <a class="dropdown-item" href="{base}/dash/settings/account" onclick={hide}>
                <i class="ri-user-settings-line" aria-hidden="true"></i>
                <span class="txt">Your account</span>
            </a>
            {#if can("team.read")}
                <a class="dropdown-item" href="{base}/dash/settings/teams" onclick={hide}>
                    <i class="ri-group-line" aria-hidden="true"></i>
                    <span class="txt">Manage the team</span>
                </a>
            {/if}
            <a class="dropdown-item" href="{base}/docs" target="_blank" rel="noreferrer" onclick={hide}>
                <i class="ri-code-s-slash-line" aria-hidden="true"></i>
                <span class="txt">API preview</span>
            </a>
            <hr />
            <button type="button" class="dropdown-item txt-danger" onclick={signOut}>
                <i class="ri-logout-circle-line" aria-hidden="true"></i>
                <span class="txt">Logout</span>
            </button>
        </div>
    </div>
{/snippet}

<main class="app">
    <!-- One boundary around the whole shell, not just around the children: the
         public branch below is the accept-invite screen, which is the first
         thing a new operator ever sees and has no nav, no toast history and no
         way back if it throws. +error.svelte cannot cover either half — there
         is no `load` in this panel, so it only ever fires for a missing route. -->
    <svelte:boundary>
        {#if onLogin}
            <!-- The notice says why the form is back when a credential stopped
                 resolving mid-session, rather than because nobody has signed
                 in yet. It is said inside the card, which brings its own
                 page (AuthShell). -->
            {#if ready && !authenticated}
                <Login
                    onauthenticated={onAuthenticated}
                    notice={session.expired ? "Your session ended. Sign in to continue." : ""}
                />
            {/if}
        {:else if isPublic}
            <!-- No header, no nav: there is nothing yet to navigate as. -->
            {@render children?.()}
        {:else if onSelect && ready && authenticated}
            <!-- Signed in, in no store yet: the store picker alone. -->
            {@render children?.()}
        {:else if ready && authenticated && myStores.state === "ok"}
            <!-- Desktop: the sidebar itself. -->
            <aside class="app-sidebar">
                {@render brand()}
                {@render searchTrigger()}
                {@render setupLink()}
                {@render navLinks()}
                <div class="app-sidebar-foot">{@render account("sidebar")}</div>
            </aside>

            <!-- Narrow: a bar with the toggle, and the sidebar as a drawer over the
                 page. Same markup either way — only where it sits changes. -->
            <div class="app-topbar">
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    aria-label="Toggle navigation"
                    aria-expanded={menuOpen}
                    onclick={() => (menuOpen = !menuOpen)}
                >
                    <i class={menuOpen ? "ri-close-line" : "ri-menu-line"} aria-hidden="true"></i>
                </button>
                {@render brand()}
                <!-- The search is a bare circle here: the labelled trigger is in
                     the drawer with the nav, and a 390px bar has no room for a
                     word and a keycap that no phone has anyway. -->
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    aria-label="Search the store"
                    onclick={() => (paletteOpen = true)}
                >
                    <i class="ri-search-line" aria-hidden="true"></i>
                </button>
                <!-- On a phone the Settings item lives in a closed drawer, and a
                     badge nobody can see is the failure the badge exists to
                     prevent. -->
                {#if navAlert.show}
                    <a
                        href="{base}{navAlert.href}"
                        class="app-topbar-alert"
                        aria-label={navAlert.label}
                    >
                        <i class="ri-error-warning-line" aria-hidden="true"></i>
                    </a>
                {/if}
            </div>

            {#if menuOpen}
                <button
                    type="button"
                    class="app-backdrop"
                    aria-label="Close navigation"
                    onclick={() => (menuOpen = false)}
                ></button>
            {/if}
            <aside class="app-drawer" class:open={menuOpen} aria-hidden={!menuOpen}>
                {@render brand()}
                {@render searchTrigger()}
                {@render setupLink()}
                {@render navLinks()}
                <div class="app-sidebar-foot">{@render account("drawer")}</div>
            </aside>

            <!-- Keyed on the switch, so landing on the same address in another
                 store still mounts the screen afresh and asks for its rows. -->
            {#key storeEpoch}
                {#if !parked}
                    {@render children?.()}
                {/if}
            {/key}

            <!-- Inside the authenticated branch, not beside it: both are
                 modals over a panel, and neither has anything to search or
                 explain to somebody who has not signed in. -->
            <CommandPalette bind:open={paletteOpen} />
            <ShortcutHelp bind:open={helpOpen} />
        {:else if ready && authenticated && myStores.state === "error"}
            <div class="page">
                <div class="wrapper sm m-auto txt-center p-t-base">
                    <p class="txt-hint">The store could not be reached.</p>
                    <button type="button" class="btn secondary" onclick={() => markStores("unknown")}>
                        <span class="txt">Try again</span>
                    </button>
                </div>
            </div>
        {:else}
            <div class="page"></div>
        {/if}

        {#snippet failed(error, reset)}
            <div class="page">
                <div class="page-content">
                    <div class="wrapper sm m-auto txt-center p-t-base">
                        <i
                            class="ri-error-warning-line txt-hint"
                            style="font-size: 2.5rem;"
                            aria-hidden="true"
                        ></i>
                        <h5 class="m-t-sm m-b-xs">This screen stopped</h5>
                        <p class="txt-hint">
                            {error?.message || "Something in the panel threw while drawing."}
                        </p>
                        <button type="button" class="btn secondary m-t-sm" onclick={reset}>
                            <span class="txt">Try again</span>
                        </button>
                    </div>
                </div>
            </div>
        {/snippet}
    </svelte:boundary>
</main>

<Toasts />
