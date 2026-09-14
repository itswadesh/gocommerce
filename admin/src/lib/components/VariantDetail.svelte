<script>
    /**
     * One variant, all of it.
     *
     * The product editor's Pricing, Inventory and Shipping cards are wrapped in
     * `{#if !hasOptions}`, and rightly so — with options, price and stock live
     * on each variant and a page-level price box would write to nothing. What
     * that left behind is the hole this drawer fills: barcode, compare-at,
     * cost, taxable, tracking, sell-when-out-of-stock, active, country of
     * origin, HS code, the weight *unit* and metadata had nowhere to be seen or
     * set on any variant of a product that has an option axis, even though
     * `PATCH /api/admin/variants/{id}` has always accepted every one of them.
     * A store selling anything in sizes could not record a barcode, a cost or a
     * customs code.
     *
     * The fields are the single-variant editor's, in its order and its words,
     * because they are the same fields — an operator who has set a cost on a
     * product with no options should not have to learn a second layout to set
     * one on a product with two sizes.
     *
     * Unlike the matrix around it, this commits on Save rather than on leaving
     * a field. The matrix edits one number at a time in a grid, where landing
     * on blur is the only sensible contract; a drawer with sixteen controls is a
     * form, and a form that writes as you tab through it cannot be abandoned.
     * Stock is the exception it always is: it goes to the inventory route as a
     * movement, never in the patch.
     */
    import { api } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import { parseMoney, stockClass } from "$lib/format.js";
    import {
        convertDimension,
    convertWeight,
        validateVariant,
        variantPatch,
        variantShape,
    } from "$lib/variantform.js";
    import { COUNTRIES } from "$lib/countries.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Select from "$lib/components/Select.svelte";

    let {
        // The variant to edit, or null while the drawer is shut. It is the row
        // the matrix holds, so it is already whole — there is no second read.
        variant = null,
        currency = "USD",
        // How many variants the product has, so the position field can say
        // what the last position is rather than leaving it to be guessed.
        total = 0,
        onsaved,
        onclose,
    } = $props();

    // Undefined everywhere in the panel: the reader's own locale is what a
    // person types in. It is named so the tests can pin one.
    const locale = undefined;

    let form = $state(variantShape(null, "USD"));
    let snapshot = $state(variantShape(null, "USD"));
    let errors = $state({});
    let saving = $state(false);

    /**
     * The unit the number in the weight box is currently expressed in.
     *
     * It trails `form.weight_unit` by exactly one change, and has to: Select
     * writes the new unit through its binding before it calls onchange, so the
     * conversion would otherwise have nothing to convert *from*.
     */
    let shownIn = $state("g");

    /** The same, for the three dimension boxes. See shownIn above. */
    let shownSizeIn = $state("mm");

    // Re-seeded whenever the drawer is pointed at a different variant. Keyed on
    // the id rather than on the object: the matrix refreshes the whole product
    // after every write, so the object identity changes under a drawer that is
    // still open and re-seeding on that would throw away what is being typed.
    let seededId = $state(null);
    $effect(() => {
        const id = variant?.id ?? null;
        if (id === seededId) return;
        seededId = id;
        form = variantShape(variant, currency);
        snapshot = variantShape(variant, currency);
        shownIn = form.weight_unit;
        shownSizeIn = form.dimension_unit;
        errors = {};
    });

    const dirty = $derived(JSON.stringify(form) !== JSON.stringify(snapshot));

    /**
     * Profit and margin, from the price and the cost.
     *
     * Derived rather than stored: they are two subtractions away from numbers
     * the variant already holds, and a stored copy is a number that can be
     * wrong. Both read "—" until there is a cost, because a margin computed
     * from an absent cost is a claim of 100%.
     *
     * Read with parseMoney rather than parseFloat, which is what the boxes
     * themselves are read with on the way to the wire: parseFloat takes "24,99"
     * as 24, so a comma-decimal typist would be shown a margin computed from a
     * price they never entered.
     */
    const profit = $derived.by(() => {
        const price = parseMoney(form.price, locale);
        const cost = parseMoney(form.cost, locale);
        if (price === null || cost === null) return null;
        return price - cost;
    });
    const margin = $derived.by(() => {
        const price = parseMoney(form.price, locale);
        if (profit === null || price === null || price <= 0) return null;
        return (profit / price) * 100;
    });

    /**
     * The country list, with an empty choice at the top — "not recorded" is a
     * real answer here and the only way back to it once one is picked.
     *
     * A code the list does not carry is kept as its own option rather than
     * silently swapped for nothing: the engine accepts any well-formed code, so
     * a record written by an import or an older list must survive being looked
     * at in the panel.
     */
    const originCountryOptions = $derived.by(() => {
        const options = [{ value: "", label: "Not recorded" }, ...COUNTRIES];
        const held = form.origin_country;
        if (held && !COUNTRIES.some((c) => c.value === held)) {
            options.push({ value: held, label: held });
        }
        return options;
    });

    /** The derived readouts, in the variant's currency and its own decimals. */
    function money(value) {
        return `${currency} ${value.toFixed(2)}`;
    }

    function onWeightUnitChange(next) {
        if (next === shownIn) return;
        form.weight = convertWeight(form.weight, shownIn, next);
        shownIn = next;
    }

    /** The same for the parcel, one side at a time. See onWeightUnitChange. */
    function onDimensionUnitChange(next) {
        if (next === shownSizeIn) return;
        form.length = convertDimension(form.length, shownSizeIn, next);
        form.width = convertDimension(form.width, shownSizeIn, next);
        form.height = convertDimension(form.height, shownSizeIn, next);
        shownSizeIn = next;
    }

    function addMetafield() {
        form.metafields = [...form.metafields, { key: "", value: "" }];
    }

    function removeMetafield(index) {
        form.metafields = form.metafields.filter((_, i) => i !== index);
    }

    function close() {
        onclose?.();
    }

    async function save(event) {
        event?.preventDefault();
        if (saving || !variant) return;

        errors = validateVariant(form, locale);
        if (Object.keys(errors).length) {
            toast.error("Some fields still need attention.");
            return;
        }

        // The variant's metadata goes in so the patch can carry across whatever
        // a module keeps beside the operator's own fields — `metadata` is
        // replaced whole, not merged, by the engine.
        const { body, stock } = variantPatch(
            form,
            snapshot,
            currency,
            locale,
            variant.metadata ?? {},
        );
        if (!Object.keys(body).length && stock === null) {
            close();
            return;
        }

        saving = true;
        try {
            if (Object.keys(body).length) {
                await api.patch(`/api/admin/variants/${variant.id}`, body);
            }
            if (stock !== null) {
                await api.post(`/api/admin/variants/${variant.id}/inventory`, { set: stock });
            }
            toast.success(`Saved ${form.sku.trim() || "variant"}`);
            // The caller re-reads the product; this drawer does not hold a copy
            // of it, so the fresh row arrives through `variant` on the next
            // open rather than being patched in here.
            onsaved?.();
            close();
        } catch (err) {
            // The engine has real refusals here — a SKU already used elsewhere
            // in the catalogue, "sell when out of stock" turned off while the
            // count is negative — and it explains them better than this could.
            toast.error(err);
        } finally {
            saving = false;
        }
    }
</script>

<Drawer
    open={!!variant}
    size="sm"
    title={variant ? `Variant ${variant.label || variant.sku}` : "Variant"}
    onclose={close}
>
    {#if variant}
        <form id="variant-detail-form" onsubmit={save}>
            <h6 class="section-title m-t-0">
                <i class="ri-money-dollar-circle-line" aria-hidden="true"></i>
                Pricing
            </h6>

            <div class="fields">
                <!-- The currency goes in the label, as the New variant drawer
                     has it. `.money-field` is the variant table's prefix-inside
                     -the-box treatment and it belongs there: it sets its own
                     `display: flex` with no wrap, so a labelled one in a
                     `.fields` row puts the box outside the drawer. -->
                <div class="field required" class:error={!!errors.price}>
                    <label for="vd-price">Price ({currency})</label>
                    <input id="vd-price" type="text" inputmode="decimal" bind:value={form.price} />
                </div>
                <div class="delimiter"></div>
                <div class="field" class:error={!!errors.compare_at}>
                    <label for="vd-compare-at">Compare-at ({currency})</label>
                    <input
                        id="vd-compare-at"
                        type="text"
                        inputmode="decimal"
                        bind:value={form.compare_at}
                    />
                </div>
            </div>
            {#if errors.price}<div class="field-help error">{errors.price}</div>{/if}
            {#if errors.compare_at}<div class="field-help error">{errors.compare_at}</div>{/if}
            <div class="field-help">
                Compare-at is the price a storefront strikes through. Emptying the box takes this
                variant off sale — the engine tells an emptied field from one the patch never
                mentioned, so the struck-through price really does come off. Both are in
                {currency}.
            </div>

            <div class="field m-t-sm">
                <input id="vd-taxable" type="checkbox" bind:checked={form.taxable} />
                <label for="vd-taxable">Charge tax on this variant</label>
            </div>

            <hr class="m-t-base m-b-base" />

            <div class="fields">
                <div class="field" class:error={!!errors.cost}>
                    <label for="vd-cost">Cost per item</label>
                    <input id="vd-cost" type="text" inputmode="decimal" bind:value={form.cost} />
                </div>
                <div class="field readonly-field">
                    <span class="readonly-label">Profit</span>
                    <output class="readonly-value">
                        {profit === null ? "—" : money(profit)}
                    </output>
                </div>
                <div class="field readonly-field">
                    <span class="readonly-label">Margin</span>
                    <output class="readonly-value">
                        {margin === null ? "—" : margin.toFixed(1) + "%"}
                    </output>
                </div>
            </div>
            {#if errors.cost}<div class="field-help error">{errors.cost}</div>{/if}
            <div class="field-help">
                Cost is yours alone — a storefront never sees it. Profit and margin follow from it
                and the price.
            </div>

            <h6 class="section-title">
                <i class="ri-archive-2-line" aria-hidden="true"></i>
                Inventory
            </h6>

            <div class="fields">
                <div class="field required" class:error={!!errors.sku}>
                    <label for="vd-sku">SKU</label>
                    <input id="vd-sku" type="text" class="txt-code" bind:value={form.sku} />
                </div>
                <div class="delimiter"></div>
                <div class="field">
                    <label for="vd-barcode">Barcode</label>
                    <input id="vd-barcode" type="text" bind:value={form.barcode} />
                </div>
            </div>
            {#if errors.sku}<div class="field-help error">{errors.sku}</div>{/if}

            <div class="field m-t-sm">
                <input
                    id="vd-track"
                    type="checkbox"
                    class="switch"
                    bind:checked={form.track_inventory}
                />
                <label for="vd-track">Track quantity</label>
            </div>

            <div class="field m-t-sm" class:disabled={!form.track_inventory}>
                <label for="vd-stock">Quantity on hand</label>
                <input
                    id="vd-stock"
                    type="number"
                    min="0"
                    disabled={!form.track_inventory}
                    bind:value={form.stock_on_hand}
                />
            </div>
            <div class="field-help">
                {#if form.track_inventory}
                    Saving this sends a stock take to the inventory endpoint, not the variant
                    patch — stock only ever moves as a transactional adjustment, so a sale that
                    lands mid-edit cannot be overwritten, and every movement is recorded with who
                    made it.
                    <strong class={stockClass(variant.available)}>{variant.available}</strong>
                    available right now ({variant.stock_reserved} reserved for open orders).
                {:else}
                    Untracked variants can always be bought. Turn tracking on to hold a count.
                {/if}
            </div>

            {#if form.track_inventory}
                <div class="field m-t-sm">
                    <input
                        id="vd-continue"
                        type="checkbox"
                        class="switch"
                        bind:checked={form.continue_selling}
                    />
                    <label for="vd-continue">Sell when out of stock</label>
                </div>
                <div class="field-help">
                    {#if form.continue_selling}
                        Orders are taken past zero and the count goes negative, so the backlog is
                        visible rather than hidden.
                    {:else}
                        A shopper is refused once the last one is spoken for. Turning this off is
                        refused while the count is negative — restock first.
                    {/if}
                </div>
            {/if}

            <h6 class="section-title">
                <i class="ri-truck-line" aria-hidden="true"></i>
                Shipping
            </h6>

            <div class="fields">
                <div class="field">
                    <label for="vd-weight">Weight</label>
                    <input
                        id="vd-weight"
                        type="number"
                        min="0"
                        step="any"
                        bind:value={form.weight}
                    />
                </div>
                <div class="delimiter"></div>
                <!-- No label: the options name themselves, and the half is read
                     as part of the number beside it. -->
                <div class="field">
                    <Select
                        id="vd-weight-unit"
                        ariaLabel="Weight unit"
                        bind:value={form.weight_unit}
                        onchange={onWeightUnitChange}
                        options={[
                            { value: "g", label: "Grams (g)" },
                            { value: "kg", label: "Kilograms (kg)" },
                            { value: "oz", label: "Ounces (oz)" },
                            { value: "lb", label: "Pounds (lb)" },
                        ]}
                    />
                </div>
                <div class="delimiter"></div>
                <div class="field">
                    <label for="vd-hs-code">HS code</label>
                    <input
                        id="vd-hs-code"
                        type="text"
                        inputmode="numeric"
                        placeholder="6109.10"
                        bind:value={form.hs_code}
                    />
                </div>
            </div>
            <div class="field-help">
                The record holds whole grams; the unit is how you read them back, exactly as a
                currency code is. Both are sent as typed and converted by the engine, so switching
                units here shows the same mass in the new unit rather than relabelling the figure.
                The tariff number is stored as digits, so 6109.10 and 610910 are the same code.
            </div>

            <!-- The parcel. Same row, same order and same words as the product
                 editor's, because it is the same field — an operator who has
                 measured a box on a product with no options should not have to
                 learn a second layout to measure one on a product with sizes. -->
            <div class="fields m-t-sm">
                <div class="field">
                    <label for="vd-length">Length</label>
                    <input
                        id="vd-length"
                        type="number"
                        min="0"
                        step="any"
                        placeholder="—"
                        bind:value={form.length}
                    />
                </div>
                <div class="delimiter"></div>
                <div class="field">
                    <label for="vd-width">Width</label>
                    <input
                        id="vd-width"
                        type="number"
                        min="0"
                        step="any"
                        placeholder="—"
                        bind:value={form.width}
                    />
                </div>
                <div class="delimiter"></div>
                <div class="field">
                    <label for="vd-height">Height</label>
                    <input
                        id="vd-height"
                        type="number"
                        min="0"
                        step="any"
                        placeholder="—"
                        bind:value={form.height}
                    />
                </div>
                <div class="delimiter"></div>
                <!-- No label, for the reason the weight unit has none. -->
                <div class="field">
                    <Select
                        id="vd-dimension-unit"
                        ariaLabel="Dimension unit"
                        bind:value={form.dimension_unit}
                        onchange={onDimensionUnitChange}
                        options={[
                            { value: "mm", label: "Millimetres (mm)" },
                            { value: "cm", label: "Centimetres (cm)" },
                            { value: "m", label: "Metres (m)" },
                            { value: "in", label: "Inches (in)" },
                        ]}
                    />
                </div>
            </div>
            <div class="field-help">
                For a carrier that prices by parcel size as well as weight. An empty box means
                that side has not been measured, which is not the same as a side of zero — a
                zero-height parcel is something a carrier will quote for. Stored in whole
                millimetres and read back in the unit you chose.
            </div>

            <div class="field m-t-sm">
                <label for="vd-origin">Country of origin</label>
                <Select
                    id="vd-origin"
                    placeholder="Not recorded"
                    bind:value={form.origin_country}
                    options={originCountryOptions}
                />
            </div>
            <div class="field-help">
                For the customs form on a cross-border parcel, and fine left empty. Stored as its
                two-letter code.
            </div>

            <h6 class="section-title">
                <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
                Availability and order
            </h6>

            <div class="field">
                <input id="vd-active" type="checkbox" class="switch" bind:checked={form.active} />
                <label for="vd-active">Available for sale</label>
            </div>
            <div class="field-help">
                An inactive variant stays in the catalogue and keeps its stock and its history,
                and a storefront will not sell it. This is how one size comes off sale without
                archiving the product it belongs to.
            </div>

            <div class="field m-t-sm">
                <label for="vd-position">Position</label>
                <input id="vd-position" type="number" min="0" bind:value={form.position} />
            </div>
            <div class="field-help">
                Where this variant sits in the product's own order, counting from zero — which is
                the order a storefront shows them in.
                {#if total > 1}
                    This product has {total} variants, so the last position is {total - 1}.
                {/if}
                The arrows on the variant table move one row at a time and renumber the list; this
                box is for putting a variant somewhere specific in one move.
            </div>

            <h6 class="section-title">
                <i class="ri-price-tag-2-line" aria-hidden="true"></i>
                Variant metafields
            </h6>

            {#if form.metafields.length}
                <div class="metafields m-b-sm">
                    {#each form.metafields as field, i (i)}
                        <div class="metafield-row">
                            <div class="field">
                                <label for="vd-mf-key-{i}">Name</label>
                                <input
                                    id="vd-mf-key-{i}"
                                    type="text"
                                    placeholder="warehouse_bin"
                                    bind:value={field.key}
                                />
                            </div>
                            <div class="field">
                                <label for="vd-mf-value-{i}">Value</label>
                                <input id="vd-mf-value-{i}" type="text" bind:value={field.value} />
                            </div>
                            <button
                                type="button"
                                class="btn circle sm transparent secondary"
                                aria-label="Remove {field.key || 'this metafield'}"
                                title="Remove"
                                onclick={() => removeMetafield(i)}
                            >
                                <i class="ri-close-line" aria-hidden="true"></i>
                            </button>
                        </div>
                    {/each}
                </div>
            {/if}

            {#if errors.metafields}
                <div class="field-help error">{errors.metafields}</div>
            {/if}

            <button type="button" class="btn sm secondary" onclick={addMetafield}>
                <i class="ri-add-line" aria-hidden="true"></i>
                <span class="txt">Add metafield</span>
            </button>

            <div class="field-help">
                Your own fields on this one variant, for a storefront or an integration to read.
                Stored under <code>metadata.custom</code>, so whatever a module keeps beside them
                survives.
            </div>
        </form>
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={close}>
            <span class="txt">Cancel</span>
        </button>
        {#if dirty}
            <span class="txt-hint txt-sm m-r-sm">Unsaved changes</span>
        {/if}
        <button
            type="submit"
            form="variant-detail-form"
            class="btn"
            class:loading={saving}
            disabled={saving}
        >
            <span class="txt">Save variant</span>
        </button>
    {/snippet}
</Drawer>
