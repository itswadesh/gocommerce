/**
 * The trade portal's session, and the one client that carries it (D77).
 *
 * A buyer's session is an ext/identity shopper session, and it never shares a
 * code path with the store's staff session (api.js) or the platform's token
 * (platform.svelte.js). Each client holds its own credential under its own
 * key and ends only its own: a 401 here signs the buyer out and touches
 * nothing of a staff session open in another tab of the same browser. The
 * client also refuses to call /api/admin at all, so a buyer's token cannot be
 * sent there by a slip of a path.
 *
 * The session is kept in localStorage, not in sessionStorage as the platform
 * token is. That token never expires and creates and deletes whole stores;
 * this one expires on its own (thirty days without use, sliding), is
 * refreshed rather than rotated so every open tab keeps working, is revoked
 * on the server when the buyer signs out, and can only buy inside the limits
 * the company's credit, threshold and approvers set. A buyer opens the portal
 * from links — an invitation, an approval request, an order email — each in
 * a new tab, and sessionStorage would ask them to sign in every time.
 */

import { browser } from "$app/environment";
import { ApiError } from "$lib/api.js";
import { formatMoney } from "$lib/format.js";

const SESSION_KEY = "gocommerce_trade_session";
// Per account, so two buyers taking turns at one browser never fill each
// other's basket.
const BASKET_PREFIX = "gocommerce_trade_basket_";

function readJSON(key) {
    if (!browser) return null;
    try {
        return JSON.parse(localStorage.getItem(key) || "null");
    } catch {
        return null;
    }
}

function writeJSON(key, value) {
    if (!browser) return;
    try {
        if (value === null || value === undefined) localStorage.removeItem(key);
        else localStorage.setItem(key, JSON.stringify(value));
    } catch {
        /* storage disabled: the portal works until the tab reloads */
    }
}

const saved = readJSON(SESSION_KEY);

export const trade = $state({
    token: saved?.token || "",
    expiresAt: saved?.expires_at || "",
    /** The identity account: id, email, email_verified, name, phone. */
    account: saved?.account || null,
    /** GET /x/b2b/me: the company, the buyer's role in it, and its credit. */
    me: null,
    /** Why /x/b2b/me refused, when it did — usually "not a buyer for any company". */
    meError: null,
    meLoading: false,
    /* Set when a session that was working stops: the sign-in form says why it
       is back rather than appearing for no reason. */
    expired: false,
    /** The store's public face, from GET /api/store. */
    store: { name: "", email: "", phone: "", support_url: "" },
    basket: { token: "", count: 0 },
    /** Requests waiting for this approver or admin to decide. */
    waiting: 0,
    /** Whether the company receives leads: it has territories, or the store handed it some. */
    dealer: false,
});

function persist() {
    writeJSON(SESSION_KEY, trade.token ? { token: trade.token, expires_at: trade.expiresAt, account: trade.account } : null);
}

// ------------------------------------------------------------------ calls

/**
 * call performs one request and unwraps the envelope, as api.js's request
 * does. The bearer token goes only to the module routes under /x/, which are
 * the only ones that read a shopper session; a public /api/ route (a basket
 * by its token, the payment methods) is sent nothing it does not need.
 */
async function call(method, path, body, { token = trade.token, list = false, headers = {} } = {}) {
    if (path.startsWith("/api/admin")) {
        // A programming error, not a refusal: no screen here may reach it.
        throw new ApiError(0, "not_for_buyers", "The trade portal does not call the store's admin API.");
    }
    const init = { method, headers: { ...headers } };
    const sent = !!token && path.startsWith("/x/");
    if (sent) init.headers["Authorization"] = "Bearer " + token;
    if (body !== undefined) {
        init.headers["Content-Type"] = "application/json";
        init.body = JSON.stringify(body);
    }
    let response;
    try {
        response = await fetch(path, init);
    } catch {
        throw new ApiError(0, "network_error", "We could not reach the store. Check your connection and try again.");
    }
    if (response.status === 204) return null;
    const payload = await response.json().catch(() => null);
    if (!response.ok) {
        const e = payload?.error;
        const err = new ApiError(response.status, e?.code || "error", e?.message || response.statusText, e?.details);
        // Only the session in use ends, and only when it was the credential
        // refused. A password being tried at the sign-in form is not a session
        // yet, and its refusal is the form's to show.
        if (response.status === 401 && sent && token === trade.token) {
            endSession(true);
            err.handled = true;
        }
        throw err;
    }
    if (list || payload?.meta) return { data: payload?.data ?? [], meta: payload?.meta ?? null };
    return payload?.data ?? payload;
}

export const tradeApi = {
    get: (path) => call("GET", path),
    list: (path) => call("GET", path, undefined, { list: true }),
    post: (path, body = {}, headers = {}) => call("POST", path, body, { headers }),
    patch: (path, body) => call("PATCH", path, body),
    put: (path, body) => call("PUT", path, body),
    delete: (path) => call("DELETE", path),
};

// ---------------------------------------------------------------- session

function startSession(auth) {
    trade.token = auth.token;
    trade.expiresAt = auth.expires_at || "";
    trade.account = auth.record || null;
    trade.expired = false;
    persist();
    trade.basket = { token: readBasket(), count: 0 };
}

function endSession(expired = false) {
    trade.token = "";
    trade.expiresAt = "";
    trade.account = null;
    trade.me = null;
    trade.meError = null;
    trade.waiting = 0;
    trade.dealer = false;
    trade.basket = { token: "", count: 0 };
    trade.expired = expired;
    persist();
}

/** signIn keeps a session only once identity has issued it. */
export async function signIn(email, password) {
    const auth = await call("POST", "/x/identity/login", { email: email.trim(), password }, { token: "" });
    startSession(auth);
    return auth;
}

export async function register(email, password, name) {
    const auth = await call(
        "POST",
        "/x/identity/register",
        { email: email.trim(), password, name: name.trim() },
        { token: "" },
    );
    startSession(auth);
    return auth;
}

/** adoptSession is a sign-in that came back from another route — a password reset. */
export function adoptSession(auth) {
    startSession(auth);
}

/**
 * refresh extends the session and re-reads the account. Identity slides the
 * expiry without rotating the token, so a refresh in one tab never signs out
 * another.
 */
export async function refresh() {
    if (!trade.token) return null;
    const auth = await call("POST", "/x/identity/refresh", {});
    trade.expiresAt = auth.expires_at || trade.expiresAt;
    trade.account = auth.record || trade.account;
    persist();
    return auth;
}

export async function signOut() {
    const token = trade.token;
    endSession(false);
    if (!token) return;
    // Revoked on the server too, so the token is dead even where a copy of it
    // survives — a second tab, a backup of the browser profile.
    try {
        await call("POST", "/x/identity/logout", {}, { token });
    } catch {
        /* already gone, or the store is unreachable: signed out here either way */
    }
}

/**
 * loadMe reads the buyer's company. A 403 is an account that buys for no
 * company, which the shell explains rather than toasting.
 */
export async function loadMe() {
    if (!trade.token) return null;
    trade.meLoading = true;
    try {
        trade.me = await tradeApi.get("/x/b2b/me");
        trade.meError = null;
        loadBadges();
        return trade.me;
    } catch (err) {
        if (!err.handled) trade.meError = err;
        return null;
    } finally {
        trade.meLoading = false;
    }
}

/** The counts the navigation carries. Silent: a badge that cannot load is not worth a toast. */
async function loadBadges() {
    const role = trade.me?.role;
    if (role === "admin" || role === "approver") {
        tradeApi
            .list("/x/b2b/approvals?status=pending&limit=1")
            .then((r) => (trade.waiting = r.meta?.total ?? 0))
            .catch(() => {});
        if ((trade.me?.company?.territory_count ?? 0) > 0) {
            trade.dealer = true;
        } else {
            // A company with no territory can still be handed a lead by the
            // store, and a lead nobody can see is a customer nobody calls.
            tradeApi
                .list("/x/b2b/leads?limit=1")
                .then((r) => (trade.dealer = (r.meta?.total ?? 0) > 0))
                .catch(() => (trade.dealer = false));
        }
    } else {
        trade.waiting = 0;
        trade.dealer = false;
    }
}

export const refreshBadges = loadBadges;

export async function loadStore() {
    try {
        const s = await call("GET", "/api/store", undefined, { token: "" });
        trade.store = { ...trade.store, ...s };
    } catch {
        /* a store without the route, or unreachable: the portal names itself instead */
    }
}

// ----------------------------------------------------------------- basket

function basketKey() {
    return trade.account?.id ? BASKET_PREFIX + trade.account.id : "";
}

function readBasket() {
    const key = basketKey();
    return key ? readJSON(key) || "" : "";
}

/** rememberBasket makes a basket the buyer's current one, or forgets it with "". */
export function rememberBasket(token, count = trade.basket.count) {
    const key = basketKey();
    if (key) writeJSON(key, token || null);
    trade.basket = { token: token || "", count: token ? count : 0 };
}

/**
 * loadBasket reads the current basket by its token. One that has been
 * checked out, or that the store no longer has, is forgotten: the next line
 * added opens a new one.
 */
export async function loadBasket() {
    const token = trade.basket.token || readBasket();
    if (!token) {
        trade.basket = { token: "", count: 0 };
        return null;
    }
    try {
        const cart = await call("GET", `/api/carts/${encodeURIComponent(token)}`);
        if (cart.status === "converted") {
            rememberBasket("");
            return null;
        }
        rememberBasket(token, cart.item_count ?? 0);
        return cart;
    } catch (err) {
        if (err.status === 404) {
            rememberBasket("");
            return null;
        }
        throw err;
    }
}

/**
 * repeatOrder puts a past order's lines into the buyer's current basket, at
 * today's prices, and says which could not go in. A basket that was checked
 * out in another tab cannot take them, so the repeat opens a new one.
 */
export async function repeatOrder(orderId) {
    const path = `/x/b2b/orders/${orderId}/reorder`;
    let fill;
    try {
        fill = await tradeApi.post(path, trade.basket.token ? { cart_id: trade.basket.token } : {});
    } catch (err) {
        if (!trade.basket.token || err.status !== 409) throw err;
        rememberBasket("");
        fill = await tradeApi.post(path, {});
    }
    noteBasket(fill.cart);
    return fill;
}

/** noteBasket records what a call that returned the basket says it holds. */
export function noteBasket(cart) {
    if (!cart) return;
    if (cart.status === "converted") rememberBasket("");
    else rememberBasket(cart.id, cart.item_count ?? 0);
}

// ------------------------------------------------------------------ words

export function isApprover() {
    return trade.me?.role === "admin" || trade.me?.role === "approver";
}

/* What each role may do, as the buyer would put it. */
export const ROLE_WORDS = {
    admin: { label: "Account admin", hint: "Orders, approves, and manages the team" },
    approver: { label: "Approver", hint: "Orders without approval and decides other buyers' requests" },
    buyer: { label: "Buyer", hint: "Orders; anything over the approval limit waits for an approver" },
};

export function roleWord(role) {
    return ROLE_WORDS[role]?.label ?? role;
}

export const APPROVAL_WORDS = {
    pending: { label: "Waiting for approval", tone: "label-warning" },
    placing: { label: "Being placed", tone: "label-info" },
    approved: { label: "Approved", tone: "label-success" },
    rejected: { label: "Turned down", tone: "label-danger" },
    cancelled: { label: "Withdrawn", tone: "" },
};

export const QUOTE_WORDS = {
    requested: { label: "Waiting for a price", tone: "label-warning" },
    quoted: { label: "Ready to accept", tone: "label-info" },
    awaiting_approval: { label: "Waiting for approval", tone: "label-warning" },
    accepted: { label: "Accepted", tone: "label-success" },
    declined: { label: "Declined", tone: "label-danger" },
    cancelled: { label: "Cancelled", tone: "" },
    expired: { label: "Expired", tone: "" },
};

export const LEAD_WORDS = {
    new: { label: "New", hint: "Nobody has answered yet", tone: "label-warning" },
    contacted: { label: "Contacted", hint: "You have been in touch", tone: "label-info" },
    won: { label: "Won", hint: "It became a sale", tone: "label-success" },
    lost: { label: "Lost", hint: "It went nowhere", tone: "" },
};

export function word(table, status) {
    return table[status] ?? { label: status, tone: "" };
}

/** An order's state as a buyer reads it: where it is, and whether it is paid. */
export function orderWords(o) {
    const status =
        {
            pending: "Received",
            confirmed: "Confirmed",
            partial: "Partly shipped",
            shipped: "Shipped",
            delivered: "Delivered",
            cancelled: "Cancelled",
        }[o.status] ?? o.status;
    const tone =
        { delivered: "label-success", shipped: "label-info", partial: "label-warning", cancelled: "label-danger" }[
            o.status
        ] ?? "";
    let payment;
    if (o.payment_status === "paid") payment = { label: "Paid", tone: "label-success" };
    else if (o.payment_status === "refunded") payment = { label: "Refunded", tone: "" };
    else if (o.overdue) payment = { label: "Overdue", tone: "label-danger" };
    else if (o.on_account) payment = { label: "On account", tone: "" };
    else payment = { label: "Not paid yet", tone: "label-warning" };
    return { status, tone, payment };
}

/** A company's payment terms, said plainly. */
export function termsWords(company) {
    if (!company?.credit_limit) return "Pay when you order";
    return company.net_days === 0 ? "Due when you order" : `Pay within ${company.net_days} days`;
}

/**
 * Why a line did not go into the basket, in the buyer's words. The engine's
 * own message is kept where it says something the reason does not — "only 3
 * left in stock" — and replaced where it is about variants and ids.
 */
export function rejectedWords(line) {
    switch (line.reason) {
        case "not_found":
            return "We don't sell anything with this code.";
        case "inactive":
            return "This item isn't available to order at the moment.";
        case "insufficient_stock":
            // "only 0 left in stock" is true and reads like a joke.
            if (/\bonly 0\b/i.test(line.message ?? "")) return "Out of stock at the moment.";
            return sentence(line.message) || "There isn't enough in stock.";
        case "invalid":
            return sentence(line.message) || "Check the quantity.";
        default:
            return sentence(line.message) || "This line could not be added.";
    }
}

/** sentence capitalises an engine message and ends it, so it reads as one. */
export function sentence(text) {
    const t = String(text ?? "").trim();
    if (!t) return "";
    const s = t[0].toUpperCase() + t.slice(1);
    return /[.!?]$/.test(s) ? s : s + ".";
}

/**
 * explain turns a refusal into what the buyer should read. Most of the
 * engine's messages are already plain; the few that speak in minor units or
 * name an API route are rewritten here.
 */
export function explain(err) {
    if (!err) return "";
    if (typeof err === "string") return sentence(err);
    switch (err.code) {
        case "credit_limit_exceeded": {
            const available = trade.me?.credit?.available;
            return available
                ? `This order is over your company's credit limit. ${formatMoney(available)} of credit is available.`
                : "This order is over your company's credit limit.";
        }
        case "email_unverified":
            return "Confirm your email address first. We can send you the link again from your account menu.";
        case "network_error":
            return err.message;
    }
    if (err.status === 409 && Array.isArray(err.details) && err.details.length) {
        return "Some lines changed since you added them. " + err.details.map(conflictWords).join(" ");
    }
    return sentence(err.message) || "Something went wrong. Please try again.";
}

function conflictWords(d) {
    const sku = d.sku ? `${d.sku}: ` : "";
    switch (d.reason) {
        case "insufficient_stock":
            return `${sku}only ${d.available ?? 0} in stock.`;
        case "inactive":
            return `${sku}no longer available.`;
        case "price_changed":
            return `${sku}the price changed.`;
        default:
            return `${sku}${d.reason}.`;
    }
}

/**
 * The portal's navigation, in the order a buyer works.
 *
 * Each item says who sees it. Three screens are being built in ext/b2b and
 * slot in where the comments are, each as one more line here: the company's
 * catalogue after Quick order, statements after Orders, and a spreadsheet
 * upload on the Quick order screen itself rather than as a destination.
 */
export const NAV = [
    { href: "/portal", label: "Overview", icon: "ri-home-5-line", exact: true },
    { href: "/portal/quick-order", label: "Quick order", icon: "ri-flashlight-line" },
    // Catalogue: { href: "/portal/catalogue", label: "Catalogue", icon: "ri-store-3-line" },
    { href: "/portal/basket", label: "Your basket", icon: "ri-shopping-basket-2-line", badge: "basket" },
    { href: "/portal/orders", label: "Orders", icon: "ri-file-list-3-line" },
    // Statements: { href: "/portal/statements", label: "Statements", icon: "ri-bill-line" },
    { href: "/portal/approvals", label: "Approvals", icon: "ri-checkbox-circle-line", badge: "waiting" },
    { href: "/portal/quotes", label: "Quotes", icon: "ri-price-tag-3-line" },
    { href: "/portal/team", label: "Team", icon: "ri-team-line", when: () => trade.me?.role === "admin" },
    { href: "/portal/leads", label: "Leads", icon: "ri-user-received-2-line", when: () => isApprover() && trade.dealer },
];

export function visibleNav() {
    return NAV.filter((item) => !item.when || item.when());
}

export function badgeFor(item) {
    if (item.badge === "basket") return trade.basket.count;
    if (item.badge === "waiting") return trade.waiting;
    return 0;
}
