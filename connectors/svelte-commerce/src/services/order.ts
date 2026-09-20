import { data, type Fetch } from '../http.js'
import { toMajor, type Money } from '../money.js'

type EngineOrderLine = {
	id: number
	product_id: number | null
	variant_id: number | null
	sku: string
	title: string
	variant_label?: string
	quantity: number
	unit_price: Money
	total: Money
}

type EngineOrder = {
	id: number
	number: string
	status: string
	payment_status: string
	fulfillment_status?: string
	currency: string
	email?: string
	lines: EngineOrderLine[]
	subtotal: Money
	discount?: Money | null
	tax?: Money | null
	shipping?: Money | null
	total: Money
	shipping_address?: Record<string, unknown> | null
	billing_address?: Record<string, unknown> | null
	created_at: string
	updated_at: string
}

/**
 * Orders, read by the person who placed them.
 *
 * The engine has no customer accounts, so there is no "my orders" list to
 * fetch — an order is read by its number plus the access token issued at
 * checkout, which is a bearer credential for that one order. That is why
 * `listOrders` is not here: a connector that returned every order to anyone who
 * asked would be the worst bug in this package.
 */
export class OrderService {
	static #instance: OrderService
	static getInstance(): OrderService {
		return (OrderService.#instance ??= new OrderService())
	}

	async getOne(
		{ number, token }: { number: string; token: string },
		opts: { fetch?: Fetch } = {},
	) {
		if (!number || !token) {
			throw new Error(
				'An order is read with its number and the access token checkout returned. ' +
					'Without the token there is nothing to authorise the read.',
			)
		}
		const o = await data<EngineOrder>(`/api/orders/${encodeURIComponent(number)}`, {
			fetch: opts.fetch,
			query: { token },
		})
		return toOrder(o)
	}
}

export function toOrder(o: EngineOrder): Record<string, unknown> {
	return {
		id: String(o.id),
		orderNo: o.number,
		number: o.number,
		status: o.status,
		paymentStatus: o.payment_status,
		fulfillmentStatus: o.fulfillment_status ?? null,
		currency: o.currency,
		email: o.email ?? null,
		items: (o.lines ?? []).map((l) => ({
			id: String(l.id),
			productId: l.product_id === null ? null : String(l.product_id),
			variantId: l.variant_id === null ? null : String(l.variant_id),
			sku: l.sku,
			// The line holds its own snapshot of the name and price, so an order
			// reads the same after the product is renamed or repriced. Nothing here
			// re-reads the catalogue to "improve" it.
			title: l.title,
			name: l.title,
			variantLabel: l.variant_label ?? '',
			qty: l.quantity,
			price: toMajor(l.unit_price),
			total: toMajor(l.total),
		})),
		subtotal: toMajor(o.subtotal),
		discountAmount: toMajor(o.discount ?? null),
		tax: toMajor(o.tax ?? null),
		shippingCharges: toMajor(o.shipping ?? null),
		total: toMajor(o.total),
		shippingAddress: o.shipping_address ?? null,
		billingAddress: o.billing_address ?? null,
		createdAt: o.created_at,
		updatedAt: o.updated_at,
	}
}

export const orderService = OrderService.getInstance()
