/*
 * The product SEO score.
 *
 * It is a number an operator will act on — reorder a title, rewrite a
 * sentence — so the rules behind it have to be pinned. The expensive mistake
 * here is a check that silently never fires: a rule nobody can satisfy reads
 * as "this product is bad" forever, and a rule everybody satisfies is a bar
 * that teaches nothing. Both look identical on screen.
 */

import { test } from "node:test";
import assert from "node:assert/strict";

import { analyse, BANDS } from "../src/lib/seo.js";

/** A product that passes everything, so each test can spoil one thing. */
function good(over = {}) {
    return {
        keyword: "cotton tee",
        title: "Cotton tee",
        seoTitle: "Cotton tee — soft organic everyday shirt",
        seoDescription:
            "Our cotton tee is a soft organic shirt cut for everyday wear, pre-shrunk and " +
            "stitched to last. Free returns within thirty days.",
        slug: "cotton-tee",
        description:
            "<p>The cotton tee is the shirt we wear most. " +
            "Combed organic cotton, pre-shrunk so the first wash changes nothing, and a " +
            "collar that keeps its shape. Cut straight through the body with a sleeve that " +
            "sits just above the elbow. Machine wash cold and hang to dry; it will outlast " +
            "the season and most of the ones after it. Made in a mill we have bought from " +
            "for nine years, in a weight that works alone in August and under a jumper in " +
            "November. Every size is cut from the same pattern.</p>",
        images: [{ alt: "A folded cotton tee" }],
        categoryId: 4,
        ...over,
    };
}

const by = (result, key) => result.checks.find((c) => c.key === key);

test("a product that does everything right scores in the top band", () => {
    const r = analyse(good());
    assert.equal(r.failed.length, 0, "unexpected failures: " + r.failed.map((c) => c.key).join(", "));
    assert.equal(r.score, 100);
    assert.equal(r.band, "great");
});

test("with no focus keyword the keyword checks stand down rather than failing", () => {
    // RankMath's own behaviour, and the honest one: "does the keyword appear
    // in the slug" has no answer until there is a keyword. Reporting six
    // separate failures for one missing field buries the one thing to do.
    const r = analyse(good({ keyword: "" }));
    assert.equal(by(r, "keyword").ok, false);
    for (const key of ["keyword-in-title", "keyword-in-slug", "keyword-in-body"]) {
        assert.equal(by(r, key).skipped, true, key + " should be waiting on a keyword");
    }
    // The score is only over what could be judged, so a product with no
    // keyword is not also punished for the checks that depend on one: the
    // missing keyword is the single failure, and the score is the share of the
    // remaining weight that was earned.
    assert.deepEqual(r.failed.map((c) => c.key), ["keyword"]);
    const judged = r.checks.filter((c) => !c.skipped);
    const possible = judged.reduce((n, c) => n + c.weight, 0);
    assert.equal(r.score, Math.round(((possible - 5) / possible) * 100));
    // Good, not excellent: a product nobody has chosen a keyword for has not
    // been written for search, however well the rest of it is filled in.
    assert.equal(r.band, "good");
});

test("the keyword is matched case-insensitively and across the slug's hyphens", () => {
    const r = analyse(good({ keyword: "COTTON Tee", slug: "blue-cotton-tee-2" }));
    assert.equal(by(r, "keyword-in-slug").ok, true);
    assert.equal(by(r, "keyword-in-title").ok, true);
});

test("a title that merely contains the keyword scores lower than one that opens with it", () => {
    const opens = analyse(good());
    const buried = analyse(good({ seoTitle: "Soft organic everyday cotton tee" }));
    assert.equal(by(opens, "keyword-opens-title").ok, true);
    assert.equal(by(buried, "keyword-opens-title").ok, false);
    assert.equal(by(buried, "keyword-in-title").ok, true, "it is still in there");
    assert.ok(buried.score < opens.score);
});

test("markup is not content", () => {
    // A description of nothing but tags is empty, however many bytes it is.
    const r = analyse(good({ description: "<div><span></span><br/><img src='x.png'/></div>" }));
    assert.equal(by(r, "body-length").ok, false);
    assert.equal(r.words, 0);
});

test("a title or description outside its band is called out in both directions", () => {
    assert.equal(by(analyse(good({ seoTitle: "Tee" })), "title-length").ok, false);
    assert.equal(
        by(analyse(good({ seoTitle: "Cotton tee ".repeat(12) })), "title-length").ok,
        false,
    );
    assert.equal(by(analyse(good({ seoDescription: "Short." })), "description-length").ok, false);
    assert.equal(
        by(analyse(good({ seoDescription: "Cotton tee. ".repeat(40) })), "description-length").ok,
        false,
    );
});

test("the page title falls back to the product title, the way the storefront does", () => {
    const r = analyse(good({ seoTitle: "", title: "Cotton tee — soft organic everyday shirt" }));
    assert.equal(by(r, "keyword-in-title").ok, true);
    assert.equal(by(r, "title-length").ok, true);
});

test("keyword density has a floor and a ceiling", () => {
    const stuffed = "cotton tee ".repeat(40);
    assert.equal(by(analyse(good({ description: stuffed })), "keyword-density").ok, false);

    // One mention in a long body is under the floor.
    const thin = "word ".repeat(1000) + "cotton tee";
    const r = analyse(good({ description: thin }));
    assert.ok(r.density < 0.4, "density was " + r.density);
    assert.equal(by(r, "keyword-density").ok, false);
});

test("a picture with no alt text fails, and so does no picture at all", () => {
    assert.equal(by(analyse(good({ images: [] })), "image").ok, false);
    assert.equal(by(analyse(good({ images: [] })), "image-alt").skipped, true);
    assert.equal(
        by(analyse(good({ images: [{ alt: "A folded tee" }, { alt: "  " }] })), "image-alt").ok,
        false,
    );
});

test("every check carries a weight and the weights total one hundred", () => {
    const r = analyse(good());
    const total = r.checks.reduce((n, c) => n + c.weight, 0);
    assert.equal(total, 100);
    for (const c of r.checks) {
        assert.ok(c.label, c.key + " has no label");
        assert.ok(c.hint, c.key + " has no hint to act on");
    }
});

test("the bands run in order and cover nought to a hundred", () => {
    assert.equal(analyse({ keyword: "", title: "", slug: "", description: "" }).band, "bad");
    let previous = -1;
    for (const b of BANDS) {
        assert.ok(b.min > previous, "bands overlap at " + b.key);
        previous = b.min;
    }
    assert.equal(BANDS[0].min, 0);
});

test("nothing at all is a score, not a crash", () => {
    const r = analyse(undefined);
    assert.equal(typeof r.score, "number");
    assert.ok(r.score >= 0 && r.score <= 100);
});

test("a skipped check says what it is waiting for", () => {
    // Two different things cause a skip, and the panel used to report both as
    // "waiting on a focus keyword" — which is wrong whenever the keyword is
    // set and the picture is the thing missing, and that is the common case.
    const noKeyword = analyse(good({ keyword: "" }));
    for (const c of noKeyword.skipped) {
        assert.match(c.why ?? "", /focus keyword/, c.key + " does not say what it waits on");
    }

    const noPicture = analyse(good({ images: [] }));
    const alt = by(noPicture, "image-alt");
    assert.equal(alt.skipped, true);
    assert.match(alt.why ?? "", /picture/, "image-alt should say it is waiting on a picture");
    // And with a keyword set, nothing claims to be waiting on one.
    for (const c of noPicture.skipped) {
        assert.doesNotMatch(c.why ?? "", /focus keyword/, c.key + " blames the keyword wrongly");
    }
});
