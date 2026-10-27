/*
 * A product's highlights and free gift note (D134): short lines of the shop's
 * own words the storefront draws beside the buy box. The engine keeps them in
 * the product's metadata and holds them to the same limits; these are here so
 * the editor can say so while the line is being typed, not after Save.
 */

export const MAX_HIGHLIGHTS = 8;
export const MAX_LINE = 80;

/** The saved lines as the editor's rows, with one blank to type into. */
export function highlightRows(metadata) {
    const saved = Array.isArray(metadata?.highlights)
        ? metadata.highlights.filter((s) => typeof s === "string")
        : [];
    return saved.length < MAX_HIGHLIGHTS ? [...saved, ""] : saved;
}

/** What is saved: each row trimmed, the blank ones left out. */
export function highlightLines(rows) {
    return (rows ?? []).map((s) => String(s ?? "").trim()).filter(Boolean);
}

/** The note as saved, or empty. */
export function freeGiftOf(metadata) {
    return typeof metadata?.free_gift === "string" ? metadata.free_gift : "";
}

/** Why a line would be refused, or "" when it will save. */
export function lineProblem(text) {
    const s = String(text ?? "").trim();
    if ([...s].length > MAX_LINE) return `${[...s].length} of ${MAX_LINE} characters: shorten it.`;
    // eslint-disable-next-line no-control-regex
    if (/[\u0000-\u001f\u007f]/.test(s)) return "One line only.";
    return "";
}

/** Rows after typing in row i: one blank row is kept at the end while there is room, and only
 *  one, so typing a line and deleting it again leaves the rows as they were. */
export function typedRows(rows, i, value) {
    const next = [...rows];
    next[i] = value;
    while (next.length > 1 && String(next[next.length - 1]).trim() === "" && String(next[next.length - 2]).trim() === "") {
        next.pop();
    }
    if (String(next[next.length - 1]).trim() !== "" && next.length < MAX_HIGHLIGHTS) {
        next.push("");
    }
    return next;
}

/** Rows without row i, keeping one blank to type into. */
export function removedRow(rows, i) {
    const next = rows.filter((_, j) => j !== i);
    if (!next.length || (String(next[next.length - 1]).trim() !== "" && next.length < MAX_HIGHLIGHTS)) {
        next.push("");
    }
    return next;
}
