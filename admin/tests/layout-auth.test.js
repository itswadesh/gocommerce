/*
 * Signing in, resetting a password and accepting an invitation live under
 * /admin/auth, as the KitCommerce admin's sign-in does, and old addresses
 * redirect. These read source, as tests/roles.test.js does, because
 * node:test cannot import a .svelte file.
 */
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync, existsSync } from "node:fs";

const at = (p) => new URL(p, import.meta.url);
const read = (p) => readFileSync(at(p), "utf8");

test("auth screens live under /admin/auth", () => {
    assert.ok(existsSync(at("../src/routes/admin/auth/login/+page.svelte")));
    assert.ok(existsSync(at("../src/routes/admin/auth/reset-password/[[token]]/+page.svelte")));
    assert.ok(existsSync(at("../src/routes/admin/auth/accept-invite/[token]/+page.svelte")));
});

test("the layout treats /admin/auth as public and sends signed-out visitors there", () => {
    const layout = read("../src/routes/+layout.svelte");
    assert.match(layout, /PUBLIC_PREFIXES = \[[^\]]*"\/admin\/auth"/);
    assert.match(layout, /LOGIN = "\/admin\/auth\/login"/);
    assert.match(layout, /\$\{LOGIN\}\?next=/);
    assert.match(layout, /safeNext\(/);
});

// kitcommerce.store's "Live demo" button opens /#email=…&password=…, and the
// login form fills itself from that fragment. Redirecting to the login address
// must carry the fragment, or the demo opens on an empty form.
test("the redirect to sign-in keeps the address's fragment", () => {
    const layout = read("../src/routes/+layout.svelte");
    assert.match(layout, /\$\{LOGIN\}\?next=\$\{encodeURIComponent\(here\)\}\$\{page\.url\.hash\}/);
});

// A catch-all route rather than the root layout's load: an address with no
// route is a 404 to the router before any layout runs, so a redirect there
// arrives after "Not found" has already been logged.
test("old addresses redirect through dashPath from a catch-all route, keeping the query", () => {
    const load = read("../src/routes/[...legacy]/+page.js");
    assert.match(load, /dashPath\(/);
    assert.match(load, /url\.search/);
    assert.match(load, /error\(404/);
    // SvelteKit's dev server throws on event.url.hash inside load; the panel
    // never renders on a server, so the fragment is read from location.
    assert.doesNotMatch(load, /url\.hash/);
    assert.match(load, /location\.hash/);
    assert.doesNotMatch(read("../src/routes/+layout.js"), /dashPath/);
});

// Signing up and a forgotten password are addresses, so a phone's back
// button returns to sign-in rather than leaving the panel.
test("signing up and a forgotten password have addresses of their own", () => {
    assert.ok(existsSync(at("../src/routes/admin/auth/signup/+page.svelte")));
    assert.ok(existsSync(at("../src/routes/admin/auth/forgot-password/+page.svelte")));
    const login = read("../src/lib/components/Login.svelte");
    assert.doesNotMatch(login, /mode = "reset"/);
});

// Each is offered only when the install can do it: a reset only where an
// email can leave, sign-up only where the install takes strangers.
test("sign-in offers a reset and sign-up only when the install can do them", () => {
    const login = read("../src/lib/components/Login.svelte");
    assert.match(login, /canReset = !!s\.password_reset/);
    assert.match(login, /canSignUp = !!s\.signup/);
    const forgot = login.slice(login.indexOf("{#if installed && canReset}"), login.indexOf("{/if}", login.indexOf("{#if installed && canReset}")));
    assert.match(forgot, /href=\{forgotHref\}/);
    assert.match(login, /forgotHref = \$derived\(\s*`\$\{base\}\/admin\/auth\/forgot-password/);
    const signup = login.slice(login.indexOf("{#if installed && canSignUp}"), login.indexOf("{/if}", login.indexOf("{#if installed && canSignUp}")));
    assert.match(signup, /\/admin\/auth\/signup/);
});

// Step two of signing up is the store picker's create form, the one way a
// store is opened, so the sign-up page makes the account and goes there.
test("signing up makes the account, then opens the store picker's create form", () => {
    const signup = read("../src/routes/admin/auth/signup/+page.svelte");
    assert.match(signup, /auth\.signup\(/);
    assert.match(signup, /select-store\?signup/);
    assert.doesNotMatch(signup, /\/api\/stores\/create/);
    const picker = read("../src/routes/select-store/+page.svelte");
    assert.match(picker, /searchParams\.has\("signup"\)/);
    assert.match(picker, /Step 2 of 2/);
});

// Every email carries a code; whether a link comes with it is the engine's
// answer, never a guess.
test("the forgot-password screen says whether a link comes with the code from the engine's answer", () => {
    const forgot = read("../src/routes/admin/auth/forgot-password/+page.svelte");
    assert.match(forgot, /link: !!result\?\.link/);
    assert.match(forgot, /\{#if sent\.link\}, or open the link/);
    // And before it is asked, from the install's own answer.
    assert.match(forgot, /link = s\.reset_link !== false/);
    assert.match(forgot, /link \? "Email me a link and code" : "Email me a code"/);
});

// The code is spent with its address, and the address travels in history
// state: an email in a URL would reach access logs and Referer headers.
test("entering the code is always offered, and the address never rides in the URL", () => {
    const forgot = read("../src/routes/admin/auth/forgot-password/+page.svelte");
    assert.match(forgot, /goto\(`\$\{base\}\/admin\/auth\/reset-password`, \{ state: \{ email: address \} \}\)/);
    const sent = forgot.slice(forgot.indexOf("Check your inbox"), forgot.indexOf("{:else if sent || canReset === false}"));
    assert.match(sent, /onclick=\{\(\) => enterCode\(sent\.email\)\}[\s\S]*Enter the code/);
    assert.doesNotMatch(sent, /\{#if !sent\.link\}/);

    const reset = read("../src/routes/admin/auth/reset-password/[[token]]/+page.svelte");
    assert.match(reset, /let email = \$state\(page\.state\?\.email \?\? ""\)/);
    assert.doesNotMatch(reset, /searchParams|location\.hash|url\.hash/);
    assert.match(reset, /\{ email: email\.trim\(\), code, password \}/);
    assert.match(reset, /linked\s*\?\s*\{ token, password \}/);
    const code = reset.slice(reset.indexOf('id="reset_code"'), reset.indexOf("/>", reset.indexOf('id="reset_code"')));
    assert.match(code, /inputmode="numeric"/);
    assert.match(code, /autocomplete="one-time-code"/);
    // A saved copy of the old template mails the link's 43-character token with
    // no link; the field has to take it, and spend it as a token without an address.
    assert.doesNotMatch(code, /maxlength/);
    assert.match(reset, /const pastedToken = \$derived\(typed\.length > 7 && !\/\^\\d\+\$\/\.test\(typed\)\)/);
    assert.match(reset, /pastedToken\s*\?\s*\{ token: typed, password \}/);
    assert.match(reset, /required=\{!pastedToken\}/);
    // A refused code is said under the code field, with the way to a new one.
    assert.match(reset, /err\.code === "invalid_token"\) codeRefused = true/);
    assert.match(reset, /id="code_refusal"[\s\S]*ask for a new code/);
});

test("every screen before a store shares the signed-out layout", () => {
    for (const p of [
        "../src/lib/components/Login.svelte",
        "../src/routes/admin/auth/signup/+page.svelte",
        "../src/routes/admin/auth/forgot-password/+page.svelte",
        "../src/routes/admin/auth/reset-password/[[token]]/+page.svelte",
        "../src/routes/admin/auth/accept-invite/[token]/+page.svelte",
        "../src/routes/select-store/+page.svelte",
    ]) {
        assert.match(read(p), /<AuthShell>/, `${p} is not in AuthShell`);
    }
});

test("the signed-out card moves, and has a reduced-motion path", () => {
    const css = read("../src/lib/styles/gocommerce.css");
    const section = css.slice(css.indexOf("/* ------------------------------------------------------------ signed out */"));
    assert.match(section, /view-transition-name: auth-card/);
    assert.match(section, /::view-transition-new\(auth-card\)/);
    const reduced = section.slice(section.indexOf("@media (prefers-reduced-motion: reduce)"));
    assert.match(reduced, /\.auth-card-enter/);
    assert.match(reduced, /fadeIn/);
    // iOS zooms the page into a field whose text is under 16px.
    assert.match(section, /\.auth-shell input:not\(\[type="checkbox"\]\):not\(\[type="radio"\]\) \{\s*font-size: 16px;/);
});

// A card that arrives inside a view transition has that as its entrance; one
// of its own would replay the moment the transition's class came off the root.
test("the card rises in once, not again after a view transition", () => {
    const shell = read("../src/lib/components/AuthShell.svelte");
    assert.match(shell, /const enter = [^;]*!document\.documentElement\.classList\.contains\("auth-vt"\)/);
    assert.match(shell, /class:auth-card-enter=\{enter\}/);
    const css = read("../src/lib/styles/gocommerce.css");
    const start = css.indexOf(".auth-card {");
    assert.doesNotMatch(css.slice(start, css.indexOf("}", start)), /animation/);
    assert.match(css, /\.auth-card-enter \{\s*animation: authCardIn/);
});

// Signed in on the sign-in address, the shell still has to ask which stores
// the person has before it can send them anywhere; the page stays meanwhile.
test("signing in keeps the page, with a spinner, until the shell knows where to go", () => {
    const layout = read("../src/routes/+layout.svelte");
    const branch = layout.slice(layout.indexOf("{#if onLogin}"), layout.indexOf("{:else if isPublic}"));
    assert.match(branch, /\{:else if ready\}[\s\S]*<AuthShell>[\s\S]*loader/);
});

// Signing up belongs to no store, so it names none: the one this browser last
// worked in may since have been suspended.
test("signing up sends no store", () => {
    const api = read("../src/lib/api.js");
    assert.match(api, /"\/api\/admin\/signup", \{ email, password \}, \{ admin: false, store: false \}/);
    assert.match(api, /\.\.\.\(store \? storeHeaders\(\) : \{\}\)/);
});
