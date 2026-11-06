// A country's own second level, as in shop.example.co.in, where the name someone
// bought is three labels long.
// ponytail: a short list rather than the 10,000-line public suffix list. A suffix
// missing here groups its stores under the suffix, as "something.in"; add it here
// when one shows up.
const SECOND_LEVEL = new Set(["ac", "co", "com", "edu", "firm", "gen", "gov", "ind", "ltd", "me", "net", "org", "plc", "res"]);

/** apexDomain is the name someone registered, for a host under it: shops.example.com → example.com. */
export function apexDomain(host) {
    const labels = (host ?? "").toLowerCase().split(".").filter(Boolean);
    if (labels.length < 2) return labels[0] ?? "";
    const countryCode = labels.at(-1).length === 2;
    const keep = countryCode && labels.length > 2 && SECOND_LEVEL.has(labels.at(-2)) ? 3 : 2;
    return labels.slice(-keep).join(".");
}
