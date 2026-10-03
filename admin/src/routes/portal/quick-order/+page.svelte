<script>
    /**
     * Quick order: lines by product code and quantity, typed or pasted from a
     * spreadsheet, into the buyer's basket (POST /x/b2b/cart/lines).
     *
     * The engine adds what it can and lists what it could not, line by line,
     * so a long paste never fails at line thirty-seven. The lines it refused
     * stay in the form with the reason beside each, ready to fix and send
     * again; the ones that went in leave it.
     *
     * Pasting works two ways because buyers do both: into the box below the
     * rows, or straight into any code field, where a block of copied cells
     * spreads across the rows from there down.
     *
     * A spreadsheet goes up whole, as the CSV it was saved as, and the engine
     * reads its columns by their names. Its refused rows are listed with their
     * row numbers rather than poured into the form: the file is what the buyer
     * will fix, and the row is how they find the line in it.
     */
    import { tick } from "svelte";
    import { base } from "$app/paths";
    import { toast } from "$lib/toast.svelte.js";
    import { trade, tradeApi, noteBasket, rememberBasket, explain, rejectedWords } from "$lib/trade.svelte.js";
    import PortalPage from "../PortalPage.svelte";
    import RejectedLines from "../RejectedLines.svelte";

    const MAX_LINES = 500;
    let seq = 0;
    const blankRow = () => ({ key: ++seq, sku: "", qty: "1", reason: "" });

    let rows = $state([blankRow(), blankRow(), blankRow(), blankRow(), blankRow()]);
    let pasteOpen = $state(false);
    let pasteText = $state("");
    let busy = $state(false);
    let result = $state(null);
    let failure = $state("");

    const filled = $derived(rows.filter((r) => r.sku.trim()));
    const badQty = (r) => !!r.sku.trim() && (!/^\d+$/.test(String(r.qty ?? "").trim()) || Number(r.qty) < 1);
    const invalid = $derived(filled.filter(badQty));
    const ordering = $derived(trade.me?.company?.status !== "closed");

    function addRow() {
        rows.push(blankRow());
        tick().then(() => document.getElementById(`sku-${rows[rows.length - 1].key}`)?.focus());
    }

    function removeRow(i) {
        rows.splice(i, 1);
        if (!rows.length) rows.push(blankRow());
    }

    /**
     * parseRows reads copied spreadsheet cells — tab-separated from Excel or
     * Sheets, comma- or semicolon-separated from a CSV — as code and quantity.
     * A row whose quantity is not a number is a header and is skipped; a row
     * with no quantity at all means one.
     */
    function parseRows(text) {
        const out = [];
        for (const raw of text.split(/\r?\n/)) {
            const line = raw.trim();
            if (!line) continue;
            const cells = line.split(/\t|;|,/).map((c) => c.trim().replace(/^"|"$/g, ""));
            const sku = cells[0];
            if (!sku) continue;
            const qtyCell = cells.slice(1).find((c) => c !== "");
            if (qtyCell === undefined) {
                out.push({ sku, qty: "1" });
            } else if (/^\d+$/.test(qtyCell.replace(/[\s,.](?=\d{3}\b)/g, ""))) {
                out.push({ sku, qty: qtyCell.replace(/[\s,.](?=\d{3}\b)/g, "") });
            }
        }
        return out;
    }

    function placeRows(parsed, at) {
        if (!parsed.length) return 0;
        const next = [...rows];
        parsed.forEach((p, n) => {
            const i = at + n;
            const row = { key: ++seq, sku: p.sku, qty: p.qty, reason: "" };
            if (i < next.length && !next[i].sku.trim()) next[i] = row;
            else if (i < next.length) next.splice(i, 0, row);
            else next.push(row);
        });
        rows = next;
        return parsed.length;
    }

    function pasteIntoRow(event, i) {
        const text = event.clipboardData?.getData("text") ?? "";
        if (!/[\t\n]/.test(text)) return;
        event.preventDefault();
        placeRows(parseRows(text), i);
    }

    function pasteBox() {
        const parsed = parseRows(pasteText);
        if (!parsed.length) {
            toast.warning("Nothing to add — each line needs a product code, then a quantity.");
            return;
        }
        const firstEmpty = rows.findIndex((r) => !r.sku.trim());
        placeRows(parsed, firstEmpty < 0 ? rows.length : firstEmpty);
        pasteText = "";
        pasteOpen = false;
    }

    // ------------------------------------------------------------ a spreadsheet

    /* The engine's own ceiling, said before the file is sent rather than after. */
    const MAX_FILE = 1 << 20;

    let uploadOpen = $state(false);
    let dragging = $state(false);
    let file = $state(null);
    let fileError = $state("");
    let uploading = $state(false);
    let fileResult = $state(null);

    function sizeWords(bytes) {
        return bytes < 1024 ? `${bytes} bytes` : `${Math.max(1, Math.round(bytes / 1024))} KB`;
    }

    function take(candidate) {
        fileError = "";
        fileResult = null;
        if (!candidate) return;
        if (!/\.csv$/i.test(candidate.name) && candidate.type !== "text/csv") {
            file = null;
            fileError = `${candidate.name} isn't a CSV file. In Excel or Sheets, save or download it as CSV first.`;
            return;
        }
        if (candidate.size > MAX_FILE) {
            file = null;
            fileError = `${candidate.name} is ${sizeWords(candidate.size)}; the most a file can be is 1 MB. Split it in two.`;
            return;
        }
        file = candidate;
    }

    function pick(event) {
        take(event.currentTarget.files?.[0] ?? null);
        // Cleared, so choosing the same file again after fixing it still fires.
        event.currentTarget.value = "";
    }

    function drop(event) {
        event.preventDefault();
        dragging = false;
        take(event.dataTransfer?.files?.[0] ?? null);
    }

    async function upload() {
        if (!file || uploading) return;
        uploading = true;
        fileError = "";
        fileResult = null;
        const sendFile = (cartId) =>
            tradeApi.upload("/x/b2b/cart/lines" + (cartId ? `?cart_id=${encodeURIComponent(cartId)}` : ""), file, "text/csv");
        try {
            let before = trade.basket.token ? trade.basket.count : 0;
            let fill;
            try {
                fill = await sendFile(trade.basket.token);
            } catch (err) {
                if (trade.basket.token && err.status === 409) {
                    rememberBasket("");
                    before = 0;
                    fill = await sendFile("");
                } else {
                    throw err;
                }
            }
            noteBasket(fill.cart);
            fileResult = {
                name: file.name,
                added: Math.max(0, (fill.cart?.item_count ?? 0) - before),
                rejected: fill.rejected ?? [],
            };
        } catch (err) {
            if (!err.handled) fileError = explain(err);
        } finally {
            uploading = false;
        }
    }

    async function send(lines, cartId) {
        return tradeApi.post("/x/b2b/cart/lines", cartId ? { cart_id: cartId, lines } : { lines });
    }

    async function submit(event) {
        event.preventDefault();
        if (busy || !filled.length) return;
        if (invalid.length) {
            failure = "Every quantity needs to be a whole number, 1 or more.";
            return;
        }
        if (filled.length > MAX_LINES) {
            failure = `That's ${filled.length} lines — add up to ${MAX_LINES} at a time.`;
            return;
        }
        busy = true;
        failure = "";
        result = null;
        const lines = filled.map((r) => ({ sku: r.sku.trim(), quantity: Number(r.qty) }));
        try {
            let fill;
            try {
                fill = await send(lines, trade.basket.token);
            } catch (err) {
                // A basket checked out in another tab cannot take more lines;
                // the next one is opened for them.
                if (trade.basket.token && (err.status === 409 || err.status === 404)) {
                    rememberBasket("");
                    fill = await send(lines, "");
                } else {
                    throw err;
                }
            }
            noteBasket(fill.cart);
            const rejected = fill.rejected ?? [];
            result = { added: lines.length - rejected.length, rejected };
            rows = rejected.length
                ? rejected.map((r) => ({ key: ++seq, sku: r.sku, qty: String(r.quantity), reason: rejectedWords(r) }))
                : [blankRow(), blankRow(), blankRow(), blankRow(), blankRow()];
        } catch (err) {
            if (!err.handled) failure = explain(err);
        } finally {
            busy = false;
        }
    }
</script>

<PortalPage title="Quick order">
    {#snippet actions()}
        <a href="{base}/portal/basket" class="btn secondary portal-press">
            <i class="ri-shopping-basket-2-line" aria-hidden="true"></i>
            <span class="txt">Your basket{trade.basket.count ? ` (${trade.basket.count})` : ""}</span>
        </a>
    {/snippet}

    <p class="field-help m-b-base portal-intro">
        Type each product code and how many you need, paste them straight from a spreadsheet — a code column and a
        quantity column — or upload the spreadsheet itself. Everything goes into your basket at
        {trade.me.company.name}'s prices; nothing is ordered until you check out.
    </p>

    {#if !ordering}
        <p class="txt-hint">Ordering is closed for {trade.me.company.name}.</p>
    {:else}
        {#if result}
            <div
                class="alert {result.rejected.length ? (result.added ? 'warning' : 'danger') : 'success'} m-b-base portal-notice portal-result"
                role="status"
            >
                <p>
                    {#if result.added}
                        <strong>{result.added} {result.added === 1 ? "line" : "lines"} added to your basket.</strong>
                    {:else}
                        <strong>Nothing was added.</strong>
                    {/if}
                    {#if result.rejected.length}
                        {result.rejected.length === 1 ? "One line needs" : `${result.rejected.length} lines need`} a look —
                        {result.rejected.length === 1 ? "it's" : "they're"} still below, with the reason.
                    {/if}
                    {#if result.added}
                        <a href="{base}/portal/basket" class="portal-inline-link">Go to your basket →</a>
                    {/if}
                </p>
            </div>
        {/if}

        <form onsubmit={submit} novalidate aria-label="Quick order">
            <div class="portal-qo tw:rounded-xl tw:border tw:bg-card" role="table" aria-label="Order lines">
                <div class="portal-qo-head" role="row">
                    <span role="columnheader">Product code</span>
                    <span role="columnheader">Quantity</span>
                    <span class="tw:sr-only" role="columnheader">Remove</span>
                </div>
                {#each rows as row, i (row.key)}
                    <div class="portal-qo-row" class:has-reason={!!row.reason} role="row">
                        <div class="field portal-qo-sku" class:error={!!row.reason} role="cell">
                            <input
                                id="sku-{row.key}"
                                type="text"
                                autocomplete="off"
                                spellcheck="false"
                                placeholder="e.g. MUG-001"
                                aria-label="Product code, line {i + 1}"
                                aria-describedby={row.reason ? `reason-${row.key}` : undefined}
                                bind:value={row.sku}
                                oninput={() => (row.reason = "")}
                                onpaste={(e) => pasteIntoRow(e, i)}
                            />
                        </div>
                        <div class="field portal-qo-qty" class:error={badQty(row)} role="cell">
                            <input
                                type="number"
                                min="1"
                                step="1"
                                inputmode="numeric"
                                aria-label="Quantity, line {i + 1}"
                                bind:value={row.qty}
                            />
                        </div>
                        <div role="cell" class="portal-qo-remove">
                            <button
                                type="button"
                                class="btn circle sm transparent secondary portal-remove"
                                aria-label="Remove line {i + 1}"
                                title="Remove this line"
                                onclick={() => removeRow(i)}
                            >
                                <i class="ri-close-line" aria-hidden="true"></i>
                            </button>
                        </div>
                        {#if row.reason}
                            <div class="portal-qo-reason txt-danger txt-sm" role="cell" id="reason-{row.key}">
                                <i class="ri-error-warning-line" aria-hidden="true"></i>
                                {row.reason}
                            </div>
                        {/if}
                    </div>
                {/each}
            </div>

            <div class="portal-qo-tools m-t-sm">
                <button type="button" class="btn sm secondary transparent portal-press" onclick={addRow}>
                    <i class="ri-add-line" aria-hidden="true"></i>
                    <span class="txt">Add a line</span>
                </button>
                <button
                    type="button"
                    class="btn sm secondary transparent portal-press"
                    aria-expanded={pasteOpen}
                    aria-controls="paste-box"
                    onclick={() => (pasteOpen = !pasteOpen)}
                >
                    <i class="ri-clipboard-line" aria-hidden="true"></i>
                    <span class="txt">Paste a list</span>
                </button>
                <button
                    type="button"
                    class="btn sm secondary transparent portal-press"
                    aria-expanded={uploadOpen}
                    aria-controls="upload-box"
                    onclick={() => (uploadOpen = !uploadOpen)}
                >
                    <i class="ri-file-upload-line" aria-hidden="true"></i>
                    <span class="txt">Upload a spreadsheet</span>
                </button>
            </div>

            {#if uploadOpen}
                <div class="portal-upload m-t-sm portal-notice" id="upload-box">
                    <label
                        class="portal-drop"
                        class:is-over={dragging}
                        class:has-file={!!file}
                        for="upload-file"
                        ondragenter={(e) => {
                            e.preventDefault();
                            dragging = true;
                        }}
                        ondragover={(e) => {
                            e.preventDefault();
                            dragging = true;
                        }}
                        ondragleave={(e) => {
                            if (!e.currentTarget.contains(e.relatedTarget)) dragging = false;
                        }}
                        ondrop={drop}
                    >
                        <i class={file ? "ri-file-excel-2-line" : "ri-upload-cloud-2-line"} aria-hidden="true"></i>
                        {#if file}
                            <span class="txt-bold">{file.name}</span>
                            <span class="txt-hint txt-sm">{sizeWords(file.size)} · choose another, or drop one here</span>
                        {:else}
                            <span class="txt-bold">Drop a CSV file here, or choose one</span>
                            <span class="txt-hint txt-sm">Up to 500 lines and 1 MB</span>
                        {/if}
                        <input id="upload-file" type="file" accept=".csv,text/csv" class="portal-file-input" onchange={pick} />
                    </label>
                    <div class="field-help">
                        The first row names the columns: <span class="txt-code">quantity</span>, and
                        <span class="txt-code">sku</span> (or <span class="txt-code">variant_id</span>). Any other column is
                        ignored, so an export with titles and prices beside the codes reads as it is. Save it from Excel or
                        Sheets as CSV.
                    </div>
                    {#key fileError}
                        {#if fileError}
                            <div class="field-help txt-danger portal-refusal" role="alert">{fileError}</div>
                        {/if}
                    {/key}
                    <div class="portal-row-actions m-t-sm">
                        <button
                            type="button"
                            class="btn sm portal-press"
                            class:loading={uploading}
                            disabled={!file || uploading}
                            onclick={upload}
                        >
                            <i class="ri-shopping-basket-2-line" aria-hidden="true"></i>
                            <span class="txt">Add the file to the basket</span>
                        </button>
                    </div>
                    {#if fileResult}
                        <div
                            class="alert {fileResult.rejected.length ? (fileResult.added ? 'warning' : 'danger') : 'success'} m-t-sm portal-result"
                            role="status"
                        >
                            <p>
                                {#if fileResult.added}
                                    <strong>Added {fileResult.added} {fileResult.added === 1 ? "item" : "items"} from {fileResult.name}.</strong>
                                {:else}
                                    <strong>Nothing from {fileResult.name} went in.</strong>
                                {/if}
                                {#if fileResult.rejected.length}
                                    {fileResult.rejected.length === 1 ? "One row" : `${fileResult.rejected.length} rows`} didn't —
                                    fix {fileResult.rejected.length === 1 ? "it" : "them"} in the file and upload it again, or type
                                    {fileResult.rejected.length === 1 ? "it" : "them"} above.
                                {/if}
                                {#if fileResult.added}
                                    <a href="{base}/portal/basket" class="portal-inline-link">Go to your basket →</a>
                                {/if}
                            </p>
                            <RejectedLines rejected={fileResult.rejected} />
                        </div>
                    {/if}
                </div>
            {/if}

            {#if pasteOpen}
                <div class="portal-paste m-t-sm portal-notice" id="paste-box">
                    <div class="field">
                        <label for="paste-text">Copied cells: a code, then a quantity, one line each</label>
                        <textarea
                            id="paste-text"
                            rows="6"
                            spellcheck="false"
                            placeholder={"MUG-001\t24\nTEE-M-BLK\t12"}
                            bind:value={pasteText}
                        ></textarea>
                    </div>
                    <div class="field-help">Tabs, commas or semicolons between the two. A heading row is skipped.</div>
                    <button type="button" class="btn sm m-t-sm portal-press" disabled={!pasteText.trim()} onclick={pasteBox}>
                        <span class="txt">Add to the form</span>
                    </button>
                </div>
            {/if}

            {#key failure}
                {#if failure}
                    <div class="alert danger m-t-base portal-refusal" role="alert"><p>{failure}</p></div>
                {/if}
            {/key}

            <div class="portal-submit m-t-base">
                <span class="txt-hint txt-sm" aria-live="polite">
                    {filled.length} {filled.length === 1 ? "line" : "lines"} ready
                </span>
                <button type="submit" class="btn lg portal-press" class:loading={busy} disabled={busy || !filled.length}>
                    <i class="ri-shopping-basket-2-line" aria-hidden="true"></i>
                    <span class="txt">Add to basket</span>
                </button>
            </div>
        </form>
    {/if}
</PortalPage>
