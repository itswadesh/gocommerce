<script>
    /**
     * One abandoned checkout, at its own address, laid out as kitcommerce-admin
     * lays it out: what happened up top as banners, the cart and its timeline
     * on the left, who it was and what was sent on the right.
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
    import { page as route } from "$app/state";
    import { can, recovery } from "$lib/api.js";
    import { formatDate, formatMoney, pluralize, relativeTime } from "$lib/format.js";
    import {
        actorMeta,
        customerLines,
        durationWords,
        holdSentence,
        indicatorLabel,
        isOpen,
        kindLabel,
        statusClass,
        statusLabel,
        timelineByDay,
        timelineIcon,
        timelineTone,
    } from "$lib/recovery.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { rightLabel } from "$lib/rights.js";
    import { copyText } from "$lib/clipboard.js";
    import { rise } from "$lib/motion.js";
    import { toast } from "$lib/toast.svelte.js";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import RecoveryLinkDialog from "$lib/components/RecoveryLinkDialog.svelte";
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
    /** A read somebody asked for: the first, the refresh buttons, an action's
        re-read. The thirty-second poll is not one, so nothing on the page
        spins or announces itself twice a minute. */
    let loading = $state(true);
    /** "missing" for a 404, the message for anything else, "" when read. */
    let failure = $state("");
    let notFound = $state(false);
    let loadedAt = $state(null);
    let sendOpen = $state(false);
    let suppressOpen = $state(false);
    let shownLink = $state(null);
    /** The link button's own word: idle, busy, copied or failed. */
    let linkState = $state("idle");
    /** Which copy button last worked, for its two seconds of "Copied". */
    let copied = $state("");
    let copiedTimer;
    let linkTimer;

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
            failure = "";
            notFound = false;
            lastLoaded = Date.now();
            loadedAt = new Date();
        } catch (err) {
            if (mine !== reqId) return;
            if (err.status === 404) {
                record = null;
                notFound = true;
            } else if (!record) {
                failure = err?.message || "The record could not be read.";
            } else if (!quiet) {
                toast.error(err);
            }
        } finally {
            if (mine === reqId && !quiet) loading = false;
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

    function settleLink(state) {
        linkState = state;
        clearTimeout(linkTimer);
        linkTimer = setTimeout(() => (linkState = "idle"), 2000);
    }

    async function copyLink() {
        if (!record || linkState === "busy") return;
        linkState = "busy";
        try {
            const link = await recovery.link(record.id);
            if (await copyText(link.url)) {
                settleLink("copied");
                if (!link.tracked) {
                    toast.info("Copied. Clicks on this link are not counted: the store has no public address set.");
                }
            } else {
                settleLink("failed");
                shownLink = link;
            }
            // Handing out the link is recorded on the timeline.
            load({ quiet: true });
        } catch (err) {
            settleLink("failed");
            toast.error(err);
            if (err.status === 409) load({ quiet: true });
        }
    }

    async function copy(which, value) {
        if (!value) return;
        if (await copyText(value)) flashCopied(which);
        else toast.info(`Copy it by hand: ${value}`);
    }

    function afterSent(result) {
        record = result.abandonment;
        lastLoaded = Date.now();
        loadedAt = new Date();
        sendOpen = false;
    }

    function afterSuppressed(detail) {
        record = detail;
        lastLoaded = Date.now();
        loadedAt = new Date();
        suppressOpen = false;
    }

    function closeMore() {
        document.getElementById("abandoned-more")?.hidePopover();
    }

    const options = $derived(record?.recovery ?? null);
    const open = $derived(record ? isOpen(record.status) : false);
    const showSend = $derived(!!record?.email && open);
    const showLink = $derived(!!record && record.status !== "recovered" && record.status !== "expired");
    const showSuppress = $derived(!!options?.can_suppress);
    const kindWord = $derived(record?.kind === "cart" ? "cart" : "checkout");
    const who = $derived(record ? customerLines({ ...record, ...(record.customer ?? {}) }) : null);
    const days = $derived(record ? timelineByDay(record.timeline) : []);
    const changed = $derived(record ? record.lines.filter((l) => l.price_changed || l.stock !== "in_stock").length : 0);

    const sendTitle = $derived(
        !mayContact ? noContactRight : !options?.can_send ? options?.send_blocked_reason || "Recovery cannot be sent right now" : undefined,
    );
    const linkTitle = $derived(
        !mayContact ? noContactRight : !options?.can_copy_link ? options?.link_blocked_reason : undefined,
    );
    const linkWord = $derived(
        { busy: "Getting link…", copied: "Copied", failed: "Couldn't copy" }[linkState] ?? "Copy recovery link",
    );

    function suppressReason(key) {
        return options?.suppress_reasons?.find((r) => r.key === key)?.label ?? key;
    }

    /** A line's chips: what is different about it now, worst first. */
    function issues(line) {
        const out = [];
        if (line.stock === "unavailable") out.push({ text: "No longer available", cls: "danger" });
        else if (line.stock === "out_of_stock") out.push({ text: "Out of stock", cls: "danger" });
        else if (line.stock === "insufficient")
            out.push({ text: "Inventory changed", cls: "warning", detail: `Requested ${line.quantity} · Available ${line.available}` });
        if (line.price_changed && line.current_price) out.push({ text: "Price changed", cls: "warning", price: true });
        return out;
    }

    function cartStateClass(state) {
        if (state === "converted") return "success";
        if (state === "abandoned") return "warning";
        return "info";
    }

    const time = (at) => new Intl.DateTimeFormat(undefined, { hour: "2-digit", minute: "2-digit" }).format(new Date(at));
    const minutesBetween = (a, b) => Math.max(1, Math.round((Date.parse(b) - Date.parse(a)) / 60000));
</script>

<svelte:head>
    <title>{record ? `Abandoned ${kindWord}` : "Abandoned checkout"} · GoCommerce</title>
</svelte:head>

{#if !readable && !missing}
    <NoAccess right="abandonment.read" what="abandoned checkouts" />
{:else}
    <div class="page page-abandoned-detail shopify-skin">
        <div class="page-content full-height">
            <header class="page-header recovery-detail-header">
                <a
                    class="btn circle transparent secondary recovery-back"
                    href="{base}/dash/abandoned"
                    aria-label="Back to abandoned checkouts"
                    title="Back to abandoned checkouts"
                >
                    <i class="ri-arrow-left-line" aria-hidden="true"></i>
                </a>
                <div class="recovery-title-block">
                    <h1 class="recovery-h1">
                        Abandoned {kindWord}
                        {#if record}
                            {#key record.status}
                                <span class="recovery-pill {statusClass(record.status) || 'neutral'}" in:rise>
                                    <span class="recovery-pill-dot" aria-hidden="true"></span>
                                    {statusLabel(record.status)}
                                </span>
                            {/key}
                            {#if record.indicator}
                                <span class="txt-hint txt-sm recovery-h1-hint">{indicatorLabel(record.indicator)}</span>
                            {/if}
                        {/if}
                    </h1>
                    {#if record}
                        <div class="recovery-subline">
                            <span class="txt-code">#{record.id}</span>
                            <button
                                type="button"
                                class="btn xs circle transparent secondary recovery-copy"
                                class:is-copied={copied === "id"}
                                title={copied === "id" ? "Copied" : "Copy ID"}
                                aria-label={copied === "id" ? "ID copied" : "Copy ID"}
                                onclick={() => copy("id", String(record.id))}
                            >
                                <i class={copied === "id" ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
                            </button>
                            <span aria-hidden="true">·</span>
                            <span title={formatDate(record.abandoned_at)}>Abandoned {relativeTime(record.abandoned_at)}</span>
                            <span aria-hidden="true">·</span>
                            <span class="tw:tabular-nums">{formatMoney(record.subtotal)}</span>
                            <span aria-hidden="true">·</span>
                            <span>{record.item_count} {pluralize(record.item_count, "item")}</span>
                        </div>
                    {/if}
                </div>

                <div class="flex-fill"></div>

                {#if record}
                    <span class="txt-hint txt-sm recovery-updated">
                        {loading ? "Refreshing…" : loadedAt ? `Updated ${time(loadedAt)}` : ""}
                    </span>
                    <!-- Every button here leads with an icon: below 550px the
                         header turns each into its icon in a circle and hides
                         the label, and a button with no icon keeps its words
                         inside a round box. -->
                    <div class="page-header-primary-btns">
                        {#if showSend}
                            <button
                                type="button"
                                class="btn sm"
                                class:is-blocked={!mayContact || !options?.can_send}
                                aria-disabled={!mayContact || !options?.can_send}
                                title={sendTitle}
                                onclick={() => mayContact && options?.can_send && (sendOpen = true)}
                            >
                                <i class="ri-send-plane-line" aria-hidden="true"></i>
                                <span class="txt">Send recovery</span>
                            </button>
                        {/if}
                        {#if showLink}
                            <button
                                type="button"
                                class="btn sm secondary recovery-copy recovery-link-btn"
                                class:loading={linkState === "busy"}
                                class:is-copied={linkState === "copied"}
                                disabled={linkState === "busy" || !mayContact || !options?.can_copy_link}
                                title={linkTitle}
                                aria-live="polite"
                                onclick={copyLink}
                            >
                                <i class={linkState === "copied" ? "ri-check-line" : "ri-link"} aria-hidden="true"></i>
                                <span class="txt">{linkWord}</span>
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
                        <button
                            type="button"
                            class="btn sm circle secondary"
                            popovertarget="abandoned-more"
                            aria-haspopup="menu"
                            aria-label="More actions"
                            title="More actions"
                        >
                            <i class="ri-more-fill" aria-hidden="true"></i>
                        </button>
                        <div id="abandoned-more" class="dropdown sm nowrap dropdown-left" popover="auto" role="menu">
                            {#if record.email && can("customers.read")}
                                <a role="menuitem" class="dropdown-item" href="{base}/dash/customers?q={encodeURIComponent(record.email)}">
                                    <i class="ri-user-3-line" aria-hidden="true"></i>
                                    <span class="txt">Open customer</span>
                                </a>
                            {/if}
                            {#if record.cart?.exists && can("orders.read")}
                                <a role="menuitem" class="dropdown-item" href="{base}/dash/checkouts?state=any&cart={record.cart_id}">
                                    <i class="ri-shopping-cart-2-line" aria-hidden="true"></i>
                                    <span class="txt">Open cart</span>
                                </a>
                            {/if}
                            {#if record.recovered_order}
                                <a role="menuitem" class="dropdown-item" href="{base}/dash/orders/{record.recovered_order.id}">
                                    <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
                                    <span class="txt">Open recovered order</span>
                                </a>
                            {/if}
                            <hr />
                            <button
                                type="button"
                                role="menuitem"
                                class="dropdown-item"
                                onclick={() => {
                                    closeMore();
                                    load();
                                }}
                            >
                                <i class="ri-refresh-line" aria-hidden="true"></i>
                                <span class="txt">Refresh</span>
                            </button>
                        </div>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing
                    module="cart-recovery"
                    what="Abandoned checkouts are recorded by ext/cart-recovery, and this binary does not have it."
                />
            {:else if notFound}
                <EmptyState
                    icon="ri-search-eye-line"
                    title="This abandoned checkout isn't in this store"
                    text="It may belong to another store, or the link is wrong."
                >
                    <a class="btn secondary" href="{base}/dash/abandoned">
                        <i class="ri-arrow-left-line" aria-hidden="true"></i>
                        <span class="txt">Back to abandoned checkouts</span>
                    </a>
                </EmptyState>
            {:else if failure && !record}
                <EmptyState icon="ri-error-warning-line" title="This abandoned checkout couldn't load" text={failure}>
                    <button type="button" class="btn secondary" onclick={() => load()}>
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                        <span class="txt">Try again</span>
                    </button>
                </EmptyState>
            {:else if loading && !record}
                <div class="recovery-detail-skeleton" aria-busy="true">
                    <div class="order-grid m-t-sm">
                        <div class="order-main">
                            <section class="order-card"><span class="skeleton-loader"></span><span class="skeleton-loader"></span><span class="skeleton-loader"></span></section>
                            <section class="order-card"><span class="skeleton-loader"></span><span class="skeleton-loader"></span></section>
                        </div>
                        <aside class="order-rail">
                            <section class="order-card"><span class="skeleton-loader"></span><span class="skeleton-loader"></span></section>
                            <section class="order-card"><span class="skeleton-loader"></span></section>
                        </aside>
                    </div>
                </div>
            {:else if record}
                <!-- What happened, said once at the top: the outcome if there
                     is one, and otherwise why nothing is coming. -->
                {#if record.status === "recovered" && record.recovered_order}
                    <div class="alert success recovery-banner" in:rise>
                        <i class="ri-verified-badge-line" aria-hidden="true"></i>
                        <p>
                            <strong>Recovered through order {record.recovered_order.number}</strong>
                            ({formatMoney(record.recovered_order.total)}), {relativeTime(record.recovered_at)} —
                            {record.recovered_after_message ? "after a recovery message." : "without a recovery message."}
                        </p>
                        <a class="btn sm secondary" href="{base}/dash/orders/{record.recovered_order.id}">
                            <span class="txt">Open order</span>
                        </a>
                    </div>
                {/if}
                {#if record.status === "expired"}
                    <div class="alert recovery-banner is-muted" in:rise>
                        <i class="ri-hourglass-line" aria-hidden="true"></i>
                        <p><strong>Expired.</strong> The cart was deleted, so nothing more will be sent.</p>
                    </div>
                {/if}
                {#if open && !record.next_step_at && record.hold_reason}
                    <div class="alert info recovery-banner" in:rise>
                        <i class="ri-information-line" aria-hidden="true"></i>
                        <p><strong>Nothing is scheduled.</strong> {holdSentence(record.hold_reason)}</p>
                    </div>
                {/if}
                {#if record.suppression}
                    <div class="alert danger recovery-banner" in:rise>
                        <i class="ri-forbid-line" aria-hidden="true"></i>
                        <div>
                            <p>
                                <strong>{record.status === "suppressed" ? "Recovery suppressed." : "Recovery was suppressed."}</strong>
                                Reason: {suppressReason(record.suppression.reason)}
                            </p>
                            {#if record.suppression.note}
                                <p class="recovery-note">“{record.suppression.note}”</p>
                            {/if}
                            <p class="txt-sm recovery-banner-meta">
                                By {record.suppression.by
                                    ? record.suppression.by === "token"
                                        ? "an admin token"
                                        : record.suppression.by
                                    : "a team member"} · {relativeTime(record.suppression.at)}
                            </p>
                        </div>
                    </div>
                {/if}

                <div class="order-grid recovery-detail">
                    <div class="order-main">
                        <section class="order-card">
                            <h6 class="order-card-title">Products</h6>
                            <p class="txt-hint txt-sm recovery-card-desc">As they were at abandonment, compared with now</p>
                            {#if changed}
                                <div class="alert warning recovery-changed">
                                    <p>
                                        {changed} of {record.lines.length}
                                        {pluralize(record.lines.length, "item")}
                                        {changed === 1 ? "has" : "have"} changed since the cart was abandoned.
                                    </p>
                                </div>
                            {/if}
                            <ul class="recovery-items">
                                {#each record.lines as line (line.variant_id)}
                                    {@const chips = issues(line)}
                                    <li class="recovery-item">
                                        {#if line.image_url}
                                            <img class="recovery-thumb" src={line.image_url} alt="" width="48" height="48" loading="lazy" />
                                        {:else}
                                            <span class="recovery-thumb is-empty" aria-hidden="true"><i class="ri-box-3-line"></i></span>
                                        {/if}
                                        <div class="recovery-item-text">
                                            {#if line.product_id && line.stock !== "unavailable"}
                                                <a class="order-item-link" href="{base}/dash/products/{line.product_id}">{line.title}</a>
                                            {:else}
                                                <span>{line.title}</span>
                                            {/if}
                                            <div class="txt-hint txt-sm">
                                                {line.variant_label || "No variant"}{line.sku ? ` · SKU ${line.sku}` : ""}
                                            </div>
                                            {#if chips.length}
                                                <div class="recovery-line-notes">
                                                    {#each chips as chip (chip.text)}
                                                        <span class="label sm {chip.cls}">{chip.text}</span>
                                                        {#if chip.detail}
                                                            <span class="txt-hint txt-sm">{chip.detail}</span>
                                                        {/if}
                                                        {#if chip.price}
                                                            <span class="txt-sm">
                                                                <s class="txt-hint">{formatMoney(line.unit_price)}</s>
                                                                → {formatMoney(line.current_price)}
                                                            </span>
                                                        {/if}
                                                    {/each}
                                                </div>
                                            {/if}
                                        </div>
                                        <div class="recovery-item-money">
                                            <span class="txt-hint txt-sm">{line.quantity} × {formatMoney(line.unit_price)}</span>
                                            <strong class="txt-money">{formatMoney(line.total)}</strong>
                                        </div>
                                    </li>
                                {/each}
                            </ul>
                            {#if !record.lines.length}
                                <p class="txt-hint">This cart has no items left.</p>
                            {/if}
                        </section>

                        <section class="order-card">
                            <div class="recovery-card-head">
                                <div>
                                    <h6 class="order-card-title">Timeline</h6>
                                    <p class="txt-hint txt-sm recovery-card-desc">Everything recorded for this cart, newest first</p>
                                </div>
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary recovery-refresh"
                                    class:is-spinning={loading}
                                    aria-label="Refresh the timeline"
                                    title="Refresh"
                                    disabled={loading}
                                    onclick={() => load()}
                                >
                                    <i class="ri-refresh-line" aria-hidden="true"></i>
                                </button>
                            </div>
                            {#each days as day (day.key)}
                                <div class="recovery-day">
                                    <div class="recovery-day-label">{day.label}</div>
                                    <ol class="recovery-events">
                                        {#each day.entries as entry (entry.key)}
                                            {@const meta = [time(entry.at), actorMeta(entry.actor)].filter(Boolean).join(" · ")}
                                            <li class="recovery-event" in:rise>
                                                <span class="recovery-event-icon is-{timelineTone(entry.kind)}" aria-hidden="true">
                                                    <i class={timelineIcon(entry.kind)}></i>
                                                </span>
                                                <div class="recovery-event-text">
                                                    <div class:txt-bold={entry.kind === "recovered"}>{entry.label}</div>
                                                    <time class="txt-hint txt-sm" datetime={entry.at} title={formatDate(entry.at)}>{meta}</time>
                                                </div>
                                            </li>
                                        {/each}
                                    </ol>
                                </div>
                            {:else}
                                <p class="txt-hint">Nothing has been recorded for this cart yet.</p>
                            {/each}
                        </section>
                    </div>

                    <aside class="order-rail">
                        <section class="order-card">
                            <h6 class="order-card-title">Customer</h6>
                            <div class="recovery-customer-name" class:txt-hint={who.guest}>{who.top}</div>
                            {#if who.guest}
                                <p class="txt-hint txt-sm">No contact details</p>
                            {/if}
                            <dl class="recovery-facts">
                                {#if record.email}
                                    <div>
                                        <dt>Email</dt>
                                        <dd class="txt-ellipsis"><a href="mailto:{record.email}">{record.email}</a></dd>
                                    </div>
                                {/if}
                                {#if record.phone}
                                    <div>
                                        <dt>Phone</dt>
                                        <dd><a href="tel:{record.phone}">{record.phone}</a></dd>
                                    </div>
                                {/if}
                                {#if record.customer}
                                    <div>
                                        <dt>Previous orders</dt>
                                        <dd class="tw:tabular-nums">{record.customer.orders}</dd>
                                    </div>
                                    <div>
                                        <dt>Lifetime value</dt>
                                        <dd class="txt-money">{formatMoney(record.customer.lifetime_value)}</dd>
                                    </div>
                                    <div>
                                        <dt>Last order</dt>
                                        <dd>
                                            {#if record.customer.last_order_id}
                                                <a href="{base}/dash/orders/{record.customer.last_order_id}">{record.customer.last_order_number}</a>
                                                <span class="txt-hint" title={formatDate(record.customer.last_order_at)}>
                                                    · {relativeTime(record.customer.last_order_at)}
                                                </span>
                                            {:else}
                                                <span class="txt-hint">None</span>
                                            {/if}
                                        </dd>
                                    </div>
                                {/if}
                            </dl>
                            {#if record.email}
                                <div class="recovery-customer-actions">
                                    <a class="btn sm secondary" href="{base}/dash/customers?q={encodeURIComponent(record.email)}">
                                        <i class="ri-user-3-line" aria-hidden="true"></i>
                                        <span class="txt">View customer</span>
                                    </a>
                                    <button
                                        type="button"
                                        class="btn sm secondary recovery-copy"
                                        class:is-copied={copied === "email"}
                                        onclick={() => copy("email", record.email)}
                                    >
                                        <i class={copied === "email" ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
                                        <span class="txt">{copied === "email" ? "Copied" : "Copy email"}</span>
                                    </button>
                                    {#if record.phone}
                                        <button
                                            type="button"
                                            class="btn sm secondary recovery-copy"
                                            class:is-copied={copied === "phone"}
                                            onclick={() => copy("phone", record.phone)}
                                        >
                                            <i class={copied === "phone" ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
                                            <span class="txt">{copied === "phone" ? "Copied" : "Copy phone"}</span>
                                        </button>
                                    {/if}
                                </div>
                                <div class="field-help">
                                    Read from the orders placed with this address: the store keeps no customer record of
                                    its own.
                                </div>
                            {:else}
                                <p class="txt-hint txt-sm">
                                    Nothing can be sent about this cart. An address only arrives if the shopper types
                                    one, or is signed in, before leaving.
                                </p>
                            {/if}
                        </section>

                        <section class="order-card">
                            <h6 class="order-card-title">Recovery</h6>
                            <dl class="recovery-facts">
                                {#if record.status === "recovered" && record.recovered_order}
                                    <div>
                                        <dt>Recovered order</dt>
                                        <dd><a href="{base}/dash/orders/{record.recovered_order.id}">{record.recovered_order.number}</a></dd>
                                    </div>
                                    <div>
                                        <dt>Order total</dt>
                                        <dd class="txt-money">{formatMoney(record.recovered_order.total)}</dd>
                                    </div>
                                    <div>
                                        <dt>Recovered</dt>
                                        <dd>{formatDate(record.recovered_at)}</dd>
                                    </div>
                                    <div>
                                        <dt>Time to recover</dt>
                                        <dd>{durationWords(minutesBetween(record.abandoned_at, record.recovered_at))}</dd>
                                    </div>
                                    <div>
                                        <dt>Message before the order</dt>
                                        <dd>{record.recovered_after_message ? "Yes" : "No"}</dd>
                                    </div>
                                {/if}
                                <div>
                                    <dt>Messages sent</dt>
                                    <dd class="tw:tabular-nums">{record.messages_sent}</dd>
                                </div>
                                {#if record.status !== "recovered"}
                                    <div>
                                        <dt>Last sent</dt>
                                        <dd>
                                            {#if record.last_sent_at}
                                                <span title={formatDate(record.last_sent_at)}>{relativeTime(record.last_sent_at)}</span>
                                            {:else}
                                                <span class="txt-hint">Never</span>
                                            {/if}
                                        </dd>
                                    </div>
                                    <div>
                                        <dt>Sequence</dt>
                                        <dd>
                                            {record.steps_total
                                                ? `Step ${record.steps_sent} of ${record.steps_total} sent`
                                                : "No steps"}
                                        </dd>
                                    </div>
                                    <div>
                                        <dt>Next step</dt>
                                        <dd>
                                            {#if open && record.next_step_at}
                                                {formatDate(record.next_step_at)}
                                                <span class="txt-hint">({relativeTime(record.next_step_at)})</span>
                                            {:else}
                                                <span class="txt-hint">Nothing scheduled</span>
                                            {/if}
                                        </dd>
                                    </div>
                                {/if}
                                <div>
                                    <dt>Link clicks</dt>
                                    <dd class="tw:tabular-nums">{record.clicks}</dd>
                                </div>
                            </dl>
                            <!-- Why a send is refused, for a phone that never shows the
                                 button's title — unless the banner above already said it. -->
                            {#if open && options && !options.can_send && options.send_blocked_reason && !(!record.next_step_at && record.hold_reason)}
                                <p class="recovery-blocked">
                                    <i class="ri-information-line" aria-hidden="true"></i>
                                    <span>{options.send_blocked_reason}</span>
                                </p>
                            {/if}
                            <!-- The link button is disabled when the link cannot be
                                 had, and a disabled button's title reaches neither a
                                 phone nor the keyboard: the reason is said here. -->
                            {#if showLink && options && !options.can_copy_link && options.link_blocked_reason && options.link_blocked_reason !== options.send_blocked_reason}
                                <p class="recovery-blocked">
                                    <i class="ri-link" aria-hidden="true"></i>
                                    <span>{options.link_blocked_reason}</span>
                                </p>
                            {/if}
                        </section>

                        <section class="order-card">
                            <h6 class="order-card-title">Checkout summary</h6>
                            <dl class="recovery-facts">
                                <div>
                                    <dt>Subtotal</dt>
                                    <dd class="txt-money txt-bold">{formatMoney(record.subtotal)}</dd>
                                </div>
                                {#if record.discount_code}
                                    <div>
                                        <dt>Discount code</dt>
                                        <dd class="txt-code">{record.discount_code}</dd>
                                    </div>
                                {/if}
                                <div>
                                    <dt>Currency</dt>
                                    <dd>{record.subtotal.currency}</dd>
                                </div>
                            </dl>
                            <p class="txt-hint txt-sm m-t-sm m-b-0">Shipping and tax are worked out at checkout, so a cart has neither.</p>
                        </section>

                        <section class="order-card">
                            <h6 class="order-card-title">The cart now</h6>
                            {#if record.cart?.exists}
                                <dl class="recovery-facts">
                                    <div>
                                        <dt>State</dt>
                                        <dd><span class="label sm {cartStateClass(record.cart.status)}">{record.cart.status}</span></dd>
                                    </div>
                                    <div>
                                        <dt>Last touched</dt>
                                        <dd title={formatDate(record.cart.updated_at)}>{relativeTime(record.cart.updated_at)}</dd>
                                    </div>
                                    <div>
                                        <dt>Expires</dt>
                                        <dd title={formatDate(record.cart.expires_at)}>{relativeTime(record.cart.expires_at)}</dd>
                                    </div>
                                </dl>
                                <a class="recovery-arrow-link m-t-sm" href="{base}/dash/checkouts?state=any&cart={record.cart_id}">
                                    Open cart <span aria-hidden="true">→</span>
                                </a>
                            {:else}
                                <p class="txt-hint">
                                    Deleted. The store purged this cart when it expired; what it held is kept above as it
                                    was.
                                </p>
                            {/if}
                        </section>
                    </aside>
                </div>

                <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                    <span class="txt txt-hint">
                        Cart #{record.cart_id} · opened {formatDate(record.cart_created_at)} · re-read every 30 seconds
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
    <RecoveryLinkDialog link={shownLink} onclose={() => (shownLink = null)} />
{/if}
