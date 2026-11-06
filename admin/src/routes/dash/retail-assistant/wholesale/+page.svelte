<script>
    /**
     * A manufacturer's document into a retailer's quote (D208). The prices come from
     * the engine, worked from the document's own figures; this screen only asks. What
     * the wholesaler paid is shown here and goes nowhere near the quote, which is
     * reviewed and sent on ext/b2b's own screen.
     */
    import { t } from "$lib/retail-lang.svelte.js";
    import RetailLanguage from "$lib/components/RetailLanguage.svelte";
    import { api, can, query, retailAssistant } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { formatMoney, isValidMoney, toMinor } from "$lib/format.js";
    import { newKey, PRICING_RULES, ruleExample, wholeQuantity } from "$lib/retail.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { titled } from "$lib/brand.svelte.js";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";
    import VariantSearch from "$lib/components/VariantSearch.svelte";

    const missing = $derived(modulesKnown() && (!hasModule("retailassistant") || !hasModule("b2b")));
    const allowed = $derived(can("retailassistant.write") && can("quotes.write") && can("companies.read"));
    const currency = $derived(settings.currency);
    const money = (minor) => formatMoney({ amount_minor: minor, currency });

    let docs = $state([]);
    let companies = $state([]);
    let members = $state([]);
    let doc = $state(null);
    let lines = $state([]);
    let company = $state("");
    let buyer = $state("");
    let mode = $state("markup");
    let percent = $state("20");
    let belowCost = $state(false);
    let preview = $state(null);
    let made = $state(null);
    let busy = $state(false);
    let loading = $state(true);
    let key = newKey();

    $effect(() => {
        if (allowed && hasModule("retailassistant") && hasModule("b2b")) load();
    });

    async function load() {
        loading = true;
        try {
            const [waiting, posted, list] = await Promise.all([
                retailAssistant.documents({ status: "needs_review", limit: 50 }),
                retailAssistant.documents({ status: "posted", limit: 50 }),
                api.get("/api/admin/x/b2b/companies" + query({ limit: 100 })),
            ]);
            docs = [...waiting.data, ...posted.data].filter((d) => d.draft?.lines?.length);
            companies = list.data ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function choose(d) {
        doc = d;
        preview = null;
        made = null;
        key = newKey();
        lines = d.draft.lines.map((l, index) => ({ index, description: l.description, cost: l.unit_price_minor, quantity: wholeQuantity(l.quantity), variant: null, price: "", on: true }));
    }

    async function pickCompany() {
        buyer = "";
        members = [];
        if (!company) return;
        try {
            const found = await api.get(`/api/admin/x/b2b/companies/${company}/members`);
            members = found ?? [];
            if (members.length) buyer = String(members[0].customer_id);
        } catch (err) {
            toast.error(err);
        }
    }

    function request() {
        const chosen = lines.filter((l) => l.on);
        if (!chosen.length) throw new Error(t("Choose at least one line"));
        return {
            document_id: doc.id,
            company_id: Number(company),
            buyer_id: Number(buyer),
            mode,
            percent_bps: Math.round(Number(percent) * 100),
            allow_below_cost: belowCost,
            lines: chosen.map((l) => {
                if (!l.variant) throw new Error(t("Choose the product for {name}", { name: l.description || t("Line {n}", { n: l.index + 1 }) }));
                const quantity = Number(l.quantity);
                if (!Number.isInteger(quantity) || quantity <= 0) throw new Error(t("Type a whole quantity for {name}", { name: l.description || t("Line {n}", { n: l.index + 1 }) }));
                const out = { index: l.index, variant_id: l.variant.id, quantity };
                if (String(l.price).trim() !== "") {
                    if (!isValidMoney(l.price)) throw new Error(t("Type a price as an amount, such as 1180.50"));
                    out.price_minor = toMinor(l.price, currency);
                }
                return out;
            }),
        };
    }

    async function run(fn) {
        busy = true;
        try {
            await fn();
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    const showPrices = () =>
        run(async () => {
            preview = await retailAssistant.wholesalePreview(request());
        });

    const makeQuote = () =>
        run(async () => {
            made = await retailAssistant.wholesaleQuote(request(), key);
            toast.success(t("Quote {number} is ready to review", { number: made.number }));
        });

    const companyOptions = $derived(companies.map((c) => ({ value: String(c.id), label: c.name })));
    const memberOptions = $derived(members.map((m) => ({ value: String(m.customer_id), label: m.name || m.email })));
    const anyBelow = $derived(preview?.lines?.some((l) => l.below_cost));
    const canAsk = $derived(!!company && !!buyer && lines.some((l) => l.on));
</script>

<svelte:head><title>{titled(t("Wholesale quote"))}</title></svelte:head>

<div class="page page-retail shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">{t("Retail assistant")}</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">{t("Wholesale quote")}</h1>
            </nav>
            <RetailLanguage />
        </header>

        {#if missing}
            <ModuleMissing module="b2b" what="Wholesale quotes need ext/retailassistant and ext/b2b, and this binary does not include both." />
        {:else if !allowed}
            <NoAccess right="quotes.write" what="wholesale quotes" />
        {:else if made}
            <div class="alert alert-success tw:my-4" role="status" in:rise>
                <p><strong>{t("Quote {number} is drafted for {company}.", { number: made.number, company: made.company_name })}</strong> {t("It has not been sent. Review it, then send it from its own screen.")}</p>
            </div>
            <div class="tw:flex tw:flex-wrap tw:gap-2">
                <a class="btn" href={`/dash/b2b/quotes/${made.id}`}><span class="txt">{t("Review and send")}</span></a>
                <button type="button" class="btn transparent secondary" onclick={() => { doc = null; made = null; preview = null; }}><span class="txt">{t("Make another")}</span></button>
            </div>
        {:else if !doc}
            <p class="tw:mb-4 tw:text-sm tw:text-muted-foreground">
                {t("Choose a manufacturer's bill or price list that arrived on WhatsApp. It stays a document: quoting from it does not put anything on your supplier accounts.")}
            </p>
            <ul class="ws-list">
                {#each docs as d (d.id)}
                    <li>
                        <span>
                            <strong>{d.draft.supplier?.name || d.filename || t("Document {id}", { id: d.id })}</strong>
                            <span class="txt-hint txt-sm">{d.draft.invoice_no || ""} · {d.draft.lines.length === 1 ? t("1 line") : t("{n} lines", { n: d.draft.lines.length })}</span>
                        </span>
                        <button type="button" class="btn sm" onclick={() => choose(d)}><span class="txt">{t("Make a quote")}</span></button>
                    </li>
                {:else}
                    {#if !loading}
                        <li><EmptyState icon="ri-file-list-3-line" title={t("No documents with lines")} text={t("Forward a manufacturer's bill on WhatsApp. A document appears here once its lines have been read.")} /></li>
                    {/if}
                {/each}
            </ul>
        {:else}
            <div class="ws-grid" in:rise>
                <div class="field">
                    <label for="ws-company">{t("Retailer")}</label>
                    <Select id="ws-company" bind:value={company} options={companyOptions} placeholder={t("Choose a retailer")} onchange={pickCompany} />
                </div>
                {#if company}
                    <div class="field">
                        <label for="ws-buyer">{t("Goes to")}</label>
                        {#if memberOptions.length}
                            <Select id="ws-buyer" bind:value={buyer} options={memberOptions} />
                        {:else}
                            <p class="txt-hint txt-sm">{t("This retailer has no buyers yet. Add one under Customers.")}</p>
                        {/if}
                    </div>
                {/if}
                <div class="field">
                    <label for="ws-mode">{t("Price rule")}</label>
                    <Select id="ws-mode" bind:value={mode} options={PRICING_RULES.map((o) => ({ ...o, label: t(o.label) }))} />
                </div>
                <div class="field">
                    <label for="ws-percent">{t("Percent")}</label>
                    <input id="ws-percent" type="number" min="0" max="500" step="0.01" inputmode="decimal" bind:value={percent} />
                    <div class="field-help">{ruleExample(mode, Number(percent), t)}</div>
                </div>
            </div>

            <h2 class="tw:mt-6 tw:mb-2 tw:text-lg tw:font-semibold">{t("Lines")}</h2>
            <ul class="ws-list">
                {#each lines as line (line.index)}
                    <li class="ws-line">
                        <label class="ws-check">
                            <input type="checkbox" bind:checked={line.on} />
                            <span><strong>{line.description || t("Line {n}", { n: line.index + 1 })}</strong> <span class="txt-hint txt-sm">{line.cost != null ? t("You pay {amount}", { amount: money(line.cost) }) : t("You pay an amount not on the bill")}</span></span>
                        </label>
                        {#if line.on}
                            {#if line.variant}
                                <div class="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
                                    <span class="txt-sm">{line.variant.product?.title ?? ""} <span class="txt-code">{line.variant.sku}</span></span>
                                    <button type="button" class="btn sm transparent secondary" onclick={() => (line.variant = null)}><span class="txt">{t("Change")}</span></button>
                                </div>
                            {:else}
                                <VariantSearch id={`ws-variant-${line.index}`} onpick={(row) => (line.variant = row)} />
                            {/if}
                            <div class="ws-pair">
                                <div class="field">
                                    <label for={`ws-qty-${line.index}`}>{t("Quantity")}</label>
                                    <input id={`ws-qty-${line.index}`} type="number" min="1" step="1" inputmode="numeric" bind:value={line.quantity} />
                                </div>
                                <div class="field">
                                    <label for={`ws-price-${line.index}`}>{t("Price instead ({currency})", { currency })}</label>
                                    <input id={`ws-price-${line.index}`} inputmode="decimal" placeholder={line.cost != null ? t("From the rule") : t("Required")} bind:value={line.price} />
                                </div>
                            </div>
                        {/if}
                    </li>
                {/each}
            </ul>

            {#if preview}
                <div class="ws-box" in:rise>
                    <h2 class="tw:mb-2 tw:text-lg tw:font-semibold">{t("Prices")}</h2>
                    <p class="txt-hint txt-sm tw:mb-2">{t("Only you see what you pay and your margin. The retailer sees the price.")}</p>
                    <ul class="ws-list">
                        {#each preview.lines as p (p.index)}
                            <li>
                                <span>{p.description} <span class="txt-hint txt-sm">× {p.quantity}</span>
                                    {#if p.below_cost}<span class="label danger">{t("Below what you pay")}</span>{/if}
                                    {#if p.overridden}<span class="label">{t("Typed")}</span>{/if}
                                </span>
                                <span>
                                    <strong>{money(p.price_minor)}</strong>
                                    {#if p.margin_bps != null}<span class="txt-hint txt-sm">{t("{n}% margin", { n: (p.margin_bps / 100).toFixed(2) })}</span>{/if}
                                </span>
                            </li>
                        {/each}
                    </ul>
                    {#if anyBelow}
                        <label class="ws-check tw:mt-2">
                            <input type="checkbox" bind:checked={belowCost} />
                            <span>{t("I mean to sell below what I pay.")}</span>
                        </label>
                    {/if}
                </div>
            {/if}

            <div class="tw:mt-4 tw:flex tw:flex-wrap tw:gap-2">
                <button type="button" class="btn transparent secondary" disabled={busy || !canAsk} onclick={showPrices}><span class="txt">{t("Show prices")}</span></button>
                <button type="button" class="btn" class:loading={busy} disabled={busy || !canAsk || !preview || (anyBelow && !belowCost)} onclick={makeQuote}><span class="txt">{t("Draft the quote")}</span></button>
                <button type="button" class="btn transparent secondary" onclick={() => (doc = null)}><span class="txt">{t("Back")}</span></button>
            </div>
        {/if}
    </div>
</div>

<style>
    .ws-list {
        list-style: none;
        margin: 0;
        padding: 0;
    }
    .ws-list > li {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: 6px 12px;
        padding: 10px 0;
        border-top: 1px solid var(--color-border, #e5e5e5);
    }
    .ws-list > li.ws-line {
        display: block;
    }
    .ws-grid {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(14rem, 1fr));
        gap: 12px;
    }
    .ws-pair {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(9rem, 1fr));
        gap: 8px;
        margin-top: 8px;
    }
    .ws-check {
        display: flex;
        align-items: center;
        gap: 8px;
        min-height: 44px;
        cursor: pointer;
    }
    .ws-box {
        margin-top: 16px;
        padding: 14px;
        border: 1px solid var(--color-border, #e5e5e5);
        border-radius: 10px;
    }
</style>
