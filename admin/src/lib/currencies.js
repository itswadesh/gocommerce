/**
 * ISO 4217 currencies, name first: "Indian Rupee (INR)".
 *
 * Built from the browser's own list (Intl), so it stays current without a
 * table here to maintain. The engine takes any three-letter code (D14, D85);
 * this is only how one is picked. A browser without the list gets the codes a
 * store is likeliest to want.
 */
const FALLBACK = ["AED", "AUD", "BDT", "CAD", "CHF", "CNY", "EUR", "GBP", "HKD", "IDR", "INR", "JPY", "KES", "LKR",
    "MYR", "NGN", "NPR", "NZD", "PHP", "PKR", "SAR", "SGD", "THB", "USD", "ZAR"];

function codes() {
    try {
        return Intl.supportedValuesOf("currency");
    } catch {
        return FALLBACK;
    }
}

function names() {
    try {
        return new Intl.DisplayNames(["en"], { type: "currency" });
    } catch {
        return null;
    }
}

const display = names();

export const CURRENCIES = codes()
    .filter((code) => /^[A-Z]{3}$/.test(code))
    .map((code) => {
        const name = display?.of(code);
        return { value: code, label: name && name !== code ? `${name} (${code})` : code };
    })
    .sort((a, b) => a.label.localeCompare(b.label));

/** currencyOptions keeps a saved code that is not on the list as a choice. */
export function currencyOptions(saved) {
    if (!saved || CURRENCIES.some((c) => c.value === saved)) return CURRENCIES;
    return [{ value: saved, label: saved }, ...CURRENCIES];
}
