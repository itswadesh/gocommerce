/**
 * The panel's keyboard layer.
 *
 * There was exactly one shortcut in the whole panel — Ctrl/Cmd+S, bound inside
 * SaveBar — so there was nothing to be consistent with and nothing to document.
 * The rules the shell now applies are here rather than inline in the layout,
 * because "is the operator typing?" is the question every single-key shortcut
 * has to get right and getting it wrong means a letter key eats a character out
 * of a form field.
 */

/**
 * True when the keystroke belongs to whatever has focus rather than to the app.
 *
 * `isContentEditable` is what covers the rich text editor, whose body is a
 * contenteditable div and not an input; without it, typing a `/` into a product
 * description would open the search palette instead.
 */
export function isTypingTarget(target) {
    if (!target) return false;
    const tag = target.tagName;
    return (
        tag === "INPUT" ||
        tag === "TEXTAREA" ||
        tag === "SELECT" ||
        target.isContentEditable === true
    );
}

/**
 * True when something modal already owns the keyboard.
 *
 * A drawer, a confirm, the palette itself, or any open popover — a `n` pressed
 * inside an open drawer must not also open a second one behind it. Drawer and
 * Confirm both carry `data-modal-state`, which is the one attribute every modal
 * in this panel agrees on.
 */
export function modalIsOpen() {
    return !!document.querySelector('[data-modal-state="open"], [popover]:popover-open');
}

/** A bare letter or symbol: no Ctrl, no Alt, no Meta. Shift is allowed — `?`. */
export function isBareKey(event) {
    return !event.ctrlKey && !event.metaKey && !event.altKey;
}

/**
 * The "create a new one" shortcut.
 *
 * The shell owns the keystroke and the screen owns what "new" means, so this is
 * a window event rather than a registry: a list screen that has a New button
 * subscribes, and one that has nothing to create simply does not — no table in
 * the layout to fall out of date, and no screen reaching up into the shell.
 *
 *     $effect(() => onNewShortcut(openCreate));
 */
export const NEW_EVENT = "gocommerce:new";

export function onNewShortcut(handler) {
    const listener = () => handler();
    window.addEventListener(NEW_EVENT, listener);
    return () => window.removeEventListener(NEW_EVENT, listener);
}

/** What the help sheet lists, and the only description of these that exists. */
export const SHORTCUTS = [
    { keys: ["Ctrl", "K"], mac: ["⌘", "K"], what: "Search the store" },
    { keys: ["/"], what: "Search the store, without a modifier" },
    { keys: ["n"], what: "New — on a list screen that can create one" },
    { keys: ["Ctrl", "S"], mac: ["⌘", "S"], what: "Save, on a screen with a save bar" },
    { keys: ["?"], what: "This sheet" },
    { keys: ["Esc"], what: "Close whatever is open" },
];
