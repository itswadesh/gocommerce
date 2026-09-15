<script>
    /**
     * Reviews: what shoppers said, and whether it shows.
     *
     * Moderation is the whole screen: the pending ones first, each with its
     * stars, its words and whether the reviewer bought the thing, and two
     * buttons. A reply is the store's public answer under the review.
     */
    import { base } from "$app/paths";
    import { api, can, query, request } from "$lib/api.js";
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
    const readable = $derived(can("catalog.read"));
    const writable = $derived(can("catalog.write"));
    const missing = $derived(modulesKnown() && !hasModule("reviews"));

    const list = listState({ q: "", status: "pending", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);
    let draft = $state(list.params.q);
    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let working = $state(0);

    let replying = $state(null);
    let reply = $state("");
    let removing = $state(null);

    $effect(() => {
        list.params;
        if (readable && hasModule("reviews")) load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/x/reviews" +
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

    async function setStatus(row, status) {
        working = row.id;
        try {
            const updated = await request("PATCH", `/api/admin/x/reviews/${row.id}`, { body: { status } });
            rows = rows.map((r) => (r.id === updated.id ? updated : r));
            toast.success(status === "approved" ? "Approved — it shows on the product page" : status === "rejected" ? "Rejected" : "Back to pending");
        } catch (err) {
            toast.error(err);
        } finally {
            working = 0;
        }
    }

    async function saveReply() {
        if (!replying) return;
        working = replying.id;
        try {
            const updated = await request("PATCH", `/api/admin/x/reviews/${replying.id}`, { body: { reply } });
            rows = rows.map((r) => (r.id === updated.id ? updated : r));
            toast.success(reply.trim() ? "Reply saved" : "Reply removed");
            replying = null;
        } catch (err) {
            toast.error(err);
        } finally {
            working = 0;
        }
    }

    async function remove() {
        if (!removing) return;
        try {
            await request("DELETE", `/api/admin/x/reviews/${removing.id}`);
            toast.success("Review deleted");
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            removing = null;
        }
    }

    function statusClass(status) {
        if (status === "approved") return "label-success";
        if (status === "rejected") return "label-danger";
        return "label-warning";
    }
    const STATUS = { pending: "Pending", approved: "Approved", rejected: "Rejected" };
    const stars = (n) => "★".repeat(n) + "☆".repeat(5 - n);
</script>

<svelte:head><title>Reviews · GoCommerce</title></svelte:head>

<div class="page page-reviews shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Reviews</div>
            </nav>

            {#if readable && !missing}
                <form class="fields searchbar" onsubmit={submitSearch}>
                    <div class="field">
                        <input type="text" class="p-l-20" placeholder="Search by name, email or words" bind:value={draft} />
                    </div>
                    {#if draft || list.params.q}
                        <div class="field addon p-r-5">
                            {#if draft !== list.params.q}
                                <button type="submit" class="btn sm pill warning">Search</button>
                            {/if}
                            <button type="button" class="btn sm pill secondary transparent" onclick={() => ((draft = ""), list.set({ q: "" }))}>Clear</button>
                        </div>
                    {/if}
                </form>
                <div class="page-header-primary-btns">
                    <div class="field">
                        <Select
                            id="review-status"
                            ariaLabel="Status"
                            value={list.params.status}
                            options={[
                                { value: "pending", label: "Pending" },
                                { value: "approved", label: "Approved" },
                                { value: "rejected", label: "Rejected" },
                                { value: "any", label: "Everything" },
                            ]}
                            onchange={(v) => list.set({ status: v })}
                        />
                    </div>
                </div>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="catalog.read" what="product reviews" />
        {:else if missing}
            <ModuleMissing module="reviews" what="Reviews are served by ext/reviews, and this binary does not have it." />
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Product</th>
                            <th class="col-field-type-select min-width">Rating</th>
                            <th class="col-field-type-text">Review</th>
                            <th class="col-field-type-select min-width">By</th>
                            <th class="col-field-type-select min-width">Status</th>
                            <th class="col-field-type-date min-width">When</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.id)}
                            <tr>
                                <td class="col-field-name-id" data-name="Product">
                                    <a href="{base}/products/{row.product_id}" class="txt-bold txt-ellipsis">{row.product || "product #" + row.product_id}</a>
                                </td>
                                <td class="col-field-type-select min-width" data-name="Rating">
                                    <span class="review-stars" title="{row.rating} of 5">{stars(row.rating)}</span>
                                </td>
                                <td class="col-field-type-text" data-name="Review" title={row.body}>
                                    <div class="row-name">
                                        {#if row.title}<span class="txt-bold txt-ellipsis">{row.title}</span>{/if}
                                        <span class="txt-hint txt-sm review-body">{row.body}</span>
                                        {#if row.reply}<span class="txt-sm txt-ellipsis"><i class="ri-reply-line" aria-hidden="true"></i> {row.reply}</span>{/if}
                                    </div>
                                </td>
                                <td class="col-field-type-select min-width" data-name="By">
                                    <div class="row-name">
                                        <span class="txt-ellipsis">{row.name}</span>
                                        <span class="txt-hint txt-sm">{row.verified ? "verified buyer" : row.email}</span>
                                    </div>
                                </td>
                                <td class="col-field-type-select min-width" data-name="Status"><span class="label {statusClass(row.status)}">{STATUS[row.status]}</span></td>
                                <td class="col-field-type-date min-width txt-hint" data-name="When" title={formatDate(row.created_at)}>{relativeTime(row.created_at)}</td>
                                <td class="col-meta min-width">
                                    {#if writable}
                                        <div class="inline-flex gap-sm">
                                            {#if row.status !== "approved"}
                                                <button type="button" class="btn sm secondary" disabled={working === row.id} onclick={() => setStatus(row, "approved")}><span class="txt">Approve</span></button>
                                            {/if}
                                            {#if row.status !== "rejected"}
                                                <button type="button" class="btn sm transparent secondary" disabled={working === row.id} onclick={() => setStatus(row, "rejected")}><span class="txt">Reject</span></button>
                                            {/if}
                                            <button type="button" class="btn circle sm transparent secondary" title="Reply publicly" aria-label="Reply" onclick={() => ((replying = row), (reply = row.reply ?? ""))}>
                                                <i class="ri-reply-line" aria-hidden="true"></i>
                                            </button>
                                            <button type="button" class="btn circle sm transparent secondary" title="Delete" aria-label="Delete" onclick={() => (removing = row)}>
                                                <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                            </button>
                                        </div>
                                    {/if}
                                </td>
                            </tr>
                        {/each}
                        {#if loading && !rows.length}
                            {#each Array(6) as _, i (i)}
                                <tr><td colspan="7"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !rows.length}
                            <tr><td colspan="7" class="txt-center txt-hint p-base">{list.params.status === "pending" ? "Nothing waiting. The storefront posts reviews to /x/reviews." : "Nothing here."}</td></tr>
                        {/if}
                    </tbody>
                </table>
            </div>
            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager {meta} {loading} noun="review" {perPage} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
                <div class="flex-fill"></div>
            </footer>
        {/if}
    </div>
</div>

<Drawer open={!!replying} size="sm" title="Reply to {replying?.name ?? ''}" onclose={() => (replying = null)}>
    {#if replying}
        <div class="txt-hint txt-sm m-b-sm">{stars(replying.rating)} — {replying.body}</div>
        <div class="field">
            <label for="review-reply">The store's reply</label>
            <textarea id="review-reply" rows="4" bind:value={reply} placeholder="Shown under the review on the product page"></textarea>
        </div>
    {/if}
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (replying = null)}><span class="txt">Cancel</span></button>
        <button type="button" class="btn" disabled={working !== 0} onclick={saveReply}><span class="txt">Save reply</span></button>
    {/snippet}
</Drawer>

<Confirm open={!!removing} title="Delete this review?" message="Gone for good. Reject it instead to keep the record." confirmLabel="Delete" danger onconfirm={remove} oncancel={() => (removing = null)} />

<style>
    .review-stars {
        color: #d97706;
        letter-spacing: 1px;
        white-space: nowrap;
    }
    .review-body {
        display: -webkit-box;
        -webkit-line-clamp: 2;
        -webkit-box-orient: vertical;
        overflow: hidden;
        white-space: normal;
    }
</style>
