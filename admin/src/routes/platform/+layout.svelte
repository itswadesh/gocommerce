<script>
    /**
     * The platform console's shell (D73): the screens a platform operator
     * manages stores from, on the platform's own hosts.
     *
     * It is the store's shell in shape — the same sidebar, drawer and page —
     * and nothing of it in substance. The root layout lets /platform through
     * untouched, like the invitation screen, and this one decides what shows:
     * the token form, or the console. There is no store here to sign in to,
     * no store settings to load and no store modules to probe, and a platform
     * operator who is also a store's owner must not find one session doing the
     * other's work.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { platform, signIn, signOut } from "$lib/platform.svelte.js";
    import { toast } from "$lib/toast.svelte.js";

    let { children } = $props();

    let candidate = $state("");
    let signingIn = $state(false);
    let refusal = $state("");
    let menuOpen = $state(false);

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

    async function submit(event) {
        event.preventDefault();
        if (signingIn || !candidate.trim()) return;
        signingIn = true;
        refusal = "";
        try {
            await signIn(candidate);
            candidate = "";
            toast.success("Signed in to the platform");
        } catch (err) {
            refusal =
                err?.status === 401
                    ? "The platform does not know that token."
                    : err?.status === 404
                      ? "This address does not serve the platform. Open the platform's own host — platform.<base domain>, or the one -platform-host names."
                      : err?.message || "Could not sign in.";
        } finally {
            signingIn = false;
        }
    }

    function leave() {
        for (const id of ["platform-account-side", "platform-account-drawer"]) {
            document.getElementById(id)?.hidePopover?.();
        }
        signOut();
        toast.info("Signed out");
    }

    const onStores = $derived(/^\/platform\/?$/.test(page.url.pathname.replace(base, "")));
</script>

<svelte:head><title>Platform · GoCommerce</title></svelte:head>

{#snippet brand()}
    <a href="{base}/platform" class="app-brand" aria-label="GoCommerce platform">
        <i class="ri-shopping-bag-3-fill app-brand-mark" aria-hidden="true"></i>
        <span class="app-brand-name">GoCommerce</span>
        <span class="label platform-brand-tag">Platform</span>
    </a>
{/snippet}

{#snippet navLinks()}
    <nav class="app-sidebar-nav">
        <!-- The one section there is, so it is always the one you are in. -->
        <a href="{base}/platform" class="app-nav-link nav-violet active" aria-current={onStores ? "page" : undefined}>
            <i class="ri-store-2-line" aria-hidden="true"></i>
            <span class="txt">Stores</span>
        </a>
    </nav>
{/snippet}

{#snippet account(where)}
    <div class="app-account">
        <button type="button" class="app-account-trigger" popovertarget="platform-account-{where}">
            <span class="app-avatar" aria-hidden="true"><i class="ri-shield-keyhole-line"></i></span>
            <span class="app-account-who">
                <span class="app-account-name txt-ellipsis">Platform operator</span>
                <span class="app-account-role txt-ellipsis">Platform token</span>
            </span>
            <i class="ri-arrow-up-s-line" aria-hidden="true"></i>
        </button>
        <div id="platform-account-{where}" class="dropdown sm nowrap logged-user-dropdown" popover="auto">
            <a class="dropdown-item" href="/api/platform/doc" target="_blank" rel="noreferrer">
                <i class="ri-code-s-slash-line" aria-hidden="true"></i>
                <span class="txt">Platform API contract</span>
            </a>
            <hr />
            <button type="button" class="dropdown-item txt-danger" onclick={leave}>
                <i class="ri-logout-circle-line" aria-hidden="true"></i>
                <span class="txt">Sign out</span>
            </button>
        </div>
    </div>
{/snippet}

{#if !platform.token}
    <div class="page platform-signin">
        <div class="wrapper sm m-auto p-b-base">
            <header class="txt-center m-b-base">
                <img class="main-logo" src="{base}/images/logo.svg" alt="" aria-hidden="true" />
                <h5 class="m-t-10">Platform sign-in</h5>
            </header>
            {#if platform.expired}
                <div class="alert warning m-b-base" role="status">
                    <p>The platform stopped accepting that token. Sign in with a current one.</p>
                </div>
            {/if}
            <div class="content txt-center txt-hint m-b-base">
                <small>
                    The platform token is the one the platform was started with
                    (<span class="txt-code">-platform-token</span> or
                    <span class="txt-code">GOCOMMERCE_PLATFORM_TOKEN</span>). It manages stores; it
                    does not sign in to any of them. It is kept until this tab closes.
                </small>
            </div>
            <form class="grid" onsubmit={submit}>
                <div class="col-12">
                    <div class="field required" class:error={!!refusal}>
                        <label for="platform-token">Platform token</label>
                        <!-- svelte-ignore a11y_autofocus -->
                        <input
                            id="platform-token"
                            type="password"
                            autocomplete="current-password"
                            autofocus
                            required
                            bind:value={candidate}
                            aria-describedby={refusal ? "platform-token-refusal" : undefined}
                        />
                    </div>
                    {#if refusal}
                        <div class="field-help txt-danger platform-refusal" id="platform-token-refusal" role="alert">
                            {refusal}
                        </div>
                    {/if}
                </div>
                <div class="col-12">
                    <button
                        type="submit"
                        class="btn lg block next"
                        class:loading={signingIn}
                        disabled={signingIn || !candidate.trim()}
                    >
                        <span class="txt">Sign in</span>
                        <i class="ri-arrow-right-line" aria-hidden="true"></i>
                    </button>
                </div>
            </form>
        </div>
    </div>
{:else}
    <aside class="app-sidebar">
        {@render brand()}
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
    </div>

    {#if menuOpen}
        <button type="button" class="app-backdrop" aria-label="Close navigation" onclick={() => (menuOpen = false)}
        ></button>
    {/if}
    <aside class="app-drawer" class:open={menuOpen} aria-hidden={!menuOpen}>
        {@render brand()}
        {@render navLinks()}
        <div class="app-sidebar-foot">{@render account("drawer")}</div>
    </aside>

    {@render children?.()}
{/if}
