<script>
    /**
     * How the store's customers sign in on the storefront (D116): one way,
     * chosen here. It is the customer-accounts module's plugin setting, so the
     * card reads and writes that plugin, and is absent when the module is not
     * installed. The engine refuses the other way, so this is not only what
     * the storefront draws.
     *
     * Phone sign-in sends its codes by SMS, so neither phone choice can be made
     * until an SMS provider delivers: choosing one without it would lock those
     * customers out. "Email or phone" lets an account carry both, each added
     * from the account once it is proved (D118).
     */
    import { base } from "$app/paths";
    import { api, can } from "$lib/api.js";
    import { toast } from "$lib/toast.svelte.js";

    let { channels = [] } = $props();

    const KEY = "identity";
    const editable = $derived(can("plugins.write"));
    const smsReady = $derived(channels.some((c) => c.channel === "sms" && c.delivers));

    let plugin = $state(null);
    let method = $state("email_password");
    let saving = $state(false);

    const METHODS = ["email_password", "phone_otp", "email_or_phone"];
    const known = (v) => (METHODS.includes(v) ? v : "email_password");
    const saved = $derived(known(plugin?.settings?.login_method));
    const usesPhone = (v) => v === "phone_otp" || v === "email_or_phone";

    $effect(() => {
        load();
    });

    async function load() {
        try {
            plugin = await api.get(`/api/admin/plugins/${KEY}`);
            method = known(plugin?.settings?.login_method);
        } catch {
            // Not installed, or no right to read plugins: the card stays away.
            plugin = null;
        }
    }

    async function save() {
        if (saving || method === saved) return;
        saving = true;
        try {
            plugin = await api.patch(`/api/admin/plugins/${KEY}`, { settings: { login_method: method } });
            toast.success(
                method === "phone_otp"
                    ? "Customers now sign in with their phone"
                    : method === "email_or_phone"
                      ? "Customers now sign in with email or phone"
                      : "Customers now sign in with email and password",
            );
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }
</script>

{#if plugin}
    <section class="card">
        <h2 class="tw:mb-1 tw:text-sm tw:font-semibold">Customer sign-in</h2>
        <p class="txt-hint txt-sm tw:mb-2">
            How shoppers sign in to their account on the storefront. Only the way chosen here works.
        </p>
        {#if !plugin.enabled}
            <div class="field-help">
                Customer accounts are switched off under
                <a href="{base}/dash/plugins">Plugins</a>, so everybody checks out as a guest.
            </div>
        {:else}
            <form
                onsubmit={(e) => {
                    e.preventDefault();
                    save();
                }}
            >
                <fieldset class="key-kind" disabled={!editable || saving}>
                    <legend class="key-kind-legend">Customers sign in with</legend>
                    <div class="key-kind-options">
                        <label class="key-kind-option" class:is-on={method === "email_password"}>
                            <input type="radio" name="login-method" value="email_password" bind:group={method} />
                            <i class="ri-mail-line key-kind-icon" aria-hidden="true"></i>
                            <span class="key-kind-text">
                                <span class="key-kind-title">Email and password</span>
                                <span class="txt-hint txt-sm">
                                    They create an account with their email address and choose a password.
                                </span>
                            </span>
                        </label>
                        <label class="key-kind-option" class:is-on={method === "phone_otp"}>
                            <input
                                type="radio"
                                name="login-method"
                                value="phone_otp"
                                bind:group={method}
                                disabled={!smsReady && saved !== "phone_otp"}
                            />
                            <i class="ri-smartphone-line key-kind-icon" aria-hidden="true"></i>
                            <span class="key-kind-text">
                                <span class="key-kind-title">Phone and SMS code</span>
                                <span class="txt-hint txt-sm">
                                    They enter their phone number and the 4-digit code we text them. No password.
                                </span>
                            </span>
                        </label>
                        <label class="key-kind-option" class:is-on={method === "email_or_phone"}>
                            <input
                                type="radio"
                                name="login-method"
                                value="email_or_phone"
                                bind:group={method}
                                disabled={!smsReady && saved !== "email_or_phone"}
                            />
                            <i class="ri-user-shared-line key-kind-icon" aria-hidden="true"></i>
                            <span class="key-kind-text">
                                <span class="key-kind-title">Email or phone</span>
                                <span class="txt-hint txt-sm">
                                    Either one. A customer can add the other to their account once they prove it,
                                    and then both open the same account.
                                </span>
                            </span>
                        </label>
                    </div>
                </fieldset>
                {#if !smsReady}
                    <div class="field-help tw:mb-2">
                        {#if usesPhone(saved)}
                            <b>No SMS provider is sending, so customers cannot sign in right now.</b>
                        {:else}
                            Phone sign-in needs an SMS provider first.
                        {/if}
                        Set one up under <a href="{base}/dash/notifications/sms">Notifications › SMS</a>.
                    </div>
                {/if}
                {#if editable}
                    <button type="submit" class="btn sm" disabled={saving || method === saved}>
                        <span class="txt">{saving ? "Saving…" : "Save sign-in"}</span>
                    </button>
                {:else}
                    <div class="field-help">Changing this needs plugins.write.</div>
                {/if}
            </form>
        {/if}
    </section>
{/if}

<style>
    /* A choice that cannot be made yet says so before it is pressed. */
    .key-kind-option:has(input:disabled) {
        opacity: 0.6;
        cursor: not-allowed;
    }
</style>
