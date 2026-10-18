<script>
    /**
     * Spending a password reset, by its link or by its code.
     *
     * Every reset email carries a 6-digit code, and a link as well when the
     * install knows its panel's address (Config.PanelURL). Both open one reset,
     * and the token is an optional route parameter so one file serves both.
     *
     * A clicked link lands on /admin/auth/reset-password/<token>, and that is
     * structurally the accept-invite page: look the link up first, say plainly
     * when it is dead, and only then ask for a password. Discovering a spent
     * link *after* typing a password twice is the failure that shape avoids.
     *
     * Without a token this is one form: the address the code went to, the
     * code, and the new password twice. The engine binds a code to its address,
     * because six digits name nobody on their own, so the address is part of
     * spending it. There is no lookup first: a mistyped code is answered by the
     * code field again, not by a screen saying the reset has died. The
     * forgot-password screen hands the address over in history state, never in
     * the URL, so it reaches no access log and no Referer.
     *
     * Nothing here names the account. The lookup deliberately returns only an
     * expiry: an endpoint that turned a token into an address would be the one
     * place in this flow that confirms who has an account, which is exactly what
     * the request route's identical 202 refuses to do.
     */
    import { page } from "$app/state";
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api } from "$lib/api.js";
    import { relativeTime, sentence } from "$lib/format.js";
    import { beginSession } from "$lib/session.svelte.js";
    import { forgetStores } from "$lib/stores.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import AuthShell from "$lib/components/AuthShell.svelte";

    const LOGIN = `${base}/admin/auth/login`;
    const FORGOT = `${base}/admin/auth/forgot-password`;

    const linked = $derived(page.params.token ?? "");

    // The link's token, once its lookup says it is alive.
    let token = $state("");
    let checking = $state(false);
    let target = $state(null);
    let problem = $state("");

    // What spends a code. The address arrives from the forgot-password screen.
    let email = $state(page.state?.email ?? "");
    let code = $state("");
    let codeRefused = $state(false);
    /*
     * A store that saved its own copy of the old email template still mails
     * the link's long token with no link, and this field is the only place
     * left to spend it. A token is sent as one, and needs no address.
     */
    const typed = $derived(code.replace(/\s+/g, ""));
    const pastedToken = $derived(typed.length > 7 && !/^\d+$/.test(typed));

    let password = $state("");
    let confirm = $state("");
    let showPassword = $state(false);
    let submitting = $state(false);
    let error = $state("");
    let attempt = $state(0);

    // "expires in an hour" beats a timestamp: the question is how long is left.
    const expiry = $derived(target?.expires_at ? relativeTime(target.expires_at) : "soon");

    $effect(() => {
        if (linked) load(linked);
    });

    async function load(t) {
        checking = true;
        problem = "";
        try {
            target = await api.get(`/api/admin/password-reset/${t}`, { admin: false });
            token = t;
        } catch (err) {
            // A dead link is the expected failure here, not an exception: it has
            // been used, or left too long. The engine gives one message for all
            // of those, because the remedy for all of them is the same button.
            problem = err.message;
            target = null;
        } finally {
            checking = false;
        }
    }

    async function submit(e) {
        e.preventDefault();
        if (submitting) return;
        codeRefused = false;
        error = "";
        if (password !== confirm) {
            attempt += 1;
            error = "The two passwords do not match.";
            return;
        }
        submitting = true;
        try {
            const result = await api.post(
                "/api/admin/password-reset/confirm",
                linked
                    ? { token, password }
                    : pastedToken
                      ? { token: typed, password }
                      : { email: email.trim(), code, password },
                { admin: false },
            );
            // The engine returns what a sign-in returns, so arriving by a reset
            // and arriving by password end in exactly the same state.
            const record = beginSession(result);
            // Whoever was signed in on this browser before, the store list
            // the shell holds is theirs: have it settle again, as at sign-in.
            forgetStores();
            toast.success(`Your password has been changed, ${record.email}`);
            // `goto`, not a document load: the shell reads the session from a
            // rune, so signing in re-renders it.
            await goto(base + "/dash");
        } catch (err) {
            attempt += 1;
            // A code the engine will not take is said under the code field,
            // with the way to a new one; every other refusal under the form.
            if (!linked && err.code === "invalid_token") codeRefused = true;
            else error = sentence(err.message);
        } finally {
            submitting = false;
        }
    }
</script>

<svelte:head><title>Reset your password · GoCommerce</title></svelte:head>

<AuthShell>
    {#if checking}
        <header class="auth-head">
            <h1 class="auth-title">Checking your link</h1>
        </header>
        <div class="block txt-center"><span class="loader lg" aria-label="Checking"></span></div>
    {:else if problem}
        <header class="auth-head">
            <h1 class="auth-title">This link no longer works</h1>
            <p class="auth-lede">{sentence(problem)} Ask for a new one; it takes a minute.</p>
        </header>
        <div class="auth-actions">
            <a class="btn lg block next" href={FORGOT}>
                <span class="txt">Email me a new link</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </a>
            <a class="btn lg block secondary" href={LOGIN}>
                <span class="txt">Back to sign in</span>
            </a>
        </div>
    {:else if !linked || token}
        <header class="auth-head">
            <h1 class="auth-title">Choose a new password</h1>
            <p class="auth-lede">
                {#if linked}
                    This link works once, and expires {expiry}.
                {:else}
                    Enter the 6&#8209;digit code from the email we sent you. It works once, for an hour.
                {/if}
            </p>
        </header>

        <form class="auth-form" onsubmit={submit}>
            {#if !linked}
                <div class="field">
                    <label for="reset_email">Email</label>
                    <!-- svelte-ignore a11y_autofocus -->
                    <input
                        id="reset_email"
                        type="email"
                        inputmode="email"
                        required={!pastedToken}
                        autofocus={!email}
                        autocomplete="username"
                        autocapitalize="off"
                        spellcheck="false"
                        enterkeyhint="next"
                        bind:value={email}
                        oninput={() => (codeRefused = false)}
                    />
                </div>

                <div class="field" class:error={codeRefused}>
                    <label for="reset_code">Code</label>
                    <!-- svelte-ignore a11y_autofocus -->
                    <input
                        id="reset_code"
                        required
                        autofocus={!!email}
                        inputmode="numeric"
                        autocomplete="one-time-code"
                        autocapitalize="off"
                        spellcheck="false"
                        enterkeyhint="next"
                        aria-invalid={codeRefused}
                        aria-describedby={codeRefused ? "code_refusal" : undefined}
                        bind:value={code}
                        oninput={() => (codeRefused = false)}
                    />
                </div>
                {#key attempt}
                    {#if codeRefused}
                        <p class="field-help error auth-refusal" id="code_refusal" role="alert" in:rise>
                            {#if pastedToken}
                                That code has expired or was already used.
                                <a class="auth-link" href={FORGOT}>Ask for a new code</a>.
                            {:else}
                                That code does not match this address, or it has expired. Check both against the
                                email, or <a class="auth-link" href={FORGOT}>ask for a new code</a>.
                            {/if}
                        </p>
                    {/if}
                {/key}
            {/if}

            <div class="fields">
                <div class="field" class:error={!!error}>
                    <label for="reset_pass">New password</label>
                    <!-- svelte-ignore a11y_autofocus -->
                    <input
                        id="reset_pass"
                        required
                        minlength="8"
                        autofocus={!!linked}
                        type={showPassword ? "text" : "password"}
                        autocomplete="new-password"
                        bind:value={password}
                        oninput={() => (error = "")}
                    />
                </div>
                <div class="field addon">
                    <button
                        type="button"
                        class="btn sm transparent secondary circle"
                        aria-label={showPassword ? "Hide password" : "Show password"}
                        aria-pressed={showPassword}
                        onclick={() => (showPassword = !showPassword)}
                    >
                        <i class={showPassword ? "ri-eye-off-line" : "ri-eye-line"} aria-hidden="true"></i>
                    </button>
                </div>
            </div>

            <div>
                <div class="field" class:error={!!error}>
                    <label for="reset_confirm">New password again</label>
                    <input
                        id="reset_confirm"
                        required
                        type={showPassword ? "text" : "password"}
                        autocomplete="new-password"
                        enterkeyhint="go"
                        bind:value={confirm}
                        oninput={() => (error = "")}
                    />
                </div>
                <!-- The second sentence is what the transaction actually does,
                     and what somebody should know before pressing the button. -->
                <div class="field-help">At least 8 characters. Resetting signs you out of every device.</div>
            </div>

            {#key attempt}
                {#if error}
                    <p class="field-help error auth-refusal" role="alert" in:rise>{error}</p>
                {/if}
            {/key}

            <button type="submit" class="btn lg block next" class:loading={submitting} disabled={submitting}>
                <span class="txt">Save and sign in</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </button>
        </form>
        {#if !linked}
            <p class="auth-alt">
                <a class="auth-link" href={LOGIN}>Back to sign in</a>
            </p>
        {/if}
    {/if}
</AuthShell>
