<script>
    /**
     * Gift cards: money shoppers hold with the store (D250).
     *
     * A list of every card with what is left on it, a way to issue one by
     * hand — a present, an apology, credit for a return — and each card's
     * history. Cards a shopper buys are made when their order is paid, from a
     * variant marked "Sold as a gift card".
     */
    import { api, can, query, request } from "$lib/api.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { formatDate, formatMoney, isValidMoney, relativeTime, toMinor } from "$lib/format.js";
    import { settings } from "$lib/settings.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { base } from "$app/paths";
    import Drawer from "$lib/components/Drawer.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const PER_PAGE = 25;
    const readable = $derived(can("discounts.read"));
    const writable = $derived(can("discounts.write"));
    const currency = $derived(settings.currency);

    const list = listState({ q: "", page: 1, limit: PER_PAGE });
    const perPage = $derived(list.params.limit);
    let draft = $state(list.params.q);
    let rows = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    let issueOpen = $state(false);
    let issuing = $state(false);
    let form = $state({ amount: "", email: "", note: "" });
    // The card just issued, shown once with its whole code.
    let issued = $state(null);

    let open = $state(null);
    let saving = $state(false);

    $effect(() => {
        list.params;
        if (readable) load();
    });

    async function load() {
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/gift-cards" + query({ q: list.params.q, page: list.page, limit: perPage }),
            );
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

    // A code read aloud or typed from a card is easier in groups of four.
    const grouped = (code) => code?.match(/.{1,4}/g)?.join("-") ?? "";

    async function issue() {
        issuing = true;
        try {
            const card = await request("POST", "/api/admin/gift-cards", {
                body: {
                    amount_minor: toMinor(form.amount, currency),
                    email: form.email.trim(),
                    note: form.note.trim(),
                },
            });
            issued = card.data ?? card;
            form = { amount: "", email: "", note: "" };
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            issuing = false;
        }
    }

    async function show(row) {
        try {
            const result = await api.get(`/api/admin/gift-cards/${row.id}`);
            open = result.data ?? result;
        } catch (err) {
            toast.error(err);
        }
    }

    async function setActive(active) {
        saving = true;
        try {
            const result = await request("PATCH", `/api/admin/gift-cards/${open.id}`, { body: { active } });
            open = result.data ?? result;
            toast.success(active ? "Gift card switched on" : "Gift card switched off");
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function copy(code) {
        try {
            await navigator.clipboard.writeText(code);
            toast.success("Code copied");
        } catch {
            toast.error("Could not copy; select the code and copy it instead");
        }
    }
</script>

<svelte:head><title>{titled("Gift cards")}</title></svelte:head>

<div class="page page-gift-cards shopify-skin">
    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="tw:text-2xl tw:font-semibold tw:tracking-tight">Gift cards</div>
            </nav>

            {#if readable}
                <form class="fields searchbar" onsubmit={submitSearch}>
                    <div class="field">
                        <input type="text" class="p-l-20" placeholder="Search by email, last 4 or order" bind:value={draft} />
                    </div>
                    {#if draft || list.params.q}
                        <div class="field addon p-r-5">
                            {#if draft !== list.params.q}
                                <button type="submit" class="btn sm pill warning">Search</button>
                            {/if}
                            <button type="button" class="btn sm pill secondary transparent" onclick={() => ((draft = ""), list.set({ q: "" }))}>
                                Clear
                            </button>
                        </div>
                    {/if}
                </form>

                {#if writable}
                    <div class="page-header-primary-btns">
                        <button type="button" class="btn expanded" onclick={() => ((issued = null), (issueOpen = true))}>
                            <i class="ri-add-line" aria-hidden="true"></i>
                            <span class="txt">Issue gift card</span>
                        </button>
                    </div>
                {/if}
            {/if}
        </header>

        {#if !readable}
            <NoAccess right="discounts.read" what="gift cards" />
        {:else}
            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-name-id">Code</th>
                            <th class="col-field-type-text">For</th>
                            <th class="col-field-type-number min-width">Left</th>
                            <th class="col-field-type-select min-width">Status</th>
                            <th class="col-field-type-date min-width">Issued</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each rows as row (row.id)}
                            <tr class="gc-row" tabindex="0" onclick={() => show(row)} onkeydown={(e) => e.key === "Enter" && show(row)}>
                                <td class="col-field-name-id" data-name="Code"><span class="txt-bold">…{row.last4}</span></td>
                                <td class="col-field-type-text" data-name="For">
                                    {row.email || "—"}
                                    {#if row.order_number}<span class="txt-hint"> · order {row.order_number}</span>{/if}
                                </td>
                                <td class="col-field-type-number min-width" data-name="Left">
                                    {formatMoney(row.balance)}
                                    <span class="txt-hint">of {formatMoney(row.initial)}</span>
                                </td>
                                <td class="col-field-type-select min-width" data-name="Status">
                                    {#if !row.active}
                                        <span class="label">Off</span>
                                    {:else if row.expires_at && new Date(row.expires_at) < new Date()}
                                        <span class="label">Expired</span>
                                    {:else if !row.balance.amount_minor}
                                        <span class="label">Spent</span>
                                    {:else}
                                        <span class="label label-success">Active</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-date min-width txt-hint" data-name="Issued" title={formatDate(row.created_at)}>
                                    {relativeTime(row.created_at)}
                                </td>
                            </tr>
                        {/each}
                        {#if loading && !rows.length}
                            {#each Array(6) as _, i (i)}
                                <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !rows.length}
                            <tr class="empty-row">
                                <td colspan="5">
                                    {#if list.params.q}
                                        <EmptyState
                                            icon="ri-filter-off-line"
                                            title="No gift cards match"
                                            text="Try another search, or clear it to see every card."
                                            onclear={() => ((draft = ""), list.clear())}
                                        />
                                    {:else}
                                        <EmptyState
                                            icon="ri-gift-line"
                                            title="No gift cards yet"
                                            text="Issue one by hand, or sell them: mark a product's variant as sold as a gift card, and each one bought becomes a card when its order is paid."
                                        >
                                            {#if writable}
                                                <button type="button" class="btn" onclick={() => ((issued = null), (issueOpen = true))}>
                                                    <i class="ri-add-line" aria-hidden="true"></i>
                                                    <span class="txt">Issue gift card</span>
                                                </button>
                                            {/if}
                                        </EmptyState>
                                    {/if}
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>
            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <Pager {meta} {loading} noun="gift card" {perPage} onpage={(n) => list.setPage(n)} onperpage={(n) => list.set({ limit: n })} />
                <div class="flex-fill"></div>
            </footer>
        {/if}
    </div>
</div>

<Drawer open={issueOpen} size="sm" title={issued ? "Gift card issued" : "Issue a gift card"} onclose={() => (issueOpen = false)}>
    {#if issued}
        <p>Give this code to {issued.email || "the person it is for"}. It is shown in full only here and on the card's page.</p>
        <div class="gc-code">
            <code>{grouped(issued.code)}</code>
            <button type="button" class="btn sm secondary" onclick={() => copy(grouped(issued.code))}>
                <i class="ri-file-copy-line" aria-hidden="true"></i><span class="txt">Copy</span>
            </button>
        </div>
        <p class="txt-hint">Worth {formatMoney(issued.initial)}. Shoppers type it at checkout, and it pays as much of the order as it holds.</p>
    {:else}
        <div class="field">
            <label for="gc-amount">Amount</label>
            <input id="gc-amount" type="text" inputmode="decimal" bind:value={form.amount} placeholder="500" />
        </div>
        <div class="field m-t-sm">
            <label for="gc-email">For (email, optional)</label>
            <input id="gc-email" type="email" bind:value={form.email} />
        </div>
        <div class="field m-t-sm">
            <label for="gc-note">Note (only you see it)</label>
            <input id="gc-note" type="text" bind:value={form.note} placeholder="Credit for the returned ring" />
        </div>
        <div class="field-help">Nothing is sent. Pass the code on yourself, by WhatsApp or email.</div>
    {/if}
    {#snippet footer()}
        {#if issued}
            <button type="button" class="btn m-l-auto" onclick={() => (issueOpen = false)}><span class="txt">Done</span></button>
        {:else}
            <button type="button" class="btn transparent m-r-auto" onclick={() => (issueOpen = false)}><span class="txt">Cancel</span></button>
            <button
                type="button"
                class="btn"
                class:loading={issuing}
                disabled={issuing || !isValidMoney(form.amount) || !(toMinor(form.amount, currency) > 0)}
                onclick={issue}
            >
                <span class="txt">Issue</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<Drawer open={!!open} size="md" title={open ? `Gift card …${open.last4}` : ""} onclose={() => (open = null)}>
    {#if open}
        <div class="gc-code">
            <code>{grouped(open.code)}</code>
            <button type="button" class="btn sm secondary" onclick={() => copy(grouped(open.code))}>
                <i class="ri-file-copy-line" aria-hidden="true"></i><span class="txt">Copy</span>
            </button>
        </div>
        <p class="m-t-sm">
            <span class="txt-bold">{formatMoney(open.balance)}</span> left of {formatMoney(open.initial)}
            {#if !open.active}<span class="label m-l-5">Off</span>{/if}
        </p>
        <p class="txt-hint">
            {#if open.order_id}
                Bought on <a href="{base}/dash/orders/{open.order_id}">order {open.order_number}</a>.
            {:else}
                Issued by hand.
            {/if}
            {#if open.email}For {open.email}.{/if}
            {#if open.note}{open.note}{/if}
        </p>

        <h6 class="m-t-base">History</h6>
        {#if open.uses?.length}
            <ul class="gc-uses">
                {#each open.uses as use, i (i)}
                    <li>
                        <span class="txt-money">{use.amount.amount_minor > 0 ? "+" : "−"}{formatMoney({ ...use.amount, amount_minor: Math.abs(use.amount.amount_minor) })}</span>
                        <span>
                            {#if use.order_id}<a href="{base}/dash/orders/{use.order_id}">{use.reason}</a>{:else}{use.reason}{/if}
                        </span>
                        <span class="txt-hint" title={formatDate(use.created_at)}>{relativeTime(use.created_at)}</span>
                    </li>
                {/each}
            </ul>
        {:else}
            <p class="txt-hint">Not spent yet.</p>
        {/if}
    {/if}
    {#snippet footer()}
        {#if open && writable}
            <button type="button" class="btn transparent m-r-auto" onclick={() => (open = null)}><span class="txt">Close</span></button>
            <button type="button" class="btn {open.active ? 'secondary' : ''}" class:loading={saving} disabled={saving} onclick={() => setActive(!open.active)}>
                <span class="txt">{open.active ? "Switch off" : "Switch on"}</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<style>
    .gc-code {
        display: flex;
        align-items: center;
        gap: 12px;
        flex-wrap: wrap;
        margin: 8px 0;
    }
    .gc-code code {
        font-size: 1.1rem;
        letter-spacing: 0.06em;
        overflow-wrap: anywhere;
    }
    .gc-uses {
        list-style: none;
        margin: 8px 0 0;
        padding: 0;
    }
    .gc-uses li {
        display: grid;
        grid-template-columns: auto 1fr auto;
        gap: 12px;
        padding: 8px 0;
        border-top: 1px solid var(--borderColor);
    }
    .gc-row {
        cursor: pointer;
        transition: background-color 150ms ease-out;
    }
    @media (prefers-reduced-motion: reduce) {
        .gc-row {
            transition: none;
        }
    }
</style>
