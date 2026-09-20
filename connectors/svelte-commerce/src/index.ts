/**
 * @misiki/gocommerce-connector — Svelte Commerce, talking to a GoCommerce engine.
 *
 * Svelte Commerce resolves its backend through a `kitcommerce.config` alias
 * that re-exports one connector's `services` object. Its core then imports some
 * four dozen named services from that object because Litekart has them.
 * GoCommerce is a commerce engine: catalog, carts, checkout, orders,
 * inventory. Seven of those services are real here.
 *
 * The other forty are not stubbed out returning empty lists. An empty list is a
 * claim — "this shop has no blog posts" — and a storefront renders it as a bare
 * heading over nothing while the developer goes looking for the bug in their
 * template. These throw instead, naming the service and the method, so the
 * answer is in the stack trace: that page belongs to a different backend.
 *
 * What works today:
 *   products, variants, categories, collections, cart, checkout, order lookup
 *
 * What does not, and why:
 *   auth, users, addresses, wishlists, reviews, blogs, banners, reels, CMS
 *   pages, chat, warranties, vendor commissions — the engine has no model for
 *   any of them. Accounts in particular: GoCommerce checkout is guest checkout,
 *   and an order is read back with the access token issued when it was placed.
 */

import { unsupportedClass, unsupportedService, UnsupportedByGoCommerce } from './unsupported.js'
import { CartService, cartService } from './services/cart.js'
import { CheckoutService, checkoutService } from './services/checkout.js'
import { OrderService, orderService } from './services/order.js'
import { ProductService, productService } from './services/product.js'
import {
	CategoryService,
	categoryService,
	CollectionService,
	collectionService,
} from './services/taxonomy.js'

export { GoCommerceError, baseURL } from './http.js'
export { currencyExponent, toMajor, toMinor } from './money.js'
export { UnsupportedByGoCommerce }
export {
	CartService,
	cartService,
	CategoryService,
	categoryService,
	CheckoutService,
	checkoutService,
	CollectionService,
	collectionService,
	OrderService,
	orderService,
	ProductService,
	productService,
}

/**
 * The services Svelte Commerce's core imports by name.
 *
 * Listed explicitly rather than generated, so that adding a real one is a visible
 * edit here and the difference between "implemented" and "refuses" is readable
 * in one screen.
 */
const UNSUPPORTED = [
	'Address',
	'Auth',
	'Autocomplete',
	'Banner',
	'Blog',
	'Chat',
	'Contact',
	'Country',
	'Coupon',
	'Currency',
	'Deal',
	'Enquiry',
	'Faq',
	'Feedback',
	'Gallery',
	'Home',
	'Init',
	'Meilisearch',
	'Menu',
	'Page',
	'PaymentMethod',
	'Plugin',
	'PopularSearch',
	'Popularity',
	'Profile',
	'Reels',
	'Region',
	'Review',
	'Search',
	'Setting',
	'State',
	'Store',
	'Team',
	'Upload',
	'User',
	'VarniCustomDesign',
	'VarniCustomProduct',
	'Vendor',
	'Warranty',
	'Wishlist',
] as const

const refusals: Record<string, unknown> = {}
for (const name of UNSUPPORTED) {
	refusals[`${name}Service`] = unsupportedClass(`${name}Service`)
	refusals[`${name[0].toLowerCase()}${name.slice(1)}Service`] = unsupportedService(`${name}Service`)
}

export const services = {
	...refusals,

	// A base every service in Litekart's connector extends. Nothing here needs
	// it, but the core re-exports it and an undefined export breaks the import.
	BaseService: class BaseService {},

	ProductService,
	productService,
	CategoryService,
	categoryService,
	CollectionService,
	collectionService,
	CartService,
	cartService,
	CheckoutService,
	checkoutService,
	OrderService,
	orderService,

	// Two the core asks for that map onto services above rather than onto
	// nothing: the storefront's payment-method list is part of checkout here,
	// and its "coupon" is the cart's discount.
	PaymentMethodService: CheckoutService,
	paymentMethodService: checkoutService,
}

export default services
