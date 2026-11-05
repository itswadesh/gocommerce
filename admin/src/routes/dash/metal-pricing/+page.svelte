<script>
    /**
     * Metal rates (D188): the day's rate per gram for each metal, and how each
     * design's price is worked out from it. Entering a rate reprices every
     * design made from that metal, so the screen leads with the rates and says
     * what a save did.
     */
    import { can, metalPricing } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { formatMoney, fromMinor, isValidMoney, relativeTime, toMinor } from "$lib/format.js";
    import { rateCode, repriceMessage, scaled, unscaled } from "$lib/metal.js";
    import { toast } from "$lib/toast.svelte.js";
    import { titled } from "$lib/brand.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";

    const missing = $derived(modulesKnown() && !hasModule("metal-pricing"));
    const readable = $derived(can("metal-pricing.read"));
    const writable = $derived(can("metal-pricing.write") && can("catalog.write"));
    const currency = $derived(settings.currency);
    const money = (minor) => formatMoney({ amount_minor: minor, currency });

    let rates = $state([]);
    let typed = $state({});
    let recipes = $state([]);
    let meta = $state({ page: 1, total_pages: 1, total: 0 });
    let loading = $state(true);
    let busy = $state(false);
    let lastFailed = $state([]);
    let rejected = $state([]);

    $effect(() => {
        if (readable && hasModule("metal-pricing")) load();
    });

    async function loadRates() {
        rates = await metalPricing.rates();
        typed = Object.fromEntries(rates.map((r) => [r.code, fromMinor(r.rate_minor, currency)]));
    }

    async function loadRecipes(page = meta.page) {
        const out = await metalPricing.recipes({ page });
        recipes = out.data;
        meta = out.meta;
    }

    async function load() {
        loading = true;
        try {
            await Promise.all([loadRates(), loadRecipes(1)]);
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function saveRate(rate) {
        const value = typed[rate.code];
        if (!isValidMoney(value)) return toast.error("Type the price of one gram, such as 6250");
        await run(async () => {
            const out = await metalPricing.setRate(rate.code, { name: rate.name, rate_minor: toMinor(value, currency) });
            lastFailed = out.failed ?? [];
            toast.success(repriceMessage(out));
        });
    }

    /** Runs a change, shows its error, and always reads back what is now true. */
    async function run(change) {
        if (busy) return;
        busy = true;
        try {
            await change();
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
        await Promise.all([loadRates(), loadRecipes()]).catch((err) => toast.error(err));
    }

    let addingMetal = $state(false);
    let newName = $state("");
    let newRate = $state("");
    const newCode = $derived(rateCode(newName));

    async function addMetal(e) {
        e.preventDefault();
        if (!newCode) return toast.error("Name the metal in English letters, such as Gold 22K");
        if (!isValidMoney(newRate)) return toast.error("Type the price of one gram, such as 6250");
        await run(async () => {
            await metalPricing.setRate(newCode, { name: newName.trim(), rate_minor: toMinor(newRate, currency) });
            toast.success(`${newName.trim()} added`);
            addingMetal = false;
            newName = newRate = "";
        });
    }

    async function removeMetal(rate) {
        await run(async () => {
            await metalPricing.removeRate(rate.code);
            toast.success(`${rate.name} removed`);
        });
    }

    const KINDS = {
        flat: { label: "Fixed amount", hint: "for the piece" },
        per_gram: { label: "Per gram", hint: "per gram" },
        percent: { label: "Percent of metal", hint: "% of the metal's value" },
    };

    const blank = () => ({ sku: "", rate_code: rates[0]?.code ?? "", weight: "", wastage: "", kind: "percent", making: "", stones: "" });
    let editing = $state(false);
    let form = $state(blank());

    function startNew() {
        form = blank();
        editing = true;
    }

    function startEdit(row) {
        form = {
            sku: row.sku,
            rate_code: row.rate_code,
            weight: unscaled(row.weight_mg, 3),
            wastage: unscaled(row.wastage_bp, 2),
            kind: row.making_kind,
            making: row.making_kind === "percent" ? unscaled(row.making_value, 2) : fromMinor(row.making_value, currency),
            stones: fromMinor(row.stones_minor, currency),
        };
        editing = true;
    }

    async function saveRecipe(e) {
        e.preventDefault();
        const weight = scaled(form.weight, 3);
        const wastage = form.wastage === "" ? 0 : scaled(form.wastage, 2);
        const making = form.kind === "percent" ? (form.making === "" ? 0 : scaled(form.making, 2)) : form.making === "" ? 0 : toMinor(form.making, currency);
        const stones = form.stones === "" ? 0 : toMinor(form.stones, currency);
        if (!form.sku.trim()) return toast.error("Type the design's SKU");
        if (!weight) return toast.error("Type the weight in grams, up to 3 decimals, such as 12.345");
        if (wastage === null) return toast.error("Type wastage as a percent, up to 2 decimals, such as 8.5");
        if (making === null) return toast.error("Check the making charge: it should be a number");
        if (stones === null) return toast.error("Check the stones value: it should be a number");
        await run(async () => {
            const out = await metalPricing.setRecipe({
                sku: form.sku.trim(),
                rate_code: form.rate_code,
                weight_mg: weight,
                wastage_bp: wastage,
                making_kind: form.kind,
                making_value: making,
                stones_minor: stones,
            });
            toast.success(`${form.sku.trim()} is now ${money(out.total_minor)}`);
            editing = false;
        });
    }

    async function removeRecipe(row) {
        await run(async () => {
            await metalPricing.removeRecipe(row.variant_id);
            toast.success(`${row.sku} keeps its price and no longer follows the rate`);
        });
    }

    async function repriceAll() {
        await run(async () => {
            const out = await metalPricing.reprice();
            lastFailed = out.failed ?? [];
            toast.success(repriceMessage(out));
        });
    }

    async function importFile(e) {
        const file = e.currentTarget.files?.[0];
        e.currentTarget.value = "";
        if (!file) return;
        const text = await file.text();
        await run(async () => {
            const out = await metalPricing.importRecipes(text);
            rejected = out.rejected ?? [];
            toast.success(`${out.saved === 1 ? "1 design" : `${out.saved} designs`} priced from the file${rejected.length ? `, ${rejected.length} left out` : ""}`);
        });
    }

    function makingText(row) {
        if (row.making_kind === "percent") return `${unscaled(row.making_value, 2)}%`;
        return row.making_kind === "per_gram" ? `${money(row.making_value)} / g` : money(row.making_value);
    }
</script>

<svelte:head><title>{titled("Metal rates")}</title></svelte:head>

<div class="page page-metal shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Products</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Metal rates</h1>
            </nav>
            {#if !missing && readable && writable}
                <div class="page-header-primary-btns">
                    <button type="button" class="btn" onclick={() => (addingMetal = true)}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">Add a metal</span>
                    </button>
                </div>
            {/if}
        </header>

        {#if missing}
            <ModuleMissing module="metal-pricing" what="Metal rates are kept by ext/metal-pricing, and this binary does not include it." />
        {:else if !readable}
            <NoAccess right="metal-pricing.read" what="the store's metal rates" />
        {:else}
            <p class="tw:mb-4 tw:text-sm tw:text-muted-foreground">
                Enter today's rate for one gram and save. Every design made from that metal is repriced straight away. Orders already placed keep their price.
            </p>

            <div class="page-table-wrapper metal-table tw:rounded-xl tw:border tw:mb-8">
                <table class="table responsive-table">
                    <thead>
                        <tr>
                            <th>Metal</th>
                            <th class="min-width">Rate per gram ({currency})</th>
                            <th class="min-width">Designs</th>
                            <th class="min-width">Last changed</th>
                            <th class="min-width"><span class="tw:sr-only">Actions</span></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rates as rate (rate.code)}
                            <tr>
                                <td data-name="Metal"><strong>{rate.name}</strong></td>
                                <td class="min-width" data-name="Rate per gram">
                                    <input class="metal-rate" inputmode="decimal" aria-label="{rate.name} rate per gram" disabled={!writable} bind:value={typed[rate.code]} />
                                </td>
                                <td class="min-width" data-name="Designs">{rate.designs}</td>
                                <td class="min-width txt-hint" data-name="Last changed">{relativeTime(rate.updated_at)}</td>
                                <td class="min-width" data-name="Actions">
                                    {#if writable}
                                        <button type="button" class="btn sm" disabled={busy} onclick={() => saveRate(rate)}><span class="txt">Save rate</span></button>
                                        {#if !rate.designs}
                                            <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => removeMetal(rate)}><span class="txt">Remove</span></button>
                                        {/if}
                                    {/if}
                                </td>
                            </tr>
                        {:else}
                            {#if !loading}
                                <tr><td colspan="5"><EmptyState icon="ri-scales-3-line" title="No metals yet" text="Add the metals you sell, such as Gold 22K, with today's rate for one gram." /></td></tr>
                            {/if}
                        {/each}
                    </tbody>
                </table>
            </div>

            {#if lastFailed.length}
                <div class="alert alert-danger tw:mb-6" role="alert">
                    <p>These designs kept their old price, so they are selling at the wrong rate. Try "Reprice everything", or check them under Products.</p>
                    <ul>
                        {#each lastFailed as f (f.variant_id)}<li>Variant {f.variant_id}: {f.error}</li>{/each}
                    </ul>
                </div>
            {/if}

            <div class="tw:mb-3 tw:flex tw:flex-wrap tw:items-center tw:justify-between tw:gap-3">
                <h2 class="tw:text-lg tw:font-semibold">Designs priced from a rate ({meta.total ?? recipes.length})</h2>
                {#if writable}
                    <div class="tw:flex tw:flex-wrap tw:gap-2">
                        <button type="button" class="btn sm" disabled={busy || !rates.length} onclick={startNew}><span class="txt">Add a design</span></button>
                        <label class="btn sm transparent secondary metal-upload">
                            <span class="txt">Upload a spreadsheet</span>
                            <input type="file" accept=".csv,text/csv" class="tw:sr-only" disabled={busy || !rates.length} onchange={importFile} />
                        </label>
                        <button type="button" class="btn sm transparent secondary" disabled={busy || !recipes.length} onclick={repriceAll}><span class="txt">Reprice everything</span></button>
                    </div>
                {/if}
            </div>

            <p class="tw:mb-3 tw:text-sm tw:text-muted-foreground">
                A price typed by hand on one of these designs is replaced at the next rate change. Trade prices in price lists are fixed amounts and do not follow the rate, so review them when the rate moves.
            </p>

            {#if rejected.length}
                <div class="alert alert-warning tw:mb-4" role="status">
                    <p>These rows were left out of the file. Fix them and upload them again.</p>
                    <ul>
                        {#each rejected as r (r.row)}<li>Row {r.row}{r.sku ? ` (${r.sku})` : ""}: {r.error}</li>{/each}
                    </ul>
                </div>
            {/if}

            <div class="page-table-wrapper metal-table tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead>
                        <tr>
                            <th>SKU</th>
                            <th class="min-width">Metal</th>
                            <th class="min-width">Weight</th>
                            <th class="min-width">Wastage</th>
                            <th class="min-width">Making</th>
                            <th class="min-width">Stones</th>
                            <th class="min-width">Price now</th>
                            <th class="min-width"><span class="tw:sr-only">Actions</span></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each recipes as row (row.variant_id)}
                            <tr>
                                <td data-name="SKU"><strong>{row.sku}</strong></td>
                                <td class="min-width" data-name="Metal">{rates.find((r) => r.code === row.rate_code)?.name ?? row.rate_code}</td>
                                <td class="min-width" data-name="Weight">{unscaled(row.weight_mg, 3)} g</td>
                                <td class="min-width" data-name="Wastage">{unscaled(row.wastage_bp, 2)}%</td>
                                <td class="min-width" data-name="Making">{makingText(row)}</td>
                                <td class="min-width" data-name="Stones">{money(row.stones_minor)}</td>
                                <td class="min-width" data-name="Price now">
                                    <strong>{row.price ? formatMoney(row.price) : "—"}</strong>
                                    {#if row.price && row.price.amount_minor !== row.breakdown.total_minor}
                                        <div class="txt-hint txt-sm">Rate says {money(row.breakdown.total_minor)}</div>
                                    {/if}
                                </td>
                                <td class="min-width" data-name="Actions">
                                    {#if writable}
                                        <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => startEdit(row)}><span class="txt">Edit</span></button>
                                        <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => removeRecipe(row)}><span class="txt">Stop following</span></button>
                                    {/if}
                                </td>
                            </tr>
                        {:else}
                            {#if !loading}
                                <tr><td colspan="8"><EmptyState icon="ri-vip-diamond-line" title="No designs follow a rate yet" text="Add a design by its SKU, or upload a spreadsheet with the columns sku, rate_code, weight_g, wastage_percent, making_kind, making_value and stones_value." /></td></tr>
                            {/if}
                        {/each}
                    </tbody>
                </table>
            </div>

            {#if (meta.total_pages ?? 1) > 1}
                <nav class="tw:mt-3 tw:flex tw:items-center tw:gap-2" aria-label="Designs pages">
                    <button type="button" class="btn sm transparent secondary" disabled={meta.page <= 1} onclick={() => loadRecipes(meta.page - 1)}><span class="txt">Previous</span></button>
                    <span class="txt-hint txt-sm">Page {meta.page} of {meta.total_pages}</span>
                    <button type="button" class="btn sm transparent secondary" disabled={meta.page >= meta.total_pages} onclick={() => loadRecipes(meta.page + 1)}><span class="txt">Next</span></button>
                </nav>
            {/if}
        {/if}
    </div>
</div>

<Drawer open={addingMetal} size="sm" title="Add a metal" onclose={() => (addingMetal = false)}>
    <form id="metal-new" onsubmit={addMetal}>
        <div class="field">
            <label for="metal-name">Name</label>
            <input id="metal-name" type="text" maxlength="60" placeholder="Gold 22K" required bind:value={newName} />
        </div>
        <div class="field m-t-sm">
            <label for="metal-rate">Today's rate for one gram ({currency})</label>
            <input id="metal-rate" inputmode="decimal" required bind:value={newRate} />
        </div>
        {#if newCode}<div class="field-help">In a spreadsheet, this metal's rate_code is <code>{newCode}</code>.</div>{/if}
    </form>
    {#snippet footer()}
        <button type="button" class="btn transparent secondary" onclick={() => (addingMetal = false)}><span class="txt">Cancel</span></button>
        <button type="submit" form="metal-new" class="btn" disabled={busy}><span class="txt">Add metal</span></button>
    {/snippet}
</Drawer>

<Drawer open={editing} size="md" title="Price a design from a rate" onclose={() => (editing = false)}>
    <form id="metal-recipe" onsubmit={saveRecipe}>
        <div class="field">
            <label for="r-sku">SKU</label>
            <input id="r-sku" type="text" required bind:value={form.sku} />
        </div>
        <div class="field m-t-sm">
            <label for="r-rate">Metal</label>
            <Select id="r-rate" bind:value={form.rate_code} options={rates.map((r) => ({ value: r.code, label: r.name }))} />
        </div>
        <div class="field m-t-sm">
            <label for="r-weight">Weight (grams)</label>
            <input id="r-weight" inputmode="decimal" required placeholder="12.345" bind:value={form.weight} />
        </div>
        <div class="field m-t-sm">
            <label for="r-wastage">Wastage (%)</label>
            <input id="r-wastage" inputmode="decimal" placeholder="8" bind:value={form.wastage} />
        </div>
        <div class="field m-t-sm">
            <label for="r-kind">Making charge is</label>
            <Select id="r-kind" bind:value={form.kind} options={Object.entries(KINDS).map(([value, kind]) => ({ value, label: kind.label }))} />
        </div>
        <div class="field m-t-sm">
            <label for="r-making">Making charge ({form.kind === "percent" ? "%" : currency}, {KINDS[form.kind].hint})</label>
            <input id="r-making" inputmode="decimal" bind:value={form.making} />
        </div>
        <div class="field m-t-sm">
            <label for="r-stones">Stones value ({currency})</label>
            <input id="r-stones" inputmode="decimal" bind:value={form.stones} />
        </div>
        <div class="field-help">The total is rounded to a whole {currency}. Saving changes the design's price straight away.</div>
    </form>
    {#snippet footer()}
        <button type="button" class="btn transparent secondary" onclick={() => (editing = false)}><span class="txt">Cancel</span></button>
        <button type="submit" form="metal-recipe" class="btn" disabled={busy}><span class="txt">Save and reprice</span></button>
    {/snippet}
</Drawer>

<style>
    .metal-rate {
        width: 8.5rem;
        font-variant-numeric: tabular-nums;
    }
    /* Two tables share this page, so each takes its full height; the rate field must never be scrolled out of a squeezed box. */
    .metal-table {
        flex: none;
        max-height: none;
        overflow: visible;
    }
    .metal-upload {
        cursor: pointer;
    }
</style>
