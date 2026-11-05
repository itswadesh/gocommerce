<script>
    /**
     * What the store owes each supplier (D192). The figures are the engine's:
     * outstanding is bills less credit notes less verified payments allocated.
     * The screen records and allocates; it never adds anything up itself.
     */
    import { can, retailAssistant } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { formatMoney, fromMinor, isValidMoney, toMinor } from "$lib/format.js";
    import { CREDIT_KINDS, moneyNote, newKey } from "$lib/retail.js";
    import { toast } from "$lib/toast.svelte.js";
    import { titled } from "$lib/brand.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";

    const missing = $derived(modulesKnown() && !hasModule("retailassistant"));
    const readable = $derived(can("retailassistant.read"));
    const writable = $derived(can("retailassistant.write"));
    const currency = $derived(settings.currency);

    let enabled = $state(true);
    let suppliers = $state([]);
    let loading = $state(true);
    let busy = $state(false);
    let open = $state(null);
    let bills = $state([]);
    let payments = $state([]);
    let credits = $state([]);
    let payment = $state(blankPayment());
    let credit = $state(blankCredit());
    let allocating = $state(null);

    function today() {
        return new Date().toISOString().slice(0, 10);
    }
    function blankPayment() {
        return { amount: "", paid_on: today(), method: "", reference: "", verified: false, key: newKey() };
    }
    function blankCredit() {
        return { amount: "", kind: "return", credit_no: "", key: newKey() };
    }

    $effect(() => {
        if (readable && hasModule("retailassistant")) load();
    });

    async function load() {
        loading = true;
        try {
            enabled = (await retailAssistant.status()).enabled;
            if (enabled) suppliers = await retailAssistant.suppliers();
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function loadAccount(row) {
        const id = row.id;
        const [b, p, c, all] = await Promise.all([
            retailAssistant.payables({ supplier_id: id, limit: 100 }),
            retailAssistant.payments({ supplier_id: id }),
            retailAssistant.credits({ supplier_id: id }),
            retailAssistant.suppliers(),
        ]);
        bills = b.data;
        payments = p;
        credits = c;
        suppliers = all;
        open = all.find((s) => s.id === id) ?? row;
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

    const show = (row) => run(() => loadAccount(row));
    const openBills = $derived(bills.filter((b) => !b.reversed && b.remaining.amount_minor > 0));
    const usable = (e) => !e.reverses_id && !e.reversed && e.unallocated.amount_minor > 0;

    function amountOf(value) {
        if (!isValidMoney(value) || toMinor(value, currency) <= 0) throw new Error("Type an amount above zero, such as 1180.50");
        return toMinor(value, currency);
    }

    async function addPayment(e) {
        e.preventDefault();
        await run(async () => {
            await retailAssistant.addPayment(
                { supplier_id: open.id, amount_minor: amountOf(payment.amount), paid_on: payment.paid_on, method: payment.method.trim(), reference: payment.reference.trim(), verified: payment.verified },
                payment.key,
            );
            toast.success(payment.verified ? "Payment recorded" : "Payment recorded as not verified");
            payment = blankPayment();
            await loadAccount(open);
        });
    }

    async function addCredit(e) {
        e.preventDefault();
        await run(async () => {
            await retailAssistant.addCredit({ supplier_id: open.id, amount_minor: amountOf(credit.amount), kind: credit.kind, credit_no: credit.credit_no.trim() }, credit.key);
            toast.success("Credit note recorded");
            credit = blankCredit();
            await loadAccount(open);
        });
    }

    function startAllocate(kind, entry) {
        const first = openBills[0];
        allocating = {
            kind,
            entry,
            invoice: first ? String(first.id) : "",
            amount: first ? fromMinor(Math.min(entry.unallocated.amount_minor, first.remaining.amount_minor), currency) : "",
            key: newKey(),
        };
    }

    async function allocate(e) {
        e.preventDefault();
        await run(async () => {
            const body = { invoice_id: Number(allocating.invoice), amount_minor: amountOf(allocating.amount) };
            const fn = allocating.kind === "payment" ? retailAssistant.allocatePayment : retailAssistant.allocateCredit;
            await fn(allocating.entry.id, body, allocating.key);
            toast.success("Allocated");
            allocating = null;
            await loadAccount(open);
        });
    }

    const act = (fn, message) =>
        run(async () => {
            await fn();
            toast.success(message);
            await loadAccount(open);
        });
</script>

<svelte:head><title>{titled("Suppliers")}</title></svelte:head>

<div class="page page-retail shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Retail assistant</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Suppliers</h1>
            </nav>
        </header>

        {#if missing}
            <ModuleMissing module="retailassistant" what="Supplier accounts are kept by ext/retailassistant, and this binary does not include it." />
        {:else if !readable}
            <NoAccess right="retailassistant.read" what="the store's supplier accounts" />
        {:else if !enabled}
            <EmptyState icon="ri-whatsapp-line" title="The retail assistant is off" text="Switch it on under Supplier bills to see what you owe each supplier." />
        {:else}
            <p class="tw:mb-4 tw:text-sm tw:text-muted-foreground">
                What you owe is posted bills, less credit notes, less verified payments allocated to a bill. Stock is separate: a bill does not put goods on the shelf.
            </p>
            <div class="page-table-wrapper retail-table tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead>
                        <tr>
                            <th>Supplier</th>
                            <th class="min-width">You owe</th>
                            <th class="min-width">Billed</th>
                            <th class="min-width"><span class="tw:sr-only">Actions</span></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each suppliers as row (row.id)}
                            <tr>
                                <td data-name="Supplier">
                                    <strong>{row.name}</strong>
                                    {#if moneyNote(row, formatMoney)}<div class="txt-hint txt-sm">{moneyNote(row, formatMoney)}</div>{/if}
                                </td>
                                <td class="min-width" data-name="You owe"><strong>{formatMoney(row.outstanding)}</strong></td>
                                <td class="min-width txt-hint" data-name="Billed">{formatMoney(row.invoiced)}</td>
                                <td class="min-width" data-name="Actions">
                                    <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => show(row)}><span class="txt">Open</span></button>
                                </td>
                            </tr>
                        {:else}
                            {#if !loading}
                                <tr><td colspan="4"><EmptyState icon="ri-store-2-line" title="No suppliers yet" text="A supplier appears here when you approve their first bill." /></td></tr>
                            {/if}
                        {/each}
                    </tbody>
                </table>
            </div>
        {/if}
    </div>
</div>

<Drawer open={!!open} size="md" title={open?.name ?? ""} onclose={() => (open = null)}>
    {#if open}
        <dl class="retail-sum">
            <div><dt>You owe</dt><dd>{formatMoney(open.outstanding)}</dd></div>
            <div><dt>Paid in advance</dt><dd>{formatMoney(open.advance)}</dd></div>
            <div><dt>Not verified yet</dt><dd>{formatMoney(open.unverified)}</dd></div>
        </dl>

        <h3 class="tw:mt-4 tw:mb-2 tw:font-semibold">Bills</h3>
        <ul class="retail-list">
            {#each bills as b (b.id)}
                <li>
                    <span><strong>{b.invoice_no}</strong> <span class="txt-hint txt-sm">{b.invoice_date}</span>{#if b.reversed} <span class="label">Reversed</span>{/if}</span>
                    <span>{formatMoney(b.remaining)} <span class="txt-hint txt-sm">of {formatMoney(b.total)}</span></span>
                </li>
            {:else}
                <li class="txt-hint">No bills posted yet.</li>
            {/each}
        </ul>

        <h3 class="tw:mt-4 tw:mb-2 tw:font-semibold">Payments</h3>
        <ul class="retail-list">
            {#each payments as p (p.id)}
                <li>
                    <span>
                        <strong>{formatMoney(p.amount)}</strong>
                        <span class="txt-hint txt-sm">{p.date}{p.reference ? ` · ${p.reference}` : ""}</span>
                        {#if p.reverses_id}<span class="label">Reversal</span>{:else if p.reversed}<span class="label">Reversed</span>{:else if p.status === "provisional"}<span class="label warning">Not verified</span>{/if}
                    </span>
                    {#if writable && !p.reverses_id && !p.reversed}
                        <span class="retail-actions">
                            {#if p.status === "provisional"}
                                <button type="button" class="btn sm" disabled={busy} onclick={() => act(() => retailAssistant.verifyPayment(p.id), "Payment verified")}><span class="txt">Verify</span></button>
                            {:else if usable(p) && openBills.length}
                                <button type="button" class="btn sm" disabled={busy} onclick={() => startAllocate("payment", p)}><span class="txt">Allocate</span></button>
                            {/if}
                            <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => act(() => retailAssistant.reversePayment(p.id), "Payment reversed")}><span class="txt">Reverse</span></button>
                        </span>
                    {/if}
                </li>
            {:else}
                <li class="txt-hint">No payments recorded yet.</li>
            {/each}
        </ul>

        <h3 class="tw:mt-4 tw:mb-2 tw:font-semibold">Credit notes</h3>
        <ul class="retail-list">
            {#each credits as c (c.id)}
                <li>
                    <span>
                        <strong>{formatMoney(c.amount)}</strong>
                        <span class="txt-hint txt-sm">{c.reference || c.kind}</span>
                        {#if c.reverses_id}<span class="label">Reversal</span>{:else if c.reversed}<span class="label">Reversed</span>{/if}
                    </span>
                    {#if writable && !c.reverses_id && !c.reversed}
                        <span class="retail-actions">
                            {#if usable(c) && openBills.length}
                                <button type="button" class="btn sm" disabled={busy} onclick={() => startAllocate("credit", c)}><span class="txt">Allocate</span></button>
                            {/if}
                            <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => act(() => retailAssistant.reverseCredit(c.id), "Credit note reversed")}><span class="txt">Reverse</span></button>
                        </span>
                    {/if}
                </li>
            {:else}
                <li class="txt-hint">No credit notes yet.</li>
            {/each}
        </ul>

        {#if allocating}
            <form class="retail-box" onsubmit={allocate}>
                <h3 class="tw:mb-2 tw:font-semibold">Allocate {formatMoney(allocating.entry.unallocated)}</h3>
                <div class="field">
                    <label for="ra-bill">To bill</label>
                    <Select id="ra-bill" bind:value={allocating.invoice} options={openBills.map((b) => ({ value: String(b.id), label: `${b.invoice_no} (${formatMoney(b.remaining)} left)` }))} />
                </div>
                <div class="field m-t-sm">
                    <label for="ra-alloc">Amount ({currency})</label>
                    <input id="ra-alloc" inputmode="decimal" required bind:value={allocating.amount} />
                </div>
                <div class="tw:mt-3 tw:flex tw:gap-2">
                    <button type="submit" class="btn" class:loading={busy} disabled={busy}><span class="txt">Allocate</span></button>
                    <button type="button" class="btn transparent secondary" onclick={() => (allocating = null)}><span class="txt">Cancel</span></button>
                </div>
            </form>
        {/if}

        {#if writable}
            <form class="retail-box" onsubmit={addPayment}>
                <h3 class="tw:mb-2 tw:font-semibold">Record a payment</h3>
                <div class="field">
                    <label for="ra-pay">Amount ({currency})</label>
                    <input id="ra-pay" inputmode="decimal" required bind:value={payment.amount} />
                </div>
                <div class="field m-t-sm">
                    <label for="ra-paid">Paid on</label>
                    <input id="ra-paid" type="date" required bind:value={payment.paid_on} />
                </div>
                <div class="field m-t-sm">
                    <label for="ra-how">Paid by</label>
                    <input id="ra-how" type="text" placeholder="Cash, UPI, bank transfer" bind:value={payment.method} />
                </div>
                <div class="field m-t-sm">
                    <label for="ra-ref">Reference</label>
                    <input id="ra-ref" type="text" bind:value={payment.reference} />
                </div>
                <label class="retail-check tw:mt-2">
                    <input type="checkbox" bind:checked={payment.verified} />
                    <span>I know this money was paid</span>
                </label>
                <div class="tw:mt-2"><button type="submit" class="btn" class:loading={busy} disabled={busy}><span class="txt">Record payment</span></button></div>
            </form>

            <form class="retail-box" onsubmit={addCredit}>
                <h3 class="tw:mb-2 tw:font-semibold">Record a credit note</h3>
                <div class="field">
                    <label for="ra-credit">Amount ({currency})</label>
                    <input id="ra-credit" inputmode="decimal" required bind:value={credit.amount} />
                </div>
                <div class="field m-t-sm">
                    <label for="ra-kind2">Reason</label>
                    <Select id="ra-kind2" bind:value={credit.kind} options={CREDIT_KINDS} />
                </div>
                <div class="field m-t-sm">
                    <label for="ra-cn">Credit note number</label>
                    <input id="ra-cn" type="text" bind:value={credit.credit_no} />
                </div>
                <div class="tw:mt-3"><button type="submit" class="btn" class:loading={busy} disabled={busy}><span class="txt">Record credit note</span></button></div>
            </form>
        {/if}
    {/if}
    {#snippet footer()}
        <button type="button" class="btn transparent secondary" onclick={() => (open = null)}><span class="txt">Close</span></button>
    {/snippet}
</Drawer>

<style>
    .retail-table {
        flex: none;
        max-height: none;
        overflow: visible;
    }
    .retail-sum {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(8rem, 1fr));
        gap: 12px;
        margin: 0;
    }
    .retail-sum dt {
        font-size: 13px;
        opacity: 0.7;
    }
    .retail-sum dd {
        margin: 2px 0 0;
        font-size: 18px;
        font-weight: 600;
        font-variant-numeric: tabular-nums;
    }
    .retail-list {
        list-style: none;
        margin: 0;
        padding: 0;
    }
    .retail-list li {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: 6px 12px;
        padding: 8px 0;
        border-top: 1px solid var(--color-border, #e5e5e5);
    }
    .retail-actions {
        display: inline-flex;
        flex-wrap: wrap;
        gap: 6px;
    }
    .retail-box {
        margin-top: 20px;
        padding: 14px;
        border: 1px solid var(--color-border, #e5e5e5);
        border-radius: 10px;
    }
    .retail-check {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        min-height: 44px;
        cursor: pointer;
    }
</style>
