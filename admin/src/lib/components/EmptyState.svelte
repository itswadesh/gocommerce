<script>
    /**
     * What a list says when it has nothing to show: what is missing, in one
     * line; why, in one sentence; and the next thing to do, as a button.
     *
     * Two cases look alike and are not: a list that is empty, and a list whose
     * filters hide everything. The caller says which by what it passes — a
     * filtered list's action is Clear, an empty one's is how rows get there.
     */
    let { icon = "ri-inbox-line", title, text = "", children } = $props();
</script>

<div class="empty-state" role="status">
    <span class="empty-state-icon" aria-hidden="true"><i class={icon}></i></span>
    <h3 class="empty-state-title">{title}</h3>
    {#if text}<p class="empty-state-text">{text}</p>{/if}
    {#if children}
        <div class="empty-state-actions">{@render children()}</div>
    {/if}
</div>
