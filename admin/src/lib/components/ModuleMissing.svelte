<script>
    /**
     * What a module screen renders instead of itself in a binary that was not
     * built with that module.
     *
     * It is not a permission and not an error: the routes behind the screen
     * were never mounted, so every request it would make answers 404. A
     * bookmarked URL therefore explains itself rather than showing four failed
     * calls, and the nav hides the link for the same reason.
     *
     * Which modules a store has is decided in its own `main()`, so there is
     * nothing actionable here for the operator reading it — only the name to
     * take to whoever builds the binary.
     */
    import { base } from "$app/paths";

    let { module = "", what = "There is nothing to manage here." } = $props();
</script>

<div class="wrapper sm m-auto txt-center p-t-base">
    <i class="ri-puzzle-2-line txt-hint" style="font-size: 2.5rem;" aria-hidden="true"></i>

    <h5 class="m-t-sm m-b-xs">This store was built without the {module} module</h5>

    <p class="txt-hint">
        {what} A store composes the modules it needs in its own <code class="txt-code">main()</code
        >, so installing one is a change to the binary rather than a setting.
    </p>

    <a href="{base}/" class="btn secondary m-t-sm">
        <span class="txt">Back to the dashboard</span>
    </a>
</div>
