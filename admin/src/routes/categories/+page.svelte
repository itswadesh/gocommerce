<script>
    /**
     * The category tree.
     *
     * A tree is rendered as a flat table with an indent rather than as nested
     * lists, because everything else on this screen — the row hover, the
     * responsive stacking, the meta column — is table behaviour that PocketBase
     * already has, and nesting <ul>s would rebuild all of it badly. The server
     * returns the tree flattened depth-first with a `depth` on each row, so the
     * indent is the only thing the table has to do that a flat list would not.
     *
     * Delete is refused by the engine while anything still points at a category
     * — subcategories or products — and says which. That refusal is the whole
     * safety story, so this page does not pre-empt it with a guess: it asks,
     * sends, and shows what came back.
     */
    import { base } from "$app/paths";
    import { api, can, query, request } from "$lib/api.js";
    import { onNewShortcut } from "$lib/shortcuts.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { selection } from "$lib/selection.svelte.js";
    import { runBulk } from "$lib/bulk.js";
    import { toast } from "$lib/toast.svelte.js";
    import { pluralize } from "$lib/format.js";
    import BulkBar from "$lib/components/BulkBar.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import TokenInput from "$lib/components/TokenInput.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import CategoryPicker from "$lib/components/CategoryPicker.svelte";
    import { fieldText } from "$lib/fieldtext.js";
    import CategoryMove from "$lib/components/CategoryMove.svelte";
    import RecordHistory from "$lib/components/RecordHistory.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    /* A page of matches rather than the default fifty: this is the screen the
       tree belongs to, not a dropdown, so it can afford to show more of an
       answer before asking the operator to narrow it. */
    const SEARCH_LIMIT = 100;

    /* The search, its page and its page size live in the URL, like every other
       list screen — a search for "bird cage" that cannot be sent to anybody is
       half a feature, and Back out of a category has to land on the same
       matches. The size is a key like the rest, so the Rows control the other
       fifteen listings have works here too. */
    const list = listState({ q: "", page: 1, limit: SEARCH_LIMIT });
    const search = $derived(list.params.q);
    const perPage = $derived(list.params.limit);
    let draftSearch = $state(list.params.q);

    let loading = $state(true);
    let saving = $state(false);
    /**
     * The rows on screen, in the order they are drawn: roots, with each
     * expanded branch's children spliced in directly beneath it.
     *
     * One level at a time, because the whole tree is not a thing that can be
     * asked for any more. Importing Shopify's taxonomy puts fourteen thousand
     * categories in this table; the listing that used to return all of them now
     * returns a bounded slice, and a page that rendered that slice would show
     * two hundred rows and call it the catalogue.
     */
    let categories = $state([]);
    let total = $state(0);
    let expanded = $state(new Set());
    let busyRow = $state(null);

    /**
     * The matches for the search box, which is a different question from the
     * tree and gets its own answer.
     *
     * `GET /api/admin/categories?q=` is the engine's own bounded search — the
     * one the picker in this page's own drawer has always used — and it returns
     * a flat set of `full_name` paths rather than a shape. So while a search is
     * running the table renders breadcrumbs with no indent and no expander: the
     * indentation would be describing a tree that is no longer on screen.
     */
    let results = $state([]);
    let resultMeta = $state(null);
    let searchLoading = $state(false);

    let editorOpen = $state(false);
    let editing = $state(null); // null = creating
    let form = $state({ title: "", slug: "", position: "", parent_id: null });
    /**
     * The fields products in this category are asked for, as the drawer edits
     * them. They live in the category's `metadata.attributes` — the same place
     * a taxonomy import would write them — and the product editor reads them
     * from there, inherited down the tree.
     */
    let attributes = $state([]);
    let errors = $state({});

    let confirmOpen = $state(false);
    let pendingDelete = $state(null);

    /* Who changed this category, from the row it is about. The route is gated
       on catalog.read — the same right that draws this screen — rather than on
       store.operate, which is what makes it answerable by a manager who cannot
       open the store-wide feed at all. */
    let historyOpen = $state(false);
    let historyFor = $state(null);

    function openHistory(category, event) {
        event?.stopPropagation();
        historyFor = category;
        historyOpen = true;
    }

    /* The tree is catalog.read; everything that changes it is catalog.write.
       Both are the nav's own gate on this screen, said again here so a typed
       address does not render an armed screen that 403s on every press. */
    const readable = $derived(can("catalog.read"));
    const writable = $derived(can("catalog.write"));

    /* `n` creates one, from anywhere on this screen. The shell owns the
       keystroke and fires an event; what "new" means is the screen's. */
    $effect(() => {
        if (!writable) return;
        return onNewShortcut(() => openCreate(null));
    });

    /**
     * The parent picker must not offer the category being edited, or anything
     * beneath it — those are exactly the moves the engine refuses as cycles,
     * and offering a choice that always fails is worse than not offering it.
     */
    const parentOptions = $derived.by(() => {
        if (!editing) return categories;
        const banned = new Set([editing.id]);
        // One pass is enough: the rows are in draw order, so a category's
        // ancestors are always seen before it.
        for (const c of categories) {
            if (c.parent_id !== null && banned.has(c.parent_id)) banned.add(c.id);
        }
        return categories.filter((c) => !banned.has(c.id));
    });

    // Past the point where the whole tree fits in one response the picker has
    // to search rather than filter what this page happens to have open.
    const parentRemote = $derived(total > categories.length);

    /**
     * The part of `full_name` above this row's own title — "Apparel" out of
     * "Apparel / Clothing", and empty for a root.
     *
     * Derived rather than requested: `full_name` is computed by the engine on
     * every row already (categories.go builds it from the ancestry rather than
     * storing a path, so a rename cannot leave a stale one behind), and asking
     * for the ancestors of every visible row would be one request per row to
     * learn something the row is already carrying.
     */
    function ancestryOf(category) {
        const full = category.full_name;
        if (!full || full === category.title) return "";
        const cut = full.length - category.title.length - 3; /* " / " */
        return cut > 0 ? full.slice(0, cut) : "";
    }

    /* ------------------------------------------------------- drag to reorder
     *
     * Siblings only. The table is a flattened tree, so a drop between two rows
     * at different depths has no single honest meaning — "after Clothing" and
     * "into Clothing" look identical on screen. Moving a category to a
     * different parent has a control that says exactly that: the Parent picker
     * in the drawer. Dragging is refused while a search is running for the same
     * reason the indent is suppressed there: the rows are matches from all over
     * the tree, not an order.
     */
    let dragId = $state(null);
    let dropId = $state(null);

    /** Where this row's subtree ends: everything deeper, up to the next row that is not. */
    function subtreeEnd(index) {
        const depth = categories[index].depth;
        let end = index + 1;
        while (end < categories.length && categories[end].depth > depth) end++;
        return end;
    }

    const draggedRow = $derived(dragId === null ? null : categories.find((c) => c.id === dragId));

    /** A row is a target when it is a sibling of the one being dragged, and not itself. */
    function isDropTarget(category) {
        const from = draggedRow;
        return !!from && category.id !== from.id && category.parent_id === from.parent_id;
    }

    function startRowDrag(event, category) {
        if (!writable || search) return;
        dragId = category.id;
        // Firefox refuses to start a drag without a payload.
        event.dataTransfer?.setData("text/plain", String(category.id));
        if (event.dataTransfer) event.dataTransfer.effectAllowed = "move";
    }

    function dragOverRow(event, category) {
        if (!isDropTarget(category)) return;
        event.preventDefault();
        dropId = category.id;
    }

    function endRowDrag() {
        dragId = null;
        dropId = null;
    }

    async function dropOnRow(event, target) {
        if (!isDropTarget(target)) return;
        event.preventDefault();
        const moving = dragId;
        endRowDrag();
        await moveBefore(moving, target.id);
    }

    /**
     * Move one category to where another sibling sits, taking its open subtree
     * with it, then persist the whole sibling order.
     *
     * The local move happens first so the row lands under the cursor rather
     * than after a round trip; a failed save reloads the tree, which is the
     * only honest way back from an order the store did not accept.
     */
    async function moveBefore(movingId, targetId) {
        const from = categories.findIndex((c) => c.id === movingId);
        const to = categories.findIndex((c) => c.id === targetId);
        if (from < 0 || to < 0 || from === to) return;

        const parentID = categories[from].parent_id ?? null;
        const block = categories.slice(from, subtreeEnd(from));
        const without = [...categories.slice(0, from), ...categories.slice(subtreeEnd(from))];

        // Dragging downward lands after the target's own subtree, so the row
        // ends up below it rather than inside the gap above it.
        let at = without.findIndex((c) => c.id === targetId);
        if (from < to) {
            const depth = without[at].depth;
            at += 1;
            while (at < without.length && without[at].depth > depth) at += 1;
        }
        categories = [...without.slice(0, at), ...block, ...without.slice(at)];
        await persistOrder(parentID);
    }

    /** The keyboard equivalent, because a drag has none unless one is written. */
    async function nudge(category, delta) {
        const siblings = categories.filter((c) => c.parent_id === category.parent_id);
        const i = siblings.findIndex((c) => c.id === category.id);
        const target = siblings[i + delta];
        if (!target) return;
        await moveBefore(category.id, target.id);
    }

    async function persistOrder(parentID) {
        const ids = categories.filter((c) => (c.parent_id ?? null) === parentID).map((c) => c.id);
        try {
            await request("PUT", "/api/admin/categories/reorder", {
                body: { parent_id: parentID, ids },
            });
            // The positions the rows carry are now stale by exactly their index.
            let n = 0;
            categories = categories.map((c) =>
                (c.parent_id ?? null) === parentID ? { ...c, position: n++ } : c,
            );
        } catch (err) {
            toast.error(err);
            loadTree();
        }
    }

    /** What the table draws: the matches while a search is running, else the tree. */
    const rows = $derived(search ? results : categories);

    /*
     * Two effects, not one. The tree is loaded once and kept — a search must not
     * collapse every branch the operator opened, and clearing the box has to put
     * them back rather than re-fetch roots. The roots are loaded even while a
     * search is running because the drawer's Parent picker reads `categories`
     * and `total` off this page, and a deep link straight into `?q=` would
     * otherwise open a drawer whose picker believes the store has no tree.
     */
    $effect(() => {
        loadTree();
    });

    $effect(() => {
        list.params;
        runSearch();
    });

    async function loadTree() {
        // The screen is refused above; asking anyway would put a 403 toast
        // over the explanation.
        if (!readable) {
            loading = false;
            return;
        }
        loading = true;
        expanded = new Set();
        try {
            const [roots, sized] = await Promise.all([
                api.get("/api/admin/categories?parent=root"),
                // The bounded listing is the only thing that reports the real
                // size of the tree, and the footer should not claim the number
                // of rows it happens to be showing.
                api.get("/api/admin/categories?flat=1"),
            ]);
            categories = roots.data ?? [];
            total = sized.meta?.total ?? categories.length;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /* Two submissions leave two requests in flight, and the table would
       otherwise settle on whichever reply arrived last rather than on the term
       in the box. */
    let searchReq = 0;

    async function runSearch() {
        const term = search;
        const mine = ++searchReq;
        if (!term) {
            results = [];
            resultMeta = null;
            searchLoading = false;
            return;
        }
        searchLoading = true;
        try {
            const res = await api.get(
                "/api/admin/categories" + list.query({ limit: perPage }),
            );
            if (mine !== searchReq) return;
            results = res.data ?? [];
            resultMeta = res.meta ?? null;
        } catch (err) {
            if (mine === searchReq) toast.error(err);
        } finally {
            if (mine === searchReq) searchLoading = false;
        }
    }

    function submitSearch(event) {
        event.preventDefault();
        list.set({ q: draftSearch.trim() });
    }

    function clearSearch() {
        draftSearch = "";
        list.set({ q: "" });
    }

    /** Refresh means everything, including the counts — which is the one thing
     *  a re-read of the tree would otherwise leave stale. */
    function refresh() {
        counted = {};
        asked.clear();
        loadTree();
        runSearch();
    }

    /**
     * How many products are filed under a category, and under everything
     * beneath it.
     *
     * There is no per-category count on the wire, and there does not need to be:
     * `GET /api/admin/products?category_id=&limit=1` answers with the subtree's
     * `meta.total` and one row. One request per row on screen, capped at four at
     * a time so opening a branch of forty does not put forty requests on the
     * wire at once, and cached — a count is what makes "what would break if I
     * delete this" answerable before the engine refuses.
     */
    let counted = $state({});
    /* Plain, not $state: reading it inside the effect below would make the
       effect depend on every id it has already seen and re-run on every answer. */
    const asked = new Set();
    const pending = [];
    let inflight = 0;

    $effect(() => {
        for (const row of rows) {
            if (asked.has(row.id)) continue;
            asked.add(row.id);
            pending.push(row.id);
        }
        pump();
    });

    function pump() {
        while (inflight < 4 && pending.length) {
            const id = pending.shift();
            inflight++;
            api.get("/api/admin/products" + query({ category_id: id, limit: 1 }))
                .then((res) => {
                    counted[id] = res.meta?.total ?? 0;
                })
                .catch(() => {
                    // A role with catalog.read but no products, or a store that
                    // answered badly: the row simply says nothing about products
                    // rather than claiming zero.
                    counted[id] = null;
                })
                .finally(() => {
                    inflight--;
                    pump();
                });
        }
    }

    /**
     * Expanding splices a branch's children in under it; collapsing removes
     * that whole subtree, however deep it was opened.
     *
     * Children are fetched once and then kept, so re-opening a branch is
     * instant — and closing one does not throw away work the operator may be
     * about to want back.
     */
    async function toggle(category) {
        const index = categories.findIndex((c) => c.id === category.id);
        if (index < 0) return;

        if (expanded.has(category.id)) {
            // Everything deeper than this row, up to the next sibling-or-
            // shallower row, is its subtree.
            let end = index + 1;
            while (end < categories.length && categories[end].depth > category.depth) end++;
            const removed = categories.slice(index + 1, end);
            categories = [...categories.slice(0, index + 1), ...categories.slice(end)];
            const next = new Set(expanded);
            next.delete(category.id);
            for (const r of removed) next.delete(r.id);
            expanded = next;
            return;
        }

        busyRow = category.id;
        try {
            const result = await api.get(`/api/admin/categories?parent=${category.id}`);
            const children = result.data ?? [];
            categories = [
                ...categories.slice(0, index + 1),
                ...children,
                ...categories.slice(index + 1),
            ];
            expanded = new Set(expanded).add(category.id);
        } catch (err) {
            toast.error(err);
        } finally {
            busyRow = null;
        }
    }

    function openCreate(parentID = null) {
        editing = null;
        // Empty rather than 0: the engine puts a new category at the end of its
        // siblings when no position is sent, and 0 would silently put every new
        // one first instead.
        form = { title: "", slug: "", position: "", parent_id: parentID };
        attributes = [];
        errors = {};
        editorOpen = true;
    }

    function openEdit(category) {
        editing = category;
        form = {
            title: category.title,
            slug: category.slug,
            position: String(category.position ?? 0),
            parent_id: category.parent_id ?? null,
        };
        attributes = (category.metadata?.attributes ?? []).map((a) => ({
            key: a.key ?? "",
            label: a.label ?? "",
            choices: [...(a.choices ?? [])],
        }));
        errors = {};
        editorOpen = true;
    }


    function addAttribute() {
        attributes = [...attributes, { key: "", label: "", choices: [] }];
    }

    /**
     * The handle suggestions for a new field.
     *
     * A category asks for a field by its handle, and the choices come from the
     * shared dictionary keyed on exactly that string. Derived keys are
     * underscored (`sleeve_length`) while every published handle is hyphenated
     * (`sleeve-length`), so without this an operator could type a field the
     * dictionary defines and get free text anyway, with nothing on screen to
     * explain it. Suggesting the real handles is what stops anyone ever having
     * to know that.
     *
     * Searched rather than listed: after the taxonomy import the dictionary runs
     * to hundreds of entries and the listing is capped at 200.
     */
    let handleSuggestions = $state([]);
    let handleTimer = null;

    function suggestHandles(term) {
        clearTimeout(handleTimer);
        handleTimer = setTimeout(async () => {
            try {
                const res = await api.get(
                    "/api/admin/taxonomy-attributes" + query({ q: term, limit: 20 }),
                );
                handleSuggestions = res.data ?? [];
            } catch {
                // A dictionary that cannot be read is a missing convenience,
                // not a broken form: the handle is free text either way.
                handleSuggestions = [];
            }
        }, 200);
    }

    /** Picking a suggestion fills the label too, while the label is still blank. */
    function handleChosen(attr) {
        const match = handleSuggestions.find(
            (h) => h.handle === attr.key.trim().toLowerCase(),
        );
        if (match && !attr.label.trim()) attr.label = match.label;
    }

    function removeAttribute(index) {
        attributes = attributes.filter((_, i) => i !== index);
    }

    /**
     * The fields as they are stored.
     *
     * The key is derived from the label once and then left alone: it is what a
     * product's saved answers are filed under, so renaming "Skin type" to
     * "Suitable for skin type" must not orphan every answer already given.
     * A row with no label is dropped — an empty row is one somebody started and
     * abandoned, the same rule the product editor's own metafields use.
     */
    function cleanedAttributes() {
        const out = [];
        const taken = new Set();
        for (const attr of attributes) {
            const label = attr.label.trim();
            if (!label) continue;
            // Folded, because the engine matches a handle literally and the
            // dictionary is keyed on the lower-case form: "Sleeve-Length" typed
            // here would be a field no dictionary entry could ever answer.
            let key = (attr.key || keyFor(label)).trim().toLowerCase();
            while (taken.has(key)) key += "-2";
            taken.add(key);
            out.push({
                key,
                label,
                choices: attr.choices.map((c) => c.trim()).filter(Boolean),
            });
        }
        return out;
    }

    function keyFor(label) {
        return (
            label
                .toLowerCase()
                .replace(/[^a-z0-9]+/g, "_")
                .replace(/^_|_$/g, "") || "field"
        );
    }

    function addChild(category, event) {
        event.stopPropagation();
        openCreate(category.id);
    }

    async function save(event) {
        event?.preventDefault();
        if (saving) return;

        errors = {};
        if (!form.title.trim()) errors.title = "A title is required.";
        /*
         * A metafield is identified by its label, and cleanedAttributes() drops
         * a row without one. Dropping an untouched row is right — clicking "Add
         * a metafield" and changing your mind should not be an error. Dropping
         * one somebody filled in is not: a handle typed with no label went away
         * on save and the screen said "Category saved", so the only way to find
         * out was to reopen the drawer and notice the row missing.
         */
        const halfFilled = attributes.findIndex(
            (a) => !a.label.trim() && (a.key.trim() || a.choices.some((c) => c.trim())),
        );
        if (halfFilled !== -1) {
            errors.attributes = `Field ${halfFilled + 1} needs a name before it can be saved.`;
        }
        const position = readPosition();
        if (position === undefined && fieldText(form.position) !== "") {
            errors.position = "A position is a whole number, 0 or more.";
        }
        if (Object.keys(errors).length) return;

        saving = true;
        try {
            if (editing) {
                // parent_id always travels on an update, including as null:
                // that null is "move to the top level", and omitting it is
                // "leave the parent alone". The two are different requests.
                await api.patch(`/api/admin/categories/${editing.id}`, {
                    title: form.title.trim(),
                    slug: form.slug.trim() || undefined,
                    // undefined is "leave the order alone"; a number is a move.
                    position,
                    parent_id: form.parent_id,
                    // metadata is replaced whole, so whatever else is on the
                    // category rides along — `taxonomy_gid` is written by the
                    // importer and would be lost by a bare {attributes}.
                    metadata: { ...(editing.metadata ?? {}), attributes: cleanedAttributes() },
                });
                toast.success("Category saved");
            } else {
                await api.post("/api/admin/categories", {
                    title: form.title.trim(),
                    slug: form.slug.trim() || undefined,
                    position,
                    parent_id: form.parent_id,
                    metadata: { attributes: cleanedAttributes() },
                });
                toast.success("Category created");
            }
            editorOpen = false;
            refresh();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    /**
     * The Position box as the API takes it: a number, or undefined for "say
     * nothing about it".
     *
     * Undefined is a real answer on both verbs — the engine appends a new
     * category to the end of its siblings when none is sent, and leaves an
     * existing one where it is. It is also what a malformed box returns, which
     * is why save() checks the box was empty before treating it as one.
     */
    function readPosition() {
        const raw = fieldText(form.position);
        if (raw === "") return undefined;
        const n = Number(raw);
        if (!Number.isInteger(n) || n < 0) return undefined;
        return n;
    }

    function askDelete(category, event) {
        event.stopPropagation();
        pendingDelete = category;
        confirmOpen = true;
    }

    /**
     * What is about to break, said before the engine says it.
     *
     * The refusal is still the safety story — it is transactional and this is a
     * count taken a moment ago — but an operator who can read "and 312 products"
     * before pressing Delete does not have to discover it from a red toast.
     */
    function deleteMessage(category) {
        const products = counted[category.id];
        const holds = [];
        if (category.child_count > 0) {
            holds.push(`${category.child_count} ${pluralize(category.child_count, "subcategory", "subcategories")}`);
        }
        if (typeof products === "number" && products > 0) {
            holds.push(`${products} ${pluralize(products, "product")}`);
        }
        if (!holds.length) {
            return `${category.title} will be removed. If any subcategories or products still point at it, the store will refuse and tell you how many.`;
        }
        return (
            `${category.title} still holds ${holds.join(" and ")} — counted through everything ` +
            `beneath it. The store will refuse the delete until they are moved elsewhere.`
        );
    }

    async function doDelete() {
        try {
            await api.delete(`/api/admin/categories/${pendingDelete.id}`);
            toast.success(`Deleted ${pendingDelete.title}`);
            refresh();
        } catch (err) {
            // A 409 here is the engine refusing while subcategories or products
            // still point at it, and its message already names the count.
            toast.error(err);
        }
    }

    // ------------------------------------------------------------ selection

    /*
     * The categories an operator has picked, across the tree they have opened
     * or the search they ran. Retiring last season's taxonomy was one
     * confirmation per node.
     *
     * Cleared by the search, because the rows that were picked are not on
     * screen any more and a delete of rows nobody can see is the accident this
     * exists to prevent. Expanding a branch is deliberately NOT a clear: the
     * rows already picked are still exactly where they were.
     *
     * There is no bulk activate or deactivate here and that is the engine's
     * shape rather than an omission — a category has no `active` (categories.go
     * carries title, slug, position, parent and metadata), so the only
     * per-row write route a selection can be N calls to is the delete.
     */
    const sel = selection();

    $effect(() => {
        list.params.q;
        sel.clear();
    });

    let bulkBusy = $state(false);
    let bulkDeleteOpen = $state(false);
    let moveOpen = $state(false);
    /*
     * The selection as it was when Move was pressed, not the live one.
     *
     * A move that half succeeds leaves the drawer open on the refusals, and
     * `ondone` has already cleared the selection and re-read the tree by then —
     * so a drawer reading `picked` would empty its own list out from under the
     * sentences explaining which rows did not go.
     */
    let moveRows = $state([]);

    function openMove() {
        moveRows = picked;
        moveOpen = true;
    }

    const picked = $derived(sel.pick(rows));

    /* How many of the selection still hold something, for the message. The
       counts are the ones already on screen beside each row, so this costs no
       request — and the engine's refusal is still the safety story. */
    const pickedHolding = $derived(
        picked.filter(
            (c) => c.child_count > 0 || (typeof counted[c.id] === "number" && counted[c.id] > 0),
        ),
    );

    /**
     * Deleting a selection: N calls to the per-row route the bin already uses,
     * deepest first.
     *
     * The engine refuses to delete a category while anything still points at
     * it, so a branch selected whole would fail on every parent if the rows
     * went in draw order — and the operator would be told about refusals that
     * were only an ordering artefact. Child before parent, the same selection
     * goes through.
     */
    async function bulkDelete() {
        const ordered = [...picked].sort((a, b) => (b.depth ?? 0) - (a.depth ?? 0));
        bulkBusy = true;
        try {
            await runBulk(ordered, (c) => api.delete(`/api/admin/categories/${c.id}`), {
                describe: "Deleted",
                noun: "category",
                plural: "categories",
                label: (c) => c.full_name || c.title,
            });
        } finally {
            bulkBusy = false;
        }
        sel.clear();
        refresh();
    }
</script>

<svelte:head><title>Categories · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="catalog.read" what="categories" />
{:else}

<div class="page page-categories shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Categories</div>
            </nav>

            <div class="inline-flex gap-sm">
                <button
                    type="button"
                    class="btn circle transparent secondary"
                    title="Refresh"
                    aria-label="Refresh"
                    onclick={refresh}
                >
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>

            <!-- The tree is browsed one level at a time because it can hold
                 fourteen thousand nodes, which is exactly why it also has to be
                 searchable: expanding level by level is not a way to reach a
                 leaf whose parent you cannot name. -->
            <form class="fields searchbar" onsubmit={submitSearch}>
                <div class="field">
                    <input
                        type="text"
                        class="p-l-20"
                        placeholder="Search every category by name"
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
                {#if writable}
                    <button type="button" class="btn" onclick={() => openCreate(null)}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New category</span>
                    </button>
                {/if}
            </div>
        </header>

        <div class="page-table-wrapper tw:rounded-xl tw:border">
            <table class="table responsive-table">
                <thead class="sticky">
                    <tr>
                        {#if writable}
                            <th class="col-bulk-select min-width">
                                <div class="field">
                                    <input
                                        id="select-all-categories"
                                        type="checkbox"
                                        checked={sel.allSelected(rows)}
                                        onchange={() => sel.toggleAll(rows)}
                                    />
                                    <!-- "on screen", not "the whole tree": the
                                         panel holds the branches that are open
                                         and nothing else, and the API has no
                                         select-everything call. -->
                                    <label
                                        for="select-all-categories"
                                        aria-label="Select every category on screen"
                                    ></label>
                                </div>
                            </th>
                        {/if}
                        <th class="col-field-name-id">Category</th>
                        <th class="col-type-text">Slug</th>
                        <!-- What is filed under it, counted through the whole
                             subtree — the same reach the link's filter has. -->
                        <th class="col-field-type-number min-width">Products</th>
                        <!-- Sibling order. Read-only here and editable in the
                             drawer: it is the number a storefront menu is drawn
                             in, and it was previously invisible. -->
                        <th class="col-field-type-number min-width">Position</th>
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each rows as category (category.id)}
                        <tr
                            class="handle"
                            class:row-dragging={dragId === category.id}
                            class:row-drop-target={dropId === category.id}
                            tabindex="0"
                            draggable={writable && !search}
                            ondragstart={(e) => startRowDrag(e, category)}
                            ondragover={(e) => dragOverRow(e, category)}
                            ondrop={(e) => dropOnRow(e, category)}
                            ondragend={endRowDrag}
                            onclick={() => openEdit(category)}
                            onkeydown={(e) => rowKey(e, () => openEdit(category))}
                        >
                            {#if writable}
                                <!-- stopPropagation rather than a guard inside
                                     the row handler: ticking a box must not also
                                     open the category. -->
                                <td
                                    class="col-bulk-select min-width"
                                    onclick={(e) => e.stopPropagation()}
                                >
                                    <div class="field">
                                        <input
                                            id="select-category-{category.id}"
                                            type="checkbox"
                                            checked={sel.has(category.id)}
                                            onchange={() => sel.toggle(category.id)}
                                        />
                                        <label
                                            for="select-category-{category.id}"
                                            aria-label="Select {category.full_name ||
                                                category.title}"
                                        ></label>
                                    </div>
                                </td>
                            {/if}
                            <td class="col-field-name-id" data-name="Category">
                                {#if search}
                                    <!-- A match may be anywhere in the tree, so
                                         its path is the only thing that says
                                         which "Shirts" this is. The indent is
                                         suppressed rather than kept: depth is
                                         still on the row and would be drawing a
                                         shape that is not on screen. -->
                                    <span class="txt-bold txt-ellipsis">
                                        {category.full_name || category.title}
                                    </span>
                                {:else}
                                    <!-- The arrows are not a nicety: dragging is
                                         the only pointer gesture for this, and
                                         it has no keyboard equivalent unless one
                                         is written. -->
                                    {#if writable && !search}
                                        <button
                                            type="button"
                                            class="btn xs transparent secondary row-grip"
                                            aria-label="Reorder {category.title}. Use the up and down arrows."
                                            title="Drag to reorder, or use the arrow keys"
                                            onclick={(e) => e.stopPropagation()}
                                            onkeydown={(e) => {
                                                if (e.key === "ArrowUp") {
                                                    e.preventDefault();
                                                    e.stopPropagation();
                                                    nudge(category, -1);
                                                } else if (e.key === "ArrowDown") {
                                                    e.preventDefault();
                                                    e.stopPropagation();
                                                    nudge(category, 1);
                                                }
                                            }}
                                        >
                                            <i class="ri-draggable" aria-hidden="true"></i>
                                        </button>
                                    {/if}
                                    <span
                                        class="category-indent"
                                        class:nested={category.depth > 0}
                                        style="--depth: {category.depth}"
                                    ></span>
                                    <!-- A leaf gets a spacer rather than a disabled
                                         chevron: an expander that opens onto
                                         nothing reads as broken, and `child_count`
                                         is on the row precisely so this can tell. -->
                                    {#if category.child_count > 0}
                                        <button
                                            type="button"
                                            class="btn circle sm transparent secondary"
                                            class:loading={busyRow === category.id}
                                            aria-expanded={expanded.has(category.id)}
                                            aria-label="{expanded.has(category.id)
                                                ? 'Collapse'
                                                : 'Expand'} {category.title}"
                                            onclick={(e) => (e.stopPropagation(), toggle(category))}
                                        >
                                            <i
                                                class={expanded.has(category.id)
                                                    ? "ri-arrow-down-s-line"
                                                    : "ri-arrow-right-s-line"}
                                                aria-hidden="true"
                                            ></i>
                                        </button>
                                    {:else}
                                        <span class="expand-spacer" aria-hidden="true"></span>
                                    {/if}
                                    <!-- The ancestry, then the name. The indent
                                         already says where a row sits, but it
                                         says it in pixels — you have to count
                                         them against the rows above, and a
                                         child scrolled away from its parent
                                         reads as a root. The path says it in
                                         words, the way the product editor does
                                         ("Bags / Backpacks"), and it stays true
                                         on a phone where the stacked card has
                                         no indent to read at all. Muted, so the
                                         name is still the thing you scan. -->
                                    {#if ancestryOf(category)}
                                        <span class="txt-hint txt-sm">{ancestryOf(category)} / </span>
                                    {/if}
                                    <span class="txt-bold">{category.title}</span>
                                    {#if category.child_count > 0}
                                        <span class="txt-hint txt-sm">
                                            {category.child_count}
                                        </span>
                                    {/if}
                                {/if}
                            </td>
                            <td class="col-type-text txt-hint txt-sm" data-name="Slug">
                                <span class="txt-ellipsis">{category.slug}</span>
                            </td>
                            <td class="col-field-type-number min-width" data-name="Products">
                                <!-- A real link, so it opens in a tab and reads
                                     its destination on hover. `category_id`
                                     matches the whole subtree, which is what
                                     somebody asking "what is under this" means. -->
                                {#if counted[category.id] === undefined}
                                    <span class="txt-hint txt-sm">…</span>
                                {:else if counted[category.id] === null}
                                    <span class="txt-hint txt-sm">—</span>
                                {:else}
                                    <a
                                        href="{base}/products?category_id={category.id}"
                                        class="txt-sm"
                                        class:txt-hint={counted[category.id] === 0}
                                        title="Products in {category.full_name || category.title}"
                                        onclick={(e) => e.stopPropagation()}
                                    >
                                        {counted[category.id]}
                                    </a>
                                {/if}
                            </td>
                            <td
                                class="col-field-type-number min-width txt-hint txt-sm"
                                data-name="Position"
                            >
                                {category.position ?? 0}
                            </td>
                            <td class="col-meta min-width">
                                <!-- Outside the writable gate: reading who
                                     changed a category is catalog.read, which
                                     is the right this whole screen is drawn
                                     under. -->
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    aria-label="Change history for {category.title}"
                                    title="Change history"
                                    onclick={(e) => openHistory(category, e)}
                                >
                                    <i class="ri-file-history-line" aria-hidden="true"></i>
                                </button>
                                {#if writable}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary"
                                        aria-label="Add a subcategory under {category.title}"
                                        title="Add a subcategory"
                                        onclick={(e) => addChild(category, e)}
                                    >
                                        <i class="ri-node-tree" aria-hidden="true"></i>
                                    </button>
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary row-delete"
                                        aria-label="Delete {category.title}"
                                        title="Delete"
                                        onclick={(e) => askDelete(category, e)}
                                    >
                                        <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                    </button>
                                {/if}
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if (loading || searchLoading) && !rows.length}
                        {#each Array(4) as _, i (i)}
                            <tr><td colspan={writable ? 6 : 5}><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {:else if !rows.length}
                        <tr>
                            <td colspan={writable ? 6 : 5} class="txt-hint txt-center p-base">
                                {#if search}
                                    No category matches “{search}”. The search reads the whole
                                    tree, not the branches you have opened.
                                {:else}
                                    No categories yet. A category is where a product sits in your
                                    taxonomy — one place, with a parent.
                                {/if}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>

        {#if writable}
            <BulkBar count={sel.count} noun="category" plural="categories" onclear={() => sel.clear()}>
                <!-- Two acts, and both are the per-row route this screen
                     already calls: the drawer's Parent field and the bin.
                     There is still no `active` on a category to switch.
                     Re-parenting is here because it is the one edit a tree
                     sized for fourteen thousand nodes makes in bulk —
                     reorganising a branch was forty drawer visits. -->
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={bulkBusy}
                    onclick={openMove}
                >
                    <i class="ri-drag-move-2-line" aria-hidden="true"></i>
                    <span class="txt">Move…</span>
                </button>
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
            {#if search}
                <Pager
                    meta={resultMeta}
                    loading={searchLoading}
                    noun="match"
                    plural="matches"
                    {perPage}
                    onpage={(n) => list.setPage(n)}
                    onperpage={(n) => list.set({ limit: n })}
                />
                <div class="flex-fill"></div>
            {:else}
                <span class="txt">
                    {total}
                    {pluralize(total, "category", "categories")}
                    {#if categories.length !== total}
                        <span class="txt-hint">· {categories.length} shown</span>
                    {/if}
                </span>
            {/if}
            <ThemeToggle />
        </footer>
    </div>
</div>

<Drawer
    open={editorOpen}
    title={editing ? editing.title : "New category"}
    size="sm"
    onclose={() => (editorOpen = false)}
>
    <form id="category-form" onsubmit={save}>
        <div class="field required" class:error={!!errors.title}>
            <label for="cat_title">Title</label>
            <input id="cat_title" type="text" autocomplete="off" bind:value={form.title} />
        </div>
        {#if errors.title}<div class="field-help error">{errors.title}</div>{/if}

        <div class="field m-t-sm">
            <label for="cat_parent">Parent</label>
            <CategoryPicker
                id="cat_parent"
                bind:value={form.parent_id}
                categories={parentOptions}
                remote={parentRemote}
                selectedCategory={editing?.parent_id ? { id: editing.parent_id } : null}
                placeholder="Top level"
            />
        </div>
        <div class="field-help">
            A category can hold products and subcategories at once. Moving one takes its
            subcategories with it.
        </div>

        <div class="fields m-t-sm">
            <div class="field">
                <label for="cat_slug">Slug</label>
                <input id="cat_slug" type="text" autocomplete="off" bind:value={form.slug} />
            </div>
            <div class="delimiter"></div>
            <div class="field" class:error={!!errors.position}>
                <label for="cat_position">Position</label>
                <input
                    id="cat_position"
                    type="number"
                    min="0"
                    step="1"
                    autocomplete="off"
                    placeholder={editing ? "" : "last"}
                    bind:value={form.position}
                />
            </div>
        </div>
        <div class="field-help">
            The slug is derived from the title when left empty. Position orders this category
            against its siblings — lowest first — which is the order a storefront draws its menu
            in. Leave it empty on a new category and it goes last.
        </div>
        {#if errors.position}<div class="field-help error">{errors.position}</div>{/if}

        <!--
            The fields products in this category are asked for. Shopify gets
            these from its own taxonomy; a store that built its own tree has to
            say what it wants asked, so this is where that is said. Inherited
            downward — a field on Bath & Body is asked of every soap under it.
        -->
        <h6 class="section-title">
            <i class="ri-list-settings-line" aria-hidden="true"></i>
            Category metafields
        </h6>

        {#if errors.attributes}<div class="field-help error">{errors.attributes}</div>{/if}

        {#each attributes as attr, i (i)}
            <div class="attr-row" class:m-t-sm={i > 0}>
                {#if attr.key}
                    <div class="field">
                        <span class="txt-sm txt-hint">Handle</span>
                        <div class="txt-code">{attr.key}</div>
                    </div>
                    <div class="field-help">
                        This is what answers already given are filed under, so it stays as
                        it is. A field that needs a different handle is a new field.
                    </div>
                {:else}
                    <div class="field">
                        <label for="attr-key-{i}">Handle</label>
                        <input
                            id="attr-key-{i}"
                            type="text"
                            autocomplete="off"
                            list="attr-handles"
                            placeholder={attr.label.trim() ? keyFor(attr.label) : "sleeve-length"}
                            bind:value={attr.key}
                            oninput={(e) => suggestHandles(e.currentTarget.value)}
                            onchange={() => handleChosen(attr)}
                        />
                    </div>
                {/if}
                <div class="field m-t-5">
                    <label for="attr-label-{i}">Field {i + 1}</label>
                    <input
                        id="attr-label-{i}"
                        type="text"
                        autocomplete="off"
                        placeholder="Age group"
                        bind:value={attr.label}
                    />
                </div>
                <div class="field m-t-5">
                    <label for="attr-choices-{i}">Choices</label>
                    <TokenInput
                        id="attr-choices-{i}"
                        bind:values={attr.choices}
                        emptyText="Type a choice and press Enter. None means the field takes free text."
                    />
                </div>
                <button
                    type="button"
                    class="btn sm transparent danger attr-remove"
                    onclick={() => removeAttribute(i)}
                >
                    <span class="txt">Remove</span>
                </button>
            </div>
        {/each}

        <datalist id="attr-handles">
            {#each handleSuggestions as entry (entry.handle)}
                <option value={entry.handle}>{entry.label}</option>
            {/each}
        </datalist>

        <button type="button" class="btn sm transparent m-t-sm" onclick={addAttribute}>
            <i class="ri-add-circle-line" aria-hidden="true"></i>
            <span class="txt">Add a metafield</span>
        </button>
        <div class="field-help">
            Products in this category — and in every category under it — are asked for these on
            their own page. Renaming one keeps the answers already given.
        </div>
        <div class="field-help">
            A handle that matches an entry in the
            <a href="{base}/settings/attributes">attribute dictionary</a> gets that entry's
            fixed list of values on the product page; one that does not is a free-text
            field. Leave it empty and it is derived from the label.
        </div>
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (editorOpen = false)}>
            <span class="txt">{writable ? "Cancel" : "Close"}</span>
        </button>
        <!-- The drawer is still reachable without catalog.write — reading a
             category's metafields is what catalog.read is for — so what goes is
             the button that writes them, not the drawer. -->
        {#if writable}
            <button
                type="submit"
                form="category-form"
                class="btn"
                class:loading={saving}
                disabled={saving}
            >
                <span class="txt">{editing ? "Save changes" : "Create category"}</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmOpen}
    title="Delete this category?"
    message={pendingDelete ? deleteMessage(pendingDelete) : ""}
    confirmLabel="Delete"
    danger
    onconfirm={doDelete}
/>

<!-- A second Confirm rather than a shared one with a mode flag: this message
     names a count and the other names a category, and one component asked to
     say both ends up saying neither. -->
<Confirm
    bind:open={bulkDeleteOpen}
    title="Delete {sel.count} {pluralize(sel.count, 'category', 'categories')}?"
    message={(pickedHolding.length
        ? `${pickedHolding.length} of these still hold subcategories or products. A branch selected together with everything beneath it is sent deepest first and should clear; anything else is refused by name, with the count. `
        : "") +
        "Products are never deleted with a category — they lose the place they were filed under."}
    confirmLabel="Delete"
    danger
    onconfirm={bulkDelete}
/>

<CategoryMove
    open={moveOpen}
    selected={moveRows}
    {categories}
    remote={parentRemote}
    onclose={() => (moveOpen = false)}
    ondone={() => (sel.clear(), refresh())}
/>

<RecordHistory
    open={historyOpen}
    kind="categories"
    id={historyFor?.id}
    label={historyFor?.full_name || historyFor?.title}
    onclose={() => (historyOpen = false)}
/>
{/if}
