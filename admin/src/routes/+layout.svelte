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
    import { auth, getToken, can, session } from "$lib/api.js";
    import { clearSettings, loadSettings } from "$lib/settings.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import Toasts from "$lib/components/Toasts.svelte";
    import Login from "$lib/components/Login.svelte";

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
     * Each entry names the right that makes the screen worth showing. A staff
     * operator has no business on a settings page they would be refused from,
     * and an item that leads only to a 403 is worse than no item.
     *
     * The engine is still the one enforcing this; the nav is only telling the
     * truth about what is behind each link.
     *
     * `accent` is the item own colour, as the B2B Leads sidebar does it: the
     * icon is always tinted and the active row takes the matching soft ground.
     * The values live in gocommerce.css, so light and dark can differ; here
     * they are only names.
     */
    const nav = [
        { href: "/", label: "Dashboard", icon: "ri-dashboard-line", exact: true , accent: "indigo" },
        // "How much did we sell" is the second thing an owner opens, and until
        // now the panel could not answer it.
        { href: "/reports", label: "Reports", icon: "ri-line-chart-line", right: "orders.read", accent: "rose" },
        { href: "/products", label: "Products", icon: "ri-price-tag-3-line", right: "catalog.read" , accent: "sky" },
        { href: "/categories", label: "Categories", icon: "ri-node-tree", right: "catalog.read" , accent: "blue" },
        { href: "/orders", label: "Orders", icon: "ri-shopping-bag-3-line", right: "orders.read" , accent: "amber" },
        { href: "/carts", label: "Carts", icon: "ri-shopping-cart-2-line", right: "orders.read" , accent: "fuchsia" },
        { href: "/discounts", label: "Discounts", icon: "ri-price-tag-2-line", right: "discounts.read" , accent: "rose" },
        { href: "/taxes", label: "Tax", icon: "ri-percent-line", right: "taxes.read" , accent: "violet" },
        { href: "/customers", label: "Customers", icon: "ri-user-3-line", right: "customers.read" , accent: "teal" },
        { href: "/inventory", label: "Inventory", icon: "ri-archive-2-line", right: "inventory.read" , accent: "emerald" },
        { href: "/locations", label: "Locations", icon: "ri-map-pin-line", right: "locations.read" , accent: "cyan" },
        // Settings has no right of its own: the section is a shell, and every
        // screen inside it carries its own gate. Hiding the whole section from
        // an operator who may reach one of them is a worse lie than showing a
        // section with one item in it.
        { href: "/settings", label: "Settings", icon: "ri-settings-3-line", accent: "orange" },
    ];

    const visibleNav = $derived(nav.filter((item) => !item.right || can(item.right)));

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

    $effect(() => {
        const onKey = (e) => {
            if (e.key === "Escape") menuOpen = false;
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
        auth.refresh().catch((err) => {
            if (err.isAuth) {
                // api.js has already ended the session; the cached settings
                // would be the previous operator's answer.
                clearSettings();
                return;
            }
            // A store that cannot be reached is not a credential that has gone
            // bad. Clearing the session here signed out an operator whose token
            // was still perfectly good.
            toast.error("Could not reach the store — you are still signed in.");
        });
    });

    function onAuthenticated() {
        // auth.login has already written the session; the shell only reacts to
        // it. Forced, because the boot attempt ran with no token — or failed.
        loadSettings({ force: true });
        toast.success("Signed in");
    }

    async function signOut() {
        document.getElementById("logged-user-dropdown")?.hidePopover();
        // auth.logout is what clears the credential. The settings go with it:
        // the next operator on a shared browser may be looking at another store.
        await auth.logout();
        clearSettings();
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
    <!-- The logo file is itself a wordmark, so it is the whole brand row: a name
         beside it would say GoCommerce twice. -->
    <a href="{base}/" class="app-brand" aria-label="GoCommerce">
        <img src="{base}/images/logo_header.svg" alt="GoCommerce" />
    </a>
{/snippet}

{#snippet navLinks()}
    <nav class="app-sidebar-nav">
        {#each visibleNav as item (item.href)}
            <a
                href="{base}{item.href}"
                class="app-nav-link nav-{item.accent}"
                class:active={isActive(item)}
            >
                <i class={item.icon} aria-hidden="true"></i>
                <span class="txt">{item.label}</span>
            </a>
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
                {@render navLinks()}
                <div class="app-sidebar-foot">{@render account()}</div>
            </aside>

            {@render children?.()}
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
