<script module>
    // One title id per instance: several drawers are mounted at once on most
    // screens, and aria-labelledby pointing at a shared id names the wrong one.
    let seq = 0;
</script>

<script>
    /**
     * PocketBase's editor panel: a right-anchored, full-height `.modal` that
     * slides 30px in from the edge over a dimmed page.
     *
     * The open/closed state is an attribute rather than a class because the
     * transition is driven by `@starting-style` and `transition-behavior:
     * allow-discrete` in modal.css — the element goes from `display: none` to
     * `display: flex` and still animates. Nothing here has to know that; it
     * just has to set the attribute the stylesheet reads.
     */
    import { dismissable } from "$lib/dismiss.js";
    import { portal } from "$lib/portal.js";
    import { trapFocus } from "$lib/focus.js";

    let {
        open = false,
        title = "",
        size = "",
        onclose,
        header,
        children,
        footer,
    } = $props();

    /*
     * A drawer needs an accessible name, and where it comes from depends on
     * whether the caller supplied its own header: with one there is no
     * `.modal-title` element to point at, so the name is the prop instead.
     */
    seq += 1;
    const titleID = `modal-title-${seq}`;
</script>

<div
    class="modal {size}"
    data-modal-state={open ? "open" : "closed"}
    inert={!open}
    role="dialog"
    aria-modal="true"
    tabindex="-1"
    aria-labelledby={header ? undefined : titleID}
    aria-label={header ? title || "Dialog" : undefined}
    use:portal
    use:trapFocus={open}
    use:dismissable={{ onclose, enabled: open }}
>
    <header class="modal-header">
        {#if header}
            {@render header()}
        {:else}
            <h5 class="modal-title" id={titleID}>{title}</h5>
        {/if}
        <button
            type="button"
            class="btn circle transparent secondary modal-close-btn m-l-auto"
            aria-label="Close"
            onclick={() => onclose?.()}
        >
            <i class="ri-close-line" aria-hidden="true"></i>
        </button>
    </header>

    <div class="modal-content">
        {@render children?.()}
    </div>

    {#if footer}
        <footer class="modal-footer">
            {@render footer()}
        </footer>
    {/if}
</div>
