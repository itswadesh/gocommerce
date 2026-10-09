<script>
    /**
     * Customer groups: who gets a price list, a delivery rate of their own, and
     * — when the group is tax-exempt — no tax.
     *
     * A group is a code and a name — trade, wholesale, staff — and the price
     * lists under Products aim at it. This screen is the address book on its
     * own, beside Customers where an operator looks for it; the same groups
     * are edited from the Price lists screen too, because a group nothing is
     * priced for is only a label.
     *
     * The exemption switch is the one control here that is a tax decision
     * (D76), so it takes taxes.write on top of groups.write and says what it
     * does in one sentence — including the consequence that is easy to miss:
     * adding an address to an exempt group exempts that address.
     */
    import { base } from "$app/paths";
    import { api, can } from "$lib/api.js";
    import { settings, ensureSettings } from "$lib/settings.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    /* The routes take groups.read and groups.write; this screen used to ask
       for the discount rights the groups were filed under before they had
       their own, which hid it from somebody granted exactly the right it
       needs. */
    const readable = $derived(can("groups.read"));
    const writable = $derived(can("groups.write"));
    const taxWritable = $derived(can("taxes.write"));

    let groups = $state([]);
    let loading = $state(true);
    let open = $state(false);
    let editing = $state(null);
    let form = $state({ code: "", name: "", tax_exempt: false });
    let saving = $state(false);
    let removing = $state(null);

    $effect(() => {
        if (readable) load();
    });
    $effect(() => {
        ensureSettings();
    });

    async function load() {
        loading = true;
        try {
            // api.get unwraps the envelope, so an unpaged list arrives as the
            // array itself; reading `.data` off it showed every store "No
            // groups yet".
            const res = await api.get("/api/admin/customer-groups");
            groups = Array.isArray(res) ? res : (res?.data ?? []);
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function add() {
        editing = null;
        form = { code: "", name: "", tax_exempt: false };
        open = true;
    }
    function edit(g) {
        editing = g;
        form = { code: g.code, name: g.name, tax_exempt: !!g.tax_exempt };
        open = true;
    }

    async function save() {
        const code = form.code.trim();
        const name = form.name.trim();
        if (!code || !name) return toast.error("A group needs a code and a name.");
        // The exemption is sent only when it is being decided, so an operator
        // who may rename a group but not decide its tax can still rename it.
        const body = { code, name };
        if (editing ? form.tax_exempt !== !!editing.tax_exempt : form.tax_exempt) {
            body.tax_exempt = form.tax_exempt;
        }
        saving = true;
        try {
            if (editing) await api.patch(`/api/admin/customer-groups/${editing.id}`, body);
            else await api.post("/api/admin/customer-groups", body);
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
            <NoAccess right="groups.read" what="customer groups" />
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Group</th>
                            <th class="col-field-type-text">Code</th>
                            <th class="col-field-type-text">Tax</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each groups as g (g.id)}
                            <tr>
                                <td class="col-field-name-id" data-name="Group"><span class="txt-bold">{g.name}</span></td>
                                <td class="col-field-type-text" data-name="Code"><span class="txt-code">{g.code}</span></td>
                                <td class="col-field-type-text" data-name="Tax">
                                    {#if g.tax_exempt}
                                        <span class="label info group-exempt" title="Members are charged no tax">Exempt</span>
                                    {:else}
                                        <span class="txt-hint">Charged</span>
                                    {/if}
                                </td>
                                <td class="col-meta min-width row-actions">
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
                            <tr><td colspan="4"><span class="skeleton-loader"></span></td></tr>
                        {:else if !groups.length}
                            <tr>
                                <td colspan="4" class="txt-center txt-hint p-base">
                                    No groups yet. Add one, then price it under <a href="{base}/dash/pricing">Price lists</a>.
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>
            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <span class="txt txt-hint">
                    An address joins a group through the API, or by joining a company that prices through it.
                    The price lists that aim at a group are under Products, and the delivery rates offered to
                    one are under Shipping.
                </span>
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
    <div class="field m-t-sm group-exempt-field">
        <input
            type="checkbox"
            id="group-tax-exempt"
            class="switch"
            bind:checked={form.tax_exempt}
            disabled={!taxWritable}
        />
        <label for="group-tax-exempt">Tax-exempt</label>
    </div>
    {#key form.tax_exempt}
        <div class="field-help group-exempt-help">
            {#if form.tax_exempt}
                Members are charged no tax on any order, and each order records this group as the reason.
                Adding an address to this group exempts it.
                {#if settings.pricesIncludeTax}
                    Prices in this store include tax, so a member pays the same price with none of it counted
                    as tax; to charge them less, give the group a price list.
                {/if}
            {:else}
                Members pay tax like everybody else. Switch this on for a group that is not charged tax —
                registered resellers, say — and every member's next order carries none.
            {/if}
            {#if !taxWritable}
                Changing this needs the taxes.write right.
            {/if}
        </div>
    {/key}
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (open = false)}><span class="txt">Cancel</span></button>
        <button type="button" class="btn" class:loading={saving} disabled={saving} onclick={save}><span class="txt">{editing ? "Save" : "Add"}</span></button>
    {/snippet}
</Drawer>

<Confirm
    open={!!removing}
    title="Remove this group?"
    message={removing
        ? `“${removing.name}” is removed from every customer in it, the price lists and delivery rates aimed at it stop applying${removing.tax_exempt ? ", and its members start paying tax" : ""}. Orders already placed keep what they were sold.`
        : ""}
    confirmLabel="Remove"
    danger
    onconfirm={remove}
    oncancel={() => (removing = null)}
/>

<style>
    /* The sentence under the switch changes with it; it arrives the way the
       page does, so flipping the switch visibly says something new. */
    .group-exempt-help {
        animation: slideTop var(--animationSpeed) ease-out;
    }
    .group-exempt {
        animation: fadeIn var(--animationSpeed);
    }
    @media (prefers-reduced-motion: reduce) {
        .group-exempt-help {
            animation-name: fadeIn;
        }
    }
</style>
