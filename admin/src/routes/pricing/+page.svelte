<script>
    /**
     * Price lists and the customer groups they aim at.
     *
     * Two tables on one screen rather than two screens, because neither is
     * usable without the other: a list aimed at nobody prices nothing, and a
     * group nothing is priced for is an address book. Keeping them together is
     * what makes "who gets the trade price" one question.
     *
     * The engine resolves a price by taking the most specific quantity break,
     * then the higher-priority list, then the cheaper of the two. That order is
     * stated on the screen rather than left in the API docs: it is the thing an
     * operator gets wrong, and the moment they get it wrong is while they are
     * looking at this table.
     */
    import { api } from "$lib/api.js";
    import { can } from "$lib/session.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { fromMinor, toMinor, isValidMoney } from "$lib/format.js";
    import { fieldText } from "$lib/fieldtext.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";

    const readable = $derived(can("discounts.read"));
    const writable = $derived(can("discounts.write"));
    /* Membership is customer data and takes its own right, so the panel hides
       the addresses from somebody who may price but may not read the customer
       list — the same split the routes make. */
    const canSeeMembers = $derived(can("customers.read"));

    let loading = $state(true);
    let lists = $state([]);
    let groups = $state([]);
    let currency = $state("USD");

    let listOpen = $state(false);
    let groupOpen = $state(false);
    let editing = $state(null);
    let editingGroup = $state(null);
    let form = $state(blankList());
    let groupForm = $state({ code: "", name: "" });
    let saving = $state(false);

    let confirmOpen = $state(false);
    let pending = $state(null);

    function blankList() {
        return { name: "", group_id: 0, priority: 0, active: true, starts_at: "", ends_at: "" };
    }

    async function load() {
        if (!readable) {
            loading = false;
            return;
        }
        loading = true;
        try {
            const [l, g, settings] = await Promise.all([
                api.get("/api/admin/price-lists"),
                api.get("/api/admin/customer-groups"),
                api.get("/api/admin/settings"),
            ]);
            lists = l.data ?? [];
            groups = g.data ?? [];
            currency = settings.data?.currency ?? currency;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }
    $effect(() => {
        load();
    });

    const groupName = (id) => groups.find((g) => g.id === id)?.name ?? `#${id}`;

    /* A window read the way an operator would say it, so the column answers
       "is this live" without arithmetic. */
    function windowOf(list) {
        const from = list.starts_at ? new Date(list.starts_at).toLocaleDateString() : "";
        const to = list.ends_at ? new Date(list.ends_at).toLocaleDateString() : "";
        if (from && to) return `${from} – ${to}`;
        if (from) return `from ${from}`;
        if (to) return `until ${to}`;
        return "always";
    }

    function openList(list) {
        editing = list ?? null;
        form = list
            ? {
                  name: list.name,
                  group_id: list.group_id ?? 0,
                  priority: list.priority,
                  active: list.active,
                  // datetime-local wants the seconds and the zone gone.
                  starts_at: list.starts_at ? list.starts_at.slice(0, 16) : "",
                  ends_at: list.ends_at ? list.ends_at.slice(0, 16) : "",
              }
            : blankList();
        listOpen = true;
    }

    async function saveList() {
        const name = fieldText(form.name);
        if (!name) {
            toast.error("A price list needs a name.");
            return;
        }
        saving = true;
        try {
            const body = {
                name,
                // 0 is the picker's "everybody"; the API wants null for it,
                // which is what makes a list reach anonymous carts.
                group_id: form.group_id || null,
                priority: parseInt(fieldText(form.priority), 10) || 0,
                active: form.active,
                starts_at: form.starts_at ? new Date(form.starts_at).toISOString() : null,
                ends_at: form.ends_at ? new Date(form.ends_at).toISOString() : null,
            };
            if (editing) await api.patch(`/api/admin/price-lists/${editing.id}`, body);
            else await api.post("/api/admin/price-lists", body);
            listOpen = false;
            await load();
            toast.success(editing ? "Price list saved." : "Price list created.");
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function saveGroup() {
        const code = fieldText(groupForm.code);
        const name = fieldText(groupForm.name);
        if (!code || !name) {
            toast.error("A group needs a code and a name.");
            return;
        }
        saving = true;
        try {
            if (editingGroup) {
                await api.patch(`/api/admin/customer-groups/${editingGroup.id}`, { code, name });
            } else {
                await api.post("/api/admin/customer-groups", { code, name });
            }
            groupOpen = false;
            await load();
            toast.success(editingGroup ? "Group saved." : "Group created.");
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function askDelete(kind, row) {
        pending = { kind, row };
        confirmOpen = true;
    }

    async function doDelete() {
        if (!pending) return;
        try {
            const path =
                pending.kind === "list"
                    ? `/api/admin/price-lists/${pending.row.id}`
                    : `/api/admin/customer-groups/${pending.row.id}`;
            await api.delete(path);
            await load();
            toast.success("Deleted.");
        } catch (err) {
            toast.error(err);
        } finally {
            pending = null;
        }
    }

    const groupOptions = $derived([
        { value: 0, label: "Everyone", note: "Reaches anonymous carts too" },
        ...groups.map((g) => ({ value: g.id, label: g.name, count: g.members })),
    ]);
</script>

<svelte:head><title>Price lists · GoCommerce</title></svelte:head>

<div class="page-content tw:bg-background tw:text-foreground">
    <nav class="breadcrumbs">
        <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Price lists</div>
    </nav>

    {#if !readable}
        <NoAccess right="discounts.read" />
    {:else}
        <div class="field-help tw:mb-4">
            A price list changes what a variant costs for the people in a group, over a window.
            When two live lists cover one line the engine takes the most specific quantity break
            first, then the higher priority, then the cheaper of the two.
        </div>

        <section class="tw:mb-8">
            <div class="tw:flex tw:items-center tw:justify-between tw:mb-3">
                <h2 class="tw:text-base tw:font-semibold">Lists</h2>
                {#if writable}
                    <button type="button" class="btn sm" onclick={() => openList(null)}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New price list</span>
                    </button>
                {/if}
            </div>

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th>Name</th>
                            <th>Applies to</th>
                            <th class="col-field-type-number min-width">Priority</th>
                            <th class="col-field-type-number min-width">Prices</th>
                            <th>Window</th>
                            <th class="min-width">State</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each lists as list (list.id)}
                            <tr>
                                <td data-name="Name"><strong>{list.name}</strong></td>
                                <td data-name="Applies to">
                                    {#if list.group_id}
                                        <span class="label">{groupName(list.group_id)}</span>
                                    {:else}
                                        <span class="txt-hint">Everyone</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-number min-width" data-name="Priority">
                                    {list.priority}
                                </td>
                                <td class="col-field-type-number min-width" data-name="Prices">
                                    {list.prices}
                                </td>
                                <td class="txt-hint txt-sm" data-name="Window">{windowOf(list)}</td>
                                <td class="min-width" data-name="State">
                                    <span class="label {list.active ? 'success' : ''}">
                                        {list.active ? "live" : "paused"}
                                    </span>
                                </td>
                                <td class="col-meta min-width">
                                    {#if writable}
                                        <button type="button" class="btn sm secondary transparent"
                                                onclick={() => openList(list)}>
                                            <span class="txt">Edit</span>
                                        </button>
                                        <button type="button" class="btn sm transparent row-delete"
                                                title="Delete" aria-label="Delete {list.name}"
                                                onclick={() => askDelete("list", list)}>
                                            <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                        </button>
                                    {/if}
                                </td>
                            </tr>
                        {/each}
                        {#if !loading && !lists.length}
                            <tr>
                                <td colspan="7" class="txt-center txt-hint p-base">
                                    No price lists yet. Everything sells at its catalogue price.
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>
        </section>

        <section>
            <div class="tw:flex tw:items-center tw:justify-between tw:mb-3">
                <h2 class="tw:text-base tw:font-semibold">Customer groups</h2>
                {#if writable}
                    <button type="button" class="btn sm secondary"
                            onclick={() => { editingGroup = null; groupForm = { code: "", name: "" }; groupOpen = true; }}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New group</span>
                    </button>
                {/if}
            </div>

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th>Name</th>
                            <th>Code</th>
                            <th class="col-field-type-number min-width">Members</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each groups as group (group.id)}
                            <tr>
                                <td data-name="Name"><strong>{group.name}</strong></td>
                                <td data-name="Code"><code>{group.code}</code></td>
                                <td class="col-field-type-number min-width" data-name="Members">
                                    {#if canSeeMembers}
                                        {group.members}
                                    {:else}
                                        <span class="txt-hint" title="Needs customers.read">—</span>
                                    {/if}
                                </td>
                                <td class="col-meta min-width">
                                    {#if writable}
                                        <button type="button" class="btn sm secondary transparent"
                                                onclick={() => { editingGroup = group; groupForm = { code: group.code, name: group.name }; groupOpen = true; }}>
                                            <span class="txt">Edit</span>
                                        </button>
                                        <button type="button" class="btn sm transparent row-delete"
                                                title="Delete" aria-label="Delete {group.name}"
                                                onclick={() => askDelete("group", group)}>
                                            <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                                        </button>
                                    {/if}
                                </td>
                            </tr>
                        {/each}
                        {#if !loading && !groups.length}
                            <tr>
                                <td colspan="4" class="txt-center txt-hint p-base">
                                    No groups yet. A list with no group prices for everyone.
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>
            <div class="field-help">
                Membership is by email address, because a customer here is an address that has
                ordered rather than a record of its own.
            </div>
        </section>
    {/if}
</div>

<Drawer open={listOpen} size="sm"
        title={editing ? "Edit price list" : "New price list"}
        onclose={() => (listOpen = false)}>
    <div class="field">
        <label for="pl-name">Name</label>
        <input id="pl-name" type="text" bind:value={form.name} placeholder="Trade 2026" />
    </div>

    <div class="field m-t-sm">
        <label for="pl-group">Applies to</label>
        <Select id="pl-group" value={form.group_id}
                onchange={(v) => (form.group_id = v || 0)} options={groupOptions} />
    </div>
    <div class="field-help">
        A list with no group reaches every cart, including one with no email on it yet.
    </div>

    <div class="fields m-t-sm">
        <div class="field">
            <label for="pl-starts">Starts</label>
            <input id="pl-starts" type="datetime-local" bind:value={form.starts_at} />
        </div>
        <div class="delimiter"></div>
        <div class="field">
            <label for="pl-ends">Ends</label>
            <input id="pl-ends" type="datetime-local" bind:value={form.ends_at} />
        </div>
        <div class="delimiter"></div>
        <div class="field">
            <label for="pl-priority">Priority</label>
            <input id="pl-priority" type="number" bind:value={form.priority} />
        </div>
    </div>
    <div class="field-help">
        Both ends are optional — most lists run from now until further notice. Priority only
        matters when two live lists cover the same line; higher wins.
    </div>

    <label class="form-field form-field-toggle m-t-sm">
        <input type="checkbox" id="pl-active" bind:checked={form.active} />
        <label for="pl-active">Live</label>
    </label>

    {#snippet footer()}
        <button type="button" class="btn secondary" onclick={() => (listOpen = false)}>
            <span class="txt">Cancel</span>
        </button>
        <button type="button" class="btn" disabled={saving} onclick={saveList}>
            <span class="txt">{editing ? "Save" : "Create"}</span>
        </button>
    {/snippet}
</Drawer>

<Drawer open={groupOpen} size="sm"
        title={editingGroup ? "Edit group" : "New customer group"}
        onclose={() => (groupOpen = false)}>
    <div class="field">
        <label for="cg-name">Name</label>
        <input id="cg-name" type="text" bind:value={groupForm.name} placeholder="Trade accounts" />
    </div>
    <div class="field m-t-sm">
        <label for="cg-code">Code</label>
        <input id="cg-code" type="text" bind:value={groupForm.code} placeholder="trade" />
    </div>
    <div class="field-help">
        The code is the handle a script or an import names the group by, so renaming the group
        keeps it the same group.
    </div>

    {#snippet footer()}
        <button type="button" class="btn secondary" onclick={() => (groupOpen = false)}>
            <span class="txt">Cancel</span>
        </button>
        <button type="button" class="btn" disabled={saving} onclick={saveGroup}>
            <span class="txt">{editingGroup ? "Save" : "Create"}</span>
        </button>
    {/snippet}
</Drawer>

<Confirm
    bind:open={confirmOpen}
    title={pending?.kind === "group" ? "Delete this group?" : "Delete this price list?"}
    message={pending?.kind === "group"
        ? `"${pending?.row?.name}" and the price lists aimed at it will go. Orders already placed keep the price they were charged.`
        : `"${pending?.row?.name}" and its prices will go. Orders already placed keep the price they were charged.`}
    confirmLabel="Delete"
    danger
    onconfirm={doDelete}
/>
