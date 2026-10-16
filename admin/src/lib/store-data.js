/**
 * Everything the panel caches that belongs to one store, dropped and asked
 * for again. Called whenever the tab's store changes — at sign-in, from
 * /select-store and from the sidebar switcher — so no screen draws Store B
 * with Store A's currency, modules or plugin screens.
 */
import { clearProfile, clearSettings, loadProfile, loadSettings } from "$lib/settings.svelte.js";
import { forgetModules, loadModules } from "$lib/modules.svelte.js";
import { forgetScreens, loadScreens } from "$lib/screens.svelte.js";

/** reloadStoreData resolves once the module list is known for the new store. */
export function reloadStoreData() {
    clearSettings();
    clearProfile();
    loadSettings({ force: true });
    loadProfile({ force: true });
    forgetScreens();
    loadScreens();
    forgetModules();
    return loadModules();
}
