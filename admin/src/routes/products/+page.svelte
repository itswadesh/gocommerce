<script>
    /**
     * The product list.
     *
     * Editing moved to `/products/{id}` — a product now has media, an option
     * matrix, per-variant stock and an SEO listing, and a drawer that has to
     * hold all of that beside the row it belongs to stops being a drawer.
     *
     * Creating stayed here, and stayed small. A product needs at least one
     * variant to exist at all, so the create form asks for exactly what the
     * engine requires and nothing else, then hands over to the editor — which
     * is where the interesting fields are and where the operator was going
     * anyway.
     */
    import { untrack } from "svelte";
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { goto } from "$app/navigation";
    import { api, can, query, request } from "$lib/api.js";
    import { downloadFile } from "$lib/download.js";
    import { rowKey } from "$lib/rowkey.js";
    import { selection } from "$lib/selection.svelte.js";
    import { runBulk } from "$lib/bulk.js";
    import { onNewShortcut } from "$lib/shortcuts.js";
    import { distinct } from "$lib/catalog.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { readSort, cycleSort, sortQuery } from "$lib/listsort.js";
    import {
        formatMoney,
        formatDate,
        relativeTime,
        toMinor,
        isValidMoney,
        stockClass,
        pluralize,
    } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { hasModule } from "$lib/modules.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import BulkBar from "$lib/components/BulkBar.svelte";
    import CategoryPicker from "$lib/components/CategoryPicker.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import ProductFilters from "$lib/components/ProductFilters.svelte";
    import Select from "$lib/components/Select.svelte";
    import SortHeader from "$lib/components/SortHeader.svelte";

    /* The starting page size, not the only one: it is a listState key below, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 30;

    /* Search, status, the five segmenting filters, ordering and page all live
       in the URL, and the window replaces the rows rather than accumulating
       them — a sorted list an operator can send to somebody is the whole point,
       and page 4 of a new ordering appended onto three pages of the old one is
       two orderings interleaved in one table.

       Every filter the engine accepts has to be *declared* here: query() walks
       these keys and nothing else, so a parameter missing from this object is
       one the request can never carry, however it got onto the URL. */
    const list = listState({
        q: "",
        status: "",
        vendor: "",
        product_type: "",
        tag: "",
        category_id: 0,
        collection_id: 0,
        // The category-attribute facets, as JSON in one parameter.
        //
        // The API takes them as a repeated `attr=handle:value`, which listState
        // cannot hold — its params are one scalar per key, and URLSearchParams
        // .set() cannot repeat one anyway. JSON round-trips the shape exactly,
        // keeps the URL openable and Back-survivable (D30), and is turned back
        // into the repeated form in load() where the request is built.
        attrs: "",
        sort: "",
        order: "",
        page: 1,
        limit: PER_PAGE,
    });
    const SORT_FIELDS = ["title", "status", "price", "available", "created_at", "updated_at"];

    let loading = $state(true);
    let saving = $state(false);
    let products = $state([]);
    let meta = $state(null);

    const search = $derived(list.params.q);
    const status = $derived(list.params.status);
    const sort = $derived(readSort(list.params, SORT_FIELDS));
    const perPage = $derived(list.params.limit);
    let draftSearch = $state(list.params.q);

    /* catalog.read is what the nav gates this screen on; somebody who typed the
       address without it got a fully armed screen and a 403 on load, which
       reads as a broken panel rather than as a permission. */
    const readable = $derived(can("catalog.read"));
    const writable = $derived(can("catalog.write"));

    /* The five segmenting filters, as one value: the drawer takes them
       together and applies them together, so the page never holds four of the
       five and a stale one. */
    const segment = $derived({
        vendor: list.params.vendor,
        product_type: list.params.product_type,
        tag: list.params.tag,
        category_id: list.params.category_id,
        collection_id: list.params.collection_id,
        attrs: parseAttrs(list.params.attrs),
    });
    const filtered = $derived(
        !!(
            segment.vendor ||
            segment.product_type ||
            segment.tag ||
            segment.category_id ||
            segment.collection_id ||
            Object.keys(segment.attrs).length
        ),
    );

    /* A hand-edited URL is the ordinary way this gets malformed, and a filter
       that throws takes the whole screen rather than one facet. */
    function parseAttrs(raw) {
        if (!raw) return {};
        try {
            const parsed = JSON.parse(raw);
            return parsed && typeof parsed === "object" && !Array.isArray(parsed) ? parsed : {};
        } catch {
            return {};
        }
    }

    let filterOpen = $state(false);

    /**
     * What the filter drawer offers to choose from.
     *
     * Three of the five are free-text columns on `products` rather than tables,
     * so the catalog is its own index — the same page-of-200 scan the product
     * editor uses to suggest a vendor. The other two are real listings.
     *
     * Fetched once, and only when they are about to be needed: opening the
     * Products screen is the most common navigation in the panel, and a second
     * 200-row read on every visit for a drawer most visits never open is a
     * cost nobody asked for. A filter already on the URL counts as needing
     * them — the chips have to be able to name a category rather than an id.
     */
    let vendors = $state([]);
    let productTypes = $state([]);
    let tagPool = $state([]);
    let categories = $state([]);
    let categoriesTruncated = $state(false);
    let collections = $state([]);
    let vocabularyState = $state("idle");

    async function loadVocabulary() {
        if (vocabularyState !== "idle") return;
        vocabularyState = "loading";
        try {
            const [catalog, cats, cols] = await Promise.all([
                api.get("/api/admin/products" + query({ limit: 200 })),
                api.get("/api/admin/categories?flat=1"),
                api.get("/api/admin/collections" + query({ limit: 200 })),
            ]);
            const rows = catalog.data ?? [];
            vendors = distinct(rows, (p) => [p.vendor]);
            productTypes = distinct(rows, (p) => [p.product_type]);
            tagPool = distinct(rows, (p) => p.tags ?? []);
            categories = cats.data ?? [];
            categoriesTruncated = (cats.meta?.total ?? 0) > categories.length;
            collections = cols.data ?? [];
            vocabularyState = "ready";
        } catch {
            // The filters are still usable — the drawer's comboboxes take a
            // typed name, and the engine matches on that alone. Only the
            // suggestions are missing, so this is not worth a toast; going back
            // to "idle" so the next open tries again is.
            vocabularyState = "idle";
        }
    }

    /*
     * Untracked, because loadVocabulary writes the very state it reads to
     * decide whether to run. Left tracked, a failing vocabulary read would set
     * it back to "idle", wake this effect, and try again for ever.
     */
    $effect(() => {
        if (filtered) untrack(() => loadVocabulary());
    });

    function openFilters() {
        loadVocabulary();
        filterOpen = true;
    }

    function applyFilters(next) {
        filterOpen = false;
        const { attrs, labels, ...rest } = next;
        if (labels) attributeLabels = { ...attributeLabels, ...labels };
        list.set({
            ...rest,
            // Empty rather than "{}" so the parameter leaves the URL entirely
            // when nothing is chosen; "{}" would read as a live filter.
            attrs: attrs && Object.keys(attrs).length ? JSON.stringify(attrs) : "",
        });
    }

    /**
     * One chip per active filter, so what the list is showing is legible
     * without opening the drawer that set it — and removable one at a time,
     * which is the ordinary way out of a filter that turned out to be one too
     * many.
     *
     * A category or collection whose name has not arrived yet is named by its
     * id rather than left blank: the chip has to be honest about being there.
     */
    const chips = $derived.by(() => {
        const out = [];
        if (segment.vendor) out.push({ key: "vendor", label: "Vendor", text: segment.vendor });
        if (segment.product_type) {
            out.push({ key: "product_type", label: "Type", text: segment.product_type });
        }
        if (segment.tag) out.push({ key: "tag", label: "Tag", text: segment.tag });
        if (segment.category_id) {
            const found = categories.find((c) => c.id === segment.category_id);
            out.push({
                key: "category_id",
                label: "Category",
                text: found?.full_name || found?.title || `#${segment.category_id}`,
            });
        }
        if (segment.collection_id) {
            const found = collections.find((c) => c.id === segment.collection_id);
            out.push({
                key: "collection_id",
                label: "Collection",
                text: found?.title || `#${segment.collection_id}`,
            });
        }
        // One chip per attribute rather than per value: the values of one
        // attribute are a single question — canvas or leather — and splitting
        // them would offer to remove half of it, which means nothing.
        for (const [key, values] of Object.entries(segment.attrs)) {
            if (!values?.length) continue;
            out.push({
                key: "attr:" + key,
                label: attributeLabels[key] ?? key,
                text: values.join(" or "),
            });
        }
        return out;
    });

    /* The human names for the handles on screen. Filled from whatever the
       filter drawer last looked up, and falling back to the handle: a chip
       reading "bag-case-material" is still true, just less kind. */
    let attributeLabels = $state({});

    // The default for each key, which is what removing a chip restores. Numbers
    // and strings both, so it is read off the chip rather than guessed.
    const CLEARED = {
        vendor: "",
        product_type: "",
        tag: "",
        category_id: 0,
        collection_id: 0,
        attrs: "",
    };

    function clearFilters() {
        list.set({ ...CLEARED });
    }

    /* An attribute chip is not a listState key — it is one entry inside the
       `attrs` object — so removing it rewrites that object rather than
       resetting a parameter. Without this branch the close button on a facet
       chip set an unknown key and appeared to do nothing. */
    function removeChip(chip) {
        if (!chip.key.startsWith("attr:")) {
            list.set({ [chip.key]: CLEARED[chip.key] });
            return;
        }
        const next = { ...segment.attrs };
        delete next[chip.key.slice(5)];
        list.set({ attrs: Object.keys(next).length ? JSON.stringify(next) : "" });
    }

    /* A fast second header click leaves two requests in flight; without this
       the table settles on the reply that lost rather than on the header that
       is lit. */
    let reqId = 0;

    // The store answers this once, at sign-in; before it does, the getter
    // returns the same USD this screen used to assume outright.
    const currency = $derived(settings.currency);

    let createOpen = $state(false);

    /* Import from Amazon. The drawer belongs to the import-amazon module and
       shows only in a binary that serves it — hasModule() is the probe. The
       job runs on the server, and this polls it, so a listing with thirty
       variations shows where it has got to rather than a spinner for a
       minute; and when Amazon asks for a person, the job says so and waits
       for the operator to click through in the Chrome window. */
    let importOpen = $state(false);
    let importing = $state(false);
    let importJob = $state(null);
    let importTimer = null;
    let importForm = $state({
        url: "",
        brighten: true,
        background: true,
        tilt: true,
        mirror: true,
        max_variants: 30,
        status: "draft",
        price: "",
    });
    const importBusy = $derived(
        !!importJob && (importJob.status === "queued" || importJob.status === "running"),
    );
    let form = $state(blankForm());
    let errors = $state({});

    let confirmOpen = $state(false);
    let pendingDelete = $state(null);

    function blankForm() {
        return { title: "", slug: "", status: "draft", sku: "", price: "", stock: 0 };
    }

    /*
     * The SKU the engine would derive, shown before it does.
     *
     * The engine names an unnamed variant after its product — the same rule the
     * option matrix uses — so leaving the box empty is now a real choice rather
     * than a refused save. But a code that appears only after saving is a code
     * nobody checked, and a SKU is the one field a warehouse reads aloud. So
     * the suggestion is in the box, where it can be read and typed over.
     *
     * It mirrors the slug field's contract: it follows the title until somebody
     * writes in it, and then it is theirs.
     */
    let skuEdited = $state(false);
    const suggestedSKU = $derived.by(() => {
        const source = form.slug.trim() || form.title.trim();
        if (!source) return "";
        const slug = source
            .toLowerCase()
            .replace(/[^a-z0-9]+/g, "-")
            .replace(/^-+|-+$/g, "");
        return slug.toUpperCase();
    });
    $effect(() => {
        if (!skuEdited) form.sku = suggestedSKU;
    });

    $effect(() => {
        // Re-runs whenever any parameter changes: the search, the status, the
        // ordering, the page.
        list.params;
        load();
    });

    async function load() {
        // The screen is refused above; asking anyway would put a 403 toast
        // over the explanation.
        if (!readable) {
            loading = false;
            return;
        }
        const mine = ++reqId;
        loading = true;
        try {
            // `attrs` is this screen's shape, not the API's: it goes out as the
            // repeated `attr=handle:value` the engine reads, and is suppressed
            // from the generic builder, which could not repeat a key anyway.
            const facets = new URLSearchParams();
            for (const [key, values] of Object.entries(segment.attrs)) {
                for (const value of values ?? []) facets.append("attr", key + ":" + value);
            }
            const base = list.query({ limit: perPage, attrs: "", ...sortQuery(sort) });
            const tail = facets.toString();
            const result = await api.get(
                "/api/admin/products" + base + (tail ? (base ? "&" : "?") + tail : ""),
            );
            if (mine !== reqId) return;
            products = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    /**
     * The CSV, cut to exactly what the screen is showing.
     *
     * The export route is given the same query the listing is, because both
     * sides read it through one parser (`productQueryFrom`): search, status,
     * vendor, product type, tag, category, collection and the attribute
     * facets. So unlike the orders export there is nothing this button cannot
     * carry, and the file matches the table rather than the whole catalogue.
     */
    function exportCSV() {
        const facets = new URLSearchParams();
        for (const [key, values] of Object.entries(segment.attrs)) {
            for (const value of values ?? []) facets.append("attr", key + ":" + value);
        }
        // Everything but the pagination and the sort: a file is not a page,
        // and the columns are the file's own.
        const base = list.query({ attrs: "", page: "", limit: "", sort: "", order: "" });
        const tail = facets.toString();
        downloadFile(
            "/api/admin/export/admin-products" + base + (tail ? (base ? "&" : "?") + tail : ""),
            "products.csv",
            "text/csv",
        );
    }

    function sortBy(field, firstDesc) {
        list.set(cycleSort(sort, field, firstDesc));
    }

    function submitSearch(e) {
        e.preventDefault();
        list.set({ q: draftSearch });
    }

    function clearSearch() {
        draftSearch = "";
        list.set({ q: "" });
    }

    function openImport() {
        importJob = null;
        importOpen = true;
    }

    function closeImport() {
        importOpen = false;
        if (importTimer) {
            clearTimeout(importTimer);
            importTimer = null;
        }
    }

    async function startImport(event) {
        event.preventDefault();
        if (!importForm.url.trim()) {
            toast.error("Paste the listing's URL.");
            return;
        }
        const body = {
            url: importForm.url.trim(),
            images: {
                brightness: importForm.brighten ? 0.03 : 0,
                contrast: importForm.brighten ? 1.04 : 1,
                background: importForm.background ? "gradient" : "keep",
                scale: importForm.background || importForm.tilt ? 0.92 : 1,
                tilt: importForm.tilt ? 3 : 0,
                shadow: importForm.background,
                flip: importForm.mirror ? "horizontal" : "none",
                rotate: 0,
            },
            max_variants: Number(importForm.max_variants) || 30,
            status: importForm.status,
        };
        if (String(importForm.price).trim() !== "") {
            const minor = toMinor(importForm.price, currency);
            if (minor === null || minor < 0) {
                toast.error("That price is not a number.");
                return;
            }
            body.price_minor = minor;
        }
        importing = true;
        try {
            const result = await api.post("/api/admin/x/import-amazon/jobs", body);
            importJob = result;
            importTimer = setTimeout(pollImport, 1200);
        } catch (err) {
            toast.error(err);
        } finally {
            importing = false;
        }
    }

    async function pollImport() {
        importTimer = null;
        if (!importJob || !importOpen) return;
        try {
            const result = await api.get(`/api/admin/x/import-amazon/jobs/${importJob.id}`);
            importJob = result;
        } catch (err) {
            toast.error(err);
            return;
        }
        if (importJob.status === "queued" || importJob.status === "running") {
            importTimer = setTimeout(pollImport, 1500);
            return;
        }
        if (importJob.status === "done") {
            toast.success("Imported.");
            await load();
        }
    }

    function openCreate() {
        form = blankForm();
        skuEdited = false;
        errors = {};
        createOpen = true;
    }

    /* /products?new=1 — the Home screen's Add product — lands with the
       drawer already open. Once: the parameter is a request, not a state. */
    let openedFromURL = false;
    $effect(() => {
        if (openedFromURL || !writable) return;
        if (page.url.searchParams.get("new") === "1") {
            openedFromURL = true;
            openCreate();
        }
    });

    /**
     * A row click opens the product. The title is also a real link, which is
     * what makes middle-click and "open in a new tab" work — three products
     * open side by side is how a range gets compared.
     *
     * The handler stands aside when the click was on the anchor, or the browser
     * would navigate and then this would navigate again over the top of it.
     */
    function openEdit(product, event) {
        if (event?.target?.closest?.("a")) return;
        goto(`${base}/products/${product.id}`);
    }

    async function create(event) {
        event?.preventDefault();
        if (saving) return;

        errors = {};
        if (!form.title.trim()) errors.title = "A title is required.";

        // isValidMoney rather than isNaN(parseFloat(...)): parseFloat reads
        // "24,99" as 24, and this guard is what keeps toMinor from having to
        // answer null on the way to the wire.
        if (!isValidMoney(form.price)) {
            errors.price = "A price is required.";
        }
        if (Object.keys(errors).length) return;

        saving = true;
        try {
            const created = await api.post("/api/admin/products", {
                title: form.title.trim(),
                slug: form.slug.trim() || undefined,
                status: form.status,
                // Omitted rather than sent empty, so the engine derives one
                // and resolves any collision as it does for the matrix.
                sku: form.sku.trim() || undefined,
                price_minor: toMinor(form.price, currency),
                stock: parseInt(form.stock, 10) || 0,
            });
            createOpen = false;
            toast.success("Product created");
            // Straight into the editor: everything else a product has — media,
            // options, SEO — lives there, and nobody creates one meaning to
            // stop at a title and a price.
            await goto(`${base}/products/${created.id}`);
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function askDelete(product, event) {
        event.stopPropagation();
        pendingDelete = product;
        confirmOpen = true;
    }

    async function doDelete() {
        try {
            await api.delete(`/api/admin/products/${pendingDelete.id}`);
            toast.success(`Deleted ${pendingDelete.title}`);
            await load();
        } catch (err) {
            toast.error(err);
        }
    }

    // ------------------------------------------------------------ selection

    /*
     * The rows an operator has picked. Retiring a season used to be one page
     * load per product; the row had no archive action on it at all, so it was
     * open the editor, change the select, save, go back, and again.
     *
     * The selection is keyed by id and survives a page change, so twelve
     * products found across two pages can be archived together. It is cleared
     * by a FILTER change, because the rows that were picked are not on screen
     * any more and a bulk action on rows nobody can see is how a hundred
     * products get archived by accident. `page` is deliberately not in the
     * list below.
     */
    const sel = selection();

    $effect(() => {
        list.params.q;
        list.params.status;
        list.params.vendor;
        list.params.product_type;
        list.params.tag;
        list.params.category_id;
        list.params.collection_id;
        list.params.attrs;
        sel.clear();
    });

    let bulkBusy = $state(false);
    let bulkDeleteOpen = $state(false);

    /*
     * The collection list the bulk bar files into is the filter drawer's, asked
     * for once and shared. Ticking a row is what asks for it, so a visit that
     * never selects anything never pays for the 200-row read — the same bargain
     * the filter drawer makes, for the same reason.
     *
     * Untracked, because loadVocabulary writes the very state it reads to decide
     * whether to run.
     */
    $effect(() => {
        if (sel.count > 0) untrack(() => loadVocabulary());
    });

    const picked = $derived(sel.pick(products));

    /**
     * Every bulk action is N calls to the per-row route a single row already
     * uses — there is no batch endpoint and this does not invent one — and each
     * reports its own failure rather than the run dying on the first.
     */
    async function bulkStatus(status) {
        // Filtered before the run, as bulk.js asks: telling an operator that
        // four products "failed" to become draft when they already were is
        // noise they could have been spared.
        const rows = picked.filter((p) => p.status !== status);
        if (!rows.length) {
            toast.info(`Already ${status}`);
            return;
        }
        bulkBusy = true;
        try {
            await runBulk(rows, (p) => api.patch(`/api/admin/products/${p.id}`, { status }), {
                describe: `Set to ${status}`,
                noun: "product",
                label: (p) => p.title,
            });
        } finally {
            bulkBusy = false;
        }
        sel.clear();
        await load();
    }

    /**
     * Adding to a collection, not replacing what a product is already in.
     *
     * `PUT /api/admin/products/{id}/collections` replaces the whole set, so the
     * ids on the row are read back and the new one appended. The row carries
     * them, so this costs no extra request.
     */
    async function bulkCollect(collectionId) {
        const id = Number(collectionId);
        if (!id) return;
        const rows = picked.filter((p) => !(p.collections ?? []).some((c) => c.id === id));
        if (!rows.length) {
            toast.info("Every selected product is already in it");
            return;
        }
        const name = collections.find((c) => c.id === id)?.title || "the collection";
        bulkBusy = true;
        try {
            await runBulk(
                rows,
                (p) =>
                    request("PUT", `/api/admin/products/${p.id}/collections`, {
                        body: {
                            collection_ids: [...(p.collections ?? []).map((c) => c.id), id],
                        },
                    }),
                { describe: `Added to ${name}`, noun: "product", label: (p) => p.title },
            );
        } finally {
            bulkBusy = false;
        }
        sel.clear();
        await load();
    }

    /**
     * Filing a selection under one category.
     *
     * This is the action a refused category delete sends an operator to
     * perform: the engine will not delete a category that still holds
     * products, and says how many, and the only way out was to open each
     * product and change its one field. A category is singular by definition —
     * one product, one answer — so unlike the collection action above this
     * REPLACES rather than appends, and PATCH takes the whole answer.
     *
     * The picker is the tree's own, which is what makes it work on an imported
     * taxonomy: fourteen thousand nodes is not a `<select>`.
     */
    let categoriseOpen = $state(false);
    let bulkCategoryID = $state(null);

    /* The selection's own answer when they all agree, so the drawer opens
       showing where these products currently are rather than at "Uncategorised"
       — which would read as a proposal to unfile them. */
    function openCategorise() {
        loadVocabulary();
        const ids = new Set(picked.map((p) => p.category?.id ?? null));
        bulkCategoryID = ids.size === 1 ? [...ids][0] : null;
        categoriseOpen = true;
    }

    /* How many of the selection the move would actually touch — the sentence
       the footer button needs, since a selection already half-filed there is
       the normal case after a filtered search. */
    const wouldRefile = $derived(
        picked.filter((p) => (p.category?.id ?? null) !== bulkCategoryID).length,
    );

    async function bulkCategorise() {
        const id = bulkCategoryID ?? null;
        const rows = picked.filter((p) => (p.category?.id ?? null) !== id);
        categoriseOpen = false;
        if (!rows.length) {
            toast.info(
                id === null
                    ? "None of them is filed anywhere"
                    : "Every selected product is already there",
            );
            return;
        }
        bulkBusy = true;
        try {
            const name = await categoryName(id);
            await runBulk(rows, (p) => api.patch(`/api/admin/products/${p.id}`, { category_id: id }), {
                describe: `Filed under ${name}`,
                noun: "product",
                label: (p) => p.title,
            });
        } finally {
            bulkBusy = false;
        }
        sel.clear();
        await load();
    }

    /**
     * What to call the category in the report.
     *
     * The tree in hand answers it for free, and on a taxonomy too large to ship
     * it does not — `categories` there is a truncated slice. One GET is the
     * honest way to name a node the panel was never sent, and naming it by id
     * would make the one sentence saying what just happened unreadable.
     */
    async function categoryName(id) {
        if (id === null) return "Uncategorised";
        const known = categories.find((c) => c.id === id);
        if (known) return known.full_name || known.title;
        try {
            const node = await api.get(`/api/admin/categories/${id}`);
            return node.full_name || node.title;
        } catch {
            return `category #${id}`;
        }
    }

    async function bulkDelete() {
        const rows = picked;
        bulkBusy = true;
        try {
            await runBulk(rows, (p) => api.delete(`/api/admin/products/${p.id}`), {
                describe: "Deleted",
                noun: "product",
                label: (p) => p.title,
            });
        } finally {
            bulkBusy = false;
        }
        sel.clear();
        await load();
    }

    function priceRange(product) {
        const prices = (product.variants || []).map((v) => v.price.amount_minor);
        if (!prices.length) return "—";
        const min = Math.min(...prices);
        const max = Math.max(...prices);
        const cur = product.variants[0].price.currency;
        if (min === max) return formatMoney({ amount_minor: min, currency: cur });
        return (
            formatMoney({ amount_minor: min, currency: cur }) +
            " – " +
            formatMoney({ amount_minor: max, currency: cur })
        );
    }

    function totalAvailable(product) {
        const tracked = (product.variants || []).filter((v) => v.track_inventory);
        if (!tracked.length) return null;
        return tracked.reduce((sum, v) => sum + v.available, 0);
    }

    /* `n` creates one, from anywhere on this screen. The shell owns the
       keystroke and fires an event; what "new" means is the screen's. */
    $effect(() => {
        if (!writable) return;
        return onNewShortcut(openCreate);
    });

    // Tones, not verdicts: a draft is a state the operator chose, not a warning
    // about one. Green for on sale, blue for not yet, grey for retired.
    const statusLabel = { active: "success", draft: "info", archived: "" };
</script>

<svelte:head><title>Products · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="catalog.read" what="the product list" />
{:else}
<div class="page page-products shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Products</div>
            </nav>

            <div class="inline-flex gap-sm">
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    title="Refresh"
                    aria-label="Refresh"
                    onclick={load}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>

            <form class="fields searchbar" onsubmit={submitSearch}>
                <div class="field">
                    <input
                        type="text"
                        class="p-l-20"
                        placeholder="Search products by title or slug"
                        bind:value={draftSearch}
                    />
                </div>
                {#if draftSearch || search}
                    <div class="field addon p-r-5">
                        {#if draftSearch !== search}
                            <button type="submit" class="btn sm pill warning">Search</button>
                        {/if}
                        <button
                            type="button"
                            class="btn sm pill secondary transparent"
                            onclick={clearSearch}
                        >
                            Clear
                        </button>
                    </div>
                {/if}
            </form>

            <div class="page-header-primary-btns">
                <div class="field">
                    <Select
                        id="status-filter"
                        placeholder="Any status"
                        value={status}
                        onchange={(v) => list.set({ status: v })}
                        options={[
                            { value: "", label: "Any status" },
                            { value: "active", label: "Active" },
                            { value: "draft", label: "Draft" },
                            { value: "archived", label: "Archived" },
                        ]}
                    />
                </div>

                <!-- The five segmenting filters are behind one button: they do
                     not fit in this row, and the header collapses its buttons
                     to bare circles at 550px, which five selects cannot do. -->
                <button
                    type="button"
                    class="btn secondary"
                    aria-expanded={filterOpen}
                    onclick={openFilters}
                >
                    <i class="ri-filter-3-line" aria-hidden="true"></i>
                    <span class="txt">
                        Filter{chips.length ? ` (${chips.length})` : ""}
                    </span>
                </button>

                {#if can("data.export")}
                    <button
                        type="button"
                        class="btn secondary"
                        onclick={exportCSV}
                        title={filtered || search || status
                            ? "Exports the rows this screen is showing, filters and all."
                            : "Exports the whole catalogue, one row per variant."}
                    >
                        <i class="ri-download-2-line" aria-hidden="true"></i>
                        <span class="txt">Export</span>
                    </button>
                {/if}
                {#if writable && hasModule("import-amazon")}
                    <button type="button" class="btn secondary" onclick={openImport}>
                        <i class="ri-amazon-line" aria-hidden="true"></i>
                        <span class="txt">Import from Amazon</span>
                    </button>
                {/if}
                {#if writable}
                    <button type="button" class="btn" onclick={openCreate}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New product</span>
                    </button>
                {/if}
            </div>
        </header>

        {#if chips.length}
            <!-- What the list is showing, spelled out. A filter that is only
                 legible by re-opening the drawer that set it is how an
                 operator comes to believe half their catalogue has gone. -->
            <!-- `.token-list` is carried for its chip-button sizing alone —
                 the same chip the tag tokens use, in the same size. -->
            <div class="token-list flex flex-wrap gap-sm m-b-sm">
                {#each chips as chip (chip.key)}
                    <span class="label">
                        <span class="txt-hint">{chip.label}:</span>
                        {chip.text}
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            aria-label="Remove the {chip.label.toLowerCase()} filter"
                            title="Remove"
                            onclick={() => removeChip(chip)}
                        >
                            <i class="ri-close-line" aria-hidden="true"></i>
                        </button>
                    </span>
                {/each}
                {#if chips.length > 1}
                    <button
                        type="button"
                        class="btn sm transparent secondary"
                        onclick={clearFilters}
                    >
                        <span class="txt">Clear all</span>
                    </button>
                {/if}
            </div>
        {/if}

        <!-- DESIGN.md §5: separation is a border and a background step, not a
             shadow. Only the edge changes here — gocommerce.css already gives
             this wrapper a border and a radius, and these two swap in the
             design's token for each.

             No `overflow-hidden`: table.css makes this element the table's
             horizontal scroller, and the table under it is pinned to a 900px
             minimum. `hidden` clips instead of scrolls, which below about
             1200px left the last column unreachable by mouse or wheel. `auto`
             clips to the radius just as well, so the corners cost nothing. -->
        <div class="page-table-wrapper tw:rounded-xl tw:border">
            <table class="table responsive-table" class:optimize={products.length > 60}>
                <thead class="sticky">
                    <tr>
                        {#if writable}
                            <th class="col-bulk-select min-width">
                                <div class="field">
                                    <input
                                        id="select-all-products"
                                        type="checkbox"
                                        checked={sel.allSelected(products)}
                                        onchange={() => sel.toggleAll(products)}
                                    />
                                    <!-- "on this page", not "all": the panel
                                         does not hold the other pages and the
                                         API has no select-everything call. -->
                                    <label
                                        for="select-all-products"
                                        aria-label="Select every product on this page"
                                    ></label>
                                </div>
                            </th>
                        {/if}
                        <SortHeader
                            field="title"
                            label="Product"
                            class="col-field-name-id"
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="status"
                            label="Status"
                            class="col-field-type-select"
                            {sort}
                            onsort={sortBy}
                        />
                        <!-- Variants counts an embedded array, not a column. A
                             header with no arrow and no hover reads as a fact
                             rather than as a broken control. -->
                        <th class="col-field-type-number min-width">Variants</th>
                        <SortHeader
                            field="price"
                            label="Price"
                            class="col-field-type-number min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="available"
                            label="Available"
                            class="col-field-type-number min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="updated_at"
                            label="Updated"
                            class="col-field-type-date min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each products as product (product.id)}
                        {@const available = totalAvailable(product)}
                        <tr
                            class="handle"
                            tabindex="0"
                            onclick={(e) => openEdit(product, e)}
                            onkeydown={(e) => rowKey(e, () => openEdit(product))}
                        >
                            {#if writable}
                                <!-- stopPropagation rather than a guard inside
                                     the row handler: ticking a box must not
                                     also open the product. -->
                                <td
                                    class="col-bulk-select min-width"
                                    onclick={(e) => e.stopPropagation()}
                                >
                                    <div class="field">
                                        <input
                                            id="select-product-{product.id}"
                                            type="checkbox"
                                            checked={sel.has(product.id)}
                                            onchange={() => sel.toggle(product.id)}
                                        />
                                        <label
                                            for="select-product-{product.id}"
                                            aria-label="Select {product.title}"
                                        ></label>
                                    </div>
                                </td>
                            {/if}
                            <!-- Name and handle on one line. The second line was
                                 costing every row 15px on a screen whose whole job
                                 is to fit rows, and the handle reads as what it is
                                 from its face and colour, not from its position. -->
                            <td class="col-field-name-id" data-name="Product">
                                <div class="row-product">
                                    <!-- A fixed frame whether or not there is a
                                         picture: a thumbnail that collapses when
                                         a product has none takes the whole
                                         column out of line with the rest. -->
                                    <div class="row-thumb">
                                        {#if product.image_url}
                                            <img src={product.image_url} alt="" loading="lazy" />
                                        {:else}
                                            <i class="ri-image-line" aria-hidden="true"></i>
                                        {/if}
                                        <!-- How many pictures there are, floating on the
                                             corner, when the lead one is not the only one. -->
                                        {#if product.media_count > 1}
                                            <span class="row-thumb-count" title="{product.media_count} pictures">
                                                {product.media_count}
                                            </span>
                                        {/if}
                                    </div>
                                    <div class="row-name row-name-stacked">
                                        <a
                                            class="txt-bold txt-ellipsis"
                                            href="{base}/products/{product.id}"
                                        >
                                            {product.title}
                                        </a>
                                        <span class="txt-hint txt-sm txt-code row-handle">
                                            {product.slug}
                                        </span>
                                    </div>
                                </div>
                            </td>
                            <td class="col-field-type-select" data-name="Status">
                                <span class="label status-chip {statusLabel[product.status] ?? ''}">
                                    {product.status}
                                </span>
                            </td>
                            <td class="col-field-type-number min-width" data-name="Variants">
                                {product.variants?.length ?? 0}
                                <span class="txt-hint txt-sm">
                                    {pluralize(product.variants?.length ?? 0, "variant")}
                                </span>
                            </td>
                            <td class="col-field-type-number min-width" data-name="Price">
                                {priceRange(product)}
                            </td>
                            <td
                                class="col-field-type-number min-width {available === null
                                    ? 'txt-hint'
                                    : stockClass(available)}"
                                data-name="Available"
                            >
                                {available === null ? "not tracked" : available}
                            </td>
                            <!-- updated_at already rides on every row, so this
                                 column costs the engine nothing — it is data
                                 the screen was throwing away, and it is what
                                 gives the Updated sort a header to live on. -->
                            <td
                                class="col-field-type-date min-width txt-hint"
                                data-name="Updated"
                                title={formatDate(product.updated_at)}
                            >
                                {relativeTime(product.updated_at)}
                            </td>
                            <td class="col-meta min-width">
                                {#if writable}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary row-delete"
                                        aria-label="Delete {product.title}"
                                        title="Delete"
                                        onclick={(e) => askDelete(product, e)}
                                    >
                                        <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                    </button>
                                {/if}
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !products.length}
                        {#each Array(6) as _, i (i)}
                            <tr>
                                <td colspan={writable ? 8 : 7}>
                                    <span class="skeleton-loader"></span>
                                </td>
                            </tr>
                        {/each}
                    {/if}

                    {#if !loading && !products.length}
                        <tr>
                            <td colspan={writable ? 8 : 7} class="txt-center txt-hint p-base">
                                <div class="m-b-10">
                                    <i class="ri-price-tag-3-line" style="font-size: 32px" aria-hidden="true"></i>
                                </div>
                                {#if search || status || filtered}
                                    No products match that. Try a different search or clear the
                                    {chips.length > 1 ? "filters" : "filter"}.
                                {:else}
                                    No products yet. A product needs at least one variant — the thing
                                    that actually gets sold.
                                {/if}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>

        {#if writable}
            <BulkBar count={sel.count} noun="product" onclear={() => sel.clear()}>
                <!-- Every action here is a per-row route the screen already
                     calls; nothing in the bulk bar is a second write path. -->
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={bulkBusy}
                    onclick={() => bulkStatus("active")}
                >
                    <i class="ri-eye-line" aria-hidden="true"></i>
                    <span class="txt">Activate</span>
                </button>
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={bulkBusy}
                    onclick={() => bulkStatus("draft")}
                >
                    <i class="ri-draft-line" aria-hidden="true"></i>
                    <span class="txt">Draft</span>
                </button>
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={bulkBusy}
                    onclick={() => bulkStatus("archived")}
                >
                    <i class="ri-archive-line" aria-hidden="true"></i>
                    <span class="txt">Archive</span>
                </button>

                <!-- Re-filing, which is what a refused category delete sends an
                     operator here to do. It opens a drawer rather than dropping
                     a tree browser into the bar: a category is singular, so this
                     REPLACES what each product says about itself, and the
                     destination is worth reading before it is applied. -->
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={bulkBusy}
                    onclick={openCategorise}
                >
                    <i class="ri-price-tag-3-line" aria-hidden="true"></i>
                    <span class="txt">File under…</span>
                </button>

                <!-- The collection list is the filter drawer's, fetched once and
                     shared, so a screen that never files anything in bulk never
                     pays for it. -->
                <div class="field">
                    <Select
                        class="compact"
                        ariaLabel="Add to a collection"
                        placeholder="Add to collection…"
                        value=""
                        disabled={bulkBusy}
                        options={[
                            { value: "", label: "Add to collection…" },
                            ...collections.map((c) => ({ value: c.id, label: c.title })),
                        ]}
                        onchange={bulkCollect}
                    />
                </div>

                <button
                    type="button"
                    class="btn sm secondary txt-danger"
                    disabled={bulkBusy}
                    onclick={() => (bulkDeleteOpen = true)}
                >
                    <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                    <span class="txt">Delete</span>
                </button>
            </BulkBar>
        {/if}

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <Pager
                {meta}
                {loading}
                noun="product"
                {perPage}
                onpage={(n) => list.setPage(n)}
                onperpage={(n) => list.set({ limit: n })}
            />
            <div class="flex-fill"></div>
        </footer>
    </div>
</div>
{/if}

<Drawer open={createOpen} size="sm" title="New product" onclose={() => (createOpen = false)}>
    <form id="product-form" onsubmit={create}>
        <div class="field required" class:error={!!errors.title}>
            <label for="title">Title</label>
            <input id="title" type="text" bind:value={form.title} />
        </div>
        {#if errors.title}<div class="field-help error">{errors.title}</div>{/if}

        <div class="field m-t-sm">
            <label for="slug">Slug</label>
            <input
                id="slug"
                type="text"
                placeholder="Derived from the title if left empty"
                bind:value={form.slug}
            />
        </div>

        <div class="field m-t-sm">
            <label for="status">Status</label>
            <Select
                id="status"
                bind:value={form.status}
                options={[
                    { value: "draft", label: "Draft — hidden from shoppers" },
                    { value: "active", label: "Active — on sale" },
                    { value: "archived", label: "Archived" },
                ]}
            />
        </div>

        <h6 class="section-title">
            <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
            The first variant
        </h6>
        <div class="field-help m-b-sm">
            A variant is what actually gets sold. A product with no options still has exactly one,
            and options can be added to it afterwards.
        </div>

        <div class="field" class:error={!!errors.sku}>
            <label for="sku">SKU</label>
            <input
                id="sku"
                type="text"
                placeholder="Derived from the title if left empty"
                value={form.sku}
                oninput={(e) => {
                    skuEdited = true;
                    form.sku = e.currentTarget.value;
                }}
            />
        </div>
        {#if errors.sku}
            <div class="field-help error">{errors.sku}</div>
        {:else if !skuEdited && form.sku}
            <div class="field-help">Suggested from the title. Type over it to use your own.</div>
        {/if}

        <div class="fields m-t-sm">
            <div class="field required" class:error={!!errors.price}>
                <label for="price">Price ({currency})</label>
                <input id="price" type="text" inputmode="decimal" bind:value={form.price} />
            </div>
            <div class="delimiter"></div>
            <div class="field">
                <label for="stock">Stock on hand</label>
                <input id="stock" type="number" min="0" bind:value={form.stock} />
            </div>
        </div>
        {#if errors.price}<div class="field-help error">{errors.price}</div>{/if}

        <div class="field-help m-t-sm">
            Description, media, options and the search listing are on the editor, which this
            opens as soon as the product exists.
        </div>
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (createOpen = false)}>
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form="product-form"
            class="btn"
            class:loading={saving}
            disabled={saving}
        >
            <span class="txt">Create product</span>
        </button>
    {/snippet}
</Drawer>

{#if hasModule("import-amazon")}
    <Drawer open={importOpen} size="sm" title="Import from Amazon" onclose={closeImport}>
        {#if !importJob}
            <form id="import-form" onsubmit={startImport}>
                <div class="field required">
                    <label for="import-url">Listing URL</label>
                    <input
                        id="import-url"
                        type="url"
                        placeholder="https://www.amazon.com/dp/…"
                        bind:value={importForm.url}
                    />
                </div>
                <div class="field-help">
                    Any marketplace. The listing and every one of its variations are read in a
                    real Chrome, the copy is rewritten, and the product is created as a draft for
                    you to check before it goes on sale.
                </div>

                <h6 class="section-title">
                    <i class="ri-image-line" aria-hidden="true"></i>
                    Pictures
                </h6>
                <div class="field">
                    <input id="import-background" type="checkbox" class="switch" bind:checked={importForm.background} />
                    <label for="import-background">New background</label>
                </div>
                <div class="field-help">
                    A plain white background becomes a soft grey gradient with a shadow under the
                    product, drawn a little smaller inside it. Photos without a plain background are
                    left as they are.
                </div>
                <div class="field m-t-sm">
                    <input id="import-brighten" type="checkbox" class="switch" bind:checked={importForm.brighten} />
                    <label for="import-brighten">Lift the lighting a little</label>
                </div>
                <div class="field m-t-sm">
                    <input id="import-tilt" type="checkbox" class="switch" bind:checked={importForm.tilt} />
                    <label for="import-tilt">Turn it three degrees</label>
                </div>
                <div class="field m-t-sm">
                    <input id="import-mirror" type="checkbox" class="switch" bind:checked={importForm.mirror} />
                    <label for="import-mirror">Mirror left to right</label>
                </div>
                <div class="field-help">
                    Background, lighting and orientation change; the product itself does not — no
                    colour shift. Mirroring reverses any text in a picture, so switch it off for
                    packaging shots. None of this alters whose picture it is.
                </div>

                <h6 class="section-title">
                    <i class="ri-price-tag-3-line" aria-hidden="true"></i>
                    Prices and variations
                </h6>
                <div class="fields">
                    <div class="field">
                        <label for="import-price">Price for every variant ({currency})</label>
                        <input
                            id="import-price"
                            type="text"
                            inputmode="decimal"
                            placeholder="Leave empty to keep the listing's"
                            bind:value={importForm.price}
                        />
                    </div>
                    <div class="delimiter"></div>
                    <div class="field">
                        <label for="import-max">Variations to visit</label>
                        <input id="import-max" type="number" min="1" max="200" bind:value={importForm.max_variants} />
                    </div>
                </div>
                <div class="field-help">
                    The listing's prices are used only when the marketplace sells in {currency};
                    otherwise they are recorded but not set, and the product stays a draft until
                    you price it here or in the editor. Each visit opens one variation's page —
                    one of every colour first — and each variant gets the pictures its page
                    showed; a size past the limit borrows its colour's.
                </div>

                <div class="field m-t-sm">
                    <label for="import-status">Create it as</label>
                    <Select
                        id="import-status"
                        bind:value={importForm.status}
                        options={[
                            { value: "draft", label: "Draft — check it first" },
                            { value: "active", label: "Active — on sale at once" },
                        ]}
                    />
                </div>
            </form>
        {:else}
            <div class="block">
                {#if importBusy}
                    <div class="flex gap-sm">
                        <span class="loader sm" aria-hidden="true"></span>
                        <span class="txt-bold">{importJob.message || importJob.step || "Starting"}</span>
                    </div>
                    {#if importJob.step === "waiting"}
                        <div class="field-help m-t-sm">
                            Amazon wants a person to click first. It is waiting in the Chrome window
                            on the machine the store runs on; the import continues on its own once
                            you have.
                        </div>
                    {/if}
                {:else if importJob.status === "done"}
                    <div class="txt-bold">
                        <i class="ri-checkbox-circle-line" aria-hidden="true"></i>
                        {importJob.message}
                    </div>
                {:else if importJob.status === "blocked"}
                    <div class="txt-bold">
                        <i class="ri-robot-line" aria-hidden="true"></i>
                        Amazon asked for a person
                    </div>
                    <div class="field-help m-t-xs">{importJob.message}</div>
                {:else}
                    <div class="txt-bold txt-danger">
                        <i class="ri-error-warning-line" aria-hidden="true"></i>
                        The import failed
                    </div>
                    <div class="field-help m-t-xs">{importJob.message}</div>
                {/if}

                {#if importJob.warnings?.length}
                    <h6 class="section-title m-t-base">
                        <i class="ri-alert-line" aria-hidden="true"></i>
                        Worth reading before it goes on sale
                    </h6>
                    <ul class="m-0 p-l-base">
                        {#each importJob.warnings as warning (warning)}
                            <li class="txt-sm txt-hint">{warning}</li>
                        {/each}
                    </ul>
                {/if}
            </div>
        {/if}

        {#snippet footer()}
            <button type="button" class="btn transparent m-r-auto" onclick={closeImport}>
                <span class="txt">{importJob && !importBusy ? "Close" : "Cancel"}</span>
            </button>
            {#if !importJob}
                <button type="submit" form="import-form" class="btn" class:loading={importing} disabled={importing}>
                    <span class="txt">Import</span>
                </button>
            {:else if importJob.status === "done" && importJob.product_id}
                <a class="btn" href="{base}/products/{importJob.product_id}">
                    <span class="txt">Open the product</span>
                </a>
            {:else if !importBusy}
                <button type="button" class="btn secondary" onclick={() => (importJob = null)}>
                    <span class="txt">Try again</span>
                </button>
            {/if}
        {/snippet}
    </Drawer>
{/if}

<ProductFilters
    open={filterOpen}
    value={segment}
    {vendors}
    {productTypes}
    tags={tagPool}
    {categories}
    {categoriesTruncated}
    {collections}
    loadingVocabulary={vocabularyState === "loading"}
    onapply={applyFilters}
    onclose={() => (filterOpen = false)}
/>

<Confirm
    bind:open={confirmOpen}
    title="Delete this product?"
    message={pendingDelete
        ? `"${pendingDelete.title}" and its variants will be removed. Orders that included it keep their own snapshot, so history stays readable.`
        : ""}
    confirmLabel="Delete"
    danger
    onconfirm={doDelete}
/>

<!-- A second Confirm rather than a shared one with a mode flag: this message
     names a count and the other names a product, and one component asked to say
     both ends up saying neither. -->
<Confirm
    bind:open={bulkDeleteOpen}
    title="Delete {sel.count} {pluralize(sel.count, 'product')}?"
    message="Their variants go with them. Orders that included them keep their own snapshot, so history stays readable. Anything the engine refuses is reported row by row."
    confirmLabel="Delete"
    danger
    onconfirm={bulkDelete}
/>

<!--
    Re-filing a selection.

    A drawer rather than a control in the bulk bar, for two reasons a
    <select> of collections does not have: the tree browser needs room, and
    a category REPLACES the one answer a product gives about what kind of
    thing it is, so the destination is worth reading before it is applied.
-->
<Drawer
    open={categoriseOpen}
    size="sm"
    title="File {sel.count} {pluralize(sel.count, 'product')}"
    onclose={() => (categoriseOpen = false)}
>
    <div class="field">
        <label for="bulk-category">Category</label>
        <CategoryPicker
            id="bulk-category"
            bind:value={bulkCategoryID}
            {categories}
            remote={categoriesTruncated}
        />
    </div>
    <div class="field-help">
        One product, one category — this replaces whatever each of them says now, rather
        than adding to it. Choosing <strong>Uncategorised</strong> clears the field, which is
        what a category that has to be deleted needs first.
    </div>

    {#snippet footer()}
        <button
            type="button"
            class="btn transparent m-r-auto"
            onclick={() => (categoriseOpen = false)}
        >
            <span class="txt">Cancel</span>
        </button>
        <span class="txt txt-hint m-r-sm">
            {wouldRefile}
            {wouldRefile === 1 ? "moves" : "move"}
        </span>
        <button
            type="button"
            class="btn"
            class:loading={bulkBusy}
            disabled={bulkBusy || !wouldRefile}
            onclick={bulkCategorise}
        >
            <span class="txt">File them</span>
        </button>
    {/snippet}
</Drawer>
