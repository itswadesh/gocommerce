<script>
    /**
     * Accounts: shoppers who registered with this store, from ext/identity.
     *
     * NOT the Customers screen, which is every order grouped by the address
     * that placed it — derived, read-only, and keyed by email. These two
     * populations overlap and are not the same list, and the screens must never
     * cross-link: an account's id is an int64 in identity_customers, a derived
     * customer's key is an email address, and an order joins an account by its
     * own access token rather than by the address on it.
     *
     * Six read-only fields and a delete is the whole of what the route offers.
     * There is no address count, no order count and no last-seen, because the
     * API returns none — anything more needs new admin routes in ext/identity.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, can } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatDate } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 25;

    const list = listState({ q: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);

    const search = $derived(list.params.q);
    let draftSearch = $state(list.params.q);

    let accounts = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    let detailOpen = $state(false);
    let account = $state(null);
    let deleteOpen = $state(false);

    const missing = $derived(modulesKnown() && !hasModule("identity"));

    /* The same all-of the engine enforces on the route: erasing an account is
       not a read, and there is no customers.write to gate it with. */
    const erasable = $derived(can("customers.read") && can("store.operate"));

    $effect(() => {
        list.params;
        // hasModule() reads a `$state` object, so this re-runs when the answer
        // arrives — and a store without ext/identity never makes the call.
        if (can("customers.read") && hasModule("identity")) load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/x/identity/customers" + list.query({ limit: perPage }),
            );
            accounts = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (err.status !== 404) toast.error(err);
            accounts = [];
            meta = null;
        } finally {
            loading = false;
        }
    }

    function submitSearch(event) {
        event.preventDefault();
        list.set({ q: draftSearch.trim() });
    }

    function open(row) {
        account = row;
        detailOpen = true;
    }

    async function remove() {
        await api.delete(`/api/admin/x/identity/customers/${account.id}`);
        toast.success("Account deleted");
        detailOpen = false;
        account = null;
        list.setPage(1);
        load();
    }
</script>

<svelte:head><title>Accounts · GoCommerce</title></svelte:head>

{#if !can("customers.read")}
    <NoAccess right="customers.read" what="accounts" />
{:else}
    <div class="page page-accounts">
        <div class="page-content full-height">
            <header class="page-header">
                <nav class="breadcrumbs"><div>Accounts</div></nav>

                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        onclick={() => load()}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

                {#if !missing}
                    <form class="fields searchbar" onsubmit={submitSearch}>
                        <div class="field">
                            <input
                                type="text"
                                class="p-l-20"
                                placeholder="Search accounts by email or name"
                                bind:value={draftSearch}
                            />
                        </div>
                        {#if draftSearch || search}
                            <div class="field addon p-r-5">
                                {#if draftSearch !== search}
                                    <button type="submit" class="btn sm pill warning">Search</button>
                                {/if}
                                <button
                                    type="button"
                                    class="btn sm pill secondary transparent"
                                    onclick={() => {
                                        draftSearch = "";
                                        list.set({ q: "" });
                                    }}
                                >
                                    Clear
                                </button>
                            </div>
                        {/if}
                    </form>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing
                    module="identity"
                    what="Shopper accounts are served by ext/identity, and this binary does not have it. The Customers screen still works: it is every order grouped by the address that placed it, which needs no accounts at all."
                />
            {:else}
                <div class="wrapper m-b-sm">
                    <!-- The disambiguation the population overlap demands.
                         Without it, two screens over two different concepts
                         read as one screen shown twice. -->
                    <div class="field-help">
                        Accounts are shoppers who registered with this store. <strong>Customers</strong>
                        is every order grouped by the address that placed it — the two overlap and
                        are not the same list.
                    </div>
                </div>

                <div class="page-table-wrapper">
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-name-id">Account</th>
                                <th class="col-field-type-text min-width">Phone</th>
                                <th class="col-field-type-date min-width">Registered</th>
                                <th class="col-meta min-width"></th>
                            </tr>
                        </thead>
                        <tbody>
                            {#each accounts as row (row.id)}
                                <tr
                                    class="handle"
                                    tabindex="0"
                                    onclick={() => open(row)}
                                    onkeydown={(e) => rowKey(e, () => open(row))}
                                >
                                    <td class="col-field-name-id" data-name="Account">
                                        <div class="row-name">
                                            <span class="txt-bold txt-ellipsis">
                                                {row.name || row.email}
                                            </span>
                                            {#if row.name}
                                                <span class="txt-hint txt-sm row-handle">
                                                    {row.email}
                                                </span>
                                            {/if}
                                        </div>
                                    </td>
                                    <td class="col-field-type-text min-width txt-hint" data-name="Phone">
                                        {row.phone || "—"}
                                    </td>
                                    <td
                                        class="col-field-type-date min-width txt-hint"
                                        data-name="Registered"
                                    >
                                        {formatDate(row.created_at)}
                                    </td>
                                    <td class="col-meta min-width">
                                        <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                                    </td>
                                </tr>
                            {/each}

                            {#if loading && !accounts.length}
                                {#each Array(5) as _, i (i)}
                                    <tr><td colspan="4"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {:else if !accounts.length}
                                <tr>
                                    <td colspan="4" class="txt-center txt-hint p-base">
                                        {search
                                            ? "No account matches that. The search reads emails and names, never phone numbers."
                                            : "Nobody has registered yet. A shopper can still check out as a guest — an account is optional."}
                                    </td>
                                </tr>
                            {/if}
                        </tbody>
                    </table>
                </div>

                <footer class="page-footer">
                    <Pager
                        {meta}
                        {loading}
                        noun="account"
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
    title={account?.email || "Account"}
    onclose={() => (detailOpen = false)}
>
    {#if account}
        <div class="field readonly-field">
            <span class="readonly-label">Name</span>
            <span class="readonly-value">{account.name || "—"}</span>
        </div>
        <div class="field readonly-field">
            <span class="readonly-label">Email</span>
            <span class="readonly-value">{account.email}</span>
        </div>
        <div class="field readonly-field">
            <span class="readonly-label">Phone</span>
            <span class="readonly-value">{account.phone || "—"}</span>
        </div>
        <div class="field readonly-field">
            <span class="readonly-label">Registered</span>
            <span class="readonly-value">{formatDate(account.created_at)}</span>
        </div>
        <div class="field readonly-field">
            <span class="readonly-label">Last updated</span>
            <span class="readonly-value">{formatDate(account.updated_at)}</span>
        </div>
        <div class="field readonly-field">
            <span class="readonly-label">Account id</span>
            <span class="readonly-value txt-code">{account.id}</span>
        </div>

        <div class="m-t-base">
            <button
                type="button"
                class="btn sm secondary"
                onclick={() =>
                    goto(`${base}/orders?email=${encodeURIComponent(account.email)}`)}
            >
                <i class="ri-shopping-bag-3-line" aria-hidden="true"></i>
                <span class="txt">Orders placed with this address</span>
            </button>
            <!-- Exact, and it matters: an order joins an account by the order's
                 own access token, so what this link shows is every order typed
                 with the same address, which is not necessarily the same set. -->
            <div class="field-help">
                Matched by email address. An account's own claimed history is reachable only from
                the shopper's session, at <code class="txt-code">/x/identity/me/orders</code>.
            </div>
        </div>
    {/if}

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (detailOpen = false)}>
            <span class="txt">Close</span>
        </button>
        {#if erasable}
            <button type="button" class="btn danger" onclick={() => (deleteOpen = true)}>
                <i class="ri-delete-bin-7-line" aria-hidden="true"></i>
                <span class="txt">Delete account</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<Confirm
    bind:open={deleteOpen}
    title="Delete this account?"
    message={account
        ? `${account.email}, its sessions, its saved addresses and its claimed-order links are removed. The orders themselves belong to the store and stay.`
        : ""}
    confirmLabel="Delete"
    danger
    onconfirm={remove}
/>
