import { api, data, type Fetch } from '../http.js'
import { toPage, toProduct, toVariant, type EngineProduct, type EngineVariant } from '../map.js'

type ListArgs = {
	page?: number
	limit?: number
	search?: string
	sort?: string
	categoryId?: string
	collectionId?: string
	fetch?: Fetch
}

/**
 * How the storefront's sort names reach the engine.
 *
 * The engine sorts on its own column names; Svelte Commerce passes Litekart's.
 * An unrecognised one is dropped rather than forwarded, because the engine
 * refuses a sort it does not know with a 400 and a shopper clicking "Most
 * popular" should get an unsorted shelf, not an error page.
 */
const SORTS: Record<string, string> = {
	'price-asc': 'price',
	'price-desc': '-price',
	'price_low_to_high': 'price',
	'price_high_to_low': '-price',
	'new': '-created_at',
	'newest': '-created_at',
	'latest': '-created_at',
	'name-asc': 'title',
	'name-desc': '-title',
	'a-z': 'title',
	'z-a': '-title',
}

function sortFor(sort?: string): string | undefined {
	if (!sort) return undefined
	return SORTS[sort] ?? undefined
}

/** Page numbers in, offset out: the engine pages by offset. */
function offsetFor(page: number | undefined, limit: number): number {
	return Math.max(0, ((page ?? 1) - 1) * limit)
}

export class ProductService {
	static #instance: ProductService
	static getInstance(): ProductService {
		return (ProductService.#instance ??= new ProductService())
	}

	async list({ page = 1, limit = 20, search, sort, categoryId, collectionId, fetch }: ListArgs = {}) {
		const res = await api<EngineProduct[]>('/api/products', {
			fetch,
			query: {
				limit,
				offset: offsetFor(page, limit),
				q: search,
				sort: sortFor(sort),
				category: categoryId,
				collection: collectionId,
			},
		})
		return toPage(res.data, res.meta, toProduct)
	}

	/**
	 * One product, by slug.
	 *
	 * The storefront asks by slug because that is what is in the URL, and the
	 * engine has a route for exactly that — rather than listing and filtering,
	 * which would page through the catalogue to find one row.
	 */
	async getOne(slug: string, opts: { fetch?: Fetch } = {}) {
		const p = await data<EngineProduct>(`/api/products/slug/${encodeURIComponent(slug)}`, {
			fetch: opts.fetch,
		})
		return toProduct(p)
	}

	async getById(id: string | number, opts: { fetch?: Fetch } = {}) {
		const p = await data<EngineProduct>(`/api/products/${encodeURIComponent(String(id))}`, {
			fetch: opts.fetch,
		})
		return toProduct(p)
	}

	async getBySku(sku: string, opts: { fetch?: Fetch } = {}) {
		const p = await data<EngineProduct>(`/api/products/sku/${encodeURIComponent(sku)}`, {
			fetch: opts.fetch,
		})
		return toProduct(p)
	}

	/**
	 * The engine keeps no popularity score and no "featured" flag, so featured
	 * and trending are both the newest first.
	 *
	 * This is a deliberate flattening, not a stub: the shelf is real products in
	 * a defensible order. What it must not do is pretend to a ranking the engine
	 * does not have — see the `popularity: 0` in the mapper.
	 */
	async listFeaturedProducts(args: ListArgs = {}) {
		return this.list({ ...args, sort: args.sort ?? 'new' })
	}

	async listTrendingProducts(args: ListArgs = {}) {
		return this.list({ ...args, sort: args.sort ?? 'new' })
	}

	/** Others in the same category, which is the relationship the engine has. */
	async listRelatedProducts({ categoryId, page = 1, limit = 8, sort, fetch }: ListArgs = {}) {
		if (!categoryId) return toPage<EngineProduct, Record<string, unknown>>([], undefined, toProduct)
		return this.list({ categoryId, page, limit, sort, fetch })
	}

	async listVariants({ page = 1, limit = 50, fetch }: ListArgs = {}) {
		const res = await api<EngineVariant[]>('/api/variants', {
			fetch,
			query: { limit, offset: offsetFor(page, limit) },
		})
		return toPage(res.data, res.meta, (v) => toVariant(v))
	}

	async getVariant(id: string | number, opts: { fetch?: Fetch } = {}) {
		const v = await data<EngineVariant>(`/api/variants/${encodeURIComponent(String(id))}`, {
			fetch: opts.fetch,
		})
		return toVariant(v)
	}
}

export const productService = ProductService.getInstance()
