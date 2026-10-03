<script>
    /**
     * Leads: enquiries from the store's dealer form that were routed to this
     * company, by territory or by hand from the store. A dealer's admins and
     * approvers work them here and report back where each one got to; the
     * store reads that status on its side.
     *
     * A lead opens beside the list rather than on a page of its own, so
     * working down a morning's enquiries never loses the place in the list.
     */
    import { base } from "$app/paths";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { rowKey } from "$lib/rowkey.js";
    import { query } from "$lib/api.js";
    import { countryName, leadPlace } from "$lib/b2b.js";
    import { trade, tradeApi, explain, isApprover, word, LEAD_WORDS, refreshBadges } from "$lib/trade.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import PortalPage from "../PortalPage.svelte";

    const list = listState({ status: "", page: 1, limit: 25 });

    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let failure = $state(null);
    let saving = $state("");
    let reqId = 0;

    const TABS = [{ value: "", label: "All" }, ...Object.entries(LEAD_WORDS).map(([value, w]) => ({ value, label: w.label }))];

    $effect(() => {
        list.params;
        load();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        failure = null;
        try {
            const p = list.params;
            const r = await tradeApi.list("/x/b2b/leads" + query({ status: p.status, page: p.page, limit: p.limit }));
            if (mine !== reqId) return;
            rows = r.data;
            meta = r.meta;
        } catch (err) {
            if (mine === reqId && !err.handled) failure = err;
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    let openId = $state(0);
    const current = $derived(openId ? rows.find((l) => l.id === openId) ?? null : null);

    function closeLead() {
        openId = 0;
    }

    async function setStatus(lead, status) {
        if (lead.status === status) return;
        saving = status;
        try {
            const updated = await tradeApi.patch(`/x/b2b/leads/${lead.id}`, { status });
            rows = rows.map((r) => (r.id === lead.id ? { ...r, ...updated } : r));
            toast.success(`${lead.name || "The lead"} is marked ${word(LEAD_WORDS, status).label.toLowerCase()}.`);
            refreshBadges();
        } catch (err) {
            if (!err.handled) toast.error(explain(err));
        } finally {
            saving = "";
        }
    }
</script>

<PortalPage title="Leads">
    {#if !isApprover()}
        <div class="portal-empty-state tw:rounded-xl tw:border tw:bg-card">
            <i class="ri-user-received-2-line" aria-hidden="true"></i>
            <h2>Leads are for account admins and approvers</h2>
            <p class="txt-hint">Ask one of {trade.me.company.name}'s admins if you should be answering enquiries.</p>
        </div>
    {:else}
        <p class="field-help m-b-base portal-intro">
            People near you who asked {trade.store.name || "the store"} about buying. Get in touch, then mark how it went —
            the store sees where each one got to.
        </p>

        <div class="portal-tabs m-b-base" role="tablist" aria-label="Which leads">
            {#each TABS as t (t.value)}
                <button
                    type="button"
                    role="tab"
                    class="portal-tab"
                    class:active={list.params.status === t.value}
                    aria-selected={list.params.status === t.value}
                    onclick={() => list.set({ status: t.value })}>{t.label}</button
                >
            {/each}
        </div>

        {#if failure}
            <div class="alert danger m-b-base portal-notice" role="alert">
                <p>{explain(failure)}</p>
                <button type="button" class="btn sm secondary portal-press" onclick={load}><span class="txt">Try again</span></button>
            </div>
        {/if}

        <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && rows.length > 0}>
            <table class="table responsive-table">
                <thead>
                    <tr>
                        <th class="col-field-name-id">Who</th>
                        <th>Where</th>
                        <th>Asked about</th>
                        <th class="min-width">Status</th>
                    </tr>
                </thead>
                <tbody>
                    {#if loading && !rows.length}
                        {#each Array(3) as _, i (i)}
                            <tr><td colspan="4"><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {/if}
                    {#each rows as l (l.id)}
                        {@const s = word(LEAD_WORDS, l.status)}
                        <tr
                            class="handle"
                            class:portal-row-open={current?.id === l.id}
                            tabindex="0"
                            onclick={() => (openId = l.id)}
                            onkeydown={(e) => rowKey(e, () => (openId = l.id))}
                        >
                            <td class="col-field-name-id" data-name="Who">
                                <div class="row-name row-name-stacked">
                                    <span class="txt-bold">{l.name || l.email || l.phone}</span>
                                    <span class="txt-hint txt-sm txt-ellipsis b2b-snippet">{l.message || l.email || l.phone}</span>
                                </div>
                            </td>
                            <td data-name="Where">{leadPlace(l) || "—"}</td>
                            <td data-name="Asked about">
                                {#if l.product_title}{l.product_title}{:else}<span class="txt-hint">Nothing in particular</span>{/if}
                            </td>
                            <td class="min-width" data-name="Status">
                                <span class="label {s.tone}">{s.label}</span>
                                <div class="txt-hint txt-sm" title={formatDate(l.created_at)}>{relativeTime(l.created_at)}</div>
                            </td>
                        </tr>
                    {/each}
                    {#if !loading && !rows.length && !failure}
                        <tr>
                            <td colspan="4" class="txt-hint txt-center p-base">
                                {list.params.status
                                    ? `No ${word(LEAD_WORDS, list.params.status).label.toLowerCase()} leads.`
                                    : "No enquiries yet. They arrive here when someone in your area asks the store about buying."}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <Pager {meta} {loading} noun="lead" perPage={list.params.limit} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
            <div class="flex-fill"></div>
        </footer>
    {/if}
</PortalPage>

<Drawer open={!!current} size="sm" title={current ? current.name || "Enquiry" : "Enquiry"} onclose={closeLead}>
    {#if current}
        {@const s = word(LEAD_WORDS, current.status)}
        <div class="portal-lead">
            <div class="tw:flex tw:flex-wrap tw:items-center tw:gap-2 m-b-sm">
                <span class="label {s.tone}">{s.label}</span>
                <span class="txt-hint txt-sm">{formatDate(current.created_at)}</span>
            </div>
            <dl class="b2b-facts b2b-facts-stacked">
                {#if current.email}
                    <div>
                        <dt>Email</dt>
                        <dd><a href="mailto:{current.email}">{current.email}</a></dd>
                    </div>
                {/if}
                {#if current.phone}
                    <div>
                        <dt>Phone</dt>
                        <dd><a href="tel:{current.phone}">{current.phone}</a></dd>
                    </div>
                {/if}
                <div>
                    <dt>Where</dt>
                    <dd>{[current.postal_code, current.state, countryName(current.country)].filter(Boolean).join(", ") || "—"}</dd>
                </div>
                <div>
                    <dt>Asked about</dt>
                    <dd>
                        {current.product_title || "Nothing in particular"}
                        {#if current.variant_sku}<span class="txt-hint txt-code"> {current.variant_sku}</span>{/if}
                    </dd>
                </div>
                <div class="b2b-fact-wide">
                    <dt>Sent to you</dt>
                    <dd>{current.routed_by === "store" ? `By ${trade.store.name || "the store"}` : "Because it's in your area"}</dd>
                </div>
            </dl>
            <h3 class="b2b-eyebrow m-t-base">What they wrote</h3>
            {#if current.message}
                <blockquote class="b2b-note">{current.message}</blockquote>
            {:else}
                <p class="txt-hint m-0">No message — just their details.</p>
            {/if}

            <h3 class="b2b-eyebrow m-t-base">Where it got to</h3>
            <div class="portal-choices portal-status-choices" role="radiogroup" aria-label="Where it got to">
                {#each Object.entries(LEAD_WORDS) as [value, w] (value)}
                    <button
                        type="button"
                        role="radio"
                        aria-checked={current.status === value}
                        class="portal-choice portal-choice-btn"
                        class:selected={current.status === value}
                        class:busy={saving === value}
                        disabled={!!saving}
                        onclick={() => setStatus(current, value)}
                    >
                        <span class="portal-choice-body">
                            <span class="txt-bold">{w.label}</span>
                            <span class="txt-hint txt-sm">{w.hint}</span>
                        </span>
                        {#if current.status === value}<i class="ri-check-line portal-choice-tick" aria-hidden="true"></i>{/if}
                    </button>
                {/each}
            </div>
        </div>
    {/if}
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={closeLead}><span class="txt">Close</span></button>
        {#if current?.email}
            <a class="btn portal-press" href="mailto:{current.email}"><i class="ri-mail-line" aria-hidden="true"></i><span class="txt">Email them</span></a>
        {:else if current?.phone}
            <a class="btn portal-press" href="tel:{current.phone}"><i class="ri-phone-line" aria-hidden="true"></i><span class="txt">Call them</span></a>
        {/if}
    {/snippet}
</Drawer>
