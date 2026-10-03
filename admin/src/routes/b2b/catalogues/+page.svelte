<script>
    /**
     * Catalogues: what a company may buy, when it may not buy everything.
     *
     * A catalogue is categories — each with everything filed under it — and
     * products one by one. A company is held to one from its own form; a
     * company held to none buys everything, as before catalogues existed. The
     * engine holds the line at checkout, in a quick order and in a quote, so
     * this list is where a restriction is read, not merely described.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { onNewShortcut } from "$lib/shortcuts.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, pluralize } from "$lib/format.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";

    const PER_PAGE = 50;
    const list = listState({ q: "", page: 1, limit: PER_PAGE });

    const readable = $derived(can("companies.read"));
    const writable = $derived(can("companies.write"));
    const missing = $derived(modulesKnown() && !hasModule("b2b"));

    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let draft = $state(list.params.q);
    let reqId = 0;

    $effect(() => {
        list.params;
        if (readable && hasModule("b2b")) load();
    });

    $effect(() => onNewShortcut(openNew));

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const p = list.params;
            const result = await api.get("/api/admin/x/b2b/catalogues" + query({ q: p.q, page: p.page, limit: p.limit }));
            if (mine !== reqId) return;
            rows = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    function openNew() {
        if (writable && !missing) goto(`${base}/b2b/catalogues/new`);
    }

    const open = (c) => goto(`${base}/b2b/catalogues/${c.id}`);

    function contents(c) {
        const parts = [];
        if (c.category_count) parts.push(`${c.category_count} ${pluralize(c.category_count, "category", "categories")}`);
        if (c.product_count) parts.push(`${c.product_count} ${pluralize(c.product_count, "product")}`);
        return parts.join(" and ");
    }
</script>

<svelte:head><title>Catalogues · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="companies.read" what="catalogues" />
{:else}
    <div class="page page-catalogues b2b-page shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Products</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Catalogues</div>
                </nav>
                <div class="flex-fill"></div>
                {#if writable && !missing}
                    <div class="page-header-primary-btns">
                        <button type="button" class="btn" aria-label="New catalogue" onclick={openNew}>
                            <i class="ri-add-line" aria-hidden="true"></i>
                            <span class="txt">New catalogue</span>
                        </button>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="b2b" what="Catalogues are served by ext/b2b, and this binary does not have it." />
            {:else}
                <p class="field-help m-b-base b2b-notice">
                    What a business may buy, when it may not buy everything: categories, each with everything
                    under it, and products one by one. A company is held to one from its own page; the checkout,
                    quick orders and quotes all keep to it.
                </p>

                <form class="fields m-b-base" role="search" onsubmit={(e) => e.preventDefault()}>
                    <div class="field">
                        <label for="catalogue-search">Search</label>
                        <input
                            id="catalogue-search"
                            type="search"
                            placeholder="Name"
                            autocomplete="off"
                            bind:value={draft}
                            oninput={(e) => list.set({ q: e.currentTarget.value.trim() })}
                        />
                    </div>
                </form>

                <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && rows.length > 0}>
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-name-id">Catalogue</th>
                                <th>Allows</th>
                                <th class="col-field-type-number min-width">Companies</th>
                                <th class="min-width">Changed</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#if loading && !rows.length}
                                {#each Array(3) as _, i (i)}
                                    <tr><td colspan="4"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {/if}
                            {#each rows as c (c.id)}
                                <tr class="handle" tabindex="0" onclick={() => open(c)} onkeydown={(e) => rowKey(e, () => open(c))}>
                                    <td class="col-field-name-id" data-name="Catalogue">
                                        <div class="row-name row-name-stacked">
                                            <a
                                                href="{base}/b2b/catalogues/{c.id}"
                                                class="txt-bold"
                                                onclick={(e) => e.stopPropagation()}>{c.name}</a
                                            >
                                            {#if c.description}<span class="txt-hint txt-sm txt-ellipsis b2b-snippet">{c.description}</span>{/if}
                                        </div>
                                    </td>
                                    <td data-name="Allows">
                                        {#if contents(c)}{contents(c)}{:else}<span class="txt-hint">Nothing yet</span>{/if}
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Companies">{c.company_count}</td>
                                    <td class="min-width txt-hint txt-sm" data-name="Changed">
                                        {formatDate(c.updated_at, { withTime: false })}
                                    </td>
                                </tr>
                            {/each}
                            {#if !loading && !rows.length}
                                <tr>
                                    <td colspan="4" class="txt-hint txt-center p-base">
                                        {#if list.params.q}
                                            No catalogue's name matches “{list.params.q}”.
                                        {:else}
                                            No catalogues yet, so every company may buy everything. Add one to hold a
                                            company to part of the range.
                                        {/if}
                                    </td>
                                </tr>
                            {/if}
                        </tbody>
                    </table>
                </div>

                <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                    <Pager
                        {meta}
                        {loading}
                        noun="catalogue"
                        perPage={list.params.limit}
                        onpage={(n) => list.setPage(n)}
                        onperpage={(n) => list.set({ limit: n })}
                    />
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>
{/if}
