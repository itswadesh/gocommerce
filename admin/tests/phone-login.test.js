/*
 * The phone option (D193). Source-reading, because node:test cannot render a
 * .svelte file; the flow itself is driven in a browser before a release.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

import { phoneFragment } from "../src/lib/phone-link.js";

const read = (p) => readFileSync(new URL(p, import.meta.url), "utf8");
const login = read("../src/lib/components/Login.svelte");
const signup = read("../src/routes/admin/auth/signup/+page.svelte");
const form = read("../src/lib/components/PhoneCodeForm.svelte");
const account = read("../src/routes/dash/settings/account/+page.svelte");

test("a phone is offered only where the install says a code can be sent", () => {
    assert.match(login, /canPhone = !!s\.phone_login/);
    assert.match(login, /textsCode = \$derived\(canPhone && looksLikePhone\(identity\)\)/);
    assert.match(signup, /canPhone = !!s\.phone_signup/);
    assert.match(signup, /textsCode = \$derived\(canPhone && looksLikePhone\(email\)\)/);
    assert.ok(account.includes("canPhone = !!state.phone_login"));
    assert.ok(account.includes("canEmail = !!state.password_reset"));
});

test("where sign-up is shut, the screen does not say a code was sent", () => {
    assert.match(form, /If \{shown\} has an account here/);
    assert.match(login, /open=\{phoneOpen\}/);
});

test("a code field is one the phone can fill in", () => {
    assert.match(form, /autocomplete="one-time-code"/);
    assert.match(form, /inputmode="numeric"/);
    assert.match(account, /autocomplete="one-time-code"/);
});

test("signing up with a phone goes on to the create form, as an email does", () => {
    assert.match(signup, /onauthenticated=\{onward\}/);
    assert.match(signup, /select-store\?signup/);
});

test("the number can travel on to another screen", () => {
    assert.equal(phoneFragment("+919876543210"), "#phone=%2B919876543210");
    assert.equal(phoneFragment(""), "");
});

test("a link fills the box and only a press sends a code", () => {
    // One read of the fragment each: a second would find it already cleared.
    assert.equal(login.split("takePhoneFromFragment()").length, 2);
    assert.equal(signup.split("takePhoneFromFragment()").length, 2);
    assert.match(login, /let identity = \$state\(readablePhone\(linkedPhone\)\)/);
    assert.match(signup, /let email = \$state\(readablePhone\(linkedPhone\)\)/);
    for (const screen of [login, signup]) {
        assert.doesNotMatch(screen, /linkedPhone[^\n]*step = "code"/);
        assert.match(screen, /if \(step === "who"\) \{\s*step = textsCode \? "code" : "password";/);
    }
});

test("one box first: the password stays hidden until an email has been given", () => {
    for (const screen of [login, signup]) {
        const gate = screen.indexOf('{#if step === "password"}\n            <div in:rise>');
        assert.ok(gate > 0, "the password sits behind the password step");
        assert.ok(screen.search(/assword<\/label>/) > gate);
        assert.doesNotMatch(screen, /Text me a code instead|Use a password instead/);
    }
    assert.match(login, /\{step === "who" \? \(textsCode \? "Send code" : "Continue"\)/);
    assert.match(signup, /step === "who" && textsCode \? "Send code" : "Continue"/);
});

test("the installer still asks for an email and a password together", () => {
    assert.match(login, /if \(!installed\) step = "password"/);
});

test("somebody with no password can add an email and change their number, and is asked for no password", () => {
    assert.match(account, /\{#if !hasPassword\}[\s\S]*?\{#if canEmail\}[\s\S]*?onsubmit=\{submitEmail\}/);
    assert.match(account, /\{#if canPhone && \(!me\.phone \|\| !hasPassword\)\}/);
    // Each password box on the phone forms sits behind hasPassword.
    assert.match(account, /\{#if hasPassword\}\s*<div class="field required"[^>]*>\s*<label for="acc-phone-remove">/);
    assert.match(account, /\{#if hasPassword\}\s*<div class="delimiter"><\/div>\s*<div class="field required">\s*<label for="acc-phone-password">/);
});
