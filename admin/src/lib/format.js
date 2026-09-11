/**
 * Formatting helpers.
 *
 * The API sends money as an integer count of the currency's minor unit plus
 * its code, and deliberately never a formatted string — because how many
 * decimal places a currency has, and where the symbol goes, are the client's
 * business. This is the client, so it is this file's business.
 */

/**
 * minorDigits returns how many decimal places a currency uses: 2 for USD, 0 for
 * JPY, 3 for KWD.
 *
 * A missing code throws rather than assuming two. Assuming two is what turned
 * ¥500 into ¥50,000 on the discounts screen, and the failure was invisible
 * because the same assumption read it back as "500.00".
 */
export function minorDigits(currency) {
    if (!currency) {
        throw new Error("a currency code is required — JPY has 0 decimals, KWD has 3");
    }
    try {
        const fmt = new Intl.NumberFormat(undefined, { style: "currency", currency });
        return fmt.resolvedOptions().maximumFractionDigits ?? 2;
    } catch {
        // A present-but-unknown code is a store using something Intl has not
        // heard of; two decimals is the only guess available and the code was
        // stated, so nothing is being assumed on the operator's behalf.
        return 2;
    }
}

/*
 * Parsing is the other half of D14's bargain. The engine sends minor units plus
 * a code and leaves the formatting to whoever is reading; a reader that formats
 * has to read back in the same terms, and this panel was doing it with
 * `parseFloat`, which treats the majority of the world's decimal mark as a
 * thousands separator. `parseFloat("24,99")` is 24, silently, and every guard
 * in the panel was `isNaN(parseFloat(x))`, which that string passes.
 */

const separatorCache = new Map();

/** The group and decimal marks a locale writes numbers with. */
function separatorsFor(locale) {
    const key = locale || "";
    if (separatorCache.has(key)) return separatorCache.get(key);

    let marks = { group: ",", decimal: "." };
    try {
        const parts = new Intl.NumberFormat(locale).formatToParts(12345.6);
        marks = {
            group: parts.find((p) => p.type === "group")?.value || marks.group,
            decimal: parts.find((p) => p.type === "decimal")?.value || marks.decimal,
        };
    } catch {
        // An unparseable locale tag: the ASCII marks are the safest guess, and
        // the single-separator rule below carries most of the real work anyway.
    }
    separatorCache.set(key, marks);
    return marks;
}

/**
 * parseMoney reads what a person typed, in their own locale, and returns a
 * number or null. Never NaN and never a half-read prefix.
 *
 * The rule that matters: when the string carries exactly one separator and the
 * digits after it are not exactly three, that separator is the decimal mark
 * whatever character it is. "24,99" is 24.99 even in an en-US panel, because
 * nobody types a thousands separator two digits from the end — and a person
 * who has just moved countries still types the mark their keyboard has.
 * Three digits after is the genuinely ambiguous case, and only there does the
 * locale get to decide.
 *
 * `locale` is a parameter, and undefined everywhere in the panel, so the tests
 * can pin one rather than asserting against whatever CI happens to run in.
 */
export function parseMoney(value, locale) {
    if (value === undefined || value === null) return null;

    // fr-FR groups with a narrow no-break space and several locales use the
    // ordinary one; none of them mean anything to the number.
    const cleaned = String(value).replace(/\s+/gu, "");
    if (!cleaned) return null;

    const { group, decimal } = separatorsFor(locale);
    const marks = new Set([group, decimal, ".", ","].filter((c) => c && !/\d/.test(c)));

    const found = [];
    for (let i = 0; i < cleaned.length; i++) {
        if (marks.has(cleaned[i])) found.push(i);
    }

    let normalised;
    if (found.length === 0) {
        normalised = cleaned;
    } else {
        const last = found[found.length - 1];
        const lastChar = cleaned[last];
        const trailing = cleaned.length - last - 1;
        const sameThroughout = found.every((i) => cleaned[i] === lastChar);

        // One separator, and not three digits behind it: a decimal mark.
        // Otherwise the mixed case settles itself — the rightmost of two
        // different marks is the decimal one — and a string written with a
        // single repeated mark is grouped, not fractional.
        const isDecimal =
            (found.length === 1 && trailing !== 3) ||
            (!sameThroughout && found.length > 1) ||
            (found.length === 1 && trailing === 3 && lastChar === decimal);

        const groups = [];
        let cursor = 0;
        const cut = isDecimal ? found.slice(0, -1) : found;
        for (const i of cut) {
            groups.push(cleaned.slice(cursor, i));
            cursor = i + 1;
        }
        groups.push(cleaned.slice(cursor, isDecimal ? last : cleaned.length));

        // Grouping that is not in threes is not grouping — "1,2,3" is a typo,
        // not a number, and reading it as 123 is the kind of quiet repair that
        // puts a wrong price on a product.
        if (groups.length > 1) {
            const first = groups[0].replace(/^-/, "");
            if (!/^\d{1,3}$/.test(first)) return null;
            if (!groups.slice(1).every((g) => /^\d{3}$/.test(g))) return null;
        }

        normalised = groups.join("") + (isDecimal ? "." + cleaned.slice(last + 1) : "");
    }

    if (!/^-?\d+(\.\d+)?$/.test(normalised)) return null;
    const amount = Number(normalised);
    return Number.isFinite(amount) ? amount : null;
}

/** isValidMoney is the guard every money field asks before sending anything. */
export function isValidMoney(value, locale) {
    return parseMoney(value, locale) !== null;
}

/** formatMoney renders a {amount_minor, currency} pair for display. */
export function formatMoney(money) {
    if (!money || money.amount_minor === undefined || money.amount_minor === null) return "—";
    const currency = money.currency || "USD";
    try {
        return new Intl.NumberFormat(undefined, { style: "currency", currency }).format(
            money.amount_minor / 10 ** minorDigits(currency),
        );
    } catch {
        return `${money.amount_minor} ${currency}`;
    }
}

/**
 * toMinor converts what a person typed ("24.99") into minor units, or null.
 *
 * Null, never 0. A price silently set to zero is the one class of bug a
 * commerce panel must not have — so every caller guards with isValidMoney and
 * refuses at the control, and this return is the backstop rather than the
 * message. A null that reaches Go is a no-op on a value field and an absent
 * field on a pointer one, which is a silent zero or a silent nothing.
 */
export function toMinor(value, currency, locale) {
    // The currency is read first, so a call that forgot it throws whatever the
    // value was — a missing code is a bug in the caller, not bad input.
    const digits = minorDigits(currency);
    const amount = parseMoney(value, locale);
    if (amount === null) return null;
    return Math.round(amount * 10 ** digits);
}

/**
 * fromMinor converts minor units back into an editable decimal string.
 *
 * The currency is required for the same reason: this is the display half of
 * the same bug, and reading ¥500 back as "5.00" is how the entry half stayed
 * invisible. The string carries a "." whatever the reader's locale, because it
 * goes into a field parseMoney reads back, and parseMoney settles a two-digit
 * fraction the same way in every locale.
 */
export function fromMinor(amountMinor, currency) {
    if (amountMinor === undefined || amountMinor === null) return "";
    const digits = minorDigits(currency);
    return (amountMinor / 10 ** digits).toFixed(digits);
}

/** formatDate renders an ISO timestamp in the reader's locale. */
export function formatDate(value, opts = {}) {
    if (!value) return "—";
    const date = new Date(value);
    if (isNaN(date.getTime())) return "—";
    return new Intl.DateTimeFormat(undefined, {
        dateStyle: "medium",
        timeStyle: opts.withTime === false ? undefined : "short",
    }).format(date);
}

/** relativeTime renders "3 minutes ago" for recent activity. */
export function relativeTime(value) {
    if (!value) return "—";
    const date = new Date(value);
    if (isNaN(date.getTime())) return "—";

    const seconds = Math.round((date.getTime() - Date.now()) / 1000);
    const units = [
        ["year", 31536000],
        ["month", 2592000],
        ["week", 604800],
        ["day", 86400],
        ["hour", 3600],
        ["minute", 60],
    ];
    const rtf = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
    for (const [unit, size] of units) {
        if (Math.abs(seconds) >= size) return rtf.format(Math.round(seconds / size), unit);
    }
    return rtf.format(Math.round(seconds), "second");
}

/**
 * The `.label` colour for each status. PocketBase's label has exactly four
 * colour variants — info, success, warning, danger — plus the neutral default,
 * so these return one of those five and nothing else.
 */
export function orderStatusClass(status) {
    switch (status) {
        case "delivered":
            return "success";
        case "shipped":
        case "confirmed":
            return "info";
        case "cancelled":
            return "danger";
        default:
            return "";
    }
}

export function paymentStatusClass(status) {
    switch (status) {
        case "paid":
            return "success";
        case "refunded":
            return "warning";
        case "failed":
            return "danger";
        default:
            return "";
    }
}

export function productStatusClass(status) {
    switch (status) {
        case "active":
            return "success";
        case "draft":
            return "warning";
        default:
            return "";
    }
}

/** stockClass colours a stock figure by how worried to be about it. */
export function stockClass(available, tracked = true) {
    if (!tracked) return "txt-hint";
    if (available <= 0) return "txt-danger";
    if (available <= 5) return "txt-warning";
    return "";
}

export function pluralize(count, singular, plural) {
    return count === 1 ? singular : plural || singular + "s";
}

/**
 * The symbol a person reads a price in — "$" for USD, "₹" for INR, "¥" for JPY.
 *
 * The engine never sends this, and should not: money crosses the API as minor
 * units plus a currency code, because the symbol and the separators belong to
 * whoever is reading, not to the store. That reader is this panel, so deriving
 * the symbol is exactly its job.
 *
 * `Intl` is asked rather than a lookup table, so a store that switches to a
 * currency nobody anticipated still gets the right glyph. When it has none it
 * hands back the code itself, which is a fine thing to show and better than a
 * guess.
 */
const symbolCache = new Map();

export function currencySymbol(currency) {
    const code = String(currency || "").toUpperCase();
    if (!code) return "";
    if (symbolCache.has(code)) return symbolCache.get(code);

    let symbol = code;
    try {
        const parts = new Intl.NumberFormat(undefined, {
            style: "currency",
            currency: code,
            currencyDisplay: "narrowSymbol",
        }).formatToParts(0);
        symbol = parts.find((p) => p.type === "currency")?.value || code;
    } catch {
        // An unknown code throws rather than falling back, and the code is a
        // perfectly readable label for it.
    }
    symbolCache.set(code, symbol);
    return symbol;
}
