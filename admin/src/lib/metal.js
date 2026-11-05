/**
 * scaled reads what a jeweller types ("12.345" grams, "8.5" percent) as a whole
 * number scaled by 10^places, in text so no float ever touches a weight. It is
 * null for anything else, including more decimals than the field holds, because
 * a weight quietly rounded is a price quietly changed.
 */
export function scaled(text, places) {
    const clean = String(text ?? "").trim().replaceAll(",", "");
    const match = /^(\d*)(?:\.(\d*))?$/.exec(clean);
    if (!match || clean === "" || clean === ".") return null;
    const fraction = match[2] ?? "";
    if (fraction.length > places) return null;
    return Number((match[1] || "0") + fraction.padEnd(places, "0"));
}

/** unscaled is scaled's reverse, for showing a stored value: 12345, 3 is "12.345". */
export function unscaled(value, places) {
    const text = String(value).padStart(places + 1, "0");
    const whole = text.slice(0, text.length - places);
    const fraction = text.slice(text.length - places).replace(/0+$/, "");
    return fraction ? `${whole}.${fraction}` : whole;
}

/** rateCode is a metal's name as the code that identifies it: "Gold 22K" is "gold-22k". */
export function rateCode(name) {
    return String(name ?? "")
        .toLowerCase()
        .replace(/[^a-z0-9]+/g, "-")
        .replace(/^-+|-+$/g, "")
        .slice(0, 40);
}

/** repriceMessage says what entering a rate did, in the words a jeweller checks for. */
export function repriceMessage(out) {
    const moved = out.repriced === 1 ? "1 design" : `${out.repriced} designs`;
    const failed = out.failed?.length ?? 0;
    if (failed) return `${moved} repriced. ${failed} could not be changed and keep their old price.`;
    return out.repriced ? `${moved} repriced.` : "Rate saved. No design's price changed.";
}
