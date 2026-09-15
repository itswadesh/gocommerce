<script>
    /**
     * Newsletter: who asked to hear from the store.
     *
     * A list, a search, an export for whatever sends the mail, an address
     * typed in by hand, and a delete for the request to be forgotten.
     * Nothing here sends a newsletter — the export is how the list reaches
     * the tool that does.
     */
    import { api, apiErrorFrom, can, getToken, query, request } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    const PER_PAGE = 25;
    const readable = $derived(can("customers.read"));
    const operates = $derived(can("store.operate"));
    const exports = $derived(can("data.export"));
    const missing = $derived(modulesKnown() && !hasModule("newsletter"));

    const list = listState({ q: "", status: "subscribed", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);
    let draft = $state(list.params.q);
    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    let addOpen = $state(false);
    let adding = $state(false);
    let form = $state({ email: "", name: "" });
    let removing = $state(null);

    $effect(() => {
        list.params;
        if (readable && hasModule("newsletter")) load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/x/newsletter/subscriptions" +
                    query({
                        q: list.params.q,
                        status: list.params.status === "any" ? "" : list.params.status,
                        page: list.page,
                        limit: perPage,
                    }),
            );
            rows = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function submitSearch(e) {
        e.preventDefault();
        list.set({ q: draft.trim() });
    }

    async function add() {
        adding = true;
        try {
            await request("POST", "/api/admin/x/newsletter/subscriptions", { body: form });
            toast.success(`${form.email.trim()} added`);
            addOpen = false;
            form = { email: "", name: "" };
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            adding = false;
        }
    }

    async function remove() {
        if (!removing) return;
        try {
            await request("DELETE", `/api/admin/x/newsletter/subscriptions/${removing.id}`);
            toast.success(`${removing.email} removed`);
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            removing = null;
        }
    }

    async function download() {
        try {
            const status = list.params.status === "any" ? "subscribed" : list.params.status;
            const response = await fetch("/api/admin/x/newsletter/subscriptions.csv" + query({ status }), {
                headers: { Authorization: "Bearer " + getToken() },
            });
            if (!response.ok) {
                toast.error(await apiErrorFrom(response));
                return;
            }
            const url = URL.createObjectURL(await response.blob());
            const a = document.createElement("a");
            a.href = url;
            a.download = "newsletter.csv";
            a.click();
            URL.revokeObjectURL(url);
            toast.success("Export downloaded");
        } catch (err) {
            toast.error(err);
        }
    }
</script>

<svelte:head><title>Newsletter · GoCommerce</title></svelte:head>

<div class="page page-newsletter shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Newsletter</div>
            </nav>

            {#if readable && !missing}
                <form class="fields searchbar" onsubmit={submitSearch}>
                    <div class="field">
                        <input type="text" class="p-l-20" placeholder="Search by email or name" bind:value={draft} />
                    </div>
                    {#if draft || list.params.q}
                        <div class="field addon p-r-5">
                            {#if draft !== list.params.q}
                                <button type="submit" class="btn sm pill warning">Search</button>
                            {/if}
                            <button type="button" class="btn sm pill secondary transparent" onclick={() => ((draft = ""), list.set({ q: "" }))}>
                                Clear
                            </button>
                        </div>
                    {/if}
                </form>

                <div class="page-header-primary-btns">
                    <div class="field">
                        <Select
                            id="newsletter-status"
                            ariaLabel="Status"
                            value={list.params.status}
                            options={[
                                { value: "subscribed", label: "Subscribed" },
                                { value: "unsubscribed", label: "Unsubscribed" },
                                { value: "any", label: "Everyone" },
                            ]}
                            onchange={(v) => list.set({ status: v })}
                        />
                    </div>
                    {#if exports}
                        <button type="button" class="btn secondary" onclick={download}>
                            <i class="ri-download-2-line" aria-hidden="true"></i>
                            <span class="txt">Export CSV</span>
                        </button>
                    {/if}
                    {#if operates}
                        <button type="button" class="btn expanded" onclick={() => (addOpen = true)}>
                            <i class="ri-add-line" aria-hidden="true"></i>
                            <span class="txt">Add address</span>
                        </button>
                    {/if}
                </div>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="customers.read" what="the newsletter list" />
        {:else if missing}
            <ModuleMissing module="newsletter" what="The newsletter list is served by ext/newsletter, and this binary does not have it." />
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Email</th>
                            <th class="col-field-type-text">Name</th>
                            <th class="col-field-type-select min-width">Source</th>
                            <th class="col-field-type-select min-width">Status</th>
                            <th class="col-field-type-date min-width">Signed up</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.id)}
                            <tr>
                                <td class="col-field-name-id" data-name="Email"><span class="txt-bold">{row.email}</span></td>
                                <td class="col-field-type-text" data-name="Name">{row.name || "—"}</td>
                                <td class="col-field-type-select min-width" data-name="Source"><span class="txt-hint">{row.source || "—"}</span></td>
                                <td class="col-field-type-select min-width" data-name="Status">
                                    <span class="label {row.status === 'subscribed' ? 'label-success' : ''}" title={row.unsubscribed_at ? "Left " + formatDate(row.unsubscribed_at) : ""}>
                                        {row.status === "subscribed" ? "Subscribed" : "Unsubscribed"}
                                    </span>
                                </td>
                                <td class="col-field-type-date min-width txt-hint" data-name="Signed up" title={formatDate(row.created_at)}>
                                    {relativeTime(row.created_at)}
                                </td>
                                <td class="col-meta min-width">
                                    {#if operates}
                                        <button type="button" class="btn circle sm transparent secondary" title="Remove this address" aria-label="Remove" onclick={() => (removing = row)}>
                                            <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                        </button>
                                    {/if}
                                </td>
                            </tr>
                        {/each}
                        {#if loading && !rows.length}
                            {#each Array(6) as _, i (i)}
                                <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !rows.length}
                            <tr><td colspan="6" class="txt-center txt-hint p-base">Nobody here yet. The storefront's signup box posts to <code>/x/newsletter/subscribe</code>.</td></tr>
                        {/if}
                    </tbody>
                </table>
            </div>
            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager {meta} {loading} noun="address" {perPage} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
                <div class="flex-fill"></div>
            </footer>
        {/if}
    </div>
</div>

<Drawer open={addOpen} size="sm" title="Add an address" onclose={() => (addOpen = false)}>
    <div class="field">
        <label for="nl-email">Email</label>
        <input id="nl-email" type="email" bind:value={form.email} />
    </div>
    <div class="field m-t-sm">
        <label for="nl-name">Name</label>
        <input id="nl-name" type="text" bind:value={form.name} />
    </div>
    <div class="field-help">For somebody who asked in person. Recorded with the source "admin".</div>
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (addOpen = false)}><span class="txt">Cancel</span></button>
        <button type="button" class="btn" class:loading={adding} disabled={adding || !form.email.trim()} onclick={add}><span class="txt">Add</span></button>
    {/snippet}
</Drawer>

<Confirm
    open={!!removing}
    title="Remove this address?"
    message={removing ? `${removing.email} is removed entirely — the request to be forgotten. To stop mail without forgetting, they use the unsubscribe link instead.` : ""}
    confirmLabel="Remove"
    danger
    onconfirm={remove}
    oncancel={() => (removing = null)}
/>
