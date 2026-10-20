/**
 * One card on Payment methods or Shipping providers: the plugin that switches
 * the provider, and what its label says.
 *
 * A module's provider is switched by its module's plugin. A built-in has no
 * module to look one up by, so when it has a switch of its own — cash on
 * delivery (D102) — the engine names the plugin, and a built-in without one
 * (manual fulfilment) has nothing to switch.
 *
 * Green is for "on and working" alone; a built-in method is simply there, so
 * it reads neutral, and one switched off says so.
 *
 * @param {{ module: string, configured: boolean, plugin?: string }} provider from the settings
 * @param {Array<{ key: string, module: string, enabled: boolean }>} plugins from the plugin list
 */
export function providerCard(provider, plugins) {
    let plugin = null;
    if (provider.plugin) plugin = plugins.find((x) => x.key === provider.plugin) ?? null;
    else if (provider.module !== "core") plugin = plugins.find((x) => x.module === provider.module) ?? null;

    let state;
    if (provider.module === "core") state = plugin && !plugin.enabled ? { label: "Off", cls: "" } : { label: "Built in", cls: "" };
    else if (provider.configured) state = { label: "Active", cls: "label-success" };
    else if (plugin?.enabled) state = { label: "Needs settings", cls: "label-warning" };
    else state = { label: "Inactive", cls: "" };
    return { plugin, state };
}
