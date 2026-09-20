# @misiki/gocommerce-connector

[Svelte Commerce](https://github.com/itswadesh/svelte-commerce) talking to a
GoCommerce engine.

Svelte Commerce picks its backend by which `@misiki/*-connector` is installed.
Until this package existed there was no way to point it at GoCommerce — its
`package.json` shipped the Litekart and Vendure connectors, and the storefront
half of "Go × Svelte" could not reach the Go half.

## Install

```bash
npm i @misiki/gocommerce-connector
```

Then tell the storefront where the engine is:

```bash
# .env
PUBLIC_GOCOMMERCE_API_URL=http://127.0.0.1:8080
```

Only one connector should be installed at a time — the backend is whichever one
`package.json` has, and two would fight over the same `kitcommerce.config`
alias.

## What works

Everything on the path from a shelf to a placed order:

| Service | Covers |
| --- | --- |
| `productService` | list, search, sort, paginate; by slug, by id, by SKU; variants |
| `categoryService` | the tree, a level at a time; a category page with its products |
| `collectionService` | curated lists, and a collection page |
| `cartService` | open, add, change quantity, remove, discount code, email |
| `checkoutService` | payment methods, shipping rates, placing the order |
| `orderService` | one order, by number and the access token checkout returned |

## What does not, and why

The engine is a commerce engine: catalog, carts, checkout, orders, inventory.
Svelte Commerce also expects blogs, reels, banners, CMS pages, wishlists,
reviews, chat, warranties and vendor commissions, because Litekart has them.
GoCommerce has none of those.

**They throw rather than return empty.** An empty list is a claim — "this shop
has no blog posts" — and a storefront renders that as a heading with nothing
under it while you go looking for the bug in your template. A refusal names the
service and the method, so the answer is in the stack trace:

```
UnsupportedByGoCommerce: BlogService.list() is not supported by GoCommerce.
The engine covers catalog, carts, checkout, orders and inventory; BlogService
is not part of it. Remove the page or component that calls this, or put the
feature behind your own API.
```

Two absences are worth calling out because they change what you can build:

- **No customer accounts.** GoCommerce checkout is guest checkout. An order is
  read back with the access token issued when it was placed — which is why
  `orderService` has `getOne` and no `list`. A connector that returned every
  order to whoever asked would be the worst bug in this package.
- **No search index.** `productService.list({ search })` is the engine's own
  filter, not Meilisearch. It is fine for a few thousand products and it is not
  a relevance ranking.

## Two things this package is careful about

**Money.** The engine speaks minor units and a currency code and never sends a
formatted string. Svelte Commerce's types say `price: number` and mean "19.99".
The conversion happens in `money.ts` and nowhere else, and it is per currency
rather than a constant 100 — the yen writes no decimals, the dinar writes three.
Dividing ¥2,500 by 100 would price the item at ¥25.

**Fields the engine does not have are not invented.** `popularity` is 0, not a
plausible number, so a storefront sorting by it gets a flat ordering rather than
a fictional one. `mrp` (the struck-through price) equals the price when there is
no compare-at, rather than 0 — which would render as a 100% discount on
everything in the shop.

## Tests

They run against a real engine, not a mock. A connector is a translation between
two systems, and mocking one of them tests the translation against your idea of
the engine rather than against the engine — which is the exact thing that is
wrong when a connector is wrong.

```bash
# with a store running (scripts/dev.ps1 -Seed, or docker compose up)
npm run build
PUBLIC_GOCOMMERCE_API_URL=http://127.0.0.1:8090 node --test test/live.test.mjs
```

They skip, loudly, when there is nothing to talk to.

## Status

Pre-1.0, like the engine. The storefront API it targets is documented at
`/docs` on any running GoCommerce, and that reference is the contract this
package translates.
