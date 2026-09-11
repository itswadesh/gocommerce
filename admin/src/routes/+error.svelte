<script>
    /**
     * What the shell shows when there is no route at an address.
     *
     * SvelteKit's stock page is an `<h1>404</h1>` and a `<p>`, and this panel
     * drops them straight into `main.app`, which is a row flexbox whose
     * children are the sidebar and a page — so a mistyped URL used to land the
     * operator in two bare text nodes wedged beside the nav. The `.page`
     * wrapper is the fix: every screen owns one, and this is a screen.
     *
     * It covers the no-route 404 only. There is no `load` anywhere in the
     * panel, so nothing else reaches this file; a screen that throws while
     * rendering is caught by the `<svelte:boundary>` in the layout instead.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";

    const notFound = $derived(page.status === 404);

    // Set from an effect that puts the old title back, for the reason
    // PrintSheet gives: Svelte compiles a head title to a bare assignment and
    // app.html's static one is never re-applied on a client navigation.
    $effect(() => {
        const previous = document.title;
        document.title = notFound ? "Not found" : "Something went wrong";
        return () => {
            document.title = previous;
        };
    });
</script>

<div class="page">
    <div class="page-content">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item">{notFound ? "Not found" : "Error"}</div>
            </nav>
        </header>

        <div class="wrapper sm m-auto txt-center p-t-base">
            <i
                class="{notFound ? 'ri-compass-3-line' : 'ri-error-warning-line'} txt-hint"
                style="font-size: 2.5rem;"
                aria-hidden="true"
            ></i>

            <h5 class="m-t-sm m-b-xs">
                {#if notFound}
                    There is nothing at this address
                {:else}
                    {page.error?.message || "Something went wrong"}
                {/if}
            </h5>

            <p class="txt-hint">
                {#if notFound}
                    <code>{page.url.pathname}</code> does not match any screen in this panel.
                {:else}
                    The store answered {page.status}. Trying again is worth a go before anything
                    else.
                {/if}
            </p>

            <a href="{base}/" class="btn secondary m-t-sm">
                <span class="txt">Back to the dashboard</span>
            </a>
        </div>
    </div>
</div>
