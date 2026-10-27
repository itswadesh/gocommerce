<script>
    /**
     * A built-in feature's switch and settings, on the screen the feature
     * lives on (D132). The feature is still a plugin row underneath — that is
     * where its switch, its settings and its secrets are kept, and what the
     * storefront reads — but it is part of the product, so it is set up beside
     * the thing itself rather than among the add-ons on the Plugins screen.
     *
     * The settings open in the same drawer the Plugins screen uses. A feature
     * with no switch (`always_on`) shows none, unless this store had it off
     * before the switch went: then the one way left is on. With no switch and
     * no settings there is nothing to say, so it draws nothing.
     *
     * Absent for an operator without plugins.read, and for a build without the
     * feature: the screen around it still works, it just has nothing to say
     * about the switch.
     */
    import { can, request } from "$lib/api.js";
    import { activateNote } from "$lib/plugin-ready.js";
    import { toast } from "$lib/toast.svelte.js";
    import { rise } from "$lib/motion.js";
    import PluginSettingsDrawer from "$lib/components/PluginSettingsDrawer.svelte";

    /** `settings` false leaves the settings to a form of the screen's own,
     *  one that knows more than the field list does (customer sign-in).
     *  @type {{ pluginKey: string, onchange?: (p: any) => void, plugin?: any, settings?: boolean }} */
    let { pluginKey, onchange, plugin = $bindable(null), settings = true } = $props();

    const readable = $derived(can("plugins.read"));
    const writable = $derived(can("plugins.write"));
    let working = $state(false);
    let editing = $state(null);

    $effect(() => {
        pluginKey;
        if (readable) load();
    });

    async function load() {
        try {
            plugin = await request("GET", `/api/admin/plugins/${pluginKey}`);
        } catch {
            // Not in this build: the screen carries on without the switch.
            plugin = null;
        }
    }

    function changed(next) {
        plugin = next;
        onchange?.(next);
    }

    async function toggle() {
        if (!plugin || working) return;
        const note = plugin.enabled ? "" : activateNote(plugin);
        if (note) {
            toast.info(note);
            editing = plugin;
            return;
        }
        working = true;
        try {
            const next = await request("PATCH", `/api/admin/plugins/${pluginKey}`, { body: { enabled: !plugin.enabled } });
            changed(next);
            toast.success(next.enabled ? `${next.title} switched on` : `${next.title} switched off`);
            if (next.enabled && !next.configured && next.fields?.length) editing = next;
        } catch (err) {
            toast.error(err);
        } finally {
            working = false;
        }
    }

    const showSwitch = $derived(!!plugin && (!plugin.always_on || !plugin.enabled));
    const state = $derived.by(() => {
        if (!plugin) return { label: "", cls: "" };
        if (!plugin.enabled) return { label: "Off", cls: "" };
        if (!plugin.configured) return { label: "Needs settings", cls: "label-warning" };
        return { label: "On", cls: "label-success" };
    });
    const id = $derived(`feature-${pluginKey}`);
</script>

{#if plugin && !(plugin.always_on && plugin.enabled && !(settings && plugin.fields?.length))}
    <section class="card feature-switch" class:is-on={plugin.enabled && plugin.configured} in:rise>
        <div class="feature-head">
            <span class="feature-icon" aria-hidden="true"><i class={plugin.icon || "ri-plug-line"}></i></span>
            <div class="feature-text">
                <h2 class="feature-title">{plugin.title}</h2>
                <p class="txt-hint txt-sm feature-desc">{plugin.description}</p>
            </div>
            {#if showSwitch}
                <div class="field feature-toggle">
                    <input
                        type="checkbox"
                        class="switch"
                        {id}
                        checked={plugin.enabled}
                        disabled={!writable || working}
                        onchange={(e) => {
                            // The box follows the engine, not the click: a refused
                            // switch must not stay drawn as flipped.
                            e.currentTarget.checked = plugin.enabled;
                            toggle();
                        }}
                    />
                    <label for={id}>{state.label}</label>
                </div>
            {:else}
                <span class="label {state.cls}">{state.label}</span>
            {/if}
        </div>
        {#if plugin.always_on && !plugin.enabled}
            <p class="field-help feature-note">
                Switched off for this store before it became part of every store. Switch it on to show it
                again; it then stays on.
            </p>
        {/if}
        {#if settings && plugin.fields?.length}
            <div class="feature-actions">
                <button type="button" class="btn sm transparent secondary" disabled={!writable} onclick={() => (editing = plugin)}>
                    <i class="ri-settings-3-line" aria-hidden="true"></i>
                    <span class="txt">Settings</span>
                </button>
                {#if !writable}<span class="txt-hint txt-sm">Changing these needs plugins.write.</span>{/if}
            </div>
        {/if}
    </section>
{/if}

<PluginSettingsDrawer
    plugin={editing}
    onclose={() => (editing = null)}
    onsaved={(next) => {
        changed(next);
        editing = null;
    }}
/>

<style>
    .feature-switch {
        margin: 0 0 var(--baseSpacing);
        transition:
            border-color var(--activeAnimationSpeed) ease-out,
            background-color var(--activeAnimationSpeed) ease-out;
    }
    .feature-switch.is-on {
        border-color: var(--successColor);
    }
    .feature-head {
        display: flex;
        align-items: flex-start;
        gap: 12px;
    }
    .feature-icon {
        display: inline-flex;
        align-items: center;
        justify-content: center;
        flex: none;
        width: 32px;
        height: 32px;
        border-radius: var(--borderRadius);
        background: var(--surfaceAlt1Color);
        font-size: 16px;
        transition: color var(--activeAnimationSpeed) ease-out;
    }
    .feature-switch.is-on .feature-icon {
        color: var(--successColor);
    }
    .feature-text {
        flex: 1 1 auto;
        min-width: 0;
    }
    .feature-title {
        margin: 0;
        font-size: 0.875rem;
        font-weight: 600;
    }
    .feature-desc {
        margin: 2px 0 0;
        line-height: 1.45;
    }
    /* .field is a full-width block everywhere else; here it is the switch
       beside the title, as wide as the switch and its word. */
    .feature-toggle {
        flex: 0 0 auto;
        width: auto;
        margin: 0;
        background: none;
        white-space: nowrap;
    }
    .feature-note {
        margin: 8px 0 0;
    }
    .feature-actions {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 8px;
        margin-top: 8px;
    }
    @media (prefers-reduced-motion: reduce) {
        .feature-switch,
        .feature-icon {
            transition: none;
        }
    }
</style>
