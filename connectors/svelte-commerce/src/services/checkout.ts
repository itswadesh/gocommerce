import { data, type Fetch } from '../http.js'
import { toMajor, type Money } from '../money.js'

type Fetcher = { fetch?: Fetch }

type EnginePaymentMethod = { code: string; name: string; kind?: string; description?: string }
/**
 * GET /api/checkout answers an object, not a list: the methods, the same set
 * again as bare codes, and the currency they are priced in. `methods` is the
 * one to read — `payment_methods` is the same order without the names.
 */
type EngineCheckoutOptions = {
	currency: string
	methods: EnginePaymentMethod[]
	payment_methods: string[]
}
type EngineRate = { id?: string; code?: string; name: string; price: Money; description?: string }
type EngineCheckoutResult = {
	order?: { id: number; number: string; access_token?: string; payment_status?: string; status?: string }
	redirect_url?: string
	payment_reference?: string
	status?: string
}

/**
 * Checkout: what this store accepts, what shipping costs, and placing the order.
 *
 * The engine draws a hard line here that the storefront should not blur — a
 * cart becomes an order in one call, and that call is the only thing that
 * moves stock and takes money. There is no "reserve then confirm" for this
 * connector to model.
 */
export class CheckoutService {
	static #instance: CheckoutService
	static getInstance(): CheckoutService {
		return (CheckoutService.#instance ??= new CheckoutService())
	}

	/** The payment methods this store has switched on. */
	async listPaymentMethods(opts: Fetcher = {}) {
		const options = await data<EngineCheckoutOptions>('/api/checkout', { fetch: opts.fetch })
		return (options?.methods ?? []).map((m) => ({
			id: m.code,
			code: m.code,
			title: m.name,
			name: m.name,
			description: m.description ?? null,
			isActive: true,
		}))
	}

	/**
	 * Everything the checkout route says: the methods, and the currency they are
	 * priced in.
	 *
	 * Kept alongside listPaymentMethods because a cart cannot answer "is cash on
	 * delivery available" on its own — that is a property of the store, not of
	 * the basket, and this is where the store says so.
	 */
	async getOptions(opts: Fetcher = {}) {
		const options = await data<EngineCheckoutOptions>('/api/checkout', { fetch: opts.fetch })
		const codes = options?.payment_methods ?? (options?.methods ?? []).map((m) => m.code)
		return {
			currency: options?.currency ?? null,
			methods: await this.listPaymentMethods(opts),
			codes,
			isCodAvailable: codes.includes('cod'),
		}
	}

	/**
	 * Shipping rates for a destination.
	 *
	 * The engine wants the cart and a country because a rate can depend on both
	 * what is in the basket and where it is going. A storefront asking before it
	 * has either gets an empty list rather than a guess.
	 */
	async listShippingRates(
		{ cartId, country, state }: { cartId: string; country: string; state?: string },
		opts: Fetcher = {},
	) {
		if (!cartId || !country) return []
		const rates = await data<EngineRate[]>('/api/checkout/rates', {
			fetch: opts.fetch,
			query: { cart: cartId, country, state },
		})
		return (rates ?? []).map((r) => ({
			id: r.id ?? r.code ?? r.name,
			code: r.code ?? r.id ?? r.name,
			name: r.name,
			title: r.name,
			description: r.description ?? null,
			price: toMajor(r.price),
			amount: toMajor(r.price),
		}))
	}

	/**
	 * Turn the cart into an order.
	 *
	 * `code` is the payment method the shopper chose. The idempotency key is
	 * passed through when the storefront supplies one and is the difference
	 * between a double-click and a double charge — the engine honours the
	 * header, so this must not invent its own on every call.
	 */
	async completeCart(
		{
			cartId,
			paymentMethod,
			email,
			phone,
			name,
			address,
			paymentData,
			returnUrl,
			idempotencyKey,
			metadata,
		}: {
			cartId: string
			paymentMethod: string
			email: string
			phone?: string
			name?: string
			address: Record<string, unknown>
			paymentData?: Record<string, unknown>
			returnUrl?: string
			idempotencyKey?: string
			metadata?: Record<string, unknown>
		},
		opts: Fetcher = {},
	) {
		const result = await data<EngineCheckoutResult>(
			`/api/checkout/${encodeURIComponent(paymentMethod)}`,
			{
				method: 'POST',
				fetch: opts.fetch,
				headers: idempotencyKey ? { 'Idempotency-Key': idempotencyKey } : undefined,
				body: {
					cart_id: cartId,
					email,
					phone,
					name,
					address,
					payment_data: paymentData,
					return_url: returnUrl,
					metadata,
				},
			},
		)
		return {
			orderId: result.order ? String(result.order.id) : null,
			orderNumber: result.order?.number ?? null,
			// The token is how a guest reads their own order back. It is returned
			// once, here, and the storefront must keep it — there is no second way
			// to get it without an operator.
			accessToken: result.order?.access_token ?? null,
			paymentStatus: result.order?.payment_status ?? null,
			status: result.order?.status ?? result.status ?? null,
			redirectUrl: result.redirect_url ?? null,
			paymentReference: result.payment_reference ?? null,
		}
	}
}

export const checkoutService = CheckoutService.getInstance()
