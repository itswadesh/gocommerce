<script>
    /**
     * Setup Email / Setup SMS: who carries the store's messages on one
     * channel, and what they say.
     *
     * Two halves, the way Litekart lays it out. The providers are the
     * channel's delivery backends as the engine reports them — the built-in
     * log that every store has, and each installed module — joined to the
     * plugin the module registered, which is where its key is typed. The
     * templates are the engine's catalogue of messages on the channel, each
     * with the wording that will actually go out: the default from code, or
     * the operator's own, restorable at any time.
     *
     * Two rights: notifications.read to see the catalogue of messages and
     * notifications.write to reword one. Activating a provider is a plugin
     * setting, so that side asks plugins.write. Pasting a delivery key and rewording
     * a confirmation are both operating the store, not selling.
     */
    import { goto } from "$app/navigation";
    import { page } from "$app/state";
    import { can, request } from "$lib/api.js";
    import { session } from "$lib/session.svelte.js";
    import { settings } from "$lib/settings.svelte.js";
    import { languageName } from "$lib/languages.js";
    import { formatDate } from "$lib/format.js";
    import { toast } from "$lib/toast.svelte.js";
    import Drawer from "$lib/components/Drawer.svelte";
    import NoAccess from "$lib/components/NoAccess.svelte";
    import PluginSettingsDrawer from "$lib/components/PluginSettingsDrawer.svelte";

    /** @type {{ channel: "email" | "sms" }} */
    let { channel } = $props();

    const WORDS = {
        email: { title: "Email", noun: "email", plural: "emails", provider: "Email provider" },
        sms: { title: "SMS", noun: "text", plural: "texts", provider: "SMS provider" },
    };
    const words = $derived(WORDS[channel]);
    const allowed = $derived(can("notifications.read"));
    const writable = $derived(can("notifications.write"));

    let loading = $state(true);
    let backends = $state([]);
    let plugins = $state([]);
    let templates = $state([]);
    let working = $state("");

    let editing = $state(null);
    let tpl = $state(null);
    let draft = $state({ subject: "", body: "" });
    let saving = $state(false);

    /*
     * Which of the store's languages the wording is for (D87). A customer is
     * written to in the language they ordered in, and one nobody has worded
     * gets the main language's wording — so the main language comes first,
     * and the picker only appears once there is a second.
     */
    const languages = $derived(settings.languages ?? []);
    const main = $derived(languages[0] ?? "");
    let picked = $state("");
    const language = $derived(languages.includes(picked) ? picked : main);
    const short = (tag) => languageName(tag).split(" · ")[0];
    const languageQuery = $derived(language ? "language=" + encodeURIComponent(language) : "");

    $effect(() => {
        channel;
        if (allowed) load();
    });
    // Another language changes the wording only, not who carries it.
    let shownLanguage = "";
    $effect(() => {
        languageQuery;
        if (allowed && shownLanguage && shownLanguage !== language) loadTemplates();
    });

    const templatesPath = () =>
        "/api/admin/notifications/templates?channel=" + channel + (languageQuery ? "&" + languageQuery : "");

    // Only the latest request is shown, so a slow answer for the language just
    // left cannot replace the one picked since.
    let latest = 0;

    async function load() {
        const mine = ++latest;
        loading = true;
        try {
            const [current, all, list] = await Promise.all([
                request("GET", "/api/admin/settings"),
                request("GET", "/api/admin/plugins"),
                request("GET", templatesPath()),
            ]);
            if (mine !== latest) return;
            backends = current.notifier_channels?.find((c) => c.channel === channel)?.backends ?? [];
            plugins = all ?? [];
            templates = list ?? [];
            shownLanguage = language;
            openLinkedTemplate();
        } catch (err) {
            if (mine === latest) toast.error(err);
        } finally {
            if (mine === latest) loading = false;
        }
    }

    async function loadTemplates() {
        const mine = ++latest;
        const wanted = language;
        loading = true;
        try {
            const list = await request("GET", templatesPath());
            if (mine !== latest) return;
            templates = list ?? [];
            shownLanguage = wanted;
        } catch (err) {
            if (mine === latest) toast.error(err);
        } finally {
            if (mine === latest) loading = false;
        }
    }

    /*
     * A provider is a backend with, where the module registered one, the
     * plugin that configures it. The built-in log has none; a module wired
     * entirely in code has none either, and says so.
     */
    const providers = $derived(
        backends.map((b) => {
            const plugin = b.module === "core" ? null : plugins.find((p) => p.module === b.module);
            let state;
            if (b.name === "log") state = { label: "Fallback", cls: "" };
            else if (b.delivers) state = { label: "Active", cls: "label-success" };
            else if (plugin?.enabled) state = { label: "Needs settings", cls: "label-warning" };
            else state = { label: "Inactive", cls: "" };
            return { ...b, plugin, state };
        }),
    );

    async function toggle(provider) {
        const p = provider.plugin;
        if (!p) return;
        working = p.key;
        try {
            const updated = await request("PATCH", `/api/admin/plugins/${p.key}`, { body: { enabled: !p.enabled } });
            plugins = plugins.map((x) => (x.key === updated.key ? updated : x));
            toast.success(updated.enabled ? `${p.title} activated` : `${p.title} deactivated`);
            if (updated.enabled && !updated.configured) editing = updated;
            await load();
        } catch (err) {
            toast.error(err);
        } finally {
            working = "";
        }
    }

    async function saved(updated) {
        plugins = plugins.map((x) => (x.key === updated.key ? updated : x));
        editing = null;
        await load();
    }

    function openTemplate(t) {
        tpl = t;
        draft = { subject: t.subject ?? "", body: t.body ?? "" };
    }

    /*
     * `?template=<event>` opens that message's editor, so a screen that sends
     * one — the recovery automation's "Edit wording" — can link straight to
     * its words rather than to a list of thirty. The parameter is taken off
     * once it has done its job: it is an instruction, not state, and leaving
     * it would reopen the editor on every refresh after the operator closed it.
     */
    function openLinkedTemplate() {
        const wanted = page.url.searchParams.get("template");
        if (!wanted) return;
        const t = templates.find((x) => x.event === wanted);
        if (t) openTemplate(t);
        const url = new URL(page.url);
        url.searchParams.delete("template");
        goto(url, { replaceState: true, keepFocus: true, noScroll: true });
    }

    async function saveTemplate() {
        if (!tpl) return;
        saving = true;
        try {
            // The language the wording was opened in, which is the one it is for.
            const updated = await request("PUT", `/api/admin/notifications/templates/${channel}/${tpl.event}?language=${encodeURIComponent(tpl.language)}`, {
                body: { subject: draft.subject, body: draft.body },
            });
            if (updated.language === language) templates = templates.map((t) => (t.event === updated.event ? updated : t));
            toast.success(`${tpl.title} saved`);
            tpl = null;
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    async function resetTemplate() {
        if (!tpl) return;
        saving = true;
        try {
            const restored = await request("DELETE", `/api/admin/notifications/templates/${channel}/${tpl.event}?language=${encodeURIComponent(tpl.language)}`);
            if (restored.language === language) templates = templates.map((t) => (t.event === restored.event ? restored : t));
            toast.success(
                restored.source === "main"
                    ? `${tpl.title} in ${short(restored.language)} uses the ${short(main)} wording again`
                    : `${tpl.title} back to its default`,
            );
            tpl = null;
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    const customizedCount = $derived(templates.filter((t) => t.customized).length);

    /*
     * Send a test email: the one way to know the provider really delivers,
     * short of placing an order. To the operator's own address unless they
     * type another. The answer stays on the page — a provider's refusal
     * ("domain not verified") is the thing to read, and a toast is gone
     * before it is read.
     */
    const delivering = $derived(providers.some((p) => p.name !== "log" && p.delivers));
    let testTo = $state(session.record?.email ?? "");
    let testing = $state(false);
    let testResult = $state(null);

    async function sendTest(e) {
        e.preventDefault();
        if (testing) return;
        testing = true;
        testResult = null;
        try {
            await request("POST", "/api/admin/notifications/test-email", { body: { to: testTo } });
            testResult = { ok: true, text: `Sent to ${testTo.trim()}. It should arrive within a minute; check spam if it doesn't.` };
        } catch (err) {
            testResult = { ok: false, text: err?.message ?? String(err) };
        } finally {
            testing = false;
        }
    }
</script>

<svelte:head><title>Setup {words.title} · GoCommerce</title></svelte:head>

<div class="page page-notify-setup shopify-skin">
    <div class="page-content tw:bg-background tw:text-foreground">
        <header class="page-header">
            <nav class="breadcrumbs">
                <div class="breadcrumb-item tw:text-sm tw:text-muted-foreground">Notifications</div>
                <div class="breadcrumb-item tw:text-2xl tw:font-semibold tw:tracking-tight">Setup {words.title}</div>
            </nav>
            <div class="inline-flex gap-sm">
                <button type="button" class="btn circle transparent secondary" title="Refresh" aria-label="Refresh" disabled={loading} onclick={load}>
                    <i class="ri-refresh-line" aria-hidden="true"></i>
                </button>
            </div>
        </header>

        {#if !allowed}
            <NoAccess right="notifications.read" what="the {words.noun} setup" />
        {:else}
            <h2 class="tw:text-2xl tw:font-semibold tw:tracking-tight">{words.provider}</h2>
            <p class="txt-hint m-b-base">
                Who carries the store's {words.plural}. Activate a provider and type its key; until
                one is active every message is written to the log and nobody receives it.
            </p>

            <!-- m-b-base, not m-b-lg: there is no `lg` step in the panel's
                 margin utilities, so the class that used to be here styled
                 nothing and the templates heading sat against the cards. -->
            <div class="card-grid provider-grid m-b-base">
                {#each providers as provider (provider.module + "/" + provider.name)}
                    <section class="card provider-card">
                        <div class="flex gap-10">
                            <div class="flex-fill">
                                <div class="provider-title">{provider.plugin?.title ?? (provider.name === "log" ? "Log only" : provider.name)}</div>
                                <div class="txt-hint txt-sm">
                                    {#if provider.name === "log"}
                                        Built in — writes each message to the server log and delivers nothing.
                                    {:else if provider.plugin}
                                        Module {provider.module}
                                    {:else}
                                        Module {provider.module}, configured in code
                                    {/if}
                                </div>
                            </div>
                            <span class="label {provider.state.cls}">{provider.state.label}</span>
                        </div>
                        {#if provider.name === "log"}
                            <p class="provider-description">
                                Every message the store sends is written here first, whatever else
                                carries it. With no provider active, this is where they stop — and
                                the Notifications screen shows each one as logged.
                            </p>
                            <div class="txt-hint txt-sm m-t-sm">Always on. Nothing to set up.</div>
                        {/if}
                        {#if provider.plugin}
                            <p class="provider-description">{provider.plugin.description}</p>
                            <div class="flex gap-10 m-t-sm">
                                <button
                                    type="button"
                                    class="btn sm {provider.plugin.enabled ? 'secondary' : ''}"
                                    class:loading={working === provider.plugin.key}
                                    disabled={working === provider.plugin.key}
                                    onclick={() => toggle(provider)}
                                >
                                    <span class="txt">{provider.plugin.enabled ? "Deactivate" : "Activate"}</span>
                                </button>
                                <button type="button" class="btn sm transparent secondary" onclick={() => (editing = provider.plugin)}>
                                    <i class="ri-settings-3-line" aria-hidden="true"></i>
                                    <span class="txt">Configure</span>
                                </button>
                                {#if provider.plugin.docs}
                                    <a class="btn sm transparent secondary m-l-auto" href={provider.plugin.docs} target="_blank" rel="noreferrer">
                                        <span class="txt">Docs</span>
                                    </a>
                                {/if}
                            </div>
                        {/if}
                    </section>
                {/each}
                {#if loading && !providers.length}
                    <div class="txt-hint p-base">Loading…</div>
                {/if}
            </div>
            {#if providers.length <= 1 && !loading}
                <p class="txt-hint txt-sm m-b-base">
                    No {words.noun} module is installed in this binary. Build the store with one
                    (ext/notify-{channel === "email" ? "sendgrid" : "msg91"}) and it appears here.
                </p>
            {/if}

            {#if channel === "email" && !loading}
                <section class="card notify-test">
                    <h3 class="notify-test-title">Send a test email</h3>
                    {#if delivering}
                        <p class="txt-hint txt-sm">
                            Sends one email through the active provider now, so you know order emails will arrive.
                        </p>
                    {:else}
                        <p class="txt-hint txt-sm">
                            Activate a provider above and enter its key first. Until then nothing can be sent.
                        </p>
                    {/if}
                    <form class="notify-test-form" onsubmit={sendTest}>
                        <div class="field">
                            <label for="notify-test-to">Send to</label>
                            <input
                                id="notify-test-to"
                                type="email"
                                autocomplete="email"
                                required
                                disabled={!writable || !delivering}
                                bind:value={testTo}
                            />
                        </div>
                        <button
                            type="submit"
                            class="btn"
                            class:loading={testing}
                            disabled={!writable || !delivering || testing}
                        >
                            <i class="ri-mail-send-line" aria-hidden="true"></i>
                            <span class="txt">Send test email</span>
                        </button>
                    </form>
                    {#if testResult}
                        <p class="notify-test-result" class:ok={testResult.ok} role="status">
                            <i class={testResult.ok ? "ri-check-line" : "ri-error-warning-line"} aria-hidden="true"></i>
                            <span>{testResult.text}</span>
                        </p>
                    {/if}
                </section>
            {/if}

            <!-- A section heading that follows a block needs air above it, or
                 it reads as a caption belonging to the cards it sits under. -->
            <h2 class="tw:mt-10 tw:text-2xl tw:font-semibold tw:tracking-tight">{words.title} templates</h2>
            {#if languages.length > 1}
                <div class="notify-langs" role="group" aria-label="Language">
                    {#each languages as tag, i (tag)}
                        <button
                            type="button"
                            class="notify-lang"
                            class:on={language === tag}
                            aria-pressed={language === tag}
                            onclick={() => (picked = tag)}
                            >{languageName(tag)}{#if i === 0}<span class="txt-hint txt-sm"> · main</span>{/if}</button
                        >
                    {/each}
                </div>
            {/if}
            <p class="txt-hint m-b-base">
                {templates.length} messages{#if customizedCount}, {customizedCount} reworded{/if}. Each is a Go
                text/template over the event's data: <code>{"{{.order_number}}"}</code> prints the
                order number, <code>{"{{if .tracking}}…{{end}}"}</code> shows a line only when
                there is one.
                {#if language !== main}
                    Customers who ordered in {short(language)} get the {short(main)} wording of any
                    message not written in {short(language)} yet.
                {/if}
                {#if channel === "sms"}
                    A provider that sends pre-approved templates of its own (MSG91's DLT flows)
                    ignores this wording and takes its template ids from its settings above.
                {/if}
            </p>

            <div class="page-table-wrapper tw:rounded-xl tw:border">
                <table class="table responsive-table">
                    <thead class="sticky">
                        <tr>
                            <th class="col-field-type-number min-width">#</th>
                            <th class="col-field-name-id">Message</th>
                            {#if channel === "email"}
                                <th class="col-field-type-text">Subject</th>
                            {:else}
                                <th class="col-field-type-text">Text</th>
                            {/if}
                            <th class="col-field-type-select min-width">Status</th>
                            <th class="col-field-type-text min-width"></th>
                        </tr>
                    </thead>
                    <tbody>
                        {#each templates as t, i (t.event)}
                            <tr>
                                <td class="col-field-type-number min-width txt-hint" data-name="#">{i + 1}</td>
                                <td class="col-field-name-id" data-name="Message">
                                    <div class="row-name row-name-stacked">
                                        <span class="txt-bold">{t.title}</span>
                                        <span class="txt-hint txt-sm">{t.description}</span>
                                        <span class="txt-hint txt-sm txt-code">{t.event}</span>
                                    </div>
                                </td>
                                <td class="col-field-type-text" data-name={channel === "email" ? "Subject" : "Text"} title={t.body}>
                                    <span class="txt-ellipsis">{channel === "email" ? t.subject : t.body}</span>
                                </td>
                                <td class="col-field-type-select min-width" data-name="Status">
                                    {#if t.customized}
                                        <span class="label success" title={t.updated_at ? "Edited " + formatDate(t.updated_at) : ""}>
                                            {language === main ? "Customized" : `In ${short(language)}`}
                                        </span>
                                    {:else if t.source === "main"}
                                        <span class="label warning">{short(main)} wording</span>
                                    {:else}
                                        <span class="label">Default</span>
                                    {/if}
                                </td>
                                <td class="col-field-type-text min-width txt-right">
                                    <button type="button" class="btn sm secondary" onclick={() => openTemplate(t)}>
                                        <span class="txt">Configure</span>
                                    </button>
                                </td>
                            </tr>
                        {/each}
                        {#if loading && !templates.length}
                            {#each Array(4) as _, i (i)}
                                <tr><td colspan="5"><span class="skeleton-loader"></span></td></tr>
                            {/each}
                        {:else if !templates.length}
                            <tr><td colspan="5" class="txt-center txt-hint p-base">Nothing is sent on this channel.</td></tr>
                        {/if}
                    </tbody>
                </table>
            </div>

            <footer class="page-footer tw:text-xs tw:text-muted-foreground">
                <span class="txt txt-hint">What was sent, and whether it went, is under Notifications.</span>
                <div class="flex-fill"></div>
            </footer>
        {/if}
    </div>
</div>

<PluginSettingsDrawer plugin={editing} onclose={() => (editing = null)} onsaved={saved} />

<Drawer
    open={!!tpl}
    size="md"
    title={tpl ? (languages.length > 1 ? `${tpl.title} · ${short(language)}` : tpl.title) : "Message"}
    onclose={() => (tpl = null)}
>
    {#if tpl}
        <div class="field-help m-b-base">{tpl.description}</div>
        {#if language !== main && !tpl.customized}
            <div class="alert info m-b-base">
                <p>
                    Not written in {short(language)} yet: these customers get the
                    {tpl.source === "main" ? `${short(main)} wording` : "default wording"} below. Write
                    theirs over it and save. Have a native speaker read it before it goes out.
                </p>
            </div>
        {/if}
        {#if channel === "email"}
            <div class="field">
                <label for="tpl-subject">Subject</label>
                <input id="tpl-subject" type="text" bind:value={draft.subject} />
            </div>
        {/if}
        <div class="field m-t-sm">
            <label for="tpl-body">{channel === "email" ? "Body" : "Text"}</label>
            <textarea id="tpl-body" rows={channel === "email" ? 12 : 4} class="tpl-body" bind:value={draft.body}></textarea>
        </div>
        {#if tpl.variables?.length}
            <div class="field-help m-t-sm">
                This message knows:
                {#each tpl.variables as v (v)}
                    <code class="tpl-var">{"{{." + v + "}}"}</code>
                {/each}
            </div>
        {/if}
        {#if tpl.customized}
            <div class="field-help m-t-sm">
                Reworded{#if tpl.updated_at} {formatDate(tpl.updated_at)}{/if}. Restoring the default forgets this
                wording.
            </div>
        {/if}
    {/if}
    {#snippet footer()}
        <button type="button" class="btn transparent m-r-auto" onclick={() => (tpl = null)}><span class="txt">Cancel</span></button>
        {#if tpl?.customized}
            <button type="button" class="btn secondary" disabled={saving || !writable} onclick={resetTemplate}>
                <span class="txt">{language === main ? "Restore default" : `Remove the ${short(language)} wording`}</span>
            </button>
        {/if}
        <button type="button" class="btn" class:loading={saving} disabled={saving || !writable || !draft.body.trim()} onclick={saveTemplate}>
            <span class="txt">Save</span>
        </button>
    {/snippet}
</Drawer>

<style>
    .provider-card {
        display: flex;
        flex-direction: column;
        min-width: 0;
        margin: 0;
    }
    .provider-title {
        font-weight: 600;
    }
    .provider-description {
        flex: 1 1 auto;
        margin: var(--xsSpacing) 0 0;
        color: var(--txtHintColor);
        font-size: var(--smFontSize);
        line-height: 1.45;
    }
    .notify-langs {
        display: flex;
        flex-wrap: wrap;
        gap: 2px;
        margin: 8px 0 12px;
        border-bottom: 1px solid var(--surfaceAlt2Color);
    }
    .notify-lang {
        min-height: 40px;
        padding: 8px 12px;
        border: 0;
        border-bottom: 2px solid transparent;
        background: none;
        color: var(--txtHintColor);
        font: inherit;
        font-weight: 500;
        cursor: pointer;
        transition:
            color 150ms ease-out,
            border-color 150ms ease-out;
    }
    .notify-lang:hover,
    .notify-lang:focus-visible {
        color: var(--txtPrimaryColor);
    }
    .notify-lang.on {
        border-bottom-color: var(--primaryColor);
        color: var(--txtPrimaryColor);
    }
    .tpl-body {
        font-family: var(--monospaceFontFamily);
        font-size: var(--smFontSize);
        line-height: 1.5;
    }
    .tpl-var {
        display: inline-block;
        margin: 2px 4px 2px 0;
        padding: 1px 6px;
        border-radius: var(--baseRadius);
        background: var(--surfaceAlt2Color);
        font-size: var(--xsFontSize);
    }
    .notify-test {
        margin: 0 0 var(--baseSpacing);
    }
    .notify-test-title {
        margin: 0 0 4px;
        font-size: 1rem;
        font-weight: 600;
    }
    .notify-test-form {
        display: flex;
        flex-wrap: wrap;
        align-items: flex-end;
        gap: var(--smSpacing);
        margin-top: var(--smSpacing);
    }
    .notify-test-form .field {
        flex: 1 1 260px;
        max-width: 420px;
        margin: 0;
    }
    .notify-test-result {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        margin: var(--smSpacing) 0 0;
        padding: 10px 12px;
        border-radius: var(--borderRadius);
        background: var(--dangerAltColor, var(--surfaceAlt1Color));
        overflow-wrap: anywhere;
        animation: notifyTestIn 200ms ease-out both;
    }
    .notify-test-result.ok {
        background: var(--surfaceSuccessColor, var(--surfaceAlt1Color));
    }
    .notify-test-result i {
        color: var(--dangerColor);
    }
    .notify-test-result.ok i {
        color: var(--successColor);
    }
    @keyframes notifyTestIn {
        from {
            opacity: 0;
            transform: translateY(-4px);
        }
    }
    @media (prefers-reduced-motion: reduce) {
        .notify-test-result {
            animation: none;
        }
        .notify-lang {
            transition: none;
        }
    }
</style>
