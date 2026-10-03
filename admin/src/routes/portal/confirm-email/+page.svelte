<script>
    /**
     * Confirming an address from the emailed link
     * (GOCOMMERCE_IDENTITY_VERIFY_URL, pointed at /portal/confirm-email?token={token}).
     * It is opened from an inbox, often where nobody is signed in, so the
     * token alone does it; a buyer who is signed in here sees their account
     * catch up at once.
     */
    import { onMount } from "svelte";
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { trade, tradeApi, refresh, loadMe, sentence } from "$lib/trade.svelte.js";

    let phase = $state("working");
    let email = $state("");
    let problem = $state("");

    onMount(async () => {
        const token = page.url.searchParams.get("token") ?? "";
        if (!token) {
            phase = "failed";
            problem = "This link is missing its code. Open the link in your email again.";
            return;
        }
        try {
            const account = await tradeApi.post("/x/identity/email-verification/confirm", { token });
            email = account?.email ?? "";
            phase = "done";
            if (trade.token) {
                await refresh().catch(() => {});
                loadMe();
            }
        } catch (err) {
            phase = "failed";
            problem =
                err.status === 400
                    ? "This link has expired or has already been used. Sign in and ask for a new one from the notice at the top of any page."
                    : sentence(err.message) || "We couldn't confirm the address.";
        }
    });
</script>

<h2 class="portal-out-heading">Confirm your email</h2>

{#if phase === "working"}
    <p class="txt-hint txt-center" aria-busy="true">
        <span class="portal-breathe portal-inline-mark" aria-hidden="true"><i class="ri-mail-check-line"></i></span>
        Confirming…
    </p>
{:else if phase === "done"}
    <div class="alert success portal-notice portal-result" role="status">
        <p><strong>{email || "Your address"} is confirmed.</strong> You can order at your company's prices.</p>
    </div>
    <a class="btn lg block m-t-base portal-press" href="{base}/portal">
        <span class="txt">{trade.token ? "Go to your account" : "Sign in"}</span>
    </a>
{:else}
    <div class="alert danger portal-refusal" role="alert"><p>{problem}</p></div>
    <p class="txt-center m-t-base m-b-0"><a class="portal-quiet-link" href="{base}/portal">Go to sign in</a></p>
{/if}
