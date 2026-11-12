/*
 * A WhatsApp chat with a customer, from the phone on their order. The number
 * is kept as they typed it, so it is read the way core reads a shop's own
 * (whatsappDigits, D255): a bare ten-digit number, or one with a trunk 0, in
 * India gets India's code; anything else must carry its own.
 */

/** whatsappDigits is the number as wa.me takes it, or "" when it cannot be one. */
export function whatsappDigits(phone, country) {
    const typed = String(phone ?? "").trim();
    let d = typed.replace(/\D/g, "");
    const indian = String(country ?? "").toUpperCase() === "IN";
    if (typed.startsWith("+")) {
        // already international
    } else if (d.startsWith("00")) {
        d = d.slice(2);
    } else if (indian && d.length === 11 && d[0] === "0") {
        d = "91" + d.slice(1);
    } else if (indian && d.length === 10) {
        d = "91" + d;
    }
    return d.length >= 8 && d.length <= 15 && d[0] !== "0" ? d : "";
}

/** whatsappLink opens the chat with text typed in, or is "" for a number WhatsApp cannot reach. */
export function whatsappLink(phone, country, text = "") {
    const d = whatsappDigits(phone, country);
    if (!d) return "";
    return "https://wa.me/" + d + (text ? "?text=" + encodeURIComponent(text) : "");
}
