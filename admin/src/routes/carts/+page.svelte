<script>
    /**
     * Carts: the baskets that have not become orders yet.
     *
     * Read-only, and deliberately so. A cart belongs to the shopper, its token
     * is the only credential that may change it, and this API never returns
     * one — so an operator can see a basket and still cannot touch it. An
     * operator who wants to act places the order at POST /api/admin/orders.
     *
     * The screen opens on abandoned baskets with something in them, because
     * that is the question it exists to answer: how much is sitting in baskets
     * nobody came back to, and who can be reached about it. Both filters are
     * rendered in their active state rather than applied silently — the API
     * applies no hidden default, and one that quietly drops rows is worse than
     * a noisy one.
     */
    import { base } from "$app/paths";
    import { api, can, query } from "$lib/api.js";
    import { rowKey } from "$lib/rowkey.js";
    import { listState } from "$lib/liststate.svelte.js";
    import { formatMoney, formatDate, relativeTime, pluralize } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Pager from "$lib/components/Pager.svelte";
    import Select from "$lib/components/Select.svelte";
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

    /* The starting page size, not the only one: `limit` is a listState key, so
       an operator can change it and the choice rides in the URL with the rest of
       the screen's state. */
    const PER_PAGE = 25;

    /* The filters and the page live in the URL, and the window replaces the
       rows rather than accumulating them, so `?page=3` reopens as page 3 of the
       same list. "any" rather than "" is the all-states value because a
       parameter equal to its default is dropped from the address, and the
       default here is a view rather than "unfiltered". */
    const list = listState({
        state: "abandoned",
        has_email: "",
        has_lines: true,
        page: 1,
        limit: PER_PAGE,
    });
    const perPage = $derived(list.params.limit);

    /* A basket is an order that has not happened yet, so it reads under the
       same right — which is what the nav gates this screen on. */
    const readable = $derived(can("orders.read"));


    const state = $derived(list.params.state);
    const hasEmail = $derived(list.params.has_email);
    const hasLines = $derived(list.params.has_lines);

    let carts = $state([]);
    let meta = $state(null);
    let loading = $state(true);

    let detailOpen = $state(false);
    let cart = $state(null);
    let detailLoading = $state(false);

    /**
     * The `.label` colour for a cart's state, from PocketBase's five.
     *
     * Abandoned is a warning rather than a danger because nothing has gone
     * wrong and the state is recoverable — there is just money on the table,
     * and the whole point of keeping the row is that the basket is still there.
     */
    function cartStateClass(value) {
        if (value === "converted") return "success";
        if (value === "abandoned") return "warning";
        return "";
    }

    $effect(() => {
        // Any parameter changing reloads: the state, either chip, the page.
        list.params;
        load();
    });

    async function load() {
        // The screen is refused above; asking anyway would put a 403 toast
        // over the explanation.
        if (!readable) {
            loading = false;
            return;
        }
        loading = true;
        try {
            const result = await api.get(
                "/api/admin/carts" +
                    query({
                        // Sent explicitly rather than through list.query(): the
                        // screen's defaults are a view, and the engine
                        // deliberately has no defaults of its own to fall back
                        // on. The chip off means no filter at all rather than
                        // has_lines=false, because "any basket" is what turning
                        // it off asks for.
                        state: state === "any" ? "" : state,
                        has_email: hasEmail,
                        has_lines: hasLines ? "true" : "",
                        page: list.page,
                        limit: perPage,
                    }),
            );
            carts = result.data ?? [];
            meta = result.meta;
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    async function open(row) {
        // The row already holds everything the table showed, so the drawer
        // opens on it and fills in the lines when they arrive.
        cart = row;
        detailOpen = true;
        detailLoading = true;
        try {
            // api.get unwraps the envelope for anything without a meta block,
            // so this is the cart itself rather than `{ data }`.
            cart = await api.get("/api/admin/carts/" + row.id);
        } catch (err) {
            // Close rather than leave the row's summary standing with no lines
            // under it: a basket purged since the page loaded would otherwise
            // read as empty, which is a different fact from gone. The toast
            // carries the reason.
            detailOpen = false;
            toast.error(err);
        } finally {
            detailLoading = false;
        }
    }
</script>

<svelte:head><title>Carts · GoCommerce</title></svelte:head>

{#if !readable}
    <NoAccess right="orders.read" what="baskets" />
{:else}

<div class="page page-carts shopify-skin">
    <div class="page-content full-height">
        <header class="page-header">
            <nav class="breadcrumbs"><div>Carts</div></nav>

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

            <div class="page-header-primary-btns">
                <div class="field">
                    <Select
                        id="cart-state"
                        ariaLabel="State"
                        value={state}
                        options={[
                            { value: "abandoned", label: "Abandoned" },
                            { value: "live", label: "Live" },
                            { value: "converted", label: "Converted" },
                            { value: "any", label: "Any state" },
                        ]}
                        onchange={(v) => list.set({ state: v })}
                    />
                </div>
                <div class="field">
                    <Select
                        id="cart-email"
                        ariaLabel="Email"
                        value={hasEmail}
                        options={[
                            { value: "", label: "Anyone" },
                            { value: "true", label: "With an email" },
                            { value: "false", label: "Without an email" },
                        ]}
                        onchange={(v) => list.set({ has_email: v })}
                    />
                </div>
                <!--
                    The icon is not decoration: below 550px the panel's header
                    turns every button in this group into a circle and hides the
                    label that follows an icon, so a chip without one keeps its
                    words and is clipped by the circle. The pressed colour is
                    what survives on a phone, and the label stays on the button
                    for a reader who cannot see it.
                -->
                <button
                    type="button"
                    class="btn sm pill"
                    class:secondary={!hasLines}
                    aria-pressed={hasLines}
                    aria-label="Only baskets with something in them"
                    title="Only baskets with something in them"
                    onclick={() => list.set({ has_lines: !hasLines })}
                >
                    <i class="ri-shopping-basket-2-line" aria-hidden="true"></i>
                    <span class="txt">With something in them</span>
                </button>
            </div>
        </header>

        <div class="page-table-wrapper">
            <table class="table responsive-table">
                <thead class="sticky">
                    <tr>
                        <th class="col-field-name-id">Basket</th>
                        <th class="col-field-type-select">State</th>
                        <th class="col-field-type-number min-width">Items</th>
                        <th class="col-field-type-number min-width">Value</th>
                        <th class="col-field-type-date min-width">Last activity</th>
                        <th class="col-field-type-date min-width">Abandoned</th>
                        <th class="col-meta min-width"></th>
                    </tr>
                </thead>
                <tbody>
                    {#each carts as row (row.id)}
                        <tr
                            class="handle"
                            tabindex="0"
                            onclick={() => open(row)}
                            onkeydown={(e) => rowKey(e, () => open(row))}
                        >
                            <td class="col-field-name-id" data-name="Basket">
                                <div class="row-name">
                                    {#if row.email}
                                        <span class="txt-bold txt-ellipsis">{row.email}</span>
                                    {:else}
                                        <span class="txt-hint">No email</span>
                                    {/if}
                                    <span class="txt-hint txt-sm row-handle">#{row.id}</span>
                                </div>
                            </td>
                            <td class="col-field-type-select" data-name="State">
                                <span class="label {cartStateClass(row.state)}">{row.state}</span>
                                <!-- The transient explained rather than hidden:
                                     the state is what the cart is, the status is
                                     what the column says, and the five-minute
                                     sweeper is what is between them. -->
                                {#if row.state === "abandoned" && row.status === "open"}
                                    <div class="txt-hint txt-sm">awaiting the sweeper</div>
                                {/if}
                            </td>
                            <td class="col-field-type-number min-width" data-name="Items">
                                {row.item_count}
                                <span class="txt-hint txt-sm">
                                    {pluralize(row.item_count, "item")}
                                </span>
                            </td>
                            <td class="col-field-type-number min-width txt-bold" data-name="Value">
                                {formatMoney(row.subtotal)}
                            </td>
                            <td
                                class="col-field-type-date min-width txt-hint"
                                data-name="Last activity"
                                title={formatDate(row.updated_at)}
                            >
                                {relativeTime(row.updated_at)}
                            </td>
                            <td
                                class="col-field-type-date min-width txt-hint"
                                data-name="Abandoned"
                                title={row.abandoned_at ? formatDate(row.abandoned_at) : ""}
                            >
                                {row.abandoned_at ? relativeTime(row.abandoned_at) : "—"}
                            </td>
                            <td class="col-meta min-width">
                                <i class="ri-arrow-right-s-line" aria-hidden="true"></i>
                            </td>
                        </tr>
                    {/each}

                    {#if loading && !carts.length}
                        {#each Array(6) as _, i (i)}
                            <tr><td colspan="7"><span class="skeleton-loader"></span></td></tr>
                        {/each}
                    {:else if !carts.length}
                        <tr>
                            <td colspan="7" class="txt-center txt-hint p-base">
                                {list.pristine
                                    ? "Nothing abandoned yet. A basket lands here when a shopper leaves one longer than the cart TTL."
                                    : "No cart matches that. Try clearing a filter."}
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
                noun="basket"
                {perPage}
                onpage={(n) => list.setPage(n)}
                onperpage={(n) => list.set({ limit: n })}
            />
            <div class="flex-fill"></div>
            <ThemeToggle />
        </footer>
    </div>
</div>

<Drawer open={detailOpen} size="lg" title="Basket" onclose={() => (detailOpen = false)}>
    {#if cart}
        <!-- The order drawer's own card recipe, reused rather than re-cut: this
             is the same kind of thing seen a step earlier, and a second set of
             selectors saying the same thing is how the two drift apart. -->
        <div class="order-detail">
            <div class="order-status">
                <span class="label {cartStateClass(cart.state)}">{cart.state}</span>
                {#if cart.state === "abandoned" && cart.status === "open"}
                    <span class="txt-hint txt-sm">
                        still <code class="txt-code">open</code> — the sweeper has not reached it
                    </span>
                {/if}
                <div class="flex-fill"></div>
                <span class="txt-hint txt-sm">#{cart.id}</span>
            </div>

            <section class="order-card">
                <h6 class="order-card-title">Shopper</h6>
                {#if cart.email}
                    <!-- The entire operator-side recovery action, and it needs
                         no route: the panel has no business sending a
                         shopper's mail, and a cart carries no other handle. -->
                    <a href="mailto:{cart.email}" class="txt-bold">{cart.email}</a>
                    <div class="m-t-5">
                        <a
                            class="btn sm secondary"
                            href="{base}/orders?email={encodeURIComponent(cart.email)}"
                        >
                            <span class="txt">Their orders</span>
                        </a>
                    </div>
                {:else}
                    <p class="txt-hint">
                        No email. Nothing can be sent about this basket — an address only arrives
                        if the shopper types one before leaving.
                    </p>
                {/if}
                {#if cart.discount_code}
                    <p class="m-t-5">
                        Code <code class="txt-code">{cart.discount_code}</code>
                    </p>
                {/if}
            </section>

            <section class="order-card">
                <h6 class="order-card-title">In the basket</h6>
                <table class="table">
                    <thead>
                        <tr>
                            <th></th>
                            <th class="txt-right">Qty</th>
                            <th class="txt-right">Unit</th>
                            <th class="txt-right">Total</th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each cart.line_items || [] as line (line.id)}
                            <tr>
                                <td>
                                    <div>{line.title}</div>
                                    <div class="txt-hint txt-sm txt-code">
                                        {line.sku}{line.variant_label
                                            ? " · " + line.variant_label
                                            : ""}
                                    </div>
                                    <!-- What decides whether chasing this
                                         basket is worth anything. -->
                                    {#if line.price_changed}
                                        <span class="label warning">price changed</span>
                                    {/if}
                                    {#if !line.in_stock}
                                        <span class="label danger">out of stock</span>
                                    {/if}
                                </td>
                                <td class="txt-right">{line.quantity}</td>
                                <td class="txt-right">{formatMoney(line.unit_price)}</td>
                                <td class="txt-right">{formatMoney(line.total)}</td>
                            </tr>
                        {/each}
                        {#if detailLoading && !(cart.line_items || []).length}
                            <tr><td colspan="4"><span class="skeleton-loader"></span></td></tr>
                        {:else if !(cart.line_items || []).length}
                            <tr>
                                <td colspan="4" class="txt-hint">This basket is empty.</td>
                            </tr>
                        {/if}
                    </tbody>
                    <tfoot>
                        <tr>
                            <td colspan="3" class="txt-right txt-hint">Subtotal</td>
                            <td class="txt-right txt-bold">{formatMoney(cart.subtotal)}</td>
                        </tr>
                    </tfoot>
                </table>
            </section>

            <section class="order-card">
                <h6 class="order-card-title">Timeline</h6>
                <table class="table">
                    <tbody>
                        <tr>
                            <td class="txt-hint">Opened</td>
                            <td class="txt-right">{formatDate(cart.created_at)}</td>
                        </tr>
                        <tr>
                            <td class="txt-hint">Last activity</td>
                            <td class="txt-right">{formatDate(cart.updated_at)}</td>
                        </tr>
                        <tr>
                            <td class="txt-hint">Expires</td>
                            <td class="txt-right">{formatDate(cart.expires_at)}</td>
                        </tr>
                        <tr>
                            <td class="txt-hint">Abandoned</td>
                            <td class="txt-right">{formatDate(cart.abandoned_at)}</td>
                        </tr>
                    </tbody>
                </table>
            </section>

            {#if cart.metadata && Object.keys(cart.metadata).length}
                <section class="order-card">
                    <!-- Where a storefront puts the campaign a recovery
                         consumer matches on, so it is shown rather than
                         swallowed. -->
                    <h6 class="order-card-title">Metadata</h6>
                    <table class="table">
                        <tbody>
                            {#each Object.entries(cart.metadata) as [key, value] (key)}
                                <tr>
                                    <td class="txt-hint">{key}</td>
                                    <td class="txt-right txt-code">
                                        {typeof value === "string" ? value : JSON.stringify(value)}
                                    </td>
                                </tr>
                            {/each}
                        </tbody>
                    </table>
                </section>
            {/if}

            <p class="txt-hint txt-sm">
                Read-only. A basket belongs to the shopper — its token is the only credential that
                may change it, and this screen never sees one. To sell to them, place the order.
            </p>
        </div>
    {/if}
</Drawer>
{/if}
