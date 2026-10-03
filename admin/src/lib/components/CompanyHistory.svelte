<script>
    /**
     * How a company's terms changed: its credit limit, payment terms, approval
     * threshold, purchase-order rule, status, customer group and catalogue,
     * each change with who made it and when.
     *
     * The engine writes a row per field in the same transaction as the change,
     * so the record cannot miss one or invent one. One save that moved two
     * fields reads as one change here: rows with the same instant and the same
     * person are shown together, because that is what the operator did.
     *
     * `version` is bumped by the page after its editor saves, which is what
     * makes the new change appear without a reload.
     */
    import { api, can, query } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { companyStatusLabel } from "$lib/b2b.js";

    let { companyId, version = 0 } = $props();

    const PER_PAGE = 20;

    let entries = $state([]);
    let total = $state(0);
    let loading = $state(true);
    let more = $state(false);
    let groups = $state({});
    let reqId = 0;

    $effect(() => {
        companyId;
        version;
        load();
    });

    $effect(() => {
        if (can("groups.read")) loadGroups();
        loadCatalogues();
    });

    let catalogues = $state({});
    async function loadCatalogues() {
        try {
            const result = await api.get("/api/admin/x/b2b/catalogues?limit=200");
            catalogues = Object.fromEntries((result.data ?? []).map((c) => [c.id, c.name]));
        } catch {
            // As with groups: the id still says which catalogue.
        }
    }

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const result = await api.get(
                `/api/admin/x/b2b/companies/${companyId}/history` + query({ limit: PER_PAGE, page: 1 }),
            );
            if (mine !== reqId) return;
            entries = result.data ?? [];
            total = result.meta?.total ?? entries.length;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    async function loadMore() {
        if (more) return;
        more = true;
        try {
            const result = await api.get(
                `/api/admin/x/b2b/companies/${companyId}/history` + query({ limit: PER_PAGE, offset: entries.length }),
            );
            entries = [...entries, ...(result.data ?? [])];
            total = result.meta?.total ?? total;
        } catch (err) {
            toast.error(err);
        } finally {
            more = false;
        }
    }

    async function loadGroups() {
        try {
            const list = (await api.get("/api/admin/customer-groups")) ?? [];
            groups = Object.fromEntries(list.map((g) => [g.id, g.name]));
        } catch {
            // Names are a nicety; the ids still say which group.
        }
    }

    const FIELDS = {
        status: "Status",
        credit_limit_minor: "Credit limit",
        net_days: "Payment terms",
        approval_threshold_minor: "Approval over",
        require_po: "Purchase orders",
        group_id: "Customer group",
        catalogue_id: "Catalogue",
    };

    function value(entry, v) {
        if (entry.field === "status") return companyStatusLabel(v);
        if (entry.field === "require_po") return v ? "Required" : "Optional";
        if (entry.field === "net_days") return `Net ${v}`;
        if (entry.field === "credit_limit_minor") {
            return v === null ? "No account" : formatMoney({ amount_minor: v, currency: entry.currency });
        }
        if (entry.field === "approval_threshold_minor") {
            return v === null ? "Never" : formatMoney({ amount_minor: v, currency: entry.currency });
        }
        if (entry.field === "group_id") return v === null ? "None" : groups[v] || `Group ${v}`;
        if (entry.field === "catalogue_id") return v === null ? "Everything" : catalogues[v] || `Catalogue ${v}`;
        return v === null ? "None" : String(v);
    }

    function who(entry) {
        if (entry.actor_kind === "token") return "An admin token";
        if (entry.actor_kind === "system") return "The store";
        if (entry.actor_kind === "buyer") return `${entry.actor_email || "A buyer"} (the company's admin)`;
        return entry.actor_email || "An operator";
    }

    /* One save, one group: the same instant and the same person. */
    const changes = $derived.by(() => {
        const out = [];
        for (const e of entries) {
            const last = out[out.length - 1];
            const key = `${e.changed_at}|${e.actor_kind}|${e.actor_email}`;
            if (last && last.key === key) last.rows.push(e);
            else out.push({ key, at: e.changed_at, who: who(e), created: e.action === "created", rows: [e] });
        }
        // The list is newest first; inside one save the fields read in the
        // order the engine wrote them, which is the order a person reads terms.
        for (const change of out) change.rows.sort((a, b) => a.id - b.id);
        return out;
    });
</script>

{#if loading && !entries.length}
    <span class="skeleton-loader"></span>
{:else if !entries.length}
    <p class="txt-hint m-b-base">Nothing recorded yet.</p>
{:else}
    <ol class="b2b-history m-b-sm" class:faded={loading}>
        {#each changes as change (change.key)}
            <li class="b2b-history-item">
                <div class="b2b-history-head">
                    <span class="txt-bold">{change.created ? "Opened with these terms" : "Changed"}</span>
                    <span class="txt-hint txt-sm">{formatDate(change.at)} · {change.who}</span>
                </div>
                <ul class="b2b-history-rows">
                    {#each change.rows as row (row.id)}
                        <li>
                            <span class="b2b-history-field">{FIELDS[row.field] ?? row.field}</span>
                            {#if row.action === "changed"}
                                <span class="txt-hint">{value(row, row.old_value)}</span>
                                <span class="b2b-history-arrow" aria-label="to">→</span>
                            {/if}
                            <span>{value(row, row.new_value)}</span>
                        </li>
                    {/each}
                </ul>
            </li>
        {/each}
    </ol>
    {#if entries.length < total}
        <button type="button" class="btn sm secondary m-b-base" class:loading={more} disabled={more} onclick={loadMore}>
            <span class="txt">Show earlier changes</span>
        </button>
    {/if}
{/if}
