/**
 * Theme colours from a logo (D149).
 *
 * The "Match to logo" button in the theme editor reads the store's logo into a
 * canvas and hands the pixels here. These are pure functions over an RGBA
 * pixel array — no canvas, no DOM — so they unit-test on their own; the canvas
 * read lives in the editor.
 *
 * The job: find the logo's prominent brand colours, then map each to a theme
 * the storefront can draw — an accent for buttons and links, readable text to
 * put on it, and a soft tint for quiet backgrounds. The one rule that never
 * bends is contrast: every accent/accent_text pair comes out at WCAG AA for
 * body text (>= 4.5:1), adjusting the accent's lightness when neither black
 * nor white would do (readablePair). The colours are the shop's; readability
 * is not negotiable.
 */

import { contrastRatio } from "./layouts.js";

// The storefront's own dark ink, the fallback accent for a logo that offers no
// colour of its own (white, or pure outline art).
const FALLBACK_ACCENT = "#0f172a";

// Which pixels count as a brand colour. A logo sits on the page's white (or a
// transparent ground), is drawn in black or grey ink, and carries its brand
// hue in between — so the mapping ignores the first two and clusters the rest.
const MIN_ALPHA = 128; // a pixel barely there is the transparent ground
const MIN_SAT = 0.18; // below this it is a grey, not a hue
const MIN_LIGHT = 0.12; // near-black is ink
const MAX_LIGHT = 0.9; // near-white is the page

function clamp(n, lo, hi) {
    return Math.min(hi, Math.max(lo, n));
}

function toHex(r, g, b) {
    const h = (n) => clamp(Math.round(n), 0, 255).toString(16).padStart(2, "0");
    return `#${h(r)}${h(g)}${h(b)}`;
}

function hexToRgb(hex) {
    return [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
}

function rgbToHsl(r, g, b) {
    r /= 255;
    g /= 255;
    b /= 255;
    const max = Math.max(r, g, b);
    const min = Math.min(r, g, b);
    const l = (max + min) / 2;
    const d = max - min;
    if (d === 0) return [0, 0, l];
    const s = l > 0.5 ? d / (2 - max - min) : d / (max + min);
    let h;
    if (max === r) h = ((g - b) / d + (g < b ? 6 : 0)) / 6;
    else if (max === g) h = ((b - r) / d + 2) / 6;
    else h = ((r - g) / d + 4) / 6;
    return [h * 360, s, l];
}

function hslToHex(h, s, l) {
    h = ((h % 360) + 360) % 360 / 360;
    if (s === 0) {
        const v = l * 255;
        return toHex(v, v, v);
    }
    const q = l < 0.5 ? l * (1 + s) : l + s - l * s;
    const p = 2 * l - q;
    const hue = (t) => {
        t = (t + 1) % 1;
        if (t < 1 / 6) return p + (q - p) * 6 * t;
        if (t < 1 / 2) return q;
        if (t < 2 / 3) return p + (q - p) * (2 / 3 - t) * 6;
        return p;
    };
    return toHex(hue(h + 1 / 3) * 255, hue(h) * 255, hue(h - 1 / 3) * 255);
}

/**
 * readablePair returns an accent and the text colour (black or white) to put
 * on it, guaranteed at >= floor (WCAG AA for body text, 4.5:1, by default). If
 * the accent is already readable with one of them it is returned untouched; a
 * mid-tone that neither sits on is moved in lightness — toward dark for white
 * text, toward light for black text, whichever is nearer — keeping its hue and
 * saturation, until a pair reaches the floor. Black on white (21:1) bounds
 * both directions, so it terminates.
 *
 * At the 4.5 floor the move never happens: black or white clears 4.5:1 on any
 * colour (the worst case, a mid-luminance grey, still reads at ~4.58:1). The
 * branch earns its place for a stricter floor — AAA's 7:1 — and keeps the
 * guarantee true if the floor is ever raised, rather than silently returning
 * an unreadable pair.
 */
export function readablePair(accent, floor = 4.5) {
    const white = contrastRatio("#ffffff", accent) ?? 0;
    const black = contrastRatio("#000000", accent) ?? 0;
    if (white >= floor || black >= floor) {
        return { accent, text: white >= black ? "#ffffff" : "#000000" };
    }
    const [h, s] = rgbToHsl(...hexToRgb(accent));
    // Nearer to readable with white (so darken) or with black (so lighten)?
    if (white >= black) {
        for (let l = 0.5; l >= 0; l -= 0.02) {
            const a = hslToHex(h, s, l);
            if (contrastRatio("#ffffff", a) >= floor) return { accent: a, text: "#ffffff" };
        }
        return { accent: "#000000", text: "#ffffff" };
    }
    for (let l = 0.5; l <= 1; l += 0.02) {
        const a = hslToHex(h, s, l);
        if (contrastRatio("#000000", a) >= floor) return { accent: a, text: "#000000" };
    }
    return { accent: "#ffffff", text: "#000000" };
}

/**
 * mutedFrom is a soft tint of the accent for quiet backgrounds — the starter's
 * pale yellow behind a strip, in the accent's own hue: its saturation pulled
 * down and its lightness up, so dark text reads on it.
 */
export function mutedFrom(accent) {
    const [h, s] = rgbToHsl(...hexToRgb(accent));
    return hslToHex(h, clamp(s * 0.5, 0.12, 0.45), 0.93);
}

/**
 * paletteFromPixels clusters the logo's brand colours, most prominent first.
 * It drops the page (near-white), the ink (near-black) and greys, then buckets
 * what is left by hue into 15 bins of 24° and averages each bin's pixels. A
 * bin's prominence is how many pixels it holds. Returns [{ hex, weight }].
 *
 * ponytail: hue-bucketing, not full k-means. A logo has a handful of flat
 * brand colours, not a photo's gradient, so narrow hue bins separate them
 * cleanly; swap in k-means only if real logos come out muddy.
 */
export function paletteFromPixels(data) {
    const BINS = 15;
    const bins = Array.from({ length: BINS }, () => ({ r: 0, g: 0, b: 0, n: 0 }));
    for (let i = 0; i + 3 < data.length; i += 4) {
        const a = data[i + 3];
        if (a < MIN_ALPHA) continue;
        const r = data[i];
        const g = data[i + 1];
        const b = data[i + 2];
        const [h, s, l] = rgbToHsl(r, g, b);
        if (s < MIN_SAT || l < MIN_LIGHT || l > MAX_LIGHT) continue;
        const bin = bins[Math.min(BINS - 1, Math.floor((h / 360) * BINS))];
        bin.r += r;
        bin.g += g;
        bin.b += b;
        bin.n++;
    }
    return bins
        .filter((bin) => bin.n > 0)
        .map((bin) => ({ hex: toHex(bin.r / bin.n, bin.g / bin.n, bin.b / bin.n), weight: bin.n }))
        .sort((x, y) => y.weight - x.weight);
}

/**
 * A logo that offers no hue — a white or a purely black-and-grey mark — still
 * needs an accent. Take the most prominent opaque colour whatever its
 * saturation; if that is near-white (or there is nothing), fall back to the
 * storefront's dark ink. readablePair then makes it safe.
 */
function fallbackAccent(data) {
    let best = null;
    const grid = new Map();
    for (let i = 0; i + 3 < data.length; i += 4) {
        if (data[i + 3] < MIN_ALPHA) continue;
        const key = `${data[i] >> 4}-${data[i + 1] >> 4}-${data[i + 2] >> 4}`;
        const cell = grid.get(key) ?? { r: 0, g: 0, b: 0, n: 0 };
        cell.r += data[i];
        cell.g += data[i + 1];
        cell.b += data[i + 2];
        cell.n++;
        grid.set(key, cell);
    }
    for (const cell of grid.values()) {
        if (!best || cell.n > best.n) best = cell;
    }
    if (!best) return FALLBACK_ACCENT;
    const [, , l] = rgbToHsl(best.r / best.n, best.g / best.n, best.b / best.n);
    if (l > MAX_LIGHT) return FALLBACK_ACCENT;
    return toHex(best.r / best.n, best.g / best.n, best.b / best.n);
}

/**
 * candidatesFromLogo turns a logo's pixels into theme candidates, each a
 * contrast-safe { accent, accent_text, muted }, most prominent first. A logo
 * with more than one strong colour offers more than one candidate (up to
 * three); one with none still yields a single usable candidate from its ink.
 */
export function candidatesFromLogo(data) {
    const palette = paletteFromPixels(data);
    const accents = palette.length ? palette.slice(0, 3).map((c) => c.hex) : [fallbackAccent(data)];
    return accents.map((raw) => {
        const { accent, text } = readablePair(raw);
        return { accent, accent_text: text, muted: mutedFrom(accent) };
    });
}
