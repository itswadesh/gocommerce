<script>
    /**
     * The trade portal's shell (D77): the screens a business buyer and a
     * dealer use, on the store's own host.
     *
     * The root layout lets /portal through untouched, as it does the platform
     * console, and this one decides what shows. Four pages are for somebody
     * who is not signed in — the invitation, forgotten and reset passwords,
     * and confirming an address — and render on their own. Every other page
     * needs a buyer: with nobody signed in, the sign-in form takes its place
     * at the same address, so signing in (or back in, when a session ran out
     * mid-task) lands the buyer on exactly the screen they were on.
     *
     * The store's shell in shape — the sidebar, the drawer, the page — and
     * nothing of it in substance: there is no staff session, no store
     * settings and no module probe here, and a buyer who is also one of the
     * store's operators must not find one session doing the other's work.
     */
    import { onMount } from "svelte";
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { onNavigate } from "$app/navigation";
    import { toast } from "$lib/toast.svelte.js";
    import {
        trade,
        loadMe,
        loadStore,
        loadBasket,
        refresh,
        signOut,
        visibleNav,
        badgeFor,
        roleWord,
        explain,
    } from "$lib/trade.svelte.js";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";
    import SignInForm from "./SignInForm.svelte";

    let { children } = $props();

    const SIGNED_OUT_PAGES = ["/portal/invitation", "/portal/forgot-password", "/portal/reset-password", "/portal/confirm-email"];
    const path = $derived(page.url.pathname.replace(base, "") || "/");
    const signedOutPage = $derived(SIGNED_OUT_PAGES.some((p) => path === p || path.startsWith(p + "/")));

    let booting = $state(true);
    let menuOpen = $state(false);
    let signInEmail = $state("");

    const storeName = $derived(trade.store.name || "Trade account");
    const company = $derived(trade.me?.company ?? null);
    const nav = $derived(visibleNav());

    async function boot() {
        loadStore();
        if (trade.token) {
            try {
                await refresh();
            } catch (err) {
                // A session the store has dropped is handled by the client: it
                // signs out and this shell shows the sign-in form. A store
                // that cannot be reached is not a session gone bad.
                if (!err.handled) toast.error(explain(err));
            }
            if (trade.token) {
                await loadMe();
                loadBasket().catch(() => {});
            }
        }
        booting = false;
    }

    onMount(() => {
        boot();
        // A long day at the portal keeps its session: identity slides the
        // expiry on every refresh, and never rotates the token, so this costs
        // nothing in another tab.
        const timer = setInterval(() => {
            if (trade.token) refresh().catch(() => {});
        }, 30 * 60 * 1000);
        return () => clearInterval(timer);
    });

    async function signedIn() {
        await loadMe();
        loadBasket().catch(() => {});
        if (trade.me) toast.success(`Signed in to ${trade.me.company.name}`);
    }

    async function leave() {
        for (const id of ["portal-account-side", "portal-account-drawer"]) {
            document.getElementById(id)?.hidePopover?.();
        }
        await signOut();
        toast.info("Signed out");
    }

    $effect(() => {
        // Reading the path is what subscribes this to navigation.
        page.url.pathname;
        menuOpen = false;
    });

    $effect(() => {
        const onKey = (event) => {
            if (event.key === "Escape") menuOpen = false;
        };
        window.addEventListener("keydown", onKey);
        return () => window.removeEventListener("keydown", onKey);
    });

    function isActive(item) {
        return item.exact ? path === item.href : path === item.href || path.startsWith(item.href + "/");
    }

    /*
     * Route transitions. Moving between portal screens cross-fades the page
     * area — the old screen lifts away, the new one rises into place — while
     * the sidebar holds still, because nothing in it changed but the
     * highlight. A browser without view transitions gets the panel's own
     * entrance on the new page instead (`.page-content`'s slideTop), and a
     * change of query alone (a filter, a page of a list) is not a new screen
     * and does not transition.
     */
    let canTransition = $state(false);
    onMount(() => {
        canTransition = typeof document.startViewTransition === "function";
    });
    onNavigate((navigation) => {
        if (!canTransition || !navigation.to || !navigation.from) return;
        if (navigation.from.url.pathname === navigation.to.url.pathname) return;
        if (!navigation.to.url.pathname.startsWith(base + "/portal")) return;
        return new Promise((resolve) => {
            const root = document.documentElement;
            root.classList.add("portal-vt");
            const transition = document.startViewTransition(async () => {
                resolve();
                await navigation.complete;
            });
            transition.finished.finally(() => root.classList.remove("portal-vt"));
        });
    });
</script>

<svelte:head><title>{storeName} · Trade account</title></svelte:head>

{#snippet brand()}
    <a href="{base}/portal" class="app-brand portal-brand" aria-label="{storeName} trade account">
        <i class="ri-store-2-fill app-brand-mark" aria-hidden="true"></i>
        <span class="app-brand-name txt-ellipsis">{storeName}</span>
    </a>
{/snippet}

{#snippet navLinks()}
    <nav class="app-sidebar-nav portal-nav" aria-label="Trade account">
        {#each nav as item (item.href)}
            {@const count = badgeFor(item)}
            <a
                href="{base}{item.href}"
                class="app-nav-link nav-violet portal-nav-link"
                class:active={isActive(item)}
                aria-current={isActive(item) ? "page" : undefined}
            >
                <i class={item.icon} aria-hidden="true"></i>
                <span class="txt">{item.label}</span>
                {#if count > 0}
                    {#key count}
                        <span class="portal-nav-badge" aria-label="{count} {item.badge === 'basket' ? 'items' : 'waiting'}"
                            >{count > 99 ? "99+" : count}</span
                        >
                    {/key}
                {/if}
            </a>
        {/each}
    </nav>
{/snippet}

{#snippet account(where)}
    <div class="app-account">
        <button type="button" class="app-account-trigger" popovertarget="portal-account-{where}">
            <span class="app-avatar" aria-hidden="true">{(trade.account?.name || trade.account?.email || "?")[0].toUpperCase()}</span>
            <span class="app-account-who">
                <span class="app-account-name txt-ellipsis">{trade.account?.name || trade.account?.email}</span>
                <span class="app-account-role txt-ellipsis">{roleWord(trade.me?.role)} · {company?.name}</span>
            </span>
            <i class="ri-arrow-up-s-line" aria-hidden="true"></i>
        </button>
        <div id="portal-account-{where}" class="dropdown sm nowrap logged-user-dropdown portal-account-menu" popover="auto">
            <div class="portal-account-email txt-hint txt-sm">{trade.account?.email}</div>
            <div class="portal-account-theme"><ThemeToggle /></div>
            {#if trade.store.support_url || trade.store.email}
                <a
                    class="dropdown-item"
                    href={trade.store.support_url || `mailto:${trade.store.email}`}
                    target={trade.store.support_url ? "_blank" : undefined}
                    rel="noreferrer"
                >
                    <i class="ri-question-line" aria-hidden="true"></i>
                    <span class="txt">Get help from {trade.store.name || "the store"}</span>
                </a>
            {/if}
            <hr />
            <button type="button" class="dropdown-item txt-danger" onclick={leave}>
                <i class="ri-logout-circle-line" aria-hidden="true"></i>
                <span class="txt">Sign out</span>
            </button>
        </div>
    </div>
{/snippet}

{#snippet signedOutFrame(body)}
    <div class="page portal-out">
        <div class="wrapper sm m-auto portal-out-card">
            <header class="txt-center m-b-base">
                <span class="portal-out-mark" aria-hidden="true"><i class="ri-store-2-fill"></i></span>
                <h1 class="portal-out-store">{storeName}</h1>
                <p class="txt-hint m-0">Trade account</p>
            </header>
            {@render body()}
            {#if trade.store.email || trade.store.phone}
                <p class="txt-hint txt-sm txt-center m-t-base m-b-0 portal-out-help">
                    Need a hand?
                    {#if trade.store.email}<a href="mailto:{trade.store.email}">{trade.store.email}</a>{/if}
                    {#if trade.store.email && trade.store.phone}·{/if}
                    {#if trade.store.phone}<a href="tel:{trade.store.phone}">{trade.store.phone}</a>{/if}
                </p>
            {/if}
        </div>
    </div>
{/snippet}

{#snippet signInBody()}
    {#if trade.expired}
        <div class="alert warning m-b-base portal-notice" role="status">
            <p>Your session ended. Sign in again to carry on where you were.</p>
        </div>
    {/if}
    <SignInForm bind:email={signInEmail} onsignedin={signedIn} />
{/snippet}

{#snippet notABuyer()}
    <div class="alert warning m-b-base portal-notice" role="status">
        {#if trade.meError?.status === 403}
            <p>
                <strong>{trade.account?.email}</strong> isn't part of a business account here yet. If you were
                invited, open the link in the invitation email — it adds you to your company.
            </p>
        {:else}
            <p>{explain(trade.meError)}</p>
        {/if}
    </div>
    <div class="portal-out-actions">
        {#if trade.meError?.status !== 403}
            <button type="button" class="btn portal-press" class:loading={trade.meLoading} onclick={loadMe}>
                <span class="txt">Try again</span>
            </button>
        {/if}
        <button type="button" class="btn secondary portal-press" onclick={leave}>
            <span class="txt">Sign out</span>
        </button>
    </div>
{/snippet}

{#if signedOutPage}
    {@render signedOutFrame(children)}
{:else if booting || (trade.token && trade.meLoading && !trade.me)}
    <div class="page portal-out" aria-busy="true">
        <div class="wrapper sm m-auto portal-out-card txt-center">
            <span class="portal-out-mark portal-breathe" aria-hidden="true"><i class="ri-store-2-fill"></i></span>
            <p class="txt-hint">Opening your trade account…</p>
        </div>
    </div>
{:else if !trade.token}
    {@render signedOutFrame(signInBody)}
{:else if !trade.me}
    {@render signedOutFrame(notABuyer)}
{:else}
    <div class="portal-shell" class:has-vt={canTransition}>
        <aside class="app-sidebar portal-sidebar">
            {@render brand()}
            <div class="portal-company">
                <span class="portal-company-name txt-ellipsis">{company.name}</span>
                <span class="txt-hint txt-sm">{roleWord(trade.me.role)}</span>
            </div>
            {@render navLinks()}
            <div class="app-sidebar-foot">{@render account("side")}</div>
        </aside>

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
            <a
                href="{base}/portal/basket"
                class="btn circle transparent secondary portal-topbar-basket"
                aria-label="Your basket{trade.basket.count ? `, ${trade.basket.count} items` : ''}"
            >
                <i class="ri-shopping-basket-2-line" aria-hidden="true"></i>
                {#if trade.basket.count > 0}
                    {#key trade.basket.count}
                        <span class="portal-nav-badge portal-topbar-badge">{trade.basket.count > 99 ? "99+" : trade.basket.count}</span>
                    {/key}
                {/if}
            </a>
        </div>

        {#if menuOpen}
            <button type="button" class="app-backdrop" aria-label="Close navigation" onclick={() => (menuOpen = false)}
            ></button>
        {/if}
        <aside class="app-drawer portal-sidebar" class:open={menuOpen} aria-hidden={!menuOpen} inert={!menuOpen}>
            {@render brand()}
            <div class="portal-company">
                <span class="portal-company-name txt-ellipsis">{company.name}</span>
                <span class="txt-hint txt-sm">{roleWord(trade.me.role)}</span>
            </div>
            {@render navLinks()}
            <div class="app-sidebar-foot">{@render account("drawer")}</div>
        </aside>

        {@render children?.()}
    </div>
{/if}
