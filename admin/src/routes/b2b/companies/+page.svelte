<script>
    /**
     * Companies: the businesses that buy from this store on their own terms.
     *
     * Not the same thing as Customers, which is every order grouped by the
     * address on it. A company is a record the store creates — a credit limit,
     * payment terms, a customer group whose prices its buyers get — and its
     * buyers are shopper accounts that joined it.
     *
     * Adding one goes straight to its page, because a company with no buyers
     * cannot order and the next thing anybody does is add them.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { can } from "$lib/session.svelte.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { onNewShortcut } from "$lib/shortcuts.js";
    import { toast } from "$lib/toast.svelte.js";
    import { formatDate, formatMoney } from "$lib/format.js";
    import { companyStatusClass, companyStatusLabel, termsLabel } from "$lib/b2b.js";
    import CompanyEditor from "$lib/components/CompanyEditor.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";

    const PER_PAGE = 50;
    const list = listState({ q: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);

    const readable = $derived(can("companies.read"));
    const writable = $derived(can("companies.write"));
    const missing = $derived(modulesKnown() && !hasModule("b2b"));

    let companies = $state([]);
    let meta = $state(null);
    let loading = $state(true);
    let draft = $state(list.params.q);
    let editorOpen = $state(false);
    let reqId = 0;

    $effect(() => {
        list.params;
        if (readable && hasModule("b2b")) load();
    });

    $effect(() => onNewShortcut(openNew));

    async function load() {
        const mine = ++reqId;
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/x/b2b/companies" +
                    query({ q: list.params.q, page: list.params.page, limit: list.params.limit }),
            );
            // A second search typed while the first was in flight must not be
            // overwritten by the first one's late answer.
            if (mine !== reqId) return;
            companies = result.data ?? [];
            meta = result.meta ?? null;
        } catch (err) {
            if (mine === reqId) toast.error(err);
        } finally {
            if (mine === reqId) loading = false;
        }
    }

    function openNew() {
        if (writable && !missing) editorOpen = true;
    }

    function search(event) {
        event.preventDefault();
        list.set({ q: draft.trim() });
    }

    async function created(company) {
        editorOpen = false;
        await goto(`${base}/b2b/companies/${company.id}`);
    }

    const open = (company) => goto(`${base}/b2b/companies/${company.id}`);
</script>

<svelte:head><title>Companies · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="companies.read" what="companies" />
{:else}
    <div class="page page-companies b2b-page shopify-skin">
        <div class="page-content full-height tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Customers</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Companies</div>
                </nav>
                <div class="flex-fill"></div>
                {#if writable && !missing}
                    <div class="page-header-primary-btns">
                        <button type="button" class="btn" aria-label="New company" onclick={openNew}>
                            <i class="ri-add-line" aria-hidden="true"></i>
                            <span class="txt">New company</span>
                        </button>
                    </div>
                {/if}
            </header>

            {#if missing}
                <ModuleMissing module="b2b" what="Companies are served by ext/b2b, and this binary does not have it." />
            {:else}
                <p class="field-help m-b-base">
                    A business that buys from this store on its own terms: a credit limit, payment
                    terms, and the prices of the customer group its buyers join. Its buyers are
                    shopper accounts, added on the company's page.
                </p>

                <form class="fields m-b-base" onsubmit={search} role="search">
                    <div class="field">
                        <label for="company-search">Search</label>
                        <input
                            id="company-search"
                            type="search"
                            placeholder="Name or code"
                            autocomplete="off"
                            bind:value={draft}
                            oninput={(e) => {
                                // Typing narrows as it goes; Enter only commits
                                // what is already there.
                                list.set({ q: e.currentTarget.value.trim() });
                            }}
                        />
                    </div>
                </form>

                <div class="page-table-wrapper tw:rounded-xl tw:border" class:faded={loading && companies.length > 0}>
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-name-id">Company</th>
                                <th class="min-width">Status</th>
                                <th class="col-field-type-number min-width">Credit limit</th>
                                <th class="min-width">Terms</th>
                                <th class="col-field-type-number min-width">Buyers</th>
                                <th class="min-width">Added</th>
                            </tr>
                        </thead>
                        <tbody>
                            {#if loading && !companies.length}
                                {#each Array(4) as _, i (i)}
                                    <tr><td colspan="6"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {/if}
                            {#each companies as company (company.id)}
                                <tr
                                    class="handle"
                                    tabindex="0"
                                    onclick={() => open(company)}
                                    onkeydown={(e) => rowKey(e, () => open(company))}
                                >
                                    <td class="col-field-name-id" data-name="Company">
                                        <div class="row-name row-name-stacked">
                                            <a
                                                href="{base}/b2b/companies/{company.id}"
                                                class="txt-bold"
                                                onclick={(e) => e.stopPropagation()}>{company.name}</a
                                            >
                                            <span class="txt-hint txt-sm txt-code">{company.code}</span>
                                        </div>
                                    </td>
                                    <td class="min-width" data-name="Status">
                                        <span class="label {companyStatusClass(company.status)}">
                                            {companyStatusLabel(company.status)}
                                        </span>
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Credit limit">
                                        {#if company.credit_limit}
                                            {formatMoney(company.credit_limit)}
                                        {:else}
                                            <span class="txt-hint">No account</span>
                                        {/if}
                                    </td>
                                    <td class="min-width" data-name="Terms">
                                        <span class:txt-hint={!company.credit_limit}>{termsLabel(company)}</span>
                                    </td>
                                    <td class="col-field-type-number min-width" data-name="Buyers">
                                        {company.member_count}
                                    </td>
                                    <td class="min-width txt-hint txt-sm" data-name="Added">
                                        {formatDate(company.created_at, { withTime: false })}
                                    </td>
                                </tr>
                            {/each}
                            {#if !loading && !companies.length}
                                <tr>
                                    <td colspan="6" class="txt-hint txt-center p-base">
                                        {#if list.params.q}
                                            No company's name or code matches “{list.params.q}”.
                                        {:else}
                                            No companies yet. Add one, then add the people who buy for
                                            it.
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
                        noun="company"
                        plural="companies"
                        {perPage}
                        onpage={(n) => list.setPage(n)}
                        onperpage={(n) => list.set({ limit: n })}
                    />
                    <div class="flex-fill"></div>
                </footer>
            {/if}
        </div>
    </div>

    <CompanyEditor open={editorOpen} onclose={() => (editorOpen = false)} onsaved={created} />
{/if}
