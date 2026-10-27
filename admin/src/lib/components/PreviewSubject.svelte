<script>
    /**
     * Which of the store's products, or collections, the theme editor shows
     * a template on (D129). A product page is one template for every
     * product, so the preview needs a real one to draw it around; the shop
     * picks which, and this browser remembers the pick for the store.
     *
     * Products are the hundred newest active ones and collections the first
     * two hundred, searched in the menu: enough to find a long title and a
     * short one, a product with variants and one without. The routes are the
     * catalogue's own, under catalog.read, which every role keeps.
     */
    import { api, query } from "$lib/api.js";
    import { getStore } from "$lib/store-scope.js";
    import Select from "$lib/components/Select.svelte";

    let { kind = "product", subject = $bindable(null), status = $bindable("loading") } = $props();

    let options = $state([]);
    let items = [];
    let value = $state("");

    const key = () => `gocommerce.preview-${kind}:` + (getStore() || "default");

    function remembered() {
        try {
            return localStorage.getItem(key()) || "";
        } catch {
            return "";
        }
    }

    function choose(id) {
        const item = items.find((x) => String(x.id) === String(id));
        if (!item) return;
        value = String(item.id);
        subject = { id: item.id, slug: item.slug, title: item.title };
        try {
            localStorage.setItem(key(), value);
        } catch {
            // A private window keeps nothing; the pick still serves now.
        }
    }

    async function load() {
        status = "loading";
        try {
            const result =
                kind === "product"
                    ? await api.get("/api/admin/products" + query({ status: "active", limit: 100 }))
                    : await api.get("/api/admin/collections" + query({ limit: 200 }));
            items = (result?.data ?? []).filter((x) => x?.slug);
            options = items.map((x) => ({ value: String(x.id), label: x.title }));
            if (!items.length) {
                status = "empty";
                subject = null;
                return;
            }
            const want = remembered();
            choose(items.some((x) => String(x.id) === want) ? want : items[0].id);
            status = "ready";
        } catch {
            // The preview is a help, not the page: without the list it says
            // there is nothing to show, and the editing goes on.
            status = "empty";
        }
    }

    $effect(() => {
        kind;
        load();
    });
</script>

{#if status === "ready"}
    <Select
        class="ps-select"
        {options}
        ariaLabel={kind === "product" ? "The product to preview the page on" : "The collection to preview the page on"}
        bind:value={() => value, (v) => choose(v)}
    />
{:else if status === "loading"}
    <span class="ps-loading txt-hint txt-sm" aria-live="polite">Finding a {kind}…</span>
{/if}

<style>
    :global(.ps-select) {
        min-width: 0;
        max-width: min(280px, 100%);
        flex: 1 1 160px;
    }
    .ps-loading {
        animation: ps-pulse 1s ease-in-out infinite alternate;
    }
    @keyframes ps-pulse {
        from {
            opacity: 0.5;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .ps-loading {
            animation: none;
        }
    }
</style>
