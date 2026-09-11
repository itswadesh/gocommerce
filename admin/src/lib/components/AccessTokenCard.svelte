<script>
    /**
     * Giving a customer their "view your order" link back.
     *
     * The access token is the guest's only credential for their own order —
     * guest checkout is permanent (D22), so there is no account to sign into —
     * and it is returned exactly once, in the checkout reply. OrderPlaced shows
     * it there. After that nothing returned it: no listing, no order read, no
     * route. A customer who deleted the confirmation mail could not be helped
     * by anybody in the shop, which is the gap this closes.
     *
     * It reveals the existing token rather than issuing a new one. Rotating
     * would answer the same question and quietly break the link in the mail the
     * customer already has — so an operator helping with a *mislaid* link would
     * be destroying the mislaid copy. The reasoning is in core/orders.go, on
     * RevealAccessToken, which is where anybody deciding to change it will look.
     *
     * Deliberately a press rather than something the card shows: this is a
     * bearer credential, `POST /api/admin/orders/{id}/access-token` writes an
     * `order.token_reveal` audit row naming the operator every time it is
     * called, and a token that painted itself onto the screen on every load
     * would make that row meaningless. Which is also why the token is dropped
     * again the moment the screen is left — nothing here caches it.
     */
    import { api } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";

    let { order = null } = $props();

    let token = $state("");
    let busy = $state(false);
    let copied = $state(false);

    /* A different order means a different credential. Without this, walking
       from one order to the next with the panel's client-side navigation would
       show the first order's token under the second order's heading.

       The id and not the object: the order screen reloads the record after
       every action it takes, so an effect watching the prop itself would blank
       a revealed token the moment somebody marked the order paid. */
    const orderID = $derived(order?.id);

    $effect(() => {
        orderID;
        token = "";
        copied = false;
    });

    async function reveal() {
        if (busy) return;
        busy = true;
        try {
            const result = await api.post(`/api/admin/orders/${order.id}/access-token`, {});
            token = result.access_token;
        } catch (err) {
            toast.error(err);
        } finally {
            busy = false;
        }
    }

    async function copy() {
        try {
            await navigator.clipboard.writeText(token);
            copied = true;
        } catch {
            // Refused in plenty of ordinary situations — an insecure origin, a
            // browser setting. The token is on screen and selectable, so say
            // that rather than pretending it worked.
            toast.info("Copy it from the box above");
        }
    }
</script>

{#if !token}
    <button
        type="button"
        class="btn sm secondary m-t-sm"
        class:loading={busy}
        disabled={busy}
        onclick={reveal}
    >
        <i class="ri-key-2-line" aria-hidden="true"></i>
        <span class="txt">Access link…</span>
    </button>
    <div class="field-help">
        The customer's own way into this order, for when they have lost the mail. Reading it is
        recorded against you in the audit trail.
    </div>
{:else}
    <!-- Full width rather than an input with a button beside it, for
         OrderPlaced's reason: this column is narrow and it is the one string on
         the screen that has to be readable in full, because an operator reading
         it down a telephone has no second chance. -->
    <div class="field m-t-sm">
        <label for="recovered-token">Access token</label>
        <input id="recovered-token" type="text" readonly value={token} />
    </div>
    <div class="flex gap-5 flex-wrap m-t-5">
        <button type="button" class="btn sm secondary" onclick={copy}>
            <i class={copied ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
            <span class="txt">{copied ? "Copied" : "Copy"}</span>
        </button>
        <button type="button" class="btn sm transparent secondary" onclick={() => (token = "")}>
            <span class="txt">Hide</span>
        </button>
    </div>
    <div class="field-help">
        The customer reads the order back with
        <span class="txt-code">GET /api/orders/{order.number}?token=…</span> — the storefront's
        own order page is the same call. It is unchanged: anyone still holding the original
        link can keep using it.
    </div>
{/if}
