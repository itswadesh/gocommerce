/**
 * What somebody typed in a box taking an email or a phone, as the engine wants
 * it. The engine asks for a country code on every number, and most people here
 * type ten digits, so a number that can only be an Indian mobile is read as one
 * (D191). Anything else goes as it came, for the engine to accept or refuse.
 */
export function toIdentity(raw) {
    const typed = String(raw ?? "").trim();
    if (/[a-z@]/i.test(typed)) return typed;
    const digits = typed.replace(/\D/g, "");
    if (typed.startsWith("+")) return `+${digits}`;
    if (/^[6-9]\d{9}$/.test(digits)) return `+91${digits}`;
    if (/^0[6-9]\d{9}$/.test(digits)) return `+91${digits.slice(1)}`;
    if (/^91[6-9]\d{9}$/.test(digits)) return `+${digits}`;
    return typed;
}
