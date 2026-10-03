<script>
    /**
     * Asking for a password reset. The answer is the same whether or not the
     * address has an account — the engine says nothing else, and neither does
     * this page — and the email links to /portal/reset-password when the
     * store points GOCOMMERCE_IDENTITY_RESET_URL there.
     */
    import { onMount } from "svelte";
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { tradeApi, sentence } from "$lib/trade.svelte.js";

    let email = $state("");
    let busy = $state(false);
    let sent = $state("");
    let problem = $state("");
    let attempt = $state(0);

    onMount(() => {
        email = page.url.searchParams.get("email") ?? "";
    });

    async function submit(event) {
        event.preventDefault();
        if (busy || !email.trim()) return;
        busy = true;
        problem = "";
        try {
            await tradeApi.post("/x/identity/password-reset", { email: email.trim() });
            sent = email.trim();
        } catch (err) {
            attempt += 1;
            problem = sentence(err.message) || "We couldn't send the link.";
        } finally {
            busy = false;
        }
    }
</script>

<h2 class="portal-out-heading">Reset your password</h2>

{#if sent}
    <div class="alert success portal-notice" role="status">
        <p>
            If there's an account for <strong>{sent}</strong>, we've emailed it a link to choose a new password. The
            link works for an hour.
        </p>
    </div>
    <p class="txt-center m-t-base m-b-0"><a class="portal-quiet-link" href="{base}/portal">Back to sign in</a></p>
{:else}
    <p class="txt-hint">We'll email you a link to choose a new one.</p>
    <form class="portal-form" onsubmit={submit} novalidate>
        <div class="field required" class:error={!!problem}>
            <label for="forgot-email">Email</label>
            <input id="forgot-email" type="email" autocomplete="email" required bind:value={email} />
        </div>
        {#key attempt}
            {#if problem}
                <div class="field-help txt-danger portal-refusal" role="alert">{problem}</div>
            {/if}
        {/key}
        <button type="submit" class="btn lg block m-t-base portal-press" class:loading={busy} disabled={busy || !email.trim()}>
            <span class="txt">Email me a link</span>
        </button>
    </form>
    <p class="txt-center m-t-sm m-b-0"><a class="portal-quiet-link" href="{base}/portal">Back to sign in</a></p>
{/if}
