<script>
    /**
     * Vendors: the sellers on this store.
     *
     * Not the same thing as the `vendor` on a product, and the distinction is
     * the whole reason this screen exists. That field is free text holding the
     * manufacturer — "Anker", "Amazon Essentials" — and it is what a Google
     * feed sends as `brand`. A vendor here is somebody selling through this
     * shop. A store with no marketplace never opens this screen and loses
     * nothing.
     *
     * Status is the column that matters. A seller arrives pending, and a
     * marketplace that lists whoever signs up is not moderating anything, so
     * the list leads with the ones waiting to be let in rather than burying
     * them in id order.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { onNewShortcut } from "$lib/shortcuts.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate } from "$lib/format.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    const PER_PAGE = 50;
    const list = listState({ page: 1, limit: PER_PAGE, q: "", status: "" });
    const perPage = $derived(list.params.limit);

    let vendors = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    let editorOpen = $state(false);
    let editing = $state(null);
    let form = $state(blankVendor());
    let saving = $state(false);
    let errors = $state({});

    let confirmOpen = $state(false);
    let pendingDelete = $state(null);

    const readable = $derived(can("vendors.read"));
    const writable = $derived(can("vendors.write"));

    const STATUSES = [
        { value: "", label: "Every status" },
        { value: "pending", label: "Pending — waiting to be let in" },
        { value: "approved", label: "Approved — selling" },
        { value: "suspended", label: "Suspended — stopped" },
    ];

    function blankVendor() {
        return {
            name: "",
            slug: "",
            legal_name: "",
            email: "",
            phone: "",
            website: "",
            about: "",
            tax_id: "",
            status: "pending",
            /* Held as a percentage in the box and converted on the way out.
               Basis points are the engine's unit because they cannot drift the
               way a float can, and "2.5" is what a person types. */
            commission_pct: "0",
            address: { line1: "", line2: "", city: "", state: "", postal_code: "", country: "" },
        };
    }

    $effect(() => {
        list.params;
        load();
    });

    async function load() {
        if (!readable) {
            loading = false;
            return;
        }
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/vendors" +
                    query({
                        limit: list.params.limit,
                        page: list.params.page,
                        q: list.params.q || undefined,
                        status: list.params.status || undefined,
                    }),
            );
            vendors = result.data ?? result ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function openNew() {
        if (!writable) return;
        editing = null;
        form = blankVendor();
        errors = {};
        editorOpen = true;
    }

    function openEdit(vendor) {
        editing = vendor;
        errors = {};
        form = {
            name: vendor.name ?? "",
            slug: vendor.slug ?? "",
            legal_name: vendor.legal_name ?? "",
            email: vendor.email ?? "",
            phone: vendor.phone ?? "",
            website: vendor.website ?? "",
            about: vendor.about ?? "",
            tax_id: vendor.tax_id ?? "",
            status: vendor.status ?? "pending",
            commission_pct: String((vendor.commission_bp ?? 0) / 100),
            address: { ...blankVendor().address, ...(vendor.address ?? {}) },
        };
        editorOpen = true;
    }

    onNewShortcut(openNew);

    /**
     * Percent in the box, basis points on the wire.
     *
     * Rounded rather than truncated: 2.5% is 250 bp exactly, but 0.07 * 100 in
     * binary floating point is 7.000000000000001, and truncating that takes a
     * basis point off every sale.
     */
    function commissionBP(pct) {
        const n = Number.parseFloat(String(pct).trim());
        if (!Number.isFinite(n)) return null;
        return Math.round(n * 100);
    }

    async function save(event) {
        event?.preventDefault();
        if (saving || !writable) return;

        errors = {};
        if (!form.name.trim()) errors.name = "A name is required.";
        const bp = commissionBP(form.commission_pct);
        if (bp === null) errors.commission = "Enter a number, or 0 for no commission.";
        else if (bp < 0 || bp > 10000) errors.commission = "A commission runs from 0 to 100%.";
        if (Object.keys(errors).length) return;

        const body = {
            name: form.name.trim(),
            legal_name: form.legal_name.trim(),
            email: form.email.trim(),
            phone: form.phone.trim(),
            website: form.website.trim(),
            about: form.about,
            tax_id: form.tax_id.trim(),
            status: form.status,
            commission_bp: bp,
            address: { ...form.address },
        };

        saving = true;
        try {
            if (editing) {
                // The slug is not in the patch: a handle is a URL somebody has
                // linked to, and the engine refuses to move it on a rename.
                await api.patch(`/api/admin/vendors/${editing.id}`, body);
                toast.success("Vendor saved");
            } else {
                if (form.slug.trim()) body.slug = form.slug.trim();
                await api.post("/api/admin/vendors", body);
                toast.success("Vendor added");
            }
            editorOpen = false;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    function askDelete(vendor, event) {
        event?.stopPropagation();
        pendingDelete = vendor;
        confirmOpen = true;
    }

    async function remove() {
        if (!pendingDelete) return;
        try {
            await api.delete(`/api/admin/vendors/${pendingDelete.id}`);
            toast.success(`Removed ${pendingDelete.name}`);
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            pendingDelete = null;
        }
    }

    const statusLabel = (s) =>
        ({ pending: "Pending", approved: "Approved", suspended: "Suspended" })[s] ?? s;

    /* Pending is the one that wants attention, so it is the one that is
       coloured. Approved is the ordinary state and says so quietly. */
    const statusClass = (s) =>
        ({ pending: "label-warning", approved: "label-success", suspended: "label-danger" })[s] ?? "";
</script>

<svelte:head><title>Vendors</title></svelte:head>

{#if !readable}
    <NoAccess right="vendors.read" />
{:else}
    <div class="page-header-wrapper">
        <header class="page-header">
            <nav class="breadcrumbs"><div class="breadcrumb-item">Vendors</div></nav>
            <div class="inline-flex gap-sm flex-gap-auto">
                {#if writable}
                    <button type="button" class="btn" onclick={openNew}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New vendor</span>
                    </button>
                {/if}
            </div>
        </header>
    </div>

    <div class="page-content">
        <div class="field-help m-b-base">
            A vendor is somebody selling through this shop. It is not the
            <strong>vendor</strong> field on a product — that one holds the brand or
            manufacturer, and it is what a product feed sends as <code>brand</code>.
        </div>

        <div class="fields m-b-base">
            <div class="field">
                <label for="vendor-search">Search</label>
                <input
                    id="vendor-search"
                    type="text"
                    placeholder="Name or handle"
                    value={list.params.q}
                    oninput={(e) => list.set({ q: e.currentTarget.value, page: 1 })}
                />
            </div>
            <div class="field">
                <label for="vendor-status">Status</label>
                <Select
                    id="vendor-status"
                    value={list.params.status}
                    options={STATUSES}
                    onchange={(v) => list.set({ status: v, page: 1 })}
                />
            </div>
        </div>

        <div class="page-table-wrapper tw:rounded-xl tw:border">
            <table class="table responsive-table">
                <thead class="sticky">
                    <tr>
                        <th>Vendor</th>
                        <th class="min-width">Status</th>
                        <th class="col-field-type-number min-width">Commission</th>
                        <th class="min-width">Added</th>
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#if loading && !vendors.length}
                        <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                    {/if}
                    {#each vendors as vendor (rowKey(vendor))}
                        <tr onclick={() => goto(`${base}/vendors/${vendor.id}`)}>
                            <td data-name="Vendor">
                                <a href="{base}/vendors/{vendor.id}" onclick={(e) => e.stopPropagation()}>
                                    <strong>{vendor.name}</strong>
                                </a>
                                <div class="txt-hint txt-sm txt-code">{vendor.slug}</div>
                            </td>
                            <td class="min-width" data-name="Status">
                                <span class="label {statusClass(vendor.status)}">
                                    {statusLabel(vendor.status)}
                                </span>
                            </td>
                            <td class="col-field-type-number min-width" data-name="Commission">
                                {#if vendor.commission_bp}
                                    {(vendor.commission_bp / 100).toFixed(2)}%
                                {:else}
                                    <span class="txt-hint">—</span>
                                {/if}
                            </td>
                            <td class="min-width txt-hint txt-sm" data-name="Added">
                                {formatDate(vendor.created_at)}
                            </td>
                            <td class="col-meta min-width">
                                {#if writable}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary"
                                        title="Edit {vendor.name}"
                                        aria-label="Edit {vendor.name}"
                                        onclick={(e) => {
                                            e.stopPropagation();
                                            openEdit(vendor);
                                        }}
                                    >
                                        <i class="ri-edit-line" aria-hidden="true"></i>
                                    </button>
                                    <button
                                        type="button"
                                        class="btn circle sm transparent danger"
                                        title="Remove {vendor.name}"
                                        aria-label="Remove {vendor.name}"
                                        onclick={(e) => askDelete(vendor, e)}
                                    >
                                        <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                    </button>
                                {/if}
                            </td>
                        </tr>
                    {/each}
                    {#if !loading && !vendors.length}
                        <tr>
                            <td colspan="5" class="txt-hint txt-center p-base">
                                {#if list.params.q || list.params.status}
                                    No vendor matches that.
                                {:else}
                                    No vendors yet. A shop selling only its own goods does not need
                                    any — the price and stock on a variant are this store's own.
                                {/if}
                            </td>
                        </tr>
                    {/if}
                </tbody>
            </table>
        </div>

        {#if meta}
            <Pager
                page={meta.page}
                total={meta.total}
                {loading}
                noun="vendor"
                {perPage}
                onpage={(n) => list.setPage(n)}
                onperpage={(n) => list.set({ limit: n })}
            />
        {/if}
    </div>

    <Drawer
        open={editorOpen}
        size="sm"
        title={editing ? `Edit ${editing.name}` : "New vendor"}
        onclose={() => (editorOpen = false)}
    >
        <form id="vendor-form" onsubmit={save}>
            <div class="field required" class:error={!!errors.name}>
                <label for="v-name">Name</label>
                <input id="v-name" type="text" bind:value={form.name} />
            </div>
            {#if errors.name}<div class="field-help error">{errors.name}</div>{/if}

            {#if !editing}
                <div class="field m-t-sm">
                    <label for="v-slug">Handle</label>
                    <input
                        id="v-slug"
                        type="text"
                        placeholder="Derived from the name if left empty"
                        bind:value={form.slug}
                    />
                    <div class="field-help">
                        The <code>/vendor/…</code> address. It does not follow a rename, because a
                        handle in circulation is a link somebody has already made.
                    </div>
                </div>
            {/if}

            <div class="field m-t-sm">
                <label for="v-status">Status</label>
                <Select
                    id="v-status"
                    bind:value={form.status}
                    options={STATUSES.filter((s) => s.value)}
                />
                <div class="field-help">
                    Only approved vendors appear on the storefront. Suspending keeps the record and
                    everything they were offering.
                </div>
            </div>

            <div class="field m-t-sm" class:error={!!errors.commission}>
                <label for="v-commission">Commission (%)</label>
                <input id="v-commission" type="text" inputmode="decimal" bind:value={form.commission_pct} />
                <div class="field-help">
                    {#if errors.commission}
                        <span class="txt-danger">{errors.commission}</span>
                    {:else}
                        What this store takes of their sales. Recorded only — nothing works out a
                        payout from it yet.
                    {/if}
                </div>
            </div>

            <h6 class="section-title">
                <i class="ri-contacts-line" aria-hidden="true"></i>
                Contact
            </h6>
            <div class="fields">
                <div class="field">
                    <label for="v-email">Email</label>
                    <input id="v-email" type="email" bind:value={form.email} />
                </div>
                <div class="field">
                    <label for="v-phone">Phone</label>
                    <input id="v-phone" type="text" bind:value={form.phone} />
                </div>
            </div>
            <div class="field m-t-sm">
                <label for="v-website">Website</label>
                <input id="v-website" type="text" bind:value={form.website} />
            </div>

            <h6 class="section-title">
                <i class="ri-building-line" aria-hidden="true"></i>
                Trading details
            </h6>
            <div class="field">
                <label for="v-legal">Legal name</label>
                <input id="v-legal" type="text" bind:value={form.legal_name} />
                <div class="field-help">The name on the invoice, when it is not the one over the shop.</div>
            </div>
            <div class="field m-t-sm">
                <label for="v-tax">Tax number</label>
                <input id="v-tax" type="text" class="txt-code" bind:value={form.tax_id} />
            </div>
            <div class="field m-t-sm">
                <label for="v-line1">Address</label>
                <input id="v-line1" type="text" placeholder="Line 1" bind:value={form.address.line1} />
            </div>
            <div class="field m-t-sm">
                <input
                    type="text"
                    placeholder="Line 2"
                    aria-label="Address line 2"
                    bind:value={form.address.line2}
                />
            </div>
            <div class="fields m-t-sm">
                <div class="field">
                    <input type="text" placeholder="City" aria-label="City" bind:value={form.address.city} />
                </div>
                <div class="field">
                    <input type="text" placeholder="State" aria-label="State" bind:value={form.address.state} />
                </div>
            </div>
            <div class="fields m-t-sm">
                <div class="field">
                    <input
                        type="text"
                        placeholder="Postcode"
                        aria-label="Postcode"
                        bind:value={form.address.postal_code}
                    />
                </div>
                <div class="field">
                    <input
                        type="text"
                        placeholder="Country"
                        aria-label="Country"
                        bind:value={form.address.country}
                    />
                </div>
            </div>

            <div class="field m-t-sm">
                <label for="v-about">About</label>
                <textarea id="v-about" rows="3" bind:value={form.about}></textarea>
                <div class="field-help">Shown on their shop page.</div>
            </div>
        </form>

        {#snippet footer()}
            <button type="button" class="btn transparent m-r-auto" onclick={() => (editorOpen = false)}>
                <span class="txt">Cancel</span>
            </button>
            <button
                type="submit"
                form="vendor-form"
                class="btn"
                class:loading={saving}
                disabled={saving || !writable}
            >
                <span class="txt">{editing ? "Save" : "Add vendor"}</span>
            </button>
        {/snippet}
    </Drawer>

    <Confirm
        bind:open={confirmOpen}
        title="Remove {pendingDelete?.name ?? 'this vendor'}?"
        message="Everything they were offering is withdrawn with them. To stop somebody selling while keeping the record, suspend them instead."
        confirmLabel="Remove"
        danger
        onconfirm={remove}
    />
{/if}
