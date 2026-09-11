<script>
    import { api } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { stockClass, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import StockHistory from "$lib/components/StockHistory.svelte";

    import ThemeToggle from "$lib/components/ThemeToggle.svelte";
    const PER_PAGE = 25;
    const DEFAULT_THRESHOLD = 5;

    /* The threshold, the location and the page live in the URL: a filtered list
       with no filter in its address cannot be sent to anybody, and the window
       replaces the rows rather than accumulating them, so `page=3` in the URL is
       page 3 of the same list when it is opened again. */
    const list = listState({ threshold: DEFAULT_THRESHOLD, location_id: 0, page: 1 });

    let loading = $state(true);
    let variants = $state([]);
    let meta = $state(null);
    let draftThreshold = $state(list.params.threshold);

    const threshold = $derived(list.params.threshold);
    /* Deliberately not `locationID`, which is the drawer's selected source
       shelf: one name for two things would make picking a shelf inside the
       drawer silently re-query the list behind it. */
    const filterLocationID = $derived(list.params.location_id);
    let filterLocations = $state([]);
    const chosenLocation = $derived(
        filterLocations.find((l) => l.id === filterLocationID) ?? null,
    );

    let adjustOpen = $state(false);
    let target = $state(null);
    let mode = $state("adjust"); // adjust | set | move | history
    let amount = $state("");
    // Why the stock moved, in the operator's words. It is the row an audit
    // reads, and the only place in the panel that collects one — the product
    // form and the variant matrix deliberately send none rather than a canned
    // sentence no human typed.
    let reason = $state("");

    /* Presets rather than free text alone: an operator with a case to put away
       should not have to compose prose, and a short shared vocabulary is what
       makes the ledger searchable three weeks later. */
    const REASONS = {
        adjust: ["Delivery received", "Damaged", "Shrinkage", "Returned to supplier", "Found"],
        set: ["Stock count", "Correction"],
        move: ["Rebalancing", "Store request"],
    };
    let saving = $state(false);
    let error = $state("");

    // Where the variant in the drawer actually is. Loaded per open rather than
    // with the list: the table is about totals, and one row's breakdown is a
    // question only asked when somebody is about to move something.
    let rows = $state([]);
    let rowsLoading = $state(false);
    let locationID = $state(0);
    let toID = $state(0);

    const multi = $derived(rows.length > 1);
    const here = $derived(rows.find((r) => r.location_id === locationID) ?? null);
    const elsewhere = $derived(rows.filter((r) => r.location_id !== locationID));

    $effect(() => {
        // Any parameter changing reloads: the threshold, the location, the page.
        list.params;
        load();
    });

    $effect(() => {
        loadLocations();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/inventory/low-stock" + list.query({ limit: PER_PAGE }),
            );
            variants = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /* Loaded once, and the picker is hidden entirely below two — a
       single-location store should never have to learn the word, which is the
       stance the engine itself takes. */
    async function loadLocations() {
        try {
            const result = await api.get("/api/admin/locations");
            filterLocations = result.data ?? [];
        } catch {
            // The filter is an extra; the list below it still works without it.
        }
    }

    function applyThreshold(e) {
        e.preventDefault();
        // An emptied or negative box is not a threshold; leave the applied one
        // alone rather than asking the server about a count that cannot exist.
        if (draftThreshold === undefined || draftThreshold === null || draftThreshold < 0) return;
        list.set({ threshold: draftThreshold });
    }

    function resetThreshold() {
        draftThreshold = DEFAULT_THRESHOLD;
        list.set({ threshold: DEFAULT_THRESHOLD });
    }

    function openAdjust(variant, initialMode, event) {
        event?.stopPropagation();
        target = variant;
        mode = initialMode;
        amount = "";
        reason = "";
        error = "";
        rows = [];
        locationID = 0;
        toID = 0;
        adjustOpen = true;
        loadRows(variant.id, initialMode);
    }

    async function loadRows(variantID, initialMode) {
        rowsLoading = true;
        try {
            const result = await api.get(`/api/admin/variants/${variantID}/stock`);
            rows = result.data ?? [];
            // Rows arrive in priority order, so the first one holding anything is
            // the shelf an order would come off — the same reading the locations
            // page states in its footer. Falling back to the first row keeps a
            // variant that is nowhere yet pointed at somewhere real.
            //
            // An open shelf first, though, and that is not cosmetic: stock may
            // not arrive at a closed location, so pre-selecting one whose only
            // units are stranded would make the drawer's default action a 409.
            const start =
                rows.find((r) => r.active && r.on_hand !== 0) ??
                rows.find((r) => r.active) ??
                rows[0];
            locationID = start?.location_id ?? 0;
            toID =
                rows.find((r) => r.location_id !== locationID && r.active)?.location_id ?? 0;
            if (initialMode === "set") amount = String(start?.on_hand ?? 0);
        } catch (err) {
            error = err.message;
        } finally {
            rowsLoading = false;
        }
    }

    function pickLocation(id) {
        locationID = id;
        if (mode === "set") amount = String(rows.find((r) => r.location_id === id)?.on_hand ?? 0);
        if (toID === id) toID = elsewhere.find((r) => r.active)?.location_id ?? 0;
        error = "";
    }

    /* The engine's own sentence, so the two surfaces do not paraphrase each
       other. It is a pre-check rather than a replacement for the 409: the
       location could close between the page loading and the button. */
    const CLOSED = "That location is closed. Stock moves out of a closed location, never into it.";

    async function save(event) {
        event?.preventDefault();
        const value = parseInt(amount, 10);
        if (isNaN(value)) {
            error = "Enter a whole number.";
            return;
        }
        if (mode === "set" && value < 0) {
            error = "Stock cannot be negative.";
            return;
        }
        if (mode === "move" && value <= 0) {
            error = "Move a positive number of units.";
            return;
        }
        if (mode === "move" && !toID) {
            error = "Choose where the units are going.";
            return;
        }
        // All three movements, not just the transfer: receiving and counting up
        // are arrivals too, and each of them is refused at a closed shelf.
        const destination = mode === "move" ? rows.find((r) => r.location_id === toID) : here;
        const arriving =
            mode === "move" ||
            (mode === "adjust" && value > 0) ||
            (mode === "set" && value > (here?.on_hand ?? 0));
        if (arriving && destination && !destination.active) {
            error = CLOSED;
            return;
        }
        // The panel's rule, not the API's: the engine accepts a blank reason
        // from any client, and a script or the MCP tool may well send one. But
        // a count that replaced the number and a write-off are exactly the two
        // movements somebody asks about later, so the panel insists.
        if ((mode === "set" || (mode === "adjust" && value < 0)) && !reason.trim()) {
            error = "Say why — this is the row an audit will read.";
            return;
        }

        saving = true;
        error = "";
        try {
            if (mode === "move") {
                await api.post(`/api/admin/variants/${target.id}/stock/transfer`, {
                    from_location_id: locationID,
                    to_location_id: toID,
                    quantity: value,
                    reason,
                });
                toast.success(`Moved ${value} × ${target.sku}`);
            } else {
                const body =
                    mode === "adjust"
                        ? { adjust: value, location_id: locationID, reason }
                        : { set: value, location_id: locationID, reason };
                await api.post(`/api/admin/variants/${target.id}/inventory`, body);
                toast.success(`Updated ${target.sku}`);
            }
            adjustOpen = false;
            // Back to the first page: the row that was just fixed usually leaves the
            // list, and page 4 of a shorter list is a screen nobody asked for.
            list.setPage(1);
            await load();
        } catch (err) {
            // The engine refuses to drop stock below what is reserved for open
            // orders, and explains why — show that rather than a generic error.
            error = err.message;
        } finally {
            saving = false;
        }
    }
</script>

<div class="page page-inventory">
    <div class="page-content full-height">
        <header class="page-header">
            <nav class="breadcrumbs"><div>Inventory</div></nav>

            <div class="inline-flex gap-sm">
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    title="Refresh"
                    aria-label="Refresh"
                    onclick={() => load()}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>

            {#if filterLocations.length > 1}
                <div class="field m-r-sm">
                    <Select
                        id="location-filter"
                        value={filterLocationID}
                        options={[
                            { value: 0, label: "Whole store" },
                            ...filterLocations.map((l) => ({
                                value: l.id,
                                label: l.name + (l.active ? "" : " — closed"),
                            })),
                        ]}
                        onchange={(v) => list.set({ location_id: Number(v) || 0 })}
                    />
                </div>
            {/if}

            <form class="fields searchbar" onsubmit={applyThreshold}>
                <div class="field addon">
                    <label class="txt-nowrap" for="threshold">Available at or below</label>
                </div>
                <div class="field">
                    <input id="threshold" type="number" min="0" bind:value={draftThreshold} />
                </div>
                {#if draftThreshold !== threshold || threshold !== DEFAULT_THRESHOLD}
                    <div class="field addon p-r-5">
                        {#if draftThreshold !== threshold}
                            <button type="submit" class="btn sm pill warning">Apply</button>
                        {/if}
                        <button
                            type="button"
                            class="btn sm pill secondary transparent"
                            onclick={resetThreshold}
                        >
                            Reset
                        </button>
                    </div>
                {/if}
            </form>
        </header>

        <div class="alert info m-b-sm">
            <p>
                <i class="ri-information-line" aria-hidden="true"></i>
                <strong>Available</strong> is on hand minus what is reserved for orders in flight.
                Stock moves as a delta or an absolute count, never as a blind overwrite — so a sale
                that lands mid-edit cannot be lost.
                {#if filterLocations.length > 1}
                    Without a location chosen, the threshold is against the store's total — a
                    variant with one unit in each of five shops is not low by that reading, even
                    though every shelf looks it.
                {/if}
            </p>
        </div>

        <div class="page-table-wrapper">
            <table class="table responsive-table" class:optimize={variants.length > 60}>
                <thead class="sticky">
                    <tr>
                        <th class="col-field-name-id">SKU</th>
                        <th class="col-field-type-text">Variant</th>
                        <th class="col-field-type-number min-width">
                            On hand{chosenLocation ? " here" : ""}
                        </th>
                        <th class="col-field-type-number min-width">
                            Reserved{chosenLocation ? " here" : ""}
                        </th>
                        <th class="col-field-type-number min-width">
                            Available{chosenLocation ? " here" : ""}
                        </th>
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each variants as variant (variant.id)}
                        <!-- Derived per row rather than by swapping objects: the
                             two shapes do not share key names, so
                             `variant.at_location ?? variant` would render two
                             blank columns in exactly the mode this exists for. -->
                        {@const al = variant.at_location}
                        {@const onHand = al ? al.on_hand : variant.stock_on_hand}
                        {@const reserved = al ? al.reserved : variant.stock_reserved}
                        {@const available = al ? al.available : variant.available}
                        <tr class="handle" onclick={() => openAdjust(variant, "adjust")}>
                            <td class="col-field-name-id" data-name="SKU">
                                <span class="txt-bold txt-code">{variant.sku}</span>
                            </td>
                            <td class="col-field-type-text txt-hint" data-name="Variant">
                                {variant.label || "—"}
                            </td>
                            <td class="col-field-type-number min-width" data-name="On hand">
                                {onHand}
                            </td>
                            <td
                                class="col-field-type-number min-width txt-hint"
                                data-name="Reserved"
                            >
                                {reserved}
                            </td>
                            <td
                                class="col-field-type-number min-width txt-bold {stockClass(
                                    available,
                                )}"
                                data-name="Available"
                            >
                                {available}
                                {#if al}
                                    <div class="txt-hint txt-sm txt-nowrap">
                                        {variant.available} in the store
                                    </div>
                                {/if}
                            </td>
                            <td class="col-meta min-width">
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    aria-label="Stock take for {variant.sku}"
                                    title="Stock take"
                                    onclick={(e) => openAdjust(variant, "set", e)}
                                >
                                    <i class="ri-list-check-2" aria-hidden="true"></i>
                                </button>
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !variants.length}
                        {#each Array(6) as _, i (i)}
                            <tr>
                                <td colspan="6"><span class="skeleton-loader"></span></td>
                            </tr>
                        {/each}
                    {/if}

                    {#if !loading && !variants.length}
                        <tr>
                            <td colspan="6" class="txt-center txt-hint p-base">
                                <div class="m-b-10">
                                    <i
                                        class="ri-checkbox-circle-line"
                                        style="font-size: 32px"
                                        aria-hidden="true"
                                    ></i>
                                </div>
                                Nothing is running low{chosenLocation
                                    ? ` at ${chosenLocation.name}`
                                    : ""}. Every tracked variant has more than
                                {threshold} available{chosenLocation ? " there" : ""}.
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>

        </div>

        <footer class="page-footer">
            <Pager {meta} {loading} noun="variant" onpage={(n) => list.setPage(n)} />
            <span class="txt txt-hint">
                at or below {threshold}{chosenLocation
                    ? ` — ${pluralize(meta?.total ?? 0, "variant")} this location carries`
                    : ""}
            </span>
            <div class="flex-fill"></div>
            <ThemeToggle />
        </footer>
    </div>
</div>

<Drawer
    open={adjustOpen}
    size={mode === "history" ? "lg" : "sm"}
    title={target
        ? mode === "adjust"
            ? `Receive stock — ${target.sku}`
            : mode === "move"
              ? `Move stock — ${target.sku}`
              : mode === "history"
                ? `History — ${target.sku}`
                : `Stock take — ${target.sku}`
        : ""}
    onclose={() => (adjustOpen = false)}
>
    {#if target}
        <form id="stock-form" onsubmit={save}>
            {#if multi}
                <!--
                    With more than one location the totals are a sum, and every
                    movement below acts on exactly one row — so the row has to be
                    the thing being pointed at, not a setting buried in a select.
                -->
                <table class="table stock-locations m-b-base">
                    <thead>
                        <tr>
                            <th>Location</th>
                            <th class="txt-right">On hand</th>
                            <th class="txt-right">Reserved</th>
                            <th class="txt-right">Available</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as r (r.location_id)}
                            <tr
                                class="handle"
                                class:selected={r.location_id === locationID}
                                onclick={() => pickLocation(r.location_id)}
                            >
                                <td>
                                    <span class="txt-bold">{r.location_name}</span>
                                    {#if !r.active}
                                        <span class="label">closed</span>
                                    {/if}
                                </td>
                                <td class="txt-right">{r.on_hand}</td>
                                <td class="txt-right txt-hint">{r.reserved}</td>
                                <td class="txt-right txt-bold {stockClass(r.available)}">
                                    {r.available}
                                </td>
                            </tr>
                        {/each}
                    </tbody>
                </table>
            {:else}
                <div class="flex m-b-sm">
                    <span class="txt-hint">On hand</span>
                    <div class="flex-fill"></div>
                    <strong>{target.stock_on_hand}</strong>
                </div>
                <div class="flex m-b-base">
                    <span class="txt-hint">Reserved for open orders</span>
                    <div class="flex-fill"></div>
                    <strong>{target.stock_reserved}</strong>
                </div>
            {/if}

            {#if mode === "history"}
                <!--
                    The location table above stays visible, so the operator can
                    see where the units are while reading how they got there.
                -->
                <!-- Keyed, so moving to another variant remounts the table
                     rather than asking for page 3 of a history it has not
                     started reading. -->
                {#key target.id}
                    <StockHistory scope="variant" id={target.id} />
                {/key}
            {:else}
                {#if mode === "move"}
                    <div class="field required">
                        <label for="move-to">Move to</label>
                        <!-- Closed destinations are disabled with the reason
                             rather than hidden: the table two rows above still
                             lists them, and a silently shorter list explains
                             nothing. -->
                        <select id="move-to" bind:value={toID}>
                            {#each elsewhere as r (r.location_id)}
                                <option value={r.location_id} disabled={!r.active}>
                                    {r.location_name}{r.active ? "" : " — closed"}
                                </option>
                            {/each}
                        </select>
                    </div>
                {/if}

                <div class="field required" class:error={!!error}>
                    <label for="amount">
                        {mode === "adjust"
                            ? "Add (or subtract) this many"
                            : mode === "move"
                              ? "How many units"
                              : "Set the count to"}
                    </label>
                    <!-- svelte-ignore a11y_autofocus -->
                    <input
                        id="amount"
                        type="number"
                        autofocus
                        bind:value={amount}
                        oninput={() => (error = "")}
                    />
                </div>
                {#if error}<div class="field-help error">{error}</div>{/if}
                <div class="field-help">
                    {#if mode === "adjust"}
                        A delta: 25 receives a case, -1 writes one off.
                        {#if multi && here}Applied at {here.location_name}.{/if}
                    {:else if mode === "move"}
                        Reserved units stay where they are — they are promised to orders that will be
                        picked from {here?.location_name ?? "here"}.
                    {:else}
                        Cannot go below the {multi
                            ? (here?.reserved ?? 0)
                            : target.stock_reserved} already promised to open orders{multi && here
                            ? ` at ${here.location_name}`
                            : ""}.
                    {/if}
                </div>

                <div class="field" class:required={mode === "set"}>
                    <label for="reason">Why</label>
                    <div class="inline-flex gap-5 m-b-5 flex-wrap">
                        {#each REASONS[mode] ?? [] as preset (preset)}
                            <button
                                type="button"
                                class="btn sm pill secondary"
                                onclick={() => ((reason = preset), (error = ""))}
                            >
                                {preset}
                            </button>
                        {/each}
                    </div>
                    <input
                        id="reason"
                        type="text"
                        maxlength="200"
                        bind:value={reason}
                        oninput={() => (error = "")}
                    />
                </div>
                <div class="field-help">
                    This is the row an audit will read — "the count says 4 and the shelf has 2" is
                    answered here or nowhere.
                </div>

                <h6 class="section-title m-t-base">Recent movements</h6>
                <!--
                    The person about to change a count is exactly the person who
                    needs to see what happened last time, so it is here rather than
                    behind another click.
                -->
                {#key target.id}
                    <StockHistory scope="variant" id={target.id} compact limit={3} />
                {/key}
            {/if}
        </form>
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (adjustOpen = false)}>
            <span class="txt">Cancel</span>
        </button>
        {#if multi && mode !== "move" && mode !== "history"}
            <button
                type="button"
                class="btn secondary"
                onclick={() => ((mode = "move"), (amount = ""), (error = ""))}
            >
                <i class="ri-arrow-left-right-line" aria-hidden="true"></i>
                <span class="txt">Move</span>
            </button>
        {/if}
        {#if mode === "history"}
            <button type="button" class="btn" onclick={() => ((mode = "adjust"), (error = ""))}>
                <span class="txt">Back</span>
            </button>
        {:else}
            <button
                type="button"
                class="btn secondary"
                onclick={() => ((mode = "history"), (error = ""))}
            >
                <i class="ri-history-line" aria-hidden="true"></i>
                <span class="txt">History</span>
            </button>
            <button
                type="submit"
                form="stock-form"
                class="btn"
                class:loading={saving || rowsLoading}
                disabled={saving || rowsLoading}
            >
                <span class="txt">{mode === "move" ? "Move" : "Apply"}</span>
            </button>
        {/if}
    {/snippet}
</Drawer>
