<script>
    /**
     * Customer groups: who gets a price list.
     *
     * A group is a code and a name — trade, wholesale, staff — and the price
     * lists under Products aim at it. This screen is the address book on its
     * own, beside Customers where an operator looks for it; the same groups
     * are edited from the Price lists screen too, because a group nothing is
     * priced for is only a label.
     */
    import { base } from "$app/paths";
    import { api, can } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    const readable = $derived(can("discounts.read"));
    const writable = $derived(can("discounts.write"));

    let groups = $state([]);
    let loading = $state(true);
    let open = $state(false);
    let editing = $state(null);
    let form = $state({ code: "", name: "" });
    let saving = $state(false);
    let removing = $state(null);

    $effect(() => {
        if (readable) load();
    });

    async function load() {
        loading = true;
        try {
            const res = await api.get("/api/admin/customer-groups");
            groups = res.data ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function add() {
        editing = null;
        form = { code: "", name: "" };
        open = true;
    }
    function edit(g) {
        editing = g;
        form = { code: g.code, name: g.name };
        open = true;
    }

    async function save() {
        const code = form.code.trim();
        const name = form.name.trim();
        if (!code || !name) return toast.error("A group needs a code and a name.");
        saving = true;
        try {
            if (editing) await api.patch(`/api/admin/customer-groups/${editing.id}`, { code, name });
            else await api.post("/api/admin/customer-groups", { code, name });
            toast.success(editing ? "Group saved" : "Group added");
            open = false;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function remove() {
        if (!removing) return;
        try {
            await api.delete(`/api/admin/customer-groups/${removing.id}`);
            toast.success("Group removed");
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            removing = null;
        }
    }
</script>

<svelte:head><title>Customer groups · GoCommerce</title></svelte:head>

<div class="page page-customer-groups shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Customers</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Groups</div>
            </nav>
            {#if readable}
                <div class="inline-flex gap-sm">
                    <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>
                <div class="page-header-primary-btns">
                    {#if writable}
                        <button type="button" class="btn expanded" onclick={add}>
                            <i class="ri-add-line" aria-hidden="true"></i>
                            <span class="txt">New group</span>
                        </button>
                    {/if}
                </div>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="discounts.read" what="customer groups" />
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Group</th>
                            <th class="col-field-type-text">Code</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each groups as g (g.id)}
                            <tr>
                                <td class="col-field-name-id" data-name="Group"><span class="txt-bold">{g.name}</span></td>
                                <td class="col-field-type-text" data-name="Code"><span class="txt-code">{g.code}</span></td>
                                <td class="col-meta min-width">
                                    {#if writable}
                                        <button type="button" class="btn circle sm transparent secondary" title="Edit" aria-label="Edit {g.name}" onclick={() => edit(g)}>
                                            <i class="ri-pencil-line" aria-hidden="true"></i>
                                        </button>
                                        <button type="button" class="btn circle sm transparent secondary" title="Remove" aria-label="Remove {g.name}" onclick={() => (removing = g)}>
                                            <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                        </button>
                                    {/if}
                                </td>
                            </tr>
                        {/each}
                        {#if loading && !groups.length}
                            <tr><td colspan="3"><span class="skeleton-loader"></span></td></tr>
                        {:else if !groups.length}
                            <tr>
                                <td colspan="3" class="txt-center txt-hint p-base">
                                    No groups yet. Add one, then price it under <a href="{base}/pricing">Price lists</a>.
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>
            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <span class="txt txt-hint">A shopper joins a group on their customer record; the price lists that aim at it are under Products.</span>
            </footer>
        {/if}
    </div>
</div>

<Drawer {open} size="sm" title={editing ? "Edit group" : "New group"} onclose={() => (open = false)}>
    <div class="field">
        <label for="group-name">Name</label>
        <input id="group-name" type="text" placeholder="Wholesale" bind:value={form.name} />
    </div>
    <div class="field m-t-sm">
        <label for="group-code">Code</label>
        <input id="group-code" type="text" placeholder="wholesale" bind:value={form.code} />
        <div class="field-help">Lowercase, no spaces; what the API and an import call it.</div>
    </div>
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (open = false)}><span class="txt">Cancel</span></button>
        <button type="button" class="btn" class:loading={saving} disabled={saving} onclick={save}><span class="txt">{editing ? "Save" : "Add"}</span></button>
    {/snippet}
</Drawer>

<Confirm
    open={!!removing}
    title="Remove this group?"
    message={removing ? `“${removing.name}” is removed from every customer in it, and the price lists aimed at it stop applying.` : ""}
    confirmLabel="Remove"
    danger
    onconfirm={remove}
    oncancel={() => (removing = null)}
/>
