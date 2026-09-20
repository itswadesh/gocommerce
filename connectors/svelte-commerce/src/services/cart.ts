import { data, type Fetch } from '../http.js'
import { toCart, type EngineCart } from '../map.js'

type Fetcher = { fetch?: Fetch }

/**
 * The cart, which is the one service here that writes.
 *
 * The engine's cart is addressed by an opaque token it mints, not by a
 * customer id, so the storefront holds that token and hands it back. Every
 * method therefore takes a cartId, and the ones that can create take an
 * optional one — `addToCart` on a first visit has no cart yet and must not
 * fail; it opens one.
 */
export class CartService {
	static #instance: CartService
	static getInstance(): CartService {
		return (CartService.#instance ??= new CartService())
	}

	async create(opts: Fetcher = {}) {
		const cart = await data<EngineCart>('/api/carts', { method: 'POST', body: {}, fetch: opts.fetch })
		return toCart(cart)
	}

	async getCartByCartId(cartId: string, opts: Fetcher = {}) {
		if (!cartId) return null
		const cart = await data<EngineCart>(`/api/carts/${encodeURIComponent(cartId)}`, { fetch: opts.fetch })
		return toCart(cart)
	}

	/** What the storefront calls on load; a missing cart is empty, not an error. */
	async fetchCartData(cartId?: string, opts: Fetcher = {}) {
		if (!cartId) return null
		try {
			return await this.getCartByCartId(cartId, opts)
		} catch (err: any) {
			// A cart expires after a month. A shopper coming back to a dead token
			// should see an empty bag, not a 404 page.
			if (err?.status === 404) return null
			throw err
		}
	}

	async refereshCart(cartId?: string, opts: Fetcher = {}) {
		return this.fetchCartData(cartId, opts)
	}

	async addToCart(
		{ productId, variantId, qty = 1, cartId, lineId }: {
			productId?: string | number
			variantId: string | number
			qty?: number
			cartId?: string
			lineId?: string | number
		},
		opts: Fetcher = {},
	) {
		// An existing line is updated rather than added again, so a shopper
		// pressing "Add" twice gets two of the thing rather than two lines of it.
		if (cartId && lineId) {
			return this.updateCart({ cartId, lineId, qty }, opts)
		}
		const id = cartId ?? ((await this.create(opts)) as { id: string }).id
		const cart = await data<EngineCart>(`/api/carts/${encodeURIComponent(id)}/line-items`, {
			method: 'POST',
			body: { variant_id: Number(variantId), quantity: qty },
			fetch: opts.fetch,
		})
		return toCart(cart)
	}

	async updateCart(
		{ cartId, lineId, qty }: { cartId: string; lineId: string | number; qty: number },
		opts: Fetcher = {},
	) {
		// Zero is a removal, not a quantity. The engine's CHECK refuses a line of
		// none, and a shopper typing 0 into the box means "take it out".
		if (qty <= 0) return this.removeCart({ cartId, lineId }, opts)
		const cart = await data<EngineCart>(
			`/api/carts/${encodeURIComponent(cartId)}/line-items/${encodeURIComponent(String(lineId))}`,
			{ method: 'PATCH', body: { quantity: qty }, fetch: opts.fetch },
		)
		return toCart(cart)
	}

	async removeCart({ cartId, lineId }: { cartId: string; lineId: string | number }, opts: Fetcher = {}) {
		const cart = await data<EngineCart>(
			`/api/carts/${encodeURIComponent(cartId)}/line-items/${encodeURIComponent(String(lineId))}`,
			{ method: 'DELETE', fetch: opts.fetch },
		)
		return toCart(cart)
	}

	async applyCoupon({ cartId, couponCode }: { cartId: string; couponCode: string }, opts: Fetcher = {}) {
		const cart = await data<EngineCart>(`/api/carts/${encodeURIComponent(cartId)}/discount`, {
			method: 'PUT',
			body: { code: couponCode },
			fetch: opts.fetch,
		})
		return toCart(cart)
	}

	async removeCoupon(cartId: string, opts: Fetcher = {}) {
		const cart = await data<EngineCart>(`/api/carts/${encodeURIComponent(cartId)}/discount`, {
			method: 'DELETE',
			fetch: opts.fetch,
		})
		return toCart(cart)
	}

	/** The engine keeps an email on the cart so an abandoned one can be chased. */
	async updateCart2({ cartId, email }: { cartId: string; email?: string }, opts: Fetcher = {}) {
		if (!email) return this.getCartByCartId(cartId, opts)
		const cart = await data<EngineCart>(`/api/carts/${encodeURIComponent(cartId)}/email`, {
			method: 'PUT',
			body: { email },
			fetch: opts.fetch,
		})
		return toCart(cart)
	}
}

export const cartService = CartService.getInstance()
