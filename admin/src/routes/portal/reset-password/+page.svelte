<script>
    /**
     * Choosing a new password from the emailed link. A successful reset signs
     * the buyer in (identity answers with a session), so the next screen is
     * their account rather than another form.
     */
    import { goto } from "$app/navigation";
    import { base } from "$app/paths";
    import { page } from "$app/state";
    import { toast } from "$lib/toast.svelte.js";
    import { tradeApi, adoptSession, loadMe, loadBasket, sentence } from "$lib/trade.svelte.js";

    const token = $derived(page.url.searchParams.get("token") ?? "");

    let password = $state("");
    let again = $state("");
    let busy = $state(false);
    let problem = $state("");
    let attempt = $state(0);

    async function submit(event) {
        event.preventDefault();
        if (busy) return;
        problem = "";
        if (password.length < 8) problem = "Choose a password of at least 8 characters.";
        else if (password !== again) problem = "The two passwords don't match.";
        if (problem) {
            attempt += 1;
            return;
        }
        busy = true;
        try {
            const auth = await tradeApi.post("/x/identity/password-reset/confirm", { token, password });
            adoptSession(auth);
            await loadMe();
            loadBasket().catch(() => {});
            toast.success("Password changed. You're signed in.");
            goto(`${base}/portal`);
        } catch (err) {
            attempt += 1;
            problem =
                err.status === 400 && /token/i.test(err.message)
                    ? "This link has expired or has already been used. Ask for a new one."
                    : sentence(err.message) || "We couldn't change the password.";
        } finally {
            busy = false;
        }
    }
</script>

<h2 class="portal-out-heading">Choose a new password</h2>

{#if !token}
    <div class="alert warning portal-notice" role="status">
        <p>This link is missing its code. Open the link in your email again, or ask for a new one.</p>
    </div>
    <p class="txt-center m-t-base m-b-0"><a class="portal-quiet-link" href="{base}/portal/forgot-password">Ask for a new link</a></p>
{:else}
    <form class="portal-form" onsubmit={submit} novalidate>
        <div class="field required" class:error={!!problem}>
            <label for="reset-password">New password</label>
            <input id="reset-password" type="password" autocomplete="new-password" minlength="8" required bind:value={password} />
        </div>
        <div class="field required m-t-sm" class:error={!!problem}>
            <label for="reset-again">The same again</label>
            <input id="reset-again" type="password" autocomplete="new-password" minlength="8" required bind:value={again} />
        </div>
        <div class="field-help">At least 8 characters.</div>
        {#key attempt}
            {#if problem}
                <div class="alert danger m-t-sm portal-refusal" role="alert"><p>{problem}</p></div>
            {/if}
        {/key}
        <button type="submit" class="btn lg block m-t-base portal-press" class:loading={busy} disabled={busy || !password || !again}>
            <span class="txt">Change password and sign in</span>
        </button>
    </form>
    {#if problem.startsWith("This link")}
        <p class="txt-center m-t-sm m-b-0"><a class="portal-quiet-link" href="{base}/portal/forgot-password">Ask for a new link</a></p>
    {/if}
{/if}
