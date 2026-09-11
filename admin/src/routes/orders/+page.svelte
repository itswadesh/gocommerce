<script>
    import { api, query, request } from "$lib/api.js";
    import {
        formatMoney,
        formatDate,
        relativeTime,
        orderStatusClass,
        orderStatusLabel,
        paymentLabel,
        pluralize,
    } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import Select from "$lib/components/Select.svelte";
    import ShipDialog from "$lib/components/ShipDialog.svelte";
    import RefundDialog from "$lib/components/RefundDialog.svelte";
    import OrderRefundList from "$lib/components/OrderRefundList.svelte";
    import OrderReturnsCard from "$lib/components/OrderReturnsCard.svelte";
    import ReturnDialog from "$lib/components/ReturnDialog.svelte";
    import OrderPlaced from "$lib/components/OrderPlaced.svelte";
    import { COUNTRIES } from "$lib/countries.js";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";
    import { page as page_ } from "$app/state";

    const PER_PAGE = 25;

    let loading = $state(true);
    let orders = $state([]);
    let meta = $state(null);
    let status = $state("");
    let paymentStatus = $state("");
    /* The list can be linked to: Customers sends the operator here with the
       person already filled in, so the filter has to start from the address
       bar rather than only from the box. */
    const linked = page_.url.searchParams.get("email") ?? "";
    let email = $state(linked);
    let draftEmail = $state(linked);
    let page = $state(1);

    let detailOpen = $state(false);
    let order = $state(null);
    let busy = $state("");

    let confirmOpen = $state(false);
    let confirmConfig = $state({});

    let trackingOpen = $state(false);
    let tracking = $state("");
    let shipCarrier = $state("");

    let refundOpen = $state(false);
    let returnOpen = $state(false);

    $effect(() => {
        // Re-runs whenever a filter changes. Page 1 replaces the list; a later
        // page appends, because the table loads more rather than paginating.
        status;
        paymentStatus;
        email;
        page;
        load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/orders" +
                    query({ status, payment_status: paymentStatus, email, page, limit: PER_PAGE }),
            );
            orders = page === 1 ? result.data : [...orders, ...result.data];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    const hasMore = $derived(!!meta && orders.length < meta.total);

    function submitSearch(e) {
        e.preventDefault();
        page = 1;
        email = draftEmail;
    }

    function clearSearch() {
        draftEmail = "";
        page = 1;
        email = "";
    }

    async function openOrder(row) {
        // Show the row we already have while the full order — line items,
        // address, fulfillments — is on its way.
        detailOpen = true;
        order = row;
        try {
            order = await api.get(`/api/admin/orders/${row.id}`);
        } catch (err) {
            toast.error(err);
        }
    }

    async function act(label, fn) {
        busy = label;
        try {
            order = await fn();
            // Every action changes a status the list shows, so the list starts
            // over rather than keeping pages loaded against the old state.
            page = 1;
            await load();
            toast.success(label);
        } catch (err) {
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    const markPaid = () =>
        act("Marked paid", () =>
            api.post(`/api/admin/orders/${order.id}/mark-paid`, { reference: "" }),
        );

    const deliver = () =>
        act("Marked delivered", () => api.post(`/api/admin/orders/${order.id}/deliver`));

    /**
     * The two corrections, for a status set on the wrong row.
     *
     * They read as undo rather than as workflow, which is why they live under
     * More rather than beside Mark paid: an operator looking for them has
     * already made the mistake, and one looking for the next step should not
     * meet a button that walks the order backwards.
     */
    const markUnpaid = () =>
        act("Marked unpaid", () => api.post(`/api/admin/orders/${order.id}/mark-unpaid`));

    /**
     * Clearing a failure is the same route as taking back a payment, because it
     * is the same question: put this order's money back where it was. It is
     * under Undo for the reason the two above it are — the operator looking for
     * it has already made the mistake.
     */
    const clearFailure = () =>
        act("Failure cleared", () => api.post(`/api/admin/orders/${order.id}/mark-unpaid`));

    /**
     * Recording what the gateway said, which is not an undo: it is the other
     * half of the one question the operator is answering at that moment — did
     * the money arrive? So it sits beside Mark paid rather than under Undo.
     *
     * The reason is fixed rather than prompted for. The engine writes it to the
     * store's log and stores it nowhere, so asking an operator to type one
     * would promise a record that does not exist.
     */
    const markPaymentFailed = () =>
        // "Failure recorded", not "Payment failed": act() uses this string for the
        // success toast as well as the spinner, and a green tick beside the
        // words "Payment failed" reads as the wrong news about the wrong thing.
        act("Failure recorded", () =>
            api.post(`/api/admin/orders/${order.id}/mark-payment-failed`, {
                reason: "recorded from the admin panel",
            }),
        );

    const undeliver = () =>
        act("Delivery undone", () => api.post(`/api/admin/orders/${order.id}/undeliver`));

    // ------------------------------------------------------- tracking number

    /** The shipment being corrected, or null. */
    let editing = $state(null);
    let editTracking = $state("");
    let editCarrier = $state("");
    /** What the number looks like it belongs to, best first. */
    let carrierOptions = $state([]);
    /** Every carrier the engine can name, for when the number gives nothing away. */
    let allCarriers = $state([]);
    let carrierPicked = $state(false);

    /**
     * The picker's options: what the number suggests, then everyone else.
     *
     * Both, because the two answer different questions. The suggestions are
     * what the number could be; the full list is who the shop actually hands
     * parcels to, and a number that matches nothing — a courier's own internal
     * format, a hand-written docket — must still be recordable. The Select
     * shows a search box past eight options, so a list this long stays typeable
     * rather than scrollable.
     */
    const carrierChoices = $derived.by(() => {
        const suggested = carrierOptions.map((c) => ({ value: c.code, label: c.name }));
        const seen = new Set(carrierOptions.map((c) => c.code));
        const rest = allCarriers
            .filter((c) => !seen.has(c.code))
            .map((c) => ({ value: c.code, label: c.name }));
        return [{ value: "", label: "Not recorded" }, ...suggested, ...rest];
    });

    async function loadCarriers() {
        if (allCarriers.length) return;
        try {
            const res = await api.get("/api/admin/carriers");
            allCarriers = res.data ?? [];
        } catch {
            // The suggestions still work, and so does clearing the field.
            allCarriers = [];
        }
    }

    /** Opening the Ship dialog: same field, same lookup, nothing chosen yet. */
    function startShipping() {
        loadCarriers();
        tracking = "";
        shipCarrier = "";
        carrierOptions = [];
        carrierPicked = false;
        trackingOpen = true;
    }

    function startEditTracking(f) {
        loadCarriers();
        editing = f;
        editTracking = f.tracking ?? "";
        editCarrier = f.carrier ?? "";
        carrierPicked = false;
        // Options only. What is stored answers the number that is still in the
        // box, and re-deriving it here would overwrite a carrier somebody set
        // by hand the last time they were in this dialog.
        lookupCarriers(editTracking, { suggest: false, target: "edit" });
    }

    /**
     * Ask the engine which carriers a number could belong to.
     *
     * The engine owns this because it is the same question the shipping path
     * answers when a number is first typed, and two implementations of it would
     * disagree the day one is updated. See carriers.go.
     */
    async function lookupCarriers(value, { suggest = true, target = "edit" } = {}) {
        const number = (value ?? "").trim();
        const fill = (code) => {
            if (!suggest || carrierPicked) return;
            if (target === "ship") shipCarrier = code;
            else editCarrier = code;
        };
        if (!number) {
            carrierOptions = [];
            fill("");
            return;
        }
        try {
            const res = await api.get("/api/admin/carriers" + query({ tracking: number }));
            carrierOptions = res.data ?? [];
            // Fill it in, unless the operator has already said otherwise: they
            // can see the parcel and this is pattern matching. An empty answer
            // clears the field rather than leaving the previous number's
            // carrier behind it.
            fill(carrierOptions[0]?.code ?? "");
        } catch {
            // The field still works without a suggestion, so a failed lookup is
            // not worth a toast over.
            carrierOptions = [];
        }
    }

    // ------------------------------------------------- customer and payment

    /**
     * Which card is being edited, or null. One at a time: these are short
     * forms and two open at once would put two Save buttons on screen with
     * nothing saying which is which.
     */
    let editingCard = $state(null);
    let form = $state({});

    function startEditCustomer() {
        editingCard = "customer";
        form = {
            name: order.name ?? "",
            email: order.email ?? "",
            phone: order.phone ?? "",
            address: { ...(order.address ?? {}) },
        };
    }

    function startEditPayment() {
        editingCard = "payment";
        form = {
            payment_provider: order.payment_provider ?? "",
            payment_reference: order.payment_reference ?? "",
        };
    }

    /**
     * The methods this build has installed, for the picker.
     *
     * The store's own settings, not the public checkout route: the engine
     * composes one provider list and both answer from it, so the panel still
     * cannot offer a method the engine would refuse — and each entry now
     * carries the name the installing module gave it, instead of the panel
     * keeping its own table of labels for codes it did not choose.
     */
    const methods = $derived(settings.paymentMethods);

    const saveCard = () =>
        act("Order updated", async () => {
            const patch =
                editingCard === "customer"
                    ? {
                          name: form.name.trim(),
                          email: form.email.trim(),
                          phone: form.phone.trim(),
                          address: form.address,
                      }
                    : {
                          payment_provider: form.payment_provider,
                          payment_reference: form.payment_reference.trim(),
                      };
            const updated = await api.patch(`/api/admin/orders/${order.id}`, patch);
            editingCard = null;
            return updated;
        });

    /**
     * Removing a shipment recorded in error.
     *
     * Confirmed rather than immediate: it is the one thing in this drawer that
     * throws a record away, and what it held goes back on the order to be
     * shipped again — which walks the order back to confirmed if nothing else
     * has gone out, and to partly shipped if something has. A bigger
     * consequence than the button implies on its own.
     */
    function askDeleteShipment(f) {
        confirmConfig = {
            title: "Remove this shipment?",
            message:
                (f.tracking ? `Tracking ${f.tracking} ` : "This shipment ") +
                "will be removed from the order, and what it carried goes back to being owed. " +
                "The order returns to confirmed only if nothing else has gone out.",
            confirmLabel: "Remove",
            danger: true,
            run: () =>
                act("Shipment removed", () =>
                    api.delete(`/api/admin/fulfillments/${f.id}`),
                ),
        };
        confirmOpen = true;
    }

    const saveTracking = () =>
        act("Tracking updated", async () => {
            await api.patch(`/api/admin/fulfillments/${editing.id}`, {
                tracking: editTracking.trim(),
                carrier: editCarrier,
            });
            editing = null;
            return api.get(`/api/admin/orders/${order.id}`);
        });



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
    /**
     * The whole create response, once there is one: {order, payment}.
     *
     * The drawer holds open on it rather than closing, because this response
     * carries the order's access token — the guest's only credential for their
     * own order, returned here and by no other route. A toast would be
     * dismissible, unselectable and gone.
     *
     * The whole order object, not a stub: the Open order button hands it to
     * openOrder, which paints the drawer from it while the detail fetch is in
     * flight, and a four-field stub would render undefined status chips and
     * empty cards on the most common path.
     */
    let placed = $state(null);

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
    async function openCreate() {
        draft = blankOrder();
        createErrors = {};
        addLineVariant = "";
        placed = null;
        createOpen = true;
        if (!catalog.length) {
            try {
                catalog = (await api.get("/api/admin/products?limit=200")).data ?? [];
            } catch (err) {
                toast.error(err);
            }
        }
        if (!draft.payment_method && methods.length) {
            draft.payment_method = methods[0].code;
        }
    }

    /* What the store can actually take, named as the shopper would see it. */
    const paymentOptions = $derived(
        methods.map((m) => ({ value: m.code, label: m.name || m.code })),
    );

    const draftOrderTotal = $derived(
        draft.lines.reduce((sum, l) => sum + l.unit_price.amount_minor * l.quantity, 0),
    );

    function addDraftOrderLine() {
        const id = Number(addLineVariant);
        if (!id) return;
        const existing = draft.lines.find((l) => l.variant_id === id);
        if (existing) {
            existing.quantity += 1;
            addLineVariant = "";
            return;
        }
        const product = catalog.find((p) => (p.variants ?? []).some((v) => v.id === id));
        const variant = product?.variants.find((v) => v.id === id);
        if (!variant) return;
        draft.lines = [
            ...draft.lines,
            {
                variant_id: id,
                sku: variant.sku,
                title: product.title,
                variant_label: variant.label ?? "",
                quantity: 1,
                unit_price: variant.price,
            },
        ];
        addLineVariant = "";
    }

    async function createOrder(event) {
        event?.preventDefault();
        if (creating) return;

        createErrors = {};
        if (!draft.email.trim() || !draft.email.includes("@")) {
            createErrors.email = "An email is required — it is how the customer reads the order back.";
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
            // is one click away and hands over the same object this used to.
            placed = { order: result.order, payment: result.payment };
            toast.success(`Order ${result.order.number} placed`);
            page = 1;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            creating = false;
        }
    }
    // ------------------------------------------------------------ editing

    /**
     * The lines as the drawer is editing them, or null when it is not.
     *
     * A copy rather than the order itself: an edit is not applied until it is
     * saved, and half of one on screen must not be mistaken for the order.
     */
    let draftLines = $state(null);
    let addVariant = $state("");
    /** One page of the catalog, for the picker. The same trade the product
     *  editor makes for its vendor and tag suggestions. */
    let catalog = $state([]);

    /* What has gone back, and what the store still holds. Both come off the
       order itself: `refunded` is on every list row too, so nothing here costs
       an extra request. */
    const refundedMinor = $derived(order?.refunded?.amount_minor ?? 0);
    const remainingMinor = $derived((order?.total?.amount_minor ?? 0) - refundedMinor);
    /* A partly refunded order stays `paid`, so it keeps the button and the
       operator can finish the refund; it vanishes on the last one. */
    const refundable = $derived(order?.payment_status === "paid" && remainingMinor > 0);
    /* The same chip the list row wears, from the same function, so the two can
       never disagree about one order. */
    const payChip = $derived(paymentLabel(order));

    /**
     * Whether this order can still be changed. It mirrors the engine's own
     * guard — a shipped order is a return, a cancelled or refunded one is
     * closed — so the button is absent rather than there and refused.
     */
    /**
     * The payment method as a person would say it. `cod` is a code, and a code
     * in a sentence reads as a leak from the database.
     */
    const paidClass = $derived(
        order?.payment_status === "refunded"
            ? "is-refunded"
            : refundedMinor > 0
              ? "is-part-refunded"
              : order?.payment_status === "paid"
                ? "is-paid"
                : "",
    );

    /* The label the module that installed the provider gave it. An order can
       name a provider this build no longer has, so the code itself is the
       fallback rather than a blank. */
    const methodName = $derived(
        methods.find((m) => m.code === order?.payment_provider)?.name ??
            order?.payment_provider ??
            "—",
    );

    /* Goods can only come back from an order that has sent some out. It mirrors
       returnableOrder, so the button is absent rather than there and refused —
       the rule `editable` already states. */
    const returnable = $derived(
        !!order &&
            (order.status === "shipped" ||
                order.status === "delivered" ||
                order.status === "partial"),
    );

    /* Only a return that still stands: a withdrawn one has given its units back
       and freezes nothing. */
    const activeReturns = $derived((order?.returns ?? []).filter((r) => r.status === "received"));

    const editable = $derived(
        !!order &&
            (order.status === "pending" || order.status === "confirmed") &&
            /* Any refund at all, mirroring the engine's own guard: changing what
               is in the order would move a total money has already come off. */
            refundedMinor === 0 &&
            /* And the engine's other one: a returned order's lines no longer
               describe what left the store. Only reachable on an order walked
               back from shipped, which is exactly when the button would 409. */
            activeReturns.length === 0,
    );

    const variantOptions = $derived(
        catalog.flatMap((p) =>
            (p.variants ?? []).map((v) => ({
                value: String(v.id),
                label: `${p.title}${v.label ? " · " + v.label : ""} — ${v.sku}`,
            })),
        ),
    );

    const draftTotal = $derived(
        (draftLines ?? []).reduce((sum, l) => sum + l.unit_price.amount_minor * l.quantity, 0) +
            (order?.shipping?.amount_minor ?? 0) -
            (order?.discount?.amount_minor ?? 0),
    );
    const draftBalance = $derived(draftTotal - (order?.total?.amount_minor ?? 0));

    async function startEdit() {
        draftLines = (order.line_items ?? []).map((l) => ({ ...l }));
        addVariant = "";
        if (!catalog.length) {
            try {
                const res = await api.get("/api/admin/products?limit=200");
                catalog = res.data ?? [];
            } catch (err) {
                // The picker is the only thing that needs it, so an edit that
                // only changes quantities still works without it.
                toast.error(err);
            }
        }
    }

    function addDraftLine() {
        const id = Number(addVariant);
        if (!id) return;
        const existing = draftLines.find((l) => l.variant_id === id);
        if (existing) {
            // Already on the order: the operator means one more of it, not a
            // second line the engine would have to merge.
            existing.quantity += 1;
            addVariant = "";
            return;
        }
        const product = catalog.find((p) => (p.variants ?? []).some((v) => v.id === id));
        const variant = product?.variants.find((v) => v.id === id);
        if (!variant) return;
        draftLines = [
            ...draftLines,
            {
                id: 0,
                variant_id: id,
                sku: variant.sku,
                title: product.title,
                variant_label: variant.label ?? "",
                quantity: 1,
                // Today's price, which is what the engine will snapshot.
                unit_price: variant.price,
            },
        ];
        addVariant = "";
    }

    function removeDraftLine(index) {
        draftLines = draftLines.filter((_, i) => i !== index);
    }

    const saveEdit = () =>
        act("Order updated", async () => {
            const result = await request("PUT", `/api/admin/orders/${order.id}/lines`, {
                body: {
                    lines: draftLines.map((l) => ({
                        id: l.id || undefined,
                        variant_id: l.id ? undefined : l.variant_id,
                        quantity: l.quantity,
                    })),
                },
            });
            for (const line of summarizeEdit(result.changed)) toast.success(line);
            draftLines = null;
            return result.order;
        });

    /** What the edit came to, in sentences rather than three lists. */
    function summarizeEdit(change) {
        if (!change) return [];
        const out = [];
        if (change.lines_added?.length) out.push(`Added ${change.lines_added.join(", ")}`);
        if (change.lines_removed?.length) out.push(`Removed ${change.lines_removed.join(", ")}`);
        if (change.lines_changed?.length) out.push(change.lines_changed.join(", "));
        if (change.balance_minor > 0) {
            out.push(`${formatMinor(change.balance_minor)} to collect`);
        } else if (change.balance_minor < 0) {
            out.push(`${formatMinor(-change.balance_minor)} to refund`);
        }
        return out;
    }

    /** A bare amount in the order's own currency, for the balance sentences. */
    function formatMinor(minor) {
        return formatMoney({ amount_minor: minor, currency: order?.currency ?? settings.currency });
    }

    function askCancel() {
        confirmConfig = {
            title: "Cancel this order?",
            message:
                order.status === "pending"
                    ? "Its inventory reservation is released and the stock goes back on sale."
                    : "The stock it took is returned to the shelf.",
            confirmLabel: "Cancel order",
            danger: true,
            run: () =>
                act("Order cancelled", () =>
                    api.post(`/api/admin/orders/${order.id}/cancel`, {
                        reason: "cancelled from the admin panel",
                    }),
                ),
        };
        confirmOpen = true;
    }

    function askPaymentFailed() {
        confirmConfig = {
            title: "Record a failed payment?",
            message:
                (order.status === "pending"
                    ? "The order stays open so the customer can try again, and the stock it is holding stays held. If nobody retries, the reservation expires and the order is cancelled automatically, returning the stock. "
                    : "The order stays as it is — nothing moves, and its stock stays committed to the sale. Use Mark paid when the money does arrive. ") +
                "You can take this back from Undo.",
            confirmLabel: "Record failure",
            danger: false,
            run: markPaymentFailed,
        };
        confirmOpen = true;
    }

    /**
     * Refunding needs a figure, so it is a form rather than a confirmation.
     *
     * The amount is decided in the dialog, against the order's own currency, and
     * the engine checks it again under the row lock — so a screen that is a
     * minute old cannot spend money the order no longer has.
     */
    async function refund({ amount_minor, reason }) {
        await act("Refunded", () =>
            api.post(`/api/admin/orders/${order.id}/refund`, { amount_minor, reason }),
        );
        refundOpen = false;
    }

    /**
     * Recording goods that have come back.
     *
     * `request` rather than `api.post` because the response carries two
     * members — the fresh order and the return just recorded — which is the
     * shape saveEdit already reads. Nothing here touches the money: refunding is
     * its own button, and chaining the two would hide a partial failure.
     */
    async function recordReturn({ reason, lines }) {
        await act("Return recorded", async () => {
            const result = await request("POST", `/api/admin/orders/${order.id}/returns`, {
                body: { reason, lines },
            });
            return result.order;
        });
        returnOpen = false;
    }

    /**
     * Withdrawing a return recorded in error.
     *
     * Confirmed rather than immediate, like removing a shipment: what it undoes
     * is a stock movement, and the message says which one — an operator who
     * meant "the customer changed their mind again" would otherwise take units
     * off a shelf that has them.
     */
    function askWithdrawReturn(ret) {
        confirmConfig = {
            title: "Withdraw this return?",
            message:
                (ret.restocked_units > 0
                    ? `The ${ret.restocked_units} unit(s) this put back on the shelf come off it again. `
                    : "The record is withdrawn. No stock moves — none of it went back on the shelf. ") +
                "Use this for a return recorded in error, not for one that was later refused.",
            confirmLabel: "Withdraw",
            danger: true,
            run: () =>
                act("Return withdrawn", () =>
                    api.delete(`/api/admin/orders/${order.id}/returns/${ret.id}`),
                ),
        };
        confirmOpen = true;
    }

    /**
     * Recording the parcel the ship dialog just described.
     *
     * `lines` is null when the whole of what is left is going out, which is the
     * usual case and the default the dialog opens on. The request is then the
     * body this panel has always sent, and the engine works out what is left
     * under the order's row lock rather than trusting a screen that may be a
     * minute old.
     */
    async function ship({ tracking: number, carrier, lines }) {
        await act("Shipped", () =>
            api.post("/api/admin/create-fulfillment", {
                order_id: order.id,
                provider: "manual",
                tracking: number,
                // An empty carrier still leaves the engine to read it off the
                // number, which is what it did before this field existed.
                carrier,
                ...(lines ? { lines } : {}),
            }),
        );
        trackingOpen = false;
        tracking = "";
        shipCarrier = "";
    }
</script>

<div class="page page-orders">
    <div class="page-content full-height">
        <header class="page-header">
            <nav class="breadcrumbs"><div>Orders</div></nav>

            <div class="inline-flex gap-sm">
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    title="Refresh"
                    aria-label="Refresh"
                    onclick={() => ((page = 1), load())}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>

            <form class="fields searchbar" onsubmit={submitSearch}>
                <div class="field">
                    <input
                        type="text"
                        class="p-l-20"
                        placeholder="Search orders by customer email"
                        bind:value={draftEmail}
                    />
                </div>
                {#if draftEmail || email}
                    <div class="field addon p-r-5">
                        {#if draftEmail !== email}
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
                        bind:value={status}
                        onchange={() => (page = 1)}
                        options={[
                            { value: "", label: "Any status" },
                            { value: "pending", label: "Pending" },
                            { value: "confirmed", label: "Confirmed" },
                            { value: "partial", label: "Partly shipped" },
                            { value: "shipped", label: "Shipped" },
                            { value: "delivered", label: "Delivered" },
                            { value: "cancelled", label: "Cancelled" },
                        ]}
                    />
                </div>

                <div class="field">
                    <Select
                        id="payment-filter"
                        placeholder="Any payment"
                        bind:value={paymentStatus}
                        onchange={() => (page = 1)}
                        options={[
                            { value: "", label: "Any payment" },
                            { value: "pending", label: "Awaiting payment" },
                            { value: "paid", label: "Paid" },
                            { value: "failed", label: "Failed" },
                            { value: "refunded", label: "Refunded" },
                        ]}
                    />
                </div>

                <button type="button" class="btn" onclick={openCreate}>
                    <i class="ri-add-line" aria-hidden="true"></i>
                    <span class="txt">New order</span>
                </button>
            </div>
        </header>
        <div class="page-table-wrapper">
            <table class="table responsive-table" class:optimize={orders.length > 60}>
                <thead class="sticky">
                    <tr>
                        <th class="col-field-name-id">Order</th>
                        <th class="col-field-type-text">Customer</th>
                        <th class="col-field-type-select">Status</th>
                        <th class="col-field-type-select">Payment</th>
                        <th class="col-field-type-number min-width">Items</th>
                        <th class="col-field-type-number min-width">Total</th>
                        <th class="col-field-type-date">Placed</th>
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each orders as row (row.id)}
                        <!-- Not the bare payment status: a partly refunded order
                             is still `paid`, which is true and is not the whole
                             of what happened to the money. -->
                        {@const pay = paymentLabel(row)}
                        <tr class="handle" onclick={() => openOrder(row)}>
                            <td class="col-field-name-id" data-name="Order">
                                <span class="txt-bold txt-code">{row.number}</span>
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
                                <td colspan="8"><span class="skeleton-loader"></span></td>
                            </tr>
                        {/each}
                    {/if}

                    {#if !loading && !orders.length}
                        <tr>
                            <td colspan="8" class="txt-center txt-hint p-base">
                                <div class="m-b-10">
                                    <i
                                        class="ri-shopping-bag-3-line"
                                        style="font-size: 32px"
                                        aria-hidden="true"
                                    ></i>
                                </div>
                                {#if status || paymentStatus || email}
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

            {#if hasMore}
                <button
                    type="button"
                    class="btn expanded block load-more-btn"
                    class:loading
                    disabled={loading}
                    onclick={() => (page += 1)}
                >
                    <i class="ri-arrow-down-s-line" aria-hidden="true"></i>
                    <span class="txt">Load more</span>
                </button>
            {/if}
        </div>

        <footer class="page-footer">
            <span class="txt">
                {#if meta}
                    Showing {orders.length} of {meta.total}
                    {pluralize(meta.total, "order")}
                {:else}
                    …
                {/if}
            </span>
            <ThemeToggle />
        </footer>
    </div>
</div>

<!-- The same pair under every card that edits in place, so Save is always in
     the same spot regardless of which card is open. -->
{#snippet cardActions(formID)}
    <div class="order-card-actions">
        <button
            type="submit"
            form={formID}
            class="btn sm"
            class:loading={busy === "Order updated"}
            disabled={busy === "Order updated"}
        >
            <span class="txt">Save</span>
        </button>
        <button
            type="button"
            class="btn sm transparent secondary"
            onclick={() => (editingCard = null)}
        >
            <span class="txt">Cancel</span>
        </button>
    </div>
{/snippet}

<Drawer
    open={detailOpen}
    size="lg"
    title={order ? `Order ${order.number}` : "Order"}
    onclose={() => (detailOpen = false)}
>
    {#if order}
    <div class="order-detail">
        <!--
            Two states and a method. The first two are what the order *is*; the
            third is how it was paid for, which is not a state and had been
            wearing the same chip as one.
        -->
        <div class="order-status">
            <span class="label {orderStatusClass(order.status)}">
                {orderStatusLabel(order.status)}
            </span>
            <span class="label {payChip.cls}">{payChip.text}</span>
            <span class="txt-hint txt-sm">via {methodName}</span>
            <div class="flex-fill"></div>
            <span class="txt-hint txt-sm">{formatDate(order.created_at)}</span>
        </div>

        <div class="order-grid">
        <div class="order-main">
        <section class="order-card">
        <div class="order-card-head">
            <h6 class="order-card-title">Items</h6>
            {#if editable && !draftLines}
                <button type="button" class="btn sm transparent secondary" onclick={startEdit}>
                    <span class="txt">Edit</span>
                </button>
            {/if}
        </div>
        <table class="table">
            <thead>
                <tr>
                    <!-- The card is titled Items; a column head saying Item
                         under it is the same word twice. The figures still
                         need naming. -->
                    <th></th>
                    <th class="txt-right">Qty</th>
                    <th class="txt-right">Unit</th>
                    <th class="txt-right">Total</th>
                    <!-- Head and cell share one predicate, so the table cannot
                         go ragged, and the column appears only once a parcel
                         exists to report. -->
                    {#if !draftLines && order.fulfillments?.length}
                        <th class="txt-right">Shipped</th>
                    {/if}
                    {#if draftLines}<th class="min-width"></th>{/if}
                </tr>
            </thead>
            <tbody>
                {#if draftLines}
                    {#each draftLines as line, i (line.id || "new-" + line.variant_id)}
                        <tr>
                            <td>
                                <div>{line.title}</div>
                                <div class="txt-hint txt-sm txt-code">
                                    {line.sku}{line.variant_label ? " · " + line.variant_label : ""}
                                </div>
                            </td>
                            <td class="txt-right">
                                <!-- A number box rather than plus and minus: an
                                     operator amending an order usually knows the
                                     figure, and clicking to it is slower. -->
                                <input
                                    type="number"
                                    class="order-qty"
                                    min="1"
                                    aria-label="Quantity of {line.sku}"
                                    bind:value={line.quantity}
                                />
                            </td>
                            <td class="txt-right">{formatMoney(line.unit_price)}</td>
                            <td class="txt-right">
                                {formatMinor(line.unit_price.amount_minor * line.quantity)}
                            </td>
                            <td class="txt-right min-width">
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    aria-label="Remove {line.sku}"
                                    title="Remove this line"
                                    onclick={() => removeDraftLine(i)}
                                >
                                    <i class="ri-close-line" aria-hidden="true"></i>
                                </button>
                            </td>
                        </tr>
                    {/each}
                {:else}
                    {#each order.line_items || [] as line (line.id)}
                        <tr>
                            <td>
                                <!-- The picture is how somebody recognises the
                                     thing in the box. A line whose product has
                                     been deleted keeps its place rather than
                                     shifting the column. -->
                                <div class="order-item">
                                    <div class="order-thumb">
                                        {#if line.image_url}
                                            <img src={line.image_url} alt="" loading="lazy" />
                                        {:else}
                                            <i class="ri-image-line" aria-hidden="true"></i>
                                        {/if}
                                    </div>
                                    <div class="order-item-text">
                                        <div>{line.title}</div>
                                        <div class="txt-hint txt-sm txt-code">
                                            {line.sku}{line.variant_label
                                                ? " · " + line.variant_label
                                                : ""}
                                        </div>
                                    </div>
                                </div>
                            </td>
                            <td class="txt-right">{line.quantity}</td>
                            <td class="txt-right">{formatMoney(line.unit_price)}</td>
                            <td class="txt-right">{formatMoney(line.total)}</td>
                            <!-- Settled lines step back to hint, so the dark
                                 figures are the ones still owing something. -->
                            {#if !draftLines && order.fulfillments?.length}
                                <td
                                    class="txt-right"
                                    class:txt-hint={(line.shipped_quantity ?? 0) >= line.quantity}
                                >
                                    {line.shipped_quantity ?? 0} of {line.quantity}
                                </td>
                            {/if}
                        </tr>
                    {/each}
                {/if}
            </tbody>
        </table>

        {#if draftLines}
            <div class="fields m-t-sm">
                <div class="field">
                    <label for="add-line">Add a product</label>
                    <Select
                        id="add-line"
                        placeholder="Choose a variant"
                        bind:value={addVariant}
                        options={variantOptions}
                    />
                </div>
                <div class="delimiter"></div>
                <div class="field addon">
                    <button
                        type="button"
                        class="btn sm secondary"
                        disabled={!addVariant}
                        onclick={addDraftLine}
                    >
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">Add</span>
                    </button>
                </div>
            </div>
            <div class="field-help">
                A line added here is priced as the variant is priced today. Stock moves with the
                change: this order has {order.status === "pending"
                    ? "only reserved its stock, so the reservation is what moves"
                    : "already taken its units off the shelf, so they go back or come off"}.
            </div>

            <div class="flex m-t-sm">
                <span class="txt-hint">New total</span>
                <div class="flex-fill"></div>
                <strong class="txt-money">{formatMinor(draftTotal)}</strong>
            </div>
            {#if draftBalance !== 0}
                <div class="flex m-t-5">
                    <span class="txt-hint">
                        {draftBalance > 0 ? "To collect" : "To refund"}
                    </span>
                    <div class="flex-fill"></div>
                    <span class="txt-money" class:txt-danger={draftBalance < 0}>
                        {formatMinor(Math.abs(draftBalance))}
                    </span>
                </div>
                <div class="field-help">
                    Saving does not move the money. {draftBalance > 0
                        ? "Collect the difference however this order was paid for."
                        : "Refunding is its own action, so it is recorded as one."}
                </div>
            {/if}

            <div class="inline-flex gap-sm m-t-sm">
                <button
                    type="button"
                    class="btn sm"
                    class:loading={busy === "Order updated"}
                    disabled={busy === "Order updated" || !draftLines.length}
                    onclick={saveEdit}
                >
                    <span class="txt">Save changes</span>
                </button>
                <button
                    type="button"
                    class="btn sm transparent secondary"
                    onclick={() => (draftLines = null)}
                >
                    <span class="txt">Discard</span>
                </button>
            </div>
            {#if !draftLines.length}
                <div class="field-help error">
                    An order cannot be emptied. Cancel it instead — that releases its stock and
                    says so on the order.
                </div>
            {/if}
        {/if}

        </section>

        {#if order.fulfillments?.length}
            <section class="order-card">
            <h6 class="order-card-title m-b-10">Shipments</h6>
            <div class="list">
                {#each order.fulfillments as f (f.id)}
                    <div class="list-item ship-row">
                        <!-- The chip is for a carrier. `manual` is how the
                             shipment was booked, which is not who has the
                             parcel, and giving it the same chip made it look
                             like an answer to the same question. -->
                        {#if f.carrier_name}
                            <span class="label">{f.carrier_name}</span>
                        {:else}
                            <span class="txt-hint txt-sm">No carrier</span>
                        {/if}
                        {#if f.tracking}
                            {#if f.tracking_url}
                                <a
                                    class="txt-code txt-sm"
                                    href={f.tracking_url}
                                    target="_blank"
                                    rel="noreferrer"
                                >
                                    {f.tracking}
                                </a>
                            {:else}
                                <span class="txt-code txt-sm">{f.tracking}</span>
                            {/if}
                        {:else}
                            <span class="txt-hint txt-sm">no tracking number</span>
                        {/if}
                        <div class="flex-fill"></div>
                        <span class="txt-hint txt-sm">{formatDate(f.created_at)}</span>
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            title="Change the tracking number"
                            aria-label="Change the tracking number"
                            onclick={() => startEditTracking(f)}
                        >
                            <i class="ri-pencil-line" aria-hidden="true"></i>
                        </button>
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            title="Remove this shipment"
                            aria-label="Remove this shipment"
                            onclick={() => askDeleteShipment(f)}
                        >
                            <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                        </button>
                        <!-- Two parcels on one order is exactly when a customer
                             rings to ask which one has the mug. -->
                        {#if f.lines?.length}
                            <div class="ship-contents txt-hint txt-sm">
                                {f.lines.map((l) => `${l.quantity} × ${l.title}`).join(", ")}
                            </div>
                        {/if}
                    </div>
                {/each}
            </div>
            </section>
        {/if}

        <OrderReturnsCard {order} onwithdraw={askWithdrawReturn} />
        </div>

        <!--
            The rail. What it holds is what somebody opening an order wants
            first — how much, whether it has been paid, and who it is for —
            and it is also what the panel had eight hundred empty pixels of
            room for while the address was squeezed into three hint lines.
        -->
        <aside class="order-rail">
            <!-- While an edit is on screen these are the figures the order
                 still has, not the ones being decided. They step back so the
                 draft total beside them is the one being read. -->
            <section class="order-card" class:is-superseded={!!draftLines}>
                <h6 class="order-card-title">
                    Summary
                    {#if draftLines}<span class="txt-hint">· as saved</span>{/if}
                </h6>
                <!--
                    The total leads. It is the number an order is opened to
                    see, and it had been sitting at the foot of the items in
                    the same weight as its own subtotal.
                -->
                <div class="order-total">{formatMoney(order.total)}</div>

                <div class="order-lines">
                    <div class="order-line">
                        <span class="txt-hint">Subtotal</span>
                        <span class="txt-money">{formatMoney(order.subtotal)}</span>
                    </div>
                    {#if order.discount?.amount_minor}
                        <div class="order-line">
                            <!-- Named, not just "Discount": an operator asked
                                 why a total is what it is wants to know which
                                 promotion did it. -->
                            <span class="txt-hint">
                                {order.discounts?.[0]?.code ||
                                    order.discounts?.[0]?.title ||
                                    "Discount"}
                            </span>
                            <span class="txt-money">−{formatMoney(order.discount)}</span>
                        </div>
                    {/if}
                    {#if order.tax?.amount_minor}
                        <div class="order-line">
                            <!-- Inclusive prices already contain it, so the line
                                 says so rather than looking like an addition
                                 that never happened. -->
                            <span class="txt-hint">
                                {order.line_items?.[0]?.tax?.name || "Tax"}{order.tax_inclusive
                                    ? " (included)"
                                    : ""}
                            </span>
                            <span class="txt-money">{formatMoney(order.tax)}</span>
                        </div>
                    {/if}
                    <div class="order-line">
                        <span class="txt-hint">Shipping</span>
                        <span class="txt-money">
                            {order.shipping?.amount_minor
                                ? formatMoney(order.shipping)
                                : "Free"}
                        </span>
                    </div>
                    <!-- Under the total rather than changing it: the total is
                         what was agreed, and the refund is what came back off
                         it. Two facts, not one. -->
                    {#if refundedMinor}
                        <div class="order-line">
                            <span class="txt-hint">Refunded</span>
                            <span class="txt-money">−{formatMoney(order.refunded)}</span>
                        </div>
                        <div class="order-line">
                            <span class="txt-hint">Net</span>
                            <span class="txt-money">
                                {formatMoney({
                                    amount_minor: remainingMinor,
                                    currency: order.currency,
                                })}
                            </span>
                        </div>
                    {/if}
                </div>
            </section>

            <!--
                Payment as its own card. How an order was settled and whether it
                has been are one thought, and they had been split between a chip
                at the top of the drawer and a line under the total.
            -->
            <section class="order-card">
                <div class="order-card-head">
                    <h6 class="order-card-title">Payment</h6>
                    {#if editingCard !== "payment"}
                        <button
                            type="button"
                            class="btn sm transparent secondary"
                            onclick={startEditPayment}
                        >
                            <span class="txt">Edit</span>
                        </button>
                    {/if}
                </div>

                {#if editingCard === "payment"}
                    <form id="payment-form" onsubmit={(e) => (e.preventDefault(), saveCard())}>
                        <div class="field">
                            <label for="pay-method">Method</label>
                            <Select
                                id="pay-method"
                                bind:value={form.payment_provider}
                                options={paymentOptions}
                            />
                        </div>
                        <div class="field m-t-sm">
                            <label for="pay-ref">Reference</label>
                            <input
                                id="pay-ref"
                                type="text"
                                bind:value={form.payment_reference}
                                placeholder="Transaction or receipt number"
                            />
                        </div>
                        <div class="field-help">
                            What the money is reconciled against. Changing the method moves none
                            of it — it records how this order was actually settled.
                        </div>
                        {@render cardActions("payment-form")}
                    </form>
                {:else}
                    <div class="order-paid {paidClass}">
                        {#if order.payment_status === "refunded"}
                            Refunded
                        {:else if refundedMinor > 0}
                            Partly refunded · {formatMoney(order.refunded)} of {formatMoney(
                                order.total,
                            )}
                        {:else if order.payment_status === "paid"}
                            Paid
                        {:else if order.payment_status === "failed"}
                            Payment failed
                        {:else}
                            Awaiting payment
                        {/if}
                    </div>
                    <div class="order-method">{methodName}</div>
                    {#if order.payment_reference}
                        <div class="order-reference txt-code">{order.payment_reference}</div>
                    {/if}
                    <OrderRefundList {order} />
                {/if}
            </section>

            <section class="order-card">
                <div class="order-card-head">
                    <h6 class="order-card-title">Customer</h6>
                    {#if editingCard !== "customer"}
                        <button
                            type="button"
                            class="btn sm transparent secondary"
                            onclick={startEditCustomer}
                        >
                            <span class="txt">Edit</span>
                        </button>
                    {/if}
                </div>

                {#if editingCard === "customer"}
                    <form id="customer-form" onsubmit={(e) => (e.preventDefault(), saveCard())}>
                        <div class="field">
                            <label for="cust-name">Name</label>
                            <input id="cust-name" type="text" bind:value={form.name} />
                        </div>
                        <div class="field m-t-5">
                            <label for="cust-email">Email</label>
                            <input id="cust-email" type="email" bind:value={form.email} />
                        </div>
                        <div class="field m-t-5">
                            <label for="cust-phone">Phone</label>
                            <input id="cust-phone" type="tel" bind:value={form.phone} />
                        </div>
                        <div class="field m-t-5">
                            <label for="cust-line1">Address</label>
                            <input id="cust-line1" type="text" bind:value={form.address.line1} />
                        </div>
                        <div class="field m-t-5">
                            <label for="cust-line2">Line 2</label>
                            <input id="cust-line2" type="text" bind:value={form.address.line2} />
                        </div>
                        <div class="fields m-t-5">
                            <div class="field">
                                <label for="cust-city">City</label>
                                <input id="cust-city" type="text" bind:value={form.address.city} />
                            </div>
                            <div class="delimiter"></div>
                            <div class="field">
                                <label for="cust-state">State</label>
                                <input id="cust-state" type="text" bind:value={form.address.state} />
                            </div>
                        </div>
                        <div class="fields m-t-5">
                            <div class="field">
                                <label for="cust-postal">Postcode</label>
                                <input
                                    id="cust-postal"
                                    type="text"
                                    bind:value={form.address.postal_code}
                                />
                            </div>
                            <div class="delimiter"></div>
                            <div class="field">
                                <label for="cust-country">Country</label>
                                <input
                                    id="cust-country"
                                    type="text"
                                    bind:value={form.address.country}
                                />
                            </div>
                        </div>
                        {#if order.status === "partial"}
                            <div class="field-help">
                                Part of this order has already gone out. Correcting the address
                                fixes the record and the parcels still to come; it does not move
                                the one that left.
                            </div>
                        {:else if order.status === "shipped" || order.status === "delivered"}
                            <div class="field-help">
                                This order has already gone out. Correcting the address fixes the
                                record; it does not move the parcel.
                            </div>
                        {/if}
                        {@render cardActions("customer-form")}
                    </form>
                {:else}
                    <div class="order-customer-name">{order.name || "—"}</div>
                <!-- Links, not text. Chasing an order means writing to somebody
                     or ringing them, and both are one click from here. -->
                {#if order.email}
                    <a class="order-contact" href="mailto:{order.email}">{order.email}</a>
                {/if}
                {#if order.phone}
                    <a class="order-contact" href="tel:{order.phone}">{order.phone}</a>
                {/if}
                    {#if order.address}
                        <address class="order-address">
                            {order.address.line1}{order.address.line2
                                ? ", " + order.address.line2
                                : ""}<br />
                            {order.address.city}{order.address.state
                                ? " " + order.address.state
                                : ""}
                            {order.address.postal_code}<br />
                            {order.address.country}
                        </address>
                    {/if}
                {/if}
            </section>
        </aside>
        </div>
    </div>
    {/if}

    {#snippet footer()}
        {#if order}
            <!-- Not `payment_status === "pending"`: a declined card is payable on
                 a second attempt and had no forward affordance at all, while a
                 cancelled order keeps its payment pending and the engine
                 answers 409 — a button that is there and refused. -->
            {#if order.payment_status !== "paid" && order.payment_status !== "refunded" && order.status !== "cancelled"}
                <button
                    type="button"
                    class="btn success"
                    class:loading={busy === "Marked paid"}
                    disabled={busy === "Marked paid"}
                    onclick={markPaid}
                >
                    <i class="ri-money-dollar-circle-line" aria-hidden="true"></i>
                    <span class="txt">Mark paid</span>
                </button>
            {/if}
            <!-- The same shape as the Cancel gate below, for the same reasons: a
                 cancelled order records nothing (the engine no-ops, and the
                 toast would report a change that did not happen), and a shipped
                 or delivered order is unpaid because the money is collected at
                 the door, not because an attempt failed — the sweeper never
                 reaches it, so the sentence about the reservation would be
                 false. -->
            {#if order.payment_status === "pending" && order.status !== "cancelled" && order.status !== "shipped" && order.status !== "delivered"}
                <button
                    type="button"
                    class="btn transparent"
                    class:loading={busy === "Failure recorded"}
                    disabled={busy === "Failure recorded"}
                    onclick={askPaymentFailed}
                >
                    <i class="ri-close-circle-line" aria-hidden="true"></i>
                    <span class="txt">Payment failed</span>
                </button>
            {/if}
            {#if order.status === "confirmed" || order.status === "partial"}
                <button type="button" class="btn" onclick={startShipping}>
                    <i class="ri-truck-line" aria-hidden="true"></i>
                    <span class="txt">{order.status === "partial" ? "Ship the rest" : "Ship"}</span>
                </button>
            {/if}
            {#if order.status === "shipped"}
                <button
                    type="button"
                    class="btn success"
                    class:loading={busy === "Marked delivered"}
                    disabled={busy === "Marked delivered"}
                    onclick={deliver}
                >
                    <i class="ri-checkbox-circle-line" aria-hidden="true"></i>
                    <span class="txt">Delivered</span>
                </button>
            {/if}
            <div class="flex-fill"></div>
            <!--
                Undo, kept apart from the workflow buttons on the left. Marking
                the wrong order paid is a slip somebody notices a minute later,
                and the fix should be here rather than in the database — but it
                must not sit where the next step goes, or it becomes one.
            -->
            {#if order.payment_status === "paid" || order.payment_status === "failed" || order.status === "delivered"}
                <button
                    type="button"
                    class="btn transparent"
                    popovertarget="order-corrections"
                    aria-haspopup="menu"
                >
                    <span class="txt">Undo</span>
                    <i class="ri-arrow-down-s-line" aria-hidden="true"></i>
                </button>
                <div
                    id="order-corrections"
                    class="dropdown dropdown-sm dropdown-upside"
                    popover="auto"
                    role="menu"
                >
                    {#if order.payment_status === "paid"}
                        <button
                            type="button"
                            role="menuitem"
                            class="dropdown-item"
                            class:loading={busy === "Marked unpaid"}
                            disabled={busy === "Marked unpaid"}
                            onclick={() => {
                                document.getElementById("order-corrections")?.hidePopover();
                                markUnpaid();
                            }}
                        >
                            <i class="ri-money-dollar-circle-line" aria-hidden="true"></i>
                            <span class="txt">Mark unpaid</span>
                        </button>
                    {/if}
                    {#if order.payment_status === "failed"}
                        <button
                            type="button"
                            role="menuitem"
                            class="dropdown-item"
                            class:loading={busy === "Failure cleared"}
                            disabled={busy === "Failure cleared"}
                            onclick={() => {
                                document.getElementById("order-corrections")?.hidePopover();
                                clearFailure();
                            }}
                        >
                            <i class="ri-arrow-go-back-line" aria-hidden="true"></i>
                            <span class="txt">Clear failure</span>
                        </button>
                    {/if}
                    {#if order.status === "delivered"}
                        <button
                            type="button"
                            role="menuitem"
                            class="dropdown-item"
                            class:loading={busy === "Delivery undone"}
                            disabled={busy === "Delivery undone"}
                            onclick={() => {
                                document.getElementById("order-corrections")?.hidePopover();
                                undeliver();
                            }}
                        >
                            <i class="ri-arrow-go-back-line" aria-hidden="true"></i>
                            <span class="txt">Not delivered</span>
                        </button>
                    {/if}
                </div>
            {/if}
            <!-- Beside Refund rather than among the workflow buttons on the
                 left: a return is not the next step of a sale. `transparent`
                 for the same reason it is neither expected nor destructive. -->
            {#if returnable}
                <button type="button" class="btn transparent" onclick={() => (returnOpen = true)}>
                    <i class="ri-inbox-unarchive-line" aria-hidden="true"></i>
                    <span class="txt">Record a return</span>
                </button>
            {/if}
            <!-- Still there on a partly refunded order, which is the fix: it
                 stays `paid` while the store holds any of the money, so the
                 operator can finish what they started. -->
            {#if refundable}
                <button type="button" class="btn transparent" onclick={() => (refundOpen = true)}>
                    <span class="txt">Refund</span>
                </button>
            {/if}
            <!-- Absent rather than there and refused, the rule `editable`
                 already states: a partly shipped order is a return. -->
            {#if order.status !== "cancelled" && order.status !== "partial" && order.status !== "shipped" && order.status !== "delivered"}
                <button type="button" class="btn danger" onclick={askCancel}>
                    <span class="txt">Cancel</span>
                </button>
            {/if}
        {/if}
    {/snippet}
</Drawer>

<ShipDialog
    open={trackingOpen}
    {order}
    busy={busy === "Shipped"}
    {carrierChoices}
    {carrierOptions}
    bind:tracking
    bind:carrier={shipCarrier}
    onlookup={(number) => lookupCarriers(number, { target: "ship" })}
    onpick={() => (carrierPicked = true)}
    onclose={() => (trackingOpen = false)}
    onship={ship}
/>

<RefundDialog
    open={refundOpen}
    {order}
    {methodName}
    busy={busy === "Refunded"}
    onclose={() => (refundOpen = false)}
    onrefund={refund}
/>

<ReturnDialog
    open={returnOpen}
    {order}
    busy={busy === "Return recorded"}
    onclose={() => (returnOpen = false)}
    onsave={recordReturn}
/>

<!--
    Correcting a tracking number after the fact. The parcel left either way, so
    this changes what was recorded about the shipment and nothing about the
    order — which is why it is its own popup rather than a second Ship form.
-->
<Drawer
    open={!!editing}
    size="popup sm"
    title="Tracking number"
    onclose={() => (editing = null)}
>
    <form id="tracking-form" onsubmit={(e) => (e.preventDefault(), saveTracking())}>
        <div class="field">
            <label for="edit-tracking">Tracking number</label>
            <input
                id="edit-tracking"
                type="text"
                bind:value={editTracking}
                oninput={() => lookupCarriers(editTracking)}
                placeholder="Leave empty if there isn't one"
            />
        </div>

        <div class="field m-t-sm">
            <label for="edit-carrier">Carrier</label>
            <Select
                id="edit-carrier"
                bind:value={editCarrier}
                onchange={() => (carrierPicked = true)}
                options={carrierChoices}
            />
        </div>
        <div class="field-help">
            {#if carrierOptions.length === 1}
                That number is {carrierOptions[0].name}'s.
            {:else if carrierOptions.length > 1}
                Several carriers issue numbers of that shape. Pick the right one if the
                first guess is wrong.
            {:else if editTracking.trim()}
                No carrier uses numbers of that shape. Pick one if you know who has it.
            {:else}
                The carrier is worked out from the number.
            {/if}
        </div>
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (editing = null)}>
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form="tracking-form"
            class="btn expanded"
            class:loading={busy === "Tracking updated"}
            disabled={busy === "Tracking updated"}
        >
            <span class="txt">Save</span>
        </button>
    {/snippet}
</Drawer>


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
        path a shopper's does — it reserves stock, snapshots prices and gets an
        access token the customer can use to read it back.
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
            <input id="no-line1" type="text" autocomplete="off" bind:value={draft.address.line1} />
        </div>
        <div class="field m-t-5">
            <label for="no-line2">Apartment, suite, etc.</label>
            <input id="no-line2" type="text" autocomplete="off" bind:value={draft.address.line2} />
        </div>
        <div class="fields m-t-5">
            <div class="field">
                <label for="no-city">City</label>
                <input id="no-city" type="text" autocomplete="off" bind:value={draft.address.city} />
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
                <input id="no-state" type="text" autocomplete="off" bind:value={draft.address.state} />
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
                                    <span class="txt-hint txt-sm txt-code row-handle">{line.sku}</span>
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
                                    onclick={() => (draft.lines = draft.lines.filter((_, j) => j !== i))}
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
                <Select
                    id="no-add"
                    placeholder="Choose a variant"
                    bind:value={addLineVariant}
                    options={variantOptions}
                />
            </div>
            <div class="delimiter"></div>
            <div class="field addon">
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={!addLineVariant}
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
            Applied the way a shopper's is — checked and claimed under the checkout's own lock,
            so an expired or fully used code refuses the whole order rather than quietly
            placing it at full price.
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
            customer can read it back with the access token this creates. Cash on delivery leaves
            it awaiting payment, which is what "Mark paid" is for. Shipping, tax and any discount
            are added by the checkout and appear on the order once it is placed.
        </div>

        {#if draft.lines.length}
            <!-- Items subtotal, not Total: this sums the lines, while the
                 checkout adds flat shipping and then tax on the discounted
                 amount. It was never the total, and a label that named only the
                 discount would be the same lie in a smaller font. -->
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
            <button
                type="button"
                class="btn"
                onclick={() => {
                    const row = placed.order;
                    createOpen = false;
                    placed = null;
                    openOrder(row);
                }}
            >
                <i class="ri-external-link-line" aria-hidden="true"></i>
                <span class="txt">Open order</span>
            </button>
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

<Confirm
    bind:open={confirmOpen}
    title={confirmConfig.title}
    message={confirmConfig.message}
    confirmLabel={confirmConfig.confirmLabel}
    danger={confirmConfig.danger}
    onconfirm={() => confirmConfig.run?.()}
/>
