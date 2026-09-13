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
     * link arrives: a clicked link lands on /reset-password/<token> with the
     * field already filled, and an operator holding a pasted code — what a store
     * with no Config.PanelURL sends — lands on /reset-password and types it in.
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
    import { relativeTime } from "$lib/format.js";
    import { beginSession } from "$lib/session.svelte.js";
    import { toast } from "$lib/toast.svelte.js";

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
            toast.success(`Your password has been changed, ${record.email}`);
            // `goto`, not a document load: the shell reads the session from a
            // rune, so signing in re-renders it.
            await goto(base + "/");
        } catch (err) {
            error = err.message;
        } finally {
            submitting = false;
        }
    }
</script>

<svelte:head><title>Reset your password · GoCommerce</title></svelte:head>

<div class="page shopify-skin">
    <div class="wrapper sm m-auto p-b-base">
        <header class="txt-center m-b-base">
            <img class="main-logo" src="{base}/images/logo.svg" alt="" aria-hidden="true" />
            <h5 class="m-t-10">
                {#if checking}Checking your link{:else if problem}This link no longer
                    works{:else if !token}Paste your reset code{:else}Choose a new password{/if}
            </h5>
        </header>

        {#if checking}
            <div class="block txt-center"><span class="loader lg"></span></div>
        {:else if problem}
            <div class="content txt-center txt-hint m-b-base">
                <p>{problem}</p>
                <p>Ask for a new link from the sign-in page.</p>
            </div>
            <a href="{base}/" class="btn lg block secondary">
                <span class="txt">Go to the sign-in page</span>
            </a>
        {:else if !token}
            <!-- A store with no Config.PanelURL mails a code rather than a link.
                 This is where it is spent. -->
            <div class="content txt-center txt-hint m-b-base">
                <small>The email you were sent carries a code. Paste it here.</small>
            </div>
            <form class="grid" onsubmit={(e) => (e.preventDefault(), load(pasted))}>
                <div class="col-12">
                    <div class="field required">
                        <label for="reset_code">Reset code</label>
                        <!-- svelte-ignore a11y_autofocus -->
                        <input id="reset_code" required autofocus bind:value={pasted} />
                    </div>
                </div>
                <div class="col-12">
                    <button type="submit" class="btn lg block next" disabled={!pasted}>
                        <span class="txt">Continue</span>
                        <i class="ri-arrow-right-line" aria-hidden="true"></i>
                    </button>
                </div>
            </form>
            <a href="{base}/" class="btn lg block transparent">
                <span class="txt">Back to sign in</span>
            </a>
        {:else}
            <div class="content txt-center txt-hint m-b-base">
                <small>
                    Choose a password — nobody else will ever see it. This link works once, and
                    expires {expiry}.
                </small>
            </div>

            <form class="grid" onsubmit={submit}>
                <div class="col-12">
                    <div class="fields">
                        <div class="field required" class:error={!!error}>
                            <label for="reset_pass">New password</label>
                            <!-- svelte-ignore a11y_autofocus -->
                            <input
                                id="reset_pass"
                                required
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
                                tabindex="-1"
                                class="btn sm transparent secondary circle"
                                aria-label={showPassword ? "Hide password" : "Show password"}
                                onclick={() => (showPassword = !showPassword)}
                            >
                                <i
                                    class={showPassword ? "ri-eye-off-line" : "ri-eye-line"}
                                    aria-hidden="true"
                                ></i>
                            </button>
                        </div>
                    </div>
                </div>

                <div class="col-12">
                    <div class="field required" class:error={!!error}>
                        <label for="reset_confirm">New password again</label>
                        <input
                            id="reset_confirm"
                            required
                            type={showPassword ? "text" : "password"}
                            autocomplete="new-password"
                            bind:value={confirm}
                            oninput={() => (error = "")}
                        />
                    </div>
                    {#if error}<div class="field-help error">{error}</div>{/if}
                    <!-- The second sentence is what the transaction actually
                         does, and what somebody should know before clicking. -->
                    <div class="field-help">
                        At least 8 characters. Resetting signs you out of every device.
                    </div>
                </div>

                <div class="col-12">
                    <button
                        type="submit"
                        class="btn lg block next"
                        class:loading={submitting}
                        disabled={submitting}
                    >
                        <span class="txt">Set the password and sign in</span>
                        <i class="ri-arrow-right-line" aria-hidden="true"></i>
                    </button>
                </div>
            </form>
        {/if}
    </div>
</div>
