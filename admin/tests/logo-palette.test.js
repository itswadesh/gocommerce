import { test } from "node:test";
import assert from "node:assert/strict";
import { contrastRatio } from "../src/lib/layouts.js";
import { readablePair, mutedFrom, paletteFromPixels, candidatesFromLogo } from "../src/lib/logo-palette.js";

// A block of count opaque pixels of one colour, as canvas getImageData gives
// them: a flat RGBA array.
function pixels(blocks) {
    const out = [];
    for (const [hex, count, alpha = 255] of blocks) {
        const r = parseInt(hex.slice(1, 3), 16);
        const g = parseInt(hex.slice(3, 5), 16);
        const b = parseInt(hex.slice(5, 7), 16);
        for (let i = 0; i < count; i++) out.push(r, g, b, alpha);
    }
    return new Uint8ClampedArray(out);
}

test("readablePair always meets 4.5:1 for text on the accent", () => {
    // A spread of hues and lightnesses, including the awkward mid-tones.
    const samples = [
        "#7c3aed", "#ff6b4a", "#a3d900", "#00b3b3", "#0f172a", "#ffd400",
        "#808080", "#767676", "#999999", "#c0c0c0", "#3366ff", "#e91e63",
        "#fef3c7", "#ffffff", "#000000", "#556b2f", "#4a4a4a",
    ];
    for (const accent of samples) {
        const { accent: a, text } = readablePair(accent);
        assert.match(a, /^#[0-9a-f]{6}$/, `${accent} -> accent ${a}`);
        assert.ok(text === "#ffffff" || text === "#000000", `${accent} -> text ${text}`);
        const ratio = contrastRatio(text, a);
        assert.ok(ratio >= 4.5, `${accent} -> pair ${a}/${text} = ${ratio?.toFixed(2)}, want >= 4.5`);
    }
});

test("readablePair keeps a vivid accent that already works untouched", () => {
    // Deep purple with white text is already readable; it must not be moved.
    const { accent, text } = readablePair("#7c3aed");
    assert.equal(accent, "#7c3aed");
    assert.equal(text, "#ffffff");
});

test("readablePair moves the accent when neither black nor white meets the floor", () => {
    // At 4.5 black or white always works, so force the move with a stricter
    // floor (AAA, 7:1). A mid-tone meets neither at 7:1, so the accent itself
    // must shift, and the result must clear 7:1.
    const mid = "#808080";
    assert.ok(contrastRatio("#ffffff", mid) < 7);
    assert.ok(contrastRatio("#000000", mid) < 7);
    const { accent, text } = readablePair(mid, 7);
    assert.notEqual(accent, mid);
    assert.ok(contrastRatio(text, accent) >= 7);
});

test("mutedFrom is a pale tint that dark text can sit on", () => {
    const muted = mutedFrom("#7c3aed");
    assert.match(muted, /^#[0-9a-f]{6}$/);
    // Pale enough for the storefront's dark text (#0f172a).
    assert.ok(contrastRatio("#0f172a", muted) >= 4.5, `dark text on muted ${muted}`);
});

test("paletteFromPixels ignores white, black and grey, keeps the vivid colour", () => {
    // A coral logo on a white page with black ink: only the coral is a brand
    // colour, so it must come out on top.
    const data = pixels([
        ["#ffffff", 500], // page
        ["#111111", 120], // ink/outline
        ["#ff6b4a", 200], // the brand coral
        ["#f2f2f2", 80], // near-white
    ]);
    const palette = paletteFromPixels(data);
    assert.ok(palette.length >= 1, "a vivid colour was found");
    // The top colour reads as coral: red dominant, green mid, blue low.
    const [r, g, b] = [1, 3, 5].map((i) => parseInt(palette[0].hex.slice(i, i + 2), 16));
    assert.ok(r > g && g > b, `top colour ${palette[0].hex} reads coral-ish`);
});

test("candidatesFromLogo gives at least one readable candidate for a two-colour logo", () => {
    const data = pixels([
        ["#ffffff", 400],
        ["#00897b", 150], // teal
        ["#ff7043", 150], // coral
    ]);
    const candidates = candidatesFromLogo(data);
    assert.ok(candidates.length >= 2, "a two-colour logo offers alternatives");
    for (const c of candidates) {
        assert.match(c.accent, /^#[0-9a-f]{6}$/);
        assert.match(c.muted, /^#[0-9a-f]{6}$/);
        assert.ok(contrastRatio(c.accent_text, c.accent) >= 4.5, `candidate ${c.accent}/${c.accent_text}`);
    }
});

test("candidatesFromLogo still yields a usable accent for a white or monochrome logo", () => {
    // A white-on-transparent logo: nothing vivid. It must not throw or return
    // an unusable pair.
    const white = pixels([["#ffffff", 600, 255], ["#fdfdfd", 400, 255]]);
    const whiteCands = candidatesFromLogo(white);
    assert.ok(whiteCands.length >= 1);
    assert.ok(contrastRatio(whiteCands[0].accent_text, whiteCands[0].accent) >= 4.5);

    // A black wordmark on transparent: the ink becomes the accent.
    const black = pixels([["#000000", 0, 0], ["#141414", 300, 255]]);
    const blackCands = candidatesFromLogo(black);
    assert.ok(blackCands.length >= 1);
    assert.ok(contrastRatio(blackCands[0].accent_text, blackCands[0].accent) >= 4.5);
});

test("candidatesFromLogo tolerates an all-transparent image", () => {
    const data = pixels([["#123456", 500, 0]]);
    const candidates = candidatesFromLogo(data);
    assert.ok(candidates.length >= 1, "a fallback candidate is offered");
    assert.ok(contrastRatio(candidates[0].accent_text, candidates[0].accent) >= 4.5);
});
