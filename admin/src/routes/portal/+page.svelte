<script>
    /**
     * Overview: where the company's account stands, what the buyer may do,
     * and what is waiting — the screen a buyer lands on.
     *
     * The figures are read again on arrival rather than taken from the
     * shell's copy: a buyer who has just placed an order and come back here
     * expects the available credit to have moved.
     */
    import { onMount } from "svelte";
    import { base } from "$app/paths";
    import { formatDate, formatMoney } from "$lib/format.js";
    import {
        trade,
        tradeApi,
        loadMe,
        explain,
        isApprover,
        orderWords,
        roleWord,
        ROLE_WORDS,
        termsWords,
        word,
        APPROVAL_WORDS,
    } from "$lib/trade.svelte.js";
    import PortalPage from "./PortalPage.svelte";

    const me = $derived(trade.me);
    const company = $derived(me?.company);
    const credit = $derived(me?.credit);

    let recent = $state([]);
    let overdue = $state([]);
    let waiting = $state([]);
    let loading = $state(true);
    let failure = $state("");

    async function load() {
        loading = true;
        failure = "";
        try {
            const [orders, late, approvals] = await Promise.all([
                tradeApi.list("/x/b2b/orders?limit=5"),
                tradeApi.list("/x/b2b/orders?overdue=true&limit=5"),
                tradeApi.list("/x/b2b/approvals?status=pending&limit=5"),
                loadMe(),
            ]);
            recent = orders.data;
            overdue = late.data;
            waiting = approvals.data;
        } catch (err) {
            if (!err.handled) failure = explain(err);
        } finally {
            loading = false;
        }
    }

    onMount(load);

    const firstName = $derived((trade.account?.name || "").trim().split(/\s+/)[0] || "");
    const usedShare = $derived.by(() => {
        if (!credit?.limit?.amount_minor) return 0;
        return Math.min(1, credit.outstanding.amount_minor / credit.limit.amount_minor);
    });
</script>

<PortalPage title={firstName ? `Hello, ${firstName}` : "Overview"}>
    {#snippet actions()}
        <a href="{base}/portal/quick-order" class="btn portal-press">
            <i class="ri-flashlight-line" aria-hidden="true"></i>
            <span class="txt">Quick order</span>
        </a>
    {/snippet}

    {#if failure}
        <div class="alert danger m-b-base portal-notice" role="alert">
            <p>{failure}</p>
            <button type="button" class="btn sm secondary portal-press" onclick={load}><span class="txt">Try again</span></button>
        </div>
    {/if}

    <section class="portal-role tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base" aria-label="Your role">
        <div class="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
            <span class="label label-info">{roleWord(me.role)}</span>
            <span class="txt-hint txt-sm">{ROLE_WORDS[me.role]?.hint}</span>
        </div>
        <dl class="b2b-facts">
            <div>
                <dt>Company</dt>
                <dd>{company.name}</dd>
            </div>
            <div>
                <dt>Payment terms</dt>
                <dd>{termsWords(company)}</dd>
            </div>
            <div>
                <dt>Approval limit</dt>
                <dd>
                    {#if company.approval_threshold}
                        {#if me.role === "buyer"}
                            Orders over {formatMoney(company.approval_threshold)} wait for an approver
                        {:else}
                            Buyers' orders over {formatMoney(company.approval_threshold)} come to you
                        {/if}
                    {:else}
                        None — every order goes straight through
                    {/if}
                </dd>
            </div>
            <div>
                <dt>PO number</dt>
                <dd>{company.require_po ? "Needed on every order" : "Optional"}</dd>
            </div>
        </dl>
    </section>

    <h2 class="section-title">
        <i class="ri-bank-card-line" aria-hidden="true"></i>
        Your account with {trade.store.name || "the store"}
    </h2>
    {#if credit?.limit}
        <div class="portal-tiles m-b-sm" aria-live="polite">
            <div class="b2b-tile">
                <span class="b2b-tile-label">Available to spend</span>
                <span class="b2b-tile-value">{formatMoney(credit.available)}</span>
                <span class="b2b-tile-hint">of a {formatMoney(credit.limit)} limit</span>
            </div>
            <div class="b2b-tile">
                <span class="b2b-tile-label">Owed</span>
                <span class="b2b-tile-value">{formatMoney(credit.outstanding)}</span>
                <span class="b2b-tile-hint">orders on account, not yet paid</span>
            </div>
            <div class="b2b-tile" class:is-bad={credit.overdue_orders > 0}>
                <span class="b2b-tile-label">Overdue</span>
                <span class="b2b-tile-value">{formatMoney(credit.overdue)}</span>
                <span class="b2b-tile-hint">
                    {credit.overdue_orders === 0
                        ? "nothing is late"
                        : `${credit.overdue_orders} ${credit.overdue_orders === 1 ? "order" : "orders"} past due`}
                </span>
            </div>
        </div>
        <div
            class="portal-meter m-b-base"
            role="meter"
            aria-label="Credit used"
            aria-valuemin="0"
            aria-valuemax="100"
            aria-valuenow={Math.round(usedShare * 100)}
        >
            <span class="portal-meter-fill" class:is-high={usedShare > 0.85} style="transform: scaleX({usedShare})"></span>
        </div>
    {:else}
        <p class="txt-hint m-b-base">
            {company.name} pays when it orders — there is no account to buy on. Ask
            {trade.store.name || "the store"} if you'd like one.
        </p>
    {/if}

    {#if overdue.length}
        <div class="alert danger m-b-base portal-notice" role="status">
            <p>
                <strong>{overdue.length === 1 ? "An order is" : "Some orders are"} past due.</strong>
                {#each overdue as o, i (o.order_id)}
                    <a href="{base}/portal/orders/{o.order_id}" class="txt-code">{o.number}</a>
                    (due {formatDate(o.due_at, { withTime: false })}){i < overdue.length - 1 ? ", " : "."}
                {/each}
            </p>
        </div>
    {/if}

    <div class="portal-columns">
        <section aria-labelledby="recent-heading">
            <div class="b2b-section-head">
                <h2 class="section-title" id="recent-heading">
                    <i class="ri-file-list-3-line" aria-hidden="true"></i>
                    {isApprover() ? "Recent orders" : "Your recent orders"}
                </h2>
                <a href="{base}/portal/orders" class="b2b-more">All orders <span aria-hidden="true">→</span></a>
            </div>
            <div class="portal-list tw:rounded-xl tw:border tw:bg-card" class:faded={loading && recent.length}>
                {#if loading && !recent.length}
                    {#each Array(3) as _, i (i)}<div class="portal-list-row"><span class="skeleton-loader"></span></div>{/each}
                {:else if !recent.length}
                    <p class="portal-empty txt-hint">
                        No orders yet. <a href="{base}/portal/quick-order">Start one with a quick order.</a>
                    </p>
                {:else}
                    {#each recent as o (o.order_id)}
                        {@const w = orderWords(o)}
                        <a class="portal-list-row portal-row-link" href="{base}/portal/orders/{o.order_id}">
                            <span class="portal-list-main">
                                <span class="txt-bold txt-code">{o.number}</span>
                                <span class="txt-hint txt-sm">
                                    {formatDate(o.created_at, { withTime: false })}{o.po_number ? ` · PO ${o.po_number}` : ""}
                                </span>
                            </span>
                            <span class="portal-list-side">
                                <span class="txt-bold">{formatMoney(o.total)}</span>
                                <span class="label {w.payment.tone}">{w.payment.label}</span>
                            </span>
                        </a>
                    {/each}
                {/if}
            </div>
        </section>

        <section aria-labelledby="waiting-heading">
            <div class="b2b-section-head">
                <h2 class="section-title" id="waiting-heading">
                    <i class="ri-checkbox-circle-line" aria-hidden="true"></i>
                    {isApprover() ? "Waiting for you" : "Waiting for approval"}
                </h2>
                <a href="{base}/portal/approvals" class="b2b-more">All requests <span aria-hidden="true">→</span></a>
            </div>
            <div class="portal-list tw:rounded-xl tw:border tw:bg-card" class:faded={loading && waiting.length}>
                {#if loading && !waiting.length}
                    {#each Array(2) as _, i (i)}<div class="portal-list-row"><span class="skeleton-loader"></span></div>{/each}
                {:else if !waiting.length}
                    <p class="portal-empty txt-hint">
                        {isApprover() ? "Nothing is waiting for a decision." : "None of your orders is waiting."}
                    </p>
                {:else}
                    {#each waiting as a (a.id)}
                        <a class="portal-list-row portal-row-link" href="{base}/portal/approvals/{a.id}">
                            <span class="portal-list-main">
                                <span class="txt-bold">{isApprover() ? a.requested_by_email : `Request ${a.id}`}</span>
                                <span class="txt-hint txt-sm">
                                    {a.kind === "quote" ? "Accepting a quote" : `${a.lines.length} ${a.lines.length === 1 ? "line" : "lines"}`}
                                    · {formatDate(a.created_at, { withTime: false })}
                                </span>
                            </span>
                            <span class="portal-list-side">
                                <span class="txt-bold">{formatMoney(a.total)}</span>
                                <span class="label {word(APPROVAL_WORDS, a.status).tone}">{word(APPROVAL_WORDS, a.status).label}</span>
                            </span>
                        </a>
                    {/each}
                {/if}
            </div>
        </section>
    </div>
</PortalPage>
