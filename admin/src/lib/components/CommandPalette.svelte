<script>
    /**
     * One box that reaches the whole store.
     *
     * Until now every screen searched only itself: finding an order, a product,
     * a customer and a discount meant visiting four screens, each with its own
     * box and its own semantics. The endpoints to do better have all existed
     * for a long time — `?q=` on products, orders, customers, discounts,
     * categories and media, and `GET /api/products/sku/{sku}`, which api.http
     * calls "the catalog's stable key" and which no UI had ever called. This
     * fans out to them in parallel and draws the answers in one list.
     *
     * Three decisions worth stating.
     *
     * It asks only the sources the operator may read. A palette that returns
     * "4 discounts" to somebody who holds no discounts.read would be showing
     * them the shape of data they are not allowed, and every row would 403 on
     * the way in. `can()` is a rune, so the set re-derives if the role is
     * re-cut mid-session.
     *
     * Every result is a real `<a href>`. Middle-click opens an order in a
     * second tab, which is exactly the working pattern the palette is for, and
     * SvelteKit turns the same anchor into a client navigation on a plain
     * click. Nothing here calls goto() except the Enter key, which has no
     * anchor to click.
     *
     * Where the panel has no route for a record — a customer is an email that
     * several orders share, a discount and a category open in a drawer — the
     * result lands on that screen filtered to the one row rather than inventing
     * an address the screen does not read. Those screens all keep `q` in the
     * URL already, so the link is the same one the operator would have built by
     * hand.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, can, query } from "$lib/api.js";
    import { dismissable } from "$lib/dismiss.js";
    import { portal } from "$lib/portal.js";
    import { trapFocus } from "$lib/focus.js";
    import { formatMoney } from "$lib/format.js";
    import { NAV, visibleNav } from "$lib/nav.js";

    let { open = $bindable(false) } = $props();

    /* Per source, not overall: six lists of twenty is a scroll, and the point
       of a palette is the first screenful. */
    const PER_SOURCE = 5;

    /**
     * The sources, in the order they are drawn.
     *
     * `run` returns rows already shaped for the list — title, subtitle, href —
     * so nothing below this table knows what a product or an order looks like.
     */
    const SOURCES = [
        {
            key: "products",
            group: "Products",
            icon: "ri-price-tag-3-line",
            right: "catalog.read",
            async run(q) {
                const res = await api.get("/api/admin/products" + query({ q, limit: PER_SOURCE }));
                return (res.data ?? []).map((row) => ({
                    title: row.title,
                    subtitle: row.slug,
                    href: `/products/${row.id}`,
                }));
            },
        },
        {
            key: "orders",
            group: "Orders",
            icon: "ri-shopping-bag-3-line",
            right: "orders.read",
            async run(q) {
                const res = await api.get("/api/admin/orders" + query({ q, limit: PER_SOURCE }));
                return (res.data ?? []).map((row) => ({
                    title: row.number,
                    subtitle: [row.name || row.email, formatMoney(row.total)]
                        .filter(Boolean)
                        .join(" · "),
                    href: `/orders/${row.id}`,
                }));
            },
        },
        {
            key: "customers",
            group: "Customers",
            icon: "ri-user-3-line",
            right: "customers.read",
            async run(q) {
                const res = await api.get("/api/admin/customers" + query({ q, limit: PER_SOURCE }));
                return (res.data ?? []).map((row) => ({
                    title: row.email,
                    subtitle: row.name,
                    // No customer route exists and D22 says there will not be
                    // one; this is the filtered list, which is one row and a
                    // click from the drawer.
                    href: `/customers?q=${encodeURIComponent(row.email)}`,
                }));
            },
        },
        {
            key: "discounts",
            group: "Discounts",
            icon: "ri-price-tag-2-line",
            right: "discounts.read",
            async run(q) {
                const res = await api.get("/api/admin/discounts" + query({ q, limit: PER_SOURCE }));
                return (res.data ?? []).map((row) => ({
                    title: row.code || row.title,
                    subtitle: row.code ? row.title : "Automatic",
                    href: `/discounts?q=${encodeURIComponent(row.code || row.title)}`,
                }));
            },
        },
        {
            key: "categories",
            group: "Categories",
            icon: "ri-node-tree",
            right: "catalog.read",
            async run(q) {
                const res = await api.get("/api/admin/categories" + query({ q, limit: PER_SOURCE }));
                return (res.data ?? []).map((row) => ({
                    title: row.full_name || row.title,
                    subtitle: row.slug,
                    href: `/categories?q=${encodeURIComponent(row.title)}`,
                }));
            },
        },
        {
            key: "media",
            group: "Media",
            icon: "ri-image-2-line",
            right: "catalog.read",
            async run(q) {
                const res = await api.get("/api/admin/media" + query({ q, limit: PER_SOURCE }));
                return (res.data ?? []).map((row) => ({
                    title: row.filename || row.alt || `#${row.id}`,
                    subtitle: row.alt && row.filename ? row.alt : row.mime,
                    href: `/media?q=${encodeURIComponent(row.filename || row.alt || "")}`,
                }));
            },
        },
    ];

    let term = $state("");
    let rows = $state([]);
    let busy = $state(false);
    let active = $state(0);
    let box = $state(null);
    let panel = $state(null);

    /* Two keystrokes leave two fan-outs in flight, and the list would otherwise
       settle on whichever set of six replies finished last. */
    let reqId = 0;
    let timer = null;

    const trimmed = $derived(term.trim());

    /**
     * The destinations, matched against what has been typed.
     *
     * They are answered locally and instantly, which is what makes the palette
     * usable as navigation: "or" reaches Orders before any request has left.
     * An empty box offers all of them, so opening the palette and pressing
     * Enter is never a dead end.
     */
    const destinations = $derived.by(() => {
        const wanted = trimmed.toLowerCase();
        return visibleNav(NAV)
            .filter(
                (item) =>
                    !wanted ||
                    item.label.toLowerCase().includes(wanted) ||
                    (item.keywords || "").includes(wanted),
            )
            .map((item) => ({
                group: "Go to",
                icon: item.icon,
                title: item.label,
                subtitle: item.href,
                href: item.href,
            }));
    });

    const results = $derived([...destinations, ...rows]);

    /** The flat list, cut into the runs of one group that the list draws. */
    const groups = $derived.by(() => {
        const out = [];
        results.forEach((row, index) => {
            const last = out[out.length - 1];
            if (last && last.name === row.group) last.items.push({ ...row, index });
            else out.push({ name: row.group, items: [{ ...row, index }] });
        });
        return out;
    });

    /*
     * Typing re-runs the fan-out, debounced. The dependency is `trimmed` and
     * `open` — closing cancels a request that is about to become irrelevant,
     * and re-opening on the same term asks again rather than showing an answer
     * from before whatever the operator just did.
     */
    $effect(() => {
        const q = trimmed;
        const isOpen = open;
        clearTimeout(timer);
        if (!isOpen || q.length < 2) {
            reqId++;
            rows = [];
            busy = false;
            return;
        }
        busy = true;
        timer = setTimeout(() => search(q), 180);
        return () => clearTimeout(timer);
    });

    async function search(q) {
        const mine = ++reqId;
        const sources = SOURCES.filter((source) => can(source.right));

        const settled = await Promise.allSettled([
            ...sources.map((source) => source.run(q)),
            skuLookup(q),
        ]);
        if (mine !== reqId) return;

        const found = [];
        settled.forEach((outcome, i) => {
            // A source that failed contributes nothing and says nothing. One
            // module being down must not take the other five results off the
            // screen, and a toast per keystroke would be unusable.
            if (outcome.status !== "fulfilled") return;
            const source = sources[i] ?? { group: "SKU", icon: "ri-barcode-line" };
            for (const row of outcome.value ?? []) {
                found.push({ group: source.group, icon: source.icon, ...row });
            }
        });

        rows = found;
        busy = false;
        active = 0;
    }

    /**
     * The SKU door.
     *
     * `GET /api/products/sku/{sku}` resolves the catalog's stable key to its
     * product and had no entry point in the panel at all — a warehouse printout
     * or a supplier's order line was unusable here. It is an exact match, so it
     * is asked only for something that could be one, and a miss is a 404 that
     * means "no", not an error worth reporting.
     */
    async function skuLookup(q) {
        if (!can("catalog.read") || /\s/.test(q) || q.length < 2) return [];
        try {
            const res = await api.get(`/api/products/sku/${encodeURIComponent(q)}`);
            const product = res?.data ?? res;
            if (!product?.id) return [];
            return [
                {
                    group: "SKU",
                    icon: "ri-barcode-line",
                    title: product.title,
                    subtitle: `SKU ${q}`,
                    href: `/products/${product.id}`,
                },
            ];
        } catch {
            return [];
        }
    }

    function close() {
        open = false;
    }

    function choose(row) {
        if (!row) return;
        open = false;
        goto(base + row.href);
    }

    function onKeydown(event) {
        if (event.key === "ArrowDown") {
            event.preventDefault();
            active = results.length ? (active + 1) % results.length : 0;
        } else if (event.key === "ArrowUp") {
            event.preventDefault();
            active = results.length ? (active - 1 + results.length) % results.length : 0;
        } else if (event.key === "Enter") {
            event.preventDefault();
            choose(results[active]);
        } else if (event.key === "Home") {
            active = 0;
        } else if (event.key === "End") {
            active = Math.max(results.length - 1, 0);
        }
        // Escape belongs to dismissable, which asks isTopModal() first.
    }

    /* Opening is a fresh start: the previous hunt's term and its answers are
       almost never what the operator wants next, and leaving them there means
       the first keystroke lands in the middle of an old string. */
    $effect(() => {
        if (open) {
            term = "";
            rows = [];
            active = 0;
            box?.focus();
        }
    });

    /* Keeping the cursor in view. `block: "nearest"` rather than "center", so
       moving one row down does not jump the list under the pointer. */
    $effect(() => {
        const index = active;
        if (!open || !panel) return;
        panel.querySelector(`[data-index="${index}"]`)?.scrollIntoView({ block: "nearest" });
    });
</script>

<div
    class="modal popup palette"
    data-modal-state={open ? "open" : "closed"}
    inert={!open}
    role="dialog"
    aria-modal="true"
    aria-label="Search the store"
    tabindex="-1"
    use:portal
    use:trapFocus={open}
    use:dismissable={{ onclose: close, enabled: open }}
>
    <div class="modal-header palette-head">
        <i class="ri-search-line txt-hint" aria-hidden="true"></i>
        <input
            bind:this={box}
            bind:value={term}
            type="text"
            class="palette-input"
            placeholder="Search orders, products, customers, a SKU…"
            aria-label="Search the store"
            autocomplete="off"
            spellcheck="false"
            onkeydown={onKeydown}
        />
        {#if busy}
            <span class="loader sm" aria-label="Searching"></span>
        {/if}
        <button
            type="button"
            class="btn circle sm transparent secondary"
            aria-label="Close the search"
            onclick={close}
        >
            <i class="ri-close-line" aria-hidden="true"></i>
        </button>
    </div>

    <div class="modal-content palette-body" bind:this={panel}>
        <div class="list sm palette-list" id="palette-results">
            {#each groups as group (group.name)}
                <div class="palette-group">{group.name}</div>
                {#each group.items as row (row.href + row.title)}
                    <a
                        class="list-item handle"
                        class:active={row.index === active}
                        data-index={row.index}
                        aria-current={row.index === active ? "true" : undefined}
                        href="{base}{row.href}"
                        onclick={() => (open = false)}
                        onmouseenter={() => (active = row.index)}
                    >
                        <i class={row.icon} aria-hidden="true"></i>
                        <div class="content">
                            <span class="txt-ellipsis">{row.title}</span>
                            {#if row.subtitle}
                                <span class="txt-hint txt-sm txt-ellipsis">{row.subtitle}</span>
                            {/if}
                        </div>
                    </a>
                {/each}
            {/each}
        </div>

        {#if trimmed.length >= 2 && !busy && !rows.length}
            <p class="txt-hint txt-center m-t-sm m-b-0">
                Nothing in the store matches “{trimmed}”.
            </p>
        {:else if trimmed.length === 1}
            <p class="txt-hint txt-center m-t-sm m-b-0">Keep typing — two letters to search.</p>
        {/if}
    </div>

    <footer class="modal-footer palette-foot">
        <span class="txt-hint txt-sm">
            <kbd>↑</kbd><kbd>↓</kbd> to move · <kbd>Enter</kbd> to open · <kbd>Esc</kbd> to close
        </span>
    </footer>
</div>
