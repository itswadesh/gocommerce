<script>
    /**
     * Email and password, against ext/identity. Used where the portal asks a
     * buyer who they are: in place of any screen when nobody is signed in, and
     * on the invitation page, where the address is the invited one.
     */
    import { base } from "$app/paths";
    import { sentence, signIn } from "$lib/trade.svelte.js";

    let { email = $bindable(""), lockEmail = false, submitLabel = "Sign in", onsignedin } = $props();

    let password = $state("");
    let busy = $state(false);
    let refusal = $state("");
    /* Bumped on every refusal so the message re-mounts and shakes again: the
       same words twice in a row would otherwise look like nothing happened. */
    let attempt = $state(0);

    const uid = Math.random().toString(36).slice(2, 8);

    async function submit(event) {
        event.preventDefault();
        if (busy || !email.trim() || !password) return;
        busy = true;
        refusal = "";
        try {
            const auth = await signIn(email, password);
            password = "";
            await onsignedin?.(auth);
        } catch (err) {
            attempt += 1;
            refusal =
                err?.status === 401 || err?.status === 400
                    ? "That email and password don't match an account here."
                    : err?.status === 429
                      ? sentence(err.message)
                      : sentence(err?.message) || "We could not sign you in.";
        } finally {
            busy = false;
        }
    }
</script>

<form class="portal-form" onsubmit={submit} novalidate>
    <div class="field required" class:error={!!refusal}>
        <label for="signin-email-{uid}">Email</label>
        <input
            id="signin-email-{uid}"
            type="email"
            autocomplete="username"
            required
            readonly={lockEmail}
            bind:value={email}
        />
    </div>
    <div class="field required m-t-sm" class:error={!!refusal}>
        <label for="signin-password-{uid}">Password</label>
        <input
            id="signin-password-{uid}"
            type="password"
            autocomplete="current-password"
            required
            bind:value={password}
            aria-describedby={refusal ? `signin-refusal-${uid}` : undefined}
        />
    </div>
    {#key attempt}
        {#if refusal}
            <div class="field-help txt-danger portal-refusal" id="signin-refusal-{uid}" role="alert">{refusal}</div>
        {/if}
    {/key}
    <button
        type="submit"
        class="btn lg block next m-t-base portal-press"
        class:loading={busy}
        disabled={busy || !email.trim() || !password}
    >
        <span class="txt">{submitLabel}</span>
        <i class="ri-arrow-right-line" aria-hidden="true"></i>
    </button>
    <p class="txt-center m-t-sm m-b-0">
        <a class="portal-quiet-link" href="{base}/portal/forgot-password{email.trim() ? `?email=${encodeURIComponent(email.trim())}` : ''}"
            >Forgot your password?</a
        >
    </p>
</form>
