<script>
    /**
     * What a screen renders instead of itself when the role does not carry its
     * read right.
     *
     * The engine refuses by name, so the panel can say the same thing before
     * the request rather than after it: a correctly-cut role otherwise gets a
     * fully armed screen where every control 403s, which reads as a broken
     * panel rather than as a permission.
     *
     * A static admin token never reaches this — `can()` answers true when the
     * record carries no rights array, because a script and the bootstrap
     * operator have no role to read. An implementation that tested for an empty
     * array instead would lock both of them out of every screen.
     */
    import { base } from "$app/paths";
    import { session } from "$lib/session.svelte.js";
    import { rightLabel } from "$lib/rights.js";

    let { right = "", anyOf = null, what = "this screen" } = $props();

    const needed = $derived(anyOf?.length ? anyOf : right ? [right] : []);
    const sentence = $derived(needed.map(rightLabel).join(" or "));
</script>

<div class="page">
    <div class="page-content">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item">Not available</div>
            </nav>
        </header>

        <div class="wrapper sm m-auto txt-center p-t-base">
            <i class="ri-lock-2-line txt-hint" style="font-size: 2.5rem;" aria-hidden="true"></i>

            <h5 class="m-t-sm m-b-xs">
                {#if sentence}
                    Your role does not carry {sentence}
                {:else}
                    Your role does not carry the rights {what} needs
                {/if}
            </h5>

            <p class="txt-hint">
                {#if session.record?.email}
                    Signed in as {session.record.email}{session.record.role
                        ? ` (${session.record.role})`
                        : ""}. Ask an owner to change what your role may do.
                {:else}
                    Ask an owner to change what your role may do.
                {/if}
            </p>

            <a href="{base}/" class="btn secondary m-t-sm">
                <span class="txt">Back to the dashboard</span>
            </a>
        </div>
    </div>
</div>
