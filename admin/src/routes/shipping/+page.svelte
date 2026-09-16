<script>
    /**
     * Shipping: where this store delivers, and what it charges to.
     *
     * It sits beside Tax because the two are the same kind of thing — money
     * added to an order that is not the goods — and it is laid out the way an
     * operator thinks about it rather than the way the tables are shaped: a
     * zone is a heading with its prices underneath, not a row in one list
     * pointing at rows in another. A zone with no rates is a zone that charges
     * nothing to nowhere, so showing the two apart invites reading half the
     * configuration and believing it.
     *
     * The band is the part worth explaining in the interface rather than in a
     * comment, because it is the part that surprises people. A method is a name
     * and a *range of basket values*, so "Standard 49, free over 2000" is one
     * method the shopper recognises and two rows here. The engine refuses two
     * bands of one name that overlap — a shopper offered the same method at two
     * prices has no way to choose — so the form says what a band means before
     * somebody discovers it from an error.
     */
    import { can, shipping } from "$lib/api.js";
    import { formatMoney } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";

    let groups = $state([]);
    let loading = $state(true);
    let currency = $state("");

    let zoneOpen = $state(false);
    let zoneForm = $state({ id: null, name: "", countries: "", states: "" });

    let rateOpen = $state(false);
    let rateForm = $state({
        id: null,
        zone_id: null,
        name: "",
        price: "",
        min: "",
        max: "",
    });

    let saving = $state(false);
    let confirmOpen = $state(false);
    let pending = $state(null);

    /* Two rights now: seeing what delivery costs and setting it. The screen
       loads on the first and arms its buttons on the second, so a reader gets
       the zones rather than an empty page. */
    const readable = $derived(can("shipping.read"));
    const writable = $derived(can("shipping.write"));

    async function load() {
        loading = true;
        try {
            const res = await shipping.zones();
            groups = res ?? [];
            currency = groups[0]?.rates?.[0]?.price?.currency ?? currency;
        } catch (err) {
            toast.error(err.message);
        } finally {
            loading = false;
        }
    }

    $effect(() => {
        if (readable) load();
    });

    function openZone(zone) {
        zoneForm = zone
            ? {
                  id: zone.id,
                  name: zone.name,
                  countries: (zone.countries ?? []).join(", "),
                  states: (zone.states ?? []).join(", "),
              }
            : { id: null, name: "", countries: "", states: "" };
        zoneOpen = true;
    }

    function openRate(zoneID, rate) {
        rateForm = rate
            ? {
                  id: rate.id,
                  zone_id: rate.zone_id,
                  name: rate.name,
                  price: String(rate.price.amount_minor),
                  min: String(rate.min_subtotal_minor ?? 0),
                  max: rate.max_subtotal_minor == null ? "" : String(rate.max_subtotal_minor),
              }
            : { id: null, zone_id: zoneID, name: "", price: "", min: "0", max: "" };
        rateOpen = true;
    }

    function codes(raw) {
        return raw
            .split(/[\s,]+/)
            .map((c) => c.trim().toUpperCase())
            .filter(Boolean);
    }

    async function saveZone() {
        if (!zoneForm.name.trim()) return toast.error("A zone needs a name.");
        saving = true;
        try {
            const body = {
                name: zoneForm.name.trim(),
                countries: codes(zoneForm.countries),
                states: codes(zoneForm.states),
            };
            if (zoneForm.id) await shipping.updateZone(zoneForm.id, body);
            else await shipping.createZone(body);
            toast.success(zoneForm.id ? "Zone saved" : "Zone added");
            zoneOpen = false;
            await load();
        } catch (err) {
            toast.error(err.message);
        } finally {
            saving = false;
        }
    }

    async function saveRate() {
        const price = Number(rateForm.price);
        if (!rateForm.name.trim()) return toast.error("A method needs a name.");
        if (!Number.isInteger(price) || price < 0) {
            return toast.error("A price is a whole number of minor units, and cannot be negative.");
        }
        saving = true;
        try {
            const body = {
                zone_id: rateForm.zone_id,
                name: rateForm.name.trim(),
                price_minor: price,
                min_subtotal_minor: Number(rateForm.min || 0),
                max_subtotal_minor: rateForm.max === "" ? null : Number(rateForm.max),
            };
            if (rateForm.id) await shipping.updateRate(rateForm.id, body);
            else await shipping.createRate(body);
            toast.success(rateForm.id ? "Method saved" : "Method added");
            rateOpen = false;
            await load();
        } catch (err) {
            toast.error(err.message);
        } finally {
            saving = false;
        }
    }

    function askRemove(what, id, label) {
        pending = { what, id, label };
        confirmOpen = true;
    }

    async function remove() {
        if (!pending) return;
        try {
            if (pending.what === "zone") await shipping.removeZone(pending.id);
            else await shipping.removeRate(pending.id);
            toast.success("Removed");
            confirmOpen = false;
            pending = null;
            await load();
        } catch (err) {
            toast.error(err.message);
        }
    }

    /* What a band says, in the words an operator would use. */
    function band(rate) {
        const min = rate.min_subtotal_minor ?? 0;
        const max = rate.max_subtotal_minor;
        if (!min && max == null) return "Any basket";
        if (max == null) return `Baskets from ${formatMoney({ amount_minor: min, currency })}`;
        if (!min) return `Baskets under ${formatMoney({ amount_minor: max, currency })}`;
        return `${formatMoney({ amount_minor: min, currency })} to ${formatMoney({ amount_minor: max, currency })}`;
    }

    /* The chip under the name, Litekart's way: the first code and how many
       more, with the whole list on hover. */
    function placesShort(zone) {
        const c = zone.countries ?? [];
        const st = zone.states ?? [];
        if (!c.length && !st.length) return "Everywhere else";
        const codes = st.length ? st : c;
        const head = st.length ? `${c[0] ?? ""} ${st[0]}`.trim() : c[0];
        return codes.length > 1 ? `${head} +${codes.length - 1} more` : head;
    }

    /* A kebab's menu is a popover; acting on an item closes it, since the
       action opens a drawer or a confirm over the page. */
    function pick(menuID, action) {
        document.getElementById(menuID)?.hidePopover();
        action();
    }

    function places(zone) {
        const c = zone.countries ?? [];
        const s = zone.states ?? [];
        if (!c.length && !s.length) return "Anywhere this store has not named";
        if (s.length) return `${c.join(", ")} — ${s.join(", ")}`;
        return c.join(", ");
    }
</script>

<svelte:head><title>Shipping and delivery · GoCommerce</title></svelte:head>

{#if !can("shipping.read")}
    <NoAccess right="store.operate" what="shipping" />
{:else}
    <div class="page page-shipping shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                    <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Shipping and delivery</div>
                </nav>

                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        title="Refresh"
                        aria-label="Refresh"
                        onclick={load}
                    >
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>

            </header>

            {#if loading && !groups.length}
                <div class="block txt-center p-base"><span class="loader"></span></div>
            {:else}
                <!-- One card, as Litekart draws it: the zones as blocks, each
                     with its rates beneath and a kebab on every row. -->
                <section class="card zones-card">
                    <div class="zones-head">
                        <h2 class="zones-title">Shipping zones</h2>
                        {#if writable}
                            <button type="button" class="btn sm secondary" onclick={() => openZone(null)}>
                                <span class="txt">Add shipping zone</span>
                            </button>
                        {/if}
                    </div>

                    {#if !groups.length}
                        <!-- A store with no zones is correctly configured: it
                             charges one rate. Said plainly, not as an alert. -->
                        <div class="block txt-center txt-hint p-base">
                            <div class="m-b-10">
                                <i class="ri-truck-line" style="font-size: 32px" aria-hidden="true"></i>
                            </div>
                            <p class="tw:mx-auto tw:max-w-[62ch]">
                                <strong>No zones yet, so every order is charged the flat rate this store
                                was configured with.</strong>
                                Add a zone and the shopper starts choosing from the rates you price in
                                it — and this store stops accepting orders going anywhere no zone covers.
                            </p>
                        </div>
                    {/if}

                    {#each groups as g (g.zone.id)}
                        <div class="zone">
                            <div class="zone-row">
                                <span class="zone-mark" aria-hidden="true"><i class="ri-global-line"></i></span>
                                <div class="flex-fill zone-text">
                                    <div class="zone-name">{g.zone.name}</div>
                                    <span class="label zone-places" title={places(g.zone)}>{placesShort(g.zone)}</span>
                                </div>
                                {#if writable}
                                    <button
                                        type="button"
                                        class="btn circle sm transparent secondary"
                                        popovertarget="zone-menu-{g.zone.id}"
                                        aria-label="Actions for {g.zone.name}"
                                    >
                                        <i class="ri-more-2-fill" aria-hidden="true"></i>
                                    </button>
                                    <div id="zone-menu-{g.zone.id}" class="dropdown sm nowrap" popover="auto">
                                        <button type="button" class="dropdown-item" onclick={() => pick(`zone-menu-${g.zone.id}`, () => openZone(g.zone))}>
                                            <i class="ri-pencil-line" aria-hidden="true"></i>
                                            <span class="txt">Edit zone</span>
                                        </button>
                                        <button type="button" class="dropdown-item txt-danger" onclick={() => pick(`zone-menu-${g.zone.id}`, () => askRemove("zone", g.zone.id, g.zone.name))}>
                                            <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                            <span class="txt">Remove zone</span>
                                        </button>
                                    </div>
                                {/if}
                            </div>

                            {#each g.rates as r (r.id)}
                                <div class="rate-row">
                                    <div class="flex-fill zone-text">
                                        <div class="rate-name">{r.name}</div>
                                        <div class="txt-hint txt-sm">{band(r)}</div>
                                    </div>
                                    {#if !r.active}
                                        <span class="label">Off</span>
                                    {/if}
                                    {#if r.price.amount_minor === 0}
                                        <span class="label label-success">Free</span>
                                    {:else}
                                        <span class="label rate-price">{formatMoney(r.price)}</span>
                                    {/if}
                                    {#if writable}
                                        <button
                                            type="button"
                                            class="btn circle sm transparent secondary"
                                            popovertarget="rate-menu-{r.id}"
                                            aria-label="Actions for {r.name}"
                                        >
                                            <i class="ri-more-2-fill" aria-hidden="true"></i>
                                        </button>
                                        <div id="rate-menu-{r.id}" class="dropdown sm nowrap" popover="auto">
                                            <button type="button" class="dropdown-item" onclick={() => pick(`rate-menu-${r.id}`, () => openRate(g.zone.id, r))}>
                                                <i class="ri-pencil-line" aria-hidden="true"></i>
                                                <span class="txt">Edit rate</span>
                                            </button>
                                            <button type="button" class="dropdown-item txt-danger" onclick={() => pick(`rate-menu-${r.id}`, () => askRemove("rate", r.id, r.name))}>
                                                <i class="ri-delete-bin-line" aria-hidden="true"></i>
                                                <span class="txt">Remove rate</span>
                                            </button>
                                        </div>
                                    {/if}
                                </div>
                            {/each}

                            {#if !g.rates.length}
                                <!-- Red because it is a fault, not a state: an
                                     order going to this zone is refused. -->
                                <div class="zone-warning">At least one shipping rate is required</div>
                            {/if}
                            {#if writable}
                                <button type="button" class="rate-add" onclick={() => openRate(g.zone.id, null)}>
                                    <i class="ri-add-circle-line" aria-hidden="true"></i>
                                    <span class="txt">Add rate</span>
                                </button>
                            {/if}
                        </div>
                    {/each}
                </section>
            {/if}

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <span class="txt-hint txt-sm">
                    The most specific zone wins: a zone naming a state beats one naming only its
                    country, which beats the zone that names nowhere in particular.
                </span>
                <div class="flex-fill"></div>
            </footer>
        </div>
    </div>

    <Drawer
        open={zoneOpen}
        title={zoneForm.id ? "Edit zone" : "Add zone"}
        onclose={() => (zoneOpen = false)}
    >
        <form
            class="block"
            onsubmit={(e) => {
                e.preventDefault();
                saveZone();
            }}
        >
            <div class="field">
                <label for="zone-name">Name</label>
                <input id="zone-name" type="text" placeholder="India" bind:value={zoneForm.name} required />
            </div>
            <div class="field m-t-sm">
                <label for="zone-countries">Countries</label>
                <input id="zone-countries" type="text" placeholder="IN, LK" bind:value={zoneForm.countries} />
            </div>
            <div class="field-help">
                ISO codes, separated by commas. Leave empty for the zone that covers anywhere
                no other zone names.
            </div>
            <div class="field m-t-sm">
                <label for="zone-states">States</label>
                <input id="zone-states" type="text" placeholder="KA, MH" bind:value={zoneForm.states} />
            </div>
            <div class="field-help">
                Optional, and only useful with a country above. A zone naming a state beats one
                that names only the country.
            </div>
        </form>
        {#snippet footer()}
            <button type="button" class="btn transparent m-r-auto" onclick={() => (zoneOpen = false)}>
                <span class="txt">Cancel</span>
            </button>
            <button type="button" class="btn" disabled={saving} class:loading={saving} onclick={saveZone}>
                <span class="txt">{zoneForm.id ? "Save zone" : "Add zone"}</span>
            </button>
        {/snippet}
    </Drawer>

    <Drawer
        open={rateOpen}
        title={rateForm.id ? "Edit method" : "Add method"}
        onclose={() => (rateOpen = false)}
    >
        <form
            class="block"
            onsubmit={(e) => {
                e.preventDefault();
                saveRate();
            }}
        >
            <div class="field">
                <label for="rate-name">Name</label>
                <input id="rate-name" type="text" placeholder="Standard" bind:value={rateForm.name} required />
            </div>
            <div class="field-help">What the shopper sees at checkout.</div>
            <div class="field m-t-sm">
                <label for="rate-price">Price, in minor units</label>
                <input id="rate-price" type="number" min="0" step="1" placeholder="4900" bind:value={rateForm.price} required />
            </div>
            <div class="field-help">
                Whole minor units, never a decimal — 4900 is 49.00. Zero is free delivery.
            </div>
            <div class="fields m-t-sm">
                <div class="field">
                    <label for="rate-min">Baskets from</label>
                    <input id="rate-min" type="number" min="0" step="1" placeholder="0" bind:value={rateForm.min} />
                </div>
                <div class="field">
                    <label for="rate-max">Up to, but not including</label>
                    <input id="rate-max" type="number" min="0" step="1" placeholder="no ceiling" bind:value={rateForm.max} />
                </div>
            </div>
            <div class="field-help">
                A method is a name and a range of basket values, which is how one method covers
                “Standard 49, free over 2000”: add it twice, once from 0 up to 200000, once from
                200000 with no ceiling. Two bands of the same name that overlap are refused,
                because a shopper offered one method at two prices cannot choose between them.
            </div>
        </form>
        {#snippet footer()}
            <button type="button" class="btn transparent m-r-auto" onclick={() => (rateOpen = false)}>
                <span class="txt">Cancel</span>
            </button>
            <button type="button" class="btn" disabled={saving} class:loading={saving} onclick={saveRate}>
                <span class="txt">{rateForm.id ? "Save method" : "Add method"}</span>
            </button>
        {/snippet}
    </Drawer>

    <Confirm
        bind:open={confirmOpen}
        title={pending?.what === "zone" ? "Remove this zone?" : "Remove this method?"}
        message={pending?.what === "zone"
            ? `“${pending?.label}” and every method priced in it. Orders already placed keep the method they were sold.`
            : `“${pending?.label}” stops being offered. Orders already placed keep it.`}
        confirmLabel="Remove"
        danger
        onconfirm={remove}
    />
{/if}

<style>
    .zones-card {
        padding: 0;
        margin: 0;
    }
    .zones-head {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 10px;
        padding: 14px 16px;
    }
    .zones-title {
        margin: 0;
        font-size: 15px;
        font-weight: 600;
    }
    .zone {
        margin: 0 16px 14px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--baseRadius);
        overflow: hidden;
    }
    .zone-row,
    .rate-row {
        display: flex;
        align-items: center;
        gap: 12px;
        padding: 12px 14px;
    }
    .rate-row {
        border-top: 1px solid var(--surfaceAlt2Color);
    }
    .zone-text {
        min-width: 0;
    }
    .zone-mark {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 30px;
        height: 30px;
        flex: 0 0 auto;
        border-radius: 8px;
        background: var(--successColor);
        color: #fff;
        font-size: 15px;
    }
    .zone-name,
    .rate-name {
        font-weight: 600;
    }
    .rate-name {
        font-weight: 500;
    }
    .zone-places {
        margin-top: 3px;
    }
    .rate-price {
        font-variant-numeric: tabular-nums;
    }
    .zone-warning {
        padding: 8px 14px;
        border-top: 1px solid var(--surfaceAlt2Color);
        background: color-mix(in srgb, var(--dangerColor), transparent 90%);
        color: var(--dangerColor);
        font-size: var(--smFontSize);
        text-align: center;
    }
    .rate-add {
        display: flex;
        width: 100%;
        align-items: center;
        justify-content: center;
        gap: 6px;
        padding: 10px;
        border: 0;
        border-top: 1px solid var(--surfaceAlt2Color);
        border-radius: 0;
        background: none;
        color: var(--txtPrimaryColor);
        font: inherit;
        font-weight: 500;
        cursor: pointer;
    }
    .rate-add:hover,
    .rate-add:focus-visible {
        background: var(--surfaceAlt1Color);
    }
</style>
