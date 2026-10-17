<script>
    /**
     * The store picker (D83): shown after sign-in to somebody in several
     * stores with none remembered, and from "All stores…" in the account menu.
     * Somebody with no store at all lands on the create form, because there is
     * nothing else for them to do.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { goto } from "$app/navigation";
    import { auth, stores as storesAPI } from "$lib/api.js";
    import { rise } from "$lib/motion.js";
    import { createStore, enterStore, myStores } from "$lib/stores.svelte.js";
    import { reloadStoreData } from "$lib/store-data.js";
    import { safeNext } from "$lib/paths.js";
    import { CURRENCIES } from "$lib/currencies.js";
    import { COUNTRIES } from "$lib/countries.js";
    import Select from "$lib/components/Select.svelte";
    import AuthShell from "$lib/components/AuthShell.svelte";

    const next = $derived(safeNext(page.url.searchParams.get("next")));
    // Arrived from signing up: this form is the second of its two steps.
    const fromSignup = $derived(page.url.searchParams.has("signup"));

    let query = $state("");
    let entering = $state("");
    let error = $state("");

    const shown = $derived.by(() => {
        const q = query.trim().toLowerCase();
        if (!q) return myStores.list;
        return myStores.list.filter((st) => st.name.toLowerCase().includes(q) || st.slug.includes(q));
    });

    // The form opens by itself when there is nothing to choose, or when the
    // account menu's "Create a store" sent them here.
    let creating = $state(false);
    $effect(() => {
        if (page.url.searchParams.has("create") || (myStores.loaded && myStores.list.length === 0)) {
            creating = true;
        }
    });

    let name = $state("");
    let slug = $state("");
    let slugTouched = $state(false);

    /*
     * A name to start from, the way GitHub offers one for a new repository:
     * shown under the field, filled in by a click, and what a store left
     * unnamed is called. The engine would choose one anyway; asking for it
     * here means the store gets the name the form showed.
     */
    let suggestion = $state(null);
    async function suggest() {
        try {
            suggestion = await storesAPI.suggest();
        } catch {
            suggestion = null; // the form still works; the engine names it
        }
    }
    $effect(() => {
        if (creating && !suggestion) suggest();
    });
    // The address follows the name, as it does for a typed one, so editing
    // "Saffron Orchard" into "Saffron Jewels" moves it too.
    // The address follows the name, as it does for a typed one, so editing
    // "Saffron Orchard" into "Saffron Jewels" moves it too.
    function useSuggestion() {
        if (!suggestion) return;
        name = suggestion.name;
        slugTouched = false;
    }
    // What the store sells in; empty is the install's. It can be changed on
    // Store settings until the first order.
    let currency = $state("");
    // Where the store is: its country on Store settings, and what its default
    // shipping zone covers. A guess from the browser's region to start with.
    let country = $state(browserCountry());

    function browserCountry() {
        try {
            const region = new Intl.Locale(navigator.language).maximize().region ?? "";
            return COUNTRIES.some((c) => c.value === region) ? region : "";
        } catch {
            return "";
        }
    }
    let saving = $state(false);

    /* The slug follows the name until somebody edits it: the shape the
       engine accepts, lowercase letters, digits and hyphens. */
    function slugFrom(text) {
        return text
            .toLowerCase()
            .replace(/[^a-z0-9]+/g, "-")
            .replace(/^-+|-+$/g, "")
            .slice(0, 63);
    }
    $effect(() => {
        if (!slugTouched) slug = slugFrom(name);
    });

    async function choose(st) {
        if (entering || st.status !== "active") return;
        entering = st.id;
        error = "";
        try {
            await enterStore(st.id);
            await reloadStoreData();
            await goto(base + next, { replaceState: true });
        } catch (err) {
            error = err?.message || "Could not open that store.";
        } finally {
            entering = "";
        }
    }

    async function create(event) {
        event.preventDefault();
        if (saving) return;
        saving = true;
        error = "";
        // Left blank, the store is called what the form suggested.
        if (!name.trim() && suggestion) name = suggestion.name;
        try {
            // An address is sent only when somebody typed one. One worked out
            // from the name is the engine's to work out, so a taken one gets a
            // number rather than a refusal.
            await createStore(name.trim(), slugTouched ? slug.trim() : "", currency, country);
            await reloadStoreData();
            await goto(base + "/dash", { replaceState: true });
        } catch (err) {
            error = err?.message || "Could not create the store.";
        } finally {
            saving = false;
        }
    }

    async function signOut() {
        await auth.logout();
    }

    const roleName = (role) => (role ? role[0].toUpperCase() + role.slice(1) : "");
</script>

<svelte:head>
    <title>Choose a store · GoCommerce</title>
</svelte:head>

<AuthShell>
    <header class="auth-head">
        {#if creating && fromSignup && !myStores.list.length}
            <p class="auth-step">Step 2 of 2</p>
            <h1 class="auth-title">Name your store</h1>
            <p class="auth-lede">A name to start with, and where it sells. The name can be changed later.</p>
        {:else}
            <h1 class="auth-title">
                {#if creating && !myStores.list.length}Create your first store{:else if creating}Create a store{:else}Choose a store{/if}
            </h1>
        {/if}
    </header>

    {#if error}
        <div class="alert danger m-b-base select-store-error" role="alert"><p>{error}</p></div>
    {/if}

    {#if !myStores.loaded}
        <div class="block txt-center"><span class="loader lg"></span></div>
    {:else if creating}
        <form class="grid select-store-form" onsubmit={create}>
            <div class="col-12">
                <div class="field">
                    <label for="store_name">Store name</label>
                    <!-- svelte-ignore a11y_autofocus -->
                    <input
                        id="store_name"
                        type="text"
                        autofocus
                        maxlength="120"
                        placeholder={suggestion?.name ?? ""}
                        aria-describedby="store_name_suggestion"
                        bind:value={name}
                    />
                </div>
                <div class="link-hint m-t-5 store-suggest" id="store_name_hint" aria-live="polite">
                    {#if suggestion}
                        {#key suggestion.slug}
                            <small id="store_name_suggestion" in:rise>
                                Short and memorable works best. Need inspiration? How about
                                <button type="button" class="store-suggest-use" onclick={useSuggestion}>{suggestion.name}</button>?
                            </small>
                        {/key}
                        <button type="button" class="btn xs circle transparent secondary store-suggest-again" aria-label="Suggest another name" title="Suggest another" onclick={suggest}>
                            <i class="ri-refresh-line" aria-hidden="true"></i>
                        </button>
                    {:else}
                        <small id="store_name_suggestion">Leave it blank and the store gets a name you can change later.</small>
                    {/if}
                </div>
            </div>
            <div class="col-12">
                <div class="field">
                    <label for="store_slug">Address name</label>
                    <input
                        id="store_slug"
                        type="text"
                        placeholder={name.trim() ? "" : (suggestion?.slug ?? "")}
                        pattern="[a-z0-9][a-z0-9\-]{'{0,62}'}"
                        maxlength="63"
                        autocomplete="off"
                        spellcheck="false"
                        bind:value={slug}
                        oninput={() => (slugTouched = true)}
                    />
                </div>
                <div class="link-hint m-t-5">
                    <small>Lowercase letters, digits and hyphens. Requests name the store by it.</small>
                </div>
            </div>
            <div class="col-12">
                <div class="field">
                    <label for="store_country">Country</label>
                    <Select id="store_country" bind:value={country} options={COUNTRIES} placeholder="Choose a country" />
                </div>
                <div class="link-hint m-t-5">
                    <small>Where the store is. Its first shipping zone covers this country.</small>
                </div>
            </div>
            <div class="col-12">
                <div class="field">
                    <label for="store_currency">Currency</label>
                    <Select id="store_currency" bind:value={currency} options={CURRENCIES} placeholder="The install's default" />
                </div>
                <div class="link-hint m-t-5">
                    <small>What the store sells in. You can change it until the first order.</small>
                </div>
            </div>
            <div class="col-12">
                <button class="btn lg block next" class:loading={saving} disabled={saving}>
                    <span class="txt">Create store</span>
                    <i class="ri-arrow-right-line" aria-hidden="true"></i>
                </button>
            </div>
            {#if myStores.list.length}
                <div class="col-12">
                    <button type="button" class="btn lg block secondary" onclick={() => (creating = false)}>
                        <span class="txt">Back to your stores</span>
                    </button>
                </div>
            {/if}
        </form>
    {:else}
        <!-- Past seven, as in the account menu: one rule for both lists. -->
        {#if myStores.list.length > 7}
            <input
                type="search"
                class="select-store-search m-b-sm"
                placeholder="Find a store"
                aria-label="Find a store"
                bind:value={query}
            />
        {/if}
        <div class="select-store-list">
            {#each shown as st (st.id)}
                {@const current = st.id === myStores.current?.id}
                <button
                    type="button"
                    class="select-store-item"
                    class:current
                    disabled={st.status !== "active" || !!entering}
                    onclick={() => choose(st)}
                >
                    <span class="store-menu-mark" aria-hidden="true">{(st.name || st.slug)[0].toUpperCase()}</span>
                    <span class="select-store-text">
                        <span class="txt txt-ellipsis">
                            {st.name}
                            {#if st.demo}<span class="store-demo-tag">Demo</span>{/if}
                        </span>
                        <span class="txt-hint txt-sm txt-ellipsis">
                            {st.slug} · {roleName(st.role)}{st.status !== "active" ? ` · ${st.status}` : ""}
                        </span>
                    </span>
                    {#if entering === st.id}
                        <span class="loader sm" aria-label="Opening"></span>
                    {:else if current}
                        <i class="ri-check-line store-menu-check" aria-hidden="true"></i>
                    {:else}
                        <i class="ri-arrow-right-s-line txt-hint" aria-hidden="true"></i>
                    {/if}
                </button>
            {:else}
                <p class="txt-hint txt-center">No store matches “{query}”.</p>
            {/each}
        </div>
        <button type="button" class="btn lg block secondary m-t-base" onclick={() => (creating = true)}>
            <i class="ri-add-line" aria-hidden="true"></i>
            <span class="txt">Create a store</span>
        </button>
    {/if}

    <p class="txt-center m-t-base">
        <button type="button" class="btn sm transparent secondary" onclick={signOut}>
            <span class="txt">Sign out</span>
        </button>
    </p>
</AuthShell>

<style>
    .store-suggest {
        display: flex;
        align-items: center;
        gap: 6px;
    }
    .store-suggest small {
        flex: 1 1 auto;
    }
    /* A word in the sentence that fills the field: a link to look at, a
       button to the keyboard and to a screen reader. */
    .store-suggest-use {
        padding: 0;
        border: 0;
        background: none;
        color: var(--txtPrimaryColor);
        font: inherit;
        font-weight: 600;
        text-decoration: underline;
        text-underline-offset: 2px;
        text-decoration-thickness: 1px;
        cursor: pointer;
        transition: text-decoration-thickness 150ms ease-out;
    }
    .store-suggest-use:hover,
    .store-suggest-use:focus-visible {
        text-decoration-thickness: 2px;
    }
    .store-suggest-again {
        flex: 0 0 auto;
        min-width: 32px;
        min-height: 32px;
    }
    @media (prefers-reduced-motion: reduce) {
        .store-suggest-use {
            transition: none;
        }
    }
</style>
