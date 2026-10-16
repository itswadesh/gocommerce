# Video showcases

**Asked for:** the owner's "Video Showcase Action Plan" (2026-10-16). Build the
engine and panel half now; the Svelte Commerce page is a follow-up PR.
Purpose: buyers trust a shop more when they see its own people showing the
products for sale.

## What GoCommerce does and does not do

- It never hosts, uploads, transcodes or streams video. A showcase holds a
  link to a YouTube or Facebook video and turns it into that platform's own
  embed address on the server.
- It never puts merchant-supplied HTML on a page: the storefront gets a
  generated embed URL, not an iframe snippet.
- It never claims a showcase is live because the link is to a livestream.
  "Live" means the store pressed "We're live", and it lapses on its own.
- It shows no viewer counts and no platform badges.

## Module `ext/showcases` (`-showcases`, D88)

- **Rights:** `showcases.read` (manager, staff) and `showcases.write`
  (manager). The owner holds both.
- **Plugin switch:** "Video showcases", on by default. While it is off, the
  public routes answer 404 and the panel still works.
- **Tables** (all scoped to the store):
  - `showcases`;
  - `showcase_products` (ordered);
  - `showcase_stats` (per showcase per day);
  - `showcase_carts` (which showcase a cart came from);
  - `showcase_orders` (each order counted once).

### A showcase

- **Text:** a title, a slug (from the title) and a short description.
- **The video:**
  - The provider is `youtube` or `facebook`, and the address is checked on the
    server: http or https, an exact host list, and the platform's own path
    shapes. The player is always built on https.
  - What is stored is a reference: the YouTube id, or Facebook's canonical
    video URL. The embed, watch and thumbnail addresses are derived when read.
  - YouTube plays from `youtube-nocookie.com`, its privacy-enhanced mode.
  - A channel's or a short-share link is refused, with what to paste instead.
- **Kind:** `video` (recorded), or `live` (a livestream the store will run).
  - For `live`, the live state is `scheduled` (needs a start time), `live`
    (sets `live_since`) or `ended`.
  - The label a storefront shows is worked out from these:

    | Showcase | Label |
    |---|---|
    | a video, or a livestream the store has said nothing about | `video` |
    | scheduled, start time in the future | `scheduled` |
    | scheduled with the start time passed | `video` (nobody said it began) |
    | live, for up to 12 hours since the store said so | `live` |
    | live for longer, or ended | `replay` |

    Saying "live" again after the 12 hours lapsed starts them again, for the
    next broadcast.
- **Products:** either picked products (up to 50, in the store's order) or a
  collection, whose active products are read live.
  - "Create collection" turns the picked products into a new collection and
    attaches it. That route also needs `collections.write`, the right
    `/api/admin/collections` asks for.
- **Featured product:** must be one of the showcase's products. It can be
  changed during a broadcast without touching the video.
- **Where it shows:** the home page (off by default), the page of its
  collection, and the pages of its products (both on by default).
- **Status:** draft, published or archived. Publishing needs a video and at
  least one product or a collection.

### Admin routes (`/api/admin/x/showcases`)

| Route | What it does |
|---|---|
| `GET /` | the list, with each showcase's totals |
| `POST /` | create |
| `GET /{id}` | one showcase, with its products and stats |
| `PATCH /{id}` | any field, including status, live state and the featured product |
| `DELETE /{id}` | remove it |
| `POST /check` | `{url}`: the provider, the embed, watch and thumbnail addresses, and for YouTube the title and whether embedding is allowed. A network failure there is not a refusal. |
| `POST /{id}/collection` | `{title}`: create a collection from the picked products and attach it |

The YouTube check uses YouTube's oEmbed endpoint: 401 means the owner turned
off embedding, 404 means the video is private or deleted.

### Public routes (`/x/showcases`)

| Route | What it returns |
|---|---|
| `GET /x/showcases?on=home` (or `?product=ID`, `?collection=ID`) | the published showcases for that place |
| `GET /x/showcases/{slug}` | the showcase, the store's name, the video, the label, the featured product and the products |
| `GET /x/showcases/{slug}/now` | the label and the featured product, cacheable for 5 s, for polling during a broadcast |
| `POST /x/showcases/{slug}/events` | see below |

- **Products in a public response** are a slim summary: id, slug, title,
  image, the default variant's price, compare-at price, whether anything is
  available, the variant count, and the default variant's id. Only active
  products the request's channel carries are included, and they are
  translated into the request's language.
- No cost, stock count or metadata is sent.
- There is no customer-specific price: price lists apply in the cart, against
  a proven email (D66).
- **Events:** `{type: view | click | add_to_cart, product_id?, cart?}`.
  - Views and clicks are rate-limited per store, showcase and address; 429
    when over.
  - No personal data is stored.
  - `add_to_cart` with the cart's token records which showcase the cart came
    from, and counts once per basket. It is not rate-limited: behind a proxy
    every buyer shares one address, and a basket is something only the
    store's own cart makes. On `order.created`, an order from that cart counts
    once, with its total.
  - The storefront sends `add_to_cart` only after the cart has accepted the
    line.

### Core changes

- `App.TranslateProducts(r, products)` is exported, so a module's public
  route translates products the way core's do.
- The panel's content security policy gains
  `frame-src https://www.youtube-nocookie.com https://www.facebook.com`, in
  both the header and the meta policy, so the editor can preview the player.

### Panel

- **Content › Video showcases:** the list, with views, clicks, add-to-carts
  and orders.
- **The editor:**
  - The video link is checked with a live preview, and the title is filled in
    from YouTube.
  - Products: picked (ordered), an existing collection, or a new collection
    from the picks.
  - The featured product, where it shows, and the kind with its live
    controls.
  - Publish, unpublish and archive.
  - A desktop and phone preview of the video beside or above its products.

## Out of scope

- The Svelte Commerce showcase page and its placements (follow-up).
- Twitch, Instagram, TikTok and Douyin.
- Platform live status, chat and comments.
- Product recognition in the video.
- Uploads.
