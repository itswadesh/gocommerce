<script>
    /**
     * Abandoned checkouts: every basket that sat idle past its automation's
     * threshold, what was sent about it, and the order it became.
     *
     * Laid out as kitcommerce-admin lays it out, so an operator who knows one
     * panel reads the other: a count beside the title, six figures, one card
     * of filters, and a table whose rows open the record.
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
    import { formatDate, formatMoney, fromMinor, relativeTime, toMinor } from "$lib/format.js";
    import {
        RANGES,
        STATUSES,
        customerLines,
        durationWords,
        indicatorLabel,
        isOpen,
        kindLabel,
        rangeLabel,
        rangeWindow,
        rateText,
        recoveryProgress,
        statusClass,
        statusLabel,
        valueLabel,
    } from "$lib/recovery.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { ensureSettings, settings } from "$lib/settings.svelte.js";
    import { rightLabel } from "$lib/rights.js";
    import { rowKey } from "$lib/rowkey.js";
    import { copyText } from "$lib/clipboard.js";
    import { rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import RecoveryLinkDialog from "$lib/components/RecoveryLinkDialog.svelte";
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
    /** Why the list could not be read, shown where the rows would be. */
    let failure = $state("");
    let figuresFailed = $state(false);
    /* The automation, for the empty state: what the thresholds are and whether
       anything is switched on. Optional — the list stands without it. */
    let automation = $state(null);

    /* The record a row action is opening a dialog for, fetched on demand: the
       dialogs need the record's own `recovery` block, which a list row does
       not carry. */
    let dialogRecord = $state(null);
    let sendOpen = $state(false);
    let suppressOpen = $state(false);
    /** A link the browser would not copy, shown for copying by hand. */
    let shownLink = $state(null);
    /** The row a menu action is waiting on, so its button can say so. */
    let pending = $state(0);

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
        // The figures are a second request on purpose: a list that loads with
        // its cards failed is still the list, and says the cards failed.
        const figures = recovery.summary(f).then(
            (s) => mine === reqId && ((summary = s), (figuresFailed = false)),
            () => mine === reqId && (figuresFailed = true),
        );
        try {
            const page = await recovery.list({ ...f, ...sortQuery(sort), page: list.page, limit: perPage });
            if (mine !== reqId) return;
            rows = page.data ?? [];
            meta = page.meta;
            failure = "";
        } catch (err) {
            if (mine !== reqId) return;
            if (rows.length) toast.error(err);
            else failure = err?.message || "The records could not be read.";
        } finally {
            await figures;
            if (mine === reqId) loading = false;
        }
    }

    function sortBy(field, firstDesc) {
        list.set(cycleSort(sort, field, firstDesc));
    }

    /* ------------------------------------------------------------ search */

    /* Live, as kitcommerce-admin's is: the list follows the typing once it
       pauses for 400ms, so a fast typist sends one request, not one a key. */
    let draftSearch = $state(list.params.q);
    let searchTimer;
    function typed(value) {
        draftSearch = value;
        clearTimeout(searchTimer);
        searchTimer = setTimeout(() => list.set({ q: draftSearch.trim() }), 400);
    }

    function clearAll() {
        clearTimeout(searchTimer);
        draftSearch = "";
        valueDraft = { min: "", max: "" };
        valueError = "";
        list.clear();
    }

    /* ------------------------------------------------------------ status */

    function toggleStatus(status) {
        const next = new Set(statusSet);
        if (next.has(status)) next.delete(status);
        else next.add(status);
        // In the engine's own order, so the same set is always the same URL.
        list.set({ status: STATUSES.filter((s) => next.has(s)).join(",") });
    }

    /* ------------------------------------------------------------ value */

    const valueText = (minor) => (minor === "" ? "" : fromMinor(Number(minor), currency));
    const valueMoney = (minor) => (minor === "" ? "" : formatMoney({ amount_minor: Number(minor), currency }));

    let valueDraft = $state({ min: "", max: "" });
    let valueError = $state("");

    function openValue() {
        valueDraft = { min: valueText(list.params.min_value_minor), max: valueText(list.params.max_value_minor) };
        valueError = "";
    }

    /* A typed amount, in the store's currency, into minor units for the URL.
       A box that will not parse is refused here rather than sent as nothing. */
    function minorOf(raw) {
        const text = raw.trim();
        if (!text) return "";
        const minor = toMinor(text, currency);
        if (minor === null || minor < 0) {
            valueError = `“${text}” is not an amount`;
            return null;
        }
        return String(minor);
    }

    function applyValue(event) {
        event.preventDefault();
        valueError = "";
        const min = minorOf(valueDraft.min);
        const max = minorOf(valueDraft.max);
        if (min === null || max === null) return;
        list.set({ min_value_minor: min, max_value_minor: max });
        document.getElementById("abandoned-value")?.hidePopover();
    }

    function clearValue() {
        valueDraft = { min: "", max: "" };
        valueError = "";
        list.set({ min_value_minor: "", max_value_minor: "" });
        document.getElementById("abandoned-value")?.hidePopover();
    }

    /* ------------------------------------------------------------ dates */

    let dateDraft = $state({ from: "", to: "" });

    function openDates() {
        const win = rangeWindow(list.params.range, list.params.from, list.params.to);
        dateDraft = { from: win.fromCivil, to: win.toCivil };
    }

    function choosePreset(value) {
        list.set({ range: value, from: "", to: "" });
        document.getElementById("abandoned-dates")?.hidePopover();
    }

    function applyDates(event) {
        event.preventDefault();
        if (!dateDraft.from && !dateDraft.to) {
            choosePreset("all");
            return;
        }
        list.set({ range: "custom", from: dateDraft.from, to: dateDraft.to });
        document.getElementById("abandoned-dates")?.hidePopover();
    }

    const filtered = $derived(!list.pristine);

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
            const link = await recovery.link(row.id);
            if (await copyText(link.url)) {
                toast.success(
                    link.tracked
                        ? "Recovery link copied"
                        : "Recovery link copied. Clicks on it are not counted: the store has no public address set.",
                );
            } else {
                // The link is still the operator's; put it where it can be
                // selected rather than claiming a copy that did not happen.
                shownLink = link;
            }
        } catch (err) {
            toast.error(err?.message ? err : "Could not get the recovery link");
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

    const count = (n) => new Intl.NumberFormat().format(n ?? 0);

    const cards = $derived([
        {
            label: "Abandoned",
            icon: "ri-shopping-cart-2-line",
            value: summary && count(summary.abandoned),
            hint: summary ? `${count(summary.reachable)} reachable` : "",
        },
        {
            label: "Potential revenue",
            icon: "ri-cash-line",
            value: summary && formatMoney(summary.potential),
            hint: "Value of these carts",
        },
        {
            label: "Contacted",
            icon: "ri-mail-check-line",
            value: summary && count(summary.contacted),
            hint: summary ? `${count(summary.messages_sent)} messages sent` : "",
        },
        {
            label: "Recovered",
            icon: "ri-checkbox-circle-line",
            success: true,
            value: summary && count(summary.recovered),
            hint: summary ? `${count(summary.recovered_after_message)} after a message` : "",
        },
        {
            label: "Revenue recovered",
            icon: "ri-wallet-3-line",
            success: true,
            value: summary && formatMoney(summary.recovered_revenue),
            hint: summary ? `${formatMoney(summary.recovered_after_message_revenue)} after a message` : "",
        },
        {
            label: "Recovery rate",
            icon: "ri-percent-line",
            value: summary && rateText(summary.recovery_rate_bp),
            hint: "Recovered ÷ abandoned",
        },
    ]);

    /* Which sequences are off, for the empty state's offer to switch them on. */
    const offSides = $derived.by(() => {
        const s = automation?.settings;
        if (!s) return [];
        const out = [];
        if (!s.checkout?.enabled) out.push("checkouts");
        if (!s.cart?.enabled) out.push("carts");
        return out;
    });

    const emptyText = $derived(
        "Carts and checkouts that customers leave without paying land here once they have been idle for the time set in Automations" +
            (automation
                ? ` (${durationWords(automation.settings.checkout.abandon_after_minutes)} for a checkout, ` +
                  `${durationWords(automation.settings.cart.abandon_after_minutes)} for a cart)`
                : "") +
            ". Each one shows what was sent, whether the link was clicked, and the order that recovered it." +
            (offSides.length ? ` Recovery emails are switched off for ${offSides.join(" and ")}.` : ""),
    );

    const KINDS = [
        { value: "", label: "All" },
        { value: "checkout", label: "Checkouts" },
        { value: "cart", label: "Carts" },
    ];
</script>

<svelte:head><title>Abandoned checkouts · GoCommerce</title></svelte:head>

{#if !readable && !missing}
    <NoAccess right="abandonment.read" what="abandoned checkouts" />
{:else}
    <div class="page page-abandoned shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <h1 class="recovery-h1">
                        Abandoned checkouts
                        {#if meta}
                            {#key meta.total}
                                <span class="recovery-count" in:rise>{count(meta.total)}</span>
                            {/key}
                        {/if}
                    </h1>
                </nav>

                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary recovery-refresh"
                        class:is-spinning={loading}
                        title={loading ? "Refreshing" : "Refresh"}
                        aria-label={loading ? "Refreshing" : "Refresh"}
                        disabled={loading}
                        onclick={load}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

                {#if !missing}
                    <div class="flex-fill"></div>
                    <div class="page-header-primary-btns">
                        <a class="btn sm secondary" href="{base}/dash/marketing/recovery">
                            <i class="ri-bar-chart-2-line" aria-hidden="true"></i>
                            <span class="txt">Report</span>
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
                <!-- Six figures with their icons, sentence case, two across on
                     a phone, three on a tablet and six wide; the same filters
                     as the table, so they count the rows below. -->
                <div class="recovery-tiles tw:mb-3 tw:grid tw:grid-cols-2 tw:gap-3 tw:md:grid-cols-3 tw:xl:grid-cols-6">
                    {#each cards as card (card.label)}
                        <div class="recovery-tile tw:flex tw:min-h-24 tw:flex-col tw:justify-between tw:rounded-lg tw:border tw:bg-card tw:p-3 tw:sm:p-4">
                            <span class="recovery-tile-label" class:is-success={card.success}>
                                <i class={card.icon} aria-hidden="true"></i>
                                {card.label}
                            </span>
                            <div>
                                {#if loading && !summary}
                                    <div class="tw:h-7 tw:w-20 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                                    <div class="tw:mt-2 tw:h-3 tw:w-24 tw:animate-pulse tw:rounded-md tw:bg-muted"></div>
                                {:else}
                                    <!-- Keyed on the figure, so a filter that
                                         changes it shows the change happening
                                         rather than swapping a number silently. -->
                                    {#key card.value}
                                        <div class="recovery-figure tw:text-xl tw:font-semibold tw:tracking-tight tw:tabular-nums tw:sm:text-2xl">
                                            {card.value ?? "—"}
                                        </div>
                                    {/key}
                                    <p class="tw:mt-1 tw:truncate tw:text-xs tw:text-muted-foreground">{card.hint}</p>
                                {/if}
                            </div>
                        </div>
                    {/each}
                </div>
                {#if figuresFailed}
                    <p class="recovery-figures-error" role="alert" transition:rise>
                        <i class="ri-error-warning-line" aria-hidden="true"></i>
                        Could not load the figures
                    </p>
                {/if}

                <div class="recovery-filter-card" role="group" aria-label="Filters">
                    <label class="recovery-search">
                        <i class="ri-search-line" aria-hidden="true"></i>
                        <span class="tw:sr-only">Search customers</span>
                        <input
                            type="search"
                            placeholder="Search name, email or phone"
                            value={draftSearch}
                            oninput={(e) => typed(e.currentTarget.value)}
                        />
                    </label>

                    <div class="recovery-filters">
                        <div class="recovery-seg" role="group" aria-label="Type">
                            {#each KINDS as k (k.value)}
                                <button
                                    type="button"
                                    class="recovery-seg-btn"
                                    aria-pressed={list.params.kind === k.value}
                                    onclick={() => list.set({ kind: k.value })}
                                >
                                    {k.label}
                                </button>
                            {/each}
                        </div>

                        <!-- Status is several at once — "contacted or
                             recovered" is a real question — so it is a list
                             of boxes behind one button. -->
                        <button
                            type="button"
                            class="btn sm secondary recovery-dashed"
                            popovertarget="abandoned-status"
                            aria-haspopup="true"
                        >
                            <i class="ri-add-circle-line" aria-hidden="true"></i>
                            <span class="txt">Status</span>
                            {#if statusSet.size}
                                <span class="recovery-sep" aria-hidden="true"></span>
                                {#if statusSet.size > 2}
                                    <span class="recovery-chip-sm">{statusSet.size} selected</span>
                                {:else}
                                    {#each STATUSES.filter((s) => statusSet.has(s)) as s (s)}
                                        <span class="recovery-chip-sm">{statusLabel(s)}</span>
                                    {/each}
                                {/if}
                            {/if}
                        </button>
                        <div id="abandoned-status" class="dropdown recovery-pop" popover="auto">
                            <button
                                type="button"
                                class="dropdown-item"
                                onclick={() =>
                                    list.set({ status: statusSet.size === STATUSES.length ? "" : STATUSES.join(",") })}
                            >
                                <span class="recovery-box" class:is-on={statusSet.size === STATUSES.length} aria-hidden="true">
                                    <i class="ri-check-line"></i>
                                </span>
                                <span class="txt">Select all ({STATUSES.length})</span>
                            </button>
                            {#each STATUSES as s (s)}
                                <button
                                    type="button"
                                    class="dropdown-item"
                                    role="menuitemcheckbox"
                                    aria-checked={statusSet.has(s)}
                                    onclick={() => toggleStatus(s)}
                                >
                                    <span class="recovery-box" class:is-on={statusSet.has(s)} aria-hidden="true">
                                        <i class="ri-check-line"></i>
                                    </span>
                                    <span class="recovery-dot {statusClass(s) || 'neutral'}" aria-hidden="true"></span>
                                    <span class="txt">{statusLabel(s)}</span>
                                </button>
                            {/each}
                            {#if statusSet.size}
                                <button type="button" class="dropdown-item recovery-pop-clear" onclick={() => list.set({ status: "" })}>
                                    <span class="txt">Clear selection ({statusSet.size})</span>
                                </button>
                            {/if}
                        </div>

                        <div class="field recovery-select">
                            <Select
                                ariaLabel="Channel"
                                value={list.params.channel}
                                options={[
                                    { value: "", label: "Any channel" },
                                    { value: "email", label: "Sent by email" },
                                ]}
                                onchange={(v) => list.set({ channel: v })}
                            />
                        </div>
                        <div class="field recovery-select">
                            <Select
                                ariaLabel="Contact details"
                                value={list.params.reachable}
                                options={[
                                    { value: "", label: "Anyone" },
                                    { value: "true", label: "Reachable" },
                                    { value: "false", label: "No contact details" },
                                ]}
                                onchange={(v) => list.set({ reachable: v })}
                            />
                        </div>

                        <button
                            type="button"
                            class="btn sm secondary"
                            popovertarget="abandoned-value"
                            aria-haspopup="dialog"
                            onclick={openValue}
                        >
                            <i class="ri-wallet-3-line" aria-hidden="true"></i>
                            <span class="txt">
                                {valueLabel(valueMoney(list.params.min_value_minor), valueMoney(list.params.max_value_minor))}
                            </span>
                        </button>
                        <form
                            id="abandoned-value"
                            class="dropdown recovery-pop recovery-pop-form"
                            popover="auto"
                            onsubmit={applyValue}
                        >
                            <div class="recovery-pop-title">Cart value</div>
                            <div class="recovery-pop-pair">
                                <div class="field">
                                    <label for="abandoned-min">Min</label>
                                    <input id="abandoned-min" type="text" inputmode="decimal" placeholder="0" bind:value={valueDraft.min} />
                                </div>
                                <div class="field">
                                    <label for="abandoned-max">Max</label>
                                    <input id="abandoned-max" type="text" inputmode="decimal" placeholder="Any" bind:value={valueDraft.max} />
                                </div>
                            </div>
                            {#if valueError}
                                <div class="field-help error" transition:rise>{valueError}</div>
                            {/if}
                            <div class="recovery-pop-actions">
                                <button type="button" class="btn sm transparent secondary" onclick={clearValue}>
                                    <span class="txt">Clear</span>
                                </button>
                                <button type="submit" class="btn sm">
                                    <span class="txt">Apply</span>
                                </button>
                            </div>
                        </form>

                        <button
                            type="button"
                            class="btn sm secondary"
                            popovertarget="abandoned-dates"
                            aria-haspopup="dialog"
                            onclick={openDates}
                        >
                            <i class="ri-calendar-line" aria-hidden="true"></i>
                            <span class="txt">{rangeLabel(list.params.range, list.params.from, list.params.to)}</span>
                        </button>
                        <form
                            id="abandoned-dates"
                            class="dropdown recovery-pop recovery-pop-form"
                            popover="auto"
                            onsubmit={applyDates}
                        >
                            <div class="recovery-pop-title">Abandoned</div>
                            <div class="recovery-presets">
                                {#each RANGES.filter((r) => r.value !== "custom") as r (r.value)}
                                    <button
                                        type="button"
                                        class="btn xs transparent secondary"
                                        aria-pressed={list.params.range === r.value}
                                        onclick={() => choosePreset(r.value)}
                                    >
                                        {r.value === "all" ? "Any date" : r.label}
                                    </button>
                                {/each}
                            </div>
                            <div class="recovery-pop-pair">
                                <div class="field">
                                    <label for="abandoned-from">From</label>
                                    <input id="abandoned-from" type="date" bind:value={dateDraft.from} />
                                </div>
                                <div class="field">
                                    <label for="abandoned-to">To</label>
                                    <input id="abandoned-to" type="date" bind:value={dateDraft.to} />
                                </div>
                            </div>
                            <div class="recovery-pop-actions">
                                <button type="submit" class="btn sm">
                                    <span class="txt">Apply</span>
                                </button>
                            </div>
                        </form>

                        {#if filtered}
                            <button type="button" class="btn sm transparent secondary" onclick={clearAll} transition:rise>
                                <i class="ri-close-line" aria-hidden="true"></i>
                                <span class="txt">Clear filters</span>
                            </button>
                        {/if}
                    </div>
                </div>

                <div class="page-table-wrapper tw:rounded-lg tw:border recovery-table-wrap" class:is-refetching={loading && rows.length}>
                    {#if loading && rows.length}
                        <div class="recovery-progress" aria-hidden="true"></div>
                    {/if}
                    <table class="table responsive-table recovery-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-name-id">Customer</th>
                                <th class="col-field-type-number min-width">Items</th>
                                <th class="col-field-type-select min-width">Type</th>
                                <SortHeader
                                    field="value"
                                    label="Cart value"
                                    {sort}
                                    firstDesc
                                    class="col-field-type-number min-width txt-right"
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
                                {@const who = customerLines(row)}
                                {@const progress = recoveryProgress(row)}
                                <tr
                                    class="handle"
                                    tabindex="0"
                                    onclick={(e) => rowClick(e, row)}
                                    onkeydown={(e) => rowKey(e, () => open(row))}
                                >
                                    <td class="col-field-name-id" data-name="Customer">
                                        <div class="row-name row-name-stacked recovery-who">
                                            <a
                                                class="txt-ellipsis recovery-row-link"
                                                class:txt-hint={who.guest}
                                                href="{base}/dash/abandoned/{row.id}"
                                            >
                                                {who.top}
                                            </a>
                                            {#if who.hint}
                                                <span class="txt-hint txt-sm txt-ellipsis">{who.hint}</span>
                                            {/if}
                                        </div>
                                    </td>
                                    <td class="col-field-type-number min-width tw:tabular-nums" data-name="Items">
                                        {row.item_count}
                                    </td>
                                    <td class="col-field-type-select min-width" data-name="Type">
                                        {kindLabel(row.kind)}
                                    </td>
                                    <td class="col-field-type-number min-width txt-right tw:tabular-nums" data-name="Cart value">
                                        {formatMoney(row.subtotal)}
                                    </td>
                                    <td class="col-field-type-date min-width" data-name="Abandoned">
                                        <time datetime={row.abandoned_at} title={formatDate(row.abandoned_at)}>
                                            {relativeTime(row.abandoned_at)}
                                        </time>
                                    </td>
                                    <td class="col-field-type-text min-width" data-name="Recovery">
                                        <div class:txt-hint={progress.muted}>{progress.text}</div>
                                        {#if progress.hint}
                                            <div class="txt-hint txt-sm" title={row.next_step_at ? formatDate(row.next_step_at) : undefined}>
                                                {progress.hint}
                                            </div>
                                        {/if}
                                    </td>
                                    <td class="col-field-type-select min-width" data-name="Status">
                                        <span class="recovery-pill {statusClass(row.status) || 'neutral'}">
                                            <span class="recovery-pill-dot" aria-hidden="true"></span>
                                            {statusLabel(row.status)}
                                        </span>
                                        {#if row.indicator || (row.suppression && row.status !== "suppressed")}
                                            <div class="txt-hint txt-sm recovery-pill-hint">
                                                {[
                                                    indicatorLabel(row.indicator),
                                                    row.suppression && row.status !== "suppressed" ? "Was suppressed" : "",
                                                ]
                                                    .filter(Boolean)
                                                    .join(" · ")}
                                            </div>
                                        {/if}
                                    </td>
                                    <td class="col-field-type-text min-width" data-name="Recovered order">
                                        {#if row.recovered_order}
                                            <a class="recovery-order-link" href="{base}/dash/orders/{row.recovered_order.id}">
                                                {row.recovered_order.number}
                                            </a>
                                        {:else}
                                            <span class="txt-hint" aria-hidden="true">—</span>
                                            <span class="tw:sr-only">None</span>
                                        {/if}
                                    </td>
                                    <td class="col-meta min-width row-actions">
                                        <button
                                            type="button"
                                            class="btn circle sm transparent secondary"
                                            class:loading={pending === row.id}
                                            disabled={pending === row.id}
                                            popovertarget="abandoned-menu-{row.id}"
                                            aria-haspopup="menu"
                                            aria-label="Row actions"
                                            title="Actions"
                                        >
                                            <i class="ri-more-fill" aria-hidden="true"></i>
                                        </button>
                                        <div id="abandoned-menu-{row.id}" class="dropdown sm nowrap" popover="auto" role="menu">
                                            <a role="menuitem" class="dropdown-item" href="{base}/dash/abandoned/{row.id}">
                                                <i class="ri-eye-line" aria-hidden="true"></i>
                                                <span class="txt">View details</span>
                                            </a>
                                            {#if isOpen(row.status) && row.email}
                                                <button
                                                    type="button"
                                                    role="menuitem"
                                                    class="dropdown-item"
                                                    disabled={!mayContact}
                                                    title={mayContact ? undefined : noContactRight}
                                                    onclick={() => startDialog(row, "send")}
                                                >
                                                    <i class="ri-send-plane-line" aria-hidden="true"></i>
                                                    <span class="txt">Send recovery</span>
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
                                                    <span class="txt">Suppress</span>
                                                </button>
                                            {/if}
                                            {#if row.email}
                                                <a
                                                    role="menuitem"
                                                    class="dropdown-item"
                                                    href="{base}/dash/customers?q={encodeURIComponent(row.email)}"
                                                >
                                                    <i class="ri-user-3-line" aria-hidden="true"></i>
                                                    <span class="txt">Open customer</span>
                                                </a>
                                            {/if}
                                            <a role="menuitem" class="dropdown-item" href="{base}/dash/checkouts?state=any&cart={row.cart_id}">
                                                <i class="ri-shopping-cart-2-line" aria-hidden="true"></i>
                                                <span class="txt">Open cart</span>
                                            </a>
                                            {#if row.recovered_order}
                                                <a
                                                    role="menuitem"
                                                    class="dropdown-item"
                                                    href="{base}/dash/orders/{row.recovered_order.id}"
                                                >
                                                    <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
                                                    <span class="txt">Open recovered order</span>
                                                </a>
                                            {/if}
                                        </div>
                                    </td>
                                </tr>
                            {/each}

                            {#if loading && !rows.length && !failure}
                                {#each Array(8) as _, i (i)}
                                    <tr><td colspan="9"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {:else if failure && !rows.length}
                                <tr class="empty-row">
                                    <td colspan="9">
                                        <EmptyState icon="ri-error-warning-line" title="Abandoned checkouts couldn't load" text={failure}>
                                            <button type="button" class="btn secondary" onclick={load}>
                                                <i class="ri-refresh-line" aria-hidden="true"></i>
                                                <span class="txt">Try again</span>
                                            </button>
                                        </EmptyState>
                                    </td>
                                </tr>
                            {:else if !rows.length}
                                <tr class="empty-row">
                                    <td colspan="9">
                                        {#if !filtered}
                                            <EmptyState icon="ri-inbox-line" title="Nothing here yet" text={emptyText}>
                                                {#if offSides.length}
                                                    <a class="btn secondary" href="{base}/dash/marketing/automations">
                                                        <i class="ri-mail-send-line" aria-hidden="true"></i>
                                                        <span class="txt">Turn on recovery emails</span>
                                                    </a>
                                                {/if}
                                            </EmptyState>
                                        {:else}
                                            <EmptyState
                                                icon="ri-filter-off-line"
                                                title="No results match your filters"
                                                text="Try a different search, or clear the filters to see everything."
                                                onclear={clearAll}
                                            />
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
                        noun="cart"
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
    <RecoveryLinkDialog link={shownLink} onclose={() => (shownLink = null)} />
{/if}
