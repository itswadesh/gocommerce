<script>
    /**
     * Signing up, on an install that takes strangers (Config.Signup).
     *
     * Two steps, as on any hosted shop builder: the account here, the store on
     * the next screen. The second is the store picker's create form, the one
     * every signed-in person already has, so there is one way a store is
     * opened — its suggested name, its country and its currency included.
     *
     * An address that already has an account is told so (the engine's 409):
     * an open sign-up cannot refuse one without saying why. The way on from
     * there is signing in, or a reset link for the same address.
     */
    import { onMount } from "svelte";
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { api, auth } from "$lib/api.js";
    import { beginSession } from "$lib/session.svelte.js";
    import { forgetStores } from "$lib/stores.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { sentence } from "$lib/format.js";
    import AuthShell from "$lib/components/AuthShell.svelte";

    const LOGIN = `${base}/admin/auth/login`;

    // null while the install is asked; then whether it takes sign-ups.
    let open = $state(null);
    let email = $state("");
    let password = $state("");
    let showPassword = $state(false);
    let submitting = $state(false);
    let problem = $state("");
    // The address the engine said is taken, so the refusal can offer the way
    // in for exactly that address.
    let taken = $state("");
    let attempt = $state(0);

    onMount(() => {
        auth.state()
            .then((s) => (open = !!s.signup && !!s.installed))
            .catch(() => (open = false));
    });

    async function submit(e) {
        e.preventDefault();
        if (submitting) return;
        submitting = true;
        problem = "";
        taken = "";
        try {
            const result = await api.post(
                "/api/admin/signup",
                { email: email.trim(), password },
                { admin: false },
            );
            beginSession(result);
            // Whoever was signed in on this browser before, the store list the
            // shell holds is theirs: have it settle again, as at sign-in.
            forgetStores();
            toast.success("Your account is ready");
            await goto(`${base}/select-store?signup`, { replaceState: true });
        } catch (err) {
            attempt += 1;
            if (err.code === "email_taken") {
                taken = email.trim();
            } else {
                problem = sentence(err.message) || "Could not make the account.";
            }
        } finally {
            submitting = false;
        }
    }
</script>

<svelte:head><title>Create your store · GoCommerce</title></svelte:head>

<AuthShell>
    {#if open === null}
        <div class="block txt-center"><span class="loader lg" aria-label="Loading"></span></div>
    {:else if !open}
        <header class="auth-head">
            <h1 class="auth-title">Sign-up is closed here</h1>
            <p class="auth-lede">
                This install doesn't take new accounts. Ask a store's owner to invite you, or sign in
                if you already have an account.
            </p>
        </header>
        <div class="auth-actions">
            <a class="btn lg block next" href={LOGIN}>
                <span class="txt">Go to sign in</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </a>
        </div>
    {:else}
        <header class="auth-head">
            <p class="auth-step">Step 1 of 2</p>
            <h1 class="auth-title">Create your store</h1>
            <p class="auth-lede">Start with the email and password you'll sign in with. You'll name your store next.</p>
        </header>

        <form class="auth-form" onsubmit={submit}>
            <div class="field" class:error={!!taken}>
                <label for="signup_email">Email</label>
                <!-- svelte-ignore a11y_autofocus -->
                <input
                    id="signup_email"
                    type="email"
                    inputmode="email"
                    required
                    autofocus
                    autocomplete="email"
                    autocapitalize="off"
                    spellcheck="false"
                    bind:value={email}
                    oninput={() => (taken = "")}
                />
            </div>

            <div>
                <div class="fields">
                    <div class="field" class:error={!!problem}>
                        <label for="signup_pass">Password</label>
                        <input
                            id="signup_pass"
                            required
                            minlength="8"
                            type={showPassword ? "text" : "password"}
                            autocomplete="new-password"
                            enterkeyhint="go"
                            aria-describedby="signup_pass_help"
                            bind:value={password}
                            oninput={() => (problem = "")}
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
                <div class="field-help" id="signup_pass_help">At least 8 characters.</div>
            </div>

            {#key attempt}
                {#if taken}
                    <p class="field-help error auth-refusal" role="alert" in:rise>
                        {taken} already has an account.
                        <a class="auth-link" href="{LOGIN}">Sign in</a> or
                        <a class="auth-link" href="{base}/admin/auth/forgot-password?email={encodeURIComponent(taken)}">reset the password</a>.
                    </p>
                {:else if problem}
                    <p class="field-help error auth-refusal" role="alert" in:rise>{problem}</p>
                {/if}
            {/key}

            <button type="submit" class="btn lg block next" class:loading={submitting} disabled={submitting}>
                <span class="txt">Continue</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </button>
        </form>

        <p class="auth-alt">
            Already have an account? <a class="auth-link" href={LOGIN}>Sign in</a>
        </p>
    {/if}
</AuthShell>
