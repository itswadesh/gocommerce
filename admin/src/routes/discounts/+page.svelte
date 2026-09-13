<script>
    /**
     * Discounts: the rules, not what any order got.
     *
     * An operator here is doing one of two things — checking whether a running
     * promotion is being used, or writing the next one. So the listing leads
     * with the code and how far through its limit it is, and everything else is
     * the editor's problem.
     */
    import { base } from "$app/paths";
    import { api, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { selection } from "$lib/selection.svelte.js";
    import { runBulk } from "$lib/bulk.js";
    import { onNewShortcut } from "$lib/shortcuts.js";
    import BulkBar from "$lib/components/BulkBar.svelte";
    import { listState } from "$lib/liststate.svelte.js";
    import { readSort, cycleSort, sortQuery } from "$lib/listsort.js";
    import { can } from "$lib/session.svelte.js";
    import {
        formatDate,
        formatMoney,
        fromMinor,
        toMinor,
        isValidMoney,
        orderStatusClass,
        pluralize,
    } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import RecordHistory from "$lib/components/RecordHistory.svelte";
    import Select from "$lib/components/Select.svelte";
    import SortHeader from "$lib/components/SortHeader.svelte";
    import TargetPicker from "$lib/components/TargetPicker.svelte";
    import VariantSearch from "$lib/components/VariantSearch.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";
    import { fieldText } from "$lib/fieldtext.js";

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 50;

    /* The search, the ordering and the page live in the URL, and the window
       replaces the rows. Until now the screen fetched one page of fifty and
       showed whatever came back; with a sort it also chooses WHICH fifty, which
       is how an operator reaches the most-used discount in a store that has
       more than that. */
    const list = listState({ q: "", active: "", sort: "", order: "", page: 1, limit: PER_PAGE });
    const SORT_FIELDS = ["code", "title", "used_count", "created_at"];
    const perPage = $derived(list.params.limit);

    /* discounts.read is the nav's gate on this screen; discounts.write is what
       creating, editing and deactivating need. */
    const readable = $derived(can("discounts.read"));
    const writable = $derived(can("discounts.write"));

    /*
     * The rules an operator has picked. "Every expired code, switched off" was
     * one drawer at a time — open, untick, save, close, again — and the State
     * column is right there telling them which ones.
     *
     * Cleared by a filter change, kept across a page change: selection.svelte.js
     * states why, and `page` is deliberately absent below.
     */
    const sel = selection();

    $effect(() => {
        list.params.q;
        list.params.active;
        sel.clear();
    });

    let bulkBusy = $state(false);
    let bulkDeleteOpen = $state(false);

    const picked = $derived(sel.pick(discounts));
    const switchOnable = $derived(picked.filter((d) => !d.active));
    const switchOffable = $derived(picked.filter((d) => d.active));

    async function setActive(rows, active) {
        if (!rows.length) return;
        bulkBusy = true;
        try {
            await runBulk(rows, (d) => api.patch(`/api/admin/discounts/${d.id}`, { active }), {
                describe: active ? "Switched on" : "Switched off",
                noun: "discount",
                label: (d) => d.code || d.title,
            });
        } finally {
            bulkBusy = false;
        }
        sel.clear();
        await load();
    }

    async function bulkDelete() {
        bulkBusy = true;
        try {
            await runBulk(picked, (d) => api.delete(`/api/admin/discounts/${d.id}`), {
                describe: "Deleted",
                noun: "discount",
                label: (d) => d.code || d.title,
            });
        } finally {
            bulkBusy = false;
        }
        sel.clear();
        await load();
    }

    /* `n` opens the new-discount drawer, from anywhere on this screen. */
    $effect(() => {
        if (!writable) return;
        return onNewShortcut(openNew);
    });

    /* `active` is the switch, not the phase: a rule can be active and scheduled,
       active and expired, active and used up. The State column below says which
       of those it is, and this narrows the question to the half of the list that
       is switched on at all — which is what "is this still running" starts with,
       and the only half of it the engine can answer (DiscountQuery carries a
       search, this flag and a sort, and nothing about a window). */
    const ACTIVE_OPTIONS = [
        { value: "", label: "Any state" },
        { value: "1", label: "Active" },
        { value: "0", label: "Off" },
    ];

    let loading = $state(true);
    let discounts = $state([]);
    let meta = $state(null);

    const search = $derived(list.params.q);
    const activeFilter = $derived(list.params.active);
    const sort = $derived(readSort(list.params, SORT_FIELDS));
    let draftSearch = $state(list.params.q);

    /* Two header clicks leave two replies in flight, and the table would
       otherwise settle on whichever arrived last. */
    let reqId = 0;

    let open = $state(false);
    let editing = $state(null);
    let form = $state(blank());
    let saving = $state(false);

    let confirmOpen = $state(false);
    let confirmConfig = $state({});

    /* Who changed this rule, from the row it is about. The route is gated on
       discounts.read — the right this screen is already drawn under — rather
       than on store.operate, so "who moved this to 40% off" is answerable by
       the person running the promotion and not only by an owner. */
    let historyOpen = $state(false);
    let historyFor = $state(null);

    function openHistory(d, event) {
        event?.stopPropagation();
        historyFor = d;
        historyOpen = true;
    }

    /* Chip labels and the red flags, for ids the picker has not fetched. They
       are decoration only: the ids the drawer will send never come from the
       request that fills these. */
    let targetNames = $state(new Map());
    let targetsMissing = $state(new Set());

    /* What the rule has cost, from the detail route. The listing carries the
       rule alone — an aggregate per row would be a join per row — so the drawer
       fetches it on open. */
    let detail = $state(null);

    /* The dry run. Nothing is locked, nothing is claimed, and a rule that would
       not apply comes back as an answer rather than an error (D43). */
    let testBasket = $state("");
    let testEmail = $state("");
    let testing = $state(false);
    let result = $state(null);

    /* A scoped rule comes off the lines its targets reach, so one number cannot
       answer for it: PreviewID says so in as many words and refuses before it
       evaluates anything. So the form asks for lines instead — which is also the
       only way to try the case worth trying, a basket holding one thing the rule
       reaches and one it does not.

       It follows the SAVED scope rather than the form's, because the dry run
       runs against the rule as the server holds it. An unsaved change to the
       form says so below rather than quietly testing something else. */
    const scoped = $derived(!!editing?.scope && editing.scope !== "order");
    let testLines = $state([]);
    let lineSeq = 0;
    const testTotalMinor = $derived(
        testLines.reduce((sum, l) => sum + (toMinor(l.total, currency) ?? 0), 0),
    );

    /* Where it has been used. Paged inside the drawer and deliberately not in
       the URL: a drawer that writes to the address bar leaves a stale page
       behind when it closes and fights the screen's own parameters. */
    let usedRows = $state([]);
    let usedMeta = $state(null);
    let usedPage = $state(1);
    let usedLoading = $state(false);

    /** The store's currency, for the amount field's prefix and for reading an
     *  amount back: a discount is written in the store's settlement currency,
     *  and how many decimals that has is exactly what fromMinor needs told. */
    const currency = $derived(settings.currency);

    $effect(() => {
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
            const result = await api.get(
                "/api/admin/discounts" + list.query({ limit: perPage, ...sortQuery(sort) }),
            );
            if (mine !== reqId) return;
            discounts = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    function sortBy(field, firstDesc) {
        list.set(cycleSort(sort, field, firstDesc));
    }

    function blank() {
        return {
            code: "",
            title: "",
            kind: "percentage",
            scope: "order",
            target_ids: [],
            // Percentages are typed as people say them and stored as basis
            // points; the conversion is one multiplication in each direction and
            // it happens here rather than in anybody's head.
            percent: "10",
            amount: "",
            min_subtotal: "",
            starts_at: "",
            ends_at: "",
            usage_limit: "",
            once_per_email: false,
            active: true,
        };
    }

    function openNew() {
        editing = null;
        form = blank();
        targetNames = new Map();
        targetsMissing = new Set();
        open = true;
    }

    function openEdit(d) {
        editing = d;
        form = {
            code: d.code ?? "",
            title: d.title,
            kind: d.kind,
            scope: d.scope ?? "order",
            // Synchronously, from the row the listing already gave us. There is
            // no second fetch to race and no loading guard to get wrong: the ids
            // this drawer will send are the ids it was handed, so a save landing
            // first cannot PATCH `target_ids: []` over a live promotion.
            target_ids: [...(d.target_ids ?? [])],
            percent: d.value_bp ? String(d.value_bp / 100) : "",
            amount: d.value_minor ? fromMinor(d.value_minor, currency) : "",
            min_subtotal: d.min_subtotal_minor
                ? fromMinor(d.min_subtotal_minor, currency)
                : "",
            starts_at: d.starts_at ? d.starts_at.slice(0, 16) : "",
            ends_at: d.ends_at ? d.ends_at.slice(0, 16) : "",
            usage_limit: d.usage_limit ? String(d.usage_limit) : "",
            once_per_email: d.once_per_email,
            active: d.active,
        };
        targetNames = new Map();
        targetsMissing = new Set();
        detail = null;
        result = null;
        testBasket = "";
        testEmail = "";
        testLines = [];
        usedRows = [];
        usedMeta = null;
        usedPage = 1;
        open = true;
        // Names, red flags and the money figures. It may land after a save, and
        // that is harmless for exactly the reason above.
        loadDetail(d.id);
        if (can("orders.read")) loadUsed(d.id);
    }

    async function loadDetail(id) {
        try {
            // api.get already unwraps the {"data": …} envelope — a single
            // record arrives as itself, and only a LIST answers {data, meta}.
            // Reaching for `.data` a second time here was reading undefined, so
            // the money line never rendered and every target chip fell back to
            // its bare id.
            const full = await api.get("/api/admin/discounts/" + id);
            detail = full ?? null;
            targetNames = new Map((full?.targets ?? []).map((t) => [t.id, t.title]));
            targetsMissing = new Set(
                (full?.targets ?? []).filter((t) => t.missing).map((t) => t.id),
            );
        } catch {
            // A chip falling back to its id is a worse label, not a broken form,
            // and the money line below simply does not render.
        }
    }

    /* Gated on the right rather than on the response: the route needs both
       discounts.read and orders.read, so a re-cut role that lost the second must
       not be shown a section that will 403. */
    async function loadUsed(id) {
        usedLoading = true;
        try {
            const res = await api.get(
                `/api/admin/discounts/${id}/orders` + query({ page: usedPage, limit: 25 }),
            );
            usedRows = res.data ?? [];
            usedMeta = res.meta ?? null;
        } catch (err) {
            toast.error(err);
        } finally {
            usedLoading = false;
        }
    }

    function goUsedPage(n) {
        usedPage = n;
        loadUsed(editing.id);
    }

    /**
     * One line of a test basket, from the same picker the stock screens use:
     * what the engine needs is a product id and what that line is worth, and the
     * variant's own price is the honest first guess at the second.
     *
     * The same product twice is a legitimate basket — two different variants of
     * one shirt are two lines the rule reaches — so lines are keyed by their own
     * counter rather than by product.
     */
    function addTestLine(row) {
        const title = row.product?.title ?? "";
        testLines = [
            ...testLines,
            {
                key: ++lineSeq,
                product_id: row.product?.id ?? row.product_id,
                sku: row.sku,
                label: [title, row.label].filter(Boolean).join(" — ") || row.sku,
                total: fromMinor(row.price?.amount_minor ?? 0, currency),
            },
        ];
        result = null;
    }

    function removeTestLine(key) {
        testLines = testLines.filter((l) => l.key !== key);
        result = null;
    }

    /**
     * Try the rule against a basket.
     *
     * A refusal is the answer, not a failure: it renders as a warning with the
     * engine's own sentence. Only a broken request — an unreadable amount — is
     * stopped here.
     */
    async function preview() {
        // Either a total or lines, never both: the handler refuses a request
        // whose stated total could disagree with its own lines.
        let basket;
        if (scoped) {
            if (!testLines.length) {
                toast.error("Add a line to the test basket.");
                return;
            }
            const unreadable = testLines.find((l) => !isValidMoney(l.total));
            if (unreadable) {
                toast.error(`Enter what ${unreadable.sku} is worth in the basket.`);
                return;
            }
            basket = {
                lines: testLines.map((l) => ({
                    product_id: l.product_id,
                    total_minor: toMinor(l.total, currency),
                })),
            };
        } else {
            if (!isValidMoney(testBasket)) {
                toast.error("Enter a basket amount to try it against.");
                return;
            }
            basket = { subtotal_minor: toMinor(testBasket, currency) };
        }
        testing = true;
        result = null;
        try {
            const res = await api.post(`/api/admin/discounts/${editing.id}/preview`, {
                ...basket,
                email: testEmail.trim(),
            });
            // The preview answers with itself, not with a second envelope; the
            // extra `.data` meant the dry run ran and then showed nothing.
            result = res ?? null;
        } catch (err) {
            toast.error(err);
        } finally {
            testing = false;
        }
    }

    /**
     * Changing the kind changes what the rest of the form can mean.
     *
     * Free shipping cannot be scoped — the engine refuses it in as many words,
     * because shipping is not part of any line — so choosing it puts the scope
     * back to the whole basket rather than letting the operator fill in a
     * target list that will be rejected on save. Going the other way, a value
     * field that was emptied by a free-shipping rule gets a usable default
     * instead of a silent zero.
     */
    function pickKind(kind) {
        if (kind === "free_shipping") {
            form.scope = "order";
            form.target_ids = [];
            return;
        }
        if (kind === "percentage" && !String(form.percent).trim()) form.percent = "10";
    }

    /** The form as the engine wants it, or null when a money field cannot be read. */
    function payload() {
        const body = {
            code: form.code.trim(),
            title: form.title.trim(),
            kind: form.kind,
            once_per_email: form.once_per_email,
            active: form.active,
            value_bp: 0,
            value_minor: 0,
        };
        // Always present, so the same body serves POST and PATCH: the drawer
        // holds the whole set, an array is a replace, and `{scope: "order",
        // target_ids: []}` is the explicit clear the API documents. It is never
        // the refused combination, because order scope sends an empty array.
        body.scope = form.scope;
        body.target_ids = form.scope === "order" ? [] : [...form.target_ids];
        if (form.kind === "percentage") {
            body.value_bp = Math.round((parseFloat(form.percent) || 0) * 100);
        } else if (form.kind === "fixed") {
            // Refused here rather than sent. toMinor answers null on anything it
            // cannot read, and a null value_minor is a discount that takes
            // nothing off — which reads back as a saved rule that does nothing.
            if (!isValidMoney(form.amount)) {
                toast.error("Enter an amount to take off.");
                return null;
            }
            body.value_minor = toMinor(form.amount, currency);
        }
        // Empty legitimately means no minimum, so only a non-empty one is
        // guarded.
        if (form.min_subtotal.trim()) {
            if (!isValidMoney(form.min_subtotal)) {
                toast.error("Enter a minimum subtotal, or leave it empty.");
                return null;
            }
            body.min_subtotal_minor = toMinor(form.min_subtotal, currency);
        } else {
            body.min_subtotal_minor = null;
        }
        body.starts_at = form.starts_at ? new Date(form.starts_at).toISOString() : null;
        body.ends_at = form.ends_at ? new Date(form.ends_at).toISOString() : null;
        const usageLimit = fieldText(form.usage_limit);
        body.usage_limit = usageLimit ? parseInt(usageLimit, 10) : null;
        return body;
    }

    async function save(event) {
        event?.preventDefault();
        const body = payload();
        if (!body) return;
        saving = true;
        try {
            if (editing) {
                await api.patch(`/api/admin/discounts/${editing.id}`, body);
                toast.success("Discount updated");
            } else {
                await api.post("/api/admin/discounts", body);
                toast.success("Discount created");
            }
            open = false;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function askDelete(d) {
        confirmConfig = {
            title: `Delete ${d.code || d.title}?`,
            message:
                "Orders that used it keep what they were given and still read correctly. " +
                "The rule stops applying to anything new.",
            confirmLabel: "Delete",
            danger: true,
            run: async () => {
                try {
                    await api.delete(`/api/admin/discounts/${d.id}`);
                    toast.success("Discount deleted");
                    await load();
                } catch (err) {
                    toast.error(err);
                }
            },
        };
        confirmOpen = true;
    }

    /**
     * What the rule takes off, in one phrase.
     *
     * Every kind is named, and an unrecognised one says so rather than
     * borrowing free shipping's label: this used to return "Free shipping" as
     * the fall-through, so a kind this panel had not been taught would have
     * been described as the one thing it definitely was not.
     */
    function value(d) {
        if (d.kind === "percentage") return `${d.value_bp / 100}%`;
        if (d.kind === "fixed") return `${currency} ${fromMinor(d.value_minor, currency)}`;
        if (d.kind === "free_shipping") return "Free shipping";
        return d.kind || "—";
    }

    /**
     * How far through its limit a discount is. A promotion with no limit says
     * how many times it has been used, because that is still the question.
     */
    function uses(d) {
        return d.usage_limit ? `${d.used_count} / ${d.usage_limit}` : String(d.used_count);
    }

    /**
     * What the rule comes off, in one phrase.
     *
     * The plural is the scope word itself rather than `pluralize`, which would
     * make "categories" into "categorys": the scope is already the plural noun,
     * and only the singular has to be spelled out.
     */
    function applies(d) {
        if (!d.scope || d.scope === "order") return null;
        const count = d.target_ids?.length ?? 0;
        return `${count} ${count === 1 ? kindNoun(d.scope) : d.scope}`;
    }

    function kindNoun(scope) {
        return (
            { products: "product", collections: "collection", categories: "category" }[scope] ??
            "target"
        );
    }

    /**
     * Whether the rule is live *now*, which is not the same as `active`: a
     * scheduled discount is active and not yet running, and an expired one is
     * active and finished. The listing says which.
     *
     * A rule that CANNOT apply is a more urgent fact than whether it is
     * scheduled or expired, so it is asked first. Such a rule cannot be created
     * through this panel or through the API any more, but a store that already
     * holds one was being shown a green `live` chip for a promotion refused at
     * the till — the second half of the same gap.
     */
    function phase(d) {
        if (!d.active) return { label: "off", cls: "" };
        if (d.scope && d.scope !== "order" && !(d.target_ids ?? []).length) {
            return { label: "no targets", cls: "danger" };
        }
        const now = Date.now();
        if (d.starts_at && new Date(d.starts_at).getTime() > now) {
            return { label: "scheduled", cls: "info" };
        }
        if (d.ends_at && new Date(d.ends_at).getTime() <= now) {
            return { label: "expired", cls: "" };
        }
        if (d.usage_limit && d.used_count >= d.usage_limit) {
            return { label: "used up", cls: "warning" };
        }
        return { label: "live", cls: "success" };
    }

    function submitSearch(e) {
        e.preventDefault();
        list.set({ q: draftSearch });
    }

    function clearFilters() {
        draftSearch = "";
        list.set({ q: "", active: "" });
    }
</script>

<svelte:head><title>Discounts · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="discounts.read" what="discounts" />
{:else}

<div class="page page-discounts shopify-skin">
    <div class="page-content full-height">
        <header class="page-header">
            <nav class="breadcrumbs"><div>Discounts</div></nav>

            <form class="fields searchbar" onsubmit={submitSearch}>
                <div class="field">
                    <input
                        type="text"
                        class="p-l-20"
                        placeholder="Search by code or title"
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
                            onclick={() => ((draftSearch = ""), list.set({ q: "" }))}
                        >
                            Clear
                        </button>
                    </div>
                {/if}
            </form>

            <div class="page-header-primary-btns">
                <!-- In the right-hand group, as the orders screen's filters
                     are: a bare `.field` in the header is `width: 100%` and
                     takes a line to itself. -->
                <div class="field">
                    <Select
                        id="d-active-filter"
                        ariaLabel="State"
                        value={activeFilter}
                        options={ACTIVE_OPTIONS}
                        onchange={(v) => list.set({ active: v })}
                    />
                </div>

                {#if writable}
                    <button type="button" class="btn" onclick={openNew}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New discount</span>
                    </button>
                {/if}
            </div>
        </header>

        <div class="page-table-wrapper">
            <table class="table">
                <thead class="sticky">
                    <tr>
                        {#if writable}
                            <th class="col-bulk-select min-width">
                                <div class="field">
                                    <input
                                        id="select-all-discounts"
                                        type="checkbox"
                                        checked={sel.allSelected(discounts)}
                                        onchange={() => sel.toggleAll(discounts)}
                                    />
                                    <label
                                        for="select-all-discounts"
                                        aria-label="Select every discount on this page"
                                    ></label>
                                </div>
                            </th>
                        {/if}
                        <SortHeader
                            field="code"
                            label="Code"
                            class="col-field-name-id"
                            {sort}
                            onsort={sortBy}
                        />
                        <SortHeader
                            field="title"
                            label="Title"
                            class="col-field-type-text"
                            {sort}
                            onsort={sortBy}
                        />
                        <!-- Three headers that deliberately do not sort. Applies
                             to is a set of targets; State renders phase(d), a
                             derivation over active, starts_at, ends_at,
                             usage_limit and used_count that no single column
                             orders; and Takes off is basis points on one row and
                             minor units on the next, so one ordering would put
                             10% next to $10 as if they were the same number. -->
                        <th class="col-field-type-text">Applies to</th>
                        <th class="col-field-type-select">State</th>
                        <th class="col-field-type-number min-width">Takes off</th>
                        <SortHeader
                            field="used_count"
                            label="Uses"
                            class="col-field-type-number min-width"
                            firstDesc
                            {sort}
                            onsort={sortBy}
                        />
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each discounts as d (d.id)}
                        {@const s = phase(d)}
                        <tr
                            class="handle"
                            tabindex="0"
                            onclick={() => openEdit(d)}
                            onkeydown={(e) => rowKey(e, () => openEdit(d))}
                        >
                            {#if writable}
                                <td
                                    class="col-bulk-select min-width"
                                    onclick={(e) => e.stopPropagation()}
                                >
                                    <div class="field">
                                        <input
                                            id="select-discount-{d.id}"
                                            type="checkbox"
                                            checked={sel.has(d.id)}
                                            onchange={() => sel.toggle(d.id)}
                                        />
                                        <label
                                            for="select-discount-{d.id}"
                                            aria-label="Select {d.code || d.title}"
                                        ></label>
                                    </div>
                                </td>
                            {/if}
                            <td class="col-field-name-id" data-name="Code">
                                {#if d.code}
                                    <span class="txt-bold txt-code">{d.code}</span>
                                {:else}
                                    <span class="txt-hint txt-sm">Automatic</span>
                                {/if}
                            </td>
                            <td class="col-field-type-text" data-name="Title">
                                <span class="txt-ellipsis">{d.title}</span>
                            </td>
                            <td class="col-field-type-text" data-name="Applies to">
                                {#if applies(d)}
                                    <span class="txt-ellipsis">{applies(d)}</span>
                                {:else}
                                    <span class="txt-hint txt-sm">Whole basket</span>
                                {/if}
                            </td>
                            <td class="col-field-type-select" data-name="State">
                                <span class="label {s.cls}">{s.label}</span>
                            </td>
                            <td class="col-field-type-number min-width" data-name="Takes off">
                                {value(d)}
                            </td>
                            <td class="col-field-type-number min-width" data-name="Uses">
                                {uses(d)}
                            </td>
                            <td class="col-meta min-width">
                                <!-- Outside the writable gate: reading what was
                                     done to a rule is discounts.read, which is
                                     the right this screen is drawn under. -->
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary"
                                    aria-label="Change history for {d.code || d.title}"
                                    title="Change history"
                                    onclick={(e) => openHistory(d, e)}
                                >
                                    <i class="ri-file-history-line" aria-hidden="true"></i>
                                </button>
                                {#if writable}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary row-delete"
                                        aria-label="Delete {d.code || d.title}"
                                        title="Delete"
                                        onclick={(e) => (e.stopPropagation(), askDelete(d))}
                                    >
                                        <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                    </button>
                                {/if}
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !discounts.length}
                        {#each Array(4) as _, i (i)}
                            <tr>
                                <td colspan={writable ? 8 : 7}>
                                    <span class="skeleton-loader"></span>
                                </td>
                            </tr>
                        {/each}
                    {/if}

                    {#if !loading && !discounts.length}
                        <tr>
                            <td colspan={writable ? 8 : 7} class="txt-center txt-hint p-base">
                                <div class="m-b-10">
                                    <i
                                        class="ri-price-tag-2-line"
                                        style="font-size: 32px"
                                        aria-hidden="true"
                                    ></i>
                                </div>
                                {#if search || activeFilter}
                                    Nothing matches {search ? "that search" : "that state"}{search &&
                                    activeFilter
                                        ? " and state"
                                        : ""}.
                                    <a
                                        href="#clear"
                                        onclick={(e) => (e.preventDefault(), clearFilters())}
                                        >Clear the filters</a
                                    >.
                                {:else}
                                    No discounts yet. Create one to take money off a basket.
                                {/if}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>

        {#if writable}
            <BulkBar count={sel.count} noun="discount" onclear={() => sel.clear()}>
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={bulkBusy || !switchOnable.length}
                    title="A rule that is already on is left alone"
                    onclick={() => setActive(switchOnable, true)}
                >
                    <i class="ri-toggle-line" aria-hidden="true"></i>
                    <span class="txt">Switch on ({switchOnable.length})</span>
                </button>
                <button
                    type="button"
                    class="btn sm secondary"
                    disabled={bulkBusy || !switchOffable.length}
                    title="A rule that is already off is left alone"
                    onclick={() => setActive(switchOffable, false)}
                >
                    <i class="ri-toggle-fill" aria-hidden="true"></i>
                    <span class="txt">Switch off ({switchOffable.length})</span>
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

        <footer class="page-footer">
            <Pager
                {meta}
                {loading}
                noun="discount"
                {perPage}
                onpage={(n) => list.setPage(n)}
                onperpage={(n) => list.set({ limit: n })}
            />
            <div class="flex-fill"></div>
            <ThemeToggle />
        </footer>
    </div>
</div>

<Drawer
    {open}
    size="popup"
    title={editing ? "Edit discount" : "New discount"}
    onclose={() => (open = false)}
>
    <form id="discount-form" onsubmit={save}>
        <div class="fields">
            <div class="field">
                <label for="d-code">Code</label>
                <input
                    id="d-code"
                    type="text"
                    bind:value={form.code}
                    placeholder="SPRING24"
                />
            </div>
            <div class="delimiter"></div>
            <div class="field required">
                <label for="d-title">Title</label>
                <input id="d-title" type="text" bind:value={form.title} />
            </div>
        </div>
        <div class="field-help">
            The code is what a shopper types, matched whatever case they type it in. Leave it empty
            for a discount that applies on its own — those are stored but not applied yet.
        </div>

        <div class="fields m-t-sm">
            <div class="field">
                <label for="d-kind">Takes off</label>
                <!-- All three the engine has. The third was missing, so a
                     free-shipping rule made over the API opened here with an
                     empty picker above an Amount box that did not apply to
                     it. -->
                <Select
                    id="d-kind"
                    bind:value={form.kind}
                    options={[
                        { value: "percentage", label: "A percentage" },
                        { value: "fixed", label: "An amount" },
                        { value: "free_shipping", label: "Free shipping" },
                    ]}
                    onchange={pickKind}
                />
            </div>
            {#if form.kind === "percentage"}
                <div class="delimiter"></div>
                <div class="field">
                    <label for="d-percent">Percent</label>
                    <input
                        id="d-percent"
                        type="number"
                        min="0.01"
                        max="100"
                        step="0.01"
                        bind:value={form.percent}
                    />
                </div>
            {:else if form.kind === "fixed"}
                <div class="delimiter"></div>
                <div class="field">
                    <label for="d-amount">Amount ({currency})</label>
                    <input
                        id="d-amount"
                        type="text"
                        inputmode="decimal"
                        bind:value={form.amount}
                    />
                </div>
            {/if}
        </div>
        {#if form.kind === "free_shipping"}
            <div class="field-help">
                Free shipping carries no value of its own: the delivery charge comes off in
                full, and it comes off the shipping rather than off any line.
            </div>
        {/if}

        <div class="field m-t-sm">
            <label for="d-scope">Applies to</label>
            <Select
                id="d-scope"
                bind:value={form.scope}
                disabled={form.kind === "free_shipping"}
                options={[
                    { value: "order", label: "The whole basket" },
                    { value: "products", label: "Chosen products" },
                    { value: "collections", label: "Chosen collections" },
                    { value: "categories", label: "Chosen categories" },
                ]}
                onchange={() => (form.target_ids = [])}
            />
        </div>
        {#if form.kind === "free_shipping"}
            <div class="field-help">
                Not narrowable: shipping is not part of any line, so there is nothing for a
                product or a collection to match. A minimum basket is how a free-shipping
                offer is limited.
            </div>
        {/if}

        {#if form.scope !== "order"}
            <div class="field m-t-sm">
                <label for="d-targets">Which {form.scope}</label>
                <TargetPicker
                    id="d-targets"
                    kind={form.scope}
                    bind:value={form.target_ids}
                    names={targetNames}
                    missing={targetsMissing}
                />
            </div>
            <div class="field-help">
                The discount comes off only the lines that match. A percentage is taken on those
                lines; an amount is capped at what they are worth. A basket with none of them is
                refused the code.{#if form.scope === "categories"}
                    Picking a category reaches everything filed beneath it.{/if}{#if form.scope === "collections"}
                    The list shows the first 200 collections.{/if}
            </div>
            {#if !form.target_ids.length}
                <div class="field-help txt-danger">
                    Pick at least one, or set this back to the whole basket.
                </div>
            {/if}
        {/if}

        <div class="field m-t-sm">
            <label for="d-min">Minimum basket ({currency})</label>
            <input
                id="d-min"
                type="text"
                inputmode="decimal"
                bind:value={form.min_subtotal}
                placeholder="No minimum"
            />
        </div>
        <div class="field-help">
            The minimum is measured on the whole basket, not just the lines this applies to.
        </div>

        <div class="fields m-t-sm">
            <div class="field">
                <label for="d-starts">Starts</label>
                <input id="d-starts" type="datetime-local" bind:value={form.starts_at} />
            </div>
            <div class="delimiter"></div>
            <div class="field">
                <label for="d-ends">Ends</label>
                <input id="d-ends" type="datetime-local" bind:value={form.ends_at} />
            </div>
        </div>
        <div class="field-help">Leave either empty for a discount with no window on that side.</div>

        <div class="field m-t-sm">
            <label for="d-limit">Total uses</label>
            <input
                id="d-limit"
                type="number"
                min="1"
                bind:value={form.usage_limit}
                placeholder="No limit"
            />
        </div>
        {#if editing}
            <div class="field-help">
                Used {editing.used_count}
                {pluralize(editing.used_count, "time")} so far. That count is what happened and
                cannot be edited.
            </div>
        {/if}

        <div class="field m-t-sm">
            <input id="d-once" type="checkbox" bind:checked={form.once_per_email} />
            <label for="d-once">Once per email address</label>
        </div>
        <div class="field-help">
            A deterrent rather than a control: this shop has no customer accounts, so a second
            address defeats it.
        </div>

        <div class="field m-t-sm">
            <input id="d-active" type="checkbox" bind:checked={form.active} />
            <label for="d-active">Active</label>
        </div>
    </form>

    <!--
        Both sections sit after </form> and inside the drawer, so the submit
        footer still targets the form by id and no form is nested in another.
    -->
    {#if editing}
        <h6 class="section-title m-t-base">Try it</h6>

        {#if scoped}
            <div class="field-help m-b-sm">
                This rule comes off {applies(editing)}, so it is tried against a basket rather
                than a total — a number alone cannot say which lines it reaches. Add what a
                shopper would have, including something the rule does <em>not</em> cover: that is
                the case most likely to be wrong.
            </div>

            <VariantSearch
                onpick={addTestLine}
                placeholder="Add a product to the test basket"
            />

            {#each testLines as line (line.key)}
                <!-- A row of fields rather than a table: the drawer is narrow,
                     and `.fields` is the shape that already fits it — the name
                     yields and the money box keeps its width. -->
                <div class="fields m-t-5">
                    <div class="field addon">
                        <span class="txt-ellipsis" title={line.sku}>{line.label}</span>
                    </div>
                    <div class="delimiter"></div>
                    <div class="field">
                        <input
                            type="text"
                            inputmode="decimal"
                            aria-label="What {line.sku} is worth in the basket"
                            bind:value={line.total}
                            oninput={() => (result = null)}
                        />
                    </div>
                    <div class="field addon p-r-5">
                        <button
                            type="button"
                            class="btn circle sm transparent secondary"
                            aria-label="Remove {line.sku}"
                            title="Remove"
                            onclick={() => removeTestLine(line.key)}
                        >
                            <i class="ri-close-line" aria-hidden="true"></i>
                        </button>
                    </div>
                </div>
            {/each}

            {#if testLines.length}
                <div class="field-help">
                    Basket of {currency}
                    {fromMinor(testTotalMinor, currency)} across {testLines.length}
                    {pluralize(testLines.length, "line")}. Each box is what that line is worth,
                    in {currency}.
                </div>
            {/if}

            <div class="field m-t-sm">
                <label for="d-test-email">Email</label>
                <input
                    id="d-test-email"
                    type="email"
                    bind:value={testEmail}
                    placeholder="Optional"
                />
            </div>
        {:else}
            <div class="fields">
                <div class="field">
                    <label for="d-test-basket">Basket ({currency})</label>
                    <input
                        id="d-test-basket"
                        type="text"
                        inputmode="decimal"
                        bind:value={testBasket}
                        placeholder="100.00"
                    />
                </div>
                <div class="delimiter"></div>
                <div class="field">
                    <label for="d-test-email">Email</label>
                    <input
                        id="d-test-email"
                        type="email"
                        bind:value={testEmail}
                        placeholder="Optional"
                    />
                </div>
            </div>
        {/if}

        {#if form.scope !== (editing.scope ?? "order") || form.kind !== editing.kind}
            <!-- The dry run is against the rule the server holds, by id. Saying
                 so is the difference between a test and a misleading one. -->
            <div class="field-help">
                Tries the rule as it is saved — the changes above are not part of it until they
                are saved.
            </div>
        {/if}

        <div class="inline-flex m-t-5">
            <button
                type="button"
                class="btn secondary"
                class:loading={testing}
                disabled={testing}
                onclick={preview}
            >
                <i class="ri-play-line" aria-hidden="true"></i>
                <span class="txt">Preview</span>
            </button>
        </div>

        {#if result}
            <!-- Branched on the shape rather than on one number: a free-shipping
                 rule applies with an amount of zero, and "takes off 0.00" is the
                 wrong sentence for a rule that is working. -->
            {#if result.applies && result.applied?.free_shipping}
                <div class="alert success m-t-sm"><p>Shipping is free.</p></div>
            {:else if result.applies}
                <div class="alert success m-t-sm">
                    <p>Takes off {currency} {fromMinor(result.applied?.amount_minor, currency)}.</p>
                </div>
            {:else}
                <div class="alert warning m-t-sm"><p>{result.reason}</p></div>
            {/if}
        {/if}

        {#if editing.min_subtotal_minor}
            <!-- A fact about the rule on screen, not a reading of the result —
                 which is what keeps it right. The engine's own refusal prints
                 raw minor units, so the formatted minimum beside the form is
                 what makes that sentence self-explanatory. -->
            <div class="field-help">
                Needs a basket of at least {currency}
                {fromMinor(editing.min_subtotal_minor, currency)}.
            </div>
        {/if}
        <div class="field-help">
            Nothing is used up by trying it — the count only moves at checkout.
        </div>

        {#if can("orders.read")}
            <h6 class="section-title m-t-base">Where it has been used</h6>
            {#if detail}
                <div>
                    <strong>{formatMoney(detail.redeemed_total)}</strong>
                    given away across {detail.redeemed_orders}
                    live {pluralize(detail.redeemed_orders, "order")}{#if detail.redeemed_other_currency_orders}, and
                        {detail.redeemed_other_currency_orders} more on orders in another currency,
                        which the total cannot add to this one{/if}.
                </div>
                <div class="field-help">
                    This list shows all {usedMeta?.total ?? detail.redeemed_orders}, cancelled ones
                    included and greyed; the total above leaves them out. “Used {editing.used_count}
                    {pluralize(editing.used_count, "time")}” counts every checkout that claimed the
                    code, cancellations and all — the three numbers answer three different questions.
                </div>
            {/if}

            <div class="page-table-wrapper m-t-sm">
                <table class="table">
                    <thead>
                        <tr>
                            <th>Order</th>
                            <th>When</th>
                            <th>Email</th>
                            <th>Status</th>
                            <th class="txt-right">Took off</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each usedRows as r (r.order_id)}
                            <tr class:txt-hint={r.status === "cancelled"}>
                                <!-- By email, because that is the only filter
                                     the orders screen takes from its address
                                     bar. Linking by number would advertise a
                                     jump the list cannot make, and land the
                                     operator on an unfiltered page. -->
                                <td>
                                    <a
                                        href="{base}/orders?email={encodeURIComponent(r.email)}"
                                        class="txt-bold"
                                        title="Open this buyer's orders"
                                    >
                                        {r.number}
                                    </a>
                                </td>
                                <td class="txt-hint txt-sm">{formatDate(r.created_at)}</td>
                                <td class="txt-sm">{r.email}</td>
                                <td>
                                    <span class="label {orderStatusClass(r.status)}">
                                        {r.status}
                                    </span>
                                </td>
                                <td class="txt-right txt-bold">{formatMoney(r.amount)}</td>
                            </tr>
                        {/each}

                        {#if usedLoading && !usedRows.length}
                            {#each Array(2) as _, i (i)}
                                <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {/if}

                        {#if !usedLoading && !usedRows.length}
                            <tr>
                                <td colspan="5" class="txt-center txt-hint p-base">
                                    Nobody has used this yet.
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>

            {#if usedMeta && usedMeta.total_pages > 1}
                <div class="inline-flex m-t-5">
                    <Pager
                        meta={usedMeta}
                        loading={usedLoading}
                        noun="order"
                        onpage={goUsedPage}
                    />
                </div>
            {/if}
        {/if}
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (open = false)}>
            <span class="txt">{writable ? "Cancel" : "Close"}</span>
        </button>
        <!-- A row opens this drawer for anyone who may read a discount, because
             reading the rule is the point of opening it. Offering Save to
             somebody the engine will refuse is the thing that wastes their
             time, so the button goes rather than greys. -->
        {#if writable}
            <button
                type="submit"
                form="discount-form"
                class="btn expanded"
                class:loading={saving}
                disabled={saving || (form.scope !== "order" && !form.target_ids.length)}
            >
                <span class="txt">{editing ? "Save changes" : "Create discount"}</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmOpen}
    title={confirmConfig.title}
    message={confirmConfig.message}
    confirmLabel={confirmConfig.confirmLabel}
    danger={confirmConfig.danger}
    onconfirm={() => confirmConfig.run?.()}
/>

<!-- A second Confirm rather than a mode flag on the first: this message names a
     count and the other names a code, and one component asked to say both ends
     up saying neither. -->
<Confirm
    bind:open={bulkDeleteOpen}
    title="Delete {sel.count} {sel.count === 1 ? 'discount' : 'discounts'}?"
    message="Orders that already used one keep their own record of it, so history stays readable. Anything the engine refuses is reported row by row."
    confirmLabel="Delete"
    danger
    onconfirm={bulkDelete}
/>

<RecordHistory
    open={historyOpen}
    kind="discounts"
    id={historyFor?.id}
    label={historyFor?.code || historyFor?.title}
    onclose={() => (historyOpen = false)}
/>
{/if}
