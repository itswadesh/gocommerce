<script>
    /**
     * Creating a company, or changing one: its name, its terms and its group.
     *
     * One drawer for both because the companies list creates and the company's
     * own page edits, and two copies of these nine fields would be two places
     * for a credit limit to be read in a different unit.
     *
     * Money is typed as a decimal and sent as integer minor units. An empty box
     * is a real answer rather than a zero: no credit limit means the company
     * has no account and pays at checkout, and no approval threshold means a
     * buyer never waits for one. The engine tells null from absent on a patch,
     * so emptying a box really does take the limit off.
     */
    import { untrack } from "svelte";
    import { api, can } from "$lib/api.js";
    import { settings } from "$lib/settings.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { isValidMoney, toMinor, fromMinor, parseMoney } from "$lib/format.js";
    import { COMPANY_STATUSES } from "$lib/b2b.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import Select from "$lib/components/Select.svelte";

    let { open = false, company = null, onclose, onsaved } = $props();

    const editing = $derived(!!company);
    const currency = $derived(company?.credit_limit?.currency ?? company?.approval_threshold?.currency ?? settings.currency);
    /* Groups are read under their own right. An operator without it can still
       manage a company; they just cannot move it between groups, and the field
       says so rather than offering an empty list. */
    const groupsReadable = $derived(can("groups.read"));

    let form = $state(blank());
    let errors = $state({});
    let saving = $state(false);
    let groups = $state([]);
    let groupsLoaded = $state(false);

    function blank() {
        return {
            name: "",
            code: "",
            tax_id: "",
            status: "active",
            group_id: "",
            credit_limit: "",
            net_days: "30",
            approval_threshold: "",
            require_po: false,
            notes: "",
        };
    }

    /* Re-read every time the drawer opens rather than whenever `company`
       changes: the page behind it reloads the company after a save, and a form
       that reset itself under the operator's cursor would lose their typing. */
    $effect(() => {
        if (!open) return;
        untrack(() => {
            errors = {};
            const c = company;
            form = c
                ? {
                      name: c.name ?? "",
                      code: c.code ?? "",
                      tax_id: c.tax_id ?? "",
                      status: c.status ?? "active",
                      group_id: c.group_id ? String(c.group_id) : "",
                      credit_limit: c.credit_limit ? fromMinor(c.credit_limit.amount_minor, currency) : "",
                      net_days: String(c.net_days ?? 30),
                      approval_threshold: c.approval_threshold
                          ? fromMinor(c.approval_threshold.amount_minor, currency)
                          : "",
                      require_po: !!c.require_po,
                      notes: c.notes ?? "",
                  }
                : blank();
            if (groupsReadable && !groupsLoaded) loadGroups();
        });
    });

    async function loadGroups() {
        try {
            groups = (await api.get("/api/admin/customer-groups")) ?? [];
            groupsLoaded = true;
        } catch (err) {
            toast.error(err);
        }
    }

    const groupOptions = $derived([
        { value: "", label: "No group — the store's own prices" },
        ...groups.map((g) => ({ value: String(g.id), label: `${g.name} (${g.code})` })),
    ]);

    /** A money box: null for empty, minor units for a number, undefined for junk. */
    function moneyField(raw) {
        const text = String(raw ?? "").trim();
        if (!text) return null;
        if (!isValidMoney(text) || parseMoney(text) < 0) return undefined;
        return toMinor(text, currency);
    }

    async function save(event) {
        event?.preventDefault();
        if (saving) return;

        errors = {};
        if (!form.name.trim()) errors.name = "A company needs a name.";
        const limit = moneyField(form.credit_limit);
        if (limit === undefined) errors.credit_limit = "Enter an amount, or leave it empty for no account.";
        const threshold = moneyField(form.approval_threshold);
        if (threshold === undefined) errors.approval_threshold = "Enter an amount, or leave it empty so nobody waits.";
        const days = Number(String(form.net_days).trim());
        if (!Number.isInteger(days) || days < 0 || days > 365) errors.net_days = "Net days run from 0 to 365.";
        if (Object.keys(errors).length) return;

        const body = {
            name: form.name.trim(),
            tax_id: form.tax_id.trim(),
            status: form.status,
            credit_limit_minor: limit,
            net_days: days,
            approval_threshold_minor: threshold,
            require_po: form.require_po,
            notes: form.notes,
        };
        // Only sent once the list it was chosen from has arrived: a form built
        // without it would otherwise send "no group" for a company that has one.
        if (groupsLoaded) body.group_id = form.group_id ? Number(form.group_id) : null;

        saving = true;
        try {
            let saved;
            if (editing) {
                saved = await api.patch(`/api/admin/x/b2b/companies/${company.id}`, body);
                toast.success(`Saved ${saved.name}`);
            } else {
                if (form.code.trim()) body.code = form.code.trim();
                saved = await api.post("/api/admin/x/b2b/companies", body);
                toast.success(`Added ${saved.name}`);
            }
            await onsaved?.(saved);
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }
</script>

<Drawer {open} size="sm" title={editing ? `Edit ${company.name}` : "New company"} {onclose}>
    <form id="company-form" class="b2b-select-fit" onsubmit={save} novalidate>
        <div class="field required" class:error={!!errors.name}>
            <label for="co-name">Name</label>
            <input id="co-name" type="text" autocomplete="off" bind:value={form.name} />
        </div>
        {#if errors.name}<div class="field-help error">{errors.name}</div>{/if}

        {#if !editing}
            <div class="field m-t-sm">
                <label for="co-code">Code</label>
                <input
                    id="co-code"
                    type="text"
                    class="txt-code"
                    autocomplete="off"
                    placeholder="Made from the name if left empty"
                    bind:value={form.code}
                />
            </div>
            <div class="field-help">
                Lower-case letters, digits and hyphens. Every order placed for the company records
                it, so it is set once rather than following a rename.
            </div>
        {/if}

        <div class="field m-t-sm">
            <label for="co-tax">Tax number</label>
            <input id="co-tax" type="text" class="txt-code" autocomplete="off" bind:value={form.tax_id} />
        </div>

        <div class="field m-t-sm">
            <label for="co-status">Status</label>
            <Select
                id="co-status"
                bind:value={form.status}
                options={COMPANY_STATUSES.map((s) => ({ value: s.value, label: `${s.label} — ${s.hint}`, short: s.label }))}
            />
        </div>

        <h6 class="section-title">
            <i class="ri-bank-card-line" aria-hidden="true"></i>
            Account terms
        </h6>

        <div class="field" class:error={!!errors.credit_limit}>
            <label for="co-limit">Credit limit ({currency})</label>
            <input
                id="co-limit"
                type="text"
                inputmode="decimal"
                autocomplete="off"
                placeholder="No account"
                bind:value={form.credit_limit}
            />
        </div>
        <div class="field-help">
            {#if errors.credit_limit}
                <span class="txt-danger">{errors.credit_limit}</span>
            {:else}
                How much the company may owe at once. Empty means no account: its buyers pay at
                checkout like anybody else.
            {/if}
        </div>

        <div class="field m-t-sm" class:error={!!errors.net_days}>
            <label for="co-net">Payment terms (days)</label>
            <input id="co-net" type="number" min="0" max="365" step="1" bind:value={form.net_days} />
        </div>
        <div class="field-help">
            {#if errors.net_days}
                <span class="txt-danger">{errors.net_days}</span>
            {:else}
                An order on account is due this many days after it is placed — 30 is "Net 30".
            {/if}
        </div>

        <div class="field m-t-sm" class:error={!!errors.approval_threshold}>
            <label for="co-threshold">Approval over ({currency})</label>
            <input
                id="co-threshold"
                type="text"
                inputmode="decimal"
                autocomplete="off"
                placeholder="Never"
                bind:value={form.approval_threshold}
            />
        </div>
        <div class="field-help">
            {#if errors.approval_threshold}
                <span class="txt-danger">{errors.approval_threshold}</span>
            {:else}
                A buyer's order above this waits for one of the company's own approvers. Admins and
                approvers never wait. Empty means nobody does.
            {/if}
        </div>

        <div class="field m-t-sm">
            <input type="checkbox" id="co-po" class="switch" bind:checked={form.require_po} />
            <label for="co-po">Every order needs a purchase-order number</label>
        </div>

        <h6 class="section-title">
            <i class="ri-price-tag-3-line" aria-hidden="true"></i>
            Prices
        </h6>
        <div class="field">
            <label for="co-group">Customer group</label>
            {#if groupsReadable}
                <Select id="co-group" bind:value={form.group_id} options={groupOptions} disabled={!groupsLoaded} />
            {:else}
                <input
                    id="co-group"
                    type="text"
                    disabled
                    value={form.group_id ? `Group ${form.group_id}` : "No group"}
                />
            {/if}
        </div>
        <div class="field-help">
            {#if groupsReadable}
                Each buyer's address joins this group, so the group's price lists price their carts.
                Changing it moves every buyer to the new group.
            {:else}
                Your role cannot read customer groups, so this stays as it is.
            {/if}
        </div>

        <div class="field m-t-sm">
            <label for="co-notes">Notes</label>
            <textarea id="co-notes" rows="3" bind:value={form.notes}></textarea>
        </div>
        <div class="field-help">For the store's own team. Buyers never see them.</div>
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">Cancel</span>
        </button>
        <button type="submit" form="company-form" class="btn" class:loading={saving} disabled={saving}>
            <span class="txt">{editing ? "Save" : "Add company"}</span>
        </button>
    {/snippet}
</Drawer>
