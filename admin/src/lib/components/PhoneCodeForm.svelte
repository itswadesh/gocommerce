<script>
    /**
     * A phone and the code texted to it, for the sign-in and sign-up screens.
     * Each screen shows it only where the install says a code can be sent
     * (auth-state's phone_login and phone_signup), so it never leads nowhere.
     *
     * The engine answers the same for every number, so this does too: on an
     * install that takes no sign-ups it cannot say a code is on its way, only
     * that one is if the number has an account.
     */
    import { auth } from "$lib/api.js";
    import { rise } from "$lib/motion.js";
    import { sentence } from "$lib/format.js";
    import { readablePhone } from "$lib/phone-link.js";
    import { toIdentity } from "$lib/identity.js";

    // onauthenticated gets the record. open says any number gets a code.
    // number is one a link brought (phone-link.js); the person still presses Send.
    let { onauthenticated, open = false, label = "Sign in", number = "" } = $props();

    const RESEND_SECONDS = 60;

    // India's code to start from: most people here are in India, and the
    // engine needs one. Anybody else types over it.
    let phone = $state(number ? readablePhone(number) : "+91 ");
    let code = $state("");
    let step = $state("phone");
    let submitting = $state(false);
    let error = $state("");
    let attempt = $state(0);
    let wait = $state(0);

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
        submitting = true;
        error = "";
        try {
            await auth.requestPhoneCode(toIdentity(phone));
            step = "code";
            code = "";
            wait = RESEND_SECONDS;
        } catch (err) {
            refuse(err, "Could not send a code. Try again.");
        } finally {
            submitting = false;
        }
    }

    async function verify() {
        submitting = true;
        error = "";
        try {
            onauthenticated?.(await auth.loginWithPhone(toIdentity(phone), code));
        } catch (err) {
            code = "";
            refuse(err, "That code did not work. Try again.");
        } finally {
            submitting = false;
        }
    }

    function submit(e) {
        e.preventDefault();
        if (!submitting) step === "phone" ? ask() : verify();
    }
</script>

<form class="auth-form" onsubmit={submit}>
    {#if step === "phone"}
        <div class="field" class:error={!!error}>
            <label for="phone_number">Phone number</label>
            <!-- svelte-ignore a11y_autofocus -->
            <input
                id="phone_number"
                type="tel"
                inputmode="tel"
                required
                autofocus={!number}
                autocomplete="tel"
                enterkeyhint="go"
                aria-invalid={!!error}
                aria-describedby={error ? "phone_refusal phone_help" : "phone_help"}
                bind:value={phone}
                oninput={() => (error = "")}
            />
        </div>
        <div class="field-help" id="phone_help">With its country code. We'll text you a code.</div>
    {:else}
        <p class="auth-lede" role="status" in:rise>
            {#if open}
                We sent a code to {phone.trim()}. It works for 10 minutes.
            {:else}
                If {phone.trim()} has an account here, a code is on its way. It works for 10 minutes.
            {/if}
        </p>
        <div class="field" class:error={!!error}>
            <label for="phone_code">Code</label>
            <!-- svelte-ignore a11y_autofocus -->
            <input
                id="phone_code"
                type="text"
                inputmode="numeric"
                pattern="[0-9]*"
                maxlength="6"
                minlength="6"
                required
                autofocus
                autocomplete="one-time-code"
                enterkeyhint="go"
                aria-invalid={!!error}
                aria-describedby={error ? "phone_refusal" : undefined}
                bind:value={code}
                oninput={() => (error = "")}
            />
        </div>
        <div class="auth-field-foot auth-phone-foot">
            <button type="button" class="auth-link auth-inline-btn" onclick={() => { step = "phone"; error = ""; }}>
                Change number
            </button>
            <button
                type="button"
                class="auth-link auth-inline-btn"
                disabled={wait > 0 || submitting}
                onclick={ask}
            >
                {wait > 0 ? `Send a new code in ${wait}s` : "Send a new code"}
            </button>
        </div>
    {/if}

    {#key attempt}
        {#if error}
            <p class="field-help error auth-refusal" id="phone_refusal" role="alert" in:rise>{error}</p>
        {/if}
    {/key}

    <button class="btn lg block next" class:loading={submitting} disabled={submitting}>
        <span class="txt">{step === "phone" ? "Send code" : label}</span>
        <i class="ri-arrow-right-line" aria-hidden="true"></i>
    </button>
</form>
