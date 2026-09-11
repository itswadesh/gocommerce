/**
 * A tiny toast queue. Messages announce themselves and then get out of the way.
 *
 * The variant names are PocketBase's — "success", "error", and the unnamed
 * default that toast.css styles as info — because the stylesheet is
 * PocketBase's and the class is what selects the colour and the icon glyph.
 *
 * "warning" is ours, and the one addition: toast.css defines only success and
 * error, so a partial outcome ("3 rows had problems") was being drawn as total
 * failure. Its rule lives in gocommerce.css, over tokens vars.css already
 * defines, because toast.css is frozen.
 */

let seq = 0;

const state = $state({ items: [] });

const timers = new Map();

function push(type, message, timeout) {
    const id = ++seq;
    state.items = [...state.items, { id, type, message, removing: false }];
    schedule(id, timeout ?? (type === "error" || type === "warning" ? 6000 : 3500));
    return id;
}

function schedule(id, delay) {
    if (delay === 0) return;
    clearTimeout(timers.get(id));
    timers.set(id, setTimeout(() => dismiss(id), delay));
}

/**
 * Reading a toast should not be a race. Hovering one cancels its removal, and
 * leaving starts the clock again.
 */
export function hold(id) {
    clearTimeout(timers.get(id));
    timers.delete(id);
}

export function release(id) {
    schedule(id, 1500);
}

export function dismiss(id) {
    clearTimeout(timers.get(id));
    timers.delete(id);

    // Mark it leaving first so the exit animation can run, then drop it. The
    // 300ms matches the grid-template-rows collapse in toast.css.
    state.items = state.items.map((t) => (t.id === id ? { ...t, removing: true } : t));
    setTimeout(() => {
        state.items = state.items.filter((t) => t.id !== id);
    }, 300);
}

export const toast = {
    get items() {
        return state.items;
    },

    success: (message, timeout) => push("success", message, timeout),
    warning: (message, timeout) => push("warning", message, timeout),
    info: (message, timeout) => push("", message, timeout),
    // An alias, because "danger" is what the class vocabulary calls this colour
    // everywhere else in the panel and a screen had already called it that.
    danger: (message, timeout) => push("error", message, timeout),

    remove: dismiss,
    hold,
    release,

    removeAll() {
        for (const t of state.items) dismiss(t.id);
    },

    /**
     * error takes whatever was thrown. An ApiError carries the engine's own
     * message, which is nearly always better than anything this layer could
     * invent — "only 2 left in stock" beats "something went wrong".
     *
     * A handled error is dropped. The flag is set in exactly one place, api.js's
     * refuse(), and means a 401 has already ended the session: the shell is
     * showing the login form with an explanation, and sixty-six screens each
     * re-toasting "authentication required" for their own in-flight call would
     * bury it.
     */
    error(err) {
        if (err?.handled) return 0;
        const message = typeof err === "string" ? err : err?.message || "Something went wrong";
        const details = err?.details;
        if (Array.isArray(details) && details.length) {
            const lines = details
                .map((d) => d.sku || d.variant_id || "")
                .filter(Boolean)
                .join(", ");
            return push("error", lines ? `${message} (${lines})` : message);
        }
        return push("error", message);
    },
};
