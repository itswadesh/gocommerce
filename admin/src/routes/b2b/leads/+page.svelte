<script>
    /**
     * Leads: members of the public asking where to buy, and the dealer each
     * one was sent to.
     *
     * The storefront's dealer form routes an enquiry to the dealer whose
     * territory covers the address. What nobody covers stays with the store,
     * marked Unrouted, and that is the part of this screen that is work: the
     * store reads it and either answers it or hands it to a dealer. Handing a
     * lead over emails that dealer and starts it again at New, which the
     * drawer says before Save rather than after.
     *
     * One filter for "whose leads": every lead, the store's own, or one
     * dealer's. The engine refuses unrouted and company_id together, and a
     * single select cannot express both, so the screen never asks it — and a
     * URL carrying both is read as unrouted.
     *
     * The open lead rides in the URL as `?lead=`, so a lead can be sent to
     * somebody: a link to one is read through the single-lead route, and a
     * row opened from the list uses the row it already has.
     */
    import { untrack } from "svelte";
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { page } from "$app/state";
    import { api, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import {
        LEAD_STATUSES,
        dealerOption,
        leadPlace,
        leadStatusClass,
        leadStatusLabel,
        loadDealers,
        routedByLabel,
    } from "$lib/b2b.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    const PER_PAGE = 50;
    const list = listState({ q: "", status: "", company_id: "", unrouted: false, routed_by: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);

    const readable = $derived(can("leads.read"));
    const writable = $derived(can("leads.write"));
    /* Dealers are companies, read under their own right. Without it the screen
       still lists and re-statuses leads; it just cannot name a dealer to pick. */
    const companiesReadable = $derived(can("companies.read"));
    const missing = $derived(modulesKnown() && !hasModule("b2b"));

    let leads = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let reqId = 0;

    let dealers = $state([]);
    let dealersMore = $state(false);
    let dealersLoaded = $state(false);

    let drawerOpen = $state(false);
    let lead = $state(null);
    let form = $state({ status: "new", dealer: "" });
    let saving = $state(false);
    let saveError = $state("");
    let draft = $state(list.params.q);

    /* Keyed on the values rather than on the params object: opening a lead
       changes the URL, which rebuilds that object with the same values, and
       the list must not be fetched again for it. */
    const listKey = $derived(JSON.stringify(list.params));
    $effect(() => {
        listKey;
        if (readable && hasModule("b2b")) untrack(load);
    });

    /* "Unrouted" is one question however the URL spells it. The engine refuses
       it alongside a dealer or another route, so here it wins over both and
       neither is sent with it. */
    const unrouted = $derived(list.params.unrouted || list.params.routed_by === "unrouted");

    const linked = $derived(page.url.searchParams.get("lead") ?? "");
    $effect(() => {
        const id = linked;
        if (!readable || !hasModule("b2b")) return;
        untrack(() => {
            if (!id) {
                if (drawerOpen) drawerOpen = false;
            } else if (!drawerOpen || String(lead?.id) !== id) {
                fetchLead(id);
            }
        });
    });

    $effect(() => {
        if (readable && companiesReadable && hasModule("b2b") && !dealersLoaded) fetchDealers();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const p = list.params;
            const result = await api.get(
                "/api/admin/x/b2b/leads" +
                    query({
                        q: p.q,
                        status: p.status,
                        unrouted: unrouted ? "true" : "",
                        company_id: unrouted ? "" : p.company_id,
                        routed_by: unrouted ? "" : p.routed_by,
                        page: p.page,
                        limit: p.limit,
                    }),
            );
            if (mine !== reqId) return;
            leads = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    async function fetchDealers() {
        dealersLoaded = true;
        try {
            const got = await loadDealers(api);
            dealers = got.dealers;
            dealersMore = got.more;
        } catch (err) {
            dealersLoaded = false;
            toast.error(err);
        }
    }

    /* A dealer named by the URL or by a lead but beyond the first page of
       companies still gets an option, so the select can show what is chosen. */
    function withKnown(options, id, name) {
        if (!id || options.some((o) => o.value === String(id))) return options;
        return [...options, { value: String(id), label: name || `Company ${id}`, short: name || `Company ${id}` }];
    }

    const whose = $derived(unrouted ? "unrouted" : list.params.company_id);
    const whoseOptions = $derived(
        withKnown(
            [
                { value: "", label: "Every lead" },
                { value: "unrouted", label: "Unrouted — the store's own", short: "Unrouted" },
                ...dealers.map(dealerOption),
            ],
            unrouted ? "" : list.params.company_id,
            leads.find((l) => String(l.company_id) === list.params.company_id)?.company_name,
        ),
    );

    function chooseWhose(v) {
        if (v === "unrouted") list.set({ unrouted: true, company_id: "", routed_by: "" });
        else
            list.set({
                unrouted: false,
                company_id: v,
                routed_by: list.params.routed_by === "unrouted" ? "" : list.params.routed_by,
            });
    }

    const ROUTE_OPTIONS = [
        { value: "", label: "However it arrived", short: "Any route" },
        { value: "territory", label: "Matched a dealer's territory", short: "By territory" },
        { value: "store", label: "Handed over by the store", short: "By the store" },
    ];

    /* A route is a question about leads a dealer has, so choosing one takes
       the place of "Unrouted" rather than being sent beside it. */
    function chooseRoute(v) {
        list.set({ routed_by: v, unrouted: v ? false : list.params.unrouted });
    }

    function search(event) {
        event.preventDefault();
        list.set({ q: draft.trim() });
    }

    const STATUS_OPTIONS = [
        { value: "", label: "Every status" },
        ...LEAD_STATUSES.map((s) => ({ value: s.value, label: `${s.label} — ${s.hint}`, short: s.label })),
    ];

    function show(l) {
        lead = l;
        form = { status: l.status, dealer: l.company_id ? String(l.company_id) : "" };
        saveError = "";
        drawerOpen = true;
    }

    function setLinked(id) {
        const url = new URL(page.url);
        if (id) url.searchParams.set("lead", String(id));
        else url.searchParams.delete("lead");
        goto(url, { replaceState: true, keepFocus: true, noScroll: true });
    }

    function openLead(l) {
        show(l);
        setLinked(l.id);
    }

    function closeLead() {
        drawerOpen = false;
        setLinked("");
    }

    async function fetchLead(id) {
        try {
            show(await api.get(`/api/admin/x/b2b/leads/${id}`));
        } catch (err) {
            toast.error(err);
            setLinked("");
        }
    }

    const currentDealer = $derived(lead?.company_id ? String(lead.company_id) : "");
    const statusChanged = $derived(!!lead && form.status !== lead.status);
    const dealerChanged = $derived(!!lead && form.dealer !== currentDealer);
    const chosenDealer = $derived(dealers.find((d) => String(d.id) === form.dealer) ?? null);
    const dealerPicker = $derived(
        withKnown(
            [{ value: "", label: "Nobody — keep it with the store", short: "Nobody (the store)" }, ...dealers.map(dealerOption)],
            lead?.company_id,
            lead?.company_name,
        ),
    );

    async function save(event) {
        event?.preventDefault();
        if (saving || !writable || !lead || (!statusChanged && !dealerChanged)) return;
        saveError = "";
        const body = {};
        if (statusChanged) body.status = form.status;
        if (dealerChanged) body.company_id = form.dealer ? Number(form.dealer) : null;
        saving = true;
        try {
            const updated = await api.patch(`/api/admin/x/b2b/leads/${lead.id}`, body);
            if (dealerChanged) {
                toast.success(
                    updated.company_id
                        ? `Sent to ${updated.company_name}`
                        : `Taken back from ${lead.company_name || "the dealer"}`,
                );
            } else {
                toast.success(`Marked ${leadStatusLabel(updated.status).toLowerCase()}`);
            }
            closeLead();
            await load();
        } catch (err) {
            // A closed dealer is a 409 naming them; it belongs beside the
            // picker that chose them, not only in a toast that fades.
            saveError = err.message;
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    const who = (l) => l.name || l.email || l.phone || `Enquiry ${l.id}`;
</script>

<svelte:head><title>Leads · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="leads.read" what="dealer leads" />
{:else}
    <div class="page page-leads b2b-page shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Customers</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Leads</div>
                </nav>
                <div class="flex-fill"></div>
                {#if !missing}
                    <div class="page-header-primary-btns b2b-filters">
                        <div class="field b2b-filter">
                            <Select
                                ariaLabel="Whose leads"
                                value={whose}
                                options={whoseOptions}
                                onchange={chooseWhose}
                            />
                        </div>
                        <div class="field b2b-filter">
                            <Select
                                ariaLabel="How it arrived"
                                value={unrouted ? "" : list.params.routed_by}
                                options={ROUTE_OPTIONS}
                                disabled={unrouted}
                                onchange={chooseRoute}
                            />
                        </div>
                        <div class="field b2b-filter">
                            <Select
                                ariaLabel="Status"
                                value={list.params.status}
                                options={STATUS_OPTIONS}
                                onchange={(v) => list.set({ status: v })}
                            />
                        </div>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="b2b" what="Leads are served by ext/b2b, and this binary does not have it." />
            {:else}
                <p class="field-help m-b-base">
                    Enquiries from the storefront's dealer form. Each goes to the dealer whose territory
                    covers the address, and the dealer reports back how it went. One nobody covers stays
                    with the store as <strong>Unrouted</strong> — answer it, or hand it to a dealer.
                    {#if dealersMore}
                        Past the first 200 companies, only dealers are offered to pick from.
                    {/if}
                </p>

                <form class="fields m-b-base" onsubmit={search} role="search">
                    <div class="field">
                        <label for="lead-search">Search</label>
                        <input
                            id="lead-search"
                            type="search"
                            placeholder="Name, message, postcode, email or phone"
                            autocomplete="off"
                            bind:value={draft}
                            oninput={(e) => list.set({ q: e.currentTarget.value.trim() })}
                        />
                    </div>
                </form>

                <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && leads.length > 0}>
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-name-id">Enquiry</th>
                                <th class="min-width">Contact</th>
                                <th class="min-width">From</th>
                                <th>Dealer</th>
                                <th class="min-width">Status</th>
                                <th class="min-width">Received</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#if loading && !leads.length}
                                {#each Array(4) as _, i (i)}
                                    <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {/if}
                            {#each leads as l (l.id)}
                                <tr
                                    class="handle"
                                    tabindex="0"
                                    onclick={() => openLead(l)}
                                    onkeydown={(e) => rowKey(e, () => openLead(l))}
                                >
                                    <td class="col-field-name-id" data-name="Enquiry">
                                        <div class="row-name row-name-stacked">
                                            <span class="txt-bold">{who(l)}</span>
                                            {#if l.message}
                                                <span class="txt-hint txt-sm txt-ellipsis b2b-snippet" title={l.message}>{l.message}</span>
                                            {/if}
                                        </div>
                                    </td>
                                    <td class="min-width txt-sm" data-name="Contact">
                                        {#if l.email}<div>{l.email}</div>{/if}
                                        {#if l.phone}<div class="txt-hint">{l.phone}</div>{/if}
                                    </td>
                                    <td class="min-width txt-sm" data-name="From">
                                        {leadPlace(l) || "—"}
                                    </td>
                                    <td data-name="Dealer">
                                        {#if l.company_id}
                                            {#if companiesReadable}
                                                <a href="{base}/b2b/companies/{l.company_id}" onclick={(e) => e.stopPropagation()}
                                                    >{l.company_name}</a
                                                >
                                            {:else}
                                                {l.company_name}
                                            {/if}
                                            <div class="txt-hint txt-sm">{routedByLabel(l.routed_by)}</div>
                                        {:else}
                                            <span class="label label-warning">Unrouted</span>
                                        {/if}
                                    </td>
                                    <td class="min-width" data-name="Status">
                                        <span class="label {leadStatusClass(l.status)}">{leadStatusLabel(l.status)}</span>
                                    </td>
                                    <td class="min-width txt-hint txt-sm" data-name="Received" title={formatDate(l.created_at)}>
                                        {relativeTime(l.created_at)}
                                    </td>
                                </tr>
                            {/each}
                            {#if !loading && !leads.length}
                                <tr>
                                    <td colspan="6" class="txt-hint txt-center p-base">
                                        {#if unrouted && !list.params.status && !list.params.q}
                                            Nothing is unrouted — every enquiry so far reached a dealer.
                                        {:else if !list.pristine}
                                            No lead matches that.
                                        {:else}
                                            No enquiries yet. They arrive from the storefront's dealer form
                                            (<code>POST /x/b2b/leads</code>).
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
                        noun="lead"
                        {perPage}
                        onpage={(n) => list.setPage(n)}
                        onperpage={(n) => list.set({ limit: n })}
                    />
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>

    <Drawer open={drawerOpen} size="sm" title={lead ? who(lead) : "Lead"} onclose={closeLead}>
        {#if lead}
            <div class="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
                <span class="label {leadStatusClass(lead.status)}">{leadStatusLabel(lead.status)}</span>
                <span class="txt-hint txt-sm">Received {formatDate(lead.created_at)}</span>
            </div>

            <h3 class="b2b-eyebrow tw:mt-4">What they asked</h3>
            {#if lead.message}
                <blockquote class="b2b-note">{lead.message}</blockquote>
            {:else}
                <p class="txt-hint tw:m-0 tw:text-sm">No message — just their details.</p>
            {/if}

            <dl class="b2b-facts b2b-facts-stacked">
                <div class="b2b-fact-wide">
                    <dt>Email</dt>
                    <dd>{#if lead.email}<a href="mailto:{lead.email}">{lead.email}</a>{:else}<span class="txt-hint">—</span>{/if}</dd>
                </div>
                <div>
                    <dt>Phone</dt>
                    <dd>{#if lead.phone}<a href="tel:{lead.phone}">{lead.phone}</a>{:else}<span class="txt-hint">—</span>{/if}</dd>
                </div>
                <div>
                    <dt>From</dt>
                    <dd>{leadPlace(lead) || "No address given"}</dd>
                </div>
                {#if lead.product_id}
                    <div>
                        <dt>About</dt>
                        <dd>
                            <a href="{base}/products/{lead.product_id}">{lead.product_title || `Product ${lead.product_id}`}</a>
                            {#if lead.variant_sku}
                                <span class="txt-hint txt-code">· {lead.variant_sku}</span>
                            {:else if lead.variant_id}
                                <span class="txt-hint">· variant {lead.variant_id}</span>
                            {/if}
                        </dd>
                    </div>
                {/if}
                {#if lead.source}
                    <div>
                        <dt>Sent from</dt>
                        <dd class="txt-code">{lead.source}</dd>
                    </div>
                {/if}
                <div class="b2b-fact-wide">
                    <dt>Dealer</dt>
                    <dd>
                        {#if lead.company_id}
                            {lead.company_name}
                            <span class="txt-hint">{routedByLabel(lead.routed_by)}</span>
                        {:else}
                            Nobody — no dealer's territory covers this address, so it is the store's own.
                        {/if}
                    </dd>
                </div>
            </dl>

            {#if writable}
                <form id="lead-form" onsubmit={save} novalidate>
                    <h6 class="section-title">
                        <i class="ri-route-line" aria-hidden="true"></i>
                        Route it
                    </h6>
                    <div class="field">
                        <label for="lead-status">Status</label>
                        <Select id="lead-status" bind:value={form.status} options={STATUS_OPTIONS.slice(1)} />
                    </div>
                    <div class="field m-t-sm" class:error={!!saveError && dealerChanged}>
                        <label for="lead-dealer">Dealer</label>
                        {#if companiesReadable}
                            <Select
                                id="lead-dealer"
                                bind:value={form.dealer}
                                options={dealerPicker}
                                disabled={!dealersLoaded}
                                onchange={() => (saveError = "")}
                            />
                        {:else}
                            <input id="lead-dealer" type="text" disabled value={lead.company_name || "Nobody (the store)"} />
                        {/if}
                    </div>
                    <div class="field-help">
                        {#if !companiesReadable}
                            Your role cannot read companies, so the dealer stays as it is.
                        {:else if dealerChanged && chosenDealer?.status === "closed"}
                            <span class="txt-danger">{chosenDealer.name} is closed. The store will refuse to send it a lead.</span>
                        {:else if dealerChanged && form.dealer}
                            Handing it to {chosenDealer?.name ?? "this dealer"} emails their admins and
                            approvers{statusChanged ? "" : ", and starts it again at New"}.
                        {:else if dealerChanged}
                            It stays with the store{statusChanged ? "" : ", starting again at New"}.
                            {lead.company_name} is not told.
                        {:else}
                            Pick another dealer to hand it over, or Nobody to take it back.
                        {/if}
                    </div>
                    {#if saveError}
                        <div class="alert danger m-t-sm b2b-notice" role="alert"><p>{saveError}</p></div>
                    {/if}
                </form>
            {/if}
        {/if}

        {#snippet footer()}
            <button type="button" class="btn transparent m-r-auto" onclick={closeLead}>
                <span class="txt">{writable ? "Cancel" : "Close"}</span>
            </button>
            {#if writable}
                <button
                    type="submit"
                    form="lead-form"
                    class="btn"
                    class:loading={saving}
                    disabled={saving || (!statusChanged && !dealerChanged)}
                >
                    <span class="txt">Save</span>
                </button>
            {/if}
        {/snippet}
    </Drawer>
{/if}
