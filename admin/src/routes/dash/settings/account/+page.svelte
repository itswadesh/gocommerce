<script>
    /**
     * Your own account.
     *
     * This screen exists so that changing your own password is not gated behind
     * team.write — which is also the right to change everybody's role. Put
     * it there and a staff member who suspects their password is known has to
     * ask an owner to choose a new one for them, which is the exact practice
     * invitations exist to end.
     *
     * The current password is asked for either way: a session left open on an
     * unlocked laptop should not be enough to take the account over. The same
     * goes for adding a phone to sign in with. A person who signed up with a
     * phone has no password to ask for, so what proves a change for them is a
     * code sent to the new address (D195).
     */
    import { auth, getRecord } from "$lib/api.js";
    import { rightLabel, rightsSummary } from "$lib/rights.js";
    import { formatDate } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import CodeBoxes from "$lib/components/CodeBoxes.svelte";
    import Confirm from "$lib/components/Confirm.svelte";
    import SaveBar from "$lib/components/SaveBar.svelte";
    import { titled } from "$lib/brand.svelte.js";

    let loading = $state(true);
    let me = $state(null);
    let sessions = $state(0);
    let newestSession = $state(null);
    /* A static admin token has no account to show. Saying so beats an empty
       form that cannot be submitted. */
    let tokenOnly = $state(false);

    let form = $state({ current_password: "", email: "", password: "", confirm: "" });
    let saving = $state(false);
    let errors = $state({});

    let confirmOpen = $state(false);

    // Adding a phone: the number, then the code texted to it. Offered only
    // where a code can be sent (auth-state's phone_login).
    let hasPassword = $state(true);
    let canPhone = $state(false);
    let phoneForm = $state({ number: "+91 ", code: "", password: "" });
    let phoneStep = $state("idle");
    let phoneBusy = $state(false);
    let phoneError = $state("");
    let removeOpen = $state(false);
    // Somebody with no password puts an email on their account the same way.
    let canEmail = $state(false);
    let emailForm = $state({ address: "", code: "" });
    let emailStep = $state("idle");
    let emailBusy = $state(false);
    let emailError = $state("");

    /*
     * Typed and unsaved. A password half-entered here is not a big loss on its
     * own, but a new email address typed into a form that then vanishes because
     * a link was clicked is — and the guard costs nothing to state.
     *
     * The email compares against the loaded account rather than against empty:
     * the field is seeded from `me.email` on load, so "not blank" would call a
     * freshly loaded form dirty.
     */
    const typed = $derived(
        !!form.current_password ||
            !!form.password ||
            !!form.confirm ||
            (!!me && (form.email ?? "").trim() !== (me.email ?? "")),
    );

    $effect(() => {
        load();
    });

    async function load() {
        loading = true;
        try {
            const result = await auth.me();
            me = result.superuser;
            sessions = result.sessions ?? 0;
            newestSession = result.newest_session ?? null;
            form.email = me.email;
            hasPassword = result.has_password !== false;
            const state = await auth.state();
            canPhone = !!state.phone_login;
            canEmail = !!state.password_reset;
        } catch (err) {
            if (err.status === 403) {
                tokenOnly = true;
            } else {
                toast.error(err);
            }
        } finally {
            loading = false;
        }
    }

    function discard() {
        form = { current_password: "", email: me?.email ?? "", password: "", confirm: "" };
        errors = {};
    }

    async function save(event) {
        event?.preventDefault();
        if (saving) return;

        errors = {};
        if (!form.current_password) {
            errors.current = "Confirm your current password to make a change.";
        }
        if (form.password && form.password.length < 8) {
            errors.password = "A password must be at least 8 characters.";
        }
        if (form.password && form.password !== form.confirm) {
            errors.password = "The two passwords do not match.";
        }
        const emailChanged = form.email.trim() && form.email.trim() !== me.email;
        if (!emailChanged && !form.password) {
            errors.password = "Nothing to change.";
        }
        if (Object.keys(errors).length) return;

        saving = true;
        try {
            const body = { current_password: form.current_password };
            if (emailChanged) body.email = form.email.trim();
            if (form.password) body.password = form.password;

            await auth.updateMe(body);
            // The header shows the email, so the stored record has to catch up.
            await auth.refresh();
            toast.success(
                form.password
                    ? "Password changed — your other devices were signed out"
                    : "Account updated",
            );
            form.current_password = "";
            form.password = "";
            form.confirm = "";
            await load();
        } catch (err) {
            errors.current = err.message;
        } finally {
            saving = false;
        }
    }

    async function phoneAct(act, done) {
        if (phoneBusy) return;
        phoneBusy = true;
        phoneError = "";
        try {
            await act();
            done();
        } catch (err) {
            phoneError = err.message;
        } finally {
            phoneBusy = false;
        }
    }

    async function submitEmail(event) {
        event.preventDefault();
        if (emailBusy) return;
        emailBusy = true;
        emailError = "";
        try {
            if (emailStep === "code") {
                await auth.setMyEmail(emailForm.address, emailForm.code);
                await auth.refresh();
                toast.success("Email saved");
                emailForm = { address: "", code: "" };
                emailStep = "idle";
                load();
            } else {
                await auth.requestMyEmailCode(emailForm.address);
                emailStep = "code";
            }
        } catch (err) {
            emailError = err.message;
        } finally {
            emailBusy = false;
        }
    }

    function askPhoneCode(event) {
        event?.preventDefault();
        phoneAct(
            () => auth.requestMyPhoneCode(phoneForm.number),
            () => (phoneStep = "code"),
        );
    }

    function savePhone(event) {
        event.preventDefault();
        phoneAct(
            async () => {
                await auth.setMyPhone(phoneForm.number, phoneForm.code, phoneForm.password);
                await auth.refresh();
            },
            () => {
                toast.success("You can now sign in with this phone");
                removeOpen = false;
                phoneForm = { number: "+91 ", code: "", password: "" };
                phoneStep = "idle";
                load();
            },
        );
    }

    function removePhone(event) {
        event.preventDefault();
        phoneAct(
            async () => {
                await auth.removeMyPhone(phoneForm.password);
                await auth.refresh();
            },
            () => {
                toast.success("Phone removed");
                phoneForm.password = "";
                removeOpen = false;
                load();
            },
        );
    }

    async function signOutEverywhere() {
        try {
            await auth.revokeMySessions();
            // Including this browser, which is what "everywhere" has to mean.
            // A reload lands on the login form because the token no longer
            // resolves — no special case needed.
            toast.info("Signed out everywhere");
            window.location.reload();
        } catch (err) {
            toast.error(err);
        }
    }
</script>

<svelte:head><title>{titled("Your account")}</title></svelte:head>

<div class="page page-account shopify-skin">

    <div class="page-content full-height tw:bg-background tw:text-foreground">
        <SaveBar dirty={typed && hasPassword} {saving} onsave={save} ondiscard={discard} />

        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Settings</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Your account</div>
            </nav>
        </header>

        {#if loading}
            <div class="block txt-center p-base"><span class="loader lg"></span></div>
        {:else if tokenOnly}
            <div class="alert info">
                <p>
                    <i class="ri-information-line" aria-hidden="true"></i>
                    You are signed in with the store's admin token. It is a credential rather
                    than a person, so there is no account here to change — sign in with an
                    email and password to manage one.
                </p>
            </div>
        {:else}
            <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Who you are</h2>
            <div class="list m-b-base">
                <div class="list-item">
                    <span class="txt-hint">Email</span>
                    <div class="flex-fill"></div>
                    <span class="txt">{me.email || "None"}</span>
                </div>
                {#if me.phone}
                    <div class="list-item">
                        <span class="txt-hint">Phone</span>
                        <div class="flex-fill"></div>
                        <span class="txt">{me.phone}</span>
                    </div>
                {/if}
                <div class="list-item">
                    <span class="txt-hint">Role</span>
                    <div class="flex-fill"></div>
                    <span class="label">{me.role}</span>
                </div>
                <div class="list-item">
                    <span class="txt-hint">Signed in on</span>
                    <div class="flex-fill"></div>
                    <span class="txt">
                        {sessions}
                        {sessions === 1 ? "device" : "devices"}
                        {#if newestSession}
                            <span class="txt-hint txt-sm">
                                · most recent {formatDate(newestSession)}
                            </span>
                        {/if}
                    </span>
                </div>
            </div>

            <details class="map-fold tw:mt-6 m-b-base">
                <summary>
                    <h2 class="map-fold-title tw:text-sm tw:m-0">What you may do</h2>
                    <span class="txt-hint txt-sm">
                        {rightsSummary(me.rights)} · {(me.rights ?? []).length}
                        {(me.rights ?? []).length === 1 ? "right" : "rights"}
                    </span>
                    <i class="ri-arrow-down-s-line map-fold-chevron" aria-hidden="true"></i>
                </summary>
                <div class="list tw:mt-3">
                    {#each me.rights ?? [] as right (right)}
                        <div class="list-item">
                            <i class="ri-check-line" aria-hidden="true"></i>
                            <span class="txt" title={right}>{rightLabel(right)}</span>
                        </div>
                    {/each}
                    {#if !(me.rights ?? []).length}
                        <div class="list-item txt-hint">
                            Nothing yet — ask an owner to give you a role.
                        </div>
                    {/if}
                </div>
            </details>

            {#if !hasPassword}
                <div class="content txt-hint m-b-base">
                    You sign in with a code texted to your phone, so you have no password to change.
                </div>
                {#if canEmail}
                    <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">{me.email ? "Change your email" : "Add an email"}</h2>
                    <form onsubmit={submitEmail} class="m-b-base">
                        <div class="field required" class:error={!!emailError}>
                            <label for="acc-add-email">Email</label>
                            <input
                                id="acc-add-email"
                                type="email"
                                required
                                autocomplete="email"
                                readonly={emailStep === "code"}
                                bind:value={emailForm.address}
                                oninput={() => (emailError = "")}
                            />
                        </div>
                        {#if emailStep === "code"}
                            <div class="field required m-t-sm" class:error={!!emailError}>
                                <label for="acc-add-email-code">Code we emailed you</label>
                                <CodeBoxes id="acc-add-email-code" bind:value={emailForm.code} oninput={() => (emailError = "")} />
                            </div>
                        {/if}
                        {#if emailError}
                            <div class="field-help error" role="alert">{emailError}</div>
                        {:else}
                            <div class="field-help">
                                Your way back in if you lose this phone. To sign in with it, use Forgot password
                                on the sign-in screen to choose a password.
                            </div>
                        {/if}
                        <button type="submit" class="btn secondary m-t-base" class:loading={emailBusy} disabled={emailBusy}>
                            <span class="txt">{emailStep === "code" ? "Save this email" : "Email me a code"}</span>
                        </button>
                    </form>
                {/if}
            {:else}
            <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Change your details</h2>
            <form onsubmit={save} class="m-b-base">
                <div class="field required" class:error={!!errors.current}>
                    <label for="acc-current">Current password</label>
                    <input
                        id="acc-current"
                        type="password"
                        autocomplete="current-password"
                        bind:value={form.current_password}
                        oninput={() => (errors = {})}
                    />
                </div>
                {#if errors.current}
                    <div class="field-help error">{errors.current}</div>
                {:else}
                    <div class="field-help">
                        Asked for either change, so that an unlocked laptop is not enough to
                        take your account over.
                    </div>
                {/if}

                {#if me?.phone}
                    <div class="field m-t-base">
                        <label for="acc-phone">Phone</label>
                        <input id="acc-phone" type="tel" value={me.phone} readonly />
                    </div>
                {/if}
                <div class="field m-t-base">
                    <label for="acc-email">Email</label>
                    <input id="acc-email" type="email" bind:value={form.email} />
                </div>

                <div class="fields m-t-sm">
                    <div class="field" class:error={!!errors.password}>
                        <label for="acc-password">New password</label>
                        <input
                            id="acc-password"
                            type="password"
                            autocomplete="new-password"
                            placeholder="Leave empty to keep it"
                            bind:value={form.password}
                        />
                    </div>
                    <div class="delimiter"></div>
                    <div class="field" class:error={!!errors.password}>
                        <label for="acc-confirm">Again</label>
                        <input
                            id="acc-confirm"
                            type="password"
                            autocomplete="new-password"
                            bind:value={form.confirm}
                        />
                    </div>
                </div>
                {#if errors.password}
                    <div class="field-help error">{errors.password}</div>
                {:else}
                    <div class="field-help">
                        At least 8 characters. Changing it signs you out of every other device
                        and keeps this one.
                    </div>
                {/if}

            </form>
            {/if}

            {#if me.phone && me.email}
                <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Sign in with your phone</h2>
                {#if removeOpen}
                    <form onsubmit={removePhone} class="m-b-base">
                        {#if hasPassword}
                            <div class="field required" class:error={!!phoneError}>
                                <label for="acc-phone-remove">Current password</label>
                                <input id="acc-phone-remove" type="password" required autocomplete="current-password" bind:value={phoneForm.password} />
                            </div>
                        {/if}
                        {#if phoneError}<div class="field-help error" role="alert">{phoneError}</div>{/if}
                        <button type="submit" class="btn secondary m-t-base" class:loading={phoneBusy} disabled={phoneBusy}>
                            <span class="txt">Remove {me.phone}</span>
                        </button>
                    </form>
                {:else}
                    <button type="button" class="btn secondary m-b-base" onclick={() => (removeOpen = true)}>
                        <span class="txt">Remove this phone</span>
                    </button>
                {/if}
            {/if}
            {#if canPhone && (!me.phone || !hasPassword)}
                <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">{me.phone ? "Change your number" : "Sign in with your phone"}</h2>
                <form onsubmit={phoneStep === "code" ? savePhone : askPhoneCode} class="m-b-base">
                    <div class="field required" class:error={!!phoneError}>
                        <label for="acc-phone">Phone number</label>
                        <input
                            id="acc-phone"
                            type="tel"
                            inputmode="tel"
                            required
                            autocomplete="tel"
                            readonly={phoneStep === "code"}
                            bind:value={phoneForm.number}
                        />
                    </div>
                    {#if phoneStep === "code"}
                        <div class="fields m-t-sm">
                            <div class="field required">
                                <label for="acc-phone-code">Code we texted you</label>
                                <CodeBoxes id="acc-phone-code" bind:value={phoneForm.code} />
                            </div>
                            {#if hasPassword}
                                <div class="delimiter"></div>
                                <div class="field required">
                                    <label for="acc-phone-password">Current password</label>
                                    <input
                                        id="acc-phone-password"
                                        type="password"
                                        required
                                        autocomplete="current-password"
                                        bind:value={phoneForm.password}
                                    />
                                </div>
                            {/if}
                        </div>
                    {/if}
                    {#if phoneError}
                        <div class="field-help error" role="alert">{phoneError}</div>
                    {:else}
                        <div class="field-help">With its country code.{hasPassword ? " Your password keeps working too." : ""}</div>
                    {/if}
                    <button type="submit" class="btn secondary m-t-base" class:loading={phoneBusy} disabled={phoneBusy}>
                        <span class="txt">{phoneStep === "code" ? (me.phone ? "Use this number" : "Add this phone") : "Text me a code"}</span>
                    </button>
                </form>
            {/if}

            <h2 class="tw:mt-6 tw:mb-3 tw:text-sm tw:font-semibold">Lost a device?</h2>
            <div class="content txt-hint m-b-sm">
                Ends every session including this one, so you will be asked to sign in again.
            </div>
            <button type="button" class="btn secondary" onclick={() => (confirmOpen = true)}>
                <i class="ri-logout-circle-line" aria-hidden="true"></i>
                <span class="txt">Sign out everywhere</span>
            </button>
        {/if}

        <footer class="page-footer tw:text-xs tw:text-muted-foreground">
            <span class="txt">
                {#if me}Signed in as {me.email || me.phone}{/if}
            </span>
        </footer>
    </div>
</div>

<Confirm
    bind:open={confirmOpen}
    title="Sign out everywhere?"
    message="Every device signed in as you will be signed out, including this one."
    confirmLabel="Sign out everywhere"
    onconfirm={signOutEverywhere}
/>
