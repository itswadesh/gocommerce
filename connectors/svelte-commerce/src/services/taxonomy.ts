import { api, data, type Fetch } from '../http.js'
import {
	toCategory,
	toCollection,
	toPage,
	toProduct,
	type EngineCategory,
	type EngineCollection,
	type EngineProduct,
} from '../map.js'

type Fetcher = { fetch?: Fetch }

/**
 * Categories: the taxonomy a product is filed in, one answer per product.
 *
 * The engine's tree is browsable a level at a time because an imported Shopify
 * taxonomy is fourteen thousand rows. A menu therefore asks for the roots and
 * expands, rather than pulling the tree and rendering it — which is why
 * `child_count` matters and why every listing computes it.
 */
export class CategoryService {
	static #instance: CategoryService
	static getInstance(): CategoryService {
		return (CategoryService.#instance ??= new CategoryService())
	}

	async list({ parent, flat, search, limit = 200, fetch }: {
		parent?: string | number | null
		flat?: boolean
		search?: string
		limit?: number
		fetch?: Fetch
	} = {}) {
		const res = await api<EngineCategory[]>('/api/categories', {
			fetch,
			query: {
				// "root" is the engine's sentinel for the top level, because an
				// absent parent means "do not filter" on this route.
				parent: parent === null ? 'root' : parent === undefined ? undefined : String(parent),
				flat: flat ? 1 : undefined,
				q: search,
				limit,
			},
		})
		return toPage(res.data, res.meta, toCategory)
	}

	/** The top level, which is what a navigation menu opens with. */
	async listRoots(opts: Fetcher = {}) {
		return this.list({ parent: null, fetch: opts.fetch })
	}

	async listChildren(parentId: string | number, opts: Fetcher = {}) {
		return this.list({ parent: parentId, fetch: opts.fetch })
	}

	async getOne(slug: string, opts: Fetcher = {}) {
		const c = await data<EngineCategory>(`/api/categories/${encodeURIComponent(slug)}`, {
			fetch: opts.fetch,
		})
		return toCategory(c)
	}

	/**
	 * A category page: the category, and the products filed under it.
	 *
	 * Two calls rather than one, because the engine keeps them apart — the
	 * category route answers what the category is, and the product listing takes
	 * a category filter. Fetched together so the page has one await.
	 */
	async getPage(
		slug: string,
		{ page = 1, limit = 20, sort, fetch }: { page?: number; limit?: number; sort?: string; fetch?: Fetch } = {},
	) {
		const category = await this.getOne(slug, { fetch })
		const res = await api<EngineProduct[]>('/api/products', {
			fetch,
			query: {
				category: String(category.id),
				limit,
				offset: Math.max(0, (page - 1) * limit),
				sort,
			},
		})
		return { category, products: toPage(res.data, res.meta, toProduct) }
	}
}

/**
 * Collections: a curated list a product can be on several of.
 *
 * Not the same thing as a category, and the engine is strict about it — a
 * product has one category and any number of collections. The storefront
 * renders them the same way, which is fine, but they must not be merged here or
 * "what kind of thing is this" stops having one answer.
 */
export class CollectionService {
	static #instance: CollectionService
	static getInstance(): CollectionService {
		return (CollectionService.#instance ??= new CollectionService())
	}

	async list({ page = 1, limit = 50, fetch }: { page?: number; limit?: number; fetch?: Fetch } = {}) {
		const res = await api<EngineCollection[]>('/api/collections', {
			fetch,
			query: { limit, offset: Math.max(0, (page - 1) * limit) },
		})
		return toPage(res.data, res.meta, toCollection)
	}

	async getOne(slug: string, opts: Fetcher = {}) {
		const c = await data<EngineCollection>(`/api/collections/${encodeURIComponent(slug)}`, {
			fetch: opts.fetch,
		})
		return toCollection(c)
	}

	async getPage(
		slug: string,
		{ page = 1, limit = 20, sort, fetch }: { page?: number; limit?: number; sort?: string; fetch?: Fetch } = {},
	) {
		const collection = await this.getOne(slug, { fetch })
		const res = await api<EngineProduct[]>('/api/products', {
			fetch,
			query: {
				collection: String(collection.id),
				limit,
				offset: Math.max(0, (page - 1) * limit),
				sort,
			},
		})
		return { collection, products: toPage(res.data, res.meta, toProduct) }
	}
}

export const categoryService = CategoryService.getInstance()
export const collectionService = CollectionService.getInstance()
