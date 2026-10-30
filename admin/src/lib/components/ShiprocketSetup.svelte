<script>
    /**
     * Connecting a Shiprocket account, inside its settings drawer: try the API
     * user typed above before saving it, and pick the pickup location from the
     * ones the account actually has rather than typing a nickname from memory.
     *
     * The password is sent as typed, or as the mask when it is the one already
     * saved, and the engine fills that in; nothing here is stored until Save.
     */
    import { tick } from "svelte";
    import { request } from "$lib/api.js";
    import { rise } from "$lib/motion.js";

    let { form = $bindable({}) } = $props();

    let testing = $state(false);
    let result = $state(null);
    let error = $state("");
    let outcome;

    async function test() {
        testing = true;
        error = "";
        result = null;
        try {
            result = await request("POST", "/api/admin/x/fulfill-shiprocket/connect", {
                body: { email: form.email ?? "", password: form.password ?? "" },
            });
            const names = (result.pickup_locations ?? []).map((p) => p.name);
            if (!names.includes(form.pickup_location)) {
                const primary = result.pickup_locations.find((p) => p.primary) ?? result.pickup_locations[0];
                if (primary) form.pickup_location = primary.name;
            }
        } catch (err) {
            error = err?.message || "Could not connect.";
        } finally {
            testing = false;
        }
        // The answer lands under the fields, often below the drawer's fold.
        await tick();
        outcome?.scrollIntoView({ block: "nearest" });
    }
</script>

<div class="sr-setup m-t-base">
    <h6 class="section-title">Account</h6>
    <div class="field-help m-b-sm">
        Shiprocket books through an API user: in Shiprocket, Settings › API › Add New API User, then
        type its email and password above and test them.
    </div>
    <button type="button" class="btn sm secondary" class:loading={testing} disabled={testing} onclick={test}>
        <i class="ri-plug-line" aria-hidden="true"></i>
        <span class="txt">Test connection</span>
    </button>

    <div aria-live="polite" bind:this={outcome}>
        {#if error}
            <div class="alert danger m-t-sm" role="alert" in:rise><p>{error}</p></div>
        {:else if result}
            <div class="sr-connected m-t-sm" in:rise>
                <i class="ri-checkbox-circle-line" aria-hidden="true"></i>
                Connected. Choose where couriers collect from:
            </div>
            {#if result.pickup_locations?.length}
                <div class="sr-pickups" role="radiogroup" aria-label="Pickup location">
                    {#each result.pickup_locations as p (p.name)}
                        <label class="sr-pickup" class:active={form.pickup_location === p.name} in:rise>
                            <input type="radio" name="sr-pickup" value={p.name} bind:group={form.pickup_location} />
                            <span class="sr-pickup-name">{p.name}{p.primary ? " · primary" : ""}</span>
                            <span class="txt-hint txt-sm">
                                {[p.address, p.address_2, p.city, p.state, p.pincode].filter(Boolean).join(", ")}
                            </span>
                        </label>
                    {/each}
                </div>
            {:else}
                <div class="alert warning m-t-sm" role="status">
                    <p>This account has no pickup address yet. Add one in Shiprocket under Settings › Pickup Addresses.</p>
                </div>
            {/if}
        {/if}
    </div>
</div>
