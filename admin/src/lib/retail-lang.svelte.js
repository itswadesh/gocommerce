/**
 * Which language the retail assistant's screens are in, for this browser. Only these
 * screens: the rest of the panel has no translations, and half a sidebar in Hindi
 * would read worse than none.
 */
import { translate } from "$lib/retail-hindi.js";

const KEY = "retail-assistant-language";

function saved() {
    try {
        return globalThis.localStorage?.getItem(KEY) === "hi" ? "hi" : "en";
    } catch {
        return "en";
    }
}

let lang = $state(saved());

export const retailLanguage = () => lang;

export function setRetailLanguage(next) {
    lang = next === "hi" ? "hi" : "en";
    try {
        globalThis.localStorage?.setItem(KEY, lang);
    } catch {
        // A private window keeps the choice for this page only, which still works.
    }
}

export const t = (text, vars) => translate(lang, text, vars);
