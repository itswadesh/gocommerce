<script>
    /**
     * The code step, once somebody has typed a phone into the one box on the
     * sign-in or sign-up screen and pressed the button. It sends a code as it
     * opens, because that press is the asking: nothing reaches this form until a
     * person has pressed something, a number brought by a link included.
     *
     * The engine answers the same for every number, so this does too: on an
     * install that takes no sign-ups it cannot say a code is on its way, only
     * that one is if the number has an account.
     *
     * A refusal to send, such as a code asked for moments ago, keeps the code
     * field: the code from the last time may be sitting in their messages.
     */
    import { onMount } from "svelte";
    import { auth } from "$lib/api.js";
    import { rise } from "$lib/motion.js";
    import { sentence } from "$lib/format.js";
    import { readablePhone } from "$lib/phone-link.js";
    import CodeBoxes from "$lib/components/CodeBoxes.svelte";

    // number is in E.164. open says any number gets a code. onback goes back to
    // the box.
    let { onauthenticated, open = false, label = "Sign in", number, onback } = $props();

    const RESEND_SECONDS = 60;

    let code = $state("");
    let sent = $state(false);
    let sending = $state(true);
    let submitting = $state(false);
    let error = $state("");
    let attempt = $state(0);
    let wait = $state(0);

    const shown = $derived(readablePhone(number));

    $effect(() => {
        if (wait <= 0) return;
        const tick = setTimeout(() => (wait -= 1), 1000);
        return () => clearTimeout(tick);
    });

    function refuse(err, fallback) {
        attempt += 1;
        error = sentence(err.message) || fallback;
    }

    async function ask() {
        sending = true;
        error = "";
        try {
            await auth.requestPhoneCode(number);
            sent = true;
            code = "";
            wait = RESEND_SECONDS;
        } catch (err) {
            refuse(err, "Could not send a code. Try again.");
        } finally {
            sending = false;
        }
    }
    onMount(ask);

    async function verify(e) {
        e.preventDefault();
        if (submitting) return;
        submitting = true;
        error = "";
        try {
            onauthenticated?.(await auth.loginWithPhone(number, code));
        } catch (err) {
            code = "";
            refuse(err, "That code did not work. Try again.");
        } finally {
            submitting = false;
        }
    }
</script>

<form class="auth-form" onsubmit={verify}>
    <p class="auth-lede" role="status">
        {#if sending}
            Sending a code to {shown}…
        {:else if !sent}
            A code could not be sent to {shown}.
        {:else if open}
            We sent a code to {shown}. It works for 10 minutes.
        {:else}
            If {shown} has an account here, a code is on its way. It works for 10 minutes.
        {/if}
    </p>
    <div class="field" class:error={!!error}>
        <label for="phone_code">Code</label>
        <CodeBoxes
            id="phone_code"
            autofocus
            invalid={!!error}
            describedby={error ? "phone_refusal" : undefined}
            bind:value={code}
            oninput={() => (error = "")}
        />
    </div>
    <div class="auth-field-foot auth-phone-foot">
        <button type="button" class="auth-link auth-inline-btn" onclick={onback}>Use something else</button>
        <button
            type="button"
            class="auth-link auth-inline-btn"
            disabled={wait > 0 || sending || submitting}
            onclick={ask}
        >
            {wait > 0 ? `Send a new code in ${wait}s` : "Send a new code"}
        </button>
    </div>

    {#key attempt}
        {#if error}
            <p class="field-help error auth-refusal" id="phone_refusal" role="alert" in:rise>{error}</p>
        {/if}
    {/key}

    <button class="btn lg block next" class:loading={submitting} disabled={submitting}>
        <span class="txt">{label}</span>
        <i class="ri-arrow-right-line" aria-hidden="true"></i>
    </button>
</form>
