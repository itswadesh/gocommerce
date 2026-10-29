<script>
    /**
     * Reviews: what shoppers said, and whether it shows.
     *
     * Moderation is the whole screen: the pending ones first, each with its
     * stars, its words and whether the reviewer bought the thing, and two
     * buttons. A reply is the store's public answer under the review.
     *
     * "Summarize reviews" on a row asks the AI gateway what shoppers praise
     * and complain about in that product's published reviews. The themes,
     * the counts and the rating come back from the engine; the drawer only
     * words them (reviewsummary.js).
     */
    import { base } from "$app/paths";
    import { api, can, query, request } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import FeatureSwitch from "$lib/components/FeatureSwitch.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import { titled } from "$lib/brand.svelte.js";
    import { rise } from "$lib/motion.js";
    import { citedReviews, costLine, ratingLine, readLine, summaryProblem, themeCount, topicLabel } from "$lib/reviewsummary.js";

    const PER_PAGE = 25;
    const readable = $derived(can("catalog.read"));
    const writable = $derived(can("catalog.write"));
    const missing = $derived(modulesKnown() && !hasModule("reviews"));
    const summarizable = $derived(can("reviews.read"));

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

    let summarizing = $state(null); // { id, title } of the product
    let digest = $state(null);
    let problem = $state(null);
    let thinking = $state(false);

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

    function summarize(row) {
        summarizing = { id: row.product_id, title: row.product };
        runSummary();
    }

    async function runSummary() {
        const target = summarizing;
        if (!target) return;
        thinking = true;
        digest = null;
        problem = null;
        try {
            const answer = await request("POST", `/api/admin/x/reviews/products/${target.id}/summarize`);
            if (summarizing === target) digest = answer;
        } catch (err) {
            if (summarizing === target) problem = summaryProblem(err);
        } finally {
            if (summarizing === target) thinking = false;
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

<svelte:head><title>{titled("Reviews")}</title></svelte:head>

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

        {#if !missing && readable}
            <!-- The feature's switch and settings live here, beside it (D132). -->
            <FeatureSwitch pluginKey="product-reviews" />
        {/if}

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
                                    <a href="{base}/dash/products/{row.product_id}" class="txt-bold txt-ellipsis">{row.product || "product #" + row.product_id}</a>
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
                                <td class="col-meta min-width row-actions">
                                    <div class="inline-flex gap-sm">
                                        {#if summarizable && row.product}
                                            <button
                                                type="button"
                                                class="btn circle sm transparent secondary summarize-btn"
                                                title="Summarize reviews"
                                                aria-label="Summarize reviews of {row.product}"
                                                onclick={() => summarize(row)}
                                            >
                                                <i class="ri-sparkling-line" aria-hidden="true"></i>
                                            </button>
                                        {/if}
                                        {#if writable}
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
                                        {/if}
                                    </div>
                                </td>
                            </tr>
                        {/each}
                        {#if loading && !rows.length}
                            {#each Array(6) as _, i (i)}
                                <tr><td colspan="7"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !rows.length}
                            <tr class="empty-row">
                                <td colspan="7">
                                    {#if list.params.q || (list.params.status !== "pending" && list.params.status !== "any")}
                                        <EmptyState
                                            icon="ri-filter-off-line"
                                            title="No reviews match these filters"
                                            text="Try another search or status, or clear them to go back to what is waiting."
                                            onclear={() => {
                                                    draft = "";
                                                    list.clear();
                                                }}
                                        />
                                    {:else if list.params.status === "pending"}
                                        <EmptyState
                                            icon="ri-chat-check-line"
                                            title="No reviews waiting"
                                            text="Shoppers write reviews on the storefront, which posts them to /x/reviews. Each one waits here until you approve or reject it."
                                        >
                                            <button type="button" class="btn secondary" onclick={() => list.set({ status: "any" })}>
                                                <i class="ri-list-check" aria-hidden="true"></i>
                                                <span class="txt">See every review</span>
                                            </button>
                                        </EmptyState>
                                    {:else}
                                        <EmptyState
                                            icon="ri-star-line"
                                            title="No reviews yet"
                                            text="Shoppers write reviews on the storefront, which posts them to /x/reviews. Each one waits here until you approve or reject it."
                                        />
                                    {/if}
                                </td>
                            </tr>
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

{#snippet themeList(heading, themes, empty)}
    <h3 class="digest-heading">{heading}</h3>
    {#if themes.length}
        <ul class="digest-themes">
            {#each themes as theme, i (i)}
                <li>
                    <details class="digest-theme">
                        <summary>
                            <i class="ri-arrow-right-s-line digest-chevron" aria-hidden="true"></i>
                            <span class="digest-text">
                                <span class="digest-label">{theme.label}</span>
                                <span class="digest-meta">{topicLabel(theme.topic)} · {themeCount(theme)}</span>
                            </span>
                        </summary>
                        <ul class="digest-cited">
                            {#each citedReviews(theme, digest.reviews) as cited (cited.id)}
                                <li>
                                    <span class="review-stars" title="{cited.rating} of 5">{stars(cited.rating)}</span>
                                    {#if cited.title}<span class="txt-bold">{cited.title}</span>{/if}
                                    <span class="digest-body">{cited.body}</span>
                                </li>
                            {/each}
                        </ul>
                    </details>
                </li>
            {/each}
        </ul>
    {:else}
        <p class="tw:text-sm tw:text-muted-foreground">{empty}</p>
    {/if}
{/snippet}

<Drawer open={!!summarizing} title="What shoppers say" onclose={() => (summarizing = null)}>
    {#if summarizing}
        <div class="digest" aria-live="polite" aria-busy={thinking}>
            <a class="txt-bold" href="{base}/dash/products/{summarizing.id}">{summarizing.title}</a>
            {#if thinking}
                <p class="tw:text-sm tw:text-muted-foreground">Reading the published reviews…</p>
                <span class="skeleton-loader"></span>
                <span class="skeleton-loader"></span>
                <span class="skeleton-loader"></span>
            {:else if problem}
                <section class="tw:rounded-xl tw:border tw:p-4" role="status" in:rise>
                    <h3 class="tw:text-sm tw:font-semibold tw:mb-1">{problem.title}</h3>
                    <p class="tw:text-sm tw:text-muted-foreground tw:mb-3">{problem.text}</p>
                    {#if problem.action === "settings"}
                        <a class="btn secondary" href="{base}/dash/plugins?open=ai-gateway">
                            <i class="ri-settings-3-line" aria-hidden="true"></i>
                            <span class="txt">AI gateway settings</span>
                        </a>
                    {:else if problem.action === "credit" && can("ai.read")}
                        <a class="btn secondary" href="{base}/dash/ai">
                            <i class="ri-sparkling-2-line" aria-hidden="true"></i>
                            <span class="txt">AI credit</span>
                        </a>
                    {:else if problem.action === "retry"}
                        <button type="button" class="btn secondary summarize-btn" onclick={runSummary}>
                            <i class="ri-refresh-line" aria-hidden="true"></i>
                            <span class="txt">Try again</span>
                        </button>
                    {/if}
                </section>
            {:else if digest}
                <div class="digest-result" in:rise>
                    <p class="tw:text-sm"><span class="review-stars" aria-hidden="true">★</span> {ratingLine(digest)}</p>
                    {#if readLine(digest)}<p class="tw:text-sm tw:text-muted-foreground">{readLine(digest)}</p>{/if}
                    {#if digest.state === "summarized"}
                        {@render themeList("What shoppers praise", digest.praise, "No praise came up.")}
                        {@render themeList("What shoppers complain about", digest.complaints, "No complaints came up.")}
                        <p class="tw:text-xs tw:text-muted-foreground">
                            Themes are written by AI from the reviews' words; the counts and the rating come from the reviews themselves. {costLine(digest)}
                        </p>
                    {/if}
                </div>
            {/if}
        </div>
    {/if}
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (summarizing = null)}><span class="txt">Close</span></button>
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

    .digest,
    .digest-result {
        display: flex;
        flex-direction: column;
        gap: 12px;
    }
    .digest-heading {
        font-size: 14px;
        font-weight: 600;
        margin: 4px 0 -4px;
    }
    .digest-themes,
    .digest-cited {
        list-style: none;
        margin: 0;
        padding: 0;
    }
    .digest-themes > li,
    .digest-cited > li {
        margin: 0;
    }
    .digest-theme > summary {
        display: flex;
        gap: 6px;
        align-items: flex-start;
        padding: 4px 8px;
        border-radius: var(--borderRadius);
        cursor: pointer;
        list-style: none;
        transition: background-color 150ms ease-out;
    }
    .digest-theme > summary::-webkit-details-marker {
        display: none;
    }
    .digest-theme > summary:hover {
        background: var(--surfaceAlt1Color);
    }
    .digest-text {
        display: flex;
        flex: 1;
        flex-wrap: wrap;
        align-items: baseline;
        column-gap: 8px;
    }
    .digest-label {
        font-weight: 500;
    }
    .digest-meta {
        margin-left: auto;
        font-size: 12px;
        color: var(--txtHintColor);
        white-space: nowrap;
    }
    .digest-chevron {
        line-height: 1.4;
        transition: transform 150ms ease-out;
    }
    .digest-theme[open] .digest-chevron {
        transform: rotate(90deg);
    }
    .digest-cited {
        display: flex;
        flex-direction: column;
        gap: 8px;
        margin: 4px 0 8px 28px;
        font-size: 13px;
    }
    .digest-cited > li {
        display: flex;
        flex-direction: column;
        gap: 2px;
    }
    .digest-body {
        color: var(--txtHintColor);
        overflow-wrap: anywhere;
    }
    .digest-theme[open] .digest-cited {
        animation: digestFade 200ms ease-out;
    }
    /* The row's buttons become a phone's only way to moderate or summarize,
       so they take a finger's width there. */
    @media (max-width: 600px) {
        .row-actions .btn {
            min-width: 44px;
            min-height: 44px;
        }
    }
    @media (prefers-reduced-motion: no-preference) {
        .summarize-btn {
            transition:
                transform 150ms ease-out,
                background-color 150ms ease-out;
        }
        .summarize-btn:active:not(:disabled) {
            transform: scale(0.97);
        }
        .digest-theme[open] .digest-cited {
            animation-name: digestRise;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .digest-chevron {
            transition: none;
        }
    }
    @keyframes digestFade {
        from {
            opacity: 0;
        }
    }
    @keyframes digestRise {
        from {
            opacity: 0;
            transform: translateY(-4px);
        }
    }
</style>
