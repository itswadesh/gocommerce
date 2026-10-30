<script>
    /**
     * Setup WhatsApp: order updates, cash-on-delivery confirmations and basket
     * reminders from the store's own WhatsApp Business account, through Meta's
     * Cloud API (ext/notify-whatsapp, D169).
     *
     * Three parts, in the order an owner meets them. Connect: the account's
     * IDs and secrets, which are the plugin's settings (plugins.write, like
     * every credential), and the webhook address Meta's app is given. Messages:
     * which Meta-approved template each moment sends and what fills its
     * variables; only an approved template of the right category is offered,
     * because Meta delivers nothing else. Recent: what went, and the cash
     * orders a shopper asked to cancel, which is the flag an operator acts on.
     *
     * Nothing here quotes a price: Meta bills the store directly.
     */
    import { base } from "$app/paths";
    import { can, whatsapp } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import { copyText } from "$lib/clipboard.js";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";
    import { titled } from "$lib/brand.svelte.js";

    /* The engine's SecretMask: what a saved secret reads as. */
    const MASK = "••••••••";
    const SECRETS = ["access_token", "app_secret", "verify_token"];

    const missing = $derived(modulesKnown() && !hasModule("notify-whatsapp"));
    const readable = $derived(can("notifications.read"));
    const writable = $derived(can("notifications.write"));
    const seesAccount = $derived(can("plugins.read"));
    const setsAccount = $derived(can("plugins.write"));

    let plugin = $state(null);
    let status = $state(null);
    let messages = $state([]);
    let loading = $state(true);
    let onlyCancels = $state(false);

    const enabled = $derived(!!plugin?.enabled);
    const badge = $derived(
        status?.configured
            ? { label: "Connected", cls: "success" }
            : enabled
              ? { label: "Needs its IDs and token", cls: "warning" }
              : { label: "Not set up", cls: "" },
    );

    let latest = 0;
    async function load() {
        const mine = ++latest;
        loading = true;
        try {
            const [p, s, m] = await Promise.all([
                seesAccount ? whatsapp.plugin() : Promise.resolve(null),
                whatsapp.status(),
                whatsapp.messages({ limit: 20, reply: onlyCancels ? "cancel_requested" : "" }),
            ]);
            if (mine !== latest) return;
            plugin = p;
            status = s;
            messages = m ?? [];
            fillDraft();
        } catch (err) {
            if (mine === latest) toast.error(err);
        } finally {
            if (mine === latest) loading = false;
        }
    }

    $effect(() => {
        void onlyCancels;
        if (modulesKnown() && !missing && readable) load();
    });

    // ------------------------------------------------------------ 1. connect

    let draft = $state({ phone_number_id: "", waba_id: "", access_token: "", app_secret: "", verify_token: "" });
    let codCancel = $state(false);
    let saving = $state(false);

    function fillDraft() {
        const s = plugin?.settings ?? {};
        draft = {
            phone_number_id: s.phone_number_id ?? "",
            waba_id: s.waba_id ?? "",
            access_token: "",
            app_secret: "",
            verify_token: "",
        };
        codCancel = !!s.cod_cancel;
    }

    const saved = (key) => plugin?.settings?.[key] === MASK;

    async function saveAccount(e) {
        e.preventDefault();
        if (saving) return;
        saving = true;
        const settings = { phone_number_id: draft.phone_number_id.trim(), waba_id: draft.waba_id.trim(), cod_cancel: codCancel };
        for (const k of SECRETS) {
            // An empty box keeps what is saved; the engine never hands it back.
            if (draft[k].trim()) settings[k] = draft[k].trim();
        }
        try {
            plugin = await whatsapp.savePlugin({ enabled: true, settings });
            toast.success("WhatsApp connection saved");
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    let switching = $state(false);
    async function toggleEnabled(e) {
        switching = true;
        try {
            plugin = await whatsapp.savePlugin({ enabled: e.currentTarget.checked });
            status = await whatsapp.status();
        } catch (err) {
            toast.error(err);
        } finally {
            switching = false;
        }
    }

    let copied = $state(false);
    async function copyWebhook() {
        if (await copyText(status?.webhook_url ?? "")) {
            copied = true;
            setTimeout(() => (copied = false), 1600);
        } else {
            toast.error("Copy it from the box instead");
        }
    }

    // ------------------------------------------------------- 2. templates

    let templates = $state(null); // null until fetched from Meta
    let fetching = $state(false);
    let templatesProblem = $state("");
    let editing = $state(""); // the slot being chosen for
    let choice = $state({ template: "", variables: {} });
    let savingSlot = $state(false);
    let suggesting = $state("");

    async function fetchTemplates() {
        if (fetching) return;
        fetching = true;
        templatesProblem = "";
        try {
            templates = (await whatsapp.templates()) ?? [];
        } catch (err) {
            templatesProblem = err?.message ?? String(err);
        } finally {
            fetching = false;
        }
    }

    const keyOf = (t) => `${t.name}|${t.language}`;

    /** Templates a slot may send: approved, of its category, with its buttons. */
    function fitting(slot) {
        const want = slot.marketing ? "MARKETING" : "UTILITY";
        return (templates ?? []).filter(
            (t) => t.usable && t.category === want && t.quick_replies.length >= (slot.buttons?.length ?? 0),
        );
    }

    async function startEditing(slot) {
        editing = slot.key;
        const current = slot.template;
        choice = {
            template: current ? `${current.name}|${current.language}` : "",
            variables: Object.fromEntries((current?.variables ?? []).map((v) => [v.name, v.key])),
        };
        if (templates === null) await fetchTemplates();
    }

    const chosen = $derived((templates ?? []).find((t) => keyOf(t) === choice.template) ?? null);

    /* A newly chosen template's variables, filled from GoCommerce's suggestion
       when it is that template, so an approved suggestion needs no typing. */
    function pick(slot, value) {
        choice.template = value;
        const t = (templates ?? []).find((x) => keyOf(x) === value);
        const vars = {};
        if (t) {
            const hint = slot.suggested?.name === t.name ? slot.suggested.keys : [];
            t.variables.forEach((name, i) => (vars[name] = choice.variables[name] ?? hint[i] ?? ""));
        }
        choice.variables = vars;
    }

    async function saveSlot(slot) {
        if (!chosen || savingSlot) return;
        savingSlot = true;
        try {
            await whatsapp.mapSlot(slot.key, {
                name: chosen.name,
                language: chosen.language,
                variables: chosen.variables.map((name) => ({ name, key: choice.variables[name] ?? "" })),
            });
            toast.success(`${slot.label} will be sent on WhatsApp`);
            editing = "";
            status = await whatsapp.status();
        } catch (err) {
            toast.error(err);
        } finally {
            savingSlot = false;
        }
    }

    async function unmap(slot) {
        try {
            await whatsapp.unmapSlot(slot.key);
            toast.success(`${slot.label} is no longer sent on WhatsApp`);
            status = await whatsapp.status();
        } catch (err) {
            toast.error(err);
        }
    }

    async function suggest(slot) {
        if (suggesting) return;
        suggesting = slot.key;
        try {
            const r = await whatsapp.suggest(slot.key);
            toast.success(`Sent “${r.name}” to Meta for review (${(r.status || "submitted").toLowerCase()}). It can be chosen once Meta approves it.`);
        } catch (err) {
            toast.error(err);
        } finally {
            suggesting = "";
        }
    }

    const KEY_LABELS = {
        customer_first_name: "Customer's first name",
        customer_name: "Customer's name",
        order_number: "Order number",
        order_total: "Order total",
        items_summary: "Items",
        tracking: "Tracking number",
        order_tracking_url: "Order tracking link",
        store_name: "Store name",
        cart_total: "Basket total",
        summary: "Basket items",
        recovery_url: "Link back to the basket",
        discount_code: "Discount code",
    };
    const keyOptions = (slot) => slot.keys.map((k) => ({ value: k, label: KEY_LABELS[k] ?? k }));

    // ------------------------------------------------------- 3. recent

    const STATUS = {
        queued: { label: "Queued", cls: "warning" },
        sent: { label: "Sent", cls: "" },
        delivered: { label: "Delivered", cls: "success" },
        read: { label: "Read", cls: "success" },
        failed: { label: "Failed", cls: "danger" },
    };
    const REPLY = { confirmed: "Confirmed", cancel_requested: "Asked to cancel", cancelled: "Cancelled by reply" };
    const slotLabel = (key) => status?.slots?.find((s) => s.key === key)?.label ?? (key === "reply" ? "Reply" : key);
</script>

<svelte:head><title>{titled("Setup WhatsApp")}</title></svelte:head>

<div class="page page-notify-setup page-whatsapp shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Notifications</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Setup WhatsApp</h1>
            </nav>
            {#if !missing && readable}
                <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            {/if}
        </header>

        {#if missing}
            <ModuleMissing module="notify-whatsapp" what="WhatsApp messages are sent by ext/notify-whatsapp, and this binary does not have it." />
        {:else if !readable}
            <NoAccess right="notifications.read" what="the WhatsApp setup" />
        {:else}
            <div class="wa-intro">
                <h2 class="tw:text-2xl tw:font-semibold tw:tracking-tight">WhatsApp Business</h2>
                {#if status}<span class="label {badge.cls}">{badge.label}</span>{/if}
            </div>
            <p class="txt-hint m-b-base">
                Order updates, cash-on-delivery confirmations and basket reminders from your own WhatsApp Business
                account, sent only to shoppers who ticked the box at checkout. Meta bills your account directly for
                what is sent.
            </p>

            {#if seesAccount}
                <section class="card wa-card m-b-base" class:done={status?.configured}>
                    <h3 class="wa-title">1. Connect your account</h3>
                    <p class="txt-hint txt-sm">
                        From your Meta app's WhatsApp › API Setup page and Business Settings › System users. Use a
                        system user's permanent token, not the 24-hour test token.
                    </p>
                    <form class="wa-grid" onsubmit={saveAccount}>
                        <div class="field">
                            <label for="wa-phone">Phone number ID</label>
                            <input id="wa-phone" type="text" inputmode="numeric" autocomplete="off" disabled={!setsAccount} bind:value={draft.phone_number_id} />
                        </div>
                        <div class="field">
                            <label for="wa-waba">WhatsApp Business Account ID</label>
                            <input id="wa-waba" type="text" inputmode="numeric" autocomplete="off" disabled={!setsAccount} bind:value={draft.waba_id} />
                        </div>
                        {#each [["access_token", "Access token"], ["app_secret", "App secret"], ["verify_token", "Webhook verify token"]] as [key, label] (key)}
                            <div class="field">
                                <label for="wa-{key}">{label}</label>
                                <input
                                    id="wa-{key}"
                                    type="password"
                                    autocomplete="off"
                                    spellcheck="false"
                                    placeholder={saved(key) ? "Saved — type a new one to replace it" : ""}
                                    disabled={!setsAccount}
                                    bind:value={draft[key]}
                                />
                            </div>
                        {/each}
                        <div class="field wa-wide wa-check">
                            <input type="checkbox" class="switch" id="wa-cod-cancel" disabled={!setsAccount} bind:checked={codCancel} />
                            <label for="wa-cod-cancel">Cancel a cash order when the shopper taps Cancel</label>
                        </div>
                        <p class="txt-hint txt-sm wa-wide">
                            Off, the order stays as it is and is listed below as asked to cancel, for you to call the shopper.
                        </p>
                        <div class="wa-wide wa-actions">
                            <button type="submit" class="btn" class:loading={saving} disabled={!setsAccount || saving}>
                                <span class="txt">Save connection</span>
                            </button>
                            {#if plugin}
                                <div class="field wa-check">
                                    <input type="checkbox" class="switch" id="wa-enabled" checked={enabled} disabled={!setsAccount || switching} onchange={toggleEnabled} />
                                    <label for="wa-enabled">Send WhatsApp messages</label>
                                </div>
                            {/if}
                        </div>
                    </form>

                    {#if status}
                        <div class="wa-webhook">
                            <p class="txt-sm">
                                In your Meta app, under WhatsApp › Configuration, set this as the callback URL, enter the
                                same verify token as above, and subscribe to <code>messages</code>:
                            </p>
                            <div class="wa-copy">
                                <input type="text" readonly value={status.webhook_url} aria-label="Webhook callback URL" onfocus={(e) => e.currentTarget.select()} />
                                <button type="button" class="btn secondary" onclick={copyWebhook}>
                                    <i class={copied ? "ri-check-line" : "ri-file-copy-line"} aria-hidden="true"></i>
                                    <span class="txt">{copied ? "Copied" : "Copy"}</span>
                                </button>
                            </div>
                        </div>
                    {/if}
                </section>
            {/if}

            {#if status}
                <section class="card wa-card m-b-base">
                    <div class="wa-head">
                        <h3 class="wa-title">2. Choose what each moment sends</h3>
                        <button type="button" class="btn secondary sm" class:loading={fetching} disabled={!status.configured || fetching} onclick={fetchTemplates}>
                            <i class="ri-download-cloud-2-line" aria-hidden="true"></i>
                            <span class="txt">{templates ? "Reload templates" : "Load templates from Meta"}</span>
                        </button>
                    </div>
                    <p class="txt-hint txt-sm">
                        WhatsApp delivers only templates Meta approved. Order updates use a utility template, basket
                        reminders a marketing one; the cash-on-delivery confirmation needs two quick-reply buttons,
                        confirm first and cancel second.
                    </p>
                    {#if templatesProblem}
                        <p class="wa-problem" role="alert" transition:rise><i class="ri-error-warning-line" aria-hidden="true"></i><span>{templatesProblem}</span></p>
                    {/if}
                    <ul class="wa-slots">
                        {#each status.slots as slot (slot.key)}
                            <li class="wa-slot">
                                <div class="wa-slot-row">
                                    <div class="wa-slot-text">
                                        <span class="wa-slot-name">{slot.label}</span>
                                        <span class="label sm">{slot.marketing ? "Marketing" : "Utility"}</span>
                                        <span class="txt-hint txt-sm wa-slot-tpl">
                                            {#if slot.template}Sends <code>{slot.template.name}</code> ({slot.template.language}){:else}Not sent{/if}
                                        </span>
                                    </div>
                                    <div class="wa-slot-actions">
                                        {#if slot.template}
                                            <button type="button" class="btn transparent secondary sm" disabled={!writable} onclick={() => unmap(slot)}>Stop</button>
                                        {/if}
                                        <button
                                            type="button"
                                            class="btn secondary sm"
                                            disabled={!writable || !status.configured}
                                            aria-expanded={editing === slot.key}
                                            onclick={() => (editing === slot.key ? (editing = "") : startEditing(slot))}
                                        >
                                            {slot.template ? "Change" : "Choose template"}
                                        </button>
                                    </div>
                                </div>
                                {#if editing === slot.key}
                                    <div class="wa-editor" transition:rise>
                                        {#if templates === null}
                                            <p class="txt-hint txt-sm">Loading templates from Meta…</p>
                                        {:else if fitting(slot).length === 0}
                                            <p class="txt-hint txt-sm">
                                                The account has no approved {slot.marketing ? "marketing" : "utility"} template that fits.
                                                Write one in WhatsApp Manager, or send ours for review:
                                            </p>
                                        {:else}
                                            <div class="field">
                                                <label for="wa-tpl-{slot.key}">Template</label>
                                                <Select
                                                    id="wa-tpl-{slot.key}"
                                                    value={choice.template}
                                                    options={fitting(slot).map((t) => ({ value: keyOf(t), label: `${t.name} (${t.language})` }))}
                                                    placeholder="Choose an approved template"
                                                    onchange={(v) => pick(slot, v)}
                                                />
                                            </div>
                                            {#if chosen}
                                                <p class="wa-body">{chosen.body}</p>
                                                {#each chosen.variables as name (name)}
                                                    <div class="field">
                                                        <label for="wa-var-{slot.key}-{name}">Fills {"{{"}{name}{"}}"}</label>
                                                        <Select
                                                            id="wa-var-{slot.key}-{name}"
                                                            value={choice.variables[name]}
                                                            options={keyOptions(slot)}
                                                            placeholder="Choose a value"
                                                            onchange={(v) => (choice.variables = { ...choice.variables, [name]: v })}
                                                        />
                                                    </div>
                                                {/each}
                                                <button type="button" class="btn" class:loading={savingSlot} disabled={savingSlot} onclick={() => saveSlot(slot)}>
                                                    <span class="txt">Use this template</span>
                                                </button>
                                            {/if}
                                        {/if}
                                        {#if slot.suggested}
                                            <details class="wa-suggest">
                                                <summary>Our suggested wording</summary>
                                                <p class="wa-body">{slot.suggested.body}</p>
                                                {#if slot.suggested.quick_replies?.length}
                                                    <p class="txt-hint txt-sm">Buttons: {slot.suggested.quick_replies.join(", ")}</p>
                                                {/if}
                                                <button type="button" class="btn secondary sm" class:loading={suggesting === slot.key} disabled={!writable || !!suggesting} onclick={() => suggest(slot)}>
                                                    <span class="txt">Send it to Meta for review</span>
                                                </button>
                                            </details>
                                        {/if}
                                    </div>
                                {/if}
                            </li>
                        {/each}
                    </ul>
                </section>

                <section class="card wa-card">
                    <div class="wa-head">
                        <h3 class="wa-title">Recent messages</h3>
                        <div class="field wa-check">
                            <input type="checkbox" class="switch" id="wa-cancels" bind:checked={onlyCancels} />
                            <label for="wa-cancels">Only asked to cancel</label>
                        </div>
                    </div>
                    <p class="txt-hint txt-sm">
                        Opted in to order updates: {status.consents?.order_updates ?? 0} · to reminders and offers:
                        {status.consents?.marketing ?? 0} · replied STOP: {status.consents?.opted_out ?? 0}
                    </p>
                    {#if messages.length === 0}
                        <p class="txt-hint txt-sm">{onlyCancels ? "No shopper has asked to cancel." : "Nothing sent yet."}</p>
                    {:else}
                        <ul class="wa-messages">
                            {#each messages as m (m.id)}
                                <li class="wa-message">
                                    <span class="wa-msg-what">
                                        {slotLabel(m.slot)}
                                        {#if m.order_id}
                                            · <a href="{base}/dash/orders/{m.order_id}">{m.order_number}</a>
                                        {/if}
                                    </span>
                                    <span class="txt-hint txt-sm">{m.phone} · {relativeTime(m.created_at)}</span>
                                    <span class="wa-msg-tags">
                                        <span class="label sm {STATUS[m.status]?.cls ?? ''}">{STATUS[m.status]?.label ?? m.status}</span>
                                        {#if m.reply}<span class="label sm {m.reply === 'confirmed' ? 'success' : 'warning'}">{REPLY[m.reply] ?? m.reply}</span>{/if}
                                    </span>
                                    {#if m.error}<span class="txt-hint txt-sm wa-error">{m.error}</span>{/if}
                                </li>
                            {/each}
                        </ul>
                    {/if}
                </section>
            {/if}
        {/if}
    </div>
</div>

<style>
    .wa-intro {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 10px;
    }
    .wa-card {
        display: flex;
        flex-direction: column;
        gap: 10px;
        min-width: 0;
        transition: border-color 200ms ease-out;
    }
    .wa-card.done {
        border-color: var(--successColor);
    }
    .wa-card p {
        margin: 0;
    }
    .wa-title {
        margin: 0;
        font-size: 1rem;
        font-weight: 600;
    }
    .wa-head {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: var(--smSpacing);
    }
    .wa-grid {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(min(100%, 260px), 1fr));
        gap: 12px 16px;
    }
    .wa-grid .field {
        margin: 0;
        min-width: 0;
    }
    .wa-wide {
        grid-column: 1 / -1;
    }
    .wa-check {
        width: auto;
    }
    .wa-actions {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: var(--smSpacing);
    }
    .wa-webhook {
        display: flex;
        flex-direction: column;
        gap: 8px;
        padding-top: 10px;
        border-top: 1px solid var(--surfaceAlt2Color);
    }
    .wa-copy {
        display: flex;
        gap: var(--smSpacing);
        min-width: 0;
    }
    .wa-copy input {
        flex: 1 1 auto;
        min-width: 0;
        font-family: var(--monospaceFontFamily);
        font-size: var(--smFontSize);
    }
    .wa-copy .btn i {
        transition: transform 200ms ease-out;
    }
    .wa-copy .btn:active i {
        transform: scale(0.85);
    }
    .wa-problem {
        display: flex;
        gap: 8px;
        padding: 10px 12px;
        border-radius: var(--borderRadius);
        background: var(--dangerAltColor, var(--surfaceAlt1Color));
        font-size: var(--smFontSize);
        overflow-wrap: anywhere;
    }
    .wa-problem i {
        color: var(--dangerColor);
    }
    .wa-slots,
    .wa-messages {
        display: flex;
        flex-direction: column;
        margin: 0;
        padding: 0;
        list-style: none;
    }
    .wa-slot,
    .wa-message {
        padding: 12px 0;
        border-bottom: 1px solid var(--surfaceAlt2Color);
        transition: background-color 150ms ease-out;
    }
    .wa-slot:last-child,
    .wa-message:last-child {
        border-bottom: 0;
    }
    .wa-slot-row {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
    }
    .wa-slot-text {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 6px;
        min-width: 0;
    }
    .wa-slot-name {
        font-weight: 600;
    }
    .wa-slot-tpl {
        flex-basis: 100%;
        overflow-wrap: anywhere;
    }
    .wa-slot-actions {
        display: flex;
        gap: 6px;
    }
    .wa-slot-actions .btn {
        min-height: 44px;
    }
    .wa-editor {
        display: flex;
        flex-direction: column;
        gap: 10px;
        margin-top: 10px;
        padding: 12px;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color);
    }
    .wa-editor .field {
        margin: 0;
    }
    .wa-body {
        white-space: pre-wrap;
        overflow-wrap: anywhere;
        font-size: var(--smFontSize);
        padding: 8px 10px;
        border-left: 3px solid var(--successColor);
        background: var(--surfaceColor);
    }
    .wa-suggest summary {
        cursor: pointer;
        color: var(--txtHintColor);
        font-size: var(--smFontSize);
        min-height: 44px;
        display: flex;
        align-items: center;
        transition: color 150ms ease-out;
    }
    .wa-suggest summary:hover,
    .wa-suggest summary:focus-visible {
        color: var(--txtPrimaryColor);
    }
    .wa-suggest[open] {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .wa-message {
        display: flex;
        flex-direction: column;
        gap: 2px;
    }
    .wa-msg-tags {
        display: flex;
        flex-wrap: wrap;
        gap: 4px;
    }
    .wa-error {
        overflow-wrap: anywhere;
    }
    @media (prefers-reduced-motion: reduce) {
        .wa-card,
        .wa-slot,
        .wa-message,
        .wa-copy .btn i,
        .wa-suggest summary {
            transition: none;
        }
        .wa-copy .btn:active i {
            transform: none;
        }
    }
</style>
