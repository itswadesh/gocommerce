<script>
    /**
     * Spending a password-reset link.
     *
     * The second screen in the panel somebody reaches without an account, and it
     * is structurally the accept-invite page: look the link up first, say
     * plainly when it is dead, and only then ask for a password. Discovering a
     * spent link *after* typing a password twice is the failure that shape
     * avoids.
     *
     * The token is an optional route parameter, so one file serves both ways a
     * link arrives: a clicked link lands on /admin/auth/reset-password/<token> with the
     * field already filled, and an operator holding a pasted code — what a store
     * with no Config.PanelURL sends — lands on /admin/auth/reset-password and types it in.
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

    const linked = $derived(page.params.token ?? "");

    // What will be spent: the token from the link, or the one pasted below.
    let token = $state("");
    // The code an operator types when the email carried one instead of a link.
    let pasted = $state("");
    let checking = $state(false);
    let target = $state(null);
    let problem = $state("");

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
        if (password !== confirm) {
            attempt += 1;
            error = "The two passwords do not match.";
            return;
        }
        submitting = true;
        error = "";
        try {
            const result = await api.post(
                "/api/admin/password-reset/confirm",
                { token, password },
                { admin: false },
            );
            // The engine returns what a sign-in returns, so arriving by a reset
            // link and arriving by password end in exactly the same state.
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
            error = sentence(err.message);
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
            <a class="btn lg block next" href="{base}/admin/auth/forgot-password">
                <span class="txt">Email me a new link</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </a>
            <a class="btn lg block secondary" href={LOGIN}>
                <span class="txt">Back to sign in</span>
            </a>
        </div>
    {:else if !token}
        <!-- A store with no Config.PanelURL mails a code rather than a link.
             This is where it is spent. -->
        <header class="auth-head">
            <h1 class="auth-title">Enter your reset code</h1>
            <p class="auth-lede">The email you were sent carries a code. Paste it here.</p>
        </header>
        <form class="auth-form" onsubmit={(e) => (e.preventDefault(), load(pasted.trim()))}>
            <div class="field">
                <label for="reset_code">Reset code</label>
                <!-- svelte-ignore a11y_autofocus -->
                <input
                    id="reset_code"
                    required
                    autofocus
                    autocomplete="one-time-code"
                    autocapitalize="off"
                    spellcheck="false"
                    bind:value={pasted}
                />
            </div>
            <button type="submit" class="btn lg block next" disabled={!pasted.trim()}>
                <span class="txt">Continue</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </button>
        </form>
        <p class="auth-alt">
            <a class="auth-link" href={LOGIN}>Back to sign in</a>
        </p>
    {:else}
        <header class="auth-head">
            <h1 class="auth-title">Choose a new password</h1>
            <p class="auth-lede">This link works once, and expires {expiry}.</p>
        </header>

        <form class="auth-form" onsubmit={submit}>
            <div class="fields">
                <div class="field" class:error={!!error}>
                    <label for="reset_pass">New password</label>
                    <!-- svelte-ignore a11y_autofocus -->
                    <input
                        id="reset_pass"
                        required
                        minlength="8"
                        autofocus
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
    {/if}
</AuthShell>
