# Theme editor — Phase 3: theme settings and more sections

Phase 1 (D125) made the home page a document of sections; Phase 2 (D126) made
the editor save itself and show the real storefront beside it. The owner then
asked (2026-10-27) to "build the next phase". Decision D127. No approval gate:
built, verified, then shipped.

## What ships

**Theme settings.** The store's design tokens, all optional:

| Group | Setting | Values |
|---|---|---|
| Colours | background, text, buttons and links (`accent`), text on buttons (`accent_text`), quiet backgrounds (`muted`) | `#rrggbb` only |
| Typography | headings, body text | a key from a list of fifteen Google fonts |
| Shape | corner rounding | none, small, medium, large |

An unset setting keeps the storefront's own value; there is no default to
invent. They are `theme_settings` in the home page's document, cleaned by the
same registry code as section settings (`ThemeSettings` in
`ext/layouts/registry.go`; colours are a new field kind, `color`). Being in
the document, they autosave, undo, preview live, publish, roll back and reset
with the page, with no second draft to keep in step.

The panel opens them from a **Theme settings** row at the top of the section
list (and from the Themes screen, `?panel=theme`), in the same column a
section's settings use. A pair that reads poorly — text on the background,
text on buttons, text on the muted colour, below 4.5:1 — is warned about above
the form, never refused; an unset half is taken as the storefront default
theme's colour.

**Every page gets them.** `GET /x/layouts/theme` answers `{revision,
settings}` from the published revision (`Cache-Control: public, max-age=30`,
`Vary: X-Store`), or a preview key's draft (`no-store`); 404 while the plugin
is off or nothing is published. `GET /x/layouts/home` carries them too. The
storefront's root layout reads them (kept 30 s per host) and writes one style
block in the SSR head mapping each token onto the variables the storefront is
built on — `--background`, `--foreground`, `--primary`, `--primary-foreground`,
`--muted`, `--font-heading`, `--font-body`, `--radius`, with captions and
hairlines derived from text and background — plus the Google Fonts link for
the chosen fonts. The storefront checks every value again (`#rrggbb`, a key of
its own font list, one of four radii) before it reaches the style sheet. On a
preview the home page draws the draft's tokens itself and the published ones
are left out, so a cleared token shows what publishing will.

**More section types**, each with engine validation tests and storefront
component tests:

- **Slideshow** — up to eight slides (picture required, phone picture,
  heading, text, button). Autoplay is off unless switched on; then it pauses
  on hover and focus, offers a pause button, and never moves under
  `prefers-reduced-motion`. Slides not on show are inert.
- **Collection list** — collections picked in order (new `collections` field
  kind), resolved live to title, slug and picture; a deleted one is dropped.
- **Video** — a YouTube or Vimeo link, or an MP4 from Files (new `video` kind).
  The engine accepts a link only when it names a video id; the storefront
  builds the embed from the id alone (youtube-nocookie.com, Vimeo with
  `dnt=1`) and loads it only when play is pressed, behind the merchant's
  poster. A file plays with controls and never by itself.
- **Newsletter sign-up** — posts to the existing `/x/newsletter/subscribe`.
  `SectionType.Requires` names the `newsletter` plugin: the library offers the
  type only while it is on, and the public page leaves the section out while
  it is off; a draft holding one still saves.
- **Questions and answers** — up to thirty, drawn as native `<details>`.
- **Columns** — up to four, each picture, heading, text, link.

Plain text everywhere; every link and picture passes the safe-URL rule.

**Connector 0.8.0** carries `themeSettings` and each collection list's cards,
makes Files addresses absolute inside lists (a slideshow's slides) and for the
new `poster` and `video_file` settings, and adds `getThemeSettings()`. The
storefront reads both through `Reflect.get`, so it runs on 0.7.0 without them.

**A Phase 2 gap closed:** a picture chosen from Files showed broken in the
editor's frame, because the panel posted `/media/…` and the storefront looked
for it on its own host. The panel now posts Files addresses on its own origin.

## Not in this phase

Other page templates (product, collection), scheduled publishing, theme
families and a marketplace. Tokens live in the home document until a second
template exists; then they move to a theme document of their own.
