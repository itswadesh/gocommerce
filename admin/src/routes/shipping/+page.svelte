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
    import ThemeToggle from "$lib/components/ThemeToggle.svelte";

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

    const writable = $derived(can("store.operate"));

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
        if (writable) load();
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

    function places(zone) {
        const c = zone.countries ?? [];
        const s = zone.states ?? [];
        if (!c.length && !s.length) return "Anywhere this store has not named";
        if (s.length) return `${c.join(", ")} — ${s.join(", ")}`;
        return c.join(", ");
    }
</script>

<svelte:head><title>Shipping · GoCommerce</title></svelte:head>

{#if !can("store.operate")}
    <NoAccess right="store.operate" what="shipping" />
{:else}
    <div class="page page-shipping shopify-skin">
        <div class="page-content full-height">
            <header class="page-header">
                <nav class="breadcrumbs"><div class="breadcrumb-item">Shipping</div></nav>

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

                <div class="page-header-primary-btns">
                    <button type="button" class="btn" onclick={() => openZone(null)}>
                        <i class="ri-add-line" aria-hidden="true"></i>
                        <span class="txt">Add zone</span>
                    </button>
                </div>
            </header>

            {#if loading && !groups.length}
                <div class="block txt-center p-base"><span class="loader"></span></div>
            {:else if !groups.length}
                <div class="alert m-b-base">
                    <p>
                        <strong>No zones yet, so every order is charged the flat rate this store
                        was configured with.</strong>
                        Add a zone and the shopper starts choosing from the methods you price in
                        it — and this store stops accepting orders going anywhere no zone covers.
                    </p>
                </div>
            {:else}
                {#each groups as g (g.zone.id)}
                    <div class="wrapper m-b-base">
                        <div class="section-title">
                            {g.zone.name}
                            <small class="txt-hint m-l-5">{places(g.zone)}</small>
                            <div class="flex-fill"></div>
                            {#if writable}
                                <button
                                    type="button"
                                    class="btn sm secondary"
                                    onclick={() => openRate(g.zone.id, null)}
                                >
                                    Add method
                                </button>
                                <button
                                    type="button"
                                    class="btn sm secondary"
                                    onclick={() => openZone(g.zone)}
                                >
                                    Edit zone
                                </button>
                                <button
                                    type="button"
                                    class="btn sm secondary txt-danger"
                                    onclick={() => askRemove("zone", g.zone.id, g.zone.name)}
                                >
                                    Remove
                                </button>
                            {/if}
                        </div>

                        {#if !g.rates.length}
                            <div class="block txt-center p-base txt-hint">
                                No methods here yet, so this zone offers nothing and an order going
                                to it is refused.
                            </div>
                        {:else}
                            <div class="table-scroll">
                                <table class="table">
                                    <thead>
                                        <tr>
                                            <th>Method</th>
                                            <th>Applies to</th>
                                            <th>Price</th>
                                            <th>State</th>
                                            <th class="min-width"></th>
                                        </tr>
                                    </thead>
                                    <tbody>
                                        {#each g.rates as r (r.id)}
                                            <tr>
                                                <td>{r.name}</td>
                                                <td class="txt-hint">{band(r)}</td>
                                                <td>
                                                    {#if r.price.amount_minor === 0}
                                                        <span class="label success">Free</span>
                                                    {:else}
                                                        {formatMoney(r.price)}
                                                    {/if}
                                                </td>
                                                <td>
                                                    <span class="label {r.active ? 'success' : ''}">
                                                        {r.active ? "active" : "off"}
                                                    </span>
                                                </td>
                                                <td class="min-width txt-right">
                                                    {#if writable}
                                                        <button
                                                            type="button"
                                                            class="btn sm secondary"
                                                            onclick={() => openRate(g.zone.id, r)}
                                                        >
                                                            Edit
                                                        </button>
                                                        <button
                                                            type="button"
                                                            class="btn sm secondary txt-danger"
                                                            onclick={() => askRemove("rate", r.id, r.name)}
                                                        >
                                                            Remove
                                                        </button>
                                                    {/if}
                                                </td>
                                            </tr>
                                        {/each}
                                    </tbody>
                                </table>
                            </div>
                        {/if}
                    </div>
                {/each}
            {/if}

            <footer class="page-footer">
                <span class="txt-hint txt-sm">
                    The most specific zone wins: a zone naming a state beats one naming only its
                    country, which beats the zone that names nowhere in particular.
                </span>
                <div class="flex-fill"></div>
                <ThemeToggle />
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
            <div class="field">
                <label for="zone-countries">Countries</label>
                <input id="zone-countries" type="text" placeholder="IN, LK" bind:value={zoneForm.countries} />
                <div class="txt-hint txt-sm m-t-5">
                    ISO codes, separated by commas. Leave empty for the zone that covers anywhere
                    no other zone names.
                </div>
            </div>
            <div class="field">
                <label for="zone-states">States</label>
                <input id="zone-states" type="text" placeholder="KA, MH" bind:value={zoneForm.states} />
                <div class="txt-hint txt-sm m-t-5">
                    Optional, and only useful with a country above. A zone naming a state beats one
                    that names only the country.
                </div>
            </div>
            <button type="submit" class="btn" disabled={saving} class:loading={saving}>
                {zoneForm.id ? "Save zone" : "Add zone"}
            </button>
        </form>
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
                <div class="txt-hint txt-sm m-t-5">What the shopper sees at checkout.</div>
            </div>
            <div class="field">
                <label for="rate-price">Price, in minor units</label>
                <input id="rate-price" type="number" min="0" step="1" placeholder="4900" bind:value={rateForm.price} required />
                <div class="txt-hint txt-sm m-t-5">
                    Whole minor units, never a decimal — 4900 is 49.00. Zero is free delivery.
                </div>
            </div>
            <div class="fields">
                <div class="field">
                    <label for="rate-min">Baskets from</label>
                    <input id="rate-min" type="number" min="0" step="1" placeholder="0" bind:value={rateForm.min} />
                </div>
                <div class="field">
                    <label for="rate-max">Up to, but not including</label>
                    <input id="rate-max" type="number" min="0" step="1" placeholder="no ceiling" bind:value={rateForm.max} />
                </div>
            </div>
            <div class="txt-hint txt-sm m-b-base">
                A method is a name and a range of basket values, which is how one method covers
                “Standard 49, free over 2000”: add it twice, once from 0 up to 200000, once from
                200000 with no ceiling. Two bands of the same name that overlap are refused,
                because a shopper offered one method at two prices cannot choose between them.
            </div>
            <button type="submit" class="btn" disabled={saving} class:loading={saving}>
                {rateForm.id ? "Save method" : "Add method"}
            </button>
        </form>
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
