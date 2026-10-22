/*
 * The Contact screen holds two kinds of message — the contact form's and
 * product enquiries — and answers either from the panel. What it says about a
 * message, and what a reply is called before anybody types a subject, comes
 * from here.
 */

import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { KIND_OPTIONS, kindParam, mailtoHref, productFacts, replySubject, sentByLabel } from "../src/lib/contact.js";

test("a reply is called after the conversation, so the shopper's mail threads it", () => {
    assert.equal(replySubject({ subject: "An order" }), "Re: An order");
    assert.equal(replySubject({ subject: "Re: An order" }), "Re: An order");
    assert.equal(replySubject({ subject: "", product: { title: "Blue mug" } }), "Re: Blue mug");
    assert.equal(replySubject({ subject: "" }), "Re: your message");
    assert.equal(replySubject(null), "Re: your message");
});

test("the kind filter is all, the contact form's, or enquiries — and all asks for nothing", () => {
    assert.deepEqual(
        KIND_OPTIONS.map((o) => o.value),
        ["all", "message", "enquiry"],
    );
    assert.equal(kindParam("all"), "");
    assert.equal(kindParam(""), "");
    assert.equal(kindParam("enquiry"), "enquiry");
    assert.equal(kindParam("message"), "message");
    // A hand-typed URL cannot send the engine a kind it refuses.
    assert.equal(kindParam("complaint"), "");
});

test("an enquiry's product reads as what was asked: the variant and how many", () => {
    assert.deepEqual(productFacts({ id: 7, title: "Blue mug" }), []);
    assert.deepEqual(productFacts({ id: 7, title: "Blue mug", variant_title: "Large / Blue", quantity: 40 }), [
        "Large / Blue",
        "40 wanted",
    ]);
    assert.deepEqual(productFacts({ id: 7, title: "Blue mug", quantity: 1 }), ["1 wanted"]);
    assert.deepEqual(productFacts(null), []);
});

test("who replied is the operator, or says what kind of caller it was", () => {
    assert.equal(sentByLabel("mia@example.com"), "mia@example.com");
    assert.equal(sentByLabel("token"), "the admin token");
    assert.equal(sentByLabel("api key: Zapier"), "API key “Zapier”");
    assert.equal(sentByLabel(""), "someone");
});

test("the mail-app reply keeps the same subject as the panel's", () => {
    assert.equal(
        mailtoHref({ email: "ann@example.com", subject: "An order" }),
        "mailto:ann@example.com?subject=Re%3A%20An%20order",
    );
});

test("the screen gates on the module's own rights, not the ones they were lifted off", () => {
    const page = readFileSync(new URL("../src/routes/dash/contact/+page.svelte", import.meta.url), "utf8");
    assert.match(page, /can\("contact\.read"\)/);
    assert.match(page, /can\("contact\.write"\)/);
    assert.match(page, /can\("contact\.reply"\)/);
    assert.doesNotMatch(page, /can\("store\.operate"\)/);
});
