<script>
    /**
     * Accepting an invitation.
     *
     * The only screen in the panel somebody reaches without an account, so it
     * stands on its own: it says who the invitation is for and what it grants
     * before asking for anything. It never asks for the email — that was
     * decided by whoever sent the link, and an editable field there is only a
     * way to typo yourself out of the invitation.
     */
    import { page } from "$app/state";
    import { base } from "$app/paths";
    import { goto } from "$app/navigation";
    import { auth } from "$lib/api.js";
    import { setStore } from "$lib/store-scope.js";
    import { forgetStores } from "$lib/stores.svelte.js";
    import { rightLabel } from "$lib/rights.js";
    import { toast } from "$lib/toast.svelte.js";
    import { sentence } from "$lib/format.js";
    import { rise } from "$lib/motion.js";
    import AuthShell from "$lib/components/AuthShell.svelte";

    const token = $derived(page.params.token);
    // The store that sent the link. An install serves many stores (D83), and
    // the invitation, and the team it joins, are that store's.
    const store = $derived(page.url.searchParams.get("store") ?? "");

    let loading = $state(true);
    let invitation = $state(null);
    let problem = $state("");

    let password = $state("");
    let confirm = $state("");
    let showPassword = $state(false);
    let submitting = $state(false);
    let error = $state("");
    let attempt = $state(0);

    $effect(() => {
        load(token, store);
    });

    async function load(t, s) {
        loading = true;
        problem = "";
        try {
            invitation = await auth.invitation(t, s);
        } catch (err) {
            // A dead link is the expected failure here, not an exception: it
            // has been used, revoked, or left too long. The engine's message
            // says which, and that is the part the person needs.
            problem = err.message;
        } finally {
            loading = false;
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
            const record = await auth.accept(token, password, store);
            // The store that invited them is the one this panel now works in.
            setStore(store);
            // Somebody already signed in keeps their session through this, so
            // nothing else tells the shell its store list and caches are now
            // stale: have it settle the store again, as at sign-in.
            forgetStores();
            // Accepting signs you in, so there is nowhere to send them but in.
            // They have typed this password once already and asking them to
            // prove it immediately is how a first impression goes wrong.
            toast.success(`Welcome, ${record.email}`);

            // `goto`, not a document load: the shell reads the session from a
            // rune now, so signing in re-renders it. This was a full reload for
            // as long as the shell decided once, on mount, from localStorage.
            await goto(base + "/dash");
        } catch (err) {
            attempt += 1;
            error = sentence(err.message);
        } finally {
            submitting = false;
        }
    }
</script>

<svelte:head><title>Accept your invitation · GoCommerce</title></svelte:head>

<AuthShell>
    {#if loading}
        <header class="auth-head">
            <h1 class="auth-title">Checking your invitation</h1>
        </header>
        <div class="block txt-center"><span class="loader lg" aria-label="Checking"></span></div>
    {:else if problem}
        <header class="auth-head">
            <h1 class="auth-title">This link no longer works</h1>
            <p class="auth-lede">{sentence(problem)} Ask whoever invited you to send a new one.</p>
        </header>
        <div class="auth-actions">
            <a href="{base}/admin/auth/login" class="btn lg block secondary">
                <span class="txt">Go to sign in</span>
            </a>
        </div>
    {:else}
        <header class="auth-head">
            <h1 class="auth-title">Join as {invitation.email}</h1>
            <p class="auth-lede">
                {invitation.invited_by ? `${invitation.invited_by} invited you` : "You have been invited"}
                as <strong>{invitation.role}</strong>. Choose a password — nobody else will ever see it. If
                you already work in another store here, use the password you sign in with there.
            </p>
        </header>

        <div class="list m-b-base">
            {#each invitation.rights as right (right)}
                <div class="list-item">
                    <i class="ri-check-line" aria-hidden="true"></i>
                    <span class="txt">{rightLabel(right)}</span>
                </div>
            {/each}
        </div>

        <form class="auth-form" onsubmit={submit}>
            <div class="fields">
                <div class="field" class:error={!!error}>
                    <label for="invite_pass">Password</label>
                    <!-- svelte-ignore a11y_autofocus -->
                    <input
                        id="invite_pass"
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
                    <label for="invite_confirm">Password again</label>
                    <input
                        id="invite_confirm"
                        required
                        type={showPassword ? "text" : "password"}
                        autocomplete="new-password"
                        enterkeyhint="go"
                        bind:value={confirm}
                        oninput={() => (error = "")}
                    />
                </div>
                <div class="field-help">At least 8 characters.</div>
            </div>

            {#key attempt}
                {#if error}
                    <p class="field-help error auth-refusal" role="alert" in:rise>{error}</p>
                {/if}
            {/key}

            <button type="submit" class="btn lg block next" class:loading={submitting} disabled={submitting}>
                <span class="txt">Join the team</span>
                <i class="ri-arrow-right-line" aria-hidden="true"></i>
            </button>
        </form>
    {/if}
</AuthShell>
