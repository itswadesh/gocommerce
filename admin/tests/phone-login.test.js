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
    assert.match(login, /\{#if installed && canPhone\}/);
    assert.match(signup, /canPhone = !!s\.phone_signup/);
    assert.match(signup, /\{#if canPhone\}/);
    assert.ok(account.includes("canPhone = !!(await auth.state()).phone_login"));
});

test("where sign-up is shut, the screen does not say a code was sent", () => {
    assert.match(form, /If \{phone\.trim\(\)\} has an account here/);
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

test("a link fills the form and never sends a code by itself", () => {
    assert.match(login, /if \(linkedPhone && installed && canPhone\) usePhone = true/);
    // One read of the fragment each: a second would find it already cleared.
    assert.equal(login.split("takePhoneFromFragment()").length, 2);
    assert.equal(signup.split("takePhoneFromFragment()").length, 2);
    assert.match(login, /let identity = \$state\(readablePhone\(linkedPhone\)\)/);
    assert.match(signup, /let email = \$state\(readablePhone\(linkedPhone\)\)/);
    assert.match(signup, /if \(linkedPhone && canPhone\) usePhone = true/);
    assert.doesNotMatch(form, /\$effect\([^)]*\{[^}]*ask\(\)/);
    assert.match(form, /autofocus=\{!number\}/);
});
