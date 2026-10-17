<script>
    /**
     * Asking for a reset link.
     *
     * The engine answers 202 for every address — it will not say which ones
     * have an account — so there is nothing to read back but how this install
     * delivers: whether anything can be sent at all, and whether the email
     * carries a link or a code to paste (Config.PanelURL). Both are facts about
     * the install, the same for every caller, and this screen says which.
     *
     * The link itself is spent on /admin/auth/reset-password/<token>.
     */
    import { onMount } from "svelte";
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { api, auth } from "$lib/api.js";
    import { rise } from "$lib/motion.js";
    import { sentence } from "$lib/format.js";
    import AuthShell from "$lib/components/AuthShell.svelte";

    const LOGIN = `${base}/admin/auth/login`;

    let email = $state("");
    // null while the install is asked; then whether a link can be sent at all.
    let canReset = $state(null);
    let sending = $state(false);
    // What was sent, once it was: the address, and the engine's answer.
    let sent = $state(null);
    let problem = $state("");
    let attempt = $state(0);

    onMount(() => {
        email = page.url.searchParams.get("email") ?? "";
        auth.state()
            .then((s) => (canReset = !!s.password_reset))
            // Unknown is not "can": a button that 202s into silence is worse
            // than the sentence saying nothing can be sent.
            .catch(() => (canReset = false));
    });

    async function submit(e) {
        e.preventDefault();
        if (sending || !email.trim()) return;
        sending = true;
        problem = "";
        try {
            const result = await api.post(
                "/api/admin/password-reset",
                { identity: email.trim() },
                { admin: false },
            );
            sent = { email: email.trim(), delivery: result?.delivery || "email", link: !!result?.link };
        } catch (err) {
            // A malformed address or the throttle; neither depends on whether
            // an account exists, so the engine's words can be shown as they are.
            attempt += 1;
            problem = sentence(err.message) || "Could not ask for a reset link.";
        } finally {
            sending = false;
        }
    }
</script>

<svelte:head><title>Reset your password · GoCommerce</title></svelte:head>

<AuthShell>
    {#if sent && sent.delivery !== "none"}
        <header class="auth-head">
            <h1 class="auth-title">Check your inbox</h1>
            <p class="auth-lede">
                {#if sent.link}
                    If <strong>{sent.email}</strong> has an account here, a link to choose a new password
                    is on its way. It works once, for an hour.
                {:else}
                    If <strong>{sent.email}</strong> has an account here, a code to choose a new password
                    is on its way. Enter it on the next screen; it works once, for an hour.
                {/if}
            </p>
        </header>
        <div class="auth-actions">
            {#if !sent.link}
                <a class="btn lg block next" href="{base}/admin/auth/reset-password">
                    <span class="txt">Enter the code</span>
                    <i class="ri-arrow-right-line" aria-hidden="true"></i>
                </a>
            {/if}
            <a class="btn lg block secondary" href={LOGIN}>
                <span class="txt">Back to sign in</span>
            </a>
        </div>
        <p class="auth-note">
            Nothing after a few minutes? Check the spam folder, or
            <button type="button" class="auth-link auth-inline-btn" onclick={() => (sent = null)}>try another address</button>.
            An owner can also send you a link from Settings &rarr; Team.
        </p>
    {:else if sent || canReset === false}
        <!-- The fresher answer is the 202's; either way nothing can be sent,
             and a sentence is more use than a form that cannot work. -->
        <header class="auth-head">
            <h1 class="auth-title">No reset link can be sent</h1>
            <p class="auth-lede">This install has no way to send email yet, so there is nowhere to send a link.</p>
        </header>
        <p class="auth-note">
            Ask an owner to set a new password for you from Settings &rarr; Team. With access to the
            server: <code>gocommerce superuser update &lt;email&gt; &lt;password&gt;</code>
        </p>
        <div class="auth-actions">
            <a class="btn lg block secondary" href={LOGIN}>
                <span class="txt">Back to sign in</span>
            </a>
        </div>
    {:else if canReset === null}
        <div class="block txt-center"><span class="loader lg" aria-label="Loading"></span></div>
    {:else}
        <header class="auth-head">
            <h1 class="auth-title">Reset your password</h1>
            <p class="auth-lede">Type the email you sign in with, and we'll send a link to choose a new password.</p>
        </header>
        <form class="auth-form" onsubmit={submit}>
            <div class="field" class:error={!!problem}>
                <label for="forgot_email">Email</label>
                <!-- svelte-ignore a11y_autofocus -->
                <input
                    id="forgot_email"
                    type="email"
                    inputmode="email"
                    required
                    autofocus
                    autocomplete="username"
                    autocapitalize="off"
                    spellcheck="false"
                    enterkeyhint="send"
                    bind:value={email}
                    oninput={() => (problem = "")}
                />
            </div>
            {#key attempt}
                {#if problem}
                    <p class="field-help error auth-refusal" role="alert" in:rise>{problem}</p>
                {/if}
            {/key}
            <button type="submit" class="btn lg block next" class:loading={sending} disabled={sending || !email.trim()}>
                <span class="txt">Email me a link</span>
                <i class="ri-mail-send-line" aria-hidden="true"></i>
            </button>
        </form>
        <p class="auth-alt">
            Remembered it? <a class="auth-link" href={LOGIN}>Back to sign in</a>
        </p>
    {/if}
</AuthShell>
