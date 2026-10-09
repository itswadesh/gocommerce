/**
 * Putting a string on the clipboard, or saying honestly that it did not go.
 *
 * `navigator.clipboard` is refused in ordinary situations — an origin the
 * browser does not count as secure, a permission policy, a document without
 * focus — and the screens that copy something used to answer every one of
 * those with a toast that read as success. The old `execCommand` path still
 * works in most of them, so it is the second try rather than the giving up.
 *
 * Returns whether the text is on the clipboard. A caller that gets `false`
 * still has the string, and should put it where the operator can select it.
 */
export async function copyText(text) {
    try {
        await navigator.clipboard.writeText(text);
        return true;
    } catch {
        // Fall through to the selection route below.
    }
    try {
        const area = document.createElement("textarea");
        area.value = text;
        area.setAttribute("readonly", "");
        area.style.position = "fixed";
        area.style.opacity = "0";
        document.body.appendChild(area);
        area.select();
        const ok = document.execCommand("copy");
        area.remove();
        return ok;
    } catch {
        return false;
    }
}
