<script>
    /**
     * Your basket, and checking it out.
     *
     * The basket is a core cart the buyer's account has claimed (D66), so its
     * lines are at the company's prices and are changed through the public
     * cart routes by its token. Ticking lines checks out only those
     * (`line_ids`, D71): the rest stay here for another day.
     *
     * Placing answers one of two ways, and the screen must never let them
     * be confused. 201 is an order — it exists, stock is held, it is on the
     * company's books. 202 is a request for approval — nothing is ordered,
     * the basket is untouched, and an approver decides. They get different
     * colours, icons, headings and next steps.
     */
    import { onMount } from "svelte";
    import { afterNavigate } from "$app/navigation";
    import { base } from "$app/paths";
    import { formatMoney } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import {
        trade,
        tradeApi,
        loadBasket,
        loadMe,
        noteBasket,
        rememberBasket,
        refreshBadges,
        explain,
        termsWords,
    } from "$lib/trade.svelte.js";
    import PortalPage from "../PortalPage.svelte";
    import CheckoutDetails from "../CheckoutDetails.svelte";

    let cart = $state(null);
    let loading = $state(true);
    let failure = $state("");
    let picked = $state(new Set());
    let busyLine = $state(0);
    let placing = $state(false);
    let placeError = $state("");
    let outcome = $state(null);
    let details = $state({ po: "", addressId: null, address: null, name: "", phone: "", method: "", rateId: null, ready: false });
    /* One key per attempt to place, kept across a retry after a network
       failure so a request that did arrive is answered, not placed twice. */
    let attemptKey = "";

    const company = $derived(trade.me.company);
    const lines = $derived(cart?.line_items ?? []);
    const chosen = $derived(lines.filter((l) => picked.has(l.id)));
    const everything = $derived(lines.length > 0 && chosen.length === lines.length);
    const currency = $derived(cart?.currency ?? company.credit_limit?.currency);
    const chosenMinor = $derived(chosen.reduce((sum, l) => sum + (l.total?.amount_minor ?? 0), 0));
    const overThreshold = $derived(
        trade.me.role === "buyer" &&
            !!company.approval_threshold &&
            chosenMinor > company.approval_threshold.amount_minor,
    );
    const closed = $derived(company.status === "closed");

    async function load() {
        loading = true;
        failure = "";
        try {
            const fresh = await loadBasket();
            const known = new Set((cart?.line_items ?? []).map((l) => l.id));
            cart = fresh;
            // New lines arrive ticked; lines somebody unticked stay unticked.
            const next = new Set();
            for (const l of fresh?.line_items ?? []) {
                if (!known.has(l.id) || picked.has(l.id)) next.add(l.id);
            }
            picked = next;
        } catch (err) {
            if (!err.handled) failure = explain(err);
        } finally {
            loading = false;
        }
    }

    onMount(load);

    /* "Your basket" in the navigation means the basket, even from the answer
       to the last checkout: a link to the page already open does not remount
       it, so the answer is put away here. */
    afterNavigate((navigation) => {
        if (navigation.type === "link" && outcome && navigation.from?.url.pathname === navigation.to?.url.pathname) {
            another();
            load();
        }
    });

    function toggle(id) {
        const next = new Set(picked);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        picked = next;
    }

    function toggleAll() {
        picked = everything ? new Set() : new Set(lines.map((l) => l.id));
    }

    async function setQuantity(line, value) {
        const qty = Number(value);
        if (!Number.isInteger(qty) || qty < 1 || qty === line.quantity) return;
        busyLine = line.id;
        try {
            const updated = await tradeApi.patch(
                `/api/carts/${encodeURIComponent(cart.id)}/line-items/${line.id}`,
                { quantity: qty },
            );
            cart = updated;
            noteBasket(updated);
        } catch (err) {
            if (!err.handled) toast.error(explain(err));
            await load();
        } finally {
            busyLine = 0;
        }
    }

    async function remove(line) {
        busyLine = line.id;
        try {
            cart = await tradeApi.delete(`/api/carts/${encodeURIComponent(cart.id)}/line-items/${line.id}`);
            noteBasket(cart);
            const next = new Set(picked);
            next.delete(line.id);
            picked = next;
            toast.info(`Removed ${line.title}.`);
        } catch (err) {
            if (!err.handled) toast.error(explain(err));
        } finally {
            busyLine = 0;
        }
    }

    async function place(event) {
        event?.preventDefault();
        if (placing || !chosen.length || !details.ready) return;
        placing = true;
        placeError = "";
        attemptKey ||= crypto.randomUUID();
        const body = {
            cart_id: cart.id,
            payment_method: details.method,
            po_number: details.po.trim(),
            name: details.name,
            phone: details.phone,
            address: details.address,
        };
        if (!everything) body.line_ids = chosen.map((l) => l.id);
        if (everything && details.rateId) body.shipping_rate_id = details.rateId;
        try {
            const result = await tradeApi.post("/x/b2b/checkout", body, { "Idempotency-Key": attemptKey });
            attemptKey = "";
            if (result.approval) {
                outcome = { kind: "approval", approval: result.approval };
                refreshBadges();
            } else {
                outcome = { kind: "order", order: result.order, payment: result.payment, partial: !everything };
                if (everything) rememberBasket("");
                // A PO number is one order's; the next order needs its own.
                details.po = "";
                loadMe();
            }
            window.scrollTo({ top: 0, behavior: matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
            await load();
        } catch (err) {
            // A refusal is final for this attempt; only a request that may not
            // have arrived keeps its key for the retry.
            if (err.status !== 0) attemptKey = "";
            if (!err.handled) placeError = explain(err);
            if (err.code === "credit_limit_exceeded") loadMe();
        } finally {
            placing = false;
        }
    }

    /* What is left was left on purpose for another order, and this is that
       order: every remaining line is ticked again. */
    function another() {
        outcome = null;
        placeError = "";
        picked = new Set(lines.map((l) => l.id));
    }
</script>

<PortalPage title="Your basket">
    {#snippet actions()}
        <a href="{base}/portal/quick-order" class="btn secondary portal-press">
            <i class="ri-flashlight-line" aria-hidden="true"></i>
            <span class="txt">Quick order</span>
        </a>
    {/snippet}

    {#if outcome?.kind === "order"}
        <section class="portal-outcome portal-outcome-order" role="status" aria-live="polite">
            <span class="portal-outcome-icon" aria-hidden="true"><i class="ri-checkbox-circle-fill"></i></span>
            <div class="portal-outcome-body">
                <h2 class="portal-outcome-title">Order {outcome.order.number} is placed</h2>
                <p>
                    {formatMoney(outcome.order.total)}{outcome.order.metadata?.b2b?.po_number
                        ? ` · PO ${outcome.order.metadata.b2b.po_number}`
                        : ""}.
                    {#if outcome.order.payment_provider === "on_account"}
                        It's on {company.name}'s account — {termsWords(company).toLowerCase()}.
                    {:else if outcome.payment?.kind === "redirect" && outcome.payment.client_data?.url}
                        Pay for it to finish.
                    {:else}
                        We'll be in touch about payment.
                    {/if}
                    {#if outcome.partial && lines.length}
                        The lines you didn't tick are still in your basket below.
                    {/if}
                </p>
                <div class="portal-row-actions">
                    {#if outcome.payment?.kind === "redirect" && outcome.payment.client_data?.url}
                        <a class="btn portal-press" href={outcome.payment.client_data.url}>
                            <span class="txt">Continue to payment</span>
                        </a>
                    {/if}
                    <a class="btn secondary portal-press" href="{base}/portal/orders/{outcome.order.id}">
                        <span class="txt">See the order</span>
                    </a>
                    {#if lines.length}
                        <button type="button" class="btn transparent secondary portal-press" onclick={another}>
                            <span class="txt">Check out more</span>
                        </button>
                    {/if}
                </div>
            </div>
        </section>
    {:else if outcome?.kind === "approval"}
        <section class="portal-outcome portal-outcome-approval" role="status" aria-live="polite">
            <span class="portal-outcome-icon" aria-hidden="true"><i class="ri-hourglass-2-fill"></i></span>
            <div class="portal-outcome-body">
                <h2 class="portal-outcome-title">Sent for approval — not ordered yet</h2>
                <p>
                    At {formatMoney(outcome.approval.total)}, this is over {company.name}'s approval limit of
                    {formatMoney(company.approval_threshold)}. We've asked your approvers to decide; you'll get an
                    email when they do. <strong>Nothing has been ordered</strong> and your basket hasn't changed.
                </p>
                <div class="portal-row-actions">
                    <a class="btn secondary portal-press" href="{base}/portal/approvals/{outcome.approval.id}">
                        <span class="txt">See the request</span>
                    </a>
                    <button type="button" class="btn transparent secondary portal-press" onclick={another}>
                        <span class="txt">Back to the basket</span>
                    </button>
                </div>
            </div>
        </section>
    {/if}

    {#if failure}
        <div class="alert danger m-b-base portal-notice" role="alert">
            <p>{failure}</p>
            <button type="button" class="btn sm secondary portal-press" onclick={load}><span class="txt">Try again</span></button>
        </div>
    {/if}

    {#if loading && !cart}
        <div class="portal-list tw:rounded-xl tw:border tw:bg-card">
            {#each Array(3) as _, i (i)}<div class="portal-list-row"><span class="skeleton-loader"></span></div>{/each}
        </div>
    {:else if !lines.length}
        {#if !outcome}
            <div class="portal-empty-state tw:rounded-xl tw:border tw:bg-card">
                <i class="ri-shopping-basket-2-line" aria-hidden="true"></i>
                <h2>Your basket is empty</h2>
                <p class="txt-hint">Add lines by product code, or repeat a past order.</p>
                <div class="portal-row-actions portal-center">
                    <a href="{base}/portal/quick-order" class="btn portal-press"><span class="txt">Quick order</span></a>
                    <a href="{base}/portal/orders" class="btn secondary portal-press"><span class="txt">Past orders</span></a>
                </div>
            </div>
        {/if}
    {:else if !outcome}
        <div class="portal-basket-grid">
            <section aria-labelledby="lines-heading">
                <div class="b2b-section-head">
                    <h2 class="section-title" id="lines-heading">
                        <i class="ri-list-check-3" aria-hidden="true"></i>
                        {lines.length} {lines.length === 1 ? "line" : "lines"}
                    </h2>
                    <label class="portal-check-all">
                        <input type="checkbox" checked={everything} indeterminate={chosen.length > 0 && !everything} onchange={toggleAll} />
                        <span>Check out every line</span>
                    </label>
                </div>
                <ul class="portal-lines tw:rounded-xl tw:border tw:bg-card" class:faded={loading}>
                    {#each lines as line (line.id)}
                        <li class="portal-line" class:unpicked={!picked.has(line.id)} class:busy={busyLine === line.id}>
                            <label class="portal-line-pick">
                                <input
                                    type="checkbox"
                                    checked={picked.has(line.id)}
                                    onchange={() => toggle(line.id)}
                                    aria-label="Check out {line.title}"
                                />
                            </label>
                            <div class="portal-line-what">
                                <span class="txt-bold">{line.title}</span>
                                <span class="txt-hint txt-sm">
                                    <span class="txt-code">{line.sku}</span>{line.variant_label ? ` · ${line.variant_label}` : ""}
                                    · {formatMoney(line.unit_price)} each
                                </span>
                                {#if !line.in_stock}
                                    <span class="txt-danger txt-sm">
                                        {line.available > 0 ? `Only ${line.available} in stock` : "Not available at the moment"}
                                    </span>
                                {/if}
                            </div>
                            <div class="field portal-line-qty">
                                <input
                                    type="number"
                                    min="1"
                                    step="1"
                                    inputmode="numeric"
                                    value={line.quantity}
                                    disabled={busyLine === line.id}
                                    aria-label="Quantity of {line.title}"
                                    onchange={(e) => setQuantity(line, e.currentTarget.value)}
                                />
                            </div>
                            <span class="portal-line-total txt-bold">{formatMoney(line.total)}</span>
                            <button
                                type="button"
                                class="btn circle sm transparent secondary portal-remove"
                                aria-label="Remove {line.title}"
                                title="Remove"
                                disabled={busyLine === line.id}
                                onclick={() => remove(line)}
                            >
                                <i class="ri-delete-bin-line" aria-hidden="true"></i>
                            </button>
                        </li>
                    {/each}
                </ul>
                <div class="b2b-total m-t-sm" aria-live="polite">
                    <span class="txt-hint">{everything ? "Basket" : `${chosen.length} of ${lines.length} lines`}</span>
                    <strong class="b2b-total-value">{formatMoney({ amount_minor: chosenMinor, currency })}</strong>
                </div>
                <p class="txt-hint txt-sm portal-right">Before delivery and tax, which are worked out when you place it.</p>
            </section>

            <section class="portal-checkout tw:rounded-xl tw:border tw:bg-card" aria-labelledby="checkout-heading">
                <h2 class="portal-card-title" id="checkout-heading">Check out</h2>
                {#if closed}
                    <p class="txt-hint">{company.name}'s account is closed, so it can't order.</p>
                {:else}
                    <form onsubmit={place} novalidate>
                        <CheckoutDetails bind:details cartToken={cart.id} quoteDelivery={everything} idPrefix="basket" />
                        {#if !everything && chosen.length}
                            <p class="txt-hint txt-sm">Delivery for part of a basket is chosen by the store when it's placed.</p>
                        {/if}
                        {#if overThreshold}
                            <div class="alert warning portal-notice m-b-sm" role="status">
                                <p>
                                    This is over your approval limit of {formatMoney(company.approval_threshold)}, so it
                                    will go to an approver instead of being ordered.
                                </p>
                            </div>
                        {/if}
                        {#key placeError}
                            {#if placeError}
                                <div class="alert danger portal-refusal m-b-sm" role="alert"><p>{placeError}</p></div>
                            {/if}
                        {/key}
                        <button
                            type="submit"
                            class="btn lg block portal-press"
                            class:warning={overThreshold}
                            class:loading={placing}
                            disabled={placing || !chosen.length || !details.ready}
                        >
                            <i class={overThreshold ? "ri-send-plane-line" : "ri-check-line"} aria-hidden="true"></i>
                            <span class="txt">
                                {overThreshold ? "Send for approval" : everything ? "Place order" : `Place order for ${chosen.length} ${chosen.length === 1 ? "line" : "lines"}`}
                            </span>
                        </button>
                        {#if !details.ready && chosen.length}
                            <p class="txt-hint txt-sm m-t-xs m-b-0">
                                {!details.address
                                    ? "Choose where it goes."
                                    : company.require_po && !details.po?.trim()
                                      ? "Add your PO number."
                                      : "Choose how to pay."}
                            </p>
                        {/if}
                    </form>
                {/if}
            </section>
        </div>
    {/if}
</PortalPage>
