/**
 * Reaching a clickable table row from the keyboard.
 *
 * Every list screen's primary action was a `<tr onclick>` with nothing else on
 * it: no tabindex, no key handler, no href. A mouse could open a product and
 * nothing else could — not Tab and Enter, not a screen reader, not a
 * middle-click into a second tab.
 *
 * What this does NOT do is give the row a `role`. `role="button"` or
 * `role="link"` on a `<tr>` takes it out of the table's own semantics, so a
 * screen reader stops announcing "row 4 of 25" and stops pairing cells with
 * their column headers — a real loss, traded for a label the row already has
 * from its first cell. The row stays a row, takes focus, and answers Enter and
 * Space; where the target is a real route the name cell carries a real `<a>`
 * as well, which is what makes middle-click and "open in new tab" work. The
 * focus ring for all of this is `tr.handle:focus-visible` in gocommerce.css,
 * which PocketBase's own `tr.handle { outline: 0 }` would otherwise suppress.
 */

/**
 * rowKey activates a focused row on Enter or Space.
 *
 *     <tr class="handle" tabindex="0"
 *         onclick={() => openEdit(row)}
 *         onkeydown={(e) => rowKey(e, () => openEdit(row))}>
 */
export function rowKey(event, activate) {
    if (event.key !== "Enter" && event.key !== " ") return;
    // A control inside the row owns its own keys: Space on the row's Delete
    // button belongs to that button, and Enter on the order-number link belongs
    // to the link. Only a press on the row itself is the row's.
    if (event.target !== event.currentTarget) return;
    // Space scrolls the page otherwise, and Enter on a row inside a form would
    // submit it.
    event.preventDefault();
    activate(event);
}
