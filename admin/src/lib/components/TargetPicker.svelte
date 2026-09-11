<script>
    import { api, query } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";

    /**
     * The chip multi-select a discount aims itself with: products, collections
     * or categories, many of them.
     *
     * It is the product form's Collections box, extracted rather than copied so
     * this screen does not become a third hand-rolled version of the pattern —
     * and extracted rather than folded into `Select.svelte`, which serves most
     * screens as a single-select and has no business growing a mode for one
     * caller. Every class here is already driven by `gocommerce.css`
     * (`.input.select.multiple .selected-container > .label`, `.chip-add`,
     * `.chip-new`), so nothing new is added to a stylesheet.
     *
     * What it deliberately does NOT carry, unlike the product form's copy: "type
     * a name that does not exist yet to create it". A promotion form is not
     * where a category gets invented.
     */
    let {
        /** "products" | "collections" | "categories" — the discount's scope. */
        kind,
        value = $bindable([]),
        /**
         * Chip labels for ids this picker has not fetched, so a product on page
         * forty of the catalog still renders by name. It comes from the single
         * GET's resolved targets.
         */
        names = new Map(),
        /** Ids whose catalog row is gone. Drawn red rather than hidden. */
        missing = new Set(),
        id = undefined,
        disabled = false,
    } = $props();

    const dropdownId = "target-picker-" + Math.random().toString(36).slice(2, 10);

    let root = $state(null);
    let trigger = $state(null);
    let dropdown = $state(null);
    let filterField = $state(null);
    let open = $state(false);
    let filter = $state("");

    /** The rows to choose from, in `{id, label}` shape whatever the kind is. */
    let options = $state([]);
    let loading = $state(false);
    let loadedKind = null;
    let timer = null;

    const noun = $derived(
        { products: "product", collections: "collection", categories: "category" }[kind] ??
            "target",
    );

    /*
     * Chips for what is chosen. An id the picker has never fetched still has to
     * render, so three sources are tried in order: the rows in hand, the names
     * the drawer was given, and finally the bare id — which is honest about what
     * is known rather than drawing an empty chip.
     */
    const chosen = $derived(
        value.map((v) => ({
            id: v,
            label: options.find((o) => o.id === v)?.label ?? names.get(v) ?? `#${v}`,
            missing: missing.has(v),
        })),
    );

    const term = $derived(filter.trim().toLowerCase());
    const visible = $derived(
        // Products are searched at the store, so they arrive already filtered;
        // the other two are whole vocabularies and filter here.
        kind === "products" ? options : options.filter((o) => o.label.toLowerCase().includes(term)),
    );

    /**
     * Each kind comes from a route that already exists and is already covered by
     * `catalog.read`, which every role keeps.
     *
     * Products are searched at the store: a catalog is not a two-hundred-row
     * vocabulary, and an operator must be able to reach any product. Collections
     * come back whole, with the same `limit=200` trade the product form already
     * admits — a store past that cannot reach everything, which the field help
     * says out loud. Categories come back flat and are shown by `full_name`, so
     * "Shirts" under Clothing is distinguishable from "Shirts" under Kids and no
     * tree has to be rebuilt in the browser.
     */
    async function loadOptions(search = "") {
        loading = true;
        try {
            if (kind === "products") {
                const result = await api.get(
                    "/api/admin/products" + query({ q: search, limit: 50 }),
                );
                // The box may have moved on while this was in flight; a stale
                // answer overwriting a newer one is the bug a debounce alone
                // only half prevents.
                if (search !== filter) return;
                options = (result.data ?? []).map((p) => ({ id: p.id, label: p.title }));
            } else if (kind === "collections") {
                const result = await api.get("/api/admin/collections" + query({ limit: 200 }));
                options = (result.data ?? []).map((c) => ({ id: c.id, label: c.title }));
            } else {
                const result = await api.get("/api/admin/categories?flat=1");
                options = (result.data ?? []).map((c) => ({
                    id: c.id,
                    label: c.full_name || c.title,
                }));
            }
            loadedKind = kind;
        } catch (err) {
            toast.error(err);
        } finally {
            if (kind !== "products" || search === filter) loading = false;
        }
    }

    /** Debounced, because the product search goes over the wire per keystroke. */
    function onFilterInput() {
        if (kind !== "products") return;
        clearTimeout(timer);
        const wanted = filter;
        loading = true;
        timer = setTimeout(() => loadOptions(wanted), 250);
    }

    function toggle(optionID) {
        value = value.includes(optionID)
            ? value.filter((v) => v !== optionID)
            : [...value, optionID];
    }

    function items() {
        return [...(dropdown?.querySelectorAll(".select-option") ?? [])];
    }

    function focusItem(index) {
        const rows = items();
        if (!rows.length) return;
        rows[(index + rows.length) % rows.length].focus();
    }

    function onTriggerKeydown(event) {
        if (event.key !== "ArrowDown" && event.key !== "ArrowUp" && event.key !== "Enter") return;
        if (dropdown?.matches(":popover-open")) return;
        event.preventDefault();
        dropdown?.showPopover();
        queueMicrotask(() => focusItem(event.key === "ArrowUp" ? -1 : 0));
    }

    function onDropdownKeydown(event) {
        const rows = items();
        const here = rows.indexOf(document.activeElement);
        if (event.key === "ArrowDown") {
            event.preventDefault();
            focusItem(here + 1);
        } else if (event.key === "ArrowUp") {
            event.preventDefault();
            focusItem(here - 1);
        } else if (event.key === "Home") {
            event.preventDefault();
            focusItem(0);
        } else if (event.key === "End") {
            event.preventDefault();
            focusItem(rows.length - 1);
        }
    }

    function onToggle(event) {
        open = event.newState === "open";
        if (open) {
            if (loadedKind !== kind) {
                options = [];
                filter = "";
                loadOptions("");
            }
            queueMicrotask(() => filterField?.focus());
            return;
        }
        filter = "";
        // Escape and light dismiss leave focus on a row that is no longer
        // rendered, which drops it to the body and loses the operator's place.
        if (dropdown?.contains(document.activeElement)) trigger?.focus();
    }

    function onFocusOut(event) {
        if (event.relatedTarget && root?.contains(event.relatedTarget)) return;
        if (dropdown?.matches(":popover-open")) dropdown.hidePopover();
    }
</script>

<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div bind:this={root} class="input select multiple" class:disabled onfocusout={onFocusOut}>
    <button
        bind:this={trigger}
        type="button"
        {id}
        {disabled}
        class="selected-container"
        popovertarget={dropdownId}
        aria-haspopup="listbox"
        aria-expanded={open}
        onkeydown={onTriggerKeydown}
    >
        {#each chosen as target (target.id)}
            <!-- A target whose catalog row is gone is drawn red rather than
                 dropped: the promotion has narrowed, and the operator has to be
                 the one who decides what to do about it. -->
            <span class="label" class:danger={target.missing}>
                {#if target.missing}
                    <i class="ri-error-warning-line" aria-hidden="true"></i>
                {/if}
                {target.label}
            </span>
        {/each}
        <span class="label chip-new">
            <i class="ri-add-line" aria-hidden="true"></i>
            {chosen.length ? "Add" : `Add ${noun}`}
        </span>
    </button>

    {#if chosen.length}
        <button
            type="button"
            class="chip-add"
            popovertarget={dropdownId}
            aria-label="Add a {noun}"
            title="Add a {noun}"
        >
            <i class="ri-add-circle-line" aria-hidden="true"></i>
        </button>
    {/if}

    <div
        bind:this={dropdown}
        id={dropdownId}
        class="dropdown"
        popover="auto"
        tabindex="-1"
        role="listbox"
        aria-multiselectable="true"
        aria-label="Discount targets"
        ontoggle={onToggle}
        onkeydown={onDropdownKeydown}
    >
        <div class="fields dropdown-search">
            <div class="field">
                <input
                    bind:this={filterField}
                    type="text"
                    aria-label="Filter {kind}"
                    placeholder="Search {kind}…"
                    bind:value={filter}
                    oninput={onFilterInput}
                />
            </div>
            {#if filter}
                <div class="field addon p-r-5">
                    <button
                        type="button"
                        title="Clear"
                        class="btn sm secondary transparent circle"
                        onclick={() => ((filter = ""), kind === "products" && loadOptions(""))}
                    >
                        <i class="ri-close-line" aria-hidden="true"></i>
                    </button>
                </div>
            {/if}
        </div>

        <!--
            The tick is an icon on a button rather than an <input
            type="checkbox">. A popover is a DOM descendant of the field it
            belongs to even while it is drawn in the top layer, so a real
            checkbox in here matches form.css's
            `.field:has(input[type="checkbox"])` — a rule written for a field
            that *is* a checkbox. It would strip the fill off the control, draw
            a phantom box beside the label, and hide this popover's own filter
            box, which that rule shrinks to 1px.
        -->
        {#each visible as option (option.id)}
            {@const picked = value.includes(option.id)}
            <button
                type="button"
                role="option"
                aria-selected={picked}
                class="dropdown-item select-option"
                onclick={() => toggle(option.id)}
            >
                <i
                    class={picked ? "ri-checkbox-line txt-success" : "ri-checkbox-blank-line txt-hint"}
                    aria-hidden="true"
                ></i>
                <span class="txt-ellipsis">{option.label}</span>
            </button>
        {/each}

        {#if loading && !visible.length}
            <div class="txt-hint txt-center m-0 p-5">Searching…</div>
        {:else if !visible.length}
            <div class="txt-hint txt-center m-0 p-5">
                {filter ? `No ${kind} match that` : `No ${kind} yet`}
            </div>
        {/if}
    </div>
</div>
