<script>
    /**
     * Shipping Settings: where this store delivers, and what it charges to.
     *
     * Laid out as KitCommerce's screen is, at the owner's request: one card of
     * zones, each with its rates beneath and a menu on every row; a zone that
     * is switched off is covered by DISABLED and a switch to bring it back; a
     * rate shows its price and its band as chips ("Free", "above $100.00").
     * The two forms ask what KitCommerce's do, in its order — a rate's name,
     * whether it is on, its rank, its price and handling fee, the delivery
     * estimate, and a band on the order's price or the parcel's weight, with a
     * checkout preview — and money is typed in the store's currency, not in
     * minor units.
     *
     * What GoCommerce has beyond KitCommerce stays: states within a country
     * (the most specific zone wins), a zone that covers everywhere else, and a
     * customer group's own rates (D76), which replace everybody's for its
     * members wherever it has one.
     */
    import { api, can, shipping } from "$lib/api.js";
    import { formatMoney, fromMinor, toMinor } from "$lib/format.js";
    import { settings } from "$lib/settings.svelte.js";
    import { COUNTRIES } from "$lib/countries.js";
    import { toast } from "$lib/toast.svelte.js";
    import Confirm from "$lib/components/Confirm.svelte";
    import Drawer from "$lib/components/Drawer.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";
    import { titled } from "$lib/brand.svelte.js";

    let groups = $state([]);
    let loading = $state(true);
    const currency = $derived(settings.currency);

    /* The customer groups a rate can be offered to. Reading them is
       groups.read, which a shipping operator need not hold: without it the
       picker says why it is empty instead of looking broken. */
    let customerGroups = $state([]);
    let groupsReadable = $state(true);

    const readable = $derived(can("shipping.read"));
    const writable = $derived(can("shipping.write"));

    let zoneOpen = $state(false);
    let zoneForm = $state(blankZone());
    let countryQuery = $state("");

    let rateOpen = $state(false);
    let rateForm = $state(blankRate(0));

    let saving = $state(false);
    let confirmOpen = $state(false);
    let pending = $state(null);

    function blankZone() {
        return { id: null, name: "", description: "", active: true, countries: [], states: "" };
    }

    function blankRate(zoneID) {
        return {
            id: null,
            zone_id: zoneID,
            name: "",
            description: "",
            active: true,
            position: 0,
            price: "0",
            handling: "0",
            min_days: "",
            max_days: "",
            basis: "price",
            min_price: "0",
            max_price: "",
            min_weight: "0",
            max_weight: "",
            group_id: 0,
            group_name: "",
        };
    }

    async function load() {
        loading = true;
        try {
            groups = (await shipping.zones()) ?? [];
        } catch (err) {
            toast.error(err.message);
        } finally {
            loading = false;
        }
    }

    async function loadCustomerGroups() {
        if (!can("groups.read")) {
            groupsReadable = false;
            return;
        }
        try {
            const res = await api.get("/api/admin/customer-groups");
            customerGroups = Array.isArray(res) ? res : (res?.data ?? []);
            groupsReadable = true;
        } catch {
            groupsReadable = false;
        }
    }

    $effect(() => {
        if (readable) load();
    });
    $effect(() => {
        if (writable) loadCustomerGroups();
    });

    const audienceOptions = $derived.by(() => {
        const opts = [
            { value: 0, label: "Everyone", note: "Shoppers in no group, and any cart not signed in" },
            ...customerGroups.map((g) => ({ value: g.id, label: g.name, count: g.members })),
        ];
        if (rateForm.group_id && !opts.some((o) => o.value === rateForm.group_id)) {
            opts.push({ value: rateForm.group_id, label: rateForm.group_name || `Group #${rateForm.group_id}` });
        }
        return opts;
    });
    const chosenGroup = $derived(audienceOptions.find((o) => o.value === rateForm.group_id && o.value !== 0));

    /* ---- Zones ---- */

    const countryName = (code) => COUNTRIES.find((c) => c.value === code)?.label ?? code;
    const filteredCountries = $derived.by(() => {
        const q = countryQuery.trim().toLowerCase();
        if (!q) return COUNTRIES;
        return COUNTRIES.filter((c) => c.label.toLowerCase().includes(q) || c.value.toLowerCase() === q);
    });

    function openZone(zone) {
        countryQuery = "";
        zoneForm = zone
            ? {
                  id: zone.id,
                  name: zone.name,
                  description: zone.description ?? "",
                  active: zone.active,
                  countries: [...(zone.countries ?? [])],
                  states: (zone.states ?? []).join(", "),
              }
            : blankZone();
        zoneOpen = true;
    }

    function toggleCountry(code, on) {
        zoneForm.countries = on ? [...zoneForm.countries, code] : zoneForm.countries.filter((c) => c !== code);
    }

    /* Commas separate states, because a state can be a name of more than one
       word ("New York") now that the engine reads names as codes (D72). */
    function stateCodes(raw) {
        const listed = raw.includes(",");
        const pieces = listed ? raw.split(",") : raw.trim().split(/\s+/);
        const allShort = pieces.every((p) => p.trim().length <= 3);
        return (listed || allShort ? pieces : [raw]).map((c) => c.trim().toUpperCase()).filter(Boolean);
    }

    function zoneBody(z, active) {
        return {
            name: z.name.trim(),
            description: (z.description ?? "").trim(),
            countries: z.countries,
            states: Array.isArray(z.states) ? z.states : stateCodes(z.states),
            active,
        };
    }

    async function saveZone() {
        if (!zoneForm.name.trim()) return toast.error("Enter the zone's name, such as Domestic.");
        saving = true;
        try {
            const body = zoneBody(zoneForm, zoneForm.active);
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

    /* The switch on a DISABLED zone brings it straight back. */
    async function activate(zone) {
        saving = true;
        try {
            await shipping.updateZone(zone.id, zoneBody(zone, true));
            toast.success("Zone activated");
            await load();
        } catch (err) {
            toast.error(err.message);
        } finally {
            saving = false;
        }
    }

    /* ---- Rates ---- */

    function openRate(zoneID, rate) {
        if (!rate) {
            rateForm = blankRate(zoneID);
        } else {
            const days = (d) => (d == null ? "" : String(d));
            rateForm = {
                id: rate.id,
                zone_id: rate.zone_id,
                name: rate.name,
                description: rate.description ?? "",
                active: rate.active,
                position: rate.position ?? 0,
                price: fromMinor(rate.price.amount_minor, currency),
                handling: fromMinor(rate.handling_fee_minor ?? 0, currency),
                min_days: days(rate.min_days),
                max_days: days(rate.max_days),
                basis: rate.basis || "price",
                min_price: fromMinor(rate.min_subtotal_minor ?? 0, currency),
                max_price: rate.max_subtotal_minor == null ? "" : fromMinor(rate.max_subtotal_minor, currency),
                min_weight: String(rate.min_weight_grams ?? 0),
                max_weight: rate.max_weight_grams == null ? "" : String(rate.max_weight_grams),
                group_id: rate.group_id ?? 0,
                group_name: rate.group_name ?? "",
            };
        }
        rateOpen = true;
    }

    /* Money typed by a person: blank is zero where zero means something, and
       a value that will not parse is refused here rather than sent as null. */
    function money(value, label, { optional = false } = {}) {
        if (optional && String(value).trim() === "") return { ok: true, minor: null };
        const minor = toMinor(String(value).trim() === "" ? "0" : value, currency);
        if (minor === null || minor < 0) return { ok: false, error: `Enter ${label} as an amount, such as 7.00.` };
        return { ok: true, minor };
    }

    function wholeOrNull(value, label) {
        if (String(value).trim() === "") return { ok: true, n: null };
        const n = Number(value);
        if (!Number.isInteger(n) || n < 0) return { ok: false, error: `Enter ${label} as a whole number.` };
        return { ok: true, n };
    }

    async function saveRate() {
        if (!rateForm.name.trim()) return toast.error("Enter the rate's name, such as Standard, which shoppers see at checkout.");
        const price = money(rateForm.price, "the base rate");
        const handling = money(rateForm.handling, "the handling fee");
        const minPrice = money(rateForm.min_price, "the minimum price");
        const maxPrice = money(rateForm.max_price, "the maximum price", { optional: true });
        const minDays = wholeOrNull(rateForm.min_days, "the minimum days");
        const maxDays = wholeOrNull(rateForm.max_days, "the maximum days");
        const minWeight = wholeOrNull(rateForm.min_weight, "the minimum weight in grams");
        const maxWeight = wholeOrNull(rateForm.max_weight, "the maximum weight in grams");
        const bad = [price, handling, minPrice, maxPrice, minDays, maxDays, minWeight, maxWeight].find((x) => !x.ok);
        if (bad) return toast.error(bad.error);
        const position = Number(rateForm.position || 0);
        saving = true;
        try {
            const body = {
                zone_id: rateForm.zone_id,
                name: rateForm.name.trim(),
                description: rateForm.description.trim(),
                active: rateForm.active,
                position: Number.isInteger(position) ? position : 0,
                price_minor: price.minor,
                handling_fee_minor: handling.minor,
                min_days: minDays.n,
                max_days: maxDays.n,
                basis: rateForm.basis,
                min_subtotal_minor: rateForm.basis === "price" ? minPrice.minor : 0,
                max_subtotal_minor: rateForm.basis === "price" ? maxPrice.minor : null,
                min_weight_grams: rateForm.basis === "weight" ? (minWeight.n ?? 0) : 0,
                max_weight_grams: rateForm.basis === "weight" ? maxWeight.n : null,
                group_id: rateForm.group_id || null,
            };
            if (rateForm.id) await shipping.updateRate(rateForm.id, body);
            else await shipping.createRate(body);
            toast.success(rateForm.id ? "Shipping rate updated" : "Shipping rate added");
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
            toast.success(pending.what === "zone" ? "Zone deleted" : "Shipping rate deleted");
            confirmOpen = false;
            pending = null;
            await load();
        } catch (err) {
            toast.error(err.message);
        }
    }

    /* ---- What the rows show ---- */

    const price = (minor) => formatMoney({ amount_minor: minor, currency });

    /* The band as KitCommerce's chips say it. "above" is from, "below" is up
       to but not including: two bands meeting at a number never both claim it. */
    function bandChips(r) {
        const out = [];
        if ((r.basis || "price") === "weight") {
            if (r.min_weight_grams) out.push(`above ${r.min_weight_grams} g`);
            if (r.max_weight_grams != null) out.push(`below ${r.max_weight_grams} g`);
        } else {
            if (r.min_subtotal_minor) out.push(`above ${price(r.min_subtotal_minor)}`);
            if (r.max_subtotal_minor != null) out.push(`below ${price(r.max_subtotal_minor)}`);
        }
        return out;
    }

    function daysText(r) {
        if (r.min_days == null && r.max_days == null) return "";
        if (r.min_days != null && r.max_days != null) {
            return r.min_days === r.max_days ? `${r.min_days} days` : `${r.min_days}–${r.max_days} days`;
        }
        return r.max_days != null ? `Up to ${r.max_days} days` : `${r.min_days}+ days`;
    }

    /* The chip under a zone's name: the first code and how many more. */
    function placesShort(zone) {
        const c = zone.countries ?? [];
        const st = zone.states ?? [];
        if (!c.length && !st.length) return "Everywhere else";
        const codes = st.length ? st : c;
        const head = st.length ? `${c[0] ?? ""} ${st[0]}`.trim() : c[0];
        return codes.length > 1 ? `${head} +${codes.length - 1} more` : head;
    }

    function places(zone) {
        const c = zone.countries ?? [];
        const s = zone.states ?? [];
        if (!c.length && !s.length) return "Anywhere this store has not named";
        if (s.length) return `${c.map(countryName).join(", ")} — ${s.join(", ")}`;
        return c.map(countryName).join(", ");
    }

    function pick(menuID, action) {
        document.getElementById(menuID)?.hidePopover();
        action();
    }

    /* What the shopper is charged for the rate being edited: its price and
       its handling fee, as checkout adds them. */
    const preview = $derived.by(() => {
        const p = toMinor(String(rateForm.price).trim() === "" ? "0" : rateForm.price, currency) ?? 0;
        const h = toMinor(String(rateForm.handling).trim() === "" ? "0" : rateForm.handling, currency) ?? 0;
        return p + h;
    });
</script>

<svelte:head><title>{titled("Shipping Settings")}</title></svelte:head>

{#if !readable}
    <NoAccess right="shipping.read" what="shipping" />
{:else}
    <div class="page page-shipping shopify-skin">
        <div class="page-content tw:bg-background tw:text-foreground">
            <header class="page-header">
                <nav class="breadcrumbs">
                    <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                    <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Shipping Settings</h1>
                </nav>
                <div class="inline-flex gap-sm">
                    <button
                        type="button"
                        class="btn circle transparent secondary"
                        class:loading={loading || saving}
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
                <section class="card zones-card">
                    <div class="zones-head">
                        <h2 class="zones-title">Shipping zones</h2>
                        {#if writable && groups.length}
                            <button type="button" class="btn sm secondary" onclick={() => openZone(null)}>
                                <span class="txt">Add shipping zone</span>
                            </button>
                        {/if}
                    </div>

                    {#if !groups.length}
                        <EmptyState
                            icon="ri-global-line"
                            title="No shipping zones"
                            text="Get started by creating your first shipping zone."
                        >
                            {#if writable}
                                <button type="button" class="btn" onclick={() => openZone(null)}>
                                    <span class="txt">Add shipping zone</span>
                                </button>
                            {/if}
                        </EmptyState>
                    {/if}

                    {#each groups as g (g.zone.id)}
                        <div class="zone" class:off={!g.zone.active}>
                            {#if !g.zone.active}
                                <!-- Switched off, not lost: nothing here is quoted
                                     until the switch brings it back. -->
                                <div class="zone-disabled">
                                    <p class="zone-disabled-word">DISABLED</p>
                                    {#if writable}
                                        <div class="field zone-disabled-toggle">
                                            <input
                                                type="checkbox"
                                                class="switch"
                                                id="zone-on-{g.zone.id}"
                                                checked={false}
                                                disabled={saving}
                                                onchange={() => activate(g.zone)}
                                            />
                                            <label for="zone-on-{g.zone.id}">Turn {g.zone.name} back on</label>
                                        </div>
                                    {/if}
                                </div>
                            {/if}
                            <div class="zone-row">
                                <span class="zone-mark" class:on={g.zone.active} aria-hidden="true"><i class="ri-global-line"></i></span>
                                <div class="flex-fill zone-text">
                                    <div class="zone-name">{g.zone.name}</div>
                                    <div class="zone-sub txt-hint txt-sm">
                                        {#if g.zone.description}
                                            <span>{g.zone.description}</span>
                                            <span aria-hidden="true">•</span>
                                        {/if}
                                        <span class="label zone-places" title={places(g.zone)}>{placesShort(g.zone)}</span>
                                    </div>
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
                                            <span class="txt">Edit</span>
                                        </button>
                                        <button type="button" class="dropdown-item txt-danger" onclick={() => pick(`zone-menu-${g.zone.id}`, () => askRemove("zone", g.zone.id, g.zone.name))}>
                                            <span class="txt">Delete</span>
                                        </button>
                                    </div>
                                {/if}
                            </div>

                            {#each g.rates as r (r.id)}
                                <div class="rate-row">
                                    <div class="zone-text rate-text">
                                        <div class="rate-name">{r.name}</div>
                                        {#if r.description || daysText(r)}
                                            <div class="txt-hint txt-sm">
                                                {[r.description, daysText(r)].filter(Boolean).join(" · ")}
                                            </div>
                                        {/if}
                                    </div>
                                    <div class="rate-chips">
                                        {#if r.group_id}
                                            <span class="label info rate-group" title="Offered only to {r.group_name || 'this group'}, in place of everybody's rates">
                                                <i class="ri-group-line" aria-hidden="true"></i>
                                                <span class="txt">{r.group_name || "A group"} only</span>
                                            </span>
                                        {/if}
                                        {#if !r.active}
                                            <span class="chip chip-off">DISABLED</span>
                                        {/if}
                                        {#if r.price.amount_minor === 0}
                                            <span class="chip chip-free">Free</span>
                                        {:else}
                                            <span class="chip chip-price">{formatMoney(r.price)}</span>
                                        {/if}
                                        {#if r.handling_fee_minor}
                                            <span class="chip" title="Handling, added to the price">+ {price(r.handling_fee_minor)} handling</span>
                                        {/if}
                                        {#each bandChips(r) as chip (chip)}
                                            <span class="chip">{chip}</span>
                                        {/each}
                                    </div>
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
                                                <span class="txt">Edit</span>
                                            </button>
                                            <button type="button" class="dropdown-item txt-danger" onclick={() => pick(`rate-menu-${r.id}`, () => askRemove("rate", r.id, r.name))}>
                                                <span class="txt">Delete</span>
                                            </button>
                                        </div>
                                    {/if}
                                </div>
                            {/each}

                            {#if !g.rates.length}
                                <div class="zone-warning">At least one shipping rate is required</div>
                            {:else if g.rates.every((r) => r.group_id)}
                                <div class="zone-note">
                                    Only customer groups have rates here. Everybody else is offered the
                                    next zone that covers them.
                                </div>
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
                    country, which beats the zone that names nowhere in particular. A customer
                    group's rates replace everybody's for its members, in the most specific zone
                    where the group has one.
                </span>
                <div class="flex-fill"></div>
            </footer>
        </div>
    </div>

    <Drawer open={zoneOpen} title={zoneForm.id ? "Edit Shipping Zone" : "Add Shipping Zone"} onclose={() => (zoneOpen = false)}>
        <form id="zone-form" class="block" onsubmit={(e) => (e.preventDefault(), saveZone())}>
            <div class="field">
                <label for="zone-name">Zone Name</label>
                <input id="zone-name" type="text" placeholder="Domestic" bind:value={zoneForm.name} required />
            </div>
            <div class="field m-t-sm">
                <input type="checkbox" class="switch" id="zone-active" bind:checked={zoneForm.active} />
                <label for="zone-active">Active</label>
            </div>
            <div class="field m-t-sm">
                <label for="zone-description">Description</label>
                <input id="zone-description" type="text" placeholder="Optional" bind:value={zoneForm.description} />
            </div>

            <div class="field m-t-sm">
                <label for="zone-country-search">Countries</label>
                <input
                    id="zone-country-search"
                    type="search"
                    placeholder="Search countries ..."
                    autocomplete="off"
                    bind:value={countryQuery}
                />
            </div>
            <p class="zone-selected txt-sm">
                {#if zoneForm.countries.length}
                    <strong>Selected:</strong>
                    {zoneForm.countries.map(countryName).join(", ")}
                {:else}
                    No countries selected yet
                {/if}
            </p>
            <div class="field-help">
                Select the countries that belong to this shipping zone. None at all is the zone that
                covers everywhere no other zone names.
            </div>
            <div class="country-list" role="group" aria-label="Countries in this zone">
                {#each filteredCountries as c (c.value)}
                    <label class="country-item">
                        <input
                            type="checkbox"
                            checked={zoneForm.countries.includes(c.value)}
                            onchange={(e) => toggleCountry(c.value, e.currentTarget.checked)}
                        />
                        <span>{c.label}</span>
                    </label>
                {:else}
                    <span class="txt-hint txt-sm">No countries found</span>
                {/each}
            </div>

            <div class="field m-t-sm">
                <label for="zone-states">States</label>
                <input id="zone-states" type="text" placeholder="KA, MH" bind:value={zoneForm.states} />
            </div>
            <div class="field-help">
                Optional, and only useful with one country. Separate states with commas; a zone
                naming a state beats one that names only the country.
            </div>
        </form>
        {#snippet footer()}
            <button type="button" class="btn transparent m-r-auto" onclick={() => (zoneOpen = false)}>
                <span class="txt">Cancel</span>
            </button>
            <button type="submit" form="zone-form" class="btn" disabled={saving || !zoneForm.name.trim()} class:loading={saving}>
                <span class="txt">Save</span>
            </button>
        {/snippet}
    </Drawer>

    <Drawer open={rateOpen} title={rateForm.id ? "Edit Rate" : "Add Rate"} onclose={() => (rateOpen = false)}>
        <form id="rate-form" class="block" onsubmit={(e) => (e.preventDefault(), saveRate())}>
            <div class="field">
                <label for="rate-name">Custom rate name</label>
                <input id="rate-name" type="text" placeholder="Standard" bind:value={rateForm.name} required />
            </div>
            <div class="field m-t-sm">
                <input type="checkbox" class="switch" id="rate-active" bind:checked={rateForm.active} />
                <label for="rate-active">Active</label>
            </div>
            <div class="field m-t-sm">
                <label for="rate-description">Description</label>
                <input id="rate-description" type="text" placeholder="Optional, shown to the shopper" bind:value={rateForm.description} />
            </div>
            <div class="field m-t-sm">
                <label for="rate-rank">Rank</label>
                <input id="rate-rank" type="number" min="0" step="1" bind:value={rateForm.position} />
            </div>
            <div class="field-help">Lower ranks are listed first at checkout.</div>

            <div class="fields m-t-sm">
                <div class="field">
                    <label for="rate-price">Base Rate ({currency})</label>
                    <input id="rate-price" type="text" inputmode="decimal" placeholder="0.00" bind:value={rateForm.price} />
                    {#if (toMinor(String(rateForm.price).trim() || "0", currency) ?? -1) === 0}<span class="field-suffix">Free</span>{/if}
                </div>
                <div class="field">
                    <label for="rate-handling">Handling Fees ({currency})</label>
                    <input id="rate-handling" type="text" inputmode="decimal" placeholder="0.00" bind:value={rateForm.handling} />
                </div>
            </div>

            <div class="fields m-t-sm">
                <div class="field">
                    <label for="rate-min-days">Minimum estimated days</label>
                    <input id="rate-min-days" type="number" min="0" step="1" placeholder="No limit" bind:value={rateForm.min_days} />
                </div>
                <div class="field">
                    <label for="rate-max-days">Maximum estimated days</label>
                    <input id="rate-max-days" type="number" min="0" step="1" placeholder="No limit" bind:value={rateForm.max_days} />
                </div>
            </div>

            <fieldset class="basis m-t-sm">
                <legend class="basis-legend">Conditional pricing</legend>
                <label class="basis-option">
                    <input type="radio" name="rate-basis" value="weight" bind:group={rateForm.basis} />
                    <span>Based on item weight</span>
                </label>
                <label class="basis-option">
                    <input type="radio" name="rate-basis" value="price" bind:group={rateForm.basis} />
                    <span>Based on order price</span>
                </label>
            </fieldset>

            {#if rateForm.basis === "weight"}
                <div class="fields m-t-sm">
                    <div class="field">
                        <label for="rate-min-weight">Minimum weight (g)</label>
                        <input id="rate-min-weight" type="number" min="0" step="1" bind:value={rateForm.min_weight} />
                    </div>
                    <div class="field">
                        <label for="rate-max-weight">Maximum weight (g)</label>
                        <input id="rate-max-weight" type="number" min="0" step="1" placeholder="No limit" bind:value={rateForm.max_weight} />
                    </div>
                </div>
                <div class="field-help">
                    The parcel's weight: each product's weight times its quantity. Up to, but not
                    including, the maximum.
                </div>
            {:else}
                <div class="fields m-t-sm">
                    <div class="field">
                        <label for="rate-min-price">Minimum price ({currency})</label>
                        <input id="rate-min-price" type="text" inputmode="decimal" bind:value={rateForm.min_price} />
                    </div>
                    <div class="field">
                        <label for="rate-max-price">Maximum price ({currency})</label>
                        <input id="rate-max-price" type="text" inputmode="decimal" placeholder="No limit" bind:value={rateForm.max_price} />
                    </div>
                </div>
                <div class="field-help">
                    The basket's value, up to but not including the maximum, so "below 100" and
                    "above 100" never both apply.
                </div>
            {/if}

            <div class="field m-t-sm">
                <label for="rate-audience">Who sees this rate</label>
                <Select id="rate-audience" value={rateForm.group_id} onchange={(v) => (rateForm.group_id = v || 0)} options={audienceOptions} />
            </div>
            <div class="field-help">
                {#if chosenGroup}
                    Only {chosenGroup.label}: its members are offered the group's rates instead of
                    everybody's wherever the group has one for their basket.
                {:else}
                    Every shopper, except members of a group with its own rate here.
                {/if}
                {#if !groupsReadable}
                    Choosing a customer group needs the groups.read right.
                {/if}
            </div>

            <h3 class="preview-title m-t-sm">Checkout preview</h3>
            <div class="preview">
                <span class="preview-check" aria-hidden="true"><i class="ri-check-line"></i></span>
                <span class="preview-name">{rateForm.name || "Rate name"}</span>
                <span class="preview-price">{preview === 0 ? "FREE" : price(preview)}</span>
            </div>
        </form>
        {#snippet footer()}
            <button type="button" class="btn transparent m-r-auto" onclick={() => (rateOpen = false)}>
                <span class="txt">Cancel</span>
            </button>
            <button type="submit" form="rate-form" class="btn" disabled={saving || !rateForm.name.trim()} class:loading={saving}>
                <span class="txt">Save</span>
            </button>
        {/snippet}
    </Drawer>

    <Confirm
        bind:open={confirmOpen}
        title={pending?.what === "zone" ? "Delete this shipping zone?" : "Delete this shipping rate?"}
        message={pending?.what === "zone"
            ? `“${pending?.label}” and every rate in it. This cannot be undone; orders already placed keep the rate they were sold.`
            : `“${pending?.label}” stops being offered. Orders already placed keep it.`}
        confirmLabel="Delete"
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
        padding: 10px 16px;
    }
    .zones-title {
        margin: 0;
        font-size: 15px;
        font-weight: 600;
    }
    .zone {
        position: relative;
        margin: 0 16px 14px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        overflow: hidden;
    }
    .zone-row,
    .rate-row {
        display: flex;
        align-items: center;
        gap: 12px;
        padding: 12px 14px;
        transition: background-color 150ms ease-out;
    }
    .zone-row:hover,
    .rate-row:hover {
        background: var(--surfaceAlt1Color);
    }
    .rate-row {
        border-top: 1px solid var(--surfaceAlt2Color);
        animation: fadeIn var(--animationSpeed);
    }
    .zone-text {
        min-width: 0;
    }
    .zone-sub {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 4px;
        margin-top: 2px;
    }
    .zone-mark {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        flex: 0 0 auto;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt2Color);
        color: var(--txtHintColor);
        font-size: 14px;
    }
    .zone-mark.on {
        background: var(--successColor);
        color: #fff;
    }
    .zone-name {
        font-weight: 700;
    }
    .rate-name {
        font-size: var(--smFontSize, 13px);
        font-weight: 600;
    }
    .rate-text {
        flex: 1 1 auto;
    }
    .rate-chips {
        display: flex;
        flex-wrap: wrap;
        justify-content: flex-end;
        gap: 6px;
        min-width: 0;
    }
    .chip {
        padding: 3px 10px;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color);
        color: var(--txtHintColor);
        font-size: 12px;
        font-variant-numeric: tabular-nums;
        white-space: nowrap;
    }
    .chip-free {
        background: color-mix(in srgb, var(--successColor), transparent 85%);
        color: color-mix(in srgb, var(--successColor), var(--txtPrimaryColor) 35%);
        font-weight: 500;
    }
    .chip-price {
        background: color-mix(in srgb, var(--infoColor, var(--primaryColor)), transparent 88%);
        color: color-mix(in srgb, var(--infoColor, var(--primaryColor)), var(--txtPrimaryColor) 35%);
        font-weight: 500;
    }
    .chip-off {
        background: color-mix(in srgb, var(--dangerColor), transparent 88%);
        color: var(--dangerColor);
        font-weight: 500;
    }
    .rate-group {
        min-width: 0;
        max-width: 100%;
    }
    .rate-group .txt {
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .zone-warning {
        padding: 8px 14px;
        border-top: 1px solid var(--surfaceAlt2Color);
        background: color-mix(in srgb, var(--dangerColor), transparent 90%);
        color: var(--dangerColor);
        font-size: var(--smFontSize);
        text-align: center;
    }
    .zone-note {
        padding: 8px 14px;
        border-top: 1px solid var(--surfaceAlt2Color);
        background: var(--surfaceAlt1Color);
        color: var(--txtHintColor);
        font-size: var(--smFontSize);
        text-align: center;
    }
    .rate-add {
        display: flex;
        width: 100%;
        align-items: center;
        gap: 8px;
        padding: 12px 14px;
        border: 0;
        border-top: 1px solid var(--surfaceAlt2Color);
        border-radius: 0;
        background: none;
        color: var(--txtPrimaryColor);
        font: inherit;
        font-size: var(--smFontSize, 13px);
        font-weight: 500;
        text-align: left;
        cursor: pointer;
        transition: background-color 150ms ease-out;
    }
    .rate-add:hover,
    .rate-add:focus-visible {
        background: var(--surfaceAlt1Color);
    }
    .rate-add:active {
        transform: scale(0.99);
    }

    /* A zone switched off: covered, with the switch that brings it back. */
    .zone-disabled {
        position: absolute;
        inset: 0;
        z-index: 2;
        display: flex;
        flex-direction: column;
        align-items: center;
        justify-content: center;
        gap: 6px;
        background: color-mix(in srgb, var(--surfaceAlt1Color), transparent 25%);
        animation: fadeIn 200ms ease-out;
    }
    .zone-disabled-word {
        margin: 0;
        font-weight: 700;
        letter-spacing: 0.04em;
    }
    .zone-disabled-toggle {
        margin: 0;
    }
    .zone.off .zone-row,
    .zone.off .rate-row {
        opacity: 0.6;
    }

    .zone-selected {
        margin: 6px 0 2px;
    }
    .country-list {
        display: flex;
        flex-direction: column;
        gap: 2px;
        height: 200px;
        margin-top: 8px;
        padding: 4px;
        overflow-y: auto;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
    }
    .country-item {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 4px 6px;
        border-radius: var(--borderRadius);
        cursor: pointer;
        transition: background-color 150ms ease-out;
    }
    .country-item:hover {
        background: var(--surfaceAlt1Color);
    }

    .basis {
        margin: 0;
        padding: 0;
        border: 0;
    }
    .basis-legend {
        margin-bottom: 6px;
        font-size: var(--smFontSize, 13px);
        font-weight: 600;
    }
    .basis-option {
        display: flex;
        align-items: center;
        gap: 8px;
        padding: 3px 0;
        cursor: pointer;
    }
    .field-suffix {
        position: absolute;
        right: 12px;
        bottom: 10px;
        color: var(--txtHintColor);
        font-size: var(--smFontSize, 13px);
        pointer-events: none;
    }

    .preview-title {
        margin-bottom: 6px;
        font-size: var(--smFontSize, 13px);
        font-weight: 600;
    }
    .preview {
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 14px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color);
    }
    .preview-check {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        width: 20px;
        height: 20px;
        border-radius: 50%;
        background: var(--markColor);
        color: var(--markTxtColor);
        font-size: 12px;
    }
    .preview-name {
        flex: 1 1 auto;
        font-weight: 500;
    }
    .preview-price {
        font-weight: 500;
        font-variant-numeric: tabular-nums;
    }

    @media (max-width: 600px) {
        .rate-row {
            flex-wrap: wrap;
            row-gap: 6px;
        }
        .rate-text {
            flex: 1 1 0;
        }
        .rate-chips {
            order: 1;
            flex: 0 0 100%;
            justify-content: flex-start;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .rate-row,
        .zone-disabled {
            animation: none;
        }
        .zone-row,
        .rate-row,
        .rate-add,
        .country-item {
            transition: none;
        }
        .rate-add:active {
            transform: none;
        }
    }
</style>
