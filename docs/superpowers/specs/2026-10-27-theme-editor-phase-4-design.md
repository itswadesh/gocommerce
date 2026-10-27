# Theme editor — Phase 4: product and collection templates, scheduled publishing

Phases 1–3 (D125–D127) made the home page a document of sections, edited
beside the real storefront, with the store's design tokens. Phase 3 left
"other page templates (product, collection), scheduled publishing" for later;
the owner then said "build" (2026-10-27). Decision D129.

## What ships

**Three pages.** `ext/layouts` keeps a document per page: `home`, `product` and
`collection` (`layouts.Pages`). The product and collection documents are
templates: one per store, drawn for every product and every collection. Every
route, the draft and its versions, preview keys, publish, rollback, reset and
history work the same for all three; `GET /x/layouts/{page}` answers each.

**A fixed core.** A template holds its page's core exactly once:

| Page | Core section | What the storefront draws there |
|---|---|---|
| product | `main-product` (Product) | breadcrumb, gallery, title, price, options, buy box, specifications and description |
| collection | `main-collection` (Products) | the collection's title, description, count and product grid |

The core has no settings: what it shows is each product's or collection's own.
The engine refuses a template without it, or with two, and saves it shown
whatever the draft says. The shop moves it like any other section, and puts
any section above or below it.

**Section types by page.** `SectionType.Pages` names the pages a type may be
on; empty means all. Every home section type is allowed on every page. The
product page adds:

- **Reviews** (`product-reviews`): the storefront's reviews block, unchanged.
- **Related products** (`related-products`): other products from the same
  category, with an optional heading (empty says "Related products").

Complementary products are a product grid of products the shop picks.

**Starting points.** Before the first save a product page is `main-product`,
`product-reviews`, `related-products`, and a collection page is
`main-collection` alone: the storefront's built-in pages, so publishing a
starting page changes nothing a shopper sees.

**Theme settings stay with the home page.** A template's document holds no
`theme_settings`; they are dropped when it is saved. `/x/layouts/theme` still
reads the published home page.

**Scheduled publishing.** `POST /api/admin/x/layouts/{page}/schedule`
(`{version, publish_at}`, `layouts.write`) copies the saved draft at `version`
into `layout_schedules`; `DELETE` cancels it. One schedule per page; setting
another replaces it. The time must be in the future and at most a year ahead.
A ticker started from `OnStart` (every 30 seconds, plus once at boot) runs
`PublishDue` for each store through `ForEachStore`, as cart recovery's pass
does. Each due row is claimed `FOR UPDATE SKIP LOCKED`, published through the
same `publish` the button uses, and deleted in that transaction. The revision
is marked `scheduled`, and `published_by` is who scheduled it. A page's state
carries `scheduled` with `outdated` when the draft was saved after it was
scheduled. Manual publish, rollback and reset leave a schedule alone.

## Panel

Themes lists Homepage, Product page and Collection page, each with its status
and any pending schedule. All three open one editor
(`$lib/components/PageEditor.svelte`). The core is a dashed, locked row that
moves but has no hide, copy or remove. The library offers only the types the
page may hold. On a product or collection page, the preview frame opens
`/products/<slug>` or `/collections/<slug>` for a product or collection the
shop picks (`PreviewSubject.svelte`: the hundred newest active products, or
the first two hundred collections), remembered per store in this browser.
**Schedule** opens a date and time in the browser's zone. The status line
shows the pending schedule, with Change and Cancel.

## Connector 0.9.0

`layoutService.getProductLayout()`, `getCollectionLayout()` and
`getPageLayout(page)`. Each is null on 404 (plugin off, nothing published,
older engine), like `getHomeLayout()`.

## Storefront

`PageComposer` replaces the body of `HomeComposer`, which becomes a wrapper.
Product and collection routes load the template next to the page's data.
Through `parts`, they draw their core snippet, the reviews and related
products at the template's positions. With no template, the plugin off, an
older connector or a template without its core, the pages render as before.
On a template, the core carries the h1 and every section heading is an h2. A
template's preview keeps the published design tokens.

The product page rewrites its address to `?variant_id=…` as it opens. That
drops the preview key, so a preview's key and editor origin are kept in the
tab's `sessionStorage` (`rememberPreview`). The editor frame, or a preview
tab moving between products, keeps showing the draft.

## Not in this phase

A template per product or per collection, the category listing (`/<slug>`,
which is not a collection), theme families and a marketplace.
