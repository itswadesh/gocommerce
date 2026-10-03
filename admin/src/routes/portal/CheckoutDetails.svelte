<script>
    /**
     * What an order needs besides its lines: a PO number, where it goes, how
     * it is paid and — for a whole basket — how it travels. Shared by the
     * basket's checkout and by accepting a quote, which ask the same things.
     *
     * Addresses are the buyer's own identity address book, and a new one is
     * added here, inline, without leaving the order: a buyer who has to go
     * somewhere else to add an address loses the basket they were checking
     * out.
     *
     * The payment choice offers "on account" only where the company has one,
     * and greys it out while the company is on hold, saying why — the guard
     * would refuse it anyway, and a refusal after pressing Place order is the
     * worst moment to learn it.
     */
    import { onMount } from "svelte";
    import { formatMoney } from "$lib/format.js";
    import { COUNTRIES } from "$lib/countries.js";
    import { trade, tradeApi, explain, termsWords } from "$lib/trade.svelte.js";
    import Select from "$lib/components/Select.svelte";

    let {
        details = $bindable(),
        /** The basket to quote delivery for; none means the store decides at placing. */
        cartToken = "",
        /** Delivery is quoted on the whole basket, so a partial checkout leaves it to the store. */
        quoteDelivery = true,
        idPrefix = "co",
    } = $props();

    const company = $derived(trade.me.company);
    const onHold = $derived(company.status === "on_hold");
    const hasAccount = $derived(!!company.credit_limit);

    let addresses = $state([]);
    let methods = $state([]);
    let loading = $state(true);
    let failure = $state("");
    let adding = $state(false);
    let saving = $state(false);
    let addError = $state("");
    let draft = $state(blankAddress());
    let rates = $state([]);
    let ratesFor = $state("");
    let ratesLoading = $state(false);

    function blankAddress() {
        return {
            label: "",
            name: trade.account?.name || "",
            phone: trade.account?.phone || "",
            line1: "",
            line2: "",
            city: "",
            state: "",
            postal_code: "",
            country: addresses?.[0]?.country || "",
        };
    }

    const payOptions = $derived([
        ...(hasAccount
            ? [
                  {
                      code: "on_account",
                      name: "On account",
                      hint: onHold ? `Not while ${company.name}'s account is on hold` : termsWords(company),
                      disabled: onHold,
                  },
              ]
            : []),
        ...methods.map((m) => ({ code: m.code, name: m.name, hint: "", disabled: false })),
    ]);

    const chosen = $derived(addresses.find((a) => a.id === details.addressId) ?? null);

    $effect(() => {
        const a = chosen;
        details.address = a
            ? { line1: a.line1, line2: a.line2, city: a.city, state: a.state, postal_code: a.postal_code, country: a.country }
            : null;
        details.name = a?.name || trade.account?.name || "";
        details.phone = a?.phone || trade.account?.phone || "";
    });

    $effect(() => {
        details.ready =
            !!details.address &&
            !!details.method &&
            !payOptions.find((p) => p.code === details.method)?.disabled &&
            (!company.require_po || !!details.po?.trim());
    });

    onMount(async () => {
        try {
            const [book, pay] = await Promise.all([
                tradeApi.list("/x/identity/me/addresses?limit=100"),
                tradeApi.get("/api/checkout").catch(() => ({ methods: [] })),
            ]);
            addresses = book.data;
            methods = pay.methods ?? [];
            if (!details.addressId) details.addressId = (addresses.find((a) => a.is_default) ?? addresses[0])?.id ?? null;
            if (!details.method) details.method = payOptions.find((p) => !p.disabled)?.code ?? "";
            if (!addresses.length) {
                draft = blankAddress();
                adding = true;
            }
        } catch (err) {
            if (!err.handled) failure = explain(err);
        } finally {
            loading = false;
        }
    });

    // Delivery options follow the address: a rate belongs to a zone, and the
    // zone is the destination.
    $effect(() => {
        const a = details.address;
        const key = quoteDelivery && cartToken && a ? `${cartToken}|${a.country}|${a.state}` : "";
        if (key === ratesFor) return;
        ratesFor = key;
        details.rateId = null;
        rates = [];
        if (!key) return;
        ratesLoading = true;
        const params = new URLSearchParams({ cart: cartToken, country: a.country });
        if (a.state) params.set("state", a.state);
        tradeApi
            .get(`/api/checkout/rates?${params}`)
            .then((r) => {
                if (ratesFor !== key) return;
                rates = r?.rates ?? r ?? [];
                if (!Array.isArray(rates)) rates = [];
                details.rateId = rates[0]?.rate_id ?? null;
            })
            .catch(() => (rates = []))
            .finally(() => (ratesLoading = false));
    });

    async function saveAddress(event) {
        event?.preventDefault();
        if (saving) return;
        addError = "";
        const missing = ["line1", "city", "postal_code", "country"].filter((k) => !String(draft[k] ?? "").trim());
        if (missing.length) {
            addError = "Fill in the street, town or city, postcode and country.";
            return;
        }
        saving = true;
        try {
            const body = Object.fromEntries(Object.entries(draft).map(([k, v]) => [k, String(v ?? "").trim()]));
            const saved = await tradeApi.post("/x/identity/me/addresses", body);
            addresses = [...addresses, saved];
            details.addressId = saved.id;
            adding = false;
        } catch (err) {
            if (!err.handled) addError = explain(err);
        } finally {
            saving = false;
        }
    }

    function startAdding() {
        draft = blankAddress();
        addError = "";
        adding = true;
    }

    const countryOptions = COUNTRIES.map((c) => ({ value: c.value, label: c.label }));
    const oneLine = (a) => [a.line1, a.line2, a.city, a.state, a.postal_code, a.country].filter(Boolean).join(", ");
</script>

<div class="portal-details" aria-busy={loading}>
    {#if failure}
        <div class="alert danger m-b-sm" role="alert"><p>{failure}</p></div>
    {/if}

    <div class="field" class:required={company.require_po}>
        <label for="{idPrefix}-po">Your PO number</label>
        <input id="{idPrefix}-po" type="text" autocomplete="off" maxlength="100" bind:value={details.po} />
    </div>
    <div class="field-help m-b-base">
        {company.require_po ? `${company.name} needs one on every order.` : "Optional — it's printed on the order and the invoice."}
    </div>

    <fieldset class="portal-fieldset">
        <legend class="b2b-eyebrow">Deliver to</legend>
        {#if loading}
            <span class="skeleton-loader"></span>
        {:else}
            {#if addresses.length}
                <div class="portal-choices" role="radiogroup" aria-label="Delivery address">
                    {#each addresses as a (a.id)}
                        <label class="portal-choice" class:selected={details.addressId === a.id}>
                            <input type="radio" name="{idPrefix}-address" value={a.id} bind:group={details.addressId} />
                            <span class="portal-choice-body">
                                <span class="txt-bold">{a.label || a.name || "Address"}</span>
                                <span class="txt-hint txt-sm">{oneLine(a)}</span>
                            </span>
                        </label>
                    {/each}
                </div>
            {/if}
            {#if adding}
                <div class="portal-add-address portal-notice" role="group" aria-label="New address">
                    <div class="portal-form-grid">
                        <div class="field">
                            <label for="{idPrefix}-a-label">Label</label>
                            <input id="{idPrefix}-a-label" type="text" placeholder="Warehouse" bind:value={draft.label} />
                        </div>
                        <div class="field">
                            <label for="{idPrefix}-a-name">Receiver</label>
                            <input id="{idPrefix}-a-name" type="text" autocomplete="name" bind:value={draft.name} />
                        </div>
                        <div class="field">
                            <label for="{idPrefix}-a-phone">Phone</label>
                            <input id="{idPrefix}-a-phone" type="tel" autocomplete="tel" bind:value={draft.phone} />
                        </div>
                        <div class="field required portal-span-2">
                            <label for="{idPrefix}-a-line1">Street</label>
                            <input id="{idPrefix}-a-line1" type="text" autocomplete="address-line1" bind:value={draft.line1} />
                        </div>
                        <div class="field portal-span-2">
                            <label for="{idPrefix}-a-line2">Unit, building, floor</label>
                            <input id="{idPrefix}-a-line2" type="text" autocomplete="address-line2" bind:value={draft.line2} />
                        </div>
                        <div class="field required">
                            <label for="{idPrefix}-a-city">Town or city</label>
                            <input id="{idPrefix}-a-city" type="text" autocomplete="address-level2" bind:value={draft.city} />
                        </div>
                        <div class="field">
                            <label for="{idPrefix}-a-state">State or county</label>
                            <input id="{idPrefix}-a-state" type="text" autocomplete="address-level1" bind:value={draft.state} />
                        </div>
                        <div class="field required">
                            <label for="{idPrefix}-a-postal">Postcode</label>
                            <input id="{idPrefix}-a-postal" type="text" autocomplete="postal-code" bind:value={draft.postal_code} />
                        </div>
                        <div class="field required">
                            <label for="{idPrefix}-a-country">Country</label>
                            <Select id="{idPrefix}-a-country" bind:value={draft.country} options={countryOptions} placeholder="Choose…" />
                        </div>
                    </div>
                    {#key addError}
                        {#if addError}
                            <div class="field-help txt-danger portal-refusal" role="alert">{addError}</div>
                        {/if}
                    {/key}
                    <div class="portal-row-actions m-t-sm">
                        <button type="button" class="btn sm portal-press" class:loading={saving} disabled={saving} onclick={saveAddress}>
                            <span class="txt">Save address</span>
                        </button>
                        {#if addresses.length}
                            <button type="button" class="btn sm transparent secondary portal-press" onclick={() => (adding = false)}>
                                <span class="txt">Cancel</span>
                            </button>
                        {/if}
                    </div>
                </div>
            {:else}
                <button type="button" class="btn sm secondary transparent portal-press m-t-xs" onclick={startAdding}>
                    <i class="ri-add-line" aria-hidden="true"></i>
                    <span class="txt">Add an address</span>
                </button>
            {/if}
        {/if}
    </fieldset>

    {#if quoteDelivery && cartToken && (ratesLoading || rates.length > 1)}
        <fieldset class="portal-fieldset">
            <legend class="b2b-eyebrow">Delivery</legend>
            {#if ratesLoading && !rates.length}
                <span class="skeleton-loader"></span>
            {:else}
                <div class="portal-choices" role="radiogroup" aria-label="Delivery option">
                    {#each rates as r (r.rate_id)}
                        <label class="portal-choice" class:selected={details.rateId === r.rate_id}>
                            <input type="radio" name="{idPrefix}-rate" value={r.rate_id} bind:group={details.rateId} />
                            <span class="portal-choice-body">
                                <span class="txt-bold">{r.name}</span>
                                <span class="txt-hint txt-sm">{formatMoney(r.price)}</span>
                            </span>
                        </label>
                    {/each}
                </div>
            {/if}
        </fieldset>
    {/if}

    <fieldset class="portal-fieldset">
        <legend class="b2b-eyebrow">Payment</legend>
        {#if loading}
            <span class="skeleton-loader"></span>
        {:else if !payOptions.length}
            <p class="txt-hint m-0">{trade.store.name || "The store"} hasn't set up a way to pay yet.</p>
        {:else}
            <div class="portal-choices" role="radiogroup" aria-label="Payment">
                {#each payOptions as p (p.code)}
                    <label class="portal-choice" class:selected={details.method === p.code} class:disabled={p.disabled}>
                        <input type="radio" name="{idPrefix}-pay" value={p.code} disabled={p.disabled} bind:group={details.method} />
                        <span class="portal-choice-body">
                            <span class="txt-bold">{p.name}</span>
                            {#if p.hint}<span class="txt-hint txt-sm">{p.hint}</span>{/if}
                        </span>
                    </label>
                {/each}
            </div>
        {/if}
    </fieldset>
</div>
