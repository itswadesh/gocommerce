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
     * panel. Each is offered only when the install can do it: a reset email
     * only where an email can leave the building, and sign-up only where the
     * install takes strangers (Config.Signup).
     *
     * One box first, for an email or a phone. A phone goes straight to a texted
     * code where one can be sent (auth-state's phone_login), and an email goes on
     * to its password, which stays out of sight until then. These are steps of
     * this card rather than addresses, because they ask for nothing a back
     * button could lose. A phone with nothing to text it goes on to a password
     * as an email does, and the installer asks for both at once.
     */
    import { tick } from "svelte";
    import { base } from "$app/paths";
    import { auth } from "$lib/api.js";
    import { applyBrand, brand, titled } from "$lib/brand.svelte.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { sentence } from "$lib/format.js";
    import AuthShell from "$lib/components/AuthShell.svelte";
    import PhoneCodeForm from "$lib/components/PhoneCodeForm.svelte";
    import { readablePhone, takePhoneFromFragment } from "$lib/phone-link.js";
    import { looksLikePhone, toIdentity } from "$lib/identity.js";

    // notice is a line said above the form, such as why the form is back.
    let { onauthenticated, notice = "" } = $props();

    // A number typed on a landing page arrives in the fragment (phone-link.js).
    const linkedPhone = takePhoneFromFragment();
    let identity = $state(readablePhone(linkedPhone));
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
     * Whether a reset email can actually leave the building. It stays false when
     * the probe fails: the panel does not know a fact about the store then, and
     * offering a button that 202s into silence is the worse of the two guesses.
     */
    let canReset = $state(false);
    let canSignUp = $state(false);
    let canPhone = $state(false);
    let phoneOpen = $state(false);
    // "who" is the one box; "password" and "code" are where it leads.
    let step = $state("who");
    const textsCode = $derived(canPhone && looksLikePhone(identity));

    $effect(() => {
        auth.state()
            .then((s) => {
                applyBrand(s);
                installed = !!s.installed;
                canReset = !!s.password_reset;
                canSignUp = !!s.signup;
                canPhone = !!s.phone_login;
                phoneOpen = !!s.phone_signup;
                // The box keeps the number either way; the installer's takes an
                // email only.
                if (linkedPhone && !installed) identity = "";
                if (!installed) step = "password";
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
        if (pass) {
            password = pass;
            step = "password";
        }
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
        error = "";
        if (step === "who") {
            step = textsCode ? "code" : "password";
            if (step === "password") tick().then(() => document.getElementById("login_pass")?.focus());
            return;
        }
        submitting = true;
        try {
            const record = installed
                ? await auth.login(identity, password)
                : await auth.install(identity, password);
            if (!installed) toast.success(brand.name ? `Welcome to ${brand.name}` : "Welcome");
            onauthenticated?.(record);
        } catch (err) {
            attempt += 1;
            error = sentence(err.message) || "Those details did not work. Try again.";
        } finally {
            submitting = false;
        }
    }
</script>

<svelte:head><title>{titled(installed === false ? "Create the first account" : "Sign in")}</title></svelte:head>

<AuthShell>
    <header class="auth-head">
        <h1 class="auth-title">{installed === false ? "Create the first account" : "Sign in"}</h1>
        <p class="auth-lede">
            {#if installed === false}
                Nobody can sign in here yet. The account you make now becomes this store's owner.
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
    {:else if step === "code"}
        <div in:rise>
            <PhoneCodeForm {onauthenticated} open={phoneOpen} number={toIdentity(identity)} onback={() => (step = "who")} />
        </div>
        {#if canSignUp}
            <p class="auth-alt">
                New here? <a class="auth-link" href="{base}/admin/auth/signup">Create a store</a>
            </p>
        {/if}
    {:else}
        <form class="auth-form auth-with-password-form" onsubmit={submit}>
            <div class="field" class:error={!!error}>
                <label for="login_identity">Email or phone</label>
                <!-- svelte-ignore a11y_autofocus -->
                <input
                    id="login_identity"
                    name="identity"
                    type="text"
                    inputmode="email"
                    required
                    autofocus
                    autocomplete="username"
                    autocapitalize="off"
                    spellcheck="false"
                    readonly={installed && step === "password"}
                    aria-invalid={!!error}
                    aria-describedby={error ? "login_refusal" : undefined}
                    bind:value={identity}
                    oninput={() => (error = "")}
                />
            </div>
            {#if installed && step === "password"}
                <div class="auth-field-foot auth-phone-foot">
                    <button type="button" class="auth-link auth-inline-btn" onclick={() => { step = "who"; password = ""; error = ""; }}>
                        Use something else
                    </button>
                </div>
            {:else if step === "who"}
                <div class="field-help">Outside India, start a phone number with its country code.</div>
            {/if}

            {#if step === "password"}
            <div in:rise>
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
                            aria-invalid={!!error}
                            aria-describedby={error ? "login_refusal" : undefined}
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

            {/if}

            <!-- Only once there is an account to reset, and only when a reset
                 email could actually be delivered: a button that silently does
                 nothing is worse than none. When nothing can be sent, the honest
                 sentence goes in its place. -->
            {#if step === "password" && installed && canReset}
                <div class="auth-field-foot">
                    <a class="auth-link" href={forgotHref}>Forgot password?</a>
                </div>
            {/if}

            {#key attempt}
                {#if error}
                    <p class="field-help error auth-refusal" id="login_refusal" role="alert" in:rise>{error}</p>
                {/if}
            {/key}

            <button class="btn lg block next" class:loading={submitting} disabled={submitting}>
                <span class="txt">{step === "who" ? (textsCode ? "Send code" : "Continue") : installed ? "Sign in" : "Create and sign in"}</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </button>
        </form>

        {#if step === "password" && installed && !canReset}
            <p class="auth-note">
                Forgotten your password? This install cannot send reset emails, so ask an owner to set
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
