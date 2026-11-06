<script>
    /**
     * Every store on the install, for its owner alone (D215). The engine is what
     * refuses everybody else; the sidebar only hides the link.
     *
     * "Legal name" is what a store typed as its registered business. A person
     * has no name in the engine, so there is no owner's name to show here.
     */
    import { stores } from "$lib/api.js";
    import { titled } from "$lib/brand.svelte.js";
    import EmptyState from "$lib/components/EmptyState.svelte";

    let loading = $state(true);
    let refused = $state(false);
    let failed = $state("");
    let list = $state([]);

    async function load() {
        loading = true;
        failed = "";
        try {
            list = await stores.directory();
        } catch (err) {
            if (err?.status === 403) refused = true;
            else failed = err?.message || "The list could not be loaded.";
        } finally {
            loading = false;
        }
    }

    load();
</script>

<svelte:head><title>{titled("All stores")}</title></svelte:head>

<div class="page page-all-stores shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">
                    All stores{#if !loading && !refused && !failed}<span class="store-total"> {list.length}</span>{/if}
                </div>
            </nav>
            {#if !refused}
                <div class="inline-flex gap-sm">
                    <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                        <i class="ri-refresh-line" class:spinning={loading} aria-hidden="true"></i>
                    </button>
                </div>
            {/if}
        </header>

        {#if refused}
            <EmptyState icon="ri-lock-line" title="This page is not available" text="It is for the owner of this install." />
        {:else if failed}
            <EmptyState icon="ri-error-warning-line" title="The list could not be loaded" text={failed} />
        {:else if !loading && list.length === 0}
            <EmptyState icon="ri-store-3-line" title="No stores yet" />
        {:else if list.length}
            <div class="page-table-wrapper tw:rounded-xl tw:border" class:reloading={loading}>
                <table class="table responsive-table">
                    <thead>
                        <tr>
                            <th>Store</th>
                            <th>Legal name</th>
                            <th>Owner</th>
                            <th class="col-field-type-number min-width">Products</th>
                            <th class="col-field-type-number min-width">Orders</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each list as store (store.id)}
                            <tr>
                                <td data-name="Store">
                                    <div class="row-name row-name-stacked">
                                        <span class="txt-bold">{store.name || store.slug}</span>
                                        <span class="txt-hint">
                                            {store.slug}{#if store.status !== "active"} · {store.status}{/if}
                                        </span>
                                    </div>
                                </td>
                                <td data-name="Legal name">{store.legal_name || "—"}</td>
                                <td data-name="Owner">
                                    <div class="owners">
                                        {#each store.owners as owner, i (i)}
                                            <div class="contacts">
                                                {#if owner.phone}<a href="tel:{owner.phone}">{owner.phone}</a>{/if}
                                                {#if owner.email}<a href="mailto:{owner.email}">{owner.email}</a>{/if}
                                            </div>
                                        {:else}
                                            —
                                        {/each}
                                    </div>
                                </td>
                                <td class="col-field-type-number min-width" data-name="Products">{store.products.toLocaleString("en-IN")}</td>
                                <td class="col-field-type-number min-width" data-name="Orders">{store.orders.toLocaleString("en-IN")}</td>
                            </tr>
                        {/each}
                    </tbody>
                </table>
            </div>
        {/if}
    </div>
</div>

<style>
    .store-total {
        margin-left: 0.35em;
        font-weight: 400;
        opacity: 0.6;
    }
    .owners {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .contacts {
        display: flex;
        flex-direction: column;
        gap: 2px;
        overflow-wrap: anywhere;
    }
    .contacts a {
        transition: opacity 150ms ease;
    }
    .contacts a:hover,
    .contacts a:active {
        opacity: 0.65;
    }
    .contacts a:focus-visible {
        outline: 2px solid currentColor;
        outline-offset: 2px;
    }
    .page-table-wrapper {
        transition: opacity 200ms ease;
    }
    .reloading {
        opacity: 0.55;
    }
    .spinning {
        display: inline-block;
        animation: all-stores-spin 800ms linear infinite;
    }
    @keyframes all-stores-spin {
        to {
            transform: rotate(360deg);
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .contacts a,
        .page-table-wrapper {
            transition: none;
        }
        .spinning {
            animation: none;
        }
    }
</style>
