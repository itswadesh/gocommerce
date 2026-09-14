<script>
    /**
     * One screen, drawn from a module's description of it.
     *
     * The panel is compiled once and embedded in the binary, so a module cannot
     * ship code into it. What it can ship is a *description* — a title, a list
     * endpoint, some columns, optionally a form — and this route is the single
     * renderer that turns any of those into a screen. The module contributes
     * data; the panel contributes the drawing, which is why a module screen
     * looks like every other screen and inherits the theme, the table
     * treatment and the drawer for free.
     *
     * What it cannot do is stated on the Go side and is worth repeating here:
     * this covers a list and a form behind it. A module wanting a chart or a
     * bespoke editor is not served by this and would need the panel to grow the
     * screen itself.
     *
     * Unknown column and field kinds fall back to text rather than throwing, so
     * a module built against a newer engine degrades into something readable
     * instead of a blank page.
     */
    import { page } from "$app/state";
    import { api } from "$lib/api.js";
    import { can } from "$lib/session.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { fieldText } from "$lib/fieldtext.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";

    const slug = $derived(page.params.slug);

    let screen = $state(null);
    let rows = $state([]);
    let loading = $state(true);
    let missing = $state(false);

    let open = $state(false);
    let editing = $state(null);
    let form = $state({});
    let saving = $state(false);
    let confirmOpen = $state(false);
    let pending = $state(null);

    const allowed = $derived(!screen || can(screen.right));
    const idKey = $derived(screen?.form?.id_key || "id");

    /* Descriptors are static, so they are fetched once per slug rather than
       alongside every refresh of the rows. */
    $effect(() => {
        const want = slug;
        if (!want) return;
        loading = true;
        missing = false;
        api.get("/api/admin/screens")
            .then((result) => {
                if (slug !== want) return;
                screen = (result.data ?? []).find((s) => s.slug === want) ?? null;
                // Absent means the module is not installed, or the operator's
                // role does not carry the right the screen names — the listing
                // filters on it. Either way the honest answer is that this
                // screen is not here, not an error.
                missing = !screen;
                if (screen) refresh();
                else loading = false;
            })
            .catch((err) => {
                if (slug !== want) return;
                toast.error(err);
                loading = false;
            });
    });

    async function refresh() {
        if (!screen) return;
        loading = true;
        try {
            const result = await api.get(screen.list.endpoint);
            rows = result.data ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /* Dots reach into nested objects, so a column can name "price.amount_minor"
       without the module flattening its own response first. */
    function valueAt(row, key) {
        return String(key)
            .split(".")
            .reduce((acc, part) => (acc == null ? acc : acc[part]), row);
    }

    function render(row, column) {
        const raw = valueAt(row, column.key);
        if (raw === null || raw === undefined || raw === "") return "—";
        switch (column.kind) {
            case "date":
                return formatDate(String(raw));
            case "relative":
                return relativeTime(String(raw));
            case "bool":
                return raw ? "Yes" : "No";
            default:
                return String(raw);
        }
    }

    function openRow(row) {
        editing = row ?? null;
        form = {};
        for (const field of screen.form?.fields ?? []) {
            form[field.key] = row ? (valueAt(row, field.key) ?? "") : "";
        }
        open = true;
    }

    function endpointFor(template, row) {
        return template.replace("{id}", encodeURIComponent(valueAt(row, idKey)));
    }

    async function save() {
        const body = {};
        for (const field of screen.form?.fields ?? []) {
            const value = form[field.key];
            if (field.required && !fieldText(value)) {
                toast.error(`${field.label} is required.`);
                return;
            }
            body[field.key] = field.kind === "number" ? Number(value) : value;
        }
        saving = true;
        try {
            if (editing && screen.form.update) {
                await api.patch(endpointFor(screen.form.update, editing), body);
            } else if (screen.form.create) {
                await api.post(screen.form.create, body);
            }
            open = false;
            await refresh();
            toast.success("Saved.");
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function doDelete() {
        if (!pending) return;
        try {
            await api.delete(endpointFor(screen.form.delete, pending));
            await refresh();
            toast.success("Deleted.");
        } catch (err) {
            toast.error(err);
        } finally {
            pending = null;
        }
    }

    const canCreate = $derived(!!screen?.form?.create);
    const canEdit = $derived(!!screen?.form?.update);
    const canDelete = $derived(!!screen?.form?.delete);
    const hasActions = $derived(canEdit || canDelete);
</script>

<svelte:head><title>{screen?.title ?? "Screen"} · GoCommerce</title></svelte:head>

<div class="page-content tw:bg-background tw:text-foreground">
    {#if missing}
        <nav class="breadcrumbs">
            <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Not here</div>
        </nav>
        <div class="field-help">
            No screen answers to <code>{slug}</code>. Either the module that provides it is not
            installed in this store, or your role does not carry the right it needs.
        </div>
    {:else if screen}
        <nav class="breadcrumbs">
            <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">{screen.title}</div>
        </nav>

        {#if !allowed}
            <NoAccess right={screen.right} />
        {:else}
            <div class="tw:flex tw:items-start tw:justify-between tw:gap-4 tw:mb-3">
                {#if screen.help}
                    <div class="field-help tw:m-0 tw:max-w-2xl">{screen.help}</div>
                {:else}
                    <span></span>
                {/if}
                {#if canCreate}
                    <button type="button" class="btn sm" onclick={() => openRow(null)}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New</span>
                    </button>
                {/if}
            </div>

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            {#each screen.list.columns as column (column.key)}
                                <th class={column.narrow ? "min-width" : ""}>{column.label}</th>
                            {/each}
                            {#if hasActions}
                                <th class="col-meta min-width"></th>
                            {/if}
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row, i (valueAt(row, idKey) ?? i)}
                            <tr>
                                {#each screen.list.columns as column (column.key)}
                                    <td class={column.narrow ? "min-width" : ""} data-name={column.label}>
                                        {#if column.kind === "code"}
                                            <code>{render(row, column)}</code>
                                        {:else if column.kind === "badge"}
                                            <span class="label">{render(row, column)}</span>
                                        {:else if column.kind === "relative" || column.kind === "date"}
                                            <span class="txt-hint txt-sm"
                                                  title={formatDate(String(valueAt(row, column.key) ?? ""))}>
                                                {render(row, column)}
                                            </span>
                                        {:else}
                                            {render(row, column)}
                                        {/if}
                                    </td>
                                {/each}
                                {#if hasActions}
                                    <td class="col-meta min-width">
                                        {#if canEdit}
                                            <button type="button" class="btn sm secondary transparent"
                                                    onclick={() => openRow(row)}>
                                                <span class="txt">Edit</span>
                                            </button>
                                        {/if}
                                        {#if canDelete}
                                            <button type="button" class="btn sm transparent row-delete"
                                                    title="Delete" aria-label="Delete"
                                                    onclick={() => { pending = row; confirmOpen = true; }}>
                                                <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                            </button>
                                        {/if}
                                    </td>
                                {/if}
                            </tr>
                        {/each}
                        {#if !loading && !rows.length}
                            <tr>
                                <td colspan={screen.list.columns.length + (hasActions ? 1 : 0)}
                                    class="txt-center txt-hint p-base">
                                    {screen.list.empty || "Nothing here yet."}
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                {rows.length}
                {rows.length === 1 ? "row" : "rows"} · provided by
                <code>{screen.module}</code>
            </footer>
        {/if}
    {/if}
</div>

{#if screen?.form?.fields?.length}
    <Drawer {open} size="sm" title={editing ? "Edit" : "New"} onclose={() => (open = false)}>
        {#each screen.form.fields as field, i (field.key)}
            <div class="field {i ? 'm-t-sm' : ''}">
                <label for="x-{field.key}">{field.label}</label>
                {#if field.kind === "textarea"}
                    <textarea id="x-{field.key}" bind:value={form[field.key]}></textarea>
                {:else if field.kind === "number"}
                    <input id="x-{field.key}" type="number" bind:value={form[field.key]} />
                {:else if field.kind === "toggle"}
                    <input id="x-{field.key}" type="checkbox" bind:checked={form[field.key]} />
                {:else if field.kind === "select"}
                    <Select id="x-{field.key}" value={form[field.key]}
                            onchange={(v) => (form[field.key] = v)}
                            options={(field.options ?? []).map((o) => ({ value: o.value, label: o.label }))} />
                {:else}
                    <input id="x-{field.key}" type="text" bind:value={form[field.key]} />
                {/if}
            </div>
            {#if field.help}
                <div class="field-help">{field.help}</div>
            {/if}
        {/each}

        {#snippet footer()}
            <button type="button" class="btn secondary" onclick={() => (open = false)}>
                <span class="txt">Cancel</span>
            </button>
            <button type="button" class="btn" disabled={saving} onclick={save}>
                <span class="txt">Save</span>
            </button>
        {/snippet}
    </Drawer>
{/if}

<Confirm
    bind:open={confirmOpen}
    title="Delete this row?"
    message={screen?.form?.delete_warning || "This cannot be undone."}
    confirmLabel="Delete"
    danger
    onconfirm={doDelete}
/>
