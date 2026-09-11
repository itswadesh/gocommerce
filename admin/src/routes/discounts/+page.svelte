<script>
    /**
     * Discounts: the rules, not what any order got.
     *
     * An operator here is doing one of two things — checking whether a running
     * promotion is being used, or writing the next one. So the listing leads
     * with the code and how far through its limit it is, and everything else is
     * the editor's problem.
     */
    import { api, query } from "$lib/api.js";
    import { fromMinor, toMinor, isValidMoney, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Select from "$lib/components/Select.svelte";
    import TargetPicker from "$lib/components/TargetPicker.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    const PER_PAGE = 50;

    let loading = $state(true);
    let discounts = $state([]);
    let meta = $state(null);
    let search = $state("");
    let draftSearch = $state("");

    let open = $state(false);
    let editing = $state(null);
    let form = $state(blank());
    let saving = $state(false);

    let confirmOpen = $state(false);
    let confirmConfig = $state({});

    /* Chip labels and the red flags, for ids the picker has not fetched. They
       are decoration only: the ids the drawer will send never come from the
       request that fills these. */
    let targetNames = $state(new Map());
    let targetsMissing = $state(new Set());

    /** The store's currency, for the amount field's prefix and for reading an
     *  amount back: a discount is written in the store's settlement currency,
     *  and how many decimals that has is exactly what fromMinor needs told. */
    const currency = $derived(settings.currency);

    $effect(() => {
        search;
        load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get("/api/admin/discounts" + query({ q: search, limit: PER_PAGE }));
            discounts = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
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
        open = true;
        // Names and red flags only. It may land after a save, and that is
        // harmless for exactly the reason above.
        if (d.target_ids?.length) loadTargetNames(d.id);
    }

    async function loadTargetNames(id) {
        try {
            const full = await api.get("/api/admin/discounts/" + id);
            targetNames = new Map((full.data?.targets ?? []).map((t) => [t.id, t.title]));
            targetsMissing = new Set(
                (full.data?.targets ?? []).filter((t) => t.missing).map((t) => t.id),
            );
        } catch {
            // A chip falling back to its id is a worse label, not a broken form.
        }
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
        body.usage_limit = form.usage_limit.trim() ? parseInt(form.usage_limit, 10) : null;
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

    /** What the rule takes off, in one phrase. */
    function value(d) {
        if (d.kind === "percentage") return `${d.value_bp / 100}%`;
        if (d.kind === "fixed") return `${currency} ${fromMinor(d.value_minor, currency)}`;
        return "Free shipping";
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
        search = draftSearch;
    }
</script>

<div class="page page-discounts">
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
                            onclick={() => ((draftSearch = ""), (search = ""))}
                        >
                            Clear
                        </button>
                    </div>
                {/if}
            </form>

            <div class="page-header-primary-btns">
                <button type="button" class="btn" onclick={openNew}>
                    <i class="ri-add-line" aria-hidden="true"></i>
                    <span class="txt">New discount</span>
                </button>
            </div>
        </header>

        <div class="page-table-wrapper">
            <table class="table">
                <thead class="sticky">
                    <tr>
                        <th class="col-field-name-id">Code</th>
                        <th class="col-field-type-text">Title</th>
                        <th class="col-field-type-text">Applies to</th>
                        <th class="col-field-type-select">State</th>
                        <th class="col-field-type-number min-width">Takes off</th>
                        <th class="col-field-type-number min-width">Uses</th>
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each discounts as d (d.id)}
                        {@const s = phase(d)}
                        <tr class="handle" onclick={() => openEdit(d)}>
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
                                <button
                                    type="button"
                                    class="btn circle sm transparent secondary row-delete"
                                    aria-label="Delete {d.code || d.title}"
                                    title="Delete"
                                    onclick={(e) => (e.stopPropagation(), askDelete(d))}
                                >
                                    <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                </button>
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !discounts.length}
                        {#each Array(4) as _, i (i)}
                            <tr><td colspan="7"><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {/if}

                    {#if !loading && !discounts.length}
                        <tr>
                            <td colspan="7" class="txt-center txt-hint p-base">
                                <div class="m-b-10">
                                    <i
                                        class="ri-price-tag-2-line"
                                        style="font-size: 32px"
                                        aria-hidden="true"
                                    ></i>
                                </div>
                                {#if search}
                                    Nothing matches that. <a
                                        href="#clear"
                                        onclick={(e) => (
                                            e.preventDefault(), (draftSearch = ""), (search = "")
                                        )}>Clear the search</a
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

        <footer class="page-footer">
            <span class="txt">
                {#if meta}
                    {meta.total}
                    {pluralize(meta.total, "discount")}
                {:else}
                    …
                {/if}
            </span>
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
                <Select
                    id="d-kind"
                    bind:value={form.kind}
                    options={[
                        { value: "percentage", label: "A percentage" },
                        { value: "fixed", label: "An amount" },
                    ]}
                />
            </div>
            <div class="delimiter"></div>
            {#if form.kind === "percentage"}
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
            {:else}
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

        <div class="field m-t-sm">
            <label for="d-scope">Applies to</label>
            <Select
                id="d-scope"
                bind:value={form.scope}
                options={[
                    { value: "order", label: "The whole basket" },
                    { value: "products", label: "Chosen products" },
                    { value: "collections", label: "Chosen collections" },
                    { value: "categories", label: "Chosen categories" },
                ]}
                onchange={() => (form.target_ids = [])}
            />
        </div>

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

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (open = false)}>
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form="discount-form"
            class="btn expanded"
            class:loading={saving}
            disabled={saving || (form.scope !== "order" && !form.target_ids.length)}
        >
            <span class="txt">{editing ? "Save changes" : "Create discount"}</span>
        </button>
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
