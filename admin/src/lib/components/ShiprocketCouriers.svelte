<script>
    /**
     * Choosing who carries a parcel booked through Shiprocket: every courier
     * that serves the order's pincode, with Shiprocket's rate, its delivery
     * estimate and whether it collects cash, cheapest first.
     *
     * It asks once when the ship dialog opens and again only when the weight
     * is changed and left alone for a moment — the engine keeps answers for
     * ten minutes too, so typing a weight is not a call to Shiprocket per key.
     */
    import { request, query } from "$lib/api.js";
    import { formatMoney } from "$lib/format.js";
    import { rise } from "$lib/motion.js";

    let { order = null, courier = $bindable(""), weight = $bindable("") } = $props();

    let quote = $state(null);
    let loading = $state(false);
    let error = $state("");
    let timer;
    let asked = 0;

    async function load(orderId, kg) {
        const mine = ++asked;
        loading = true;
        error = "";
        try {
            const q = await request(
                "GET",
                `/api/admin/x/fulfill-shiprocket/orders/${orderId}/couriers` + query({ weight_kg: kg }),
            );
            if (mine !== asked) return;
            quote = q;
            const ids = (q.couriers ?? []).map((c) => c.id);
            if (!ids.includes(Number(courier))) courier = String(q.recommended_id || q.cheapest_id || ids[0] || "");
        } catch (err) {
            if (mine !== asked) return;
            quote = null;
            courier = "";
            error = err?.message || "Shiprocket could not be asked.";
        } finally {
            if (mine === asked) loading = false;
        }
    }

    // The id, not the order: the page hands down a fresh order after every
    // action, and that is not a reason to ask Shiprocket again.
    const orderId = $derived(order?.id);
    $effect(() => {
        const id = orderId;
        const kg = Number(weight) > 0 ? weight : "";
        if (!id) return;
        clearTimeout(timer);
        timer = setTimeout(() => load(id, kg), quote ? 400 : 0);
        return () => clearTimeout(timer);
    });

    const picks = $derived(
        quote
            ? [
                  { id: quote.recommended_id, label: "Recommended" },
                  { id: quote.cheapest_id, label: "Cheapest" },
                  { id: quote.fastest_id, label: "Fastest" },
              ].filter((p) => p.id)
            : [],
    );

    function eta(c) {
        if (c.etd) return c.etd;
        if (c.estimated_days) return `${c.estimated_days} day${c.estimated_days === 1 ? "" : "s"}`;
        return "";
    }
</script>

<div class="sr-couriers" aria-busy={loading}>
    <div class="sr-couriers-head">
        <span class="sr-couriers-title">Courier</span>
        <label class="sr-weight">
            <span class="txt-hint txt-sm">Weight (kg)</span>
            <input
                type="number"
                min="0.01"
                step="0.01"
                inputmode="decimal"
                placeholder={quote?.weight_kg ? String(quote.weight_kg) : ""}
                bind:value={weight}
                aria-label="Parcel weight in kilograms"
            />
        </label>
    </div>

    {#if loading && !quote}
        <div class="sr-couriers-loading txt-hint txt-sm" role="status" in:rise>
            <span class="sr-spinner" aria-hidden="true"></span>
            Asking Shiprocket which couriers serve {order?.address?.postal_code || "this pincode"}…
        </div>
    {:else if error}
        <div class="alert danger sr-alert" role="alert" in:rise><p>{error}</p></div>
    {:else if quote}
        {#if quote.couriers.length === 0}
            <div class="alert warning sr-alert" role="status" in:rise>
                <p>
                    No courier on this Shiprocket account delivers to {quote.delivery_postcode}
                    {quote.cod ? "and collects cash on delivery" : ""}.
                </p>
            </div>
        {:else}
            {#if picks.length}
                <div class="sr-picks" role="group" aria-label="Quick picks">
                    {#each picks as p (p.label)}
                        <button
                            type="button"
                            class="sr-pick"
                            class:active={String(p.id) === String(courier)}
                            aria-pressed={String(p.id) === String(courier)}
                            onclick={() => (courier = String(p.id))}
                        >
                            {p.label}
                        </button>
                    {/each}
                </div>
            {/if}
            <div class="sr-courier-list" class:stale={loading} role="radiogroup" aria-label="Couriers">
                {#each quote.couriers as c (c.id)}
                    <label class="sr-courier" class:active={String(c.id) === String(courier)} in:rise>
                        <input type="radio" name="sr-courier" value={String(c.id)} bind:group={courier} />
                        <span class="sr-courier-name">{c.name}</span>
                        <span class="sr-courier-rate">{formatMoney(c.rate)}</span>
                        <span class="sr-courier-meta txt-hint txt-sm">
                            {#if eta(c)}<span>Delivery by {eta(c)}</span>{/if}
                            {#if c.cod}<span class="label sr-cod">COD</span>{/if}
                            {#if c.rating}<span>★ {c.rating}</span>{/if}
                        </span>
                    </label>
                {/each}
            </div>
            <div class="field-help">
                Shiprocket's rates for {quote.weight_kg} kg from {quote.pickup_postcode} to
                {quote.delivery_postcode}{quote.cod ? ", collecting the order's total in cash" : ""}.
            </div>
        {/if}
    {/if}
</div>
