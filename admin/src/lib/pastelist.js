/**
 * A pasted item list, one item per line with its price last (D272):
 * "Chocolate cake - 450", "Veg puff ₹25", "Samosa: 15/-". Each line becomes
 * { title, price } with price as typed, or { title, error } when no price can
 * be read from its end. Blank lines are dropped.
 */
const LINE = /^(.*?)[\s\-–—:,|=]*(?:rs\.?|inr|₹|\$|€|£)?\s*(\d[\d,]*(?:\.\d{1,3})?)\s*(?:\/-)?\s*$/i;

export function parseItemList(text) {
    const items = [];
    for (const raw of String(text ?? "").split(/\r?\n/)) {
        const line = raw.replace(/^\s*(?:[-*•]|\d+[.)])\s+/, "").trim();
        if (!line) continue;
        const m = line.match(LINE);
        const title = m?.[1]?.trim();
        if (!m || !title) {
            items.push({ title: line, error: "No price at the end of the line" });
            continue;
        }
        items.push({ title, price: m[2].replace(/,/g, "") });
    }
    return items;
}
