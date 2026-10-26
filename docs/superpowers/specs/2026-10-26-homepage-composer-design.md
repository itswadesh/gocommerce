# Homepage composer — gap analysis and Phase 1 design

The owner asked for a no-code page composer: a merchant puts any registered
section at any position of the storefront's home page, repeats a type, reorders,
hides, edits, previews a draft and publishes — with no deployment. This is
Phase 1 of the "ShopNX Dynamic Theme Builder & Visual Page Composer" spec
(owner, 2026-10-26). Decision D125.

## What exists (audit, 2026-10-26)

| Area | Today | Reused for the composer |
|---|---|---|
| Engine content | `ext/banners` (home slides and grids as rows, D99), `ext/cms` (pages with draft/published status, one body), `ext/navigation`, `ext/faq`, `ext/showcases` (products resolved for the storefront) | `safeURL` rules, the showcase way of resolving products (active, on the request's channel, translated), store scoping (D83), module rights (D65), plugin switch (D57) |
| Draft and publish | Status columns only (`cms_pages`, `showcases`). No revisions, no optimistic concurrency, no cache layer | New: a draft with a version number, immutable published revisions |
| Themes | None in the engine. G28 (theme colours) and G37 (page builder in `ext/cms`) are proposals; PLAN §1 called "choose a theme, never touch code" a non-target | The owner's request supersedes that line (D125) |
| Storefront home | `src/lib/theme/default/DefaultHomepage.svelte` renders fixed sections from hardcoded content; a small `ThemeSections` registry renders `store.themeLayout`, which only Litekart ever supplied | A new section renderer beside it; the legacy page stays the fallback |
| Panel | Content → Banners (up/down ordering, drawers, `ImageField`, `TargetPicker`), `RichText`, `PluginFields` (schema-driven forms), `SaveBar`, `Confirm` | All of it. No storefront iframe: the panel's CSP forbids framing, so preview opens the storefront in a tab |

Gaps found on the way: the connector's banner support (0.5.0) sits on an
unpushed local branch, so live storefronts on 0.4.9 may not show banners.

## Phase 1 — what ships

**Engine — `ext/layouts`** (module `layouts`, plugin `layouts`, off by default:
the feature flag).

- A page document per store and page key (`home` only for now): an ordered
  list of sections, each `{id, type, enabled, settings}`. The order of the list
  is the order on the page; any type may appear any number of times, anywhere.
- Section types, each a schema the panel draws its form from and the engine
  validates against: `announcement-bar`, `hero`, `product-grid`,
  `image-with-text`, `rich-text`, `benefits`. Unknown types, bad links
  (`javascript:` and the like), oversized documents (over 50 sections or
  256 KB) and malformed settings are refused with 400 naming the section.
- One draft per page with a version number. Saving names the version it was
  read at; a stale save is 409, so nobody overwrites a newer edit unseen.
- Publishing copies the draft into an immutable revision and points the page
  at it in one transaction. Rolling back publishes an earlier revision's
  document as a new revision. Both are audited.
- Preview: a 30-minute token, stored hashed, that lets the storefront read the
  draft. Answered `Cache-Control: no-store` and `X-Robots-Tag: noindex`.
- `GET /x/layouts/home` answers the published document, with each product
  grid's products resolved live (active, on the request's channel, translated)
  — prices and stock never live in the document. 404 when the plugin is off or
  nothing is published: the storefront then renders its legacy home page.

**Storefront** — the home page asks for the layout; with one, it renders the
sections through a registry of trusted Svelte components, server-side;
without, the legacy page as today.

**Panel** — Content → Homepage: the section list with an "Add section" at every
boundary, move up/down, duplicate, hide, remove; a settings drawer generated
from the schema (with the existing image and product pickers); Save draft,
Preview, Publish, and the revision history with roll back.

## Not in Phase 1

Drag and drop, undo/redo, an in-panel preview frame, nested custom layouts,
video/gallery/testimonials/FAQ/newsletter sections, theme families and design
tokens, other pages, scheduled publishing. Rich text is plain paragraphs: a
sanitizer for merchant HTML is its own piece of work.
