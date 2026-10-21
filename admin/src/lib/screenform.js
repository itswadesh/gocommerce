/**
 * What a role may do on a module's descriptor screen (/dash/x/[slug]).
 *
 * The screen as a whole is gated on the right the descriptor names, which is
 * the right to read its list. Deleting can need more: a descriptor names it in
 * `form.delete_right` (core/screens.go, ScreenForm.DeleteRight), and the route
 * asks for both, so the button is offered only to a role carrying both.
 */

/** mayDelete is whether `can` lets this role delete a row on `screen`. */
export function mayDelete(screen, can) {
    const form = screen?.form;
    if (!form?.delete) return false;
    return can(screen.right) && (!form.delete_right || can(form.delete_right));
}

/**
 * listOf is a list as api.get hands it back, whichever shape that was: a
 * response carrying a meta block comes back as {data, meta}, and one without
 * as the bare array. The screens listing has no meta and a module's list may
 * or may not, so reading `.data` alone saw nothing in either case.
 */
export function listOf(result) {
    if (Array.isArray(result)) return result;
    return Array.isArray(result?.data) ? result.data : [];
}
