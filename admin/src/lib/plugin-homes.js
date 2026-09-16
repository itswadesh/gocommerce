/**
 * Which plugins are configured somewhere better than the Plugins screen.
 *
 * The Plugins screen grew into a second place to configure half the panel.
 * Forty-six rows, of which the payment gateways belong on Payment methods, the
 * carriers on Shipping providers, the email and SMS backends under Setup
 * Email and Setup SMS, and a dozen more have a screen of their own with the
 * thing itself on it — the Feeds screen shows what is in the feed, the Menus
 * screen shows the menu. Editing any of them here meant a form with no context
 * next to the form that has it.
 *
 * So nothing is hidden and nothing is duplicated: a plugin with a home is
 * listed with a link to it instead of a settings drawer, under its own
 * heading. The switch stays here, because "is this installed and on" is a
 * question about plugins and this is the plugins screen.
 *
 * A key missing from both maps simply has no home, which is the common case
 * and needs no entry — a plugin whose whole configuration is three fields has
 * nowhere better to be than here.
 */

/** Whole categories whose members all live on one screen. */
export const CATEGORY_HOMES = {
    payments: { href: "/settings/payments", label: "Payment methods" },
    shipping: { href: "/shipping/providers", label: "Shipping providers" },
};

/** Individual plugins with a screen of their own. */
export const PLUGIN_HOMES = {
    "product-feeds": { href: "/feeds", label: "Feeds" },
    sitemap: { href: "/sitemap", label: "Sitemap" },
    "email-sendgrid": { href: "/notifications/email", label: "Setup Email" },
    "email-resend": { href: "/notifications/email", label: "Setup Email" },
    "sms-msg91": { href: "/notifications/sms", label: "Setup SMS" },
    "sms-twilio": { href: "/notifications/sms", label: "Setup SMS" },
    navigation: { href: "/menus", label: "Menus" },
    "product-reviews": { href: "/reviews", label: "Reviews" },
    "contact-form": { href: "/contact", label: "Contact messages" },
    faq: { href: "/faq", label: "FAQ" },
    wishlist: { href: "/wishlists", label: "Wishlists" },
    newsletter: { href: "/newsletter", label: "Newsletter" },
};

/**
 * Where this plugin is configured, or null if the answer is "here".
 *
 * @param {{key?: string, category?: string}} plugin
 */
export function homeOf(plugin) {
    if (!plugin) return null;
    return PLUGIN_HOMES[plugin.key] ?? CATEGORY_HOMES[plugin.category] ?? null;
}
