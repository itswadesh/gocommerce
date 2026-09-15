<script>
    /**
     * One order, at its own address.
     *
     * This was a drawer over the orders list, holding the record in component
     * state, and that had nothing to do with how it looked. An order with no
     * URL cannot be sent to a colleague, cannot be opened in a second tab
     * beside the first, does not survive a reload, and gives the Back button
     * nothing to go back to — while every action on it reloaded the list
     * underneath and threw away the pages it had taken to find the order. A
     * warehouse also cannot print it: Ctrl+P on a drawer prints the list behind
     * it, with the drawer on top.
     *
     * So it is a route. `GET /api/admin/orders/{id}` is what the drawer already
     * fetched, which is why this is a move rather than a rewrite: the cards,
     * the dialogs and the action bar are the ones that were here before, with
     * the fields the API has always sent and the drawer dropped now on screen.
     *
     * The list keeps its own URL, so Back from here lands on page 6 of the
     * filter the operator built, which is the thing a drawer could never do.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { page as route } from "$app/state";
    import { api, can, query, request } from "$lib/api.js";
    import {
        formatMoney,
        formatDate,
        orderStatusClass,
        orderStatusLabel,
        paymentLabel,
        summarizeEdit,
    } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { hasModule } from "$lib/modules.svelte.js";
    import { openDocument, safeFilename } from "$lib/download.js";
    import AccessTokenCard from "$lib/components/AccessTokenCard.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import OrderNoteCard from "$lib/components/OrderNoteCard.svelte";
    import OrderRefundList from "$lib/components/OrderRefundList.svelte";
    import OrderReturnsCard from "$lib/components/OrderReturnsCard.svelte";
    import OrderTimeline from "$lib/components/OrderTimeline.svelte";
    import PromptDialog from "$lib/components/PromptDialog.svelte";
    import RefundDialog from "$lib/components/RefundDialog.svelte";
    import ReturnDialog from "$lib/components/ReturnDialog.svelte";
    import Select from "$lib/components/Select.svelte";
    import ShipDialog from "$lib/components/ShipDialog.svelte";
    import VariantPicker from "$lib/components/VariantPicker.svelte";

    const orderId = $derived(route.params.id);
    const readable = $derived(can("orders.read"));

    /*
     * An order's actions are cut three ways in the engine, and this screen —
     * where every order write now lives — honoured none of them: an operator
     * holding orders.read alone was shown Mark paid, Payment failed, Ship,
     * Delivered, Refund, Cancel, both card editors and the shipment buttons,
     * and every one of them answered 403.
     *
     * The split is the engine's, read off core/commerce_http.go rather than
     * guessed at, because a gate that disagrees with the route it guards is
     * worse than none — it hides a button the engine would have allowed.
     *
     *   orders.write   the order's own state: mark paid / unpaid / payment
     *                  failed, deliver and undeliver (:213-214, NOT fulfill —
     *                  fulfill is the parcel, not the order), cancel, PATCH,
     *                  the line editor, and recording or withdrawing a return.
     *   orders.fulfill creating a fulfillment and amending or removing one.
     *                  A warehouse account is meant to hold this and nothing
     *                  else on an order (rights.go).
     *   orders.refund  sending money back out of the store, alone.
     */
    const mayWrite = $derived(can("orders.write"));
    const mayFulfill = $derived(can("orders.fulfill"));
    const mayRefund = $derived(can("orders.refund"));

    let loading = $state(true);
    let order = $state(null);
    let busy = $state("");

    let confirmOpen = $state(false);
    let confirmConfig = $state({});

    let trackingOpen = $state(false);
    let tracking = $state("");
    let shipCarrier = $state("");
    let shipProvider = $state("");

    let refundOpen = $state(false);
    let returnOpen = $state(false);
    let markPaidOpen = $state(false);
    let cancelOpen = $state(false);

    $effect(() => {
        orderId;
        if (readable) load();
    });

    async function load() {
        loading = true;
        try {
            order = await api.get(`/api/admin/orders/${orderId}`);
            loadTimeline(orderId);
        } catch (err) {
            // A deleted or mistyped id is not a screen. The list is where the
            // operator can do something about it, and the toast says what
            // happened on the way.
            if (err.status === 404) {
                toast.error(err);
                goto(`${base}/orders`);
                return;
            }
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /** The order's own history: what happened, who did it, and in what order. */
    let timeline = $state([]);
    let timelineLoading = $state(false);

    async function loadTimeline(id) {
        timelineLoading = true;
        try {
            // 200 is the engine's MaxLimit, so this is the largest single page
            // it will serve — and more history than any order has.
            const res = await api.get(`/api/admin/orders/${id}/timeline` + query({ limit: 200 }));
            timeline = res.data ?? [];
        } catch {
            // A history that failed to load must not toast over an order that
            // opened perfectly well. The card says it has nothing.
            timeline = [];
        } finally {
            timelineLoading = false;
        }
    }

    async function act(label, fn) {
        busy = label;
        try {
            order = await fn();
            // Every action writes a line of history — including a note, which
            // announces nothing to the customer but is still recorded as
            // something somebody did.
            if (order?.id) loadTimeline(order.id);
            toast.success(label);
        } catch (err) {
            toast.error(err);
        } finally {
            busy = "";
        }
    }

    /**
     * Marking paid, with the reference the money arrived under.
     *
     * The reference was hard-coded to "" for as long as this screen has
     * existed, so no panel-marked payment recorded how it was collected and the
     * only way to add one was a second trip through the Payment card. The
     * engine writes the field only when it is non-empty — deliberately, so it
     * can never erase one a gateway already set — which is exactly why leaving
     * the box blank here is safe and why it is not a required field.
     */
    function markPaid(reference) {
        markPaidOpen = false;
        return act("Marked paid", () =>
            api.post(`/api/admin/orders/${order.id}/mark-paid`, { reference }),
        );
    }

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
     * is the same question: put this order's money back where it was.
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
     * would promise a record that does not exist. Cancel is the opposite case,
     * and gets a box for exactly that reason.
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
     * format, a hand-written docket — must still be recordable.
     */
    const carrierChoices = $derived.by(() => {
        const suggested = carrierOptions.map((c) => ({ value: c.code, label: c.name }));
        const seen = new Set(carrierOptions.map((c) => c.code));
        const rest = allCarriers
            .filter((c) => !seen.has(c.code))
            .map((c) => ({ value: c.code, label: c.name }));
        return [{ value: "", label: "Not recorded" }, ...suggested, ...rest];
    });

    /**
     * Who can book a shipment in this build.
     *
     * From the store's own settings, which is where the engine composes the one
     * provider list every client reads — the same source the payment picker
     * uses. Before this the panel posted the literal "manual" on every
     * shipment, so a store that installed a carrier module could not book a
     * parcel through it from the panel at all, and the module was dead weight.
     */
    const providerChoices = $derived(
        settings.fulfillmentProviders.map((p) => ({ value: p.code, label: p.name || p.code })),
    );

    /** `manual` when this build has it, which is the provider that records what
     *  an operator types; otherwise whatever is installed, in order. */
    const defaultProvider = $derived(
        providerChoices.find((p) => p.value === "manual")?.value ??
            providerChoices[0]?.value ??
            "manual",
    );

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
        shipProvider = defaultProvider;
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
            fill(carrierOptions[0]?.code ?? "");
        } catch {
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

    const ADDRESS_FIELDS = ["line1", "line2", "city", "state", "postal_code", "country"];

    function startEditCustomer() {
        editingCard = "customer";
        form = {
            name: order.name ?? "",
            email: order.email ?? "",
            phone: order.phone ?? "",
            address: { ...(order.address ?? {}) },
        };
    }

    function startEditNotes() {
        editingCard = "notes";
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
     * cannot offer a method the engine would refuse.
     */
    const methods = $derived(settings.paymentMethods);

    /**
     * Whether the address in the form differs from the one on the order.
     *
     * This decides whether `address` is sent at all, and that is the whole fix
     * for a phone number that could not be corrected. OrderPatch takes six
     * pointers, so an omitted address means "leave alone" — but the panel sent
     * it on every save, and the engine validates any address it is given
     * against line1, city, postal_code and country. An order imported with a
     * sparse address therefore refused every contact correction with "address
     * is missing line1, city, postal_code, country", and there was no way to
     * get past it except to invent an address.
     */
    function addressChanged() {
        const was = order.address ?? {};
        return ADDRESS_FIELDS.some((k) => (form.address[k] ?? "") !== (was[k] ?? ""));
    }

    /**
     * Saving whichever card is open. `noteText` comes from the note card, which
     * owns its own draft; the other two read `form`.
     */
    function saveCard(noteText) {
        const card = editingCard;

        if (card === "customer") {
            const patch = {};
            if (form.name.trim() !== (order.name ?? "")) patch.name = form.name.trim();
            if (form.email.trim() !== (order.email ?? "")) patch.email = form.email.trim();
            if (form.phone.trim() !== (order.phone ?? "")) patch.phone = form.phone.trim();
            if (addressChanged()) patch.address = form.address;
            if (!Object.keys(patch).length) {
                // The engine answers "nothing to change" to an empty patch, and
                // it is right to — but an operator who opened a card and closed
                // it has not made an error worth a red toast.
                editingCard = null;
                return;
            }
            return act("Order updated", async () => {
                const updated = await api.patch(`/api/admin/orders/${order.id}`, patch);
                editingCard = null;
                return updated;
            });
        }

        return act(card === "notes" ? "Note saved" : "Order updated", async () => {
            let patch;
            if (card === "payment") {
                patch = {
                    payment_provider: form.payment_provider,
                    payment_reference: form.payment_reference.trim(),
                };
            } else {
                /* The engine replaces metadata whole, so the read-modify-write
                   is the caller's — the same merge the products and categories
                   screens do. An emptied box removes the key rather than
                   storing "", so "no note yet" and "a note that says nothing"
                   stay one state. */
                const meta = { ...(order.metadata ?? {}) };
                const text = (noteText ?? "").trim();
                if (text) meta.notes = text;
                else delete meta.notes;
                patch = { metadata: meta };
            }
            const updated = await api.patch(`/api/admin/orders/${order.id}`, patch);
            editingCard = null;
            return updated;
        });
    }

    /**
     * Removing a shipment recorded in error.
     *
     * Confirmed rather than immediate: it is the one thing on this screen that
     * throws a record away, and what it held goes back on the order to be
     * shipped again.
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
            run: () => act("Shipment removed", () => api.delete(`/api/admin/fulfillments/${f.id}`)),
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

    // ------------------------------------------------------------ editing

    /**
     * The lines as the screen is editing them, or null when it is not.
     *
     * A copy rather than the order itself: an edit is not applied until it is
     * saved, and half of one on screen must not be mistaken for the order.
     */
    let draftLines = $state(null);
    let addVariant = $state("");
    let addPicked = $state(null);

    /* What has gone back, and what the store still holds. Both come off the
       order itself. */
    const refundedMinor = $derived(order?.refunded?.amount_minor ?? 0);
    const remainingMinor = $derived((order?.total?.amount_minor ?? 0) - refundedMinor);
    /* A partly refunded order stays `paid`, so it keeps the button and the
       operator can finish the refund; it vanishes on the last one. */
    const refundable = $derived(order?.payment_status === "paid" && remainingMinor > 0);
    /* The same chip the list row wears, from the same function, so the two can
       never disagree about one order. */
    const payChip = $derived(paymentLabel(order));

    const paidClass = $derived(
        order?.payment_status === "refunded"
            ? "is-refunded"
            : refundedMinor > 0
              ? "is-part-refunded"
              : order?.payment_status === "paid"
                ? "is-paid"
                : "",
    );

    /*
     * The total, coloured by whether the money is actually held.
     *
     * Its own derivation rather than `paidClass` above, because the two answer
     * different questions. That one drives the line UNDER the figure and stays
     * in the hint colour while an order is merely unpaid — the argument being
     * that owing money is the next thing to do rather than bad news. This one
     * was asked for as a two-state signal: green when payment has been made,
     * red when it has not. Refunded counts as not held, because the money has
     * gone back out.
     *
     * Read off `payment_status` alone, so the figure and the chip beside it
     * cannot disagree about one order.
     */
    const totalState = $derived(order?.payment_status === "paid" ? "is-paid" : "is-unpaid");

    /* The label the module that installed the provider gave it. An order can
       name a provider this build no longer has, so the code itself is the
       fallback rather than a blank. */
    const methodName = $derived(
        methods.find((m) => m.code === order?.payment_provider)?.name ??
            order?.payment_provider ??
            "—",
    );

    /* Goods can only come back from an order that has sent some out. */
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
            refundedMinor === 0 &&
            activeReturns.length === 0,
    );

    /* The engine refuses any patch but a note on a cancelled order, so the two
       cards that send one hide their Edit button rather than offering a button
       whose only possible answer is 409. The note card is deliberately still
       editable — "refunded manually by bank transfer" belongs on the order that
       went wrong. */
    const patchable = $derived(!!order && order.status !== "cancelled");

    const draftTotal = $derived(
        (draftLines ?? []).reduce((sum, l) => sum + l.unit_price.amount_minor * l.quantity, 0) +
            (order?.shipping?.amount_minor ?? 0) -
            (order?.discount?.amount_minor ?? 0),
    );
    const draftBalance = $derived(draftTotal - (order?.total?.amount_minor ?? 0));

    function startEdit() {
        draftLines = (order.line_items ?? []).map((l) => ({ ...l }));
        addVariant = "";
        addPicked = null;
    }

    function addDraftLine() {
        const row = addPicked;
        if (!row) return;
        const existing = draftLines.find((l) => l.variant_id === row.variant_id);
        if (existing) {
            // Already on the order: the operator means one more of it, not a
            // second line the engine would have to merge.
            existing.quantity += 1;
        } else {
            draftLines = [
                ...draftLines,
                {
                    id: 0,
                    variant_id: row.variant_id,
                    sku: row.sku,
                    title: row.title,
                    variant_label: row.variant_label,
                    quantity: 1,
                    // Today's price, which is what the engine will snapshot.
                    unit_price: row.unit_price,
                },
            ];
        }
        addVariant = "";
        addPicked = null;
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
            for (const line of summarizeEdit(result.changed, order?.currency ?? settings.currency)) {
                toast.success(line);
            }
            draftLines = null;
            return result.order;
        });

    /** A bare amount in the order's own currency, for the balance sentences. */
    function formatMinor(minor) {
        return formatMoney({ amount_minor: minor, currency: order?.currency ?? settings.currency });
    }

    /**
     * Cancelling, with the reason it was cancelled for.
     *
     * The engine stores this free text on the order.cancelled event, and it is
     * the store's only durable explanation of a closed sale. The panel sent the
     * literal "cancelled from the admin panel" on every cancellation, so fraud,
     * a customer's change of mind, an out-of-stock line and a duplicate order
     * all read identically afterwards — the field was carrying no information
     * at all.
     */
    function cancelOrder(reason) {
        cancelOpen = false;
        return act("Order cancelled", () =>
            api.post(`/api/admin/orders/${order.id}/cancel`, { reason }),
        );
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
     */
    async function refund({ amount_minor, reason }) {
        await act("Refunded", () =>
            api.post(`/api/admin/orders/${order.id}/refund`, { amount_minor, reason }),
        );
        refundOpen = false;
    }

    /**
     * Recording goods that have come back. Nothing here touches the money:
     * refunding is its own button, and chaining the two would hide a partial
     * failure.
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

    /** Withdrawing a return recorded in error. */
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
     * usual case and the default the dialog opens on. The engine then works out
     * what is left under the order's row lock rather than trusting a screen
     * that may be a minute old.
     */
    async function ship({ tracking: number, carrier, provider, lines }) {
        await act("Shipped", () =>
            api.post("/api/admin/create-fulfillment", {
                order_id: order.id,
                provider: provider || defaultProvider,
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

    // --------------------------------------------------- fields off the order

    /**
     * Where the units came off, by name.
     *
     * `line_items[].location_id` decides where a cancellation puts the stock
     * back, so it is a real operational fact rather than a curiosity — and an
     * id on screen is not a place. Best effort: `locations.read` is a separate
     * right, and an operator who has orders.read without it still gets the
     * order, with the id rather than a name.
     */
    let locations = $state([]);
    /* Plain, not `$state`: the effect below both reads and writes this, and a
       reactive flag would re-run the effect it just satisfied — which, on a
       store that answers with an empty list, is a request per tick forever. */
    let locationsAsked = false;

    const needsLocations = $derived(
        (order?.line_items ?? []).some((l) => l.location_id != null),
    );

    $effect(() => {
        if (!needsLocations || locationsAsked) return;
        locationsAsked = true;
        api.get("/api/admin/locations")
            .then((res) => (locations = res.data ?? []))
            .catch(() => {
                // `locations.read` is its own right. An operator holding
                // orders.read without it still gets the order; the line then
                // shows the id rather than the name.
                locations = [];
            });
    });

    function locationName(id) {
        return locations.find((l) => l.id === id)?.name || `Location #${id}`;
    }

    /** Whether any line was actually charged tax, which is what decides the
     *  column: adding an empty one to every zero-rated order is noise. */
    const taxed = $derived((order?.line_items ?? []).some((l) => l.tax?.amount_minor));

    /**
     * What to call the tax line in the summary.
     *
     * It read `line_items[0].tax.name` — the first line's — which is a fair
     * guess on a single-rate order and a mislabelling on a mixed one: a basket
     * of books at 0% and a mug at 20% was labelled with whichever happened to
     * be first. So the name is used only when every taxed line agrees on it.
     */
    const taxName = $derived.by(() => {
        const names = new Set(
            (order?.line_items ?? [])
                .filter((l) => l.tax?.amount_minor)
                .map((l) => l.tax?.name || ""),
        );
        return names.size === 1 ? [...names][0] || "Tax" : "Tax";
    });

    /** Basis points as a percentage: 1800 → "18", 1250 → "12.5". */
    const ratePercent = (bp) => String(Math.round(bp) / 100);

    /* Metadata the store put there that is not the note. A module writes its
       own keys here and they were invisible — which made the panel the one
       client that could not see what the store was recording. */
    const extraMetadata = $derived(
        Object.entries(order?.metadata ?? {}).filter(([key]) => key !== "notes"),
    );

    function metaValue(value) {
        return typeof value === "string" ? value : JSON.stringify(value);
    }

    /** The provider that booked a parcel, named as the module names itself. */
    function providerName(code) {
        return settings.fulfillmentProviders.find((p) => p.code === code)?.name || code;
    }

    function closeMore() {
        document.getElementById("order-more")?.hidePopover();
    }
</script>

<svelte:head><title>{order ? `Order ${order.number}` : "Order"} · GoCommerce</title></svelte:head>

<!-- The same pair under every card that edits in place, so Save is always in
     the same spot regardless of which card is open. -->
{#snippet cardActions(formID, label = "Order updated")}
    <div class="order-card-actions">
        <button
            type="submit"
            form={formID}
            class="btn sm"
            class:loading={busy === label}
            disabled={busy === label}
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

{#if !readable}
    <NoAccess right="orders.read" what="orders" />
{:else}
    <div class="page page-orders shopify-skin">
        <div class="page-content full-height">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <a href="{base}/orders">Orders</a>
                    <div>{order?.number || "…"}</div>
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

                <div class="page-header-primary-btns">
                    {#if order}
                        <!-- Not `payment_status === "pending"`: a declined card is
                             payable on a second attempt and had no forward
                             affordance at all, while a cancelled order keeps its
                             payment pending and the engine answers 409. -->
                        {#if mayWrite && order.payment_status !== "paid" && order.payment_status !== "refunded" && order.status !== "cancelled"}
                            <button
                                type="button"
                                class="btn sm success"
                                class:loading={busy === "Marked paid"}
                                disabled={busy === "Marked paid"}
                                onclick={() => (markPaidOpen = true)}
                            >
                                <i class="ri-money-dollar-circle-line" aria-hidden="true"></i>
                                <span class="txt">Mark paid</span>
                            </button>
                        {/if}
                        {#if mayWrite && order.payment_status === "pending" && order.status !== "cancelled" && order.status !== "shipped" && order.status !== "delivered"}
                            <button
                                type="button"
                                class="btn sm secondary"
                                class:loading={busy === "Failure recorded"}
                                disabled={busy === "Failure recorded"}
                                onclick={askPaymentFailed}
                            >
                                <i class="ri-close-circle-line" aria-hidden="true"></i>
                                <span class="txt">Payment failed</span>
                            </button>
                        {/if}
                        <!-- Booking the parcel is orders.fulfill; marking it
                             delivered below is orders.write, which is how the
                             engine cuts them. -->
                        {#if mayFulfill && (order.status === "confirmed" || order.status === "partial")}
                            <button type="button" class="btn sm" onclick={startShipping}>
                                <i class="ri-truck-line" aria-hidden="true"></i>
                                <span class="txt">
                                    {order.status === "partial" ? "Ship the rest" : "Ship"}
                                </span>
                            </button>
                        {/if}
                        {#if mayWrite && order.status === "shipped"}
                            <button
                                type="button"
                                class="btn sm success"
                                class:loading={busy === "Marked delivered"}
                                disabled={busy === "Marked delivered"}
                                onclick={deliver}
                            >
                                <i class="ri-checkbox-circle-line" aria-hidden="true"></i>
                                <span class="txt">Delivered</span>
                            </button>
                        {/if}
                        <!-- Every button in `.page-header-primary-btns` leads
                             with an icon, and that is PocketBase's contract
                             rather than a preference: below 550px the
                             stylesheet turns each one into a circle and hides
                             `i + .txt`, so a button with no icon keeps its
                             label inside a round box and overlaps its
                             neighbours. -->
                        {#if mayRefund && refundable}
                            <button
                                type="button"
                                class="btn sm secondary"
                                title="Refund"
                                onclick={() => (refundOpen = true)}
                            >
                                <i class="ri-refund-2-line" aria-hidden="true"></i>
                                <span class="txt">Refund</span>
                            </button>
                        {/if}

                        <button
                            type="button"
                            class="btn sm secondary"
                            title="More actions"
                            popovertarget="order-more"
                            aria-haspopup="menu"
                        >
                            <i class="ri-more-2-line" aria-hidden="true"></i>
                            <span class="txt">More actions</span>
                        </button>
                        <div id="order-more" class="dropdown dropdown-sm" popover="auto" role="menu">
                            <!-- The one thing a warehouse actually needs off this
                                 screen, and the panel had no way to print
                                 anything at all. Its own URL, so ten of them can
                                 be opened in ten tabs and printed in a row. -->
                            <a
                                role="menuitem"
                                class="dropdown-item"
                                href="{base}/orders/{orderId}/print"
                                onclick={closeMore}
                            >
                                <i class="ri-printer-line" aria-hidden="true"></i>
                                <span class="txt">Packing slip</span>
                            </a>
                            {#if mayWrite && returnable}
                                <button
                                    type="button"
                                    role="menuitem"
                                    class="dropdown-item"
                                    onclick={() => {
                                        closeMore();
                                        returnOpen = true;
                                    }}
                                >
                                    <i class="ri-inbox-unarchive-line" aria-hidden="true"></i>
                                    <span class="txt">Record a return</span>
                                </button>
                            {/if}
                            {#if mayWrite && order.payment_status === "paid"}
                                <button
                                    type="button"
                                    role="menuitem"
                                    class="dropdown-item"
                                    class:loading={busy === "Marked unpaid"}
                                    disabled={busy === "Marked unpaid"}
                                    onclick={() => {
                                        closeMore();
                                        markUnpaid();
                                    }}
                                >
                                    <i class="ri-money-dollar-circle-line" aria-hidden="true"></i>
                                    <span class="txt">Mark unpaid</span>
                                </button>
                            {/if}
                            {#if mayWrite && order.payment_status === "failed"}
                                <button
                                    type="button"
                                    role="menuitem"
                                    class="dropdown-item"
                                    class:loading={busy === "Failure cleared"}
                                    disabled={busy === "Failure cleared"}
                                    onclick={() => {
                                        closeMore();
                                        clearFailure();
                                    }}
                                >
                                    <i class="ri-arrow-go-back-line" aria-hidden="true"></i>
                                    <span class="txt">Clear failure</span>
                                </button>
                            {/if}
                            {#if mayWrite && order.status === "delivered"}
                                <button
                                    type="button"
                                    role="menuitem"
                                    class="dropdown-item"
                                    class:loading={busy === "Delivery undone"}
                                    disabled={busy === "Delivery undone"}
                                    onclick={() => {
                                        closeMore();
                                        undeliver();
                                    }}
                                >
                                    <i class="ri-arrow-go-back-line" aria-hidden="true"></i>
                                    <span class="txt">Not delivered</span>
                                </button>
                            {/if}
                            <!-- Absent rather than there and refused, the rule
                                 `editable` already states: a partly shipped
                                 order is a return. -->
                            {#if mayWrite && order.status !== "cancelled" && order.status !== "partial" && order.status !== "shipped" && order.status !== "delivered"}
                                <button
                                    type="button"
                                    role="menuitem"
                                    class="dropdown-item txt-danger"
                                    onclick={() => {
                                        closeMore();
                                        cancelOpen = true;
                                    }}
                                >
                                    <i class="ri-close-circle-line" aria-hidden="true"></i>
                                    <span class="txt">Cancel order</span>
                                </button>
                            {/if}
                        </div>
                    {/if}
                </div>
            </header>

            {#if loading && !order}
                <div class="block txt-center p-base"><span class="loader lg"></span></div>
            {:else if order}
                <!--
                    Two states and a method. The first two are what the order
                    *is*; the third is how it was paid for, which is not a state
                    and had been wearing the same chip as one.
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
                                <!-- The line editor is PUT /orders/{id}/lines,
                                     which is orders.write like the rest of the
                                     order's own state. -->
                                {#if mayWrite && editable && !draftLines}
                                    <button
                                        type="button"
                                        class="btn sm transparent secondary"
                                        onclick={startEdit}
                                    >
                                        <span class="txt">Edit</span>
                                    </button>
                                {/if}
                            </div>
                            <table class="table">
                                <thead>
                                    <tr>
                                        <!-- The card is titled Items; a column head
                                             saying Item under it is the same word
                                             twice. The figures still need naming. -->
                                        <th></th>
                                        <th class="txt-right">Qty</th>
                                        <th class="txt-right">Unit</th>
                                        <!-- Per line, because the summary's single
                                             figure cannot say which line carried
                                             which rate — and a mixed-rate order is
                                             exactly when somebody asks. -->
                                        {#if !draftLines && taxed}
                                            <th class="txt-right">Tax</th>
                                        {/if}
                                        <th class="txt-right">Total</th>
                                        <!-- Head and cell share one predicate, so the
                                             table cannot go ragged. -->
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
                                                        {line.sku}{line.variant_label
                                                            ? " · " + line.variant_label
                                                            : ""}
                                                    </div>
                                                </td>
                                                <td class="txt-right">
                                                    <!-- A number box rather than plus
                                                         and minus: an operator
                                                         amending an order usually
                                                         knows the figure. -->
                                                    <input
                                                        type="number"
                                                        class="order-qty"
                                                        min="1"
                                                        aria-label="Quantity of {line.sku}"
                                                        bind:value={line.quantity}
                                                    />
                                                </td>
                                                <td class="txt-right">
                                                    {formatMoney(line.unit_price)}
                                                </td>
                                                <td class="txt-right">
                                                    {formatMinor(
                                                        line.unit_price.amount_minor * line.quantity,
                                                    )}
                                                </td>
                                                <td class="txt-right min-width">
                                                    <button
                                                        type="button"
                                                        class="btn circle sm transparent secondary"
                                                        aria-label="Remove {line.sku}"
                                                        title="Remove this line"
                                                        onclick={() => removeDraftLine(i)}
                                                    >
                                                        <i class="ri-close-line" aria-hidden="true"
                                                        ></i>
                                                    </button>
                                                </td>
                                            </tr>
                                        {/each}
                                    {:else}
                                        {#each order.line_items || [] as line (line.id)}
                                            <tr>
                                                <td>
                                                    <!-- The picture is how somebody
                                                         recognises the thing in the
                                                         box. -->
                                                    <div class="order-item">
                                                        <div class="order-thumb">
                                                            {#if line.image_url}
                                                                <img
                                                                    src={line.image_url}
                                                                    alt=""
                                                                    loading="lazy"
                                                                />
                                                            {:else}
                                                                <i
                                                                    class="ri-image-line"
                                                                    aria-hidden="true"
                                                                ></i>
                                                            {/if}
                                                        </div>
                                                        <div class="order-item-text">
                                                            <!--
                                                                Through to the product, when there
                                                                still is one. `product_id` is null
                                                                for a line whose product has since
                                                                been deleted — the rest of the row
                                                                is a snapshot and outlives it — so
                                                                the title falls back to plain text
                                                                and says why, which is what the
                                                                best-sellers table on Reports
                                                                already does.

                                                                Only in the read view. The draft
                                                                rows above are the same titles
                                                                while the items are being edited,
                                                                and a link there would leave the
                                                                page with the edit unsaved.
                                                            -->
                                                            {#if line.product_id}
                                                                <a
                                                                    href="{base}/products/{line.product_id}"
                                                                    class="order-item-link"
                                                                >
                                                                    {line.title}
                                                                </a>
                                                            {:else}
                                                                <div>{line.title}</div>
                                                            {/if}
                                                            <div class="txt-hint txt-sm txt-code">
                                                                {line.sku}{line.variant_label
                                                                    ? " · " + line.variant_label
                                                                    : ""}
                                                            </div>
                                                            {#if !line.product_id}
                                                                <div class="txt-hint txt-sm">
                                                                    product deleted
                                                                </div>
                                                            {/if}
                                                            <!-- Which shelf the units came
                                                                 off, and therefore where a
                                                                 cancellation puts them
                                                                 back. -->
                                                            {#if line.location_id != null}
                                                                <div class="txt-hint txt-sm">
                                                                    <i
                                                                        class="ri-map-pin-line"
                                                                        aria-hidden="true"
                                                                    ></i>
                                                                    {locationName(line.location_id)}
                                                                </div>
                                                            {/if}
                                                        </div>
                                                    </div>
                                                </td>
                                                <td class="txt-right">{line.quantity}</td>
                                                <td class="txt-right">
                                                    {formatMoney(line.unit_price)}
                                                </td>
                                                {#if taxed}
                                                    <td class="txt-right">
                                                        {#if line.tax?.amount_minor}
                                                            {formatMinor(line.tax.amount_minor)}
                                                            <div class="txt-hint txt-sm">
                                                                {line.tax.name ||
                                                                    "Tax"} · {ratePercent(
                                                                    line.tax.rate_bp,
                                                                )}%
                                                            </div>
                                                        {:else}
                                                            <span class="txt-hint">—</span>
                                                        {/if}
                                                    </td>
                                                {/if}
                                                <td class="txt-right">{formatMoney(line.total)}</td>
                                                <!-- Settled lines step back to hint,
                                                     so the dark figures are the ones
                                                     still owing something. -->
                                                {#if order.fulfillments?.length}
                                                    <td
                                                        class="txt-right"
                                                        class:txt-hint={(line.shipped_quantity ??
                                                            0) >= line.quantity}
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
                                        <VariantPicker
                                            id="add-line"
                                            bind:value={addVariant}
                                            onchange={(row) => (addPicked = row)}
                                        />
                                    </div>
                                    <div class="delimiter"></div>
                                    <div class="field addon">
                                        <button
                                            type="button"
                                            class="btn sm secondary"
                                            disabled={!addPicked}
                                            onclick={addDraftLine}
                                        >
                                            <i class="ri-add-line" aria-hidden="true"></i>
                                            <span class="txt">Add</span>
                                        </button>
                                    </div>
                                </div>
                                <div class="field-help">
                                    A line added here is priced as the variant is priced today.
                                    Stock moves with the change: this order has {order.status ===
                                    "pending"
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
                                        <span
                                            class="txt-money"
                                            class:txt-danger={draftBalance < 0}
                                        >
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
                                        An order cannot be emptied. Cancel it instead — that
                                        releases its stock and says so on the order.
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
                                            <!-- The chip is for a carrier. `manual` is
                                                 how the shipment was booked, which is
                                                 not who has the parcel. -->
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
                                            <!-- Who booked it, and what state the
                                                 booking is in. Both are served on
                                                 every shipment and neither was drawn,
                                                 so a parcel a carrier module had
                                                 cancelled looked exactly like one on
                                                 its way. -->
                                            {#if f.provider}
                                                <span class="txt-hint txt-sm">
                                                    via {providerName(f.provider)}
                                                </span>
                                            {/if}
                                            {#if f.status && f.status !== "shipped"}
                                                <span class="label">{f.status}</span>
                                            {/if}
                                            <div class="flex-fill"></div>
                                            <span class="txt-hint txt-sm">
                                                {formatDate(f.created_at)}
                                            </span>
                                            <!-- The label a carrier returned. It is on
                                                 every read and the panel reached it
                                                 nowhere, so a store using a provider
                                                 that prints one had to go to the
                                                 carrier's own site for it. An absolute
                                                 URL at the carrier, so a plain link
                                                 rather than a fetch with our token. -->
                                            {#if f.label_url}
                                                <a
                                                    class="btn circle sm transparent secondary"
                                                    href={f.label_url}
                                                    target="_blank"
                                                    rel="noreferrer"
                                                    title="Open the shipping label"
                                                    aria-label="Open the shipping label"
                                                >
                                                    <i class="ri-printer-line" aria-hidden="true"></i>
                                                </a>
                                            {/if}
                                            <!-- Amending and removing a parcel are
                                                 the same right as booking one. -->
                                            {#if mayFulfill}
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
                                                    <i
                                                        class="ri-delete-bin-7-line"
                                                        aria-hidden="true"
                                                    ></i>
                                                </button>
                                            {/if}
                                            <!-- Two parcels on one order is exactly when
                                                 a customer rings to ask which one has
                                                 the mug. -->
                                            {#if f.lines?.length}
                                                <div class="ship-contents txt-hint txt-sm">
                                                    {f.lines
                                                        .map((l) => `${l.quantity} × ${l.title}`)
                                                        .join(", ")}
                                                </div>
                                            {/if}
                                        </div>
                                    {/each}
                                </div>
                            </section>
                        {/if}

                        <!-- No handler, no button: recording and withdrawing a
                             return are orders.write, and the card still shows
                             what came back to anyone who may read the order. -->
                        <OrderReturnsCard
                            {order}
                            onwithdraw={mayWrite ? askWithdrawReturn : undefined}
                        />

                        <!-- The coercion is belt-and-braces even with the engine's
                             own validation: orders.metadata is jsonb, rows written
                             before the reserved key existed are unconstrained, and
                             a module can still put an object there. -->
                        <OrderNoteCard
                            note={typeof order.metadata?.notes === "string"
                                ? order.metadata.notes
                                : ""}
                            editing={editingCard === "notes"}
                            onedit={mayWrite ? startEditNotes : undefined}
                            onsave={saveCard}
                            {cardActions}
                        />

                        {#if extraMetadata.length}
                            <!-- Read-only, and deliberately: these keys belong to
                                 whatever module wrote them, and the patch replaces
                                 metadata whole — an editor here would be a way to
                                 delete a module's state by accident. -->
                            <section class="order-card">
                                <h6 class="order-card-title">Metadata</h6>
                                <div class="order-lines">
                                    {#each extraMetadata as [key, value] (key)}
                                        <div class="order-line">
                                            <span class="txt-hint txt-code">{key}</span>
                                            <span class="txt-ellipsis">{metaValue(value)}</span>
                                        </div>
                                    {/each}
                                </div>
                                <div class="field-help">
                                    Written by a module or a script. Only the note above is the
                                    shop's own, and only it can be edited here.
                                </div>
                            </section>
                        {/if}

                        <OrderTimeline
                            entries={timeline}
                            loading={timelineLoading}
                            currency={order.currency}
                        />
                    </div>

                    <!--
                        The rail. What it holds is what somebody opening an order
                        wants first — how much, whether it has been paid, and who
                        it is for.
                    -->
                    <aside class="order-rail">
                        <!-- While an edit is on screen these are the figures the
                             order still has, not the ones being decided. -->
                        <section class="order-card" class:is-superseded={!!draftLines}>
                            <h6 class="order-card-title">
                                Summary
                                {#if draftLines}<span class="txt-hint">· as saved</span>{/if}
                            </h6>
                            <!-- The total leads. It is the number an order is
                                 opened to see. -->
                            <div class="order-total {totalState}">{formatMoney(order.total)}</div>

                            <div class="order-lines">
                                <div class="order-line">
                                    <span class="txt-hint">Subtotal</span>
                                    <span class="txt-money">{formatMoney(order.subtotal)}</span>
                                </div>
                                <!-- Every discount, not just the first. An order
                                     given two promotions showed one of them and
                                     the total did not add up on screen. -->
                                {#each order.discounts ?? [] as d (d.discount_id)}
                                    <div class="order-line">
                                        <span class="txt-hint">{d.code || d.title || "Discount"}</span>
                                        <span class="txt-money">
                                            {d.free_shipping && !d.amount_minor
                                                ? "Free shipping"
                                                : "−" +
                                                  formatMinor(d.amount_minor)}
                                        </span>
                                    </div>
                                {:else}
                                    {#if order.discount?.amount_minor}
                                        <div class="order-line">
                                            <span class="txt-hint">Discount</span>
                                            <span class="txt-money">−{formatMoney(order.discount)}</span>
                                        </div>
                                    {/if}
                                {/each}
                                {#if order.tax?.amount_minor}
                                    <div class="order-line">
                                        <!-- Inclusive prices already contain it, so
                                             the line says so rather than looking
                                             like an addition that never happened. -->
                                        <span class="txt-hint">
                                            {taxName}{order.tax_inclusive ? " (included)" : ""}
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
                                <!-- Under the total rather than changing it: the
                                     total is what was agreed, and the refund is
                                     what came back off it. -->
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
                            Payment as its own card. How an order was settled and
                            whether it has been are one thought.
                        -->
                        <section class="order-card">
                            <div class="order-card-head">
                                <h6 class="order-card-title">Payment</h6>
                                {#if mayWrite && editingCard !== "payment" && patchable}
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
                                <form
                                    id="payment-form"
                                    onsubmit={(e) => (e.preventDefault(), saveCard())}
                                >
                                    <div class="field">
                                        <label for="pay-method">Method</label>
                                        <Select
                                            id="pay-method"
                                            bind:value={form.payment_provider}
                                            options={methods.map((m) => ({
                                                value: m.code,
                                                label: m.name || m.code,
                                            }))}
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
                                        What the money is reconciled against. Changing the method
                                        moves none of it — it records how this order was actually
                                        settled.
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
                                    <div class="order-reference txt-code">
                                        {order.payment_reference}
                                    </div>
                                {/if}
                                <OrderRefundList {order} />
                                <!-- In the Payment card because an invoice is the
                                     document for money already taken. Gated on
                                     paid because ext/invoices issues on
                                     order.paid. -->
                                {#if hasModule("invoices") && order.payment_status === "paid"}
                                    <button
                                        type="button"
                                        class="btn sm secondary m-t-sm"
                                        onclick={() =>
                                            openDocument(
                                                `/api/admin/x/invoices/${order.id}`,
                                                safeFilename(`invoice-${order.number}`, "html"),
                                            )}
                                    >
                                        <i class="ri-printer-line" aria-hidden="true"></i>
                                        <span class="txt">Invoice</span>
                                    </button>
                                {/if}
                            {/if}
                        </section>

                        <section class="order-card">
                            <div class="order-card-head">
                                <h6 class="order-card-title">Customer</h6>
                                <!-- Hidden on a cancelled order rather than shown
                                     and refused: the engine rejects every patch
                                     but a note once an order is cancelled. -->
                                {#if mayWrite && editingCard !== "customer" && patchable}
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
                                <form
                                    id="customer-form"
                                    onsubmit={(e) => (e.preventDefault(), saveCard())}
                                >
                                    <div class="field">
                                        <label for="cust-name">Name</label>
                                        <input id="cust-name" type="text" bind:value={form.name} />
                                    </div>
                                    <div class="field m-t-5">
                                        <label for="cust-email">Email</label>
                                        <input
                                            id="cust-email"
                                            type="email"
                                            bind:value={form.email}
                                        />
                                    </div>
                                    <div class="field m-t-5">
                                        <label for="cust-phone">Phone</label>
                                        <input id="cust-phone" type="tel" bind:value={form.phone} />
                                    </div>
                                    <div class="field m-t-5">
                                        <label for="cust-line1">Address</label>
                                        <input
                                            id="cust-line1"
                                            type="text"
                                            bind:value={form.address.line1}
                                        />
                                    </div>
                                    <div class="field m-t-5">
                                        <label for="cust-line2">Line 2</label>
                                        <input
                                            id="cust-line2"
                                            type="text"
                                            bind:value={form.address.line2}
                                        />
                                    </div>
                                    <div class="fields m-t-5">
                                        <div class="field">
                                            <label for="cust-city">City</label>
                                            <input
                                                id="cust-city"
                                                type="text"
                                                bind:value={form.address.city}
                                            />
                                        </div>
                                        <div class="delimiter"></div>
                                        <div class="field">
                                            <label for="cust-state">State</label>
                                            <input
                                                id="cust-state"
                                                type="text"
                                                bind:value={form.address.state}
                                            />
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
                                    <!-- The address is sent only when it has
                                         actually been touched, because the engine
                                         validates any address it is given — so on
                                         an order with a sparse one (a historical
                                         import) this is the difference between
                                         correcting a phone number and not being
                                         able to. -->
                                    <div class="field-help">
                                        {#if addressChanged()}
                                            The address is being changed, so the engine will
                                            check it: line 1, city, postcode and country are all
                                            required together.
                                        {:else}
                                            Only what you change is sent. Leave the address alone
                                            and it is left exactly as it is, however sparse.
                                        {/if}
                                    </div>
                                    {#if order.status === "partial"}
                                        <div class="field-help">
                                            Part of this order has already gone out. Correcting the
                                            address fixes the record and the parcels still to come;
                                            it does not move the one that left.
                                        </div>
                                    {:else if order.status === "shipped" || order.status === "delivered"}
                                        <div class="field-help">
                                            This order has already gone out. Correcting the address
                                            fixes the record; it does not move the parcel.
                                        </div>
                                    {/if}
                                    {@render cardActions("customer-form")}
                                </form>
                            {:else}
                                <div class="order-customer-name">{order.name || "—"}</div>
                                <!-- Links, not text. Chasing an order means writing
                                     to somebody or ringing them. -->
                                {#if order.email}
                                    <a class="order-contact" href="mailto:{order.email}">
                                        {order.email}
                                    </a>
                                {/if}
                                {#if order.phone}
                                    <a class="order-contact" href="tel:{order.phone}">
                                        {order.phone}
                                    </a>
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
                                <!-- Which language this customer is written to in.
                                     It decides what every notification about this
                                     order said, so it belongs beside the address
                                     rather than nowhere. -->
                                {#if order.language}
                                    <div class="txt-hint txt-sm m-t-5">
                                        Written to in {order.language}
                                    </div>
                                {/if}
                                <!-- In the Customer card because the access
                                     token is the customer's, not the shop's: it
                                     is how they read this order back, and the
                                     call it answers is "I have lost my link".
                                     Behind orders.write, which is what the route
                                     asks for — reading an order is every role's,
                                     handing out the key to it is not. -->
                                {#if mayWrite}
                                    <AccessTokenCard {order} />
                                {/if}
                            {/if}
                        </section>
                    </aside>
                </div>

                <footer class="page-footer">
                    <span class="txt txt-hint">Order #{order.id}</span>
                    {#if order.updated_at && order.updated_at !== order.created_at}
                        <span class="txt txt-hint">
                            · last changed {formatDate(order.updated_at)}
                        </span>
                    {/if}
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>
{/if}

<ShipDialog
    open={trackingOpen}
    {order}
    busy={busy === "Shipped"}
    {carrierChoices}
    {carrierOptions}
    {providerChoices}
    bind:tracking
    bind:carrier={shipCarrier}
    bind:provider={shipProvider}
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

<PromptDialog
    open={markPaidOpen}
    title="Mark this order paid"
    label="Payment reference"
    placeholder="Cheque number, UTR, receipt number…"
    help="How the money arrived, so it can be reconciled later. Leaving it empty records the payment and touches no reference the order already has — the engine only writes this field when it is not blank."
    confirmLabel="Mark paid"
    busy={busy === "Marked paid"}
    onclose={() => (markPaidOpen = false)}
    onconfirm={markPaid}
/>

<PromptDialog
    open={cancelOpen}
    title="Cancel this order?"
    message={order?.status === "pending"
        ? "Its inventory reservation is released and the stock goes back on sale."
        : "The stock it took is returned to the shelf."}
    label="Reason"
    placeholder="Fraud, customer request, out of stock, duplicate…"
    help="Stored on the order's cancellation record, and the only durable explanation of why this sale was closed."
    confirmLabel="Cancel order"
    danger
    busy={busy === "Order cancelled"}
    onclose={() => (cancelOpen = false)}
    onconfirm={cancelOrder}
/>

<!--
    Correcting a tracking number after the fact. The parcel left either way, so
    this changes what was recorded about the shipment and nothing about the
    order — which is why it is its own popup rather than a second Ship form.
-->
<Drawer open={!!editing} size="popup sm" title="Tracking number" onclose={() => (editing = null)}>
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
                Several carriers issue numbers of that shape. Pick the right one if the first
                guess is wrong.
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

<Confirm
    bind:open={confirmOpen}
    title={confirmConfig.title}
    message={confirmConfig.message}
    confirmLabel={confirmConfig.confirmLabel}
    danger={confirmConfig.danger}
    onconfirm={() => confirmConfig.run?.()}
/>
