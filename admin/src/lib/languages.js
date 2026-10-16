/**
 * The languages a store can sell in, named in English and in themselves:
 * "Hindi · हिन्दी (hi)".
 *
 * Browsers have no list of languages to read, as they have of currencies, so
 * this is a short one: every language of India's Eighth Schedule, then the
 * ones most stores elsewhere are written in. The engine takes any BCP 47 tag
 * (D87); this is only how one is picked, and a tag a store already has that is
 * not here stays a choice.
 */
const INDIA = ["as", "bn", "brx", "doi", "gu", "hi", "kn", "ks", "kok", "mai", "ml", "mni", "mr", "ne", "or", "pa",
    "sa", "sat", "sd", "ta", "te", "ur"];
const WORLD = ["ar", "de", "en", "es", "fa", "fil", "fr", "he", "id", "it", "ja", "km", "ko", "ms", "my", "nl", "pl",
    "pt", "ru", "si", "sv", "sw", "th", "tr", "uk", "vi", "zh-Hans", "zh-Hant"];

function displayNames(locale) {
    try {
        return new Intl.DisplayNames([locale], { type: "language" });
    } catch {
        return null;
    }
}

const english = displayNames("en");

function nameIn(names, tag) {
    try {
        return names?.of(tag) ?? "";
    } catch {
        return ""; // not a tag Intl can read; the tag itself names it
    }
}

/** languageName is a tag's name in English, then in itself when that differs. */
export function languageName(tag) {
    const en = nameIn(english, tag);
    if (!en || en === tag) return tag;
    const own = nameIn(displayNames(tag), tag);
    return own && own.toLowerCase() !== en.toLowerCase() ? `${en} · ${own}` : en;
}

export const LANGUAGES = [...new Set([...INDIA, ...WORLD])]
    .map((tag) => ({ value: tag, label: `${languageName(tag)} (${tag})` }))
    .sort((a, b) => a.label.localeCompare(b.label));

/** languageOptions is the list without the tags a store already has, so
 *  nothing is offered twice. */
export function languageOptions(chosen = []) {
    const have = new Set(chosen.map((t) => t.toLowerCase()));
    return LANGUAGES.filter((l) => !have.has(l.value.toLowerCase()));
}
