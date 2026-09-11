<script>
    /**
     * The wrapper a printable screen puts its content in.
     *
     * Printing is a route rather than a mode, because what a warehouse wants is
     * ten orders open in ten tabs and printed in a row — and because the order
     * drawer has no URL of its own, so print CSS alone would print the list
     * underneath it. The shell around this is hidden by the `@media print` block
     * that must stay last in gocommerce.css.
     *
     * The title is set with an effect that puts the old one back, not with
     * `<svelte:head><title>`: Svelte compiles a head title to a bare
     * `document.title =` assignment and nothing restores it on unmount, so
     * navigating away from /orders/12/print would leave the tab reading
     * "Order 1001" on the products screen. app.html's static title is the
     * parse-time default and is not re-applied on a client navigation.
     *
     * The contract for a screen using this: create the route, fetch what it
     * needs, gate it on `can(...)` with <NoAccess>, and wrap the body in
     * `<PrintSheet title="…" auto>`. `ri-printer-line` is already in the icon
     * font for the button a screen adds beside it.
     */
    let { title = "", auto = false, children } = $props();

    $effect(() => {
        if (!title) return;
        const previous = document.title;
        document.title = title;
        return () => {
            document.title = previous;
        };
    });

    $effect(() => {
        if (!auto) return;
        // A frame first, so the dialog opens over a painted page rather than
        // over the empty one the browser has so far.
        const frame = requestAnimationFrame(() => window.print());
        return () => cancelAnimationFrame(frame);
    });
</script>

<div class="print-sheet">
    {@render children?.()}
</div>
