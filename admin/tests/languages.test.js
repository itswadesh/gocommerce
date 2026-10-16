import { test } from "node:test";
import assert from "node:assert/strict";
import { LANGUAGES, languageName, languageOptions } from "../src/lib/languages.js";

test("every language of India's Eighth Schedule is offered, named in English and in itself", () => {
    for (const tag of ["hi", "or", "bn", "ta", "te", "mr", "sat", "mni"]) {
        assert.ok(LANGUAGES.some((l) => l.value === tag), tag);
    }
    const hindi = LANGUAGES.find((l) => l.value === "hi");
    assert.match(hindi.label, /^Hindi/);
    assert.ok(hindi.label.includes("(hi)"), hindi.label);
    assert.ok(LANGUAGES.some((l) => l.value === "en"));
});

test("a language is named in English, then in itself where that differs", () => {
    assert.match(languageName("hi"), /^Hindi · हिन्दी$/);
    assert.equal(languageName("en"), "English");
    assert.equal(languageName("x-unknown"), "x-unknown");
});

test("what a store already sells in is not offered again", () => {
    const left = languageOptions(["en", "HI"]);
    assert.ok(!left.some((l) => l.value === "en" || l.value === "hi"));
    assert.equal(left.length, LANGUAGES.length - 2);
});
