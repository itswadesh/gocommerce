<script>
    /**
     * Every store on the install, for its owner alone (D215). The engine is what
     * refuses everybody else; the sidebar only hides the link.
     *
     * "Legal name" is what a store typed as its registered business. A person
     * has no name in the engine, so there is no owner's name to show here.
     */
    import { isApexDomain } from "$lib/apex.js";
    import { stores } from "$lib/api.js";
    import { titled } from "$lib/brand.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { rise } from "$lib/motion.js";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import OwnerEmailDialog from "$lib/components/OwnerEmailDialog.svelte";

    let loading = $state(true);
    let refused = $state(false);
    let failed = $state("");
    let list = $state([]);
    let apexOnly = $state(false);
    /** The owner the email dialog is open for. */
    let writingTo = $state(null);

    // Every domain the store has, verified or not: one still being set up is
    // still the store's own (D225).
    const hasApex = (store) => isApexDomain(store.domain) || store.domains.some((d) => isApexDomain(d.host));
    const shown = $derived(apexOnly ? list.filter(hasApex) : list);
    // Domain is the address the shop answers on; the rest are worth showing too.
    const otherDomains = (store) => store.domains.filter((d) => d.host !== store.domain);

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

    /* Every row of that person, as they can own several stores. */
    function emailSent(id, sent) {
        for (const store of list) for (const owner of store.owners) if (owner.id === id) owner.email_sent = sent;
    }

    // The link in an email works for 7 days (ownerVerifyTTL).
    const linkExpired = (sent) => Date.now() - new Date(sent.sent_at).getTime() > 7 * 24 * 60 * 60 * 1000;

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
                    All stores{#if !loading && !refused && !failed}<span class="store-total"> {shown.length}</span>{/if}
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
            <div class="field apex-filter">
                <input id="apex-only" type="checkbox" bind:checked={apexOnly} />
                <label for="apex-only">Apex domains only</label>
            </div>
            {#if shown.length === 0}
                <EmptyState icon="ri-filter-off-line" title="No stores on an apex domain" clearLabel="Show all stores" onclear={() => (apexOnly = false)} />
            {:else}
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
                        {#each shown as store (store.id)}
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
                                    <div class="contacts">
                                        {#if store.domain}
                                            <a class="domain" href="https://{store.domain}" target="_blank" rel="noopener noreferrer">{store.domain}</a>
                                        {/if}
                                        {#each otherDomains(store) as other (other.host)}
                                            <span class="contact">
                                                {#if other.verified}
                                                    <a class="domain" href="https://{other.host}" target="_blank" rel="noopener noreferrer">{other.host}</a>
                                                {:else}
                                                    <span>{other.host}</span>
                                                    <span class="proof">
                                                        <i class="ri-question-line" aria-hidden="true"></i>
                                                        Not verified
                                                    </span>
                                                {/if}
                                            </span>
                                        {/each}
                                        {#if !store.domain && store.domains.length === 0}—{/if}
                                    </div>
                                </td>
                                <td data-name="Legal name">{store.legal_name || "—"}</td>
                                <td data-name="Owner">
                                    <div class="owners">
                                        {#each store.owners as owner, i (i)}
                                            <div class="contacts">
                                                {#if owner.phone}{@render contact(`tel:${owner.phone}`, owner.phone, owner.phone_verified)}{/if}
                                                {#if owner.email}{@render contact(`mailto:${owner.email}`, owner.email, owner.email_verified)}{/if}
                                                {#if owner.email && !owner.email_verified}
                                                    <button
                                                        type="button"
                                                        class="btn sm secondary verify"
                                                        aria-label="Write to {owner.email} to verify it"
                                                        onclick={() => (writingTo = { superuser_id: owner.id, store_id: store.id, email: owner.email, store: store.name || store.slug })}
                                                    >
                                                        <i class="ri-mail-send-line" aria-hidden="true"></i>
                                                        <span class="txt">{owner.email_sent ? "Send again" : "Verify email"}</span>
                                                    </button>
                                                {/if}
                                            </div>
                                            {#if owner.email_sent && !owner.email_verified}
                                                <div class="sent-email" in:rise>
                                                    <p class="sent-status" title={formatDate(owner.email_sent.sent_at)}>
                                                        <i class="ri-mail-check-line" aria-hidden="true"></i>
                                                        Sent {relativeTime(owner.email_sent.sent_at)}.
                                                        {linkExpired(owner.email_sent) ? "The link has expired, so send it again." : "Waiting for them to click the link."}
                                                    </p>
                                                    <p class="sent-subject">{owner.email_sent.subject}</p>
                                                    <p class="sent-body">{owner.email_sent.body}</p>
                                                </div>
                                            {/if}
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
        {/if}
    </div>
</div>

<OwnerEmailDialog target={writingTo} onclose={() => (writingTo = null)} onsent={(sent) => emailSent(writingTo.superuser_id, sent)} />

<style>
    .verify {
        align-self: flex-start;
    }
    .apex-filter {
        margin-bottom: 12px;
    }
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
    /* What went out, in the words it went out in, while it waits on them. */
    .sent-email {
        min-width: 240px;
        max-width: 340px;
        margin-top: 4px;
        padding: 8px 10px;
        font-size: 0.85em;
        overflow-wrap: anywhere;
        border-left: 2px solid var(--borderColor, currentColor);
    }
    .sent-email p {
        margin: 0;
    }
    .sent-status {
        opacity: 0.75;
    }
    .sent-subject {
        margin-top: 6px !important;
        font-weight: 600;
    }
    .sent-body {
        margin-top: 4px !important;
        white-space: pre-wrap;
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
