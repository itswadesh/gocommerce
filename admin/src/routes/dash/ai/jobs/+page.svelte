<script>
    /**
     * AI › Jobs: model work queued by the store, and the work that stopped.
     *
     * Every job, newest first, with the status filter to narrow it: the two
     * states that need a person, "Needs a person" and "Gave up", are both a
     * choice away, and opening on one of them would hide the other. A job is
     * retried by the gateway only
     * when the provider was down or busy. Anything else (a refusal, an answer
     * that failed its checks, the cap, a missing price) stops at "Needs a
     * person" with its reason, and the Retry here is that person's answer once
     * the cause is fixed.
     */
    import { base } from "$app/paths";
    import { api, query } from "$lib/api.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { JOB_OPTIONS, jobLabel, jobClass, canRetry, canCancel } from "$lib/ai.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const API = "/api/admin/x/ai-gateway";
    const readable = $derived(can("ai.read"));
    const operable = $derived(can("ai.configure"));
    const missing = $derived(modulesKnown() && !hasModule("ai-gateway"));

    const list = listState({ status: "", page: 1, limit: 25 });
    const perPage = $derived(list.params.limit);

    let jobs = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let working = $state(0);
    let flashed = $state(0);
    let cancelling = $state(null);
    let confirmOpen = $state(false);
    let reqId = 0;

    $effect(() => {
        list.params;
        if (readable && hasModule("ai-gateway")) load();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const p = list.params;
            const result = await api.get(`${API}/jobs` + query({ status: p.status, page: p.page, limit: p.limit }));
            if (mine !== reqId) return;
            jobs = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    async function act(job, verb) {
        working = job.id;
        try {
            const updated = await api.post(`${API}/jobs/${job.id}/${verb}`);
            jobs = jobs.map((j) => (j.id === job.id ? updated : j));
            flashed = job.id;
            setTimeout(() => (flashed === job.id ? (flashed = 0) : null), 900);
            toast.success(verb === "retry" ? `Job #${job.id} is queued again` : `Job #${job.id} is cancelled`);
        } catch (err) {
            toast.error(err);
        } finally {
            working = 0;
        }
    }

    function when(job) {
        if (job.finished_at) return { label: "Finished", at: job.finished_at };
        if (job.status === "queued" && job.attempts > 0) return { label: "Next try", at: job.next_run_at };
        return { label: "Queued", at: job.created_at };
    }
</script>

<svelte:head><title>{titled("AI jobs")}</title></svelte:head>

{#if !readable}
    <NoAccess right="ai.read" what="the AI job queue" />
{:else}
    <div class="page page-ai shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">AI</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Jobs</div>
                </nav>
                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        disabled={loading}
                        onclick={load}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>
                {#if !missing}
                    <div class="page-header-primary-btns">
                        <div class="field ai-filter">
                            <Select
                                ariaLabel="Status"
                                value={list.params.status}
                                options={JOB_OPTIONS}
                                onchange={(v) => list.set({ status: v })}
                            />
                        </div>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="ai-gateway" what="The AI job queue is served by ext/ai-gateway, and this binary does not have it." />
            {:else}
                <p class="field-help m-b-base">
                    Model work the store queued. The gateway tries again by itself only when the
                    provider was down or busy, up to twelve times. Anything else stops here with its
                    reason until somebody retries it or cancels it.
                </p>

                <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && jobs.length > 0}>
                    <table class="table responsive-table">
                        <thead>
                            <tr>
                                <th class="col-field-name-id">Job</th>
                                <th class="min-width">Status</th>
                                <th class="col-field-type-number min-width">Attempts</th>
                                <th>Last error</th>
                                <th class="col-field-type-date min-width">When</th>
                                <th class="col-meta min-width"></th>
                            </tr>
                        </thead>
                        <tbody>
                            {#if loading && !jobs.length}
                                {#each Array(4) as _, i (i)}
                                    <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {/if}
                            {#each jobs as job (job.id)}
                                {@const w = when(job)}
                                <tr class:ai-flash={flashed === job.id}>
                                    <td class="col-field-name-id" data-name="Job">
                                        <div class="row-name row-name-stacked">
                                            <span class="txt-bold txt-code">{job.task}</span>
                                            <span class="txt-hint txt-sm">
                                                #{job.id}{#if job.execution_id} · last run #{job.execution_id}{/if}
                                            </span>
                                        </div>
                                    </td>
                                    <td class="min-width" data-name="Status">
                                        <span class="label {jobClass(job.status)}">
                                            {#if job.status === "done"}
                                                <i class="ri-check-line state-tick" aria-hidden="true"></i>
                                            {/if}
                                            {jobLabel(job.status)}
                                        </span>
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Attempts">
                                        {job.attempts}<span class="txt-hint txt-sm"> of {job.max_attempts}</span>
                                    </td>
                                    <td data-name="Last error">
                                        {#if job.last_error}
                                            <span class="txt-danger txt-sm ai-error" title={job.last_error}>{job.last_error}</span>
                                        {:else}
                                            <span class="txt-hint">—</span>
                                        {/if}
                                    </td>
                                    <td class="col-field-type-date min-width txt-hint" data-name={w.label} title={formatDate(w.at)}>
                                        {w.label} {relativeTime(w.at)}
                                    </td>
                                    <td class="col-meta min-width row-actions">
                                        {#if operable}
                                            <div class="inline-flex gap-5">
                                                {#if canRetry(job.status)}
                                                    <button
                                                        type="button"
                                                        class="btn sm secondary"
                                                        class:loading={working === job.id}
                                                        disabled={working === job.id}
                                                        title="Put it back in the queue with a fresh set of attempts"
                                                        onclick={() => act(job, "retry")}
                                                    >
                                                        <span class="txt">Retry</span>
                                                    </button>
                                                {/if}
                                                {#if canCancel(job.status)}
                                                    <button
                                                        type="button"
                                                        class="btn sm transparent secondary"
                                                        disabled={working === job.id}
                                                        onclick={() => {
                                                            cancelling = job;
                                                            confirmOpen = true;
                                                        }}
                                                    >
                                                        <span class="txt">Cancel</span>
                                                    </button>
                                                {/if}
                                            </div>
                                        {/if}
                                    </td>
                                </tr>
                            {/each}
                            {#if !loading && !jobs.length}
                                <tr class="empty-row">
                                    <td colspan="6">
                                        {#if list.params.status === "failed" || list.params.status === "dead"}
                                            <EmptyState
                                                icon="ri-checkbox-circle-line"
                                                title="Nothing needs a person"
                                                text="No AI job has stopped in this state. Jobs the gateway is still trying are under Waiting."
                                                onclear={() => list.set({ status: "" })}
                                                clearLabel="Every job"
                                            />
                                        {:else if list.params.status}
                                            <EmptyState
                                                icon="ri-filter-off-line"
                                                title="No jobs in this state"
                                                text="Try another status, or choose Every job."
                                                onclear={() => list.set({ status: "" })}
                                            />
                                        {:else}
                                            <EmptyState
                                                icon="ri-sparkling-2-line"
                                                title="No AI jobs yet"
                                                text="Work a module queues for a model appears here, from the moment it is queued to its answer."
                                            >
                                                <a class="btn secondary" href="{base}/dash/ai">
                                                    <i class="ri-line-chart-line" aria-hidden="true"></i>
                                                    <span class="txt">See usage</span>
                                                </a>
                                            </EmptyState>
                                        {/if}
                                    </td>
                                </tr>
                            {/if}
                        </tbody>
                    </table>
                </div>

                <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                    <Pager
                        {meta}
                        {loading}
                        noun="job"
                        {perPage}
                        onpage={(n) => list.setPage(n)}
                        onperpage={(n) => list.set({ limit: n })}
                    />
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>

    <Confirm
        bind:open={confirmOpen}
        title="Cancel this job?"
        message="It leaves the queue for good. A call already in flight is not stopped, and its record is kept. Retry brings it back."
        confirmLabel="Cancel job"
        danger
        onconfirm={() => act(cancelling, "cancel")}
    />
{/if}
