<script>
    /**
     * Setup Telegram: the owner told about every order, on a bot of their own
     * (ext/telegram-alerts).
     *
     * Laid out like Setup Email and Setup SMS — who carries the messages, then
     * what they say — with the carrying half as three steps, because unlike an
     * email provider a bot is made by the owner in Telegram itself, and the
     * one number they need from it (the chat id) is not shown anywhere. So the
     * screen walks them through it: paste the token @BotFather gave, message
     * the bot and let Find my chat pick the chat up, send a test, switch on.
     *
     * Rights are split the way the engine splits them. The token, the chat and
     * the switch are the plugin's settings (plugins.write, like every other
     * credential); finding the chat, the test, and the alerts' switches and
     * wording are the module's own (alerts.write). A manager reads alerts.read
     * and sees everything without a control that would refuse them.
     */
    import { base } from "$app/paths";
    import { can, telegram } from "$lib/api.js";
    import { hasModule, modulesKnown } from "$lib/modules.svelte.js";
    import { formatDate, relativeTime } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import EmptyState from "$lib/components/EmptyState.svelte";
    import ModuleMissing from "$lib/components/ModuleMissing.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import Select from "$lib/components/Select.svelte";
    import { titled } from "$lib/brand.svelte.js";

    /* The engine's SecretMask: what a saved secret reads as. */
    const MASK = "••••••••";
    /* @BotFather's shape, the module's tokenRE: checked here too so a paste
       of the wrong line is caught before it is saved, not at the first order. */
    const TOKEN = /^[0-9]{3,}:[A-Za-z0-9_-]{30,}$/;
    /* Display only: the module's maxAttempts. */
    const MAX_ATTEMPTS = 12;

    const missing = $derived(modulesKnown() && !hasModule("telegram-alerts"));
    const readable = $derived(can("alerts.read"));
    const writable = $derived(can("alerts.write"));
    const seesBot = $derived(can("plugins.read"));
    const setsBot = $derived(can("plugins.write"));

    let plugin = $state(null);
    let alerts = $state([]);
    let deliveries = $state([]);
    let loading = $state(true);
    let stateFilter = $state("");

    const tokenSaved = $derived(plugin?.settings?.bot_token === MASK);
    const chatID = $derived(plugin?.settings?.chat_id ?? "");
    const enabled = $derived(!!plugin?.enabled);
    const ready = $derived(tokenSaved && !!chatID);
    const status = $derived(
        enabled ? { label: "On", cls: "success" } : ready ? { label: "Off", cls: "" } : { label: "Not set up", cls: "warning" },
    );

    let latest = 0;
    async function load() {
        const mine = ++latest;
        const filter = stateFilter;
        loading = true;
        try {
            const [p, a, d] = await Promise.all([
                seesBot ? telegram.plugin() : Promise.resolve(null),
                telegram.alerts(),
                telegram.deliveries({ state: filter, limit: 10 }),
            ]);
            if (mine !== latest) return;
            plugin = p;
            alerts = a ?? [];
            deliveries = d?.data ?? [];
        } catch (err) {
            if (mine === latest) toast.error(err);
        } finally {
            if (mine === latest) loading = false;
        }
    }

    $effect(() => {
        void stateFilter;
        if (modulesKnown() && !missing && readable) load();
    });

    async function reloadDeliveries() {
        try {
            deliveries = (await telegram.deliveries({ state: stateFilter, limit: 10 }))?.data ?? [];
        } catch (err) {
            toast.error(err);
        }
    }

    // ------------------------------------------------------- 1. the bot

    let tokenDraft = $state("");
    let savingToken = $state(false);
    let tokenProblem = $state("");

    async function saveToken(e) {
        e.preventDefault();
        if (savingToken) return;
        const value = tokenDraft.trim();
        if (!TOKEN.test(value)) {
            tokenProblem =
                "That is not a bot token. @BotFather's look like 123456789:AAE… — copy the whole line it sent after /newbot.";
            return;
        }
        savingToken = true;
        tokenProblem = "";
        try {
            plugin = await telegram.savePlugin({ settings: { bot_token: value } });
            tokenDraft = "";
            toast.success("Bot token saved");
        } catch (err) {
            tokenProblem = err?.message ?? String(err);
        } finally {
            savingToken = false;
        }
    }

    // ------------------------------------------------------ 2. the chat

    let chats = $state(null); // null until asked
    let finding = $state(false);
    let chatProblem = $state("");
    let chatDraft = $state("");
    let savingChat = $state("");

    async function findChats() {
        if (finding) return;
        finding = true;
        chatProblem = "";
        try {
            chats = (await telegram.chats()) ?? [];
        } catch (err) {
            chats = null;
            chatProblem = err?.message ?? String(err);
        } finally {
            finding = false;
        }
    }

    async function useChat(id, title = "") {
        const value = String(id).trim();
        if (!value || savingChat) return;
        savingChat = value;
        chatProblem = "";
        try {
            plugin = await telegram.savePlugin({ settings: { chat_id: value } });
            chatDraft = "";
            toast.success(title ? `Alerts will go to ${title}` : "Chat saved");
        } catch (err) {
            chatProblem = err?.message ?? String(err);
        } finally {
            savingChat = "";
        }
    }

    const chatTitle = $derived(chats?.find((c) => c.id === chatID)?.title ?? "");
    const kind = (type) =>
        ({ private: "Your chat with the bot", group: "Group", supergroup: "Group", channel: "Channel" })[type] ?? type;

    // ------------------------------------------- 3. the test and the switch

    let testing = $state(false);
    let testResult = $state(null);

    async function sendTest() {
        if (testing) return;
        testing = true;
        testResult = null;
        try {
            await telegram.test();
            testResult = { ok: true, text: "Sent. It should be in the chat now; if it is not, check the bot is not muted or blocked there." };
        } catch (err) {
            testResult = { ok: false, text: err?.message ?? String(err) };
        } finally {
            testing = false;
        }
    }

    let switching = $state(false);
    async function toggleEnabled() {
        if (switching || !plugin) return;
        switching = true;
        const next = !plugin.enabled;
        plugin = { ...plugin, enabled: next };
        try {
            plugin = await telegram.savePlugin({ enabled: next });
            toast.success(next ? "Telegram alerts on" : "Telegram alerts off");
        } catch (err) {
            plugin = { ...plugin, enabled: !next };
            toast.error(err);
        } finally {
            switching = false;
        }
    }

    // ------------------------------------------------------- the alerts

    // An alert's switch waits for its save, so two flips cannot land out of
    // order, and is put back if the engine refuses.
    let toggling = $state(new Set());
    async function toggleAlert(a) {
        if (toggling.has(a.event)) return;
        toggling = new Set([...toggling, a.event]);
        const next = !a.active;
        alerts = alerts.map((x) => (x.event === a.event ? { ...x, active: next } : x));
        try {
            const updated = await telegram.updateAlert(a.event, { active: next });
            alerts = alerts.map((x) => (x.event === updated.event ? updated : x));
            toast.success(`${a.title} ${next ? "on" : "off"}`);
        } catch (err) {
            alerts = alerts.map((x) => (x.event === a.event ? { ...x, active: a.active } : x));
            toast.error(err);
        } finally {
            toggling = new Set([...toggling].filter((e) => e !== a.event));
        }
    }

    let editing = $state(null);
    let draft = $state("");
    let saving = $state(false);
    let preview = $state("");
    let previewProblem = $state("");
    let previewing = $state(false);

    function openAlert(a) {
        editing = a;
        draft = a.template;
        preview = "";
        previewProblem = "";
    }

    // The preview follows the typing, a quarter of a second behind it, and
    // only the newest answer is shown.
    let previewSeq = 0;
    $effect(() => {
        if (!editing) return;
        const event = editing.event;
        const text = draft;
        const timer = setTimeout(async () => {
            const mine = ++previewSeq;
            previewing = true;
            try {
                const out = await telegram.preview(event, text);
                if (mine !== previewSeq) return;
                preview = out?.text ?? "";
                previewProblem = out?.error ?? "";
            } catch (err) {
                if (mine === previewSeq) previewProblem = err?.message ?? String(err);
            } finally {
                if (mine === previewSeq) previewing = false;
            }
        }, 250);
        return () => clearTimeout(timer);
    });

    async function saveAlert() {
        if (!editing || saving) return;
        saving = true;
        try {
            const updated = await telegram.updateAlert(editing.event, { template: draft });
            alerts = alerts.map((x) => (x.event === updated.event ? updated : x));
            toast.success(`${editing.title} saved`);
            editing = null;
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    // Wording never saved is only put back in the box; saved wording is
    // replaced on the engine, so the phone gets the default from now on.
    async function restoreAlert() {
        if (!editing || saving) return;
        if (!editing.customized) {
            draft = editing.default_template;
            return;
        }
        saving = true;
        try {
            const updated = await telegram.updateAlert(editing.event, { template: editing.default_template });
            alerts = alerts.map((x) => (x.event === updated.event ? updated : x));
            toast.success(`${editing.title} back to its default wording`);
            editing = null;
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    // ------------------------------------------------- recent deliveries

    const titleOf = (event) => alerts.find((a) => a.event === event)?.title ?? event;
    const stateLabel = (s) => ({ pending: "Still trying", delivered: "Delivered", dead: "Gave up" })[s] ?? s;
    const stateClass = (s) => ({ delivered: "success", dead: "danger" })[s] ?? "warning";

    async function retry(d) {
        try {
            await telegram.retry(d.id);
            toast.success("Sending it again");
            await reloadDeliveries();
        } catch (err) {
            toast.error(err);
        }
    }
</script>

<svelte:head><title>{titled("Setup Telegram")}</title></svelte:head>

<div class="page page-notify-setup page-telegram shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Notifications</div>
                <h1 class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Setup Telegram</h1>
            </nav>
            {#if !missing && readable}
                <div class="inline-flex gap-sm">
                    <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                        <i class="ri-refresh-line" aria-hidden="true"></i>
                    </button>
                </div>
            {/if}
        </header>

        {#if missing}
            <ModuleMissing
                module="telegram-alerts"
                what="Telegram alerts are sent by ext/telegram-alerts, and this binary does not have it."
            />
        {:else if !readable}
            <NoAccess right="alerts.read" what="the Telegram alerts" />
        {:else}
            <div class="tg-intro">
                <h2 class="tw:text-2xl tw:font-semibold tw:tracking-tight">Telegram alerts</h2>
                {#if plugin}
                    <span class="label {status.cls}">{#if enabled}<i class="ri-check-line" aria-hidden="true"></i>{/if}{status.label}</span>
                {/if}
            </div>
            <p class="txt-hint m-b-base">
                Your phone hears about every new order, payment, cancellation and return as it happens,
                from a Telegram bot of your own. It is free, and set up once.
            </p>

            {#if seesBot}
                <div class="tg-steps m-b-base">
                    <!-- 1. The bot -->
                    <section class="card tg-step" class:done={tokenSaved}>
                        <h3 class="tg-step-title">
                            <span class="tg-step-no" aria-hidden="true">{#if tokenSaved}<i class="ri-check-line"></i>{:else}1{/if}</span>
                            Connect your bot
                        </h3>
                        <p class="txt-hint txt-sm">
                            In Telegram, open <a href="https://t.me/BotFather" target="_blank" rel="noreferrer">@BotFather</a>,
                            send <code>/newbot</code> and give the bot a name. It answers with a token — paste it here.
                            Keep it to yourself: anyone holding it can write as your bot.
                        </p>
                        {#if tokenSaved}
                            <p class="tg-state"><i class="ri-key-2-line" aria-hidden="true"></i> A token is saved. Paste a new one to replace it.</p>
                        {/if}
                        <form class="tg-row" onsubmit={saveToken}>
                            <div class="field">
                                <label for="tg-token">Bot token</label>
                                <input
                                    id="tg-token"
                                    type="password"
                                    autocomplete="off"
                                    spellcheck="false"
                                    placeholder={tokenSaved ? "Saved — paste a new one to replace it" : "123456789:AAE…"}
                                    disabled={!setsBot}
                                    bind:value={tokenDraft}
                                    aria-describedby={tokenProblem ? "tg-token-problem" : undefined}
                                />
                            </div>
                            <button type="submit" class="btn" class:loading={savingToken} disabled={!setsBot || savingToken || !tokenDraft.trim()}>
                                <span class="txt">Save token</span>
                            </button>
                        </form>
                        {#if tokenProblem}
                            <p id="tg-token-problem" class="tg-result" role="alert" transition:rise>
                                <i class="ri-error-warning-line" aria-hidden="true"></i><span>{tokenProblem}</span>
                            </p>
                        {/if}
                    </section>

                    <!-- 2. The chat -->
                    <section class="card tg-step" class:done={!!chatID}>
                        <h3 class="tg-step-title">
                            <span class="tg-step-no" aria-hidden="true">{#if chatID}<i class="ri-check-line"></i>{:else}2{/if}</span>
                            Choose the chat
                        </h3>
                        <p class="txt-hint txt-sm">
                            Open your bot in Telegram and press Start, or send it any message. For a team, add the
                            bot to a group instead. Then find the chat here.
                        </p>
                        {#if chatID}
                            <p class="tg-state">
                                <i class="ri-chat-check-line" aria-hidden="true"></i>
                                <span>Alerts go to {#if chatTitle}<strong>{chatTitle}</strong> ({chatID}){:else}chat <code>{chatID}</code>{/if}</span>
                            </p>
                        {/if}
                        <div class="tg-row">
                            <button
                                type="button"
                                class="btn secondary"
                                class:loading={finding}
                                disabled={!writable || !tokenSaved || finding}
                                title={tokenSaved ? "" : "Save the bot token first"}
                                onclick={findChats}
                            >
                                <i class="ri-search-line" aria-hidden="true"></i>
                                <span class="txt">Find my chat</span>
                            </button>
                        </div>
                        {#if chats}
                            {#if chats.length}
                                <ul class="tg-chats" transition:rise>
                                    {#each chats as c (c.id)}
                                        <li>
                                            <button
                                                type="button"
                                                class="tg-chat"
                                                class:on={c.id === chatID}
                                                aria-pressed={c.id === chatID}
                                                disabled={!setsBot || !!savingChat}
                                                onclick={() => useChat(c.id, c.title)}
                                            >
                                                <i class={c.type === "private" ? "ri-user-3-line" : "ri-group-line"} aria-hidden="true"></i>
                                                <span class="tg-chat-text">
                                                    <span class="tg-chat-title">{c.title || c.id}</span>
                                                    <span class="txt-hint txt-sm">{kind(c.type)}{#if c.username} · @{c.username}{/if}</span>
                                                </span>
                                                <span class="tg-chat-use">
                                                    {#if savingChat === c.id}
                                                        <span class="loader sm" aria-hidden="true"></span>
                                                    {:else if c.id === chatID}
                                                        <i class="ri-check-line" aria-hidden="true"></i> In use
                                                    {:else}
                                                        Use this chat
                                                    {/if}
                                                </span>
                                            </button>
                                        </li>
                                    {/each}
                                </ul>
                            {:else}
                                <p class="tg-result" role="status" transition:rise>
                                    <i class="ri-information-line" aria-hidden="true"></i>
                                    <span>Telegram has no message for the bot yet. Send it one — or press Start in its chat — then find the chat again.</span>
                                </p>
                            {/if}
                        {/if}
                        {#if chatProblem}
                            <p class="tg-result" role="alert" transition:rise>
                                <i class="ri-error-warning-line" aria-hidden="true"></i><span>{chatProblem}</span>
                            </p>
                        {/if}
                        <details class="tg-manual">
                            <summary>Type the chat ID instead</summary>
                            <form class="tg-row" onsubmit={(e) => (e.preventDefault(), useChat(chatDraft))}>
                                <div class="field">
                                    <label for="tg-chat">Chat ID</label>
                                    <input id="tg-chat" type="text" inputmode="numeric" autocomplete="off" placeholder="-1001234567890" disabled={!setsBot} bind:value={chatDraft} />
                                </div>
                                <button type="submit" class="btn secondary" class:loading={!!savingChat && savingChat === chatDraft.trim()} disabled={!setsBot || !chatDraft.trim() || !!savingChat}>
                                    <span class="txt">Save chat</span>
                                </button>
                            </form>
                        </details>
                    </section>

                    <!-- 3. Test, and switch on -->
                    <section class="card tg-step" class:done={enabled}>
                        <h3 class="tg-step-title">
                            <span class="tg-step-no" aria-hidden="true">{#if enabled}<i class="ri-check-line"></i>{:else}3{/if}</span>
                            Test, then switch on
                        </h3>
                        <p class="txt-hint txt-sm">
                            Send one message to the chat now, so you know alerts will arrive before an order depends
                            on it. Then switch the alerts on.
                        </p>
                        <div class="tg-row tg-row-split">
                            <button
                                type="button"
                                class="btn secondary"
                                class:loading={testing}
                                disabled={!writable || !ready || testing}
                                title={ready ? "" : "Save the token and choose a chat first"}
                                onclick={sendTest}
                            >
                                <i class="ri-send-plane-line" aria-hidden="true"></i>
                                <span class="txt">Send test message</span>
                            </button>
                            <div class="field tg-switch">
                                <input
                                    type="checkbox"
                                    class="switch"
                                    id="tg-enabled"
                                    checked={enabled}
                                    disabled={!setsBot || switching || (!enabled && !ready)}
                                    onchange={toggleEnabled}
                                />
                                <label for="tg-enabled">Send alerts</label>
                            </div>
                        </div>
                        {#if testResult}
                            <p class="tg-result" class:ok={testResult.ok} role="status" transition:rise>
                                <i class={testResult.ok ? "ri-check-line" : "ri-error-warning-line"} aria-hidden="true"></i>
                                <span>{testResult.text}</span>
                            </p>
                        {/if}
                    </section>
                </div>
            {/if}

            <h2 class="tw:mt-10 tw:text-2xl tw:font-semibold tw:tracking-tight">Alerts</h2>
            <p class="txt-hint m-b-base">
                One message per kind of event. Switch off what you do not want; each one's wording is yours to
                change. It is a Go text/template over the order: <code>{"{{.order_number}}"}</code> prints the
                order number, <code>{"{{if .reason}}…{{end}}"}</code> shows a line only when there is one.
            </p>

            <div class="card tg-alerts">
                {#each alerts as a (a.event)}
                    <div class="tg-alert">
                        <div class="field tg-switch">
                            <input
                                type="checkbox"
                                class="switch"
                                id="tg-alert-{a.event}"
                                checked={a.active}
                                disabled={!writable || toggling.has(a.event)}
                                onchange={() => toggleAlert(a)}
                                aria-label="Send the {a.title} alert"
                            />
                            <label for="tg-alert-{a.event}"></label>
                        </div>
                        <div class="tg-alert-text">
                            <div class="tg-alert-title">
                                <span class="txt-bold">{a.title}</span>
                                {#if a.customized}<span class="label sm">Reworded</span>{/if}
                            </div>
                            <div class="txt-hint txt-sm">{a.description}</div>
                            <div class="txt-hint txt-sm txt-code">{a.event}</div>
                        </div>
                        <button type="button" class="btn sm secondary tg-alert-edit" onclick={() => openAlert(a)}>
                            <i class="ri-edit-2-line" aria-hidden="true"></i>
                            <span class="txt">{writable ? "Wording" : "View"}</span>
                        </button>
                    </div>
                {/each}
                {#if loading && !alerts.length}
                    {#each Array(4) as _, i (i)}
                        <div class="tg-alert"><span class="skeleton-loader"></span></div>
                    {/each}
                {/if}
            </div>

            <div class="tg-recent-head">
                <h2 class="tw:mt-10 tw:text-2xl tw:font-semibold tw:tracking-tight">Recent alerts</h2>
                <div class="field tg-filter">
                    <Select
                        id="tg-state"
                        ariaLabel="Delivery state"
                        bind:value={stateFilter}
                        options={[
                            { value: "", label: "Any state" },
                            { value: "pending", label: "Still trying" },
                            { value: "dead", label: "Gave up" },
                            { value: "delivered", label: "Delivered" },
                        ]}
                    />
                </div>
            </div>
            <p class="txt-hint m-b-base">
                Whether each one reached Telegram. One that is still trying goes again on its own; one that gave up
                says why, and can be sent again once that is fixed.
            </p>

            {#if !deliveries.length && !loading}
                <EmptyState
                    icon="ri-telegram-line"
                    title={stateFilter ? "No alerts in this state" : "No alerts yet"}
                    text={stateFilter
                        ? "Choose another state to see the rest."
                        : "Each order event is listed here once the alerts are switched on."}
                />
            {:else}
                <div class="page-table-wrapper tw:rounded-xl tw:border">
                    <table class="table responsive-table">
                        <thead class="sticky">
                            <tr>
                                <th class="col-field-name-id">Alert</th>
                                <th class="col-field-type-select min-width">State</th>
                                <th class="col-field-type-text">Last answer</th>
                                <th class="col-field-type-date min-width">When</th>
                                <th class="col-field-type-text min-width"></th>
                            </tr>
                        </thead>
                        <tbody>
                            {#each deliveries as d (d.id)}
                                <tr>
                                    <td class="col-field-name-id" data-name="Alert">
                                        <div class="row-name row-name-stacked">
                                            <span class="txt-bold">{titleOf(d.event_name)}</span>
                                            {#if d.order_id}
                                                <a class="txt-sm" href="{base}/dash/orders/{d.order_id}">{d.order_number || `Order ${d.order_id}`}</a>
                                            {/if}
                                        </div>
                                    </td>
                                    <td class="col-field-type-select min-width" data-name="State">
                                        <span class="label {stateClass(d.state)}">{stateLabel(d.state)}</span>
                                        {#if d.state !== "delivered"}
                                            <span class="txt-hint txt-sm">{d.attempts} of {MAX_ATTEMPTS}</span>
                                        {/if}
                                    </td>
                                    <td class="col-field-type-text txt-hint" data-name="Last answer">
                                        <span class="tg-error">{d.last_error ?? ""}</span>
                                    </td>
                                    <td class="col-field-type-date min-width txt-hint" data-name="When" title={formatDate(d.created_at)}>
                                        {relativeTime(d.created_at)}
                                    </td>
                                    <td class="col-field-type-text min-width txt-right">
                                        {#if writable && d.state !== "delivered"}
                                            <button
                                                type="button"
                                                class="btn sm secondary"
                                                title={d.state === "dead" ? "Give it a fresh set of attempts" : "Send it now rather than waiting"}
                                                onclick={() => retry(d)}
                                            >
                                                <span class="txt">Send again</span>
                                            </button>
                                        {/if}
                                    </td>
                                </tr>
                            {/each}
                            {#if loading && !deliveries.length}
                                {#each Array(3) as _, i (i)}
                                    <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                                {/each}
                            {/if}
                        </tbody>
                    </table>
                </div>
            {/if}

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <span class="txt txt-hint">What the shoppers were sent is under Notifications.</span>
                <div class="flex-fill"></div>
            </footer>
        {/if}
    </div>
</div>

<Drawer open={!!editing} size="md" title={editing ? editing.title : "Alert"} onclose={() => (editing = null)}>
    {#if editing}
        <div class="field-help m-b-base">{editing.description}</div>
        <div class="field">
            <label for="tg-template">Message</label>
            <textarea id="tg-template" rows="7" class="tg-template" readonly={!writable} bind:value={draft}></textarea>
        </div>
        {#if editing.variables?.length}
            <div class="field-help m-t-sm">
                This alert knows:
                {#each editing.variables as v (v)}
                    <code class="tg-var">{"{{." + v + "}}"}</code>
                {/each}
            </div>
        {/if}
        <div class="tg-preview-head m-t-base">
            <span class="txt-bold">Preview</span>
            <span class="txt-hint txt-sm">with a made-up order{#if previewing} <span class="loader sm" aria-hidden="true"></span>{/if}</span>
        </div>
        {#if previewProblem}
            <p class="tg-result" role="alert" transition:rise>
                <i class="ri-error-warning-line" aria-hidden="true"></i><span>{previewProblem}</span>
            </p>
        {:else}
            <div class="tg-bubble" aria-live="polite">{preview}</div>
        {/if}
    {/if}
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (editing = null)}><span class="txt">Cancel</span></button>
        {#if writable && editing && (editing.customized || draft !== editing.default_template)}
            <button type="button" class="btn secondary" disabled={saving} onclick={restoreAlert}>
                <span class="txt">Restore default</span>
            </button>
        {/if}
        {#if writable}
            <button type="button" class="btn" class:loading={saving} disabled={saving || !draft.trim() || !!previewProblem} onclick={saveAlert}>
                <span class="txt">Save</span>
            </button>
        {/if}
    {/snippet}
</Drawer>

<style>
    .tg-intro {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 10px;
    }
    .tg-steps {
        display: grid;
        grid-template-columns: repeat(auto-fit, minmax(min(100%, 280px), 1fr));
        gap: 16px;
    }
    .tg-step {
        display: flex;
        flex-direction: column;
        gap: 8px;
        min-width: 0;
        margin: 0;
        transition: border-color 200ms ease-out;
    }
    /* The panel spaces a card that follows a card; in a grid the gap does. */
    .tg-steps > .tg-step + .tg-step {
        margin-top: 0;
    }
    /* A .field is full width, which beside a label or a row's text would
       take the whole line; a switch is only as wide as itself. */
    .tg-switch {
        width: auto;
    }
    .tg-step.done {
        border-color: var(--successColor);
    }
    .tg-step p {
        margin: 0;
    }
    .tg-step-title {
        display: flex;
        align-items: center;
        gap: 8px;
        margin: 0;
        font-size: 1rem;
        font-weight: 600;
    }
    .tg-step-no {
        display: inline-grid;
        place-items: center;
        flex: none;
        width: 24px;
        height: 24px;
        border-radius: 50%;
        background: var(--surfaceAlt2Color);
        font-size: var(--smFontSize);
        transition:
            background-color 200ms ease-out,
            color 200ms ease-out,
            transform 200ms ease-out;
    }
    .tg-step.done .tg-step-no {
        background: var(--successColor);
        color: #fff;
        transform: scale(1.06);
    }
    .tg-state {
        display: flex;
        align-items: flex-start;
        gap: 6px;
        font-size: var(--smFontSize);
        overflow-wrap: anywhere;
    }
    .tg-row {
        display: flex;
        flex-wrap: wrap;
        align-items: flex-end;
        gap: var(--smSpacing);
        margin-top: 4px;
    }
    .tg-row .field {
        flex: 1 1 200px;
        min-width: 0;
        margin: 0;
    }
    .tg-row-split {
        justify-content: space-between;
        align-items: center;
    }
    .tg-row-split .tg-switch {
        flex: none;
    }
    .tg-result {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        margin: 4px 0 0;
        padding: 10px 12px;
        border-radius: var(--borderRadius);
        background: var(--dangerAltColor, var(--surfaceAlt1Color));
        font-size: var(--smFontSize);
        overflow-wrap: anywhere;
    }
    .tg-result.ok {
        background: var(--surfaceSuccessColor, var(--surfaceAlt1Color));
    }
    .tg-result i {
        color: var(--dangerColor);
    }
    .tg-result.ok i {
        color: var(--successColor);
    }
    .tg-chats {
        display: flex;
        flex-direction: column;
        gap: 6px;
        margin: 4px 0 0;
        padding: 0;
        list-style: none;
    }
    .tg-chat {
        display: flex;
        align-items: center;
        gap: 10px;
        width: 100%;
        min-height: 44px;
        padding: 8px 12px;
        border: 1px solid var(--surfaceAlt2Color);
        border-radius: var(--borderRadius);
        background: none;
        color: inherit;
        font: inherit;
        text-align: left;
        cursor: pointer;
        transition:
            border-color 150ms ease-out,
            background-color 150ms ease-out,
            transform 150ms ease-out;
    }
    .tg-chat:hover:not(:disabled),
    .tg-chat:focus-visible {
        border-color: var(--txtHintColor);
        background: var(--surfaceAlt1Color);
    }
    .tg-chat:active:not(:disabled) {
        transform: scale(0.98);
    }
    .tg-chat.on {
        border-color: var(--successColor);
    }
    .tg-chat:disabled {
        cursor: default;
    }
    .tg-chat-text {
        display: flex;
        flex-direction: column;
        flex: 1 1 auto;
        min-width: 0;
    }
    .tg-chat-title {
        font-weight: 600;
        overflow-wrap: anywhere;
    }
    .tg-chat-use {
        flex: none;
        font-size: var(--smFontSize);
        color: var(--txtHintColor);
    }
    .tg-chat.on .tg-chat-use {
        color: var(--successColor);
    }
    .tg-manual summary {
        cursor: pointer;
        color: var(--txtHintColor);
        font-size: var(--smFontSize);
        transition: color 150ms ease-out;
    }
    .tg-manual summary:hover,
    .tg-manual summary:focus-visible {
        color: var(--txtPrimaryColor);
    }
    .tg-alerts {
        display: flex;
        flex-direction: column;
        margin: 0;
        padding: 0;
    }
    .tg-alert {
        display: flex;
        align-items: center;
        gap: 12px;
        padding: 12px 16px;
        border-bottom: 1px solid var(--surfaceAlt2Color);
    }
    .tg-alert:last-child {
        border-bottom: 0;
    }
    .tg-alert .tg-switch {
        flex: none;
        margin: 0;
    }
    .tg-alert-text {
        flex: 1 1 auto;
        min-width: 0;
    }
    .tg-alert-title {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 6px;
    }
    .tg-alert-edit {
        flex: none;
    }
    .tg-recent-head {
        display: flex;
        flex-wrap: wrap;
        align-items: flex-end;
        justify-content: space-between;
        gap: var(--smSpacing);
    }
    .tg-filter {
        flex: 0 1 200px;
        margin: 0;
    }
    .tg-error {
        overflow-wrap: anywhere;
    }
    .tg-template {
        font-family: var(--monospaceFontFamily);
        font-size: var(--smFontSize);
        line-height: 1.5;
    }
    .tg-var {
        display: inline-block;
        margin: 2px 4px 2px 0;
        padding: 1px 6px;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt2Color);
        font-size: var(--xsFontSize);
    }
    .tg-preview-head {
        display: flex;
        align-items: baseline;
        gap: 8px;
        margin-bottom: 6px;
    }
    /* A chat bubble, so the preview reads as what the phone will show. */
    .tg-bubble {
        max-width: 100%;
        min-height: 2.5em;
        padding: 10px 12px;
        border-radius: 14px 14px 14px 4px;
        background: var(--surfaceAlt1Color);
        border: 1px solid var(--surfaceAlt2Color);
        white-space: pre-wrap;
        overflow-wrap: anywhere;
        line-height: 1.45;
        transition: opacity 150ms ease-out;
    }
    @media (max-width: 600px) {
        .tg-alert {
            flex-wrap: wrap;
            padding: 12px;
        }
        .tg-alert-text {
            flex-basis: calc(100% - 60px);
        }
        .tg-alert-edit {
            margin-left: auto;
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .tg-step,
        .tg-step-no,
        .tg-chat,
        .tg-manual summary,
        .tg-bubble {
            transition: none;
        }
        .tg-step.done .tg-step-no {
            transform: none;
        }
        .tg-chat:active:not(:disabled) {
            transform: none;
        }
    }
</style>
