/**
 * Signing in with Google, from the browser's side (D237). The tab holds the
 * state, nonce and PKCE verifier for the trip to Google and back, and the engine
 * holds the client secret, so no Google script runs on the sign-in screen.
 *
 * Google sends the browser back to the sign-in address, which has to be listed
 * as a redirect URI on the OAuth client.
 */
const KEY = "gocommerce_google_attempt";
const AUTHORIZE = "https://accounts.google.com/o/oauth2/v2/auth";

function randomText() {
    return base64url(crypto.getRandomValues(new Uint8Array(32)));
}

function base64url(bytes) {
    return btoa(String.fromCharCode(...bytes))
        .replaceAll("+", "-")
        .replaceAll("/", "_")
        .replace(/=+$/, "");
}

/** googleAuthURL is the address that asks Google who this person is. */
export function googleAuthURL({ clientId, redirectUri, state, nonce, challenge }) {
    const q = new URLSearchParams({
        client_id: clientId,
        redirect_uri: redirectUri,
        response_type: "code",
        scope: "openid email",
        state,
        nonce,
        code_challenge: challenge,
        code_challenge_method: "S256",
        prompt: "select_account",
    });
    return `${AUTHORIZE}?${q}`;
}

/**
 * startGoogleSignIn leaves for Google. next is the screen to reach afterwards,
 * which the trip would otherwise lose: the redirect URI cannot carry it.
 */
export async function startGoogleSignIn(clientId, redirectUri, next = "") {
    const state = randomText();
    const nonce = randomText();
    const verifier = randomText();
    const digest = await crypto.subtle.digest("SHA-256", new TextEncoder().encode(verifier));
    sessionStorage.setItem(KEY, JSON.stringify({ state, nonce, verifier, redirectUri, next }));
    window.location.assign(
        googleAuthURL({ clientId, redirectUri, state, nonce, challenge: base64url(new Uint8Array(digest)) }),
    );
}

/**
 * readGoogleReturn says what an address Google sent back means, given what the
 * tab kept: nothing to do with Google (null), the person backing out, a state
 * that does not match this tab's attempt, or a sign-in to finish.
 */
export function readGoogleReturn(search, kept) {
    const q = new URLSearchParams(search);
    const code = q.get("code");
    const error = q.get("error");
    if (!code && !error) return null;
    const attempt = kept ? JSON.parse(kept) : null;
    if (!attempt || q.get("state") !== attempt.state) return { outcome: "stale" };
    if (error) return { outcome: "cancelled", next: attempt.next };
    return {
        outcome: "signin",
        next: attempt.next,
        signin: { code, code_verifier: attempt.verifier, nonce: attempt.nonce, redirect_uri: attempt.redirectUri },
    };
}

/** takeGoogleReturn reads this tab's return, once. */
export function takeGoogleReturn() {
    if (typeof window === "undefined") return null;
    const result = readGoogleReturn(window.location.search, sessionStorage.getItem(KEY));
    if (result) sessionStorage.removeItem(KEY);
    return result;
}
