import { test } from "node:test";
import assert from "node:assert/strict";

import { googleAuthURL, readGoogleReturn } from "../src/lib/google-signin.js";

const kept = JSON.stringify({ state: "s1", nonce: "n1", verifier: "v1", redirectUri: "https://a.example/admin/auth/login", next: "/dash" });

test("the address to Google asks for a code with PKCE and only the email", () => {
    const u = new URL(googleAuthURL({ clientId: "c", redirectUri: "https://a.example/admin/auth/login", state: "s1", nonce: "n1", challenge: "ch" }));
    assert.equal(u.origin, "https://accounts.google.com");
    assert.equal(u.searchParams.get("response_type"), "code");
    assert.equal(u.searchParams.get("scope"), "openid email");
    assert.equal(u.searchParams.get("code_challenge"), "ch");
    assert.equal(u.searchParams.get("code_challenge_method"), "S256");
    assert.equal(u.searchParams.get("state"), "s1");
    assert.equal(u.searchParams.get("nonce"), "n1");
});

test("an address with nothing from Google is left alone", () => {
    assert.equal(readGoogleReturn("?next=%2Fdash", kept), null);
    assert.equal(readGoogleReturn("", null), null);
});

test("a return with this tab's state finishes the sign-in", () => {
    const r = readGoogleReturn("?code=abc&state=s1", kept);
    assert.equal(r.outcome, "signin");
    assert.equal(r.next, "/dash");
    assert.deepEqual(r.signin, { code: "abc", code_verifier: "v1", nonce: "n1", redirect_uri: "https://a.example/admin/auth/login" });
});

test("a code this tab did not ask for signs nobody in", () => {
    assert.equal(readGoogleReturn("?code=abc&state=other", kept).outcome, "stale");
    assert.equal(readGoogleReturn("?code=abc&state=s1", null).outcome, "stale");
    assert.equal(readGoogleReturn("?code=abc", kept).outcome, "stale");
});

test("backing out at Google is no error to show", () => {
    assert.equal(readGoogleReturn("?error=access_denied&state=s1", kept).outcome, "cancelled");
});
