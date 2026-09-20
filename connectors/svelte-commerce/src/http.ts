/**
 * The one place this connector talks to the engine.
 *
 * Every service goes through `api`, so there is exactly one implementation of
 * "what is the base URL", "what does an error look like" and "what does an
 * empty body mean". A service that reached for `fetch` itself would be a second
 * answer to all three.
 */

/**
 * Where the engine is.
 *
 * Read at call time rather than at module load. SvelteKit evaluates modules
 * once per server process but the environment is only settled after the
 * platform has started, and a base URL captured at import time is the classic
 * "works in dev, empty in production" bug.
 */
export function baseURL(): string {
	const env =
		(globalThis as Record<string, any>).process?.env?.PUBLIC_GOCOMMERCE_API_URL ??
		(globalThis as Record<string, any>).PUBLIC_GOCOMMERCE_API_URL
	const url = (env ?? '').toString().trim()
	if (!url) {
		throw new GoCommerceError(
			'PUBLIC_GOCOMMERCE_API_URL is not set, so this storefront does not know where its ' +
				'commerce engine is. Point it at a running GoCommerce, e.g. http://127.0.0.1:8080.',
			0,
		)
	}
	return url.replace(/\/+$/, '')
}

/**
 * The global fetch's type, captured before any parameter named `fetch` shadows
 * it. `fetch?: Fetch` in a destructured parameter refers to itself.
 */
export type Fetch = typeof globalThis.fetch

/**
 * What the engine refused, in a shape the storefront can show a shopper.
 *
 * `status` travels because the difference between 404 and 409 is the difference
 * between "that product is gone" and "somebody just bought the last one", and a
 * storefront renders those differently.
 */
export class GoCommerceError extends Error {
	status: number
	code: string | undefined

	constructor(message: string, status: number, code?: string) {
		super(message)
		this.name = 'GoCommerceError'
		this.status = status
		this.code = code
	}
}

export type Envelope<T> = {
	data: T
	meta?: { total: number; limit: number; offset: number; page: number; total_pages: number }
}

type Options = {
	method?: string
	body?: unknown
	query?: Record<string, string | number | boolean | undefined | null>
	/** Passed straight through, for the cookie a SvelteKit load function holds. */
	headers?: Record<string, string>
	fetch?: Fetch
}

function queryString(query: Options['query']): string {
	if (!query) return ''
	const parts: string[] = []
	for (const [key, value] of Object.entries(query)) {
		// Undefined and null are "did not ask", not "asked for nothing" — sending
		// `?search=` would filter to the empty string on some routes.
		if (value === undefined || value === null || value === '') continue
		parts.push(encodeURIComponent(key) + '=' + encodeURIComponent(String(value)))
	}
	return parts.length ? '?' + parts.join('&') : ''
}

/**
 * One request to the engine.
 *
 * The engine answers `{data, meta}` on success and `{error: {...}}` on failure,
 * so the envelope is unwrapped here and every caller sees the thing it asked
 * for. A non-JSON body from a proxy or a load balancer is reported as what it
 * is rather than throwing a parse error three frames away.
 */
export async function api<T>(path: string, options: Options = {}): Promise<Envelope<T>> {
	const doFetch = options.fetch ?? fetch
	const url = baseURL() + path + queryString(options.query)

	let response: Response
	try {
		response = await doFetch(url, {
			method: options.method ?? 'GET',
			headers: {
				accept: 'application/json',
				...(options.body === undefined ? {} : { 'content-type': 'application/json' }),
				...options.headers,
			},
			body: options.body === undefined ? undefined : JSON.stringify(options.body),
		})
	} catch (cause) {
		// A refused connection is the commonest failure in development and the
		// least self-explanatory, so it says which address did not answer.
		throw new GoCommerceError(
			`Could not reach the commerce engine at ${baseURL()}: ${(cause as Error).message}`,
			0,
		)
	}

	const text = await response.text()
	let parsed: any = null
	if (text) {
		try {
			parsed = JSON.parse(text)
		} catch {
			throw new GoCommerceError(
				`The commerce engine answered ${response.status} with something that is not JSON. ` +
					`The first of it: ${text.slice(0, 120)}`,
				response.status,
			)
		}
	}

	if (!response.ok) {
		const err = parsed?.error ?? {}
		throw new GoCommerceError(
			err.message ?? `The commerce engine answered ${response.status}.`,
			response.status,
			err.code,
		)
	}

	// 204 and friends: a successful call that returns nothing is not an error,
	// and the caller decides what "nothing" means for it.
	return (parsed ?? { data: null }) as Envelope<T>
}

/** The bare payload, for the many callers that never look at `meta`. */
export async function data<T>(path: string, options: Options = {}): Promise<T> {
	return (await api<T>(path, options)).data
}
