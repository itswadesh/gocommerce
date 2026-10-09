<script>
    /**
     * Abandoned checkouts: every basket that sat idle past its automation's
     * threshold, what was sent about it, and the order it became.
     *
     * It is ext/cart-recovery's record, not the Carts screen's table. A cart is
     * core's and lives as long as its TTL; a recovery record is made the moment
     * a basket goes quiet for minutes, keeps the basket as it was then, and
     * outlives the cart — which is how "this one was bought through order
     * GC-000412" and "this one expired" stay answerable after core has purged
     * the basket.
     *
     * The figures above the table are `/summary` with exactly the filters the
     * table sends, so the cards can never count a different set from the rows
     * under them. Every filter, the ordering and the page live in the URL
     * (D30), and the date range is sent as instants in the reader's own day
     * rather than as bare dates the engine would read at UTC midnight.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { can, recovery } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { cycleSort, readSort, sortQuery } from "$lib/listsort.js";
    import { formatDate, formatMoney, fromMinor, pluralize, relativeTime, toMinor } from "$lib/format.js";
    import {
        RANGES,
        STATUSES,
        durationWords,
        indicatorLabel,
        indicatorTone,
        isOpen,
        kindLabel,
        rangeWindow,
        rateText,
        recoveryProgress,
        statusClass,
        statusLabel,
    } from "$lib/recovery.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { ensureSettings, settings } from "$lib/settings.svelte.js";
    import { rightLabel } from "$lib/rights.js";
    import { rowKey } from "$lib/rowkey.js";
    import { copyText } from "$lib/clipboard.js";
    import { rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import RecoverySendDialog from "$lib/components/RecoverySendDialog.svelte";
    import RecoverySuppressDialog from "$lib/components/RecoverySuppressDialog.svelte";
    import Select from "$lib/components/Select.svelte";
    import SortHeader from "$lib/components/SortHeader.svelte";

    const PER_PAGE = 25;
    const SORT_FIELDS = ["value", "abandoned_at"];

    /* Every default is "everything": the screen opens on the whole record, and
       a filter is something the operator adds and can see they added. */
    const list = listState({
        range: "all",
        from: "",
        to: "",
        kind: "",
        status: "",
        reachable: "",
        channel: "",
        q: "",
        min_value_minor: "",
        max_value_minor: "",
        sort: "",
        order: "",
        page: 1,
        limit: PER_PAGE,
    });
    const perPage = $derived(list.params.limit);
    const sort = $derived(readSort(list.params, SORT_FIELDS));

    const readable = $derived(can("abandonment.read"));
    /* Trusted only where the store could have been asked: the probe is made for
       an operator holding this right or orders.read (modules.svelte.js), and
       for anybody else the rights answer below is the true one. */
    const missing = $derived(
        modulesKnown() && !hasModule("cart-recovery") && (readable || can("orders.read")),
    );
    /* Disabled with the reason rather than hidden: an operator whose role
       lacks the right should learn the action exists and why it is not theirs. */
    const mayContact = $derived(can("abandonment.contact"));
    const maySuppress = $derived(can("abandonment.suppress"));
    const noContactRight = `Your role does not carry “${rightLabel("abandonment.contact")}”`;
    const noSuppressRight = `Your role does not carry “${rightLabel("abandonment.suppress")}”`;

    let rows = $state([]);
    let meta = $state(null);
    let summary = $state(null);
    let loading = $state(true);
    /* The automation, for the empty state: what the thresholds are and whether
       anything is switched on. Optional — the list stands without it. */
    let automation = $state(null);

    /* The record a row action is opening a dialog for, fetched on demand: the
       dialogs need the record's own `recovery` block, which a list row does
       not carry. */
    let dialogRecord = $state(null);
    let sendOpen = $state(false);
    let suppressOpen = $state(false);
    /** The row a menu action is waiting on, so its button can say so. */
    let pending = $state(0);

    let draftSearch = $state(list.params.q);

    const statusSet = $derived(new Set(list.params.status ? list.params.status.split(",") : []));
    const currency = $derived(settings.currency);

    $effect(() => {
        ensureSettings();
    });

    $effect(() => {
        list.params;
        load();
    });

    $effect(() => {
        if (readable && hasModule("cart-recovery") && !automation) {
            recovery
                .settings()
                .then((s) => (automation = s))
                .catch(() => {});
        }
    });

    /** The filters, as the API takes them — shared by the rows and the cards. */
    function filters() {
        const p = list.params;
        const win = rangeWindow(p.range, p.from, p.to);
        return {
            from: win.from,
            to: win.to,
            kind: p.kind,
            status: p.status,
            reachable: p.reachable,
            channel: p.channel,
            q: p.q,
            min_value_minor: p.min_value_minor,
            max_value_minor: p.max_value_minor,
        };
    }

    /* Two replies in flight after a fast second click: the table settles on
       the filter that is lit, not on whichever answer arrived last. */
    let reqId = 0;

    async function load() {
        if (!readable || !hasModule("cart-recovery")) {
            loading = false;
            return;
        }
        const mine = ++reqId;
        loading = true;
        const f = filters();
        try {
            const [page, figures] = await Promise.all([
                recovery.list({ ...f, ...sortQuery(sort), page: list.page, limit: perPage }),
                recovery.summary(f),
            ]);
            if (mine !== reqId) return;
            rows = page.data ?? [];
            meta = page.meta;
            summary = figures;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    function sortBy(field, firstDesc) {
        list.set(cycleSort(sort, field, firstDesc));
    }

    function submitSearch(event) {
        event.preventDefault();
        list.set({ q: draftSearch.trim() });
    }

    function clearSearch() {
        draftSearch = "";
        list.set({ q: "" });
    }

    function clearAll() {
        draftSearch = "";
        list.clear();
    }

    function toggleStatus(status) {
        const next = new Set(statusSet);
        if (next.has(status)) next.delete(status);
        else next.add(status);
        // In the engine's own order, so the same set is always the same URL.
        list.set({ status: STATUSES.filter((s) => next.has(s)).join(",") });
    }

    function chooseRange(value) {
        if (value === "custom") {
            const win = rangeWindow(list.params.range === "custom" ? "30d" : list.params.range);
            list.set({ range: "custom", from: list.params.from || win.fromCivil, to: list.params.to || win.toCivil });
            return;
        }
        list.set({ range: value, from: "", to: "" });
    }

    /* A typed amount, in the store's currency, into minor units for the URL.
       A box that will not parse is refused here rather than sent as nothing. */
    let valueError = $state("");
    function setValue(key, typed) {
        const raw = typed.trim();
        if (!raw) {
            valueError = "";
            list.set({ [key]: "" });
            return;
        }
        const minor = toMinor(raw, currency);
        if (minor === null || minor < 0) {
            valueError = `“${raw}” is not an amount`;
            return;
        }
        valueError = "";
        list.set({ [key]: String(minor) });
    }

    const valueText = (minor) => (minor === "" ? "" : fromMinor(Number(minor), currency));

    /* ------------------------------------------------------------ rows */

    function open(row) {
        goto(`${base}/dash/abandoned/${row.id}`);
    }

    /* A click on the row opens it unless it landed on something of its own —
       the order link, the actions menu and everything inside it. */
    function rowClick(event, row) {
        if (event.target.closest("a, button, .dropdown")) return;
        open(row);
    }

    function closeMenu(id) {
        document.getElementById(`abandoned-menu-${id}`)?.hidePopover();
    }

    const linkable = (row) => row.status !== "recovered" && row.status !== "expired";

    function hasActions(row) {
        return isOpen(row.status) || linkable(row) || !!row.recovered_order;
    }

    async function startDialog(row, which) {
        closeMenu(row.id);
        pending = row.id;
        try {
            dialogRecord = await recovery.get(row.id);
            if (which === "send") sendOpen = true;
            else suppressOpen = true;
        } catch (err) {
            toast.error(err);
        } finally {
            pending = 0;
        }
    }

    async function copyLink(row) {
        closeMenu(row.id);
        pending = row.id;
        try {
            const { url, tracked } = await recovery.link(row.id);
            if (await copyText(url)) {
                toast.success(
                    tracked
                        ? "Recovery link copied"
                        : "Recovery link copied. Clicks on it are not counted: the store has no public address set.",
                );
            } else {
                // The link is still the operator's; put it where it can be
                // selected rather than claiming a copy that did not happen.
                toast.info(`Copy the link by hand: ${url}`, 15000);
            }
        } catch (err) {
            toast.error(err);
            if (err.status === 409) load();
        } finally {
            pending = 0;
        }
    }

    function afterSent() {
        sendOpen = false;
        load();
    }

    function afterSuppressed() {
        suppressOpen = false;
        load();
    }

    const cards = $derived([
        {
            label: "Abandoned",
            value: summary && String(summary.abandoned),
            hint: summary ? `${summary.reachable} with an email address` : "",
        },
        {
            label: "Potential revenue",
            value: summary && formatMoney(summary.potential),
            hint: "What these baskets were worth",
        },
        {
            label: "Recovery emails sent",
            value: summary && String(summary.messages_sent),
            hint: summary ? `To ${summary.contacted} ${pluralize(summary.contacted, "basket")}` : "",
        },
        {
            label: "Recovered orders",
            value: summary && String(summary.recovered),
            hint: summary ? `${summary.recovered_after_message} after an email` : "",
        },
        {
            label: "Recovered revenue",
            value: summary && formatMoney(summary.recovered_revenue),
            hint: summary ? `${formatMoney(summary.recovered_after_message_revenue)} after an email` : "",
        },
        {
            label: "Recovery rate",
            value: summary && rateText(summary.recovery_rate_bp),
            hint: "Recovered ÷ abandoned",
        },
    ]);

    /* Which sequences are off, for the empty state's one sentence. */
    const offSides = $derived.by(() => {
        const s = automation?.settings;
        if (!s) return [];
        const out = [];
        if (!s.checkout?.enabled) out.push("checkouts");
        if (!s.cart?.enabled) out.push("carts");
        return out;
    });
</script>

<svelte:head><title>Abandoned checkouts · GoCommerce</title></svelte:head>

{#if !readable && !missing}
    <NoAccess right="abandonment.read" what="abandoned checkouts" />
{:else}
    <div class="page page-abandoned shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Abandoned checkouts</div>
                </nav>

                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        disabled={loading}
                        onclick={load}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

                {#if !missing}
                    <form class="fields searchbar" onsubmit={submitSearch}>
                        <div class="field">
                            <input
                                type="text"
                                class="p-l-20"
                                placeholder="Search by email"
                                aria-label="Search by email"
                                bind:value={draftSearch}
                            />
                        </div>
                        {#if draftSearch || list.params.q}
                            <div class="field addon p-r-5">
                                {#if draftSearch.trim() !== list.params.q}
                                    <button type="submit" class="btn sm pill warning">Search</button>
                                {/if}
                                <button type="button" class="btn sm pill secondary transparent" onclick={clearSearch}>
                                    Clear
                                </button>
                            </div>
                        {/if}
                    </form>

                    <div class="page-header-primary-btns">
                        <a class="btn sm secondary" href="{base}/dash/marketing/recovery">
                            <i class="ri-bar-chart-2-line" aria-hidden="true"></i>
                            <span class="txt">Analytics</span>
                        </a>
                        <a class="btn sm secondary" href="{base}/dash/marketing/automations">
                            <i class="ri-flow-chart" aria-hidden="true"></i>
                            <span class="txt">Automations</span>
                        </a>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing
                    module="cart-recovery"
                    what="Abandoned checkouts are recorded by ext/cart-recovery, and this binary does not have it."
                />
            {:else}
                <!-- The tiles: uppercase muted label, big tabular figure, small
                     muted subline (DESIGN.md §7), on bg-card so they lift in
                     dark. Two across on a phone, three on a tablet, six wide. -->
                <div class="recovery-tiles tw:mb-4 tw:grid tw:grid-cols-2 tw:gap-3 tw:md:grid-cols-3 tw:xl:grid-cols-6">
                    {#each cards as card (card.label)}
                        <div class="recovery-tile tw:flex tw:min-h-[96px] tw:flex-col tw:justify-between tw:rounded-xl tw:border tw:bg-card tw:p-4">
                            <span class="tw:text-xs tw:font-medium tw:tracking-wide tw:text-muted-foreground tw:uppercase">
                                {card.label}
                            </span>
                            <div>
                                {#if loading && !summary}
                                    <div class="tw:h-7 tw:w-20 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                                {:else}
                                    <!-- Keyed on the figure, so a filter that
                                         changes it shows the change happening
                                         rather than swapping a number silently. -->
                                    {#key card.value}
                                        <div class="recovery-figure tw:text-xl tw:font-bold tw:tracking-tight tw:tabular-nums tw:sm:text-2xl">
                                            {card.value ?? "—"}
                                        </div>
                                    {/key}
                                {/if}
                                <p class="tw:mt-1 tw:text-xs tw:text-muted-foreground">{card.hint}</p>
                            </div>
                        </div>
                    {/each}
                </div>

                <div class="recovery-filters" role="group" aria-label="Filters">
                    <div class="field">
                        <Select ariaLabel="Abandoned" value={list.params.range} options={RANGES} onchange={chooseRange} />
                    </div>
                    {#if list.params.range === "custom"}
                        <div class="field">
                            <input
                                type="date"
                                aria-label="Abandoned from"
                                value={list.params.from}
                                onchange={(e) => list.set({ from: e.currentTarget.value })}
                            />
                        </div>
                        <div class="field">
                            <input
                                type="date"
                                aria-label="Abandoned to, inclusive"
                                value={list.params.to}
                                onchange={(e) => list.set({ to: e.currentTarget.value })}
                            />
                        </div>
                    {/if}
                    <div class="field">
                        <Select
                            ariaLabel="Type"
                            value={list.params.kind}
                            options={[
                                { value: "", label: "Checkouts and carts" },
                                { value: "checkout", label: "Checkouts" },
                                { value: "cart", label: "Carts" },
                            ]}
                            onchange={(v) => list.set({ kind: v })}
                        />
                    </div>
                    <div class="field">
                        <Select
                            ariaLabel="Reachable"
                            value={list.params.reachable}
                            options={[
                                { value: "", label: "Anyone" },
                                { value: "true", label: "With an email" },
                                { value: "false", label: "Without an email" },
                            ]}
                            onchange={(v) => list.set({ reachable: v })}
                        />
                    </div>
                    <div class="field">
                        <Select
                            ariaLabel="Messages"
                            value={list.params.channel}
                            options={[
                                { value: "", label: "Emailed or not" },
                                { value: "email", label: "Emailed" },
                            ]}
                            onchange={(v) => list.set({ channel: v })}
                        />
                    </div>
                    <div class="field recovery-value">
                        <input
                            type="text"
                            inputmode="decimal"
                            placeholder="Min value"
                            aria-label="Minimum value"
                            value={valueText(list.params.min_value_minor)}
                            onchange={(e) => setValue("min_value_minor", e.currentTarget.value)}
                        />
                    </div>
                    <div class="field recovery-value">
                        <input
                            type="text"
                            inputmode="decimal"
                            placeholder="Max value"
                            aria-label="Maximum value"
                            value={valueText(list.params.max_value_minor)}
                            onchange={(e) => setValue("max_value_minor", e.currentTarget.value)}
                        />
                    </div>
                    {#if !list.pristine}
                        <button type="button" class="btn sm transparent secondary" onclick={clearAll}>
                            <i class="ri-close-line" aria-hidden="true"></i>
                            <span class="txt">Clear filters</span>
                        </button>
                    {/if}
                </div>
                {#if valueError}
                    <div class="field-help error recovery-value-error" transition:rise>{valueError}</div>
                {/if}

                <!-- Status is several at once, so it is a row of toggles rather
                     than a select: "contacted or recovered" is a real question. -->
                <div class="recovery-chips" role="group" aria-label="Status">
                    <button
                        type="button"
                        class="btn sm pill secondary recovery-chip"
                        class:transparent={statusSet.size > 0}
                        aria-pressed={statusSet.size === 0}
                        onclick={() => list.set({ status: "" })}
                    >
                        All statuses
                    </button>
                    {#each STATUSES as s (s)}
                        <button
                            type="button"
                            class="btn sm pill secondary recovery-chip"
                            class:transparent={!statusSet.has(s)}
                            aria-pressed={statusSet.has(s)}
                            onclick={() => toggleStatus(s)}
                        >
                            <span class="recovery-dot {statusClass(s) || 'neutral'}" aria-hidden="true"></span>
                            {statusLabel(s)}
                        </button>
                    {/each}
                </div>

                <div class="page-table-wrapper tw:rounded-xl tw:border">
                    <table class="table responsive-table recovery-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-name-id">Customer</th>
                                <th class="col-field-type-number min-width">Items</th>
                                <th class="col-field-type-select min-width">Type</th>
                                <SortHeader
                                    field="value"
                                    label="Value"
                                    {sort}
                                    firstDesc
                                    class="col-field-type-number min-width"
                                    onsort={sortBy}
                                />
                                <SortHeader
                                    field="abandoned_at"
                                    label="Abandoned"
                                    {sort}
                                    firstDesc
                                    class="col-field-type-date min-width"
                                    onsort={sortBy}
                                />
                                <th class="col-field-type-text min-width">Recovery</th>
                                <th class="col-field-type-select min-width">Status</th>
                                <th class="col-field-type-text min-width">Recovered order</th>
                                <th class="col-meta min-width"><span class="tw:sr-only">Actions</span></th>
                            </tr>
                        </thead>
                        <tbody>
                            {#each rows as row (row.id)}
                                {@const progress = recoveryProgress(row)}
                                <tr
                                    class="handle"
                                    tabindex="0"
                                    onclick={(e) => rowClick(e, row)}
                                    onkeydown={(e) => rowKey(e, () => open(row))}
                                >
                                    <td class="col-field-name-id" data-name="Customer">
                                        <div class="row-name row-name-stacked">
                                            {#if row.email}
                                                <a
                                                    class="txt-bold txt-ellipsis recovery-row-link"
                                                    href="{base}/dash/abandoned/{row.id}"
                                                >
                                                    {row.email}
                                                </a>
                                            {:else}
                                                <a class="recovery-row-link" href="{base}/dash/abandoned/{row.id}">
                                                    <span class="txt-hint">No email</span>
                                                </a>
                                            {/if}
                                            <span class="txt-hint txt-sm">
                                                #{row.id} · {row.kind === "checkout"
                                                    ? "left at checkout"
                                                    : "left before checkout"}
                                            </span>
                                        </div>
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Items">
                                        {row.item_count}
                                        <span class="txt-hint txt-sm">{pluralize(row.item_count, "item")}</span>
                                    </td>
                                    <td class="col-field-type-select min-width" data-name="Type">
                                        <span class="label">{kindLabel(row.kind)}</span>
                                    </td>
                                    <td class="col-field-type-number min-width txt-bold" data-name="Value">
                                        {formatMoney(row.subtotal)}
                                    </td>
                                    <td
                                        class="col-field-type-date min-width txt-hint"
                                        data-name="Abandoned"
                                        title={formatDate(row.abandoned_at)}
                                    >
                                        {relativeTime(row.abandoned_at)}
                                    </td>
                                    <td class="col-field-type-text min-width" data-name="Recovery">
                                        <div class:txt-hint={!row.email}>{progress.text}</div>
                                        {#if progress.hint}
                                            <div class="txt-hint txt-sm" title={row.next_step_at ? formatDate(row.next_step_at) : undefined}>
                                                {progress.hint}
                                            </div>
                                        {/if}
                                    </td>
                                    <td class="col-field-type-select min-width" data-name="Status">
                                        <span class="label {statusClass(row.status)}">{statusLabel(row.status)}</span>
                                        <!-- The one secondary fact, under the
                                             badge rather than in it — except
                                             where it would only repeat it. -->
                                        {#if row.indicator && !(row.indicator === "purchased" || (row.indicator === "scheduled" && row.status === "scheduled"))}
                                            <div class="txt-sm {indicatorTone(row.indicator)}">
                                                {indicatorLabel(row.indicator)}
                                            </div>
                                        {/if}
                                    </td>
                                    <td class="col-field-type-text min-width" data-name="Recovered order">
                                        {#if row.recovered_order}
                                            <a class="recovery-order-link txt-code" href="{base}/dash/orders/{row.recovered_order.id}">
                                                {row.recovered_order.number}
                                            </a>
                                            <div class="txt-hint txt-sm">{formatMoney(row.recovered_order.total)}</div>
                                        {:else}
                                            <span class="txt-hint">—</span>
                                        {/if}
                                    </td>
                                    <td class="col-meta min-width row-actions">
                                        {#if hasActions(row)}
                                            <button
                                                type="button"
                                                class="btn circle sm transparent secondary"
                                                class:loading={pending === row.id}
                                                disabled={pending === row.id}
                                                popovertarget="abandoned-menu-{row.id}"
                                                aria-haspopup="menu"
                                                aria-label="Actions for basket #{row.id}"
                                                title="Actions"
                                            >
                                                <i class="ri-more-2-fill" aria-hidden="true"></i>
                                            </button>
                                            <div id="abandoned-menu-{row.id}" class="dropdown sm nowrap" popover="auto" role="menu">
                                                {#if isOpen(row.status) && row.email}
                                                    <button
                                                        type="button"
                                                        role="menuitem"
                                                        class="dropdown-item"
                                                        disabled={!mayContact}
                                                        title={mayContact ? undefined : noContactRight}
                                                        onclick={() => startDialog(row, "send")}
                                                    >
                                                        <i class="ri-mail-send-line" aria-hidden="true"></i>
                                                        <span class="txt">Send recovery…</span>
                                                    </button>
                                                {/if}
                                                {#if linkable(row)}
                                                    <button
                                                        type="button"
                                                        role="menuitem"
                                                        class="dropdown-item"
                                                        disabled={!mayContact}
                                                        title={mayContact ? undefined : noContactRight}
                                                        onclick={() => copyLink(row)}
                                                    >
                                                        <i class="ri-link" aria-hidden="true"></i>
                                                        <span class="txt">Copy recovery link</span>
                                                    </button>
                                                {/if}
                                                {#if isOpen(row.status)}
                                                    <button
                                                        type="button"
                                                        role="menuitem"
                                                        class="dropdown-item"
                                                        disabled={!maySuppress}
                                                        title={maySuppress ? undefined : noSuppressRight}
                                                        onclick={() => startDialog(row, "suppress")}
                                                    >
                                                        <i class="ri-forbid-line" aria-hidden="true"></i>
                                                        <span class="txt">Suppress…</span>
                                                    </button>
                                                {/if}
                                                {#if row.recovered_order}
                                                    <a
                                                        role="menuitem"
                                                        class="dropdown-item"
                                                        href="{base}/dash/orders/{row.recovered_order.id}"
                                                    >
                                                        <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
                                                        <span class="txt">Open order {row.recovered_order.number}</span>
                                                    </a>
                                                {/if}
                                            </div>
                                        {:else}
                                            <i class="ri-arrow-right-s-line txt-hint" aria-hidden="true"></i>
                                        {/if}
                                    </td>
                                </tr>
                            {/each}

                            {#if loading && !rows.length}
                                {#each Array(6) as _, i (i)}
                                    <tr><td colspan="9"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {:else if !rows.length}
                                <tr>
                                    <td colspan="9" class="txt-center txt-hint p-base">
                                        <div class="recovery-empty" in:rise>
                                            <div class="m-b-10">
                                                <i class="ri-shopping-cart-2-line" style="font-size: 32px" aria-hidden="true"></i>
                                            </div>
                                            {#if list.pristine}
                                                <p class="tw:mx-auto tw:max-w-[62ch]">
                                                    <strong>Nothing has been left behind yet.</strong>
                                                    A basket lands here once it has sat untouched past its
                                                    automation's threshold{#if automation}
                                                        — {durationWords(automation.settings.checkout.abandon_after_minutes)}
                                                        for a checkout, {durationWords(automation.settings.cart.abandon_after_minutes)}
                                                        for a cart{/if} — whether or not anything is sent about it.
                                                </p>
                                                {#if offSides.length}
                                                    <p class="m-t-sm">
                                                        Recovery emails are switched off for {offSides.join(" and ")}.
                                                        <a class="recovery-arrow-link" href="{base}/dash/marketing/automations">
                                                            Turn them on <span aria-hidden="true">→</span>
                                                        </a>
                                                    </p>
                                                {/if}
                                            {:else}
                                                <p>No basket matches these filters.</p>
                                                <button type="button" class="btn sm secondary m-t-sm" onclick={clearAll}>
                                                    <i class="ri-close-line" aria-hidden="true"></i>
                                                    <span class="txt">Clear filters</span>
                                                </button>
                                            {/if}
                                        </div>
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
                        noun="basket"
                        {perPage}
                        onpage={(n) => list.setPage(n)}
                        onperpage={(n) => list.set({ limit: n })}
                    />
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>

    <RecoverySendDialog
        open={sendOpen}
        record={dialogRecord}
        onclose={() => (sendOpen = false)}
        onsent={afterSent}
        onrefresh={load}
    />
    <RecoverySuppressDialog
        open={suppressOpen}
        record={dialogRecord}
        onclose={() => (suppressOpen = false)}
        ondone={afterSuppressed}
        onrefresh={load}
    />
{/if}
