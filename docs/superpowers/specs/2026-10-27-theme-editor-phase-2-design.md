# Theme editor — Phase 2: autosave, reset, and the visual editor

Phase 1 (D125, `2026-10-26-homepage-composer-design.md`) shipped the home page
as a document a shop arranges, with a list editor, a manual **Save draft**, and
a preview in a new tab. The owner then asked (2026-10-27):

- "any change in theme must auto save, with a button to reset"
- "can we introduce preview (visual editor) like shopify"
- "goahead building the next part of the theme implementation"

Decision D126. No approval gate: built, verified, then shipped.

## What ships

**Autosave.** Every edit saves the draft 800 ms after the last change, one
save at a time and always the newest; leaving the screen saves at once (a
SvelteKit navigation flushes; closing the tab sends the last save with
`keepalive`). The header says *Saving…*, *Saved*, or *Couldn't save* with a
retry; a failed save is kept and retried. **Save draft** and the save bar are
gone. Optimistic concurrency stays: each save names the version it read, and
a 409 `stale_draft` stops saving and offers to reload — the person sees that
somebody else's save won, rather than overwriting it.

**Reset.** `POST /api/admin/x/layouts/{page}/reset` (`layouts.write`) makes the
draft the published document again, or the starting page when nothing is
published; it names the version like a save, so it never discards another
tab's edit unseen, and leaves the storefront alone. It records who did it in
`updated_by` — the trail the module keeps; `admin_audit` has no writer for
modules (D26). The panel asks first, and Undo brings the changes back.

**Undo and redo** over whole documents for the session: buttons and
Ctrl/Cmd+Z, Shift+Z / Ctrl+Y (outside text fields, which keep their own). A
run of typing in one field within a second is one step.

**The visual editor** (wider than 760 px). Left: the sections — drag by a
handle (pointer events, measured on layout positions so animating rows do not
oscillate), arrow keys on the handle, up/down buttons, hide, and a click opens
the section's settings in the same column. Centre: the store's real storefront
in a frame at desktop or 390 px width. On phones the Phase 1 list stays, with
settings in a drawer and **Open preview** in a tab.

How the frame stays live:

1. The panel mints a preview key when the editor opens (and every 25 minutes;
   a key lasts 30). Before the first save the key opens the starting page, so
   opening the editor writes nothing.
2. The frame loads `<storefront>/?preview=<key>&editor=<panel origin>`. The
   storefront honours `editor` only for a preview the engine accepted, and
   then answers `Content-Security-Policy: frame-ancestors <panel origin>`.
3. Each edit is posted to the frame (`postMessage`, target origin the
   storefront's) as the draft document; the storefront draws it with the same
   trusted section components, links through `safeHref`, hidden sections
   dropped, unknown types skipped. Product grids keep the products the engine
   last resolved for that section id; after each save the panel says so and
   the storefront re-reads the draft through the key, resolving them again.
4. A click on a section in the frame posts its id back; the panel opens it.
   Selecting in the panel scrolls the frame to it and outlines it.

Both sides read a message only from the other's window and exact origin, and
only messages tagged as the other's. A storefront older than this (no answer
within four seconds) is reloaded after each save instead, and the panel says
so.

**The panel's frame policy.** The panel's CSP header named only two video
players in `frame-src`. It now adds, for the shell document, the storefront
addresses of the store it is opened for — every verified domain and
`<slug>.<base domain>` — and the origins the server's owner listed in
`GOCOMMERCE_CORS_ORIGINS` (the storefronts it already trusts to call the API).
The browser fetches the shell without `X-Store`, so the store is the one
`?store=` names, else the store cookie, else the host, else the install's only
store; when that guess is another store than the tab's, the editor sees the
frame refused and reloads itself once with `?store=`. The build-time meta
policy cannot know a store, so its `frame-src` is `https: http:` and the
header does the narrowing; both are enforced. The storefront address can be
overridden per store in the browser (a storefront the engine does not know
of); an address the policy refuses gets the tab instead.

## Not in this phase

More section types, design tokens and theme families, other pages, scheduled
publishing, nested layouts, merchant HTML (still never). Drag and drop inside
the preview itself.
