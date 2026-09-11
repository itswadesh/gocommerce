<script module>
    import { api, query } from "$lib/api.js";

    /**
     * Finding one variant by whatever an operator can actually type.
     *
     * There is no variant-search route: `GET /api/admin/variants` does not
     * exist, and `GET /api/variants` needs a product_id. What does exist is the
     * product listing, which embeds every variant with its stock — so a search
     * for a variant is a search for its product, flattened.
     *
     * That listing's `q` matches the product's TITLE and DESCRIPTION and
     * nothing else (core/catalog.go, productFilters) — never a SKU. Typing a
     * SKU into it therefore finds nothing, which is precisely the thing an
     * operator holding a box does. So a search that comes back empty is tried
     * once more against `GET /api/products/sku/{sku}`, the exact-SKU lookup the
     * storefront uses. It is exact and it only answers for an ACTIVE product,
     * so it is a fallback rather than the search: a draft product's SKU still
     * has to be reached by its product's name.
     *
     * The honest fix is one line of SQL in productFilters — an EXISTS over
     * variants — and it is reported rather than worked around any further here.
     */

    /** rowsOf flattens products into one row per variant, carrying its product. */
    function rowsOf(products) {
        const out = [];
        for (const p of products ?? []) {
            for (const v of p.variants ?? []) {
                out.push({
                    ...v,
                    product: { id: p.id, title: p.title, image_url: p.image_url ?? "" },
                });
            }
        }
        return out;
    }

    /**
     * findVariants answers a search with variant rows and the listing's own
     * meta — which counts PRODUCTS, because that is what the page of rows was
     * cut from. A caller paging these has to say "product" or it would be
     * describing its rows with somebody else's total.
     */
    export async function findVariants(term, options = {}) {
        /* `sort` and `order` are the product listing's own, passed straight
           through — the rows here are that listing flattened, so the only
           orderings that mean anything are the ones it serves. A caller that
           names none sends none, which is byte-for-byte the request this made
           before sorting existed. */
        const { page = 1, limit = 25, sort = "", order = "" } = options;
        const text = String(term ?? "").trim();
        if (!text) return { rows: [], meta: null, bySKU: false };

        const result = await api.get(
            "/api/admin/products" + query({ q: text, page, limit, sort, order }),
        );
        const rows = rowsOf(result.data ?? []);
        // Only the first page falls back: page 2 of an empty result is not a
        // question anybody asked.
        if (rows.length || page > 1) return { rows, meta: result.meta ?? null, bySKU: false };

        const exact = await findBySKU(text);
        if (!exact) return { rows, meta: result.meta ?? null, bySKU: false };
        return {
            rows: [exact],
            meta: { total: 1, limit, offset: 0, page: 1, total_pages: 1 },
            bySKU: true,
        };
    }

    /**
     * The exact-SKU lookup, or null. Just the variant that was asked for and
     * not its siblings: somebody who typed a SKU asked about one box, and
     * answering with the other eleven colours of the same shirt buries it.
     */
    async function findBySKU(sku) {
        try {
            const product = await api.get("/api/products/sku/" + encodeURIComponent(sku));
            const wanted = sku.toLowerCase();
            return rowsOf([product]).find((r) => String(r.sku).toLowerCase() === wanted) ?? null;
        } catch {
            // A 404 is the normal answer here — no such SKU, or its product is
            // a draft. The empty state below says both.
            return null;
        }
    }
</script>

<script>
    /**
     * The picker: type or scan, press Enter, get a variant.
     *
     * It is deliberately not a <Combobox> or a <TargetPicker>. Those two write
     * into a field; this one hands a row to whoever asked and empties itself,
     * because the caller is building a list and the next SKU is already coming.
     */
    import { toast } from "$lib/toast.svelte.js";

    let {
        /** Called with the chosen row. The box clears and keeps the focus. */
        onpick,
        id = undefined,
        placeholder = "Scan or type a SKU, or search a product name",
        disabled = false,
        limit = 8,
    } = $props();

    let field = $state(null);
    let text = $state("");
    let rows = $state([]);
    let loading = $state(false);
    let searched = $state(false);
    let bySKU = $state(false);
    let timer = null;

    /* Two keystrokes leave two replies in flight, and the list would otherwise
       settle on whichever arrived last. */
    let seq = 0;

    function onInput() {
        clearTimeout(timer);
        searched = false;
        if (!text.trim()) {
            rows = [];
            loading = false;
            return;
        }
        loading = true;
        const wanted = text;
        timer = setTimeout(() => run(wanted), 250);
    }

    async function run(wanted) {
        const mine = ++seq;
        try {
            const found = await findVariants(wanted, { limit });
            if (mine !== seq) return;
            rows = found.rows;
            bySKU = found.bySKU;
            searched = true;
            return found;
        } catch (err) {
            if (mine === seq) toast.error(err);
            return null;
        } finally {
            if (mine === seq) loading = false;
        }
    }

    /**
     * Enter is the scanner's key: a barcode reader types the SKU and presses it
     * before the debounce has fired, so this searches straight away and takes
     * the row whose SKU is exactly what was typed. Anything less exact is left
     * for the operator to click — picking a near match on their behalf is how
     * the wrong box gets received.
     */
    async function submit() {
        clearTimeout(timer);
        const wanted = text.trim();
        if (!wanted) return;
        const hit = exactIn(rows, wanted);
        if (hit) {
            choose(hit);
            return;
        }
        loading = true;
        const found = await run(wanted);
        const arrived = exactIn(found?.rows ?? [], wanted);
        if (arrived) choose(arrived);
    }

    function exactIn(list, wanted) {
        const needle = wanted.toLowerCase();
        return list.find((r) => String(r.sku).toLowerCase() === needle) ?? null;
    }

    function choose(row) {
        onpick?.(row);
        text = "";
        rows = [];
        searched = false;
        bySKU = false;
        field?.focus();
    }

    function onKeydown(event) {
        if (event.key === "Enter") {
            // Never a form submission: this box lives inside drawers that have
            // a form of their own with an entirely different button.
            event.preventDefault();
            submit();
        } else if (event.key === "Escape" && text) {
            event.preventDefault();
            text = "";
            rows = [];
            searched = false;
        }
    }
</script>

<div class="field">
    <input
        bind:this={field}
        bind:value={text}
        {id}
        {disabled}
        {placeholder}
        type="text"
        autocomplete="off"
        oninput={onInput}
        onkeydown={onKeydown}
    />
</div>

{#if text.trim()}
    <!-- The panel's own `.dropdown`, in flow rather than in the top layer: it
         is a result list the operator reads and clicks down, not a menu that
         should cover the form it is filling in. -->
    <div class="dropdown" style="width: 100%">
        {#each rows as row (row.id)}
            <button
                type="button"
                class="dropdown-item select-option"
                onclick={() => choose(row)}
            >
                <span class="txt-bold txt-code">{row.sku}</span>
                <span class="txt-ellipsis">
                    {row.product?.title ?? ""}{row.label ? ` — ${row.label}` : ""}
                </span>
                <span class="txt-hint txt-sm m-l-auto txt-nowrap">
                    {row.track_inventory ? `${row.available} available` : "not tracked"}
                </span>
            </button>
        {/each}

        {#if loading && !rows.length}
            <div class="txt-hint txt-center m-0 p-5">Searching…</div>
        {:else if searched && !rows.length}
            <div class="txt-hint txt-center m-0 p-5">
                Nothing matched. Product search reads titles and descriptions, not SKUs — a
                SKU has to be typed in full, and only reaches an active product.
            </div>
        {/if}
    </div>

    {#if bySKU && rows.length}
        <div class="field-help">Matched by exact SKU.</div>
    {/if}
{/if}
