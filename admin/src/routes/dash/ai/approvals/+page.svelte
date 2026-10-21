<script>
    /**
     * AI › Approvals: what an agent asked to do, waiting for a person.
     *
     * An agent never runs a change that reaches a customer, moves money or
     * stock, or changes a price or a page. It files a proposal, and it waits
     * here. Review opens the proposal with its preview recomputed against the
     * store as it is now — not as it was when the agent asked — and marks what
     * moved since. Approve runs it as you, through the same service the panel's
     * own button calls, so the audit trail names you and the proposal.
     *
     * Two gates are shown before the server applies them, so nobody is handed
     * a button that can only refuse: approving needs the action's own right as
     * well as ai.approve, and a proposal whose action this build does not have
     * can only be rejected.
     *
     * Approve sends the hash of the preview on screen. If the store moved
     * while the drawer was open, the engine runs nothing and answers 409 with
     * the preview as it is now; the drawer shows that one, marks what moved
     * from what was on screen, and waits for the person to look again.
     */
    import { api, query } from "$lib/api.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { rightLabel } from "$lib/rights.js";
    import { rise } from "$lib/motion.js";
    import { APPROVAL_OPTIONS, approvalLabel, approvalClass, entries, movedKeys, summary } from "$lib/ai.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";

    const API = "/api/admin/x/ai-gateway/approvals";
    const allowed = $derived(can("ai.approve"));
    const missing = $derived(modulesKnown() && !hasModule("ai-gateway"));

    const list = listState({ status: "pending", page: 1, limit: 25 });
    const perPage = $derived(list.params.limit);

    let approvals = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let reqId = 0;

    let open = $state(false);
    let current = $state(null);
    let reviewing = $state(false);
    let deciding = $state("");
    let outcome = $state(null);
    let rejectOpen = $state(false);
    // What was on screen when Approve met a store that had moved, and what
    // the engine said about it.
    let shownBefore = $state(null);
    let movedNotice = $state("");

    $effect(() => {
        list.params;
        if (allowed && hasModule("ai-gateway")) load();
    });

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const p = list.params;
            const result = await api.get(API + query({ status: p.status, page: p.page, limit: p.limit }));
            if (mine !== reqId) return;
            approvals = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    async function review(a) {
        current = a;
        outcome = null;
        shownBefore = null;
        movedNotice = "";
        open = true;
        reviewing = true;
        try {
            current = await api.get(`${API}/${a.id}`);
        } catch (err) {
            toast.error(err);
        } finally {
            reviewing = false;
        }
    }

    async function decide(verb) {
        deciding = verb;
        try {
            const body = verb === "approve" ? { preview_hash: current.preview_hash } : undefined;
            const updated = await api.post(`${API}/${current.id}/${verb}`, body);
            shownBefore = null;
            movedNotice = "";
            current = updated;
            outcome = updated;
            approvals = approvals.map((a) => (a.id === updated.id ? updated : a));
            if (updated.status === "executed") toast.success(`Proposal #${updated.id} approved and done`);
            else if (updated.status === "failed") toast.warning(`Proposal #${updated.id} was approved, and the action failed`);
            else if (updated.status === "rejected") toast.success(`Proposal #${updated.id} rejected`);
        } catch (err) {
            if (err?.status === 409 && err.details?.preview_hash) {
                shownBefore = current.preview;
                movedNotice = err.message;
                current = {
                    ...current,
                    preview: err.details.preview,
                    preview_hash: err.details.preview_hash,
                    changed: true,
                };
            } else {
                toast.error(err);
            }
        } finally {
            deciding = "";
        }
    }

    /* The fields whose value moved: since what was on screen, when Approve
       met a store that had moved, else since the proposal was filed. */
    const moved = $derived(
        shownBefore
            ? movedKeys(shownBefore, current?.preview)
            : current?.changed
              ? movedKeys(current.proposed_preview, current?.preview)
              : new Set(),
    );
    const mayApprove = $derived(current && current.right && can(current.right));
    const open_ = $derived(current?.status === "pending");
</script>

<svelte:head><title>AI approvals · GoCommerce</title></svelte:head>

{#if !allowed}
    <NoAccess right="ai.approve" what="the AI approval queue" />
{:else}
    <div class="page page-ai shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">AI</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Approvals</div>
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
                                options={APPROVAL_OPTIONS}
                                onchange={(v) => list.set({ status: v })}
                            />
                        </div>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="ai-gateway" what="The approval queue is served by ext/ai-gateway, and this binary does not have it." />
            {:else}
                <p class="field-help m-b-base">
                    When an agent asks to change something a customer would see, or money, stock or a
                    price, the request waits here for a person. Review shows what it would do to the
                    store as it is now; approving runs it as you. A request left for a week expires.
                </p>

                <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && approvals.length > 0}>
                    <table class="table responsive-table">
                        <thead>
                            <tr>
                                <th class="col-field-name-id">Proposal</th>
                                <th>What it would do</th>
                                <th>Asked by</th>
                                <th class="min-width">Status</th>
                                <th class="col-meta min-width"></th>
                            </tr>
                        </thead>
                        <tbody>
                            {#if loading && !approvals.length}
                                {#each Array(3) as _, i (i)}
                                    <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {/if}
                            {#each approvals as a (a.id)}
                                <tr>
                                    <td class="col-field-name-id" data-name="Proposal">
                                        <div class="row-name row-name-stacked">
                                            <span class="txt-bold txt-code">{a.kind}</span>
                                            <span class="txt-hint txt-sm">#{a.id}</span>
                                        </div>
                                    </td>
                                    <td data-name="What it would do">
                                        <span class="ai-summary" title={summary(a.preview)}>{summary(a.preview)}</span>
                                    </td>
                                    <td data-name="Asked by">
                                        <div class="row-name row-name-stacked">
                                            <span class="txt-ellipsis">{a.requested_by_label || "—"}</span>
                                            <span class="txt-hint txt-sm" title={formatDate(a.created_at)}>{relativeTime(a.created_at)}</span>
                                        </div>
                                    </td>
                                    <td class="min-width" data-name="Status">
                                        <span class="label {approvalClass(a.status)}">
                                            {#if a.status === "executed"}
                                                <i class="ri-check-line state-tick" aria-hidden="true"></i>
                                            {/if}
                                            {approvalLabel(a.status)}
                                        </span>
                                        {#if a.status === "pending"}
                                            <div class="txt-hint txt-sm" title={formatDate(a.expires_at)}>
                                                expires {relativeTime(a.expires_at)}
                                            </div>
                                        {/if}
                                    </td>
                                    <td class="col-meta min-width row-actions">
                                        <button type="button" class="btn sm {a.status === 'pending' ? '' : 'secondary'}" onclick={() => review(a)}>
                                            <span class="txt">{a.status === "pending" ? "Review" : "Open"}</span>
                                        </button>
                                    </td>
                                </tr>
                            {/each}
                            {#if !loading && !approvals.length}
                                <tr class="empty-row">
                                    <td colspan="5">
                                        {#if list.params.status === "pending"}
                                            <EmptyState
                                                icon="ri-shield-check-line"
                                                title="Nothing is waiting for you"
                                                text="When an agent asks to change something that needs a person, it waits here with what it would do."
                                            />
                                        {:else}
                                            <EmptyState
                                                icon="ri-filter-off-line"
                                                title="No proposals in this state"
                                                text="Try another status, or go back to what is waiting."
                                                onclear={() => list.set({ status: "pending" })}
                                                clearLabel="Waiting for a person"
                                            />
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
                        noun="proposal"
                        {perPage}
                        onpage={(n) => list.setPage(n)}
                        onperpage={(n) => list.set({ limit: n })}
                    />
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>

    <Drawer open={open} title={current ? `Proposal #${current.id}` : "Proposal"} onclose={() => (open = false)}>
        {#if current}
            <div class="ai-proposal">
                <div class="ai-run-head">
                    <span class="txt-bold txt-code">{current.kind}</span>
                    {#key current.status}
                        <span class="label {approvalClass(current.status)} ai-status" in:rise>
                            {#if current.status === "executed"}
                                <i class="ri-check-line state-tick" aria-hidden="true"></i>
                            {/if}
                            {approvalLabel(current.status)}
                        </span>
                    {/key}
                </div>
                <dl class="ai-facts">
                    <dt>Asked by</dt>
                    <dd>{current.requested_by_label || "—"}, {relativeTime(current.created_at)}</dd>
                    {#if current.status === "pending"}
                        <dt>Expires</dt>
                        <dd title={formatDate(current.expires_at)}>{relativeTime(current.expires_at)}</dd>
                    {/if}
                    {#if current.decided_by_email}
                        <dt>Decided by</dt>
                        <dd>{current.decided_by_email}, {relativeTime(current.decided_at)}</dd>
                    {/if}
                    {#if current.right}
                        <dt>Needs</dt>
                        <dd>{rightLabel(current.right)} <span class="txt-hint txt-sm">({current.right})</span></dd>
                    {/if}
                    <dt>Trace</dt>
                    <dd><span class="txt-code txt-sm">{current.trace_id || "—"}</span></dd>
                </dl>

                <div aria-live="polite">
                    {#if movedNotice}
                        <div class="alert warning m-b-sm ai-moved-notice" transition:rise>
                            <i class="ri-refresh-line" aria-hidden="true"></i>
                            <div><strong>{movedNotice}</strong> Nothing ran. The marked lines are what changed since you opened it.</div>
                        </div>
                    {:else if current.stale}
                        <div class="alert warning m-b-sm" transition:rise>
                            <i class="ri-error-warning-line" aria-hidden="true"></i>
                            <div>{current.stale}</div>
                        </div>
                    {:else if current.changed}
                        <div class="alert warning m-b-sm" transition:rise>
                            <i class="ri-exchange-line" aria-hidden="true"></i>
                            <div>The store has changed since this was proposed. What it would do now is below; the marked lines moved.</div>
                        </div>
                    {/if}
                    {#if outcome?.status === "failed"}
                        <div class="alert danger m-b-sm" transition:rise>
                            <i class="ri-close-circle-line" aria-hidden="true"></i>
                            <div><strong>The action failed.</strong> {outcome.error}</div>
                        </div>
                    {/if}
                </div>

                <h6 class="section-title">
                    {current.status === "pending" ? "What it would do now" : "What was decided on"}
                    {#if reviewing}<span class="loader sm"></span>{/if}
                </h6>
                <dl class="ai-preview">
                    {#each entries(current.preview) as [k, v] (k)}
                        <div class:moved={moved.has(k)}>
                            <dt>{k}</dt>
                            <dd>{v}</dd>
                        </div>
                    {/each}
                </dl>
                {#if current.changed && current.proposed_preview}
                    <details class="ai-before">
                        <summary>As it was when proposed</summary>
                        <dl class="ai-preview">
                            {#each entries(current.proposed_preview) as [k, v] (k)}
                                <div>
                                    <dt>{k}</dt>
                                    <dd>{v}</dd>
                                </div>
                            {/each}
                        </dl>
                    </details>
                {/if}
                {#if current.result}
                    <h6 class="section-title">What it returned</h6>
                    <dl class="ai-preview">
                        {#each entries(current.result) as [k, v] (k)}
                            <div>
                                <dt>{k}</dt>
                                <dd>{v}</dd>
                            </div>
                        {/each}
                    </dl>
                {/if}
                {#if current.status === "failed" && current.error && outcome?.status !== "failed"}
                    <div class="alert danger m-t-sm">
                        <i class="ri-close-circle-line" aria-hidden="true"></i>
                        <div>{current.error}</div>
                    </div>
                {/if}
            </div>
        {/if}

        {#snippet footer()}
            <div class="ai-drawer-actions">
            {#if current && open_}
                {#if current.right && !mayApprove}
                    <span class="txt-hint txt-sm flex-fill">Approving needs “{rightLabel(current.right)}”, which your role does not carry.</span>
                {:else}
                    <span class="flex-fill"></span>
                {/if}
                <button
                    type="button"
                    class="btn transparent secondary"
                    disabled={!!deciding || (current.right && !mayApprove)}
                    onclick={() => (rejectOpen = true)}
                >
                    <span class="txt">Reject</span>
                </button>
                <button
                    type="button"
                    class="btn"
                    class:loading={deciding === "approve"}
                    disabled={!!deciding || !mayApprove || !!current.stale || reviewing}
                    onclick={() => decide("approve")}
                >
                    <i class="ri-check-line" aria-hidden="true"></i>
                    <span class="txt">Approve and run</span>
                </button>
            {:else}
                <span class="flex-fill"></span>
                <button type="button" class="btn secondary" onclick={() => (open = false)}>
                    <span class="txt">Close</span>
                </button>
            {/if}
            </div>
        {/snippet}
    </Drawer>

    <Confirm
        bind:open={rejectOpen}
        title="Reject this proposal?"
        message="It closes without running, and nothing it proposed changes."
        confirmLabel="Reject"
        danger
        onconfirm={() => decide("reject")}
    />
{/if}
