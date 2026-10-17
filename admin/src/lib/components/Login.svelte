<script>
    /**
     * Signing in, in the signed-out layout every screen before a store shares
     * (AuthShell).
     *
     * The install branch is the same form under a different verb. A fresh
     * database has nobody to sign in as, and asking for credentials that
     * cannot exist yet is a dead end — so the server is asked first.
     *
     * A forgotten password and signing up are addresses of their own
     * (/admin/auth/forgot-password, /admin/auth/signup) rather than modes of
     * this card, so a phone's back button returns here instead of leaving the
     * panel. Each is offered only when the install can do it: a reset link
     * only where an email can leave the building, and sign-up only where the
     * install takes strangers (Config.Signup).
     */
    import { base } from "$app/paths";
    import { auth } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { sentence } from "$lib/format.js";
    import AuthShell from "$lib/components/AuthShell.svelte";

    // notice is a line said above the form, such as why the form is back.
    let { onauthenticated, notice = "" } = $props();

    let identity = $state("");
    let password = $state("");
    let showPassword = $state(false);
    let submitting = $state(false);
    let error = $state("");
    // Bumped on each refusal, so the same message rises in again rather than
    // sitting there unchanged while the person wonders whether it was read.
    let attempt = $state(0);

    // null while we are still asking; true once we know an operator exists.
    let installed = $state(null);
    /*
     * Whether a reset link can actually leave the building. It stays false when
     * the probe fails: the panel does not know a fact about the store then, and
     * offering a button that 202s into silence is the worse of the two guesses.
     */
    let canReset = $state(false);
    let canSignUp = $state(false);

    $effect(() => {
        auth.state()
            .then((s) => {
                installed = !!s.installed;
                canReset = !!s.password_reset;
                canSignUp = !!s.signup;
            })
            // If the probe itself fails the store is unreachable, and a login
            // form is the more useful of the two guesses.
            .catch(() => (installed = true));
    });

    /*
     * A demo link may carry credentials in the URL fragment — `#email=…&
     * password=…`. The fragment is the one part of a URL the browser never
     * sends, so it reaches no access log and no Referer. It prefills the form
     * and nothing more: signing in is still the person's click. The fragment
     * is then dropped from the address bar so a copied URL does not carry a
     * password onward.
     */
    $effect(() => {
        if (typeof window === "undefined" || !window.location.hash) return;
        const params = new URLSearchParams(window.location.hash.slice(1));
        const email = params.get("email");
        const pass = params.get("password");
        if (!email && !pass) return;
        if (email) identity = email;
        if (pass) password = pass;
        history.replaceState(null, "", window.location.pathname + window.location.search);
    });

    // The address typed so far travels to the reset screen, so nobody types
    // it twice.
    const forgotHref = $derived(
        `${base}/admin/auth/forgot-password${identity.trim() ? `?email=${encodeURIComponent(identity.trim())}` : ""}`,
    );

    async function submit(e) {
        e.preventDefault();
        if (submitting) return;
        submitting = true;
        error = "";
        try {
            const record = installed
                ? await auth.login(identity, password)
                : await auth.install(identity, password);
            if (!installed) toast.success("Welcome to GoCommerce");
            onauthenticated?.(record);
        } catch (err) {
            attempt += 1;
            error = sentence(err.message) || "Those details did not work. Try again.";
        } finally {
            submitting = false;
        }
    }
</script>

<AuthShell>
    <header class="auth-head">
        <h1 class="auth-title">{installed === false ? "Create the first account" : "Sign in"}</h1>
        <p class="auth-lede">
            {#if installed === false}
                Nobody can sign in here yet. The account you make now owns this install.
            {:else}
                Welcome back. Sign in to run your store.
            {/if}
        </p>
    </header>

    {#if notice}
        <div class="alert warning auth-notice" role="status" in:rise>
            <p>{notice}</p>
        </div>
    {/if}

    {#if installed === null}
        <div class="block txt-center"><span class="loader lg" aria-label="Loading"></span></div>
    {:else}
        <form class="auth-form auth-with-password-form" onsubmit={submit}>
            <div class="field" class:error={!!error}>
                <label for="login_identity">Email</label>
                <!-- svelte-ignore a11y_autofocus -->
                <input
                    id="login_identity"
                    name="identity"
                    type="email"
                    inputmode="email"
                    required
                    autofocus
                    autocomplete="username"
                    autocapitalize="off"
                    spellcheck="false"
                    bind:value={identity}
                    oninput={() => (error = "")}
                />
            </div>

            <div>
                <div class="fields">
                    <div class="field" class:error={!!error}>
                        <label for="login_pass">Password</label>
                        <input
                            id="login_pass"
                            name="password"
                            required
                            type={showPassword ? "text" : "password"}
                            autocomplete={installed ? "current-password" : "new-password"}
                            enterkeyhint="go"
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
                {#if !installed}
                    <div class="field-help">At least 8 characters.</div>
                {/if}
            </div>

            <!-- Only once there is an account to reset, and only when a link
                 could actually be delivered: a link that silently does nothing
                 is worse than none. When nothing can be sent, the honest
                 sentence goes in its place. -->
            {#if installed && canReset}
                <div class="auth-field-foot">
                    <a class="auth-link" href={forgotHref}>Forgot password?</a>
                </div>
            {/if}

            {#key attempt}
                {#if error}
                    <p class="field-help error auth-refusal" role="alert" in:rise>{error}</p>
                {/if}
            {/key}

            <button class="btn lg block next" class:loading={submitting} disabled={submitting}>
                <span class="txt">{installed ? "Sign in" : "Create and sign in"}</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </button>
        </form>

        {#if installed && !canReset}
            <p class="auth-note">
                Forgotten your password? This install cannot send reset links, so ask an owner to set
                a new one for you from Settings &rarr; Team.
            </p>
        {/if}

        {#if installed && canSignUp}
            <p class="auth-alt">
                New here? <a class="auth-link" href="{base}/admin/auth/signup">Create a store</a>
            </p>
        {/if}
    {/if}
</AuthShell>
