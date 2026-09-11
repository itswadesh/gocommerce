<script>
    /**
     * Warning before typed work is thrown away — including by the panel itself.
     *
     * `beforeunload` only covers leaving the site: a reload, a closed tab, a
     * typed address. Everything an operator actually does in a single-page
     * panel is a client-side navigation, so the product editor's breadcrumb,
     * its previous/next arrows and "Manage the tree" all discarded a dirty form
     * with no warning at all, and the shortest route out of a half-finished
     * edit was the one that said nothing.
     *
     * This renders nothing. It is a component rather than a function because
     * `beforeNavigate` has to be registered during a component's
     * initialisation, and a component is the shape a screen already knows how
     * to add.
     *
     *     <DirtyGuard dirty={form.changed} />
     *
     * The prompt is the browser's own `confirm`, deliberately. `beforeNavigate`
     * is synchronous — `cancel()` has to be called before the callback returns
     * — so a Drawer or a Confirm cannot answer in time. The alternative is to
     * cancel unconditionally and re-issue the navigation after a modal, which
     * breaks the Back button: a cancelled popstate has already been undone, and
     * re-navigating pushes a new entry instead of going back.
     */
    import { beforeNavigate } from "$app/navigation";

    let {
        dirty = false,
        message = "You have unsaved changes. Leave this screen and lose them?",
        /*
         * Whether this also guards leaving the site. SaveBar registers its own
         * `beforeunload` and renders one of these with it off, so a screen that
         * has a save bar does not end up with two listeners for one event.
         */
        unload = true,
    } = $props();

    beforeNavigate((navigation) => {
        if (!dirty) return;
        // `leave` is the browser leaving the app entirely, which beforeunload
        // already answers with the browser's own dialog. Answering it twice
        // shows one prompt and then another.
        if (navigation.type === "leave") return;
        if (window.confirm(message)) return;
        navigation.cancel();
    });

    $effect(() => {
        if (!dirty || !unload) return;
        // The message is the browser's own — every engine has ignored a custom
        // one for a decade — so preventDefault is the entire API.
        const warn = (event) => {
            event.preventDefault();
            event.returnValue = "";
        };
        window.addEventListener("beforeunload", warn);
        return () => window.removeEventListener("beforeunload", warn);
    });
</script>
