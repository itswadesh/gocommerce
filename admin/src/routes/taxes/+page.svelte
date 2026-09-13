<script>
    /**
     * Tax rates: rules about a place and a kind of thing.
     *
     * The listing arrives ordered the way the engine resolves them — most
     * specific first — so an operator wondering which rule wins can read it off
     * the page instead of reproducing the resolution in their head. That is the
     * default and the fallback, not a fixture: a store modelling fifty US
     * states also has to be able to read the table by place, by rate or by
     * name, and clearing the sort puts resolution order back.
     */
    import { api, can } from "$lib/api.js";
    import { onNewShortcut } from "$lib/shortcuts.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { readSort, cycleSort } from "$lib/listsort.js";
    import { pageSlice } from "$lib/clientpage.js";
    import { selection } from "$lib/selection.svelte.js";
    import { runBulk } from "$lib/bulk.js";
    import { toast } from "$lib/toast.svelte.js";
    import { pluralize } from "$lib/format.js";
    import { settings, ensureSettings } from "$lib/settings.svelte.js";
    import { COUNTRIES } from "$lib/countries.js";
    import BulkBar from "$lib/components/BulkBar.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import CategoryPicker from "$lib/components/CategoryPicker.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import RecordHistory from "$lib/components/RecordHistory.svelte";
    import Select from "$lib/components/Select.svelte";
    import SortHeader from "$lib/components/SortHeader.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    let loading = $state(true);
    let rates = $state([]);
    let categories = $state([]);
    let categoriesTruncated = $state(false);

    let open = $state(false);
    let editing = $state(null);
    let form = $state(blank());
    let saving = $state(false);
    let togglingID = $state(null);

    let confirmOpen = $state(false);
    let confirmConfig = $state({});

    /* Who changed this rate, from the row it is about. The route is gated on
       taxes.read — the right this screen is already drawn under — rather than
       on store.operate, which is the whole point of it: "who moved Karnataka
       to 12%" is a question the person who owns the rate table has to be able
       to answer without being able to read the store-wide feed. */
    let historyOpen = $state(false);
    let historyFor = $state(null);

    function openHistory(r, event) {
        event?.stopPropagation();
        historyFor = r;
        historyOpen = true;
    }

    const mayRead = $derived(can("taxes.read"));
    const mayWrite = $derived(can("taxes.write"));

    /* `n` creates one, from anywhere on this screen. The shell owns the
       keystroke and fires an event; what "new" means is the screen's. */
    $effect(() => {
        if (!mayWrite) return;
        return onNewShortcut(openNew);
    });

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL. */
    const PER_PAGE = 50;

    /* The listing is unpaginated by design — the engine resolves rates in one
       pass and sends the lot — so the filter is a filter over what is already
       here rather than a query. It still lives in the URL: a store modelling
       fifty US states has a screen worth linking somebody to, and so is the
       page, for the same reason. */
    const list = listState({ q: "", sort: "", order: "", page: 1, limit: PER_PAGE });
    const filter = $derived(list.params.q);
    const perPage = $derived(list.params.limit);
    let draftFilter = $state(list.params.q);

    /*
     * A rate table means one thing if the price on the product page already has
     * the tax in it and the opposite if it does not, and this screen never said
     * which. The setting is read-only configuration the binary was started
     * with, served by GET /api/admin/settings.
     */
    const inclusive = $derived(settings.pricesIncludeTax);

    /* Country is rendered with the empty option first, because "everywhere" is
       the fallback rule every other rate is more specific than — not a missing
       value. */
    const COUNTRY_OPTIONS = [{ value: "", label: "Anywhere" }, ...COUNTRIES];

    const COUNTRY_NAMES = new Map(COUNTRIES.map((c) => [c.value, c.label]));

    /** "IN" as "India", so the Where column can be read without the table. */
    function countryName(code) {
        return COUNTRY_NAMES.get(code) || code;
    }

    const visible = $derived.by(() => {
        const needle = filter.trim().toLowerCase();
        if (!needle) return rates;
        return rates.filter((r) =>
            [
                r.name,
                r.country,
                countryName(r.country),
                r.state,
                r.category_name,
                r.active ? "on active" : "off inactive",
                `${r.rate_bp / 100}%`,
            ]
                .filter(Boolean)
                .join(" ")
                .toLowerCase()
                .includes(needle),
        );
    });

    /*
     * Ordering, which is done here for the same reason the paging is: there is
     * no sort parameter on the wire. GET /api/admin/tax-rates answers with the
     * whole table in resolution order and takes nothing that would change it
     * (core/taxes_http.go mounts list/create/get/patch/delete and no more), so
     * `sort` and `order` are two more keys in this screen's own URL state and
     * are deliberately never sent — `load()` still fetches the bare route.
     *
     * Ordering the rows an operator can see is not a second implementation of
     * anything: resolution is the engine's, it is the order the rows arrive in,
     * and clearing the sort is what gets it back.
     */
    const SORT_FIELDS = ["name", "applies", "where", "rate", "active"];
    const sort = $derived(readSort(list.params, SORT_FIELDS));

    function sortBy(field, firstDesc) {
        list.set(cycleSort(sort, field, firstDesc));
    }

    /* Numeric collation, so "State 2" comes before "State 10" — a rate table
       built per state or per slab is full of names that end in a number. */
    const collate = (a, b) =>
        String(a ?? "").localeCompare(String(b ?? ""), undefined, {
            numeric: true,
            sensitivity: "base",
        });

    /*
     * Compared on what the cell actually reads rather than on the field behind
     * it: Where renders "India · KA" and an empty country as "Everywhere", and
     * Applies to renders a rule with no category as "Everything". An operator
     * clicking a header is ordering the words in front of them, and a hidden
     * key that sorts "Everywhere" somewhere other than under E would be a table
     * that disagrees with itself.
     */
    const COMPARE = {
        name: (a, b) => collate(a.name, b.name),
        applies: (a, b) =>
            collate(a.category_name || "Everything", b.category_name || "Everything"),
        where: (a, b) => collate(where(a), where(b)),
        rate: (a, b) => a.rate_bp - b.rate_bp,
        active: (a, b) => Number(!!a.active) - Number(!!b.active),
    };

    const ordered = $derived.by(() => {
        const compare = COMPARE[sort.field];
        if (!compare) return visible;
        /*
         * A copy, because `visible` IS `rates` when nothing is filtered and
         * sorting in place would destroy the resolution order this screen falls
         * back to. The direction is a sign on the comparator rather than a
         * reverse() afterwards: Array#sort is stable, so rows the column cannot
         * tell apart keep the order they are applied in — and reversing would
         * throw that away in exactly the case it matters, a column of fifty
         * identical rates.
         */
        const sign = sort.desc ? -1 : 1;
        return [...visible].sort((a, b) => sign * compare(a, b));
    });

    /*
     * The page is cut here rather than asked for, because there is nothing to
     * ask: GET /api/admin/tax-rates answers with the whole table and reports
     * ListMeta{Total: len, Limit: len} (core/taxes_http.go) — the resolver
     * reads every rate in one pass, and the order they arrive in is the order
     * they are applied in. A store modelling fifty US states still needs to
     * reach rate fifty-one, and until this the footer said "50 of 63 shown"
     * with no way to see the other thirteen.
     *
     * Cut from `ordered`, never from `visible`: slicing first and sorting after
     * would order fifty rows out of sixty-three and call it the top of the
     * table.
     */
    const paged = $derived(pageSlice(ordered, { page: list.page, limit: perPage }));

    $effect(() => {
        load();
        ensureSettings();
    });

    async function load() {
        // The screen is refused above; asking anyway would put a 403 toast
        // over the explanation.
        if (!mayRead) {
            loading = false;
            return;
        }
        loading = true;
        try {
            const [rateResult, catResult] = await Promise.all([
                api.get("/api/admin/tax-rates"),
                api.get("/api/admin/categories?flat=1"),
            ]);
            rates = rateResult.data ?? [];
            categories = catResult.data ?? [];
            categoriesTruncated = (catResult.meta?.total ?? 0) > categories.length;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function blank() {
        return {
            name: "",
            percent: "18",
            country: "",
            state: "",
            category_id: null,
            active: true,
        };
    }

    function openNew() {
        editing = null;
        form = blank();
        open = true;
    }

    function openEdit(r) {
        editing = r;
        form = {
            name: r.name,
            percent: String(r.rate_bp / 100),
            country: r.country ?? "",
            state: r.state ?? "",
            category_id: r.category_id ?? null,
            active: r.active,
        };
        open = true;
    }

    async function save(event) {
        event?.preventDefault();
        saving = true;
        const body = {
            name: form.name.trim(),
            // Typed as people say it, stored as basis points.
            rate_bp: Math.round((parseFloat(form.percent) || 0) * 100),
            country: form.country.trim(),
            // Folded, because the engine matches a state literally: "ka",
            // "KA" and " KA " would otherwise be three rules that each fire on
            // a different spelling of the same place.
            state: form.state.trim().toUpperCase(),
            category_id: form.category_id,
            active: form.active,
        };
        try {
            if (editing) {
                await api.patch(`/api/admin/tax-rates/${editing.id}`, body);
                toast.success("Rate updated");
            } else {
                await api.post("/api/admin/tax-rates", body);
                toast.success("Rate created");
            }
            open = false;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function askDelete(r) {
        confirmConfig = {
            title: `Delete ${r.name}?`,
            message:
                "Orders already placed keep what they were charged. Nothing new will be taxed " +
                "under this rule.",
            confirmLabel: "Delete",
            danger: true,
            run: async () => {
                try {
                    await api.delete(`/api/admin/tax-rates/${r.id}`);
                    toast.success("Rate deleted");
                    await load();
                } catch (err) {
                    toast.error(err);
                }
            },
        };
        confirmOpen = true;
    }

    /**
     * Turning a rate off from the row.
     *
     * The state chip was already here and was the one thing on the row that
     * looked like a control and was not: switching a rate off meant opening the
     * drawer, unticking a box and saving. The patch carries `active` alone, so
     * nothing else on the rate can be disturbed by a mis-click.
     *
     * The row is updated in place rather than reloading the list: a reload
     * re-sorts by resolution order, and a rate jumping under the cursor the
     * instant it is switched is how the next one gets switched by accident.
     */
    async function toggleActive(r, next) {
        if (togglingID) return;
        togglingID = r.id;
        try {
            const updated = await api.patch(`/api/admin/tax-rates/${r.id}`, { active: next });
            rates = rates.map((row) => (row.id === r.id ? (updated ?? { ...row, active: next }) : row));
            toast.success(next ? `${r.name} is on` : `${r.name} is off`);
        } catch (err) {
            toast.error(err);
            // Put the switch back where the store still has it.
            rates = rates.map((row) => (row.id === r.id ? { ...row, active: r.active } : row));
        } finally {
            togglingID = null;
        }
    }

    // ------------------------------------------------------------ selection

    /*
     * The rows an operator has picked. A store that models fifty US states
     * switches them on and off a season at a time, and the only way to do it
     * was one drawer, one tick and one save per rate.
     *
     * Cleared by the filter, kept across a page change — the rule
     * selection.svelte.js states, and here the page is a slice of what is
     * already in hand, so a rate picked on page 1 is still a real rate on
     * page 2.
     */
    const sel = selection();

    $effect(() => {
        list.params.q;
        sel.clear();
    });

    let bulkBusy = $state(false);

    /* Picked from `ordered` rather than `visible`: the same set either way —
       pick is by id — but a bulk run goes row by row, and going in the order on
       screen is the order the operator will read the failures in. */
    const picked = $derived(sel.pick(ordered));

    /**
     * Every bulk action is N calls to the per-row route this screen already
     * uses — the switch's PATCH and the bin's DELETE — and each reports its own
     * failure rather than the run dying on the first.
     */
    async function runOver(rows, fn, describe) {
        if (!rows.length) return;
        bulkBusy = true;
        try {
            await runBulk(rows, fn, { describe, noun: "rate", label: (r) => r.name });
        } finally {
            bulkBusy = false;
        }
        sel.clear();
        await load();
    }

    /* Filtered before the run, as bulk.js asks: telling an operator that four
       rates "failed" to switch on when they already were is noise they could
       have been spared. */
    const switchable = (next) => picked.filter((r) => r.active !== next);

    const bulkActive = (next) =>
        runOver(
            switchable(next),
            (r) => api.patch(`/api/admin/tax-rates/${r.id}`, { active: next }),
            next ? "Switched on" : "Switched off",
        );

    function askBulkDelete() {
        const rows = picked;
        confirmConfig = {
            title: `Delete ${rows.length} ${pluralize(rows.length, "rate")}?`,
            message:
                "Orders already placed keep what they were charged. Nothing new will be taxed " +
                "under these rules.",
            confirmLabel: "Delete",
            danger: true,
            run: () =>
                runOver(rows, (r) => api.delete(`/api/admin/tax-rates/${r.id}`), "Deleted"),
        };
        confirmOpen = true;
    }

    /**
     * A row click opens the rate, except where the row carries its own control.
     *
     * Testing the target rather than stopping propagation on each one: the
     * switch is a label and a visually hidden input, and a handler on either
     * leaves the other one opening the drawer.
     */
    function rowClick(r, event) {
        if (event.target.closest("button, a, label, input")) return;
        openEdit(r);
    }

    function submitFilter(event) {
        event.preventDefault();
        list.set({ q: draftFilter.trim() });
    }

    function clearFilter() {
        draftFilter = "";
        list.set({ q: "" });
    }

    /** Where a rule applies, named rather than coded. */
    function where(r) {
        if (!r.country) return "Everywhere";
        const place = countryName(r.country);
        return r.state ? `${place} · ${r.state}` : place;
    }
</script>

<svelte:head><title>Tax · GoCommerce</title></svelte:head>

{#if !mayRead}
    <NoAccess right="taxes.read" what="tax rates" />
{:else}

<div class="page page-taxes shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Tax rates</div>
            </nav>
            <div class="flex-fill"></div>

            <!-- A store modelling every US state has fifty rows here and the
                 screen had no way to reach one of them. The listing is
                 unpaginated by design, so this filters what is already on the
                 page rather than asking again. -->
            <form class="fields searchbar" onsubmit={submitFilter}>
                <div class="field">
                    <input
                        type="text"
                        class="p-l-20"
                        placeholder="Filter by name, place or category"
                        bind:value={draftFilter}
                        oninput={() => list.set({ q: draftFilter.trim() })}
                    />
                </div>
                {#if draftFilter || filter}
                    <div class="field addon p-r-5">
                        <button
                            type="button"
                            class="btn sm pill secondary transparent"
                            onclick={clearFilter}
                        >
                            Clear
                        </button>
                    </div>
                {/if}
            </form>

            <div class="page-header-primary-btns">
                {#if mayWrite}
                    <button type="button" class="btn" onclick={openNew}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New rate</span>
                    </button>
                {/if}
            </div>
        </header>

        <div class="page-table-wrapper tw:rounded-xl tw:border">
            <!-- Responsive now that the row carries a control: below 900px the
                 header row is replaced by a label per cell, and without it the
                 Active switch sits in a column a phone cuts off. Every cell
                 already had its `data-name`. -->
            <table class="table responsive-table">
                <thead class="sticky">
                    <tr>
                        {#if mayWrite}
                            <th class="col-bulk-select min-width">
                                <div class="field">
                                    <input
                                        id="select-all-rates"
                                        type="checkbox"
                                        checked={sel.allSelected(paged.rows)}
                                        onchange={() => sel.toggleAll(paged.rows)}
                                    />
                                    <!-- "on this page", not "all": the page is a
                                         slice of what is in hand, and select-all
                                         means the rows being looked at. -->
                                    <label
                                        for="select-all-rates"
                                        aria-label="Select every rate on this page"
                                    ></label>
                                </div>
                            </th>
                        {/if}
                        <!-- Every column sorts, because every one of them is a
                             single value on the row. The order the rows arrive
                             in is the order they are applied in, and it is one
                             more click away: the third press on a header clears
                             the sort rather than reversing it again. -->
                        <SortHeader
                            field="name"
                            label="Name"
                            class="col-field-name-id"
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="applies"
                            label="Applies to"
                            class="col-field-type-text"
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="where"
                            label="Where"
                            class="col-field-type-select"
                            {sort}
                            onsort={sortBy}
                        />
                        <!-- Biggest first, like every other number in the
                             panel: somebody sorting by rate is looking for the
                             highest one. -->
                        <SortHeader
                            field="rate"
                            label="Rate"
                            class="col-field-type-number min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <!-- Named "Active" rather than "State": the geographic
                             state is in Where, and the chip that used to sit
                             under this heading read as the same word. On first,
                             because a table sorted by this column is being read
                             for what is live. -->
                        <SortHeader
                            field="active"
                            label="Active"
                            class="col-field-type-select"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each paged.rows as r (r.id)}
                        <tr
                            class="handle"
                            tabindex="0"
                            onclick={(e) => rowClick(r, e)}
                            onkeydown={(e) => rowKey(e, () => rowClick(r, e))}
                        >
                            {#if mayWrite}
                                <!-- stopPropagation rather than a guard inside
                                     the row handler: ticking a box must not also
                                     open the rate. -->
                                <td
                                    class="col-bulk-select min-width"
                                    onclick={(e) => e.stopPropagation()}
                                >
                                    <div class="field">
                                        <input
                                            id="select-rate-{r.id}"
                                            type="checkbox"
                                            checked={sel.has(r.id)}
                                            onchange={() => sel.toggle(r.id)}
                                        />
                                        <label
                                            for="select-rate-{r.id}"
                                            aria-label="Select {r.name}"
                                        ></label>
                                    </div>
                                </td>
                            {/if}
                            <td class="col-field-name-id" data-name="Name">
                                <span class="txt-bold">{r.name}</span>
                            </td>
                            <td class="col-field-type-text" data-name="Applies to">
                                {#if r.category_name}
                                    <span class="txt-ellipsis">{r.category_name}</span>
                                    <span class="txt-hint txt-sm">and everything under it</span>
                                {:else}
                                    <span class="txt-hint">Everything</span>
                                {/if}
                            </td>
                            <td class="col-field-type-select" data-name="Where">{where(r)}</td>
                            <td class="col-field-type-number min-width" data-name="Rate">
                                {r.rate_bp / 100}%
                            </td>
                            <td class="col-field-type-select" data-name="Active">
                                {#if mayWrite}
                                    <div class="field">
                                        <input
                                            id="tax-active-{r.id}"
                                            type="checkbox"
                                            class="switch sm"
                                            checked={r.active}
                                            disabled={togglingID === r.id}
                                            onchange={(e) =>
                                                toggleActive(r, e.currentTarget.checked)}
                                        />
                                        <label for="tax-active-{r.id}">
                                            {r.active ? "on" : "off"}
                                        </label>
                                    </div>
                                {:else}
                                    <span class="label {r.active ? 'success' : ''}">
                                        {r.active ? "on" : "off"}
                                    </span>
                                {/if}
                            </td>
                            <td class="col-meta min-width">
                                <!-- Outside the mayWrite gate: reading who
                                     changed a rate is taxes.read, which is the
                                     right this screen is drawn under. -->
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    aria-label="Change history for {r.name}"
                                    title="Change history"
                                    onclick={(e) => openHistory(r, e)}
                                >
                                    <i class="ri-file-history-line" aria-hidden="true"></i>
                                </button>
                                {#if mayWrite}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary row-delete"
                                        aria-label="Delete {r.name}"
                                        title="Delete"
                                        onclick={(e) => (e.stopPropagation(), askDelete(r))}
                                    >
                                        <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                    </button>
                                {/if}
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !rates.length}
                        {#each Array(3) as _, i (i)}
                            <tr><td colspan={mayWrite ? 7 : 6}><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {/if}

                    {#if !loading && !visible.length}
                        <tr>
                            <td colspan={mayWrite ? 7 : 6} class="txt-center txt-hint p-base">
                                {#if rates.length}
                                    No rate matches “{filter}”.
                                {:else}
                                    <div class="m-b-10">
                                        <i
                                            class="ri-percent-line"
                                            style="font-size: 32px"
                                            aria-hidden="true"
                                        ></i>
                                    </div>
                                    No rates yet, so nothing is taxed. Add one for the country you
                                    sell into.
                                {/if}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>

        {#if mayWrite}
            <BulkBar count={sel.count} noun="rate" onclear={() => sel.clear()}>
                <!-- Every action here is a per-row route the screen already
                     calls; nothing in the bulk bar is a second write path. Each
                     says how many of the selection it can act on, because a
                     mixed selection is the normal case. -->
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={bulkBusy || !switchable(true).length}
                    title="Rates already on are left alone"
                    onclick={() => bulkActive(true)}
                >
                    <i class="ri-toggle-line" aria-hidden="true"></i>
                    <span class="txt">Switch on ({switchable(true).length})</span>
                </button>
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={bulkBusy || !switchable(false).length}
                    title="Rates already off are left alone"
                    onclick={() => bulkActive(false)}
                >
                    <i class="ri-toggle-fill" aria-hidden="true"></i>
                    <span class="txt">Switch off ({switchable(false).length})</span>
                </button>
                <button
                    type="button"
                    class="btn sm secondary txt-danger"
                    disabled={bulkBusy}
                    onclick={askBulkDelete}
                >
                    <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                    <span class="txt">Delete</span>
                </button>
            </BulkBar>
        {/if}

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <Pager
                meta={paged.meta}
                {loading}
                noun="rate"
                {perPage}
                onpage={(n) => list.setPage(n)}
                onperpage={(n) => list.set({ limit: n })}
            />
            <!-- Which order the table is actually in. The sentence below is
                 true of the listing as it arrives and false of it the moment a
                 header is clicked, and a footer that kept saying it would be
                 explaining the wrong table. -->
            {#if sort.field}
                <span class="txt">
                    <i class="ri-arrow-up-down-line" aria-hidden="true"></i>
                    Sorted by a column, so this is <strong>not</strong> the order rules are
                    applied in. Click the header once more to clear it.
                </span>
            {:else}
                <span class="txt">
                    Rules are read most specific first — a category beats a country, and a deeper
                    category beats the one above it.
                </span>
            {/if}
            <!--
                What a rate on this table means, which the table cannot say on
                its own: 20% added to a price and 20% taken out of one are
                different numbers on the same order, and the setting that
                decides which is on the other side of the panel.
            -->
            <span class="txt">
                {#if inclusive}
                    <i class="ri-price-tag-3-line" aria-hidden="true"></i>
                    Catalog prices <strong>already include tax</strong>, so a rate here is
                    extracted from the price rather than added to it.
                {:else}
                    <i class="ri-price-tag-3-line" aria-hidden="true"></i>
                    Catalog prices <strong>exclude tax</strong>, so a rate here is added at
                    checkout.
                {/if}
            </span>
            <div class="flex-fill"></div>
            <!-- The Pager says how many of the whole table are on screen; this
                 says how many of the whole table the filter admitted, which is
                 the other question. -->
            {#if filter && rates.length}
                <span class="txt-hint">{visible.length} of {rates.length} match</span>
            {/if}
            <ThemeToggle />
        </footer>
    </div>
</div>

<Drawer
    {open}
    size="popup"
    title={editing ? "Edit rate" : "New rate"}
    onclose={() => (open = false)}
>
    <form id="tax-form" onsubmit={save}>
        <div class="fields">
            <div class="field required">
                <label for="t-name">Name</label>
                <input id="t-name" type="text" bind:value={form.name} placeholder="GST 18%" />
            </div>
            <div class="delimiter"></div>
            <div class="field required">
                <label for="t-percent">Rate</label>
                <input
                    id="t-percent"
                    type="number"
                    min="0"
                    max="100"
                    step="0.01"
                    bind:value={form.percent}
                />
            </div>
        </div>
        <div class="field-help">
            The name is what appears on the invoice.
            {#if inclusive}
                This store's catalog prices already include tax, so this rate is extracted from
                the price rather than added to it.
            {:else}
                This store's catalog prices exclude tax, so this rate is added at checkout.
            {/if}
        </div>

        <div class="fields m-t-sm">
            <div class="field">
                <label for="t-country">Country</label>
                <!-- A list, not a two-character box. "UK" instead of "GB" is a
                     rule that never fires, and a rule that never fires
                     under-collects tax on every order into that market with
                     nothing on any screen to show for it. -->
                <Select
                    id="t-country"
                    placeholder="Anywhere"
                    bind:value={form.country}
                    options={COUNTRY_OPTIONS}
                />
            </div>
            <div class="delimiter"></div>
            <div class="field">
                <label for="t-state">State</label>
                <input
                    id="t-state"
                    type="text"
                    maxlength="8"
                    bind:value={form.state}
                    placeholder="KA"
                    oninput={(e) => (form.state = e.currentTarget.value.toUpperCase())}
                />
            </div>
        </div>
        <div class="field-help">
            Leave the country on <em>Anywhere</em> for a rule that applies wherever you ship; a
            state needs the country it is in. The state is matched literally and upper-cased here,
            so it has to be the code the checkout address carries — "KA", not "Karnataka".
        </div>

        <div class="field m-t-sm">
            <label for="t-category">Category</label>
            <CategoryPicker
                id="t-category"
                bind:value={form.category_id}
                {categories}
                remote={categoriesTruncated}
            />
        </div>
        <div class="field-help">
            Leave it empty to tax everything. A category reaches everything beneath it, and a rule
            on a deeper category wins.
        </div>

        <div class="field m-t-sm">
            <input id="t-active" type="checkbox" bind:checked={form.active} />
            <label for="t-active">Active</label>
        </div>
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (open = false)}>
            <span class="txt">{mayWrite ? "Cancel" : "Close"}</span>
        </button>
        <!-- The drawer still opens without taxes.write: reading how a rate is
             cut is what taxes.read is for. Only the button that writes goes. -->
        {#if mayWrite}
            <button
                type="submit"
                form="tax-form"
                class="btn expanded"
                class:loading={saving}
                disabled={saving}
            >
                <span class="txt">{editing ? "Save changes" : "Create rate"}</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmOpen}
    title={confirmConfig.title}
    message={confirmConfig.message}
    confirmLabel={confirmConfig.confirmLabel}
    danger={confirmConfig.danger}
    onconfirm={() => confirmConfig.run?.()}
/>

<RecordHistory
    open={historyOpen}
    kind="tax-rates"
    id={historyFor?.id}
    label={historyFor?.name}
    onclose={() => (historyOpen = false)}
/>
{/if}
