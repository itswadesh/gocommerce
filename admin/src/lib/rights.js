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
    "store.operate": "Run health checks and maintenance",
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
    "store.operate": "The health report, the maintenance passes and their record",
};

/*
 * Both lookups fall back to the identifier rather than to an empty string. A
 * right this table has not caught up with is still a row the operator is being
 * asked about, and `orders.refund` is a poor label but an honest one; nothing
 * at all is a checkbox with no question attached to it.
 */

/** rightLabel is the imperative gloss: what holding this right lets you do. */
export function rightLabel(right) {
    return RIGHT_LABELS[right] || right;
}

/** rightScope is the scope sentence: what the right covers. */
export function rightScope(right) {
    return RIGHT_SCOPES[right] || right;
}
