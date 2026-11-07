<script>
    /**
     * "Continue with Google", under an "or", on the sign-in and sign-up cards.
     * Drawn only where auth-state names a client, so the caller passes it in.
     */
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { startGoogleSignIn } from "$lib/google-signin.js";

    let { clientId } = $props();
    let leaving = $state(false);
    let failed = $state(false);

    async function go() {
        if (leaving) return;
        leaving = true;
        failed = false;
        try {
            await startGoogleSignIn(
                clientId,
                `${window.location.origin}${base}/admin/auth/login`,
                page.url.searchParams.get("next") || "",
            );
        } catch {
            // Web Crypto needs https, and storage can be blocked: stay on the form.
            leaving = false;
            failed = true;
        }
    }
</script>

<div class="auth-or"><span>or</span></div>
<button type="button" class="btn lg block secondary auth-google" class:loading={leaving} disabled={leaving} onclick={go}>
    <svg viewBox="0 0 48 48" width="18" height="18" aria-hidden="true" focusable="false">
        <path fill="#EA4335" d="M24 9.5c3.5 0 6.7 1.2 9.2 3.6l6.9-6.9C35.9 2.4 30.5 0 24 0 14.6 0 6.5 5.4 2.6 13.2l8 6.2C12.5 13.7 17.8 9.5 24 9.5z" />
        <path fill="#4285F4" d="M46.9 24.5c0-1.6-.1-3.1-.4-4.5H24v9h12.9c-.6 3-2.2 5.5-4.7 7.2l7.7 6C44.4 37.9 46.9 31.8 46.9 24.5z" />
        <path fill="#FBBC05" d="M10.5 28.6c-.5-1.5-.8-3-.8-4.6s.3-3.2.8-4.6l-8-6.2C.9 16.5 0 20.1 0 24s.9 7.5 2.6 10.8l7.9-6.2z" />
        <path fill="#34A853" d="M24 48c6.5 0 11.9-2.1 15.9-5.8l-7.7-6c-2.1 1.4-4.9 2.3-8.2 2.3-6.2 0-11.5-4.2-13.4-9.9l-8 6.2C6.5 42.6 14.6 48 24 48z" />
    </svg>
    <span class="txt">Continue with Google</span>
</button>
{#if failed}
    <p class="field-help error auth-refusal" role="alert">This browser could not start that sign-in. Try another way in.</p>
{/if}
