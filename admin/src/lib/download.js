/**
 * Fetching a document that sits behind the admin token.
 *
 * A plain <a href> cannot carry a header, and every document the engine renders
 * for an operator — an export, an invoice — is behind `Authorization: Bearer`.
 * So the file is fetched with the token, turned into a blob, and handed to the
 * browser from there. The same reasoning data/+page.svelte works from; that
 * screen keeps its own copy until the toast fix landing on it merges, and then
 * the two fold together.
 */

import { request } from "$lib/api.js";
import { toast } from "$lib/toast.svelte.js";

/**
 * safeFilename turns a document's own name into one a filesystem will take.
 *
 * An invoice number is whatever the store's NumberFormat says it is, and
 * "INV/2026/00001" is a perfectly ordinary one — a slash in a download name is
 * not. Never build a filename from a server string without this.
 */
export function safeFilename(name, ext) {
    const cleaned = String(name || "document")
        .replace(/[^A-Za-z0-9._-]+/g, "-")
        .replace(/^-+|-+$/g, "");
    return (cleaned || "document") + (ext ? "." + ext : "");
}

/** save hands a blob URL to the browser as a download and then releases it. */
function save(url, filename) {
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.click();
}

/**
 * downloadFile fetches a document with the admin token and saves it.
 *
 * `raw` is what keeps the body intact: request() unwraps the engine's {data}
 * envelope for everything else, and an HTML document is not one.
 */
export async function downloadFile(path, filename, type = "text/html") {
    try {
        const body = await request("GET", path, { raw: true });
        const url = URL.createObjectURL(new Blob([body], { type }));
        save(url, filename);
        URL.revokeObjectURL(url);
    } catch (err) {
        toast.error(err);
    }
}

/**
 * openDocument opens a fetched document in a new tab, for printing.
 *
 * The window is opened synchronously inside the click and pointed at the blob
 * afterwards. That ordering is the whole trick: by the time the fetch resolves
 * the browser no longer counts the call as a user gesture and blocks the
 * popup, so a window opened after the await never appears.
 *
 * An in-panel iframe preview is not an option, and that is a CSP fact rather
 * than a preference: the panel is served with `frame-ancestors 'none'` and
 * neither that header nor the meta policy declares `frame-src`, so a blob:
 * frame falls back to `default-src 'self'` and is refused. A top-level blob:
 * window inherits the policy that admits the document's one inline <style>.
 */
export async function openDocument(path, filename) {
    const win = window.open("", "_blank");
    try {
        const html = await request("GET", path, { raw: true });
        const url = URL.createObjectURL(new Blob([html], { type: "text/html" }));
        if (win) {
            win.location = url;
        } else {
            // Popups blocked: the operator still asked for the document, so
            // give them the file rather than nothing.
            save(url, filename);
        }
        // Revoking now would race the navigation the line above just started.
        setTimeout(() => URL.revokeObjectURL(url), 60000);
    } catch (err) {
        win?.close();
        toast.error(err);
    }
}
