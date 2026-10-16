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
    import { auth } from "$lib/api.js";
    import { createStore, enterStore, myStores } from "$lib/stores.svelte.js";
    import { reloadStoreData } from "$lib/store-data.js";
    import { safeNext } from "$lib/paths.js";

    const next = $derived(safeNext(page.url.searchParams.get("next")));

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
        try {
            await createStore(name.trim(), slug.trim());
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

<div class="page select-store-page">
    <div class="wrapper sm m-auto p-b-base select-store">
        <header class="txt-center m-b-base">
            <img class="main-logo" src="{base}/images/logo.svg" alt="" aria-hidden="true" />
            <h5 class="m-t-10">
                {#if creating && !myStores.list.length}Create your first store{:else if creating}Create a store{:else}Choose a store{/if}
            </h5>
        </header>

        {#if error}
            <div class="alert danger m-b-base select-store-error" role="alert"><p>{error}</p></div>
        {/if}

        {#if !myStores.loaded}
            <div class="block txt-center"><span class="loader lg"></span></div>
        {:else if creating}
            <form class="grid select-store-form" onsubmit={create}>
                <div class="col-12">
                    <div class="field required">
                        <label for="store_name">Store name</label>
                        <!-- svelte-ignore a11y_autofocus -->
                        <input id="store_name" type="text" required autofocus maxlength="120" bind:value={name} />
                    </div>
                </div>
                <div class="col-12">
                    <div class="field required">
                        <label for="store_slug">Address name</label>
                        <input
                            id="store_slug"
                            type="text"
                            required
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
                            <span class="txt txt-ellipsis">{st.name}</span>
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
    </div>
</div>
