<script>
    /**
     * One company: its terms, where its account stands, who buys for it, the
     * territories it serves as a dealer, and what it has ordered on account.
     *
     * The credit figures are the engine's, not sums of the orders table below:
     * that table is one page of a ledger and the outstanding figure is all of
     * it. The overdue tile filters the table to the orders it counts, so the
     * number and the rows behind it are one click apart.
     *
     * Adding a buyer has two answers and the screen says which one it got. An
     * account whose address its owner has already confirmed joins at once; any
     * other address is sent an invitation, because joining gives it the
     * company's prices and an unproven address is exactly how somebody would
     * inherit a dealer's terms by registering the dealer's email first.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { page } from "$app/state";
    import { api, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, formatMoney, paymentStatusClass, pluralize } from "$lib/format.js";
    import {
        ROLES,
        companyStatusClass,
        companyStatusLabel,
        roleLabel,
        termsLabel,
    } from "$lib/b2b.js";
    import CompanyEditor from "$lib/components/CompanyEditor.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import CompanyTerritories from "$lib/components/CompanyTerritories.svelte";
    import CompanyStatement from "$lib/components/CompanyStatement.svelte";
    import CompanyHistory from "$lib/components/CompanyHistory.svelte";

    const ORDERS_PER_PAGE = 25;

    const id = $derived(page.params.id);
    const readable = $derived(can("companies.read"));
    const writable = $derived(can("companies.write"));
    const missing = $derived(modulesKnown() && !hasModule("b2b"));

    /* The orders' filter and page ride in the URL, so "Acme's overdue orders"
       is a link somebody can be sent. */
    const orderList = listState({ oq: "", overdue: false, page: 1 });
    let orderDraft = $state(orderList.params.oq);

    let company = $state(null);
    let credit = $state(null);
    let members = $state([]);
    let invitations = $state([]);
    let groupName = $state("");
    let loading = $state(true);
    let notFound = $state(false);

    let orders = $state([]);
    let ordersMeta = $state(null);
    let ordersLoading = $state(true);
    let ordersReq = 0;

    let editorOpen = $state(false);
    let busy = $state(null);
    /* Bumped by a save, so the terms history shows the change it just made. */
    let historyVersion = $state(0);

    let addForm = $state({ email: "", role: "buyer" });
    let addError = $state("");
    let adding = $state(false);
    /* The last invitation this screen sent, said once in words under the form:
       a toast alone vanishes before somebody has read why the buyer is not in
       the table yet. */
    let invited = $state(null);

    let confirmOpen = $state(false);
    let confirmAsk = $state({ title: "", message: "", label: "", run: null });

    let ordersHeading = $state(null);

    $effect(() => {
        id;
        if (readable && hasModule("b2b")) load();
    });

    $effect(() => {
        id;
        orderList.params;
        if (readable && hasModule("b2b")) loadOrders();
    });

    async function load() {
        loading = true;
        notFound = false;
        try {
            const [c, cr, ms, inv] = await Promise.all([
                api.get(`/api/admin/x/b2b/companies/${id}`),
                api.get(`/api/admin/x/b2b/companies/${id}/credit`),
                api.get(`/api/admin/x/b2b/companies/${id}/members`),
                api.get(`/api/admin/x/b2b/companies/${id}/invitations`),
            ]);
            company = c;
            credit = cr;
            members = ms ?? [];
            invitations = inv ?? [];
            loadGroupName();
        } catch (err) {
            if (err.status === 404) notFound = true;
            else toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function loadGroupName() {
        groupName = "";
        if (!company?.group_id) return;
        if (!can("groups.read")) {
            groupName = `Group ${company.group_id}`;
            return;
        }
        try {
            const group = await api.get(`/api/admin/customer-groups/${company.group_id}`);
            groupName = group?.name ?? `Group ${company.group_id}`;
        } catch {
            // A deleted group leaves the id behind with no foreign key to stop
            // it, and the company simply prices as everybody does.
            groupName = `Group ${company.group_id} (deleted)`;
        }
    }

    async function loadOrders() {
        const mine = ++ordersReq;
        ordersLoading = true;
        try {
            const result = await api.get(
                `/api/admin/x/b2b/companies/${id}/orders` +
                    query({
                        q: orderList.params.oq,
                        overdue: orderList.params.overdue ? "true" : "",
                        page: orderList.params.page,
                        limit: ORDERS_PER_PAGE,
                    }),
            );
            if (mine !== ordersReq) return;
            orders = result.data ?? [];
            ordersMeta = result.meta ?? null;
        } catch (err) {
            if (mine === ordersReq && err.status !== 404) toast.error(err);
        } finally {
            if (mine === ordersReq) ordersLoading = false;
        }
    }

    /** Re-reads what a membership change moves: the table, the count, the invitations. */
    async function refreshPeople() {
        try {
            const [c, ms, inv] = await Promise.all([
                api.get(`/api/admin/x/b2b/companies/${id}`),
                api.get(`/api/admin/x/b2b/companies/${id}/members`),
                api.get(`/api/admin/x/b2b/companies/${id}/invitations`),
            ]);
            company = c;
            members = ms ?? [];
            invitations = inv ?? [];
        } catch (err) {
            toast.error(err);
        }
    }

    async function saved(next) {
        editorOpen = false;
        company = next;
        historyVersion++;
        loadGroupName();
        try {
            credit = await api.get(`/api/admin/x/b2b/companies/${id}/credit`);
        } catch (err) {
            toast.error(err);
        }
    }

    async function addBuyer(event) {
        event.preventDefault();
        if (adding || !writable) return;
        addError = "";
        const email = addForm.email.trim();
        if (!email.includes("@")) {
            addError = "Enter the buyer's email address.";
            return;
        }
        adding = true;
        try {
            const result = await api.post(`/api/admin/x/b2b/companies/${id}/members`, {
                email,
                role: addForm.role,
            });
            if (result?.invitation) {
                invited = result.invitation;
                toast.info(`Invitation sent to ${email}`);
            } else {
                invited = null;
                toast.success(`${email} now buys for ${company.name} as ${roleLabel(addForm.role).toLowerCase()}`);
            }
            addForm = { email: "", role: addForm.role };
            await refreshPeople();
        } catch (err) {
            addError = err.message;
            toast.error(err);
        } finally {
            adding = false;
        }
    }

    async function changeRole(member, role) {
        if (role === member.role) return;
        busy = `role-${member.customer_id}`;
        try {
            await api.patch(`/api/admin/x/b2b/companies/${id}/members/${member.customer_id}`, { role });
            toast.success(`${who(member)} is now ${roleLabel(role).toLowerCase()}`);
        } catch (err) {
            // The last admin cannot be demoted; the engine says so, and the
            // reload below puts the select back where it really is.
            toast.error(err);
        } finally {
            busy = null;
            await refreshPeople();
        }
    }

    function ask(title, message, label, run) {
        confirmAsk = { title, message, label, run };
        confirmOpen = true;
    }

    function askRemove(member) {
        ask(
            `Remove ${who(member)} from ${company.name}?`,
            "Their address leaves the company's customer group, so their next cart is priced like anybody's. Orders they already placed stay on the company's books.",
            "Remove",
            async () => {
                try {
                    await api.delete(`/api/admin/x/b2b/companies/${id}/members/${member.customer_id}`);
                    toast.success(`Removed ${who(member)}`);
                    await refreshPeople();
                } catch (err) {
                    toast.error(err);
                }
            },
        );
    }

    function askRevoke(invitation) {
        ask(
            `Withdraw the invitation to ${invitation.email}?`,
            "The link in their inbox stops working. Invite them again to send a new one.",
            "Withdraw",
            async () => {
                try {
                    await api.delete(`/api/admin/x/b2b/companies/${id}/invitations/${invitation.id}`);
                    toast.success("Invitation withdrawn");
                    if (invited?.id === invitation.id) invited = null;
                    await refreshPeople();
                } catch (err) {
                    toast.error(err);
                }
            },
        );
    }

    function askDelete() {
        ask(
            `Delete ${company.name}?`,
            "Its buyers leave its customer group and their open invitations go with it. A company with orders on its books cannot be deleted — close it instead, which keeps the record and stops it ordering.",
            "Delete",
            async () => {
                try {
                    await api.delete(`/api/admin/x/b2b/companies/${id}`);
                    toast.success(`Deleted ${company.name}`);
                    await goto(`${base}/b2b/companies`);
                } catch (err) {
                    toast.error(err);
                }
            },
        );
    }

    function showOverdue() {
        orderList.set({ overdue: true });
        ordersHeading?.scrollIntoView({ behavior: "smooth", block: "start" });
        ordersHeading?.focus({ preventScroll: true });
    }

    /* A buyer whose address moved to one they have not confirmed has none in
       the company's group, so the engine sends the email blank. Their name is
       what is left to call them. */
    const who = (m) => m.name || m.email || `Account ${m.customer_id}`;
    const awaiting = (m) => m.confirmed === false || !m.email;

    const openOrder = (o) => goto(`${base}/orders/${o.order_id}`);
</script>

<svelte:head><title>{company?.name ?? "Company"} · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="companies.read" what="companies" />
{:else}
    <div class="page page-company b2b-page shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <a class="breadcrumb-item tw:text-sm" href="{base}/b2b/companies">Companies</a>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">
                        {company?.name ?? "…"}
                    </div>
                </nav>
                <div class="flex-fill"></div>
                {#if company && writable}
                    <div class="page-header-primary-btns">
                        <button type="button" class="btn transparent danger" aria-label="Delete company" onclick={askDelete}>
                            <i class="ri-delete-bin-line" aria-hidden="true"></i>
                            <span class="txt">Delete</span>
                        </button>
                        <button type="button" class="btn secondary" aria-label="Edit company" onclick={() => (editorOpen = true)}>
                            <i class="ri-edit-line" aria-hidden="true"></i>
                            <span class="txt">Edit</span>
                        </button>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="b2b" what="Companies are served by ext/b2b, and this binary does not have it." />
            {:else if notFound}
                <div class="wrapper sm m-auto txt-center p-t-base">
                    <h5 class="m-b-xs">This company does not exist</h5>
                    <p class="txt-hint">It may have been deleted.</p>
                    <a href="{base}/b2b/companies" class="btn secondary m-t-sm"><span class="txt">All companies</span></a>
                </div>
            {:else if loading && !company}
                <span class="skeleton-loader"></span>
                <span class="skeleton-loader"></span>
            {:else if company}
                {#if company.status === "on_hold"}
                    <div class="alert warning m-b-base b2b-notice">
                        <p>
                            <strong>On hold.</strong> Buyers can still order, but not on account — they pay
                            at checkout until the hold is lifted.
                        </p>
                    </div>
                {:else if company.status === "closed"}
                    <div class="alert danger m-b-base b2b-notice">
                        <p><strong>Closed.</strong> Its buyers cannot order or ask for quotes.</p>
                    </div>
                {/if}

                <section class="tw:rounded-xl tw:border tw:bg-card tw:p-5 m-b-base">
                    <div class="tw:flex tw:flex-wrap tw:items-center tw:gap-2">
                        <h2 class="tw:m-0 tw:text-base tw:font-semibold">{company.name}</h2>
                        <span class="label {companyStatusClass(company.status)}">{companyStatusLabel(company.status)}</span>
                        <span class="txt-hint txt-sm txt-code">{company.code}</span>
                    </div>
                    <dl class="b2b-facts">
                        <div>
                            <dt>Terms</dt>
                            <dd>{termsLabel(company)}</dd>
                        </div>
                        <div>
                            <dt>Approval</dt>
                            <dd>
                                {#if company.approval_threshold}
                                    Over {formatMoney(company.approval_threshold)}
                                {:else}
                                    <span class="txt-hint">Never needed</span>
                                {/if}
                            </dd>
                        </div>
                        <div>
                            <dt>Purchase orders</dt>
                            <dd>{company.require_po ? "Required on every order" : "Optional"}</dd>
                        </div>
                        <div>
                            <dt>Customer group</dt>
                            <dd>
                                {#if company.group_id}
                                    {groupName || "…"}
                                {:else}
                                    <span class="txt-hint">None — the store's own prices</span>
                                {/if}
                            </dd>
                        </div>
                        <div>
                            <dt>Tax number</dt>
                            <dd class:txt-code={!!company.tax_id}>
                                {#if company.tax_id}{company.tax_id}{:else}<span class="txt-hint">—</span>{/if}
                            </dd>
                        </div>
                        <div>
                            <dt>Added</dt>
                            <dd>{formatDate(company.created_at, { withTime: false })}</dd>
                        </div>
                    </dl>
                    {#if company.notes}
                        <p class="tw:mt-4 tw:mb-0 tw:text-sm tw:whitespace-pre-line">{company.notes}</p>
                    {/if}
                    <div class="tw:mt-4 tw:flex tw:flex-wrap tw:gap-x-5 tw:gap-y-1">
                        {#if can("quotes.read")}
                            <a class="b2b-more" href="{base}/b2b/quotes?company_id={company.id}">Quotes <span aria-hidden="true">→</span></a>
                        {/if}
                        <a class="b2b-more" href="{base}/b2b/approvals?company_id={company.id}">Approval requests <span aria-hidden="true">→</span></a>
                        {#if can("leads.read")}
                            <a class="b2b-more" href="{base}/b2b/leads?company_id={company.id}">Leads sent to it <span aria-hidden="true">→</span></a>
                        {/if}
                    </div>
                </section>

                <h2 class="section-title">
                    <i class="ri-bank-card-line" aria-hidden="true"></i>
                    Account
                </h2>
                {#if credit}
                    <div class="tw:grid tw:grid-cols-2 tw:gap-3 tw:lg:grid-cols-4 m-b-base">
                        <div class="b2b-tile">
                            <span class="b2b-tile-label">Credit limit</span>
                            <span class="b2b-tile-value">{credit.limit ? formatMoney(credit.limit) : "No account"}</span>
                            <span class="b2b-tile-hint">
                                {credit.limit ? termsLabel(company) : "Its buyers pay at checkout"}
                            </span>
                        </div>
                        <div class="b2b-tile">
                            <span class="b2b-tile-label">Outstanding</span>
                            <span class="b2b-tile-value">{formatMoney(credit.outstanding)}</span>
                            <span class="b2b-tile-hint">Ordered on account, not yet paid</span>
                        </div>
                        <div class="b2b-tile">
                            <span class="b2b-tile-label">Available</span>
                            <span class="b2b-tile-value">{credit.available ? formatMoney(credit.available) : "—"}</span>
                            <span class="b2b-tile-hint">What the next order may come to</span>
                        </div>
                        <button
                            type="button"
                            class="b2b-tile b2b-tile-action"
                            class:is-bad={credit.overdue_orders > 0}
                            aria-pressed={orderList.params.overdue}
                            disabled={!credit.overdue_orders}
                            onclick={showOverdue}
                        >
                            <span class="b2b-tile-label">Overdue</span>
                            <span class="b2b-tile-value">{formatMoney(credit.overdue)}</span>
                            <span class="b2b-tile-hint">
                                {#if credit.overdue_orders}
                                    {credit.overdue_orders}
                                    {pluralize(credit.overdue_orders, "order")} past due — show them
                                {:else}
                                    Nothing past due
                                {/if}
                            </span>
                        </button>
                    </div>
                {/if}

                <h2 class="section-title">
                    <i class="ri-team-line" aria-hidden="true"></i>
                    Buyers
                </h2>
                <p class="field-help m-b-sm">
                    Shopper accounts that buy for {company.name}. What a role may do is the company's
                    business: its admins manage these people from the storefront too.
                </p>

                <div class="page-table-wrapper tw:rounded-xl tw:border m-b-sm">
                    <table class="table responsive-table">
                        <thead>
                            <tr>
                                <th class="col-field-name-id">Buyer</th>
                                <th class="min-width">Role</th>
                                <th class="min-width">Joined</th>
                                {#if writable}<th class="min-width" aria-label="Remove"></th>{/if}
                            </tr>
                        </thead>
                        <tbody>
                            {#each members as member (member.customer_id)}
                                <tr>
                                    <td class="col-field-name-id" data-name="Buyer">
                                        <div class="row-name row-name-stacked">
                                            <span class="txt-bold">{who(member)}</span>
                                            {#if awaiting(member)}
                                                <span>
                                                    <span
                                                        class="label label-warning b2b-awaiting"
                                                        title="They moved to an address they have not confirmed, so their carts are priced like anybody's until they do."
                                                        >Awaiting confirmation</span
                                                    >
                                                </span>
                                            {:else if member.name}
                                                <span class="txt-hint txt-sm">{member.email}</span>
                                            {/if}
                                        </div>
                                    </td>
                                    <td class="min-width" data-name="Role">
                                        {#if writable}
                                            <div class="field b2b-role">
                                                <Select
                                                    ariaLabel="Role for {who(member)}"
                                                    value={member.role}
                                                    options={ROLES.map((r) => ({ value: r.value, label: r.label }))}
                                                    disabled={busy === `role-${member.customer_id}`}
                                                    onchange={(v) => changeRole(member, v)}
                                                />
                                            </div>
                                        {:else}
                                            <span class="label">{roleLabel(member.role)}</span>
                                        {/if}
                                    </td>
                                    <td class="min-width txt-hint txt-sm" data-name="Joined">
                                        {formatDate(member.created_at, { withTime: false })}
                                    </td>
                                    {#if writable}
                                        <td class="min-width b2b-actions">
                                            <button
                                                type="button"
                                                class="btn circle sm transparent danger"
                                                title="Remove {who(member)}"
                                                aria-label="Remove {who(member)}"
                                                onclick={() => askRemove(member)}
                                            >
                                                <i class="ri-user-unfollow-line" aria-hidden="true"></i>
                                            </button>
                                        </td>
                                    {/if}
                                </tr>
                            {/each}
                            {#if !members.length}
                                <tr>
                                    <td colspan="4" class="txt-hint txt-center p-base">
                                        Nobody buys for {company.name} yet, so it cannot order. Add its
                                        first buyer below — make them an admin so they can add the rest.
                                    </td>
                                </tr>
                            {/if}
                        </tbody>
                    </table>
                </div>

                {#if writable}
                    <form class="b2b-inline-form m-b-base" onsubmit={addBuyer} novalidate>
                        <div class="field b2b-grow" class:error={!!addError}>
                            <label for="buyer-email">Add a buyer by email</label>
                            <input
                                id="buyer-email"
                                type="email"
                                autocomplete="off"
                                placeholder="name@company.com"
                                bind:value={addForm.email}
                                oninput={() => (addError = "")}
                            />
                        </div>
                        <div class="field b2b-fixed">
                            <label for="buyer-role">Role</label>
                            <Select
                                id="buyer-role"
                                bind:value={addForm.role}
                                options={ROLES.map((r) => ({ value: r.value, label: r.label }))}
                            />
                        </div>
                        <button type="submit" class="btn" class:loading={adding} disabled={adding}>
                            <i class="ri-user-add-line" aria-hidden="true"></i>
                            <span class="txt">Add buyer</span>
                        </button>
                    </form>
                    <div class="field-help m-b-base">
                        {#if addError}
                            <span class="txt-danger">{addError}</span>
                        {:else}
                            {ROLES.find((r) => r.value === addForm.role)?.hint}. An account whose address is
                            already confirmed joins at once; anybody else is emailed an invitation and joins
                            when they accept it.
                        {/if}
                    </div>
                {/if}

                {#if invited}
                    <div class="alert info m-b-base b2b-notice" role="status">
                        <div class="tw:flex tw:items-start tw:gap-3">
                            <p class="tw:flex-1">
                                <strong>Invitation sent to {invited.email}.</strong> Their address is not
                                confirmed yet, so they join as {roleLabel(invited.role).toLowerCase()} when
                                they accept the emailed link. It works until {formatDate(invited.expires_at)}.
                            </p>
                            <button
                                type="button"
                                class="btn circle sm transparent secondary"
                                aria-label="Dismiss"
                                onclick={() => (invited = null)}
                            >
                                <i class="ri-close-line" aria-hidden="true"></i>
                            </button>
                        </div>
                    </div>
                {/if}

                {#if invitations.length}
                    <h2 class="section-title">
                        <i class="ri-mail-send-line" aria-hidden="true"></i>
                        Open invitations
                    </h2>
                    <div class="page-table-wrapper tw:rounded-xl tw:border m-b-base">
                        <table class="table responsive-table">
                            <thead>
                                <tr>
                                    <th class="col-field-name-id">Sent to</th>
                                    <th class="min-width">Role</th>
                                    <th class="min-width">Sent by</th>
                                    <th class="min-width">Expires</th>
                                    {#if writable}<th class="min-width" aria-label="Withdraw"></th>{/if}
                                </tr>
                            </thead>
                            <tbody>
                                {#each invitations as invitation (invitation.id)}
                                    <tr>
                                        <td class="col-field-name-id" data-name="Sent to">
                                            <span class="txt-bold">{invitation.email}</span>
                                        </td>
                                        <td class="min-width" data-name="Role">
                                            <span class="label">{roleLabel(invitation.role)}</span>
                                        </td>
                                        <td class="min-width txt-hint txt-sm" data-name="Sent by">
                                            {invitation.invited_by || "—"}
                                        </td>
                                        <td class="min-width txt-hint txt-sm" data-name="Expires">
                                            {formatDate(invitation.expires_at)}
                                        </td>
                                        {#if writable}
                                            <td class="min-width b2b-actions">
                                                <button
                                                    type="button"
                                                    class="btn sm transparent danger"
                                                    onclick={() => askRevoke(invitation)}
                                                >
                                                    <span class="txt">Withdraw</span>
                                                </button>
                                            </td>
                                        {/if}
                                    </tr>
                                {/each}
                            </tbody>
                        </table>
                    </div>
                {/if}

                <h2 class="section-title">
                    <i class="ri-map-2-line" aria-hidden="true"></i>
                    Territories
                </h2>
                <CompanyTerritories companyId={company.id} companyName={company.name} {writable} />

                <div class="b2b-section-head">
                    <h2 class="section-title" tabindex="-1" bind:this={ordersHeading}>
                        <i class="ri-file-list-3-line" aria-hidden="true"></i>
                        Orders
                    </h2>
                    <div class="field b2b-order-search">
                        <input
                            type="search"
                            aria-label="Search this company's orders"
                            placeholder="PO or order number"
                            autocomplete="off"
                            bind:value={orderDraft}
                            oninput={(e) => orderList.set({ oq: e.currentTarget.value.trim() })}
                        />
                    </div>
                    <div class="field b2b-filter">
                        <Select
                            ariaLabel="Which orders"
                            value={orderList.params.overdue ? "overdue" : ""}
                            options={[
                                { value: "", label: "Every order" },
                                { value: "overdue", label: "Overdue only" },
                            ]}
                            onchange={(v) => orderList.set({ overdue: v === "overdue" })}
                        />
                    </div>
                </div>

                <div
                    class="page-table-wrapper tw:rounded-xl tw:border"
                    class:faded={ordersLoading && orders.length > 0}
                >
                    <table class="table responsive-table">
                        <thead>
                            <tr>
                                <th class="col-field-name-id">Order</th>
                                <th class="min-width">PO number</th>
                                <th class="min-width">Placed by</th>
                                <th class="col-field-type-number min-width">Total</th>
                                <th class="min-width">Payment</th>
                                <th class="min-width">Due</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#if ordersLoading && !orders.length}
                                <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                            {/if}
                            {#each orders as order (order.order_id)}
                                <tr
                                    class="handle"
                                    tabindex="0"
                                    onclick={() => openOrder(order)}
                                    onkeydown={(e) => rowKey(e, () => openOrder(order))}
                                >
                                    <td class="col-field-name-id" data-name="Order">
                                        <div class="row-name row-name-stacked">
                                            <a
                                                href="{base}/orders/{order.order_id}"
                                                class="txt-bold txt-code"
                                                onclick={(e) => e.stopPropagation()}>{order.number}</a
                                            >
                                            <span class="txt-hint txt-sm">{formatDate(order.created_at)}</span>
                                        </div>
                                    </td>
                                    <td class="min-width" data-name="PO number">
                                        {#if order.po_number}<span class="txt-code">{order.po_number}</span>{:else}<span class="txt-hint">—</span>{/if}
                                    </td>
                                    <td class="min-width txt-sm" data-name="Placed by">{order.placed_by || "—"}</td>
                                    <td class="col-field-type-number min-width" data-name="Total">{formatMoney(order.total)}</td>
                                    <td class="min-width" data-name="Payment">
                                        <span class="label label-{paymentStatusClass(order.payment_status)}">{order.payment_status}</span>
                                        {#if !order.on_account}<div class="txt-hint txt-sm">paid at checkout</div>{/if}
                                    </td>
                                    <td class="min-width" data-name="Due">
                                        {#if order.due_at}
                                            <div class="inline-flex gap-5">
                                                <span class="txt-sm">{formatDate(order.due_at, { withTime: false })}</span>
                                                {#if order.overdue}<span class="label label-danger">Overdue</span>{/if}
                                            </div>
                                        {:else}
                                            <span class="txt-hint">—</span>
                                        {/if}
                                    </td>
                                </tr>
                            {/each}
                            {#if !ordersLoading && !orders.length}
                                <tr>
                                    <td colspan="6" class="txt-hint txt-center p-base">
                                        {#if orderList.params.oq}
                                            No order matches “{orderList.params.oq}”.
                                        {:else if orderList.params.overdue}
                                            Nothing is overdue.
                                        {:else}
                                            {company.name} has not ordered yet.
                                        {/if}
                                    </td>
                                </tr>
                            {/if}
                        </tbody>
                    </table>
                </div>
                <!-- Not the page footer: that one is sticky, and an orders count
                     pinned to the bottom of the window reads as the count of
                     whatever is on screen — the buyers, the tiles. -->
                {#if ordersMeta?.total}
                    <div class="b2b-pager">
                        <Pager
                            meta={ordersMeta}
                            loading={ordersLoading}
                            noun="order"
                            onpage={(n) => orderList.setPage(n)}
                        />
                    </div>
                {/if}

                <h2 class="section-title">
                    <i class="ri-file-text-line" aria-hidden="true"></i>
                    Statement
                </h2>
                <p class="field-help m-b-sm">
                    The account over a period, as {company.name} would be sent it: every order on account,
                    every payment on the day the store recorded it, and what is owed by how late it is.
                </p>
                <CompanyStatement companyId={company.id} companyCode={company.code} />

                <h2 class="section-title">
                    <i class="ri-history-line" aria-hidden="true"></i>
                    Terms history
                </h2>
                <CompanyHistory companyId={company.id} version={historyVersion} />
            {/if}
        </div>
    </div>

    <CompanyEditor open={editorOpen} {company} onclose={() => (editorOpen = false)} onsaved={saved} />

    <Confirm
        bind:open={confirmOpen}
        title={confirmAsk.title}
        message={confirmAsk.message}
        confirmLabel={confirmAsk.label}
        danger
        onconfirm={() => confirmAsk.run?.()}
    />
{/if}
