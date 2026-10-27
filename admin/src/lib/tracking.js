/**
 * The storefront page where a customer sees their order, the number filled in.
 * It mirrors the engine's link in order emails: the store's own domain, else
 * its address under the base domain, and nothing when it has neither. With the
 * order's key the link opens the order by itself; without it the customer
 * types the email or phone on the order.
 */
export function orderTrackingURL(store, number, key = "") {
    const host = store?.domain || store?.subdomain || "";
    if (!host || !number) return "";
    const params = new URLSearchParams({ order: number });
    if (key) params.set("key", key);
    return `https://${host}/order-tracking?${params}`;
}
