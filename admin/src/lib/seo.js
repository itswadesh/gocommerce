/**
 * How well a product is written for search, scored the way RankMath scores a
 * post: a list of checks, each worth a fixed number of points, and a total out
 * of a hundred with a band around it.
 *
 * The point of a score rather than a checklist is that it ranks the work. A
 * product editor already has a dozen fields that could be improved and no
 * signal about which of them matters; "you are at 62, and the biggest single
 * thing missing is the keyword in the handle" is a next action, where twelve
 * grey checkboxes are a chore.
 *
 * Three decisions worth stating, because they are the ones a reader will
 * second-guess:
 *
 * It is pure, and takes a plain object rather than the product. The editor
 * scores what is *in the form*, not what was last saved — a score that only
 * moves after Save teaches nothing while you are typing — and a function that
 * reaches into component state cannot be tested.
 *
 * A check with no keyword is skipped, not failed. "Does the focus keyword
 * appear in the handle" has no answer until there is a keyword, and reporting
 * six failures for one missing field buries the single thing to do. Skipped
 * checks leave the scale, so a product with no keyword is judged on what it
 * does have and told, once, to set one.
 *
 * Nothing here talks to the storefront. A real audit would fetch the rendered
 * page and look at its headings, its internal links and its response time; the
 * engine does not serve the storefront and cannot see any of that. These are
 * the checks that can be answered honestly from the product itself, and the
 * screen says so rather than implying a full crawl.
 */

/** The bands, in order. `min` is inclusive. */
export const BANDS = [
    { key: "bad", min: 0, label: "Needs work" },
    { key: "ok", min: 51, label: "Getting there" },
    { key: "good", min: 71, label: "Good" },
    { key: "great", min: 91, label: "Excellent" },
];

export function bandFor(score) {
    let found = BANDS[0];
    for (const b of BANDS) if (score >= b.min) found = b;
    return found;
}

/* Tags out, entities to spaces, runs of whitespace to one. A description that
   is markup all the way down has no words in it however many bytes it is. */
function plain(html) {
    return String(html ?? "")
        .replace(/<[^>]*>/g, " ")
        .replace(/&nbsp;/gi, " ")
        .replace(/&[a-z]+;/gi, " ")
        .replace(/\s+/g, " ")
        .trim();
}

/* Comparison form: lower case, and every run of non-letters becomes one space
   so a hyphenated handle and a spaced phrase compare equal. Without this
   "cotton tee" never matches "blue-cotton-tee", which is the single most
   common way a keyword is actually written into a URL. */
function loose(text) {
    return String(text ?? "")
        .toLowerCase()
        .replace(/[^\p{L}\p{N}]+/gu, " ")
        .trim();
}

function words(text) {
    const t = plain(text);
    return t ? t.split(/\s+/).length : 0;
}

/* How many times the phrase occurs, on the loose form so punctuation between
   the words does not hide an occurrence. */
function occurrences(haystack, needle) {
    const h = loose(haystack);
    const n = loose(needle);
    if (!h || !n) return 0;
    let count = 0;
    let at = h.indexOf(n);
    while (at !== -1) {
        count++;
        at = h.indexOf(n, at + n.length);
    }
    return count;
}

function contains(haystack, needle) {
    return occurrences(haystack, needle) > 0;
}

// The bands each length check accepts. A page title over about sixty
// characters is cut off in a result; under fifteen it is usually just the
// product name and wastes the line. A meta description under seventy leaves
// the engine to invent one, and over a hundred and fifty-five is truncated.
const TITLE_MIN = 15;
const TITLE_MAX = 60;
const DESCRIPTION_MIN = 70;
const DESCRIPTION_MAX = 155;
const BODY_MIN_WORDS = 60;
const DENSITY_MIN = 0.4;
const DENSITY_MAX = 2.5;

/**
 * Score one product.
 *
 * @param {{
 *   keyword?: string, title?: string, seoTitle?: string, seoDescription?: string,
 *   slug?: string, description?: string, images?: {alt?: string}[], categoryId?: number|null,
 * }} [input]
 */
export function analyse(input) {
    const p = input ?? {};
    const keyword = String(p.keyword ?? "").trim();
    // What a search engine would actually show, which is the SEO title when
    // there is one and the product's own title when there is not. Scoring the
    // empty box instead would mark down every product that never needed an
    // override.
    const pageTitle = String(p.seoTitle ?? "").trim() || String(p.title ?? "").trim();
    const metaDescription = String(p.seoDescription ?? "").trim();
    const slug = String(p.slug ?? "").trim();
    const body = plain(p.description);
    const bodyWords = words(p.description);
    const images = Array.isArray(p.images) ? p.images : [];
    const hasKeyword = keyword.length > 0;

    const density = bodyWords > 0 ? (occurrences(body, keyword) * loose(keyword).split(" ").length * 100) / bodyWords : 0;

    const checks = [
        {
            key: "keyword",
            weight: 5,
            label: "A focus keyword is set",
            ok: hasKeyword,
            hint: "Name the phrase a buyer would type. Everything below is measured against it.",
        },
        {
            key: "keyword-in-title",
            weight: 10,
            label: "The keyword is in the page title",
            needsKeyword: true,
            ok: contains(pageTitle, keyword),
            hint: "The title is the line a searcher reads. Work the phrase into it.",
        },
        {
            key: "keyword-opens-title",
            weight: 5,
            label: "The page title opens with the keyword",
            needsKeyword: true,
            ok: hasKeyword && loose(pageTitle).startsWith(loose(keyword)),
            hint: "A phrase at the front of the title carries more weight than one buried at the end.",
        },
        {
            key: "keyword-in-description",
            weight: 10,
            label: "The keyword is in the meta description",
            needsKeyword: true,
            ok: contains(metaDescription, keyword),
            hint: "The words a searcher typed are bolded in the result, which is what makes them click.",
        },
        {
            key: "keyword-in-slug",
            weight: 10,
            label: "The keyword is in the URL handle",
            needsKeyword: true,
            ok: contains(slug, keyword),
            hint: "Handles are read by people as well as crawlers. Hyphens count as spaces.",
        },
        {
            key: "keyword-in-body",
            weight: 10,
            label: "The keyword is in the description",
            needsKeyword: true,
            ok: contains(body, keyword),
            hint: "Use the phrase in the product copy at least once, in a sentence that reads naturally.",
        },
        {
            key: "keyword-density",
            weight: 5,
            label: "The keyword appears often enough, and not too often",
            needsKeyword: true,
            ok: density >= DENSITY_MIN && density <= DENSITY_MAX,
            hint: `Aim for ${DENSITY_MIN}% to ${DENSITY_MAX}% of the description. This one is at ${density.toFixed(1)}%.`,
        },
        {
            key: "title-length",
            weight: 10,
            label: "The page title is a usable length",
            ok: pageTitle.length >= TITLE_MIN && pageTitle.length <= TITLE_MAX,
            hint: `Between ${TITLE_MIN} and ${TITLE_MAX} characters. This one is ${pageTitle.length}.`,
        },
        {
            key: "description-length",
            weight: 10,
            label: "The meta description is a usable length",
            ok: metaDescription.length >= DESCRIPTION_MIN && metaDescription.length <= DESCRIPTION_MAX,
            hint: `Between ${DESCRIPTION_MIN} and ${DESCRIPTION_MAX} characters. This one is ${metaDescription.length}.`,
        },
        {
            key: "body-length",
            weight: 10,
            label: "There is enough description to rank",
            ok: bodyWords >= BODY_MIN_WORDS,
            hint: `At least ${BODY_MIN_WORDS} words. This one has ${bodyWords}.`,
        },
        {
            key: "image",
            weight: 5,
            label: "The product has a picture",
            ok: images.length > 0,
            hint: "Every platform and every result page wants one, and a listing without one is skipped.",
        },
        {
            key: "image-alt",
            weight: 5,
            label: "Every picture has alt text",
            // Nothing to judge until there is a picture; the check above
            // already says what is wrong.
            skipped: images.length === 0,
            why: "Waiting on a picture",
            ok: images.length > 0 && images.every((i) => String(i?.alt ?? "").trim().length > 0),
            hint: "Describe the picture in a few words. It is what a screen reader says and what image search reads.",
        },
        {
            key: "category",
            weight: 5,
            label: "The product is in a category",
            ok: p.categoryId != null && p.categoryId !== "",
            hint: "A category is how the feeds classify the item and how the storefront groups it.",
        },
    ];

    for (const c of checks) {
        if (c.needsKeyword && !hasKeyword) {
            c.skipped = true;
            // Why, and not just that: two different things cause a skip, and a
            // panel that blames the keyword for both is wrong exactly when the
            // keyword is set and the picture is what is missing.
            c.why = "Waiting on a focus keyword";
        }
        c.ok = !!c.ok && !c.skipped;
        if (!c.skipped) delete c.why;
    }

    // Out of what could be judged, so skipping never counts against a product.
    const judged = checks.filter((c) => !c.skipped);
    const possible = judged.reduce((n, c) => n + c.weight, 0);
    const earned = judged.reduce((n, c) => n + (c.ok ? c.weight : 0), 0);
    const score = possible === 0 ? 0 : Math.round((earned / possible) * 100);

    return {
        score,
        band: bandFor(score).key,
        bandLabel: bandFor(score).label,
        keyword,
        words: bodyWords,
        density,
        checks,
        passed: checks.filter((c) => c.ok),
        failed: checks.filter((c) => !c.ok && !c.skipped),
        skipped: checks.filter((c) => c.skipped),
    };
}
