<script>
    /**
     * PocketBase's superuser login, structurally: a centred `.wrapper.sm` with
     * the logo above an `.grid` form of `.col-12` rows, the password field
     * paired with an eye toggle in a `.field.addon`, and a full-width
     * `.btn.lg.block.next` at the bottom.
     *
     * The install branch is the same form under a different verb. A fresh
     * database has no operator, and asking someone to sign in with credentials
     * that cannot exist yet is a dead end — so the server is asked first.
     *
     * The reset branch is the same card again, and deliberately not its own
     * route: the shell renders this component on every unauthenticated path, so
     * a separate page would buy nothing but a second public prefix and a second
     * auth-state probe. The screen that spends a link *is* its own route — it
     * is reached from an email, with no card already on screen.
     */
    import { base } from "$app/paths";
    import { api, auth } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";

    let { onauthenticated } = $props();

    let identity = $state("");
    let password = $state("");
    let showPassword = $state(false);
    let submitting = $state(false);

    // null while we are still asking; true once we know an operator exists.
    let installed = $state(null);
    /*
     * Whether a reset link can actually leave the building. It stays false when
     * the probe fails: the panel does not know a fact about the store then, and
     * offering a button that 202s into silence is the worse of the two guesses.
     */
    let canReset = $state(false);

    // "login" | "reset" — which half of this card is on screen.
    let mode = $state("login");
    let resetEmail = $state("");
    let resetSending = $state(false);
    let resetSent = $state(false);
    // The 202's own answer about delivery, which is fresher than the probe.
    let resetDelivery = $state("email");

    $effect(() => {
        auth.state()
            .then((s) => {
                installed = !!s.installed;
                canReset = !!s.password_reset;
            })
            // If the probe itself fails the store is unreachable, and a login
            // form is the more useful of the two guesses.
            .catch(() => (installed = true));
    });

    async function submit(e) {
        e.preventDefault();
        if (submitting) return;
        submitting = true;
        try {
            const record = installed
                ? await auth.login(identity, password)
                : await auth.install(identity, password);
            if (!installed) toast.success("Welcome to GoCommerce");
            onauthenticated?.(record);
        } catch (err) {
            toast.error(err.message || "Invalid login credentials.");
        } finally {
            submitting = false;
        }
    }

    function askForReset() {
        resetEmail = identity;
        resetSent = false;
        resetDelivery = "email";
        mode = "reset";
    }

    /*
     * The engine answers 202 for every address — it will not say which ones
     * belong to an operator — so there is nothing to read back but how this
     * store delivers, and the screen moves on whatever comes back. A 429 is the
     * exception: that message is the engine's, and repeating it is not telling
     * the caller anything the API refused to.
     */
    async function requestReset(e) {
        e.preventDefault();
        if (resetSending) return;
        resetSending = true;
        try {
            const result = await api.post(
                "/api/admin/password-reset",
                { identity: resetEmail },
                { admin: false },
            );
            resetDelivery = result?.delivery || "email";
            resetSent = true;
        } catch (err) {
            if (err.status === 429) {
                toast.error(err.message);
            } else {
                // Anything else is a fact about the request, not about the
                // address, and the operator can only act on the first kind.
                toast.error(err.message || "Could not ask for a reset link.");
            }
        } finally {
            resetSending = false;
        }
    }
</script>

<div class="wrapper sm m-auto p-b-base">
    <header class="txt-center m-b-base">
        <img class="main-logo" src="{base}/images/logo.svg" alt="" aria-hidden="true" />
        <h5 class="m-t-10">
            {#if mode === "reset" && resetSent && resetDelivery === "none"}No reset link can be
                sent{:else if mode === "reset" && resetSent}Check your inbox{:else if mode ===
                "reset"}Reset your password{:else if installed === false}Create your first
                superuser{:else}Superuser login{/if}
        </h5>
    </header>

    {#if installed === null}
        <div class="block txt-center"><span class="loader lg"></span></div>
    {:else if mode === "reset"}
        {#if resetSent && resetDelivery === "none"}
            <!-- The 202 is the fresher answer than the probe, and it says
                 nothing is coming. A sentence is more use than a form that
                 cannot work. -->
            <div class="content txt-center txt-hint m-b-base">
                <p>
                    This store has no email delivery installed, so there is nowhere to send a link.
                </p>
                <p>
                    Ask an owner to set a new password for you from Settings &rarr; Team. With
                    access to the server:
                    <code>gocommerce superuser update &lt;email&gt; &lt;password&gt;</code>
                </p>
            </div>
        {:else if resetSent}
            <div class="content txt-center txt-hint m-b-base">
                <p>
                    If {resetEmail} belongs to an operator here, a link is on its way. It works once
                    and expires in an hour.
                </p>
                <p>
                    The email may carry a code instead of a link; paste it at
                    <code>/reset-password</code>. Nothing arrived? Check the spam folder, or ask an
                    owner to send you one from Settings &rarr; Team.
                </p>
            </div>
        {:else}
            <div class="content txt-center txt-hint m-b-base">
                <small>
                    Type the address you sign in with. If it belongs to an operator here, a link is
                    on its way — the answer is the same either way.
                </small>
            </div>

            <form class="grid" onsubmit={requestReset}>
                <div class="col-12">
                    <div class="field required">
                        <label for="reset_identity">Email</label>
                        <!-- svelte-ignore a11y_autofocus -->
                        <input
                            id="reset_identity"
                            name="identity"
                            type="email"
                            required
                            autofocus
                            autocomplete="username"
                            bind:value={resetEmail}
                        />
                    </div>
                </div>
                <div class="col-12">
                    <button
                        type="submit"
                        class="btn lg block next"
                        class:loading={resetSending}
                        disabled={resetSending}
                    >
                        <span class="txt">Email me a link</span>
                        <i class="ri-mail-send-line" aria-hidden="true"></i>
                    </button>
                </div>
            </form>
        {/if}

        <button type="button" class="btn lg block secondary" onclick={() => (mode = "login")}>
            <span class="txt">Back to sign in</span>
        </button>
    {:else}
        <form class="grid auth-with-password-form" onsubmit={submit}>
            {#if !installed}
                <div class="col-12">
                    <div class="content txt-center txt-hint">
                        <small>
                            This store has no operator yet. The account you create here signs in to
                            the panel from now on.
                        </small>
                    </div>
                </div>
            {/if}

            <div class="col-12">
                <div class="field">
                    <label for="login_identity">Email</label>
                    <!-- svelte-ignore a11y_autofocus -->
                    <input
                        id="login_identity"
                        name="identity"
                        type="email"
                        required
                        autofocus
                        autocomplete="username"
                        bind:value={identity}
                    />
                </div>
            </div>

            <div class="col-12">
                <div class="fields">
                    <div class="field">
                        <label for="login_pass">Password</label>
                        <input
                            id="login_pass"
                            name="password"
                            required
                            type={showPassword ? "text" : "password"}
                            autocomplete={installed ? "current-password" : "new-password"}
                            bind:value={password}
                        />
                    </div>
                    <div class="field addon">
                        <button
                            type="button"
                            tabindex="-1"
                            class="btn sm transparent secondary circle"
                            aria-label={showPassword ? "Hide password" : "Show password"}
                            onclick={() => (showPassword = !showPassword)}
                        >
                            <i class={showPassword ? "ri-eye-off-line" : "ri-eye-line"}></i>
                        </button>
                    </div>
                </div>
                {#if !installed}
                    <div class="link-hint m-t-5"><small>At least 8 characters.</small></div>
                {/if}
                <!-- Only once there is an account to reset, and only when a link
                     could actually be delivered: a button that silently does
                     nothing is worse than no button. When nothing can be sent,
                     the honest sentence goes in its place. -->
                {#if installed && canReset}
                    <div class="link-hint m-t-5">
                        <button
                            type="button"
                            class="btn sm transparent secondary"
                            onclick={askForReset}
                        >
                            <span class="txt">Forgot your password?</span>
                        </button>
                    </div>
                {:else if installed}
                    <div class="link-hint m-t-5">
                        <small>
                            Forgotten it? This store cannot send reset links; ask an owner to set a
                            new password for you.
                        </small>
                    </div>
                {/if}
            </div>

            <div class="col-12">
                <button class="btn lg block next" class:loading={submitting} disabled={submitting}>
                    <span class="txt">{installed ? "Login" : "Create and sign in"}</span>
                    <i class="ri-arrow-right-line"></i>
                </button>
            </div>
        </form>
    {/if}
</div>
