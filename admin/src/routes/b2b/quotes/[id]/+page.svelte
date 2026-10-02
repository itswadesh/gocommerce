<script>
    /**
     * One quote: what the buyer asked for, and the price the store answers with.
     *
     * Prices are typed as decimals and sent as integer minor units, line by
     * line. A box left empty is an unpriced line rather than a free one, and
     * the quote cannot be sent until every line has a price — the engine
     * refuses it too, but a Send button that is enabled and then fails is a
     * question the screen could have answered first.
     *
     * Editing a quote that has already been sent takes it back to requested.
     * That is the engine's rule (the buyer must not accept a price being
     * rewritten under them), so the screen says it before Save is pressed, and
     * "Send again" saves and sends in one go.
     *
     * The list price beside each line is the variant's own price, for
     * reference: the figure a buyer asking "can you do better than list" means.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { api, can } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import {
        currencySymbol,
        formatDate,
        formatMoney,
        fromMinor,
        isValidMoney,
        parseMoney,
        toMinor,
    } from "$lib/format.js";
    import { QUOTE_STATUSES, dateInputValue, endOfDay, quoteStatusClass, quoteStatusLabel } from "$lib/b2b.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import DirtyGuard from "$lib/components/DirtyGuard.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import VariantSearch from "$lib/components/VariantSearch.svelte";

    const id = $derived(page.params.id);
    const readable = $derived(can("quotes.read"));
    const writable = $derived(can("quotes.write"));
    const missing = $derived(modulesKnown() && !hasModule("b2b"));

    let quote = $state(null);
    let loading = $state(true);
    let notFound = $state(false);
    let form = $state({ lines: [], reply: "", expiry: "" });
    let snapshot = $state("");
    let listPrices = $state({});
    let saving = $state(false);
    let sending = $state(false);
    let declineOpen = $state(false);

    const currency = $derived(
        quote?.lines?.find((l) => l.unit_price)?.unit_price.currency ?? quote?.total?.currency ?? settings.currency,
    );
    const symbol = $derived(currencySymbol(currency));
    const editable = $derived(writable && ["requested", "quoted", "expired"].includes(quote?.status));
    const declinable = $derived(writable && ["requested", "quoted", "expired"].includes(quote?.status));

    $effect(() => {
        id;
        if (readable && hasModule("b2b")) load();
    });

    async function load() {
        loading = true;
        notFound = false;
        try {
            adopt(await api.get(`/api/admin/x/b2b/quotes/${id}`));
            loadListPrices();
        } catch (err) {
            if (err.status === 404) notFound = true;
            else toast.error(err);
        } finally {
            loading = false;
        }
    }

    /** Takes the engine's quote as the truth, and the form back to it. */
    function adopt(q) {
        quote = q;
        const cur = q.lines.find((l) => l.unit_price)?.unit_price.currency ?? q.total?.currency ?? settings.currency;
        form = {
            lines: q.lines.map((l) => ({
                variant_id: l.variant_id,
                sku: l.sku,
                title: l.title,
                quantity: l.quantity,
                price: l.unit_price ? fromMinor(l.unit_price.amount_minor, cur) : "",
            })),
            reply: q.reply ?? "",
            expiry: dateInputValue(q.expires_at),
        };
        snapshot = fingerprint(form);
    }

    function fingerprint(f) {
        return JSON.stringify({
            lines: f.lines.map((l) => [l.variant_id, String(l.quantity ?? ""), String(l.price ?? "").trim()]),
            reply: f.reply.trim(),
            expiry: f.expiry,
        });
    }

    /* One request per variant. There is no "these ids" route, and a quote is
       a handful of lines; the vendors screen made the same trade. */
    async function loadListPrices() {
        const wanted = [...new Set(form.lines.map((l) => l.variant_id))].filter((v) => !(v in listPrices));
        const found = {};
        await Promise.all(
            wanted.map(async (variantId) => {
                try {
                    found[variantId] = (await api.get(`/api/variants/${variantId}`))?.price ?? null;
                } catch {
                    found[variantId] = null;
                }
            }),
        );
        listPrices = { ...listPrices, ...found };
    }

    const dirty = $derived(!!quote && fingerprint(form) !== snapshot);

    /* Each line's state in one place: what the boxes say, read as the engine
       will read them. `minor` is null for an empty box and undefined for one
       that does not hold a number. */
    const lines = $derived(
        form.lines.map((l) => {
            const text = String(l.price ?? "").trim();
            let minor = null;
            if (text) minor = isValidMoney(text) && parseMoney(text) >= 0 ? toMinor(text, currency) : undefined;
            const qty = Number(l.quantity);
            const qtyOK = Number.isInteger(qty) && qty >= 1;
            return {
                minor,
                qtyOK,
                priceError: minor === undefined,
                total: minor != null && qtyOK ? minor * qty : null,
            };
        }),
    );
    const allPriced = $derived(lines.length > 0 && lines.every((l) => l.total !== null));
    const priced = $derived(lines.filter((l) => l.minor != null).length);
    const totalMinor = $derived(lines.reduce((n, l) => n + (l.total ?? 0), 0));
    const linesValid = $derived(lines.length > 0 && lines.every((l) => l.qtyOK && !l.priceError));

    const expiryError = $derived.by(() => {
        if (!form.expiry) {
            // The engine leaves an absent expiry alone rather than clearing it,
            // so emptying the box would be a change that silently does nothing.
            return quote?.expires_at ? "An expiry cannot be taken off once set; pick a date." : "";
        }
        const at = endOfDay(form.expiry);
        if (!at) return "Pick a date.";
        return new Date(at) < new Date() ? "Pick a day from today on." : "";
    });

    const valid = $derived(linesValid && !expiryError);
    const canSave = $derived(editable && dirty && valid && !saving && !sending);
    const canSend = $derived(
        editable &&
            valid &&
            allPriced &&
            !saving &&
            !sending &&
            (quote?.status === "requested" || quote?.status === "expired" || dirty),
    );

    function body() {
        const out = {
            lines: form.lines.map((l, i) => ({
                variant_id: l.variant_id,
                quantity: Number(l.quantity),
                unit_price_minor: lines[i].minor,
            })),
            reply: form.reply,
        };
        if (form.expiry && form.expiry !== dateInputValue(quote.expires_at)) out.expires_at = endOfDay(form.expiry);
        return out;
    }

    async function save() {
        if (!canSave) return;
        saving = true;
        try {
            adopt(await api.put(`/api/admin/x/b2b/quotes/${id}`, body()));
            toast.success(`${quote.number} saved`);
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    /* Saving first whenever the engine needs it to: unsaved boxes, or a sent
       or lapsed quote, which only a save brings back to requested. */
    async function send() {
        if (!canSend) return;
        sending = true;
        try {
            if (dirty || quote.status !== "requested") {
                adopt(await api.put(`/api/admin/x/b2b/quotes/${id}`, body()));
            }
            adopt(await api.post(`/api/admin/x/b2b/quotes/${id}/send`));
            toast.success(`${quote.number} sent to ${quote.company_name}`);
        } catch (err) {
            toast.error(err);
        } finally {
            sending = false;
        }
    }

    async function decline() {
        try {
            adopt(await api.post(`/api/admin/x/b2b/quotes/${id}/decline`));
            toast.success(`${quote.number} declined`);
        } catch (err) {
            toast.error(err);
        }
    }

    function addLine(row) {
        if (form.lines.some((l) => l.variant_id === row.id)) {
            toast.error(`${row.sku} is already on this quote.`);
            return;
        }
        form.lines.push({
            variant_id: row.id,
            sku: row.sku,
            title: row.product?.title ?? "",
            quantity: 1,
            price: "",
        });
        listPrices = { ...listPrices, [row.id]: row.price ?? null };
    }

    function removeLine(index) {
        form.lines.splice(index, 1);
    }

    /* Only the empty boxes: a price somebody typed is a decision, and this
       button is a starting point, not a reset. */
    function fillFromList() {
        for (const l of form.lines) {
            const list = listPrices[l.variant_id];
            if (!String(l.price ?? "").trim() && list) l.price = fromMinor(list.amount_minor, currency);
        }
    }

    const canFill = $derived(
        editable && form.lines.some((l) => !String(l.price ?? "").trim() && listPrices[l.variant_id]),
    );
</script>

<svelte:head><title>{quote?.number ?? "Quote"} · GoCommerce</title></svelte:head>

<DirtyGuard dirty={dirty && editable} />

{#if !readable}
    <NoAccess right="quotes.read" what="quotes" />
{:else}
    <div class="page page-quote b2b-page shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <a class="breadcrumb-item tw:text-sm" href="{base}/b2b/quotes">Quotes</a>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight txt-code">
                        {quote?.number ?? "…"}
                    </div>
                </nav>
                <div class="flex-fill"></div>
                {#if quote && (editable || declinable)}
                    <div class="page-header-primary-btns">
                        {#if dirty && editable}
                            <span class="label b2b-notice b2b-unsaved">Unsaved changes</span>
                        {/if}
                        {#if declinable}
                            <button
                                type="button"
                                class="btn transparent danger"
                                aria-label="Decline"
                                disabled={saving || sending}
                                onclick={() => (declineOpen = true)}
                            >
                                <i class="ri-close-circle-line" aria-hidden="true"></i>
                                <span class="txt">Decline</span>
                            </button>
                        {/if}
                        {#if editable}
                            <button
                                type="button"
                                class="btn secondary"
                                class:loading={saving}
                                aria-label="Save"
                                disabled={!canSave}
                                onclick={save}
                            >
                                <i class="ri-save-3-line" aria-hidden="true"></i>
                                <span class="txt">Save</span>
                            </button>
                            <button
                                type="button"
                                class="btn"
                                class:loading={sending}
                                disabled={!canSend}
                                title={allPriced ? "" : "Every line needs a price before the quote can be sent"}
                                aria-label={quote.status === "requested" ? "Send quote" : "Send again"}
                                onclick={send}
                            >
                                <i class="ri-send-plane-line" aria-hidden="true"></i>
                                <span class="txt">{quote.status === "requested" ? "Send quote" : "Send again"}</span>
                            </button>
                        {/if}
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="b2b" what="Quotes are served by ext/b2b, and this binary does not have it." />
            {:else if notFound}
                <div class="wrapper sm m-auto txt-center p-t-base">
                    <h5 class="m-b-xs">This quote does not exist</h5>
                    <a href="{base}/b2b/quotes" class="btn secondary m-t-sm"><span class="txt">All quotes</span></a>
                </div>
            {:else if loading && !quote}
                <span class="skeleton-loader"></span>
                <span class="skeleton-loader"></span>
            {:else if quote}
                {#if quote.status === "accepted"}
                    <div class="alert success m-b-base b2b-notice">
                        <p>
                            <strong>Accepted {formatDate(quote.accepted_at)}.</strong>
                            {#if quote.order_id}
                                It became order
                                <a href="{base}/orders/{quote.order_id}" class="txt-code txt-bold">{quote.order_number}</a>,
                                placed at these prices.
                            {/if}
                        </p>
                    </div>
                {:else if quote.status === "declined"}
                    <div class="alert danger m-b-base b2b-notice">
                        <p>
                            <strong>Declined</strong>
                            {quote.declined_by === "store" ? "by the store" : quote.declined_by ? `by ${quote.declined_by}` : ""}.
                            It is closed; the buyer would have to ask again.
                        </p>
                    </div>
                {:else if quote.status === "cancelled"}
                    <div class="alert m-b-base b2b-notice"><p><strong>Cancelled.</strong> It is closed.</p></div>
                {:else if dirty && quote.status !== "requested" && editable}
                    <div class="alert warning m-b-base b2b-notice">
                        <p>
                            This quote was sent {formatDate(quote.quoted_at)}. Saving a change takes it back to
                            requested, so the buyer cannot accept a price you are still changing — send it
                            again when you are done.
                        </p>
                    </div>
                {:else if quote.status === "quoted"}
                    <div class="alert info m-b-base b2b-notice">
                        <p>
                            Sent {formatDate(quote.quoted_at)}. {quote.company_name} can accept it until
                            {formatDate(quote.expires_at)}.
                        </p>
                    </div>
                {:else if quote.status === "expired"}
                    <div class="alert warning m-b-base b2b-notice">
                        <p>
                            Expired {formatDate(quote.expires_at)} without being accepted. Pick a new expiry
                            and send it again to re-offer it.
                        </p>
                    </div>
                {/if}

                <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base">
                    <div class="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
                        <span class="label {quoteStatusClass(quote.status)}">{quoteStatusLabel(quote.status)}</span>
                        <span class="txt-hint txt-sm">{QUOTE_STATUSES.find((s) => s.value === quote.status)?.hint ?? ""}</span>
                    </div>
                    <dl class="b2b-facts">
                        <div>
                            <dt>Company</dt>
                            <dd>
                                {#if can("companies.read")}
                                    <a href="{base}/b2b/companies/{quote.company_id}">{quote.company_name}</a>
                                {:else}
                                    {quote.company_name}
                                {/if}
                            </dd>
                        </div>
                        <div>
                            <dt>Asked by</dt>
                            <dd>{quote.requested_by_email || "a former buyer"}</dd>
                        </div>
                        <div>
                            <dt>Asked</dt>
                            <dd>{formatDate(quote.created_at)}</dd>
                        </div>
                    </dl>
                    <h3 class="b2b-eyebrow">What they wrote</h3>
                    {#if quote.note}
                        <blockquote class="b2b-note">{quote.note}</blockquote>
                    {:else}
                        <p class="txt-hint tw:m-0 tw:text-sm">No note — just the lines below.</p>
                    {/if}
                </section>

                <div class="b2b-section-head">
                    <h2 class="section-title">
                        <i class="ri-list-check-3" aria-hidden="true"></i>
                        Lines
                    </h2>
                    {#if canFill}
                        <button type="button" class="btn sm secondary transparent" onclick={fillFromList}>
                            <span class="txt">Fill empty prices from list</span>
                        </button>
                    {/if}
                </div>

                <div class="page-table-wrapper tw:rounded-xl tw:border m-b-sm">
                    <table class="table responsive-table b2b-quote-lines">
                        <thead>
                            <tr>
                                <th class="col-field-name-id">Item</th>
                                <th class="col-field-type-number min-width">Quantity</th>
                                <th class="col-field-type-number min-width">Unit price</th>
                                <th class="col-field-type-number min-width">Line total</th>
                                {#if editable && form.lines.length > 1}<th class="min-width" aria-label="Remove"></th>{/if}
                            </tr>
                        </thead>
                        <tbody>
                            {#each form.lines as line, i (line.variant_id)}
                                <tr>
                                    <td class="col-field-name-id" data-name="Item">
                                        <div class="row-name row-name-stacked">
                                            <span class="txt-bold">{line.title || `Variant ${line.variant_id}`}</span>
                                            <span class="txt-hint txt-sm txt-code">{line.sku}</span>
                                        </div>
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Quantity">
                                        {#if editable}
                                            <div class="field b2b-qty" class:error={!lines[i]?.qtyOK}>
                                                <input
                                                    type="number"
                                                    min="1"
                                                    step="1"
                                                    aria-label="Quantity of {line.sku}"
                                                    bind:value={line.quantity}
                                                />
                                            </div>
                                        {:else}
                                            {line.quantity}
                                        {/if}
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Unit price">
                                        {#if editable}
                                            <div class="field money-field b2b-price" class:error={lines[i]?.priceError}>
                                                <span class="money-prefix" aria-hidden="true">{symbol}</span>
                                                <input
                                                    type="text"
                                                    inputmode="decimal"
                                                    autocomplete="off"
                                                    placeholder="Not priced"
                                                    aria-label="Unit price for {line.sku} ({currency})"
                                                    aria-invalid={lines[i]?.priceError}
                                                    bind:value={line.price}
                                                />
                                            </div>
                                        {:else if lines[i]?.minor != null}
                                            {formatMoney({ amount_minor: lines[i].minor, currency })}
                                        {:else}
                                            <span class="txt-hint">Not priced</span>
                                        {/if}
                                        {#if listPrices[line.variant_id]}
                                            <div class="txt-hint txt-sm">list {formatMoney(listPrices[line.variant_id])}</div>
                                        {/if}
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Line total">
                                        {#if lines[i]?.total != null}
                                            {formatMoney({ amount_minor: lines[i].total, currency })}
                                        {:else}
                                            <span class="txt-hint">—</span>
                                        {/if}
                                    </td>
                                    {#if editable && form.lines.length > 1}
                                        <td class="min-width b2b-actions">
                                            <button
                                                type="button"
                                                class="btn circle sm transparent danger"
                                                title="Take {line.sku} off the quote"
                                                aria-label="Take {line.sku} off the quote"
                                                onclick={() => removeLine(i)}
                                            >
                                                <i class="ri-close-line" aria-hidden="true"></i>
                                            </button>
                                        </td>
                                    {/if}
                                </tr>
                            {/each}
                        </tbody>
                    </table>
                </div>

                <div class="b2b-total m-b-base" aria-live="polite">
                    {#if allPriced}
                        <span class="txt-hint">Total</span>
                        <strong class="b2b-total-value">{formatMoney({ amount_minor: totalMinor, currency })}</strong>
                    {:else}
                        <span class="txt-hint">
                            {priced} of {form.lines.length} lines priced{#if priced}
                                · {formatMoney({ amount_minor: totalMinor, currency })} so far{/if}
                        </span>
                    {/if}
                </div>

                {#if editable && can("catalog.read")}
                    <div class="field m-b-base">
                        <label for="quote-add">Add a line — something they forgot, or an alternative</label>
                        <VariantSearch id="quote-add" disabled={saving || sending} onpick={addLine} />
                    </div>
                {/if}

                <h2 class="section-title">
                    <i class="ri-reply-line" aria-hidden="true"></i>
                    Your answer
                </h2>
                {#if editable}
                    <div class="field">
                        <label for="quote-reply">Reply</label>
                        <textarea
                            id="quote-reply"
                            rows="4"
                            placeholder="Terms, lead time, anything the prices need explaining"
                            bind:value={form.reply}
                        ></textarea>
                    </div>
                    <div class="field-help">The buyer reads this with the prices.</div>

                    <div class="field m-t-sm b2b-expiry" class:error={!!expiryError}>
                        <label for="quote-expiry">Open until</label>
                        <input id="quote-expiry" type="date" bind:value={form.expiry} />
                    </div>
                    <div class="field-help m-b-base">
                        {#if expiryError}
                            <span class="txt-danger">{expiryError}</span>
                        {:else}
                            The buyer can accept it until the end of this day. Left empty, it stays open for
                            the store's standard time once sent — thirty days unless this store set its own.
                        {/if}
                    </div>
                {:else}
                    <div class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base">
                        {#if quote.reply}
                            <p class="tw:m-0 tw:text-sm tw:whitespace-pre-line">{quote.reply}</p>
                        {:else}
                            <p class="txt-hint tw:m-0 tw:text-sm">No reply was written.</p>
                        {/if}
                        {#if quote.expires_at}
                            <p class="txt-hint tw:mt-3 tw:mb-0 tw:text-sm">Open until {formatDate(quote.expires_at)}.</p>
                        {/if}
                    </div>
                {/if}
            {/if}
        </div>
    </div>

    <Confirm
        bind:open={declineOpen}
        title="Decline {quote?.number ?? 'this quote'}?"
        message="It closes without an order and cannot be reopened. The buyer sees it as declined and would have to ask again."
        confirmLabel="Decline"
        danger
        onconfirm={decline}
    />
{/if}
