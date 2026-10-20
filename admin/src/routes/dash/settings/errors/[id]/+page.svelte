<script>
    /**
     * One error: what it says, where, how often, and its latest occurrences —
     * each with the request it happened on and the stack it came from (D86).
     *
     * Resolve says "fixed": the issue leaves the open list and comes back by
     * itself if it happens again. Ignore says "known and not worth fixing": it
     * stays out however often it happens, its count still kept.
     */
    import { page } from "$app/state";
    import { base } from "$app/paths";
    import { can, errors } from "$lib/api.js";
    import { copyText } from "$lib/clipboard.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import NoAccess from "$lib/components/NoAccess.svelte";

    const id = $derived(page.params.id);
    const readable = $derived(can("errors.read"));
    const writable = $derived(can("errors.write"));

    let issue = $state(null);
    let loading = $state(true);
    let missing = $state(false);
    let chosen = $state(0);
    let working = $state("");

    $effect(() => {
        id;
        if (readable) load();
    });

    async function load() {
        loading = true;
        try {
            issue = await errors.get(id);
            chosen = 0;
            missing = false;
        } catch (err) {
            if (err?.status === 404) missing = true;
            else toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function setStatus(status) {
        working = status;
        try {
            issue = await errors.setStatus(id, status);
            toast.success(status === "resolved" ? "Marked resolved" : status === "ignored" ? "Ignored" : "Reopened");
        } catch (err) {
            toast.error(err);
        } finally {
            working = "";
        }
    }

    const event = $derived(issue?.events?.[chosen] ?? null);

    /* The request, in the order a person reads it. */
    const CONTEXT = [
        ["method", "Method"],
        ["path", "Path"],
        ["url", "Page"],
        ["status", "Status"],
        ["operator", "Operator"],
        ["user_agent", "Browser"],
        ["version", "Engine version"],
    ];
    const contextRows = $derived(
        event ? CONTEXT.filter(([k]) => event.context?.[k] !== undefined && event.context?.[k] !== "").map(([k, label]) => [label, String(event.context[k])]) : [],
    );

    /* A Go stack is a function and its file on two lines, a browser's one
       frame a line; either way the frames from this code are the ones worth
       reading, and they are drawn stronger than the runtime's. */
    function frames(stack) {
        return (stack ?? "").split("\n").map((line) => ({
            line,
            mine: /gocommerce|\/_app\//.test(line) && !/\/runtime\/|net\/http/.test(line),
        }));
    }

    async function copyStack() {
        if (!event) return;
        const ok = await copyText(`${issue.title}\n\n${event.message}\n\n${event.stack}`);
        if (ok) toast.success("Copied");
    }
</script>

<svelte:head><title>{issue ? issue.title : "Error"} · GoCommerce</title></svelte:head>

<div class="page page-error shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <a class="breadcrumb-item tw:text-sm tw:text-muted-foreground" href="{base}/dash/settings/errors">Errors</a>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Issue #{id}</h1>
            </nav>
            {#if issue && writable}
                <div class="page-header-primary-btns">
                    {#if issue.status === "unresolved"}
                        <button type="button" class="btn sm" class:loading={working === "resolved"} disabled={!!working} onclick={() => setStatus("resolved")}>
                            <i class="ri-check-line" aria-hidden="true"></i>
                            <span class="txt">Resolve</span>
                        </button>
                        <button type="button" class="btn sm secondary" class:loading={working === "ignored"} disabled={!!working} onclick={() => setStatus("ignored")}>
                            <i class="ri-eye-off-line" aria-hidden="true"></i>
                            <span class="txt">Ignore</span>
                        </button>
                    {:else}
                        <button type="button" class="btn sm secondary" class:loading={working === "unresolved"} disabled={!!working} onclick={() => setStatus("unresolved")}>
                            <i class="ri-arrow-go-back-line" aria-hidden="true"></i>
                            <span class="txt">Reopen</span>
                        </button>
                    {/if}
                </div>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="errors.read" what="the store's errors" />
        {:else if loading && !issue}
            <div class="block txt-center p-base"><span class="loader lg"></span></div>
        {:else if missing}
            <div class="card err-card">
                <p>This error is not here: it was another store's, or the link is wrong.</p>
                <a class="btn sm secondary" href="{base}/dash/settings/errors"><span class="txt">All errors</span></a>
            </div>
        {:else if issue}
            <section class="card err-card err-head">
                <div class="err-head-title">
                    <span class="err-mark" class:panel={issue.source === "panel"} aria-hidden="true">
                        <i class={issue.source === "panel" ? "ri-window-line" : "ri-server-line"}></i>
                    </span>
                    <div class="err-head-text">
                        <h2 class="err-title">{issue.title}</h2>
                        {#if issue.culprit}<div class="txt-code txt-sm err-culprit">{issue.culprit}</div>{/if}
                    </div>
                    <span class="label {issue.status === 'unresolved' ? 'danger' : issue.status === 'resolved' ? 'success' : ''}">
                        {issue.status === "unresolved" ? "Unresolved" : issue.status === "resolved" ? "Resolved" : "Ignored"}
                    </span>
                </div>
                <div class="err-stats">
                    <div class="err-stat">
                        <div class="err-stat-label">Events</div>
                        <div class="err-stat-value">{issue.count.toLocaleString()}</div>
                    </div>
                    <div class="err-stat">
                        <div class="err-stat-label">Last seen</div>
                        <div class="err-stat-value" title={formatDate(issue.last_seen)}>{relativeTime(issue.last_seen)}</div>
                    </div>
                    <div class="err-stat">
                        <div class="err-stat-label">First seen</div>
                        <div class="err-stat-value" title={formatDate(issue.first_seen)}>{relativeTime(issue.first_seen)}</div>
                    </div>
                    <div class="err-stat">
                        <div class="err-stat-label">Where</div>
                        <div class="err-stat-value">{issue.source === "panel" ? "Panel" : "Server"}</div>
                    </div>
                </div>
            </section>

            {#if issue.events?.length}
                <div class="err-body">
                    <aside class="card err-card err-events" aria-label="Latest occurrences">
                        <h3 class="err-section">Latest {issue.events.length === 1 ? "occurrence" : `${issue.events.length} occurrences`}</h3>
                        <div class="err-event-list">
                            {#each issue.events as e, i (e.id)}
                                <button type="button" class="err-event" class:on={i === chosen} aria-pressed={i === chosen} onclick={() => (chosen = i)}>
                                    <span class="err-event-when">{formatDate(e.created_at)}</span>
                                    <span class="err-event-where txt-hint txt-sm">{e.context?.path ?? e.context?.url ?? ""}</span>
                                </button>
                            {/each}
                        </div>
                        {#if issue.count > issue.events.length}
                            <p class="txt-hint txt-sm">The {issue.events.length} most recent of {issue.count.toLocaleString()} are kept.</p>
                        {/if}
                    </aside>

                    {#if event}
                        <section class="card err-card err-event-detail">
                            <h3 class="err-section">Message</h3>
                            <pre class="err-message">{event.message}</pre>

                            {#if contextRows.length}
                                <h3 class="err-section">Request</h3>
                                <dl class="err-context">
                                    {#each contextRows as [label, value] (label)}
                                        <dt>{label}</dt>
                                        <dd>{value}</dd>
                                    {/each}
                                </dl>
                            {/if}

                            {#if event.stack}
                                <div class="err-stack-head">
                                    <h3 class="err-section">Stack trace</h3>
                                    <button type="button" class="btn sm transparent secondary" onclick={copyStack}>
                                        <i class="ri-file-copy-line" aria-hidden="true"></i>
                                        <span class="txt">Copy</span>
                                    </button>
                                </div>
                                <pre class="err-stack">{#each frames(event.stack) as f, i (i)}<span class:mine={f.mine}>{f.line}</span>
{/each}</pre>
                            {/if}
                        </section>
                    {/if}
                </div>
            {/if}
        {/if}
    </div>
</div>

<style>
    .err-card {
        margin: 0 0 12px;
    }
    .err-head-title {
        display: flex;
        align-items: flex-start;
        gap: 12px;
    }
    .err-head-text {
        flex: 1 1 auto;
        min-width: 0;
    }
    .err-title {
        margin: 0;
        font-size: 1.05rem;
        font-weight: 600;
        overflow-wrap: anywhere;
    }
    .err-culprit {
        margin-top: 4px;
        color: var(--txtHintColor);
        overflow-wrap: anywhere;
    }
    .err-mark {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 32px;
        height: 32px;
        flex: 0 0 auto;
        border-radius: var(--borderRadius);
        background: color-mix(in srgb, var(--dangerColor), transparent 88%);
        color: var(--dangerColor);
    }
    .err-mark.panel {
        background: color-mix(in srgb, var(--warningColor), transparent 85%);
        color: color-mix(in srgb, var(--warningColor), var(--txtPrimaryColor) 30%);
    }
    .err-stats {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(130px, 1fr));
        gap: 10px;
        margin-top: 14px;
        padding: 12px;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color);
    }
    .err-stat-label {
        font-size: 12px;
        font-weight: 600;
        letter-spacing: 0.04em;
        text-transform: uppercase;
        color: var(--txtHintColor);
    }
    .err-stat-value {
        margin-top: 2px;
        font-weight: 600;
        font-variant-numeric: tabular-nums;
    }
    .err-body {
        display: grid;
        grid-template-columns: minmax(200px, 280px) 1fr;
        gap: 12px;
        align-items: start;
    }
    /* Side by side, not stacked: the skin's card-after-card margin would
       drop the second below the first. */
    .err-body > .err-card {
        margin: 0;
    }
    .err-section {
        margin: 0 0 6px;
        font-size: 13px;
        font-weight: 600;
    }
    .err-event-list {
        display: flex;
        flex-direction: column;
        gap: 2px;
        margin-bottom: 8px;
    }
    .err-event {
        display: flex;
        flex-direction: column;
        gap: 1px;
        padding: 6px 8px;
        border: 0;
        border-radius: var(--borderRadius);
        background: none;
        font: inherit;
        text-align: left;
        cursor: pointer;
        transition: background-color 150ms ease-out;
    }
    .err-event:hover,
    .err-event:focus-visible {
        background: var(--surfaceAlt1Color);
    }
    .err-event.on {
        background: color-mix(in srgb, var(--markColor), transparent 90%);
    }
    .err-event-when {
        font-size: 13px;
        font-variant-numeric: tabular-nums;
    }
    .err-event-where {
        overflow-wrap: anywhere;
    }
    .err-message,
    .err-stack {
        margin: 0 0 14px;
        padding: 10px 12px;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color);
        font-family: var(--monospaceFontFamily, ui-monospace, monospace);
        font-size: 12px;
        line-height: 1.5;
        white-space: pre-wrap;
        overflow-wrap: anywhere;
    }
    .err-stack {
        max-height: 520px;
        overflow: auto;
        color: var(--txtHintColor);
    }
    .err-stack .mine {
        color: var(--txtPrimaryColor);
        font-weight: 600;
    }
    .err-stack-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
    }
    .err-context {
        display: grid;
        grid-template-columns: max-content 1fr;
        gap: 4px 14px;
        margin: 0 0 14px;
        font-size: 13px;
    }
    .err-context dt {
        color: var(--txtHintColor);
    }
    .err-context dd {
        margin: 0;
        overflow-wrap: anywhere;
    }
    .err-event-detail {
        min-width: 0;
        animation: fadeIn 200ms ease-out;
    }
    @media (max-width: 800px) {
        .err-body {
            grid-template-columns: 1fr;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .err-event {
            transition: none;
        }
        .err-event-detail {
            animation: none;
        }
    }
</style>
