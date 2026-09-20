/**
 * The one copy of the rights table.
 *
 * There were three, in two voices, and the stale one was the copy an operator
 * read about themselves: the account screen listed eight rights, still named
 * `settings.write` — abolished by D24 and absent from the engine — and
 * rendered every other right as a raw dotted identifier.
 *
 * Two voices are kept on purpose, because two screens are asking different
 * questions. A list of what you may do wants the imperative ("See the
 * catalog"); the roles matrix wants the scope the checkbox covers ("Products,
 * variants, categories, collections, media"). Merging them would silently
 * rewrite one screen's text with the other's.
 *
 * The labels are not fetched from `GET /api/admin/roles`: that route is behind
 * `roles.write`, so a manager reading their own account would get a 403 and no
 * labels at all.
 *
 * RIGHT_ORDER is the engine's own order from core/rights.go — a Go test parses
 * these three tables and fails if any of them drifts from `AllRights`.
 */

/** Every right, in the order the engine declares them. */
export const RIGHT_ORDER = [
    "catalog.read",
    "catalog.write",
    "inventory.read",
    "inventory.write",
    "discounts.read",
    "discounts.write",
    "taxes.read",
    "taxes.write",
    "locations.read",
    "locations.write",
    "orders.read",
    "orders.write",
    "orders.fulfill",
    "orders.refund",
    "customers.read",
    "team.read",
    "team.write",
    "roles.write",
    "data.export",
    "data.import",
    "store.operate",
    "shipping.read",
    "shipping.write",
    "channels.read",
    "channels.write",
    "plugins.read",
    "plugins.write",
    "notifications.read",
    "notifications.write",
    "collections.read",
    "collections.write",
    "categories.read",
    "categories.write",
    "media.read",
    "media.write",
    "pricing.read",
    "pricing.write",
    "groups.read",
    "groups.write",
    "reports.read",
    "carts.read",
    "payouts.read",
    "store.write",
    "apikeys.read",
    "apikeys.write",
    "reports.write",
    "vendors.read",
    "vendors.write",
];

/** A right reads better as a sentence than as a dotted identifier. */
export const RIGHT_LABELS = {
    "catalog.read": "See the catalog",
    "catalog.write": "Edit products and categories",
    "inventory.read": "See stock levels",
    "inventory.write": "Adjust stock",
    "discounts.read": "See discounts",
    "discounts.write": "Create and edit discounts",
    "taxes.read": "See tax rates",
    "taxes.write": "Edit tax rates",
    "locations.read": "See locations",
    "locations.write": "Edit locations",
    "orders.read": "See orders",
    "orders.write": "Place, edit and cancel orders",
    "orders.fulfill": "Fulfil and ship orders",
    "orders.refund": "Refund money",
    "customers.read": "See customers",
    "team.read": "See the team",
    "team.write": "Invite and manage the team",
    "roles.write": "Change what each role may do",
    "data.export": "Export the catalog and orders",
    "data.import": "Import the catalog and orders",
    "store.operate": "Run health checks, maintenance and the outbox",
    "shipping.read": "See delivery zones and rates",
    "shipping.write": "Set what delivery costs",
    "channels.read": "See the sales channels",
    "channels.write": "Open and close sales channels",
    "plugins.read": "See which integrations are on",
    "plugins.write": "Switch integrations on and hold their keys",
    "notifications.read": "See the messages the store sends",
    "notifications.write": "Reword the messages the store sends",
    "collections.read": "See collections",
    "collections.write": "Curate collections",
    "categories.read": "See the category tree",
    "categories.write": "Edit the category tree",
    "media.read": "See the file library",
    "media.write": "Upload and remove files",
    "pricing.read": "See price lists",
    "pricing.write": "Edit price lists",
    "groups.read": "See customer groups",
    "groups.write": "Edit customer groups",
    "reports.read": "See the reports",
    "carts.read": "See abandoned carts",
    "payouts.read": "See what each gateway owes",
    "store.write": "Edit the store's name, address and contact details",
    "apikeys.read": "See which API keys exist",
    "apikeys.write": "Create and revoke API keys",
    "reports.write": "Write custom SQL reports",
    "vendors.read": "See the sellers on this store",
    "vendors.write": "Add sellers, approve them, and set what they are offering",
};

/*
 * One line per right, in the store's own words. The API sends the names; a row
 * labelled `orders.refund` and nothing else asks the person granting it to
 * already know what it covers.
 */
export const RIGHT_SCOPES = {
    "catalog.read": "Products, variants, categories, collections, media",
    "catalog.write": "Editing any of them",
    "inventory.read": "Stock levels and the low-stock report",
    "inventory.write": "Stock takes, adjustments and transfers",
    "discounts.read": "Discount codes and what they take off",
    "discounts.write": "Creating, editing and ending discounts",
    "taxes.read": "The tax rates orders are charged at",
    "taxes.write": "Changing what every future order collects",
    "locations.read": "The places stock lives",
    "locations.write": "Opening, closing and choosing the default",
    "orders.read": "Orders and what customers bought",
    "orders.write": "Placing, editing, cancelling, settling payment",
    "orders.fulfill": "Fulfilling and shipping",
    "orders.refund": "Sending money back out of the store",
    "customers.read": "Orders grouped by who placed them — personal data",
    "team.read": "Who is on the team, and who has been invited",
    "team.write": "Inviting, removing and changing roles",
    "roles.write": "This screen — what each role may do",
    "data.export": "The catalog or every order, as a file",
    "data.import": "Changing prices and stock in bulk, from a file",
    "store.operate": "Health, maintenance, and the outbox — which carries buyers' data",
    "shipping.read": "The zones a buyer is charged delivery under",
    "shipping.write": "What every future order collects for delivery",
    "channels.read": "Where the catalogue is sold",
    "channels.write": "Opening and closing those places",
    "plugins.read": "Which integrations this build carries, and which are on",
    "plugins.write": "Their settings — where a gateway's live keys live",
    "notifications.read": "The catalogue of messages the store sends",
    "notifications.write": "Their wording, on every channel",
    "collections.read": "The curated lists a storefront shows",
    "collections.write": "Creating them and choosing what is in one",
    "categories.read": "The tree products are filed under, and its attributes",
    "categories.write": "Moving, renaming and deleting branches of it",
    "media.read": "The file library",
    "media.write": "Uploading, linking and deleting files",
    "pricing.read": "Trade and wholesale price lists",
    "pricing.write": "What a group pays, which is money off by another name",
    "groups.read": "Which customers are grouped together",
    "groups.write": "Creating groups and moving people between them",
    "reports.read": "Sales and best sellers — the shape, without the orders",
    "carts.read": "Baskets nobody came back to",
    "payouts.read": "What each gateway collected, refunded and still owes",
    "store.write": "What the store calls itself on an invoice, a slip and a receipt",
    "apikeys.read": "The keys other systems use, and when each was last seen",
    "apikeys.write": "Minting a credential — and a key can be given any role, including one above yours",
    "reports.write": "Writing SQL that reads any table in the store, and deciding what everyone with reports.read may see",
    "vendors.read": "Who sells through this shop, what they charge and how much they are holding",
    "vendors.write": "Letting a seller in or stopping them, their commission, and the price on anything they offer",
};

/*
 * Both lookups fall back to the identifier rather than to an empty string. A
 * right this table has not caught up with is still a row the operator is being
 * asked about, and `orders.refund` is a poor label but an honest one; nothing
 * at all is a checkbox with no question attached to it.
 */

/** rightLabel is the imperative gloss: what holding this right lets you do. */
export function rightLabel(right) {
    return RIGHT_LABELS[right] || fromServer[right]?.label || right;
}

/** rightScope is the scope sentence: what the right covers. */
export function rightScope(right) {
    return RIGHT_SCOPES[right] || fromServer[right]?.scope || right;
}

/*
 * The same rights, read as a grid.
 *
 * Every right the engine has is `resource.verb`, which is a table nobody was
 * drawing: the roles screen listed twenty-one dotted names down one side, so
 * "what may this role do to orders" meant finding four rows that happened to
 * share a prefix. Split on the dot and the answer is one line.
 *
 * Both tables below are labels only. Anything they have not caught up with
 * falls back to the identifier with its first letter raised, so a resource or
 * a verb added to the engine appears here as a row rather than disappearing.
 */
/*
 * Words a module sent with its own rights, keyed by right.
 *
 * The tables below cover core, which the panel is built beside and can have a
 * table for. A module's rights are declared in the module, so the panel cannot
 * know them ahead of time — the roles matrix carries them and the Roles screen
 * hands them here before it draws.
 */
let fromServer = {};

/** learnRights takes the catalogue the roles matrix sent. */
export function learnRights(catalogue) {
    const next = {};
    for (const entry of catalogue ?? []) {
        if (entry?.right) next[entry.right] = entry;
    }
    fromServer = next;
}

export const RESOURCE_LABELS = {
    catalog: "Catalog",
    inventory: "Inventory",
    discounts: "Discounts",
    taxes: "Tax",
    locations: "Locations",
    orders: "Orders",
    customers: "Customers",
    team: "Team",
    roles: "Roles",
    data: "Import and export",
    store: "Store",
    shipping: "Shipping",
    channels: "Channels",
    plugins: "Plugins",
    notifications: "Notifications",
    // Brought by modules. Named here all the same: the panel ships beside
    // these modules even when a given build does not install them, and a
    // resource with no name falls back to its own key raised, which reads as
    // a bug rather than as a heading.
    collections: "Collections",
    categories: "Categories",
    media: "Files",
    pricing: "Price lists",
    groups: "Customer groups",
    reports: "Reports",
    carts: "Abandoned carts",
    payouts: "Payouts",
    accounts: "Accounts",
    agent: "Agent",
    reviews: "Reviews",
    pages: "Pages",
    menus: "Menus",
    faq: "FAQ",
    contact: "Contact messages",
    newsletter: "Newsletter",
    wishlists: "Wishlists",
    invoices: "Invoices",
    webhooks: "Webhooks",
};

export const VERB_LABELS = {
    read: "Read",
    write: "Write",
    moderate: "Moderate",
    erase: "Erase",
    dispatch: "Dispatch",
    fulfill: "Fulfil",
    refund: "Refund",
    export: "Export",
    import: "Import",
    operate: "Operate",
};

const titleCase = (s) => (s ? s[0].toUpperCase() + s.slice(1) : s);

/** resourceLabel is the grid's left column. */
export function resourceLabel(key) {
    return RESOURCE_LABELS[key] || titleCase(key);
}

/** verbLabel is what one cell is called. */
export function verbLabel(verb) {
    return VERB_LABELS[verb] || titleCase(verb);
}

/*
 * The sections the sidebar is cut into, and which resources answer for each.
 *
 * Grouping the grid this way rather than leaving fifteen rows in a column is
 * what lets somebody granting access think in the shape they already navigate:
 * "can this person touch Products" is a block, not five rows that happen to
 * share a neighbourhood. The order is the sidebar's own.
 *
 * A resource named nowhere here still appears, under "Other" — a right added
 * to the engine must never fall out of the only screen that grants it just
 * because this table has not caught up.
 */
export const RIGHT_SECTIONS = [
    { section: "Orders", resources: ["orders", "carts", "invoices", "payouts"] },
    {
        section: "Products",
        resources: ["catalog", "collections", "categories", "inventory", "pricing", "reviews"],
    },
    {
        section: "Customers",
        resources: ["customers", "groups", "accounts", "contact", "newsletter", "wishlists"],
    },
    { section: "Discounts", resources: ["discounts"] },
    { section: "Notifications", resources: ["notifications"] },
    { section: "Content", resources: ["media", "pages", "menus", "faq"] },
    { section: "Reports", resources: ["reports"] },
    { section: "Plugins", resources: ["plugins"] },
    // The big one, and honestly so: Settings is where a store is configured,
    // and eight of these are things only an owner would ordinarily touch.
    {
        section: "Settings",
        resources: [
            "shipping",
            "channels",
            "taxes",
            "locations",
            "team",
            "roles",
            "data",
            "webhooks",
            "agent",
            "store",
            "apikeys",
        ],
    },
];

/**
 * rightsBySection groups the grid rows under the sidebar's own sections.
 *
 * Returns `[{ section, rows }]`, dropping a section whose resources this
 * engine does not have, so a build without a surface shows no empty heading.
 */
export function rightsBySection(all) {
    const rows = rightsByResource(all);
    const byKey = new Map(rows.map((r) => [r.key, r]));
    const out = [];
    for (const { section, resources } of RIGHT_SECTIONS) {
        const mine = [];
        for (const key of resources) {
            const row = byKey.get(key);
            if (row) {
                mine.push(row);
                byKey.delete(key);
            }
        }
        if (mine.length) out.push({ section, rows: mine });
    }
    // Whatever the table above has not caught up with.
    const rest = [...byKey.values()];
    if (rest.length) out.push({ section: "Other", rows: rest });
    return out;
}

/**
 * rightsByResource groups a list of rights into grid rows.
 *
 * The order is the engine's own (`AllRights`), both down the rows and across
 * each row, for the reason core/rights.go gives for never inserting into that
 * list: an operator learns where a thing sits, and re-sorting moves it.
 */
export function rightsByResource(all) {
    const rows = [];
    const byKey = new Map();
    for (const right of all ?? []) {
        const dot = right.indexOf(".");
        // A right with no verb is its own resource rather than a dropped row.
        const key = dot < 0 ? right : right.slice(0, dot);
        const verb = dot < 0 ? right : right.slice(dot + 1);
        let row = byKey.get(key);
        if (!row) {
            row = { key, label: resourceLabel(key), rights: [] };
            byKey.set(key, row);
            rows.push(row);
        }
        row.rights.push({ right, verb, label: verbLabel(verb) });
    }
    return rows;
}
