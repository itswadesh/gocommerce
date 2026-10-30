// Builds the sample shop's photographs into the engine (D159): every colour's
// shots of every product in core/sample/catalogue.json, from out/, shrunk into
// core/sample/2/, plus the home page banners cut from them.
//
//   cd scripts/fashion-images
//   npm install
//   node build-sample.mjs
//
// out/ is what fetch-photos.mjs downloads and the colour edits beside it. A
// file missing there stops the build rather than leaving a colour without its
// pictures. A file in core/sample/2/ that this run did not write is deleted,
// so the folder is exactly what the catalogue names.
//
// Every file in core/sample/2/ is linked from stores' sample shops by its
// address, and is cached as immutable. Rebuilding from the same out/ writes
// the same pictures; a picture that changes what it shows belongs in a new
// set (core/sample/3/) rather than over this one.

import { readFile, writeFile, readdir, unlink, mkdir } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import sharp from 'sharp'

const here = dirname(fileURLToPath(import.meta.url))
const out = join(here, 'out')
const set = join(here, '..', '..', 'core', 'sample', '2')
const catalogue = JSON.parse(await readFile(join(here, '..', '..', 'core', 'sample', 'catalogue.json'), 'utf8'))

// 750x1000 is a 3:4 card at twice its widest on a phone and the product
// page's gallery at desktop width. Quality 75 keeps prints and embroidery
// crisp; the whole set has to stay under the budget, since it is in every
// copy of the binary.
const photo = { width: 750, height: 1000, quality: 75 }
const budget = 6_000_000

// A banner is the size D96's were: 1500x380 across a desktop, 720x380 on a
// phone. Each is a row of the products' front shots, four across and two on
// a phone, so it shows the goods its slide links to.
const banners = {
	'banner-festive': ['embroidered-lehenga-set-rani-pink-front', 'satin-saree-magenta-front', 'printed-anarkali-kurta-rose-front', 'mens-nehru-jacket-silver-front'],
	'banner-women': ['floral-co-ord-set-orchid-front', 'floral-tiered-maxi-dress-blush-front', 'paisley-print-jumpsuit-rust-front', 'ikat-print-co-ord-set-aqua-front'],
	'banner-men': ['mens-embroidered-kurta-yellow-front', 'mens-nehru-jacket-maroon-front', 'mens-kurta-pyjama-set-royal-blue-front', 'mens-embroidered-kurta-navy-front'],
}

const slugify = (s) => s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')

// Every picture the catalogue names, by its name in the set: the engine
// names them the same way (samplePhotoName in core/sample_products.go).
const sources = new Map()
for (const p of catalogue.products) {
	for (const colour of p.colours) {
		for (const { shot } of p.photos) {
			sources.set(`${p.slug}-${slugify(colour.name)}-${shot}`, join(out, p.slug, `${slugify(colour.name)}-${shot}.webp`))
		}
	}
}

async function webp(img) {
	return img.webp({ quality: photo.quality, effort: 6 }).toBuffer()
}

async function banner(stems, width, height, across) {
	const tile = width / across
	const tiles = await Promise.all(stems.slice(0, across).map((stem, i) => {
		if (!sources.has(stem)) throw new Error(`${stem} is not a picture the catalogue names`)
		return sharp(sources.get(stem)).resize(tile, height, { fit: 'cover', position: 'north' }).toBuffer()
			.then((input) => ({ input, left: i * tile, top: 0 }))
	}))
	return webp(sharp({ create: { width, height, channels: 3, background: '#ffffff' } }).composite(tiles))
}

await mkdir(set, { recursive: true })
const written = new Map()
for (const [stem, source] of sources) {
	written.set(`${stem}.webp`, await webp(sharp(source).resize(photo.width, photo.height, { fit: 'cover' })))
}
for (const [name, stems] of Object.entries(banners)) {
	written.set(`${name}-wide.webp`, await banner(stems, 1500, 380, 4))
	written.set(`${name}-phone.webp`, await banner(stems, 720, 380, 2))
}

let total = 0
let largest = ['', 0]
for (const [name, body] of written) {
	await writeFile(join(set, name), body)
	total += body.length
	if (body.length > largest[1]) largest = [name, body.length]
}
for (const name of await readdir(set)) {
	if (!written.has(name)) await unlink(join(set, name))
}
const kb = (n) => `${(n / 1000).toFixed(0)} KB`
console.log(`${written.size} pictures, ${(total / 1e6).toFixed(2)} MB; the largest is ${largest[0]} at ${kb(largest[1])}`)
if (total > budget) throw new Error(`the set is ${total} bytes, over the ${budget} budget`)
