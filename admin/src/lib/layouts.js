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
    const text = s.heading || s.text || s.body || (Array.isArray(s.items) ? s.items.map((i) => i.title).filter(Boolean).join(" · ") : "") || "";
    return { label: t.label, text: String(text).slice(0, 120) };
}

/** The fields to show for settings: a field with show_when only while the other setting matches. */
export function visibleFields(fields, settings) {
    const value = (key) => settings?.[key] ?? fields.find((f) => f.key === key)?.default;
    return fields.filter((f) => !f.show_when || Object.entries(f.show_when).every(([k, v]) => value(k) === v));
}
