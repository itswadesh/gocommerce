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
