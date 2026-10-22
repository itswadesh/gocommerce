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
    import { auth } from "$lib/api.js";
    import { forgetStores } from "$lib/stores.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { sentence } from "$lib/format.js";
    import AuthShell from "$lib/components/AuthShell.svelte";
    import { titled } from "$lib/brand.svelte.js";

    const LOGIN = `${base}/admin/auth/login`;

    // What the install said: "loading", "open", "closed", "uninstalled", or
    // "unreachable" when it could not be asked.
    let state = $state("loading");
    let email = $state("");
    let password = $state("");
    let showPassword = $state(false);
    let submitting = $state(false);
    let problem = $state("");
    // Which field a refusal is about, so only that one is marked: "email",
    // "password", or "" for one about neither (the limit, say).
    let problemField = $state("");
    // The address the engine said is taken, so the refusal can offer the way
    // in for exactly that address.
    let taken = $state("");
    let attempt = $state(0);

    function probe() {
        state = "loading";
        auth.state()
            .then((s) => (state = !s.installed ? "uninstalled" : s.signup ? "open" : "closed"))
            .catch(() => (state = "unreachable"));
    }
    onMount(probe);

    async function submit(e) {
        e.preventDefault();
        if (submitting) return;
        submitting = true;
        problem = "";
        problemField = "";
        taken = "";
        try {
            await auth.signup(email.trim(), password);
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
                // The engine's validation names the field it refused.
                if (err.status === 400) problemField = /password/i.test(problem) ? "password" : /email|address/i.test(problem) ? "email" : "";
            }
        } finally {
            submitting = false;
        }
    }

    const emailRefused = $derived(!!taken || problemField === "email");
    const passwordRefused = $derived(problemField === "password");
</script>

<svelte:head><title>{titled("Create your store")}</title></svelte:head>

<AuthShell>
    {#if state === "loading"}
        <div class="block txt-center"><span class="loader lg" aria-label="Loading"></span></div>
    {:else if state === "unreachable"}
        <header class="auth-head">
            <h1 class="auth-title">Couldn't reach the store</h1>
            <p class="auth-lede">Check your connection, then try again.</p>
        </header>
        <div class="auth-actions">
            <button type="button" class="btn lg block next" onclick={probe}>
                <span class="txt">Try again</span>
            </button>
        </div>
    {:else if state === "uninstalled"}
        <header class="auth-head">
            <h1 class="auth-title">Nobody can sign in here yet</h1>
            <p class="auth-lede">The first account is made on the sign-in screen, and it owns this store.</p>
        </header>
        <div class="auth-actions">
            <a class="btn lg block next" href={LOGIN}>
                <span class="txt">Make the first account</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </a>
        </div>
    {:else if state === "closed"}
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
            <div class="field" class:error={emailRefused}>
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
                    aria-invalid={emailRefused}
                    aria-describedby={emailRefused ? "signup_refusal" : undefined}
                    bind:value={email}
                    oninput={() => {
                        taken = "";
                        if (problemField === "email") problem = problemField = "";
                    }}
                />
            </div>

            <div>
                <div class="fields">
                    <div class="field" class:error={passwordRefused}>
                        <label for="signup_pass">Password</label>
                        <input
                            id="signup_pass"
                            required
                            minlength="8"
                            type={showPassword ? "text" : "password"}
                            autocomplete="new-password"
                            enterkeyhint="go"
                            aria-invalid={passwordRefused}
                            aria-describedby={passwordRefused ? "signup_refusal signup_pass_help" : "signup_pass_help"}
                            bind:value={password}
                            oninput={() => {
                                if (problemField === "password") problem = problemField = "";
                            }}
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
                    <p class="field-help error auth-refusal" id="signup_refusal" role="alert" in:rise>
                        {taken} already has an account.
                        <a class="auth-link" href={LOGIN}>Sign in</a> or
                        <a class="auth-link" href="{base}/admin/auth/forgot-password?email={encodeURIComponent(taken)}">reset the password</a>.
                    </p>
                {:else if problem}
                    <p class="field-help error auth-refusal" id="signup_refusal" role="alert" in:rise>{problem}</p>
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
