<script>
    /**
     * Jobs: what the store is doing in the background.
     *
     * Three things run behind the panel, and "is it still running" is one
     * question about all of them: imports from Amazon (a job per product),
     * the outbox (every event waiting to reach the outside world, and the
     * ones that gave up), and webhook deliveries. Each has its own screen
     * for the detail; this one is the glance. store.operate, as the outbox
     * screen is.
     */
    import { base } from "$app/paths";
    import { api, can, events, query } from "$lib/api.js";
    import { hasModule } from "$lib/modules.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";

    const allowed = $derived(can("store.operate"));
    const hasAmazon = $derived(hasModule("import-amazon"));
    const hasWebhooks = $derived(hasModule("webhooks"));

    let loading = $state(true);
    let pending = $state({ rows: [], total: 0 });
    let dead = $state({ rows: [], total: 0 });
    let imports = $state([]);
    let deliveries = $state({ rows: [], total: 0 });
    let retrying = $state(0);

    $effect(() => {
        hasAmazon;
        hasWebhooks;
        if (allowed) load();
    });

    async function load() {
        loading = true;
        try {
            const [p, d, jobs, wh] = await Promise.all([
                events.list({ state: "pending", limit: 8 }),
                events.list({ state: "dead", limit: 8 }),
                hasAmazon ? api.get("/api/admin/x/import-amazon/jobs" + query({ limit: 10 })) : null,
                hasWebhooks ? api.get("/api/admin/x/webhooks/deliveries" + query({ status: "failed", limit: 8 })) : null,
            ]);
            pending = { rows: p.data ?? [], total: p.meta?.total ?? 0 };
            dead = { rows: d.data ?? [], total: d.meta?.total ?? 0 };
            imports = jobs?.data ?? [];
            deliveries = { rows: wh?.data ?? [], total: wh?.meta?.total ?? 0 };
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function retry(row) {
        retrying = row.id;
        try {
            await events.retry(row.id);
            toast.success("Queued again");
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            retrying = 0;
        }
    }

    function importClass(status) {
        if (status === "done" || status === "imported" || status === "succeeded") return "label-success";
        if (status === "failed" || status === "error") return "label-danger";
        if (status === "running" || status === "queued") return "label-warning";
        return "";
    }
</script>

<svelte:head><title>Jobs · GoCommerce</title></svelte:head>

<div class="page page-jobs shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Jobs</div>
            </nav>
            <div class="inline-flex gap-sm">
                <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>
        </header>

        {#if !allowed}
            <NoAccess right="store.operate" what="the store's background work" />
        {:else}
            <div class="tw:grid tw:grid-cols-1 tw:gap-4 tw:sm:grid-cols-3">
                <a href="{base}/settings/events?state=pending" class="job-tile">
                    <span class="job-tile-label">Events waiting</span>
                    <span class="job-tile-value">{loading && !pending.total ? "…" : pending.total}</span>
                    <span class="job-tile-hint">In the outbox, about to go out</span>
                </a>
                <a href="{base}/settings/events?state=dead" class="job-tile" class:bad={dead.total > 0}>
                    <span class="job-tile-label">Dead letters</span>
                    <span class="job-tile-value">{loading && !dead.total ? "…" : dead.total}</span>
                    <span class="job-tile-hint">Gave up after twelve attempts</span>
                </a>
                {#if hasWebhooks}
                    <a href="{base}/settings/webhooks" class="job-tile" class:bad={deliveries.total > 0}>
                        <span class="job-tile-label">Failed webhook deliveries</span>
                        <span class="job-tile-value">{loading && !deliveries.total ? "…" : deliveries.total}</span>
                        <span class="job-tile-hint">Endpoints that did not answer 2xx</span>
                    </a>
                {:else}
                    <div class="job-tile">
                        <span class="job-tile-label">Webhooks</span>
                        <span class="job-tile-value">—</span>
                        <span class="job-tile-hint">The webhooks module is not installed</span>
                    </div>
                {/if}
            </div>

            {#if hasAmazon}
                <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Imports from Amazon</h2>
                <div class="page-table-wrapper tw:rounded-xl tw:border">
                    <table class="table responsive-table">
                        <thead>
                            <tr>
                                <th class="col-field-name-id">Product</th>
                                <th class="col-field-type-select min-width">Status</th>
                                <th class="col-field-type-text">Step</th>
                                <th class="col-field-type-date min-width">Started</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#each imports as job (job.id)}
                                <tr>
                                    <td class="col-field-name-id" data-name="Product">
                                        <div class="row-name row-name-stacked">
                                            {#if job.product_id}
                                                <a href="{base}/products/{job.product_id}" class="txt-bold txt-ellipsis">{job.asin || "#" + job.id}</a>
                                            {:else}
                                                <span class="txt-bold txt-ellipsis">{job.asin || "#" + job.id}</span>
                                            {/if}
                                            <span class="txt-hint txt-sm">{job.url}</span>
                                        </div>
                                    </td>
                                    <td class="col-field-type-select min-width" data-name="Status"><span class="label {importClass(job.status)}">{job.status}</span></td>
                                    <td class="col-field-type-text" data-name="Step" title={job.message}>
                                        <span class="txt-ellipsis">{job.message || job.step || "—"}</span>
                                    </td>
                                    <td class="col-field-type-date min-width txt-hint" data-name="Started" title={formatDate(job.created_at)}>{relativeTime(job.created_at)}</td>
                                </tr>
                            {/each}
                            {#if !loading && !imports.length}
                                <tr><td colspan="4" class="txt-center txt-hint p-base">No imports yet. Products › Import from Amazon starts one.</td></tr>
                            {/if}
                        </tbody>
                    </table>
                </div>
            {/if}

            <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Dead letters</h2>
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead>
                        <tr>
                            <th class="col-field-name-id">Event</th>
                            <th class="col-field-type-text">Last error</th>
                            <th class="col-field-type-number min-width">Attempts</th>
                            <th class="col-field-type-date min-width">Created</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each dead.rows as row (row.id)}
                            <tr>
                                <td class="col-field-name-id" data-name="Event"><span class="txt-code">{row.name}</span> <span class="txt-hint txt-sm">#{row.id}</span></td>
                                <td class="col-field-type-text" data-name="Last error" title={row.last_error}><span class="txt-danger txt-sm txt-ellipsis">{row.last_error || "—"}</span></td>
                                <td class="col-field-type-number min-width" data-name="Attempts">{row.attempts}</td>
                                <td class="col-field-type-date min-width txt-hint" data-name="Created" title={formatDate(row.created_at)}>{relativeTime(row.created_at)}</td>
                                <td class="col-meta min-width">
                                    <button type="button" class="btn sm secondary" class:loading={retrying === row.id} disabled={retrying === row.id} onclick={() => retry(row)}>
                                        <span class="txt">Retry</span>
                                    </button>
                                </td>
                            </tr>
                        {/each}
                        {#if !loading && !dead.rows.length}
                            <tr><td colspan="5" class="txt-center txt-hint p-base">Nothing has given up. Every event reached where it was going.</td></tr>
                        {/if}
                    </tbody>
                </table>
            </div>
            {#if dead.total > dead.rows.length}
                <p class="txt-hint txt-sm m-t-sm">{dead.total - dead.rows.length} more under <a href="{base}/settings/events?state=dead">Event log</a>.</p>
            {/if}

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <span class="txt txt-hint">The full outbox, with every event's history, is under Settings › Event log.</span>
            </footer>
        {/if}
    </div>
</div>

<style>
    .job-tile {
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding: 14px 16px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--baseRadius);
        background: var(--baseColor);
        color: inherit;
        text-decoration: none;
    }
    .job-tile.bad {
        border-color: var(--dangerColor);
    }
    .job-tile-label {
        font-size: var(--xsFontSize);
        font-weight: 500;
        letter-spacing: 0.04em;
        text-transform: uppercase;
        color: var(--txtHintColor);
    }
    .job-tile-value {
        font-size: 24px;
        font-weight: 700;
        font-variant-numeric: tabular-nums;
        line-height: 1.1;
    }
    .job-tile-hint {
        font-size: var(--xsFontSize);
        color: var(--txtHintColor);
    }
</style>
