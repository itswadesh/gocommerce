<script>
    /**
     * Moving many SKUs from one shelf to another.
     *
     * The single-variant drawer's "move" mode is the right shape for one
     * correction and the wrong shape for closing a shelf: forty SKUs off a
     * stockroom was forty drawer visits, each one a search, a pair of location
     * pickers and a reason typed again.
     *
     * There is no batch route and this does not invent one. It is N calls to
     * POST /api/admin/variants/{id}/stock/transfer — the same route one row
     * uses — so each lands in its own transaction, takes both shelf locks in
     * ascending order and writes its own pair of movement rows, exactly as it
     * does today. `runBulk` reports a partial failure line by line rather than
     * hiding it behind a count, which is the normal outcome here: a SKU whose
     * units at the source are already promised to an open order is refused, and
     * the ones beside it are not.
     *
     * The source may be a CLOSED location and the destination may not. That is
     * the engine's own asymmetry (D44) and the reason this form exists: units
     * stranded on a shelf somebody closed are moved off it, never onto it.
     */
    import { api } from "$lib/api.js";
    import { runBulk } from "$lib/bulk.js";
    import { pluralize, stockClass } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import VariantSearch from "$lib/components/VariantSearch.svelte";

    let {
        open = false,
        /** Every location, as the Inventory screen already has them. */
        locations = [],
        /** Where the screen thinks the units are coming from, if it has a view. */
        fromID = 0,
        /**
         * Variants to start the list with — the rows an operator ticked on the
         * table behind. Each is a variant as the engine sends one, so the same
         * fields `addLine` reads off a search result are already here.
         */
        seed = [],
        onclose,
        /** Called after anything landed, so the list behind can refresh. */
        ondone,
    } = $props();

    let source = $state(0);
    let destination = $state(0);
    let reason = $state("");
    let lines = $state([]);
    let saving = $state(false);
    let error = $state("");

    const REASONS = ["Rebalancing", "Store request", "Closing a location", "Stock consolidation"];

    /* Out of a closed location, never into it — so the two pickers do not offer
       the same list. The source offers everywhere, because stranded units are
       exactly what has to be movable; the destination offers open shelves. */
    const openShelves = $derived(locations.filter((l) => l.active));
    const from = $derived(locations.find((l) => l.id === source) ?? null);
    const to = $derived(locations.find((l) => l.id === destination) ?? null);

    const total = $derived(
        lines.reduce((sum, l) => sum + (parseInt(l.qty, 10) || 0), 0),
    );

    /* Opening is what resets: the drawer stays mounted so it can animate, and a
       second transfer must not start with the first one's lines still in it. */
    let wasOpen = false;
    $effect(() => {
        if (open && !wasOpen) reset();
        wasOpen = open;
    });

    function reset() {
        reason = "";
        error = "";
        saving = false;
        source = (locations.find((l) => l.id === fromID) ?? locations[0] ?? null)?.id ?? 0;
        destination = (openShelves.find((l) => l.id !== source) ?? null)?.id ?? 0;
        lines = (seed ?? []).map(lineOf);
        pump();
    }

    /** One table row, from a variant however it arrived. */
    function lineOf(row) {
        return {
            id: row.id,
            sku: row.sku,
            label: row.label ?? "",
            title: row.product?.title ?? "",
            tracked: row.track_inventory !== false,
            storeOnHand: row.stock_on_hand ?? 0,
            rows: null,
            qty: "1",
            error: "",
        };
    }

    /** What this line's variant holds at either end, once its shelves are known. */
    function at(line, locationID) {
        return line.rows?.find((r) => r.location_id === locationID) ?? null;
    }

    /** How many of it the source could actually part with. */
    function freeAtSource(line) {
        const held = at(line, source);
        if (!held) return 0;
        // on_hand minus reserved: the engine refuses a move that would take the
        // shelf below what is already promised to open orders there, and this is
        // the same arithmetic said before the request rather than after it.
        return Math.max(0, held.on_hand - held.reserved);
    }

    /**
     * Where each line's units are, four requests at a time.
     *
     * A selection of forty rows is forty GETs, and firing them all at once
     * buys nothing but a queue — the same cap the categories screen puts on its
     * product counts, for the same reason.
     */
    async function pump() {
        const queue = lines.filter((l) => l.rows === null);
        let next = 0;
        const worker = async () => {
            while (next < queue.length) {
                const line = queue[next++];
                await loadShelves(line);
            }
        };
        await Promise.all([worker(), worker(), worker(), worker()]);
    }

    async function loadShelves(line) {
        try {
            const result = await api.get(`/api/admin/variants/${line.id}/stock`);
            line.rows = result.data ?? [];
        } catch {
            // The line still works: the engine reads both shelves under their
            // own locks when the transfer is applied, and this was only ever
            // the number shown beside the box.
            line.rows = [];
        }
    }

    function addLine(row) {
        error = "";
        const already = lines.find((l) => l.id === row.id);
        if (already) {
            // Scanning the same box twice is moving two of them, which is what a
            // scanner is for.
            already.qty = String((parseInt(already.qty, 10) || 0) + 1);
            already.error = "";
            return;
        }
        lines = [...lines, lineOf(row)];
        // The line INSIDE the state array, not the object that went into it:
        // reading an index back hands out the reactive proxy, and writing to the
        // raw object instead updates the data while nothing re-renders.
        loadShelves(lines[lines.length - 1]);
    }

    function removeLine(id) {
        lines = lines.filter((l) => l.id !== id);
        error = "";
    }

    /**
     * Empty the source shelf of everything on the list.
     *
     * This is the operation the form exists for — a location being closed has
     * to be emptied — and typing the same number a row already shows forty
     * times is not a way to do it.
     *
     * A line the source holds none of is dropped rather than set to zero: zero
     * is not a transfer, so keeping it would leave a list that cannot be
     * submitted at all until the operator removes those rows by hand, which is
     * the work this button exists to save.
     */
    function moveAll() {
        error = "";
        for (const line of lines) {
            line.qty = String(freeAtSource(line));
            line.error = "";
        }
        lines = lines.filter((l) => l.rows === null || freeAtSource(l) > 0);
    }

    function pickSource(id) {
        source = id;
        // The two ends cannot be the same place, and the engine says so; moving
        // the destination out of the way beats a refusal on the button.
        if (destination === id) {
            destination = (openShelves.find((l) => l.id !== id) ?? null)?.id ?? 0;
        }
        error = "";
        for (const line of lines) line.error = "";
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
        if (!source || !destination) {
            error = "Choose where the units are coming from and where they are going.";
            return;
        }
        if (source === destination) {
            error = "A transfer needs two different locations.";
            return;
        }
        if (!to?.active) {
            error = "That location is closed. Stock moves out of a closed location, never into it.";
            return;
        }

        let bad = false;
        for (const line of lines) {
            const n = parseInt(line.qty, 10);
            const free = freeAtSource(line);
            if (!Number.isFinite(n) || n <= 0) {
                line.error = "Move a positive whole number of units.";
                bad = true;
            } else if (line.rows && n > free) {
                // The engine's own rule, said before the round trip: what is
                // reserved for open orders at the source cannot leave it.
                line.error = free
                    ? `Only ${free} free at ${from?.name ?? "the source"}.`
                    : `Nothing free at ${from?.name ?? "the source"}.`;
                bad = true;
            }
        }
        if (bad) {
            error = "Some lines cannot be moved yet.";
            return;
        }

        saving = true;
        // Silent: the failures belong on the lines that caused them, where the
        // number that was refused is still on screen beside the reason.
        const { done, failures } = await runBulk(
            [...lines],
            (line) =>
                api.post(`/api/admin/variants/${line.id}/stock/transfer`, {
                    from_location_id: source,
                    to_location_id: destination,
                    quantity: parseInt(line.qty, 10),
                    reason: reason.trim(),
                }),
            { label: (line) => line.sku },
        );
        saving = false;

        if (done) {
            toast.success(
                `Moved ${done} ${pluralize(done, "SKU")} from ${from?.name ?? "there"} to ${to?.name ?? "there"}`,
            );
            ondone?.();
        }
        if (!failures.length) {
            onclose?.();
            return;
        }
        // What landed is gone from the list, so pressing the button again cannot
        // move the same units twice.
        const refused = new Map(failures.map((f) => [f.row.id, f.message]));
        for (const line of lines) line.error = refused.get(line.id) ?? "";
        lines = lines.filter((l) => l.error);
        // Both shelves moved under the lines that are left — a half-applied
        // transfer changed one end of each — so the figures beside the boxes are
        // read again rather than left showing what was true before the run.
        for (const line of lines) line.rows = null;
        pump();
        error =
            failures.length === 1
                ? "One line was refused — it is still listed below, with the reason."
                : `${failures.length} lines were refused — they are still listed below, each with its reason.`;
    }
</script>

<Drawer {open} size="lg" title="Move stock" onclose={() => onclose?.()}>
    <form id="move-form" onsubmit={submit}>
        <div class="grid">
            <div class="col-lg-6">
                <div class="field required">
                    <label for="move-from">From</label>
                    <select
                        id="move-from"
                        value={source}
                        onchange={(e) => pickSource(Number(e.currentTarget.value))}
                    >
                        {#each locations as l (l.id)}
                            <option value={l.id}>{l.name}{l.active ? "" : " — closed"}</option>
                        {/each}
                    </select>
                </div>
            </div>
            <div class="col-lg-6">
                <div class="field required">
                    <label for="move-to">To</label>
                    <select
                        id="move-to"
                        value={destination}
                        onchange={(e) => (
                            (destination = Number(e.currentTarget.value)), (error = "")
                        )}
                    >
                        {#each openShelves.filter((l) => l.id !== source) as l (l.id)}
                            <option value={l.id}>{l.name}</option>
                        {/each}
                    </select>
                </div>
            </div>
        </div>
        <div class="field-help">
            A closed location can be emptied but not filled — which is the whole of the recovery
            path for units stranded on a shelf somebody closed, so it is offered on the left and
            never on the right.
        </div>

        <div class="field m-t-sm">
            <label for="move-why">Why</label>
            <div class="inline-flex gap-5 m-b-5 flex-wrap">
                {#each REASONS as preset (preset)}
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
                id="move-why"
                type="text"
                maxlength="200"
                bind:value={reason}
                oninput={() => (error = "")}
            />
        </div>
        <div class="field-help">
            One sentence for the whole transfer — both ends of every line record it, so the
            ledger reads the same three weeks later.
        </div>
    </form>

    <h6 class="section-title m-t-base">
        Add SKUs
        {#if lines.length}
            <button
                type="button"
                class="btn sm secondary"
                title="Fills every line with what the source can spare, and drops the ones it holds none of"
                onclick={moveAll}
            >
                <i class="ri-inbox-unarchive-line" aria-hidden="true"></i>
                <span class="txt">Move everything free</span>
            </button>
        {/if}
    </h6>
    <VariantSearch onpick={addLine} disabled={!destination} />
    <div class="field-help">
        Scan or type a SKU and press Enter; the same SKU twice moves two. Ticking rows on the
        table behind fills this in for you.
    </div>

    <!-- No `.page-table-wrapper`: that wrapper carries `table { min-width: 900px }`,
         which is right for a page and wrong inside a drawer — it pushes the
         quantity box past the edge and behind a horizontal scrollbar. -->
    <div class="m-t-sm move-lines">
        <table class="table drawer-table">
            <thead>
                <tr>
                    <th>SKU</th>
                    <th class="txt-right">At {from?.name ?? "the source"}</th>
                    <th class="txt-right">At {to?.name ?? "the destination"}</th>
                    <th class="min-width">Moving</th>
                    <th class="min-width"></th>
                </tr>
            </thead>
            <tbody>
                {#each lines as line (line.id)}
                    {@const here = at(line, source)}
                    {@const there = at(line, destination)}
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
                            {#if line.rows === null}
                                <span class="txt-hint">…</span>
                            {:else}
                                <span class="txt-bold">{here?.on_hand ?? 0}</span>
                                <div class="txt-hint txt-sm">
                                    <span class={stockClass(freeAtSource(line))}>
                                        {freeAtSource(line)}
                                    </span>
                                    free
                                </div>
                            {/if}
                        </td>
                        <td class="txt-right txt-hint">
                            {#if line.rows === null}
                                …
                            {:else}
                                {there?.on_hand ?? 0}
                            {/if}
                        </td>
                        <td class="min-width">
                            <input
                                type="number"
                                class="qty"
                                min="1"
                                aria-label="Units of {line.sku} to move"
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
                        <td colspan="5" class="txt-center txt-hint p-base">
                            Nothing to move yet.
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
        The store's total does not change: a transfer is two movements that cancel, one out of
        {from?.name ?? "the source"} and one into {to?.name ?? "the destination"}. Units already
        reserved for open orders stay where they are.
    </div>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">Cancel</span>
        </button>
        <span class="txt txt-hint m-r-sm">
            {lines.length}
            {pluralize(lines.length, "SKU")}{total ? `, ${total} ${pluralize(total, "unit")}` : ""}
        </span>
        <button
            type="submit"
            form="move-form"
            class="btn"
            class:loading={saving}
            disabled={saving || !lines.length || !destination}
        >
            <span class="txt">Move</span>
        </button>
    {/snippet}
</Drawer>

<style>
    /*
     * A quantity is four digits, not a sentence. A bare <input> in a table cell
     * takes the whole column, which pushes the remove button off the edge of the
     * drawer — the one rule this component needs, scoped here rather than
     * appended to gocommerce.css while several agents are editing it.
     */
    .qty {
        width: 5rem;
    }
    /*
     * Five columns is one more than the delivery form's, and on a phone the
     * fifth — the remove button — was simply clipped off the right edge of the
     * drawer rather than reachable. Wide content scrolls inside its own box;
     * the min-width is what makes there be something to scroll, since the
     * table would otherwise squeeze to fit and lose the column anyway.
     */
    .move-lines {
        overflow-x: auto;
    }
    .move-lines table {
        min-width: 28rem;
    }
</style>
