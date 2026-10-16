/**
 * Whether a plugin has what it needs to be switched on.
 *
 * The engine refuses to switch a plugin on while a required setting is empty
 * (core/plugins.go), and says what to fill in. The panel asks the same
 * question first, so an Activate button that can only fail is shown disabled
 * with the reason beside it, rather than offered and refused.
 */

export const filled = (v) => v !== undefined && v !== null && String(v).trim() !== "";

/** missingSettings lists the required fields a plugin has no value for. */
export function missingSettings(plugin) {
    return (plugin.fields ?? []).filter((f) => f.required && !filled(plugin.settings?.[f.key]));
}

function hint(f) {
    if (f.kind === "url") return " (the full address, starting with https://)";
    if (f.kind === "secret") return " (from your account with the provider)";
    return "";
}

/** activateNote is what to fill in first, or "" when the plugin is ready. */
export function activateNote(plugin) {
    const missing = missingSettings(plugin);
    if (!missing.length) return "";
    const list = missing.map((f) => f.label + hint(f)).join(", ");
    return `Fill in ${list} before switching ${plugin.title} on.`;
}
