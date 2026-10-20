<script>
    /**
     * Errors: what failed in this store, Sentry's way (D86).
     *
     * One row per issue — the same failure however often it happened — with
     * how often, when first and when last, and where. Unresolved first,
     * because that is the list there is something to do about; a resolved
     * issue comes back here by itself when it happens again.
     *
     * The rows carry the code's and the database's own words, which is why
     * reading them is errors.read rather than store.operate.
     */
    import { goto } from "$app/navigation";
    import { base } from "$app/paths";
    import { can, errors } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";

    const PER_PAGE = 25;
    const list = listState({ status: "unresolved", source: "any", q: "", page: 1, limit: PER_PAGE });
    const status = $derived(list.params.status);
    const source = $derived(list.params.source);
    const search = $derived(list.params.q);
    const perPage = $derived(list.params.limit);
    const readable = $derived(can("errors.read"));

    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let draft = $state(list.params.q);

    $effect(() => {
        list.params;
        if (!readable) return;
        load();
    });

    async function load() {
        loading = true;
        try {
            const result = await errors.list({
                status: status === "all" ? "" : status,
                source: source === "any" ? "" : source,
                q: search,
                page: list.page,
                limit: perPage,
            });
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

    const open = (row) => goto(`${base}/dash/settings/errors/${row.id}`);
    const filtered = $derived(source !== "any" || !!search);

    const TABS = [
        { value: "unresolved", label: "Unresolved" },
        { value: "resolved", label: "Resolved" },
        { value: "ignored", label: "Ignored" },
        { value: "all", label: "All" },
    ];
    const SOURCES = [
        { value: "any", label: "Everywhere" },
        { value: "server", label: "Server" },
        { value: "panel", label: "Panel" },
    ];
</script>

<svelte:head><title>Errors · GoCommerce</title></svelte:head>

<div class="page page-errors shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Errors</h1>
            </nav>
            {#if readable}
                <form class="fields searchbar" role="search" onsubmit={submitSearch}>
                    <div class="field">
                        <input type="search" class="p-l-20" placeholder="Search the message or where it happened" bind:value={draft} />
                    </div>
                    {#if draft || search}
                        <div class="field addon p-r-5">
                            {#if draft !== search}
                                <button type="submit" class="btn sm pill warning">Search</button>
                            {/if}
                            <button type="button" class="btn sm pill secondary transparent" onclick={() => ((draft = ""), list.set({ q: "" }))}>
                                Clear
                            </button>
                        </div>
                    {/if}
                </form>
                <div class="inline-flex gap-sm">
                    <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="errors.read" what="the store's errors" />
        {:else}
            <div class="err-filters">
                <div class="err-tabs" role="tablist" aria-label="Status">
                    {#each TABS as t (t.value)}
                        <button
                            type="button"
                            role="tab"
                            class="err-tab"
                            class:on={status === t.value}
                            aria-selected={status === t.value}
                            onclick={() => list.set({ status: t.value })}>{t.label}</button
                        >
                    {/each}
                </div>
                <div class="err-sources" role="group" aria-label="Where">
                    {#each SOURCES as s (s.value)}
                        <button
                            type="button"
                            class="btn sm {source === s.value ? '' : 'transparent secondary'}"
                            aria-pressed={source === s.value}
                            onclick={() => list.set({ source: s.value })}><span class="txt">{s.label}</span></button
                        >
                    {/each}
                </div>
            </div>

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Issue</th>
                            <th class="col-field-type-number min-width">Events</th>
                            <th class="col-field-type-date min-width">Last seen</th>
                            <th class="col-field-type-date min-width">First seen</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.id)}
                            <tr class="handle" tabindex="0" onclick={() => open(row)} onkeydown={(e) => rowKey(e, () => open(row))}>
                                <td class="col-field-name-id" data-name="Issue">
                                    <div class="err-issue">
                                        <span class="err-mark" class:panel={row.source === "panel"} aria-hidden="true">
                                            <i class={row.source === "panel" ? "ri-window-line" : "ri-server-line"}></i>
                                        </span>
                                        <div class="err-text">
                                            <div class="err-title">{row.title}</div>
                                            <div class="err-sub txt-hint txt-sm">
                                                <span class="label sm">{row.source === "panel" ? "Panel" : "Server"}</span>
                                                {#if row.culprit}<span class="txt-code err-culprit">{row.culprit}</span>{/if}
                                                {#if row.status !== "unresolved"}<span class="label sm">{row.status === "resolved" ? "Resolved" : "Ignored"}</span>{/if}
                                            </div>
                                        </div>
                                    </div>
                                </td>
                                <td class="col-field-type-number min-width err-count" data-name="Events">{row.count.toLocaleString()}</td>
                                <td class="col-field-type-date min-width" data-name="Last seen" title={formatDate(row.last_seen)}>{relativeTime(row.last_seen)}</td>
                                <td class="col-field-type-date min-width txt-hint" data-name="First seen" title={formatDate(row.first_seen)}>{relativeTime(row.first_seen)}</td>
                            </tr>
                        {:else}
                            {#if !loading}
                                <tr>
                                    <td colspan="4">
                                        {#if filtered}
                                            <EmptyState
                                                icon="ri-search-line"
                                                title="No errors match"
                                                text="Nothing here fits that search or that place."
                                                onclear={() => ((draft = ""), list.set({ q: "", source: "any" }))}
                                            />
                                        {:else if status === "unresolved"}
                                            <EmptyState
                                                icon="ri-shield-check-line"
                                                title="Nothing is failing"
                                                text="Every error the store has had is resolved or ignored. A new one, or a resolved one happening again, appears here."
                                            />
                                        {:else}
                                            <EmptyState
                                                icon="ri-bug-line"
                                                title={status === "all" ? "No errors yet" : `No ${status} errors`}
                                                text="When a request fails on the server, or a screen breaks in somebody's panel, it is recorded here with what happened and where."
                                            />
                                        {/if}
                                    </td>
                                </tr>
                            {/if}
                        {/each}
                    </tbody>
                </table>
            </div>

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager {meta} {loading} noun="issue" {perPage} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
                <div class="flex-fill"></div>
            </footer>
        {/if}
    </div>
</div>

<style>
    .err-filters {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: 10px;
        margin-bottom: 12px;
    }
    .err-tabs {
        display: flex;
        gap: 2px;
        border-bottom: 1px solid var(--surfaceAlt2Color);
    }
    .err-tab {
        padding: 8px 12px;
        border: 0;
        border-bottom: 2px solid transparent;
        background: none;
        color: var(--txtHintColor);
        font: inherit;
        font-weight: 500;
        cursor: pointer;
        transition:
            color 150ms ease-out,
            border-color 150ms ease-out;
    }
    .err-tab:hover,
    .err-tab:focus-visible {
        color: var(--txtPrimaryColor);
    }
    .err-tab.on {
        border-bottom-color: var(--markColor);
        color: var(--txtPrimaryColor);
    }
    .err-sources {
        display: flex;
        gap: 4px;
    }
    .err-issue {
        display: flex;
        align-items: flex-start;
        gap: 10px;
        min-width: 0;
    }
    .err-mark {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        flex: 0 0 auto;
        border-radius: var(--borderRadius);
        background: color-mix(in srgb, var(--dangerColor), transparent 88%);
        color: var(--dangerColor);
    }
    .err-mark.panel {
        background: color-mix(in srgb, var(--warningColor), transparent 85%);
        color: color-mix(in srgb, var(--warningColor), var(--txtPrimaryColor) 30%);
    }
    .err-text {
        min-width: 0;
    }
    .err-title {
        font-weight: 600;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        max-width: 70ch;
    }
    .err-sub {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 6px;
        margin-top: 2px;
    }
    .err-culprit {
        overflow-wrap: anywhere;
    }
    .err-count {
        font-variant-numeric: tabular-nums;
        font-weight: 600;
    }
    @media (max-width: 600px) {
        .err-title {
            white-space: normal;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .err-tab {
            transition: none;
        }
    }
</style>
