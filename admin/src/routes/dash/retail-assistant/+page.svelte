<script>
    /**
     * Supplier bills (D192): what arrived on WhatsApp, and the one screen where
     * it becomes a ledger entry. Nothing posts itself, so the list leads with
     * what is waiting and the drawer shows each figure beside what it found wrong.
     * Numbers come from the engine; this screen only moves them.
     */
    import { t } from "$lib/retail-lang.svelte.js";
    import RetailLanguage from "$lib/components/RetailLanguage.svelte";
    import { can, retailAssistant } from "$lib/api.js";
    import { downloadBinary, safeFilename } from "$lib/download.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { formatDate, formatMoney, fromMinor, isValidMoney, toMinor } from "$lib/format.js";
    import { aiReadable, CREDIT_KINDS, DOCUMENT_TYPES, isMismatch, statusOf } from "$lib/retail.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { titled } from "$lib/brand.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import FeatureSwitch from "$lib/components/FeatureSwitch.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";

    const FILTERS = [
        { value: "needs_review", label: "Needs review" },
        { value: "", label: "Everything" },
        { value: "posted", label: "Posted" },
        { value: "failed", label: "Failed" },
        { value: "rejected", label: "Rejected" },
    ];

    const missing = $derived(modulesKnown() && !hasModule("retailassistant"));
    const readable = $derived(can("retailassistant.read"));
    const writable = $derived(can("retailassistant.write"));
    const linking = $derived(can("retailassistant.links"));
    const currency = $derived(settings.currency);

    let status = $state(null);
    let filter = $state("needs_review");
    let docs = $state([]);
    let links = $state([]);
    let loading = $state(true);
    let busy = $state(false);
    let doc = $state(null);
    let form = $state(null);
    let acceptMismatch = $state(false);
    let phone = $state("");
    let issued = $state(null);
    let confirmingRemove = $state(false);
    let compared = $state(false);

    $effect(() => {
        if (readable && hasModule("retailassistant")) refresh();
    });

    async function refresh() {
        loading = true;
        try {
            status = await retailAssistant.status();
            if (status.enabled) await loadAll();
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function loadAll() {
        docs = (await retailAssistant.documents({ status: filter, limit: 50 })).data;
        if (linking) links = await retailAssistant.links();
    }

    async function setFilter(next) {
        filter = next;
        await run(loadAll);
    }

    async function run(fn) {
        busy = true;
        try {
            await fn();
            return true;
        } catch (err) {
            toast.error(err);
            return false;
        } finally {
            busy = false;
        }
    }

    const amountText = (minor) => (minor === null || minor === undefined ? "" : fromMinor(minor, currency));

    async function open(row) {
        await run(async () => {
            doc = await retailAssistant.document(row.id);
            confirmingRemove = false;
            fill(doc);
        });
    }

    function fill(d) {
        const draft = d.draft ?? {};
        acceptMismatch = false;
        compared = false;
        form = {
            document_type: draft.document_type || "purchase_invoice",
            supplier: draft.supplier?.name ?? "",
            gstin: draft.supplier?.gstin ?? "",
            invoice_no: draft.invoice_no ?? "",
            invoice_date: draft.invoice_date ?? "",
            subtotal: amountText(draft.amounts_minor?.subtotal),
            tax: amountText(draft.amounts_minor?.tax),
            total: amountText(draft.amounts_minor?.total),
            method: draft.method ?? "",
            credit_kind: draft.credit_kind || "return",
            reason: draft.reason ?? "",
        };
    }

    const reviewing = $derived(doc?.status === "needs_review" && writable);
    const exceptions = $derived(doc?.draft?.exceptions ?? []);
    const hasMismatch = $derived(exceptions.some(isMismatch));
    const readByAI = $derived(doc?.draft?.source?.extraction_method === "ai");
    const canApprove = $derived(exceptions.every(isMismatch) && (!hasMismatch || acceptMismatch) && (!readByAI || compared));

    function minor(value, name) {
        if (value.trim() === "") return null;
        if (!isValidMoney(value)) throw new Error(t("Type the {name} as an amount, such as 1180.50", { name: t(name) }));
        return toMinor(value, currency);
    }

    /** The reviewer's fields over the draft, keeping the lines as they were read. */
    function draftFromForm() {
        return {
            ...doc.draft,
            document_type: form.document_type,
            supplier: { name: form.supplier.trim(), gstin: form.gstin.trim() || null },
            invoice_no: form.invoice_no.trim(),
            invoice_date: form.invoice_date,
            amounts_minor: { subtotal: minor(form.subtotal, "subtotal"), tax: minor(form.tax, "tax"), total: minor(form.total, "total") },
            method: form.method.trim(),
            credit_kind: form.credit_kind,
            reason: form.reason.trim(),
        };
    }

    async function save(e) {
        e?.preventDefault();
        await run(async () => {
            doc = await retailAssistant.correct(doc.id, { version: doc.version, draft: draftFromForm() });
            fill(doc);
            toast.success(t("Saved. The figures were checked again."));
        });
    }

    async function approve() {
        const ok = await run(async () => {
            await retailAssistant.approve(doc.id, { version: doc.version, accept_mismatch: acceptMismatch, compared_with_file: compared });
            toast.success(t("Posted to the supplier's account"));
            doc = null;
            await loadAll();
        });
        if (!ok && doc) doc = await retailAssistant.document(doc.id);
    }

    async function reject() {
        await run(async () => {
            await retailAssistant.reject(doc.id, { version: doc.version });
            toast.success(t("Rejected. It stays on record and counts for nothing."));
            doc = null;
            await loadAll();
        });
    }

    async function readAgain() {
        await run(async () => {
            doc = await retailAssistant.readWithAI(doc.id);
            fill(doc);
            toast.success(t("Read. Compare every figure with the file."));
            await loadAll();
        });
    }

    async function removeFile() {
        await run(async () => {
            await retailAssistant.removeFile(doc.id);
            toast.success(t("The file was deleted. The supplier's account did not change."));
            confirmingRemove = false;
            doc = await retailAssistant.document(doc.id);
        });
    }

    async function retry(row) {
        await run(async () => {
            await retailAssistant.retry(row.id);
            toast.success(t("Fetching it again"));
            await loadAll();
        });
    }

    async function addPhone(e) {
        e.preventDefault();
        await run(async () => {
            issued = await retailAssistant.addLink({ phone: phone.trim() });
            phone = "";
            links = await retailAssistant.links();
        });
    }

    async function setNotify(link, notify) {
        await run(async () => {
            await retailAssistant.updateLink(link.id, { notify });
            links = await retailAssistant.links();
        });
    }

    async function setLanguage(link, language) {
        await run(async () => {
            await retailAssistant.updateLink(link.id, { language });
            links = await retailAssistant.links();
        });
    }

    async function removeLink(link) {
        await run(async () => {
            await retailAssistant.removeLink(link.id);
            toast.success(t("+{phone} can no longer send to this store", { phone: link.phone }));
            links = await retailAssistant.links();
        });
    }
</script>

<svelte:head><title>{titled(t("Supplier bills"))}</title></svelte:head>

<div class="page page-retail shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">{t("Retail assistant")}</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">{t("Supplier bills")}</h1>
            </nav>
            <RetailLanguage />
        </header>

        {#if missing}
            <ModuleMissing module="retailassistant" what="Supplier bills are kept by ext/retailassistant, and this binary does not include it." />
        {:else if !readable}
            <NoAccess right="retailassistant.read" what="the store's supplier bills" />
        {:else}
            <FeatureSwitch pluginKey="retailassistant" onchange={refresh} />

            {#if status && !status.enabled}
                {#if !loading}
                    <p class="tw:mt-4 tw:text-sm tw:text-muted-foreground" in:rise>
                        {t("Switch this on to forward supplier bills and payment proofs on WhatsApp and see what you owe each supplier. Nothing else in your store changes.")}
                    </p>
                {/if}
            {:else if status}
                {#if !status.channel_ready}
                    <div class="alert alert-warning tw:my-4" role="status">
                        <p>{t("The install's WhatsApp number is not set up yet, so nothing can arrive here. Its operator sets the RETAIL_ASSISTANT_ settings.")}</p>
                    </div>
                {/if}

                {#if status.files_sealed || status.files_scanned}
                    <p class="tw:mt-4 tw:text-sm tw:text-muted-foreground">
                        {#if status.files_scanned}New files are checked by a virus scanner before they are kept.{/if}
                        {#if status.files_sealed}New files are encrypted in the database.{/if}
                    </p>
                {/if}

                <div class="tw:my-4 tw:flex tw:flex-wrap tw:gap-2" role="group" aria-label={t("Show documents")}>
                    {#each FILTERS as f (f.value)}
                        <button type="button" class="btn sm" class:transparent={filter !== f.value} class:secondary={filter !== f.value} aria-pressed={filter === f.value} disabled={busy} onclick={() => setFilter(f.value)}>
                            <span class="txt">{t(f.label)}</span>
                        </button>
                    {/each}
                </div>

                <div class="page-table-wrapper retail-table tw:rounded-xl tw:border tw:mb-8">
                    <table class="table responsive-table">
                        <thead>
                            <tr>
                                <th>{t("Document")}</th>
                                <th class="min-width">{t("Supplier")}</th>
                                <th class="min-width">{t("Total")}</th>
                                <th class="min-width">{t("Status")}</th>
                                <th class="min-width">{t("Received")}</th>
                                <th class="min-width"><span class="tw:sr-only">{t("Actions")}</span></th>
                            </tr>
                        </thead>
                        <tbody>
                            {#each docs as row (row.id)}
                                {@const s = statusOf(row.status)}
                                <tr>
                                    <td data-name="Document">
                                        <strong>{row.draft?.invoice_no || row.filename || t("Document {id}", { id: row.id })}</strong>
                                        {#if row.draft?.exceptions?.length}
                                            <div class="txt-hint txt-sm">{t("{n} to check", { n: row.draft.exceptions.length })}</div>
                                        {/if}
                                    </td>
                                    <td class="min-width" data-name="Supplier">{row.draft?.supplier?.name || "—"}</td>
                                    <td class="min-width" data-name="Total">
                                        {row.draft?.amounts_minor?.total != null ? formatMoney({ amount_minor: row.draft.amounts_minor.total, currency }) : "—"}
                                    </td>
                                    <td class="min-width" data-name="Status"><span class="label {s.cls}">{t(s.label)}</span></td>
                                    <td class="min-width txt-hint" data-name="Received">{formatDate(row.created_at)}</td>
                                    <td class="min-width" data-name="Actions">
                                        {#if row.status === "failed" && writable}
                                            <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => retry(row)}><span class="txt">{t("Try again")}</span></button>
                                        {:else if row.status !== "received" && row.status !== "parsing"}
                                            <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => open(row)}>
                                                <span class="txt">{row.status === "needs_review" && writable ? t("Review") : t("Open")}</span>
                                            </button>
                                        {/if}
                                    </td>
                                </tr>
                            {:else}
                                {#if !loading}
                                    <tr><td colspan="6"><EmptyState icon="ri-file-list-3-line" title={t("Nothing here")} text={t("Forward a supplier bill to the store's WhatsApp number from a linked phone, and it will wait here for review.")} /></td></tr>
                                {/if}
                            {/each}
                        </tbody>
                    </table>
                </div>

                {#if linking}
                    <h2 class="tw:mb-2 tw:text-lg tw:font-semibold">{t("Phones that can send documents")}</h2>
                    <p class="tw:mb-3 tw:text-sm tw:text-muted-foreground">
                        {t("A phone counts once it sends its code to {number}. Files from any other phone are not kept.", { number: status.display_number || t("the store's WhatsApp number") })}
                    </p>
                    <form class="retail-add tw:mb-3 tw:flex tw:flex-wrap tw:items-end tw:gap-2" onsubmit={addPhone}>
                        <div class="field tw:m-0">
                            <label for="ra-phone">{t("Phone number")}</label>
                            <input id="ra-phone" type="tel" inputmode="tel" autocomplete="off" placeholder="+91 98765 43210" required bind:value={phone} />
                        </div>
                        <button type="submit" class="btn" class:loading={busy} disabled={busy}><span class="txt">{t("Add phone")}</span></button>
                    </form>
                    {#if issued}
                        <div class="alert alert-success tw:mb-3" role="status" in:rise>
                            <p>{t("From +{phone}, send", { phone: issued.phone })} <strong>LINK {issued.code}</strong> {t("to {number}. It works for 24 hours and is shown only now.", { number: status.display_number || t("the store's WhatsApp number") })}</p>
                        </div>
                    {/if}
                    <div class="page-table-wrapper retail-table tw:rounded-xl tw:border">
                        <table class="table responsive-table">
                            <thead>
                                <tr>
                                    <th>{t("Phone")}</th>
                                    <th class="min-width">{t("State")}</th>
                                    <th class="min-width">{t("Language")}</th>
                                    <th class="min-width">{t("Confirmations")}</th>
                                    <th class="min-width"><span class="tw:sr-only">{t("Actions")}</span></th>
                                </tr>
                            </thead>
                            <tbody>
                                {#each links as link (link.id)}
                                    <tr>
                                        <td data-name="Phone"><strong>+{link.phone}</strong></td>
                                        <td class="min-width" data-name="State">
                                            <span class="label" class:success={link.verified_at}>{link.verified_at ? t("Linked") : t("Waiting for the code")}</span>
                                        </td>
                                        <td class="min-width" data-name="Language">
                                            <select aria-label={t("Language for +{phone}", { phone: link.phone })} value={link.language} disabled={busy} onchange={(e) => setLanguage(link, e.currentTarget.value)}>
                                                <option value="en">English</option>
                                                <option value="hi">हिंदी</option>
                                            </select>
                                        </td>
                                        <td class="min-width" data-name="Confirmations">
                                            {#if link.opted_out}
                                                <span class="label warning">{t("Sent STOP")}</span>
                                            {:else}
                                                <label class="retail-check">
                                                    <input type="checkbox" checked={link.notify} disabled={busy || !link.verified_at} onchange={(e) => setNotify(link, e.currentTarget.checked)} />
                                                    <span>{t("Send")}</span>
                                                </label>
                                            {/if}
                                        </td>
                                        <td class="min-width" data-name="Actions">
                                            <button type="button" class="btn sm transparent secondary" disabled={busy} onclick={() => removeLink(link)}><span class="txt">{t("Remove")}</span></button>
                                        </td>
                                    </tr>
                                {:else}
                                    {#if !loading}
                                        <tr><td colspan="5"><EmptyState icon="ri-whatsapp-line" title={t("No phones yet")} text={t("Add the phone you forward bills from.")} /></td></tr>
                                    {/if}
                                {/each}
                            </tbody>
                        </table>
                    </div>
                {/if}
            {/if}
        {/if}
    </div>
</div>

<Drawer open={!!doc && !!form} size="md" title={doc ? doc.draft?.invoice_no || doc.filename || t("Document {id}", { id: doc.id }) : ""} onclose={() => (doc = null)}>
    {#if doc && form}
        <div class="tw:mb-3 tw:flex tw:flex-wrap tw:items-center tw:gap-2">
            <span class="label {statusOf(doc.status).cls}">{t(statusOf(doc.status).label)}</span>
            <span class="txt-hint txt-sm">{t("From +{phone}", { phone: doc.sender })}</span>
            {#if doc.file_removed_at}
                <span class="txt-hint txt-sm">{t("Original file deleted on {date}", { date: formatDate(doc.file_removed_at) })}</span>
            {:else}
                <button type="button" class="btn sm transparent secondary" onclick={() => downloadBinary(`/api/admin/x/retailassistant/documents/${doc.id}/file`, safeFilename(doc.filename || `document-${doc.id}`))}>
                    <i class="ri-download-line" aria-hidden="true"></i>
                    <span class="txt">{t("Original file")}</span>
                </button>
                {#if writable && ["posted", "rejected", "failed"].includes(doc.status)}
                    {#if confirmingRemove}
                        <button type="button" class="btn sm danger" class:loading={busy} disabled={busy} onclick={removeFile} in:rise><span class="txt">{t("Yes, delete the file")}</span></button>
                        <button type="button" class="btn sm transparent secondary" onclick={() => (confirmingRemove = false)}><span class="txt">{t("Keep it")}</span></button>
                    {:else}
                        <button type="button" class="btn sm transparent secondary" onclick={() => (confirmingRemove = true)}><span class="txt">{t("Delete file")}</span></button>
                    {/if}
                {/if}
            {/if}
        </div>

        {#if readByAI}
            <div class="alert alert-info tw:mb-3" role="status" in:rise>
                <p><strong>{doc.draft.source.confidence != null ? t("Read by AI, {n}% sure.", { n: doc.draft.source.confidence }) : t("Read by AI.")}</strong> {t("It can misread a figure and still look right, so compare each one with the file.")}</p>
                {#if doc.draft.source.notes?.length}
                    <ul>
                        {#each doc.draft.source.notes as note (note)}<li>{note}</li>{/each}
                    </ul>
                {/if}
            </div>
        {/if}

        {#if reviewing && !readByAI && aiReadable(doc.mime_type)}
            <div class="tw:mb-3" in:rise>
                {#if status?.ai_reading}
                    {#if doc.last_error}<p class="txt-hint txt-sm tw:mb-2">{doc.last_error}</p>{/if}
                    <button type="button" class="btn sm" class:loading={busy} disabled={busy} onclick={readAgain}>
                        <i class="ri-sparkling-line" aria-hidden="true"></i>
                        <span class="txt">{t("Read with AI")}</span>
                    </button>
                {:else if status?.ai_reading_why}
                    <p class="txt-hint txt-sm">{t(status.ai_reading_why)}</p>
                {/if}
            </div>
        {/if}

        {#if exceptions.length}
            <div class="alert alert-warning tw:mb-3" role="status" in:rise>
                <ul>
                    {#each exceptions as x (x)}<li>{x}</li>{/each}
                </ul>
            </div>
        {/if}

        <form id="ra-review" onsubmit={save}>
            <fieldset disabled={!reviewing} class="retail-fields">
                <div class="field">
                    <label for="ra-type">{t("This is a")}</label>
                    <Select id="ra-type" bind:value={form.document_type} options={DOCUMENT_TYPES.map((o) => ({ ...o, label: t(o.label) }))} />
                </div>
                <div class="field m-t-sm">
                    <label for="ra-supplier">{t("Supplier")}</label>
                    <input id="ra-supplier" type="text" bind:value={form.supplier} />
                </div>
                <div class="field m-t-sm">
                    <label for="ra-gstin">{t("Supplier's GSTIN")}</label>
                    <input id="ra-gstin" type="text" autocapitalize="characters" bind:value={form.gstin} />
                </div>
                <div class="field m-t-sm">
                    <label for="ra-no">{form.document_type === "payment_proof" ? t("Reference") : t("Number")}</label>
                    <input id="ra-no" type="text" bind:value={form.invoice_no} />
                </div>
                <div class="field m-t-sm">
                    <label for="ra-date">{t("Date")}</label>
                    <input id="ra-date" type="date" bind:value={form.invoice_date} />
                </div>
                {#if form.document_type === "purchase_invoice"}
                    <div class="field m-t-sm">
                        <label for="ra-subtotal">{t("Subtotal ({currency})", { currency })}</label>
                        <input id="ra-subtotal" inputmode="decimal" bind:value={form.subtotal} />
                    </div>
                    <div class="field m-t-sm">
                        <label for="ra-tax">{t("Tax ({currency})", { currency })}</label>
                        <input id="ra-tax" inputmode="decimal" bind:value={form.tax} />
                    </div>
                {/if}
                <div class="field m-t-sm">
                    <label for="ra-total">{form.document_type === "payment_proof" ? t("Amount ({currency})", { currency }) : t("Total ({currency})", { currency })}</label>
                    <input id="ra-total" inputmode="decimal" bind:value={form.total} />
                </div>
                {#if form.document_type === "payment_proof"}
                    <div class="field m-t-sm">
                        <label for="ra-method">{t("Paid by")}</label>
                        <input id="ra-method" type="text" placeholder={t("UPI, cash, bank transfer")} bind:value={form.method} />
                    </div>
                    <div class="field-help">{t("A screenshot does not prove the money moved, so this goes in as not verified until you verify it on the supplier's account.")}</div>
                {/if}
                {#if form.document_type === "credit_note"}
                    <div class="field m-t-sm">
                        <label for="ra-kind">{t("Reason")}</label>
                        <Select id="ra-kind" bind:value={form.credit_kind} options={CREDIT_KINDS.map((o) => ({ ...o, label: t(o.label) }))} />
                    </div>
                {/if}
            </fieldset>
        </form>

        {#if doc.draft?.lines?.length}
            <h3 class="tw:mt-4 tw:mb-2 tw:font-semibold">{t("Lines as read")}</h3>
            <div class="page-table-wrapper retail-table tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead><tr><th>{t("Item")}</th><th class="min-width">{t("Qty")}</th><th class="min-width">{t("Amount")}</th></tr></thead>
                    <tbody>
                        {#each doc.draft.lines as line, i (i)}
                            <tr>
                                <td data-name="Item">{line.description || "—"}</td>
                                <td class="min-width" data-name="Qty">{line.quantity ?? "—"}</td>
                                <td class="min-width" data-name="Amount">{line.line_total_minor != null ? formatMoney({ amount_minor: line.line_total_minor, currency }) : "—"}</td>
                            </tr>
                        {/each}
                    </tbody>
                </table>
            </div>
        {/if}

        {#if reviewing && readByAI}
            <label class="retail-check tw:mt-4">
                <input type="checkbox" bind:checked={compared} />
                <span>{t("I compared these figures with the file.")}</span>
            </label>
        {/if}
        {#if reviewing && hasMismatch}
            <label class="retail-check tw:mt-4">
                <input type="checkbox" bind:checked={acceptMismatch} />
                <span>{t("I checked the figures against the file, and they are right as they stand.")}</span>
            </label>
        {/if}
    {/if}
    {#snippet footer()}
        <div class="retail-footer">
            {#if reviewing}
                <button type="button" class="btn transparent secondary" disabled={busy} onclick={reject}><span class="txt">{t("Reject")}</span></button>
                <button type="submit" form="ra-review" class="btn transparent secondary" disabled={busy}><span class="txt">{t("Save changes")}</span></button>
                <button type="button" class="btn" class:loading={busy} disabled={busy || !canApprove} onclick={approve}><span class="txt">{t("Approve and post")}</span></button>
            {:else}
                <button type="button" class="btn transparent secondary" onclick={() => (doc = null)}><span class="txt">{t("Close")}</span></button>
            {/if}
        </div>
    {/snippet}
</Drawer>

<style>
    .retail-table {
        flex: none;
        max-height: none;
        overflow: visible;
    }
    .retail-fields {
        border: 0;
        margin: 0;
        padding: 0;
        min-width: 0;
    }
    .retail-check {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        min-height: 44px;
        cursor: pointer;
    }
    /* Hindi labels run longer, and the main action must not be cut off on a phone. */
    .retail-footer {
        display: flex;
        flex-wrap: wrap;
        justify-content: flex-end;
        gap: 8px;
        width: 100%;
    }
    .retail-add input {
        min-width: min(16rem, 100%);
    }
</style>
