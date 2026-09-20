/**
 * The services this engine does not have.
 *
 * Svelte Commerce's core expects some four dozen services because Litekart has
 * them: blogs, reels, warranties, vendor commissions, a chat. GoCommerce is a
 * commerce engine — catalog, carts, checkout, orders, inventory — and it has
 * none of those.
 *
 * The temptation is to stub them out returning `[]`, and it is the wrong
 * answer. An empty list is a claim: it says "there are no blog posts", and a
 * storefront renders that as a page with a heading and nothing under it. The
 * developer then goes looking for the bug in their template. A refusal that
 * names the backend and the feature puts the answer in the stack trace instead,
 * and it takes about ten seconds to decide the page should not be there.
 *
 * So: unsupported means unsupported, loudly, once, at the call.
 */

export class UnsupportedByGoCommerce extends Error {
	service: string
	method: string

	constructor(service: string, method: string) {
		super(
			`${service}.${method}() is not supported by GoCommerce. The engine covers catalog, ` +
				`carts, checkout, orders and inventory; ${service} is not part of it. Remove the ` +
				`page or component that calls this, or put the feature behind your own API.`,
		)
		this.name = 'UnsupportedByGoCommerce'
		this.service = service
		this.method = method
	}
}

/**
 * A service that answers every call by saying it is not here.
 *
 * A Proxy rather than a hand-written object per service: the core imports
 * whatever Litekart happens to export, that list moves between versions, and a
 * connector that has to be edited every time a storefront gains a service is a
 * connector that breaks on upgrade. Anything asked of one of these fails by
 * name, whatever the name turns out to be.
 */
export function unsupportedService(name: string): any {
	return new Proxy(
		{},
		{
			get(_target, prop) {
				// The runtime pokes at objects for reasons of its own — awaiting one
				// looks for `then`, printing one looks for Symbol.toPrimitive. Those
				// must not detonate, or a console.log becomes a crash.
				if (typeof prop === 'symbol' || prop === 'then' || prop === 'toJSON') return undefined
				if (prop === 'name' || prop === 'serviceName') return name
				return () => {
					throw new UnsupportedByGoCommerce(name, String(prop))
				}
			},
		},
	)
}

/** The same thing where a class is expected rather than an instance. */
export function unsupportedClass(name: string): any {
	const Cls = function () {
		return unsupportedService(name)
	} as unknown as { new (): any; getInstance(): any }
	Cls.getInstance = () => unsupportedService(name)
	return Cls
}
