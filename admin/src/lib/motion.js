/**
 * Svelte transitions that honour `prefers-reduced-motion`.
 *
 * The CSS half of the panel's motion is switched off for that preference in
 * gocommerce.css; transitions Svelte runs from JavaScript never see a media
 * query, so they ask here. The rule is the panel's: the movement goes and the
 * feedback stays — an element added or removed still fades, it just does not
 * travel. `fly` and `flip` animate transform and opacity only, so neither
 * costs a layout per frame.
 */
import { fade, fly } from "svelte/transition";
import { cubicOut } from "svelte/easing";

function reduced() {
    return typeof matchMedia === "function" && matchMedia("(prefers-reduced-motion: reduce)").matches;
}

/** An element arriving or leaving: a short rise and a fade, or the fade alone. */
export function rise(node, { y = 6, duration = 180, delay = 0 } = {}) {
    if (reduced()) return fade(node, { duration: 150, delay });
    return fly(node, { y, duration, delay, easing: cubicOut });
}

/** How long `animate:flip` may take to move siblings into place: none at all
 *  for a reader who asked for no movement. */
export function flipDuration() {
    return reduced() ? 0 : 200;
}
