/**
 * The connector against a running engine.
 *
 * Not mocked. A connector is a translation between two systems, and a mock of
 * one of them tests the translation against my idea of the engine rather than
 * against the engine — which is exactly the thing that is wrong when a
 * connector is wrong. It needs a store:
 *
 *   PUBLIC_GOCOMMERCE_API_URL=http://127.0.0.1:8090 node --test test/
 *
 * Skipped, loudly, when there is nothing to talk to.
 */
import { test } from 'node:test'
import assert from 'node:assert/strict'

const BASE = process.env.PUBLIC_GOCOMMERCE_API_URL ?? 'http://127.0.0.1:8090'
process.env.PUBLIC_GOCOMMERCE_API_URL = BASE

const {
	productService,
	categoryService,
	collectionService,
	cartService,
	checkoutService,
	orderService,
	UnsupportedByGoCommerce,
	services,
	toMajor,
	currencyExponent,
} = await import('../dist/index.js')

let live = false
try {
	const r = await fetch(BASE + '/health')
	live = r.ok
} catch {
	live = false
}
if (!live) console.log(`
  ! no engine at ${BASE} — the live tests are skipped
`)

// ---------------------------------------------------------------- money

test('minor units become the number the storefront renders', () => {
	assert.equal(toMajor({ amount_minor: 2500, currency: 'USD' }), 25)
	assert.equal(toMajor({ amount_minor: 1999, currency: 'USD' }), 19.99)
	// The yen writes no decimals. Dividing by 100 would price a ¥2,500 item at
	// ¥25, which is the single most expensive bug this file can catch.
	assert.equal(toMajor({ amount_minor: 2500, currency: 'JPY' }), 2500)
	// And the dinar writes three.
	assert.equal(toMajor({ amount_minor: 2500, currency: 'KWD' }), 2.5)
	assert.equal(currencyExponent('jpy'), 0)
	assert.equal(currencyExponent(null), 2)
	assert.equal(toMajor(null), 0)
})

// ---------------------------------------------------------------- refusals

test('a service the engine does not have refuses by name', () => {
	assert.throws(
		() => services.blogService.list(),
		(err) => err instanceof UnsupportedByGoCommerce && /BlogService\.list/.test(err.message),
		'a missing feature must name itself rather than return nothing',
	)
	// Awaiting or logging one must not detonate — the runtime probes objects.
	assert.equal(services.blogService.then, undefined)
	assert.doesNotThrow(() => JSON.stringify({ s: services.blogService }))
})

// ---------------------------------------------------------------- catalogue

test('products list, and carry a price a shopper could pay', { skip: !live }, async () => {
	const page = await productService.list({ limit: 5 })
	assert.ok(page.count > 0, 'the store has products')
	assert.ok(page.data.length > 0 && page.data.length <= 5, 'the limit is honoured')
	assert.equal(typeof page.noOfPage, 'number')

	const p = page.data[0]
	assert.equal(typeof p.id, 'string', 'ids cross as strings')
	assert.equal(typeof p.title, 'string')
	assert.equal(typeof p.price, 'number', 'price is a number, not a Money object')
	assert.ok(p.price >= 0)
	// mrp is what gets struck through. Equal to price means "not on sale";
	// zero would render as a 100% discount on every product in the shop.
	assert.ok(p.mrp >= p.price, `mrp ${p.mrp} must not be under price ${p.price}`)
	assert.ok(['published', 'draft'].includes(p.status))
})

test('one product, by the slug that is in the URL', { skip: !live }, async () => {
	const page = await productService.list({ limit: 1 })
	const slug = page.data[0].slug
	const p = await productService.getOne(slug)
	assert.equal(p.slug, slug)
	assert.ok(Array.isArray(p.variants))
	if (p.variants.length) {
		const v = p.variants[0]
		assert.equal(typeof v.id, 'string')
		assert.equal(typeof v.price, 'number')
	}
	// A product that does not exist is a 404, not an empty object the page
	// would render as a blank product.
	await assert.rejects(() => productService.getOne('no-such-product-' + Date.now()), { status: 404 })
})

test('the category tree can be walked', { skip: !live }, async () => {
	const roots = await categoryService.listRoots()
	assert.ok(roots.data.length > 0, 'there are root categories')
	const branch = roots.data.find((c) => c.childCount > 0)
	assert.ok(branch, 'at least one root reports children — otherwise nothing is expandable')
	const kids = await categoryService.listChildren(branch.id)
	assert.ok(kids.data.length > 0, 'and opening it returns them')
	assert.equal(kids.data[0].parentCategoryId, branch.id)
})

test('collections list', { skip: !live }, async () => {
	const page = await collectionService.list({ limit: 5 })
	assert.ok(Array.isArray(page.data))
	for (const c of page.data) assert.equal(typeof c.slug, 'string')
})

// ---------------------------------------------------------------- the buying path

test('a cart can be filled, changed and emptied', { skip: !live }, async () => {

	// Something actually sellable: in stock, and priced.
	const page = await productService.list({ limit: 40 })
	let variantId = null
	for (const p of page.data) {
		const v = (p.variants ?? []).find((x) => x.active && x.stock > 0)
		if (v) {
			variantId = v.id
			break
		}
	}
	assert.ok(variantId, 'the store has something in stock to buy')

	const empty = await cartService.create()
	assert.equal(typeof empty.id, 'string')
	assert.equal(empty.qty, 0)
	assert.equal(empty.lineItems.length, 0)

	const filled = await cartService.addToCart({ variantId, qty: 2, cartId: empty.id })
	assert.equal(filled.lineItems.length, 1, 'one line')
	assert.equal(filled.qty, 2, 'two of the thing')
	const line = filled.lineItems[0]
	assert.equal(typeof line.id, 'string')
	assert.equal(line.qty, 2)
	assert.ok(line.price > 0, 'the line carries a unit price')
	// The arithmetic has to survive the minor-unit crossing.
	assert.ok(
		Math.abs(line.total - line.price * 2) < 0.001,
		`line total ${line.total} should be twice the unit price ${line.price}`,
	)
	assert.ok(Math.abs(filled.subtotal - line.total) < 0.001, 'and the subtotal is the lines')

	const fewer = await cartService.updateCart({ cartId: filled.id, lineId: line.id, qty: 1 })
	assert.equal(fewer.qty, 1)

	// Zero means take it out: the engine refuses a line of none, so the
	// connector must turn it into a removal rather than a refused request.
	const emptied = await cartService.updateCart({ cartId: filled.id, lineId: line.id, qty: 0 })
	assert.equal(emptied.lineItems.length, 0, 'setting a line to zero removes it')

	// A cart that has expired or never existed is an empty bag, not a crash.
	assert.equal(await cartService.fetchCartData('not-a-real-cart-token'), null)
	assert.equal(await cartService.fetchCartData(undefined), null)
})

test('the store says how it can be paid', { skip: !live }, async () => {
	const methods = await checkoutService.listPaymentMethods()
	assert.ok(Array.isArray(methods), 'the route answers an object; the methods come out of it as a list')
	assert.ok(methods.length > 0, 'the dev store accepts something')
	for (const m of methods) {
		assert.equal(typeof m.code, 'string')
		assert.ok(m.code.length > 0)
		assert.equal(typeof m.title, 'string')
	}

	// Cash on delivery is a property of the store, and the cart payload cannot
	// answer it. This is the call that can.
	const options = await checkoutService.getOptions()
	assert.equal(typeof options.currency, 'string')
	assert.equal(options.isCodAvailable, options.codes.includes('cod'))
	assert.deepEqual(options.codes, methods.map((m) => m.code), 'the two lists agree')
})

test('an order cannot be read without its token', { skip: !live }, async () => {
	// The guard is the point: there are no customer accounts, so the token is
	// the only thing standing between a shopper and everyone else's orders.
	await assert.rejects(() => orderService.getOne({ number: '1000', token: '' }), /access token/)
	await assert.rejects(() => orderService.getOne({ number: '1000', token: 'wrong-token' }))
})

test('a missing base URL says so, rather than fetching undefined', async () => {
	const saved = process.env.PUBLIC_GOCOMMERCE_API_URL
	delete process.env.PUBLIC_GOCOMMERCE_API_URL
	try {
		const { baseURL } = await import('../dist/http.js')
		assert.throws(() => baseURL(), /PUBLIC_GOCOMMERCE_API_URL is not set/)
	} finally {
		process.env.PUBLIC_GOCOMMERCE_API_URL = saved
	}
})
