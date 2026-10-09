<script>
    /**
     * One abandoned checkout, at its own address.
     *
     * A page rather than a drawer for the reason the order screen gives: a
     * record somebody is chasing gets sent to a colleague, opened beside
     * another, reloaded after a phone call. Everything on it is one request —
     * `GET …/abandonments/{id}` — because the module assembles the whole
     * reading: the basket as it was beside the basket as it is, what the store
     * knows about the address from its orders, the timeline already worded,
     * and which actions are possible right now. The screen draws that and
     * decides nothing; in particular it never rebuilds a timeline sentence.
     *
     * The record is re-read every thirty seconds while the tab is visible, so
     * a page left open while the shopper buys says so on its own. Nothing
     * depends on that: every action is re-checked by the module when it lands,
     * and a refusal (409) is shown with the module's own sentence and the
     * record re-read on the spot.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { page as route } from "$app/state";
    import { can, recovery } from "$lib/api.js";
    import { formatDate, formatMoney, pluralize, relativeTime } from "$lib/format.js";
    import {
        actorWords,
        indicatorLabel,
        indicatorTone,
        isOpen,
        kindLabel,
        statusClass,
        statusLabel,
        timelineIcon,
        timelineTone,
    } from "$lib/recovery.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { rightLabel } from "$lib/rights.js";
    import { copyText } from "$lib/clipboard.js";
    import { rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import RecoverySendDialog from "$lib/components/RecoverySendDialog.svelte";
    import RecoverySuppressDialog from "$lib/components/RecoverySuppressDialog.svelte";

    const POLL_MS = 30_000;

    const id = $derived(route.params.id);
    const readable = $derived(can("abandonment.read"));
    /* Trusted only where the store could have been asked: the probe is made for
       an operator holding this right or orders.read (modules.svelte.js), and
       for anybody else the rights answer below is the true one. */
    const missing = $derived(
        modulesKnown() && !hasModule("cart-recovery") && (readable || can("orders.read")),
    );
    const mayContact = $derived(can("abandonment.contact"));
    const maySuppress = $derived(can("abandonment.suppress"));
    const noContactRight = `Your role does not carry “${rightLabel("abandonment.contact")}”`;
    const noSuppressRight = `Your role does not carry “${rightLabel("abandonment.suppress")}”`;

    let record = $state(null);
    let loading = $state(true);
    let sendOpen = $state(false);
    let suppressOpen = $state(false);
    let linkBusy = $state(false);
    /** Which copy button last worked, for its two seconds of "Copied". */
    let copied = $state("");
    let copiedTimer;

    /* Not $state: the poll reads it and must not re-run because of it. */
    let lastLoaded = 0;
    let reqId = 0;

    $effect(() => {
        id;
        if (readable && hasModule("cart-recovery")) load();
    });

    /**
     * quiet: a re-read nobody asked for — the poll, or the page agreeing with a
     * refusal. It keeps the record on screen while it works and does not toast
     * a failure; the next tick, or the refresh button, will try again.
     */
    async function load({ quiet = false } = {}) {
        const mine = ++reqId;
        if (!quiet) loading = true;
        try {
            const next = await recovery.get(id);
            if (mine !== reqId) return;
            record = next;
            lastLoaded = Date.now();
        } catch (err) {
            if (mine !== reqId) return;
            if (err.status === 404) {
                // A mistyped or purged id is not a screen; the list is where
                // the operator can do something, and the toast says why.
                toast.error(err);
                goto(`${base}/dash/abandoned`);
                return;
            }
            if (!quiet) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    $effect(() => {
        if (!readable) return;
        const tick = () => {
            if (document.visibilityState === "visible") load({ quiet: true });
        };
        const timer = setInterval(tick, POLL_MS);
        // Coming back to a tab that has been hidden for longer than a tick
        // re-reads at once rather than showing a stale record for up to 30s.
        const onVisible = () => {
            if (document.visibilityState === "visible" && Date.now() - lastLoaded > POLL_MS) {
                load({ quiet: true });
            }
        };
        document.addEventListener("visibilitychange", onVisible);
        return () => {
            clearInterval(timer);
            document.removeEventListener("visibilitychange", onVisible);
        };
    });

    function flashCopied(which) {
        copied = which;
        clearTimeout(copiedTimer);
        copiedTimer = setTimeout(() => (copied = ""), 2000);
    }

    async function copyLink() {
        if (!record || linkBusy) return;
        linkBusy = true;
        try {
            const { url, tracked } = await recovery.link(record.id);
            if (await copyText(url)) {
                flashCopied("link");
                if (!tracked) {
                    toast.info("Copied. Clicks on this link are not counted: the store has no public address set.");
                }
            } else {
                toast.info(`Copy the link by hand: ${url}`, 15000);
            }
            // Handing out the link is recorded on the timeline.
            load({ quiet: true });
        } catch (err) {
            toast.error(err);
            if (err.status === 409) load({ quiet: true });
        } finally {
            linkBusy = false;
        }
    }

    async function copyEmail() {
        if (!record?.email) return;
        if (await copyText(record.email)) flashCopied("email");
        else toast.info(`Copy the address by hand: ${record.email}`);
    }

    function afterSent(result) {
        record = result.abandonment;
        lastLoaded = Date.now();
        sendOpen = false;
    }

    function afterSuppressed(detail) {
        record = detail;
        lastLoaded = Date.now();
        suppressOpen = false;
    }

    const options = $derived(record?.recovery ?? null);
    const open = $derived(record ? isOpen(record.status) : false);
    const showSend = $derived(!!record?.email && open);
    const showLink = $derived(!!record && record.status !== "recovered" && record.status !== "expired");
    const showSuppress = $derived(!!options?.can_suppress);

    const sendTitle = $derived(
        !mayContact ? noContactRight : !options?.can_send ? options?.send_blocked_reason : undefined,
    );
    const linkTitle = $derived(
        !mayContact ? noContactRight : !options?.can_copy_link ? options?.link_blocked_reason : undefined,
    );

    function suppressReason(key) {
        return options?.suppress_reasons?.find((r) => r.key === key)?.label ?? key;
    }

    function stockNote(line) {
        switch (line.stock) {
            case "insufficient":
                return { text: `Requested ${line.quantity} · ${line.available} available`, cls: "warning" };
            case "out_of_stock":
                return { text: "Out of stock", cls: "danger" };
            case "unavailable":
                return { text: "No longer sold", cls: "danger" };
            default:
                return null;
        }
    }

    function cartStateClass(state) {
        if (state === "converted") return "success";
        if (state === "abandoned") return "warning";
        return "info";
    }
</script>

<svelte:head><title>{record ? `Abandoned ${kindLabel(record.kind).toLowerCase()} #${record.id}` : "Abandoned checkout"} · GoCommerce</title></svelte:head>

{#if !readable && !missing}
    <NoAccess right="abandonment.read" what="abandoned checkouts" />
{:else}
    <div class="page page-abandoned-detail shopify-skin">
        <div class="page-content full-height">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <a href="{base}/dash/abandoned">Abandoned checkouts</a>
                    <div>#{id}</div>
                </nav>

                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        disabled={loading}
                        onclick={() => load()}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

                <!-- Every button here leads with an icon: below 550px the
                     header turns each into its icon in a circle and hides the
                     label, and a button with no icon keeps its words inside a
                     round box. -->
                <div class="page-header-primary-btns">
                    {#if record}
                        {#if showSend}
                            <button
                                type="button"
                                class="btn sm"
                                disabled={!mayContact || !options?.can_send}
                                title={sendTitle}
                                onclick={() => (sendOpen = true)}
                            >
                                <i class="ri-mail-send-line" aria-hidden="true"></i>
                                <span class="txt">Send recovery</span>
                            </button>
                        {/if}
                        {#if showLink}
                            <button
                                type="button"
                                class="btn sm secondary recovery-copy"
                                class:loading={linkBusy}
                                class:is-copied={copied === "link"}
                                disabled={linkBusy || !mayContact || !options?.can_copy_link}
                                title={linkTitle}
                                aria-live="polite"
                                onclick={copyLink}
                            >
                                <i class={copied === "link" ? "ri-check-line" : "ri-link"} aria-hidden="true"></i>
                                <span class="txt">{copied === "link" ? "Copied" : "Copy link"}</span>
                            </button>
                        {/if}
                        {#if showSuppress}
                            <button
                                type="button"
                                class="btn sm secondary"
                                disabled={!maySuppress}
                                title={maySuppress ? undefined : noSuppressRight}
                                onclick={() => (suppressOpen = true)}
                            >
                                <i class="ri-forbid-line" aria-hidden="true"></i>
                                <span class="txt">Suppress</span>
                            </button>
                        {/if}
                        {#if record.recovered_order}
                            <a class="btn sm secondary" href="{base}/dash/orders/{record.recovered_order.id}">
                                <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
                                <span class="txt">Open order</span>
                            </a>
                        {/if}
                    {/if}
                </div>
            </header>

            {#if missing}
                <ModuleMissing
                    module="cart-recovery"
                    what="Abandoned checkouts are recorded by ext/cart-recovery, and this binary does not have it."
                />
            {:else if loading && !record}
                <div class="recovery-detail-skeleton" aria-busy="true">
                    <span class="skeleton-loader lg"></span>
                    <div class="order-grid m-t-sm">
                        <div class="order-main">
                            <section class="order-card"><span class="skeleton-loader"></span><span class="skeleton-loader"></span><span class="skeleton-loader"></span></section>
                            <section class="order-card"><span class="skeleton-loader"></span><span class="skeleton-loader"></span></section>
                        </div>
                        <aside class="order-rail">
                            <section class="order-card"><span class="skeleton-loader"></span><span class="skeleton-loader"></span></section>
                        </aside>
                    </div>
                </div>
            {:else if record}
                <div class="order-status recovery-status">
                    <span class="label">{kindLabel(record.kind)}</span>
                    {#key record.status}
                        <span class="label {statusClass(record.status)} recovery-status-badge">{statusLabel(record.status)}</span>
                    {/key}
                    {#if record.indicator}
                        <span class="txt-sm {indicatorTone(record.indicator)}">{indicatorLabel(record.indicator)}</span>
                    {/if}
                    <div class="flex-fill"></div>
                    <span class="txt-hint txt-sm" title={formatDate(record.abandoned_at)}>
                        Abandoned {relativeTime(record.abandoned_at)}
                    </span>
                </div>

                <div class="order-grid recovery-detail">
                    <div class="order-main">
                        <section class="order-card">
                            <h6 class="order-card-title">Products</h6>
                            <div class="recovery-lines-scroll">
                                <table class="table recovery-lines">
                                    <thead>
                                        <tr>
                                            <th>Item</th>
                                            <th class="txt-right">Qty</th>
                                            <th class="txt-right recovery-price-col">Price then</th>
                                            <th class="txt-right recovery-price-col">Price now</th>
                                            <th class="txt-right">Total</th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {#each record.lines as line (line.variant_id)}
                                            {@const note = stockNote(line)}
                                            <tr>
                                                <td>
                                                    <div class="order-item-text">
                                                        {#if line.product_id && line.stock !== "unavailable"}
                                                            <a class="order-item-link" href="{base}/dash/products/{line.product_id}">{line.title}</a>
                                                        {:else}
                                                            <span>{line.title}</span>
                                                        {/if}
                                                        <div class="txt-hint txt-sm txt-code">
                                                            {line.sku}{line.variant_label ? " · " + line.variant_label : ""}
                                                        </div>
                                                        <div class="txt-hint txt-sm recovery-line-compact">
                                                            {line.quantity} × {formatMoney(line.unit_price)}{line.current_price && line.price_changed
                                                                ? `, now ${formatMoney(line.current_price)}`
                                                                : ""}
                                                        </div>
                                                        {#if line.price_changed || note}
                                                            <div class="recovery-line-notes">
                                                                {#if line.price_changed}
                                                                    <span class="label sm warning">
                                                                        Price changed: {formatMoney(line.unit_price)} → {formatMoney(line.current_price)}
                                                                    </span>
                                                                {/if}
                                                                {#if note}
                                                                    <span class="label sm {note.cls}">{note.text}</span>
                                                                {/if}
                                                            </div>
                                                        {/if}
                                                    </div>
                                                </td>
                                                <td class="txt-right">{line.quantity}</td>
                                                <td class="txt-right txt-money recovery-price-col">{formatMoney(line.unit_price)}</td>
                                                <td class="txt-right txt-money recovery-price-col" class:txt-warning={line.price_changed}>
                                                    {line.current_price ? formatMoney(line.current_price) : "—"}
                                                </td>
                                                <td class="txt-right txt-money">{formatMoney(line.total)}</td>
                                            </tr>
                                        {/each}
                                        {#if !record.lines.length}
                                            <tr><td colspan="5" class="txt-hint">Nothing was in the basket.</td></tr>
                                        {/if}
                                    </tbody>
                                </table>
                            </div>
                            <div class="order-lines">
                                <div class="order-line">
                                    <span class="txt-hint">{record.item_count} {pluralize(record.item_count, "item")} · subtotal when abandoned</span>
                                    <strong class="txt-money">{formatMoney(record.subtotal)}</strong>
                                </div>
                                {#if record.discount_code}
                                    <div class="order-line">
                                        <span class="txt-hint">Discount code</span>
                                        <span class="txt-code">{record.discount_code}</span>
                                    </div>
                                {/if}
                            </div>
                            <div class="field-help m-t-5">
                                Prices are the basket's own, as it held them when it was abandoned; "now"
                                is the basket as it stands today. Tax and delivery are added at checkout.
                            </div>
                        </section>

                        <section class="order-card">
                            <h6 class="order-card-title m-b-10">Timeline</h6>
                            <ol class="list order-history recovery-timeline">
                                {#each record.timeline as entry, i (entry.at + entry.kind + i)}
                                    {@const who = actorWords(entry.actor)}
                                    <li class="list-item {timelineTone(entry.kind)}" in:rise>
                                        <i class={timelineIcon(entry.kind)} aria-hidden="true"></i>
                                        <div class="order-item-text">
                                            {entry.label}
                                            {#if who}
                                                <div class="txt-hint txt-sm">{who}</div>
                                            {/if}
                                        </div>
                                        <time
                                            class="txt-hint txt-sm order-history-when"
                                            datetime={entry.at}
                                            title={relativeTime(entry.at)}
                                        >
                                            {formatDate(entry.at)}
                                        </time>
                                    </li>
                                {/each}
                            </ol>
                        </section>
                    </div>

                    <aside class="order-rail">
                        <section class="order-card">
                            <h6 class="order-card-title">Recovery</h6>

                            {#if record.status === "recovered" && record.recovered_order}
                                <div class="recovery-outcome is-success" in:rise>
                                    <i class="ri-checkbox-circle-line" aria-hidden="true"></i>
                                    <div>
                                        <div>
                                            Bought as
                                            <a class="txt-bold" href="{base}/dash/orders/{record.recovered_order.id}">
                                                {record.recovered_order.number}
                                            </a>
                                        </div>
                                        <div class="order-total">{formatMoney(record.recovered_order.total)}</div>
                                        <div class="txt-hint txt-sm">
                                            {record.recovered_after_message
                                                ? "After a recovery email"
                                                : "The shopper came back without a message"}
                                            · {formatDate(record.recovered_at)}
                                        </div>
                                    </div>
                                </div>
                            {/if}

                            {#if record.suppression}
                                <div class="recovery-outcome" in:rise>
                                    <i class="ri-forbid-line" aria-hidden="true"></i>
                                    <div>
                                        <div class="txt-bold">Suppressed: {suppressReason(record.suppression.reason)}</div>
                                        {#if record.suppression.note}
                                            <p class="recovery-note">{record.suppression.note}</p>
                                        {/if}
                                        <div class="txt-hint txt-sm">
                                            {record.suppression.by ? `By ${record.suppression.by === "token" ? "an admin token" : record.suppression.by}` : "By an operator"}
                                            · {formatDate(record.suppression.at)}
                                        </div>
                                    </div>
                                </div>
                            {/if}

                            {#if record.status === "expired"}
                                <div class="recovery-outcome" in:rise>
                                    <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                    <div>
                                        <div class="txt-bold">The basket expired</div>
                                        <div class="txt-hint txt-sm">
                                            Core deleted it {record.expired_at ? formatDate(record.expired_at) : ""}; nothing more can be sent.
                                        </div>
                                    </div>
                                </div>
                            {/if}

                            <dl class="recovery-facts">
                                <div>
                                    <dt>Emails sent</dt>
                                    <dd>
                                        {record.messages_sent}
                                        {#if record.steps_total && record.messages_sent}
                                            <span class="txt-hint txt-sm">· {record.steps_sent} of {record.steps_total} automatic</span>
                                        {/if}
                                    </dd>
                                </div>
                                {#if record.last_sent_at}
                                    <div>
                                        <dt>Last sent</dt>
                                        <dd title={formatDate(record.last_sent_at)}>{relativeTime(record.last_sent_at)}</dd>
                                    </div>
                                {/if}
                                {#if open}
                                    <div>
                                        <dt>Next reminder</dt>
                                        <dd title={record.next_step_at ? formatDate(record.next_step_at) : undefined}>
                                            {#if record.next_step_at}
                                                {relativeTime(record.next_step_at)}
                                            {:else}
                                                <span class="txt-hint">None scheduled</span>
                                            {/if}
                                        </dd>
                                    </div>
                                {/if}
                                <div>
                                    <dt>Link opened</dt>
                                    <dd>
                                        {#if record.clicks}
                                            {record.clicks} {pluralize(record.clicks, "time")}
                                            <span class="txt-hint txt-sm" title={formatDate(record.first_clicked_at)}>
                                                · first {relativeTime(record.first_clicked_at)}
                                            </span>
                                        {:else}
                                            <span class="txt-hint">Not yet</span>
                                        {/if}
                                    </dd>
                                </div>
                                <div>
                                    <dt>Last activity</dt>
                                    <dd title={formatDate(record.last_active_at)}>{relativeTime(record.last_active_at)}</dd>
                                </div>
                            </dl>

                            {#if open && options && !options.can_send && options.send_blocked_reason}
                                <p class="recovery-blocked">
                                    <i class="ri-information-line" aria-hidden="true"></i>
                                    <span>{options.send_blocked_reason}</span>
                                </p>
                            {/if}
                            {#if showLink && options && !options.can_copy_link && options.link_blocked_reason && options.link_blocked_reason !== options.send_blocked_reason}
                                <p class="recovery-blocked">
                                    <i class="ri-link" aria-hidden="true"></i>
                                    <span>{options.link_blocked_reason}</span>
                                </p>
                            {/if}
                            {#if record.history}
                                <p class="recovery-blocked">
                                    <i class="ri-time-line" aria-hidden="true"></i>
                                    <span>This basket went quiet before recovery was set up, so it is shown and never written to.</span>
                                </p>
                            {/if}
                        </section>

                        <section class="order-card">
                            <h6 class="order-card-title">Customer</h6>
                            {#if record.email}
                                <div class="recovery-email">
                                    <a href="mailto:{record.email}" class="txt-bold txt-ellipsis">{record.email}</a>
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary recovery-copy"
                                        class:is-copied={copied === "email"}
                                        title={copied === "email" ? "Copied" : "Copy email"}
                                        aria-label={copied === "email" ? "Email copied" : "Copy email"}
                                        onclick={copyEmail}
                                    >
                                        <i class={copied === "email" ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
                                    </button>
                                </div>
                                {#if record.customer}
                                    <div class="order-lines">
                                        <div class="order-line">
                                            <span class="txt-hint">Previous orders</span>
                                            <span>{record.customer.orders}</span>
                                        </div>
                                        <div class="order-line">
                                            <span class="txt-hint">Lifetime value</span>
                                            <span class="txt-money">{formatMoney(record.customer.lifetime_value)}</span>
                                        </div>
                                        {#if record.customer.last_order_id}
                                            <div class="order-line">
                                                <span class="txt-hint">Last order</span>
                                                <span>
                                                    <a href="{base}/dash/orders/{record.customer.last_order_id}">{record.customer.last_order_number}</a>
                                                    <span class="txt-hint txt-sm" title={formatDate(record.customer.last_order_at)}>
                                                        · {relativeTime(record.customer.last_order_at)}
                                                    </span>
                                                </span>
                                            </div>
                                        {/if}
                                    </div>
                                    <a
                                        class="recovery-arrow-link m-t-sm"
                                        href="{base}/dash/orders?email={encodeURIComponent(record.email)}"
                                    >
                                        Their orders <span aria-hidden="true">→</span>
                                    </a>
                                    <div class="field-help">
                                        Read from the orders placed with this address — the store keeps no customer
                                        record of its own.
                                    </div>
                                {/if}
                            {:else}
                                <p class="txt-hint">
                                    No email. Nothing can be sent about this basket — an address only arrives if the
                                    shopper types one, or is signed in, before leaving.
                                </p>
                            {/if}
                        </section>

                        <section class="order-card">
                            <h6 class="order-card-title">The basket now</h6>
                            {#if record.cart?.exists}
                                <div class="order-lines recovery-cart">
                                    <div class="order-line">
                                        <span class="txt-hint">State</span>
                                        <span class="label sm {cartStateClass(record.cart.status)}">{record.cart.status}</span>
                                    </div>
                                    <div class="order-line">
                                        <span class="txt-hint">Last touched</span>
                                        <span title={formatDate(record.cart.updated_at)}>{relativeTime(record.cart.updated_at)}</span>
                                    </div>
                                    <div class="order-line">
                                        <span class="txt-hint">Expires</span>
                                        <span title={formatDate(record.cart.expires_at)}>{relativeTime(record.cart.expires_at)}</span>
                                    </div>
                                </div>
                                <a class="recovery-arrow-link m-t-sm" href="{base}/dash/checkouts">
                                    All carts <span aria-hidden="true">→</span>
                                </a>
                            {:else}
                                <p class="txt-hint">
                                    Deleted. The store purged this basket when it expired; what it held is kept above as
                                    it was.
                                </p>
                            {/if}
                        </section>
                    </aside>
                </div>

                <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                    <span class="txt txt-hint">
                        Basket #{record.cart_id} · opened {formatDate(record.cart_created_at)} · re-read every 30 seconds
                        while this tab is open
                    </span>
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>

    <RecoverySendDialog
        open={sendOpen}
        {record}
        onclose={() => (sendOpen = false)}
        onsent={afterSent}
        onrefresh={() => load({ quiet: true })}
    />
    <RecoverySuppressDialog
        open={suppressOpen}
        {record}
        onclose={() => (suppressOpen = false)}
        ondone={afterSuppressed}
        onrefresh={() => load({ quiet: true })}
    />
{/if}
