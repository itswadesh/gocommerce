<script>
    /**
     * One vendor, and what they are selling.
     *
     * An offer is this seller's price and stock for one variant. Several
     * sellers can offer the same variant — that is the point of the table —
     * and the variant keeps its own price and stock underneath all of it. Those
     * are the shop's own first-party offer, which is how every store that is
     * not a marketplace sells, and they are shown beside each offer so the
     * comparison is on the screen rather than in the operator's head.
     *
     * Nothing sells from an offer yet. Checkout still prices and reserves
     * against the variant, so this screen says so once rather than implying a
     * shopper can buy from a seller today.
     */
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { page } from "$app/state";
    import { api } from "$lib/api.js";
    import { can } from "$lib/session.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { formatMoney, parseMoney, isValidMoney, toMinor, fromMinor } from "$lib/format.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import VariantSearch from "$lib/components/VariantSearch.svelte";

    const id = $derived(page.params.id);
    const readable = $derived(can("vendors.read"));
    const writable = $derived(can("vendors.write"));
    const currency = $derived(settings.currency ?? "USD");

    let vendor = $state(null);
    let offers = $state([]);
    /* The variant behind each offer, by id: an offer carries a variant_id and
       nothing a person can read, and a screen listing bare numbers is a screen
       nobody can check. */
    let variants = $state({});
    let loading = $state(true);
    let working = $state(false);

    let confirmOpen = $state(false);
    let pendingWithdraw = $state(null);

    $effect(() => {
        id;
        load();
    });

    async function load() {
        if (!readable) {
            loading = false;
            return;
        }
        loading = true;
        try {
            vendor = await api.get(`/api/admin/vendors/${id}`);
            offers = (await api.get(`/api/admin/vendors/${id}/offers`)) ?? [];
            await loadVariants();
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
        }
    }

    /**
     * The variants the offers point at.
     *
     * One request each, because there is no "give me these ids" route and an
     * offer list is a seller's catalogue rather than the whole shop's. If that
     * ever gets long enough to matter, the fix is a filter on the variant
     * listing rather than a loop that got quietly slower.
     */
    async function loadVariants() {
        const wanted = [...new Set(offers.map((o) => o.variant_id))];
        const found = {};
        await Promise.all(
            wanted.map(async (variantId) => {
                try {
                    found[variantId] = await api.get(`/api/variants/${variantId}`);
                } catch {
                    // A variant deleted under an offer cascades the offer away,
                    // so this is a race rather than a state. The row renders
                    // with the id it has.
                    found[variantId] = null;
                }
            }),
        );
        variants = found;
    }

    async function addOffer(row) {
        if (!writable || working) return;
        if (offers.some((o) => o.variant_id === row.id)) {
            toast.error(`${vendor.name} already has an offer on ${row.sku}.`);
            return;
        }
        working = true;
        try {
            // Seeded from the variant's own price, because a seller listing a
            // thing usually starts from what the shop charges and adjusts.
            await api.put(`/api/admin/vendors/${id}/offers`, {
                variant_id: row.id,
                price_minor: row.price?.amount_minor ?? 0,
                stock_on_hand: 0,
            });
            toast.success(`Offering ${row.sku}`);
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            working = false;
        }
    }

    async function commitPrice(offer, raw) {
        if (!isValidMoney(raw)) return;
        const price_minor = toMinor(raw, currency);
        if (price_minor === offer.price.amount_minor) return;
        await save(offer, { price_minor });
    }

    async function commitStock(offer, raw) {
        const count = Number.parseInt(raw, 10);
        if (Number.isNaN(count) || count === offer.stock_on_hand) return;
        await save(offer, { stock_on_hand: count });
    }

    async function commitStatus(offer, status) {
        if (status === offer.status) return;
        await save(offer, { status });
    }

    /**
     * Every write sends the whole offer.
     *
     * PUT replaces rather than patches — one seller holds one row per variant,
     * and the route is an upsert — so sending only the field that changed would
     * reset the others to their defaults.
     */
    async function save(offer, changes) {
        working = true;
        try {
            await api.put(`/api/admin/vendors/${id}/offers`, {
                variant_id: offer.variant_id,
                price_minor: offer.price.amount_minor,
                stock_on_hand: offer.stock_on_hand,
                track_inventory: offer.track_inventory,
                status: offer.status,
                ...changes,
            });
            await load();
        } catch (err) {
            toast.error(err);
            await load();
        } finally {
            working = false;
        }
    }

    function askWithdraw(offer) {
        pendingWithdraw = offer;
        confirmOpen = true;
    }

    async function withdraw() {
        if (!pendingWithdraw) return;
        try {
            await api.delete(`/api/admin/vendors/${id}/offers/${pendingWithdraw.variant_id}`);
            toast.success("Offer withdrawn");
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            pendingWithdraw = null;
        }
    }

    const statusLabel = (s) =>
        ({ pending: "Pending", approved: "Approved", suspended: "Suspended" })[s] ?? s;
    const statusClass = (s) =>
        ({ pending: "label-warning", approved: "label-success", suspended: "label-danger" })[s] ?? "";

    const skuOf = (offer) => variants[offer.variant_id]?.sku ?? `variant ${offer.variant_id}`;
    const ownPrice = (offer) => variants[offer.variant_id]?.price ?? null;
</script>

<svelte:head><title>{vendor?.name ?? "Vendor"}</title></svelte:head>

{#if !readable}
    <NoAccess right="vendors.read" />
{:else}
    <div class="page-header-wrapper">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item">
                    <a href="{base}/vendors">Vendors</a>
                </div>
                <div class="breadcrumb-item">{vendor?.name ?? "…"}</div>
            </nav>
        </header>
    </div>

    <div class="page-content">
        {#if loading && !vendor}
            <span class="skeleton-loader"></span>
        {:else if vendor}
            <section class="order-card m-b-base">
                <div class="inline-flex gap-sm">
                    <h5 class="m-0">{vendor.name}</h5>
                    <span class="label {statusClass(vendor.status)}">{statusLabel(vendor.status)}</span>
                    {#if vendor.commission_bp}
                        <span class="label">{(vendor.commission_bp / 100).toFixed(2)}% commission</span>
                    {/if}
                </div>
                <div class="txt-hint txt-sm txt-code m-t-5">{vendor.slug}</div>
                {#if vendor.status !== "approved"}
                    <div class="field-help m-t-sm">
                        A vendor is invisible to shoppers until they are approved. Their offers are
                        kept either way.
                    </div>
                {/if}
                {#if vendor.about}
                    <p class="m-t-sm">{vendor.about}</p>
                {/if}
                <table class="table media-detail-facts m-t-sm">
                    <tbody>
                        {#if vendor.email}
                            <tr><td class="txt-hint">Email</td><td class="txt-right">{vendor.email}</td></tr>
                        {/if}
                        {#if vendor.phone}
                            <tr><td class="txt-hint">Phone</td><td class="txt-right">{vendor.phone}</td></tr>
                        {/if}
                        {#if vendor.tax_id}
                            <tr>
                                <td class="txt-hint">Tax number</td>
                                <td class="txt-right txt-code">{vendor.tax_id}</td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </section>

            <h6 class="section-title">
                <i class="ri-price-tag-3-line" aria-hidden="true"></i>
                What they are selling
            </h6>
            <div class="field-help m-b-sm">
                An offer is this seller's price for one variant. The shop's own price and stock stay
                on the variant and are shown beside each row. Nothing checks out through an offer
                yet — the engine still prices and reserves against the variant.
            </div>

            {#if writable}
                <div class="field m-b-base">
                    <label for="offer-add">Offer another variant</label>
                    <VariantSearch id="offer-add" disabled={working} onpick={addOffer} />
                </div>
            {/if}

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th>Variant</th>
                            <th class="col-field-type-number min-width">Their price</th>
                            <th class="col-field-type-number min-width">The shop's</th>
                            <th class="col-field-type-number min-width">Their stock</th>
                            <th class="min-width">Status</th>
                            <th class="col-meta min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each offers as offer (offer.id)}
                            <tr>
                                <td data-name="Variant">
                                    <span class="txt-code">{skuOf(offer)}</span>
                                </td>
                                <td class="col-field-type-number min-width" data-name="Their price">
                                    <div class="field money-field">
                                        <input
                                            type="text"
                                            inputmode="decimal"
                                            disabled={working || !writable}
                                            aria-label="Price for {skuOf(offer)}"
                                            value={fromMinor(offer.price.amount_minor, currency)}
                                            onchange={(e) => commitPrice(offer, e.currentTarget.value)}
                                        />
                                    </div>
                                </td>
                                <td class="col-field-type-number min-width txt-hint" data-name="The shop's">
                                    {#if ownPrice(offer)}
                                        {formatMoney(ownPrice(offer))}
                                    {:else}
                                        —
                                    {/if}
                                </td>
                                <td class="col-field-type-number min-width" data-name="Their stock">
                                    {#if offer.track_inventory}
                                        <div class="field">
                                            <input
                                                type="number"
                                                min="0"
                                                disabled={working || !writable}
                                                aria-label="Stock for {skuOf(offer)}"
                                                value={offer.stock_on_hand}
                                                onchange={(e) => commitStock(offer, e.currentTarget.value)}
                                            />
                                        </div>
                                    {:else}
                                        <span class="txt-hint">not tracked</span>
                                    {/if}
                                </td>
                                <td class="min-width" data-name="Status">
                                    {#if writable}
                                        <button
                                            type="button"
                                            class="btn sm transparent"
                                            disabled={working}
                                            title={offer.status === "active"
                                                ? "Pause this offer"
                                                : "Offer it again"}
                                            onclick={() =>
                                                commitStatus(
                                                    offer,
                                                    offer.status === "active" ? "paused" : "active",
                                                )}
                                        >
                                            <span class="label {offer.status === 'active' ? 'label-success' : ''}">
                                                {offer.status === "active" ? "Active" : "Paused"}
                                            </span>
                                        </button>
                                    {:else}
                                        <span class="label">{offer.status}</span>
                                    {/if}
                                </td>
                                <td class="col-meta min-width">
                                    {#if writable}
                                        <button
                                            type="button"
                                            class="btn circle sm transparent danger"
                                            disabled={working}
                                            title="Withdraw this offer"
                                            aria-label="Withdraw the offer on {skuOf(offer)}"
                                            onclick={() => askWithdraw(offer)}
                                        >
                                            <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                        </button>
                                    {/if}
                                </td>
                            </tr>
                        {/each}
                        {#if !loading && !offers.length}
                            <tr>
                                <td colspan="6" class="txt-hint txt-center p-base">
                                    This vendor is not offering anything yet.
                                </td>
                            </tr>
                        {/if}
                    </tbody>
                </table>
            </div>
        {/if}
    </div>

    <Confirm
        bind:open={confirmOpen}
        title="Withdraw this offer?"
        message="The variant keeps the shop's own price and stock. Pausing takes it off sale without losing the price they set."
        confirmLabel="Withdraw"
        danger
        onconfirm={withdraw}
    />
{/if}
