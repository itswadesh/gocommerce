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
    import { auth, events, getToken, can, session } from "$lib/api.js";
    import { clearProfile, clearSettings, loadProfile, loadSettings } from "$lib/settings.svelte.js";
    import { forgetModules, loadModules } from "$lib/modules.svelte.js";
    import { forgetScreens, loadScreens } from "$lib/screens.svelte.js";
    import { health } from "$lib/health.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { NAV, visibleNav as allowedNav } from "$lib/nav.js";
    import { isBareKey, isTypingTarget, modalIsOpen, NEW_EVENT } from "$lib/shortcuts.js";
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
     */
    const PUBLIC_PREFIXES = ["/accept-invite"];
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
        if (!getToken()) {
            ready = true;
            return;
        }
        // A stored token is a claim, not a fact. Refresh turns it back into an
        // identity — or tells us it has expired, before the operator hits a
        // 401 on something they actually meant to do.
        ready = true;
        // Every screen formats money before it can draw anything, so the store
        // is asked here, once, rather than by each screen that needs it.
        // Nothing in the shell waits on the answer.
        loadSettings();
        loadProfile();
        auth.refresh()
            .then(() => {
                // After the refresh rather than beside it: a stored token is a
                // claim, and probing four module routes with one the store has
                // just rejected is four 401s on the commonest path there is —
                // come back the next morning, session expired, sign in again.
                loadModules();
                loadScreens();
            })
            .catch((err) => {
                if (err.isAuth) {
                    // api.js has already ended the session; the cached settings
                    // would be the previous operator's answer.
                    clearSettings();
        clearProfile();
                    clearProfile();
                    return;
                }
                // A store that cannot be reached is not a credential that has
                // gone bad. Clearing the session here signed out an operator
                // whose token was still perfectly good.
                toast.error("Could not reach the store — you are still signed in.");
            });
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
    $effect(() => {
        if (!authenticated) return;
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
        // Reading the path is what subscribes this to navigation.
        page.url.pathname;
        if (!authenticated || !can("store.operate")) {
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
            ? { show: true, href: "/settings/diagnostics", label: "A health check is failing" }
            : deadLetters > 0
              ? {
                    show: true,
                    href: "/settings/events",
                    label: `${deadLetters} event${deadLetters === 1 ? "" : "s"} could not be delivered`,
                }
              : { show: false, href: "/settings", label: "" },
    );

    function onAuthenticated() {
        // auth.login has already written the session; the shell only reacts to
        // it. Forced, because the boot attempt ran with no token — or failed.
        loadSettings({ force: true });
        loadProfile({ force: true });
        // A new sign-in may be a different operator against a different store,
        // so the previous answer is dropped before a new one is asked for.
        forgetModules();
        loadModules();
        forgetScreens();
        loadScreens();
        toast.success("Signed in");
    }

    async function signOut() {
        document.getElementById("logged-user-dropdown")?.hidePopover();
        // auth.logout is what clears the credential. The settings go with it:
        // the next operator on a shared browser may be looking at another store.
        await auth.logout();
        clearSettings();
        forgetModules();
        forgetScreens();
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
    <a href="{base}/" class="app-brand" aria-label="GoCommerce">
        <i class="ri-shopping-bag-3-fill app-brand-mark" aria-hidden="true"></i>
        <span class="app-brand-name">GoCommerce</span>
    </a>
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

{#snippet account()}
    <!-- The account block sits at the foot of the sidebar, as it does in B2B
         Leads: avatar, who you are, and the role you are signed in as — which
         this panel now knows, so it can say it. -->
    <div class="app-account">
        <button
            type="button"
            class="app-account-trigger"
            popovertarget="logged-user-dropdown"
            use:trackPopover
        >
            <span class="app-avatar" aria-hidden="true">{initials}</span>
            <span class="app-account-who">
                <span class="app-account-name txt-ellipsis">{record?.email || "admin token"}</span>
                <span class="app-account-role txt-ellipsis">{roleLabel}</span>
            </span>
            <i class="ri-arrow-up-s-line" aria-hidden="true"></i>
        </button>
        <div id="logged-user-dropdown" class="dropdown sm nowrap logged-user-dropdown" popover="auto">
            <!-- First, and unconditional: changing your own password needs no
                 right, and this is the shortest way to it. -->
            <a
                class="dropdown-item"
                href="{base}/settings/account"
                onclick={() => document.getElementById("logged-user-dropdown")?.hidePopover()}
            >
                <i class="ri-user-settings-line" aria-hidden="true"></i>
                <span class="txt">Your account</span>
            </a>
            {#if can("team.read")}
                <a
                    class="dropdown-item"
                    href="{base}/settings/superusers"
                    onclick={() => document.getElementById("logged-user-dropdown")?.hidePopover()}
                >
                    <i class="ri-group-line" aria-hidden="true"></i>
                    <span class="txt">Manage the team</span>
                </a>
            {/if}
            <a
                class="dropdown-item"
                href="{base}/docs"
                target="_blank"
                rel="noreferrer"
                onclick={() => document.getElementById("logged-user-dropdown")?.hidePopover()}
            >
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
        {#if isPublic}
            <!-- No header, no nav: there is nothing yet to navigate as. -->
            {@render children?.()}
        {:else if ready && authenticated}
            <!-- Desktop: the sidebar itself. -->
            <aside class="app-sidebar">
                {@render brand()}
                {@render searchTrigger()}
                {@render navLinks()}
                <div class="app-sidebar-foot">{@render account()}</div>
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
                {@render navLinks()}
                <div class="app-sidebar-foot">{@render account()}</div>
            </aside>

            {@render children?.()}

            <!-- Inside the authenticated branch, not beside it: both are
                 modals over a panel, and neither has anything to search or
                 explain to somebody who has not signed in. -->
            <CommandPalette bind:open={paletteOpen} />
            <ShortcutHelp bind:open={helpOpen} />
        {:else if ready}
            <div class="page">
                <!-- Why the login form is back, when it is back because a
                     credential stopped resolving mid-session rather than
                     because nobody has signed in yet. -->
                {#if session.expired}
                    <div class="alert warning m-b-base">
                        <p>Your session ended. Sign in to continue.</p>
                    </div>
                {/if}
                <Login onauthenticated={onAuthenticated} />
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
