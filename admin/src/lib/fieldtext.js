/**
 * The text in a form field, whatever Svelte has turned the binding into.
 *
 * `bind:value` on `<input type="number">` does not hand back what was typed. It
 * coerces to a **number** as soon as the field is touched, and to **null** when
 * the box is emptied or holds something it cannot parse. So a field initialised
 * as `""` is a string until somebody uses it and a number or null afterwards.
 *
 * That matters because the obvious next line is `form.position.trim()`, which
 * throws `trim is not a function` on the number and `Cannot read properties of
 * null` on the empty box. Both throw from inside a submit handler, so the form
 * sends no request, closes nothing and shows nothing — to the operator the Save
 * button has simply stopped working, with the reason only in the console.
 *
 * Normalising at the point of reading is the fix that stays fixed. The
 * alternative is to remember, at every new number field, that the binding is
 * not a string; that was already missed on two screens.
 */
export function fieldText(value) {
    if (value === null || value === undefined) return "";
    return String(value).trim();
}
