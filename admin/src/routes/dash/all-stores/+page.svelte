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
    import { formatDate, relativeTime } from "$lib/format.js";
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

    // The audit trail writes sentences and the event log writes order.placed.
    function activityName(name) {
        if (name.includes(" ")) return name;
        const words = name.replace(/[._]/g, " ");
        return words.charAt(0).toUpperCase() + words.slice(1);
    }
</script>

{#snippet contact(href, text, verified)}
    <span class="contact">
        <a {href}>{text}</a>
        <span class="proof" class:proved={verified}>
            <i class={verified ? "ri-checkbox-circle-fill" : "ri-question-line"} aria-hidden="true"></i>
            {verified ? "Verified" : "Not verified"}
        </span>
    </span>
{/snippet}

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
                            <th>Domain</th>
                            <th>Legal name</th>
                            <th>Owner</th>
                            <th class="col-field-type-number min-width">Products</th>
                            <th class="col-field-type-number min-width">Orders</th>
                            <th class="min-width">Last activity</th>
                            <th class="min-width">Created</th>
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
                                <td data-name="Domain">
                                    {#if store.domain}
                                        <a class="domain" href="https://{store.domain}" target="_blank" rel="noopener noreferrer">{store.domain}</a>
                                    {:else}
                                        —
                                    {/if}
                                </td>
                                <td data-name="Legal name">{store.legal_name || "—"}</td>
                                <td data-name="Owner">
                                    <div class="owners">
                                        {#each store.owners as owner, i (i)}
                                            <div class="contacts">
                                                {#if owner.phone}{@render contact(`tel:${owner.phone}`, owner.phone, owner.phone_verified)}{/if}
                                                {#if owner.email}{@render contact(`mailto:${owner.email}`, owner.email, owner.email_verified)}{/if}
                                            </div>
                                        {:else}
                                            —
                                        {/each}
                                    </div>
                                </td>
                                <td class="col-field-type-number min-width" data-name="Products">{store.products.toLocaleString("en-IN")}</td>
                                <td class="col-field-type-number min-width" data-name="Orders">{store.orders.toLocaleString("en-IN")}</td>
                                <td class="activity" data-name="Last activity">
                                    {#if store.last_activity_at}
                                        <div class="row-name row-name-stacked">
                                            <span class="txt-nowrap" title={formatDate(store.last_activity_at)}>{relativeTime(store.last_activity_at)}</span>
                                            <span class="txt-hint">{activityName(store.last_activity)}</span>
                                        </div>
                                    {:else}
                                        —
                                    {/if}
                                </td>
                                <td class="min-width txt-nowrap" data-name="Created" title={formatDate(store.created_at)}>{relativeTime(store.created_at)}</td>
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
        gap: 4px;
        overflow-wrap: anywhere;
    }
    .contact {
        display: flex;
        flex-wrap: wrap;
        align-items: baseline;
        column-gap: 8px;
    }
    .activity {
        min-width: 200px;
    }
    .proof {
        font-size: 0.85em;
        white-space: nowrap;
        opacity: 0.75;
    }
    .proof.proved {
        color: var(--successColor);
        opacity: 1;
    }
    .contacts a,
    .domain {
        transition: opacity 150ms ease;
    }
    .contacts a:hover,
    .contacts a:active,
    .domain:hover,
    .domain:active {
        opacity: 0.65;
    }
    .contacts a:focus-visible,
    .domain:focus-visible {
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
        .domain,
        .page-table-wrapper {
            transition: none;
        }
        .spinning {
            animation: none;
        }
    }
</style>
