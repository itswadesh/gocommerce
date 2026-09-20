/**
 * Engine shapes to storefront shapes.
 *
 * Svelte Commerce's types were drawn around Litekart's API, so every connector
 * is a translation and this file is the whole of ours. Three rules run through
 * it:
 *
 *   1. Ids cross as strings. The engine uses int64; the storefront types say
 *      `id: string` and its components put ids straight into URLs and keys.
 *   2. Money crosses as a major-unit number, through money.ts and nowhere else.
 *   3. A field the engine does not have is null, never invented. `popularity`
 *      is 0 rather than a guess, and a storefront that sorts by it gets a flat
 *      ordering instead of a fictional one.
 */

import { toMajor, type Money } from './money.js'

// ---------------------------------------------------------------- engine types

export type EngineImage = { media_id?: number; id?: number; url: string; alt?: string; kind?: string }

export type EngineVariant = {
	id: number
	product_id: number
	sku: string
	barcode?: string
	price: Money
	compare_at_price?: Money | null
	cost?: Money | null
	taxable: boolean
	requires_shipping: boolean
	options: string[]
	label: string
	stock_on_hand: number
	stock_reserved: number
	available: number
	track_inventory: boolean
	continue_selling: boolean
	active: boolean
	weight_grams?: number | null
	weight_unit?: string
	dimensions?: Record<string, number> | null
	dimension_unit?: string
	position?: number
	origin_country?: string
	hs_code?: string
	images?: EngineImage[]
	image?: EngineImage | null
	metadata?: Record<string, unknown>
}

export type EngineProduct = {
	id: number
	slug: string
	title: string
	description: string
	status: string
	currency: string
	product_type: string
	vendor: string
	tags: string[]
	category?: { id: number; slug: string; title: string; full_name?: string } | null
	seo_title: string
	seo_description: string
	options?: { id: number; name: string; position: number; values: { id: number; value: string; position: number }[] }[]
	variants?: EngineVariant[]
	collections?: { id: number; slug: string; title: string }[]
	media?: EngineImage[]
	metadata?: Record<string, unknown>
	created_at: string
	updated_at: string
}

export type EngineCategory = {
	id: number
	parent_id: number | null
	slug: string
	title: string
	position: number
	full_name?: string
	depth?: number
	child_count?: number
	metadata?: Record<string, unknown>
	created_at?: string
	updated_at?: string
}

export type EngineCollection = {
	id: number
	slug: string
	title: string
	description?: string
	product_count?: number
	created_at?: string
	updated_at?: string
}

export type EngineCartLine = {
	id: number
	product_id: number
	variant_id: number
	sku: string
	title: string
	variant_label?: string
	quantity: number
	unit_price: Money
	total: Money
	image?: EngineImage | null
}

export type EngineCart = {
	id: string
	status: string
	currency: string
	email?: string | null
	line_items: EngineCartLine[]
	item_count: number
	subtotal: Money
	discount?: Money | null
	discount_code?: string | null
	tax?: Money | null
	shipping?: Money | null
	total?: Money | null
	metadata?: Record<string, unknown>
	created_at: string
	updated_at: string
}

// ---------------------------------------------------------------- helpers

const str = (v: number | string | null | undefined): string => (v === null || v === undefined ? '' : String(v))

/**
 * The variant a product is priced and pictured by.
 *
 * The first active one rather than the cheapest: the matrix is ordered by
 * position, which is the order the merchandiser put them in, and "from £X"
 * pricing is a storefront decision this connector should not make for it.
 */
export function defaultVariant(p: EngineProduct): EngineVariant | undefined {
	const variants = p.variants ?? []
	return variants.find((v) => v.active !== false) ?? variants[0]
}

function imageURL(img: EngineImage | null | undefined): string | null {
	return img?.url ?? null
}

/** Every picture the product shows, its variants' nominations included. */
export function productImages(p: EngineProduct): string[] {
	const seen = new Set<string>()
	const out: string[] = []
	const push = (img: EngineImage | null | undefined) => {
		const url = imageURL(img)
		if (url && !seen.has(url)) {
			seen.add(url)
			out.push(url)
		}
	}
	for (const m of p.media ?? []) push(m)
	for (const v of p.variants ?? []) {
		push(v.image)
		for (const img of v.images ?? []) push(img)
	}
	return out
}

// ---------------------------------------------------------------- products

export function toProduct(p: EngineProduct): Record<string, unknown> {
	const v = defaultVariant(p)
	const images = productImages(p)
	const currency = p.currency ?? v?.price?.currency ?? 'USD'

	return {
		id: str(p.id),
		// The engine has draft / active / archived. Only `active` is on sale, and
		// the storefront's vocabulary for that is "published".
		active: p.status === 'active',
		status: p.status === 'active' ? 'published' : 'draft',
		type: p.product_type ?? '',
		// Free text on the product, not a record — see the engine's README. It
		// travels as the brand it is rather than as a vendor id the storefront
		// could try to look up.
		vendorId: '',
		vendor: p.vendor ?? '',
		categoryId: p.category ? str(p.category.id) : null,
		categorySlug: p.category?.slug ?? null,
		currency,
		instructions: null,
		description: p.description ?? null,
		hsnCode: v?.hs_code ?? null,
		images: images.length ? images.join(',') : null,
		imageList: images,
		featuredImage: images[0] ?? null,
		thumbnail: images[0] ?? null,
		keywords: (p.tags ?? []).join(',') || null,
		link: `/product/${p.slug}`,
		metaTitle: p.seo_title || p.title,
		metaDescription: p.seo_description || null,
		title: p.title,
		subtitle: null,
		// The engine keeps no popularity score. Zero is the honest answer; a
		// random one would sort the shop into a plausible-looking lie.
		popularity: 0,
		rank: 0,
		slug: p.slug,
		expiryDate: null,
		weight: v?.weight_grams ?? null,
		mfgDate: null,
		// `mrp` is the struck-through price. Absent compare-at, it equals the
		// price, so nothing is struck through — rather than 0, which renders as a
		// 100% discount.
		mrp: toMajor(v?.compare_at_price ?? v?.price),
		price: toMajor(v?.price),
		costPerItem: toMajor(v?.cost ?? null),
		sku: v?.sku ?? null,
		stock: v?.track_inventory === false ? Number.MAX_SAFE_INTEGER : (v?.available ?? 0),
		allowBackorder: v?.continue_selling ?? false,
		manageInventory: v?.track_inventory ?? true,
		shippingWeight: v?.weight_grams ?? null,
		shippingHeight: v?.dimensions?.height ?? null,
		shippingLen: v?.dimensions?.length ?? null,
		shippingWidth: v?.dimensions?.width ?? null,
		height: v?.dimensions?.height ?? null,
		width: v?.dimensions?.width ?? null,
		len: v?.dimensions?.length ?? null,
		barcode: v?.barcode ?? null,
		shippingCost: null,
		returnAllowed: true,
		replaceAllowed: true,
		originCountry: v?.origin_country ?? null,
		weightUnit: v?.weight_unit ?? 'g',
		dimensionUnit: v?.dimension_unit ?? 'mm',
		metadata: p.metadata ?? null,
		collectionId: p.collections?.[0] ? str(p.collections[0].id) : null,
		options: (p.options ?? []).map((o) => ({
			id: str(o.id),
			title: o.name,
			type: 'text',
			values: (o.values ?? []).map((val) => ({ id: str(val.id), value: val.value })),
		})),
		variants: (p.variants ?? []).map((variant) => toVariant(variant, p)),
		createdAt: p.created_at,
		updatedAt: p.updated_at,
	}
}

export function toVariant(v: EngineVariant, p?: EngineProduct): Record<string, unknown> {
	const images = [v.image, ...(v.images ?? [])].map(imageURL).filter(Boolean) as string[]
	return {
		id: str(v.id),
		productId: str(v.product_id),
		title: v.label || v.sku,
		sku: v.sku,
		barcode: v.barcode ?? null,
		price: toMajor(v.price),
		mrp: toMajor(v.compare_at_price ?? v.price),
		stock: v.track_inventory === false ? Number.MAX_SAFE_INTEGER : v.available,
		manageInventory: v.track_inventory,
		allowBackorder: v.continue_selling,
		active: v.active !== false,
		requiresShipping: v.requires_shipping,
		taxable: v.taxable,
		weight: v.weight_grams ?? null,
		weightUnit: v.weight_unit ?? 'g',
		thumbnail: images[0] ?? null,
		images,
		// The option values in axis order, which is what a picker matches on.
		options: (v.options ?? []).map((value, i) => ({
			id: str(p?.options?.[i]?.id ?? i),
			title: p?.options?.[i]?.name ?? '',
			value,
		})),
		metadata: v.metadata ?? null,
	}
}

// ---------------------------------------------------------------- taxonomy

export function toCategory(c: EngineCategory): Record<string, unknown> {
	return {
		id: str(c.id),
		isActive: true,
		isInternal: false,
		isMegamenu: false,
		thumbnail: null,
		path: c.full_name ?? null,
		level: c.depth ?? null,
		description: null,
		isFeatured: false,
		keywords: null,
		rank: c.position ?? 0,
		link: `/category/${c.slug}`,
		metaDescription: null,
		metaKeywords: null,
		metaTitle: c.title,
		name: c.title,
		parentCategoryId: c.parent_id === null || c.parent_id === undefined ? null : str(c.parent_id),
		store: null,
		slug: c.slug,
		userId: '',
		// Every listing computes this, so 0 means a leaf rather than a listing
		// that did not count — which is what lets a menu draw a chevron.
		childCount: c.child_count ?? 0,
		createdAt: c.created_at ?? null,
		updatedAt: c.updated_at ?? null,
	}
}

export function toCollection(c: EngineCollection): Record<string, unknown> {
	return {
		id: str(c.id),
		name: c.title,
		slug: c.slug,
		description: c.description ?? null,
		isActive: true,
		isFeatured: false,
		userId: '',
		productCount: c.product_count ?? 0,
		thumbnail: null,
		metaTitle: c.title,
		metaDescription: c.description ?? null,
		createdAt: c.created_at ?? null,
		updatedAt: c.updated_at ?? null,
	}
}

// ---------------------------------------------------------------- cart

export function toCartLine(l: EngineCartLine): Record<string, unknown> {
	return {
		id: str(l.id),
		productId: str(l.product_id),
		variantId: str(l.variant_id),
		item_id: str(l.id),
		sku: l.sku,
		title: l.title,
		name: l.title,
		variantLabel: l.variant_label ?? '',
		qty: l.quantity,
		price: toMajor(l.unit_price),
		total: toMajor(l.total),
		thumbnail: l.image?.url ?? null,
	}
}

export function toCart(c: EngineCart): Record<string, unknown> {
	const subtotal = toMajor(c.subtotal)
	const discount = toMajor(c.discount ?? null)
	const tax = toMajor(c.tax ?? null)
	const shipping = toMajor(c.shipping ?? null)
	return {
		id: c.id,
		email: c.email ?? null,
		phone: null,
		lineItems: (c.line_items ?? []).map(toCartLine),
		billingAddressId: null,
		shippingAddressId: null,
		regionId: null,
		userId: null,
		salesChannelId: null,
		storeId: null,
		couponCode: c.discount_code ?? null,
		discountAmount: discount,
		couponAppliedDate: null,
		paymentId: null,
		paymentMethod: null,
		paymentAuthorizedAt: null,
		needAddress: true,
		// Whether cash on delivery is offered is a property of the store, not of
		// the basket, and this payload does not carry it — the first draft of this
		// file asserted `false` and was wrong the moment a store switched `cod`
		// on. The honest default is false and the real answer is one call away:
		// checkoutService.getOptions().isCodAvailable.
		isCodAvailable: false,
		type: 'default',
		completedAt: c.status === 'completed' ? c.updated_at : null,
		idempotencyKey: null,
		shippingCharges: shipping,
		shippingMethod: null,
		qty: c.item_count ?? 0,
		subtotal,
		codCharges: 0,
		tax,
		total: c.total ? toMajor(c.total) : subtotal - discount + tax + shipping,
		savingAmount: discount,
		currency: c.currency,
	}
}

/** `{data, meta}` from the engine to the page shape the storefront reads. */
export function toPage<T, U>(
	rows: T[],
	meta: { total: number; limit: number; page: number; total_pages: number } | undefined,
	map: (row: T) => U,
): { data: U[]; count: number; pageSize: number; noOfPage: number; page: number } {
	return {
		data: (rows ?? []).map(map),
		count: meta?.total ?? rows?.length ?? 0,
		pageSize: meta?.limit ?? rows?.length ?? 0,
		noOfPage: meta?.total_pages ?? 1,
		page: meta?.page ?? 1,
	}
}
