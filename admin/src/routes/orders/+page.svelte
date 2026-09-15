<script>
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, can, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { selection } from "$lib/selection.svelte.js";
    import { runBulk } from "$lib/bulk.js";
    import { onNewShortcut } from "$lib/shortcuts.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { readSort, cycleSort, sortQuery } from "$lib/listsort.js";
    import {
        formatMoney,
        formatDate,
        relativeTime,
        orderStatusClass,
        orderStatusLabel,
        paymentLabel,
    } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { downloadFile } from "$lib/download.js";
    import BulkBar from "$lib/components/BulkBar.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import SortHeader from "$lib/components/SortHeader.svelte";
    import OrderPlaced from "$lib/components/OrderPlaced.svelte";
    import VariantPicker from "$lib/components/VariantPicker.svelte";
    import { COUNTRIES } from "$lib/countries.js";

    /* The starting page size, not the only one: it is a listState key below, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 25;

    /* Every filter, the ordering and the page live in the address bar. This
       screen always needed that — Customers links here with `?email=` already
       set — and the rest follow the same road now rather than a second one.

       Two search parameters, deliberately. `q` is what the box writes: a number
       by prefix, an email or a name by contains. `email` is the exact-match
       filter Customers links with, where the whole address is on screen and
       means that one person — and where `_`, a LIKE wildcard, is common enough
       in real addresses that widening it would quietly turn a value into a
       pattern. Touching the box drops the exact filter, because that is the
       moment the intent stops being "that person".

       `from` and `to` are civil dates, inclusive and EXCLUSIVE respectively,
       which is what the engine means by them everywhere else. They were the one
       filter the API has always accepted and this screen has never offered,
       which made "today's orders" and "this week's unpaid" unaskable from the
       panel at all. */
    const list = listState({
        status: "",
        payment_status: "",
        q: "",
        email: "",
        from: "",
        to: "",
        sort: "",
        order: "",
        page: 1,
        limit: PER_PAGE,
    });
    const SORT_FIELDS = ["number", "name", "status", "payment_status", "total", "created_at"];

    /* orders.read is what the nav gates this screen on; without it the address
       used to render a fully armed screen and 403 on load. */
    const readable = $derived(can("orders.read"));
    const writable = $derived(can("orders.write"));
    /* Booking a parcel is orders.fulfill and nothing else — the same cut the
       order screen makes and the same one the engine makes on
       create-fulfillment. A warehouse role carries it without orders.write, so
       the selection column below cannot be gated on orders.write alone or the
       one person whose whole job is shipping could not tick a row. */
    const mayFulfill = $derived(can("orders.fulfill"));
    const bulkable = $derived(writable || mayFulfill);

    let loading = $state(true);
    let orders = $state([]);
    let meta = $state(null);

    const status = $derived(list.params.status);
    const paymentStatus = $derived(list.params.payment_status);
    const email = $derived(list.params.email);
    const search = $derived(list.params.q);
    const sort = $derived(readSort(list.params, SORT_FIELDS));
    const perPage = $derived(list.params.limit);
    /* Seeded from whichever of the two is on the URL, so a link from Customers
       shows the address it filtered by in the box the operator would clear. */
    let draftSearch = $state(list.params.q || list.params.email);

    /* A fast second header click leaves two requests in flight; without this
       the table settles on the reply that lost rather than on the header that
       is lit. */
    let reqId = 0;

    $effect(() => {
        // Re-runs whenever any parameter changes: a filter, the ordering, the
        // page. The window replaces the rows rather than accumulating them.
        list.params;
        load();
    });

    async function load() {
        // The screen is refused above; asking anyway would put a 403 toast
        // over the explanation.
        if (!readable) {
            loading = false;
            return;
        }
        const mine = ++reqId;
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/orders" + list.query({ limit: perPage, ...sortQuery(sort) }),
            );
            if (mine !== reqId) return;
            orders = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
        loadCounts();
    }

    /* ----------------------------------------------- counts per status
     *
     * What each choice in the status filter would actually return, shown
     * beside it. Without them the dropdown is six guesses: an operator opens
     * it to find the confirmed orders and cannot tell, until they pick one,
     * whether there are three or none.
     *
     * The counts honour every OTHER filter on the screen — the search box, the
     * date range, the payment status — and only override status itself. A
     * count that ignored the date range would read "Delivered 214" above a
     * table showing two, which is worse than no count at all.
     *
     * Seven requests, each `limit=1`: the number comes from `meta.total`, so
     * the rows are never fetched. It is the same trick the dashboard uses for
     * its tiles, and they go out in parallel with the listing itself.
     */
    const STATUSES = ["pending", "confirmed", "partial", "shipped", "delivered", "cancelled"];
    let statusCounts = $state({});
    let countsId = 0;

    async function loadCounts() {
        if (!readable) return;
        const mine = ++countsId;
        const ask = async (value) => {
            const q = list.query({ status: value, limit: 1, page: 1, sort: "", order: "" });
            const res = await api.get("/api/admin/orders" + q);
            return [value, res.meta?.total ?? 0];
        };
        try {
            const pairs = await Promise.all(["", ...STATUSES].map(ask));
            if (mine !== countsId) return;
            statusCounts = Object.fromEntries(pairs);
        } catch {
            // A count is an adornment. If it cannot be had, the filter still
            // works and the numbers simply do not appear.
            if (mine === countsId) statusCounts = {};
        }
    }

    const statusOptions = $derived([
        { value: "", label: "Any status", count: statusCounts[""] },
        { value: "pending", label: "Pending", count: statusCounts.pending },
        { value: "confirmed", label: "Confirmed", count: statusCounts.confirmed },
        { value: "partial", label: "Partly shipped", count: statusCounts.partial },
        { value: "shipped", label: "Shipped", count: statusCounts.shipped },
        { value: "delivered", label: "Delivered", count: statusCounts.delivered },
        { value: "cancelled", label: "Cancelled", count: statusCounts.cancelled },
    ]);

    function sortBy(field, firstDesc) {
        list.set(cycleSort(sort, field, firstDesc));
    }

    function submitSearch(e) {
        e.preventDefault();
        list.set({ q: draftSearch, email: "" });
    }

    function clearSearch() {
        draftSearch = "";
        list.set({ q: "", email: "" });
    }

    // ------------------------------------------------------------- the window

    /*
     * Presets are computed as LOCAL civil dates and sent as YYYY-MM-DD, with
     * `to` set to the day AFTER the last day wanted. `to` is exclusive
     * everywhere in this API and this screen must not be the one place it is
     * not — which is why the custom picker's label says so out loud. The same
     * arithmetic the Reports screen does, for the same reason.
     */
    function civil(date) {
        const pad = (n) => String(n).padStart(2, "0");
        return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
    }

    function daysAgo(n) {
        const d = new Date();
        d.setHours(0, 0, 0, 0);
        d.setDate(d.getDate() - n);
        return d;
    }

    function monthStart(offset) {
        const d = new Date();
        return new Date(d.getFullYear(), d.getMonth() + offset, 1);
    }

    const PRESETS = {
        today: () => ({ from: civil(daysAgo(0)), to: civil(daysAgo(-1)) }),
        "7d": () => ({ from: civil(daysAgo(6)), to: civil(daysAgo(-1)) }),
        "30d": () => ({ from: civil(daysAgo(29)), to: civil(daysAgo(-1)) }),
        month: () => ({ from: civil(monthStart(0)), to: civil(daysAgo(-1)) }),
        lastMonth: () => ({ from: civil(monthStart(-1)), to: civil(monthStart(0)) }),
    };

    /* Which preset the URL currently describes — so a shared link comes back
       with the right control selected instead of reading as "custom". */
    const presetRange = $derived.by(() => {
        const p = list.params;
        if (!p.from && !p.to) return "";
        for (const [name, build] of Object.entries(PRESETS)) {
            const want = build();
            if (want.from === p.from && want.to === p.to) return name;
        }
        return "custom";
    });

    /*
     * Custom is a MODE, not a window, and that is why it is held rather than
     * inferred.
     *
     * The derivation above can name any preset from the dates alone, but it can
     * never name "custom" for a window that happens to match one — and the seed
     * `chooseRange("custom")` writes on an unfiltered list is
     * character-for-character the Last 30 days preset. So the derivation read
     * the seed straight back as "30d", the pickers below stayed hidden and the
     * dropdown snapped to the preset it was on: picking Custom silently applied
     * Last 30 days and there was no way to ask for an arbitrary window at all.
     *
     * Holding the choice fixes that without costing the shared link anything.
     * The mode is only consulted for "custom"; every other value still comes
     * off the URL, so a link arriving with `from`/`to` in it lights the preset
     * it matches, and "Any date" clears the mode on its way past.
     */
    let chosenRange = $state("");
    const range = $derived(chosenRange === "custom" ? "custom" : presetRange);

    const RANGE_OPTIONS = [
        { value: "", label: "Any date" },
        { value: "today", label: "Today" },
        { value: "7d", label: "Last 7 days" },
        { value: "30d", label: "Last 30 days" },
        { value: "month", label: "This month" },
        { value: "lastMonth", label: "Last month" },
        { value: "custom", label: "Custom…" },
    ];

    function chooseRange(value) {
        chosenRange = value;
        if (!value) {
            list.set({ from: "", to: "" });
            return;
        }
        if (value === "custom") {
            // Seed the pickers with the window already on screen rather than
            // blanking it: the operator is narrowing what they can see.
            list.set({
                from: list.params.from || civil(daysAgo(29)),
                to: list.params.to || civil(daysAgo(-1)),
            });
            return;
        }
        list.set(PRESETS[value]());
    }

    /* The last day the window actually covers. `to` is exclusive, so the day an
       operator means is the one before it — said out loud under the pickers
       rather than left as a trap. */
    const windowWords = $derived.by(() => {
        const { from, to } = list.params;
        if (!from && !to) return "";
        const spoken = (iso, shift) => {
            const d = new Date(iso + "T00:00:00");
            if (shift) d.setDate(d.getDate() + shift);
            return formatDate(d.toISOString(), { withTime: false });
        };
        if (from && to) return `${spoken(from)} to ${spoken(to, -1)}, inclusive.`;
        if (from) return `${spoken(from)} onwards.`;
        return `Everything before ${spoken(to, -1)}, inclusive.`;
    });

    /**
     * The CSV, cut to the window on screen.
     *
     * The button used to call the bare path, so a store with any history got one
     * row per line for every order ever placed — or nothing. The export route
     * takes status, from and to and nothing else, which is why the title says
     * what it will not carry: silently exporting more rows than the screen
     * shows would be worse than not offering the button.
     */
    function closeMore() {
        document.getElementById("orders-more")?.hidePopover();
    }

    function exportCSV() {
        const p = list.params;
        const suffix = new URLSearchParams();
        if (p.status) suffix.set("status", p.status);
        if (p.from) suffix.set("from", p.from);
        if (p.to) suffix.set("to", p.to);
        const qs = suffix.toString();
        downloadFile(
            "/api/admin/export/admin-orders" + (qs ? "?" + qs : ""),
            "orders.csv",
            "text/csv",
        );
    }

    /* Which of the filters on screen the export route cannot be given. Named
       rather than hinted at: an accountant handed a file that quietly ignored
       the search box is worse off than one told to narrow it differently. */
    const uncarried = $derived(
        [
            search || email ? "the search" : "",
            paymentStatus ? "the payment filter" : "",
        ].filter(Boolean),
    );

    /**
     * The picking list, over the window on screen or over a selection.
     *
     * Unlike the export there is nothing it cannot carry: /orders/picking reads
     * the same six parameters this screen keeps in the URL and asks the same
     * listing for them, so the run is the table. The selection form takes ids
     * instead, because a picker's selection is deliberately not a filter.
     */
    const pickingHref = $derived.by(() => {
        const p = list.params;
        return `${base}/orders/picking${query({
            status: p.status,
            payment_status: p.payment_status,
            q: p.q,
            email: p.email,
            from: p.from,
            to: p.to,
        })}`;
    });


    // ---------------------------------------------------------- in bulk

    /*
     * Ten cash-on-delivery orders arriving in one batch was ten drawer opens,
     * and a van-load of parcels coming back signed for was another ten. Each of
     * these is the same per-row route the order screen already calls — there is
     * no batch endpoint and nothing here invents one.
     *
     * The selection survives a page change and is cleared by a filter change,
     * which is what stops an action landing on rows nobody can see. `page` is
     * deliberately absent from the effect below.
     */
    const sel = selection();

    $effect(() => {
        list.params.status;
        list.params.payment_status;
        list.params.q;
        list.params.email;
        list.params.from;
        list.params.to;
        sel.clear();
    });

    let bulkBusy = $state(false);
    let cancelOpen = $state(false);

    const picked = $derived(sel.pick(orders));

    /* The selection as a picking run. Ids rather than a filter, because a
       selection is precisely the thing no filter describes. */
    const pickingSelectionHref = $derived(
        `${base}/orders/picking?ids=${picked.map((o) => o.id).join(",")}`,
    );

    /*
     * What each action is legal for, taken from the order screen's own rules
     * rather than guessed at. Filtering before the run is what bulk.js asks
     * for: an operator should not be told about four refusals they could have
     * been spared, and should still be told about any the engine makes.
     *
     * Not `payment_status === "pending"`: a declined card is payable on a
     * second attempt, while a cancelled order keeps its payment pending and the
     * engine answers 409.
     */
    const payable = $derived(
        picked.filter(
            (o) =>
                o.payment_status !== "paid" &&
                o.payment_status !== "refunded" &&
                o.status !== "cancelled",
        ),
    );
    const deliverable = $derived(picked.filter((o) => o.status === "shipped"));
    /* The order screen's own rule for when the Ship button appears: confirmed,
       or partial with a remainder still to go. Anything else — pending, already
       shipped, delivered, cancelled — the engine refuses, so it is dropped from
       the run rather than turned into a toast the operator could have been
       spared. */
    const shippable = $derived(
        picked.filter((o) => o.status === "confirmed" || o.status === "partial"),
    );
    const cancellable = $derived(
        picked.filter(
            (o) =>
                o.status !== "cancelled" &&
                o.status !== "partial" &&
                o.status !== "shipped" &&
                o.status !== "delivered",
        ),
    );

    async function runOver(rows, fn, describe) {
        if (!rows.length) return;
        bulkBusy = true;
        try {
            await runBulk(rows, fn, { describe, noun: "order", label: (o) => o.number });
        } finally {
            bulkBusy = false;
        }
        sel.clear();
        await load();
    }

    const bulkMarkPaid = () =>
        // No reference: one string cannot be the reconciliation reference for
        // ten different payments, and the engine only writes the field when it
        // is not blank — so the per-order drawer stays the place to record one.
        runOver(payable, (o) => api.post(`/api/admin/orders/${o.id}/mark-paid`, {}), "Marked paid");

    const bulkDeliver = () =>
        runOver(deliverable, (o) => api.post(`/api/admin/orders/${o.id}/deliver`), "Marked delivered");

    /**
     * The provider a batch is booked through: `manual` where this build has it,
     * otherwise whatever is installed. The same choice the order screen's ship
     * dialog opens on, read from the same store settings — the panel used to
     * post the literal "manual" and a store with a carrier module could not
     * book through it at all.
     */
    const defaultProvider = $derived(
        settings.fulfillmentProviders.find((p) => p.code === "manual")?.code ??
            settings.fulfillmentProviders[0]?.code ??
            "manual",
    );

    /**
     * Shipping the batch.
     *
     * `lines` is deliberately absent, which is what the order screen's dialog
     * defaults to as well: the engine then works out what is left under the
     * order's own row lock rather than trusting a list that may be a minute
     * old. No tracking number either — one number cannot belong to eight
     * parcels, and the engine writes the field only when it is not blank, so
     * the order screen stays the place a number is recorded.
     */
    const bulkShip = () =>
        runOver(
            shippable,
            (o) =>
                api.post("/api/admin/create-fulfillment", {
                    order_id: o.id,
                    provider: defaultProvider,
                }),
            "Shipped",
        );

    const bulkCancel = () =>
        runOver(
            cancellable,
            // Reason left empty for the same reason the payment reference is:
            // one sentence cannot honestly explain ten cancellations.
            (o) => api.post(`/api/admin/orders/${o.id}/cancel`, {}),
            "Cancelled",
        );

    // ---------------------------------------------------------- new order

    /**
     * The order being placed by hand: the phone order, the trade counter.
     *
     * It goes through the same checkout a shopper uses, so what this collects
     * is exactly what a shopper supplies — who they are, where it goes, what
     * they are buying, and how they are paying.
     */
    let createOpen = $state(false);
    let creating = $state(false);
    let createErrors = $state({});
    let draft = $state(blankOrder());
    let addLineVariant = $state("");
    let addLinePicked = $state(null);
    /**
     * The whole create response, once there is one: {order, payment}.
     *
     * The drawer holds open on it rather than closing, because this response
     * carries the order's access token — the guest's only credential for their
     * own order, returned here and by no other route. A toast would be
     * dismissible, unselectable and gone.
     */
    let placed = $state(null);

    /* What the store can actually take, named as the shopper would see it. */
    const methods = $derived(settings.paymentMethods);
    const paymentOptions = $derived(methods.map((m) => ({ value: m.code, label: m.name || m.code })));

    function blankOrder() {
        return {
            email: "",
            name: "",
            phone: "",
            payment_method: "",
            discount_code: "",
            address: { line1: "", line2: "", city: "", state: "", postal_code: "", country: "" },
            lines: [],
        };
    }

    function openCreate() {
        draft = blankOrder();
        createErrors = {};
        addLineVariant = "";
        addLinePicked = null;
        placed = null;
        createOpen = true;
        if (!draft.payment_method && methods.length) {
            draft.payment_method = methods[0].code;
        }
    }

    const draftOrderTotal = $derived(
        draft.lines.reduce((sum, l) => sum + l.unit_price.amount_minor * l.quantity, 0),
    );

    function addDraftOrderLine() {
        const row = addLinePicked;
        if (!row) return;
        const existing = draft.lines.find((l) => l.variant_id === row.variant_id);
        if (existing) {
            existing.quantity += 1;
        } else {
            draft.lines = [
                ...draft.lines,
                {
                    variant_id: row.variant_id,
                    sku: row.sku,
                    title: row.title,
                    variant_label: row.variant_label,
                    quantity: 1,
                    unit_price: row.unit_price,
                },
            ];
        }
        addLineVariant = "";
        addLinePicked = null;
    }

    async function createOrder(event) {
        event?.preventDefault();
        if (creating) return;

        createErrors = {};
        if (!draft.email.trim() || !draft.email.includes("@")) {
            createErrors.email =
                "An email is required — it is how the customer reads the order back.";
        }
        if (!draft.lines.length) createErrors.lines = "Add at least one product.";
        if (Object.keys(createErrors).length) return;

        creating = true;
        try {
            const result = await api.post("/api/admin/orders", {
                email: draft.email.trim(),
                name: draft.name.trim(),
                phone: draft.phone.trim(),
                payment_method: draft.payment_method || undefined,
                discount_code: draft.discount_code.trim() || undefined,
                address: draft.address,
                lines: draft.lines.map((l) => ({ variant_id: l.variant_id, quantity: l.quantity })),
            });
            // The drawer stays open on the success state rather than dropping
            // the operator into the order: the access token is in this response
            // and in no other, so closing over it loses it for good. Open order
            // is one click away, and now it is a link to a real address.
            placed = { order: result.order, payment: result.payment };
            toast.success(`Order ${result.order.number} placed`);
            list.setPage(1);
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            creating = false;
        }
    }

    /**
     * A row click opens the order. The number is also a real link, which is the
     * point of the route existing: middle-click opens a second order beside the
     * first, and the address in the bar is the order rather than the list.
     *
     * The handler stands aside when the click was on the anchor, or the browser
     * would navigate and then this would navigate again over the top of it.
     */
    function openRow(event, row) {
        if (event.target.closest("a")) return;
        // A click on the row's checkbox is the checkbox's; its cell stops the
        // event, but a keyboard activation has no cell to stop it.
        if (event.target.closest("input")) return;
        goto(`${base}/orders/${row.id}`);
    }

    /* `n` places one, from anywhere on this screen. */
    $effect(() => {
        if (!writable) return;
        return onNewShortcut(openCreate);
    });
</script>

<svelte:head><title>Orders · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="orders.read" what="orders" />
{:else}
<div class="page page-orders shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Orders</div>
            </nav>

            <div class="inline-flex gap-sm">
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    title="Refresh"
                    aria-label="Refresh"
                    onclick={load}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>

            <form class="fields searchbar" onsubmit={submitSearch}>
                <div class="field">
                    <input
                        type="text"
                        class="p-l-20"
                        placeholder="Search by number, email or name"
                        bind:value={draftSearch}
                    />
                </div>
                {#if draftSearch || search || email}
                    <div class="field addon p-r-5">
                        {#if draftSearch !== (search || email)}
                            <button type="submit" class="btn sm pill warning">Search</button>
                        {/if}
                        <button
                            type="button"
                            class="btn sm pill secondary transparent"
                            onclick={clearSearch}
                        >
                            Clear
                        </button>
                    </div>
                {/if}
            </form>

            <div class="page-header-primary-btns">
                <div class="field">
                    <Select
                        id="status-filter"
                        placeholder="Any status"
                        value={status}
                        onchange={(v) => list.set({ status: v })}
                        options={statusOptions}
                    />
                </div>

                <div class="field">
                    <Select
                        id="payment-filter"
                        placeholder="Any payment"
                        value={paymentStatus}
                        onchange={(v) => list.set({ payment_status: v })}
                        options={[
                            { value: "", label: "Any payment" },
                            { value: "pending", label: "Awaiting payment" },
                            { value: "paid", label: "Paid" },
                            { value: "failed", label: "Failed" },
                            { value: "refunded", label: "Refunded" },
                        ]}
                    />
                </div>

                <!-- The first question anybody asks of an order list, and the
                     one parameter the engine has always taken and this screen
                     has never sent. -->
                <div class="field">
                    <Select
                        id="date-filter"
                        ariaLabel="Date range"
                        value={range}
                        options={RANGE_OPTIONS}
                        onchange={chooseRange}
                    />
                </div>

                {#if range === "custom"}
                    <div class="field">
                        <input
                            type="date"
                            aria-label="From"
                            value={list.params.from}
                            onchange={(e) => list.set({ from: e.currentTarget.value })}
                        />
                    </div>
                    <div class="field">
                        <input
                            type="date"
                            aria-label="To (exclusive)"
                            value={list.params.to}
                            onchange={(e) => list.set({ to: e.currentTarget.value })}
                        />
                    </div>
                {/if}

                <!--
                     Export and the picking list moved behind one button.

                     This header carries more than any other — three filters, two
                     documents and New order — and at 1440px the six of them left
                     the search 76 pixels, narrow enough to truncate its own
                     placeholder. These two are the ones that can move: neither is
                     a filter, and a warehouse reaches for them once a shift, not
                     once a minute. Folding them frees ~205px, which is what buys
                     the search a readable width on the same row.
                -->
                <button
                    type="button"
                    class="btn sm secondary"
                    title="Export and print"
                    aria-label="Export and print"
                    popovertarget="orders-more"
                    aria-haspopup="menu"
                >
                    <i class="ri-more-2-line" aria-hidden="true"></i>
                </button>
                <div id="orders-more" class="dropdown dropdown-sm" popover="auto" role="menu">
                    {#if can("data.export")}
                        <button
                            type="button"
                            role="menuitem"
                            class="dropdown-item"
                            onclick={() => {
                                closeMore();
                                exportCSV();
                            }}
                            title={"One row per order line, for the status and the dates on screen." +
                                (uncarried.length
                                    ? ` The export route has no parameter for ${uncarried.join(" or ")}, so the file is wider than the table.`
                                    : "")}
                        >
                            <i class="ri-download-2-line" aria-hidden="true"></i>
                            <span class="txt">Export CSV</span>
                        </button>
                    {/if}

                    <!-- One sheet for the whole window rather than one tab per
                         order. A link, not a button: a warehouse opens it in a
                         second tab and leaves the list where it is. -->
                    <a
                        role="menuitem"
                        class="dropdown-item"
                        href={pickingHref}
                        onclick={closeMore}
                        title="A pick sheet for every order matching these filters, and a packing sheet for each"
                    >
                        <i class="ri-printer-line" aria-hidden="true"></i>
                        <span class="txt">Picking list</span>
                    </a>
                </div>

                {#if writable}
                    <button type="button" class="btn" onclick={openCreate}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New order</span>
                    </button>
                {/if}
            </div>
        </header>

        <!-- Outside the header, not in it: `.page-header` is a wrapping flex
             row and a sentence dropped into it becomes a flex item competing
             with the filters for width. -->
        {#if windowWords}
            <div class="field-help m-b-sm">{windowWords}</div>
        {/if}

        <!-- DESIGN.md §5: separation is a border and a background step, not a
             shadow. Only the edge changes here — gocommerce.css already gives
             this wrapper a border and a radius, and these two swap in the
             design's token for each.

             No `overflow-hidden`: table.css makes this element the table's
             horizontal scroller, and the table under it is pinned to a 900px
             minimum. `hidden` clips instead of scrolls, which below about
             1200px left the last column unreachable by mouse or wheel. `auto`
             clips to the radius just as well, so the corners cost nothing. -->
        <div class="page-table-wrapper tw:rounded-xl tw:border">
            <table class="table responsive-table" class:optimize={orders.length > 60}>
                <thead class="sticky">
                    <tr>
                        {#if bulkable}
                            <th class="col-bulk-select min-width">
                                <div class="field">
                                    <input
                                        id="select-all-orders"
                                        type="checkbox"
                                        checked={sel.allSelected(orders)}
                                        onchange={() => sel.toggleAll(orders)}
                                    />
                                    <!-- "on this page", not "all": the panel
                                         does not hold the other pages and the
                                         API has no select-everything call. -->
                                    <label
                                        for="select-all-orders"
                                        aria-label="Select every order on this page"
                                    ></label>
                                </div>
                            </th>
                        {/if}
                        <SortHeader
                            field="number"
                            label="Order"
                            class="col-field-name-id"
                            {sort}
                            onsort={sortBy}
                        />
                        <!-- The cell renders row.name, so the key is `name`
                             rather than the column's own label. -->
                        <SortHeader
                            field="name"
                            label="Customer"
                            class="col-field-type-text"
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="status"
                            label="Status"
                            class="col-field-type-select"
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="payment_status"
                            label="Payment"
                            class="col-field-type-select"
                            {sort}
                            onsort={sortBy}
                        />
                        <!-- Items counts the line_items array, not a column. -->
                        <th class="col-field-type-number min-width">Items</th>
                        <SortHeader
                            field="total"
                            label="Total"
                            class="col-field-type-number min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="created_at"
                            label="Placed"
                            class="col-field-type-date"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each orders as row (row.id)}
                        <!-- Not the bare payment status: a partly refunded order
                             is still `paid`, which is true and is not the whole
                             of what happened to the money. -->
                        {@const pay = paymentLabel(row)}
                        <tr
                            class="handle"
                            tabindex="0"
                            onclick={(e) => openRow(e, row)}
                            onkeydown={(e) => rowKey(e, () => openRow(e, row))}
                        >
                            {#if bulkable}
                                <td
                                    class="col-bulk-select min-width"
                                    onclick={(e) => e.stopPropagation()}
                                >
                                    <div class="field">
                                        <input
                                            id="select-order-{row.id}"
                                            type="checkbox"
                                            checked={sel.has(row.id)}
                                            onchange={() => sel.toggle(row.id)}
                                        />
                                        <label
                                            for="select-order-{row.id}"
                                            aria-label="Select order {row.number}"
                                        ></label>
                                    </div>
                                </td>
                            {/if}
                            <td class="col-field-name-id" data-name="Order">
                                <!-- A real link, so an operator can open three
                                     orders in three tabs and work through them —
                                     which is what the drawer this replaced could
                                     never do. -->
                                <a class="txt-bold txt-code" href="{base}/orders/{row.id}">
                                    {row.number}
                                </a>
                            </td>
                            <td class="col-field-type-text" data-name="Customer">
                                <span class="txt-ellipsis">{row.name || "—"}</span>
                            </td>
                            <td class="col-field-type-select" data-name="Status">
                                <span class="label {orderStatusClass(row.status)}">
                                    {orderStatusLabel(row.status)}
                                </span>
                            </td>
                            <td class="col-field-type-select" data-name="Payment">
                                <span class="label {pay.cls}">{pay.text}</span>
                            </td>
                            <td class="col-field-type-number min-width" data-name="Items">
                                {row.line_items?.length ?? 0}
                            </td>
                            <td class="col-field-type-number min-width txt-bold" data-name="Total">
                                {formatMoney(row.total)}
                            </td>
                            <td
                                class="col-field-type-date txt-hint"
                                data-name="Placed"
                                title={formatDate(row.created_at)}
                            >
                                {relativeTime(row.created_at)}
                            </td>
                            <td class="col-meta min-width">
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !orders.length}
                        {#each Array(6) as _, i (i)}
                            <tr>
                                <td colspan={bulkable ? 9 : 8}>
                                    <span class="skeleton-loader"></span>
                                </td>
                            </tr>
                        {/each}
                    {/if}

                    {#if !loading && !orders.length}
                        <tr>
                            <td colspan={bulkable ? 9 : 8} class="txt-center txt-hint p-base">
                                <div class="m-b-10">
                                    <i
                                        class="ri-shopping-bag-3-line"
                                        style="font-size: 32px"
                                        aria-hidden="true"
                                    ></i>
                                </div>
                                {#if status || paymentStatus || search || email || list.params.from || list.params.to}
                                    Nothing matches that. Try clearing a filter.
                                {:else}
                                    No orders yet. Orders appear here the moment somebody checks
                                    out.
                                {/if}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>

        {#if bulkable}
            <BulkBar count={sel.count} noun="order" onclear={() => sel.clear()}>
                <!-- Each button says how many of the selection it can actually
                     act on, because a mixed selection is the normal case: three
                     of the eight are already paid, and a button that claimed all
                     eight would be promising four refusals. -->
                <!-- The one entry here that asks nothing of the engine: it is a
                     link to a document over the rows that are ticked. Before
                     this, printing eight orders meant opening eight tabs. -->
                <a
                    class="btn sm secondary"
                    href={pickingSelectionHref}
                    title="A pick sheet for these orders, and a packing sheet for each"
                >
                    <i class="ri-printer-line" aria-hidden="true"></i>
                    <span class="txt">Picking list ({sel.count})</span>
                </a>
                {#if mayFulfill}
                    <!-- The other half of the picking list, and the reason this
                         bar exists on a warehouse morning: the sheet said which
                         eight to pack, and until now booking them meant eight
                         drawer opens. Its own right, because the engine cuts
                         booking a parcel away from editing an order. -->
                    <button
                        type="button"
                        class="btn sm secondary"
                        disabled={bulkBusy || !shippable.length}
                        title={"Books the whole of what is left on each, through " +
                            defaultProvider +
                            ", with no tracking number — one number cannot belong to " +
                            "eight parcels, so the order screen stays where a number goes." +
                            (shippable.length === sel.count
                                ? ""
                                : ` ${shippable.length} of ${sel.count} are confirmed or partly shipped.`)}
                        onclick={bulkShip}
                    >
                        <i class="ri-truck-line" aria-hidden="true"></i>
                        <span class="txt">Ship ({shippable.length})</span>
                    </button>
                {/if}
                {#if writable}
                    <button
                        type="button"
                        class="btn sm secondary"
                        disabled={bulkBusy || !payable.length}
                        title={payable.length === sel.count
                            ? "Record payment against these orders"
                            : `${payable.length} of ${sel.count} can be marked paid`}
                        onclick={bulkMarkPaid}
                    >
                        <i class="ri-money-dollar-circle-line" aria-hidden="true"></i>
                        <span class="txt">Mark paid ({payable.length})</span>
                    </button>
                    <button
                        type="button"
                        class="btn sm secondary"
                        disabled={bulkBusy || !deliverable.length}
                        title="Only a shipped order can be marked delivered"
                        onclick={bulkDeliver}
                    >
                        <i class="ri-checkbox-circle-line" aria-hidden="true"></i>
                        <span class="txt">Delivered ({deliverable.length})</span>
                    </button>
                    <button
                        type="button"
                        class="btn sm secondary txt-danger"
                        disabled={bulkBusy || !cancellable.length}
                        title="A shipped, partly shipped or delivered order is a return, not a cancellation"
                        onclick={() => (cancelOpen = true)}
                    >
                        <i class="ri-close-circle-line" aria-hidden="true"></i>
                        <span class="txt">Cancel ({cancellable.length})</span>
                    </button>
                {/if}
            </BulkBar>
        {/if}

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <Pager
                {meta}
                {loading}
                noun="order"
                {perPage}
                onpage={(n) => list.setPage(n)}
                onperpage={(n) => list.set({ limit: n })}
            />
            <div class="flex-fill"></div>
        </footer>
    </div>
</div>

<Confirm
    bind:open={cancelOpen}
    title="Cancel {cancellable.length} {cancellable.length === 1 ? 'order' : 'orders'}?"
    message="The stock each one is holding goes back on sale. No reason is recorded — one sentence cannot honestly explain several cancellations, so use the order's own screen where the reason matters."
    confirmLabel="Cancel them"
    danger
    onconfirm={bulkCancel}
/>

<Drawer
    open={createOpen}
    title={placed ? `Order ${placed.order.number} placed` : "New order"}
    size="sm"
    onclose={() => {
        createOpen = false;
        placed = null;
    }}
>
    {#if placed}
        <OrderPlaced
            order={placed.order}
            payment={placed.payment}
            methodName={methods.find((m) => m.code === placed.order.payment_provider)?.name ??
                placed.order.payment_provider}
        />
    {:else}
        <!--
            The same fields a shopper fills in, because this order takes the same
            path a shopper's does — it reserves stock, snapshots prices and gets
            an access token the customer can use to read it back.
        -->
        <form id="new-order-form" onsubmit={createOrder}>
            <div class="field required" class:error={!!createErrors.email}>
                <label for="no-email">Email</label>
                <input id="no-email" type="email" autocomplete="off" bind:value={draft.email} />
            </div>
            {#if createErrors.email}<div class="field-help error">{createErrors.email}</div>{/if}

            <div class="fields m-t-sm">
                <div class="field">
                    <label for="no-name">Name</label>
                    <input id="no-name" type="text" autocomplete="off" bind:value={draft.name} />
                </div>
                <div class="delimiter"></div>
                <div class="field">
                    <label for="no-phone">Phone</label>
                    <input id="no-phone" type="text" autocomplete="off" bind:value={draft.phone} />
                </div>
            </div>

            <h6 class="section-title">
                <i class="ri-map-pin-line" aria-hidden="true"></i>
                Delivery address
            </h6>
            <div class="field">
                <label for="no-line1">Address</label>
                <input
                    id="no-line1"
                    type="text"
                    autocomplete="off"
                    bind:value={draft.address.line1}
                />
            </div>
            <div class="field m-t-5">
                <label for="no-line2">Apartment, suite, etc.</label>
                <input
                    id="no-line2"
                    type="text"
                    autocomplete="off"
                    bind:value={draft.address.line2}
                />
            </div>
            <div class="fields m-t-5">
                <div class="field">
                    <label for="no-city">City</label>
                    <input
                        id="no-city"
                        type="text"
                        autocomplete="off"
                        bind:value={draft.address.city}
                    />
                </div>
                <div class="delimiter"></div>
                <div class="field">
                    <label for="no-postal">Postal code</label>
                    <input
                        id="no-postal"
                        type="text"
                        autocomplete="off"
                        bind:value={draft.address.postal_code}
                    />
                </div>
            </div>
            <div class="fields m-t-5">
                <div class="field">
                    <label for="no-state">State or region</label>
                    <input
                        id="no-state"
                        type="text"
                        autocomplete="off"
                        bind:value={draft.address.state}
                    />
                </div>
                <div class="delimiter"></div>
                <div class="field">
                    <label for="no-country">Country</label>
                    <Select
                        id="no-country"
                        placeholder="Choose a country"
                        bind:value={draft.address.country}
                        options={COUNTRIES}
                    />
                </div>
            </div>

            <h6 class="section-title">
                <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
                Items
            </h6>
            {#if draft.lines.length}
                <table class="table m-b-sm">
                    <tbody>
                        {#each draft.lines as line, i (line.variant_id)}
                            <tr>
                                <td>
                                    <div class="row-name">
                                        <span class="txt-bold txt-ellipsis">{line.title}</span>
                                        <span class="txt-hint txt-sm txt-code row-handle">
                                            {line.sku}
                                        </span>
                                    </div>
                                </td>
                                <td class="txt-right min-width">
                                    <input
                                        type="number"
                                        class="order-qty"
                                        min="1"
                                        aria-label="Quantity of {line.sku}"
                                        bind:value={line.quantity}
                                    />
                                </td>
                                <td class="txt-right min-width">
                                    {formatMoney({
                                        amount_minor: line.unit_price.amount_minor * line.quantity,
                                        currency: line.unit_price.currency,
                                    })}
                                </td>
                                <td class="txt-right min-width">
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary"
                                        aria-label="Remove {line.sku}"
                                        onclick={() =>
                                            (draft.lines = draft.lines.filter((_, j) => j !== i))}
                                    >
                                        <i class="ri-close-line" aria-hidden="true"></i>
                                    </button>
                                </td>
                            </tr>
                        {/each}
                    </tbody>
                </table>
            {/if}
            {#if createErrors.lines}<div class="field-help error">{createErrors.lines}</div>{/if}

            <div class="fields">
                <div class="field">
                    <label for="no-add">Add a product</label>
                    <!-- The store is asked as the operator types, so a catalogue
                         past the first page is still sellable by hand — and each
                         row carries the price and the count, so a variant the
                         checkout would refuse is visible before Place order
                         rather than after it. -->
                    <VariantPicker
                        id="no-add"
                        bind:value={addLineVariant}
                        onchange={(row) => (addLinePicked = row)}
                    />
                </div>
                <div class="delimiter"></div>
                <div class="field addon">
                    <button
                        type="button"
                        class="btn sm secondary"
                        disabled={!addLinePicked}
                        onclick={addDraftOrderLine}
                    >
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">Add</span>
                    </button>
                </div>
            </div>

            <div class="field m-t-sm">
                <label for="no-discount">Discount code</label>
                <input
                    id="no-discount"
                    type="text"
                    autocomplete="off"
                    placeholder="Optional"
                    bind:value={draft.discount_code}
                />
            </div>
            <div class="field-help">
                Applied the way a shopper's is — checked and claimed under the checkout's own
                lock, so an expired or fully used code refuses the whole order rather than
                quietly placing it at full price.
            </div>

            <div class="field m-t-sm">
                <label for="no-payment">Payment method</label>
                <Select
                    id="no-payment"
                    placeholder="Choose a method"
                    bind:value={draft.payment_method}
                    options={paymentOptions}
                />
            </div>
            <div class="field-help">
                The order is placed the way a shopper places one: it reserves stock now, and the
                customer can read it back with the access token this creates. Cash on delivery
                leaves it awaiting payment, which is what "Mark paid" is for. Shipping, tax and
                any discount are added by the checkout and appear on the order once it is placed.
            </div>

            {#if draft.lines.length}
                <!-- Items subtotal, not Total: this sums the lines, while the
                     checkout adds flat shipping and then tax on the discounted
                     amount. -->
                <div class="flex m-t-sm">
                    <strong>Items subtotal</strong>
                    <div class="flex-fill"></div>
                    <strong class="txt-money">
                        {formatMoney({
                            amount_minor: draftOrderTotal,
                            currency: draft.lines[0].unit_price.currency,
                        })}
                    </strong>
                </div>
            {/if}
        </form>
    {/if}

    {#snippet footer()}
        {#if placed}
            <button
                type="button"
                class="btn transparent m-r-auto"
                onclick={() => {
                    createOpen = false;
                    placed = null;
                }}
            >
                <span class="txt">Done</span>
            </button>
            <a class="btn" href="{base}/orders/{placed.order.id}">
                <i class="ri-external-link-line" aria-hidden="true"></i>
                <span class="txt">Open order</span>
            </a>
        {:else}
            <button
                type="button"
                class="btn transparent m-r-auto"
                onclick={() => (createOpen = false)}
            >
                <span class="txt">Cancel</span>
            </button>
            <button
                type="submit"
                form="new-order-form"
                class="btn"
                class:loading={creating}
                disabled={creating}
            >
                <span class="txt">Place order</span>
            </button>
        {/if}
    {/snippet}
</Drawer>
{/if}
