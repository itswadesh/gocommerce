import { COUNTRIES } from "$lib/countries.js";

/**
 * Words and colours for ext/b2b's states, shared by its screens.
 *
 * One copy because the same company appears on four of them — its own page,
 * the list, a quote's header and a receivables row — and a status that reads
 * "On hold" in one place and "on_hold" in another is two facts to an operator.
 *
 * The colours are PocketBase's four `.label` variants and nothing else, chosen
 * by what wants attention: a quote waiting to be priced and an approval
 * waiting on the buyer are amber because somebody has to act; the ordinary
 * finished state is green and quiet.
 */

export const COMPANY_STATUSES = [
    { value: "active", label: "Active", hint: "Orders on account as normal" },
    { value: "on_hold", label: "On hold", hint: "Still buys, but pays up front until the hold is lifted" },
    { value: "closed", label: "Closed", hint: "Cannot order at all" },
];

export function companyStatusLabel(status) {
    return COMPANY_STATUSES.find((s) => s.value === status)?.label ?? status;
}

export function companyStatusClass(status) {
    return { active: "label-success", on_hold: "label-warning", closed: "label-danger" }[status] ?? "";
}

/* What each role may do inside its company. The company's business, not the
   store's: these are not operator rights, and nothing in the panel checks them. */
export const ROLES = [
    { value: "admin", label: "Admin", hint: "Runs the company's account: buyers, invitations and approvals" },
    { value: "approver", label: "Approver", hint: "Orders without approval and decides other buyers' requests" },
    { value: "buyer", label: "Buyer", hint: "Places orders; one over the approval threshold waits for an approver" },
];

export function roleLabel(role) {
    return ROLES.find((r) => r.value === role)?.label ?? role;
}

/* `expired` is read, not stored: the engine reports a sent quote past its date
   as expired, so it is a filter value like the others. */
export const QUOTE_STATUSES = [
    { value: "requested", label: "Requested", hint: "Waiting for a price" },
    { value: "quoted", label: "Sent", hint: "Priced and waiting for the buyer" },
    { value: "accepted", label: "Accepted", hint: "Turned into an order" },
    { value: "declined", label: "Declined" },
    { value: "cancelled", label: "Cancelled" },
    { value: "expired", label: "Expired", hint: "Sent, and the buyer did not take it in time" },
];

export function quoteStatusLabel(status) {
    return QUOTE_STATUSES.find((s) => s.value === status)?.label ?? status;
}

export function quoteStatusClass(status) {
    return { requested: "label-warning", quoted: "label-info", accepted: "label-success", declined: "label-danger" }[status] ?? "";
}

export const APPROVAL_STATUSES = [
    { value: "pending", label: "Waiting", hint: "For one of the company's approvers" },
    { value: "placing", label: "Placing", hint: "Approved, and the order is being placed" },
    { value: "approved", label: "Approved", hint: "The order was placed" },
    { value: "rejected", label: "Rejected" },
    { value: "cancelled", label: "Withdrawn", hint: "The buyer took it back" },
];

export function approvalStatusLabel(status) {
    return APPROVAL_STATUSES.find((s) => s.value === status)?.label ?? status;
}

export function approvalStatusClass(status) {
    return { pending: "label-warning", placing: "label-info", approved: "label-success", rejected: "label-danger" }[status] ?? "";
}

/**
 * A company's payment terms as a person says them.
 *
 * Net days mean nothing without a credit limit — a company with no account
 * pays at checkout like anybody else — so that case says so instead of
 * printing a "Net 30" nothing will ever apply.
 */
export function termsLabel(company) {
    if (!company?.credit_limit) return "Pays up front";
    return company.net_days === 0 ? "Due on order" : `Net ${company.net_days}`;
}

/**
 * The value an `<input type="date">` shows for a timestamp, in the reader's
 * own calendar. `toISOString().slice(0, 10)` would be UTC's date, which is a
 * day early for every evening in the Americas.
 */
export function dateInputValue(iso) {
    if (!iso) return "";
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return "";
    const pad = (n) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/**
 * The last second of a picked day, local time, as RFC 3339.
 *
 * End rather than start of the day: "valid until the 20th" means the buyer can
 * still accept on the 20th, and midnight at its start would expire the quote
 * the moment the day the operator typed begins.
 */
export function endOfDay(value) {
    const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(value ?? "");
    if (!m) return null;
    return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]), 23, 59, 59).toISOString();
}

/* What a dealer reports back about an enquiry — not a sales pipeline; the
   pipeline is the dealer's own business. New is amber because nobody has
   answered the person yet. */
export const LEAD_STATUSES = [
    { value: "new", label: "New", hint: "Nobody has answered yet" },
    { value: "contacted", label: "Contacted", hint: "The dealer has been in touch" },
    { value: "won", label: "Won", hint: "It became a sale" },
    { value: "lost", label: "Lost", hint: "It went nowhere" },
];

export function leadStatusLabel(status) {
    return LEAD_STATUSES.find((s) => s.value === status)?.label ?? status;
}

export function leadStatusClass(status) {
    return { new: "label-warning", contacted: "label-info", won: "label-success" }[status] ?? "";
}

/** How a lead reached whoever holds it, as a phrase that follows the name. */
export function routedByLabel(routedBy) {
    return { territory: "by territory", store: "by the store" }[routedBy] ?? "";
}

/** A country's name for its ISO code, or the code itself for one the list lacks. */
export function countryName(code) {
    return COUNTRIES.find((c) => c.value === code)?.label ?? code;
}

/**
 * A territory as one phrase, widest part first: "CA, US · postcodes 941…".
 * An empty part means the whole of the part above it, so it is left out
 * rather than printed as a blank.
 */
export function areaLabel(t) {
    const parts = [t.state ? `${t.state}, ${t.country}` : countryName(t.country)];
    if (t.postal_prefix) parts.push(`postcodes ${t.postal_prefix}…`);
    return parts.join(" · ");
}

/** Where an enquiry came from, as an address line: "94105, CA, US". */
export function leadPlace(lead) {
    return [lead.postal_code, lead.state, lead.country].filter(Boolean).join(", ");
}

/**
 * Companies for a dealer picker or filter, dealers first.
 *
 * A lead can be handed to any company, so every company is offered — but the
 * ones with territories are the dealers, and they lead the list. One page of
 * the engine's largest is read; past that, the dealers are read on their own
 * as well, so a store with more companies than one page still offers every
 * dealer. `dealersOnly` is for a filter where a company without territories
 * would only ever match nothing.
 */
export async function loadDealers(api, { dealersOnly = false } = {}) {
    const first = await api.get("/api/admin/x/b2b/companies?limit=200" + (dealersOnly ? "&dealers=true" : ""));
    let list = first.data ?? [];
    const more = (first.meta?.total ?? 0) > list.length;
    if (more && !dealersOnly) {
        const dealers = (await api.get("/api/admin/x/b2b/companies?limit=200&dealers=true")).data ?? [];
        const seen = new Set(list.map((c) => c.id));
        list = [...list, ...dealers.filter((c) => !seen.has(c.id))];
    }
    const ranked = [...list].sort(
        (a, b) => Number(b.territory_count > 0) - Number(a.territory_count > 0) || a.name.localeCompare(b.name),
    );
    return { dealers: ranked, more };
}

/**
 * A company as a picker option: the name, how much it covers, and why it
 * might not take a lead.
 */
export function dealerOption(c) {
    const parts = [];
    if (c.territory_count > 0) parts.push(`${c.territory_count} ${c.territory_count === 1 ? "territory" : "territories"}`);
    else if (c.territory_count === 0) parts.push("no territories");
    if (c.status === "closed") parts.push("closed, takes no leads");
    else if (c.status === "on_hold") parts.push("on hold");
    return { value: String(c.id), label: parts.length ? `${c.name} — ${parts.join(", ")}` : c.name, short: c.name };
}
