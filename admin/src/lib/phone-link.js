/*
 * A number typed on a marketing page reaches the panel in the fragment
 * (`#phone=%2B919876543210`), the one part of a URL no access log or Referer
 * sees. It is taken off the address bar as soon as it is read, so a copied URL
 * does not carry it onward. It only fills the form: the code form where one can
 * be texted, and the "Email or phone" box either way. Sending a code costs money,
 * and that stays the person's click.
 */

const E164 = /^\+[1-9][0-9]{7,14}$/;

/**
 * phoneFromFragment splits a location.hash into the number it carries, if it is
 * one, and what is left of the fragment. decodeURIComponent rather than
 * URLSearchParams, which reads a "+" as a space.
 */
export function phoneFromFragment(hash) {
    const parts = (hash ?? "").replace(/^#/, "").split("&").filter(Boolean);
    const rest = parts.filter((p) => !p.startsWith("phone="));
    if (rest.length === parts.length) return { found: false, phone: "", rest: hash ?? "" };
    let phone = "";
    try {
        phone = decodeURIComponent(parts.find((p) => p.startsWith("phone=")).slice("phone=".length));
    } catch {
        // Not valid percent-encoding: nothing to fill in, and still cleared.
    }
    return { found: true, phone: E164.test(phone) ? phone : "", rest: rest.length ? `#${rest.join("&")}` : "" };
}

/** takePhoneFromFragment reads the number and clears it from the address bar, leaving any other fragment. */
export function takePhoneFromFragment(loc = globalThis.location, hist = globalThis.history) {
    if (!loc?.hash) return "";
    const { found, phone, rest } = phoneFromFragment(loc.hash);
    if (found) hist.replaceState(hist.state, "", loc.pathname + loc.search + rest);
    return phone;
}

/** phoneFragment is the number as a fragment again, for a link on to another screen. */
export function phoneFragment(phone) {
    return phone ? `#phone=${encodeURIComponent(phone)}` : "";
}

/** readablePhone spaces an Indian number the way people read it; any other stays as it came. */
export function readablePhone(phone) {
    const india = /^\+91(\d{5})(\d{5})$/.exec(phone);
    return india ? `+91 ${india[1]} ${india[2]}` : phone;
}
