/**
 * The storefront page where a customer opens their order with a code sent to
 * the email or phone on it (D115), the order number filled in. It mirrors the
 * engine's link in order emails: the store's own domain, else its address
 * under the base domain, and nothing when it has neither. The link carries no
 * access of its own, so it is safe to paste into a chat.
 */
export function orderTrackingURL(store, number) {
    const host = store?.domain || store?.subdomain || "";
    if (!host || !number) return "";
    return `https://${host}/order-tracking?${new URLSearchParams({ number })}`;
}
