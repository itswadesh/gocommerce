<script>
    import { api, query } from "$lib/api.js";
    import { formatMoney, stockClass } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    /* The search itself is VariantSearch's, imported rather than written a
       second time. It knows the thing this picker had wrong: `?q=` on the
       product listing matches `lower(p.title)` and `lower(p.description)` and
       never a SKU (core/catalog.go, productFilters), so a typed SKU finds
       nothing — and it falls back to the exact-SKU lookup the storefront uses.
       Two implementations of that would disagree the day the engine gains a
       real variant search. */
    import { findVariants } from "$lib/components/VariantSearch.svelte";

    /**
     * Choosing a variant to put on an order.
     *
     * It replaces a `Select` fed from one unfiltered page of 200 products, which
     * had two faults a catalogue makes fatal. The Select's own search box
     * filters the options already in the browser, so the 201st product could
     * not be reached at all and a store past that size was silently unsellable
     * by hand. And the option read `Title · Label — SKU` and nothing else, so
     * an inactive variant — which the cart refuses outright — looked exactly
     * like a sellable one until Place order came back with a 409.
     *
     * So: the box asks the store, debounced, and every row carries the two
     * facts that decide whether it can be sold. The shell is
     * CategoryPicker's, which is Select's, which is PocketBase's — same
     * `.input.select.single` trigger, same `.dropdown[popover]`, same keyboard
     * contract.
     *
     * What it does NOT do is filter by product status. A draft product is a
     * perfectly ordinary thing to sell across a trade counter, and the cart
     * does not look at product status either: it refuses on the VARIANT's
     * `active` flag, and on stock when the variant tracks it and does not
     * oversell. Those are the two rows below that are struck out, so the panel
     * refuses exactly what the engine would and nothing else.
     */
    let {
        /** The chosen variant id as a string, or "" — the shape Select had. */
        value = $bindable(""),
        id = undefined,
        placeholder = "Choose a variant",
        disabled = false,
        /** The chosen row, handed back whole so the caller does not have to
         *  re-find a product it never held. */
        onchange,
    } = $props();

    const PAGE = 50;

    const dropdownId = "variant-picker-" + Math.random().toString(36).slice(2, 10);

    let dropdown = $state(null);
    let root = $state(null);
    let search = $state("");
    let open = $state(false);
    let loading = $state(false);
    let rows = $state([]);
    /** How many PRODUCTS the store holds behind this answer — the listing's own
     *  total, which counts products because that is what the page was cut
     *  from. Saying "variants" over it would be describing these rows with
     *  somebody else's number. */
    let total = $state(0);
    let shownProducts = $state(0);
    /** True when nothing matched the text and the exact-SKU lookup did. */
    let bySKU = $state(false);
    let timer = null;
    /* Two keystrokes leave two replies in flight, and the list would otherwise
       settle on whichever arrived last rather than on what is in the box. */
    let seq = 0;

    /** The row just picked, for the trigger: the next search replaces the list
     *  it came from, and the trigger would otherwise fall back to the
     *  placeholder for a variant that is plainly chosen. */
    let chosen = $state(null);

    /** One row per variant, in the shape this picker hands back. */
    function shape(v, title) {
        return {
            value: String(v.id),
            variant_id: v.id,
            sku: v.sku,
            title,
            variant_label: v.label ?? "",
            unit_price: v.price,
            available: v.available,
            tracked: v.track_inventory,
            /* Exactly the engine's own two refusals — see the header. */
            sellable:
                v.active && (!v.track_inventory || v.continue_selling || (v.available ?? 0) > 0),
            active: v.active,
        };
    }

    const selected = $derived(
        (chosen && chosen.value === value ? chosen : null) ??
            rows.find((r) => r.value === value) ??
            null,
    );

    /* The store holds more than came back, so the box is the way to the rest
       rather than a convenience over what is already here. */
    const truncated = $derived(total > shownProducts);

    async function fetchPage(term) {
        const mine = ++seq;
        loading = true;
        try {
            let next = [];
            let meta = null;
            let matchedBySKU = false;
            if (term) {
                const found = await findVariants(term, { limit: PAGE });
                next = found.rows.map((r) => shape(r, r.product?.title ?? ""));
                meta = found.meta;
                matchedBySKU = found.bySKU;
            } else {
                // Nothing typed: one page of the catalog, so opening the picker
                // shows something to choose from rather than an empty box.
                const result = await api.get("/api/admin/products" + query({ limit: PAGE }));
                next = (result.data ?? []).flatMap((p) =>
                    (p.variants ?? []).map((v) => shape(v, p.title)),
                );
                meta = result.meta;
            }
            // The box may have moved on while this was in flight; a stale answer
            // overwriting a newer one is the bug a debounce is only half a
            // defence against.
            if (mine !== seq) return;
            rows = next;
            total = meta?.total ?? 0;
            shownProducts = Math.min(meta?.limit ?? next.length, meta?.total ?? next.length);
            bySKU = matchedBySKU;
        } catch (err) {
            if (mine === seq) toast.error(err);
        } finally {
            if (mine === seq) loading = false;
        }
    }

    /**
     * Debounced, because this one goes over the wire. A request per keystroke
     * sends five for "shirt" and their answers can land out of order.
     */
    function onSearchInput() {
        clearTimeout(timer);
        loading = true;
        const wanted = search.trim();
        timer = setTimeout(() => fetchPage(wanted), 250);
    }

    function close() {
        if (dropdown?.matches(":popover-open")) dropdown.hidePopover();
    }

    function pick(row) {
        if (!row.sellable) return;
        value = row.value;
        chosen = row;
        onchange?.(row);
        close();
    }

    function onToggle(event) {
        open = event.newState === "open";
        if (open) {
            if (!rows.length) fetchPage("");
            return;
        }
        search = "";
        clearTimeout(timer);
    }

    function onTriggerKeydown(event) {
        if (event.key === "ArrowDown" || event.key === "ArrowUp" || event.key === "Enter") {
            if (!open) {
                event.preventDefault();
                dropdown?.showPopover();
                queueMicrotask(() => focusOption(event.key === "ArrowUp" ? -1 : 0));
            }
        }
    }

    function optionButtons() {
        return [...(dropdown?.querySelectorAll(".select-option:not([disabled])") ?? [])];
    }

    function focusOption(index) {
        const items = optionButtons();
        if (!items.length) return;
        items[(index + items.length) % items.length].focus();
    }

    function onDropdownKeydown(event) {
        const items = optionButtons();
        const here = items.indexOf(document.activeElement);
        if (event.key === "ArrowDown") {
            event.preventDefault();
            focusOption(here + 1);
        } else if (event.key === "ArrowUp") {
            event.preventDefault();
            focusOption(here - 1);
        } else if (event.key === "Home") {
            event.preventDefault();
            focusOption(0);
        } else if (event.key === "End") {
            event.preventDefault();
            focusOption(items.length - 1);
        }
    }

    function onFocusOut(event) {
        if (!event.relatedTarget || !root?.contains(event.relatedTarget)) close();
    }

    function stockWords(row) {
        if (!row.active) return "Not for sale";
        if (!row.tracked) return "Not tracked";
        if ((row.available ?? 0) <= 0) return "Out of stock";
        return `${row.available} left`;
    }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div
    bind:this={root}
    class="input select single variant-picker"
    class:disabled
    onfocusout={onFocusOut}
>
    <button
        type="button"
        {id}
        {disabled}
        class="selected-container"
        popovertarget={dropdownId}
        aria-haspopup="listbox"
        aria-expanded={open}
        onkeydown={onTriggerKeydown}
    >
        {#if selected}
            <div class="selected-item">
                {selected.title}{selected.variant_label ? " · " + selected.variant_label : ""}
            </div>
        {:else}
            <span class="placeholder">{placeholder}</span>
        {/if}
    </button>

    <div
        bind:this={dropdown}
        id={dropdownId}
        class="dropdown"
        popover="auto"
        tabindex="-1"
        role="listbox"
        ontoggle={onToggle}
        onkeydown={onDropdownKeydown}
    >
        <div class="fields dropdown-search">
            <div class="field">
                <input
                    type="text"
                    placeholder="Product name, or a SKU in full…"
                    bind:value={search}
                    oninput={onSearchInput}
                />
            </div>
            {#if search}
                <div class="field addon p-r-5">
                    <button
                        type="button"
                        title="Clear"
                        class="btn sm secondary transparent circle"
                        onclick={() => ((search = ""), onSearchInput())}
                    >
                        <i class="ri-close-line" aria-hidden="true"></i>
                    </button>
                </div>
            {/if}
        </div>

        {#each rows as row (row.value)}
            <button
                type="button"
                role="option"
                aria-selected={row.value === value}
                disabled={!row.sellable}
                title={row.sellable ? undefined : "The checkout would refuse this line"}
                class="dropdown-item select-option"
                class:active={row.value === value}
                onclick={() => pick(row)}
            >
                <span class="variant-text">
                    <span class="txt-ellipsis">
                        {row.title}{row.variant_label ? " · " + row.variant_label : ""}
                    </span>
                    <span class="txt-hint txt-sm txt-code txt-ellipsis">{row.sku}</span>
                </span>
                <span class="variant-facts">
                    <span class="txt-money txt-sm">{formatMoney(row.unit_price)}</span>
                    <span class="txt-sm {stockClass(row.available ?? 0, row.tracked)}">
                        {stockWords(row)}
                    </span>
                </span>
            </button>
        {/each}

        {#if loading && !rows.length}
            <div class="txt-hint txt-center m-0 p-5">Searching…</div>
        {:else if !rows.length}
            <div class="txt-hint txt-center m-0 p-5">
                {#if search.trim()}
                    Nothing matched. Product search reads titles and descriptions, not SKUs — a
                    SKU has to be typed in full, and only reaches an active product.
                {:else}
                    No products yet
                {/if}
            </div>
        {:else if bySKU}
            <div class="txt-hint txt-sm txt-center m-0 p-5">Matched by exact SKU.</div>
        {:else if truncated}
            <!-- The honest version of a capped list: it says how much it is not
                 showing, so nobody concludes a product does not exist. -->
            <div class="txt-hint txt-sm txt-center m-0 p-5">
                From the first {shownProducts} of {total} products. Narrow the search to reach
                the rest.
            </div>
        {/if}
    </div>
</div>

<style>
    /* `.dropdown-item` is a centred flex row, which is right for one line of
       text and wrong for a row that carries a name, a code, a price and a
       count. The title stack takes the space and the two facts hold the right
       edge, so the figures line up down the list rather than tracking each
       title's length. */
    .variant-text {
        display: flex;
        flex-direction: column;
        flex: 1;
        min-width: 0;
        text-align: left;
    }
    .variant-facts {
        display: flex;
        flex-direction: column;
        align-items: flex-end;
        white-space: nowrap;
    }
</style>
