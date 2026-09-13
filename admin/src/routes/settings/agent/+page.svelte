<script>
    /**
     * Agent activity: what ext/mcp exposes, and what an agent has done with it.
     *
     * Two halves, and they come from two different kinds of endpoint. The card
     * is one JSON-RPC call to the MCP endpoint itself, which answers with a
     * bare JSON-RPC envelope rather than the engine's `{data}` one — a standing
     * exception, because an MCP client cannot be asked to unwrap a second
     * envelope. So it is fetched `raw` and parsed here, rather than through a
     * helper written for a shape this body does not have.
     *
     * Two consequences of JSON-RPC worth stating, because both look like bugs
     * from the outside: a tool error arrives with HTTP 200 and a top-level
     * `error` member, so `response.ok` is true and the body has to be read; and
     * 401 and 403 still arrive as the engine's own envelope, from the
     * middleware, before the body is ever parsed.
     *
     * The audit list underneath is an ordinary admin list and needs none of
     * that.
     */
    import { api, can, request } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatDate } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import SettingsSidebar from "$lib/components/SettingsSidebar.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 25;

    const list = listState({ page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);


    let tools = $state([]);
    let statusLoading = $state(true);
    let reachable = $state(false);

    let entries = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    let detailOpen = $state(false);
    let entry = $state(null);

    const missing = $derived(modulesKnown() && !hasModule("mcp"));

    /* Read from the server's own answer, never from a list of builtin names
       hardcoded here: a tool contributed by another module through
       Config.Tools would be guessed wrong by any such list. */
    const mutating = $derived(tools.filter((t) => t.annotations?.readOnlyHint === false));
    const readOnly = $derived(tools.length > 0 && mutating.length === 0);

    /* Both wait for the module answer. Without that the status call would POST
       to a route a store without ext/mcp does not serve, which answers 405
       rather than 404 — the panel's own static handler takes GET and HEAD for
       every path, so an unmounted POST is a method mismatch rather than a
       missing route. hasModule() reads a `$state` object, so both re-run when
       the answer lands. */
    $effect(() => {
        if (can("store.operate") && hasModule("mcp")) loadStatus();
    });

    $effect(() => {
        list.params;
        if (can("store.operate") && hasModule("mcp")) loadAudit();
    });

    async function loadStatus() {
        statusLoading = true;
        try {
            const body = await request("POST", "/api/admin/x/mcp", {
                body: { jsonrpc: "2.0", id: 1, method: "tools/list" },
                raw: true,
            });
            const rpc = JSON.parse(body);
            if (rpc.error) {
                // HTTP 200 with an error member: nothing threw, so the body is
                // the only place this is visible.
                toast.error(rpc.error.message);
                return;
            }
            tools = rpc.result?.tools ?? [];
            reachable = true;
        } catch (err) {
            if (err.status !== 404) toast.error(err);
        } finally {
            statusLoading = false;
        }
    }

    async function loadAudit() {
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/x/mcp/audit" + list.query({ limit: perPage }),
            );
            entries = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (err.status !== 404) toast.error(err);
            entries = [];
            meta = null;
        } finally {
            loading = false;
        }
    }

    function open(row) {
        entry = row;
        detailOpen = true;
    }

    /** The arguments as the agent sent them, readable. */
    function pretty(value) {
        try {
            return JSON.stringify(value ?? {}, null, 2);
        } catch {
            return String(value);
        }
    }
</script>

<svelte:head><title>Agent activity · GoCommerce</title></svelte:head>

{#if !can("store.operate")}
    <NoAccess right="store.operate" what="the agent audit" />
{:else}
    <div class="page page-agent">
        <SettingsSidebar />

        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Agent activity</div>
                </nav>

                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        onclick={() => {
                            loadStatus();
                            loadAudit();
                        }}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>
            </header>

            {#if missing}
                <ModuleMissing
                    module="mcp"
                    what="The agent endpoint is served by ext/mcp, and this binary does not have it."
                />
            {:else}
                <div class="wrapper m-b-base">
                    <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">The server</h2>

                    {#if statusLoading && !tools.length}
                        <div class="block txt-center p-base"><span class="loader"></span></div>
                    {:else if reachable}
                        <p>
                            <span class="txt-bold">{tools.length}</span>
                            {tools.length === 1 ? "tool" : "tools"} exposed at
                            <code class="txt-code">/api/admin/x/mcp</code>.
                        </p>

                        <div class="inline-flex gap-5 flex-wrap m-t-5 m-b-5">
                            {#each tools as tool (tool.name)}
                                <span
                                    class="label"
                                    class:warning={tool.annotations?.readOnlyHint === false}
                                    title={tool.description}
                                >
                                    {tool.name}
                                </span>
                            {/each}
                        </div>

                        {#if readOnly}
                            <div class="field-help">
                                Read-only: no tool on this server changes anything. A store running
                                the module read-only withholds the mutating tools entirely.
                            </div>
                        {:else}
                            <div class="field-help">
                                The {mutating.length} highlighted tools change state, and each one
                                is recorded below. Every tool is additionally checked against the
                                rights the caller's role carries, so an agent driven by an operator
                                cannot reach through this door what the front door refuses.
                            </div>
                        {/if}
                    {:else}
                        <p class="txt-hint">
                            The agent endpoint did not answer. It is mounted, or this screen would
                            not be here — see the toast for what it said.
                        </p>
                    {/if}
                </div>

                <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Recorded calls</h2>

                <div class="page-table-wrapper tw:rounded-xl tw:border">
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-type-date min-width">When</th>
                                <th class="col-field-name-id">Tool</th>
                                <th class="col-field-type-select min-width">Outcome</th>
                                <th class="col-field-type-text">Detail</th>
                                <th class="col-meta min-width"></th>
                            </tr>
                        </thead>
                        <tbody>
                            {#each entries as row (row.id)}
                                <tr
                                    class="handle"
                                    tabindex="0"
                                    onclick={() => open(row)}
                                    onkeydown={(e) => rowKey(e, () => open(row))}
                                >
                                    <td
                                        class="col-field-type-date min-width txt-hint"
                                        data-name="When"
                                    >
                                        {formatDate(row.called_at)}
                                    </td>
                                    <td class="col-field-name-id" data-name="Tool">
                                        <span class="txt-code txt-bold">{row.tool}</span>
                                    </td>
                                    <td class="col-field-type-select min-width" data-name="Outcome">
                                        <span
                                            class="label"
                                            class:success={row.outcome === "ok"}
                                            class:danger={row.outcome === "error"}
                                        >
                                            {row.outcome}
                                        </span>
                                    </td>
                                    <td class="col-field-type-text txt-hint" data-name="Detail">
                                        <span class="txt-ellipsis">{row.detail || "—"}</span>
                                    </td>
                                    <td class="col-meta min-width">
                                        <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                                    </td>
                                </tr>
                            {/each}

                            {#if loading && !entries.length}
                                {#each Array(5) as _, i (i)}
                                    <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {:else if !entries.length}
                                <tr>
                                    <td colspan="5" class="txt-center txt-hint p-base">
                                        Nothing recorded. Only tools that change something are
                                        audited — an agent reading the catalogue leaves no entry
                                        here — along with any call refused for want of a right.
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
                        noun="call"
                        {perPage}
                        onpage={(n) => list.setPage(n)}
                        onperpage={(n) => list.set({ limit: n })}
                    />
                    <div class="flex-fill"></div>
                    <ThemeToggle />
                </footer>
            {/if}
        </div>
    </div>
{/if}

<Drawer
    open={detailOpen}
    size="sm"
    title={entry?.tool || "Tool call"}
    onclose={() => (detailOpen = false)}
>
    {#if entry}
        <div class="field readonly-field">
            <span class="readonly-label">When</span>
            <span class="readonly-value">{formatDate(entry.called_at)}</span>
        </div>
        <div class="field readonly-field">
            <span class="readonly-label">Outcome</span>
            <span class="readonly-value">{entry.outcome}</span>
        </div>

        {#if entry.detail}
            <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">What it said</h2>
            <p>{entry.detail}</p>
        {/if}

        <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Arguments</h2>
        <!-- base.css already gives <pre> pre-wrap and a radius, and .txt-code
             the monospace face: a JSON blob needs nothing this panel does not
             already have. -->
        <pre class="txt-code">{pretty(entry.arguments)}</pre>
    {/if}
</Drawer>

