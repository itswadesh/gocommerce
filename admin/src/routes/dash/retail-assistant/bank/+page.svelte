<script>
    /**
     * A bank statement against the supplier ledger (D206). A screenshot is not proof
     * money moved; a statement line is, so matching one is what verifies a payment.
     * The engine suggests by exact amount and nearby date, and a person says which.
     */
    import { can, retailAssistant } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatMoney } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { titled } from "$lib/brand.svelte.js";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";

    const FILTERS = [
        { value: "unmatched", label: "To match" },
        { value: "matched", label: "Matched" },
        { value: "ignored", label: "Ignored" },
    ];

    const missing = $derived(modulesKnown() && !hasModule("retailassistant"));
    const readable = $derived(can("retailassistant.read"));
    const writable = $derived(can("retailassistant.write"));

    let enabled = $state(true);
    let filter = $state("unmatched");
    let lines = $state([]);
    let suppliers = $state([]);
    let chosen = $state({});
    let result = $state(null);
    let loading = $state(true);
    let busy = $state(false);

    $effect(() => {
        if (readable && hasModule("retailassistant")) load();
    });

    async function load() {
        loading = true;
        try {
            enabled = (await retailAssistant.status()).enabled;
            if (enabled) await refresh();
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function refresh() {
        const [page, all] = await Promise.all([retailAssistant.bankLines({ status: filter, limit: 50 }), retailAssistant.suppliers()]);
        lines = page.data;
        suppliers = all;
    }

    async function run(fn, message) {
        busy = true;
        try {
            await fn();
            if (message) toast.success(message);
            await refresh();
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    const show = (next) => {
        filter = next;
        return run(() => {});
    };

    async function upload(e) {
        const file = e.currentTarget.files?.[0];
        e.currentTarget.value = "";
        if (!file) return;
        await run(async () => {
            result = await retailAssistant.importStatement(await file.text());
        });
    }

    const supplierOptions = $derived(suppliers.map((s) => ({ value: String(s.id), label: s.name })));
</script>

<svelte:head><title>{titled("Bank statement")}</title></svelte:head>

<div class="page page-retail shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Retail assistant</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Bank statement</h1>
            </nav>
        </header>

        {#if missing}
            <ModuleMissing module="retailassistant" what="Bank statements are matched by ext/retailassistant, and this binary does not include it." />
        {:else if !readable}
            <NoAccess right="retailassistant.read" what="the store's supplier accounts" />
        {:else if !enabled}
            <EmptyState icon="ri-bank-line" title="The retail assistant is off" text="Switch it on under Supplier bills to match bank statements." />
        {:else}
            <p class="tw:mb-4 tw:text-sm tw:text-muted-foreground">
                Download your statement from your bank as a CSV and upload it here. Money going out is matched to the payments you recorded, and a match marks a payment as verified. If your bank only gives Excel, open it and save it as CSV.
            </p>

            {#if writable}
                <div class="tw:mb-4">
                    <label class="btn" class:loading={busy} for="ra-statement">
                        <i class="ri-upload-2-line" aria-hidden="true"></i>
                        <span class="txt">Upload statement</span>
                    </label>
                    <input id="ra-statement" class="tw:sr-only" type="file" accept=".csv,text/csv" disabled={busy} onchange={upload} />
                </div>
            {/if}

            {#if result}
                <div class="alert alert-{result.unreadable ? 'warning' : 'success'} tw:mb-4" role="status" in:rise>
                    <p>
                        {result.added} new {result.added === 1 ? "line" : "lines"} added.
                        {#if result.already_here}{result.already_here} already here.{/if}
                        {#if result.unreadable === 1}1 row could not be read and was left out, so check it against the file.{:else if result.unreadable}{result.unreadable} rows could not be read and were left out, so check them against the file.{/if}
                    </p>
                </div>
            {/if}

            <div class="tw:my-4 tw:flex tw:flex-wrap tw:gap-2" role="group" aria-label="Show lines">
                {#each FILTERS as f (f.value)}
                    <button type="button" class="btn sm" class:transparent={filter !== f.value} class:secondary={filter !== f.value} aria-pressed={filter === f.value} disabled={busy} onclick={() => show(f.value)}>
                        <span class="txt">{f.label}</span>
                    </button>
                {/each}
            </div>

            <ul class="bank-list">
                {#each lines as line (line.id)}
                    <li class="bank-line" in:rise>
                        <div class="bank-head">
                            <span><strong>{formatMoney(line.amount)}</strong> <span class="txt-hint txt-sm">{line.date}</span></span>
                            {#if line.status === "matched"}<span class="label success">Matched</span>{/if}
                            {#if line.status === "ignored"}<span class="label">Ignored</span>{/if}
                        </div>
                        <div class="txt-sm bank-narration">{line.narration || line.reference || "No description"}</div>

                        {#if line.status === "unmatched" && writable}
                            {#each line.candidates as c (c.payment_id)}
                                <div class="bank-choice">
                                    <span class="txt-sm">
                                        {c.supplier_name}, paid {c.paid_on}{c.reference ? ` · ${c.reference}` : ""}
                                        {#if c.same_reference}<span class="label success">Same reference</span>{/if}
                                        {#if c.status === "provisional"}<span class="label warning">Not verified</span>{/if}
                                    </span>
                                    <button type="button" class="btn sm" disabled={busy} onclick={() => run(() => retailAssistant.matchLine(line.id, { payment_id: c.payment_id }), "Matched. The payment is verified.")}><span class="txt">This is it</span></button>
                                </div>
                            {/each}
                            {#if !line.candidates.length}
                                <p class="txt-hint txt-sm tw:mt-1">No recorded payment has this amount within a week of this date.</p>
                            {/if}
                            <div class="bank-actions">
                                {#if supplierOptions.length}
                                    <div class="bank-record">
                                        <Select id={`ra-supplier-${line.id}`} ariaLabel="Supplier paid" bind:value={chosen[line.id]} options={supplierOptions} />
                                        <button type="button" class="btn sm transparent secondary" disabled={busy || !chosen[line.id]} onclick={() => run(() => retailAssistant.recordFromLine(line.id, { supplier_id: Number(chosen[line.id]) }), "Recorded as a verified payment")}><span class="txt">Record as paid to them</span></button>
                                    </div>
                                {/if}
                                <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => run(() => retailAssistant.ignoreLine(line.id), "Ignored")}><span class="txt">Not a supplier payment</span></button>
                            </div>
                        {:else if line.status !== "unmatched" && writable}
                            <div class="bank-actions">
                                <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => run(() => retailAssistant.undoLine(line.id), "Undone")}><span class="txt">Undo</span></button>
                            </div>
                        {/if}
                    </li>
                {:else}
                    {#if !loading}
                        <li><EmptyState icon="ri-bank-line" title="Nothing here" text="Upload a statement to see its payments here." /></li>
                    {/if}
                {/each}
            </ul>
        {/if}
    </div>
</div>

<style>
    .bank-list {
        list-style: none;
        margin: 0;
        padding: 0;
    }
    .bank-line {
        padding: 14px 0;
        border-top: 1px solid var(--color-border, #e5e5e5);
    }
    .bank-head,
    .bank-choice,
    .bank-actions,
    .bank-record {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 8px 12px;
    }
    .bank-head,
    .bank-choice {
        justify-content: space-between;
    }
    .bank-choice {
        margin-top: 8px;
    }
    .bank-actions {
        margin-top: 10px;
    }
    .bank-record :global(.select-trigger) {
        min-width: 10rem;
    }
    .bank-narration {
        overflow-wrap: anywhere;
    }
    label.btn:focus-within {
        outline: 2px solid currentColor;
        outline-offset: 2px;
    }
</style>
