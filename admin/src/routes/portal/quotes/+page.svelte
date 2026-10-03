<script>
    /**
     * Quotes: asking the store for a price on a set of lines, and the answers.
     *
     * A buyer asks with product codes and quantities; the store prices and
     * sends it; the buyer accepts it on the quote's page — an order at the
     * quoted prices, or a request for approval when it is over their limit.
     * The engine takes lines by variant, so each code is looked up in the
     * store's catalogue before anything is sent, and a code it does not know
     * is said beside its line rather than refusing the whole request.
     */
    import { goto } from "$app/navigation";
    import { base } from "$app/paths";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { rowKey } from "$lib/rowkey.js";
    import { query } from "$lib/api.js";
    import { trade, tradeApi, explain, isApprover, word, QUOTE_WORDS } from "$lib/trade.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import PortalPage from "../PortalPage.svelte";

    const list = listState({ status: "", page: 1, limit: 25 });

    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let failure = $state("");
    let reqId = 0;

    const TABS = [
        { value: "", label: "All" },
        { value: "requested", label: "Waiting for a price" },
        { value: "quoted", label: "Ready to accept" },
        { value: "accepted", label: "Accepted" },
    ];

    $effect(() => {
        list.params;
        load();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        failure = "";
        try {
            const p = list.params;
            const r = await tradeApi.list("/x/b2b/quotes" + query({ status: p.status, page: p.page, limit: p.limit }));
            if (mine !== reqId) return;
            rows = r.data;
            meta = r.meta;
        } catch (err) {
            if (mine === reqId && !err.handled) failure = explain(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    const open = (q) => goto(`${base}/portal/quotes/${q.id}`);

    // ------------------------------------------------------------ asking

    let asking = $state(false);
    let sending = $state(false);
    let askError = $state("");
    let note = $state("");
    let seq = 0;
    const blank = () => ({ key: ++seq, sku: "", qty: "1", error: "" });
    let lines = $state([blank(), blank(), blank()]);

    function startAsking() {
        lines = [blank(), blank(), blank()];
        note = "";
        askError = "";
        asking = true;
    }

    /** variantFor finds a product code in the store's catalogue. */
    async function variantFor(sku) {
        try {
            const product = await tradeApi.get(`/api/products/sku/${encodeURIComponent(sku)}`);
            return (product.variants ?? []).find((v) => v.sku.toLowerCase() === sku.toLowerCase()) ?? null;
        } catch (err) {
            if (err.status === 404) return null;
            throw err;
        }
    }

    async function ask(event) {
        event?.preventDefault();
        if (sending) return;
        askError = "";
        const wanted = lines.filter((l) => l.sku.trim());
        if (!wanted.length) {
            askError = "Add at least one product code.";
            return;
        }
        for (const l of wanted) {
            l.error = /^\d+$/.test(String(l.qty ?? "").trim()) && Number(l.qty) >= 1 ? "" : "A whole number, 1 or more.";
        }
        if (wanted.some((l) => l.error)) return;
        sending = true;
        try {
            const found = await Promise.all(wanted.map((l) => variantFor(l.sku.trim())));
            let missing = 0;
            wanted.forEach((l, i) => {
                if (!found[i]) {
                    l.error = "We don't sell anything with this code.";
                    missing++;
                }
            });
            if (missing) {
                askError = `${missing === 1 ? "One code isn't" : `${missing} codes aren't`} in the catalogue. Fix or remove ${missing === 1 ? "it" : "them"} and send again.`;
                return;
            }
            // One line per variant: the engine refuses a variant listed twice,
            // and a buyer who typed it twice means the sum.
            const byVariant = new Map();
            wanted.forEach((l, i) => byVariant.set(found[i].id, (byVariant.get(found[i].id) ?? 0) + Number(l.qty)));
            const quote = await tradeApi.post("/x/b2b/quotes", {
                lines: [...byVariant].map(([variant_id, quantity]) => ({ variant_id, quantity })),
                note: note.trim(),
            });
            asking = false;
            toast.success(`Quote ${quote.number} requested. We'll email you when it's priced.`);
            goto(`${base}/portal/quotes/${quote.id}`);
        } catch (err) {
            if (!err.handled) askError = explain(err);
        } finally {
            sending = false;
        }
    }
</script>

<PortalPage title="Quotes">
    {#snippet actions()}
        {#if trade.me.company.status !== "closed"}
            <button type="button" class="btn portal-press" onclick={startAsking}>
                <i class="ri-add-line" aria-hidden="true"></i>
                <span class="txt">Request a quote</span>
            </button>
        {/if}
    {/snippet}

    <p class="field-help m-b-base portal-intro">
        Ask {trade.store.name || "the store"} for a price on a larger or unusual order. Once it's priced you can accept
        it{trade.me.role === "buyer" ? " — over your approval limit, it goes to an approver first" : ""}.
    </p>

    <div class="portal-tabs m-b-base" role="tablist" aria-label="Which quotes">
        {#each TABS as t (t.value)}
            <button
                type="button"
                role="tab"
                class="portal-tab"
                class:active={list.params.status === t.value}
                aria-selected={list.params.status === t.value}
                onclick={() => list.set({ status: t.value })}>{t.label}</button
            >
        {/each}
    </div>

    {#if failure}
        <div class="alert danger m-b-base portal-notice" role="alert">
            <p>{failure}</p>
            <button type="button" class="btn sm secondary portal-press" onclick={load}><span class="txt">Try again</span></button>
        </div>
    {/if}

    <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && rows.length > 0}>
        <table class="table responsive-table">
            <thead>
                <tr>
                    <th class="col-field-name-id">Quote</th>
                    <th>Lines</th>
                    <th class="col-field-type-number min-width">Total</th>
                    <th class="min-width">Status</th>
                </tr>
            </thead>
            <tbody>
                {#if loading && !rows.length}
                    {#each Array(3) as _, i (i)}
                        <tr><td colspan="4"><span class="skeleton-loader"></span></td></tr>
                    {/each}
                {/if}
                {#each rows as q (q.id)}
                    {@const s = word(QUOTE_WORDS, q.status)}
                    <tr class="handle" tabindex="0" onclick={() => open(q)} onkeydown={(e) => rowKey(e, () => open(q))}>
                        <td class="col-field-name-id" data-name="Quote">
                            <div class="row-name row-name-stacked">
                                <a href="{base}/portal/quotes/{q.id}" class="txt-bold txt-code" onclick={(e) => e.stopPropagation()}>{q.number}</a>
                                <span class="txt-hint txt-sm">
                                    {formatDate(q.created_at, { withTime: false })}{isApprover() && q.requested_by !== trade.account?.id
                                        ? ` · ${q.requested_by_email}`
                                        : ""}
                                </span>
                            </div>
                        </td>
                        <td class="txt-sm" data-name="Lines">
                            {q.lines.length} {q.lines.length === 1 ? "line" : "lines"}{q.note ? ` · ${q.note}` : ""}
                        </td>
                        <td class="col-field-type-number min-width" data-name="Total">
                            {#if q.total}{formatMoney(q.total)}{:else}<span class="txt-hint">Not priced yet</span>{/if}
                        </td>
                        <td class="min-width" data-name="Status">
                            <span class="label {s.tone}">{s.label}</span>
                            {#if q.status === "quoted" && q.expires_at}
                                <div class="txt-hint txt-sm">until {formatDate(q.expires_at, { withTime: false })}</div>
                            {/if}
                        </td>
                    </tr>
                {/each}
                {#if !loading && !rows.length && !failure}
                    <tr>
                        <td colspan="4" class="txt-hint txt-center p-base">
                            {list.params.status ? "No quotes here." : "No quotes yet. Request one for a price on a big order."}
                        </td>
                    </tr>
                {/if}
            </tbody>
        </table>
    </div>

    <footer class="page-footer tw:text-xs tw:text-muted-foreground">
        <Pager {meta} {loading} noun="quote" perPage={list.params.limit} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
        <div class="flex-fill"></div>
    </footer>
</PortalPage>

<Drawer open={asking} size="sm" title="Request a quote" onclose={() => (asking = false)}>
    <form id="quote-form" class="portal-drawer-form" onsubmit={ask} novalidate>
        <p class="field-help m-t-0">Product codes and quantities. {trade.store.name || "The store"} replies with prices.</p>
        {#each lines as l, i (l.key)}
            <div class="portal-quote-line">
                <div class="field portal-grow" class:error={!!l.error && !/number/.test(l.error)}>
                    <label for="q-sku-{l.key}">Product code</label>
                    <input id="q-sku-{l.key}" type="text" autocomplete="off" spellcheck="false" bind:value={l.sku} oninput={() => (l.error = "")} />
                </div>
                <div class="field portal-qty-field" class:error={/number/.test(l.error)}>
                    <label for="q-qty-{l.key}">Quantity</label>
                    <input id="q-qty-{l.key}" type="number" min="1" step="1" inputmode="numeric" bind:value={l.qty} />
                </div>
                <button
                    type="button"
                    class="btn circle sm transparent secondary portal-remove"
                    aria-label="Remove line {i + 1}"
                    onclick={() => (lines = lines.length > 1 ? lines.filter((x) => x.key !== l.key) : [blank()])}
                >
                    <i class="ri-close-line" aria-hidden="true"></i>
                </button>
                {#if l.error}
                    <p class="txt-danger txt-sm portal-line-error">{l.error}</p>
                {/if}
            </div>
        {/each}
        <button type="button" class="btn sm secondary transparent portal-press m-b-sm" onclick={() => lines.push(blank())}>
            <i class="ri-add-line" aria-hidden="true"></i>
            <span class="txt">Add a line</span>
        </button>
        <div class="field">
            <label for="quote-note">Anything the store should know</label>
            <textarea id="quote-note" rows="4" placeholder="Delivery date, volumes over the year, alternatives you'd accept" bind:value={note}></textarea>
        </div>
        {#key askError}
            {#if askError}
                <div class="alert danger m-t-sm portal-refusal" role="alert"><p>{askError}</p></div>
            {/if}
        {/key}
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (asking = false)}>
            <span class="txt">Cancel</span>
        </button>
        <button type="submit" form="quote-form" class="btn portal-press" class:loading={sending} disabled={sending}>
            <span class="txt">Send request</span>
        </button>
    {/snippet}
</Drawer>
