<script>
    /**
     * Reports: the questions only this shop has.
     *
     * What used to be here — the sales chart, the by-period table, the
     * best-seller list — is the question every shop has, and it now lives on
     * the dashboard where somebody arriving at the panel already sees it.
     * Having it in both places meant two screens showing the same figure and
     * an owner choosing between them.
     *
     * So this screen is the other kind: a saved SELECT, written once and run
     * by whoever needs the answer. Which wholesale customers have not ordered
     * since March, what the Tuesday promotion actually cost — no fixed screen
     * can enumerate those, and a store that cannot ask them exports everything
     * to a spreadsheet instead.
     *
     * The editor is deliberately plain. A report reads any table the engine
     * can, so the thing that matters is not autocomplete — it is that the
     * operator can see exactly what will run, that running it can only read,
     * and that saving one is a decision about what everybody with reports.read
     * may then look at. The screen says all three.
     */
    import { base } from "$app/paths";
    import { can, request } from "$lib/api.js";
    import { formatDate } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import DirtyGuard from "$lib/components/DirtyGuard.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    const readable = $derived(can("reports.read"));
    const writable = $derived(can("reports.write"));

    let saved = $state([]);
    let loading = $state(true);
    let running = $state(false);
    let saving = $state(false);
    let confirmOpen = $state(false);
    let toDelete = $state(null);

    /** The report being edited, or null for a new one. */
    let editing = $state(null);
    let name = $state("");
    let description = $state("");
    let sql = $state("");
    let result = $state(null);
    let error = $state("");

    const EXAMPLE = `SELECT p.title,
       sum(l.quantity) AS units,
       count(DISTINCT o.id) AS orders
FROM order_lines l
JOIN orders o ON o.id = l.order_id
JOIN variants v ON v.id = l.variant_id
JOIN products p ON p.id = v.product_id
WHERE o.created_at > now() - interval '30 days'
GROUP BY p.title
ORDER BY units DESC`;

    const dirty = $derived(
        writable &&
            (sql.trim() !== (editing?.sql ?? "") ||
                name.trim() !== (editing?.name ?? "") ||
                description.trim() !== (editing?.description ?? "")),
    );

    $effect(() => {
        if (readable) load();
    });

    async function load() {
        loading = true;
        try {
            saved = await request("GET", "/api/admin/reports/custom");
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    function open(report) {
        editing = report;
        name = report?.name ?? "";
        description = report?.description ?? "";
        sql = report?.sql ?? "";
        result = null;
        error = "";
    }

    function blank() {
        open(null);
        sql = EXAMPLE;
    }

    /* Running an unsaved query is reports.write, because it is writing a
       report that has not been kept; running a saved one is reports.read, so
       somebody who cannot write SQL can still get the answer. */
    async function run() {
        if (running) return;
        running = true;
        error = "";
        try {
            const dirtyOrNew = !editing || sql.trim() !== editing.sql;
            result = dirtyOrNew
                ? await request("POST", "/api/admin/reports/custom/run", { body: { sql } })
                : await request("POST", `/api/admin/reports/custom/${editing.id}/run`);
        } catch (err) {
            // Shown in the panel rather than as a toast: a SQL error is
            // something you read while fixing the query beside it, and a
            // message that slides away after four seconds is one you re-run to
            // see again.
            result = null;
            error = err?.message ?? String(err);
        } finally {
            running = false;
        }
    }

    async function save() {
        if (saving || !name.trim()) return;
        saving = true;
        try {
            const body = { name: name.trim(), description: description.trim(), sql };
            const out = editing
                ? await request("PATCH", `/api/admin/reports/custom/${editing.id}`, { body })
                : await request("POST", "/api/admin/reports/custom", { body });
            editing = out;
            toast.success(out.name + " saved");
            await load();
        } catch (err) {
            error = err?.message ?? String(err);
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function remove(report) {
        try {
            await request("DELETE", `/api/admin/reports/custom/${report.id}`);
            if (editing?.id === report.id) open(null);
            toast.success(report.name + " deleted");
            await load();
        } catch (err) {
            toast.error(err);
        }
    }

    /* The result as a file, built here rather than asked of the engine: the
       rows are already in the browser, and a second round trip would run the
       query twice and could answer differently. */
    function download() {
        if (!result) return;
        const cell = (v) => {
            if (v === null || v === undefined) return "";
            const s = String(v);
            return /[",\n]/.test(s) ? '"' + s.replaceAll('"', '""') + '"' : s;
        };
        const lines = [result.columns.map(cell).join(",")];
        for (const row of result.rows) lines.push(row.map(cell).join(","));
        const blob = new Blob([lines.join("\n")], { type: "text/csv" });
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = (name.trim() || "report").toLowerCase().replace(/[^a-z0-9]+/g, "-") + ".csv";
        a.click();
        URL.revokeObjectURL(url);
    }
</script>

<svelte:head><title>Reports · GoCommerce</title></svelte:head>

<DirtyGuard
    dirty={dirty && !!sql.trim()}
    message="The query has not been saved. Leave and lose it?"
/>

<div class="page page-reports shopify-skin">
    <div class="page-content">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item feed-title">Reports</div>
            </nav>
            {#if writable}
                <div class="page-header-primary-btns">
                    <button type="button" class="btn sm" onclick={blank}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">New report</span>
                    </button>
                </div>
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="reports.read" what="reports" />
        {:else}
            <p class="txt-hint m-b-base rpt-intro">
                Sales — what the store sold, by period, with its best sellers — is on the
                <a href="{base}/">dashboard</a>. This is for the questions only this shop has, written
                as SQL once and run by whoever needs the answer.
            </p>

            <div class="rpt-layout">
                <!-- The saved list: short, and the reason the screen exists. -->
                <section class="card feed-card rpt-list">
                    <h2 class="feed-card-title">Saved</h2>
                    {#if loading && !saved.length}
                        <div class="block txt-center p-base"><span class="loader"></span></div>
                    {:else if !saved.length}
                        <p class="txt-hint txt-sm m-t-sm">
                            Nothing saved yet.{#if writable}{" "}Write a query and save it, and
                                anybody who can read reports can run it afterwards.{/if}
                        </p>
                    {:else}
                        <ul class="rpt-saved">
                            {#each saved as report (report.id)}
                                <li>
                                    <button
                                        type="button"
                                        class="rpt-saved-item"
                                        class:on={editing?.id === report.id}
                                        onclick={() => open(report)}
                                    >
                                        <span class="txt-bold">{report.name}</span>
                                        {#if report.description}
                                            <span class="txt-hint txt-sm">{report.description}</span>
                                        {/if}
                                        <span class="txt-hint txt-sm">
                                            Changed {formatDate(report.updated_at, { withTime: false })}
                                        </span>
                                    </button>
                                    {#if writable}
                                        <button
                                            type="button"
                                            class="btn sm transparent secondary rpt-del"
                                            aria-label="Delete {report.name}"
                                            onclick={() => ((toDelete = report), (confirmOpen = true))}
                                        >
                                            <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                        </button>
                                    {/if}
                                </li>
                            {/each}
                        </ul>
                    {/if}
                </section>

                <!-- The query and what it returned. -->
                <section class="card feed-card rpt-work">
                    {#if !editing && !sql}
                        <h2 class="feed-card-title">Pick a report, or write one</h2>
                        <p class="txt-hint txt-sm feed-card-sub">
                            {#if writable}
                                A report is a single SELECT. It runs inside a read-only transaction
                                with a fifteen-second limit, so it cannot change anything — but it
                                can read any table in this store, which is why saving one is a
                                decision about what everybody who can read reports may see.
                            {:else}
                                Choose one on the left to run it. Writing a new one needs
                                reports.write, which is an owner's.
                            {/if}
                        </p>
                    {:else}
                        <div class="rpt-fields">
                            <div class="field">
                                <label for="rpt-name">Name</label>
                                <input
                                    id="rpt-name"
                                    type="text"
                                    maxlength="120"
                                    placeholder="What does it answer?"
                                    readonly={!writable}
                                    bind:value={name}
                                />
                            </div>
                            <div class="field">
                                <label for="rpt-desc">Description</label>
                                <input
                                    id="rpt-desc"
                                    type="text"
                                    placeholder="Optional"
                                    readonly={!writable}
                                    bind:value={description}
                                />
                            </div>
                        </div>

                        <div class="field m-t-sm">
                            <label for="rpt-sql">Query</label>
                            <textarea
                                id="rpt-sql"
                                class="txt-code rpt-sql"
                                rows="12"
                                spellcheck="false"
                                readonly={!writable}
                                bind:value={sql}
                            ></textarea>
                        </div>
                        <div class="field-help">
                            SELECT only, one statement. It can read every table, so treat a saved
                            report as something you have published.
                        </div>

                        <div class="feed-form-actions">
                            <button
                                type="button"
                                class="btn sm"
                                class:loading={running}
                                disabled={running || !sql.trim()}
                                onclick={run}
                            >
                                <i class="ri-play-line" aria-hidden="true"></i>
                                <span class="txt">Run</span>
                            </button>
                            {#if writable}
                                <button
                                    type="button"
                                    class="btn sm secondary"
                                    class:loading={saving}
                                    disabled={saving || !name.trim() || !sql.trim()}
                                    onclick={save}
                                >
                                    <span class="txt">{editing ? "Save" : "Save as new"}</span>
                                </button>
                            {/if}
                            {#if result}
                                <button type="button" class="btn sm transparent secondary" onclick={download}>
                                    <i class="ri-download-2-line" aria-hidden="true"></i>
                                    <span class="txt">CSV</span>
                                </button>
                            {/if}
                        </div>

                        {#if error}
                            <div class="alert danger m-t-sm rpt-error">
                                <p><i class="ri-error-warning-line" aria-hidden="true"></i> {error}</p>
                            </div>
                        {/if}

                        {#if result}
                            <div class="rpt-result-head">
                                <span class="txt-bold">
                                    {result.rows.length}
                                    {result.rows.length === 1 ? "row" : "rows"}
                                </span>
                                <span class="txt-hint txt-sm">in {result.took_ms} ms</span>
                                {#if result.truncated}
                                    <span class="label label-warning">
                                        Cut at {result.rows.length} — there are more
                                    </span>
                                {/if}
                            </div>
                            {#if result.columns.length}
                                <div class="rpt-scroll">
                                    <table class="table rpt-table">
                                        <thead>
                                            <tr>
                                                {#each result.columns as column, i (i)}
                                                    <th>{column}</th>
                                                {/each}
                                            </tr>
                                        </thead>
                                        <tbody>
                                            {#each result.rows as row, i (i)}
                                                <tr>
                                                    {#each row as cell, j (j)}
                                                        <td class:is-null={cell === null}>
                                                            {cell === null ? "null" : cell}
                                                        </td>
                                                    {/each}
                                                </tr>
                                            {/each}
                                        </tbody>
                                    </table>
                                </div>
                            {/if}
                        {/if}
                    {/if}
                </section>
            </div>
        {/if}
    </div>
</div>

<Confirm
    bind:open={confirmOpen}
    danger
    title="Delete {toDelete?.name ?? 'this report'}?"
    message="The query is lost. It holds no data of its own, so nothing else goes with it."
    confirmLabel="Delete"
    onconfirm={() => toDelete && remove(toDelete)}
/>
