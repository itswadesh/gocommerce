/**
 * The homepage editor's page logic (ext/layouts, D125), kept free of the DOM
 * so it can be tested on its own: a page is an ordered list of sections, and
 * every edit returns a new list rather than changing the old one, so the
 * screen can compare against what was saved and put a failed save back.
 */

/** The registry entry for a section type, or undefined. */
export function sectionType(type, registry) {
    return registry.find((t) => t.type === type);
}

function newId(taken) {
    const used = new Set(taken);
    for (;;) {
        const id = "s-" + Math.random().toString(36).slice(2, 8);
        if (!used.has(id)) return id;
    }
}

/** A fresh section of type, with its fields' defaults and an id the page does not use. */
export function blankSection(type, registry, takenIds = []) {
    const settings = {};
    for (const f of sectionType(type, registry)?.fields ?? []) {
        if (f.default !== undefined && f.default !== null) settings[f.key] = f.default;
    }
    return { id: newId(takenIds), type, enabled: true, settings };
}

/** sections with section put at index: 0 is the top, sections.length the bottom. */
export function insertAt(sections, index, section) {
    const at = Math.max(0, Math.min(index, sections.length));
    return [...sections.slice(0, at), section, ...sections.slice(at)];
}

/** sections with the one at index moved one place up (dir -1) or down (dir 1). */
export function moveSection(sections, index, dir) {
    const to = index + dir;
    if (to < 0 || to >= sections.length) return sections;
    const out = [...sections];
    [out[index], out[to]] = [out[to], out[index]];
    return out;
}

/** sections with a deep copy of the one at index, under a new id, right below it. */
export function duplicateSection(sections, index) {
    // A JSON copy, not structuredClone: the screen's sections are Svelte
    // state proxies, which structuredClone refuses to copy.
    const copy = JSON.parse(JSON.stringify(sections[index]));
    copy.id = newId(sections.map((s) => s.id));
    return insertAt(sections, index + 1, copy);
}

export function removeSection(sections, index) {
    return sections.filter((_, i) => i !== index);
}

/** sections with the one at index shown or hidden; hiding keeps its settings. */
export function toggleSection(sections, index) {
    return sections.map((s, i) => (i === index ? { ...s, enabled: !s.enabled } : s));
}

/** What a row in the list says: the type, and the first words the section shows. */
export function sectionSummary(section, registry) {
    const t = sectionType(section.type, registry);
    if (!t) return { label: `Unknown section (${section.type})`, text: "" };
    const s = section.settings ?? {};
    const list = Array.isArray(s.items) ? s.items : Array.isArray(s.slides) ? s.slides : [];
    const text = s.heading || s.text || s.body || list.map((i) => i?.title || i?.heading || i?.question).filter(Boolean).join(" · ") || "";
    return { label: t.label, text: String(text).slice(0, 120) };
}

/** The fields to show for settings: a field with show_when only while the other setting matches. */
export function visibleFields(fields, settings) {
    const value = (key) => settings?.[key] ?? fields.find((f) => f.key === key)?.default;
    // Compared as text: a switch's true is the registry's "true".
    return fields.filter((f) => !f.show_when || Object.entries(f.show_when).every(([k, v]) => String(value(k)) === v));
}

// ------------------------------------------------------------ the theme editor (D126)

/** sections with the one at from moved to index to; the same list when nothing moves. */
export function moveTo(sections, from, to) {
    if (from === to || from < 0 || to < 0 || from >= sections.length || to >= sections.length) return sections;
    const out = [...sections];
    const [moved] = out.splice(from, 1);
    out.splice(to, 0, moved);
    return out;
}

/**
 * The position a dragged row goes to, from where the pointer is: past a row's
 * middle is below it. middles are the rows' vertical centres, top to bottom.
 */
export function dropIndex(middles, y) {
    let i = 0;
    while (i < middles.length - 1 && y > middles[i]) i++;
    return i;
}

/**
 * Undo and redo over whole documents (serialised, so a step is a value and
 * never a live object the screen goes on changing). record() is called with
 * the document as it was before an edit; a run of edits to one field within a
 * second is one step, so undo takes back a word typed, not a letter.
 */
export function createHistory({ limit = 100, window = 1000, now = () => Date.now() } = {}) {
    let past = [];
    let future = [];
    let lastKey = null;
    let lastAt = 0;
    return {
        record(before, key = null) {
            const t = now();
            const folds = key !== null && key === lastKey && t - lastAt < window && past.length > 0;
            if (!folds) {
                past.push(before);
                if (past.length > limit) past.shift();
            }
            lastKey = key;
            lastAt = t;
            future = [];
        },
        /** The document to go back to, given the current one; null when there is none. */
        undo(current) {
            if (!past.length) return null;
            future.push(current);
            lastKey = null;
            return past.pop();
        },
        redo(current) {
            if (!future.length) return null;
            past.push(current);
            lastKey = null;
            return future.pop();
        },
        canUndo: () => past.length > 0,
        canRedo: () => future.length > 0,
        size: () => past.length,
    };
}

/**
 * Saves the draft a pause after the last edit, one save at a time, always the
 * newest edit. States: "saved", "pending" (an edit is waiting for the pause),
 * "saving", "error" (kept, and retried on the next edit or retry()), and
 * "conflict" — somebody else saved the page since it was read, so nothing more
 * is sent until the screen reloads it.
 */
export function createAutosave({ save, delay = 800, setTimer = setTimeout, clearTimer = clearTimeout, onstate = () => {} }) {
    let state = "saved";
    let latest; // the newest unsaved document, or undefined
    let timer = null;
    let running = null;

    const set = (s) => {
        if (s !== state) {
            state = s;
            onstate(s);
        }
    };

    async function run() {
        if (timer !== null) {
            clearTimer(timer);
            timer = null;
        }
        if (running) return running;
        if (latest === undefined || state === "conflict") return;
        const doc = latest;
        latest = undefined;
        set("saving");
        running = (async () => {
            try {
                await save(doc);
                running = null;
                if (latest !== undefined) return run();
                set("saved");
            } catch (err) {
                running = null;
                if (latest === undefined) latest = doc;
                set(err?.status === 409 ? "conflict" : "error");
            }
        })();
        return running;
    }

    return {
        change(doc) {
            latest = doc;
            if (state === "conflict") return;
            if (state !== "saving") set("pending");
            if (timer !== null) clearTimer(timer);
            timer = setTimer(run, delay);
        },
        /** Save now, without waiting for the pause: leaving the screen. */
        flush: () => run(),
        retry: () => run(),
        /** Forget the waiting edit, and wait out a save already sent. */
        async cancel() {
            if (timer !== null) clearTimer(timer);
            timer = null;
            latest = undefined;
            if (running) await running;
            if (state !== "conflict") set("saved");
        },
        /** Resolves once nothing is being saved. */
        async idle() {
            while (running) await running;
        },
        /** Back to a clean slate after the screen reloaded the page. */
        reset() {
            if (timer !== null) clearTimer(timer);
            timer = null;
            latest = undefined;
            set("saved");
        },
        pending: () => latest !== undefined || running !== null,
        state: () => state,
    };
}

/**
 * The storefront's home page with a preview key, and — for the frame — the
 * panel's origin, which the storefront then accepts messages from and lets
 * frame it. Only an http(s) address makes one; anything else is "".
 */
export function previewAddress(storefront, key, editorOrigin) {
    let url;
    try {
        url = new URL(storefront);
    } catch {
        return "";
    }
    if (url.protocol !== "https:" && url.protocol !== "http:") return "";
    const out = new URL("/", url.origin);
    out.searchParams.set("preview", key);
    if (editorOrigin) out.searchParams.set("editor", editorOrigin);
    return out.href;
}

// ------------------------------------------------------------ theme settings (D127)

/**
 * The storefront's default theme's own colours, which an unset theme setting
 * keeps: the stand-in for the other half of a pair when only one is set.
 */
const STOREFRONT_COLOURS = { background: "#ffffff", text: "#0f172a", accent: "#0f172a", accent_text: "#ffffff", muted: "#f1f5f9" };

function channel(hex, at) {
    const c = parseInt(hex.slice(at, at + 2), 16) / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
}

function luminance(hex) {
    return 0.2126 * channel(hex, 1) + 0.7152 * channel(hex, 3) + 0.0722 * channel(hex, 5);
}

/** The WCAG contrast ratio of two #rrggbb colours, or null for anything else. */
export function contrastRatio(a, b) {
    const ok = (x) => typeof x === "string" && /^#[0-9a-f]{6}$/i.test(x);
    if (!ok(a) || !ok(b)) return null;
    const [hi, lo] = [luminance(a), luminance(b)].sort((x, y) => y - x);
    return (hi + 0.05) / (lo + 0.05);
}

const PAIRS = [
    { keys: ["text", "background"], what: "Text on the background" },
    { keys: ["accent_text", "accent"], what: "Text on buttons" },
    { keys: ["text", "muted"], what: "Text on quiet backgrounds" },
];

/**
 * The pairs the shop set that read poorly: below 4.5:1, the WCAG AA floor for
 * body text. A warning, never a refusal — the colours are the shop's. A pair
 * neither half of which is set is the storefront's own and not judged here.
 */
export function themeWarnings(settings) {
    const s = settings ?? {};
    const out = [];
    for (const { keys, what } of PAIRS) {
        if (!s[keys[0]] && !s[keys[1]]) continue;
        const ratio = contrastRatio(s[keys[0]] || STOREFRONT_COLOURS[keys[0]], s[keys[1]] || STOREFRONT_COLOURS[keys[1]]);
        if (ratio !== null && ratio < 4.5) {
            out.push({ keys, ratio, message: `${what}: ${(Math.floor(ratio * 10) / 10).toFixed(1)}:1. Below 4.5:1 is hard to read for many shoppers.` });
        }
    }
    return out;
}

/** What the Themes screen says of the theme settings. */
export function themeSettingsSummary(settings) {
    const n = Object.keys(settings ?? {}).length;
    if (!n) return "The storefront's own colours and fonts";
    return n === 1 ? "1 setting changed" : `${n} settings changed`;
}

const MEDIA_KEYS = new Set(["image", "mobile_image", "poster", "video_file"]);

/**
 * A copy of doc for the storefront frame, with every picture or video from
 * Files given the panel's origin. Files answers root-relative addresses
 * (/media/…), which the storefront would look for on its own host.
 */
export function absoluteMedia(doc, origin) {
    const fix = (v, key) => {
        if (Array.isArray(v)) return v.map((x) => fix(x, null));
        if (v && typeof v === "object") return Object.fromEntries(Object.entries(v).map(([k, x]) => [k, fix(x, k)]));
        if (MEDIA_KEYS.has(key) && typeof v === "string" && v.startsWith("/") && !v.startsWith("//") && origin) return origin + v;
        return v;
    };
    return { ...doc, sections: fix(JSON.parse(JSON.stringify(doc.sections ?? [])), null) };
}
