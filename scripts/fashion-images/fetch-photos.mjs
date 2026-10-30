// Downloads the fashion sample catalogue's photographs and writes the files
// that describe them.
//
//   cd scripts/fashion-images
//   npm install
//   node fetch-photos.mjs            # downloads what out/ is missing
//   node fetch-photos.mjs --force    # downloads every photograph again
//
// The photographs are free stock (Unsplash and Pexels licences), listed per
// product in core/sample/catalogue.json, the sample shop's catalogue, with the
// page each came from. Each product was photographed in one colour, its first;
// that colour's files are downloaded here as out/<product>/<colour>-<shot>.webp,
// cropped to 3:4 and at most 1200x1600 under 200 KB, which is what
// seed-fashion.ps1 uploads and build-sample.mjs shrinks into the engine.
//
// The product's other colours are edits of those files, made by hand with an
// image editor and saved beside them under their own colour's name. This
// writes recolour.json, one entry per file to make with its source, target and
// prompt, and CREDITS.md, the attribution for every photograph. Both are
// rewritten from catalogue.json on every run; edit the catalogue, not them.
//
// out/ is git-ignored: the photographs are not ours to redistribute as a set.

import { readFile, writeFile, mkdir, access } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import sharp from 'sharp'

const here = dirname(fileURLToPath(import.meta.url))
const out = join(here, 'out')
const catalogue = JSON.parse(await readFile(join(here, '..', '..', 'core', 'sample', 'catalogue.json'), 'utf8'))
const force = process.argv.includes('--force')

const slugify = (s) => s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
const exists = (p) => access(p).then(() => true, () => false)
const file = (p, colour, shot) => `${p.slug}/${slugify(colour.name)}-${shot}.webp`

// ------------------------------------------------------------- recolour.json

const recolour = []
for (const p of catalogue.products) {
	const [base, ...others] = p.colours
	for (const colour of others) {
		for (const photo of p.photos) {
			recolour.push({
				product: p.slug,
				colour: colour.name,
				hex: colour.hex,
				shot: photo.shot,
				source: `out/${file(p, base, photo.shot)}`,
				target: `out/${file(p, colour, photo.shot)}`,
				prompt: `Change only the colour of the ${p.garment} to ${colour.name.toLowerCase()} (${colour.hex}); keep the model, face, hair, pose, background, lighting, fabric texture, print pattern and embroidery exactly the same, and leave every other garment and accessory as it is.`,
			})
		}
	}
}
await writeFile(join(here, 'recolour.json'), JSON.stringify(recolour, null, '\t') + '\n')

// ---------------------------------------------------------------- CREDITS.md

const lines = [
	'# Photo credits',
	'',
	'Written by `fetch-photos.mjs` from `core/sample/catalogue.json`; edit the catalogue, not this file.',
	'',
	'Each product was photographed in its first colour. Its other colours are colour',
	'variants edited with ChatGPT from the original photo, which both licences allow.',
	'',
	'| Product | Shot | Photographer | Photo | Licence |',
	'| --- | --- | --- | --- | --- |',
]
for (const p of catalogue.products) {
	for (const photo of p.photos) {
		lines.push(`| ${p.title} | ${photo.shot} | [${photo.photographer}](${photo.profile}) | [${photo.page.replace(/^https:\/\/(www\.)?/, '')}](${photo.page}) | [${photo.licence}](${catalogue.licences[photo.licence]}) |`)
	}
}
await writeFile(join(here, 'CREDITS.md'), lines.join('\n') + '\n')

// ---------------------------------------------------------------- photographs

// Asked for at 2000 px wide so the 3:4 crop still has 1200x1600 to give.
function downloadUrl(image) {
	if (image.startsWith('https://images.unsplash.com/')) return `${image}?fm=jpg&q=90&w=2000`
	if (image.startsWith('https://images.pexels.com/')) return `${image}?auto=compress&cs=tinysrgb&w=2000`
	throw new Error(`not an Unsplash or Pexels image address: ${image}`)
}

// Steps the quality down, then the size, until the file is under 200 KB: a
// plain backdrop fits at 1200x1600 and high quality, while a lehenga covered
// in sequins does not fit at any quality worth looking at without shrinking.
async function toWebp(buf) {
	for (const width of [1200, 1080, 960]) {
		const img = sharp(buf).resize(width, width * 4 / 3, { fit: 'cover', position: 'centre' })
		for (const quality of [82, 74, 66]) {
			const webp = await img.clone().webp({ quality, effort: 6 }).toBuffer()
			if (webp.length < 200_000) return webp
		}
	}
	throw new Error('does not fit in 200 KB even at 960x1280')
}

let fetched = 0
for (const p of catalogue.products) {
	await mkdir(join(out, p.slug), { recursive: true })
	for (const photo of p.photos) {
		const target = join(out, file(p, p.colours[0], photo.shot))
		if (!force && (await exists(target))) continue
		const res = await fetch(downloadUrl(photo.image))
		if (!res.ok) throw new Error(`${photo.page}: HTTP ${res.status}`)
		// Upright first, as its own image: metadata() reports the stored size,
		// which an EXIF-rotated photograph has the wrong way round.
		let img = sharp(await sharp(Buffer.from(await res.arrayBuffer())).rotate().toBuffer())
		// crop is a box in fractions of the photograph, for a subject the centre
		// crop would leave small or beside something that is not for sale. It
		// keeps the source's shape: left/top place it, size scales both sides.
		if (photo.crop) {
			const { width, height } = await img.metadata()
			const { left, top, size } = photo.crop
			img = sharp(await img.extract({
				left: Math.round(left * width), top: Math.round(top * height),
				width: Math.round(size * width), height: Math.round(size * height),
			}).toBuffer())
		}
		await writeFile(target, await toWebp(await img.toBuffer()))
		fetched++
		console.log(`fetched  ${file(p, p.colours[0], photo.shot)}`)
	}
}
console.log(`${fetched} photograph(s) fetched; ${recolour.length} colour edits listed in recolour.json`)
