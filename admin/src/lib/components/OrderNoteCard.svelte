<script>
    /**
     * The shop's own note on an order.
     *
     * Its own component rather than another block in a 1,800-line screen, for
     * the reason the ship and refund dialogs are one: several changes land in
     * that file at once.
     *
     * `cardActions` is the parent's snippet, passed in rather than duplicated —
     * the same shape Drawer takes its header and footer in, so Cancel stays in
     * the same place under every card that edits in place.
     */
    let { note = "", draft = $bindable(""), editing = false, onedit, cardActions } = $props();

    /* The parent's save bar reads the draft, and it is re-seeded from the saved
       note each time the card opens so an abandoned edit does not survive as a
       suggestion. */
    $effect(() => {
        if (editing) draft = note;
    });
</script>

<section class="order-card">
    <div class="order-card-head">
        <h6 class="order-card-title">Note</h6>
        <!-- No handler, no button. A note is edited with orders.write, and the
             screen says so by withholding `onedit` rather than by passing a
             second prop that could disagree with it. -->
        {#if !editing && onedit}
            <button type="button" class="btn sm transparent secondary" onclick={onedit}>
                <span class="txt">Edit</span>
            </button>
        {/if}
    </div>

    {#if editing}
        <form id="notes-form" onsubmit={(e) => e.preventDefault()}>
            <!-- `aria-label` rather than a `<label>`, for the reason
                 TokenInput gives: a visible one here would put the word "Note"
                 directly under the card titled NOTE, saying the same thing
                 twice a line apart. The control still has an accessible name. -->
            <div class="field">
                <textarea
                    id="order-notes"
                    aria-label="Note"
                    rows="4"
                    bind:value={draft}
                ></textarea>
            </div>
            <div class="field-help">
                Only the shop sees this. The customer's own view of the order does not show it,
                and saving it sends them nothing.
            </div>
        </form>
        {@render cardActions()}
    {:else if note}
        <p class="order-note">{note}</p>
    {:else}
        <p class="txt-hint txt-sm">No note yet.</p>
    {/if}
</section>
