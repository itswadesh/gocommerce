<script>
    /**
     * A delivery, or a stock take, across many SKUs at once.
     *
     * The single-variant drawer is the right shape for one correction and the
     * wrong shape for a pallet: forty SKUs off a supplier's note was forty
     * drawer visits, each one a search, a number and a reason typed again.
     *
     * There is no batch route and this does not invent one. It is N calls to
     * POST /api/admin/variants/{id}/inventory — the same route one row uses —
     * so each lands in its own transaction with its own movement row, exactly
     * as it does today, and a partial failure is reported line by line rather
     * than hidden behind a count. That is `runBulk`'s whole contract, reused
     * here rather than re-written.
     */
    import { base } from "$app/paths";
    import { api } from "$lib/api.js";
    import { runBulk } from "$lib/bulk.js";
    import { can } from "$lib/session.svelte.js";
    import { pluralize, stockClass } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import VariantSearch from "$lib/components/VariantSearch.svelte";

    let {
        open = false,
        /** Every location, as the Inventory screen already has them. */
        locations = [],
        /** Where to land the delivery, if the screen has an opinion. */
        locationID = 0,
        onclose,
        /** Called after anything landed, so the list behind can refresh. */
        ondone,
    } = $props();

    /* Receiving is a delta and a stock take is an absolute count, and they are
       one form because the difference is one word in the body. Keeping them
       apart matters more than merging them: "5" typed as a delivery and "5"
       read as a count are two different shelves afterwards. */
    let mode = $state("receive"); // receive | count
    let destination = $state(0);
    let reason = $state("");
    let lines = $state([]);
    let saving = $state(false);
    let error = $state("");

    const REASONS = {
        receive: ["Delivery received", "Returned to supplier", "Found"],
        count: ["Stock count", "Correction"],
    };

    /* Stock moves out of a closed location, never into it — the engine's rule,
       so a closed shelf is not offered as a destination at all. A count that
       has to LOWER a closed shelf is still possible from that row's own drawer,
       which is where a single correction belongs. */
    const openShelves = $derived(locations.filter((l) => l.active));
    const shelf = $derived(locations.find((l) => l.id === destination) ?? null);
    const multi = $derived(locations.length > 1);

    const total = $derived(
        lines.reduce((sum, l) => sum + (Number.isFinite(parseInt(l.qty, 10)) ? parseInt(l.qty, 10) : 0), 0),
    );

    /* Opening is what resets: the drawer stays mounted so it can animate, and a
       second delivery must not start with the first one's lines still in it. */
    let wasOpen = false;
    $effect(() => {
        if (open && !wasOpen) reset();
        wasOpen = open;
    });

    function reset() {
        mode = "receive";
        reason = "";
        lines = [];
        error = "";
        saving = false;
        const wanted = openShelves.find((l) => l.id === locationID);
        destination = (wanted ?? openShelves[0] ?? null)?.id ?? 0;
    }

    function setMode(next) {
        if (mode === next) return;
        mode = next;
        error = "";
        // Every number means something else now. Re-defaulting rather than
        // keeping them is the point: a delta silently read as a count is the
        // one mistake this form must not make.
        for (const line of lines) {
            line.error = "";
            line.qty = next === "receive" ? "1" : String(here(line)?.on_hand ?? 0);
        }
    }

    function pickDestination(id) {
        destination = id;
        error = "";
        // A count is about one shelf, so the numbers follow the shelf.
        if (mode === "count") {
            for (const line of lines) line.qty = String(here(line)?.on_hand ?? 0);
        }
    }

    /** What this line's variant holds at the chosen destination, once known. */
    function here(line) {
        return line.rows?.find((r) => r.location_id === destination) ?? null;
    }

    function addLine(row) {
        error = "";
        const already = lines.find((l) => l.id === row.id);
        if (already) {
            // Scanning the same box twice is counting two of them, which is
            // what a scanner is for. A count is not cumulative, so it is left
            // alone and the row is simply brought back into view.
            if (mode === "receive") {
                already.qty = String((parseInt(already.qty, 10) || 0) + 1);
            }
            already.error = "";
            return;
        }
        const line = {
            id: row.id,
            sku: row.sku,
            label: row.label ?? "",
            title: row.product?.title ?? "",
            tracked: row.track_inventory !== false,
            storeOnHand: row.stock_on_hand ?? 0,
            rows: null,
            qty: mode === "receive" ? "1" : "",
            error: "",
        };
        lines = [...lines, line];
        // The line INSIDE the state array, not the object that went into it:
        // reading an index back hands out the reactive proxy, and writing to
        // the raw object instead updates the data while nothing re-renders.
        loadShelves(lines[lines.length - 1]);
    }

    /**
     * Where this variant's units actually are. One call per line as it is
     * added, not one for the whole list: the list is built a box at a time by a
     * person, and the number a stock take starts from has to be the shelf's,
     * not the store's total.
     */
    async function loadShelves(line) {
        try {
            const result = await api.get(`/api/admin/variants/${line.id}/stock`);
            line.rows = result.data ?? [];
            if (mode === "count" && !line.qty) line.qty = String(here(line)?.on_hand ?? 0);
        } catch {
            // The line still works: the engine reads the shelf under its own
            // lock when the count is applied, and this was only ever the
            // starting number.
            line.rows = [];
            if (mode === "count" && !line.qty) line.qty = "0";
        }
    }

    function removeLine(id) {
        lines = lines.filter((l) => l.id !== id);
        error = "";
    }

    function submit(event) {
        event?.preventDefault();
        if (saving) return;
        apply();
    }

    async function apply() {
        error = "";
        for (const line of lines) line.error = "";

        if (!lines.length) {
            error = "Add at least one SKU.";
            return;
        }
        if (!destination) {
            error = "Choose where the stock is going.";
            return;
        }
        if (!shelf?.active) {
            error = "That location is closed. Stock moves out of a closed location, never into it.";
            return;
        }
        // The panel's rule, not the API's: a count replaces a number nobody can
        // reconstruct afterwards, so the row an audit reads has to say why.
        if (mode === "count" && !reason.trim()) {
            error = "Say why — this is the row an audit will read.";
            return;
        }

        let bad = false;
        for (const line of lines) {
            const n = parseInt(line.qty, 10);
            if (!Number.isFinite(n)) {
                line.error = "Enter a whole number.";
                bad = true;
            } else if (mode === "receive" && n === 0) {
                line.error = "Zero receives nothing — remove the line instead.";
                bad = true;
            } else if (mode === "count" && n < 0) {
                line.error = "A count cannot be negative.";
                bad = true;
            }
        }
        if (bad) {
            error = "Some lines cannot be applied yet.";
            return;
        }

        saving = true;
        // Silent: the failures belong on the lines that caused them, where the
        // number that was refused is still on screen beside the reason.
        const { done, failures } = await runBulk(
            [...lines],
            (line) =>
                api.post(`/api/admin/variants/${line.id}/inventory`, {
                    ...(mode === "receive"
                        ? { adjust: parseInt(line.qty, 10) }
                        : { set: parseInt(line.qty, 10) }),
                    location_id: destination,
                    reason: reason.trim(),
                }),
            { label: (line) => line.sku },
        );
        saving = false;

        if (done) {
            toast.success(
                `${mode === "receive" ? "Received" : "Counted"} ${done} ${pluralize(done, "SKU")} at ${shelf.name}`,
            );
            ondone?.();
        }
        if (!failures.length) {
            onclose?.();
            return;
        }
        // What landed is gone from the list, so pressing the button again
        // cannot double a delivery that half worked.
        const refused = new Map(failures.map((f) => [f.row.id, f.message]));
        for (const line of lines) line.error = refused.get(line.id) ?? "";
        lines = lines.filter((l) => l.error);
        error =
            failures.length === 1
                ? "One line was refused — it is still listed below, with the reason."
                : `${failures.length} lines were refused — they are still listed below, each with its reason.`;
    }
</script>

<Drawer
    {open}
    size="lg"
    title={mode === "receive" ? "Receive a delivery" : "Stock take"}
    onclose={() => onclose?.()}
>
    <div class="tabs-header m-b-base">
        <button
            type="button"
            class="tab-item"
            class:active={mode === "receive"}
            onclick={() => setMode("receive")}
        >
            <i class="ri-inbox-archive-line" aria-hidden="true"></i>
            Receive
        </button>
        <button
            type="button"
            class="tab-item"
            class:active={mode === "count"}
            onclick={() => setMode("count")}
        >
            <i class="ri-list-check-2" aria-hidden="true"></i>
            Stock take
        </button>
    </div>

    <form id="receive-form" onsubmit={submit}>
        {#if multi}
            <div class="field required">
                <label for="receive-where">
                    {mode === "receive" ? "Landing at" : "Counting"}
                </label>
                <select
                    id="receive-where"
                    value={destination}
                    onchange={(e) => pickDestination(Number(e.currentTarget.value))}
                >
                    {#each openShelves as l (l.id)}
                        <option value={l.id}>{l.name}</option>
                    {/each}
                </select>
            </div>
            <div class="field-help">
                Closed locations are not listed: stock moves out of a closed location, never
                into it.
            </div>
        {/if}

        {#if !openShelves.length}
            <div class="alert warning">
                <p>Every location is closed, so nothing can be received. Reopen one first.</p>
            </div>
        {/if}

        <div class="field m-t-sm" class:required={mode === "count"}>
            <label for="receive-why">Why</label>
            <div class="inline-flex gap-5 m-b-5 flex-wrap">
                {#each REASONS[mode] as preset (preset)}
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
                id="receive-why"
                type="text"
                maxlength="200"
                bind:value={reason}
                oninput={() => (error = "")}
            />
        </div>
        <div class="field-help">
            One sentence for the whole delivery — every line below records it, so the ledger
            reads the same three weeks later.
        </div>
    </form>

    <h6 class="section-title m-t-base">Add SKUs</h6>
    <VariantSearch onpick={addLine} disabled={!openShelves.length} />
    <div class="field-help">
        Scan or type a SKU and press Enter; the same SKU twice counts two.
        {#if can("data.import")}
            A whole delivery in a file goes through
            <a href="{base}/data">the products importer</a> instead — its
            <span class="txt-code"
                >stock_on_hand{multi && shelf ? `:${shelf.code}` : ""}</span
            > column is a whole stock take, one row per SKU.
        {/if}
    </div>

    <!-- No `.page-table-wrapper`: that wrapper carries `table { min-width:
         900px }`, which is right for a page and wrong inside a drawer — it put
         the quantity box past the edge and behind a horizontal scrollbar. The
         adjust drawer's own `.stock-locations` table is bare for the same
         reason. -->
    <div class="m-t-sm">
        <table class="table drawer-table">
            <thead>
                <tr>
                    <th>SKU</th>
                    <th class="txt-right">{multi ? "Here now" : "On hand"}</th>
                    <th class="min-width">{mode === "receive" ? "Receiving" : "Counted"}</th>
                    <th class="min-width"></th>
                </tr>
            </thead>
            <tbody>
                {#each lines as line (line.id)}
                    <tr>
                        <td>
                            <span class="txt-bold txt-code">{line.sku}</span>
                            <div class="txt-hint txt-sm txt-ellipsis">
                                {line.title}{line.label ? ` — ${line.label}` : ""}
                            </div>
                            {#if line.error}
                                <div class="field-help error">{line.error}</div>
                            {/if}
                        </td>
                        <td class="txt-right">
                            {#if !line.tracked}
                                <span class="txt-hint">not tracked</span>
                            {:else if line.rows === null}
                                <span class="txt-hint">…</span>
                            {:else}
                                {@const at = here(line)}
                                <span class="txt-bold">{at?.on_hand ?? 0}</span>
                                <div class="txt-hint txt-sm">
                                    <span class={stockClass(at?.available ?? 0)}>
                                        {at?.available ?? 0}
                                    </span>
                                    free
                                </div>
                                {#if multi}
                                    <div class="txt-hint txt-sm">
                                        {line.storeOnHand} in all
                                    </div>
                                {/if}
                            {/if}
                        </td>
                        <td class="min-width">
                            <input
                                type="number"
                                class="qty"
                                aria-label="{mode === 'receive'
                                    ? 'Units received'
                                    : 'Counted'} for {line.sku}"
                                min={mode === "count" ? 0 : undefined}
                                bind:value={line.qty}
                                oninput={() => ((line.error = ""), (error = ""))}
                            />
                        </td>
                        <td class="min-width">
                            <button
                                type="button"
                                class="btn circle sm transparent secondary"
                                aria-label="Remove {line.sku}"
                                title="Remove"
                                onclick={() => removeLine(line.id)}
                            >
                                <i class="ri-close-line" aria-hidden="true"></i>
                            </button>
                        </td>
                    </tr>
                {/each}

                {#if !lines.length}
                    <tr>
                        <td colspan="4" class="txt-center txt-hint p-base">
                            Nothing on the note yet.
                        </td>
                    </tr>
                {/if}
            </tbody>
        </table>
    </div>

    {#if error}
        <div class="field-help error m-t-sm">{error}</div>
    {/if}

    <div class="field-help m-t-sm">
        {#if mode === "receive"}
            Each line is a delta: 25 receives a case, -1 writes one off. Nothing is applied
            until the button below.
        {:else}
            Each line REPLACES the count at {shelf?.name ?? "this location"}, and cannot go
            below what is already promised to open orders there.
        {/if}
    </div>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">Cancel</span>
        </button>
        <span class="txt txt-hint m-r-sm">
            {lines.length}
            {pluralize(lines.length, "SKU")}{mode === "receive" && total
                ? `, ${total} ${pluralize(total, "unit")}`
                : ""}
        </span>
        <button
            type="submit"
            form="receive-form"
            class="btn"
            class:loading={saving}
            disabled={saving || !lines.length || !openShelves.length}
        >
            <span class="txt">
                {mode === "receive" ? "Receive" : "Apply count"}
            </span>
        </button>
    {/snippet}
</Drawer>

<style>
    /*
     * A count is four digits, not a sentence. A bare <input> in a table cell
     * takes the whole column, which pushes the remove button off the edge of
     * the drawer — the one rule this component needs, scoped here rather than
     * appended to gocommerce.css while several agents are editing it.
     */
    .qty {
        width: 5rem;
    }
</style>
