<script>
    /**
     * PocketBase's toasts: a fixed container pinned to the bottom centre, each
     * toast a `.toast-container` with an icon rail on the left. The icon glyph
     * itself comes from CSS (`.toast.success .toast-icon::before`), so the
     * markup carries only the variant class.
     *
     * `use:portal` is not cosmetic. Rendered where it sits in the layout, the
     * container is inside app.html's `<div style="display: contents">` wrapper
     * — which is the one node the modal focus trap inerts while a modal is
     * open. Leaving it there would silence the live region and make the
     * dismiss button unclickable exactly when a modal's save fails and a toast
     * is the panel's only error surface. `.toasts-container` is already
     * `position: fixed`, so moving it to <body> changes nothing visually.
     *
     * Polite rather than assertive, deliberately: this container carries
     * successes too, and an assertive region interrupts a screen reader
     * mid-sentence every time something saves.
     */
    import { toast } from "$lib/toast.svelte.js";
    import { portal } from "$lib/portal.js";
</script>

<div
    class="toasts-container"
    role="status"
    aria-live="polite"
    aria-atomic="false"
    use:portal
>
    {#each toast.items as item (item.id)}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div
            class="toast {item.type}"
            class:removing={item.removing}
            onmouseenter={() => toast.hold(item.id)}
            onmouseleave={() => toast.release(item.id)}
        >
            <div class="toast-container">
                <div class="toast-icon"></div>
                <div class="toast-content">
                    <span>{item.message}</span>
                    <button
                        type="button"
                        class="m-l-auto btn circle sm transparent secondary toast-remove"
                        aria-label="Dismiss"
                        onclick={() => toast.remove(item.id)}
                    >
                        <i class="ri-close-line" aria-hidden="true"></i>
                    </button>
                </div>
            </div>
        </div>
    {/each}
</div>
