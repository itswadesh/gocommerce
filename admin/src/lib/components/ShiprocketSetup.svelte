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
    import { copyText } from "$lib/clipboard.js";
    import { toast } from "$lib/toast.svelte.js";
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

    /* Tracking updates. Shiprocket refuses a webhook address holding its own
       name, "kartrocket", "sr" or "kr", so the engine serves a neutral path;
       the host is this server's, which the warning below checks too. */
    let hook = $state(null);
    let making = $state(false);
    const hookURL = $derived(hook ? location.origin + hook.path : "");
    const refused = $derived(/shiprocket|kartrocket|sr|kr/i.test(hookURL));

    async function makeToken() {
        making = true;
        try {
            hook = await request("POST", "/api/admin/x/fulfill-shiprocket/webhook");
            // Saved already; the mask keeps the drawer's Save from clearing it.
            form.webhook_token = "••••••••";
        } catch (err) {
            toast.error(err);
        } finally {
            making = false;
        }
    }

    async function copy(text, what) {
        if (await copyText(text)) toast.success(`${what} copied`);
        else toast.error(`Select the ${what.toLowerCase()} and copy it by hand`);
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

    <h6 class="section-title m-t-base">Tracking updates</h6>
    <div class="field-help m-b-sm">
        Shiprocket tells this store when a parcel moves. In Shiprocket, Settings › API › Webhooks: add
        the address below, switch it on, and paste the token as its security token. Until then the
        store asks Shiprocket every half hour.
    </div>
    <button type="button" class="btn sm secondary" class:loading={making} disabled={making} onclick={makeToken}>
        <i class="ri-key-2-line" aria-hidden="true"></i>
        <span class="txt">{hook ? "Make another token" : "Generate token"}</span>
    </button>
    {#if hook}
        <div class="sr-hook m-t-sm" in:rise>
            <div class="sr-hook-row">
                <span class="txt-hint txt-sm">Address</span>
                <code class="sr-hook-value">{hookURL}</code>
                <button type="button" class="btn circle sm transparent secondary" title="Copy the address" aria-label="Copy the address" onclick={() => copy(hookURL, "Address")}>
                    <i class="ri-file-copy-line" aria-hidden="true"></i>
                </button>
            </div>
            <div class="sr-hook-row">
                <span class="txt-hint txt-sm">Token</span>
                <code class="sr-hook-value">{hook.token}</code>
                <button type="button" class="btn circle sm transparent secondary" title="Copy the token" aria-label="Copy the token" onclick={() => copy(hook.token, "Token")}>
                    <i class="ri-file-copy-line" aria-hidden="true"></i>
                </button>
            </div>
            <div class="field-help">Saved already. It is shown only now; a new one replaces it.</div>
            {#if refused}
                <div class="alert warning m-t-sm" role="status">
                    <p>Shiprocket refuses addresses containing "sr", "kr" or its own name, and this server's address does. Open the panel at another address for this server, then generate again.</p>
                </div>
            {/if}
        </div>
    {/if}
</div>
