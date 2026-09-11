/**
 * The modal focus trap, as a stack.
 *
 * A stack and not a single trap, because nesting is ordinary here: the orders
 * screen opens a Confirm from inside its Drawer, and so do the team screen and
 * the variant matrix. Drawer and Confirm both stay mounted and toggle
 * `data-modal-state`, so a naive `use:trapFocus={open}` on each leaves two live
 * traps fighting over Tab.
 *
 * Two facts about this panel shape the implementation:
 *
 *   Drawer, Confirm and Toasts all `use:portal`, so an open modal is a direct
 *   child of `<body>` and the only body child the pass actually inerts is
 *   app.html's `<div style="display: contents">` wrapper — the whole panel.
 *   That is what makes "inert every body child that does not contain this node"
 *   a safe pass. A future modal that is NOT portalled would have this action
 *   inert its own ancestor, which is why the containment test is there rather
 *   than assumed.
 *
 *   The pass skips the nodes named in NEVER_INERT below — the other modals,
 *   whose `inert` attribute Svelte owns, and the live region, which is the one
 *   thing that must go on speaking while a modal is open.
 *
 * What this does NOT do: inert the drawer behind an open confirm. Taking the
 * `inert` attribute away from Drawer/Confirm is a bigger change than this
 * warrants, so keyboard containment is carried by the Tab handler and the
 * accessibility tree by `aria-modal="true"` on the inner dialog.
 *
 * Escape stays in dismiss.js, which asks isTopModal() before honouring it.
 */

const stack = [];

/*
 * Body children the inert pass must leave alone.
 *
 * `[data-modal-state]` is Drawer and Confirm: Svelte owns `inert` on those
 * through `inert={!open}` and would re-assert its own value on the next flush,
 * so setting it here is a fight rather than a fix.
 *
 * The live region is the toasts container, which is portalled to <body> for
 * exactly this reason. Inerting it takes it out of the accessibility tree and
 * makes its dismiss button unclickable — precisely when a modal's save fails
 * and the toast is the only error surface the panel has.
 */
const NEVER_INERT = '[data-modal-state], [aria-live], [role="status"]';

const TABBABLE = 'a[href], button, input, select, textarea, [tabindex]:not([tabindex="-1"])';

function tabbable(node) {
    return [...node.querySelectorAll(TABBABLE)].filter(
        // offsetParent is null for anything display:none, which is how a closed
        // panel inside an open modal stays out of the cycle.
        (el) => !el.disabled && el.offsetParent !== null,
    );
}

function top() {
    return stack.length ? stack[stack.length - 1] : null;
}

function inertOthers(entry) {
    for (const child of document.body.children) {
        if (child === entry.node || child.contains(entry.node)) continue;
        if (child.matches(NEVER_INERT)) continue;
        // Already inert for its own reasons: leave it, and leave it that way.
        if (child.hasAttribute("inert")) continue;
        child.setAttribute("inert", "");
        entry.inerted.push(child);
    }
}

function restoreOthers(entry) {
    // Exactly the elements this pass set, so a node that was already inert for
    // its own reasons stays inert.
    for (const child of entry.inerted) child.removeAttribute("inert");
    entry.inerted = [];
}

function activate(entry) {
    inertOthers(entry);
    entry.node.addEventListener("keydown", entry.onKeydown);
}

function deactivate(entry) {
    entry.node.removeEventListener("keydown", entry.onKeydown);
    restoreOthers(entry);
}

function focusFirst(node) {
    // A frame later: Svelte may still be flipping `inert={!open}` on this very
    // element in the current tick, and focus() on an inert subtree does nothing
    // at all — silently, which is the worst way for it to fail.
    requestAnimationFrame(() => {
        if (!node.isConnected) return;
        const autofocus = node.querySelector("[autofocus]");
        const target = autofocus || tabbable(node)[0] || node;
        target.focus?.();
    });
}

/**
 * trapFocus keeps Tab inside the node while `enabled`, and puts focus back
 * where it came from when it is not.
 *
 *     <div class="modal" use:portal use:trapFocus={open}>
 *
 * Declare it AFTER `use:portal`: actions run in order, and the first inert pass
 * has to see the node already parented to `<body>`.
 */
export function trapFocus(node, enabled = true) {
    const entry = {
        node,
        inerted: [],
        returnTo: null,
        onKeydown(event) {
            if (event.key !== "Tab") return;
            const items = tabbable(node);
            if (!items.length) {
                // Nothing to move to, so Tab must not escape to the page behind.
                event.preventDefault();
                return;
            }
            const first = items[0];
            const last = items[items.length - 1];
            const active = document.activeElement;
            if (event.shiftKey && (active === first || !node.contains(active))) {
                event.preventDefault();
                last.focus();
            } else if (!event.shiftKey && (active === last || !node.contains(active))) {
                event.preventDefault();
                first.focus();
            }
        },
    };

    let on = false;

    function enable() {
        if (on) return;
        on = true;
        const previous = top();
        if (previous) deactivate(previous);
        stack.push(entry);
        entry.returnTo = document.activeElement;
        activate(entry);
        focusFirst(node);
    }

    function disable() {
        if (!on) return;
        on = false;
        const at = stack.indexOf(entry);
        if (at !== -1) stack.splice(at, 1);
        deactivate(entry);

        const returnTo = entry.returnTo;
        entry.returnTo = null;

        const next = top();
        if (next) activate(next);

        // Only if it is still on the page: a drawer whose trigger was a row
        // that has since been deleted has nowhere to give focus back to, and
        // focusing a detached node quietly moves focus to <body>.
        if (returnTo?.isConnected) returnTo.focus?.();
    }

    if (enabled) enable();

    return {
        update(next) {
            if (next) enable();
            else disable();
        },
        destroy() {
            disable();
        },
    };
}

/**
 * isTopModal answers whether this node owns the keyboard right now.
 *
 * True when the stack is empty, so a dismissable that is not a modal at all —
 * and a single modal, the common case — behaves exactly as it did before the
 * stack existed.
 */
export function isTopModal(node) {
    return stack.length === 0 || top()?.node === node;
}
