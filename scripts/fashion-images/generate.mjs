// Generates the fashion sample catalogue's photographs with Google Gemini.
//
//   cd scripts/fashion-images
//   npm install
//   node generate.mjs                     # every product; skips files already made
//   node generate.mjs --only wrap-midi-dress --force
//
// Reads GEMINI_API_KEY (or GOOGLE_API_KEY) from the environment and nothing
// else; the key is never printed or written anywhere.
//
// Consistency is the point. A shopper who picks another colour should see the
// same person in the same pose wearing the same cut, so only the first colour's
// front shot is drawn from text. Its other angles are edits of that front shot,
// and every other colour is a recolour edit of the first colour's matching
// shot. Prompts live in catalogue.json beside this file.
//
// Output: out/<product-slug>/<colour-slug>-<shot>.webp, 1200x1600, each under
// 200 KB, which is what seed-fashion.ps1 uploads. The model's own PNGs are kept
// in out/raw/ because the edits start from them, not from the lossy WebP.
// out/ is git-ignored: the set is around 150 files and several megabytes.
//
// A product's first-colour front shot is the base of all its others. Redo it
// with --only <slug> --force, which redoes the whole product, rather than by
// deleting that one file: the other shots would still show the old model.

import { readFile, writeFile, mkdir, access } from 'node:fs/promises'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'
import sharp from 'sharp'

const here = dirname(fileURLToPath(import.meta.url))
const out = join(here, 'out')
const catalogue = JSON.parse(await readFile(join(here, 'catalogue.json'), 'utf8'))

const args = process.argv.slice(2)
const only = args.includes('--only') ? args[args.indexOf('--only') + 1] : null
const force = args.includes('--force')
if (args.includes('--only') && !catalogue.products.some((p) => p.slug === only)) {
	console.error(`--only needs a product slug from catalogue.json`)
	process.exit(1)
}

const key = process.env.GEMINI_API_KEY || process.env.GOOGLE_API_KEY
if (!key) {
	console.error('GEMINI_API_KEY is not set. Set it in the environment and run again.')
	process.exit(1)
}

const slugify = (s) => s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '')
const exists = (p) => access(p).then(() => true, () => false)

// The Interactions API (v1beta). The response's image block is looked for
// anywhere in the body rather than at one path, because the API is still in
// beta and has already moved it once (output_image, steps[].content[]).
async function gemini(text, image) {
	const input = [{ type: 'text', text }]
	if (image) input.push({ type: 'image', mime_type: 'image/png', data: image.toString('base64') })
	const body = JSON.stringify({
		model: catalogue.image_model,
		input,
		response_format: { type: 'image', mime_type: 'image/png', aspect_ratio: '3:4' }
	})
	for (let attempt = 1; ; attempt++) {
		const res = await fetch('https://generativelanguage.googleapis.com/v1beta/interactions', {
			method: 'POST',
			headers: { 'x-goog-api-key': key, 'content-type': 'application/json' },
			body
		})
		if (res.ok) {
			const data = findImage(await res.json())
			if (data) return Buffer.from(data, 'base64')
			throw new Error('the response held no image (the prompt may have been refused)')
		}
		// Rate limits and the odd 5xx are routine on a batch this size.
		if ((res.status === 429 || res.status >= 500) && attempt < 5) {
			await new Promise((r) => setTimeout(r, 2000 * 2 ** attempt))
			continue
		}
		throw new Error(`Gemini answered ${res.status}: ${(await res.text()).slice(0, 300)}`)
	}
}

function findImage(node) {
	if (!node || typeof node !== 'object') return null
	if (node.type === 'image' && typeof node.data === 'string') return node.data
	if (node.inlineData?.data) return node.inlineData.data
	if (node.inline_data?.data) return node.inline_data.data
	for (const [k, v] of Object.entries(node)) {
		// A response that echoes the request would otherwise hand back the
		// source picture of an edit as if it were the result.
		if (k === 'input') continue
		const found = findImage(v)
		if (found) return found
	}
	return null
}

// Highest quality that fits the 200 KB budget; a phone on mobile data pays
// for every byte of a gallery it swipes through.
async function toWebp(png, file) {
	const img = sharp(png).resize(1200, 1600, { fit: 'cover' })
	for (let quality = 82; quality >= 40; quality -= 6) {
		const buf = await img.clone().webp({ quality }).toBuffer()
		if (buf.length < 200 * 1024 || quality <= 40) {
			await writeFile(file, buf)
			return buf.length
		}
	}
}

let made = 0
let failed = 0
for (const p of catalogue.products) {
	if (only && p.slug !== only) continue
	const rawDir = join(out, 'raw', p.slug)
	const webDir = join(out, p.slug)
	await mkdir(rawDir, { recursive: true })
	await mkdir(webDir, { recursive: true })

	const [first, ...rest] = p.colours
	const outfit = (c) => p.image.garment.replace('{colour}', c.describe)
	const scene = (c) =>
		`${catalogue.style}\nThe model: ${p.image.model}.\nThe outfit: ${outfit(c)}.\nThe setting: ${p.image.setting}.`

	// Each job names its file and how to make it from files already made, so
	// the order below is the dependency order.
	const jobs = []
	const raw = (c, shot) => join(rawDir, `${slugify(c.name)}-${shot}.png`)
	for (const shot of catalogue.shots) {
		jobs.push({
			colour: first, shot: shot.name,
			make: async () =>
				shot.name === 'front'
					? gemini(`${scene(first)}\n${shot.prompt}`)
					: gemini(`${shot.prompt}\nThe outfit: ${outfit(first)}.`, await readFile(raw(first, 'front')))
		})
	}
	for (const c of rest) {
		for (const shot of catalogue.shots) {
			jobs.push({
				colour: c, shot: shot.name,
				make: async () =>
					gemini(catalogue.recolour.replace('{colour}', c.describe), await readFile(raw(first, shot.name)))
			})
		}
	}

	for (const job of jobs) {
		const rawFile = raw(job.colour, job.shot)
		const webFile = join(webDir, `${slugify(job.colour.name)}-${job.shot}.webp`)
		try {
			// The WebP is what a store gets, so one that exists is done; the raw
			// alone missing (out/raw cleared to save space) is not worth a call.
			if (!force && (await exists(webFile))) continue
			if (force || !(await exists(rawFile))) {
				// Re-encoded as PNG whatever the model returned, because the
				// edits send it back declared as image/png.
				await writeFile(rawFile, await sharp(await job.make()).png().toBuffer())
			}
			const size = await toWebp(await readFile(rawFile), webFile)
			made++
			console.log(`  ${p.slug}/${slugify(job.colour.name)}-${job.shot}.webp  ${Math.round(size / 1024)} KB`)
		} catch (err) {
			failed++
			console.error(`  FAILED ${p.slug} ${job.colour.name} ${job.shot}: ${err.message}`)
		}
	}
}
console.log(`${made} image(s) written, ${failed} failed`)
process.exit(failed ? 1 : 0)
