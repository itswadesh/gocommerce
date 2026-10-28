/**
 * Create from photos: the review step's edits, as plain functions.
 *
 * The engine proposes products from a batch of photos; the shop corrects the
 * proposal before anything is created — moves a photo, merges two products,
 * splits one off, leaves one out. Every edit is a function from the list of
 * proposals to a new list, so the screen holds one array and node can test
 * the rules without a browser.
 *
 * A proposal is `{ id?, draft, skip? }`; a draft holds `variants`, each
 * `{ value, photos: [photo ids] }`. A photo belongs to at most one variant.
 */

/** The sizes the engine sends the model copies at, in pixels on the long side. */
export const THUMB_SIDE = 512;
export const IMAGE_SIDE = 1024;

// JSON rather than structuredClone: the screen hands in Svelte state, and a
// proxy cannot be structured-cloned.
const clone = (items) => JSON.parse(JSON.stringify(items));

/** Where a photo is: `[proposal index, variant index]`, or null. */
export function findPhoto(items, photoId) {
    for (let i = 0; i < items.length; i++) {
        const variants = items[i].draft.variants;
        for (let v = 0; v < variants.length; v++) {
            if (variants[v].photos.includes(photoId)) return [i, v];
        }
    }
    return null;
}

/**
 * movePhoto takes a photo out of where it is and puts it in another variant,
 * or, with `to` of "new", in a product of its own. A variant left with no
 * photo goes, and so does a product left with no variant.
 */
export function movePhoto(items, photoId, to) {
    const out = clone(items);
    const at = findPhoto(out, photoId);
    if (!at) return out;
    const [i, v] = at;
    out[i].draft.variants[v].photos = out[i].draft.variants[v].photos.filter((p) => p !== photoId);
    if (to === "new") {
        out.push({ draft: blankDraft(out[i].draft, [{ value: "", photos: [photoId] }]) });
    } else {
        const [ti, tv] = to;
        out[ti].draft.variants[tv].photos.push(photoId);
    }
    return tidy(out);
}

/** mergeInto moves every variant of proposal `from` into proposal `into`. */
export function mergeInto(items, from, into) {
    if (from === into) return clone(items);
    const out = clone(items);
    const target = out[into].draft;
    const source = out[from].draft;
    // Two single-variant products merged are two variants of one: each needs
    // a name to be told apart by, and the product's own title is the one the
    // shop already gave it.
    if (target.variants.length === 1 && !target.variants[0].value) target.variants[0].value = target.title;
    for (const v of source.variants) {
        target.variants.push({ value: v.value || source.title, photos: [...v.photos] });
    }
    if (!target.option_name) target.option_name = "Style";
    out.splice(from, 1);
    return tidy(out);
}

/** setSkip leaves a proposal out of what is created, or puts it back. */
export function setSkip(items, index, skip) {
    const out = clone(items);
    out[index].skip = skip;
    return out;
}

/**
 * tidy drops what is empty and keeps one variant honest: a product with one
 * variant has no option to choose, so neither an option name nor a value.
 */
export function tidy(items) {
    return items
        .map((it) => {
            const variants = it.draft.variants.filter((v) => v.photos.length > 0);
            const draft = { ...it.draft, variants };
            if (variants.length === 1) {
                draft.option_name = "";
                draft.variants = [{ ...variants[0], value: "" }];
            }
            return { ...it, draft };
        })
        .filter((it) => it.draft.variants.length > 0);
}

/**
 * problems is what stops a Create: a proposal with no title, or two variants
 * of one product with the same value, which the catalogue would refuse.
 */
export function problems(items) {
    const out = [];
    items.forEach((it, i) => {
        if (it.skip) return;
        const name = it.draft.title?.trim() || `Product ${i + 1}`;
        if (!it.draft.title?.trim()) out.push(`Product ${i + 1} needs a title.`);
        if (it.draft.variants.length > 1) {
            const seen = new Set();
            for (const v of it.draft.variants) {
                const key = (v.value ?? "").trim().toLowerCase();
                if (!key) out.push(`${name}: every variant needs a name.`);
                else if (seen.has(key)) out.push(`${name}: two variants are called ${v.value.trim()}.`);
                seen.add(key);
            }
        }
    });
    return out;
}

/** saveBody is the PUT the review screen sends: what is kept, in order. */
export function saveBody(items) {
    return {
        items: items
            .filter((it) => !it.skip && it.product_id == null)
            .map((it) => (it.id ? { id: it.id, draft: it.draft } : { draft: it.draft })),
    };
}

/** sizesFrom reads the sizes box: commas or spaces, each once. */
export function sizesFrom(text) {
    const seen = new Set();
    const out = [];
    for (const s of String(text ?? "").split(/[,\n]+/)) {
        const t = s.trim();
        if (t && !seen.has(t.toLowerCase())) {
            seen.add(t.toLowerCase());
            out.push(t);
        }
    }
    return out;
}

/** progress is the bar: a fraction, or null while the count is unknown. */
export function progress(batch) {
    if (!batch || !batch.step_total) return null;
    return Math.min(1, batch.step_done / batch.step_total);
}

/** costMinor is a batch's spend in minor units, from the gateway's millionths. */
export function costMinor(batch) {
    return Math.round((batch?.cost_micro_minor ?? 0) / 1e6);
}

function blankDraft(from, variants) {
    return {
        title: "New product",
        description: "",
        highlights: [],
        product_type: from.product_type ?? "",
        tags: [],
        category_id: from.category_id ?? null,
        seo_title: "",
        seo_description: "",
        suggested_category: "",
        hs_code: "",
        option_name: "",
        sizes: [...(from.sizes ?? [])],
        variants,
        alts: { ...(from.alts ?? {}) },
    };
}

/**
 * downscale draws a photo at most `side` pixels on its long side and returns
 * it as JPEG bytes in base64, the form the engine takes. The browser's own
 * decoder, so a format it cannot read (HEIC on most) throws, and the photo is
 * reported rather than the batch failing.
 */
export async function downscale(file, side, quality = 0.82) {
    const bitmap = await createImageBitmap(file, { imageOrientation: "from-image" });
    const scale = Math.min(1, side / Math.max(bitmap.width, bitmap.height));
    const w = Math.max(1, Math.round(bitmap.width * scale));
    const h = Math.max(1, Math.round(bitmap.height * scale));
    const canvas = new OffscreenCanvas(w, h);
    const ctx = canvas.getContext("2d");
    // A transparent PNG drawn onto JPEG would turn black where it is clear.
    ctx.fillStyle = "#fff";
    ctx.fillRect(0, 0, w, h);
    ctx.drawImage(bitmap, 0, 0, w, h);
    bitmap.close();
    const blob = await canvas.convertToBlob({ type: "image/jpeg", quality });
    const bytes = new Uint8Array(await blob.arrayBuffer());
    let binary = "";
    for (let i = 0; i < bytes.length; i += 0x8000) binary += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
    return btoa(binary);
}
