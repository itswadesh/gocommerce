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
    import { can, request } from "$lib/api.js";
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

    $effect(() => {
        channel;
        if (allowed) load();
    });

    async function load() {
        loading = true;
        try {
            const [settings, all, list] = await Promise.all([
                request("GET", "/api/admin/settings"),
                request("GET", "/api/admin/plugins"),
                request("GET", "/api/admin/notifications/templates?channel=" + channel),
            ]);
            backends = settings.notifier_channels?.find((c) => c.channel === channel)?.backends ?? [];
            plugins = all ?? [];
            templates = list ?? [];
        } catch (err) {
            toast.error(err);
        } finally {
            loading = false;
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

    async function saveTemplate() {
        if (!tpl) return;
        saving = true;
        try {
            const updated = await request("PUT", `/api/admin/notifications/templates/${channel}/${tpl.event}`, {
                body: { subject: draft.subject, body: draft.body },
            });
            templates = templates.map((t) => (t.event === updated.event ? updated : t));
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
            const restored = await request("DELETE", `/api/admin/notifications/templates/${channel}/${tpl.event}`);
            templates = templates.map((t) => (t.event === restored.event ? restored : t));
            toast.success(`${tpl.title} back to its default`);
            tpl = null;
        } catch (err) {
            toast.error(err);
        } finally {
            saving = false;
        }
    }

    const customizedCount = $derived(templates.filter((t) => t.customized).length);
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

            <!-- A section heading that follows a block needs air above it, or
                 it reads as a caption belonging to the cards it sits under. -->
            <h2 class="tw:mt-10 tw:text-2xl tw:font-semibold tw:tracking-tight">{words.title} templates</h2>
            <p class="txt-hint m-b-base">
                {templates.length} messages{#if customizedCount}, {customizedCount} reworded{/if}. Each is a Go
                text/template over the event's data: <code>{"{{.order_number}}"}</code> prints the
                order number, <code>{"{{if .tracking}}…{{end}}"}</code> shows a line only when
                there is one.
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
                                        <span class="label label-success" title={t.updated_at ? "Edited " + formatDate(t.updated_at) : ""}>Customized</span>
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

<Drawer open={!!tpl} size="md" title={tpl ? tpl.title : "Message"} onclose={() => (tpl = null)}>
    {#if tpl}
        <div class="field-help m-b-base">{tpl.description}</div>
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
            <button type="button" class="btn secondary" disabled={saving || !writable} onclick={resetTemplate}><span class="txt">Restore default</span></button>
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
</style>
