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

/**
 * findSaveTarget is what Ctrl/Cmd+S presses on a screen without a save bar.
 *
 * Forms here save in a dozen shapes — a drawer's footer button, a settings
 * section's own Save, a form submitted on Enter — and a registry of them would
 * be one more thing every new screen forgets. So the shell looks, in the place
 * the operator is working: the topmost open drawer or dialog, else the page.
 * The form holding focus wins if it can be submitted; otherwise the one
 * visible, enabled button that says Save. Two Save buttons and no focus to
 * choose between them is a guess, and a guess that saves the wrong thing is
 * worse than nothing, so that returns null.
 *
 * @returns {{ form?: HTMLFormElement, button?: HTMLElement } | null}
 */
export function findSaveTarget(doc = document) {
    const modals = [...doc.querySelectorAll('[data-modal-state="open"]')];
    const scope = modals[modals.length - 1] ?? doc.querySelector("main") ?? doc.body;
    const visible = (el) => !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
    const enabled = (el) => !el.disabled && el.getAttribute("aria-disabled") !== "true";

    const form = doc.activeElement?.closest?.("form");
    if (form && scope.contains(form)) {
        const submit = form.querySelector('button[type="submit"], button:not([type]), input[type="submit"]');
        if (submit && visible(submit) && enabled(submit)) return { form, button: submit };
    }

    const saves = [...scope.querySelectorAll("button, a.btn")].filter((el) => {
        if (!visible(el) || !enabled(el)) return false;
        if (el.hasAttribute("data-save")) return true;
        const label = (el.getAttribute("aria-label") || el.textContent || "").trim();
        return /^save\b/i.test(label);
    });
    if (saves.length === 1) return { button: saves[0] };
    // Several: the one in the same section as whatever has focus.
    const near = saves.filter((el) => doc.activeElement && el.closest("section, form, .card")?.contains(doc.activeElement));
    return near.length === 1 ? { button: near[0] } : null;
}

/** What the help sheet lists, and the only description of these that exists. */
export const SHORTCUTS = [
    { keys: ["Ctrl", "K"], mac: ["⌘", "K"], what: "Search the store" },
    { keys: ["/"], what: "Search the store, without a modifier" },
    { keys: ["n"], what: "New — on a list screen that can create one" },
    { keys: ["Ctrl", "S"], mac: ["⌘", "S"], what: "Save the form you are working in" },
    { keys: ["?"], what: "This sheet" },
    { keys: ["Esc"], what: "Close whatever is open" },
];
