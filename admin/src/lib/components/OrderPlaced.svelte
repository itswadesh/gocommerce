<script>
    /**
     * What the create drawer shows once the order exists.
     *
     * A component rather than another block in the orders page, which is
     * already 1,800 lines — and because everything here is read off one
     * response and none of it is the order list's business.
     *
     * It exists at all because `POST /api/admin/orders` answers with two things
     * the panel used to throw away. The access token is the guest's only
     * credential for their own order: that one response returns it and no other
     * route ever does, so if this screen does not show it, nobody can. The
     * payment intent is what the gateway said to do next — for a method that
     * needs the shopper in a page, an operator who never sees it is left with
     * an unpaid order and no way to collect.
     */
    import { toast } from "$lib/toast.svelte.js";

    let {
        order = null,
        payment = null,
        /** The provider as it names itself. The page owns the lookup, because it
            is the same list the rest of the drawer labels methods from — and a
            code in a sentence reads as a leak from the database. */
        methodName = "",
    } = $props();

    let copied = $state(false);

    async function copyToken() {
        try {
            await navigator.clipboard.writeText(order.access_token);
            copied = true;
        } catch {
            // Clipboard access is refused in plenty of ordinary situations — an
            // insecure origin, a browser setting. The token is on screen and
            // selectable, so say that rather than pretending it worked.
            toast.info("Copy it from the box above");
        }
    }

    /* An intent of kind "none" is cash on delivery: nothing to do and nothing
       to read out. Anything else is a conversation with a gateway that this
       panel cannot have on the operator's behalf. */
    const awaiting = $derived(!!payment && payment.kind && payment.kind !== "none");
    const clientData = $derived(Object.entries(payment?.client_data ?? {}));
</script>

<!-- The token exists in this response and nowhere else. Saying so is what stops
     somebody closing the drawer expecting to find it on the order later. -->
<div class="alert info m-b-base">
    <p>
        <i class="ri-information-line" aria-hidden="true"></i>
        This is the only time the customer's access token is shown. Close this and it cannot be
        recovered — it is how they read the order back without an account, and no other screen
        or route returns it.
    </p>
</div>

<!-- Full width rather than an input with the button beside it: the drawer is
     narrow, and this is the one string on the screen that has to be readable in
     full — an operator reading it down a telephone has no second chance. -->
<div class="field">
    <label for="order-token">Access token</label>
    <input id="order-token" type="text" readonly value={order.access_token} />
</div>
<div class="flex m-t-5">
    <button type="button" class="btn sm secondary" onclick={copyToken}>
        <i class={copied ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
        <span class="txt">{copied ? "Copied" : "Copy"}</span>
    </button>
    <div class="flex-fill"></div>
</div>
<div class="field-help m-t-5">
    The customer reads the order back with
    <span class="txt-code">GET /api/orders/{order.number}?token=…</span>
</div>

{#if awaiting}
    <div class="alert warning m-t-base">
        <p>
            <i class="ri-error-warning-line" aria-hidden="true"></i>
            This order is awaiting payment through {methodName || payment.provider}, and it
            cannot be completed from here. Give the customer what is below, or take the money
            another way and use Mark paid.
        </p>
    </div>

    {#if payment.reference}
        <div class="field">
            <label for="order-payment-ref">Payment reference</label>
            <input id="order-payment-ref" type="text" readonly value={payment.reference} />
        </div>
    {/if}
    {#each clientData as [key, value] (key)}
        <div class="flex m-t-5">
            <span class="txt-hint">{key}</span>
            <div class="flex-fill"></div>
            <span class="txt-code txt-ellipsis">{value}</span>
        </div>
    {/each}
{/if}
