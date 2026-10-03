<script>
    /**
     * Catalogue: what the company may buy, at the buyer's prices, by category
     * and by search (GET /x/b2b/catalogue). A company held to a catalogue
     * (D75) sees only that; one with none sees everything on sale.
     *
     * A quantity goes straight into the basket from the row, through the same
     * route a quick order uses, so it is priced and checked the same way; a
     * line it refuses says why on the row it came from.
     *
     * The categories offered are the ones holding something the company may
     * buy (GET /x/b2b/catalogue/categories), never the store's whole tree: a
     * branch outside the catalogue could only ever filter to nothing.
     */
    import { onMount } from "svelte";
    import { base } from "$app/paths";
    import { formatMoney } from "$lib/format.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { query } from "$lib/api.js";
    import { trade, tradeApi, noteBasket, rememberBasket, explain, rejectedWords } from "$lib/trade.svelte.js";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import PortalPage from "../PortalPage.svelte";

    const list = listState({ q: "", category: 0, page: 1, limit: 24 });

    let products = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let failure = $state("");
    let categories = $state([]);
    let draft = $state(list.params.q);
    let reqId = 0;

    /* Per variant: the quantity typed, and what the last add said. */
    let qty = $state({});
    let adding = $state(0);
    let added = $state({});
    let refused = $state({});

    const company = $derived(trade.me.company);
    const ordering = $derived(company.status !== "closed");

    $effect(() => {
        list.params;
        load();
    });

    onMount(async () => {
        try {
            categories = (await tradeApi.get("/x/b2b/catalogue/categories")) ?? [];
        } catch (err) {
            // The list still works without its filter; the failure is shown
            // where the products are if they cannot be read either.
            categories = [];
        }
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        failure = "";
        try {
            const p = list.params;
            const r = await tradeApi.list(
                "/x/b2b/catalogue" +
                    query({ q: p.q, category_id: p.category || "", page: p.page, limit: p.limit }),
            );
            if (mine !== reqId) return;
            products = r.data;
            meta = r.meta;
        } catch (err) {
            if (mine === reqId && !err.handled) failure = explain(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    let typing;
    function onSearchInput(value) {
        clearTimeout(typing);
        typing = setTimeout(() => list.set({ q: value.trim() }), 300);
    }
    function search(event) {
        event.preventDefault();
        clearTimeout(typing);
        list.set({ q: draft.trim() });
    }

    const categoryOptions = $derived([
        { value: "", label: "Every category", short: "Every category" },
        ...categories.map((c) => ({ value: String(c.id), label: `${c.full_name} (${c.product_count})`, short: c.title })),
    ]);

    function variantName(product, v) {
        if (v.label) return v.label;
        if (v.options?.length) return v.options.join(" / ");
        return product.variants.length === 1 ? "" : v.sku;
    }

    function stockWords(v) {
        if (!v.in_stock) return { text: "Out of stock", tone: "is-out" };
        if (v.available > 0) return { text: `${v.available} in stock`, tone: v.available < 10 ? "is-low" : "" };
        return { text: "Available", tone: "" };
    }

    async function add(v) {
        const n = Number(qty[v.id] ?? 1);
        if (!Number.isInteger(n) || n < 1) {
            refused = { ...refused, [v.id]: "The quantity needs to be a whole number, 1 or more." };
            return;
        }
        adding = v.id;
        refused = { ...refused, [v.id]: "" };
        const lines = [{ variant_id: v.id, quantity: n }];
        try {
            let fill;
            try {
                fill = await tradeApi.post("/x/b2b/cart/lines", trade.basket.token ? { cart_id: trade.basket.token, lines } : { lines });
            } catch (err) {
                if (!trade.basket.token || err.status !== 409) throw err;
                rememberBasket("");
                fill = await tradeApi.post("/x/b2b/cart/lines", { lines });
            }
            noteBasket(fill.cart);
            const no = fill.rejected?.[0];
            if (no) {
                refused = { ...refused, [v.id]: rejectedWords(no) };
            } else {
                added = { ...added, [v.id]: (added[v.id] ?? 0) + 1 };
                qty = { ...qty, [v.id]: 1 };
            }
        } catch (err) {
            if (!err.handled) refused = { ...refused, [v.id]: explain(err) };
        } finally {
            adding = 0;
        }
    }
</script>

<PortalPage title="Catalogue">
    {#snippet actions()}
        <a href="{base}/portal/basket" class="btn secondary portal-press">
            <i class="ri-shopping-basket-2-line" aria-hidden="true"></i>
            <span class="txt">Your basket{trade.basket.count ? ` (${trade.basket.count})` : ""}</span>
        </a>
    {/snippet}

    <p class="field-help m-b-base portal-intro">
        {#if company.catalogue_id}
            What {company.name} buys from {trade.store.name || "the store"}, at your prices.
        {:else}
            Everything {trade.store.name || "the store"} sells, at {company.name}'s prices.
        {/if}
        Add a quantity straight to your basket; nothing is ordered until you check out.
    </p>

    <div class="b2b-section-head portal-filters portal-catalogue-filters">
        <form class="field portal-search" onsubmit={search} role="search">
            <input
                type="search"
                placeholder="Name or product code"
                aria-label="Search the catalogue by name or product code"
                autocomplete="off"
                bind:value={draft}
                oninput={(e) => onSearchInput(e.currentTarget.value)}
            />
        </form>
        {#if categories.length}
            <div class="field b2b-filter portal-category-pick">
                <Select
                    ariaLabel="Category"
                    value={list.params.category ? String(list.params.category) : ""}
                    options={categoryOptions}
                    onchange={(v) => list.set({ category: v ? Number(v) : 0 })}
                />
            </div>
        {/if}
    </div>

    {#if failure}
        <div class="alert danger m-b-base portal-notice" role="alert">
            <p>{failure}</p>
            <button type="button" class="btn sm secondary portal-press" onclick={load}><span class="txt">Try again</span></button>
        </div>
    {/if}

    {#if loading && !products.length}
        <div class="portal-products">
            {#each Array(6) as _, i (i)}
                <div class="portal-product tw:rounded-xl tw:border tw:bg-card"><span class="skeleton-loader"></span><span class="skeleton-loader"></span></div>
            {/each}
        </div>
    {:else if !products.length && !failure}
        <div class="portal-empty-state tw:rounded-xl tw:border tw:bg-card portal-notice">
            <i class="ri-store-3-line" aria-hidden="true"></i>
            {#if list.params.q || list.params.category}
                <h2>Nothing matches</h2>
                <p class="txt-hint">
                    {list.params.q ? `No product called or coded “${list.params.q}”` : "Nothing"}{list.params.category ? " in this category" : ""}
                    is in {company.catalogue_id ? `${company.name}'s catalogue` : "the catalogue"}.
                </p>
                <button type="button" class="btn secondary portal-press" onclick={() => { draft = ""; list.clear(); }}>
                    <span class="txt">Show everything</span>
                </button>
            {:else}
                <h2>The catalogue is empty</h2>
                <p class="txt-hint">There's nothing for {company.name} to buy yet. {trade.store.name || "The store"} can add to it.</p>
            {/if}
        </div>
    {:else}
        <div class="portal-products" class:faded={loading}>
            {#each products as p (p.id)}
                <article class="portal-product tw:rounded-xl tw:border tw:bg-card" aria-labelledby="product-{p.id}">
                    <header class="portal-product-head">
                        <span class="portal-product-image" aria-hidden="true">
                            {#if p.image_url}
                                <img src={p.image_url} alt="" loading="lazy" />
                            {:else}
                                <i class="ri-image-line"></i>
                            {/if}
                        </span>
                        <span class="portal-product-name">
                            <span class="txt-bold" id="product-{p.id}">{p.title}</span>
                            {#if p.category}<span class="txt-hint txt-sm">{p.category.full_name || p.category.title}</span>{/if}
                        </span>
                    </header>
                    {#if !p.variants.length}
                        <p class="txt-hint txt-sm m-0">None of its versions can be ordered at the moment.</p>
                    {/if}
                    <ul class="portal-variants">
                        {#each p.variants as v (v.id)}
                            {@const stock = stockWords(v)}
                            <li class="portal-variant">
                                <span class="portal-variant-what">
                                    {#if variantName(p, v)}<span>{variantName(p, v)}</span>{/if}
                                    <span class="txt-hint txt-sm"><span class="txt-code">{v.sku}</span> · <span class="portal-stock {stock.tone}">{stock.text}</span></span>
                                </span>
                                <span class="portal-variant-price txt-bold">{formatMoney({ amount_minor: v.price_minor, currency: v.currency })}</span>
                                {#if ordering}
                                    <span class="portal-variant-add">
                                        <span class="field portal-variant-qty">
                                            <input
                                                type="number"
                                                min="1"
                                                step="1"
                                                inputmode="numeric"
                                                aria-label="Quantity of {p.title}{variantName(p, v) ? `, ${variantName(p, v)}` : ''}"
                                                value={qty[v.id] ?? 1}
                                                oninput={(e) => (qty = { ...qty, [v.id]: e.currentTarget.value })}
                                                onkeydown={(e) => {
                                                    if (e.key === "Enter") {
                                                        e.preventDefault();
                                                        add(v);
                                                    }
                                                }}
                                            />
                                        </span>
                                        <button
                                            type="button"
                                            class="btn sm portal-press portal-add-btn"
                                            class:loading={adding === v.id}
                                            disabled={!!adding || !v.in_stock}
                                            aria-label="Add {p.title}{variantName(p, v) ? `, ${variantName(p, v)}` : ''} to the basket"
                                            onclick={() => add(v)}
                                        >
                                            {#key added[v.id]}
                                                <i class={added[v.id] ? "ri-check-line portal-added" : "ri-add-line"} aria-hidden="true"></i>
                                            {/key}
                                            <span class="txt">{added[v.id] ? "Added" : "Add"}</span>
                                        </button>
                                    </span>
                                {/if}
                                {#if refused[v.id]}
                                    <span class="portal-variant-refused txt-danger txt-sm portal-refusal" role="alert">{refused[v.id]}</span>
                                {/if}
                            </li>
                        {/each}
                    </ul>
                </article>
            {/each}
        </div>
    {/if}

    <footer class="page-footer tw:text-xs tw:text-muted-foreground">
        <Pager {meta} {loading} noun="product" perPage={list.params.limit} sizes={[24, 48, 96]} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
        <div class="flex-fill"></div>
    </footer>
</PortalPage>
