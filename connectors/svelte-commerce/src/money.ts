/**
 * Money, across the one boundary in this connector where it changes shape.
 *
 * The engine speaks minor units and a currency code, always, and never sends a
 * formatted string (AGENTS.md rule 6). Svelte Commerce's types say `price:
 * number`, and every backend it already talks to means "19.99" by that. So the
 * conversion happens here, once, in the only file allowed to divide money.
 *
 * The exponent is per currency, not a constant 100: the yen writes no decimals
 * and the dinar writes three. Dividing 2500 JPY by 100 would price a ¥2,500
 * item at ¥25. The table mirrors `currencyExponent` in core/transfer_shopify.go
 * — the same list, because a second opinion about the yen is a bug.
 */

const ZERO_DECIMAL = new Set([
	'BIF', 'CLP', 'DJF', 'GNF', 'ISK', 'JPY', 'KMF', 'KRW', 'PYG', 'RWF',
	'UGX', 'VND', 'VUV', 'XAF', 'XOF', 'XPF',
])

const THREE_DECIMAL = new Set(['BHD', 'IQD', 'JOD', 'KWD', 'LYD', 'OMR', 'TND'])

export function currencyExponent(code: string | null | undefined): number {
	const c = (code ?? '').toUpperCase()
	if (ZERO_DECIMAL.has(c)) return 0
	if (THREE_DECIMAL.has(c)) return 3
	return 2
}

/** The engine's money object. */
export type Money = { amount_minor: number; currency: string }

/**
 * Minor units to the major-unit number the storefront renders.
 *
 * Rounded to the currency's own precision rather than left as raw float
 * division: 1999/100 is 19.99 exactly, but 1 / 1000 for a three-decimal
 * currency is not, and a price that renders as 0.0010000000000000002 is the
 * kind of thing nobody notices until a customer screenshots it.
 */
export function toMajor(money: Money | null | undefined): number {
	if (!money) return 0
	const exp = currencyExponent(money.currency)
	if (exp === 0) return money.amount_minor
	return Number((money.amount_minor / 10 ** exp).toFixed(exp))
}

/**
 * Back the other way, for the few places the storefront hands us a number.
 *
 * Rounded, not truncated: 19.99 * 100 is 1998.9999999999998 in binary floating
 * point, and truncating that charges the customer a cent less than the page
 * showed them.
 */
export function toMinor(amount: number, currency: string): number {
	const exp = currencyExponent(currency)
	return Math.round(amount * 10 ** exp)
}
