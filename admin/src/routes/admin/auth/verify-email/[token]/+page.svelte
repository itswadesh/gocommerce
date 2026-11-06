<script>
    /**
     * Where a verification email's link lands (D221). The link does nothing by
     * being fetched, because mail scanners fetch every link; this page spends it
     * once it has really opened in a browser.
     */
    import { page } from "$app/state";
    import { api } from "$lib/api.js";
    import { titled } from "$lib/brand.svelte.js";
    import { rise } from "$lib/motion.js";
    import AuthShell from "$lib/components/AuthShell.svelte";

    let state = $state("checking");

    api.post("/api/stores/verify-email", { token: page.params.token })
        .then(() => (state = "done"))
        .catch(() => (state = "gone"));
</script>

<svelte:head>
    <title>{titled("Verify your email")}</title>
    <meta name="robots" content="noindex" />
    <meta name="referrer" content="no-referrer" />
</svelte:head>

<AuthShell>
    {#if state === "checking"}
        <header class="auth-head">
            <h1 class="auth-title">Checking your link</h1>
        </header>
        <div class="block txt-center"><span class="loader lg" aria-label="Checking"></span></div>
    {:else if state === "done"}
        <header class="auth-head" in:rise>
            <h1 class="auth-title">Your email is verified</h1>
            <p class="auth-lede">Thank you. There is nothing else to do, and you can close this page.</p>
        </header>
    {:else}
        <header class="auth-head" in:rise>
            <h1 class="auth-title">This link no longer works</h1>
            <p class="auth-lede">It has expired or was already used.</p>
        </header>
    {/if}
</AuthShell>
