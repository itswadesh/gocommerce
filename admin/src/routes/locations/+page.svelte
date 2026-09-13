<script>
    /**
     * Locations: the places stock physically is.
     *
     * Most stores have exactly one and should be able to ignore this screen
     * entirely, so the listing leads with what each place is *holding* rather
     * than with its settings — the number is the reason to come here.
     */
    import { api, can, query } from "$lib/api.js";
    import { onNewShortcut } from "$lib/shortcuts.js";
    import { rowKey } from "$lib/rowkey.js";
    import { selection } from "$lib/selection.svelte.js";
    import { runBulk } from "$lib/bulk.js";
    import { pluralize, stockClass } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import BulkBar from "$lib/components/BulkBar.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import RecordHistory from "$lib/components/RecordHistory.svelte";
    import Select from "$lib/components/Select.svelte";
    import StockHistory from "$lib/components/StockHistory.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";
    import { COUNTRIES } from "$lib/countries.js";

    const STOCK_PER_PAGE = 25;

    /* A location's country is optional — most stores never fill the address in
       at all — so the list leads with the way out of it. The Tax screen and the
       New order form pick the same value from the same list; this was the last
       two-character text box, where "UK" instead of "GB" is a customs
       declaration that quietly names no country. */
    const COUNTRY_OPTIONS = [{ value: "", label: "No country" }, ...COUNTRIES];

    /* The nav gates this screen on locations.read and every control on it
       needs locations.write; both are said again here, so a typed address gets
       the refusal rather than a screen where each button 403s. */
    const readable = $derived(can("locations.read"));
    const writable = $derived(can("locations.write"));

    /* `n` creates one, from anywhere on this screen. The shell owns the
       keystroke and fires an event; what "new" means is the screen's. */
    $effect(() => {
        if (!writable) return;
        return onNewShortcut(openNew);
    });

    let loading = $state(true);
    let locations = $state([]);

    let open = $state(false);
    let editing = $state(null);
    let form = $state(blank());
    let saving = $state(false);
    let error = $state("");

    let confirmOpen = $state(false);
    let confirmConfig = $state({});

    /* A refusal belongs on the row it is about.
     *
     * Both of the acts below are refused for the same reason — the place still
     * holds something — and the engine's sentence names how many units across
     * how many SKUs. A <Confirm> closes as soon as its action resolves, so a
     * toast fired from inside one is a sentence appearing where the dialog just
     * was and scrolling away from a table the operator is still reading. This
     * keeps it under the row, beside the numbers it is about, with the one
     * button that answers it. */
    let rowError = $state({ id: 0, message: "" });

    function refused(l, err) {
        rowError = { id: l.id, message: err.message };
    }

    function clearRowError() {
        rowError = { id: 0, message: "" };
    }

    /* A second drawer rather than a tab inside the editor: configuring a place
       and reading what has moved there are different jobs, and burying the form
       behind a tab would make both worse. */
    let historyOpen = $state(false);
    let historyFor = $state(null);

    /*
     * Two histories, and they answer different questions.
     *
     * The one above is the stock ledger: what moved in and out of this place.
     * This one is the record's own audit trail — who renamed it, who closed it,
     * who made it the default — and it is gated on locations.read rather than
     * on store.operate, so the person who runs the warehouse can answer it
     * without being able to read the whole store's feed. Both are on the row
     * because both are about that row; the labels say which is which.
     */
    let changesOpen = $state(false);
    let changesFor = $state(null);

    function openChanges(l, event) {
        event?.stopPropagation();
        changesFor = l;
        changesOpen = true;
    }

    /* What this place is holding, which is what the three number columns were
       pointing at and nothing could open. */
    let stockOpen = $state(false);
    let stockFor = $state(null);
    let stockRows = $state([]);
    let stockMeta = $state(null);
    let stockPage = $state(1);
    let stockLoading = $state(false);
    let nonzero = $state(true);

    /* The move form is inline in that drawer rather than a third stacked one:
       two <Drawer>s open at once each mount their own window-level Escape
       handler, so Escape closes both, and a click inside the upper one is
       outside the lower one's node and dismisses it along with its unsaved
       form. */
    let movingID = $state(0);
    let moveTo = $state(0);
    let moveQty = $state("");
    let moveReason = $state("");
    let moveError = $state("");
    let moving = $state(false);

    $effect(() => {
        load();
    });

    async function load() {
        // The screen is refused above; asking anyway would put a 403 toast
        // over the explanation.
        if (!readable) {
            loading = false;
            return;
        }
        loading = true;
        try {
            const result = await api.get("/api/admin/locations");
            locations = result.data ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function blank() {
        return {
            code: "",
            name: "",
            priority: 0,
            active: true,
            line1: "",
            line2: "",
            city: "",
            state: "",
            postal_code: "",
            country: "",
        };
    }

    function openNew() {
        editing = null;
        form = blank();
        error = "";
        open = true;
    }

    function openEdit(l) {
        editing = l;
        const a = l.address ?? {};
        form = {
            code: l.code,
            name: l.name,
            priority: l.priority,
            active: l.active,
            line1: a.line1 ?? "",
            line2: a.line2 ?? "",
            city: a.city ?? "",
            state: a.state ?? "",
            postal_code: a.postal_code ?? "",
            country: a.country ?? "",
        };
        error = "";
        open = true;
    }

    /** An address is sent only when something was typed into it. */
    function address() {
        const a = {
            line1: form.line1.trim(),
            line2: form.line2.trim(),
            city: form.city.trim(),
            state: form.state.trim(),
            postal_code: form.postal_code.trim(),
            country: form.country.trim().toUpperCase(),
        };
        return Object.values(a).some(Boolean) ? a : null;
    }

    async function save(event) {
        event?.preventDefault();
        saving = true;
        error = "";
        const body = {
            name: form.name.trim(),
            priority: Number(form.priority) || 0,
            active: form.active,
            address: address(),
        };
        try {
            if (editing) {
                await api.patch(`/api/admin/locations/${editing.id}`, body);
                toast.success("Location updated");
            } else {
                await api.post("/api/admin/locations", {
                    ...body,
                    code: form.code.trim().toLowerCase(),
                });
                toast.success("Location opened");
            }
            open = false;
            await load();
        } catch (err) {
            // Deactivating a location that still holds stock is refused, and
            // the message names how many units are stranded. That is the answer
            // the operator needs, so show it in place rather than as a toast
            // that scrolls away.
            error = err.message;
        } finally {
            saving = false;
        }
    }

    /**
     * Open or closed for new orders.
     *
     * The reversible act, inline, under its own word. It used to be reachable
     * only as a checkbox inside the editor — while the bin beside it said
     * "Close" and deleted the place outright.
     *
     * No confirmation: it is one click to put back, and the engine refuses it
     * outright while the shelf still holds anything, which is the only
     * consequence worth stopping for and is answered under the row.
     */
    async function setActive(l, active, event) {
        event?.stopPropagation();
        clearRowError();
        try {
            await api.patch(`/api/admin/locations/${l.id}`, { active });
            toast.success(
                active ? `${l.name} is open for new orders` : `${l.name} is closed to new orders`,
            );
            await load();
        } catch (err) {
            refused(l, err);
        }
    }

    /**
     * Making a location default redirects every future reservation in the store
     * and is where new stock lands when nothing says otherwise — so it asks
     * first. It was the one row action with no confirmation at all.
     */
    function askDefault(l, event) {
        event?.stopPropagation();
        clearRowError();
        confirmConfig = {
            title: `Make ${l.name} the default?`,
            message:
                "New stock lands here when nothing names a location, and it is where an " +
                "order looks first. The current default stays open and keeps what it holds.",
            confirmLabel: "Make default",
            danger: false,
            run: async () => {
                try {
                    await api.post(`/api/admin/locations/${l.id}/default`, {});
                    toast.success(`${l.name} is now the default`);
                    await load();
                } catch (err) {
                    refused(l, err);
                }
            },
        };
        confirmOpen = true;
    }

    function openHistory(l, event) {
        event?.stopPropagation();
        historyFor = l;
        historyOpen = true;
    }

    /**
     * What one place holds.
     *
     * The editor closes first, and that is not tidiness — see the note on the
     * move form above. The refusal has already been read by the time this is
     * pressed, and the operator's next act is on stock.
     */
    function openStock(l, event) {
        event?.stopPropagation();
        open = false;
        stockFor = l;
        stockPage = 1;
        nonzero = true;
        closeMove();
        stockOpen = true;
        loadStock();
    }

    async function loadStock() {
        if (!stockFor) return;
        stockLoading = true;
        try {
            const result = await api.get(
                `/api/admin/locations/${stockFor.id}/stock` +
                    query({
                        nonzero: nonzero ? "" : 0,
                        page: stockPage,
                        limit: STOCK_PER_PAGE,
                    }),
            );
            stockRows = result.data ?? [];
            stockMeta = result.meta ?? null;
        } catch (err) {
            toast.error(err);
        } finally {
            stockLoading = false;
        }
    }

    function toggleZeros() {
        nonzero = !nonzero;
        stockPage = 1;
        closeMove();
        loadStock();
    }

    function goStockPage(n) {
        stockPage = n;
        closeMove();
        loadStock();
    }

    function startMove(row) {
        movingID = row.id;
        // What can actually leave: reserved units do not travel. A shelf holding
        // nothing but reservations offers no default rather than a zero the
        // form would then refuse.
        const free = row.at_location?.available ?? 0;
        moveQty = free > 0 ? String(free) : "";
        moveReason = "";
        moveError = "";
        // An open destination, or the engine would refuse the default action.
        moveTo = (destinations.find((d) => d.active) ?? destinations[0])?.id ?? 0;
    }

    function closeMove() {
        movingID = 0;
        moveQty = "";
        moveReason = "";
        moveError = "";
    }

    /* Every other location, closed ones included but disabled with the reason.
       Hiding them would leave a silently shorter list explaining nothing, and
       the locations table two clicks away still shows them. */
    const destinations = $derived(locations.filter((l) => l.id !== stockFor?.id));

    async function submitMove(row) {
        const qty = parseInt(moveQty, 10);
        if (!Number.isFinite(qty) || qty <= 0) {
            moveError = "Move a positive number of units.";
            return;
        }
        if (!moveTo) {
            moveError = "Choose where the units are going.";
            return;
        }
        moving = true;
        moveError = "";
        try {
            await api.post(`/api/admin/variants/${row.id}/stock/transfer`, {
                from_location_id: stockFor.id,
                to_location_id: moveTo,
                quantity: qty,
                reason: moveReason.trim(),
            });
            toast.success(`Moved ${qty} × ${row.sku}`);
            closeMove();
            // A refusal that named the units just moved is no longer the
            // answer, and leaving it under the row would send the operator
            // looking for stock that has gone.
            clearRowError();
            await loadStock();
            await load();
            // The footer reads the location record, so it has to be the
            // refreshed one: the whole point is that it cannot drift from the
            // refusal above it.
            stockFor = locations.find((l) => l.id === stockFor.id) ?? stockFor;
        } catch (err) {
            // The engine's own sentences — a closed destination, or the floor
            // under what is reserved — belong beside the field that caused them.
            moveError = err.message;
        } finally {
            moving = false;
        }
    }

    /**
     * Deleting is not closing, and the two used to share the word.
     *
     * DELETE removes the location row and its stock rows outright; the title
     * and the body now say so, and the reversible act has its own button two
     * along. The engine refuses this while anything is held, and refuses it for
     * the default — both answers land under the row.
     */
    function askDelete(l, event) {
        event?.stopPropagation();
        clearRowError();
        confirmConfig = {
            title: `Delete ${l.name}?`,
            message:
                "This removes the location permanently, and it has to be empty first. " +
                "Orders already filled from here keep reading exactly as they do now — the " +
                "movements keep its code. To stop new orders using it without losing it, " +
                "close it instead.",
            confirmLabel: "Delete",
            danger: true,
            run: async () => {
                try {
                    await api.delete(`/api/admin/locations/${l.id}`);
                    toast.success(`${l.name} deleted`);
                    await load();
                } catch (err) {
                    refused(l, err);
                }
            },
        };
        confirmOpen = true;
    }

    // ------------------------------------------------------------ selection

    /*
     * The places an operator has picked. A store closing a region for the
     * winter was doing it one row at a time, and each close is a PATCH this
     * screen already makes from the lock button.
     *
     * Nothing clears the selection on a filter change here because this screen
     * has no filters and no pages: `GET /api/admin/locations` returns the lot,
     * and a store with more locations than fit on a screen does not exist yet.
     */
    const sel = selection();

    let bulkBusy = $state(false);

    const picked = $derived(sel.pick(locations));

    /*
     * What each action is legal for, taken from the row's own rules rather than
     * guessed at — the same predicates that decide which buttons a row gets.
     * The default location is excluded from closing and deleting because that
     * is where new stock lands and where an order looks first; the engine
     * refuses the delete and the row refuses the close.
     */
    const closable = $derived(picked.filter((l) => l.active && !l.is_default));
    const reopenable = $derived(picked.filter((l) => !l.active));
    const deletable = $derived(picked.filter((l) => !l.is_default));

    /**
     * Every bulk action is N calls to the per-row route this screen already
     * uses, and each reports its own refusal — "still holds 43 unit(s) across 7
     * SKU(s)" is the engine's sentence, named with the place it is about.
     */
    async function runOver(rows, fn, describe) {
        if (!rows.length) return;
        clearRowError();
        bulkBusy = true;
        try {
            await runBulk(rows, fn, { describe, noun: "location", label: (l) => l.name });
        } finally {
            bulkBusy = false;
        }
        sel.clear();
        await load();
    }

    const bulkActive = (active) =>
        runOver(
            active ? reopenable : closable,
            (l) => api.patch(`/api/admin/locations/${l.id}`, { active }),
            active ? "Reopened" : "Closed to new orders",
        );

    function askBulkDelete() {
        const rows = deletable;
        confirmConfig = {
            title: `Delete ${rows.length} ${pluralize(rows.length, "location")}?`,
            message:
                "This removes them permanently, and each has to be empty first — any that still " +
                "hold something are refused by name. Orders already filled from them keep " +
                "reading exactly as they do now. To stop new orders using a place without " +
                "losing it, close it instead.",
            confirmLabel: "Delete",
            danger: true,
            run: () =>
                runOver(rows, (l) => api.delete(`/api/admin/locations/${l.id}`), "Deleted"),
        };
        confirmOpen = true;
    }

    function where(l) {
        if (!l.address) return "";
        return [l.address.city, l.address.country].filter(Boolean).join(", ");
    }

    const totalUnits = $derived(locations.reduce((sum, l) => sum + l.on_hand, 0));
</script>

<svelte:head><title>Locations · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="locations.read" what="locations" />
{:else}

<div class="page page-locations">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Locations</div>
            </nav>
            <div class="flex-fill"></div>
            <div class="page-header-primary-btns">
                {#if writable}
                    <button type="button" class="btn" onclick={openNew}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New location</span>
                    </button>
                {/if}
            </div>
        </header>

        <!-- DESIGN.md §5: a border and a background step, never a shadow. -->
        <div class="page-table-wrapper tw:rounded-xl tw:border">
            <!-- `responsive-table` restacks each row as a card below 600px,
                 labelling the cells from their `data-name`. Every cell here
                 already carried one, but the class was never added, so on a
                 phone the sticky name and meta columns squeezed the middle of
                 the table out of existence: Where, On hand, Reserved, SKUs and
                 State were not scrolled off, they were gone. A locations screen
                 that cannot say what a location holds is the one thing it is
                 for. -->
            <table class="table responsive-table">
                <thead class="sticky">
                    <tr>
                        {#if writable}
                            <th class="col-bulk-select min-width">
                                <div class="field">
                                    <input
                                        id="select-all-locations"
                                        type="checkbox"
                                        checked={sel.allSelected(locations)}
                                        onchange={() => sel.toggleAll(locations)}
                                    />
                                    <label
                                        for="select-all-locations"
                                        aria-label="Select every location"
                                    ></label>
                                </div>
                            </th>
                        {/if}
                        <th class="col-field-name-id">Name</th>
                        <th class="col-field-type-text">Where</th>
                        <th class="col-field-type-number min-width">On hand</th>
                        <th class="col-field-type-number min-width">Reserved</th>
                        <th class="col-field-type-number min-width">SKUs</th>
                        <th class="col-field-type-select">State</th>
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each locations as l (l.id)}
                        <tr
                            class="handle"
                            tabindex="0"
                            onclick={() => openEdit(l)}
                            onkeydown={(e) => rowKey(e, () => openEdit(l))}
                        >
                            {#if writable}
                                <!-- stopPropagation rather than a guard inside
                                     the row handler: ticking a box must not also
                                     open the location. -->
                                <td
                                    class="col-bulk-select min-width"
                                    onclick={(e) => e.stopPropagation()}
                                >
                                    <div class="field">
                                        <input
                                            id="select-location-{l.id}"
                                            type="checkbox"
                                            checked={sel.has(l.id)}
                                            onchange={() => sel.toggle(l.id)}
                                        />
                                        <label
                                            for="select-location-{l.id}"
                                            aria-label="Select {l.name}"
                                        ></label>
                                    </div>
                                </td>
                            {/if}
                            <td class="col-field-name-id" data-name="Name">
                                <div class="txt-bold">{l.name}</div>
                                <div class="txt-hint txt-sm txt-code">{l.code}</div>
                            </td>
                            <td class="col-field-type-text txt-hint" data-name="Where">
                                {where(l) || "—"}
                            </td>
                            <!-- The number an operator is staring at is the
                                 thing they want to click: these three cells were
                                 the reason to come here and led nowhere. -->
                            <td class="col-field-type-number min-width" data-name="On hand">
                                <button
                                    type="button"
                                    class="btn sm transparent"
                                    title="What {l.name} holds"
                                    onclick={(e) => openStock(l, e)}
                                >
                                    {l.on_hand}
                                </button>
                            </td>
                            <td
                                class="col-field-type-number min-width txt-hint"
                                data-name="Reserved"
                            >
                                <button
                                    type="button"
                                    class="btn sm transparent"
                                    title="What {l.name} holds"
                                    onclick={(e) => openStock(l, e)}
                                >
                                    {l.reserved}
                                </button>
                            </td>
                            <td
                                class="col-field-type-number min-width txt-hint"
                                data-name="SKUs"
                            >
                                <button
                                    type="button"
                                    class="btn sm transparent"
                                    title="What {l.name} holds"
                                    onclick={(e) => openStock(l, e)}
                                >
                                    {l.skus}
                                </button>
                            </td>
                            <!-- Both facts, not one: a location can be the
                                 default AND closed (the engine refuses to make
                                 a closed one default, but not to close the one
                                 that already is), and a row that said only
                                 "default" hid the half that explains why
                                 nothing is landing there. -->
                            <td class="col-field-type-select" data-name="State">
                                {#if l.is_default}
                                    <span class="label success">default</span>
                                {/if}
                                {#if !l.active}
                                    <span class="label">closed</span>
                                {:else if !l.is_default}
                                    <span class="label">open</span>
                                {/if}
                            </td>
                            <td class="col-meta min-width">
                                <!-- Outside the is_default guard: the default
                                     location has the most history of all, and
                                     "this location still holds 4 units across 2
                                     SKUs" is the refusal this answers. -->
                                <!-- Also here, for a row whose figures are all
                                     zero and so have nothing to click. -->
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    aria-label="What {l.name} holds"
                                    title="Stock"
                                    onclick={(e) => openStock(l, e)}
                                >
                                    <i class="ri-stack-line" aria-hidden="true"></i>
                                </button>
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    aria-label="Stock history for {l.name}"
                                    title="Stock history"
                                    onclick={(e) => openHistory(l, e)}
                                >
                                    <i class="ri-history-line" aria-hidden="true"></i>
                                </button>
                                <!-- The record's own trail, not the ledger:
                                     who renamed this place, closed it, or made
                                     it the default. -->
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    aria-label="Change history for {l.name}"
                                    title="Change history"
                                    onclick={(e) => openChanges(l, e)}
                                >
                                    <i class="ri-file-history-line" aria-hidden="true"></i>
                                </button>
                                <!-- Closing and deleting are two acts and now
                                     two buttons. The toggle is here rather than
                                     only inside the editor because it is the
                                     one an operator actually wants — and the
                                     bin, which really does delete, no longer
                                     borrows its word. -->
                                {#if !l.active}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary"
                                        aria-label="Reopen {l.name} for new orders"
                                        title="Reopen"
                                        onclick={(e) => setActive(l, true, e)}
                                    >
                                        <i class="ri-lock-unlock-line" aria-hidden="true"></i>
                                    </button>
                                {:else if !l.is_default}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary"
                                        aria-label="Close {l.name} to new orders"
                                        title="Close to new orders"
                                        onclick={(e) => setActive(l, false, e)}
                                    >
                                        <i class="ri-lock-line" aria-hidden="true"></i>
                                    </button>
                                {/if}
                                {#if writable && !l.is_default}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary"
                                        aria-label="Make {l.name} the default"
                                        title="Make default"
                                        onclick={(e) => askDefault(l, e)}
                                    >
                                        <i class="ri-pushpin-line" aria-hidden="true"></i>
                                    </button>
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary row-delete"
                                        aria-label="Delete {l.name}"
                                        title="Delete"
                                        onclick={(e) => askDelete(l, e)}
                                    >
                                        <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                    </button>
                                {/if}
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>

                        {#if rowError.id === l.id}
                            <!-- The engine's own sentence — "still holds 43
                                 unit(s) across 7 SKU(s)" — under the row whose
                                 numbers it is about, with the button that shows
                                 them. -->
                            <tr>
                                <td colspan={writable ? 8 : 7}>
                                    <div class="field-help error">{rowError.message}</div>
                                    <div class="inline-flex gap-5 m-t-5">
                                        <button
                                            type="button"
                                            class="btn sm secondary"
                                            onclick={(e) => openStock(l, e)}
                                        >
                                            <i class="ri-stack-line" aria-hidden="true"></i>
                                            <span class="txt">See what it holds</span>
                                        </button>
                                        <button
                                            type="button"
                                            class="btn sm transparent"
                                            onclick={clearRowError}
                                        >
                                            <span class="txt">Dismiss</span>
                                        </button>
                                    </div>
                                </td>
                            </tr>
                        {/if}
                    {/each}

                    {#if loading && !locations.length}
                        {#each Array(2) as _, i (i)}
                            <tr><td colspan={writable ? 8 : 7}><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {/if}
                </tbody>
            </table>
        </div>

        {#if writable}
            <BulkBar count={sel.count} noun="location" onclear={() => sel.clear()}>
                <!-- Each button says how many of the selection it can act on:
                     the default location cannot be closed or deleted, and a
                     button claiming all six would be promising a refusal. -->
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={bulkBusy || !closable.length}
                    title="A place has to be empty to close, and the default cannot be closed at all"
                    onclick={() => bulkActive(false)}
                >
                    <i class="ri-lock-line" aria-hidden="true"></i>
                    <span class="txt">Close ({closable.length})</span>
                </button>
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={bulkBusy || !reopenable.length}
                    title="Open these again for new orders"
                    onclick={() => bulkActive(true)}
                >
                    <i class="ri-lock-unlock-line" aria-hidden="true"></i>
                    <span class="txt">Reopen ({reopenable.length})</span>
                </button>
                <button
                    type="button"
                    class="btn sm secondary txt-danger"
                    disabled={bulkBusy || !deletable.length}
                    title="Deleting is permanent and each has to be empty first"
                    onclick={askBulkDelete}
                >
                    <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                    <span class="txt">Delete ({deletable.length})</span>
                </button>
            </BulkBar>
        {/if}

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <span class="txt">
                {locations.length}
                {pluralize(locations.length, "location")} holding {totalUnits}
                {pluralize(totalUnits, "unit")}. Orders are filled from the first one, top to
                bottom, that can cover the line.
            </span>
            <ThemeToggle />
        </footer>
    </div>
</div>

<Drawer
    {open}
    size="popup"
    title={editing ? `Edit ${editing.name}` : "New location"}
    onclose={() => (open = false)}
>
    <form id="location-form" onsubmit={save}>
        <div class="fields">
            <div class="field required">
                <label for="l-name">Name</label>
                <input id="l-name" type="text" bind:value={form.name} placeholder="North warehouse" />
            </div>
            <div class="delimiter"></div>
            <div class="field required">
                <label for="l-code">Code</label>
                <input
                    id="l-code"
                    type="text"
                    bind:value={form.code}
                    disabled={!!editing}
                    placeholder="warehouse"
                />
            </div>
        </div>
        <div class="field-help">
            The code is what an import or a script refers to, so it does not change once orders
            have been filled from here.
        </div>

        <div class="fields m-t-sm">
            <div class="field">
                <label for="l-priority">Priority</label>
                <input id="l-priority" type="number" bind:value={form.priority} />
            </div>
            <div class="delimiter"></div>
            <div class="field">
                <span class="label-block"></span>
                <div class="inline-flex">
                    <input id="l-active" type="checkbox" bind:checked={form.active} />
                    <label for="l-active">Open for new orders</label>
                </div>
            </div>
        </div>
        <div class="field-help">
            Lower is tried first. A location has to be empty before it can be closed — otherwise
            its units would be counted as in stock while nothing could reserve them.
        </div>

        <div class="field m-t-base">
            <label for="l-line1">Address</label>
            <input id="l-line1" type="text" bind:value={form.line1} placeholder="Line 1" />
        </div>
        <div class="field m-t-5">
            <input type="text" bind:value={form.line2} placeholder="Line 2" aria-label="Line 2" />
        </div>
        <div class="fields m-t-5">
            <div class="field">
                <input type="text" bind:value={form.city} placeholder="City" aria-label="City" />
            </div>
            <div class="delimiter"></div>
            <div class="field">
                <input
                    type="text"
                    bind:value={form.state}
                    placeholder="State"
                    aria-label="State"
                />
            </div>
        </div>
        <div class="fields m-t-5">
            <div class="field">
                <input
                    type="text"
                    bind:value={form.postal_code}
                    placeholder="Postal code"
                    aria-label="Postal code"
                />
            </div>
            <div class="delimiter"></div>
            <div class="field">
                <Select
                    ariaLabel="Country"
                    placeholder="Country"
                    bind:value={form.country}
                    options={COUNTRY_OPTIONS}
                />
            </div>
        </div>
        <div class="field-help">
            Only needed where it is used: a pickup point a shopper is sent to, or the origin on a
            customs form.
        </div>

        {#if error}
            <div class="field-help error m-t-sm">{error}</div>
            {#if editing}
                <!-- The whole gap in one control: the refusal names 43 units,
                     and now the sentence that names them can show them. -->
                <button
                    type="button"
                    class="btn sm secondary m-t-5"
                    onclick={() => openStock(editing)}
                >
                    <i class="ri-stack-line" aria-hidden="true"></i>
                    <span class="txt">See what it holds</span>
                </button>
            {/if}
        {/if}
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (open = false)}>
            <span class="txt">{writable ? "Cancel" : "Close"}</span>
        </button>
        <!-- Reading a location's address and hours is what locations.read is
             for, so the drawer stays; only the button that writes goes. -->
        {#if writable}
            <button
                type="submit"
                form="location-form"
                class="btn expanded"
                class:loading={saving}
                disabled={saving}
            >
                <span class="txt">{editing ? "Save changes" : "Open location"}</span>
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

<Drawer
    open={historyOpen}
    size="lg"
    title={historyFor ? `Stock history — ${historyFor.name}` : ""}
    onclose={() => (historyOpen = false)}
>
    {#if historyFor}
        {#key historyFor.id}
            <StockHistory scope="location" id={historyFor.id} showSku />
        {/key}
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent" onclick={() => (historyOpen = false)}>
            <span class="txt">Close</span>
        </button>
    {/snippet}
</Drawer>

<!--
    What this place is holding. A full-height drawer because it is a list, not a
    form, and read-only: `.drawer-table` rather than `.stock-locations`, which
    documents itself as a picker and paints a pointer on every row.
-->
<Drawer
    open={stockOpen}
    size=""
    title={stockFor ? `What ${stockFor.name} holds` : ""}
    onclose={() => ((stockOpen = false), closeMove())}
>
    {#if stockFor}
        <div class="inline-flex m-b-sm">
            <input
                id="stock-zeros"
                type="checkbox"
                checked={!nonzero}
                onchange={toggleZeros}
            />
            <label for="stock-zeros">Include shelves at zero</label>
        </div>
        <div class="field-help m-b-sm">
            A shelf counts as held when it has units on hand or reserved — the same test that
            refuses a close, so this list and that refusal always agree. Only SKUs this location
            has carried appear at all.
        </div>

        <div class="page-table-wrapper">
            <table class="table drawer-table">
                <thead>
                    <tr>
                        <th>SKU</th>
                        <th class="txt-right">On hand</th>
                        <th class="txt-right">Reserved</th>
                        <th class="txt-right">Available</th>
                        <th class="min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each stockRows as r (r.id)}
                        {@const at = r.at_location}
                        <tr>
                            <td>
                                <span class="txt-bold txt-code">{r.sku}</span>
                                {#if r.label}
                                    <div class="txt-hint txt-sm">{r.label}</div>
                                {/if}
                            </td>
                            <td class="txt-right">{at?.on_hand ?? 0}</td>
                            <td class="txt-right txt-hint">{at?.reserved ?? 0}</td>
                            <td class="txt-right txt-bold {stockClass(at?.available ?? 0)}">
                                {at?.available ?? 0}
                            </td>
                            <td class="min-width">
                                <button
                                    type="button"
                                    class="btn sm secondary"
                                    onclick={() =>
                                        movingID === r.id ? closeMove() : startMove(r)}
                                >
                                    <span class="txt">Move</span>
                                </button>
                            </td>
                        </tr>
                        {#if movingID === r.id}
                            <tr>
                                <td colspan="5">
                                    <div class="fields">
                                        <div class="field required">
                                            <label for="move-dest">Move to</label>
                                            <select id="move-dest" bind:value={moveTo}>
                                                {#each destinations as d (d.id)}
                                                    <option value={d.id} disabled={!d.active}>
                                                        {d.name}{d.active ? "" : " — closed"}
                                                    </option>
                                                {/each}
                                            </select>
                                        </div>
                                        <div class="delimiter"></div>
                                        <div class="field required">
                                            <label for="move-qty">How many</label>
                                            <input
                                                id="move-qty"
                                                type="number"
                                                min="1"
                                                bind:value={moveQty}
                                                oninput={() => (moveError = "")}
                                            />
                                        </div>
                                    </div>
                                    <div class="field m-t-5">
                                        <label for="move-why">Why</label>
                                        <input
                                            id="move-why"
                                            type="text"
                                            maxlength="200"
                                            bind:value={moveReason}
                                            placeholder="Clearing the shelf"
                                        />
                                    </div>
                                    <div class="field-help">
                                        Reserved units stay where they are — they are promised to
                                        orders that will be picked from here. A closed location
                                        cannot receive them.
                                    </div>
                                    {#if moveError}
                                        <div class="field-help error">{moveError}</div>
                                    {/if}
                                    <div class="inline-flex gap-5 m-t-5">
                                        <button
                                            type="button"
                                            class="btn sm"
                                            class:loading={moving}
                                            disabled={moving}
                                            onclick={() => submitMove(r)}
                                        >
                                            <span class="txt">Move</span>
                                        </button>
                                        <button
                                            type="button"
                                            class="btn sm transparent"
                                            onclick={closeMove}
                                        >
                                            <span class="txt">Cancel</span>
                                        </button>
                                    </div>
                                </td>
                            </tr>
                        {/if}
                    {/each}

                    {#if stockLoading && !stockRows.length}
                        {#each Array(3) as _, i (i)}
                            <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {/if}

                    {#if !stockLoading && !stockRows.length}
                        <tr>
                            <td colspan="5" class="txt-center txt-hint p-base">
                                {nonzero
                                    ? "This location is holding nothing."
                                    : "This location has never carried anything."}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>

        <!--
            Read off the location record rather than summed over the rows on
            screen: this is refuseIfHolding's own arithmetic, so the footer
            cannot disagree with the refusal on a location with more than one
            page of SKUs.
        -->
        <div class="field-help m-t-sm">
            {stockFor.on_hand + stockFor.reserved}
            {pluralize(stockFor.on_hand + stockFor.reserved, "unit")} across {stockFor.skus}
            {pluralize(stockFor.skus, "SKU")} here.
        </div>
    {/if}

    {#snippet footer()}
        <Pager meta={stockMeta} loading={stockLoading} noun="SKU" onpage={goStockPage} />
        <div class="flex-fill"></div>
        <button
            type="button"
            class="btn transparent"
            onclick={() => ((stockOpen = false), closeMove())}
        >
            <span class="txt">Close</span>
        </button>
    {/snippet}
</Drawer>

<RecordHistory
    open={changesOpen}
    kind="locations"
    id={changesFor?.id}
    label={changesFor?.name}
    onclose={() => (changesOpen = false)}
/>
{/if}
