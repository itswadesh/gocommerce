<script>
    /**
     * A confirmation that asks for one line of text before it goes through.
     *
     * Two actions on an order need exactly this and had neither: marking an
     * order paid, which takes the reference the money arrived under, and
     * cancelling one, which takes the reason. Both endpoints accept the field,
     * both store it, and the panel was sending a hard-coded string to each — so
     * every panel-marked payment recorded nothing about how it was collected,
     * and fraud, a customer's change of mind, an out-of-stock line and a
     * duplicate all read identically afterwards.
     *
     * It is not a wider `Confirm`: that component is shared by ten destructive
     * actions and its whole shape is a heading, a sentence and two buttons.
     * Putting a form in it would put a form in all ten. This follows the
     * pattern the orders screen already uses for an action that needs a field —
     * ShipDialog and RefundDialog — so there are now three of one kind rather
     * than one of three.
     *
     * The value is the dialog's own state and is re-seeded from `initial` each
     * time it opens, so an abandoned cancellation does not offer its reason to
     * the next order.
     */
    import { untrack } from "svelte";
    import Drawer from "$lib/components/Drawer.svelte";

    let {
        open = false,
        title = "",
        /** The consequence, in the same voice Confirm uses. */
        message = "",
        label = "",
        placeholder = "",
        /** Under the field: what the text is *for*, and where it ends up. */
        help = "",
        confirmLabel = "Confirm",
        danger = false,
        busy = false,
        /** Refuse an empty box. Off by default — the engine coalesces an empty
         *  reference precisely so it cannot erase one that is already there. */
        required = false,
        /** What the engine will take. Both fields it serves are short. */
        maxlength = 200,
        initial = "",
        onclose,
        onconfirm,
    } = $props();

    /*
     * Per instance, and it has to be. The orders screen mounts two of these at
     * once — mark paid and cancel — and a Drawer stays in the document while it
     * is closed. With one shared id the footer's `form="prompt-form"` resolves
     * to the FIRST match in tree order, so pressing Cancel order would submit
     * the mark-paid dialog's form. The label's `for` is the same hazard, one
     * click quieter.
     */
    const uid = Math.random().toString(36).slice(2, 10);
    const formID = "prompt-form-" + uid;
    const fieldID = "prompt-value-" + uid;

    let value = $state("");

    /* `initial` is read untracked: the page re-reads the order after every
       action, and a reload behind an open dialog must not reset what somebody
       has typed into it. */
    $effect(() => {
        if (open) value = untrack(() => initial);
    });

    const text = $derived(value.trim());
    const blocked = $derived(busy || (required && !text));

    function submit(event) {
        event?.preventDefault();
        if (blocked) return;
        onconfirm?.(text);
    }
</script>

<Drawer {open} size="popup sm" {title} onclose={() => onclose?.()}>
    <form id={formID} onsubmit={submit}>
        {#if message}
            <div class="txt-hint m-b-sm">{message}</div>
        {/if}

        <div class="field" class:required>
            <label for={fieldID}>{label}</label>
            <input
                id={fieldID}
                type="text"
                {placeholder}
                {maxlength}
                autocomplete="off"
                bind:value
            />
        </div>
        {#if help}
            <div class="field-help">{help}</div>
        {/if}
    </form>

    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => onclose?.()}>
            <span class="txt">Cancel</span>
        </button>
        <button
            type="submit"
            form={formID}
            class="btn expanded"
            class:danger
            class:loading={busy}
            disabled={blocked}
        >
            <span class="txt">{confirmLabel}</span>
        </button>
    {/snippet}
</Drawer>
